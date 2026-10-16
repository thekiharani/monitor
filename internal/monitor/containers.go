package monitor

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Container struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Image    string  `json:"image"`
	State    string  `json:"state"`
	Status   string  `json:"status"`
	Health   string  `json:"health"`
	ExitCode int     `json:"exitCode"`
	CPU      float64 `json:"cpu"`
	Memory   uint64  `json:"memory"`
	MemLimit uint64  `json:"memLimit"`
	ReadBps  float64 `json:"readBps"`
	WriteBps float64 `json:"writeBps"`
	RxBps    float64 `json:"rxBps"`
	TxBps    float64 `json:"txBps"`
	Pids     uint64  `json:"pids"`
	Project  string  `json:"project,omitempty"`
	Service  string  `json:"service,omitempty"`
	Created  int64   `json:"created"`
}

type ContainerCollector struct {
	client    *http.Client
	baseURL   string
	sys, proc string
	at        time.Time
	usage     map[string]cgroupCounters
	netns     map[string]netnsInfo
}

type cgroupCounters struct{ cpuUsec, readBytes, writeBytes, rxBytes, txBytes uint64 }

type netnsInfo struct {
	pid     int
	private bool
}

var containerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,254}$`)

var errContainerLogsOff = errors.New("container logs are turned off")

func NewContainerCollector(dockerHost, sys, proc string) *ContainerCollector {
	if dockerHost == "" {
		return nil
	}
	return &ContainerCollector{
		client:  &http.Client{Timeout: 5 * time.Second},
		baseURL: strings.TrimRight(strings.Replace(dockerHost, "tcp://", "http://", 1), "/"),
		sys:     sys,
		proc:    proc,
		netns:   map[string]netnsInfo{},
	}
}

var exitCodePattern = regexp.MustCompile(`^Exited \((-?\d+)\)`)

func (c *ContainerCollector) fetch(ctx context.Context, path string) (*http.Response, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusNotFound:
			return nil, errors.New("no such container")
		case http.StatusForbidden:
			return nil, errContainerLogsOff
		}
		return nil, fmt.Errorf("docker proxy: %s", resp.Status)
	}
	return resp, nil
}

func (c *ContainerCollector) getJSON(ctx context.Context, path string, v any) error {
	resp, err := c.fetch(ctx, path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.UnmarshalRead(resp.Body, v, lenient)
}

func (c *ContainerCollector) Collect(ctx context.Context) ([]Container, error) {
	var listed []struct {
		ID      string            `json:"Id"`
		Names   []string          `json:"Names"`
		Image   string            `json:"Image"`
		State   string            `json:"State"`
		Status  string            `json:"Status"`
		Created int64             `json:"Created"`
		Labels  map[string]string `json:"Labels"`
	}
	if err := c.getJSON(ctx, "/containers/json?all=1", &listed); err != nil {
		return nil, err
	}

	now := time.Now()
	elapsed := now.Sub(c.at).Seconds()
	first := c.at.IsZero()
	c.at = now
	next := map[string]cgroupCounters{}
	netns := map[string]netnsInfo{}

	out := make([]Container, 0, len(listed))
	for _, l := range listed {
		ct := Container{
			ID:      l.ID[:min(12, len(l.ID))],
			Image:   l.Image,
			State:   l.State,
			Status:  l.Status,
			Created: l.Created,
			Project: l.Labels["com.docker.compose.project"],
			Service: l.Labels["com.docker.compose.service"],
		}
		if len(l.Names) > 0 {
			ct.Name = strings.TrimPrefix(l.Names[0], "/")
		}
		switch {
		case strings.Contains(l.Status, "(unhealthy)"):
			ct.Health = "unhealthy"
		case strings.Contains(l.Status, "(healthy)"):
			ct.Health = "healthy"
		case strings.Contains(l.Status, "(health: starting)"):
			ct.Health = "starting"
		}
		if m := exitCodePattern.FindStringSubmatch(l.Status); m != nil {
			ct.ExitCode, _ = strconv.Atoi(m[1])
		}

		if dir := c.cgroupDir(l.ID); dir != "" {
			counters := readCgroup(dir)
			if l.State == "running" {
				ns, ok := c.netns[l.ID]
				if !ok {
					ns = c.lookupNetns(ctx, l.ID)
				}
				netns[l.ID] = ns
				if ns.private {
					counters.rxBytes, counters.txBytes = readNetDev(filepath.Join(c.proc, strconv.Itoa(ns.pid), "net/dev"))
				}
			}
			next[l.ID] = counters
			ct.Memory = parseUint(firstLine(filepath.Join(dir, "memory.current")))
			ct.MemLimit = parseUint(firstLine(filepath.Join(dir, "memory.max")))
			ct.Pids = parseUint(firstLine(filepath.Join(dir, "pids.current")))
			if prev, ok := c.usage[l.ID]; ok && !first && elapsed > 0 {
				ct.CPU = rate(counters.cpuUsec, prev.cpuUsec, elapsed) / 1e4
				ct.ReadBps = rate(counters.readBytes, prev.readBytes, elapsed)
				ct.WriteBps = rate(counters.writeBytes, prev.writeBytes, elapsed)
				ct.RxBps = rate(counters.rxBytes, prev.rxBytes, elapsed)
				ct.TxBps = rate(counters.txBytes, prev.txBytes, elapsed)
			}
		}
		out = append(out, ct)
	}
	c.usage = next
	c.netns = netns
	return out, nil
}

func (c *ContainerCollector) lookupNetns(ctx context.Context, id string) netnsInfo {
	var inspect struct {
		State      struct{ Pid int }
		HostConfig struct{ NetworkMode string }
	}
	if c.getJSON(ctx, "/containers/"+id+"/json", &inspect) != nil || inspect.State.Pid == 0 {
		return netnsInfo{}
	}
	mode := inspect.HostConfig.NetworkMode
	return netnsInfo{pid: inspect.State.Pid, private: mode != "host" && mode != "none"}
}

func readNetDev(path string) (rx, tx uint64) {
	eachLine(path, func(line string) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			return
		}
		if f := strings.Fields(rest); len(f) >= 9 {
			rx += parseUint(f[0])
			tx += parseUint(f[8])
		}
	})
	return rx, tx
}

func (c *ContainerCollector) cgroupDir(id string) string {
	for _, dir := range []string{
		filepath.Join(c.sys, "fs/cgroup/system.slice", "docker-"+id+".scope"),
		filepath.Join(c.sys, "fs/cgroup/docker", id),
	} {
		if _, err := os.Stat(filepath.Join(dir, "cpu.stat")); err == nil {
			return dir
		}
	}
	return ""
}

func readCgroup(dir string) cgroupCounters {
	var c cgroupCounters
	eachLine(filepath.Join(dir, "cpu.stat"), func(line string) {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			c.cpuUsec = parseUint(v)
		}
	})
	eachLine(filepath.Join(dir, "io.stat"), func(line string) {
		for field := range strings.FieldsSeq(line) {
			if v, ok := strings.CutPrefix(field, "rbytes="); ok {
				c.readBytes += parseUint(v)
			} else if v, ok := strings.CutPrefix(field, "wbytes="); ok {
				c.writeBytes += parseUint(v)
			}
		}
	})
	return c
}

func parseUint(s string) uint64 {
	v, _ := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return v
}

type ContainerDetail struct {
	Container
	FullID         string             `json:"fullId"`
	ImageID        string             `json:"imageId"`
	CreatedAt      string             `json:"createdAt"`
	StartedAt      string             `json:"startedAt"`
	FinishedAt     string             `json:"finishedAt"`
	RestartCount   int                `json:"restartCount"`
	OOMKilled      bool               `json:"oomKilled"`
	OOMKills       uint64             `json:"oomKills"`
	Error          string             `json:"error,omitempty"`
	Command        string             `json:"command"`
	User           string             `json:"user"`
	WorkingDir     string             `json:"workingDir"`
	Platform       string             `json:"platform"`
	RestartPolicy  string             `json:"restartPolicy"`
	TTY            bool               `json:"tty"`
	Security       ContainerSecurity  `json:"security"`
	Limits         ContainerLimits    `json:"limits"`
	MemoryParts    *MemoryParts       `json:"memoryParts"`
	Health         *ContainerHealth   `json:"healthCheck"`
	Networks       []ContainerNetwork `json:"networks"`
	Ports          []ContainerPort    `json:"ports"`
	Mounts         []ContainerMount   `json:"mounts"`
	Env            []string           `json:"env"`
	Labels         map[string]string  `json:"labels"`
	Processes      []ContainerProcess `json:"processes"`
	ProcessesError string             `json:"processesError,omitempty"`
}

type ContainerSecurity struct {
	Privileged   bool     `json:"privileged"`
	ReadOnlyRoot bool     `json:"readOnlyRoot"`
	CapAdd       []string `json:"capAdd"`
	CapDrop      []string `json:"capDrop"`
	SecurityOpt  []string `json:"securityOpt"`
	NetworkMode  string   `json:"networkMode"`
}

type ContainerLimits struct {
	CPUs              float64 `json:"cpus"`
	Memory            int64   `json:"memory"`
	MemoryReservation int64   `json:"memoryReservation"`
	Pids              int64   `json:"pids"`
}

type MemoryParts struct {
	Anon   uint64 `json:"anon"`
	File   uint64 `json:"file"`
	Kernel uint64 `json:"kernel"`
}

type ContainerHealth struct {
	Status        string        `json:"status"`
	FailingStreak int           `json:"failingStreak"`
	Test          string        `json:"test"`
	Interval      float64       `json:"interval"`
	Timeout       float64       `json:"timeout"`
	Retries       int           `json:"retries"`
	Log           []HealthProbe `json:"log"`
}

type HealthProbe struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	ExitCode int    `json:"exitCode"`
	Output   string `json:"output"`
}

type ContainerNetwork struct {
	Name    string   `json:"name"`
	IP      string   `json:"ip"`
	Gateway string   `json:"gateway"`
	MAC     string   `json:"mac"`
	Aliases []string `json:"aliases"`
}

type ContainerPort struct {
	Container string `json:"container"`
	Host      string `json:"host"`
}

type ContainerMount struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readOnly"`
}

type ContainerProcess struct {
	PID     string   `json:"pid"`
	User    string   `json:"user"`
	CPU     *float64 `json:"cpu"`
	RSS     uint64   `json:"rss"`
	Started string   `json:"started"`
	Time    string   `json:"time"`
	Command string   `json:"command"`
}

type dockerInspect struct {
	ID           string `json:"Id"`
	Created      string
	Path         string
	Args         []string
	Platform     string
	RestartCount int
	Image        string
	State        struct {
		OOMKilled  bool
		ExitCode   int
		Error      string
		StartedAt  string
		FinishedAt string
		Health     *struct {
			Status        string
			FailingStreak int
			Log           []struct {
				Start, End string
				ExitCode   int
				Output     string
			}
		}
	}
	Config struct {
		User        string
		WorkingDir  string
		Env         []string
		Labels      map[string]string
		Tty         bool
		Healthcheck *struct {
			Test              []string
			Interval, Timeout int64
			Retries           int
		}
	}
	HostConfig struct {
		NetworkMode       string
		Privileged        bool
		ReadonlyRootfs    bool
		CapAdd, CapDrop   []string
		SecurityOpt       []string
		NanoCpus          int64
		CpuQuota          int64
		CpuPeriod         int64
		Memory            int64
		MemoryReservation int64
		PidsLimit         *int64
		RestartPolicy     struct {
			Name              string
			MaximumRetryCount int
		}
	}
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string
		}
		Networks map[string]struct {
			IPAddress  string
			Gateway    string
			MacAddress string
			Aliases    []string
		}
	}
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

func (c *ContainerCollector) Detail(ctx context.Context, live Container) (ContainerDetail, error) {
	var in dockerInspect
	if err := c.getJSON(ctx, "/containers/"+live.Name+"/json", &in); err != nil {
		return ContainerDetail{}, err
	}
	d := ContainerDetail{
		Container:     live,
		FullID:        in.ID,
		ImageID:       in.Image,
		CreatedAt:     in.Created,
		StartedAt:     in.State.StartedAt,
		FinishedAt:    in.State.FinishedAt,
		RestartCount:  in.RestartCount,
		OOMKilled:     in.State.OOMKilled,
		Error:         in.State.Error,
		Command:       strings.Join(append([]string{in.Path}, in.Args...), " "),
		User:          in.Config.User,
		WorkingDir:    in.Config.WorkingDir,
		Platform:      in.Platform,
		RestartPolicy: in.HostConfig.RestartPolicy.Name,
		TTY:           in.Config.Tty,
		Env:           in.Config.Env,
		Labels:        in.Config.Labels,
		Security: ContainerSecurity{
			Privileged:   in.HostConfig.Privileged,
			ReadOnlyRoot: in.HostConfig.ReadonlyRootfs,
			CapAdd:       in.HostConfig.CapAdd,
			CapDrop:      in.HostConfig.CapDrop,
			SecurityOpt:  in.HostConfig.SecurityOpt,
			NetworkMode:  in.HostConfig.NetworkMode,
		},
		Limits: ContainerLimits{
			Memory:            in.HostConfig.Memory,
			MemoryReservation: in.HostConfig.MemoryReservation,
		},
	}
	if n := in.HostConfig.RestartPolicy.MaximumRetryCount; n > 0 {
		d.RestartPolicy += fmt.Sprintf(" (%d retries)", n)
	}
	switch {
	case in.HostConfig.NanoCpus > 0:
		d.Limits.CPUs = float64(in.HostConfig.NanoCpus) / 1e9
	case in.HostConfig.CpuQuota > 0 && in.HostConfig.CpuPeriod > 0:
		d.Limits.CPUs = float64(in.HostConfig.CpuQuota) / float64(in.HostConfig.CpuPeriod)
	}
	if p := in.HostConfig.PidsLimit; p != nil && *p > 0 {
		d.Limits.Pids = *p
	}
	if hc := in.Config.Healthcheck; hc != nil && len(hc.Test) > 0 && hc.Test[0] != "NONE" {
		d.Health = &ContainerHealth{
			Test:     strings.Join(hc.Test[1:], " "),
			Interval: cmpDuration(hc.Interval, 30),
			Timeout:  cmpDuration(hc.Timeout, 30),
			Retries:  hc.Retries,
			Log:      []HealthProbe{},
		}
		if h := in.State.Health; h != nil {
			d.Health.Status, d.Health.FailingStreak = h.Status, h.FailingStreak
			for _, probe := range h.Log {
				d.Health.Log = append(d.Health.Log, HealthProbe{Start: probe.Start, End: probe.End, ExitCode: probe.ExitCode, Output: strings.TrimSpace(probe.Output)})
			}
		}
	}
	for name, n := range in.NetworkSettings.Networks {
		d.Networks = append(d.Networks, ContainerNetwork{Name: name, IP: n.IPAddress, Gateway: n.Gateway, MAC: n.MacAddress, Aliases: n.Aliases})
	}
	slices.SortFunc(d.Networks, func(a, b ContainerNetwork) int { return cmp.Compare(a.Name, b.Name) })
	for port, bindings := range in.NetworkSettings.Ports {
		if len(bindings) == 0 {
			d.Ports = append(d.Ports, ContainerPort{Container: port})
		}
		for _, b := range bindings {
			d.Ports = append(d.Ports, ContainerPort{Container: port, Host: net.JoinHostPort(cmpString(b.HostIP, "0.0.0.0"), b.HostPort)})
		}
	}
	slices.SortFunc(d.Ports, func(a, b ContainerPort) int {
		return cmp.Or(cmp.Compare(a.Container, b.Container), cmp.Compare(a.Host, b.Host))
	})
	for _, m := range in.Mounts {
		d.Mounts = append(d.Mounts, ContainerMount{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, ReadOnly: !m.RW})
	}
	if dir := c.cgroupDir(in.ID); dir != "" {
		stat := map[string]uint64{}
		eachLine(filepath.Join(dir, "memory.stat"), func(line string) {
			if k, v, ok := strings.Cut(line, " "); ok {
				stat[k] = parseUint(v)
			}
		})
		if len(stat) > 0 {
			kernel := stat["kernel"]
			if kernel == 0 {
				kernel = stat["kernel_stack"] + stat["slab"] + stat["pagetables"]
			}
			d.MemoryParts = &MemoryParts{Anon: stat["anon"], File: stat["file"], Kernel: kernel}
		}
		eachLine(filepath.Join(dir, "memory.events"), func(line string) {
			if v, ok := strings.CutPrefix(line, "oom_kill "); ok {
				d.OOMKills = parseUint(v)
			}
		})
		if max := firstLine(filepath.Join(dir, "pids.max")); max != "" && max != "max" && d.Limits.Pids == 0 {
			d.Limits.Pids = int64(parseUint(max))
		}
	}
	if live.State == "running" {
		d.Processes, d.ProcessesError = c.processes(ctx, live.Name)
	}
	return d, nil
}

func cmpDuration(nanos int64, fallback float64) float64 {
	if nanos <= 0 {
		return fallback
	}
	return float64(nanos) / 1e9
}

func cmpString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func (c *ContainerCollector) processes(ctx context.Context, name string) ([]ContainerProcess, string) {
	var top struct {
		Titles    []string
		Processes [][]string
	}
	if err := c.getJSON(ctx, "/containers/"+name+"/top", &top); err != nil {
		return nil, friendlyError(err)
	}
	procs := parseTop(top.Titles, top.Processes)
	for i, p := range procs {
		if p.RSS == 0 && p.PID != "" {
			if f := strings.Fields(firstLine(filepath.Join(c.proc, p.PID, "statm"))); len(f) > 1 {
				procs[i].RSS = parseUint(f[1]) * uint64(os.Getpagesize())
			}
		}
	}
	return procs, ""
}

func parseTop(titles []string, rows [][]string) []ContainerProcess {
	col := map[string]int{}
	for i, t := range titles {
		col[strings.ToUpper(t)] = i
	}
	field := func(row []string, names ...string) string {
		for _, n := range names {
			if i, ok := col[n]; ok && i < len(row) {
				return row[i]
			}
		}
		return ""
	}
	out := make([]ContainerProcess, 0, len(rows))
	for _, row := range rows {
		p := ContainerProcess{
			PID:     field(row, "PID"),
			User:    field(row, "USER", "UID"),
			Started: field(row, "START", "STIME"),
			Time:    field(row, "TIME"),
			Command: field(row, "COMMAND", "CMD"),
			RSS:     parseUint(field(row, "RSS")) * 1024,
		}
		if cpu, err := strconv.ParseFloat(field(row, "%CPU"), 64); err == nil {
			p.CPU = &cpu
		}
		out = append(out, p)
	}
	slices.SortStableFunc(out, func(a, b ContainerProcess) int { return cmp.Compare(ptrValue(b.CPU), ptrValue(a.CPU)) })
	return out
}

func ptrValue(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

type LogLine struct {
	ID     string `json:"id"`
	T      int64  `json:"t"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func (c *ContainerCollector) Logs(ctx context.Context, name, since string, tail int) ([]LogLine, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}, "tail": {strconv.Itoa(tail)}}
	var after int64
	if since != "" {
		n, err := strconv.ParseInt(since, 10, 64)
		if err != nil {
			return nil, errors.New("invalid since")
		}
		after = n
		q.Set("since", fmt.Sprintf("%d.%09d", n/1e9, n%1e9))
	}
	resp, err := c.fetch(ctx, "/containers/"+name+"/logs?"+q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	lines := parseDockerLogs(body)
	out := lines[:0]
	for _, l := range lines {
		if id, _ := strconv.ParseInt(l.ID, 10, 64); id > after {
			out = append(out, l)
		}
	}
	return out, nil
}

func parseDockerLogs(body []byte) []LogLine {
	streams := map[byte]*bytes.Buffer{}
	var order []byte
	appendTo := func(stream byte, payload []byte) {
		if streams[stream] == nil {
			streams[stream] = &bytes.Buffer{}
			order = append(order, stream)
		}
		streams[stream].Write(payload)
	}
	if multiplexed(body) {
		for len(body) >= 8 {
			size := int(binary.BigEndian.Uint32(body[4:8]))
			end := min(len(body), 8+size)
			appendTo(body[0], body[8:end])
			body = body[end:]
		}
	} else {
		appendTo(1, body)
	}
	var lines []LogLine
	for _, stream := range order {
		name := "stdout"
		if stream == 2 {
			name = "stderr"
		}
		scanner := bufio.NewScanner(streams[stream])
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		for scanner.Scan() {
			if l, ok := parseLogLine(scanner.Text(), name); ok {
				lines = append(lines, l)
			}
		}
	}
	slices.SortStableFunc(lines, func(a, b LogLine) int { return cmp.Compare(a.T, b.T) })
	return lines
}

func multiplexed(body []byte) bool {
	return len(body) >= 8 && body[0] <= 2 && body[1] == 0 && body[2] == 0 && body[3] == 0
}

func parseLogLine(raw, stream string) (LogLine, bool) {
	stamp, text, _ := strings.Cut(raw, " ")
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return LogLine{}, false
	}
	text = ansiEscape.ReplaceAllString(strings.TrimRight(text, "\r"), "")
	if len(text) > 8192 {
		text = text[:8192] + "…"
	}
	return LogLine{ID: strconv.FormatInt(t.UnixNano(), 10), T: t.UnixMilli(), Stream: stream, Text: text}, true
}

type ContainerPoint struct {
	T        int64   `json:"t"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	ReadBps  float64 `json:"readBps"`
	WriteBps float64 `json:"writeBps"`
	RxBps    float64 `json:"rxBps"`
	TxBps    float64 `json:"txBps"`
}

const containerRecentSize = 30 * 60 / 5

type containerSeries struct {
	recent, minutes, pending []ContainerPoint
	minute, seen             int64
}

type ContainerHistory struct {
	mu     sync.RWMutex
	series map[string]*containerSeries
}

func NewContainerHistory() *ContainerHistory {
	return &ContainerHistory{series: map[string]*containerSeries{}}
}

func (h *ContainerHistory) Record(now time.Time, list []Container) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t := now.UnixMilli()
	for _, c := range list {
		if c.State != "running" {
			continue
		}
		s := h.series[c.Name]
		if s == nil {
			s = &containerSeries{}
			h.series[c.Name] = s
		}
		p := ContainerPoint{T: t, CPU: c.CPU, Mem: float64(c.Memory), ReadBps: c.ReadBps, WriteBps: c.WriteBps, RxBps: c.RxBps, TxBps: c.TxBps}
		s.seen = t
		s.recent = append(s.recent, p)
		if len(s.recent) > containerRecentSize {
			s.recent = s.recent[len(s.recent)-containerRecentSize:]
		}
		minute := t / 60000
		if s.minute != 0 && minute != s.minute && len(s.pending) > 0 {
			s.minutes = append(s.minutes, averageContainer(s.pending))
			if len(s.minutes) > minuteSize {
				s.minutes = s.minutes[len(s.minutes)-minuteSize:]
			}
			s.pending = s.pending[:0]
		}
		s.minute = minute
		s.pending = append(s.pending, p)
	}
	for name, s := range h.series {
		if t-s.seen > 24*60*60*1000 {
			delete(h.series, name)
		}
	}
}

func (h *ContainerHistory) Range(name, rangeName string) []ContainerPoint {
	span, ok := Ranges[rangeName]
	if !ok {
		span = 5 * time.Minute
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	s := h.series[name]
	if s == nil {
		return []ContainerPoint{}
	}
	source := s.minutes
	if span <= containerRecentSize*5*time.Second {
		source = s.recent
	}
	cutoff := time.Now().Add(-span).UnixMilli()
	start := len(source)
	for start > 0 && source[start-1].T > cutoff {
		start--
	}
	return downsample(source[start:], chartPoints, averageContainer)
}

func averageContainer(points []ContainerPoint) ContainerPoint {
	var a ContainerPoint
	for _, p := range points {
		a.CPU += p.CPU
		a.Mem += p.Mem
		a.ReadBps += p.ReadBps
		a.WriteBps += p.WriteBps
		a.RxBps += p.RxBps
		a.TxBps += p.TxBps
	}
	if n := float64(len(points)); n > 0 {
		a.T = points[len(points)-1].T
		a.CPU /= n
		a.Mem /= n
		a.ReadBps /= n
		a.WriteBps /= n
		a.RxBps /= n
		a.TxBps /= n
	}
	return a
}

var (
	allowedDockerPaths = regexp.MustCompile(`^(/v[0-9.]+)?/(_ping|version|containers/json|containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/(json|top|logs))$`)
	inspectPath        = regexp.MustCompile(`^(/v[0-9.]+)?/containers/[a-zA-Z0-9][a-zA-Z0-9_.-]*/json$`)
)

func runDockerProxy(addr, socket, systemdSocket string, logs bool) error {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Scheme = "http"
			r.Out.URL.Host = "docker"
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		},
		ModifyResponse: redactInspect,
	}
	systemd := &systemdBus{socket: systemdSocket}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case r.Method != http.MethodGet:
			http.Error(w, "forbidden", http.StatusForbidden)
		case path == "/systemd/units":
			systemd.ServeHTTP(w, r)
		case !allowedDockerPaths.MatchString(path):
			http.Error(w, "forbidden", http.StatusForbidden)
		case strings.HasSuffix(path, "/logs") && !logs:
			http.Error(w, "container logs are turned off", http.StatusForbidden)
		default:
			r.URL.RawQuery = proxyQuery(path, r.URL.Query())
			proxy.ServeHTTP(w, r)
		}
	})
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

func proxyQuery(path string, q url.Values) string {
	switch {
	case strings.HasSuffix(path, "/top"):
		return "ps_args=aux"
	case strings.HasSuffix(path, "/logs"):
		q.Del("follow")
	}
	return q.Encode()
}

func redactInspect(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || !inspectPath.MatchString(resp.Request.URL.Path) {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc, lenient); err != nil {
		return err
	}
	if config, ok := doc["Config"].(map[string]any); ok {
		if env, ok := config["Env"].([]any); ok {
			for i, entry := range env {
				name, _, _ := strings.Cut(fmt.Sprint(entry), "=")
				env[i] = name
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	return nil
}
