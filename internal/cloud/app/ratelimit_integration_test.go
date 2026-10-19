package app

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/usage"
)

func clearBucket(t *testing.T, a *App, key string) {
	t.Helper()
	if _, err := a.db.Exec(context.Background(), `DELETE FROM rate_buckets WHERE bucket_key=$1`, key); err != nil {
		t.Fatalf("clear bucket: %v", err)
	}
}

func TestIntegrationSharedLimitStopsAtBurstAcrossInstances(t *testing.T) {
	db := openIntegrationDB(t)
	first := newCoreIntegrationApp(t, db, &capturingMail{})
	second := newCoreIntegrationApp(t, db, &capturingMail{})

	key := fmt.Sprintf("test:%d", time.Now().UnixNano())
	defer clearBucket(t, first, key)

	limits := usage.Limits{RequestsPerSecond: 0.001, Burst: 6}
	ctx := context.Background()

	allowed := 0
	for index := range 20 {
		instance := first
		if index%2 == 1 {
			instance = second
		}
		if instance.allowShared(ctx, key, limits) {
			allowed++
		}
	}
	if allowed != limits.Burst {
		t.Fatalf("allowed %d requests across two instances, want the shared burst of %d", allowed, limits.Burst)
	}
}

func TestIntegrationSharedLimitRefillsOverTime(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	key := fmt.Sprintf("refill:%d", time.Now().UnixNano())
	defer clearBucket(t, a, key)

	limits := usage.Limits{RequestsPerSecond: 20, Burst: 2}
	ctx := context.Background()

	for range limits.Burst {
		if !a.allowShared(ctx, key, limits) {
			t.Fatal("the initial burst should be admitted")
		}
	}
	if a.allowShared(ctx, key, limits) {
		t.Fatal("the bucket should be empty after the burst")
	}

	time.Sleep(200 * time.Millisecond)
	if !a.allowShared(ctx, key, limits) {
		t.Error("the bucket should have refilled after 200ms at 20/s")
	}
}

func TestIntegrationSharedLimitIsAtomicUnderConcurrency(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	key := fmt.Sprintf("race:%d", time.Now().UnixNano())
	defer clearBucket(t, a, key)

	limits := usage.Limits{RequestsPerSecond: 0.001, Burst: 5}
	ctx := context.Background()

	var wg sync.WaitGroup
	results := make(chan bool, 40)
	for range 40 {
		wg.Go(func() {
			results <- a.allowShared(ctx, key, limits)
		})
	}
	wg.Wait()
	close(results)

	allowed := 0
	for ok := range results {
		if ok {
			allowed++
		}
	}
	if allowed != limits.Burst {
		t.Fatalf("40 concurrent requests admitted %d, want exactly the burst of %d", allowed, limits.Burst)
	}
}

func TestIntegrationSharedLimitFailsOpenAndSkipsUnlimitedProfiles(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()

	if !a.allowShared(ctx, "unused", usage.Limits{RequestsPerSecond: 0, Burst: 0}) {
		t.Error("a profile with no rate configured should not be limited")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if !a.allowShared(cancelled, "unreachable", usage.Limits{RequestsPerSecond: 1, Burst: 1}) {
		t.Error("the limiter should fail open when the database cannot be reached")
	}
}
