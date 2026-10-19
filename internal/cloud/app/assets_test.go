package app

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func versionedHandler(icon string) *dashboardHandler {
	return newDashboardHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(`<!doctype html><html><head>` +
			`<link rel="icon" href="/favicon.ico?v=3" sizes="any" />` +
			`<link rel="icon" type="image/svg+xml" href="/favicon.svg?v=3" />` +
			`<meta name="robots" content="noindex, nofollow" />` +
			`<title>Monitor</title></head><body></body></html>`)},
		"favicon.ico": &fstest.MapFile{Data: []byte(icon)},
		"favicon.svg": &fstest.MapFile{Data: []byte("<svg/>")},
	}, "https://monitor.noria.co.ke")
}

func servedEntry(t *testing.T, handler *dashboardHandler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

func TestIconsCarryAFingerprintOfTheirContent(t *testing.T) {
	body := servedEntry(t, versionedHandler("first icon"))

	for _, asset := range []string{"/favicon.ico", "/favicon.svg"} {
		reference := regexp.MustCompile(regexp.QuoteMeta(asset) + `\?v=[0-9a-f]{12}`)
		if !reference.MatchString(body) {
			t.Errorf("%s is referenced without a fingerprint, so a cache cannot tell one version from the next", asset)
		}
	}
	if strings.Contains(body, "?v=3") {
		t.Error("the hand-maintained ?v=3 survived; it does not change when the file does")
	}
}

func TestChangingAnIconChangesTheURLItIsServedAt(t *testing.T) {
	reference := regexp.MustCompile(`/favicon\.ico\?v=[0-9a-f]{12}`)
	before := reference.FindString(servedEntry(t, versionedHandler("first icon")))
	after := reference.FindString(servedEntry(t, versionedHandler("second icon")))

	if before == "" || after == "" {
		t.Fatalf("no fingerprinted icon URL: before=%q after=%q", before, after)
	}
	if before == after {
		t.Errorf("both versions of the icon share the URL %s, so a cached copy of the old one keeps being served", before)
	}
}

func TestTheFileItselfIsStillServedWithoutTheFingerprint(t *testing.T) {
	rec := httptest.NewRecorder()
	versionedHandler("first icon").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/favicon.ico?v=deadbeefcafe", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /favicon.ico with a fingerprint = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "first icon" {
		t.Errorf("body = %q, want the icon itself", rec.Body.String())
	}
}
