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
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
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
	mu   sync.Mutex
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
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *PostgresCollector) connect(ctx context.Context) error {
	if c.conn != nil {
		return nil
	}
	conn, err := pgx.Connect(ctx, c.url)
	if err != nil {
		return err
	}
	c.conn = conn
	return nil
}

func (c *PostgresCollector) query(ctx context.Context) (PostgresStats, error) {
	var s PostgresStats
	if err := c.connect(ctx); err != nil {
		return s, err
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
	reply, err := readRedisReply(r)
	if err != nil {
		return "", err
	}
	text, ok := reply.(string)
	if !ok {
		return "", fmt.Errorf("redis: unexpected reply %v", reply)
	}
	return text, nil
}

func redisCall(conn net.Conn, r *bufio.Reader, args ...string) (any, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		return nil, err
	}
	return readRedisReply(r)
}

func readRedisReply(r *bufio.Reader) (any, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("redis: empty reply")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return nil, errors.New(line[1:])
	case ':':
		return strconv.ParseInt(line[1:], 10, 64)
	case '$':
		size, err := strconv.Atoi(line[1:])
		if err != nil || size < 0 {
			return nil, nil
		}
		buf := make([]byte, size+2)
		if _, err := io.ReadFull(r, buf); err != nil {
			return nil, err
		}
		return string(buf[:size]), nil
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil || n < 0 {
			return nil, nil
		}
		items := make([]any, n)
		for i := range items {
			if items[i], err = readRedisReply(r); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	return nil, fmt.Errorf("redis: unexpected reply %q", line)
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

func (c *MySQLCollector) replicaFields(ctx context.Context) map[string]sql.NullString {
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
	return field
}

func (c *MySQLCollector) replica(ctx context.Context) *Replica {
	field := c.replicaFields(ctx)
	if field == nil {
		return nil
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
	mu        sync.Mutex
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
	c.mu.Lock()
	defer c.mu.Unlock()
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
	mu                sync.Mutex
	events            []CheckEvent
	last              *Check
}

type CheckEvent struct {
	Time   int64  `json:"time"`
	Up     bool   `json:"up"`
	Error  string `json:"error,omitempty"`
	Status int    `json:"status,omitempty"`
}

type CheckDetail struct {
	Target      string       `json:"target"`
	Status      int          `json:"status"`
	CertExpires int64        `json:"certExpires"`
	Events      []CheckEvent `json:"events"`
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
	ck := c.probe(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last == nil || c.last.Up != ck.Up || (!ck.Up && c.last.Error != ck.Error) {
		c.events = append(c.events, CheckEvent{Time: time.Now().Unix(), Up: ck.Up, Error: ck.Error, Status: ck.Status})
		if len(c.events) > 100 {
			c.events = c.events[len(c.events)-100:]
		}
	}
	c.last = &ck
	return ck
}

func (c *CheckCollector) Detail(context.Context) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := CheckDetail{Target: c.target, Events: make([]CheckEvent, 0, len(c.events))}
	for i := len(c.events) - 1; i >= 0; i-- {
		d.Events = append(d.Events, c.events[i])
	}
	if c.last != nil {
		d.Status, d.CertExpires = c.last.Status, c.last.CertExpires
	}
	return d, nil
}

func (c *CheckCollector) probe(ctx context.Context) Check {
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
	mu     sync.Mutex
	units  []Unit
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
	c.mu.Lock()
	c.units = units
	c.mu.Unlock()
	s := summariseUnits(units, c.watch)
	return &s
}

func (c *SystemdCollector) Detail(context.Context) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.units == nil {
		return nil, errors.New("no unit list yet")
	}
	rank := map[string]int{"failed": 0, "activating": 1, "deactivating": 1, "active": 2, "inactive": 3}
	units := slices.Clone(c.units)
	slices.SortFunc(units, func(a, b Unit) int {
		return cmp.Or(cmp.Compare(rank[a.Active], rank[b.Active]), cmp.Compare(a.Name, b.Name))
	})
	return struct {
		Units   []Unit   `json:"units"`
		Watched []string `json:"watched"`
	}{units, c.watch}, nil
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

type Detailer interface {
	Detail(context.Context) (any, error)
}

type ServiceRegistry struct {
	mu        sync.RWMutex
	services  map[string]any
	history   *ServiceHistory
	queryText bool
}

func NewServiceRegistry(queryText bool) *ServiceRegistry {
	return &ServiceRegistry{services: map[string]any{}, history: NewServiceHistory(), queryText: queryText}
}

func (r *ServiceRegistry) add(key string, collector any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[key] = collector
}

func (r *ServiceRegistry) lookup(key string) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.services[key]
	return c, ok
}

func pollEach[T any](ctx context.Context, every time.Duration, raw, kind string, build func(Target) Collector[T], metrics func(T) map[string]float64, registry *ServiceRegistry) func() []T {
	var polls []*poller[*T]
	for _, t := range parseTargets(raw) {
		c, p := build(t), &poller[*T]{}
		key := kind + ":" + t.Name
		registry.add(key, c)
		polls = append(polls, p)
		go p.Run(ctx, every, func(ctx context.Context) *T {
			v := c.Collect(ctx)
			registry.history.Record(key, time.Now(), metrics(v))
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

type ServicePoint map[string]float64

const serviceRecentSize = 30 * 60 / 5

type serviceSeries struct {
	recent, minutes, pending []ServicePoint
	minute                   int64
}

type ServiceHistory struct {
	mu     sync.RWMutex
	series map[string]*serviceSeries
}

func NewServiceHistory() *ServiceHistory {
	return &ServiceHistory{series: map[string]*serviceSeries{}}
}

func (h *ServiceHistory) Record(key string, now time.Time, values map[string]float64) {
	p := ServicePoint{"t": float64(now.UnixMilli())}
	for k, v := range values {
		p[k] = v
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.series[key]
	if s == nil {
		s = &serviceSeries{}
		h.series[key] = s
	}
	s.recent = append(s.recent, p)
	if len(s.recent) > serviceRecentSize {
		s.recent = s.recent[len(s.recent)-serviceRecentSize:]
	}
	minute := now.UnixMilli() / 60000
	if s.minute != 0 && minute != s.minute && len(s.pending) > 0 {
		s.minutes = append(s.minutes, averageServicePoints(s.pending))
		if len(s.minutes) > minuteSize {
			s.minutes = s.minutes[len(s.minutes)-minuteSize:]
		}
		s.pending = s.pending[:0]
	}
	s.minute = minute
	s.pending = append(s.pending, p)
}

func (h *ServiceHistory) Range(key, rangeName string) []ServicePoint {
	span, ok := Ranges[rangeName]
	if !ok {
		span = 5 * time.Minute
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	s := h.series[key]
	if s == nil {
		return []ServicePoint{}
	}
	source := s.minutes
	if span <= 30*time.Minute {
		source = s.recent
	}
	cutoff := float64(time.Now().Add(-span).UnixMilli())
	start := len(source)
	for start > 0 && source[start-1]["t"] > cutoff {
		start--
	}
	return downsample(source[start:], chartPoints, averageServicePoints)
}

func averageServicePoints(points []ServicePoint) ServicePoint {
	sums, counts := ServicePoint{}, map[string]int{}
	for _, p := range points {
		for k, v := range p {
			sums[k] += v
			counts[k]++
		}
	}
	for k := range sums {
		sums[k] /= float64(counts[k])
	}
	if len(points) > 0 {
		sums["t"] = points[len(points)-1]["t"]
	}
	return sums
}

func upValue(up bool) float64 {
	if up {
		return 1
	}
	return 0
}

func withUp(up bool, values map[string]float64) map[string]float64 {
	if !up {
		return map[string]float64{"up": 0}
	}
	values["up"] = 1
	return values
}

func postgresMetrics(s PostgresStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"tps": s.TPS, "connections": float64(s.Connections), "active": float64(s.Active), "cacheHit": s.CacheHit, "longestQuery": s.LongestQuery})
}

func mysqlMetrics(s MySQLStats) map[string]float64 {
	m := map[string]float64{"qps": s.QPS, "connections": float64(s.Connections), "running": float64(s.Running), "bufferPoolHit": s.BufferPoolHit, "longestQuery": s.LongestQuery}
	if s.Replica != nil && s.Replica.Lag != nil {
		m["lag"] = *s.Replica.Lag
	}
	return withUp(s.Up, m)
}

func mongoMetrics(s MongoStats) map[string]float64 {
	m := map[string]float64{"ops": s.OpsPerSec, "connections": float64(s.Connections), "memory": float64(s.Memory)}
	if s.Lag != nil {
		m["lag"] = *s.Lag
	}
	return withUp(s.Up, m)
}

func redisMetrics(s RedisStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"ops": s.OpsPerSec, "memory": float64(s.Memory), "clients": float64(s.Clients), "hitRate": s.HitRate, "keys": float64(s.Keys)})
}

func memcachedMetrics(s MemcachedStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"ops": s.OpsPerSec, "memory": float64(s.Memory), "connections": float64(s.Connections), "hitRate": s.HitRate, "items": float64(s.Items)})
}

func nginxMetrics(s NginxStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"requests": s.RequestsPerSec, "active": float64(s.Active), "reading": float64(s.Reading), "writing": float64(s.Writing), "waiting": float64(s.Waiting)})
}

func rabbitMetrics(s RabbitStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"messages": float64(s.Messages), "ready": float64(s.Ready), "unacked": float64(s.Unacked), "publish": s.PublishRate, "deliver": s.DeliverRate, "consumers": float64(s.Consumers)})
}

func checkMetrics(c Check) map[string]float64 {
	return withUp(c.Up, map[string]float64{"latency": c.Latency})
}

func systemdMetrics(s *SystemdStatus) map[string]float64 {
	return withUp(s.Up, map[string]float64{"running": float64(s.Running), "failed": float64(len(s.Failed)), "services": float64(s.Services)})
}

type queryTextKey struct{}

func withQueryText(ctx context.Context, show bool) context.Context {
	return context.WithValue(ctx, queryTextKey{}, show)
}

func queryText(ctx context.Context, text string) string {
	if show, _ := ctx.Value(queryTextKey{}).(bool); !show {
		return ""
	}
	return text
}

type Setting struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	Hint  string `json:"hint,omitempty"`
}

type PostgresSession struct {
	PID       int32   `json:"pid"`
	User      string  `json:"user"`
	Database  string  `json:"database"`
	Client    string  `json:"client"`
	State     string  `json:"state"`
	Wait      string  `json:"wait"`
	Duration  float64 `json:"duration"`
	XactAge   float64 `json:"xactAge"`
	Query     string  `json:"query"`
	BlockedBy []int32 `json:"blockedBy"`
}

type PostgresDatabase struct {
	Name         string  `json:"name"`
	Size         int64   `json:"size"`
	Connections  int32   `json:"connections"`
	Transactions int64   `json:"transactions"`
	CacheHit     float64 `json:"cacheHit"`
	Deadlocks    int64   `json:"deadlocks"`
	TempBytes    int64   `json:"tempBytes"`
}

type PostgresReplica struct {
	Name   string  `json:"name"`
	Client string  `json:"client"`
	State  string  `json:"state"`
	Sync   string  `json:"sync"`
	Lag    float64 `json:"lag"`
}

type PostgresDetail struct {
	Sessions   []PostgresSession  `json:"sessions"`
	Databases  []PostgresDatabase `json:"databases"`
	Replicas   []PostgresReplica  `json:"replicas"`
	InRecovery bool               `json:"inRecovery"`
	ReplayLag  *float64           `json:"replayLag"`
	Settings   []Setting          `json:"settings"`
}

func (c *PostgresCollector) Detail(ctx context.Context) (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.connect(ctx); err != nil {
		return nil, err
	}
	d := PostgresDetail{Sessions: []PostgresSession{}, Databases: []PostgresDatabase{}, Replicas: []PostgresReplica{}}
	rows, err := c.conn.Query(ctx, `
		select pid, coalesce(usename, ''), coalesce(datname, ''), coalesce(host(client_addr), 'local'), coalesce(state, ''),
		       coalesce(wait_event_type || ': ' || wait_event, ''),
		       coalesce(extract(epoch from now() - query_start), 0)::float8,
		       coalesce(extract(epoch from now() - xact_start), 0)::float8,
		       left(coalesce(query, ''), 4000), pg_blocking_pids(pid)
		from pg_stat_activity
		where backend_type = 'client backend' and pid <> pg_backend_pid()
		order by state = 'active' desc, query_start nulls last
		limit 200`)
	if err != nil {
		c.conn.Close(context.Background())
		c.conn = nil
		return nil, err
	}
	d.Sessions, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PostgresSession, error) {
		var p PostgresSession
		err := row.Scan(&p.PID, &p.User, &p.Database, &p.Client, &p.State, &p.Wait, &p.Duration, &p.XactAge, &p.Query, &p.BlockedBy)
		p.Query = queryText(ctx, p.Query)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	rows, err = c.conn.Query(ctx, `
		select d.datname, pg_database_size(d.datname), s.numbackends, s.xact_commit + s.xact_rollback,
		       case when s.blks_hit + s.blks_read > 0 then 100.0 * s.blks_hit / (s.blks_hit + s.blks_read) else 0 end::float8,
		       s.deadlocks, s.temp_bytes
		from pg_database d join pg_stat_database s on s.datid = d.oid
		where not d.datistemplate and d.datallowconn
		order by 2 desc`)
	if err == nil {
		d.Databases, _ = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PostgresDatabase, error) {
			var db PostgresDatabase
			return db, row.Scan(&db.Name, &db.Size, &db.Connections, &db.Transactions, &db.CacheHit, &db.Deadlocks, &db.TempBytes)
		})
	}
	rows, err = c.conn.Query(ctx, `
		select coalesce(application_name, ''), coalesce(host(client_addr), 'local'), coalesce(state, ''), coalesce(sync_state, ''),
		       coalesce(extract(epoch from replay_lag), 0)::float8
		from pg_stat_replication`)
	if err == nil {
		d.Replicas, _ = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PostgresReplica, error) {
			var r PostgresReplica
			return r, row.Scan(&r.Name, &r.Client, &r.State, &r.Sync, &r.Lag)
		})
	}
	c.conn.QueryRow(ctx, `select pg_is_in_recovery(), extract(epoch from now() - pg_last_xact_replay_timestamp())::float8`).Scan(&d.InRecovery, &d.ReplayLag)
	rows, err = c.conn.Query(ctx, `
		select name, current_setting(name)
		from unnest(array['server_version', 'max_connections', 'shared_buffers', 'effective_cache_size', 'work_mem',
		                  'maintenance_work_mem', 'wal_level', 'max_wal_size', 'checkpoint_timeout', 'autovacuum',
		                  'statement_timeout', 'idle_in_transaction_session_timeout']) as name`)
	if err == nil {
		d.Settings, _ = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Setting, error) {
			var st Setting
			return st, row.Scan(&st.Name, &st.Value)
		})
	}
	return d, nil
}

type MySQLProcess struct {
	ID      int64   `json:"id"`
	User    string  `json:"user"`
	Host    string  `json:"host"`
	DB      string  `json:"db"`
	Command string  `json:"command"`
	Time    float64 `json:"time"`
	State   string  `json:"state"`
	Query   string  `json:"query"`
}

type MySQLSchema struct {
	Name   string `json:"name"`
	Data   int64  `json:"data"`
	Index  int64  `json:"index"`
	Tables int64  `json:"tables"`
}

type MySQLDetail struct {
	Processes   []MySQLProcess `json:"processes"`
	Schemas     []MySQLSchema  `json:"schemas"`
	Counters    []Setting      `json:"counters"`
	Variables   []Setting      `json:"variables"`
	Replication []Setting      `json:"replication"`
}

var mysqlCounters = []Setting{
	{Name: "Connections", Hint: "Connection attempts since start"},
	{Name: "Max_used_connections", Hint: "Most connections open at once"},
	{Name: "Aborted_connects", Hint: "Failed connection attempts"},
	{Name: "Aborted_clients", Hint: "Connections dropped without closing"},
	{Name: "Threads_created", Hint: "Threads created to handle connections"},
	{Name: "Slow_queries", Hint: "Queries slower than long_query_time"},
	{Name: "Created_tmp_disk_tables", Hint: "Temporary tables written to disk"},
	{Name: "Created_tmp_tables", Hint: "Temporary tables created"},
	{Name: "Select_full_join", Hint: "Joins without a usable index"},
	{Name: "Sort_merge_passes", Hint: "Sorts that spilled past the sort buffer"},
	{Name: "Table_locks_waited", Hint: "Table locks that had to wait"},
	{Name: "Innodb_row_lock_waits", Hint: "Row locks that had to wait"},
	{Name: "Innodb_row_lock_time_avg", Hint: "Average row lock wait, ms"},
	{Name: "Innodb_buffer_pool_wait_free", Hint: "Waits for a free buffer pool page"},
}

var mysqlReplicationFields = [][2]string{
	{"Master_Host", "Source host"}, {"Master_Port", "Source port"}, {"Master_User", "Replication user"},
	{"Slave_IO_Running", "Receiving"}, {"Slave_SQL_Running", "Applying"}, {"Seconds_Behind_Master", "Seconds behind"},
	{"Master_Log_File", "Source log file"}, {"Read_Master_Log_Pos", "Read position"},
	{"Relay_Master_Log_File", "Applied log file"}, {"Exec_Master_Log_Pos", "Applied position"},
	{"Using_Gtid", "Using GTID"}, {"Gtid_IO_Pos", "GTID position"}, {"Retrieved_Gtid_Set", "Retrieved GTIDs"},
	{"Executed_Gtid_Set", "Executed GTIDs"}, {"Last_IO_Error", "Last receive error"}, {"Last_SQL_Error", "Last apply error"},
}

func (c *MySQLCollector) Detail(ctx context.Context) (any, error) {
	if c.err != nil {
		return nil, c.err
	}
	d := MySQLDetail{Processes: []MySQLProcess{}, Schemas: []MySQLSchema{}}
	rows, err := c.db.QueryContext(ctx, `
		select id, user, host, coalesce(db, ''), command, time, coalesce(state, ''), left(coalesce(info, ''), 4000)
		from information_schema.processlist
		where id <> connection_id() and command <> 'Daemon'
		order by command = 'Sleep', time desc
		limit 200`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p MySQLProcess
		if rows.Scan(&p.ID, &p.User, &p.Host, &p.DB, &p.Command, &p.Time, &p.State, &p.Query) == nil {
			p.Query = queryText(ctx, p.Query)
			d.Processes = append(d.Processes, p)
		}
	}
	rows.Close()
	if rows, err = c.db.QueryContext(ctx, `
		select table_schema, coalesce(sum(data_length), 0), coalesce(sum(index_length), 0), count(*)
		from information_schema.tables
		where table_schema not in ('mysql', 'information_schema', 'performance_schema', 'sys')
		group by table_schema
		order by sum(data_length + index_length) desc`); err == nil {
		for rows.Next() {
			var sc MySQLSchema
			if rows.Scan(&sc.Name, &sc.Data, &sc.Index, &sc.Tables) == nil {
				d.Schemas = append(d.Schemas, sc)
			}
		}
		rows.Close()
	}
	if status, err := queryPairs(ctx, c.db, "show global status"); err == nil {
		for _, counter := range mysqlCounters {
			if v, ok := status[counter.Name]; ok {
				counter.Value = v
				d.Counters = append(d.Counters, counter)
			}
		}
	}
	if vars, err := queryPairs(ctx, c.db, `show global variables where Variable_name in
		('version', 'version_comment', 'max_connections', 'innodb_buffer_pool_size', 'innodb_log_file_size',
		 'innodb_flush_log_at_trx_commit', 'long_query_time', 'slow_query_log', 'log_bin', 'binlog_format',
		 'server_id', 'read_only', 'sql_mode', 'character_set_server', 'time_zone', 'wait_timeout')`); err == nil {
		for _, name := range slices.Sorted(maps.Keys(vars)) {
			d.Variables = append(d.Variables, Setting{Name: name, Value: vars[name]})
		}
	}
	if field := c.replicaFields(ctx); field != nil {
		for _, f := range mysqlReplicationFields {
			if v, ok := field[f[0]]; ok {
				value := v.String
				if !v.Valid {
					value = "unknown"
				}
				d.Replication = append(d.Replication, Setting{Name: f[1], Value: value})
			}
		}
	}
	return d, nil
}

type MongoOp struct {
	ID          string  `json:"id"`
	Op          string  `json:"op"`
	Namespace   string  `json:"namespace"`
	Client      string  `json:"client"`
	Description string  `json:"description"`
	Seconds     float64 `json:"seconds"`
	Command     string  `json:"command"`
}

type MongoDatabase struct {
	Name        string  `json:"name"`
	Collections float64 `json:"collections"`
	Objects     float64 `json:"objects"`
	Data        float64 `json:"data"`
	Indexes     float64 `json:"indexes"`
	Storage     float64 `json:"storage"`
}

type MongoMember struct {
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Healthy bool     `json:"healthy"`
	Lag     *float64 `json:"lag"`
	Ping    float64  `json:"ping"`
	Uptime  float64  `json:"uptime"`
	Self    bool     `json:"self"`
}

type MongoDetail struct {
	Operations []MongoOp       `json:"operations"`
	Databases  []MongoDatabase `json:"databases"`
	Members    []MongoMember   `json:"members"`
	Server     []Setting       `json:"server"`
}

func (c *MongoCollector) Detail(ctx context.Context) (any, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, errors.New("not connected yet")
	}
	admin := client.Database("admin")
	d := MongoDetail{Operations: []MongoOp{}, Databases: []MongoDatabase{}, Members: []MongoMember{}}
	var current struct {
		Inprog []bson.M `bson:"inprog"`
	}
	if err := admin.RunCommand(ctx, bson.D{{Key: "currentOp", Value: true}, {Key: "active", Value: true}}).Decode(&current); err != nil {
		return nil, err
	}
	for _, op := range current.Inprog {
		command := asDocument(op["command"])
		if op["op"] == "none" || hasAnyKey(command, "currentOp", "hello", "isMaster", "ismaster") {
			continue
		}
		o := MongoOp{
			ID:          fmt.Sprint(op["opid"]),
			Op:          fmt.Sprint(op["op"]),
			Namespace:   stringOf(op["ns"]),
			Client:      cmp.Or(stringOf(op["client"]), stringOf(op["client_s"])),
			Description: stringOf(op["desc"]),
		}
		if us, ok := op["microsecs_running"].(int64); ok {
			o.Seconds = float64(us) / 1e6
		}
		if command != nil {
			if b, err := bson.MarshalExtJSON(command, false, false); err == nil {
				o.Command = queryText(ctx, truncate(string(b), 4000))
			}
		}
		d.Operations = append(d.Operations, o)
	}
	sort.Slice(d.Operations, func(i, j int) bool { return d.Operations[i].Seconds > d.Operations[j].Seconds })
	if names, err := client.ListDatabaseNames(ctx, bson.D{}); err == nil {
		for _, name := range names[:min(len(names), 30)] {
			var st struct {
				Collections float64 `bson:"collections"`
				Objects     float64 `bson:"objects"`
				DataSize    float64 `bson:"dataSize"`
				IndexSize   float64 `bson:"indexSize"`
				StorageSize float64 `bson:"storageSize"`
			}
			if client.Database(name).RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&st) == nil {
				d.Databases = append(d.Databases, MongoDatabase{Name: name, Collections: st.Collections, Objects: st.Objects, Data: st.DataSize, Indexes: st.IndexSize, Storage: st.StorageSize})
			}
		}
		sort.Slice(d.Databases, func(i, j int) bool { return d.Databases[i].Storage > d.Databases[j].Storage })
	}
	var rs struct {
		Members []struct {
			Name       string    `bson:"name"`
			StateStr   string    `bson:"stateStr"`
			Health     float64   `bson:"health"`
			OptimeDate time.Time `bson:"optimeDate"`
			PingMs     float64   `bson:"pingMs"`
			Uptime     float64   `bson:"uptime"`
			Self       bool      `bson:"self"`
		} `bson:"members"`
	}
	if admin.RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Decode(&rs) == nil {
		var primary time.Time
		for _, m := range rs.Members {
			if m.StateStr == "PRIMARY" {
				primary = m.OptimeDate
			}
		}
		for _, m := range rs.Members {
			member := MongoMember{Name: m.Name, State: m.StateStr, Healthy: m.Health == 1, Ping: m.PingMs, Uptime: m.Uptime, Self: m.Self}
			if !primary.IsZero() && !m.OptimeDate.IsZero() && m.StateStr == "SECONDARY" {
				lag := max(0, primary.Sub(m.OptimeDate).Seconds())
				member.Lag = &lag
			}
			d.Members = append(d.Members, member)
		}
	}
	var st struct {
		Version       string `bson:"version"`
		Process       string `bson:"process"`
		StorageEngine struct {
			Name string `bson:"name"`
		} `bson:"storageEngine"`
		Connections struct {
			TotalCreated float64 `bson:"totalCreated"`
		} `bson:"connections"`
		Asserts struct {
			Regular, Warning, User float64
		} `bson:"asserts"`
	}
	if admin.RunCommand(ctx, bson.D{{Key: "serverStatus", Value: 1}}).Decode(&st) == nil {
		d.Server = []Setting{
			{Name: "Version", Value: st.Version},
			{Name: "Process", Value: st.Process},
			{Name: "Storage engine", Value: st.StorageEngine.Name},
			{Name: "Connections created", Value: strconv.FormatFloat(st.Connections.TotalCreated, 'f', 0, 64)},
			{Name: "User assertions", Value: strconv.FormatFloat(st.Asserts.User, 'f', 0, 64), Hint: "Errors such as failed writes or bad queries"},
		}
	}
	return d, nil
}

func hasAnyKey(m bson.M, keys ...string) bool {
	for _, k := range keys {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func asDocument(v any) bson.M {
	switch doc := v.(type) {
	case bson.M:
		return doc
	case bson.D:
		m := bson.M{}
		for _, e := range doc {
			m[e.Key] = e.Value
		}
		return m
	}
	return nil
}

func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var redisDBKey = regexp.MustCompile(`^db[0-9]+$`)

type RedisDB struct {
	Name    string  `json:"name"`
	Keys    int64   `json:"keys"`
	Expires int64   `json:"expires"`
	AvgTTL  float64 `json:"avgTtl"`
}

type RedisSlow struct {
	ID       int64   `json:"id"`
	Time     int64   `json:"time"`
	Duration float64 `json:"duration"`
	Command  string  `json:"command"`
	Client   string  `json:"client"`
}

type RedisClient struct {
	Addr    string `json:"addr"`
	Name    string `json:"name"`
	Age     int64  `json:"age"`
	Idle    int64  `json:"idle"`
	DB      string `json:"db"`
	Command string `json:"command"`
}

type SettingGroup struct {
	Title string    `json:"title"`
	Items []Setting `json:"items"`
}

type RedisDetail struct {
	Sections     []SettingGroup `json:"sections"`
	Keyspace     []RedisDB      `json:"keyspace"`
	Slowlog      []RedisSlow    `json:"slowlog"`
	SlowlogError string         `json:"slowlogError,omitempty"`
	Clients      []RedisClient  `json:"clients"`
	ClientsError string         `json:"clientsError,omitempty"`
}

var redisSections = []struct {
	title string
	keys  []string
}{
	{"Server", []string{"redis_version", "redis_mode", "os", "uptime_in_days", "tcp_port"}},
	{"Memory", []string{"used_memory_human", "used_memory_peak_human", "used_memory_rss_human", "maxmemory_human", "maxmemory_policy", "mem_fragmentation_ratio"}},
	{"Persistence", []string{"loading", "rdb_last_save_time", "rdb_changes_since_last_save", "rdb_last_bgsave_status", "aof_enabled", "aof_last_write_status"}},
	{"Clients", []string{"connected_clients", "blocked_clients", "maxclients"}},
	{"Stats", []string{"total_connections_received", "total_commands_processed", "expired_keys", "evicted_keys", "rejected_connections", "keyspace_hits", "keyspace_misses"}},
	{"Replication", []string{"role", "connected_slaves", "master_host", "master_link_status", "master_last_io_seconds_ago"}},
}

func (c *RedisCollector) Detail(ctx context.Context) (any, error) {
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
	d := RedisDetail{Keyspace: []RedisDB{}, Slowlog: []RedisSlow{}, Clients: []RedisClient{}}
	for line := range strings.SplitSeq(reply, "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		info[key] = value
		if redisDBKey.MatchString(key) {
			db := RedisDB{Name: key}
			for _, part := range strings.Split(value, ",") {
				k, v, _ := strings.Cut(part, "=")
				n, _ := strconv.ParseFloat(v, 64)
				switch k {
				case "keys":
					db.Keys = int64(n)
				case "expires":
					db.Expires = int64(n)
				case "avg_ttl":
					db.AvgTTL = n / 1000
				}
			}
			d.Keyspace = append(d.Keyspace, db)
		}
	}
	slices.SortFunc(d.Keyspace, func(a, b RedisDB) int { return cmp.Compare(b.Keys, a.Keys) })
	if t, err := strconv.ParseInt(info["rdb_last_save_time"], 10, 64); err == nil && t > 0 {
		info["rdb_last_save_time"] = time.Unix(t, 0).UTC().Format("2006-01-02 15:04 UTC")
	}
	for _, section := range redisSections {
		group := SettingGroup{Title: section.title}
		for _, k := range section.keys {
			if v, ok := info[k]; ok {
				group.Items = append(group.Items, Setting{Name: k, Value: v})
			}
		}
		if len(group.Items) > 0 {
			d.Sections = append(d.Sections, group)
		}
	}
	if slow, err := redisCall(conn, r, "SLOWLOG", "GET", "25"); err != nil {
		d.SlowlogError = err.Error()
	} else {
		entries, _ := slow.([]any)
		for _, e := range entries {
			if s, ok := parseSlowlogEntry(e); ok {
				s.Command = queryText(ctx, s.Command)
				if s.Command == "" {
					s.Command = slowlogCommandName(e)
				}
				d.Slowlog = append(d.Slowlog, s)
			}
		}
	}
	if list, err := redisCommand(conn, r, "CLIENT", "LIST"); err != nil {
		d.ClientsError = err.Error()
	} else {
		d.Clients = parseClientList(list)
	}
	return d, nil
}

func parseSlowlogEntry(e any) (RedisSlow, bool) {
	fields, ok := e.([]any)
	if !ok || len(fields) < 4 {
		return RedisSlow{}, false
	}
	id, _ := fields[0].(int64)
	at, _ := fields[1].(int64)
	us, _ := fields[2].(int64)
	var args []string
	if list, ok := fields[3].([]any); ok {
		for _, a := range list {
			args = append(args, fmt.Sprint(a))
		}
	}
	s := RedisSlow{ID: id, Time: at, Duration: float64(us) / 1000, Command: truncate(strings.Join(args, " "), 2000)}
	if len(fields) > 4 {
		s.Client = fmt.Sprint(fields[4])
	}
	return s, true
}

func slowlogCommandName(e any) string {
	if fields, ok := e.([]any); ok && len(fields) > 3 {
		if args, ok := fields[3].([]any); ok && len(args) > 0 {
			return fmt.Sprint(args[0])
		}
	}
	return ""
}

func parseClientList(list string) []RedisClient {
	clients := []RedisClient{}
	for line := range strings.SplitSeq(strings.TrimSpace(list), "\n") {
		f := map[string]string{}
		for _, part := range strings.Fields(line) {
			if k, v, ok := strings.Cut(part, "="); ok {
				f[k] = v
			}
		}
		if f["addr"] == "" || f["cmd"] == "client|list" {
			continue
		}
		age, _ := strconv.ParseInt(f["age"], 10, 64)
		idle, _ := strconv.ParseInt(f["idle"], 10, 64)
		clients = append(clients, RedisClient{Addr: f["addr"], Name: f["name"], Age: age, Idle: idle, DB: f["db"], Command: f["cmd"]})
	}
	return clients
}

func (c *MemcachedCollector) Detail(ctx context.Context) (any, error) {
	stats, err := c.stats(ctx)
	if err != nil {
		return nil, err
	}
	settings := []Setting{}
	for _, k := range slices.Sorted(maps.Keys(stats)) {
		settings = append(settings, Setting{Name: k, Value: stats[k]})
	}
	return struct {
		Stats []Setting `json:"stats"`
	}{settings}, nil
}

type RabbitQueue struct {
	Name        string  `json:"name"`
	Vhost       string  `json:"vhost"`
	State       string  `json:"state"`
	Messages    int64   `json:"messages"`
	Ready       int64   `json:"ready"`
	Unacked     int64   `json:"unacked"`
	Consumers   int64   `json:"consumers"`
	Memory      int64   `json:"memory"`
	PublishRate float64 `json:"publishRate"`
	DeliverRate float64 `json:"deliverRate"`
}

type RabbitNodeDetail struct {
	Name          string  `json:"name"`
	Running       bool    `json:"running"`
	MemUsed       uint64  `json:"memUsed"`
	MemLimit      uint64  `json:"memLimit"`
	MemAlarm      bool    `json:"memAlarm"`
	DiskFree      uint64  `json:"diskFree"`
	DiskFreeLimit uint64  `json:"diskFreeLimit"`
	DiskAlarm     bool    `json:"diskAlarm"`
	FDUsed        int64   `json:"fdUsed"`
	FDTotal       int64   `json:"fdTotal"`
	ProcUsed      int64   `json:"procUsed"`
	ProcTotal     int64   `json:"procTotal"`
	Uptime        float64 `json:"uptime"`
}

func (c *RabbitCollector) Detail(ctx context.Context) (any, error) {
	body, err := httpGet(ctx, c.client, c.url+"/api/queues?columns=name,vhost,state,messages,messages_ready,messages_unacknowledged,consumers,memory,message_stats")
	if err != nil {
		return nil, err
	}
	var listed []struct {
		Name      string `json:"name"`
		Vhost     string `json:"vhost"`
		State     string `json:"state"`
		Messages  int64  `json:"messages"`
		Ready     int64  `json:"messages_ready"`
		Unacked   int64  `json:"messages_unacknowledged"`
		Consumers int64  `json:"consumers"`
		Memory    int64  `json:"memory"`
		Stats     struct {
			Publish struct {
				Rate float64 `json:"rate"`
			} `json:"publish_details"`
			Deliver struct {
				Rate float64 `json:"rate"`
			} `json:"deliver_get_details"`
		} `json:"message_stats"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, errors.New("not a RabbitMQ management API")
	}
	queues := make([]RabbitQueue, 0, len(listed))
	for _, q := range listed {
		queues = append(queues, RabbitQueue{Name: q.Name, Vhost: q.Vhost, State: q.State, Messages: q.Messages, Ready: q.Ready, Unacked: q.Unacked, Consumers: q.Consumers, Memory: q.Memory, PublishRate: q.Stats.Publish.Rate, DeliverRate: q.Stats.Deliver.Rate})
	}
	slices.SortFunc(queues, func(a, b RabbitQueue) int {
		return cmp.Or(cmp.Compare(b.Messages, a.Messages), cmp.Compare(a.Name, b.Name))
	})
	nodes := []RabbitNodeDetail{}
	if body, err = httpGet(ctx, c.client, c.url+"/api/nodes"); err == nil {
		var listedNodes []struct {
			Name          string  `json:"name"`
			Running       bool    `json:"running"`
			MemUsed       uint64  `json:"mem_used"`
			MemLimit      uint64  `json:"mem_limit"`
			MemAlarm      bool    `json:"mem_alarm"`
			DiskFree      uint64  `json:"disk_free"`
			DiskFreeLimit uint64  `json:"disk_free_limit"`
			DiskAlarm     bool    `json:"disk_free_alarm"`
			FDUsed        int64   `json:"fd_used"`
			FDTotal       int64   `json:"fd_total"`
			ProcUsed      int64   `json:"proc_used"`
			ProcTotal     int64   `json:"proc_total"`
			Uptime        float64 `json:"uptime"`
		}
		if json.Unmarshal(body, &listedNodes) == nil {
			for _, n := range listedNodes {
				nodes = append(nodes, RabbitNodeDetail(n))
			}
		}
	}
	return struct {
		Queues []RabbitQueue      `json:"queues"`
		Nodes  []RabbitNodeDetail `json:"nodes"`
	}{queues, nodes}, nil
}

type promSample struct {
	name   string
	labels map[string]string
	value  float64
}

func parsePrometheus(body string) []promSample {
	var samples []promSample
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		s := promSample{labels: map[string]string{}}
		rest := line
		if i := strings.IndexAny(line, "{ "); i < 0 {
			continue
		} else if line[i] == '{' {
			s.name = line[:i]
			end := parseLabels(line[i+1:], s.labels)
			if end < 0 {
				continue
			}
			rest = line[i+1+end+1:]
		} else {
			s.name, rest = line[:i], line[i:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			continue
		}
		s.value = v
		samples = append(samples, s)
	}
	return samples
}

func parseLabels(s string, labels map[string]string) int {
	i := 0
	for i < len(s) {
		if s[i] == '}' {
			return i
		}
		if s[i] == ',' || s[i] == ' ' {
			i++
			continue
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 || i+eq+1 >= len(s) || s[i+eq+1] != '"' {
			return -1
		}
		key := s[i : i+eq]
		i += eq + 2
		var value strings.Builder
		for i < len(s) && s[i] != '"' {
			if s[i] == '\\' && i+1 < len(s) {
				i++
				if s[i] == 'n' {
					value.WriteByte('\n')
				} else {
					value.WriteByte(s[i])
				}
			} else {
				value.WriteByte(s[i])
			}
			i++
		}
		labels[key] = value.String()
		i++
	}
	return -1
}

func sumSamples(samples []promSample, name string, keep func(map[string]string) bool) (float64, bool) {
	total, found := 0.0, false
	for _, s := range samples {
		if s.name == name && (keep == nil || keep(s.labels)) {
			total += s.value
			found = true
		}
	}
	return total, found
}

type counterRate struct {
	at     time.Time
	values map[string]float64
}

func (c *counterRate) deltas(now time.Time, values map[string]float64) (map[string]float64, float64) {
	prev, prevAt := c.values, c.at
	c.values, c.at = values, now
	if prevAt.IsZero() {
		return nil, 0
	}
	out := map[string]float64{}
	for k, v := range values {
		if p, ok := prev[k]; ok && v >= p {
			out[k] = v - p
		}
	}
	return out, now.Sub(prevAt).Seconds()
}

func serverErrorCode(labels map[string]string) bool { return strings.HasPrefix(labels["code"], "5") }

type CaddyUpstream struct {
	Address  string `json:"address"`
	Healthy  bool   `json:"healthy"`
	Requests int64  `json:"requests"`
	Fails    int64  `json:"fails"`
}

type CaddyStats struct {
	Name           string          `json:"name"`
	Up             bool            `json:"up"`
	Error          string          `json:"error,omitempty"`
	Upstreams      []CaddyUpstream `json:"upstreams"`
	Unhealthy      []string        `json:"unhealthy"`
	Servers        int             `json:"servers"`
	Metrics        bool            `json:"metrics"`
	RequestsPerSec float64         `json:"requestsPerSec"`
	InFlight       float64         `json:"inFlight"`
	ErrorRate      float64         `json:"errorRate"`
	Latency        float64         `json:"latency"`
	ConfigOK       *bool           `json:"configOk"`
}

type CaddyCollector struct {
	name, url string
	client    *http.Client
	rates     counterRate
}

func NewCaddyCollector(t Target) Collector[CaddyStats] {
	return &CaddyCollector{name: t.Name, url: strings.TrimRight(t.URL, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *CaddyCollector) Collect(ctx context.Context) CaddyStats {
	s := CaddyStats{Name: c.name, Upstreams: []CaddyUpstream{}, Unhealthy: []string{}}
	body, err := httpGet(ctx, c.client, c.url+"/reverse_proxy/upstreams")
	if err != nil {
		s.Error = friendlyError(err)
		return s
	}
	var upstreams []struct {
		Address  string `json:"address"`
		Requests int64  `json:"num_requests"`
		Fails    int64  `json:"fails"`
	}
	if json.Unmarshal(body, &upstreams) != nil {
		s.Error = "not a Caddy admin API"
		return s
	}
	s.Up = true
	var samples []promSample
	if body, err := httpGet(ctx, c.client, c.url+"/metrics"); err == nil {
		samples = parsePrometheus(string(body))
	}
	health := map[string]bool{}
	for _, sm := range samples {
		if sm.name == "caddy_reverse_proxy_upstreams_healthy" {
			health[sm.labels["upstream"]] = sm.value == 1
		}
	}
	for _, u := range upstreams {
		healthy, known := health[u.Address]
		if !known {
			healthy = u.Fails == 0
		}
		s.Upstreams = append(s.Upstreams, CaddyUpstream{Address: u.Address, Healthy: healthy, Requests: u.Requests, Fails: u.Fails})
		if !healthy {
			s.Unhealthy = append(s.Unhealthy, u.Address)
		}
	}
	slices.SortFunc(s.Upstreams, func(a, b CaddyUpstream) int { return cmp.Compare(a.Address, b.Address) })
	slices.Sort(s.Unhealthy)
	if ok, found := sumSamples(samples, "caddy_config_last_reload_successful", nil); found {
		success := ok == 1
		s.ConfigOK = &success
	}
	requests, found := sumSamples(samples, "caddy_http_requests_total", nil)
	s.Metrics = found
	s.InFlight, _ = sumSamples(samples, "caddy_http_requests_in_flight", nil)
	all, _ := sumSamples(samples, "caddy_http_request_duration_seconds_count", nil)
	failed, _ := sumSamples(samples, "caddy_http_request_duration_seconds_count", serverErrorCode)
	seconds, _ := sumSamples(samples, "caddy_http_request_duration_seconds_sum", nil)
	d, elapsed := c.rates.deltas(time.Now(), map[string]float64{"requests": requests, "all": all, "failed": failed, "seconds": seconds})
	if elapsed > 0 {
		s.RequestsPerSec = d["requests"] / elapsed
		if d["all"] > 0 {
			s.ErrorRate = 100 * d["failed"] / d["all"]
			s.Latency = 1000 * d["seconds"] / d["all"]
		}
	}
	if body, err := httpGet(ctx, c.client, c.url+"/config/apps/http/servers"); err == nil {
		var servers map[string]any
		if json.Unmarshal(body, &servers) == nil {
			s.Servers = len(servers)
		}
	}
	return s
}

type CaddySite struct {
	Server    string   `json:"server"`
	Listen    []string `json:"listen"`
	Hosts     []string `json:"hosts"`
	Handlers  []string `json:"handlers"`
	Upstreams []string `json:"upstreams"`
}

func (c *CaddyCollector) Detail(ctx context.Context) (any, error) {
	body, err := httpGet(ctx, c.client, c.url+"/config/apps/http/servers")
	if err != nil {
		return nil, err
	}
	var servers map[string]struct {
		Listen []string `json:"listen"`
		Routes []any    `json:"routes"`
	}
	if err := json.Unmarshal(body, &servers); err != nil {
		return nil, errors.New("not a Caddy admin API")
	}
	sites := []CaddySite{}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		site := CaddySite{Server: name, Listen: servers[name].Listen}
		hosts, handlers, upstreams := map[string]bool{}, map[string]bool{}, map[string]bool{}
		walkCaddyRoutes(servers[name].Routes, hosts, handlers, upstreams)
		site.Hosts, site.Handlers, site.Upstreams = slices.Sorted(maps.Keys(hosts)), slices.Sorted(maps.Keys(handlers)), slices.Sorted(maps.Keys(upstreams))
		sites = append(sites, site)
	}
	codes, perServer := []Setting{}, []Setting{}
	if body, err := httpGet(ctx, c.client, c.url+"/metrics"); err == nil {
		samples := parsePrometheus(string(body))
		byCode, byServer := map[string]float64{}, map[string]float64{}
		for _, s := range samples {
			switch s.name {
			case "caddy_http_request_duration_seconds_count":
				byCode[s.labels["code"]] += s.value
			case "caddy_http_requests_total":
				byServer[s.labels["server"]] += s.value
			}
		}
		for _, k := range slices.Sorted(maps.Keys(byCode)) {
			codes = append(codes, Setting{Name: k, Value: strconv.FormatFloat(byCode[k], 'f', 0, 64)})
		}
		for _, k := range slices.Sorted(maps.Keys(byServer)) {
			perServer = append(perServer, Setting{Name: k, Value: strconv.FormatFloat(byServer[k], 'f', 0, 64)})
		}
	}
	return struct {
		Sites   []CaddySite `json:"sites"`
		Codes   []Setting   `json:"codes"`
		Servers []Setting   `json:"servers"`
	}{sites, codes, perServer}, nil
}

func walkCaddyRoutes(routes []any, hosts, handlers, upstreams map[string]bool) {
	for _, r := range routes {
		route, _ := r.(map[string]any)
		matches, _ := route["match"].([]any)
		for _, m := range matches {
			match, _ := m.(map[string]any)
			list, _ := match["host"].([]any)
			for _, h := range list {
				hosts[fmt.Sprint(h)] = true
			}
		}
		handles, _ := route["handle"].([]any)
		for _, h := range handles {
			handler, _ := h.(map[string]any)
			name := fmt.Sprint(handler["handler"])
			if name != "subroute" {
				handlers[name] = true
			}
			list, _ := handler["upstreams"].([]any)
			for _, u := range list {
				if up, ok := u.(map[string]any); ok {
					upstreams[fmt.Sprint(up["dial"])] = true
				}
			}
			nested, _ := handler["routes"].([]any)
			walkCaddyRoutes(nested, hosts, handlers, upstreams)
		}
	}
}

type TraefikStats struct {
	Name           string   `json:"name"`
	Up             bool     `json:"up"`
	Error          string   `json:"error,omitempty"`
	Version        string   `json:"version"`
	Routers        int      `json:"routers"`
	RouterErrors   []string `json:"routerErrors"`
	Services       int      `json:"services"`
	ServersUp      int      `json:"serversUp"`
	ServersDown    []string `json:"serversDown"`
	Metrics        bool     `json:"metrics"`
	RequestsPerSec float64  `json:"requestsPerSec"`
	ErrorRate      float64  `json:"errorRate"`
	Connections    float64  `json:"connections"`
	Providers      []string `json:"providers"`
}

type traefikRouter struct {
	Name        string   `json:"name"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
	Status      string   `json:"status"`
	Errors      []string `json:"error"`
	Provider    string   `json:"provider"`
	TLS         any      `json:"tls"`
}

type traefikService struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	Provider     string            `json:"provider"`
	UsedBy       []string          `json:"usedBy"`
	ServerStatus map[string]string `json:"serverStatus"`
	Errors       []string          `json:"error"`
}

type TraefikCollector struct {
	name, url string
	client    *http.Client
	rates     counterRate
}

func NewTraefikCollector(t Target) Collector[TraefikStats] {
	return &TraefikCollector{name: t.Name, url: strings.TrimRight(t.URL, "/"), client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *TraefikCollector) fetch(ctx context.Context, path string, v any) error {
	body, err := httpGet(ctx, c.client, c.url+path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("not a Traefik API")
	}
	return nil
}

func (c *TraefikCollector) Collect(ctx context.Context) TraefikStats {
	s := TraefikStats{Name: c.name, RouterErrors: []string{}, ServersDown: []string{}, Providers: []string{}}
	var version struct{ Version string }
	if err := c.fetch(ctx, "/api/version", &version); err != nil {
		s.Error = friendlyError(err)
		return s
	}
	s.Up, s.Version = true, version.Version
	var overview struct {
		Providers []string `json:"providers"`
	}
	if c.fetch(ctx, "/api/overview", &overview) == nil && overview.Providers != nil {
		s.Providers = overview.Providers
	}
	var routers []traefikRouter
	if c.fetch(ctx, "/api/http/routers", &routers) == nil {
		for _, r := range routers {
			if r.Provider == "internal" {
				continue
			}
			s.Routers++
			if r.Status == "disabled" || len(r.Errors) > 0 {
				s.RouterErrors = append(s.RouterErrors, r.Name)
			}
		}
	}
	var services []traefikService
	if c.fetch(ctx, "/api/http/services", &services) == nil {
		for _, svc := range services {
			if svc.Provider == "internal" {
				continue
			}
			s.Services++
			for _, url := range slices.Sorted(maps.Keys(svc.ServerStatus)) {
				if svc.ServerStatus[url] == "UP" {
					s.ServersUp++
				} else {
					s.ServersDown = append(s.ServersDown, svc.Name+" → "+url)
				}
			}
		}
	}
	if body, err := httpGet(ctx, c.client, c.url+"/metrics"); err == nil {
		samples := parsePrometheus(string(body))
		requests, found := sumSamples(samples, "traefik_entrypoint_requests_total", nil)
		s.Metrics = found
		failed, _ := sumSamples(samples, "traefik_entrypoint_requests_total", serverErrorCode)
		s.Connections, _ = sumSamples(samples, "traefik_open_connections", func(l map[string]string) bool { return l["entrypoint"] != "traefik" })
		d, elapsed := c.rates.deltas(time.Now(), map[string]float64{"requests": requests, "failed": failed})
		if elapsed > 0 {
			s.RequestsPerSec = d["requests"] / elapsed
			if d["requests"] > 0 {
				s.ErrorRate = 100 * d["failed"] / d["requests"]
			}
		}
	}
	return s
}

type TraefikServer struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

type TraefikServiceDetail struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	Status   string          `json:"status"`
	Provider string          `json:"provider"`
	UsedBy   []string        `json:"usedBy"`
	Servers  []TraefikServer `json:"servers"`
	Errors   []string        `json:"errors"`
}

type TraefikRouterDetail struct {
	Name        string   `json:"name"`
	Rule        string   `json:"rule"`
	Service     string   `json:"service"`
	EntryPoints []string `json:"entryPoints"`
	Status      string   `json:"status"`
	Errors      []string `json:"errors"`
	Provider    string   `json:"provider"`
	TLS         bool     `json:"tls"`
}

func (c *TraefikCollector) Detail(ctx context.Context) (any, error) {
	var routers []traefikRouter
	if err := c.fetch(ctx, "/api/http/routers", &routers); err != nil {
		return nil, err
	}
	var services []traefikService
	c.fetch(ctx, "/api/http/services", &services)
	var entrypoints []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	}
	c.fetch(ctx, "/api/entrypoints", &entrypoints)
	d := struct {
		Routers     []TraefikRouterDetail  `json:"routers"`
		Services    []TraefikServiceDetail `json:"services"`
		EntryPoints []Setting              `json:"entryPoints"`
	}{[]TraefikRouterDetail{}, []TraefikServiceDetail{}, []Setting{}}
	for _, r := range routers {
		if r.Provider != "internal" {
			d.Routers = append(d.Routers, TraefikRouterDetail{Name: r.Name, Rule: r.Rule, Service: r.Service, EntryPoints: r.EntryPoints, Status: r.Status, Errors: r.Errors, Provider: r.Provider, TLS: r.TLS != nil})
		}
	}
	for _, svc := range services {
		if svc.Provider == "internal" {
			continue
		}
		detail := TraefikServiceDetail{Name: svc.Name, Type: svc.Type, Status: svc.Status, Provider: svc.Provider, UsedBy: svc.UsedBy, Errors: svc.Errors, Servers: []TraefikServer{}}
		for _, url := range slices.Sorted(maps.Keys(svc.ServerStatus)) {
			detail.Servers = append(detail.Servers, TraefikServer{URL: url, Status: svc.ServerStatus[url]})
		}
		d.Services = append(d.Services, detail)
	}
	for _, e := range entrypoints {
		d.EntryPoints = append(d.EntryPoints, Setting{Name: e.Name, Value: e.Address})
	}
	return d, nil
}

type HAProxyRow struct {
	Proxy       string  `json:"proxy"`
	Server      string  `json:"server"`
	Status      string  `json:"status"`
	Sessions    int64   `json:"sessions"`
	MaxSessions int64   `json:"maxSessions"`
	Limit       int64   `json:"limit"`
	Total       int64   `json:"total"`
	Rate        float64 `json:"rate"`
	Errors5xx   int64   `json:"errors5xx"`
	Check       string  `json:"check"`
	LastChange  int64   `json:"lastChange"`
	Weight      int64   `json:"weight"`
}

type HAProxyStats struct {
	Name           string   `json:"name"`
	Up             bool     `json:"up"`
	Error          string   `json:"error,omitempty"`
	Frontends      int      `json:"frontends"`
	Backends       int      `json:"backends"`
	BackendsDown   []string `json:"backendsDown"`
	ServersUp      int      `json:"serversUp"`
	ServersDown    []string `json:"serversDown"`
	Sessions       int64    `json:"sessions"`
	RequestsPerSec float64  `json:"requestsPerSec"`
	ErrorRate      float64  `json:"errorRate"`
}

type HAProxyCollector struct {
	name, url string
	client    *http.Client
	rates     counterRate
}

func NewHAProxyCollector(t Target) Collector[HAProxyStats] {
	u := strings.TrimRight(t.URL, "/")
	if !strings.Contains(u, ";csv") {
		u += ";csv"
	}
	return &HAProxyCollector{name: t.Name, url: u, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *HAProxyCollector) rows(ctx context.Context) ([]HAProxyRow, error) {
	body, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return nil, err
	}
	return parseHAProxyCSV(string(body))
}

func parseHAProxyCSV(body string) ([]HAProxyRow, error) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "# pxname,svname") {
		return nil, errors.New("not an HAProxy stats CSV page")
	}
	col := map[string]int{}
	for i, name := range strings.Split(strings.TrimPrefix(lines[0], "# "), ",") {
		col[name] = i
	}
	var rows []HAProxyRow
	for _, line := range lines[1:] {
		f := strings.Split(line, ",")
		get := func(name string) string {
			if i, ok := col[name]; ok && i < len(f) {
				return f[i]
			}
			return ""
		}
		n := func(name string) int64 { v, _ := strconv.ParseInt(get(name), 10, 64); return v }
		row := HAProxyRow{Proxy: get("pxname"), Server: get("svname"), Status: get("status"), Sessions: n("scur"), MaxSessions: n("smax"), Limit: n("slim"), Total: n("stot"), Errors5xx: n("hrsp_5xx"), Check: strings.TrimSpace(strings.TrimPrefix(get("check_status"), "*")), LastChange: n("lastchg"), Weight: n("weight")}
		row.Rate, _ = strconv.ParseFloat(cmp.Or(get("req_rate"), get("rate")), 64)
		if row.Server == "FRONTEND" {
			row.Total = cmp.Or(n("req_tot"), row.Total)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (c *HAProxyCollector) Collect(ctx context.Context) HAProxyStats {
	s := HAProxyStats{Name: c.name, BackendsDown: []string{}, ServersDown: []string{}}
	rows, err := c.rows(ctx)
	if err != nil {
		s.Error = friendlyError(err)
		return s
	}
	s.Up = true
	var requests, failed float64
	for _, r := range rows {
		switch r.Server {
		case "FRONTEND":
			if r.Proxy == "stats" {
				continue
			}
			s.Frontends++
			s.Sessions += r.Sessions
			requests += float64(r.Total)
			failed += float64(r.Errors5xx)
		case "BACKEND":
			if r.Proxy == "stats" {
				continue
			}
			s.Backends++
			if strings.HasPrefix(r.Status, "DOWN") {
				s.BackendsDown = append(s.BackendsDown, r.Proxy)
			}
		default:
			switch {
			case strings.HasPrefix(r.Status, "UP"), r.Status == "no check", r.Status == "OPEN":
				s.ServersUp++
			case strings.HasPrefix(r.Status, "DOWN"), strings.HasPrefix(r.Status, "MAINT"), r.Status == "NOLB":
				s.ServersDown = append(s.ServersDown, r.Proxy+"/"+r.Server)
			}
		}
	}
	d, elapsed := c.rates.deltas(time.Now(), map[string]float64{"requests": requests, "failed": failed})
	if elapsed > 0 {
		s.RequestsPerSec = d["requests"] / elapsed
		if d["requests"] > 0 {
			s.ErrorRate = 100 * d["failed"] / d["requests"]
		}
	}
	return s
}

func (c *HAProxyCollector) Detail(ctx context.Context) (any, error) {
	rows, err := c.rows(ctx)
	if err != nil {
		return nil, err
	}
	return struct {
		Rows []HAProxyRow `json:"rows"`
	}{rows}, nil
}

type ApacheStats struct {
	Name           string  `json:"name"`
	Up             bool    `json:"up"`
	Error          string  `json:"error,omitempty"`
	Version        string  `json:"version"`
	MPM            string  `json:"mpm"`
	Uptime         float64 `json:"uptime"`
	RequestsPerSec float64 `json:"requestsPerSec"`
	BytesPerSec    float64 `json:"bytesPerSec"`
	Busy           int64   `json:"busy"`
	Idle           int64   `json:"idle"`
	Connections    int64   `json:"connections"`
}

type ApacheCollector struct {
	name, url string
	client    *http.Client
	rates     counterRate
}

func NewApacheCollector(t Target) Collector[ApacheStats] {
	u := t.URL
	if !strings.Contains(u, "?auto") {
		u += "?auto"
	}
	return &ApacheCollector{name: t.Name, url: u, client: &http.Client{Timeout: 5 * time.Second}}
}

func (c *ApacheCollector) status(ctx context.Context) (map[string]string, error) {
	body, err := httpGet(ctx, c.client, c.url)
	if err != nil {
		return nil, err
	}
	status := map[string]string{}
	for line := range strings.SplitSeq(string(body), "\n") {
		if k, v, ok := strings.Cut(line, ": "); ok {
			status[k] = strings.TrimSpace(v)
		}
	}
	if status["Total Accesses"] == "" && status["BusyWorkers"] == "" {
		return nil, errors.New("not an Apache mod_status page")
	}
	return status, nil
}

func (c *ApacheCollector) Collect(ctx context.Context) ApacheStats {
	s := ApacheStats{Name: c.name}
	status, err := c.status(ctx)
	if err != nil {
		s.Error = friendlyError(err)
		return s
	}
	n := func(k string) float64 { v, _ := strconv.ParseFloat(status[k], 64); return v }
	s.Up = true
	s.Version, _, _ = strings.Cut(strings.TrimPrefix(status["ServerVersion"], "Apache/"), " ")
	s.MPM = status["ServerMPM"]
	s.Uptime = n("ServerUptimeSeconds")
	s.Busy, s.Idle, s.Connections = int64(n("BusyWorkers")), int64(n("IdleWorkers")), int64(n("ConnsTotal"))
	d, elapsed := c.rates.deltas(time.Now(), map[string]float64{"accesses": n("Total Accesses"), "kbytes": n("Total kBytes")})
	if elapsed > 0 {
		s.RequestsPerSec = d["accesses"] / elapsed
		s.BytesPerSec = 1024 * d["kbytes"] / elapsed
	}
	return s
}

var scoreboardStates = []struct {
	key   byte
	label string
}{
	{'_', "Waiting for a connection"}, {'S', "Starting up"}, {'R', "Reading the request"}, {'W', "Sending the reply"},
	{'K', "Keeping the connection alive"}, {'D', "Looking up DNS"}, {'C', "Closing the connection"}, {'L', "Logging"},
	{'G', "Finishing gracefully"}, {'I', "Cleaning up idle workers"}, {'.', "Open slot, no process"},
}

func (c *ApacheCollector) Detail(ctx context.Context) (any, error) {
	status, err := c.status(ctx)
	if err != nil {
		return nil, err
	}
	workers := []Setting{}
	board := status["Scoreboard"]
	for _, st := range scoreboardStates {
		if n := strings.Count(board, string(st.key)); n > 0 {
			workers = append(workers, Setting{Name: st.label, Value: strconv.Itoa(n)})
		}
	}
	server := []Setting{}
	for _, k := range []string{"ServerVersion", "ServerMPM", "Server Built", "RestartTime", "ServerUptime", "Total Accesses", "Total kBytes", "ReqPerSec", "BytesPerReq", "DurationPerReq", "Processes", "ConnsTotal", "ConnsAsyncKeepAlive", "ConnsAsyncWriting", "ConnsAsyncClosing", "Load1", "Load5", "Load15"} {
		if v, ok := status[k]; ok {
			server = append(server, Setting{Name: k, Value: v})
		}
	}
	return struct {
		Workers []Setting `json:"workers"`
		Server  []Setting `json:"server"`
	}{workers, server}, nil
}

func caddyMetrics(s CaddyStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"requests": s.RequestsPerSec, "healthy": float64(len(s.Upstreams) - len(s.Unhealthy)), "unhealthy": float64(len(s.Unhealthy)), "inFlight": s.InFlight, "errorRate": s.ErrorRate, "latency": s.Latency})
}

func traefikMetrics(s TraefikStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"requests": s.RequestsPerSec, "serversUp": float64(s.ServersUp), "serversDown": float64(len(s.ServersDown)), "errorRate": s.ErrorRate, "connections": s.Connections})
}

func haproxyMetrics(s HAProxyStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"requests": s.RequestsPerSec, "sessions": float64(s.Sessions), "serversUp": float64(s.ServersUp), "serversDown": float64(len(s.ServersDown)), "errorRate": s.ErrorRate})
}

func apacheMetrics(s ApacheStats) map[string]float64 {
	return withUp(s.Up, map[string]float64{"requests": s.RequestsPerSec, "bytes": s.BytesPerSec, "busy": float64(s.Busy), "idle": float64(s.Idle)})
}
