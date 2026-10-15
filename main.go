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
	Containers []Container    `json:"containers"`
	Postgres   *PostgresStats `json:"postgres"`
	Redis      *RedisStats    `json:"redis"`
	Health     HostHealth     `json:"health"`
	Verdict    Verdict        `json:"verdict"`
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
		err = runDockerProxy(env("ADDR", ":2375"), env("DOCKER_SOCKET", "/var/run/docker.sock"))
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
		postgres   poller[*PostgresStats]
		redis      poller[*RedisStats]
		hostHealth poller[HostHealth]
		updates    poller[*Updates]
	)
	go hostHealth.Run(ctx, time.Minute, health.Collect)
	go updates.Run(ctx, 30*time.Minute, health.CheckUpdates)
	if cc := NewContainerCollector(os.Getenv("DOCKER_HOST"), sys); cc != nil {
		go containers.Run(ctx, 5*time.Second, func(ctx context.Context) []Container {
			list, err := cc.Collect(ctx)
			if err != nil {
				slog.Warn("containers unavailable", "err", err)
			}
			return list
		})
	}
	if pc := NewPostgresCollector(os.Getenv("POSTGRES_URL")); pc != nil {
		go postgres.Run(ctx, 5*time.Second, func(ctx context.Context) *PostgresStats {
			s := pc.Collect(ctx)
			return &s
		})
	}
	if rc := NewRedisCollector(os.Getenv("REDIS_URL")); rc != nil {
		go redis.Run(ctx, 5*time.Second, func(ctx context.Context) *RedisStats {
			s := rc.Collect(ctx)
			return &s
		})
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			now := time.Now()
			s := Snapshot{
				Time:       now.UnixMilli(),
				System:     system.Sample(now),
				Containers: containers.Get(),
				Postgres:   postgres.Get(),
				Redis:      redis.Get(),
				Health:     hostHealth.Get(),
			}
			s.Health.Updates = updates.Get()
			history.Add(pointOf(now, s.System))
			recent := average(history.Recent(60))
			s.Verdict = assess(s, Averages{
				CPU: recent.CPU, IOWait: recent.IOWait, Steal: recent.Steal,
				ReadOps: recent.ReadOps, WriteOps: recent.WriteOps,
				ReadLatency: recent.ReadLat, WriteLatency: recent.WriteLat, Queue: recent.Queue,
			})
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
