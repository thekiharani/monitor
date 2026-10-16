package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type DiskHealth struct {
	Device          string `json:"device"`
	Model           string `json:"model"`
	Passed          *bool  `json:"passed"`
	TempC           int    `json:"tempC"`
	PowerOnHours    int    `json:"powerOnHours"`
	LifeLeft        *int   `json:"lifeLeft"`
	WrittenBytes    uint64 `json:"writtenBytes"`
	Reallocated     int64  `json:"reallocated"`
	Pending         int64  `json:"pending"`
	Uncorrectable   int64  `json:"uncorrectable"`
	UnsafeShutdowns int64  `json:"unsafeShutdowns"`
}

type SmartReport struct {
	CheckedAt int64        `json:"checkedAt"`
	Disks     []DiskHealth `json:"disks"`
}

type Updates struct {
	Pending   int   `json:"pending"`
	Security  int   `json:"security"`
	CheckedAt int64 `json:"checkedAt"`
}

type HostHealth struct {
	RebootRequired bool         `json:"rebootRequired"`
	LastAutoUpdate int64        `json:"lastAutoUpdate"`
	Updates        *Updates     `json:"updates"`
	Smart          *SmartReport `json:"smart"`
}

type HealthCollector struct {
	root      string
	smartFile string
}

func (c *HealthCollector) Collect(context.Context) HostHealth {
	var h HostHealth
	for _, flag := range []string{"run/reboot-required", "var/run/reboot-required"} {
		if _, err := os.Stat(filepath.Join(c.root, flag)); err == nil {
			h.RebootRequired = true
		}
	}
	if st, err := os.Stat(filepath.Join(c.root, "var/lib/apt/periodic/unattended-upgrades-stamp")); err == nil {
		h.LastAutoUpdate = st.ModTime().Unix()
	}
	if b, err := os.ReadFile(c.smartFile); err == nil {
		var report SmartReport
		if json.Unmarshal(b, &report) == nil {
			h.Smart = &report
		}
	}
	return h
}

func (c *HealthCollector) CheckUpdates(context.Context) *Updates {
	installed := readInstalled(filepath.Join(c.root, "var/lib/dpkg/status"))
	if len(installed) == 0 {
		return nil
	}
	lists, _ := filepath.Glob(filepath.Join(c.root, "var/lib/apt/lists/*_Packages"))
	if len(lists) == 0 {
		return nil
	}
	newest := map[string]string{}
	newestSecurity := map[string]string{}
	for _, list := range lists {
		if notAutomatic(list) {
			continue
		}
		security := strings.Contains(filepath.Base(list), "security")
		eachStanza(list, func(f map[string]string) {
			pkg, ok := installed[f["Package"]]
			if !ok || (f["Architecture"] != pkg.arch && f["Architecture"] != "all") {
				return
			}
			if compareDebVersions(f["Version"], newest[f["Package"]]) > 0 {
				newest[f["Package"]] = f["Version"]
			}
			if security && compareDebVersions(f["Version"], newestSecurity[f["Package"]]) > 0 {
				newestSecurity[f["Package"]] = f["Version"]
			}
		})
	}
	u := &Updates{CheckedAt: time.Now().Unix()}
	for name, pkg := range installed {
		if v, ok := newest[name]; ok && compareDebVersions(v, pkg.version) > 0 {
			u.Pending++
		}
		if v, ok := newestSecurity[name]; ok && compareDebVersions(v, pkg.version) > 0 {
			u.Security++
		}
	}
	return u
}

type installedPackage struct{ version, arch string }

func readInstalled(status string) map[string]installedPackage {
	installed := map[string]installedPackage{}
	eachStanza(status, func(f map[string]string) {
		if strings.HasSuffix(f["Status"], " installed") && f["Version"] != "" {
			installed[f["Package"]] = installedPackage{version: f["Version"], arch: f["Architecture"]}
		}
	})
	return installed
}

func notAutomatic(list string) bool {
	host, rest, ok := strings.Cut(filepath.Base(list), "_dists_")
	if !ok {
		return false
	}
	suite, _, _ := strings.Cut(rest, "_")
	found := false
	for _, name := range []string{"InRelease", "Release"} {
		eachLine(filepath.Join(filepath.Dir(list), host+"_dists_"+suite+"_"+name), func(line string) {
			found = found || strings.TrimSpace(line) == "NotAutomatic: yes"
		})
	}
	return found
}

var stanzaFields = map[string]bool{"Package": true, "Version": true, "Architecture": true, "Status": true}

func eachStanza(path string, fn func(map[string]string)) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	fields := map[string]string{}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if len(fields) > 0 {
				fn(fields)
				fields = map[string]string{}
			}
			continue
		}
		if key, value, ok := strings.Cut(line, ": "); ok && stanzaFields[key] {
			fields[key] = value
		}
	}
	if len(fields) > 0 {
		fn(fields)
	}
}

func compareDebVersions(a, b string) int {
	if a == b {
		return 0
	}
	if b == "" {
		return 1
	}
	if a == "" {
		return -1
	}
	epochA, upstreamA, revisionA := splitDebVersion(a)
	epochB, upstreamB, revisionB := splitDebVersion(b)
	if epochA != epochB {
		return cmp.Compare(epochA, epochB)
	}
	if c := compareDebPart(upstreamA, upstreamB); c != 0 {
		return c
	}
	return compareDebPart(revisionA, revisionB)
}

func splitDebVersion(v string) (int, string, string) {
	epoch := 0
	if e, rest, ok := strings.Cut(v, ":"); ok {
		epoch, _ = strconv.Atoi(e)
		v = rest
	}
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

func debOrder(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	c := s[i]
	switch {
	case c >= '0' && c <= '9':
		return 0
	case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		return int(c)
	case c == '~':
		return -1
	}
	return int(c) + 256
}

func compareDebPart(a, b string) int {
	digit := func(s string, i int) bool { return i < len(s) && s[i] >= '0' && s[i] <= '9' }
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		for (i < len(a) && !digit(a, i)) || (j < len(b) && !digit(b, j)) {
			if oa, ob := debOrder(a, i), debOrder(b, j); oa != ob {
				return cmp.Compare(oa, ob)
			}
			i++
			j++
		}
		for digit(a, i) && a[i] == '0' {
			i++
		}
		for digit(b, j) && b[j] == '0' {
			j++
		}
		first := 0
		for digit(a, i) && digit(b, j) {
			if first == 0 {
				first = cmp.Compare(a[i], b[j])
			}
			i++
			j++
		}
		if digit(a, i) {
			return 1
		}
		if digit(b, j) {
			return -1
		}
		if first != 0 {
			return first
		}
	}
	return 0
}

func runSmart(ctx context.Context, out string, every time.Duration) error {
	for {
		report := SmartReport{CheckedAt: time.Now().Unix(), Disks: scanSmart(ctx)}
		if err := writeJSONAtomic(out, report); err != nil {
			slog.Error("smart report not written", "err", err)
		} else {
			slog.Info("smart report written", "disks", len(report.Disks))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
}

func scanSmart(ctx context.Context) []DiskHealth {
	var scan struct {
		Devices []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"devices"`
	}
	b, _ := exec.CommandContext(ctx, "smartctl", "--scan-open", "-j").Output()
	if json.Unmarshal(b, &scan) != nil {
		return nil
	}
	var disks []DiskHealth
	for _, dev := range scan.Devices {
		b, _ := exec.CommandContext(ctx, "smartctl", "-j", "-a", "-d", dev.Type, dev.Name).Output()
		if d, ok := parseSmart(dev.Name, b); ok {
			disks = append(disks, d)
		}
	}
	return disks
}

func parseSmart(device string, raw []byte) (DiskHealth, bool) {
	var r struct {
		ModelName   string `json:"model_name"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int `json:"hours"`
		} `json:"power_on_time"`
		ATA *struct {
			Table []struct {
				ID    int    `json:"id"`
				Name  string `json:"name"`
				Value int    `json:"value"`
				Raw   struct {
					Value int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		NVMe *struct {
			PercentageUsed   int    `json:"percentage_used"`
			DataUnitsWritten uint64 `json:"data_units_written"`
			UnsafeShutdowns  int64  `json:"unsafe_shutdowns"`
			MediaErrors      int64  `json:"media_errors"`
		} `json:"nvme_smart_health_information_log"`
	}
	if json.Unmarshal(raw, &r) != nil || r.ModelName == "" {
		return DiskHealth{}, false
	}
	d := DiskHealth{
		Device:       device,
		Model:        r.ModelName,
		TempC:        r.Temperature.Current,
		PowerOnHours: r.PowerOnTime.Hours,
	}
	if r.SmartStatus != nil {
		d.Passed = &r.SmartStatus.Passed
	}
	if r.NVMe != nil {
		life := max(0, 100-r.NVMe.PercentageUsed)
		d.LifeLeft = &life
		d.WrittenBytes = r.NVMe.DataUnitsWritten * 512000
		d.UnsafeShutdowns = r.NVMe.UnsafeShutdowns
		d.Uncorrectable = r.NVMe.MediaErrors
	}
	if r.ATA != nil {
		for _, a := range r.ATA.Table {
			name := strings.ToLower(a.Name)
			switch {
			case a.ID == 5:
				d.Reallocated = a.Raw.Value
			case a.ID == 197:
				d.Pending = a.Raw.Value
			case a.ID == 187 || a.ID == 198:
				d.Uncorrectable = max(d.Uncorrectable, a.Raw.Value)
			case a.ID == 192 || a.ID == 174:
				d.UnsafeShutdowns = max(d.UnsafeShutdowns, a.Raw.Value)
			case strings.Contains(name, "life_left"), strings.Contains(name, "lifetime_remain"),
				strings.Contains(name, "wear_leveling"), strings.Contains(name, "media_wearout"),
				strings.Contains(name, "remaining_lifetime"):
				life := a.Value
				d.LifeLeft = &life
			case strings.Contains(name, "lifetime_writes_gib"), strings.Contains(name, "host_writes_gib"):
				d.WrittenBytes = uint64(a.Raw.Value) << 30
			case strings.Contains(name, "total_lbas_written") && d.WrittenBytes == 0:
				d.WrittenBytes = uint64(a.Raw.Value) * sectorBytes
			}
		}
	}
	return d, true
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type Reason struct {
	Level string `json:"level"`
	Text  string `json:"text"`
	Ref   string `json:"ref"`
}

type Verdict struct {
	Level   string   `json:"level"`
	Reasons []Reason `json:"reasons"`
}

const (
	levelOK   = "ok"
	levelWarn = "warn"
	levelCrit = "crit"
)

var levelRank = map[string]int{levelOK: 0, levelWarn: 1, levelCrit: 2}

func (v Verdict) worst() (int, string) {
	rank, issue := levelRank[v.Level], ""
	for _, r := range v.Reasons {
		if levelRank[r.Level] == rank && issue == "" {
			issue = r.Text
		}
	}
	return rank, issue
}

type Averages struct {
	CPU, IOWait, Steal        float64
	ReadOps, WriteOps         float64
	ReadLatency, WriteLatency float64
	Queue                     float64
}

func assess(s Snapshot, avg Averages) Verdict {
	v := Verdict{Level: levelOK, Reasons: []Reason{}}
	add := func(level, ref, format string, args ...any) {
		v.Reasons = append(v.Reasons, Reason{Level: level, Text: fmt.Sprintf(format, args...), Ref: ref})
		if level == levelCrit || (level == levelWarn && v.Level == levelOK) {
			v.Level = level
		}
	}
	pct := func(used, total uint64) float64 {
		if total == 0 {
			return 0
		}
		return 100 * float64(used) / float64(total)
	}
	whole := func(f float64) int { return int(math.Round(f)) }

	switch {
	case avg.CPU >= 95:
		add(levelCrit, "cpu", "CPU is maxed out (%d%% over the last minute)", whole(avg.CPU))
	case avg.CPU >= 85:
		add(levelWarn, "cpu", "CPU is busy (%d%% over the last minute)", whole(avg.CPU))
	}
	if avg.IOWait >= 20 {
		add(levelWarn, "cpu", "CPU is waiting on the disk %d%% of the time", whole(avg.IOWait))
	}
	if avg.Steal >= 10 {
		add(levelWarn, "cpu", "The host is taking %d%% of CPU time from this server", whole(avg.Steal))
	}

	switch mem := pct(s.Mem.Used, s.Mem.Total); {
	case mem >= 95:
		add(levelCrit, "memory", "Memory is almost full (%d%%)", whole(mem))
	case mem >= 85:
		add(levelWarn, "memory", "Memory is running low (%d%% used)", whole(mem))
	}
	if swap := pct(s.Mem.SwapUsed, s.Mem.SwapTotal); swap >= 50 {
		add(levelWarn, "memory", "Swap is %d%% used", whole(swap))
	}

	for _, d := range []struct {
		kind    string
		ops, ms float64
	}{{"reads", avg.ReadOps, avg.ReadLatency}, {"writes", avg.WriteOps, avg.WriteLatency}} {
		if d.ops < 0.5 {
			continue
		}
		switch {
		case d.ms >= 500:
			add(levelCrit, "disk", "Disk %s are very slow (%d ms on average)", d.kind, whole(d.ms))
		case d.ms >= 100:
			add(levelWarn, "disk", "Disk %s are slow (%d ms on average)", d.kind, whole(d.ms))
		}
	}
	if avg.Queue >= 16 {
		add(levelWarn, "disk", "Disk requests are piling up (%d waiting on average)", whole(avg.Queue))
	}

	for _, fs := range s.Filesystems {
		if fs.ReadOnly {
			add(levelCrit, "storage", "Disk %s is read-only, so nothing can be saved to it", fs.Mount)
		}
		if fs.Errors > 0 {
			since := time.Since(time.Unix(fs.LastErrorAt, 0))
			level := levelWarn
			if fs.LastErrorAt > 0 && since < 24*time.Hour {
				level = levelCrit
			}
			last := ""
			if fs.LastErrorAt > 0 {
				last = fmt.Sprintf(", last one %s ago", humanDuration(since.Seconds()))
			}
			add(level, "storage", "Disk %s has recorded %d filesystem %s%s", fs.Mount, fs.Errors, plural(int(fs.Errors), "error", "errors"), last)
		}
		switch used := pct(fs.Used, fs.Total); {
		case used >= 95:
			add(levelCrit, "storage", "Disk %s is almost full (%d%%)", fs.Mount, whole(used))
		case used >= 85:
			add(levelWarn, "storage", "Disk %s is %d%% full", fs.Mount, whole(used))
		}
		if inodes := pct(fs.InodesUsed, fs.InodesTotal); inodes >= 85 {
			add(levelWarn, "storage", "Disk %s is running out of file slots (%d%% of inodes used)", fs.Mount, whole(inodes))
		}
	}

	if p := s.Pressure; p != nil {
		switch {
		case p.IO >= 60:
			add(levelCrit, "pressure", "Tasks are stuck waiting on the disk (%d%% of the time)", whole(p.IO))
		case p.IO >= 30:
			add(levelWarn, "pressure", "Tasks are often waiting on the disk (%d%% of the time)", whole(p.IO))
		}
		if p.Memory >= 10 {
			add(levelWarn, "pressure", "Tasks are waiting on memory (%d%% of the time)", whole(p.Memory))
		}
		if p.CPU >= 50 {
			add(levelWarn, "pressure", "Tasks are queueing for CPU (%d%% of the time)", whole(p.CPU))
		}
	}

	switch {
	case s.TempC >= 90:
		add(levelCrit, "cpu", "CPU is overheating (%d°C)", whole(s.TempC))
	case s.TempC >= 80:
		add(levelWarn, "cpu", "CPU is running hot (%d°C)", whole(s.TempC))
	}

	for _, c := range s.Containers {
		switch {
		case c.Health == "unhealthy":
			add(levelCrit, "container:"+c.Name, "Container %s is unhealthy", c.Name)
		case c.State == "restarting":
			add(levelCrit, "container:"+c.Name, "Container %s keeps restarting", c.Name)
		case c.State == "exited" && c.ExitCode != 0:
			add(levelWarn, "container:"+c.Name, "Container %s stopped with an error (exit %d)", c.Name, c.ExitCode)
		}
	}

	nearLimit := func(used, limit int64) bool { return limit > 0 && pct(uint64(used), uint64(limit)) >= 80 }
	for _, pg := range s.Postgres {
		switch {
		case !pg.Up:
			add(levelCrit, "postgres:"+pg.Name, "Postgres %s is %s", pg.Name, pg.Error)
		case nearLimit(int64(pg.Connections), int64(pg.MaxConnections)):
			add(levelWarn, "postgres:"+pg.Name, "Postgres %s is using %d of %d connections", pg.Name, pg.Connections, pg.MaxConnections)
		}
		if pg.Up && pg.LongestQuery >= 300 {
			add(levelWarn, "postgres:"+pg.Name, "A Postgres %s query has been running for %s", pg.Name, humanDuration(pg.LongestQuery))
		}
	}
	for _, my := range s.MySQL {
		switch {
		case !my.Up:
			add(levelCrit, "mysql:"+my.Name, "%s %s is %s", my.Flavor, my.Name, my.Error)
		case nearLimit(int64(my.Connections), int64(my.MaxConnections)):
			add(levelWarn, "mysql:"+my.Name, "%s %s is using %d of %d connections", my.Flavor, my.Name, my.Connections, my.MaxConnections)
		}
		if my.Up && my.LongestQuery >= 300 {
			add(levelWarn, "mysql:"+my.Name, "A %s %s query has been running for %s", my.Flavor, my.Name, humanDuration(my.LongestQuery))
		}
		if r := my.Replica; my.Up && r != nil {
			switch {
			case !r.IORunning || !r.SQLRunning:
				add(levelCrit, "mysql:"+my.Name, "%s %s has stopped replicating%s", my.Flavor, my.Name, suffix(": ", r.Error))
			case r.Lag != nil && *r.Lag >= 300:
				add(levelWarn, "mysql:"+my.Name, "%s %s is %s behind its source", my.Flavor, my.Name, humanDuration(*r.Lag))
			}
		}
	}
	for _, m := range s.Mongo {
		switch {
		case !m.Up:
			add(levelCrit, "mongo:"+m.Name, "MongoDB %s is %s", m.Name, m.Error)
		case nearLimit(m.Connections, m.MaxConnections):
			add(levelWarn, "mongo:"+m.Name, "MongoDB %s is using %d of %d connections", m.Name, m.Connections, m.MaxConnections)
		}
		if m.UnhealthyMembers > 0 {
			add(levelWarn, "mongo:"+m.Name, "MongoDB %s has %d unreachable replica set %s", m.Name, m.UnhealthyMembers, plural(m.UnhealthyMembers, "member", "members"))
		}
		if m.Lag != nil && *m.Lag >= 300 {
			add(levelWarn, "mongo:"+m.Name, "MongoDB %s is %s behind its primary", m.Name, humanDuration(*m.Lag))
		}
	}
	for _, r := range s.Redis {
		switch {
		case !r.Up:
			add(levelCrit, "redis:"+r.Name, "Redis %s is %s", r.Name, r.Error)
		case r.MaxMemory > 0 && pct(r.Memory, r.MaxMemory) >= 90:
			add(levelWarn, "redis:"+r.Name, "Redis %s memory is %d%% full", r.Name, whole(pct(r.Memory, r.MaxMemory)))
		}
	}
	for _, m := range s.Memcached {
		switch {
		case !m.Up:
			add(levelCrit, "memcached:"+m.Name, "Memcached %s is %s", m.Name, m.Error)
		case nearLimit(m.Connections, m.MaxConnections):
			add(levelWarn, "memcached:"+m.Name, "Memcached %s is using %d of %d connections", m.Name, m.Connections, m.MaxConnections)
		}
	}
	for _, n := range s.Nginx {
		switch {
		case !n.Up:
			add(levelCrit, "nginx:"+n.Name, "Nginx %s is %s", n.Name, n.Error)
		case n.Dropped > 0:
			add(levelWarn, "nginx:"+n.Name, "Nginx %s has dropped %d %s since it started; raise worker_connections", n.Name, n.Dropped, plural(int(n.Dropped), "connection", "connections"))
		}
	}
	for _, r := range s.RabbitMQ {
		if !r.Up {
			add(levelCrit, "rabbitmq:"+r.Name, "RabbitMQ %s is %s", r.Name, r.Error)
			continue
		}
		for _, node := range r.Nodes {
			switch {
			case !node.Running:
				add(levelCrit, "rabbitmq:"+r.Name, "RabbitMQ node %s is down", node.Name)
			case node.MemAlarm:
				add(levelCrit, "rabbitmq:"+r.Name, "RabbitMQ node %s hit its memory limit, so publishers are blocked", node.Name)
			case node.DiskAlarm:
				add(levelCrit, "rabbitmq:"+r.Name, "RabbitMQ node %s is low on disk, so publishers are blocked", node.Name)
			}
		}
	}
	for _, c := range s.Checks {
		if !c.Up {
			add(levelCrit, "check:"+c.Name, "%s is %s", c.Name, c.Error)
		}
		if c.CertExpires > 0 {
			switch left := time.Until(time.Unix(c.CertExpires, 0)); {
			case left <= 7*24*time.Hour:
				add(levelCrit, "check:"+c.Name, "The certificate for %s expires in %s", c.Name, humanDuration(left.Seconds()))
			case left <= 21*24*time.Hour:
				add(levelWarn, "check:"+c.Name, "The certificate for %s expires in %s", c.Name, humanDuration(left.Seconds()))
			}
		}
	}
	if sd := s.Systemd; sd != nil {
		if !sd.Up {
			add(levelWarn, "systemd:host", "Systemd is %s", sd.Error)
		}
		failed := map[string]bool{}
		for _, u := range sd.Failed {
			failed[u.Name] = true
			add(levelCrit, "systemd:host", "%s has failed", unitLabel(u.Name))
		}
		for _, u := range sd.Watched {
			if u.Active != "active" && !failed[u.Name] {
				add(levelCrit, "systemd:host", "%s is not running (%s)", unitLabel(u.Name), u.Active)
			}
		}
	}

	h := s.Health
	if h.Smart != nil {
		for _, d := range h.Smart.Disks {
			if d.Passed != nil && !*d.Passed {
				add(levelCrit, "upkeep", "Drive %s is failing its health check", d.Model)
			}
			if d.LifeLeft != nil {
				switch {
				case *d.LifeLeft <= 10:
					add(levelCrit, "upkeep", "Drive %s is nearly worn out (%d%% life left)", d.Model, *d.LifeLeft)
				case *d.LifeLeft <= 20:
					add(levelWarn, "upkeep", "Drive %s is wearing out (%d%% life left)", d.Model, *d.LifeLeft)
				}
			}
			if bad := d.Reallocated + d.Pending + d.Uncorrectable; bad > 0 {
				add(levelWarn, "upkeep", "Drive %s has %d bad or failing sectors", d.Model, bad)
			}
			if d.TempC >= 60 {
				add(levelWarn, "upkeep", "Drive %s is running hot (%d°C)", d.Model, d.TempC)
			}
		}
	}
	if h.RebootRequired {
		add(levelWarn, "upkeep", "A reboot is needed to finish installing updates")
	}
	if h.Updates != nil && h.Updates.Security > 0 {
		add(levelWarn, "upkeep", "%d security %s waiting", h.Updates.Security, plural(h.Updates.Security, "update", "updates"))
	}
	if h.LastAutoUpdate > 0 && time.Since(time.Unix(h.LastAutoUpdate, 0)) > 72*time.Hour {
		add(levelWarn, "upkeep", "Automatic updates haven't run for %s", humanDuration(time.Since(time.Unix(h.LastAutoUpdate, 0)).Seconds()))
	}
	return v
}

func humanDuration(seconds float64) string {
	d := time.Duration(seconds) * time.Second
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}

func unitLabel(name string) string {
	if service, ok := strings.CutSuffix(name, ".service"); ok {
		return "Service " + service
	}
	return "Unit " + name
}

func suffix(sep, text string) string {
	if text == "" {
		return ""
	}
	return sep + text
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
