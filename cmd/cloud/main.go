package main

import (
	"cmp"
	"context"
	"errors"
	"expvar"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/thekiharani/monitor/internal/cloud/app"
	"github.com/thekiharani/monitor/internal/cloud/cloudwatchlog"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/controlcmd"
	"github.com/thekiharani/monitor/internal/cloud/email"
	"github.com/thekiharani/monitor/internal/cloud/logging"
	"github.com/thekiharani/monitor/internal/cloud/migrate"
	"github.com/thekiharani/monitor/internal/cloud/postgres"
	"github.com/thekiharani/monitor/internal/cloud/probe"
)

var version = "dev"
var commit = "unknown"
var buildDate = "unknown"

const (
	httpDrainTimeout  = 8 * time.Second
	workerStopTimeout = 6 * time.Second
	logFlushTimeout   = 6 * time.Second
)

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		return run()
	}
	switch args[0] {
	case "serve":
		if len(args) == 1 {
			return run()
		}
	case "healthcheck":
		if len(args) == 1 {
			return healthcheck()
		}
	case "probe":
		if len(args) == 1 {
			return runProbe()
		}
	case "migrate", "control":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if args[0] == "migrate" {
			return migrate.Run(ctx, args[1:], os.Stdout, os.Stderr)
		}
		return controlcmd.Run(ctx, args[1:], os.Stdout, os.Stderr)
	case "help", "--help", "-h":
		fmt.Fprintln(os.Stdout, "usage: monitor-cloud [serve|migrate|control|probe|healthcheck]")
		return 0
	}
	fmt.Fprintln(os.Stderr, "unknown command or arguments; use monitor-cloud help")
	return 2
}

func runProbe() int {
	logger := newLogger(newHandler(os.Stdout, cmp.Or(os.Getenv("LOG_LEVEL"), "info")), cmp.Or(os.Getenv("APP_ENV"), "production")).With("component", "remote_probe")
	endpoint, token := os.Getenv("PROBE_ENDPOINT"), os.Getenv("PROBE_TOKEN")
	if endpoint == "" || !strings.HasPrefix(token, probe.TokenPrefix) {
		logger.Error("PROBE_ENDPOINT and a PROBE_TOKEN starting with " + probe.TokenPrefix + " are required")
		return 2
	}
	if parsed, err := url.Parse(endpoint); err != nil || (parsed.Scheme != "https" && os.Getenv("PROBE_ALLOW_PRIVATE") != "true") {
		logger.Error("PROBE_ENDPOINT must be an https URL")
		return 2
	}
	concurrency, err := strconv.Atoi(cmp.Or(os.Getenv("PROBE_CONCURRENCY"), "64"))
	if err != nil || concurrency < 1 || concurrency > 1024 {
		logger.Error("PROBE_CONCURRENCY must be between 1 and 1024")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	logger.Info("remote probe starting", "endpoint", endpoint, "concurrency", concurrency)
	agent := probe.Agent{
		Endpoint: endpoint, Token: token, Version: version, Concurrency: concurrency,
		AllowPrivate: os.Getenv("PROBE_ALLOW_PRIVATE") == "true", Logger: logger,
	}
	if err = agent.Start(ctx); err != nil {
		logger.Error("remote probe stopped", "error", err.Error())
		return 1
	}
	logger.Info("remote probe stopped")
	return 0
}

const defaultHealthcheckPort = "8900"

func healthcheck() int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+healthcheckPort()+"/health/ready", nil)
	if err != nil {
		return 1
	}
	req.Host = healthcheckHost()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return 1
	}
	return 0
}

func healthcheckPort() string {
	if _, port, err := net.SplitHostPort(os.Getenv("APP_ADDR")); err == nil && port != "" {
		return port
	}
	return defaultHealthcheckPort
}

func healthcheckHost() string {
	if parsed, err := url.Parse(os.Getenv("APP_ORIGIN")); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return "localhost:" + healthcheckPort()
}

func run() (exitCode int) {
	c, err := config.Load()
	if err != nil {
		logging.Error(newLogger(newHandler(os.Stdout, "info"), ""), "configuration loading failed", err, "component", "startup", "operation", "load_configuration")
		return 1
	}
	ctx := context.Background()
	var cloudWatch *cloudwatchlog.Writer
	if c.CloudWatchEnabled {
		cloudWatch, err = cloudwatchlog.New(ctx, cloudwatchlog.Config{
			Region:         c.CloudWatchRegion,
			Group:          c.CloudWatchLogGroup,
			StreamTemplate: c.CloudWatchLogStream,
			Environment:    c.Env,
			CreateGroup:    c.CloudWatchCreateGroup,
			BatchSize:      c.CloudWatchBatchSize,
			BufferSize:     c.CloudWatchBufferSize,
			FlushInterval:  c.CloudWatchFlushInterval,
			RetentionDays:  c.CloudWatchRetentionDays,
		})
		if err != nil {
			logging.Error(newLogger(newHandler(os.Stdout, c.LogLevel), c.Env), "CloudWatch logger initialization failed", err, "component", "startup", "operation", "initialize_cloudwatch", "region", c.CloudWatchRegion, "log_group", c.CloudWatchLogGroup)
			return 1
		}
	}
	handler := newHandler(os.Stdout, c.LogLevel)
	if cloudWatch != nil {
		handler = slog.NewMultiHandler(handler, newHandler(cloudWatch, c.LogLevel))
	}
	logger := newLogger(handler, c.Env)
	if cloudWatch != nil {
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), logFlushTimeout)
			defer cancel()
			if err := cloudWatch.Close(flushCtx); err != nil {
				logging.Error(logger, "CloudWatch logger shutdown failed", err, "component", "shutdown", "operation", "flush_cloudwatch")
				exitCode = 1
			}
		}()
	}
	db, err := postgres.Open(ctx, c)
	if err != nil {
		logging.Error(logger, "database open failed", err, "component", "startup", "operation", "open_database")
		return 1
	}
	defer db.Close()
	if err = db.Ping(ctx); err != nil {
		logging.Error(logger, "database unavailable", err, "component", "startup", "operation", "ping_database")
		return 1
	}
	tenantDB, err := postgres.OpenTenant(ctx, c)
	if err != nil {
		logging.Error(logger, "tenant database open failed", err, "component", "startup", "operation", "open_tenant_database")
		return 1
	}
	defer tenantDB.Close()
	if err = tenantDB.Ping(ctx); err != nil {
		logging.Error(logger, "tenant database unavailable", err, "component", "startup", "operation", "ping_tenant_database")
		return 1
	}
	sender, err := email.New(ctx, c)
	if err != nil {
		logging.Error(logger, "email client failed", err, "component", "startup", "operation", "initialize_email_client")
		return 1
	}
	options := make([]app.Option, 0, 2)
	if dashboardDir := os.Getenv("DASHBOARD_DIR"); dashboardDir != "" {
		if _, err = os.Stat(filepath.Join(dashboardDir, "index.html")); err != nil {
			logging.Error(logger, "dashboard assets unavailable", err, "component", "startup", "operation", "load_dashboard_assets", "directory", dashboardDir)
			return 1
		}
		options = append(options, app.WithDashboard(os.DirFS(dashboardDir)))
	}
	service := app.New(c, db, tenantDB, sender, logger, options...)
	if err = service.InitializePlans(ctx); err != nil {
		logging.Error(logger, "plan catalog unavailable", err, "component", "startup", "operation", "load_plans")
		return 1
	}
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	service.Start(workerCtx)
	server := &http.Server{Addr: c.Addr, Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10, MaxHeaderValueCount: 100}
	if c.PprofEnabled {
		go startPprof(c.PprofAddr, logger)
	}

	signalCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serverErr := make(chan error, 1)

	go func() {
		logger.Info("server listening", "component", "http_server", "operation", "listen_and_serve", "addr", c.Addr)
		if e := server.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
			logging.Error(logger, "server stopped unexpectedly", e, "component", "http_server", "operation", "listen_and_serve", "addr", c.Addr)
			serverErr <- e
		}
	}()

	exitCode = 0
	select {
	case <-signalCtx.Done():
	case <-serverErr:
		exitCode = 1
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), httpDrainTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logging.Error(logger, "server shutdown failed", err, "component", "shutdown", "operation", "http_server_shutdown")
		exitCode = 1
	}
	cancelWorkers()
	select {
	case <-serverErr:
		exitCode = 1
	default:
	}
	stopCtx, cancelStop := context.WithTimeout(context.Background(), workerStopTimeout)
	defer cancelStop()
	service.Stop(stopCtx)
	return exitCode
}

func newHandler(output io.Writer, level string) slog.Handler {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level:       l,
		AddSource:   true,
		ReplaceAttr: renameTimeKey,
	})
}

func renameTimeKey(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && a.Key == slog.TimeKey {
		return slog.Attr{
			Key:   "timestamp",
			Value: slog.StringValue(a.Value.Time().UTC().Format(time.RFC3339Nano)),
		}
	}
	return a
}

func newLogger(handler slog.Handler, env string) *slog.Logger {
	return slog.New(handler).With(
		"log_schema_version", 1,
		"service", "monitor",
		"environment", env,
		"version", version,
		"commit", commit,
		"build_date", buildDate,
	)
}
func startPprof(addr string, logger *slog.Logger) {
	server := &http.Server{
		Addr:              addr,
		Handler:           newPprofHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if e := server.ListenAndServe(); e != nil {
		logging.Error(logger, "pprof stopped", e, "component", "pprof", "operation", "listen_and_serve", "addr", addr)
	}
}

func newPprofHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /debug/vars", expvar.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/goroutineleak", pprof.Handler("goroutineleak"))
	return mux
}
