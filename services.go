package main

import (
	"bufio"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/godbus/dbus/v5"
	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Database struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type PostgresStats struct {
	Name           string     `json:"name"`
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
	name string
	url  string
	conn *pgx.Conn
	at   time.Time
	xact int64
}

func NewPostgresCollector(t Target) Collector[PostgresStats] {
	return &PostgresCollector{name: t.Name, url: t.URL}
}

func (c *PostgresCollector) Collect(ctx context.Context) PostgresStats {
	stats, err := c.query(ctx)
	if err != nil {
		if c.conn != nil {
			c.conn.Close(context.Background())
			c.conn = nil
		}
		return PostgresStats{Name: c.name, Error: friendlyError(err)}
	}
	stats.Name, stats.Up = c.name, true
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
	Name      string  `json:"name"`
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
	name, addr, username, password string
}

func NewRedisCollector(t Target) Collector[RedisStats] {
	c := &RedisCollector{name: t.Name, addr: hostPort(t.URL, "6379")}
	if u, err := url.Parse(t.URL); err == nil && u.User != nil {
		c.username = u.User.Username()
		c.password, _ = u.User.Password()
	}
	return c
}

func (c *RedisCollector) Collect(ctx context.Context) RedisStats {
	info, err := c.info(ctx)
	if err != nil {
		return RedisStats{Name: c.name, Error: friendlyError(err)}
	}
	n := func(key string) int64 { v, _ := strconv.ParseInt(info[key], 10, 64); return v }
	s := RedisStats{
		Name:      c.name,
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
	msg := err.Error()
	switch {
	case errors.As(err, &netErr) && netErr.Timeout(), errors.Is(err, context.DeadlineExceeded), strings.Contains(msg, "server selection timeout"):
		return "not responding"
	case strings.Contains(msg, "password authentication failed"), strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "NOAUTH"),
		strings.Contains(msg, "Access denied"), strings.Contains(msg, "AuthenticationFailed"), strings.Contains(msg, "HTTP 401"):
		return "rejecting the login"
	case strings.Contains(msg, "connection refused"), strings.Contains(msg, "no such host"), strings.Contains(msg, "no such file or directory"):
		return "unreachable"
	case strings.Contains(msg, "x509:"):
		return "presenting an invalid certificate"
	}
	return msg
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

type MySQLStats struct {
	Name           string     `json:"name"`
	Up             bool       `json:"up"`
	Error          string     `json:"error,omitempty"`
	Flavor         string     `json:"flavor"`
	Version        string     `json:"version"`
	Uptime         float64    `json:"uptime"`
	Connections    int        `json:"connections"`
	MaxConnections int        `json:"maxConnections"`
	Running        int        `json:"running"`
	LongestQuery   float64    `json:"longestQuery"`
	QPS            float64    `json:"qps"`
	SlowQueries    int64      `json:"slowQueries"`
	BufferPoolHit  float64    `json:"bufferPoolHit"`
	Replica        *Replica   `json:"replica"`
	Databases      []Database `json:"databases"`
}

type Replica struct {
	IORunning  bool     `json:"ioRunning"`
	SQLRunning bool     `json:"sqlRunning"`
	Lag        *float64 `json:"lag"`
	Error      string   `json:"error,omitempty"`
}

type MySQLCollector struct {
	name      string
	flavor    string
	db        *sql.DB
	err       error
	at        time.Time
	questions int64
	sizesAt   time.Time
	sizes     []Database
}

func NewMySQLCollector(t Target) Collector[MySQLStats] {
	c := &MySQLCollector{name: t.Name, flavor: "MySQL"}
	if strings.HasPrefix(t.URL, "mariadb://") {
		c.flavor = "MariaDB"
	}
	cfg, err := mysqlConfig(t.URL)
	if err != nil {
		c.err = err
		return c
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		c.err = err
		return c
	}
	c.db = sql.OpenDB(connector)
	c.db.SetMaxOpenConns(1)
	c.db.SetMaxIdleConns(1)
	return c
}

func mysqlConfig(raw string) (*mysql.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "mysql" && u.Scheme != "mariadb") {
		return nil, errors.New("invalid URL, expected mysql://user:password@host:3306")
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = hostPort(raw, "3306")
	cfg.Timeout = 5 * time.Second
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	cfg.TLSConfig = u.Query().Get("tls")
	return cfg, nil
}

func (c *MySQLCollector) Collect(ctx context.Context) MySQLStats {
	if c.err != nil {
		return MySQLStats{Name: c.name, Flavor: c.flavor, Error: c.err.Error()}
	}
	s, err := c.query(ctx)
	if err != nil {
		return MySQLStats{Name: c.name, Flavor: c.flavor, Error: friendlyError(err)}
	}
	s.Name, s.Up = c.name, true
	return s
}

func (c *MySQLCollector) query(ctx context.Context) (MySQLStats, error) {
	var s MySQLStats
	var comment string
	if err := c.db.QueryRowContext(ctx, "select @@version, @@version_comment, @@max_connections").Scan(&s.Version, &comment, &s.MaxConnections); err != nil {
		return s, err
	}
	if strings.Contains(strings.ToLower(s.Version+comment), "mariadb") {
		c.flavor = "MariaDB"
	}
	s.Flavor = c.flavor
	s.Version, _, _ = strings.Cut(s.Version, "-")

	status, err := queryPairs(ctx, c.db, "show global status")
	if err != nil {
		return s, err
	}
	n := func(key string) int64 { v, _ := strconv.ParseInt(status[key], 10, 64); return v }
	s.Uptime = float64(n("Uptime"))
	s.Connections = int(n("Threads_connected"))
	s.SlowQueries = n("Slow_queries")
	if requests, misses := n("Innodb_buffer_pool_read_requests"), n("Innodb_buffer_pool_reads"); requests > misses {
		s.BufferPoolHit = 100 * float64(requests-misses) / float64(requests)
	}
	now, questions := time.Now(), n("Questions")
	if !c.at.IsZero() && questions >= c.questions {
		s.QPS = float64(questions-c.questions) / now.Sub(c.at).Seconds()
	}
	c.at, c.questions = now, questions

	var longest sql.NullFloat64
	c.db.QueryRowContext(ctx, `
		select count(*), max(time) from information_schema.processlist
		where command not in ('Sleep', 'Daemon', 'Binlog Dump', 'Binlog Dump GTID')
		  and user not in ('system user', 'event_scheduler')
		  and id <> connection_id()`).Scan(&s.Running, &longest)
	s.LongestQuery = longest.Float64
	s.Replica = c.replica(ctx)

	if time.Since(c.sizesAt) >= time.Minute {
		c.sizesAt, c.sizes = time.Now(), c.largest(ctx)
	}
	s.Databases = c.sizes
	return s, nil
}

var replicaColumns = strings.NewReplacer("Replica", "Slave", "Source", "Master")

func (c *MySQLCollector) replica(ctx context.Context) *Replica {
	rows, err := c.db.QueryContext(ctx, "show replica status")
	if err != nil {
		if rows, err = c.db.QueryContext(ctx, "show slave status"); err != nil {
			return nil
		}
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || !rows.Next() {
		return nil
	}
	values := make([]sql.NullString, len(columns))
	targets := make([]any, len(columns))
	for i := range values {
		targets[i] = &values[i]
	}
	if rows.Scan(targets...) != nil {
		return nil
	}
	field := map[string]sql.NullString{}
	for i, column := range columns {
		field[replicaColumns.Replace(column)] = values[i]
	}
	r := &Replica{
		IORunning:  field["Slave_IO_Running"].String == "Yes",
		SQLRunning: field["Slave_SQL_Running"].String == "Yes",
		Error:      cmp.Or(field["Last_SQL_Error"].String, field["Last_IO_Error"].String),
	}
	if lag := field["Seconds_Behind_Master"]; lag.Valid {
		v, _ := strconv.ParseFloat(lag.String, 64)
		r.Lag = &v
	}
	return r
}

func (c *MySQLCollector) largest(ctx context.Context) []Database {
	rows, err := c.db.QueryContext(ctx, `
		select table_schema, coalesce(sum(data_length + index_length), 0)
		from information_schema.tables
		where table_schema not in ('mysql', 'information_schema', 'performance_schema', 'sys')
		group by table_schema
		order by 2 desc
		limit 5`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var dbs []Database
	for rows.Next() {
		var d Database
		if rows.Scan(&d.Name, &d.Size) == nil {
			dbs = append(dbs, d)
		}
	}
	return dbs
}

func queryPairs(ctx context.Context, db *sql.DB, query string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	pairs := map[string]string{}
	for rows.Next() {
		var key string
		var value sql.NullString
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		pairs[key] = value.String
	}
	return pairs, rows.Err()
}

type MongoStats struct {
	Name             string     `json:"name"`
	Up               bool       `json:"up"`
	Error            string     `json:"error,omitempty"`
	Version          string     `json:"version"`
	Uptime           float64    `json:"uptime"`
	Connections      int64      `json:"connections"`
	MaxConnections   int64      `json:"maxConnections"`
	OpsPerSec        float64    `json:"opsPerSec"`
	Memory           uint64     `json:"memory"`
	ReplicaSet       string     `json:"replicaSet,omitempty"`
	Role             string     `json:"role"`
	Lag              *float64   `json:"lag"`
	UnhealthyMembers int        `json:"unhealthyMembers"`
	Databases        []Database `json:"databases"`
}

type MongoCollector struct {
	name, url string
	client    *mongo.Client
	at        time.Time
	ops       int64
	sizesAt   time.Time
	sizes     []Database
}

func NewMongoCollector(t Target) Collector[MongoStats] {
	return &MongoCollector{name: t.Name, url: t.URL}
}

func (c *MongoCollector) Collect(ctx context.Context) MongoStats {
	s, err := c.query(ctx)
	if err != nil {
		if c.client != nil {
			c.client.Disconnect(context.Background())
			c.client = nil
		}
		return MongoStats{Name: c.name, Error: friendlyError(err)}
	}
	s.Name, s.Up = c.name, true
	return s
}

func (c *MongoCollector) query(ctx context.Context) (MongoStats, error) {
	var s MongoStats
	if c.client == nil {
		client, err := mongo.Connect(options.Client().ApplyURI(c.url).
			SetServerSelectionTimeout(5 * time.Second).
			SetConnectTimeout(5 * time.Second).
			SetMaxPoolSize(1))
		if err != nil {
			return s, err
		}
		c.client = client
	}
	admin := c.client.Database("admin")
	var st struct {
		Version     string  `bson:"version"`
		Uptime      float64 `bson:"uptime"`
		Connections struct {
			Current   int64 `bson:"current"`
			Available int64 `bson:"available"`
		} `bson:"connections"`
		Opcounters struct {
			Insert, Query, Update, Delete, Getmore, Command int64
		} `bson:"opcounters"`
		Mem struct {
			Resident int64 `bson:"resident"`
		} `bson:"mem"`
		Repl *struct {
			SetName           string `bson:"setName"`
			IsWritablePrimary bool   `bson:"isWritablePrimary"`
			IsMaster          bool   `bson:"ismaster"`
			Secondary         bool   `bson:"secondary"`
		} `bson:"repl"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&st); err != nil {
		return s, err
	}
	s.Version, s.Uptime, s.Memory = st.Version, st.Uptime, uint64(st.Mem.Resident)<<20
	s.Connections = st.Connections.Current
	s.MaxConnections = st.Connections.Current + st.Connections.Available
	o := st.Opcounters
	now, ops := time.Now(), o.Insert+o.Query+o.Update+o.Delete+o.Getmore+o.Command
	if !c.at.IsZero() && ops >= c.ops {
		s.OpsPerSec = float64(ops-c.ops) / now.Sub(c.at).Seconds()
	}
	c.at, c.ops = now, ops

	s.Role = "standalone"
	if r := st.Repl; r != nil && r.SetName != "" {
		s.ReplicaSet = r.SetName
		switch {
		case r.IsWritablePrimary || r.IsMaster:
			s.Role = "primary"
		case r.Secondary:
			s.Role = "secondary"
		default:
			s.Role = "other"
		}
		c.replication(ctx, admin, &s)
	}

	if time.Since(c.sizesAt) >= time.Minute {
		c.sizesAt, c.sizes = time.Now(), nil
		if res, err := c.client.ListDatabases(ctx, bson.D{}); err == nil {
			for _, d := range res.Databases {
				if d.Name != "admin" && d.Name != "local" && d.Name != "config" {
					c.sizes = append(c.sizes, Database{Name: d.Name, Size: d.SizeOnDisk})
				}
			}
			slices.SortFunc(c.sizes, func(a, b Database) int { return cmp.Compare(b.Size, a.Size) })
			c.sizes = c.sizes[:min(5, len(c.sizes))]
		}
	}
	s.Databases = c.sizes
	return s, nil
}

func (c *MongoCollector) replication(ctx context.Context, admin *mongo.Database, s *MongoStats) {
	var rs struct {
		Members []struct {
			Health     float64   `bson:"health"`
			StateStr   string    `bson:"stateStr"`
			OptimeDate time.Time `bson:"optimeDate"`
			Self       bool      `bson:"self"`
		} `bson:"members"`
	}
	if admin.RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&rs) != nil {
		return
	}
	var primary, self time.Time
	for _, m := range rs.Members {
		if m.Health == 0 {
			s.UnhealthyMembers++
		}
		if m.StateStr == "PRIMARY" {
			primary = m.OptimeDate
		}
		if m.Self {
			self = m.OptimeDate
		}
	}
	if s.Role == "secondary" && !primary.IsZero() && !self.IsZero() {
		lag := max(0, primary.Sub(self).Seconds())
		s.Lag = &lag
	}
}

type MemcachedStats struct {
	Name           string  `json:"name"`
	Up             bool    `json:"up"`
	Error          string  `json:"error,omitempty"`
	Version        string  `json:"version"`
	Uptime         float64 `json:"uptime"`
	Connections    int64   `json:"connections"`
	MaxConnections int64   `json:"maxConnections"`
	Memory         uint64  `json:"memory"`
	MaxMemory      uint64  `json:"maxMemory"`
	OpsPerSec      float64 `json:"opsPerSec"`
	HitRate        float64 `json:"hitRate"`
	Items          int64   `json:"items"`
	Evicted        int64   `json:"evicted"`
}

type MemcachedCollector struct {
	name, addr string
	at         time.Time
	ops        int64
}

func NewMemcachedCollector(t Target) Collector[MemcachedStats] {
	return &MemcachedCollector{name: t.Name, addr: hostPort(t.URL, "11211")}
}

func (c *MemcachedCollector) Collect(ctx context.Context) MemcachedStats {
	stats, err := c.stats(ctx)
	if err != nil {
		return MemcachedStats{Name: c.name, Error: friendlyError(err)}
	}
	n := func(key string) int64 { v, _ := strconv.ParseInt(stats[key], 10, 64); return v }
	s := MemcachedStats{
		Name:           c.name,
		Up:             true,
		Version:        stats["version"],
		Uptime:         float64(n("uptime")),
		Connections:    n("curr_connections"),
		MaxConnections: n("max_connections"),
		Memory:         uint64(n("bytes")),
		MaxMemory:      uint64(n("limit_maxbytes")),
		Items:          n("curr_items"),
		Evicted:        n("evictions"),
	}
	if hits, misses := n("get_hits"), n("get_misses"); hits+misses > 0 {
		s.HitRate = 100 * float64(hits) / float64(hits+misses)
	}
	now, ops := time.Now(), n("cmd_get")+n("cmd_set")
	if !c.at.IsZero() && ops >= c.ops {
		s.OpsPerSec = float64(ops-c.ops) / now.Sub(c.at).Seconds()
	}
	c.at, c.ops = now, ops
	return s
}

func (c *MemcachedCollector) stats(ctx context.Context) (map[string]string, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	if _, err := conn.Write([]byte("stats\r\n")); err != nil {
		return nil, err
	}
	return readMemcachedStats(bufio.NewReader(conn))
}

func readMemcachedStats(r *bufio.Reader) (map[string]string, error) {
	stats := map[string]string{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "END" {
			return stats, nil
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "STAT" {
			return nil, fmt.Errorf("memcached: unexpected reply %q", line)
		}
		stats[fields[1]] = fields[2]
	}
}

type NginxStats struct {
	Name           string  `json:"name"`
	Up             bool    `json:"up"`
	Error          string  `json:"error,omitempty"`
	Active         int64   `json:"active"`
	Reading        int64   `json:"reading"`
	Writing        int64   `json:"writing"`
	Waiting        int64   `json:"waiting"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	Dropped        int64   `json:"dropped"`
}

type NginxCollector struct {
	name, url string
	client    *http.Client
	at        time.Time
	requests  int64
}

func NewNginxCollector(t Target) Collector[NginxStats] {
	return &NginxCollector{name: t.Name, url: t.URL, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *NginxCollector) Collect(ctx context.Context) NginxStats {
	body, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return NginxStats{Name: c.name, Error: friendlyError(err)}
	}
	s, requests, err := parseStubStatus(string(body))
	if err != nil {
		return NginxStats{Name: c.name, Error: err.Error()}
	}
	s.Name, s.Up = c.name, true
	now := time.Now()
	if !c.at.IsZero() && requests >= c.requests {
		s.RequestsPerSec = float64(requests-c.requests) / now.Sub(c.at).Seconds()
	}
	c.at, c.requests = now, requests
	return s
}

func parseStubStatus(body string) (NginxStats, int64, error) {
	var nums []int64
	for _, field := range strings.Fields(body) {
		if v, err := strconv.ParseInt(field, 10, 64); err == nil {
			nums = append(nums, v)
		}
	}
	if !strings.HasPrefix(body, "Active connections:") || len(nums) != 7 {
		return NginxStats{}, 0, errors.New("not an nginx stub_status page")
	}
	return NginxStats{
		Active:  nums[0],
		Dropped: nums[1] - nums[2],
		Reading: nums[4],
		Writing: nums[5],
		Waiting: nums[6],
	}, nums[3], nil
}

type RabbitStats struct {
	Name        string       `json:"name"`
	Up          bool         `json:"up"`
	Error       string       `json:"error,omitempty"`
	Version     string       `json:"version"`
	Cluster     string       `json:"cluster"`
	Messages    int64        `json:"messages"`
	Ready       int64        `json:"ready"`
	Unacked     int64        `json:"unacked"`
	PublishRate float64      `json:"publishRate"`
	DeliverRate float64      `json:"deliverRate"`
	Connections int64        `json:"connections"`
	Queues      int64        `json:"queues"`
	Consumers   int64        `json:"consumers"`
	Nodes       []RabbitNode `json:"nodes"`
}

type RabbitNode struct {
	Name      string `json:"name"`
	Running   bool   `json:"running"`
	MemUsed   uint64 `json:"mem_used"`
	MemLimit  uint64 `json:"mem_limit"`
	MemAlarm  bool   `json:"mem_alarm"`
	DiskFree  uint64 `json:"disk_free"`
	DiskAlarm bool   `json:"disk_free_alarm"`
}

type RabbitCollector struct {
	name, url string
	client    *http.Client
}

func NewRabbitCollector(t Target) Collector[RabbitStats] {
	return &RabbitCollector{name: t.Name, url: strings.TrimRight(t.URL, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *RabbitCollector) Collect(ctx context.Context) RabbitStats {
	s, err := c.query(ctx)
	if err != nil {
		return RabbitStats{Name: c.name, Error: friendlyError(err)}
	}
	s.Name, s.Up = c.name, true
	return s
}

func (c *RabbitCollector) query(ctx context.Context) (RabbitStats, error) {
	var s RabbitStats
	var overview struct {
		Version     string `json:"rabbitmq_version"`
		Cluster     string `json:"cluster_name"`
		QueueTotals struct {
			Messages int64 `json:"messages"`
			Ready    int64 `json:"messages_ready"`
			Unacked  int64 `json:"messages_unacknowledged"`
		} `json:"queue_totals"`
		MessageStats struct {
			Publish struct {
				Rate float64 `json:"rate"`
			} `json:"publish_details"`
			Deliver struct {
				Rate float64 `json:"rate"`
			} `json:"deliver_get_details"`
		} `json:"message_stats"`
		ObjectTotals struct {
			Connections int64 `json:"connections"`
			Queues      int64 `json:"queues"`
			Consumers   int64 `json:"consumers"`
		} `json:"object_totals"`
	}
	body, err := httpGet(ctx, c.client, c.url+"/api/overview")
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(body, &overview); err != nil {
		return s, errors.New("not a RabbitMQ management API")
	}
	if body, err = httpGet(ctx, c.client, c.url+"/api/nodes"); err != nil {
		return s, err
	}
	if err := json.Unmarshal(body, &s.Nodes); err != nil {
		return s, errors.New("not a RabbitMQ management API")
	}
	s.Version, s.Cluster = overview.Version, overview.Cluster
	s.Messages, s.Ready, s.Unacked = overview.QueueTotals.Messages, overview.QueueTotals.Ready, overview.QueueTotals.Unacked
	s.PublishRate, s.DeliverRate = overview.MessageStats.Publish.Rate, overview.MessageStats.Deliver.Rate
	s.Connections, s.Queues, s.Consumers = overview.ObjectTotals.Connections, overview.ObjectTotals.Queues, overview.ObjectTotals.Consumers
	return s, nil
}

func httpGet(ctx context.Context, client *http.Client, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, errors.New("invalid URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("returning HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

type Check struct {
	Name        string  `json:"name"`
	Target      string  `json:"target"`
	Up          bool    `json:"up"`
	Error       string  `json:"error,omitempty"`
	Latency     float64 `json:"latency"`
	Status      int     `json:"status,omitempty"`
	CertExpires int64   `json:"certExpires,omitempty"`
}

type CheckCollector struct {
	name, url, target string
	client            *http.Client
}

func NewCheckCollector(t Target) Collector[Check] {
	target := t.URL
	if u, err := url.Parse(t.URL); err == nil {
		target = u.Redacted()
	}
	return &CheckCollector{
		name:   t.Name,
		url:    t.URL,
		target: target,
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *CheckCollector) Collect(ctx context.Context) Check {
	ck := Check{Name: c.name, Target: c.target}
	u, err := url.Parse(c.url)
	if err != nil {
		ck.Error = "an invalid URL"
		return ck
	}
	start := time.Now()
	switch u.Scheme {
	case "tcp":
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			ck.Error = friendlyError(err)
			return ck
		}
		conn.Close()
	case "http", "https":
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
		resp, err := c.client.Do(req)
		if err != nil {
			ck.Error = friendlyError(err)
			return ck
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		ck.Status = resp.StatusCode
		if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
			ck.CertExpires = resp.TLS.PeerCertificates[0].NotAfter.Unix()
		}
		if resp.StatusCode >= 400 {
			ck.Error = fmt.Sprintf("returning HTTP %d", resp.StatusCode)
			return ck
		}
	default:
		ck.Error = fmt.Sprintf("using unsupported scheme %q", u.Scheme)
		return ck
	}
	ck.Latency = float64(time.Since(start).Microseconds()) / 1000
	ck.Up = true
	return ck
}

type Unit struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Load        string `json:"load"`
	Active      string `json:"active"`
	Sub         string `json:"sub"`
}

type SystemdStatus struct {
	Up       bool   `json:"up"`
	Error    string `json:"error,omitempty"`
	Services int    `json:"services"`
	Running  int    `json:"running"`
	Failed   []Unit `json:"failed"`
	Watched  []Unit `json:"watched"`
}

type SystemdCollector struct {
	url    string
	watch  []string
	client *http.Client
}

func NewSystemdCollector(url, units string) *SystemdCollector {
	if url == "" {
		return nil
	}
	c := &SystemdCollector{url: url, client: &http.Client{Timeout: 5 * time.Second}}
	for _, name := range strings.Fields(units) {
		if !strings.Contains(name, ".") {
			name += ".service"
		}
		c.watch = append(c.watch, name)
	}
	return c
}

func (c *SystemdCollector) Collect(ctx context.Context) *SystemdStatus {
	var units []Unit
	body, err := httpGet(ctx, c.client, c.url)
	if err == nil {
		err = json.Unmarshal(body, &units)
	}
	if err != nil {
		return &SystemdStatus{Error: friendlyError(err), Failed: []Unit{}, Watched: []Unit{}}
	}
	s := summariseUnits(units, c.watch)
	return &s
}

func summariseUnits(units []Unit, watch []string) SystemdStatus {
	s := SystemdStatus{Up: true, Failed: []Unit{}, Watched: []Unit{}}
	byName := map[string]Unit{}
	for _, u := range units {
		byName[u.Name] = u
		if strings.HasSuffix(u.Name, ".service") && u.Load == "loaded" {
			s.Services++
			if u.Active == "active" {
				s.Running++
			}
		}
		if u.Active == "failed" {
			s.Failed = append(s.Failed, u)
		}
	}
	slices.SortFunc(s.Failed, func(a, b Unit) int { return cmp.Compare(a.Name, b.Name) })
	for _, name := range watch {
		unit, ok := byName[name]
		if !ok {
			unit = Unit{Name: name, Load: "not-found", Active: "inactive", Sub: "dead"}
		}
		s.Watched = append(s.Watched, unit)
	}
	return s
}

type systemdBus struct {
	mu     sync.Mutex
	socket string
	conn   *dbus.Conn
}

func (b *systemdBus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == nil {
		if _, err := os.Stat(b.socket); err != nil {
			http.Error(w, "systemd bus not mounted", http.StatusNotFound)
			return
		}
		conn, err := dbus.Connect("unix:path=" + b.socket)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		b.conn = conn
	}
	var listed []struct {
		Name, Description, LoadState, ActiveState, SubState, Followed string
		Path                                                          dbus.ObjectPath
		JobID                                                         uint32
		JobType                                                       string
		JobPath                                                       dbus.ObjectPath
	}
	err := b.conn.Object("org.freedesktop.systemd1", "/org/freedesktop/systemd1").
		CallWithContext(r.Context(), "org.freedesktop.systemd1.Manager.ListUnits", 0).Store(&listed)
	if err != nil {
		b.conn.Close()
		b.conn = nil
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	units := make([]Unit, len(listed))
	for i, u := range listed {
		units[i] = Unit{Name: u.Name, Description: u.Description, Load: u.LoadState, Active: u.ActiveState, Sub: u.SubState}
	}
	writeJSON(w, units)
}

type Target struct{ Name, URL string }

var targetName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func parseTargets(raw string) []Target {
	var targets []Target
	for _, item := range strings.Fields(raw) {
		t := Target{URL: item}
		if name, rest, ok := strings.Cut(item, "="); ok && rest != "" && targetName.MatchString(name) {
			t = Target{Name: name, URL: rest}
		}
		if t.Name == "" {
			t.Name = hostPort(t.URL, "")
		}
		targets = append(targets, t)
	}
	return targets
}

func hostPort(raw, defaultPort string) string {
	host := raw
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		host = u.Host
	} else if at := strings.LastIndex(raw, "@"); at >= 0 {
		host = raw[at+1:]
	}
	if defaultPort != "" {
		if _, _, err := net.SplitHostPort(host); err != nil {
			host = net.JoinHostPort(strings.Trim(host, "[]"), defaultPort)
		}
	}
	return host
}

type Collector[T any] interface {
	Collect(context.Context) T
}

func pollEach[T any](ctx context.Context, every time.Duration, raw string, build func(Target) Collector[T]) func() []T {
	var polls []*poller[*T]
	for _, t := range parseTargets(raw) {
		c, p := build(t), &poller[*T]{}
		polls = append(polls, p)
		go p.Run(ctx, every, func(ctx context.Context) *T {
			v := c.Collect(ctx)
			return &v
		})
	}
	return func() []T {
		var out []T
		for _, p := range polls {
			if v := p.Get(); v != nil {
				out = append(out, *v)
			}
		}
		return out
	}
}
