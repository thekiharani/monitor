package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"testing/synctest"

	"github.com/thekiharani/monitor/internal/cloud/usage"
	"time"
)

func testLimits() usage.Limits { return usage.LimitsFor("pro") }

func workerApp() *App {
	cfg := testConfig()
	return &App{
		cfg:         cfg,
		log:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		metrics:     newAppMetrics(),
		limiters:    newLimiterSet(),
		emailWake:   make(chan struct{}, 1),
		statusPages: newStatusCache(),
		stop:        make(chan struct{}),
	}
}

func TestWorkersStopPromptly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := workerApp()
		done := make(chan struct{})
		go func() {
			a.cleanupWorker(context.Background())
			close(done)
		}()
		synctest.Wait()
		close(a.stop)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("cleanupWorker did not return after stop was closed")
		}
	})
}

func TestWorkersHonourContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := workerApp()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			a.metricsWorker(ctx)
			close(done)
		}()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("metricsWorker ignored context cancellation")
		}
	})
}

func TestNotifyEmailNeverBlocks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := workerApp()
		for range 100 {
			a.notifyEmail()
		}
		if len(a.emailWake) != 1 {
			t.Fatalf("wake depth=%d, want 1 coalesced signal", len(a.emailWake))
		}
	})
}

func TestLimiterSweepsIdleEntries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newLimiterSet()
		limits := testLimits()
		s.allow("caller-a", limits)
		if len(s.items) != 1 {
			t.Fatalf("items=%d, want 1", len(s.items))
		}

		synctest.Sleep(11 * time.Minute)
		s.allow("caller-b", limits)

		if _, stale := s.items["caller-a"]; stale {
			t.Fatal("an entry idle for eleven minutes was not swept")
		}
	})
}
