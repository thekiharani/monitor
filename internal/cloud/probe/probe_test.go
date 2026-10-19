package probe

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/checks"
)

type fakeControlPlane struct {
	mu       sync.Mutex
	claims   int
	reported []Report
	revoked  atomic.Bool
}

func (f *fakeControlPlane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+TokenPrefix+"test" || f.revoked.Load() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case ClaimPath:
		f.claims++
		work := Work{Region: "fra"}
		if f.claims == 1 {
			work.Checks = []Check{{MonitorID: "m1", Kind: checks.KindHTTP, URL: "http://10.0.0.1"}, {MonitorID: "m2", Kind: checks.KindTCP, URL: "1.1.1.1:53"}}
		}
		_ = json.MarshalWrite(w, work)
	case ResultsPath:
		var batch Batch
		_ = json.Unmarshal(body, &batch)
		f.reported = append(f.reported, batch.Results...)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeControlPlane) reports() []Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Report(nil), f.reported...)
}

func TestAgentRunsClaimedChecksAndReportsEveryResult(t *testing.T) {
	plane := &fakeControlPlane{}
	server := httptest.NewServer(plane)
	defer server.Close()
	var publicOnly atomic.Int32
	agent := Agent{
		Endpoint: server.URL, Token: TokenPrefix + "test", Version: "test", Concurrency: 4,
		Run: func(_ context.Context, spec checks.Spec) checks.Result {
			if spec.PublicOnly {
				publicOnly.Add(1)
			}
			return checks.Result{Up: spec.Kind == checks.KindTCP, TotalMs: 12}
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- agent.Start(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for len(plane.reports()) < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("agent stopped with %v", err)
	}
	got := map[string]bool{}
	for _, report := range plane.reports() {
		got[report.MonitorID] = report.Up
	}
	if len(got) != 2 || got["m1"] || !got["m2"] {
		t.Fatalf("reported %+v", plane.reports())
	}
	if publicOnly.Load() != 2 {
		t.Fatalf("a probe without AllowPrivate must refuse private targets; %d of 2 checks were public-only", publicOnly.Load())
	}
}

func TestAgentStopsWhenItsTokenIsRevoked(t *testing.T) {
	plane := &fakeControlPlane{}
	plane.revoked.Store(true)
	server := httptest.NewServer(plane)
	defer server.Close()
	agent := Agent{Endpoint: server.URL, Token: TokenPrefix + "test"}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := agent.Start(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Start returned %v, want ErrUnauthorized", err)
	}
}

func TestChecksSurviveTheWireUnchanged(t *testing.T) {
	spec := checks.Spec{
		Kind: checks.KindKeyword, URL: "https://example.com", Method: http.MethodPost, Headers: map[string]string{"Authorization": "Bearer x"},
		Body: "{}", ExpectedStatuses: []int{200, 204}, Keyword: "ok", KeywordAbsent: true, FollowRedirects: true,
		Timeout: 7 * time.Second, PublicOnly: true,
	}
	payload, err := json.Marshal(CheckFromSpec("m", spec))
	if err != nil {
		t.Fatal(err)
	}
	var back Check
	if err = json.Unmarshal(payload, &back); err != nil {
		t.Fatal(err)
	}
	got := back.Spec()
	if got.Kind != spec.Kind || got.URL != spec.URL || got.Method != spec.Method || got.Headers["Authorization"] != "Bearer x" ||
		got.Body != spec.Body || len(got.ExpectedStatuses) != 2 || got.Keyword != spec.Keyword || !got.KeywordAbsent ||
		!got.FollowRedirects || got.Timeout != spec.Timeout || !got.PublicOnly {
		t.Fatalf("round trip changed the spec: %+v", got)
	}
}
