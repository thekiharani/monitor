package host

import (
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	gnet "github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Darwin uses Mach, sysctl, and IOKit through gopsutil; no Linux VM or root is required.
type darwinCollector struct {
	at        time.Time
	cpu       []cpu.TimesStat
	disks     map[string]disk.IOCountersStat
	network   map[string]byteCounters
	procAt    time.Time
	procTimes map[int32]float64
	processes Processes
}

func newHostCollector(_, _, _ string) hostCollector { return &darwinCollector{} }

func (c *darwinCollector) HostInfo() HostInfo {
	name, _ := os.Hostname()
	h := HostInfo{Hostname: name, OS: "macOS", Cores: runtime.NumCPU()}
	if info, err := host.Info(); err == nil {
		h.Kernel = info.KernelVersion
		h.OS += " " + info.PlatformVersion
	}
	if info, err := cpu.Info(); err == nil && len(info) > 0 {
		h.CPUModel = info[0].ModelName
	}
	return h
}

func (c *darwinCollector) Sample(now time.Time) System {
	elapsed := now.Sub(c.at).Seconds()
	if c.at.IsZero() {
		elapsed = 0
	}
	c.at = now
	s := System{CPU: CPU{Cores: []float64{}}, Filesystems: []Filesystem{}, Net: []NetIface{}}
	if times, err := cpu.Times(true); err == nil {
		s.CPU = darwinCPU(c.cpu, times)
		c.cpu = times
	}
	if avg, err := load.Avg(); err == nil {
		s.Load = [3]float64{avg.Load1, avg.Load5, avg.Load15}
	}
	if memory, err := mem.VirtualMemory(); err == nil {
		s.Mem = Memory{Total: memory.Total, Used: memory.Used, Cached: memory.Inactive}
	}
	if swap, err := mem.SwapMemory(); err == nil {
		s.Mem.SwapTotal, s.Mem.SwapUsed = swap.Total, swap.Used
	}
	if uptime, err := host.Uptime(); err == nil {
		s.Uptime = float64(uptime)
	}
	if counters, err := disk.IOCounters(); err == nil {
		s.Disk = darwinDiskIO(c.disks, counters, elapsed)
		c.disks = counters
	}
	s.Filesystems = darwinFilesystems()
	s.Net = c.readNetwork(elapsed)
	if now.Sub(c.procAt) >= 2*time.Second {
		c.readProcesses(now)
	}
	s.Processes = c.processes
	return s
}

func darwinCPU(previous, current []cpu.TimesStat) CPU {
	out := CPU{Cores: make([]float64, len(current))}
	var user, system, total float64
	for i, t := range current {
		if i >= len(previous) {
			continue
		}
		p := previous[i]
		u, s, idle := t.User+t.Nice-p.User-p.Nice, t.System-p.System, t.Idle-p.Idle
		if u < 0 || s < 0 || idle < 0 || u+s+idle <= 0 {
			continue
		}
		out.Cores[i] = 100 * (u + s) / (u + s + idle)
		user, system, total = user+u, system+s, total+u+s+idle
	}
	if total > 0 {
		out.User, out.System = 100*user/total, 100*system/total
		out.Total = out.User + out.System
	}
	return out
}

func darwinDiskIO(previous, current map[string]disk.IOCountersStat, elapsed float64) DiskIO {
	var out DiskIO
	var reads, writes, readMs, writeMs float64
	if elapsed <= 0 {
		return out
	}
	for name, t := range current {
		p, ok := previous[name]
		if !ok {
			continue
		}
		out.ReadBps += rate(t.ReadBytes, p.ReadBytes, elapsed)
		out.WriteBps += rate(t.WriteBytes, p.WriteBytes, elapsed)
		out.ReadIOPS += rate(t.ReadCount, p.ReadCount, elapsed)
		out.WriteIOPS += rate(t.WriteCount, p.WriteCount, elapsed)
		reads, writes = reads+delta(t.ReadCount, p.ReadCount), writes+delta(t.WriteCount, p.WriteCount)
		readMs, writeMs = readMs+delta(t.ReadTime, p.ReadTime), writeMs+delta(t.WriteTime, p.WriteTime)
	}
	if reads > 0 {
		out.ReadLatency = readMs / reads
	}
	if writes > 0 {
		out.WriteLatency = writeMs / writes
	}
	// IOKit exposes operation times, but not Linux's busy time or weighted queue depth.
	return out
}

func darwinMount(p disk.PartitionStat, hasDataVolume bool) (string, bool) {
	if p.Fstype != "apfs" && p.Fstype != "hfs" && p.Fstype != "exfat" && p.Fstype != "msdos" {
		return "", false
	}
	if p.Mountpoint == "/System/Volumes/Data" {
		return "/", true
	}
	if strings.HasPrefix(p.Mountpoint, "/System/") || (p.Mountpoint == "/" && hasDataVolume) {
		return "", false
	}
	return p.Mountpoint, true
}

func darwinFilesystems() []Filesystem {
	out := []Filesystem{}
	partitions, err := disk.Partitions(false)
	if err != nil {
		return out
	}
	hasDataVolume := slices.ContainsFunc(partitions, func(p disk.PartitionStat) bool { return p.Mountpoint == "/System/Volumes/Data" })
	for _, p := range partitions {
		mount, ok := darwinMount(p, hasDataVolume)
		if !ok {
			continue
		}
		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u.Total == 0 {
			continue
		}
		out = append(out, Filesystem{
			Mount: mount, Device: p.Device, Type: p.Fstype, Total: u.Total, Used: u.Total - u.Free,
			InodesTotal: u.InodesTotal, InodesUsed: u.InodesUsed,
			// macOS's sealed system volume is intentionally read-only, not a disk failure.
			ReadOnly: p.Mountpoint != "/" && slices.Contains(p.Opts, "ro"),
		})
	}
	return out
}

func (c *darwinCollector) readNetwork(elapsed float64) []NetIface {
	out := []NetIface{}
	counters, err := gnet.IOCounters(true)
	if err != nil {
		return out
	}
	interfaces, _ := gnet.Interfaces()
	up := map[string]bool{}
	for _, iface := range interfaces {
		up[iface.Name] = slices.Contains(iface.Flags, "up")
	}
	next := map[string]byteCounters{}
	for _, t := range counters {
		if t.Name == "lo0" || skipIface(t.Name) || !up[t.Name] {
			continue
		}
		iface := NetIface{Name: t.Name, RxTotal: t.BytesRecv, TxTotal: t.BytesSent}
		if p, ok := c.network[t.Name]; ok && elapsed > 0 {
			iface.RxBps, iface.TxBps = rate(t.BytesRecv, p.rx, elapsed), rate(t.BytesSent, p.tx, elapsed)
		}
		next[t.Name] = byteCounters{t.BytesRecv, t.BytesSent}
		out = append(out, iface)
	}
	c.network = next
	return out
}

func (c *darwinCollector) readProcesses(now time.Time) {
	pids, err := process.Pids()
	if err != nil {
		if c.processes.ByCPU == nil {
			c.processes = Processes{ByCPU: []Process{}, ByMemory: []Process{}}
		}
		return
	}
	elapsed := now.Sub(c.procAt).Seconds()
	next := map[int32]float64{}
	all := []Process{}
	for _, pid := range pids {
		p, err := process.NewProcess(pid)
		if err != nil {
			continue
		}
		memory, err := p.MemoryInfo()
		if err != nil || memory.RSS == 0 {
			continue
		}
		times, err := p.Times()
		if err != nil {
			continue
		}
		total := times.User + times.System
		next[pid] = total
		item := Process{PID: int(pid), RSS: memory.RSS}
		if before, ok := c.procTimes[pid]; ok && elapsed > 0 {
			item.CPU = 100 * max(0, total-before) / elapsed
		}
		all = append(all, item)
	}
	top := func(less func(a, b Process) int) []Process {
		items := slices.Clone(all)
		slices.SortFunc(items, less)
		items = items[:min(topN, len(items))]
		for i := range items {
			if p, err := process.NewProcess(int32(items[i].PID)); err == nil {
				items[i].Name, _ = p.Name()
			}
		}
		return items
	}
	c.processes = Processes{
		ByCPU:    top(func(a, b Process) int { return compareDesc(a.CPU, b.CPU) }),
		ByMemory: top(func(a, b Process) int { return compareDesc(float64(a.RSS), float64(b.RSS)) }),
	}
	c.procAt, c.procTimes = now, next
}
