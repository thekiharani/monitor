package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

//go:embed web
var webFS embed.FS

var version = "dev"

type Snapshot struct {
	Time int64 `json:"time"`
	System
	Containers []Container      `json:"containers"`
	Postgres   []PostgresStats  `json:"postgres"`
	Redis      []RedisStats     `json:"redis"`
	MySQL      []MySQLStats     `json:"mysql"`
	Mongo      []MongoStats     `json:"mongo"`
	Memcached  []MemcachedStats `json:"memcached"`
	Nginx      []NginxStats     `json:"nginx"`
	RabbitMQ   []RabbitStats    `json:"rabbitmq"`
	Checks     []Check          `json:"checks"`
	Systemd    *SystemdStatus   `json:"systemd"`
	Health     HostHealth       `json:"health"`
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

func main() {
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
	proc, sys, root := env("HOST_PROC", "/host/proc"), env("HOST_SYS", "/host/sys"), env("HOST_ROOT", "/host/root")
	system := NewSystemCollector(proc, sys, root)
	host := system.HostInfo()
	host.Version = version
	history := NewHistory(env("HISTORY_FILE", "/data/history.json"))
	health := &HealthCollector{root: root, smartFile: env("SMART_FILE", "/shared/smart.json")}
	hub := &Hub{clients: map[chan []byte]struct{}{}}

	var (
		containers poller[[]Container]
		systemd    poller[*SystemdStatus]
		hostHealth poller[HostHealth]
		updates    poller[*Updates]
	)
	go hostHealth.Run(ctx, time.Minute, health.Collect)
	go updates.Run(ctx, 30*time.Minute, health.CheckUpdates)
	containerHistory := NewContainerHistory()
	cc := NewContainerCollector(os.Getenv("DOCKER_HOST"), sys, proc)
	if cc != nil {
		go containers.Run(ctx, 5*time.Second, func(ctx context.Context) []Container {
			list, err := cc.Collect(ctx)
			if err != nil {
				slog.Warn("containers unavailable", "err", err)
			}
			containerHistory.Record(time.Now(), list)
			return list
		})
	}
	queryText, _ := strconv.ParseBool(env("MONITOR_QUERY_TEXT", "true"))
	registry := NewServiceRegistry(queryText)
	if sc := NewSystemdCollector(os.Getenv("SYSTEMD_URL"), os.Getenv("SYSTEMD_UNITS")); sc != nil {
		registry.add("systemd:host", sc)
		go systemd.Run(ctx, 15*time.Second, func(ctx context.Context) *SystemdStatus {
			status := sc.Collect(ctx)
			registry.history.Record("systemd:host", time.Now(), systemdMetrics(status))
			return status
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

	go func() {
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
			point.Level, point.Issue = s.Verdict.worst()
			history.Add(point)
			if payload, err := json.Marshal(s); err == nil {
				hub.Publish(payload)
			}
			select {
			case <-ctx.Done():
				history.Save()
				return
			case <-ticker.C:
			}
		}
	}()

	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(static))
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
	return server.ListenAndServe()
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
	json.NewEncoder(w).Encode(v)
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
