package app

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testApp(mutate func(*App)) *App {
	a := &App{
		cfg:      testConfig(),
		log:      slog.New(slog.NewJSONHandler(io.Discard, nil)),
		metrics:  newAppMetrics(),
		limiters: newLimiterSet(),
	}
	if mutate != nil {
		mutate(a)
	}
	return a
}

func corsApp(origins ...string) *App {
	return testApp(func(a *App) { a.cfg.CORSAllowedOrigins = origins })
}

func handler(a *App) http.Handler {
	return a.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestCORSCoversAPIPaths(t *testing.T) {
	a := corsApp("http://localhost:3900")
	for _, path := range []string{"/api/dashboard/v1/me", "/api/v1/links", "/auth/otp/request"} {
		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "http://localhost:3900")
		req.Header.Set("Access-Control-Request-Method", "GET")
		rec := httptest.NewRecorder()
		handler(a).ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Errorf("%s preflight status=%d, want 204", path, rec.Code)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3900" {
			t.Errorf("%s allow-origin=%q, want the request origin", path, got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("%s allow-credentials=%q", path, got)
		}
	}
}

func TestCORSRejectsUnlistedOrigin(t *testing.T) {
	a := corsApp("http://localhost:3900")
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard/v1/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("allow-origin=%q for an unlisted origin", got)
	}
	if got := rec.Header().Values("Vary"); len(got) == 0 {
		t.Fatal("Vary: Origin missing on a rejected origin")
	}
}

func TestOptionsWithoutPreflightHeadersReachesTheHandler(t *testing.T) {
	a := corsApp("http://localhost:3900")
	req := httptest.NewRequest(http.MethodOptions, "/api/v1/links", nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want the handler's 200", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	a := testApp(func(a *App) {
		a.cfg.HSTSMaxAge = 31536000
		a.cfg.HSTSHeader = "max-age=31536000; includeSubDomains; preload"
	})

	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	for header, want := range map[string]string{
		"Strict-Transport-Security":    "max-age=31536000; includeSubDomains; preload",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "same-origin",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Errorf("%s=%q, want %q", header, got, want)
		}
	}
}

func TestHSTSOmittedWhenDisabled(t *testing.T) {
	a := testApp(nil)
	rec := httptest.NewRecorder()
	handler(a).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health/live", nil))

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("HSTS=%q, want it absent when HSTSMaxAge is zero", got)
	}
}

func TestResponseWriterUnwraps(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec}
	if rw.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap did not return the wrapped writer")
	}
	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Fatalf("ResponseController.Flush: %v", err)
	}
}

func TestCrossSiteBrowserWritesAreRefusedButOtherClientsAreNot(t *testing.T) {
	a := corsApp("https://trusted.example")
	for _, c := range []struct {
		name    string
		headers map[string]string
		method  string
		refused bool
	}{
		{"cross-site browser post", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, http.MethodPost, true},
		{"cross-site browser read", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, http.MethodGet, false},
		{"same-origin browser post", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.MethodPost, false},
		{"trusted origin post", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://trusted.example"}, http.MethodPost, false},
		{"server-to-server post", nil, http.MethodPost, false},
	} {
		req := httptest.NewRequest(c.method, "/no-such-route", nil)
		for name, value := range c.headers {
			req.Header.Set(name, value)
		}
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, req)
		if refused := rec.Code == http.StatusForbidden; refused != c.refused {
			t.Errorf("%s: status=%d, refused=%v want %v", c.name, rec.Code, refused, c.refused)
		}
	}
}
