package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func loginRequest(method, path, host string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Host = host
	return r
}

func TestOnlyAReadOfTheLoginPageIsEverRedirected(t *testing.T) {
	a := &App{}
	a.cfg.DashboardOrigin = "https://monitor.noria.co.ke"

	for _, unrelated := range []*http.Request{
		loginRequest(http.MethodGet, "/app", "monitor.noria.co.ke"),
		loginRequest(http.MethodGet, "/", "monitor.noria.co.ke"),
		loginRequest(http.MethodPost, "/login", "monitor.noria.co.ke"),
		loginRequest(http.MethodGet, "/login", "somewhere-else.example"),
	} {
		if _, ok := a.signedInAtLoginPage(unrelated); ok {
			t.Errorf("%s %s on %s was treated as the login page", unrelated.Method, unrelated.URL.Path, unrelated.Host)
		}
	}
}

func TestAVisitorWithNoSessionStillSeesTheLoginPage(t *testing.T) {
	a := &App{}
	a.cfg.DashboardOrigin = "https://monitor.noria.co.ke"

	if _, ok := a.signedInAtLoginPage(loginRequest(http.MethodGet, "/login", "monitor.noria.co.ke")); ok {
		t.Error("a visitor with no session cookie was redirected away from signing in")
	}
}

func TestTheLoginPageOnlySendsAVisitorSomewhereThisDeploymentServes(t *testing.T) {
	for next, expected := range map[string]string{
		"":                         "/app",
		"/app":                     "/app",
		"/app/monitors":            "/app/monitors",
		"/control":                 "/control",
		"/control/accounts?q=a":    "/control/accounts",
		"/login":                   "/app",
		"//evil.example/app":       "/app",
		"https://evil.example/app": "/app",
		"/assets/index.js":         "/app",
		"/docs":                    "/app",
		"app":                      "/app",
		"\\\\evil.example":         "/app",
	} {
		if got := loginDestination(next); got != expected {
			t.Errorf("next=%q sent the visitor to %q, want %q", next, got, expected)
		}
	}
}
