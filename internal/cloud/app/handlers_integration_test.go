package app

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
)

func sessionRequest(t *testing.T, method, path, secret string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = "localhost:8900"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: secret})
	if method != http.MethodGet && method != http.MethodHead {
		req.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "csrf-token"})
		req.Header.Set("X-CSRF-Token", "csrf-token")
	}
	return req
}

func serve(a *App, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestIntegrationDashboardMeAndAPIKeys(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("keys-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	createTestAccount(t, db, email)
	secret := newSession(t, db, email)

	rec := serve(a, sessionRequest(t, http.MethodGet, "/api/dashboard/v1/me", secret, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("me status=%d body=%s", rec.Code, rec.Body)
	}
	account := decodeBody(t, rec)["data"].(map[string]any)["account"].(map[string]any)
	if account["email"] != email {
		t.Fatalf("email=%v", account["email"])
	}
	var reissued bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-csrf" && c.MaxAge > 0 {
			reissued = true
		}
	}
	if !reissued {
		t.Fatal("dashboardMe did not re-issue a persistent CSRF cookie")
	}

	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/api-keys", secret, []byte(`{"name":"ci"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key status=%d body=%s", rec.Code, rec.Body)
	}
	key := decodeBody(t, rec)["data"].(map[string]any)
	id := key["id"].(string)

	rec = serve(a, sessionRequest(t, http.MethodGet, "/api/dashboard/v1/api-keys", secret, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list keys status=%d", rec.Code)
	}
	listed := decodeBody(t, rec)["data"].([]any)
	if len(listed) != 1 {
		t.Fatalf("keys=%d, want 1", len(listed))
	}
	prefix := listed[0].(map[string]any)["prefix"].(string)
	if len(prefix) > len("usk_test_")+4 {
		t.Fatalf("prefix %q exposes too much of the key", prefix)
	}

	rec = serve(a, sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/api-keys/"+id, secret, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body)
	}
	rec = serve(a, sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/api-keys/"+id, secret, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("re-revoking an owned key returned %d, want 204", rec.Code)
	}
	rec = serve(a, sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/api-keys/"+uuid.NewV7().String(), secret, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("revoking an unknown key returned %d, want 404", rec.Code)
	}
}

func TestIntegrationDashboardRequiresCSRF(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("csrf-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	createTestAccount(t, db, email)
	secret := newSession(t, db, email)

	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/v1/api-keys", bytes.NewReader([]byte(`{"name":"no csrf"}`)))
	req.Host = "localhost:8900"
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: secret})

	if rec := serve(a, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want 401 without a CSRF token", rec.Code)
	}
}

func TestIntegrationLogoutRevokesSessionAndCSRF(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("logout-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	createTestAccount(t, db, email)
	secret := newSession(t, db, email)

	rec := serve(a, sessionRequest(t, http.MethodPost, "/auth/logout", secret, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", rec.Code, rec.Body)
	}
	cleared := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	if !cleared["__Host-session"] || !cleared["__Host-csrf"] {
		t.Fatalf("logout left cookies behind: %v", cleared)
	}
	if rec := serve(a, sessionRequest(t, http.MethodGet, "/api/dashboard/v1/me", secret, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d, want 401", rec.Code)
	}
}

func TestIntegrationHealthChecksAnswerOnlyOnTheAppHost(t *testing.T) {
	db := openIntegrationDB(t)
	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	for path, host := range map[string]string{
		"/health/live":  "localhost:8900",
		"/health/ready": "localhost:8900",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = host
		if rec := serve(a, req); rec.Code != http.StatusOK {
			t.Errorf("%s status=%d", path, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	req.Host = "elsewhere.example"
	if rec := serve(a, req); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("wrong host status=%d, want 421", rec.Code)
	}
}

func TestIntegrationPublicAPIWithKey(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("apikey-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	createTestAccount(t, db, email)
	key := newAPIKey(t, db, email)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Host = "localhost:8900"
	req.Header.Set("Authorization", "Bearer "+key)
	rec := serve(a, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me via API status=%d body=%s", rec.Code, rec.Body)
	}
	if rec.Header().Get("RateLimit-Limit") == "" {
		t.Error("RateLimit-Limit header missing on an API response")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req.Host = "localhost:8900"
	req.Header.Set("Authorization", "Bearer "+key)
	if rec := serve(a, req); rec.Code != http.StatusOK {
		t.Fatalf("usage via API status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/usage", nil)
	req.Host = "localhost:8900"
	req.Header.Set("Authorization", "Bearer usk_test_nope")
	if rec := serve(a, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad key status=%d, want 401", rec.Code)
	}
}

func TestIntegrationControlSurface(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("control-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	_, token := grantOperator(t, db,
		"platform.accounts.read", "platform.usage.read",
		"platform.credentials.read", "platform.billing.read",
		"platform.jobs.read", "platform.access.read", "platform.audit.read")

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "localhost:8900"
		req.Header.Set("Authorization", "Bearer "+token)
		return serve(a, req)
	}

	for _, path := range []string{
		"/api/control/v1/session",
		"/api/control/v1/stats",
		"/api/control/v1/accounts",
		"/api/control/v1/accounts/" + accountID,
		"/api/control/v1/usage",
		"/api/control/v1/credentials",
		"/api/control/v1/billing",
		"/api/control/v1/billing/checkouts",
		"/api/control/v1/billing/events",
		"/api/control/v1/email-jobs",
		"/api/control/v1/access-grants",
		"/api/control/v1/audit-events",
	} {
		if rec := get(path); rec.Code != http.StatusOK {
			t.Errorf("%s status=%d body=%s", path, rec.Code, rec.Body)
		}
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/control/v1/accounts/"+accountID+"/status",
		bytes.NewReader([]byte(`{"status":"suspended","reason":"not permitted"}`)))
	req.Host = "localhost:8900"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if rec := serve(a, req); rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized suspend status=%d, want 403", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/control/v1/accounts", nil)
	req.Host = "localhost:8900"
	if rec := serve(a, req); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous control status=%d, want 401", rec.Code)
	}
}
