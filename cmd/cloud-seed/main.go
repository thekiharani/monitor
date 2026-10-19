package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/probe"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

const (
	historyDays        = 30
	extendedDailyDays  = 90
	partitionsAhead    = 7
	resultBudget       = 1_400_000
	confirmRetryDelay  = 30 * time.Second
	exhaustedAttempts  = 8
	defaultTimeoutMs   = 10000
	day                = 24 * time.Hour
	apiKeyMarker       = "usk_test_"
	webhookSecretBytes = 32
)

type settings struct {
	databaseURL     string
	encryptionKey   []byte
	regions         []string
	dashboardOrigin string
	apiOrigin       string
	currency        string
	starterPrice    int64
}

type person struct {
	id, email, name string
}

type membership struct {
	who  *person
	role string
}

type outage struct {
	ago, length  time.Duration
	status       int
	cause        string
	acknowledger *person
	failStart    time.Time
	failEnd      time.Time
	incidentID   string
	startedAt    time.Time
	resolvedAt   *time.Time
	silenced     bool
}

func (o *outage) open() bool { return o.length == 0 }

type monitor struct {
	id, name, kind, target, keyword, method string
	interval, grace                         int
	baselineMs                              float64
	certInDays                              int
	createdAgo, pausedAgo                   time.Duration
	pending                                 bool
	outages                                 []*outage
	regions                                 []string
	heartbeatToken                          string
	step                                    time.Duration
	gridStart, gridEnd                      time.Time
	createdAt                               time.Time
	pausedAt                                *time.Time
	certExpiresAt                           *time.Time
	state                                   string
	stateChangedAt                          time.Time
}

type channelConfig struct {
	Emails     []string `json:"emails,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`
	TopicID    int64    `json:"topic_id,omitempty"`
	WebhookURL string   `json:"webhook_url,omitempty"`
	URL        string   `json:"url,omitempty"`
	Secret     string   `json:"secret,omitempty"`
}

type channel struct {
	id, kind, name string
	cfg            channelConfig
	monitorNames   []string
	failing        string
}

type alertEvent struct {
	Kind          string     `json:"event"`
	MonitorID     string     `json:"monitor_id,omitempty"`
	MonitorName   string     `json:"monitor_name"`
	Workspace     string     `json:"workspace,omitempty"`
	Target        string     `json:"target,omitempty"`
	DashboardURL  string     `json:"dashboard_url,omitempty"`
	IncidentID    string     `json:"incident_id,omitempty"`
	Cause         string     `json:"cause,omitempty"`
	RegionsDown   []string   `json:"regions_down,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	ResolvedAt    *time.Time `json:"resolved_at,omitempty"`
	CertExpiresAt *time.Time `json:"cert_expires_at,omitempty"`
}

type maintenance struct {
	title        string
	startsIn     time.Duration
	length       time.Duration
	monitorNames []string
	creator      *person
	startsAt     time.Time
	endsAt       time.Time
	monitorIDs   []string
}

type apiKey struct {
	name   string
	owner  *person
	secret string
}

type account struct {
	id, name, plan string
	createdAgo     time.Duration
	members        []membership
	monitors       []*monitor
	channels       []*channel
	windows        []*maintenance
	apiKeys        []*apiKey
	apiRequests    int64
	createdAt      time.Time
}

func (a *account) monitor(name string) *monitor {
	for _, m := range a.monitors {
		if m.name == name {
			return m
		}
	}
	panic("unknown monitor " + name)
}

func (a *account) owner() *person {
	for _, m := range a.members {
		if m.role == "owner" {
			return m.who
		}
	}
	panic("account without owner " + a.name)
}

type seeder struct {
	cfg     settings
	tx      pgx.Tx
	now     time.Time
	rng     *mathrand.Rand
	people  []*person
	ops     *person
	acme    *account
	kibanda *account
	solo    *account
	noria   *account
	probes  []seededProbe
}

type seededProbe struct {
	region, token string
}

func main() {
	run := run
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		run = serveTargets
	}
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

const (
	demoHTTPAddress    = "127.0.0.1:8920"
	demoTCPAddress     = "127.0.0.1:8921"
	heartbeatPingEvery = time.Minute
	brokenHeartbeat    = "Invoice mailer cron"
)

var brokenPaths = map[string]int{"/mpesa/stk/health": http.StatusServiceUnavailable}

func serveTargets(ctx context.Context) error {
	cfg, err := loadSettings()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	tcpListener, err := net.Listen("tcp", demoTCPAddress)
	if err != nil {
		return err
	}
	defer tcpListener.Close()
	go func() {
		for {
			conn, err := tcpListener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	server := &http.Server{Addr: demoHTTPAddress, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(demoTarget)}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	go pingHeartbeats(ctx, cfg)
	fmt.Printf("serving seeded targets on http://*.localhost:8920 and tcp %s; pinging healthy heartbeats every %s\n", demoTCPAddress, heartbeatPingEvery)
	fmt.Println("the dev server must run with PROBE_ALLOW_PRIVATE=true to reach them")
	if err = server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func demoTarget(w http.ResponseWriter, r *http.Request) {
	time.Sleep(time.Duration(15+mathrand.IntN(90)) * time.Millisecond)
	if status, broken := brokenPaths[r.URL.Path]; broken {
		http.Error(w, "upstream unavailable", status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><title>%s</title><p>%s is healthy. Pay with M-Pesa. 12 results. Latest posts.</p>", r.Host, r.Host)
}

func pingHeartbeats(ctx context.Context, cfg settings) {
	ticker := time.NewTicker(heartbeatPingEvery)
	defer ticker.Stop()
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		conn, err := pgx.Connect(ctx, cfg.databaseURL)
		if err == nil {
			rows, queryErr := conn.Query(ctx, `SELECT heartbeat_token FROM monitors WHERE kind='heartbeat' AND deleted_at IS NULL AND paused_at IS NULL AND name<>$1`, brokenHeartbeat)
			tokens, collectErr := pgx.CollectRows(rows, pgx.RowTo[string])
			if err = errors.Join(queryErr, collectErr); err == nil {
				for _, token := range tokens {
					if resp, pingErr := client.Post(cfg.apiOrigin+"/ping/"+token, "text/plain", nil); pingErr == nil {
						_ = resp.Body.Close()
					}
				}
			}
			_ = conn.Close(context.WithoutCancel(ctx))
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "heartbeat pings:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func run(ctx context.Context) error {
	cfg, err := loadSettings()
	if err != nil {
		return err
	}
	started := time.Now()
	conn, err := pgx.Connect(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	s := &seeder{cfg: cfg, tx: tx, now: time.Now().UTC().Truncate(time.Second), rng: mathrand.New(mathrand.NewPCG(20261016, 42))}
	s.define()
	if err = s.seed(ctx); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return s.report(ctx, conn, time.Since(started))
}

func loadSettings() (settings, error) {
	if os.Getenv("APP_ENV") != "development" {
		return settings{}, errors.New("refusing to run: APP_ENV must be development")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return settings{}, errors.New("DATABASE_URL is required")
	}
	parsed, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return settings{}, fmt.Errorf("DATABASE_URL: %w", err)
	}
	if !slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, parsed.Host) {
		return settings{}, fmt.Errorf("refusing to run: DATABASE_URL host %q is not local", parsed.Host)
	}
	raw := strings.TrimSpace(os.Getenv("APP_ENCRYPTION_KEY"))
	if raw == "" {
		return settings{}, errors.New("APP_ENCRYPTION_KEY is required")
	}
	key, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return settings{}, fmt.Errorf("APP_ENCRYPTION_KEY must be unpadded base64: %w", err)
	}
	if len(key) != 32 {
		return settings{}, errors.New("APP_ENCRYPTION_KEY must decode to exactly 32 bytes")
	}
	regions := splitCSV(envOr("PROBE_REGIONS", "local"))
	if len(regions) == 0 || len(regions) > 16 {
		return settings{}, errors.New("PROBE_REGIONS must name 1 to 16 regions")
	}
	price, err := strconv.ParseInt(envOr("STARTER_PRICE_KES_MINOR", "50000"), 10, 64)
	if err != nil || price <= 0 {
		price = 50000
	}
	return settings{
		databaseURL:     databaseURL,
		encryptionKey:   key,
		regions:         regions,
		dashboardOrigin: strings.TrimRight(envOr("DASHBOARD_ORIGIN", "http://localhost:3910"), "/"),
		apiOrigin:       strings.TrimRight(envOr("API_ORIGIN", "http://localhost:8910"), "/"),
		currency:        envOr("PAYSTACK_CURRENCY", "KES"),
		starterPrice:    price,
	}, nil
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func splitCSV(value string) []string {
	var out []string
	for part := range strings.SplitSeq(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s *seeder) person(email, name string) *person {
	p := &person{id: newID(), email: email, name: name}
	s.people = append(s.people, p)
	return p
}

func ago(days, hours, minutes int) time.Duration {
	return time.Duration(days)*day + time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute
}

func web(name, target string, interval int, baseline float64, outages ...*outage) *monitor {
	return &monitor{name: name, kind: "http", target: target, method: "GET", interval: interval, baselineMs: baseline, outages: outages}
}

func tcp(name, target string, interval int, baseline float64, outages ...*outage) *monitor {
	return &monitor{name: name, kind: "tcp", target: "tcp://" + target, method: "GET", interval: interval, baselineMs: baseline, outages: outages}
}

func keyword(name, target, word string, interval int, baseline float64, outages ...*outage) *monitor {
	m := web(name, target, interval, baseline, outages...)
	m.kind, m.keyword = "keyword", word
	return m
}

func heartbeat(name string, interval, grace int, outages ...*outage) *monitor {
	return &monitor{name: name, kind: "heartbeat", method: "GET", interval: interval, grace: grace, outages: outages}
}

func fault(at, length time.Duration, status int, cause string, acknowledger *person) *outage {
	return &outage{ago: at, length: length, status: status, cause: cause, acknowledger: acknowledger}
}

func (s *seeder) define() {
	s.ops = s.person("ops@noria.test", "Noria Ops")
	amina := s.person("amina@acme.test", "Amina Hassan")
	brian := s.person("brian@acme.test", "Brian Kiprop")
	chloe := s.person("chloe@acme.test", "Chloe Atieno")
	wanjiru := s.person("wanjiru@kibanda.test", "Wanjiru Kamau")
	otieno := s.person("otieno@kibanda.test", "Otieno Odhiambo")
	sam := s.person("sam@solo.test", "Sam Mwangi")

	docs := web("Developer docs", "http://docs.acme.localhost:8920/", 300, 140)
	docs.certInDays = 9
	legacy := web("Legacy admin console", "http://legacy.acme.localhost:8920/login", 300, 420)
	legacy.pausedAgo = ago(5, 3, 0)
	edge := web("Nairobi edge", "http://ke.acme.localhost:8920/healthz", 60, 35)
	edge.pending = true
	pingHead := web("Webhook receiver", "http://hooks.acme.localhost:8920/ping", 300, 95)
	pingHead.method = "HEAD"
	github := web("GitHub API", "https://api.github.com/", 300, 210, fault(ago(8, 4, 0), 14*time.Minute, 500, "returning HTTP 500", nil))
	github.createdAgo = 45 * day

	s.acme = &account{
		name: "Acme Cloud", plan: "business", createdAgo: 140 * day, apiRequests: 18432,
		members: []membership{{amina, "owner"}, {brian, "admin"}, {chloe, "member"}},
		monitors: []*monitor{
			web("Marketing site", "http://acme.localhost:8920/", 60, 180, fault(ago(6, 2, 0), 5*time.Minute, 0, "resetting the connection", nil)),
			web("Customer API", "http://api.acme.localhost:8920/healthz", 30, 48,
				fault(ago(26, 3, 0), 18*time.Minute, 503, "returning HTTP 503", brian),
				fault(ago(9, 5, 0), 7*time.Minute, 0, "not responding", nil)),
			keyword("Checkout", "http://shop.acme.localhost:8920/checkout", "Pay with M-Pesa", 60, 260,
				fault(ago(14, 8, 0), 23*time.Minute, 200, `missing "Pay with M-Pesa"`, chloe)),
			web("Auth service", "http://auth.acme.localhost:8920/.well-known/openid-configuration", 60, 72),
			web("M-Pesa callback relay", "http://payments.acme.localhost:8920/mpesa/health", 30, 64,
				fault(ago(19, 2, 0), 47*time.Minute, 502, "returning HTTP 502", amina),
				fault(ago(4, 11, 0), 12*time.Minute, 0, "not responding", brian),
				fault(ago(1, 6, 0), 6*time.Minute, 504, "returning HTTP 504", nil)),
			docs,
			pingHead,
			tcp("Postgres primary", "db.acme.localhost:8921", 60, 6, fault(ago(12, 1, 0), 9*time.Minute, 0, "refusing connections", nil)),
			tcp("Redis cache", "cache.acme.localhost:8921", 60, 3),
			tcp("Cloudflare DNS", "1.1.1.1:53", 120, 12),
			github,
			heartbeat("Nightly backup", 86400, 3600, fault(ago(22, 0, 0), 36*time.Hour, 0, "", nil)),
			heartbeat("Invoice mailer cron", 900, 300,
				fault(ago(17, 4, 0), 2*time.Hour, 0, "", brian),
				fault(ago(0, 3, 0), 0, 0, "", nil)),
			legacy,
			edge,
		},
		windows: []*maintenance{
			{title: "Postgres 17 upgrade", startsIn: -ago(12, 1, 15), length: 2 * time.Hour, monitorNames: []string{"Postgres primary", "Customer API"}, creator: amina},
			{title: "Docs platform migration", startsIn: -30 * time.Minute, length: 2 * time.Hour, monitorNames: []string{"Developer docs"}, creator: brian},
			{title: "Kubernetes node pool rotation", startsIn: ago(3, 2, 0), length: 90 * time.Minute, creator: brian},
		},
		apiKeys: []*apiKey{{name: "CI deploys", owner: amina}, {name: "Terraform", owner: brian}},
	}
	s.acme.channels = []*channel{
		{kind: "email", name: "On-call email", cfg: channelConfig{Emails: []string{"amina@acme.test", "brian@acme.test"}}},
		{kind: "slack", name: "#ops-alerts", cfg: channelConfig{WebhookURL: "https://hooks.slack.com/services/T0ACME0001/B0OPS00001/dev" + token(9)}},
		{kind: "webhook", name: "Incident bridge", cfg: channelConfig{URL: "https://incidents.acme.example.com/hooks/noria"}, monitorNames: []string{"Customer API", "M-Pesa callback relay", "Postgres primary", "Checkout"}},
		{kind: "telegram", name: "Payments team", cfg: channelConfig{ChatID: "-1001987654321"}, monitorNames: []string{"M-Pesa callback relay", "Checkout"}},
	}

	stk := web("M-Pesa STK webhook", "http://api.kibanda.localhost:8920/mpesa/stk/health", 120, 110,
		fault(ago(11, 3, 0), 35*time.Minute, 0, "not responding", wanjiru),
		fault(ago(0, 0, 47), 0, 502, "returning HTTP 502", nil))
	s.kibanda = &account{
		name: "Kibanda Shop", plan: "starter", createdAgo: 75 * day, apiRequests: 2210,
		members: []membership{{wanjiru, "owner"}, {otieno, "member"}, {brian, "member"}},
		monitors: []*monitor{
			web("Storefront", "http://kibanda.localhost:8920/", 120, 320, fault(ago(21, 7, 0), 72*time.Minute, 503, "returning HTTP 503", otieno)),
			keyword("Product search", "http://kibanda.localhost:8920/search?q=sukuma", "results", 300, 410),
			stk,
			web("Orders API", "http://api.kibanda.localhost:8920/v1/health", 120, 90, fault(ago(5, 9, 0), 16*time.Minute, 500, "returning HTTP 500", nil)),
			tcp("SMTP relay", "smtp.kibanda.localhost:8921", 300, 28, fault(ago(15, 0, 0), 25*time.Minute, 0, "not responding", nil)),
			heartbeat("Stock sync job", 3600, 600),
		},
		apiKeys: []*apiKey{{name: "Shop backend", owner: wanjiru}},
	}
	s.kibanda.channels = []*channel{
		{kind: "email", name: "Shop owner", cfg: channelConfig{Emails: []string{"wanjiru@kibanda.test"}}},
		{kind: "webhook", name: "Order system hook", cfg: channelConfig{URL: "https://api.kibanda.example.com/hooks/noria"}, monitorNames: []string{"M-Pesa STK webhook", "Orders API"}, failing: "returning HTTP 500"},
		{kind: "telegram", name: "Shop group", cfg: channelConfig{ChatID: "-1004455667788"}},
	}

	s.solo = &account{
		name: "Solo Dev", plan: "free", createdAgo: 40 * day,
		members: []membership{{sam, "owner"}},
		monitors: []*monitor{
			web("Portfolio", "http://sam.localhost:8920/", 300, 240),
			web("Side project API", "http://api.sam.localhost:8920/health", 300, 160, fault(ago(13, 2, 0), 220*time.Minute, 0, "not resolving", nil)),
			keyword("Blog", "http://blog.sam.localhost:8920/", "Latest posts", 300, 380),
		},
	}
	s.solo.channels = []*channel{{kind: "email", name: "Me", cfg: channelConfig{Emails: []string{"sam@solo.test"}}}}

	s.noria = &account{
		name: "Noria Labs", plan: "pro", createdAgo: 200 * day,
		members: []membership{{s.ops, "owner"}},
		monitors: []*monitor{
			web("Monitor dashboard", "http://monitor.noria.localhost:8920/", 60, 85),
			web("Public status pages", "http://status.noria.localhost:8920/", 60, 70, fault(ago(7, 0, 0), 10*time.Minute, 502, "returning HTTP 502", s.ops)),
			tcp("Cloudflare DNS secondary", "1.0.0.1:53", 300, 11),
		},
	}
	s.noria.channels = []*channel{
		{kind: "email", name: "Ops inbox", cfg: channelConfig{Emails: []string{"ops@noria.test"}}},
		{kind: "slack", name: "#noria-ops", cfg: channelConfig{WebhookURL: "https://hooks.slack.com/services/T0NORIA001/B0OPS00002/dev" + token(9)}},
	}
}

func (s *seeder) accounts() []*account { return []*account{s.noria, s.acme, s.kibanda, s.solo} }

func (s *seeder) seed(ctx context.Context) error {
	steps := []func(context.Context) error{
		s.truncate, s.createPartitions, s.insertPeople, s.insertAccounts, s.insertOperatorGrants, s.insertBilling,
		s.insertMonitors, s.insertHistory, s.insertDaily, s.insertIncidents, s.finishMonitors, s.insertSchedules,
		s.insertMaintenance, s.insertChannels, s.insertNotifications, s.insertStatusPages, s.insertAPIKeys, s.insertUsage, s.insertAudit, s.insertProbes,
	}
	if _, err := s.tx.Exec(ctx, `SET LOCAL lock_timeout = '15s'`); err != nil {
		return err
	}
	if _, err := s.tx.Exec(ctx, `SELECT setseed(0.2026)`); err != nil {
		return err
	}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) insertProbes(ctx context.Context) error {
	for _, region := range s.cfg.regions {
		if region == "local" {
			continue
		}
		seeded := seededProbe{region: region, token: probe.TokenPrefix + token(32)}
		if err := s.exec(ctx, "probes", `INSERT INTO probes(region,name,key_prefix,secret_hash,creator_id) VALUES($1,$2,$3,$4,$5)`,
			region, region+"-dev", seeded.token[:len(probe.TokenPrefix)+6], config.HashSecret(seeded.token), s.ops.id); err != nil {
			return err
		}
		s.probes = append(s.probes, seeded)
	}
	return nil
}

func (s *seeder) exec(ctx context.Context, table, sql string, args ...any) error {
	if _, err := s.tx.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	return nil
}

func (s *seeder) truncate(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, `
		SELECT t.table_name::text FROM information_schema.tables t
		JOIN pg_class c ON c.relname = t.table_name AND c.relnamespace = 'public'::regnamespace
		WHERE t.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		  AND t.table_name <> 'schema_migrations' AND NOT c.relispartition
		ORDER BY t.table_name`)
	if err != nil {
		return err
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return errors.New("no application tables found; run the migrations first")
	}
	quoted := make([]string, len(tables))
	for i, name := range tables {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	_, err = s.tx.Exec(ctx, `TRUNCATE `+strings.Join(quoted, ", ")+` CASCADE`)
	return err
}

func partitionName(d time.Time) string { return "check_results_p" + d.Format("20060102") }

func (s *seeder) historyStart() time.Time { return s.now.Add(-historyDays * day) }

func (s *seeder) createPartitions(ctx context.Context) error {
	today := s.now.Truncate(day)
	for d := s.historyStart().Truncate(day); !d.After(today.AddDate(0, 0, partitionsAhead)); d = d.AddDate(0, 0, 1) {
		statement := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF check_results FOR VALUES FROM ('%s') TO ('%s')`,
			partitionName(d), d.Format(time.RFC3339), d.AddDate(0, 0, 1).Format(time.RFC3339))
		if _, err := s.tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("partition %s: %w", partitionName(d), err)
		}
	}
	return nil
}

func (s *seeder) insertPeople(ctx context.Context) error {
	for i, p := range s.people {
		created := s.now.Add(-time.Duration(200-i*7) * day)
		if err := s.exec(ctx, "principals",
			`INSERT INTO principals(id,kind,display_name,timezone,created_at,updated_at) VALUES($1,'human',$2,'Africa/Nairobi',$3,$3)`,
			p.id, p.name, created); err != nil {
			return err
		}
		if err := s.exec(ctx, "principal_identities",
			`INSERT INTO principal_identities(principal_id,provider,provider_subject,verified_email,created_at,last_used_at) VALUES($1,'email_otp',$2,$2,$3,$4)`,
			p.id, p.email, created, s.now.Add(-time.Duration(1+i)*time.Hour)); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) insertAccounts(ctx context.Context) error {
	for _, a := range s.accounts() {
		a.id = newID()
		a.createdAt = s.now.Add(-a.createdAgo)
		if err := s.exec(ctx, "accounts",
			`INSERT INTO accounts(id,name,status,plan,created_at,updated_at) VALUES($1,$2,'active',$3,$4,$4)`,
			a.id, a.name, a.plan, a.createdAt); err != nil {
			return err
		}
		for i, m := range a.members {
			joined := a.createdAt.Add(time.Duration(i) * 6 * day)
			if err := s.exec(ctx, "account_memberships",
				`INSERT INTO account_memberships(principal_id,account_id,role,status,created_at,updated_at) VALUES($1,$2,$3,'active',$4,$4)`,
				m.who.id, a.id, m.role, joined); err != nil {
				return err
			}
		}
	}
	return s.exec(ctx, "account_invites",
		`INSERT INTO account_invites(account_id,email,role,token_hash,inviter_id,expires_at,created_at) VALUES($1,'dan@acme.test','member',$2,$3,$4,$5)`,
		s.acme.id, config.HashSecret(token(32)), s.acme.owner().id, s.now.Add(5*day), s.now.Add(-2*day))
}

func (s *seeder) insertOperatorGrants(ctx context.Context) error {
	capabilities := slices.Sorted(slices.Values(access.PermissionSets["platform.owner"]))
	const reason = "platform owner provisioned by the development seed"
	granted := s.now.Add(-180 * day)
	for _, capability := range capabilities {
		if err := s.exec(ctx, "access_grants",
			`INSERT INTO access_grants(principal_id,capability,scope_type,scope_id,grantor_id,reason,created_at) VALUES($1,$2,$3,NULL,$1,$4,$5)`,
			s.ops.id, capability, access.ScopePlatform, reason, granted); err != nil {
			return err
		}
	}
	after, err := json.Marshal(map[string]any{"permission_set": "platform.owner", "scope_type": access.ScopePlatform, "email": s.ops.email, "capabilities": capabilities})
	if err != nil {
		return err
	}
	return s.exec(ctx, "audit_events",
		`INSERT INTO audit_events(principal_id,capability,action,target_type,target_id,reason,after_state,created_at) VALUES($1,$2,'access.granted','principal',$6,$3,$4,$5)`,
		s.ops.id, access.AccessGrant, reason, after, granted, s.ops.id)
}

func (s *seeder) insertBilling(ctx context.Context) error {
	compStart := s.now.Add(-60 * day)
	compEnd := s.now.Add(305 * day)
	if err := s.exec(ctx, "billing_subscriptions",
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,effective_at,created_at,updated_at)
		 VALUES($1,'internal',$2,'business','active',0,$3,$4,$5,'comp',$4,$4,$4)`,
		s.acme.id, "COMP-"+newID(), s.cfg.currency, compStart, compEnd); err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{"plan": "business", "billing_mode": "comp", "expires_at": compEnd})
	if err != nil {
		return err
	}
	if err = s.exec(ctx, "audit_events",
		`INSERT INTO audit_events(account_id,principal_id,capability,action,target_type,target_id,reason,after_state,created_at) VALUES($1,$2,$3,'billing.complimentary_granted','account',$6,'design partner programme',$4,$5)`,
		s.acme.id, s.ops.id, access.BillingComp, after, compStart, s.acme.id); err != nil {
		return err
	}
	checkoutID := newID()
	paid := s.now.Add(-12 * day)
	accessCode := strings.ToLower(token(10))
	if err = s.exec(ctx, "billing_checkouts",
		`INSERT INTO billing_checkouts(id,account_id,provider,provider_reference,provider_access_code,provider_transaction_id,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,authorization_url,completed_at,created_at,updated_at)
		 VALUES($1,$2,'paystack',$3,$4,$5,'starter',$6,$7,'successful','subscription','auto',$6,$8,$9,$10,$9)`,
		checkoutID, s.kibanda.id, "bil-"+checkoutID, accessCode, strconv.Itoa(4100000000+s.rng.IntN(99999999)), s.cfg.starterPrice, s.cfg.currency,
		"https://checkout.paystack.com/"+accessCode, paid, paid.Add(-3*time.Minute)); err != nil {
		return err
	}
	sealedToken, err := config.Seal(s.cfg.encryptionKey, strings.ToLower(token(12)))
	if err != nil {
		return err
	}
	return s.exec(ctx, "billing_subscriptions",
		`INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,effective_at,created_at,updated_at)
		 VALUES($1,'paystack',$2,$3,$4,'starter','active',$5,$6,$7,$8,$7,$7,$7)`,
		s.kibanda.id, "CUS_"+strings.ToLower(token(10)), "SUB_"+strings.ToLower(token(10)), sealedToken, s.cfg.starterPrice, s.cfg.currency, paid, paid.AddDate(0, 1, 0))
}

func (s *seeder) historyThinning() int64 {
	var estimate int64
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			regions := max(1, len(m.regions))
			estimate += int64(regions) * int64(historyDays*86400/m.interval)
		}
	}
	return (estimate + resultBudget - 1) / resultBudget
}

func (s *seeder) planMonitors() {
	for _, a := range s.accounts() {
		limits := usage.LimitsFor(a.plan)
		for _, m := range a.monitors {
			m.interval = max(m.interval, limits.MinIntervalSeconds)
			if m.kind != "heartbeat" {
				m.regions = slices.Sorted(slices.Values(s.cfg.regions[:min(len(s.cfg.regions), limits.MaxRegions, 3)]))
			}
		}
	}
	factor := time.Duration(max(1, s.historyThinning()))
	for _, a := range s.accounts() {
		for i, m := range a.monitors {
			m.id = newID()
			if m.createdAgo == 0 {
				m.createdAgo = a.createdAgo - time.Duration(2+i)*day
			}
			if m.pending {
				m.createdAgo = 20 * time.Minute
			}
			m.createdAt = s.now.Add(-m.createdAgo)
			if m.kind == "heartbeat" {
				m.heartbeatToken = token(24)
			}
			if strings.HasPrefix(m.target, "https://") {
				days := m.certInDays
				if days == 0 {
					days = 35 + s.rng.IntN(50)
				}
				expires := s.now.Add(time.Duration(days)*day + time.Duration(s.rng.IntN(86400))*time.Second)
				m.certExpiresAt = &expires
			}
			m.step = time.Duration(m.interval) * time.Second * factor
			start := latest(s.historyStart(), m.createdAt).Truncate(m.step).Add(time.Duration(s.rng.IntN(int(m.step/time.Second))) * time.Second)
			if start.Before(s.historyStart().Truncate(day)) {
				start = start.Add(m.step)
			}
			m.gridStart = start
			end := s.now
			if m.pausedAgo > 0 {
				paused := s.now.Add(-m.pausedAgo)
				m.pausedAt = &paused
				end = paused
			}
			m.gridEnd = m.floorGrid(end)
			m.planOutages()
			m.settleState()
		}
	}
}

func (m *monitor) floorGrid(t time.Time) time.Time {
	return m.gridStart.Add(t.Sub(m.gridStart) / m.step * m.step)
}

func (m *monitor) ceilGrid(t time.Time) time.Time {
	f := m.floorGrid(t)
	if f.Before(t) {
		f = f.Add(m.step)
	}
	return f
}

func (m *monitor) planOutages() {
	now := m.gridEnd
	for _, o := range m.outages {
		o.incidentID = newID()
		begin := now.Add(-o.ago)
		o.failStart = m.ceilGrid(begin)
		if o.open() {
			o.failEnd = now.Add(day)
		} else {
			o.failEnd = latest(m.ceilGrid(begin.Add(o.length)), o.failStart.Add(m.minimumOutageSteps()*m.step))
			resolved := o.failEnd
			o.resolvedAt = &resolved
		}
		if m.kind == "heartbeat" {
			o.startedAt = o.failStart.Add(-m.step).Add(time.Duration(m.interval+m.grace) * time.Second)
			o.cause = "not pinging within " + humanDuration(time.Duration(m.interval+m.grace)*time.Second)
		} else {
			o.startedAt = o.failStart.Add(confirmRetryDelay)
		}
	}
}

func (m *monitor) minimumOutageSteps() time.Duration {
	if m.kind == "heartbeat" {
		return 1
	}
	return 2
}

func (m *monitor) settleState() {
	m.state, m.stateChangedAt = "up", m.createdAt
	for _, o := range m.outages {
		if o.resolvedAt != nil && o.resolvedAt.After(m.stateChangedAt) {
			m.stateChangedAt = *o.resolvedAt
		}
		if o.open() {
			m.state, m.stateChangedAt = "down", o.startedAt
		}
	}
	switch {
	case m.pausedAt != nil:
		m.state, m.stateChangedAt = "paused", *m.pausedAt
	case m.pending:
		m.state, m.stateChangedAt = "pending", m.createdAt
	}
}

func (m *monitor) openOutage() *outage {
	for _, o := range m.outages {
		if o.open() {
			return o
		}
	}
	return nil
}

func (s *seeder) insertMonitors(ctx context.Context) error {
	s.planMonitors()
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			var target, keyword, hbToken, lastError *string
			if m.kind != "heartbeat" {
				target = &m.target
			} else {
				hbToken = &m.heartbeatToken
			}
			if m.keyword != "" {
				keyword = &m.keyword
			}
			expected := []int{}
			if m.kind == "http" || m.kind == "keyword" {
				expected = []int{200}
			}
			if o := m.openOutage(); o != nil && m.kind == "heartbeat" {
				lastError = &o.cause
			}
			var warnedFor *time.Time
			if m.certInDays > 0 {
				warnedFor = m.certExpiresAt
			}
			regions := m.regions
			if regions == nil {
				regions = []string{}
			}
			if err := s.exec(ctx, "monitors",
				`INSERT INTO monitors(id,account_id,name,kind,target,method,expected_statuses,keyword,follow_redirects,interval_seconds,timeout_ms,regions,
					cert_warn_days,cert_warned_for,heartbeat_token,grace_seconds,state,state_changed_at,last_error,cert_expires_at,paused_at,created_at,updated_at)
				 VALUES($1,$2,$3,$4,$5,$6,$7,$8,true,$9,$10,$11,14,$12,$13,$14,$15,$16,$17,$18,$19,$20,$20)`,
				m.id, a.id, m.name, m.kind, target, m.method, expected, keyword, m.interval, min(defaultTimeoutMs, m.interval*1000-1000), regions,
				warnedFor, hbToken, m.grace, m.state, m.stateChangedAt, lastError, m.certExpiresAt, m.pausedAt, m.createdAt); err != nil {
				return err
			}
		}
	}
	return nil
}

const probeSeriesSQL = `
WITH grid AS MATERIALIZED (
	SELECT g.t, f.status AS fault_status, f.cause AS fault_cause, random() AS r1, random() AS r2, random() AS r3
	FROM generate_series($4::timestamptz, $5::timestamptz, make_interval(secs => $6)) AS g(t)
	LEFT JOIN LATERAL (
		SELECT o.status, o.cause FROM unnest($7::timestamptz[], $8::timestamptz[], $9::int[], $10::text[]) AS o(s, e, status, cause)
		WHERE g.t >= o.s AND g.t < o.e LIMIT 1
	) f ON true
), shaped AS (
	SELECT t, r1, r2,
		CASE WHEN fault_cause IS NOT NULL THEN fault_cause
		     WHEN r3 < 0.0008 AND t < $14::timestamptz - interval '1 hour' THEN 'not responding' END AS failure,
		CASE WHEN fault_cause IS NOT NULL THEN NULLIF(fault_status, 0)
		     WHEN r3 < 0.0008 AND t < $14::timestamptz - interval '1 hour' THEN NULL
		     WHEN $11 <> 'tcp' THEN 200 END AS status,
		$12::float8 * (0.82 + 0.36 * r1)
			* (1 + 0.18 * sin((extract(epoch FROM t) / 86400.0 - 0.25) * 2 * pi()))
			* CASE WHEN r2 > 0.994 THEN 2.5 + 5 * r3 ELSE 1 END AS latency
	FROM grid
), measured AS (
	SELECT t, r1, r2, failure, status, (failure IS NULL OR status IS NOT NULL) AS timed, latency FROM shaped
)
INSERT INTO check_results(monitor_id,account_id,region,checked_at,up,status_code,total_ms,dns_ms,connect_ms,tls_ms,ttfb_ms,error,cert_expires_at)
SELECT $1::uuid, $2::uuid, $3::varchar, t, failure IS NULL,
	CASE WHEN $11 = 'tcp' THEN NULL ELSE status END,
	CASE WHEN timed THEN round(latency::numeric, 2) END,
	CASE WHEN timed THEN round((latency * (0.03 + 0.05 * r1))::numeric, 2) END,
	CASE WHEN timed THEN round((latency * CASE WHEN $11 = 'tcp' THEN 0.82 ELSE 0.11 + 0.05 * r2 END)::numeric, 2) END,
	CASE WHEN timed AND $13 THEN round((latency * (0.17 + 0.06 * r1))::numeric, 2) END,
	CASE WHEN timed AND $11 <> 'tcp' THEN round((latency * (0.42 + 0.1 * r2))::numeric, 2) END,
	failure,
	CASE WHEN timed AND $13 THEN $15::timestamptz END
FROM measured`

const retrySQL = `
INSERT INTO check_results(monitor_id,account_id,region,checked_at,up,status_code,total_ms,dns_ms,connect_ms,tls_ms,ttfb_ms,error,cert_expires_at)
SELECT monitor_id, account_id, region, checked_at + $4::interval, up, status_code, total_ms * 1.07, dns_ms, connect_ms, tls_ms, ttfb_ms * 1.1, error, cert_expires_at
FROM check_results WHERE monitor_id = $1 AND region = $2 AND checked_at = $3 AND NOT up
ON CONFLICT DO NOTHING`

const heartbeatSeriesSQL = `
INSERT INTO check_results(monitor_id,account_id,region,checked_at,up)
SELECT $1::uuid, $2::uuid, 'heartbeat', g.t, true
FROM generate_series($3::timestamptz, $4::timestamptz, make_interval(secs => $5)) AS g(t)
WHERE NOT EXISTS (SELECT 1 FROM unnest($6::timestamptz[], $7::timestamptz[]) AS o(s, e) WHERE g.t >= o.s AND g.t < o.e)`

func (s *seeder) insertHistory(ctx context.Context) error {
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			if m.pending {
				continue
			}
			var starts, ends []time.Time
			var statuses []int
			var causes []string
			for _, o := range m.outages {
				starts, ends, statuses, causes = append(starts, o.failStart), append(ends, o.failEnd), append(statuses, o.status), append(causes, o.cause)
			}
			stepSeconds := int(m.step / time.Second)
			if m.kind == "heartbeat" {
				if err := s.exec(ctx, "check_results", heartbeatSeriesSQL, m.id, a.id, m.gridStart, m.gridEnd, stepSeconds, starts, ends); err != nil {
					return err
				}
				for _, o := range m.outages {
					if err := s.exec(ctx, "check_results",
						`INSERT INTO check_results(monitor_id,account_id,region,checked_at,up,error) VALUES($1,$2,'heartbeat',$3,false,$4) ON CONFLICT DO NOTHING`,
						m.id, a.id, o.startedAt, o.cause); err != nil {
						return err
					}
				}
				continue
			}
			https := strings.HasPrefix(m.target, "https://")
			for i, region := range m.regions {
				baseline := m.baselineMs * (1 + 0.35*float64(i))
				if err := s.exec(ctx, "check_results", probeSeriesSQL, m.id, a.id, region, m.gridStart, m.gridEnd, stepSeconds,
					starts, ends, statuses, causes, m.kind, baseline, https, s.now, m.certExpiresAt); err != nil {
					return err
				}
				if m.step <= confirmRetryDelay {
					continue
				}
				for _, o := range m.outages {
					if err := s.exec(ctx, "check_results", retrySQL, m.id, region, o.failStart, confirmRetryDelay.String()); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func (s *seeder) insertDaily(ctx context.Context) error {
	if err := s.exec(ctx, "monitor_daily", `
		INSERT INTO monitor_daily(monitor_id,account_id,day,checks,failures,response_ms_sum,response_count)
		SELECT monitor_id, account_id, (checked_at AT TIME ZONE 'UTC')::date, count(*), count(*) FILTER (WHERE NOT up),
			COALESCE(sum(total_ms::float8) FILTER (WHERE up AND total_ms IS NOT NULL), 0),
			count(*) FILTER (WHERE up AND total_ms IS NOT NULL)
		FROM check_results GROUP BY 1, 2, 3`); err != nil {
		return err
	}
	today := s.now.Truncate(day)
	for _, m := range s.acme.monitors {
		if m.pending {
			continue
		}
		from := latest(today.AddDate(0, 0, -(extendedDailyDays-1)), m.createdAt.Truncate(day).AddDate(0, 0, 1))
		to := m.gridStart.Truncate(day).AddDate(0, 0, -1)
		if to.Before(from) {
			continue
		}
		perDay := max(1, len(m.regions)) * int(day/m.step)
		if err := s.exec(ctx, "monitor_daily", `
			INSERT INTO monitor_daily(monitor_id,account_id,day,checks,failures,response_ms_sum,response_count)
			SELECT $1::uuid, $2::uuid, d, $5::int, f,
				CASE WHEN $7 THEN ($5 - f) * $6 * (0.9 + 0.2 * random()) ELSE 0 END,
				CASE WHEN $7 THEN $5 - f ELSE 0 END
			FROM (
				SELECT d::date AS d, CASE WHEN random() < 0.07 THEN 1 + floor(random() * least($5, 6))::int ELSE 0 END AS f
				FROM generate_series($3::date, $4::date, interval '1 day') AS d
			) days`,
			m.id, s.acme.id, from, to, perDay, m.baselineMs, m.kind != "heartbeat"); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) insertIncidents(ctx context.Context) error {
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			regionsDown := m.regions
			if regionsDown == nil {
				regionsDown = []string{}
			}
			for _, o := range m.outages {
				var acknowledgedAt *time.Time
				var acknowledgerID *string
				if o.acknowledger != nil {
					at := o.startedAt.Add(time.Duration(3+s.rng.IntN(15)) * time.Minute)
					if o.resolvedAt != nil {
						at = earliest(at, *o.resolvedAt)
					}
					acknowledgedAt, acknowledgerID = &at, &o.acknowledger.id
				}
				if err := s.exec(ctx, "incidents",
					`INSERT INTO incidents(id,monitor_id,account_id,started_at,resolved_at,cause,regions_down,acknowledged_at,acknowledger_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
					o.incidentID, m.id, a.id, o.startedAt, o.resolvedAt, o.cause, regionsDown, acknowledgedAt, acknowledgerID); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *seeder) finishMonitors(ctx context.Context) error {
	if err := s.exec(ctx, "monitor_last_state", `
		UPDATE monitors m SET last_checked_at = l.checked_at, last_status_code = l.status_code, last_response_ms = l.total_ms, last_error = l.error
		FROM (
			SELECT DISTINCT ON (monitor_id) monitor_id, checked_at, status_code, total_ms, error
			FROM check_results WHERE region <> 'heartbeat' ORDER BY monitor_id, checked_at DESC, region
		) l
		WHERE m.id = l.monitor_id AND m.kind <> 'heartbeat'`); err != nil {
		return err
	}
	return s.exec(ctx, "monitor_last_state", `
		UPDATE monitors m SET last_checked_at = (SELECT max(c.checked_at) FROM check_results c WHERE c.monitor_id = m.id AND c.up)
		WHERE m.kind = 'heartbeat'`)
}

func (s *seeder) insertSchedules(ctx context.Context) error {
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			if m.kind == "heartbeat" || m.pausedAt != nil {
				continue
			}
			for _, region := range m.regions {
				var lastChecked *time.Time
				next, failures := s.now, 0
				if !m.pending {
					last := m.gridEnd
					lastChecked, next = &last, last.Add(m.step)
					if o := m.openOutage(); o != nil {
						failures = int(m.gridEnd.Sub(o.failStart)/m.step) + 1
						if m.step > confirmRetryDelay {
							failures++
						}
					}
				}
				if err := s.exec(ctx, "monitor_schedules",
					`INSERT INTO monitor_schedules(monitor_id,account_id,region,next_run_at,consecutive_failures,last_checked_at) VALUES($1,$2,$3,$4,$5,$6)`,
					m.id, a.id, region, next, failures, lastChecked); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *seeder) insertMaintenance(ctx context.Context) error {
	for _, a := range s.accounts() {
		for _, w := range a.windows {
			w.startsAt = s.now.Add(w.startsIn).Truncate(time.Minute)
			w.endsAt = w.startsAt.Add(w.length)
			w.monitorIDs = []string{}
			for _, name := range w.monitorNames {
				w.monitorIDs = append(w.monitorIDs, a.monitor(name).id)
			}
			created := earliest(w.startsAt, s.now).Add(-2 * day)
			if err := s.exec(ctx, "maintenance_windows",
				`INSERT INTO maintenance_windows(account_id,title,starts_at,ends_at,monitor_ids,creator_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`,
				a.id, w.title, w.startsAt, w.endsAt, w.monitorIDs, w.creator.id, created); err != nil {
				return err
			}
			for _, m := range a.monitors {
				if len(w.monitorIDs) > 0 && !slices.Contains(w.monitorIDs, m.id) {
					continue
				}
				for _, o := range m.outages {
					if !o.startedAt.Before(w.startsAt) && o.startedAt.Before(w.endsAt) {
						o.silenced = true
					}
				}
			}
		}
	}
	return nil
}

func (s *seeder) insertChannels(ctx context.Context) error {
	for _, a := range s.accounts() {
		for i, c := range a.channels {
			c.id = newID()
			if c.kind == "webhook" {
				c.cfg.Secret = "whsec_" + token(webhookSecretBytes)
			}
			raw, err := json.Marshal(c.cfg)
			if err != nil {
				return err
			}
			sealed, err := config.Seal(s.cfg.encryptionKey, string(raw))
			if err != nil {
				return err
			}
			var lastError *string
			if c.failing != "" {
				lastError = &c.failing
			}
			created := a.createdAt.Add(time.Duration(1+i) * day)
			if err = s.exec(ctx, "alert_channels",
				`INSERT INTO alert_channels(id,account_id,kind,name,config_encrypted,all_monitors,enabled,last_error,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,true,$7,$8,$8)`,
				c.id, a.id, c.kind, c.name, sealed, len(c.monitorNames) == 0, lastError, created); err != nil {
				return err
			}
			for _, name := range c.monitorNames {
				if err = s.exec(ctx, "monitor_channels",
					`INSERT INTO monitor_channels(monitor_id,channel_id,account_id) VALUES($1,$2,$3)`, a.monitor(name).id, c.id, a.id); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *channel) covers(m *monitor) bool {
	return len(c.monitorNames) == 0 || slices.Contains(c.monitorNames, m.name)
}

func (s *seeder) event(a *account, m *monitor, kind string) alertEvent {
	e := alertEvent{Kind: kind, MonitorID: m.id, MonitorName: m.name, Workspace: a.name, DashboardURL: s.cfg.dashboardOrigin + "/app/monitors/" + m.id}
	if m.kind != "heartbeat" {
		e.Target, _, _ = strings.Cut(m.target, "?")
	}
	return e
}

func (s *seeder) notify(ctx context.Context, a *account, c *channel, m *monitor, incidentID *string, e alertEvent, at time.Time, failed bool) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	attempts, sentAt, next := 1, new(at.Add(time.Duration(2+s.rng.IntN(6))*time.Second)), at.Add(15*time.Second)
	var lastError, messageID *string
	if failed {
		attempts, sentAt, lastError = exhaustedAttempts, nil, &c.failing
		next = at.Add(time.Hour * exhaustedAttempts)
	} else if c.kind == "telegram" {
		messageID = new(strconv.Itoa(1000 + s.rng.IntN(90000)))
	}
	return s.exec(ctx, "notifications",
		`INSERT INTO notifications(account_id,channel_id,monitor_id,incident_id,event,payload,attempts,next_attempt_at,sent_at,last_error,provider_message_id,created_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.id, c.id, m.id, incidentID, e.Kind, payload, attempts, next, sentAt, lastError, messageID, at)
}

func (s *seeder) insertNotifications(ctx context.Context) error {
	for _, a := range s.accounts() {
		for _, m := range a.monitors {
			for _, o := range m.outages {
				if o.silenced {
					continue
				}
				down := s.event(a, m, "down")
				down.IncidentID, down.Cause, down.StartedAt = o.incidentID, o.cause, &o.startedAt
				down.RegionsDown = m.regions
				for _, c := range a.channels {
					if !c.covers(m) {
						continue
					}
					failed := c.failing != "" && o.open()
					if err := s.notify(ctx, a, c, m, &o.incidentID, down, o.startedAt, failed); err != nil {
						return err
					}
					if o.resolvedAt == nil {
						continue
					}
					up := s.event(a, m, "up")
					up.IncidentID, up.StartedAt, up.ResolvedAt = o.incidentID, &o.startedAt, o.resolvedAt
					if err := s.notify(ctx, a, c, m, &o.incidentID, up, *o.resolvedAt, false); err != nil {
						return err
					}
				}
			}
			if m.certInDays > 0 {
				warning := s.event(a, m, "certificate")
				warning.CertExpiresAt = m.certExpiresAt
				for _, c := range a.channels {
					if c.covers(m) {
						if err := s.notify(ctx, a, c, m, nil, warning, s.now.Add(-5*time.Hour), false); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return s.exec(ctx, "alert_channel_delivery", `
		UPDATE alert_channels c SET last_delivered_at = d.at
		FROM (SELECT channel_id, max(sent_at) AS at FROM notifications WHERE sent_at IS NOT NULL GROUP BY channel_id) d
		WHERE c.id = d.channel_id`)
}

type pageItem struct {
	monitor, group, display string
}

type pageUpdate struct {
	status, body string
	after        time.Duration
}

type statusPost struct {
	kind, title, impact, status string
	startsAt                    time.Time
	endsAt, resolvedAt          *time.Time
	author                      *person
	updates                     []pageUpdate
}

func (s *seeder) insertStatusPage(ctx context.Context, a *account, slug, title string, description, homepage *string, published bool, items []pageItem, posts []statusPost) error {
	pageID := newID()
	created := a.createdAt.Add(3 * day)
	if err := s.exec(ctx, "status_pages",
		`INSERT INTO status_pages(id,account_id,slug,title,description,homepage_url,theme,show_response_times,published,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,'auto',$7,$8,$9,$9)`,
		pageID, a.id, slug, title, description, homepage, published, published, created); err != nil {
		return err
	}
	for position, item := range items {
		var group, display *string
		if item.group != "" {
			group = &item.group
		}
		if item.display != "" {
			display = &item.display
		}
		if err := s.exec(ctx, "status_page_items",
			`INSERT INTO status_page_items(status_page_id,monitor_id,account_id,group_name,display_name,position) VALUES($1,$2,$3,$4,$5,$6)`,
			pageID, a.monitor(item.monitor).id, a.id, group, display, position); err != nil {
			return err
		}
	}
	for _, post := range posts {
		postID := newID()
		created := earliest(post.startsAt, s.now).Add(-time.Minute)
		if post.kind == "maintenance" {
			created = s.now.Add(-26 * time.Hour)
		}
		if err := s.exec(ctx, "status_posts",
			`INSERT INTO status_posts(id,status_page_id,account_id,kind,title,impact,status,starts_at,ends_at,resolved_at,author_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			postID, pageID, a.id, post.kind, post.title, post.impact, post.status, post.startsAt, post.endsAt, post.resolvedAt, post.author.id, created); err != nil {
			return err
		}
		for _, u := range post.updates {
			if err := s.exec(ctx, "status_post_updates",
				`INSERT INTO status_post_updates(status_post_id,account_id,status,body,author_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`,
				postID, a.id, u.status, u.body, post.author.id, created.Add(u.after)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *seeder) insertStatusPages(ctx context.Context) error {
	mpesa := s.acme.monitor("M-Pesa callback relay").outages[0]
	rotation := s.acme.windows[2]
	amina := s.acme.owner()
	if err := s.insertStatusPage(ctx, s.acme, "acme", "Acme Cloud", new("Live status of the Acme Cloud platform and payments."), new("https://acme.example.com"), true,
		[]pageItem{
			{"Marketing site", "Customer-facing", "Website"},
			{"Checkout", "Customer-facing", ""},
			{"Customer API", "Customer-facing", "Public API"},
			{"M-Pesa callback relay", "Payments", "M-Pesa payments"},
			{"Auth service", "Platform", "Sign-in"},
			{"Developer docs", "Platform", "Documentation"},
		},
		[]statusPost{
			{
				kind: "incident", title: "Delayed M-Pesa payment confirmations", impact: "major", status: "resolved",
				startsAt: mpesa.startedAt, resolvedAt: mpesa.resolvedAt, author: amina,
				updates: []pageUpdate{
					{"investigating", "Some M-Pesa payments are not being confirmed in checkout. We are investigating.", 0},
					{"identified", "The callback relay lost its connection to the upstream gateway after a certificate rotation. A fix is rolling out.", 18 * time.Minute},
					{"resolved", "Payment confirmations are flowing again and delayed callbacks have been replayed. No payments were lost.", mpesa.resolvedAt.Sub(mpesa.startedAt) + 5*time.Minute},
				},
			},
			{
				kind: "maintenance", title: rotation.title, impact: "minor", status: "scheduled",
				startsAt: rotation.startsAt, endsAt: &rotation.endsAt, author: amina,
				updates: []pageUpdate{{"scheduled", "We are rotating the Kubernetes node pools. API requests may see brief added latency; no downtime is expected.", 0}},
			},
		}); err != nil {
		return err
	}
	stk := s.kibanda.monitor("M-Pesa STK webhook").openOutage()
	if err := s.insertStatusPage(ctx, s.kibanda, "kibanda", "Kibanda Shop", new("Is the shop up? Check here before calling us."), new("https://kibanda.example.com"), true,
		[]pageItem{
			{"Storefront", "", "Online shop"},
			{"Orders API", "", "Orders"},
			{"M-Pesa STK webhook", "", "M-Pesa payments"},
		},
		[]statusPost{{
			kind: "incident", title: "M-Pesa payments failing", impact: "major", status: "investigating",
			startsAt: stk.startedAt, author: s.kibanda.owner(),
			updates: []pageUpdate{{"investigating", "M-Pesa prompts are not completing. Please pay on delivery while we look into it.", 6 * time.Minute}},
		}}); err != nil {
		return err
	}
	return s.insertStatusPage(ctx, s.solo, "sam-builds", "Sam's projects", nil, nil, false,
		[]pageItem{{"Portfolio", "", ""}, {"Side project API", "", "API"}}, nil)
}

func (s *seeder) insertAPIKeys(ctx context.Context) error {
	for _, a := range s.accounts() {
		for i, k := range a.apiKeys {
			k.secret = apiKeyMarker + token(32)
			created := s.now.Add(-time.Duration(40-i*11) * day)
			if err := s.exec(ctx, "credentials",
				`INSERT INTO credentials(id,principal_id,account_id,kind,audience,name,key_prefix,secret_hash,created_at,last_used_at,expires_at)
				 VALUES($1,$2,$3,'api_key','customer_api',$4,$5,$6,$7,$8,$9)`,
				newID(), k.owner.id, a.id, k.name, keyPrefix(k.secret), config.HashSecret(k.secret), created,
				s.now.Add(-time.Duration(5+i*40)*time.Minute), created.AddDate(1, 0, 0)); err != nil {
				return err
			}
		}
	}
	return nil
}

func keyPrefix(secret string) string {
	body := strings.TrimPrefix(secret, apiKeyMarker)
	return apiKeyMarker + body[:min(4, len(body))]
}

func (s *seeder) insertUsage(ctx context.Context) error {
	period := s.now.Format("2006-01") + "-01"
	for _, a := range s.accounts() {
		if a.apiRequests == 0 {
			continue
		}
		if err := s.exec(ctx, "account_monthly_usage",
			`INSERT INTO account_monthly_usage(account_id,period,api_requests) VALUES($1,$2::date,$3)`, a.id, period, a.apiRequests); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) insertAudit(ctx context.Context) error {
	brian := s.acme.members[1].who
	chloe := s.acme.members[2].who
	events := []struct {
		a              *account
		actor, target  *person
		action, reason string
		at             time.Time
	}{
		{s.acme, s.acme.owner(), brian, "membership.role_changed", "role set to admin", s.now.Add(-100 * day)},
		{s.acme, s.acme.owner(), chloe, "membership.role_changed", "role set to member", s.now.Add(-41 * day)},
		{s.kibanda, s.kibanda.owner(), brian, "membership.role_changed", "role set to member", s.now.Add(-20 * day)},
	}
	for i, e := range events {
		if err := s.exec(ctx, "audit_events",
			`INSERT INTO audit_events(principal_id,account_id,capability,action,target_type,target_id,reason,request_id,source_ip,user_agent,created_at)
			 VALUES($1,$2,'account.self.team',$3,'membership',$4,$5,$6,$7,$8,$9)`,
			e.actor.id, e.a.id, e.action, e.target.id, e.reason, newID(), fmt.Sprintf("198.51.100.%d", 20+i),
			"Mozilla/5.0 (Macintosh; Intel Mac OS X 15_6) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/19.0 Safari/605.1.15", e.at); err != nil {
			return err
		}
	}
	return nil
}

func (s *seeder) report(ctx context.Context, conn *pgx.Conn, elapsed time.Duration) error {
	tables := []string{"accounts", "principals", "account_memberships", "account_invites", "access_grants", "monitors", "monitor_schedules",
		"check_results", "monitor_daily", "incidents", "alert_channels", "monitor_channels", "notifications", "maintenance_windows",
		"status_pages", "status_page_items", "status_posts", "status_post_updates", "credentials", "billing_subscriptions",
		"billing_checkouts", "account_monthly_usage", "audit_events"}
	fmt.Printf("Seeded Noria Monitor in %s (regions: %s)\n\n", elapsed.Round(time.Millisecond), strings.Join(s.cfg.regions, ", "))
	for _, table := range tables {
		var n int64
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
			return err
		}
		fmt.Printf("  %-24s %9d\n", table, n)
	}
	fmt.Println("\nSign in with a code sent to any of:")
	for _, a := range s.accounts() {
		for _, m := range a.members {
			fmt.Printf("  %-24s %-7s %s\n", m.who.email, m.role, a.name)
		}
	}
	fmt.Println("  dan@acme.test            invited Acme Cloud")
	fmt.Printf("\n%s has the platform.owner access grants.\n\nAPI keys:\n", s.ops.email)
	for _, a := range s.accounts() {
		for _, k := range a.apiKeys {
			fmt.Printf("  %-13s %-13s %s\n", a.name, k.name, k.secret)
		}
	}
	if len(s.probes) > 0 {
		fmt.Println("\nRemote probes (run each in its own terminal):")
		for _, p := range s.probes {
			fmt.Printf("  PROBE_ENDPOINT=%s PROBE_TOKEN=%s PROBE_ALLOW_PRIVATE=true go run ./cmd/cloud probe   # %s\n", s.cfg.apiOrigin, p.token, p.region)
		}
	}
	fmt.Println("\nLive targets: go run ./cmd/cloud-seed serve")
	return nil
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

func latest(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func newID() string { return uuid.NewV7().String() }

func token(size int) string {
	raw := make([]byte, size)
	_, _ = rand.Read(raw)
	return base64.RawURLEncoding.EncodeToString(raw)
}
