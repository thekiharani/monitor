package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServeErrorPageRendersBrandedHTML(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone, http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErrorPage(rec, httptest.NewRequest(http.MethodGet, "/", nil), status, "https://monitor.noria.co.ke", true)
			if rec.Code != status {
				t.Fatalf("status=%d want=%d", rec.Code, status)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("content-type=%q", ct)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "Monitor") || !strings.Contains(body, "error page") {
				t.Fatalf("expected branded HTML, got %d bytes", len(body))
			}
			if strings.Contains(body, "404 page not found") {
				t.Fatalf("bare error text leaked into the page")
			}
		})
	}
}

func TestDashboardServeHTTPUsesErrorPageForUnknown(t *testing.T) {
	handler := newDashboardHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<main>Monitor</main>")},
	}, "https://monitor.noria.co.ke")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/unknown/path", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Monitor") {
		t.Fatalf("expected branded error page, got %q", rec.Body.String())
	}
}

func TestErrorPageOnACustomerDomainIsStillOurs(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gone", nil)
	req.Host = "go.acme.example"
	rec := httptest.NewRecorder()
	writeErrorPage(rec, req, http.StatusNotFound, "https://monitor.noria.co.ke", false)

	body := rec.Body.String()
	for _, wanted := range []string{
		"M13 45C13 19 32 13 50 24",
		`Monitor<span class="brand-dot">.</span>`,
		"#009498",
		`href="https://monitor.noria.co.ke/favicon.svg"`,
		`href="https://monitor.noria.co.ke/"`,
		"Go to monitor.noria.co.ke",
	} {
		if !strings.Contains(body, wanted) {
			t.Errorf("a 404 of ours should carry %q wherever it is served", wanted)
		}
	}
}

func TestErrorPageNeverLinksBackIntoTheDomainThatServedIt(t *testing.T) {
	for _, onMonitor := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodGet, "/gone", nil)
		req.Host = "go.acme.example"
		rec := httptest.NewRecorder()
		writeErrorPage(rec, req, http.StatusNotFound, "https://monitor.noria.co.ke", onMonitor)

		for line := range strings.SplitSeq(rec.Body.String(), "\n") {
			for _, attribute := range []string{`href="/`, `src="/`} {
				if strings.Contains(line, attribute) {
					t.Errorf("onMonitor=%v: a relative %s would stay on the customer's domain: %s", onMonitor, attribute, strings.TrimSpace(line))
				}
			}
		}
	}
}

func TestErrorPageOnMonitorOffersTheDashboardRatherThanTheLanding(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/gone", nil)
	req.Host = "monitor.noria.co.ke"
	rec := httptest.NewRecorder()
	writeErrorPage(rec, req, http.StatusNotFound, "https://monitor.noria.co.ke", true)

	body := rec.Body.String()
	if !strings.Contains(body, "Back to dashboard") {
		t.Error("our own surfaces should offer the dashboard")
	}
	if strings.Contains(body, "Go to monitor.noria.co.ke") {
		t.Error("offering the landing page to someone already on it is noise")
	}
}

func TestOnMonitorSurfaceSeparatesOurHostsFromCustomerDomains(t *testing.T) {
	a := &App{}
	a.cfg.AppOrigin = "https://monitor.noria.co.ke"
	a.cfg.DashboardOrigin = "https://monitor.noria.co.ke"
	a.cfg.StatusOrigin = "https://monitor.noria.co.ke"

	for host, want := range map[string]bool{
		"monitor.noria.co.ke": true,
		"go.acme.example":     false,
		"":                    false,
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		if got := a.onMonitorSurface(req); got != want {
			t.Errorf("onMonitorSurface(%q)=%v, want %v", host, got, want)
		}
	}
}
