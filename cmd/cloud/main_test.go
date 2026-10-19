package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPprofExposesGoroutineLeakProfile(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/goroutineleak?debug=1", nil)

	newPprofHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("goroutine leak profile status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHealthcheckProbesTheConfiguredPortWithTheOriginHost(t *testing.T) {
	var gotHost string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		if r.URL.Path != "/health/ready" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	t.Setenv("APP_ADDR", ":"+port)
	t.Setenv("APP_ORIGIN", "https://monitor.example.com")

	if code := healthcheck(); code != 0 {
		t.Fatalf("healthcheck exit=%d, want 0", code)
	}
	if gotHost != "monitor.example.com" {
		t.Fatalf("host=%q, want monitor.example.com", gotHost)
	}
}

func TestHealthcheckFailsWhenReadinessIsNotOK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, port, _ := net.SplitHostPort(server.Listener.Addr().String())
	t.Setenv("APP_ADDR", "127.0.0.1:"+port)
	t.Setenv("APP_ORIGIN", "")

	if code := healthcheck(); code != 1 {
		t.Fatalf("healthcheck exit=%d, want 1", code)
	}
}

func TestHealthcheckDefaultsWhenAddressHasNoPort(t *testing.T) {
	t.Setenv("APP_ADDR", "")
	t.Setenv("APP_ORIGIN", "")

	if got := healthcheckHost(); got != "localhost:"+defaultHealthcheckPort {
		t.Fatalf("host=%q", got)
	}
}

func TestDispatcherRoutesHelpAndRejectsInvalidCommandsWithoutConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"help"}, 0}, {[]string{"migrate", "--help"}, 0}, {[]string{"control", "--help"}, 0},
		{[]string{"migrate", "up"}, 2}, {[]string{"serve", "extra"}, 2}, {[]string{"unknown"}, 2},
	} {
		if got := dispatch(tc.args); got != tc.code {
			t.Fatalf("args=%v code=%d want=%d", tc.args, got, tc.code)
		}
	}
}
