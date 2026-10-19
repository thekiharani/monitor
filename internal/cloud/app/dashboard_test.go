package app

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestDashboardHandlerServesSPAAndAssets(t *testing.T) {
	files := fstest.MapFS{
		"index.html":           &fstest.MapFile{Data: []byte("<main>Monitor</main>")},
		"assets/app.js":        &fstest.MapFile{Data: []byte("export {}")},
		"favicon.svg":          &fstest.MapFile{Data: []byte("<svg>Monitor</svg>")},
		"favicon.ico":          &fstest.MapFile{Data: []byte("ico")},
		"favicon-16x16.png":    &fstest.MapFile{Data: []byte("16")},
		"favicon-32x32.png":    &fstest.MapFile{Data: []byte("32")},
		"apple-touch-icon.png": &fstest.MapFile{Data: []byte("180")},
		"icon-192.png":         &fstest.MapFile{Data: []byte("192")},
		"icon-512.png":         &fstest.MapFile{Data: []byte("512")},
		"site.webmanifest":     &fstest.MapFile{Data: []byte(`{"name":"Monitor"}`)},
		"branding/badge.png":   &fstest.MapFile{Data: []byte("badge")},
	}
	handler := newDashboardHandler(files, "https://monitor.noria.co.ke")

	for _, path := range []string{"/login", "/app", "/app/monitors/example"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || response.Body.String() != "<main>Monitor</main>" {
				t.Fatalf("expected SPA entry point, got status %d and body %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("expected no-cache for entry point, got %q", got)
			}
		})
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/app.js", nil))
	if response.Code != http.StatusOK || response.Body.String() != "export {}" {
		t.Fatalf("expected asset, got status %d and body %q", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable asset caching, got %q", got)
	}

	for _, asset := range []struct {
		path        string
		body        string
		contentType string
	}{
		{path: "/favicon.svg", body: "<svg>Monitor</svg>", contentType: "image/svg+xml"},
		{path: "/favicon.ico", body: "ico", contentType: "image/vnd.microsoft.icon"},
		{path: "/favicon-16x16.png", body: "16", contentType: "image/png"},
		{path: "/favicon-32x32.png", body: "32", contentType: "image/png"},
		{path: "/apple-touch-icon.png", body: "180", contentType: "image/png"},
		{path: "/icon-192.png", body: "192", contentType: "image/png"},
		{path: "/icon-512.png", body: "512", contentType: "image/png"},
		{path: "/site.webmanifest", body: `{"name":"Monitor"}`, contentType: "application/manifest+json"},
		{path: "/branding/badge.png", body: "badge", contentType: "image/png"},
	} {
		t.Run(asset.path, func(t *testing.T) {
			if !handler.handles(asset.path) {
				t.Fatalf("expected discovered static asset to be handled: %s", asset.path)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, asset.path, nil))
			if response.Code != http.StatusOK || response.Body.String() != asset.body {
				t.Fatalf("expected root static asset, got status %d and body %q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Cache-Control"); got != "public, max-age=3600" {
				t.Fatalf("expected short-lived root asset caching, got %q", got)
			}
			if got := response.Header().Get("Content-Type"); got != asset.contentType {
				t.Fatalf("expected content type %q, got %q", asset.contentType, got)
			}
		})
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, "/assets/missing.js", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("expected missing asset to be 404, got %d", missing.Code)
	}
}

func TestDashboardHandlerIgnoresAbsentOptionalAssets(t *testing.T) {
	handler := newDashboardHandler(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("ok")},
	}, "https://monitor.noria.co.ke")
	for _, path := range []string{"/favicon.ico", "/icon-512.png", "/site.webmanifest", "/branding/badge.png"} {
		if handler.handles(path) {
			t.Fatalf("expected absent optional asset to remain unclaimed: %s", path)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("expected absent optional asset to be 404, got %d for %s", response.Code, path)
		}
	}
}

func TestDashboardPageDoesNotClaimBackendOrShortLinkRoutes(t *testing.T) {
	for _, path := range []string{"/auth/otp/request", "/api/dashboard/v1/me", "/billing/paystack/callback", "/health/live", "/short123", "/unknown-client-route"} {
		if isDashboardPage(path) {
			t.Errorf("expected %q to remain a backend route", path)
		}
	}
	for _, path := range []string{"/control", "/control/accounts", "/app", "/app/monitors", "/assets/app.js", "/login"} {
		if !isDashboardPage(path) {
			t.Fatalf("expected dashboard page route: %s", path)
		}
	}
	for _, path := range []string{"/", "/docs", "/docs/quickstart", "/docs/reference/links"} {
		if isDashboardPage(path) {
			t.Errorf("%q belongs to the marketing site, not the dashboard SPA", path)
		}
	}
	if isDashboardPage("/api/control/v1/accounts") {
		t.Fatal("control API routes must not be handled by the SPA")
	}
	for _, path := range []string{"/docs/openapi.yaml", "/docs/config.json"} {
		if isDashboardPage(path) {
			t.Fatalf("expected %q to remain a backend route", path)
		}
	}
}

func TestDashboardFilesAreValidFS(t *testing.T) {
	var files fs.FS = fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}}
	response := httptest.NewRecorder()
	newDashboardHandler(files, "https://monitor.noria.co.ke").ServeHTTP(response, httptest.NewRequest(http.MethodHead, "/login", nil))
	if response.Code != http.StatusOK || response.Body.Len() != 0 {
		t.Fatalf("expected successful HEAD without body, got %d and %q", response.Code, response.Body.String())
	}
}
