package monitor

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func openTestMetricStore(t *testing.T, path, legacy string) *MetricStore {
	t.Helper()
	s, err := OpenMetricStore(path, legacy)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testHistories(t *testing.T, s *MetricStore, now time.Time) (*History, *ContainerHistory, *ServiceHistory) {
	t.Helper()
	h, c, v, err := restoreHistories(s, now)
	if err != nil {
		t.Fatal(err)
	}
	return h, c, v
}

func TestMetricStoreRestoresAllChartRangesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	s := openTestMetricStore(t, path, "")
	now := time.Now().Truncate(time.Minute)
	h, c, v := testHistories(t, s, now)
	start := now.Add(-3 * time.Minute)
	for i := range 36 {
		at := start.Add(time.Duration(i) * 5 * time.Second)
		h.Add(Point{T: at.UnixMilli(), CPU: float64(i), CPUMax: float64(i), Level: 2, Issue: "disk full"})
		c.Record(at, []Container{{Name: "web", State: "running", CPU: float64(i), Memory: 100}})
		v.Record("mysql:main", at, map[string]float64{"up": 1, "qps": float64(i)})
	}
	// Flush through the periodic path, then close the connection directly to
	// simulate losing the process without a graceful final history save.
	if err := s.Flush(now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestMetricStore(t, path, "")
	defer s.Close()
	restoredH, restoredC, restoredV := testHistories(t, s, now)
	for _, span := range []string{"5m", "15m", "30m", "1h", "3h", "6h", "12h", "24h"} {
		if got := restoredH.Range(span); len(got) == 0 || len(got) > chartPoints || got[0].Issue != "disk full" {
			t.Fatalf("host %s: %+v", span, got)
		}
		if got := restoredC.Range("web", span); len(got) == 0 || len(got) > chartPoints || got[0].Mem != 100 {
			t.Fatalf("container %s: %+v", span, got)
		}
		if got := restoredV.Range("mysql:main", span); len(got) == 0 || len(got) > chartPoints || got[0]["up"] != 1 {
			t.Fatalf("service %s: %+v", span, got)
		}
	}
	if !reflect.DeepEqual(h.Range("5m"), restoredH.Range("5m")) || !reflect.DeepEqual(c.Range("web", "5m"), restoredC.Range("web", "5m")) || !reflect.DeepEqual(v.Range("mysql:main", "5m"), restoredV.Range("mysql:main", "5m")) {
		t.Fatal("raw history changed after restart")
	}
	// The unfinished pre-restart minute is available as a summary, too.
	if len(restoredH.minutes) != 3 || restoredH.minutes[2].CPU != 29.5 || restoredH.minutes[2].CPUMax != 35 {
		t.Fatalf("restored rollups: %+v", restoredH.minutes)
	}
}

func TestMetricStoreContinuesUnfinishedMinuteWithoutDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	now := time.Now().Truncate(time.Minute).Add(30 * time.Second)
	s := openTestMetricStore(t, path, "")
	h, c, v := testHistories(t, s, now)
	at := now.Add(-10 * time.Second)
	h.Add(Point{T: at.UnixMilli(), CPU: 10, CPUMax: 10})
	c.Record(at, []Container{{Name: "web", State: "running", CPU: 10}})
	v.Record("check:api", at, map[string]float64{"up": 0, "latency": 10})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestMetricStore(t, path, "")
	h, c, v = testHistories(t, s, now)
	h.Add(Point{T: now.UnixMilli(), CPU: 30, CPUMax: 30})
	c.Record(now, []Container{{Name: "web", State: "running", CPU: 30}})
	v.Record("check:api", now, map[string]float64{"up": 1})
	next := now.Add(time.Minute)
	h.Add(Point{T: next.UnixMilli(), CPU: 90, CPUMax: 90})
	c.Record(next, []Container{{Name: "web", State: "running", CPU: 90}})
	v.Record("check:api", next, map[string]float64{"up": 1})
	if len(h.minutes) != 1 || h.minutes[0].CPU != 20 || h.minutes[0].CPUMax != 30 {
		t.Fatalf("host: %+v", h.minutes)
	}
	if got := c.series["web"].minutes; len(got) != 1 || got[0].CPU != 20 {
		t.Fatalf("container: %+v", got)
	}
	if got := v.series["check:api"].minutes; len(got) != 1 || got[0]["up"] != .5 || got[0]["latency"] != 10 {
		t.Fatalf("service: %+v", got)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTestMetricStore(t, path, "")
	defer s.Close()
	h, c, v = testHistories(t, s, next)
	if len(h.minutes) != 1 || h.minutes[0].CPU != 20 || len(c.series["web"].minutes) != 1 || len(v.series["check:api"].minutes) != 1 {
		t.Fatal("minute duplicated after second restart")
	}
}

func TestMetricStoreImportsLegacyHistoryOnce(t *testing.T) {
	dir := t.TempDir()
	path, legacy := filepath.Join(dir, "monitor.db"), filepath.Join(dir, "history.json")
	now := time.Now().Truncate(time.Minute)
	old := Point{T: now.Add(-2 * time.Minute).UnixMilli(), CPU: 42}
	b, err := json.Marshal([]Point{{T: now.Add(-25 * time.Hour).UnixMilli(), CPU: 100}, old})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := openTestMetricStore(t, path, legacy)
	h, _, _ := testHistories(t, s, now)
	if len(h.minutes) != 1 || h.minutes[0] != old {
		t.Fatalf("import: %+v", h.minutes)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Changes to the rollback file must never be imported a second time.
	if err := os.WriteFile(legacy, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	s = openTestMetricStore(t, path, legacy)
	defer s.Close()
	h, _, _ = testHistories(t, s, now)
	if len(h.minutes) != 1 || h.minutes[0] != old {
		t.Fatalf("second import: %+v", h.minutes)
	}
}

func TestMetricStoreExpiresBothTiers(t *testing.T) {
	s := openTestMetricStore(t, filepath.Join(t.TempDir(), "monitor.db"), "")
	defer s.Close()
	now := time.Now()
	for _, kind := range []string{"host", "container", "service"} {
		for resolution, retention := range []time.Duration{detailRetention, minuteRetention} {
			for _, age := range []time.Duration{retention + time.Minute, retention - time.Minute} {
				if _, err := s.db.Exec("INSERT INTO metrics VALUES (?, '', ?, ?, '{}')", kind, resolution, now.Add(-age).UnixMilli()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	s.prunedAt = time.Time{}
	if err := s.Flush(now); err != nil {
		t.Fatal(err)
	}
	for resolution := range 2 {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM metrics WHERE resolution = ?", resolution).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 3 {
			t.Fatalf("resolution %d: %d rows", resolution, count)
		}
	}
	if err := s.Flush(now.Add(25 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM metrics").Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired rows: %d, %v", count, err)
	}
}

func TestMetricStoreRollsBackAndRetriesFailedBatch(t *testing.T) {
	s := openTestMetricStore(t, filepath.Join(t.TempDir(), "monitor.db"), "")
	defer s.Close()
	now := time.Now().Truncate(time.Minute).Add(10 * time.Second)
	h, _, v := testHistories(t, s, now)
	h.Add(Point{T: now.UnixMilli(), CPU: 10})
	v.Record("check:api", now, map[string]float64{"up": 0})
	if _, err := s.db.Exec(`CREATE TRIGGER fail_metrics BEFORE INSERT ON metrics WHEN NEW.kind = 'service' BEGIN SELECT RAISE(ABORT, 'simulated write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(now); err == nil {
		t.Fatal("write failure ignored")
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM metrics").Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial batch committed: %d, %v", count, err)
	}
	// A newer version of the minute replaces the one being retried.
	v.Record("check:api", now.Add(time.Second), map[string]float64{"up": 1})
	if _, err := s.db.Exec("DROP TRIGGER fail_metrics"); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(now); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT count(*) FROM metrics").Scan(&count); err != nil || count != 5 {
		t.Fatalf("retry: %d rows, %v", count, err)
	}
	var b []byte
	if err := s.db.QueryRow("SELECT value FROM metrics WHERE kind = 'service' AND resolution = 1").Scan(&b); err != nil {
		t.Fatal(err)
	}
	var p ServicePoint
	if err := json.Unmarshal(b, &p); err != nil || p["up"] != .5 {
		t.Fatalf("new minute lost: %v, %v", p, err)
	}
}

func TestMetricStoreConcurrentRecordingAndFlushing(t *testing.T) {
	s := openTestMetricStore(t, filepath.Join(t.TempDir(), "monitor.db"), "")
	defer s.Close()
	now := time.Now().Truncate(time.Minute)
	h, c, v := testHistories(t, s, now)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 50 {
			h.Add(Point{T: now.Add(time.Duration(i) * time.Millisecond).UnixMilli(), CPU: 10})
		}
	})
	wg.Go(func() {
		for i := range 50 {
			c.Record(now.Add(time.Duration(i)*time.Millisecond), []Container{{Name: "web", State: "running", CPU: 20}})
		}
	})
	wg.Go(func() {
		for i := range 50 {
			v.Record("check:api", now.Add(time.Duration(i)*time.Millisecond), map[string]float64{"up": 1})
		}
	})
	wg.Go(func() {
		for range 10 {
			if err := s.Flush(now); err != nil {
				t.Error(err)
			}
		}
	})
	wg.Wait()
	if err := s.Flush(now); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.db.QueryRow("SELECT count(*) FROM metrics WHERE resolution = 0").Scan(&count); err != nil || count != 150 {
		t.Fatalf("concurrent writes: %d rows, %v", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("writer did not stop")
	}
}

func TestMetricStoreRequiresWritableDatabase(t *testing.T) {
	if s, err := OpenMetricStore(filepath.Join(t.TempDir(), "missing", "monitor.db"), ""); err == nil {
		s.Close()
		t.Fatal("unwritable metrics path silently accepted")
	}
}

func TestServeShutdownFlushesCollectorsBeforeClosingDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.db")
	for key, value := range map[string]string{
		"METRICS_DB": path, "HISTORY_FILE": filepath.Join(dir, "history.json"),
		"ALERTS_FILE": filepath.Join(dir, "alerts.json"), "SMART_FILE": filepath.Join(dir, "smart.json"),
		"HOST_PROC": dir, "HOST_SYS": dir, "HOST_ROOT": dir, "ADDR": "127.0.0.1:0",
		"DOCKER_HOST": "", "SYSTEMD_URL": "", "HEARTBEAT_URL": "",
		"POSTGRES_URL": "", "MYSQL_URL": "", "MONGODB_URL": "", "REDIS_URL": "", "MEMCACHED_URL": "",
		"NGINX_URL": "", "CADDY_URL": "", "TRAEFIK_URL": "", "HAPROXY_URL": "", "APACHE_URL": "", "RABBITMQ_URL": "",
		"CHECKS": "api=http://127.0.0.1:1",
	} {
		t.Setenv(key, value)
	}
	// Cancellation races the first collection. Even collectors that finish
	// after the server stops must land in the final transaction.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
	s := openTestMetricStore(t, path, "")
	defer s.Close()
	for _, kind := range []string{"host", "service"} {
		var count int
		if err := s.db.QueryRow("SELECT count(*) FROM metrics WHERE kind = ? AND resolution = 0", kind).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s final flush: %d samples, %v", kind, count, err)
		}
	}
}
