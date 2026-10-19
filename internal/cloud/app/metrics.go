package app

import (
	"context"
	"expvar"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"sync/atomic"
	"time"
)

type appMetrics struct {
	httpRequests      atomic.Int64
	httpStatus        map[int]*atomic.Int64
	paystackFailures  atomic.Int64
	sesFailures       atomic.Int64
	rateLimitRejected atomic.Int64
	quotaRejected     atomic.Int64
	dbLatencyMs       atomic.Int64
	dbLatencyMsMax    atomic.Int64
	otpRequests       atomic.Int64
	otpVerifyFailures atomic.Int64
	emailQueueDepth   atomic.Int64
	httpClass         map[int]*atomic.Int64
}

func newAppMetrics() *appMetrics {
	return &appMetrics{
		httpStatus: map[int]*atomic.Int64{
			200: &atomic.Int64{},
			201: &atomic.Int64{},
			202: &atomic.Int64{},
			204: &atomic.Int64{},
			302: &atomic.Int64{},
			303: &atomic.Int64{},
			400: &atomic.Int64{},
			401: &atomic.Int64{},
			403: &atomic.Int64{},
			404: &atomic.Int64{},
			409: &atomic.Int64{},
			410: &atomic.Int64{},
			421: &atomic.Int64{},
			422: &atomic.Int64{},
			429: &atomic.Int64{},
			500: &atomic.Int64{},
			503: &atomic.Int64{},
		},
		httpClass: map[int]*atomic.Int64{
			2: {}, 3: {}, 4: {}, 5: {},
		},
	}
}

func (m *appMetrics) recordHTTP(status int) {
	m.httpRequests.Add(1)
	if counter, ok := m.httpStatus[status]; ok {
		counter.Add(1)
		return
	}
	if counter, ok := m.httpClass[statusClass(status)]; ok {
		counter.Add(1)
	}
}

func statusClass(status int) int {
	switch {
	case status >= 500:
		return 5
	case status >= 400:
		return 4
	case status >= 300:
		return 3
	default:
		return 2
	}
}

func (m *appMetrics) snapshot() map[string]int64 {
	out := make(map[string]int64, len(m.httpStatus)+len(m.httpClass)+16)
	for status, counter := range m.httpStatus {
		out["http_"+strconv.Itoa(status)] = counter.Load()
	}
	for class, counter := range m.httpClass {
		out["http_"+strconv.Itoa(class)+"xx_other"] = counter.Load()
	}
	out["http_requests"] = m.httpRequests.Load()
	out["db_latency_ms"] = m.dbLatencyMs.Load()
	out["db_latency_ms_max"] = m.dbLatencyMsMax.Load()
	out["paystack_failures"] = m.paystackFailures.Load()
	out["ses_failures"] = m.sesFailures.Load()
	out["otp_requests"] = m.otpRequests.Load()
	out["otp_verify_failures"] = m.otpVerifyFailures.Load()
	out["rate_limit_rejected"] = m.rateLimitRejected.Load()
	out["quota_rejected"] = m.quotaRejected.Load()
	out["email_queue_depth"] = m.emailQueueDepth.Load()
	out["goroutines"] = int64(runtime.NumGoroutine())
	return out
}

func (m *appMetrics) recordDBLatency(ms int64) {
	m.dbLatencyMs.Store(ms)
	for {
		peak := m.dbLatencyMsMax.Load()
		if ms <= peak || m.dbLatencyMsMax.CompareAndSwap(peak, ms) {
			return
		}
	}
}

func (a *App) metricsWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.logMetrics(ctx)
		case <-a.stop:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (a *App) logMetrics(ctx context.Context) {
	a.sampleDBLatency(ctx)
	a.metrics.emailQueueDepth.Store(a.countPending(ctx, "count_pending_emails",
		`SELECT count(*) FROM email_jobs WHERE sent_at IS NULL AND attempts<8`))

	snapshot := a.snapshotMetrics()
	attrs := make([]any, 0, len(snapshot)*2)
	for _, key := range slices.Sorted(maps.Keys(snapshot)) {
		attrs = append(attrs, key, snapshot[key])
	}
	a.logEvent("metrics", "metrics_worker", "emit_metrics", attrs...)
}

func (a *App) sampleDBLatency(parent context.Context) {
	if a.db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, sampleTimeout)
	defer cancel()
	start := time.Now()
	if err := a.db.Ping(ctx); err != nil {
		a.logError("metrics database latency sample failed", "metrics_worker", "sample_database_latency", err)
		return
	}
	a.metrics.recordDBLatency(time.Since(start).Milliseconds())
}

func (a *App) countPending(parent context.Context, operation, query string, args ...any) int64 {
	if a.db == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(parent, sampleTimeout)
	defer cancel()
	var n int64
	if err := a.db.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		a.logError("metrics queue sample failed", "metrics_worker", operation, err)
		return 0
	}
	return n
}

func (a *App) snapshotMetrics() map[string]int64 {
	return a.metrics.snapshot()
}

func (a *App) publishExpvar() {
	expvar.Publish("monitor", expvar.Func(func() any { return a.snapshotMetrics() }))
}
