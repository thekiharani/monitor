package monitor

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	sectorBytes = 512
	clockTicks  = 100
	topN        = 5
)

type HostInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`
	CPUModel string `json:"cpuModel"`
	Cores    int    `json:"cores"`
	Version  string `json:"version"`
}

type CPU struct {
	Total  float64   `json:"total"`
	User   float64   `json:"user"`
	System float64   `json:"system"`
	IOWait float64   `json:"iowait"`
	Steal  float64   `json:"steal"`
	Cores  []float64 `json:"cores"`
}

type Pressure struct {
	CPU    float64 `json:"cpu"`
	Memory float64 `json:"memory"`
	IO     float64 `json:"io"`
}

type Memory struct {
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	Cached    uint64 `json:"cached"`
	SwapTotal uint64 `json:"swapTotal"`
	SwapUsed  uint64 `json:"swapUsed"`
}

type DiskIO struct {
	ReadBps      float64 `json:"readBps"`
	WriteBps     float64 `json:"writeBps"`
	ReadIOPS     float64 `json:"readIops"`
	WriteIOPS    float64 `json:"writeIops"`
	ReadLatency  float64 `json:"readLatency"`
	WriteLatency float64 `json:"writeLatency"`
	Queue        float64 `json:"queue"`
	Busy         float64 `json:"busy"`
}

type Filesystem struct {
	Mount       string `json:"mount"`
	Device      string `json:"device"`
	Type        string `json:"type"`
	Total       uint64 `json:"total"`
	Used        uint64 `json:"used"`
	InodesTotal uint64 `json:"inodesTotal"`
	InodesUsed  uint64 `json:"inodesUsed"`
	ReadOnly    bool   `json:"readOnly"`
	Errors      int64  `json:"errors"`
	LastErrorAt int64  `json:"lastErrorAt"`
}

type NetIface struct {
	Name    string  `json:"name"`
	RxBps   float64 `json:"rxBps"`
	TxBps   float64 `json:"txBps"`
	RxTotal uint64  `json:"rxTotal"`
	TxTotal uint64  `json:"txTotal"`
}

type Process struct {
	PID  int     `json:"pid"`
	Name string  `json:"name"`
	CPU  float64 `json:"cpu"`
	RSS  uint64  `json:"rss"`
}

type Processes struct {
	ByCPU    []Process `json:"byCpu"`
	ByMemory []Process `json:"byMemory"`
}

type System struct {
	CPU         CPU          `json:"cpu"`
	Load        [3]float64   `json:"load"`
	Pressure    *Pressure    `json:"pressure"`
	Mem         Memory       `json:"mem"`
	Disk        DiskIO       `json:"disk"`
	Filesystems []Filesystem `json:"filesystems"`
	Net         []NetIface   `json:"net"`
	TempC       float64      `json:"tempC"`
	Uptime      float64      `json:"uptime"`
	Processes   Processes    `json:"processes"`
}

type cpuTimes struct{ user, system, idle, iowait, steal, total uint64 }

type diskCounters struct {
	readBytes, writeBytes, reads, writes, readMs, writeMs, busyMs, weightedMs uint64
}

type byteCounters struct{ rx, tx uint64 }

type SystemCollector struct {
	proc, sys, root string
	at              time.Time
	cpu             map[string]cpuTimes
	disk            map[string]diskCounters
	net             map[string]byteCounters
	procAt          time.Time
	procTicks       map[int]uint64
	processes       Processes
}

func NewSystemCollector(proc, sys, root string) *SystemCollector {
	return &SystemCollector{proc: proc, sys: sys, root: root}
}

func (c *SystemCollector) HostInfo() HostInfo {
	h := HostInfo{
		Hostname: firstLine(filepath.Join(c.root, "etc/hostname")),
		Kernel:   firstLine(filepath.Join(c.proc, "sys/kernel/osrelease")),
		OS:       osRelease(filepath.Join(c.root, "etc/os-release")),
	}
	eachLine(filepath.Join(c.proc, "cpuinfo"), func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		switch strings.TrimSpace(key) {
		case "model name":
			h.CPUModel = strings.TrimSpace(value)
		case "processor":
			h.Cores++
		}
	})
	return h
}

func (c *SystemCollector) Sample(now time.Time) System {
	elapsed := now.Sub(c.at).Seconds()
	first := c.at.IsZero()
	c.at = now

	s := System{
		CPU:         c.readCPU(),
		Load:        c.readLoad(),
		Pressure:    c.readPressure(),
		Mem:         c.readMemory(),
		Disk:        c.readDiskIO(elapsed, first),
		Filesystems: c.readFilesystems(),
		Net:         c.readNet(elapsed, first),
		TempC:       c.readTemp(),
	}
	if fields := strings.Fields(firstLine(filepath.Join(c.proc, "uptime"))); len(fields) > 0 {
		s.Uptime, _ = strconv.ParseFloat(fields[0], 64)
	}
	if now.Sub(c.procAt) >= 2*time.Second {
		c.processes = c.readProcesses(now)
	}
	s.Processes = c.processes
	if s.Processes.ByCPU == nil {
		s.Processes = Processes{ByCPU: []Process{}, ByMemory: []Process{}}
	}
	return s
}

func (c *SystemCollector) readCPU() CPU {
	next := map[string]cpuTimes{}
	out := CPU{Cores: []float64{}}
	eachLine(filepath.Join(c.proc, "stat"), func(line string) {
		if !strings.HasPrefix(line, "cpu") {
			return
		}
		fields := strings.Fields(line)
		v := make([]uint64, 8)
		for i := range v {
			if i+1 < len(fields) {
				v[i], _ = strconv.ParseUint(fields[i+1], 10, 64)
			}
		}
		t := cpuTimes{
			user:   v[0] + v[1],
			system: v[2] + v[5] + v[6],
			idle:   v[3],
			iowait: v[4],
			steal:  v[7],
		}
		t.total = t.user + t.system + t.idle + t.iowait + t.steal
		next[fields[0]] = t

		prev, ok := c.cpu[fields[0]]
		if !ok || t.total <= prev.total {
			if fields[0] != "cpu" {
				out.Cores = append(out.Cores, 0)
			}
			return
		}
		span := float64(t.total - prev.total)
		pct := func(now, before uint64) float64 { return 100 * float64(now-before) / span }
		busy := 100 - pct(t.idle+t.iowait, prev.idle+prev.iowait)
		if fields[0] != "cpu" {
			out.Cores = append(out.Cores, busy)
			return
		}
		out.Total = busy
		out.User = pct(t.user, prev.user)
		out.System = pct(t.system, prev.system)
		out.IOWait = pct(t.iowait, prev.iowait)
		out.Steal = pct(t.steal, prev.steal)
	})
	c.cpu = next
	return out
}

func (c *SystemCollector) readLoad() [3]float64 {
	var load [3]float64
	fields := strings.Fields(firstLine(filepath.Join(c.proc, "loadavg")))
	for i := range min(3, len(fields)) {
		load[i], _ = strconv.ParseFloat(fields[i], 64)
	}
	return load
}

func (c *SystemCollector) readPressure() *Pressure {
	some := func(resource string) (float64, bool) {
		line := firstLine(filepath.Join(c.proc, "pressure", resource))
		for field := range strings.FieldsSeq(line) {
			if value, ok := strings.CutPrefix(field, "avg10="); ok {
				v, err := strconv.ParseFloat(value, 64)
				return v, err == nil
			}
		}
		return 0, false
	}
	cpu, ok := some("cpu")
	if !ok {
		return nil
	}
	memory, _ := some("memory")
	io, _ := some("io")
	return &Pressure{CPU: cpu, Memory: memory, IO: io}
}

func (c *SystemCollector) readMemory() Memory {
	b := map[string]uint64{}
	eachLine(filepath.Join(c.proc, "meminfo"), func(line string) {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			return
		}
		n, _ := strconv.ParseUint(fields[0], 10, 64)
		b[key] = n * 1024
	})
	return Memory{
		Total:     b["MemTotal"],
		Used:      b["MemTotal"] - b["MemAvailable"],
		Cached:    b["Buffers"] + b["Cached"] + b["SReclaimable"],
		SwapTotal: b["SwapTotal"],
		SwapUsed:  b["SwapTotal"] - b["SwapFree"],
	}
}

func (c *SystemCollector) physicalDisks() map[string]bool {
	disks := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(c.sys, "block"))
	for _, e := range entries {
		name := e.Name()
		virtual := false
		for _, prefix := range []string{"loop", "ram", "zram", "sr", "dm-", "md", "nbd"} {
			virtual = virtual || strings.HasPrefix(name, prefix)
		}
		if !virtual {
			disks[name] = true
		}
	}
	return disks
}

func (c *SystemCollector) readDiskIO(elapsed float64, first bool) DiskIO {
	physical := c.physicalDisks()
	next := map[string]diskCounters{}
	var out DiskIO
	var reads, writes, readMs, writeMs float64
	eachLine(filepath.Join(c.proc, "diskstats"), func(line string) {
		f := strings.Fields(line)
		if len(f) < 14 || !physical[f[2]] {
			return
		}
		n := func(i int) uint64 { v, _ := strconv.ParseUint(f[i], 10, 64); return v }
		d := diskCounters{
			reads:      n(3),
			readBytes:  n(5) * sectorBytes,
			readMs:     n(6),
			writes:     n(7),
			writeBytes: n(9) * sectorBytes,
			writeMs:    n(10),
			busyMs:     n(12),
			weightedMs: n(13),
		}
		next[f[2]] = d
		prev, ok := c.disk[f[2]]
		if !ok || first || elapsed <= 0 {
			return
		}
		out.ReadBps += rate(d.readBytes, prev.readBytes, elapsed)
		out.WriteBps += rate(d.writeBytes, prev.writeBytes, elapsed)
		out.ReadIOPS += rate(d.reads, prev.reads, elapsed)
		out.WriteIOPS += rate(d.writes, prev.writes, elapsed)
		out.Busy = max(out.Busy, min(100, rate(d.busyMs, prev.busyMs, elapsed)/10))
		out.Queue += rate(d.weightedMs, prev.weightedMs, elapsed) / 1000
		reads += delta(d.reads, prev.reads)
		writes += delta(d.writes, prev.writes)
		readMs += delta(d.readMs, prev.readMs)
		writeMs += delta(d.writeMs, prev.writeMs)
	})
	if reads > 0 {
		out.ReadLatency = readMs / reads
	}
	if writes > 0 {
		out.WriteLatency = writeMs / writes
	}
	c.disk = next
	return out
}

var realFilesystems = map[string]bool{
	"ext2": true, "ext3": true, "ext4": true, "xfs": true, "btrfs": true,
	"zfs": true, "f2fs": true, "bcachefs": true, "jfs": true, "reiserfs": true,
}

func (c *SystemCollector) readFilesystems() []Filesystem {
	out := []Filesystem{}
	seen := map[string]bool{}
	eachLine(filepath.Join(c.proc, "1/mounts"), func(line string) {
		f := strings.Fields(line)
		if len(f) < 3 || !realFilesystems[f[2]] || seen[f[0]] {
			return
		}
		mount := unescapeMount(f[1])
		if strings.HasPrefix(mount, "/var/lib/docker") || strings.HasPrefix(mount, "/boot") {
			return
		}
		var st syscall.Statfs_t
		if syscall.Statfs(filepath.Join(c.root, mount), &st) != nil || st.Blocks == 0 {
			return
		}
		seen[f[0]] = true
		bsize := uint64(st.Bsize)
		fs := Filesystem{
			Mount:       mount,
			Device:      f[0],
			Type:        f[2],
			Total:       st.Blocks * bsize,
			Used:        (st.Blocks - st.Bfree) * bsize,
			InodesTotal: st.Files,
			InodesUsed:  st.Files - st.Ffree,
		}
		if len(f) > 3 {
			fs.ReadOnly = slices.Contains(strings.Split(f[3], ","), "ro")
		}
		if f[2] == "ext4" || f[2] == "ext3" || f[2] == "ext2" {
			dir := filepath.Join(c.sys, "fs/ext4", c.blockName(f[0]))
			fs.Errors = int64(parseUint(firstLine(filepath.Join(dir, "errors_count"))))
			fs.LastErrorAt = int64(parseUint(firstLine(filepath.Join(dir, "last_error_time"))))
		}
		out = append(out, fs)
	})
	return out
}

func (c *SystemCollector) blockName(device string) string {
	if target, err := os.Readlink(filepath.Join(c.root, device)); err == nil {
		return filepath.Base(target)
	}
	return filepath.Base(device)
}

func unescapeMount(s string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(s)
}

func (c *SystemCollector) readNet(elapsed float64, first bool) []NetIface {
	next := map[string]byteCounters{}
	out := []NetIface{}
	eachLine(filepath.Join(c.proc, "1/net/dev"), func(line string) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			return
		}
		name = strings.TrimSpace(name)
		if skipIface(name) || firstLine(filepath.Join(c.sys, "class/net", name, "operstate")) == "down" {
			return
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		next[name] = byteCounters{rx, tx}
		iface := NetIface{Name: name, RxTotal: rx, TxTotal: tx}
		if prev, ok := c.net[name]; ok && !first && elapsed > 0 {
			iface.RxBps = rate(rx, prev.rx, elapsed)
			iface.TxBps = rate(tx, prev.tx, elapsed)
		}
		out = append(out, iface)
	})
	c.net = next
	return out
}

func skipIface(name string) bool {
	for _, prefix := range []string{"veth", "br-", "docker", "virbr", "cni", "flannel", "cali"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return name == "lo"
}

func (c *SystemCollector) readTemp() float64 {
	dirs, _ := filepath.Glob(filepath.Join(c.sys, "class/hwmon/hwmon*"))
	var hottest float64
	for _, dir := range dirs {
		switch firstLine(filepath.Join(dir, "name")) {
		case "coretemp", "k10temp", "zenpower", "cpu_thermal":
		default:
			continue
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		for _, input := range inputs {
			milli, err := strconv.ParseFloat(firstLine(input), 64)
			if err == nil {
				hottest = max(hottest, milli/1000)
			}
		}
	}
	return hottest
}

func (c *SystemCollector) readProcesses(now time.Time) Processes {
	elapsed := now.Sub(c.procAt).Seconds()
	first := c.procAt.IsZero()
	c.procAt = now

	entries, _ := os.ReadDir(c.proc)
	ticks := make(map[int]uint64, len(entries))
	pageSize := uint64(os.Getpagesize())
	var all []Process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(c.proc, e.Name(), "stat"))
		if err != nil {
			continue
		}
		stat := string(b)
		_, rest, ok := strings.Cut(stat, "(")
		name, tail, ok2 := strings.CutLast(rest, ")")
		if !ok || !ok2 {
			continue
		}
		f := strings.Fields(tail)
		if len(f) < 22 {
			continue
		}
		utime, _ := strconv.ParseUint(f[11], 10, 64)
		stime, _ := strconv.ParseUint(f[12], 10, 64)
		rssPages, _ := strconv.ParseUint(f[21], 10, 64)
		if rssPages == 0 {
			continue
		}
		total := utime + stime
		ticks[pid] = total
		p := Process{PID: pid, Name: name, RSS: rssPages * pageSize}
		if prev, ok := c.procTicks[pid]; ok && !first && elapsed > 0 && total >= prev {
			p.CPU = 100 * float64(total-prev) / clockTicks / elapsed
		}
		all = append(all, p)
	}
	c.procTicks = ticks

	top := func(cmp func(a, b Process) int) []Process {
		sorted := slices.Clone(all)
		slices.SortFunc(sorted, cmp)
		sorted = sorted[:min(topN, len(sorted))]
		for i := range sorted {
			sorted[i].Name = c.commandName(sorted[i])
		}
		return sorted
	}
	if len(all) == 0 {
		return Processes{ByCPU: []Process{}, ByMemory: []Process{}}
	}
	return Processes{
		ByCPU:    top(func(a, b Process) int { return compareDesc(a.CPU, b.CPU) }),
		ByMemory: top(func(a, b Process) int { return compareDesc(float64(a.RSS), float64(b.RSS)) }),
	}
}

func (c *SystemCollector) commandName(p Process) string {
	b, err := os.ReadFile(filepath.Join(c.proc, strconv.Itoa(p.PID), "cmdline"))
	if err != nil || len(b) == 0 {
		return p.Name
	}
	args := strings.Fields(strings.ReplaceAll(string(b), "\x00", " "))
	if len(args) == 0 {
		return p.Name
	}
	args[0] = filepath.Base(args[0])
	name := strings.Join(args, " ")
	if len(name) > 72 {
		name = name[:72] + "…"
	}
	return name
}

func compareDesc(a, b float64) int {
	switch {
	case a > b:
		return -1
	case a < b:
		return 1
	}
	return 0
}

func delta(now, prev uint64) float64 {
	if now < prev {
		return 0
	}
	return float64(now - prev)
}

func rate(now, prev uint64, seconds float64) float64 {
	if now < prev || seconds <= 0 {
		return 0
	}
	return float64(now-prev) / seconds
}

func osRelease(path string) string {
	var name string
	eachLine(path, func(line string) {
		if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			name = strings.Trim(value, `"`)
		}
	})
	return name
}

func firstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

func eachLine(path string, fn func(string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fn(scanner.Text())
	}
}
