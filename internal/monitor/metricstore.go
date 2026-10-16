package monitor

import (
	"context"
	"database/sql"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	detailRetention     = 30 * time.Minute
	minuteRetention     = 24 * time.Hour
	metricFlushInterval = 5 * time.Second
	maxPendingMetrics   = 65536
)

type metricKey struct {
	kind, name string
	resolution int
	t          int64
}

// MetricStore batches incremental writes. Charts continue to use the bounded
// in-memory histories, so neither chart requests nor collectors wait on disk.
type MetricStore struct {
	db       *sql.DB
	mu       sync.Mutex
	pending  map[metricKey][]byte
	overflow bool
	flushMu  sync.Mutex
	prunedAt time.Time
}

func OpenMetricStore(path, legacyPath string) (*MetricStore, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "cache_size(-4096)", "wal_autocheckpoint(256)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &MetricStore{db: db, pending: map[metricKey][]byte{}}
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS metrics (
  kind TEXT NOT NULL, name TEXT NOT NULL, resolution INTEGER NOT NULL,
  t INTEGER NOT NULL, value BLOB NOT NULL,
  PRIMARY KEY (kind, name, resolution, t)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS metrics_expiry ON metrics(resolution, t);
CREATE TABLE IF NOT EXISTS metric_metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL) WITHOUT ROWID;
`)
	if err == nil {
		err = s.importLegacy(legacyPath)
	}
	if err == nil {
		err = s.Flush(time.Now())
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open metrics database: %w", err)
	}
	return s, nil
}

// Import and the marker commit together. The JSON file remains untouched for
// rollback, and its old values never replace samples already in SQLite.
func (s *MetricStore) importLegacy(path string) error {
	var done string
	err := s.db.QueryRow("SELECT value FROM metric_metadata WHERE key = 'legacy_host_import'").Scan(&done)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var points []Point
	if path != "" {
		b, err := os.ReadFile(path)
		if err == nil {
			if err = json.Unmarshal(b, &points); err != nil {
				return fmt.Errorf("import %s: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := time.Now().Add(-minuteRetention).UnixMilli()
	for _, p := range points {
		if p.T < cutoff {
			continue
		}
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT OR IGNORE INTO metrics VALUES ('host', '', 1, ?, ?)", p.T/60000*60000, b); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("INSERT INTO metric_metadata VALUES ('legacy_host_import', '1')"); err != nil {
		return err
	}
	return tx.Commit()
}

func recordMetric[T any](s *MetricStore, kind, name string, t int64, point, minute T) {
	if s == nil {
		return
	}
	raw, err := json.Marshal(point)
	if err != nil {
		slog.Warn("metric not encoded", "kind", kind, "err", err)
		return
	}
	rollup, err := json.Marshal(minute)
	if err != nil {
		slog.Warn("metric not encoded", "kind", kind, "err", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueue(metricKey{kind, name, 0, t}, raw)
	// Replacing a minute in this batch also saves the unfinished minute. A
	// restart in that minute rebuilds its accumulator from the raw samples.
	s.enqueue(metricKey{kind, name, 1, t / 60000 * 60000}, rollup)
}

func (s *MetricStore) enqueue(k metricKey, value []byte) {
	if _, exists := s.pending[k]; !exists && len(s.pending) >= maxPendingMetrics {
		if !s.overflow {
			slog.Warn("metrics write backlog full; some samples will be lost")
		}
		s.overflow = true
		return
	}
	s.pending[k] = value
}

func (s *MetricStore) Run(ctx context.Context) {
	ticker := time.NewTicker(metricFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.Flush(now); err != nil {
				slog.Warn("metrics not saved", "err", err)
			}
		}
	}
}

func metricExpired(k metricKey, now time.Time) bool {
	retention := detailRetention
	if k.resolution == 1 {
		retention = minuteRetention
	}
	return k.t < now.Add(-retention).UnixMilli()
}

func (s *MetricStore) Flush(now time.Time) (err error) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.mu.Lock()
	batch := s.pending
	s.pending = map[metricKey][]byte{}
	s.mu.Unlock()
	// A failed transaction keeps its samples for the next attempt. Newer
	// versions of an unfinished minute take precedence over the failed batch.
	defer func() {
		if err == nil {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for k, v := range batch {
			if _, newer := s.pending[k]; !newer && !metricExpired(k, now) {
				s.enqueue(k, v)
			}
		}
	}()
	prune := now.Sub(s.prunedAt) >= time.Minute
	if len(batch) == 0 && !prune {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(batch) > 0 {
		stmt, err := tx.PrepareContext(ctx, "INSERT INTO metrics VALUES (?, ?, ?, ?, ?) ON CONFLICT(kind, name, resolution, t) DO UPDATE SET value = excluded.value")
		if err != nil {
			return err
		}
		defer stmt.Close()
		for k, value := range batch {
			if metricExpired(k, now) {
				continue
			}
			if _, err := stmt.ExecContext(ctx, k.kind, k.name, k.resolution, k.t, value); err != nil {
				return err
			}
		}
	}
	if prune {
		for resolution, retention := range []time.Duration{detailRetention, minuteRetention} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM metrics WHERE resolution = ? AND t < ?", resolution, now.Add(-retention).UnixMilli()); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if prune {
		s.prunedAt = now
	}
	s.mu.Lock()
	s.overflow = false
	s.mu.Unlock()
	return nil
}

func (s *MetricStore) Close() error {
	err := s.Flush(time.Now())
	return errors.Join(err, s.db.Close())
}

type restoredSeries[T any] struct {
	recent, minutes, pending []T
	minute                   int64
}

func restoreMetricSeries[T any](s *MetricStore, kind string, stamp func(T) int64, now time.Time) (map[string]*restoredSeries[T], error) {
	rows, err := s.db.Query(`SELECT name, resolution, value FROM metrics WHERE kind = ?
AND ((resolution = 0 AND t >= ?) OR (resolution = 1 AND t >= ?)) ORDER BY name, resolution, t`, kind, now.Add(-detailRetention).UnixMilli(), now.Add(-minuteRetention).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	series := map[string]*restoredSeries[T]{}
	for rows.Next() {
		var name string
		var resolution int
		var b []byte
		if err := rows.Scan(&name, &resolution, &b); err != nil {
			return nil, err
		}
		var p T
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("restore %s/%s: %w", kind, name, err)
		}
		v := series[name]
		if v == nil {
			v = &restoredSeries[T]{}
			series[name] = v
		}
		if resolution == 0 {
			v.recent = append(v.recent, p)
		} else {
			v.minutes = append(v.minutes, p)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, v := range series {
		minute := now.UnixMilli() / 60000
		for _, p := range v.recent {
			if stamp(p)/60000 == minute {
				v.pending = append(v.pending, p)
			}
		}
		if len(v.pending) > 0 {
			v.minute = minute
			if n := len(v.minutes); n > 0 && stamp(v.minutes[n-1])/60000 == minute {
				v.minutes = v.minutes[:n-1]
			}
		}
	}
	return series, nil
}

func restoreHistories(s *MetricStore, now time.Time) (*History, *ContainerHistory, *ServiceHistory, error) {
	h := NewHistory()
	c := NewContainerHistory()
	v := NewServiceHistory()
	host, err := restoreMetricSeries(s, "host", func(p Point) int64 { return p.T }, now)
	if err != nil {
		return nil, nil, nil, err
	}
	if p := host[""]; p != nil {
		h.seconds, h.minutes, h.pending, h.minute = p.recent, p.minutes, p.pending, p.minute
	}
	containers, err := restoreMetricSeries(s, "container", func(p ContainerPoint) int64 { return p.T }, now)
	if err != nil {
		return nil, nil, nil, err
	}
	for name, p := range containers {
		var seen int64
		if n := len(p.minutes); n > 0 {
			seen = p.minutes[n-1].T
		}
		if n := len(p.recent); n > 0 {
			seen = p.recent[n-1].T
		}
		c.series[name] = &containerSeries{recent: p.recent, minutes: p.minutes, pending: p.pending, minute: p.minute, seen: seen}
	}
	services, err := restoreMetricSeries(s, "service", func(p ServicePoint) int64 { return int64(p["t"]) }, now)
	if err != nil {
		return nil, nil, nil, err
	}
	for name, p := range services {
		v.series[name] = &serviceSeries{recent: p.recent, minutes: p.minutes, pending: p.pending, minute: p.minute}
	}
	h.store, c.store, v.store = s, s, s
	slog.Info("metrics restored", "host_minutes", len(h.minutes), "containers", len(c.series), "services", len(v.series))
	return h, c, v, nil
}
