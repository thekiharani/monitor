package monitor

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/thekiharani/monitor/web"
)

var version = "dev"

type Snapshot struct {
	Time int64 `json:"time"`
	System
	Containers []Container      `json:"containers,omitzero"`
	Postgres   []PostgresStats  `json:"postgres"`
	Redis      []RedisStats     `json:"redis"`
	MySQL      []MySQLStats     `json:"mysql"`
	Mongo      []MongoStats     `json:"mongo"`
	Memcached  []MemcachedStats `json:"memcached"`
	Nginx      []NginxStats     `json:"nginx"`
	RabbitMQ   []RabbitStats    `json:"rabbitmq"`
	Caddy      []CaddyStats     `json:"caddy"`
	Traefik    []TraefikStats   `json:"traefik"`
	HAProxy    []HAProxyStats   `json:"haproxy"`
	Apache     []ApacheStats    `json:"apache"`
	Checks     []Check          `json:"checks"`
	Systemd    *SystemdStatus   `json:"systemd"`
	Health     HostHealth       `json:"health"`
	Alerts     AlertSummary     `json:"alerts"`
	Verdict    Verdict          `json:"verdict"`
}

type Hub struct {
	mu      sync.Mutex
	latest  []byte
	clients map[chan []byte]struct{}
}

func (h *Hub) Publish(payload []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.latest = payload
	for ch := range h.clients {
		select {
		case ch <- payload:
		default:
		}
	}
}

func (h *Hub) Subscribe() (chan []byte, []byte) {
	ch := make(chan []byte, 4)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[ch] = struct{}{}
	return ch, h.latest
}

func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, ch)
}

func Main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "serve":
		err = serve(ctx)
	case "healthcheck":
		os.Exit(probe(env("ADDR", ":8080")))
	case "docker-proxy":
		logs, _ := strconv.ParseBool(env("CONTAINER_LOGS", "true"))
		err = runDockerProxy(env("ADDR", ":2375"), env("DOCKER_SOCKET", "/var/run/docker.sock"), env("SYSTEMD_SOCKET", "/run/dbus/system_bus_socket"), logs)
	case "smart":
		every, parseErr := time.ParseDuration(env("SMART_INTERVAL", "15m"))
		if parseErr != nil {
			err = parseErr
			break
		}
		err = runSmart(ctx, env("SMART_FILE", "/shared/smart.json"), every)
	default:
		err = fmt.Errorf("unknown command %q (serve, healthcheck, docker-proxy, smart)", command)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error(command+" stopped", "err", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proc, sys, root := env("HOST_PROC", "/host/proc"), env("HOST_SYS", "/host/sys"), env("HOST_ROOT", "/host/root")
	system := NewSystemCollector(proc, sys, root)
	host := system.HostInfo()
	host.Version = version
	store, err := OpenMetricStore(env("METRICS_DB", "/data/monitor.db"), env("HISTORY_FILE", "/data/history.json"))
	if err != nil {
		return err
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("metrics database close failed", "err", err)
		}
	}()
	history, containerHistory, serviceHistory, err := restoreHistories(store, time.Now())
	if err != nil {
		return err
	}
	queryText, _ := strconv.ParseBool(env("MONITOR_QUERY_TEXT", "true"))
	registry := NewServiceRegistry(queryText)
	registry.history = serviceHistory
	health := &HealthCollector{root: root, smartFile: env("SMART_FILE", "/shared/smart.json")}
	alertConfig := alertConfigFromEnv(host.Hostname)
	alerter := NewAlerter(alertConfig, time.Now())
	defer func() {
		cancel()
		registry.wg.Wait()
		alerter.Save()
	}()
	registry.run(func() { store.Run(ctx) })
	if hb := alertConfig.Heartbeat; hb != nil {
		every, err := time.ParseDuration(env("HEARTBEAT_INTERVAL", "1m"))
		if err != nil || every < 10*time.Second {
			every = time.Minute
		}
		registry.run(func() { hb.Run(ctx, every) })
	}
	hub := &Hub{clients: map[chan []byte]struct{}{}}

	var (
		containers poller[[]Container]
		systemd    poller[*SystemdStatus]
		hostHealth poller[HostHealth]
		updates    poller[*Updates]
	)
	registry.run(func() { hostHealth.Run(ctx, time.Minute, health.Collect) })
	registry.run(func() { updates.Run(ctx, 30*time.Minute, health.CheckUpdates) })
	cc := NewContainerCollector(os.Getenv("DOCKER_HOST"), sys, proc)
	if cc != nil {
		registry.run(func() {
			containers.Run(ctx, 5*time.Second, func(ctx context.Context) []Container {
				list, err := cc.Collect(ctx)
				if err != nil {
					slog.Warn("containers unavailable", "err", err)
				}
				containerHistory.Record(time.Now(), list)
				return list
			})
		})
	}
	if sc := NewSystemdCollector(os.Getenv("SYSTEMD_URL"), os.Getenv("SYSTEMD_UNITS")); sc != nil {
		registry.add("systemd:host", sc)
		registry.run(func() {
			systemd.Run(ctx, 15*time.Second, func(ctx context.Context) *SystemdStatus {
				status := sc.Collect(ctx)
				registry.history.Record("systemd:host", time.Now(), systemdMetrics(status))
				return status
			})
		})
	}
	postgres := pollEach(ctx, 5*time.Second, os.Getenv("POSTGRES_URL"), "postgres", NewPostgresCollector, postgresMetrics, registry)
	redis := pollEach(ctx, 5*time.Second, os.Getenv("REDIS_URL"), "redis", NewRedisCollector, redisMetrics, registry)
	mysql := pollEach(ctx, 5*time.Second, os.Getenv("MYSQL_URL"), "mysql", NewMySQLCollector, mysqlMetrics, registry)
	mongo := pollEach(ctx, 5*time.Second, os.Getenv("MONGODB_URL"), "mongo", NewMongoCollector, mongoMetrics, registry)
	memcached := pollEach(ctx, 5*time.Second, os.Getenv("MEMCACHED_URL"), "memcached", NewMemcachedCollector, memcachedMetrics, registry)
	nginx := pollEach(ctx, 5*time.Second, os.Getenv("NGINX_URL"), "nginx", NewNginxCollector, nginxMetrics, registry)
	rabbitmq := pollEach(ctx, 10*time.Second, os.Getenv("RABBITMQ_URL"), "rabbitmq", NewRabbitCollector, rabbitMetrics, registry)
	checks := pollEach(ctx, 15*time.Second, os.Getenv("CHECKS"), "check", NewCheckCollector, checkMetrics, registry)
	caddy := pollEach(ctx, 5*time.Second, os.Getenv("CADDY_URL"), "caddy", NewCaddyCollector, caddyMetrics, registry)
	traefik := pollEach(ctx, 5*time.Second, os.Getenv("TRAEFIK_URL"), "traefik", NewTraefikCollector, traefikMetrics, registry)
	haproxy := pollEach(ctx, 5*time.Second, os.Getenv("HAPROXY_URL"), "haproxy", NewHAProxyCollector, haproxyMetrics, registry)
	apache := pollEach(ctx, 5*time.Second, os.Getenv("APACHE_URL"), "apache", NewApacheCollector, apacheMetrics, registry)

	registry.run(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			now := time.Now()
			s := Snapshot{
				Time:       now.UnixMilli(),
				System:     system.Sample(now),
				Containers: containers.Get(),
				Postgres:   postgres(),
				Redis:      redis(),
				MySQL:      mysql(),
				Mongo:      mongo(),
				Memcached:  memcached(),
				Nginx:      nginx(),
				RabbitMQ:   rabbitmq(),
				Caddy:      caddy(),
				Traefik:    traefik(),
				HAProxy:    haproxy(),
				Apache:     apache(),
				Checks:     checks(),
				Systemd:    systemd.Get(),
				Health:     hostHealth.Get(),
			}
			s.Health.Updates = updates.Get()
			point := pointOf(now, s.System)
			recent := average(append(history.Recent(59), point))
			s.Verdict = assess(s, Averages{
				CPU: recent.CPU, IOWait: recent.IOWait, Steal: recent.Steal,
				ReadOps: recent.ReadOps, WriteOps: recent.WriteOps,
				ReadLatency: recent.ReadLat, WriteLatency: recent.WriteLat, Queue: recent.Queue,
			})
			s.Verdict.include(alerter.Findings())
			alerter.Observe(now, s.Verdict.Reasons)
			s.Alerts = alerter.Summary()
			point.Level, point.Issue = s.Verdict.worst()
			history.Add(point)
			if payload, err := json.Marshal(s); err == nil {
				hub.Publish(payload)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	assets, err := loadAssets(web.Files)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", serveAssets(assets))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/host", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, host)
	})
	mux.HandleFunc("GET /api/history", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, history.Range(r.URL.Query().Get("range")))
	})
	mux.HandleFunc("GET /api/stream", func(w http.ResponseWriter, r *http.Request) {
		stream(w, r, hub)
	})
	container := func(w http.ResponseWriter, r *http.Request) (Container, bool) {
		name := r.PathValue("name")
		if cc != nil && containerName.MatchString(name) {
			for _, c := range containers.Get() {
				if c.Name == name {
					return c, true
				}
			}
		}
		http.Error(w, "no such container", http.StatusNotFound)
		return Container{}, false
	}
	mux.HandleFunc("GET /api/containers/{name}", func(w http.ResponseWriter, r *http.Request) {
		if c, ok := container(w, r); ok {
			detail, err := cc.Detail(r.Context(), c)
			if err != nil {
				http.Error(w, friendlyError(err), http.StatusBadGateway)
				return
			}
			writeJSON(w, detail)
		}
	})
	mux.HandleFunc("GET /api/containers/{name}/history", func(w http.ResponseWriter, r *http.Request) {
		if c, ok := container(w, r); ok {
			writeJSON(w, containerHistory.Range(c.Name, r.URL.Query().Get("range")))
		}
	})
	mux.HandleFunc("GET /api/alerts", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, alerter.Status())
	})
	mux.HandleFunc("POST /api/alerts/test", writeAction(func(r *http.Request, body actionBody) error {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		return alerter.Test(ctx, body.Channel, time.Now())
	}))
	mux.HandleFunc("POST /api/alerts/mute", writeAction(func(_ *http.Request, body actionBody) error {
		return alerter.Mute(body.Key, body.Minutes, time.Now())
	}))
	mux.HandleFunc("POST /api/alerts/unmute", writeAction(func(_ *http.Request, body actionBody) error {
		alerter.Unmute(body.Key)
		return nil
	}))
	mux.HandleFunc("POST /api/alerts/maintenance", writeAction(func(_ *http.Request, body actionBody) error {
		alerter.SetMaintenance(body.Minutes, time.Now())
		return nil
	}))
	mux.HandleFunc("GET /api/services/{kind}/{name}", func(w http.ResponseWriter, r *http.Request) {
		collector, ok := registry.lookup(r.PathValue("kind") + ":" + r.PathValue("name"))
		if !ok {
			http.Error(w, "no such service", http.StatusNotFound)
			return
		}
		out := struct {
			Detail    any    `json:"detail"`
			Error     string `json:"error,omitempty"`
			QueryText bool   `json:"queryText"`
		}{QueryText: registry.queryText}
		if d, ok := collector.(Detailer); ok {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			detail, err := d.Detail(withQueryText(ctx, registry.queryText))
			out.Detail = detail
			if err != nil {
				out.Error = friendlyError(err)
			}
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/services/{kind}/{name}/history", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("kind") + ":" + r.PathValue("name")
		if _, ok := registry.lookup(key); !ok {
			http.Error(w, "no such service", http.StatusNotFound)
			return
		}
		writeJSON(w, registry.history.Range(key, r.URL.Query().Get("range")))
	})
	mux.HandleFunc("GET /api/containers/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
		if c, ok := container(w, r); ok {
			lines, err := cc.Logs(r.Context(), c.Name, r.URL.Query().Get("since"), 500)
			switch {
			case errors.Is(err, errContainerLogsOff):
				http.Error(w, err.Error(), http.StatusForbidden)
			case err != nil:
				http.Error(w, friendlyError(err), http.StatusBadGateway)
			default:
				writeJSON(w, lines)
			}
		}
	})

	server := &http.Server{
		Addr:              env("ADDR", ":8080"),
		Handler:           secure(mux, os.Getenv("MONITOR_PASSWORD")),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdown)
	}()
	slog.Info("listening", "addr", server.Addr, "host", host.Hostname, "version", version)
	err = server.ListenAndServe()
	return err
}

func secure(next http.Handler, password string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if password != "" && r.URL.Path != "/healthz" {
			_, given, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(given), []byte(password)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="monitor", charset="UTF-8"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func stream(w http.ResponseWriter, r *http.Request, hub *Hub) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, latest := hub.Subscribe()
	defer hub.Unsubscribe(ch)
	if latest != nil {
		fmt.Fprintf(w, "data: %s\n\n", latest)
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.MarshalWrite(w, v)
}

func probe(addr string) int {
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1" + addr + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type asset struct {
	body, gzip, brotli []byte
	etag, contentType  string
}

func loadAssets(files fs.FS) (map[string]*asset, error) {
	assets := map[string]*asset{}
	err := fs.WalkDir(files, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || slices.Contains([]string{".gz", ".br", ".go"}, path.Ext(name)) {
			return err
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return err
		}
		a := &asset{body: body, contentType: mime.TypeByExtension(path.Ext(name))}
		a.gzip, _ = fs.ReadFile(files, name+".gz")
		a.brotli, _ = fs.ReadFile(files, name+".br")
		if a.gzip == nil {
			var b bytes.Buffer
			zw := gzip.NewWriter(&b)
			zw.Write(body)
			zw.Close()
			a.gzip = b.Bytes()
		}
		sum := sha256.Sum256(body)
		a.etag = `"` + hex.EncodeToString(sum[:12]) + `"`
		assets["/"+name] = a
		return nil
	})
	if index, ok := assets["/index.html"]; ok {
		assets["/"] = index
	}
	return assets, err
}

func serveAssets(assets map[string]*asset) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := assets[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", a.contentType)
		h.Set("ETag", a.etag)
		h.Set("Cache-Control", "no-cache")
		h.Set("Vary", "Accept-Encoding")
		if strings.Contains(r.Header.Get("If-None-Match"), a.etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		body := a.body
		switch accept := r.Header.Get("Accept-Encoding"); {
		case a.brotli != nil && acceptsEncoding(accept, "br"):
			h.Set("Content-Encoding", "br")
			body = a.brotli
		case acceptsEncoding(accept, "gzip"):
			h.Set("Content-Encoding", "gzip")
			body = a.gzip
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method != http.MethodHead {
			w.Write(body)
		}
	})
}

func acceptsEncoding(header, encoding string) bool {
	for part := range strings.SplitSeq(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), encoding) {
			continue
		}
		q, found := strings.CutPrefix(strings.TrimSpace(params), "q=")
		weight, err := strconv.ParseFloat(strings.TrimSpace(q), 64)
		return !found || err != nil || weight > 0
	}
	return false
}

type actionBody struct {
	Channel string `json:"channel"`
	Key     string `json:"key"`
	Minutes int    `json:"minutes"`
}

func writeAction(act func(*http.Request, actionBody) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Monitor") != "1" || !slices.Contains([]string{"", "same-origin", "none"}, r.Header.Get("Sec-Fetch-Site")) {
			http.Error(w, "cross-site requests are not allowed", http.StatusForbidden)
			return
		}
		var body actionBody
		if err := json.UnmarshalRead(io.LimitReader(r.Body, 4096), &body); err != nil || body.Minutes < 0 || body.Minutes > 7*24*60 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if err := act(r, body); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.MarshalWrite(w, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}
}
