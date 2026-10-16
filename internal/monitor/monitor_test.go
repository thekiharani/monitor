package monitor

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCPUBreakdownComesFromTickDeltas(t *testing.T) {
	proc := writeTree(t, map[string]string{"stat": "cpu  100 0 50 800 50 0 0 0\ncpu0 100 0 50 800 50 0 0 0\n"})
	c := NewSystemCollector(proc, t.TempDir(), t.TempDir())
	c.readCPU()

	os.WriteFile(filepath.Join(proc, "stat"), []byte("cpu  160 0 70 880 80 0 0 10\ncpu0 160 0 70 880 80 0 0 10\n"), 0o644)
	cpu := c.readCPU()

	if cpu.User != 30 || cpu.System != 10 || cpu.IOWait != 15 || cpu.Steal != 5 {
		t.Fatalf("breakdown = %+v, want user 30 system 10 iowait 15 steal 5", cpu)
	}
	if cpu.Total != 45 || len(cpu.Cores) != 1 || cpu.Cores[0] != 45 {
		t.Fatalf("total %v cores %v, want 45 busy excluding idle and iowait", cpu.Total, cpu.Cores)
	}
}

func TestMemoryUsedExcludesReclaimableCache(t *testing.T) {
	proc := writeTree(t, map[string]string{"meminfo": "MemTotal: 1000 kB\nMemAvailable: 600 kB\nBuffers: 50 kB\nCached: 200 kB\nSReclaimable: 30 kB\nSwapTotal: 500 kB\nSwapFree: 400 kB\n"})
	m := NewSystemCollector(proc, "", "").readMemory()

	if m.Used != 400*1024 || m.Cached != 280*1024 || m.SwapUsed != 100*1024 {
		t.Fatalf("memory = %+v", m)
	}
}

func TestDiskIOCountsOnlyPhysicalDisks(t *testing.T) {
	sys := writeTree(t, map[string]string{"block/sda/x": "", "block/loop0/x": ""})
	proc := writeTree(t, map[string]string{"diskstats": "8 0 sda 100 0 2000 0 50 0 4000 0 0 1000 0\n7 0 loop0 999 0 99999 0 999 0 99999 0 0 9999 0\n"})
	c := NewSystemCollector(proc, sys, "")
	c.readDiskIO(0, true)

	os.WriteFile(filepath.Join(proc, "diskstats"), []byte("8 0 sda 110 0 2200 0 70 0 4400 0 0 1500 0\n7 0 loop0 1999 0 199999 0 1999 0 199999 0 0 19999 0\n"), 0o644)
	io := c.readDiskIO(2, false)

	if io.ReadIOPS != 5 || io.WriteIOPS != 10 || io.ReadBps != 200*sectorBytes/2 || io.WriteBps != 400*sectorBytes/2 || io.Busy != 25 {
		t.Fatalf("io = %+v", io)
	}
}

func TestPressureMissingOnOlderKernels(t *testing.T) {
	if p := NewSystemCollector(t.TempDir(), "", "").readPressure(); p != nil {
		t.Fatalf("pressure = %+v, want nil without /proc/pressure", p)
	}
	proc := writeTree(t, map[string]string{
		"pressure/cpu":    "some avg10=12.50 avg60=1.00 avg300=0.00 total=1\n",
		"pressure/memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1\n",
		"pressure/io":     "some avg10=40.25 avg60=0.00 avg300=0.00 total=1\n",
	})
	p := NewSystemCollector(proc, "", "").readPressure()
	if p == nil || p.CPU != 12.5 || p.IO != 40.25 {
		t.Fatalf("pressure = %+v", p)
	}
}

func TestNetworkSkipsDockerAndDownInterfaces(t *testing.T) {
	proc := writeTree(t, map[string]string{"1/net/dev": "Inter-|\n face |\n    lo: 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\nenp1s0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\nveth1: 5 0 0 0 0 0 0 0 5 0 0 0 0 0 0 0\nwlan0: 5 0 0 0 0 0 0 0 5 0 0 0 0 0 0 0\n"})
	sys := writeTree(t, map[string]string{"class/net/enp1s0/operstate": "up\n", "class/net/wlan0/operstate": "down\n"})
	c := NewSystemCollector(proc, sys, "")
	c.readNet(0, true)
	os.WriteFile(filepath.Join(proc, "1/net/dev"), []byte("enp1s0: 3000 0 0 0 0 0 0 0 2500 0 0 0 0 0 0 0\n"), 0o644)
	got := c.readNet(2, false)

	if len(got) != 1 || got[0].Name != "enp1s0" || got[0].RxBps != 1000 || got[0].TxBps != 250 {
		t.Fatalf("net = %+v", got)
	}
}

func TestDockerProxyAllowsOnlyReadingContainers(t *testing.T) {
	for path, allowed := range map[string]bool{
		"/containers/json":             true,
		"/v1.47/containers/json":       true,
		"/_ping":                       true,
		"/containers/abc/json":         true,
		"/containers/my_app-1.web/top": true,
		"/v1.47/containers/abc/logs":   true,
		"/containers/create":           false,
		"/v1.47/containers/abc/exec":   false,
		"/containers/abc/archive":      false,
		"/containers/abc/stats":        false,
		"/containers/../images/json":   false,
		"/images/json":                 false,
	} {
		if got := allowedDockerPaths.MatchString(path); got != allowed {
			t.Errorf("%s allowed = %v, want %v", path, got, allowed)
		}
	}
	if q := proxyQuery("/containers/a/top", url.Values{"ps_args": {"-e; rm"}}); q != "ps_args=aux" {
		t.Errorf("top query = %q", q)
	}
	if q := proxyQuery("/containers/a/logs", url.Values{"follow": {"1"}, "tail": {"5"}}); q != "tail=5" {
		t.Errorf("logs query = %q", q)
	}
}

func TestProxyRedactsEnvironmentValues(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1.47/containers/db/json", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Request:    req,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"Id":"abc","Config":{"Env":["POSTGRES_PASSWORD=hunter2","PATH=/usr/bin","EMPTY"]}}`)),
	}
	if err := redactInspect(resp); err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "hunter2") || !strings.Contains(string(body), `"Env":["POSTGRES_PASSWORD","PATH","EMPTY"]`) {
		t.Fatalf("body = %s", body)
	}
}

func TestDockerLogsDemultiplexAndTTY(t *testing.T) {
	frame := func(stream byte, text string) []byte {
		b := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[4:], uint32(len(text)))
		return append(b, text...)
	}
	var body []byte
	body = append(body, frame(1, "2026-10-16T06:00:00.000000001Z started\n")...)
	body = append(body, frame(2, "2026-10-16T06:00:01.5Z \x1b[31mfailed\x1b[0m to conn")...)
	body = append(body, frame(2, "ect\n")...)
	lines := parseDockerLogs(body)
	if len(lines) != 2 || lines[0].Text != "started" || lines[0].Stream != "stdout" || lines[1].Text != "failed to connect" || lines[1].Stream != "stderr" || lines[0].ID != "1792130400000000001" {
		t.Fatalf("lines = %+v", lines)
	}
	tty := parseDockerLogs([]byte("2026-10-16T06:00:02Z prompt> \r\nnot a log line\n"))
	if len(tty) != 1 || tty[0].Text != "prompt> " || tty[0].Stream != "stdout" {
		t.Fatalf("tty = %+v", tty)
	}
}

func TestContainerProcessesFromPsAux(t *testing.T) {
	got := parseTop(
		[]string{"USER", "PID", "%CPU", "%MEM", "VSZ", "RSS", "TTY", "STAT", "START", "TIME", "COMMAND"},
		[][]string{
			{"root", "10", "0.1", "0.2", "1000", "2048", "?", "Ss", "06:00", "0:00", "nginx: master process"},
			{"www", "11", "3.5", "1.0", "1000", "4096", "?", "S", "06:00", "0:12", "nginx: worker process"},
		},
	)
	if len(got) != 2 || got[0].PID != "11" || *got[0].CPU != 3.5 || got[0].RSS != 4096*1024 || got[1].Command != "nginx: master process" {
		t.Fatalf("processes = %+v", got)
	}
}

func TestContainerHistoryKeepsRunningContainersByName(t *testing.T) {
	h := NewContainerHistory()
	start := time.Now().Add(-3 * time.Minute).Truncate(time.Minute)
	for i := range 36 {
		h.Record(start.Add(time.Duration(i)*5*time.Second), []Container{
			{Name: "web", State: "running", CPU: float64(i), Memory: 100},
			{Name: "job", State: "exited"},
		})
	}
	if got := h.Range("web", "5m"); len(got) != 36 || got[35].CPU != 35 || got[0].Mem != 100 {
		t.Fatalf("web history = %d points", len(got))
	}
	if got := h.Range("web", "1h"); len(got) != 2 || got[0].CPU != 5.5 {
		t.Fatalf("web minutes = %+v", got)
	}
	if got := h.Range("job", "5m"); len(got) != 0 {
		t.Fatalf("stopped container recorded %d points", len(got))
	}
}

func TestContainerProcessesWithoutPsColumns(t *testing.T) {
	got := parseTop([]string{"PID", "USER", "TIME", "COMMAND"}, [][]string{{"7", "999", "0:01", "mariadbd"}})
	if len(got) != 1 || got[0].CPU != nil || got[0].Command != "mariadbd" || got[0].User != "999" {
		t.Fatalf("processes = %+v", got)
	}
}

func TestContainerNetworkCountersSkipLoopback(t *testing.T) {
	proc := writeTree(t, map[string]string{"42/net/dev": "Inter-|\n face |\n    lo: 999 0 0 0 0 0 0 0 999 0 0 0 0 0 0 0\n  eth0: 1000 0 0 0 0 0 0 0 2000 0 0 0 0 0 0 0\n  eth1: 5 0 0 0 0 0 0 0 7 0 0 0 0 0 0 0\n"})
	if rx, tx := readNetDev(filepath.Join(proc, "42/net/dev")); rx != 1005 || tx != 2007 {
		t.Fatalf("rx %d tx %d", rx, tx)
	}
}

func TestSmartReadsKingstonLifeAndWrites(t *testing.T) {
	raw := `{"model_name":"KINGSTON SA400S37480G","smart_status":{"passed":true},"temperature":{"current":25},"power_on_time":{"hours":5637},
	"ata_smart_attributes":{"table":[{"id":5,"name":"Reallocated_Sector_Ct","value":100,"raw":{"value":0}},{"id":192,"name":"Unsafe_Shutdown_Count","value":100,"raw":{"value":424}},
	{"id":231,"name":"SSD_Life_Left","value":86,"raw":{"value":86}},{"id":241,"name":"Lifetime_Writes_GiB","value":100,"raw":{"value":22489}}]}}`
	d, ok := parseSmart("/dev/sda", []byte(raw))

	if !ok || d.LifeLeft == nil || *d.LifeLeft != 86 || d.UnsafeShutdowns != 424 || d.WrittenBytes != 22489<<30 || d.Passed == nil || !*d.Passed {
		t.Fatalf("disk = %+v", d)
	}
}

func TestSmartReadsNVMeWear(t *testing.T) {
	raw := `{"model_name":"Samsung 980","smart_status":{"passed":true},"nvme_smart_health_information_log":{"percentage_used":7,"data_units_written":1000,"unsafe_shutdowns":3,"media_errors":0}}`
	d, ok := parseSmart("/dev/nvme0", []byte(raw))

	if !ok || *d.LifeLeft != 93 || d.WrittenBytes != 512000000 || d.UnsafeShutdowns != 3 {
		t.Fatalf("disk = %+v", d)
	}
}

func TestVerdictIsGoodOnAQuietServer(t *testing.T) {
	s := Snapshot{Mem: Memory{Total: 100, Used: 10}, Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 5, InodesTotal: 100, InodesUsed: 1}}}
	if v := assess(s, Averages{CPU: 3}); v.Level != levelOK || len(v.Reasons) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestVerdictEscalatesToTheWorstProblem(t *testing.T) {
	failed := false
	s := Snapshot{
		Mem:         Memory{Total: 100, Used: 90},
		Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 96}},
		Containers:  []Container{{Name: "api", State: "running", Health: "unhealthy"}, {Name: "job", State: "exited", ExitCode: 0}},
		Postgres:    []PostgresStats{{Name: "db", Error: "unreachable"}},
		Health:      HostHealth{RebootRequired: true, Smart: &SmartReport{Disks: []DiskHealth{{Model: "X", Passed: &failed}}}},
	}
	v := assess(s, Averages{CPU: 50})

	if v.Level != levelCrit {
		t.Fatalf("level = %s, want crit", v.Level)
	}
	want := []string{"Memory is running low (90% used)", "Disk / is almost full (96%)", "Container api is unhealthy", "Postgres db is unreachable", "Drive X is failing its health check", "A reboot is needed to finish installing updates"}
	var got []string
	for _, r := range v.Reasons {
		got = append(got, r.Text)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reasons:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestHistoryRollsSecondsIntoMinutesAndDownsamples(t *testing.T) {
	h := NewHistory("")
	start := time.Now().Add(-10 * time.Minute).Truncate(time.Minute)
	for i := range 600 {
		h.Add(Point{T: start.Add(time.Duration(i) * time.Second).UnixMilli(), CPU: float64(i % 60), CPUMax: float64(i % 60)})
	}

	if len(h.minutes) != 9 || h.minutes[0].CPU != 29.5 || h.minutes[0].CPUMax != 59 {
		t.Fatalf("minutes = %d, first = %+v", len(h.minutes), h.minutes[0])
	}
	for _, name := range []string{"5m", "15m", "24h"} {
		if got := h.Range(name); len(got) == 0 || len(got) > chartPoints {
			t.Fatalf("%s range has %d points, want 1-%d", name, len(got), chartPoints)
		}
	}
}

func TestRedisInfoOverRESP(t *testing.T) {
	server, client := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		body := "# Server\r\nredis_version:8.10.2\r\nconnected_clients:3\r\ndb0:keys=7\r\n"
		for _, reply := range []string{"+OK\r\n", fmt.Sprintf("$%d\r\n%s\r\n", len(body), body)} {
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "$") && (line == "AUTH\r\n" || line == "INFO\r\n") {
					if line == "AUTH\r\n" {
						r.ReadString('\n')
						r.ReadString('\n')
					}
					break
				}
			}
			server.Write([]byte(reply))
		}
	}()
	r := bufio.NewReader(client)
	if _, err := redisCommand(client, r, "AUTH", "secret"); err != nil {
		t.Fatal(err)
	}
	info, err := redisCommand(client, r, "INFO")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, "redis_version:8.10.2") || !strings.Contains(info, "db0:keys=7") {
		t.Fatalf("info = %q", info)
	}
}

func TestQuietSnapshotSerialisesEmptyListsNotNull(t *testing.T) {
	c := NewSystemCollector(t.TempDir(), t.TempDir(), t.TempDir())
	s := Snapshot{System: c.Sample(time.Now())}
	s.Verdict = assess(s, Averages{})
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"reasons":null`, `"cores":null`, `"filesystems":null`, `"net":null`, `"byCpu":null`} {
		if strings.Contains(string(b), field) {
			t.Errorf("snapshot contains %s", field)
		}
	}
}

func TestDebianVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0},
		{"1.0-1", "1.0-2", -1},
		{"1.10", "1.9", 1},
		{"1.0~rc1", "1.0", -1},
		{"1.0~rc1", "1.0~rc2", -1},
		{"1:0.9", "2.0", 1},
		{"2.0", "1:0.9", -1},
		{"1.0a", "1.0", 1},
		{"1.0+deb13u1", "1.0", 1},
		{"1.0+deb13u2", "1.0+deb13u10", -1},
		{"1.0.0", "1.0", 1},
		{"6.12.111-1", "6.12.98-1", 1},
		{"1.01", "1.1", 0},
	} {
		if got := compareDebVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestUpdatesCountSecurityAndSkipBackports(t *testing.T) {
	root := writeTree(t, map[string]string{
		"var/lib/dpkg/status": "Package: openssl\nStatus: install ok installed\nArchitecture: amd64\nVersion: 3.5.1-1\n\n" +
			"Package: curl\nStatus: install ok installed\nArchitecture: amd64\nVersion: 8.14.1-2\n\n" +
			"Package: tzdata\nStatus: install ok installed\nArchitecture: all\nVersion: 2025b-4\n\n" +
			"Package: gone\nStatus: deinstall ok config-files\nArchitecture: amd64\nVersion: 1.0\n",
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie_main_binary-amd64_Packages":                        "Package: openssl\nArchitecture: amd64\nVersion: 3.5.1-1\n\nPackage: curl\nArchitecture: amd64\nVersion: 8.14.1-2\n\nPackage: tzdata\nArchitecture: all\nVersion: 2025c-1\n",
		"var/lib/apt/lists/security.debian.org_debian-security_dists_trixie-security_main_binary-amd64_Packages": "Package: openssl\nArchitecture: amd64\nVersion: 3.5.1-1+deb13u1\n",
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie-backports_main_binary-amd64_Packages":              "Package: curl\nArchitecture: amd64\nVersion: 8.16.0-1~bpo13+1\n",
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie-backports_InRelease":                               "Suite: trixie-backports\nNotAutomatic: yes\nButAutomaticUpgrades: yes\n",
	})
	u := (&HealthCollector{root: root}).CheckUpdates(context.Background())

	if u == nil || u.Pending != 2 || u.Security != 1 {
		t.Fatalf("updates = %+v, want 2 pending (openssl, tzdata) and 1 security", u)
	}
}

func TestUpdatesUnknownWithoutDpkg(t *testing.T) {
	if u := (&HealthCollector{root: t.TempDir()}).CheckUpdates(context.Background()); u != nil {
		t.Fatalf("updates = %+v, want nil on a non-Debian host", u)
	}
}

func TestDiskLatencyAndQueueFromDiskstats(t *testing.T) {
	sys := writeTree(t, map[string]string{"block/vda/x": ""})
	proc := writeTree(t, map[string]string{"diskstats": "252 0 vda 100 0 800 200 100 0 800 500 0 1000 4000\n"})
	c := NewSystemCollector(proc, sys, "")
	c.readDiskIO(0, true)

	os.WriteFile(filepath.Join(proc, "diskstats"), []byte("252 0 vda 110 0 880 300 140 0 1120 4500 2 3000 12000\n"), 0o644)
	io := c.readDiskIO(2, false)

	if io.ReadLatency != 10 || io.WriteLatency != 100 || io.Queue != 4 {
		t.Fatalf("read %vms write %vms queue %v, want 10, 100 and 4", io.ReadLatency, io.WriteLatency, io.Queue)
	}
}

func TestFilesystemReadOnlyAndExt4Errors(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "dev/mapper"), 0o755)
	os.Symlink("../dm-0", filepath.Join(root, "dev/mapper/vg-data"))
	os.MkdirAll(filepath.Join(root, "data"), 0o755)
	proc := writeTree(t, map[string]string{"1/mounts": "/dev/sda2 / ext4 rw,relatime 0 0\n/dev/mapper/vg-data /data ext4 ro,relatime 0 0\noverlay /var/lib/docker/overlay2/x/merged overlay rw 0 0\n"})
	sys := writeTree(t, map[string]string{
		"fs/ext4/sda2/errors_count":    "0\n",
		"fs/ext4/dm-0/errors_count":    "3\n",
		"fs/ext4/dm-0/last_error_time": "1792000000\n",
	})
	got := NewSystemCollector(proc, sys, root).readFilesystems()

	if len(got) != 2 || got[0].ReadOnly || got[0].Errors != 0 || !got[1].ReadOnly || got[1].Errors != 3 || got[1].LastErrorAt != 1792000000 {
		t.Fatalf("filesystems = %+v", got)
	}
}

func TestVerdictFlagsSlowDisksOnlyWhenBusy(t *testing.T) {
	quiet := assess(Snapshot{}, Averages{WriteOps: 0.1, WriteLatency: 900})
	if quiet.Level != levelOK {
		t.Fatalf("one slow write on an idle disk raised %+v", quiet.Reasons)
	}
	busy := assess(Snapshot{}, Averages{ReadOps: 40, ReadLatency: 150, WriteOps: 20, WriteLatency: 600, Queue: 20})
	var got []string
	for _, r := range busy.Reasons {
		got = append(got, r.Level+": "+r.Text)
	}
	want := "warn: Disk reads are slow (150 ms on average)\ncrit: Disk writes are very slow (600 ms on average)\nwarn: Disk requests are piling up (20 waiting on average)"
	if strings.Join(got, "\n") != want {
		t.Fatalf("reasons:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestVerdictTreatsReadOnlyAndRecentErrorsAsCritical(t *testing.T) {
	s := Snapshot{Filesystems: []Filesystem{
		{Mount: "/", Total: 100, ReadOnly: true},
		{Mount: "/data", Total: 100, Errors: 2, LastErrorAt: time.Now().Add(-3 * time.Hour).Unix()},
		{Mount: "/old", Total: 100, Errors: 1, LastErrorAt: time.Now().Add(-10 * 24 * time.Hour).Unix()},
	}}
	v := assess(s, Averages{})
	var got []string
	for _, r := range v.Reasons {
		got = append(got, r.Level+": "+r.Text)
	}
	want := "crit: Disk / is read-only, so nothing can be saved to it\ncrit: Disk /data has recorded 2 filesystem errors, last one 3 hours ago\nwarn: Disk /old has recorded 1 filesystem error, last one 10 days ago"
	if v.Level != levelCrit || strings.Join(got, "\n") != want {
		t.Fatalf("level %s reasons:\n%s\nwant:\n%s", v.Level, strings.Join(got, "\n"), want)
	}
}

func TestTargetsAcceptSeveralNamedOrBareURLs(t *testing.T) {
	got := parseTargets("main=postgres://u:p%40ss@db:5432/app?sslmode=disable  postgres://u:p@replica/app\nmemcached:11211")
	want := []Target{
		{"main", "postgres://u:p%40ss@db:5432/app?sslmode=disable"},
		{"replica", "postgres://u:p@replica/app"},
		{"memcached:11211", "memcached:11211"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}
	for raw, want := range map[string]string{
		"redis://:secret@cache":         "cache:6379",
		"mysql://root:pw@10.0.0.5":      "10.0.0.5:6379",
		"10.0.0.5:7000":                 "10.0.0.5:7000",
		"memcached://[::1]":             "[::1]:6379",
		"user:pa:ss@host.internal:6380": "host.internal:6380",
	} {
		if got := hostPort(raw, "6379"); got != want {
			t.Errorf("hostPort(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestMySQLConfigFromURL(t *testing.T) {
	cfg, err := mysqlConfig("mariadb://monitor:p%40ss@db?tls=skip-verify")
	if err != nil || cfg.Addr != "db:3306" || cfg.User != "monitor" || cfg.Passwd != "p@ss" || cfg.TLSConfig != "skip-verify" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
	if _, err := mysqlConfig("monitor:pw@tcp(db:3306)/"); err == nil || strings.Contains(err.Error(), "pw") {
		t.Fatalf("DSN form err = %v, want a rejection that hides the password", err)
	}
}

func TestNginxStubStatus(t *testing.T) {
	body := "Active connections: 291 \nserver accepts handled requests\n 16630948 16630940 31070465 \nReading: 6 Writing: 179 Waiting: 106 \n"
	s, requests, err := parseStubStatus(body)
	if err != nil || s.Active != 291 || s.Dropped != 8 || s.Writing != 179 || s.Waiting != 106 || requests != 31070465 {
		t.Fatalf("stats = %+v requests %d err %v", s, requests, err)
	}
	if _, _, err := parseStubStatus("<html>Welcome to nginx!</html>"); err == nil {
		t.Fatal("welcome page parsed as stub_status")
	}
}

func TestMemcachedStats(t *testing.T) {
	reply := "STAT version 1.6.38\r\nSTAT curr_connections 10\r\nSTAT get_hits 3\r\nEND\r\n"
	stats, err := readMemcachedStats(bufio.NewReader(strings.NewReader(reply)))
	if err != nil || stats["version"] != "1.6.38" || stats["curr_connections"] != "10" {
		t.Fatalf("stats = %v err %v", stats, err)
	}
	if _, err := readMemcachedStats(bufio.NewReader(strings.NewReader("ERROR\r\n"))); err == nil {
		t.Fatal("ERROR reply accepted")
	}
}

func TestRabbitMQOverviewAndNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "monitor" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/overview":
			w.Write([]byte(`{"rabbitmq_version":"4.1.0","queue_totals":{"messages":12,"messages_ready":9},"message_stats":{"publish_details":{"rate":2.5}},"object_totals":{"queues":3}}`))
		case "/api/nodes":
			w.Write([]byte(`[{"name":"rabbit@a","running":true,"mem_alarm":true}]`))
		}
	}))
	defer server.Close()
	ctx := context.Background()

	s := NewRabbitCollector(Target{"q", strings.Replace(server.URL, "://", "://monitor:secret@", 1) + "/"}).Collect(ctx)
	if !s.Up || s.Version != "4.1.0" || s.Messages != 12 || s.Ready != 9 || s.PublishRate != 2.5 || s.Queues != 3 || len(s.Nodes) != 1 || !s.Nodes[0].MemAlarm {
		t.Fatalf("stats = %+v", s)
	}
	if s := NewRabbitCollector(Target{"q", server.URL}).Collect(ctx); s.Up || s.Error != "rejecting the login" {
		t.Fatalf("anonymous stats = %+v", s)
	}
}

func TestChecksOverHTTPAndTCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, tc := range []struct {
		url, want string
	}{
		{server.URL + "/health", ""},
		{server.URL + "/broken", "returning HTTP 503"},
		{"tcp://" + server.Listener.Addr().String(), ""},
		{"tcp://" + closed, "unreachable"},
		{"udp://" + closed, `using unsupported scheme "udp"`},
	} {
		ck := NewCheckCollector(Target{"x", tc.url}).Collect(ctx)
		if ck.Up != (tc.want == "") || ck.Error != tc.want {
			t.Errorf("%s: check = %+v, want error %q", tc.url, ck, tc.want)
		}
	}
	if ck := NewCheckCollector(Target{"x", "https://user:secret@example.invalid"}); strings.Contains(ck.(*CheckCollector).target, "secret") {
		t.Fatalf("target %q exposes the password", ck.(*CheckCollector).target)
	}
}

func TestSystemdFailedAndWatchedUnits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"nginx.service","load":"loaded","active":"active","sub":"running"},
			{"name":"mariadb.service","load":"loaded","active":"failed","sub":"failed"},
			{"name":"backup.timer","load":"loaded","active":"failed","sub":"failed"},
			{"name":"cron.service","load":"loaded","active":"inactive","sub":"dead"}]`))
	}))
	defer server.Close()
	s := *NewSystemdCollector(server.URL, "nginx mariadb cron redis-server").Collect(context.Background())
	if s.Services != 3 || s.Running != 1 || len(s.Failed) != 2 || s.Failed[0].Name != "backup.timer" || len(s.Watched) != 4 || s.Watched[3].Active != "inactive" {
		t.Fatalf("status = %+v", s)
	}
	v := assess(Snapshot{Systemd: &s}, Averages{})
	var got []string
	for _, r := range v.Reasons {
		got = append(got, r.Text)
	}
	want := "Unit backup.timer has failed\nService mariadb has failed\nService cron is not running (inactive)\nService redis-server is not running (inactive)"
	if strings.Join(got, "\n") != want {
		t.Fatalf("reasons:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
	if NewSystemdCollector("", "nginx") != nil {
		t.Fatal("collector enabled without a URL")
	}
}

func TestVerdictCoversEveryService(t *testing.T) {
	lag := 600.0
	s := Snapshot{
		MySQL: []MySQLStats{
			{Name: "db", Flavor: "MariaDB", Up: true, Connections: 90, MaxConnections: 100, Replica: &Replica{IORunning: true, SQLRunning: false, Error: "Duplicate entry"}},
			{Name: "ro", Flavor: "MySQL", Up: true, MaxConnections: 100, Replica: &Replica{IORunning: true, SQLRunning: true, Lag: &lag}},
		},
		Mongo:     []MongoStats{{Name: "m", Up: true, MaxConnections: 100, UnhealthyMembers: 1}},
		Memcached: []MemcachedStats{{Name: "mc", Error: "unreachable"}},
		Nginx:     []NginxStats{{Name: "web", Up: true, Dropped: 4}},
		RabbitMQ:  []RabbitStats{{Name: "q", Up: true, Nodes: []RabbitNode{{Name: "rabbit@a", Running: true, DiskAlarm: true}}}},
		Checks: []Check{
			{Name: "api", Error: "returning HTTP 502"},
			{Name: "site", Up: true, CertExpires: time.Now().Add(10 * 24 * time.Hour).Unix()},
		},
	}
	v := assess(s, Averages{})
	var got []string
	for _, r := range v.Reasons {
		got = append(got, r.Level+": "+r.Text)
	}
	want := []string{
		"warn: MariaDB db is using 90 of 100 connections",
		"crit: MariaDB db has stopped replicating: Duplicate entry",
		"warn: MySQL ro is 10 minutes behind its source",
		"warn: MongoDB m has 1 unreachable replica set member",
		"crit: Memcached mc is unreachable",
		"warn: Nginx web has dropped 4 connections since it started; raise worker_connections",
		"crit: RabbitMQ node rabbit@a is low on disk, so publishers are blocked",
		"crit: api is returning HTTP 502",
		"warn: The certificate for site expires in 9 days",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reasons:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestSystemdBusMissingSocketIsNotFound(t *testing.T) {
	w := httptest.NewRecorder()
	(&systemdBus{socket: filepath.Join(t.TempDir(), "missing")}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/systemd/units", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestHistoryKeepsTheWorstLevelAndItsIssue(t *testing.T) {
	v := assess(Snapshot{Mem: Memory{Total: 100, Used: 90}, Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 99}}}, Averages{})
	level, issue := v.worst()
	if level != 2 || issue != "Disk / is almost full (99%)" || v.Reasons[0].Ref != "memory" || v.Reasons[1].Ref != "storage" {
		t.Fatalf("worst = %d %q, reasons %+v", level, issue, v.Reasons)
	}
	a := average([]Point{{Level: 1, Issue: "busy"}, {Level: 2, Issue: "full"}, {Level: 2, Issue: "later"}, {}})
	if a.Level != 2 || a.Issue != "full" {
		t.Fatalf("average level %d issue %q", a.Level, a.Issue)
	}
}

func TestServiceHistoryAveragesWhateverMetricsArrive(t *testing.T) {
	h := NewServiceHistory()
	start := time.Now().Add(-3 * time.Minute).Truncate(time.Minute)
	for i := range 24 {
		values := map[string]float64{"up": 1, "qps": float64(i)}
		if i == 5 {
			values = map[string]float64{"up": 0}
		}
		h.Record("mysql:db", start.Add(time.Duration(i)*5*time.Second), values)
	}
	recent := h.Range("mysql:db", "5m")
	if len(recent) != 24 || recent[5]["up"] != 0 || recent[23]["qps"] != 23 {
		t.Fatalf("recent = %v", recent)
	}
	minutes := h.Range("mysql:db", "1h")
	if len(minutes) != 1 || minutes[0]["up"] != 11.0/12 || minutes[0]["qps"] != 61.0/11 {
		t.Fatalf("minutes = %v", minutes)
	}
	if got := h.Range("redis:none", "5m"); len(got) != 0 {
		t.Fatalf("unknown service = %v", got)
	}
}

func TestRedisArraysSlowlogAndClients(t *testing.T) {
	reply := "*1\r\n*6\r\n:14\r\n:1792120000\r\n:15000\r\n*3\r\n$3\r\nSET\r\n$4\r\nuser\r\n$6\r\nsecret\r\n$14\r\n10.0.0.5:51234\r\n$0\r\n\r\n"
	parsed, err := readRedisReply(bufio.NewReader(strings.NewReader(reply)))
	if err != nil {
		t.Fatal(err)
	}
	entry := parsed.([]any)[0]
	slow, ok := parseSlowlogEntry(entry)
	if !ok || slow.ID != 14 || slow.Duration != 15 || slow.Command != "SET user secret" || slow.Client != "10.0.0.5:51234" || slowlogCommandName(entry) != "SET" {
		t.Fatalf("slow = %+v", slow)
	}
	clients := parseClientList("id=3 addr=10.0.0.5:5000 laddr=x fd=8 name=worker age=120 idle=3 flags=N db=0 cmd=get\nid=4 addr=10.0.0.6:5001 name= age=1 idle=0 db=2 cmd=client|list\n")
	if len(clients) != 1 || clients[0].Name != "worker" || clients[0].Age != 120 || clients[0].DB != "0" || clients[0].Command != "get" {
		t.Fatalf("clients = %+v", clients)
	}
}

func TestQueryTextFollowsTheSwitch(t *testing.T) {
	if got := queryText(withQueryText(context.Background(), true), "select 1"); got != "select 1" {
		t.Fatalf("shown = %q", got)
	}
	if got := queryText(withQueryText(context.Background(), false), "select 1"); got != "" {
		t.Fatalf("hidden = %q", got)
	}
}

func TestCheckRecordsOnlyChanges(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer server.Close()
	c := NewCheckCollector(Target{"api", server.URL}).(*CheckCollector)
	ctx := context.Background()
	for _, code := range []int{200, 200, 503, 503, 500, 200} {
		status = code
		c.Collect(ctx)
	}
	d, _ := c.Detail(ctx)
	var got []string
	for _, e := range d.(CheckDetail).Events {
		got = append(got, fmt.Sprintf("%v %s", e.Up, e.Error))
	}
	want := "true \nfalse returning HTTP 500\nfalse returning HTTP 503\ntrue "
	if strings.Join(got, "\n") != want {
		t.Fatalf("events newest first:\n%s\nwant:\n%s", strings.Join(got, "\n"), want)
	}
}

func TestServicesRegisterForDetailPages(t *testing.T) {
	registry := NewServiceRegistry(true)
	ctx := t.Context()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	checks := pollEach(ctx, time.Hour, "api="+server.URL, "check", NewCheckCollector, checkMetrics, registry)
	deadline := time.Now().Add(2 * time.Second)
	for len(checks()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c, ok := registry.lookup("check:api"); !ok || c == nil {
		t.Fatal("check not registered")
	}
	if points := registry.history.Range("check:api", "5m"); len(points) != 1 || points[0]["up"] != 1 {
		t.Fatalf("history = %v", points)
	}
}

func TestSystemdDetailListsFailedUnitsFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"name":"zebra.service","load":"loaded","active":"active","sub":"running"},
			{"name":"backup.service","load":"loaded","active":"inactive","sub":"dead"},
			{"name":"mariadb.service","load":"loaded","active":"failed","sub":"failed"},
			{"name":"app.service","load":"loaded","active":"activating","sub":"start"}]`))
	}))
	defer server.Close()
	c := NewSystemdCollector(server.URL, "zebra")
	if _, err := c.Detail(context.Background()); err == nil {
		t.Fatal("detail before the first poll should report no unit list")
	}
	c.Collect(context.Background())
	d, err := c.Detail(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	var got struct {
		Units   []Unit
		Watched []string
	}
	json.Unmarshal(b, &got)
	var names []string
	for _, u := range got.Units {
		names = append(names, u.Name)
	}
	if strings.Join(names, ",") != "mariadb.service,app.service,zebra.service,backup.service" || len(got.Watched) != 1 || got.Watched[0] != "zebra.service" {
		t.Fatalf("units %v watched %v", names, got.Watched)
	}
}

func TestAssetsNegotiateEncodingAndRevalidate(t *testing.T) {
	files := fstest.MapFS{
		"index.html": {Data: []byte("<p>hi</p>")},
		"app.js":     {Data: []byte("let a=1")},
		"app.js.br":  {Data: []byte("brotli-bytes")},
		"app.js.gz":  {Data: []byte("gzip-bytes")},
		"style.css":  {Data: []byte("p{}")},
		"web.go":     {Data: []byte("package web")},
	}
	assets, err := loadAssets(files)
	if err != nil {
		t.Fatal(err)
	}
	handler := serveAssets(assets)
	get := func(path, accept, match string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept-Encoding", accept)
		r.Header.Set("If-None-Match", match)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := get("/app.js", "gzip, deflate, br", ""); w.Header().Get("Content-Encoding") != "br" || w.Body.String() != "brotli-bytes" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("brotli: %v %q", w.Header(), w.Body.String())
	}
	if w := get("/app.js", "gzip, br;q=0.0", ""); w.Header().Get("Content-Encoding") != "gzip" || w.Body.String() != "gzip-bytes" {
		t.Fatalf("gzip: %v", w.Header())
	}
	if w := get("/app.js", "", ""); w.Header().Get("Content-Encoding") != "" || w.Body.String() != "let a=1" {
		t.Fatalf("identity: %v", w.Header())
	}
	css := get("/style.css", "gzip", "")
	zr, err := gzip.NewReader(css.Body)
	if err != nil {
		t.Fatal(err)
	}
	if plain, _ := io.ReadAll(zr); string(plain) != "p{}" {
		t.Fatalf("on-the-fly gzip = %q", plain)
	}
	etag := get("/", "", "").Header().Get("ETag")
	if w := get("/", "br", etag); w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("revalidation = %d", w.Code)
	}
	if w := get("/app.js.gz", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("compressed copy served directly: %d", w.Code)
	}
	if w := get("/web.go", "", ""); w.Code != http.StatusNotFound {
		t.Fatalf("Go source served: %d", w.Code)
	}
}

func TestPrometheusTextWithLabelsAndEscapes(t *testing.T) {
	body := `# HELP caddy_http_requests_total Counter.
# TYPE caddy_http_requests_total counter
caddy_http_requests_total{handler="reverse_proxy",server="srv0"} 3
caddy_reverse_proxy_upstreams_healthy{upstream="deadhost:80"} 0
weird{path="/a\"b",note="x,y"} 1.5e+01 1792125104000
plain_metric 7
`
	samples := parsePrometheus(body)
	if len(samples) != 4 || samples[0].labels["server"] != "srv0" || samples[2].labels["path"] != `/a"b` || samples[2].labels["note"] != "x,y" || samples[2].value != 15 || samples[3].name != "plain_metric" || samples[3].value != 7 {
		t.Fatalf("samples = %+v", samples)
	}
	if v, ok := sumSamples(samples, "caddy_http_requests_total", nil); !ok || v != 3 {
		t.Fatalf("sum = %v %v", v, ok)
	}
}

func TestHAProxyStatsCSV(t *testing.T) {
	csv := "# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,weight,act,bck,chkfail,chkdown,lastchg,downtime,qlimit,pid,iid,sid,throttle,lbtot,tracked,type,rate,rate_lim,rate_max,check_status,check_code,check_duration,hrsp_1xx,hrsp_2xx,hrsp_3xx,hrsp_4xx,hrsp_5xx,hrsp_other,hanafail,req_rate,req_rate_max,req_tot,\n" +
		"web,FRONTEND,,,0,2,524266,3,0,0,0,0,0,,,,,OPEN,,,,,,,,,1,2,0,,,,0,0,0,3,,,,0,3,0,0,4,0,,7,3,30,\n" +
		"stats,FRONTEND,,,1,1,524266,1,0,0,0,0,0,,,,,OPEN,,,,,,,,,1,3,0,,,,0,1,0,1,,,,0,0,0,0,0,0,,1,1,1,\n" +
		"app,nginx,0,0,0,1,,3,0,0,,0,,0,0,0,0,UP,1,1,0,0,0,35,0,,1,4,1,,3,,2,0,,1,L7OK,200,1,0,3,0,0,0,0,,,,,\n" +
		"app,dead,0,0,0,0,,0,0,0,,0,,0,0,0,0,DOWN,1,1,0,1,1,32,32,,1,4,2,,0,,2,0,,0,* L4TOUT,,2001,0,0,0,0,0,0,,,,,\n" +
		"app,BACKEND,0,0,0,1,52427,3,0,0,0,0,,0,0,0,0,UP,1,1,0,,1,35,0,,1,4,0,,3,,1,0,,1,,,,0,3,0,0,0,0,,,,3,\n"
	rows, err := parseHAProxyCSV(csv)
	if err != nil || len(rows) != 5 || rows[3].Status != "DOWN" || rows[3].Check != "L4TOUT" || rows[0].Total != 30 || rows[0].Errors5xx != 4 || rows[0].Rate != 7 {
		t.Fatalf("rows = %+v err %v", rows, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "/stats;csv" {
			w.WriteHeader(http.StatusNotFound)
		}
		w.Write([]byte(csv))
	}))
	defer server.Close()
	s := NewHAProxyCollector(Target{"lb", server.URL + "/stats"}).Collect(context.Background())
	if !s.Up || s.Frontends != 1 || s.Backends != 1 || s.ServersUp != 1 || fmt.Sprint(s.ServersDown) != "[app/dead]" || len(s.BackendsDown) != 0 {
		t.Fatalf("stats = %+v", s)
	}
	if _, err := parseHAProxyCSV("<html>stats</html>"); err == nil {
		t.Fatal("HTML page accepted")
	}
}

func TestCaddyUpstreamHealthAndRates(t *testing.T) {
	requests := 3.0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/reverse_proxy/upstreams":
			w.Write([]byte(`[{"address":"deadhost:80","num_requests":0,"fails":0},{"address":"nginx:80","num_requests":2,"fails":0},{"address":"flaky:80","num_requests":0,"fails":3}]`))
		case "/metrics":
			fmt.Fprintf(w, "caddy_config_last_reload_successful 1\ncaddy_reverse_proxy_upstreams_healthy{upstream=\"deadhost:80\"} 0\ncaddy_reverse_proxy_upstreams_healthy{upstream=\"nginx:80\"} 1\ncaddy_http_requests_total{handler=\"reverse_proxy\",server=\"srv0\"} %v\n"+
				"caddy_http_request_duration_seconds_count{code=\"200\",server=\"srv0\"} %v\ncaddy_http_request_duration_seconds_count{code=\"502\",server=\"srv0\"} %v\ncaddy_http_request_duration_seconds_sum{code=\"200\",server=\"srv0\"} %v\n", requests, requests*0.9, requests*0.1, requests*0.02)
		case "/config/apps/http/servers":
			w.Write([]byte(`{"srv0":{"listen":[":443"],"routes":[{"match":[{"host":["app.example.com"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"reverse_proxy","upstreams":[{"dial":"nginx:80"}]}]}]}]}]},"srv1":{"listen":[":8081"]}}`))
		}
	}))
	defer server.Close()
	c := NewCaddyCollector(Target{"edge", server.URL + "/"}).(*CaddyCollector)
	ctx := context.Background()
	c.Collect(ctx)
	requests = 103
	c.rates.at = c.rates.at.Add(-10 * time.Second)
	s := c.Collect(ctx)
	if !s.Up || !s.Metrics || s.Servers != 2 || fmt.Sprint(s.Unhealthy) != "[deadhost:80 flaky:80]" || *s.ConfigOK != true || s.RequestsPerSec < 9.9 || s.RequestsPerSec > 10.1 || math.Abs(s.ErrorRate-10) > 0.01 || math.Abs(s.Latency-20) > 0.01 {
		t.Fatalf("stats = %+v", s)
	}
	d, err := c.Detail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `"hosts":["app.example.com"]`) || !strings.Contains(string(b), `"handlers":["reverse_proxy"]`) || !strings.Contains(string(b), `"upstreams":["nginx:80"]`) {
		t.Fatalf("detail = %s", b)
	}
}

func TestTraefikRoutersServersAndMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"Version":"3.7.15"}`))
		case "/api/overview":
			w.Write([]byte(`{"providers":["File","Docker"]}`))
		case "/api/http/routers":
			w.Write([]byte(`[{"name":"api@internal","provider":"internal","status":"enabled"},{"name":"app@file","provider":"file","status":"enabled","service":"app"},{"name":"broken@file","provider":"file","status":"disabled","error":["the service \"missing@file\" does not exist"]}]`))
		case "/api/http/services":
			w.Write([]byte(`[{"name":"app@file","provider":"file","status":"enabled","serverStatus":{"http://deadhost:80":"DOWN","http://nginx:80":"UP"}}]`))
		case "/metrics":
			w.Write([]byte("traefik_entrypoint_requests_total{code=\"200\",entrypoint=\"web\"} 3\ntraefik_open_connections{entrypoint=\"traefik\"} 1\ntraefik_open_connections{entrypoint=\"web\"} 4\n"))
		}
	}))
	defer server.Close()
	s := NewTraefikCollector(Target{"edge", server.URL}).Collect(context.Background())
	if !s.Up || s.Version != "3.7.15" || s.Routers != 2 || fmt.Sprint(s.RouterErrors) != "[broken@file]" || s.ServersUp != 1 || fmt.Sprint(s.ServersDown) != "[app@file → http://deadhost:80]" || !s.Metrics || s.Connections != 4 || len(s.Providers) != 2 {
		t.Fatalf("stats = %+v", s)
	}
}

func TestApacheModStatus(t *testing.T) {
	accesses := 3
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "auto" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "localhost\nServerVersion: Apache/2.4.69 (Unix)\nServerMPM: event\nServerUptimeSeconds: 35\nTotal Accesses: %d\nTotal kBytes: 10\nBusyWorkers: 1\nIdleWorkers: 74\nConnsTotal: 2\nScoreboard: __W_K..\n", accesses)
	}))
	defer server.Close()
	c := NewApacheCollector(Target{"web", server.URL + "/server-status"}).(*ApacheCollector)
	c.Collect(context.Background())
	accesses = 53
	c.rates.at = c.rates.at.Add(-10 * time.Second)
	s := c.Collect(context.Background())
	if !s.Up || s.Version != "2.4.69" || s.MPM != "event" || s.Busy != 1 || s.Idle != 74 || s.RequestsPerSec < 4.9 || s.RequestsPerSec > 5.1 {
		t.Fatalf("stats = %+v", s)
	}
	d, _ := c.Detail(context.Background())
	b, _ := json.Marshal(d)
	if !strings.Contains(string(b), `{"name":"Waiting for a connection","value":"3"}`) || !strings.Contains(string(b), `{"name":"Keeping the connection alive","value":"1"}`) {
		t.Fatalf("detail = %s", b)
	}
}

func TestVerdictCoversReverseProxies(t *testing.T) {
	failed := false
	s := Snapshot{
		Caddy:   []CaddyStats{{Name: "edge", Up: true, Upstreams: make([]CaddyUpstream, 2), Unhealthy: []string{"app:80"}, ConfigOK: &failed, RequestsPerSec: 10, ErrorRate: 12}},
		Traefik: []TraefikStats{{Name: "lb", Up: true, ServersDown: []string{"app@file → http://a:80"}, RouterErrors: []string{"broken@file"}}},
		HAProxy: []HAProxyStats{{Name: "ha", Up: true, BackendsDown: []string{"api"}, ServersDown: []string{"api/one"}}},
		Apache:  []ApacheStats{{Name: "www", Up: true, Busy: 150}},
	}
	var got []string
	for _, r := range assess(s, Averages{}).Reasons {
		got = append(got, r.Level+": "+r.Text+" ["+r.Ref+"]")
	}
	want := []string{
		"warn: Caddy edge has 1 unhealthy upstream: app:80 [caddy:edge]",
		"warn: Caddy edge failed to load its latest config [caddy:edge]",
		"warn: Caddy edge is answering 12% of requests with server errors [caddy:edge]",
		"crit: Traefik lb has 1 server down: app@file → http://a:80 [traefik:lb]",
		"warn: Traefik lb has 1 router with errors: broken@file [traefik:lb]",
		"crit: HAProxy ha backend api is down [haproxy:ha]",
		"warn: HAProxy ha has 1 server down: api/one [haproxy:ha]",
		"warn: Apache www has no idle workers left (150 busy) [apache:www]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("reasons:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

type fakeChannel struct {
	name, level string
	err         error
	mu          sync.Mutex
	sent        []Notification
}

func (f *fakeChannel) Name() string  { return f.name }
func (f *fakeChannel) Level() string { return f.level }
func (f *fakeChannel) Send(_ context.Context, n Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, n)
	return f.err
}

func (f *fakeChannel) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, n := range f.sent {
		var texts []string
		for _, inc := range n.Incidents {
			texts = append(texts, inc.Text)
		}
		out = append(out, n.Event+": "+strings.Join(texts, " | "))
	}
	return out
}

func newTestAlerter(t *testing.T, start time.Time, channels ...Notifier) *Alerter {
	cfg := AlertConfig{Host: "web-1", PublicURL: "http://mon", DelayCrit: time.Minute, DelayWarn: 5 * time.Minute, ResolveAfter: 2 * time.Minute, Repeat: 4 * time.Hour, Grace: 2 * time.Minute, Group: 30 * time.Second, StateFile: filepath.Join(t.TempDir(), "alerts.json"), Channels: channels}
	a := NewAlerter(cfg, start)
	a.async = func(f func()) { f() }
	a.retries = []time.Duration{0}
	return a
}

func reason(level, key, text string) Reason {
	ref, _, _ := strings.Cut(key, "/")
	return Reason{Level: level, Key: key, Ref: ref, Text: text}
}

func run(a *Alerter, from time.Time, seconds int, reasons func(i int) []Reason) time.Time {
	for i := range seconds {
		a.Observe(from.Add(time.Duration(i)*time.Second), reasons(i))
	}
	return from.Add(time.Duration(seconds) * time.Second)
}

func TestAlertsWaitGroupResolveAndIgnoreFlaps(t *testing.T) {
	start := time.Now()
	ch := &fakeChannel{name: "Telegram", level: levelWarn}
	a := newTestAlerter(t, start, ch)
	down := reason(levelCrit, "postgres:main/down", "Postgres main is unreachable")
	full := reason(levelCrit, "storage/full:/", "Disk / is almost full (97%)")
	now := run(a, start, 180, func(int) []Reason { return nil })
	now = run(a, now, 40, func(i int) []Reason {
		if i%10 < 5 {
			return []Reason{reason(levelCrit, "cpu/busy", "CPU is maxed out")}
		}
		return nil
	})
	if got := ch.log(); len(got) != 0 {
		t.Fatalf("flapping problem alerted: %v", got)
	}
	now = run(a, now, 95, func(i int) []Reason {
		if i < 5 {
			return []Reason{down}
		}
		return []Reason{down, full}
	})
	if got := ch.log(); len(got) != 1 || got[0] != "firing: Postgres main is unreachable | Disk / is almost full (97%)" {
		t.Fatalf("expected one grouped alert, got %v", got)
	}
	now = run(a, now, 200, func(int) []Reason { return []Reason{full} })
	if got := ch.log(); len(got) != 2 || got[1] != "resolved: Postgres main is unreachable" {
		t.Fatalf("expected a resolution, got %v", got)
	}
	n := ch.sent[1]
	if n.Incidents[0].ResolvedAt == 0 || len(n.Open) != 1 || n.Open[0].Key != "storage/full:/" {
		t.Fatalf("resolution = %+v", n)
	}
	_ = now
}

func TestAlertsEscalateRemindAndRespectChannelLevels(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	telegram := &fakeChannel{name: "Telegram", level: levelWarn}
	email := &fakeChannel{name: "Email", level: levelCrit}
	a := newTestAlerter(t, start, telegram, email)
	a.cfg.Grace = 0
	warn := reason(levelWarn, "storage/full:/data", "Disk /data is 88% full")
	crit := reason(levelCrit, "storage/full:/data", "Disk /data is almost full (96%)")
	now := run(a, start, 340, func(int) []Reason { return []Reason{warn} })
	if fmt.Sprint(telegram.log()) != "[firing: Disk /data is 88% full]" || len(email.log()) != 0 {
		t.Fatalf("telegram %v email %v", telegram.log(), email.log())
	}
	now = run(a, now, 40, func(int) []Reason { return []Reason{crit} })
	if fmt.Sprint(telegram.log()) != "[firing: Disk /data is 88% full escalated: Disk /data is almost full (96%)]" || fmt.Sprint(email.log()) != "[escalated: Disk /data is almost full (96%)]" {
		t.Fatalf("telegram %v email %v", telegram.log(), email.log())
	}
	a.cfg.Repeat = time.Minute
	run(a, now, 100, func(int) []Reason { return []Reason{crit} })
	if got := email.log(); len(got) != 2 || !strings.HasPrefix(got[1], "reminder: ") {
		t.Fatalf("email %v", got)
	}
}

func TestAlertsMuteAndMaintenance(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	ch := &fakeChannel{name: "Telegram", level: levelWarn}
	a := newTestAlerter(t, start, ch)
	a.cfg.Grace = 0
	down := reason(levelCrit, "redis:cache/down", "Redis cache is unreachable")
	now := run(a, start, 5, func(int) []Reason { return []Reason{down} })
	if err := a.Mute("redis:cache/down", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := a.Mute("nothing/open", 10, now); err == nil {
		t.Fatal("muting an unknown problem should fail")
	}
	now = run(a, now, 120, func(int) []Reason { return []Reason{down} })
	if len(ch.log()) != 0 || !a.state.Open["redis:cache/down"].Silenced {
		t.Fatalf("muted problem alerted: %v", ch.log())
	}
	now = run(a, now, 150, func(int) []Reason { return nil })
	if len(ch.log()) != 0 || len(a.state.Mutes) != 0 {
		t.Fatalf("silenced resolution alerted %v or mute kept %v", ch.log(), a.state.Mutes)
	}
	a.SetMaintenance(10, now)
	now = run(a, now, 120, func(int) []Reason { return []Reason{down} })
	if len(ch.log()) != 0 {
		t.Fatalf("alerted during maintenance: %v", ch.log())
	}
	run(a, now.Add(10*time.Minute), 40, func(int) []Reason { return []Reason{down} })
	if got := ch.log(); len(got) != 1 || got[0] != "firing: Redis cache is unreachable" {
		t.Fatalf("after maintenance: %v", got)
	}
}

func TestAlertsSurviveRestartWithoutRealerting(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	ch := &fakeChannel{name: "Telegram", level: levelWarn}
	a := newTestAlerter(t, start, ch)
	a.cfg.Grace = 0
	down := reason(levelCrit, "check:api/down", "api is unreachable")
	now := run(a, start, 100, func(int) []Reason { return []Reason{down} })
	a.Save()
	restartAt := now.Add(5 * time.Minute)
	cfg := a.cfg
	cfg.Grace = 2 * time.Minute
	restarted := NewAlerter(cfg, restartAt)
	restarted.async = func(f func()) { f() }
	restarted.retries = []time.Duration{0}
	now = run(restarted, restartAt, 30, func(int) []Reason { return nil })
	now = run(restarted, now, 200, func(int) []Reason { return []Reason{down} })
	if got := ch.log(); fmt.Sprint(got) != "[firing: api is unreachable]" {
		t.Fatalf("restart re-alerted or resolved during warm-up: %v", got)
	}
	run(restarted, now, 200, func(int) []Reason { return nil })
	if got := ch.log(); fmt.Sprint(got) != "[firing: api is unreachable resolved: api is unreachable]" {
		t.Fatalf("after restart: %v", got)
	}
	if h := restarted.Status().History; len(h) != 1 || h[0].Key != "check:api/down" {
		t.Fatalf("history = %+v", h)
	}
}

func TestMessageWaitingToBeGroupedSurvivesRestart(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	ch := &fakeChannel{name: "Telegram", level: levelWarn}
	a := newTestAlerter(t, start, ch)
	a.cfg.Grace = 0
	down := reason(levelCrit, "check:api/down", "api is unreachable")
	now := run(a, start, 62, func(int) []Reason { return []Reason{down} })
	if len(ch.log()) != 0 || len(a.state.Queue) != 1 {
		t.Fatalf("expected a queued alert, sent %v queue %d", ch.log(), len(a.state.Queue))
	}
	restarted := NewAlerter(a.cfg, now.Add(3*time.Second))
	restarted.async = func(f func()) { f() }
	restarted.retries = []time.Duration{0}
	run(restarted, now.Add(3*time.Second), 40, func(int) []Reason { return []Reason{down} })
	if got := ch.log(); fmt.Sprint(got) != "[firing: api is unreachable]" {
		t.Fatalf("queued alert lost across restart: %v", got)
	}
}

func TestMuteDropsAMessageStillWaitingToBeGrouped(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	ch := &fakeChannel{name: "Telegram", level: levelWarn}
	a := newTestAlerter(t, start, ch)
	a.cfg.Grace = 0
	down := reason(levelCrit, "redis:cache/down", "Redis cache is unreachable")
	now := run(a, start, 62, func(int) []Reason { return []Reason{down} })
	if len(a.state.Queue) != 1 {
		t.Fatalf("queue = %+v", a.state.Queue)
	}
	a.Mute("redis:cache/down", 60, now)
	run(a, now, 40, func(int) []Reason { return []Reason{down} })
	if got := ch.log(); len(got) != 0 {
		t.Fatalf("muted problem still sent: %v", got)
	}
}

func TestFailingChannelBecomesAFinding(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	broken := &fakeChannel{name: "Email", level: levelWarn, err: errors.New("535 authentication failed")}
	a := newTestAlerter(t, start, broken)
	a.cfg.Grace = 0
	run(a, start, 100, func(int) []Reason { return []Reason{reason(levelCrit, "cpu/busy", "CPU is maxed out")} })
	f := a.Findings()
	if len(f) != 1 || f[0].Key != "alerts/failing:Email" || !strings.Contains(f[0].Text, "535 authentication failed") {
		t.Fatalf("findings = %+v", f)
	}
	if d := a.Status().Deliveries; len(d) != 1 || d[0].OK || d[0].Channel != "Email" {
		t.Fatalf("deliveries = %+v", d)
	}
}

func TestTelegramMessageAndErrors(t *testing.T) {
	var got map[string]any
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		if got["chat_id"] == "bad" {
			w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	tg := NewTelegramNotifier("123:SECRET", "42", levelWarn)
	tg.api = server.URL
	now := time.Now()
	n := Notification{Event: eventFiring, Host: "web-1", PublicURL: "http://mon", Time: now, Incidents: []Incident{{Key: "container:api/unhealthy", Ref: "container:api", Level: levelCrit, Text: "Container <api> is unhealthy", FirstSeen: now.Add(-3 * time.Minute).UnixMilli()}}}
	if err := tg.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	text := got["text"].(string)
	if path != "/bot123:SECRET/sendMessage" || got["parse_mode"] != "HTML" || !strings.Contains(text, "🔴 <b>web-1</b> · critical") || !strings.Contains(text, "Container &lt;api&gt; is unhealthy · critical for 3 minutes") || !strings.Contains(text, `<a href="http://mon/#/containers/api">open</a>`) {
		t.Fatalf("path %s payload %v", path, got)
	}
	test := telegramText(Notification{Event: eventTest, Host: "web-1", PublicURL: "http://mon", Open: []Incident{{}}})
	if test != "🧪 <b>web-1</b> · test\nAlerts from the monitor reach this chat.\n1 problem is open right now.\n<a href=\"http://mon/#/\">Open the dashboard</a>" {
		t.Fatalf("test message = %q", test)
	}
	tg.chat = "bad"
	if err := tg.Send(context.Background(), n); err == nil || err.Error() != "telegram: Bad Request: chat not found" {
		t.Fatalf("err = %v", err)
	}
	tg.api = "http://127.0.0.1:1"
	if err := tg.Send(context.Background(), n); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("network error leaks the token or is missing: %v", err)
	}
}

func fakeSMTP(t *testing.T) (addr string, messages chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	messages = make(chan string, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 fake ESMTP\r\n")
				var data strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
					case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
						fmt.Fprint(conn, "250-fake\r\n250 8BITMIME\r\n")
					case cmd == "DATA":
						fmt.Fprint(conn, "354 go ahead\r\n")
						for {
							l, err := r.ReadString('\n')
							if err != nil || l == ".\r\n" {
								break
							}
							data.WriteString(l)
						}
						messages <- data.String()
						fmt.Fprint(conn, "250 queued\r\n")
					case cmd == "QUIT":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "250 ok\r\n")
					}
				}
			}()
		}
	}()
	return listener.Addr().String(), messages
}

func TestEmailOverSMTP(t *testing.T) {
	addr, messages := fakeSMTP(t)
	if _, err := NewEmailNotifier("smtp://"+addr, "monitor@example.com", "", levelCrit); err == nil {
		t.Fatal("missing recipients accepted")
	}
	strict, _ := NewEmailNotifier("smtp://"+addr, "monitor@example.com", "ops@example.com", levelCrit)
	now := time.Now()
	n := Notification{Event: eventResolved, Host: "web-1", PublicURL: "http://mon", Time: now,
		Incidents: []Incident{{Key: "postgres:main/down", Ref: "postgres:main", Level: levelCrit, NotifiedLevel: levelCrit, Text: "Postgres main is unreachable", FirstSeen: now.Add(-7 * time.Minute).UnixMilli(), ResolvedAt: now.UnixMilli()}},
		Open:      []Incident{{Key: "storage/full:/", Level: levelWarn, Text: "Disk / is 88% full", FirstSeen: now.Add(-time.Hour).UnixMilli()}}}
	if err := strict.Send(context.Background(), n); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("plaintext relay accepted without opting in: %v", err)
	}
	relay, _ := NewEmailNotifier("smtp://"+addr+"?tls=none", "monitor@example.com", "ops@example.com, joseph@example.com", levelCrit)
	if err := relay.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	msg := <-messages
	subjectLine := "Subject: " + mime.QEncoding.Encode("utf-8", "[RESOLVED] web-1: Postgres main is unreachable (lasted 7 minutes)")
	for _, want := range []string{"To: ops@example.com, joseph@example.com\r\n", subjectLine + "\r\n", "Resolved:\r\n", "Postgres main is unreachable · lasted 7 minutes", "http://mon/#/services/postgres/main", "Also open:\r\n", "Disk / is 88% full · warning for 60 minutes", "http://mon/#/alerts"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q:\n%s", want, msg)
		}
	}
}

func TestHeartbeatReportsAfterRepeatedFailures(t *testing.T) {
	status := http.StatusOK
	pings := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		w.WriteHeader(status)
	}))
	defer server.Close()
	hb, err := NewHeartbeat(server.URL + "/ping/7f3c-secret")
	if err != nil {
		t.Fatal(err)
	}
	hb.ping(context.Background())
	status = http.StatusNotFound
	hb.ping(context.Background())
	hb.ping(context.Background())
	if hb.Problem() != "" {
		t.Fatal("reported after two failures")
	}
	hb.ping(context.Background())
	if p := hb.Problem(); pings != 4 || !strings.Contains(p, "answered HTTP 404") || strings.Contains(p, "secret") {
		t.Fatalf("problem = %q", p)
	}
	if _, err := NewHeartbeat("ftp://x"); err == nil {
		t.Fatal("ftp heartbeat accepted")
	}
}

func TestWriteActionsRejectCrossSiteRequests(t *testing.T) {
	called := false
	h := writeAction(func(*http.Request, actionBody) error { called = true; return nil })
	for _, tc := range []struct {
		header, site string
		want         int
	}{{"", "", http.StatusForbidden}, {"1", "cross-site", http.StatusForbidden}, {"1", "same-origin", http.StatusOK}} {
		r := httptest.NewRequest(http.MethodPost, "/api/alerts/maintenance", strings.NewReader(`{"minutes":30}`))
		if tc.header != "" {
			r.Header.Set("X-Monitor", tc.header)
		}
		if tc.site != "" {
			r.Header.Set("Sec-Fetch-Site", tc.site)
		}
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code != tc.want {
			t.Errorf("header %q site %q: %d, want %d", tc.header, tc.site, w.Code, tc.want)
		}
	}
	if !called {
		t.Fatal("same-origin request was not handled")
	}
}

func TestFindingKeysAreUnique(t *testing.T) {
	failed := false
	s := Snapshot{
		Mem: Memory{Total: 100, Used: 97}, Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 99}, {Mount: "/data", Total: 100, Used: 90, ReadOnly: true}},
		Containers: []Container{{Name: "a", Health: "unhealthy"}, {Name: "b", State: "exited", ExitCode: 1}},
		Postgres:   []PostgresStats{{Name: "x", Error: "unreachable"}, {Name: "y", Up: true, Connections: 99, MaxConnections: 100, LongestQuery: 600}},
		HAProxy:    []HAProxyStats{{Name: "h", Up: true, BackendsDown: []string{"one", "two"}}},
		Health:     HostHealth{Smart: &SmartReport{Disks: []DiskHealth{{Model: "A", Passed: &failed}, {Model: "B", Passed: &failed}}}},
	}
	seen := map[string]string{}
	for _, r := range assess(s, Averages{CPU: 99, ReadOps: 10, ReadLatency: 900}).Reasons {
		if r.Key == "" || seen[r.Key] != "" {
			t.Errorf("key %q for %q also used by %q", r.Key, r.Text, seen[r.Key])
		}
		seen[r.Key] = r.Text
	}
}
