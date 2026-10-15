package main

import (
	"bufio"
	"context"
	"encoding/json"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

type Container struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Image     string  `json:"image"`
	State     string  `json:"state"`
	Status    string  `json:"status"`
	Health    string  `json:"health"`
	ExitCode  int     `json:"exitCode"`
	CPU       float64 `json:"cpu"`
	Memory    uint64  `json:"memory"`
	MemLimit  uint64  `json:"memLimit"`
	ReadBps   float64 `json:"readBps"`
	WriteBps  float64 `json:"writeBps"`
	StartedAt int64   `json:"startedAt"`
}

type ContainerCollector struct {
	client  *http.Client
	baseURL string
	sys     string
	at      time.Time
	usage   map[string]cgroupCounters
}

type cgroupCounters struct{ cpuUsec, readBytes, writeBytes uint64 }

func NewContainerCollector(dockerHost, sys string) *ContainerCollector {
	if dockerHost == "" {
		return nil
	}
	return &ContainerCollector{
		client:  &http.Client{Timeout: 3 * time.Second},
		baseURL: strings.TrimRight(strings.Replace(dockerHost, "tcp://", "http://", 1), "/"),
		sys:     sys,
	}
}

var exitCodePattern = regexp.MustCompile(`^Exited \((-?\d+)\)`)

func (c *ContainerCollector) Collect(ctx context.Context) ([]Container, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/containers/json?all=1", nil)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker proxy: %s", resp.Status)
	}
	var listed []struct {
		ID      string   `json:"Id"`
		Names   []string `json:"Names"`
		Image   string   `json:"Image"`
		State   string   `json:"State"`
		Status  string   `json:"Status"`
		Created int64    `json:"Created"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return nil, err
	}

	now := time.Now()
	elapsed := now.Sub(c.at).Seconds()
	first := c.at.IsZero()
	c.at = now
	next := map[string]cgroupCounters{}

	out := make([]Container, 0, len(listed))
	for _, l := range listed {
		ct := Container{
			ID:     l.ID[:min(12, len(l.ID))],
			Image:  l.Image,
			State:  l.State,
			Status: l.Status,
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
			next[l.ID] = counters
			ct.Memory = parseUint(firstLine(filepath.Join(dir, "memory.current")))
			ct.MemLimit = parseUint(firstLine(filepath.Join(dir, "memory.max")))
			if prev, ok := c.usage[l.ID]; ok && !first && elapsed > 0 {
				ct.CPU = rate(counters.cpuUsec, prev.cpuUsec, elapsed) / 1e4
				ct.ReadBps = rate(counters.readBytes, prev.readBytes, elapsed)
				ct.WriteBps = rate(counters.writeBytes, prev.writeBytes, elapsed)
			}
		}
		out = append(out, ct)
	}
	c.usage = next
	return out, nil
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
		for _, field := range strings.Fields(line) {
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

var allowedDockerPaths = regexp.MustCompile(`^(/v[0-9.]+)?/(_ping|version|containers/json)$`)

func runDockerProxy(addr, socket string) error {
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
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !allowedDockerPaths.MatchString(r.URL.Path) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	})
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

type Database struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type PostgresStats struct {
	Up             bool       `json:"up"`
	Error          string     `json:"error,omitempty"`
	Version        string     `json:"version"`
	Uptime         float64    `json:"uptime"`
	Connections    int        `json:"connections"`
	Active         int        `json:"active"`
	MaxConnections int        `json:"maxConnections"`
	LongestQuery   float64    `json:"longestQuery"`
	TPS            float64    `json:"tps"`
	CacheHit       float64    `json:"cacheHit"`
	Databases      []Database `json:"databases"`
}

type PostgresCollector struct {
	url  string
	conn *pgx.Conn
	at   time.Time
	xact int64
}

func NewPostgresCollector(url string) *PostgresCollector {
	if url == "" {
		return nil
	}
	return &PostgresCollector{url: url}
}

func (c *PostgresCollector) Collect(ctx context.Context) PostgresStats {
	stats, err := c.query(ctx)
	if err != nil {
		if c.conn != nil {
			c.conn.Close(context.Background())
			c.conn = nil
		}
		return PostgresStats{Error: friendlyError(err)}
	}
	stats.Up = true
	return stats
}

func (c *PostgresCollector) query(ctx context.Context) (PostgresStats, error) {
	var s PostgresStats
	if c.conn == nil {
		conn, err := pgx.Connect(ctx, c.url)
		if err != nil {
			return s, err
		}
		c.conn = conn
	}
	err := c.conn.QueryRow(ctx, `
		select current_setting('server_version'),
		       extract(epoch from now() - pg_postmaster_start_time())::float8,
		       current_setting('max_connections')::int,
		       count(*) filter (where backend_type = 'client backend')::int,
		       count(*) filter (where backend_type = 'client backend' and state = 'active' and pid <> pg_backend_pid())::int,
		       coalesce(max(extract(epoch from now() - query_start)) filter (where backend_type = 'client backend' and state = 'active' and pid <> pg_backend_pid()), 0)::float8
		from pg_stat_activity`).Scan(&s.Version, &s.Uptime, &s.MaxConnections, &s.Connections, &s.Active, &s.LongestQuery)
	if err != nil {
		return s, err
	}

	var xact, hit, read int64
	err = c.conn.QueryRow(ctx, `
		select coalesce(sum(xact_commit + xact_rollback), 0)::bigint,
		       coalesce(sum(blks_hit), 0)::bigint,
		       coalesce(sum(blks_read), 0)::bigint
		from pg_stat_database`).Scan(&xact, &hit, &read)
	if err != nil {
		return s, err
	}
	now := time.Now()
	if !c.at.IsZero() && xact >= c.xact {
		s.TPS = float64(xact-c.xact) / now.Sub(c.at).Seconds()
	}
	c.at, c.xact = now, xact
	if hit+read > 0 {
		s.CacheHit = 100 * float64(hit) / float64(hit+read)
	}

	rows, err := c.conn.Query(ctx, `
		select datname, pg_database_size(datname)
		from pg_database
		where not datistemplate and datallowconn
		order by 2 desc
		limit 5`)
	if err != nil {
		return s, err
	}
	s.Databases, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Database, error) {
		var d Database
		return d, row.Scan(&d.Name, &d.Size)
	})
	return s, err
}

type RedisStats struct {
	Up        bool    `json:"up"`
	Error     string  `json:"error,omitempty"`
	Version   string  `json:"version"`
	Uptime    float64 `json:"uptime"`
	Clients   int     `json:"clients"`
	Memory    uint64  `json:"memory"`
	MaxMemory uint64  `json:"maxMemory"`
	OpsPerSec float64 `json:"opsPerSec"`
	HitRate   float64 `json:"hitRate"`
	Keys      int64   `json:"keys"`
	Evicted   int64   `json:"evicted"`
}

type RedisCollector struct {
	addr, username, password string
}

func NewRedisCollector(raw string) *RedisCollector {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return &RedisCollector{addr: raw}
	}
	c := &RedisCollector{addr: u.Host}
	if u.User != nil {
		c.username = u.User.Username()
		c.password, _ = u.User.Password()
	}
	if !strings.Contains(c.addr, ":") {
		c.addr += ":6379"
	}
	return c
}

func (c *RedisCollector) Collect(ctx context.Context) RedisStats {
	info, err := c.info(ctx)
	if err != nil {
		return RedisStats{Error: friendlyError(err)}
	}
	n := func(key string) int64 { v, _ := strconv.ParseInt(info[key], 10, 64); return v }
	s := RedisStats{
		Up:        true,
		Version:   info["redis_version"],
		Uptime:    float64(n("uptime_in_seconds")),
		Clients:   int(n("connected_clients")),
		Memory:    uint64(n("used_memory")),
		MaxMemory: uint64(n("maxmemory")),
		Evicted:   n("evicted_keys"),
	}
	s.OpsPerSec, _ = strconv.ParseFloat(info["instantaneous_ops_per_sec"], 64)
	if hits, misses := n("keyspace_hits"), n("keyspace_misses"); hits+misses > 0 {
		s.HitRate = 100 * float64(hits) / float64(hits+misses)
	}
	for key, value := range info {
		if !strings.HasPrefix(key, "db") {
			continue
		}
		for _, part := range strings.Split(value, ",") {
			if v, ok := strings.CutPrefix(part, "keys="); ok {
				k, _ := strconv.ParseInt(v, 10, 64)
				s.Keys += k
			}
		}
	}
	return s
}

func (c *RedisCollector) info(ctx context.Context) (map[string]string, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	r := bufio.NewReader(conn)

	if c.password != "" {
		args := []string{"AUTH", c.password}
		if c.username != "" {
			args = []string{"AUTH", c.username, c.password}
		}
		if _, err := redisCommand(conn, r, args...); err != nil {
			return nil, err
		}
	}
	reply, err := redisCommand(conn, r, "INFO")
	if err != nil {
		return nil, err
	}
	info := map[string]string{}
	for line := range strings.SplitSeq(reply, "\r\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && !strings.HasPrefix(line, "#") {
			info[key] = value
		}
	}
	return info, nil
}

func redisCommand(conn net.Conn, r *bufio.Reader, args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return "", err
	}
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return "", errors.New(line[1:])
	case '$':
		size, err := strconv.Atoi(line[1:])
		if err != nil || size < 0 {
			return "", errors.New("redis: bad reply")
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return "", err
		}
		return string(buf[:size]), nil
	}
	return "", fmt.Errorf("redis: unexpected reply %q", line)
}

func friendlyError(err error) string {
	var netErr net.Error
	switch {
	case errors.As(err, &netErr) && netErr.Timeout():
		return "not responding"
	case strings.Contains(err.Error(), "password authentication failed"), strings.Contains(err.Error(), "WRONGPASS"), strings.Contains(err.Error(), "NOAUTH"):
		return "login rejected"
	case strings.Contains(err.Error(), "connection refused"), strings.Contains(err.Error(), "no such host"):
		return "unreachable"
	}
	return err.Error()
}

type poller[T any] struct {
	mu    sync.RWMutex
	value T
}

func (p *poller[T]) Get() T {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.value
}

func (p *poller[T]) Run(ctx context.Context, every time.Duration, fn func(context.Context) T) {
	tick := func() {
		callCtx, cancel := context.WithTimeout(ctx, min(every, 10*time.Second))
		defer cancel()
		v := fn(callCtx)
		p.mu.Lock()
		p.value = v
		p.mu.Unlock()
	}
	tick()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}
