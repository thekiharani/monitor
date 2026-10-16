package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestDockerProxyAllowsOnlyContainerListing(t *testing.T) {
	for path, allowed := range map[string]bool{
		"/containers/json":           true,
		"/v1.47/containers/json":     true,
		"/_ping":                     true,
		"/containers/abc/json":       false,
		"/containers/create":         false,
		"/v1.47/containers/abc/exec": false,
		"/images/json":               false,
	} {
		if got := allowedDockerPaths.MatchString(path); got != allowed {
			t.Errorf("%s allowed = %v, want %v", path, got, allowed)
		}
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
	s := Snapshot{System: System{Mem: Memory{Total: 100, Used: 10}, Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 5, InodesTotal: 100, InodesUsed: 1}}}}
	if v := assess(s, Averages{CPU: 3}); v.Level != levelOK || len(v.Reasons) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestVerdictEscalatesToTheWorstProblem(t *testing.T) {
	failed := false
	s := Snapshot{
		System: System{
			Mem:         Memory{Total: 100, Used: 90},
			Filesystems: []Filesystem{{Mount: "/", Total: 100, Used: 96}},
		},
		Containers: []Container{{Name: "api", State: "running", Health: "unhealthy"}, {Name: "job", State: "exited", ExitCode: 0}},
		Postgres:   []PostgresStats{{Name: "db", Error: "unreachable"}},
		Health:     HostHealth{RebootRequired: true, Smart: &SmartReport{Disks: []DiskHealth{{Model: "X", Passed: &failed}}}},
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
	s := Snapshot{System: System{Filesystems: []Filesystem{
		{Mount: "/", Total: 100, ReadOnly: true},
		{Mount: "/data", Total: 100, Errors: 2, LastErrorAt: time.Now().Add(-3 * time.Hour).Unix()},
		{Mount: "/old", Total: 100, Errors: 1, LastErrorAt: time.Now().Add(-10 * 24 * time.Hour).Unix()},
	}}}
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
