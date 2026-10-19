package app

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/usage"
)

func TestLimiterIsBoundedAndConcurrent(t *testing.T) {
	s := newLimiterSet()
	s.max = 32
	l := usage.LimitsFor("free")
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			for range 10 {
				s.allow("key", l)
				s.allow("key:"+string(rune(i)), l)
			}
		})
	}
	wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.items) > 32 {
		t.Fatalf("limiter size=%d", len(s.items))
	}
}

func configuredLimits(t *testing.T, s *limiterSet, key string) (float64, int) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[key]
	if !ok {
		t.Fatalf("no limiter for %q", key)
	}
	return float64(entry.limiter.Limit()), entry.limiter.Burst()
}

func TestLimiterFollowsAPlanChangeWhileTheCallerStaysActive(t *testing.T) {
	s := newLimiterSet()
	free, pro := usage.LimitsFor("free"), usage.LimitsFor("pro")

	s.allow("key:upgrader", free)
	if rps, burst := configuredLimits(t, s, "key:upgrader"); rps != free.RequestsPerSecond || burst != free.Burst {
		t.Fatalf("new limiter = %v rps burst %d, want the free plan %v/%d", rps, burst, free.RequestsPerSecond, free.Burst)
	}

	for range 5 {
		s.allow("key:upgrader", pro)
	}
	rps, burst := configuredLimits(t, s, "key:upgrader")
	if rps != pro.RequestsPerSecond || burst != pro.Burst {
		t.Errorf("after upgrading, limiter = %v rps burst %d, want the pro plan %v/%d; a paying caller was left on their old rate",
			rps, burst, pro.RequestsPerSecond, pro.Burst)
	}
}

func TestLimiterNarrowsWhenAPlanIsDowngraded(t *testing.T) {
	s := newLimiterSet()
	business, starter := usage.LimitsFor("business"), usage.LimitsFor("starter")

	s.allow("account:leaver", business)
	s.allow("account:leaver", starter)

	rps, burst := configuredLimits(t, s, "account:leaver")
	if rps != starter.RequestsPerSecond || burst != starter.Burst {
		t.Errorf("after downgrading, limiter = %v rps burst %d, want the starter plan %v/%d",
			rps, burst, starter.RequestsPerSecond, starter.Burst)
	}
}

func TestLimiterKeepsItsBudgetAcrossCallsOnAnUnchangedPlan(t *testing.T) {
	s := newLimiterSet()
	free := usage.LimitsFor("free")

	allowed := 0
	for range 10 {
		if s.allow("key:steady", free) {
			allowed++
		}
	}
	if allowed > free.Burst {
		t.Errorf("allowed %d of 10 immediate requests, want no more than the free burst of %d; the limiter is being rebuilt per call",
			allowed, free.Burst)
	}
}

func TestActiveEntriesSurviveTheSweepThatWouldHaveRefreshedTheirLimits(t *testing.T) {
	s := newLimiterSet()
	free := usage.LimitsFor("free")
	s.allow("key:busy", free)

	s.mu.Lock()
	entry := s.items["key:busy"]
	entry.seen = time.Now().Add(-11 * time.Minute)
	s.items["key:busy"] = entry
	s.lastSweep = time.Now().Add(-2 * time.Minute)
	s.mu.Unlock()

	s.allow("key:busy", free)

	s.mu.Lock()
	_, present := s.items["key:busy"]
	s.mu.Unlock()
	if !present {
		t.Fatal("the sweep evicted a key that was just used")
	}
}

func TestIntegrationPruningARefilledBucketChangesNothing(t *testing.T) {
	db := openIntegrationDB(t)
	a := &App{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: newAppMetrics()}
	limits := usage.LimitsFor("pro")

	idle := "account:idle-" + uuid.NewV7().String()
	pruned := "account:pruned-" + uuid.NewV7().String()
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM rate_buckets WHERE bucket_key IN ($1,$2)`, idle, pruned)
	})

	if _, err := db.Exec(context.Background(),
		`INSERT INTO rate_buckets(bucket_key,tokens,refilled_at) VALUES($1,0,now()-interval '2 hours')`, idle); err != nil {
		t.Fatalf("seed idle bucket: %v", err)
	}

	if !a.allowShared(context.Background(), idle, limits) {
		t.Fatal("a bucket idle for two hours refused a request; it should have refilled to the plan burst")
	}
	if !a.allowShared(context.Background(), pruned, limits) {
		t.Fatal("a bucket that does not exist refused a request")
	}

	var idleTokens, prunedTokens float64
	if err := db.QueryRow(context.Background(),
		`SELECT (SELECT tokens FROM rate_buckets WHERE bucket_key=$1),(SELECT tokens FROM rate_buckets WHERE bucket_key=$2)`,
		idle, pruned).Scan(&idleTokens, &prunedTokens); err != nil {
		t.Fatalf("read tokens: %v", err)
	}
	if idleTokens != prunedTokens {
		t.Errorf("idle bucket left %v tokens but a pruned one left %v; deleting an hour-old bucket must be indistinguishable from keeping it",
			idleTokens, prunedTokens)
	}
	if want := float64(limits.Burst) - 1; idleTokens != want {
		t.Errorf("tokens=%v, want %v", idleTokens, want)
	}
}

func TestIntegrationHourlyCleanupPrunesOnlyRefilledBuckets(t *testing.T) {
	db := openIntegrationDB(t)
	a := &App{db: db, log: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: newAppMetrics()}

	stale := "account:stale-" + uuid.NewV7().String()
	fresh := "account:fresh-" + uuid.NewV7().String()
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM rate_buckets WHERE bucket_key IN ($1,$2)`, stale, fresh)
	})
	if _, err := db.Exec(ctx,
		`INSERT INTO rate_buckets(bucket_key,tokens,refilled_at) VALUES($1,0,now()-interval '90 minutes'),($2,0,now()-interval '5 minutes')`,
		stale, fresh); err != nil {
		t.Fatalf("seed buckets: %v", err)
	}

	a.cleanupHourly(ctx)

	var staleLeft, freshLeft int
	if err := db.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM rate_buckets WHERE bucket_key=$1),(SELECT count(*) FROM rate_buckets WHERE bucket_key=$2)`,
		stale, fresh).Scan(&staleLeft, &freshLeft); err != nil {
		t.Fatalf("count buckets: %v", err)
	}
	if staleLeft != 0 {
		t.Error("a bucket untouched for ninety minutes survived the sweep; it has long since refilled and only costs writes")
	}
	if freshLeft != 1 {
		t.Error("the sweep removed a bucket still inside its refill window, handing its owner a fresh burst")
	}
}
