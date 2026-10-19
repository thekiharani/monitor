package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func (f monitorFixture) statusPage(t *testing.T, body string) map[string]any {
	t.Helper()
	rec, out := f.call(t, http.MethodPost, "/api/v1/status-pages", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status page status=%d body=%s", rec.Code, rec.Body.String())
	}
	return data(out)
}

func (f monitorFixture) public(t *testing.T, host, path string) *httptest.ResponseRecorder {
	t.Helper()
	f.app.statusPages.clear()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = host
	return serve(f.app, req)
}

func uniqueSlug(label string) string {
	return fmt.Sprintf("%s-%d", label, time.Now().UnixNano()%1_000_000_000)
}

func TestIntegrationAStatusPageShowsComponentsWithoutLeakingTargets(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	api := f.create(t, `{"name":"internal api name","kind":"http","target":"https://secret-origin.example.com/health"}`)
	web := f.create(t, `{"name":"web","kind":"http","target":"https://www.example.com"}`)
	slug := uniqueSlug("acme")
	page := f.statusPage(t, fmt.Sprintf(`{"title":"Acme","slug":%q,"description":"Live status for Acme."}`, slug))
	id := page["id"].(string)
	if page["public_url"] != "http://status.local:8900/"+slug {
		t.Fatalf("public_url = %v", page["public_url"])
	}
	rec, out := f.call(t, http.MethodPut, "/api/v1/status-pages/"+id+"/components", fmt.Sprintf(
		`{"items":[{"monitor_id":%q,"display_name":"API","group_name":"Platform"},{"monitor_id":%q,"display_name":"Website","group_name":"Platform"}]}`, api, web))
	if rec.Code != http.StatusOK || len(data(out)["components"].([]any)) != 2 {
		t.Fatalf("components status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := f.db.Exec(context.Background(), `UPDATE monitors SET state='up' WHERE id = ANY($1::uuid[])`, []string{api, web}); err != nil {
		t.Fatal(err)
	}

	res := f.public(t, "localhost:8900", "/status/"+slug)
	html := res.Body.String()
	if res.Code != http.StatusOK || !strings.Contains(html, "All systems operational") || !strings.Contains(html, "Platform") || !strings.Contains(html, ">API<") {
		t.Fatalf("status page status=%d body=%s", res.Code, html)
	}
	for _, secret := range []string{"secret-origin", "internal api name", "www.example.com"} {
		if strings.Contains(html, secret) {
			t.Errorf("the public page leaked %q", secret)
		}
	}
	if csp := res.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") {
		t.Errorf("status page CSP = %q", csp)
	}
	if strings.Count(html, `title="`) < 2*statusBarDays {
		t.Error("each component should carry a bar per day")
	}

	if _, err := f.db.Exec(context.Background(), `UPDATE monitors SET state='down' WHERE id=$1`, api); err != nil {
		t.Fatal(err)
	}
	if html = f.public(t, "localhost:8900", "/status/"+slug).Body.String(); !strings.Contains(html, "Partial outage") || !strings.Contains(html, "Outage") {
		t.Fatal("one component down out of two should read as a partial outage")
	}
	if _, err := f.db.Exec(context.Background(), `UPDATE monitors SET state='down' WHERE id=$1`, web); err != nil {
		t.Fatal(err)
	}
	if html = f.public(t, "localhost:8900", "/status/"+slug).Body.String(); !strings.Contains(html, "Major outage") {
		t.Fatal("every component down should read as a major outage")
	}

	var feed map[string]any
	res = f.public(t, "localhost:8900", "/status/"+slug+"/status.json")
	if err := json.Unmarshal(res.Body.Bytes(), &feed); err != nil || feed["status"].(map[string]any)["indicator"] != "major" {
		t.Fatalf("status.json: %s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "secret-origin") || res.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("status.json leaked a target or blocks cross-origin widgets")
	}
	res = f.public(t, "localhost:8900", "/status/"+slug+"/badge.svg")
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(res.Body.String(), "major outage") {
		t.Fatalf("badge: %d %s", res.Code, res.Body.String())
	}
}

func TestIntegrationUptimeBarsCountConfirmedDowntimeOnly(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	id := f.create(t, `{"name":"api","kind":"http","target":"https://api.example.com"}`)
	slug := uniqueSlug("bars")
	f.statusPage(t, fmt.Sprintf(`{"title":"Bars","slug":%q,"monitor_ids":[%q]}`, slug, id))
	ctx := context.Background()
	if _, err := f.db.Exec(ctx, `UPDATE monitors SET created_at=now()-interval '10 days', state='up' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	yesterday := time.Now().UTC().Truncate(24 * time.Hour).Add(-24*time.Hour + 6*time.Hour)
	if _, err := f.db.Exec(ctx, `INSERT INTO incidents(monitor_id,account_id,started_at,resolved_at,cause) VALUES($1,$2,$3,$4,'refusing connections')`,
		id, f.accountID, yesterday, yesterday.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	html := f.public(t, "localhost:8900", "/status/"+slug).Body.String()
	label := yesterday.Format("2 Jan 2006")
	if !strings.Contains(html, label+" · 1 incident, 10m 0s down") {
		t.Fatalf("yesterday's bar does not report the outage:\n%s", html)
	}
	if !strings.Contains(html, `class="warn" title="`+label) {
		t.Error("a ten-minute outage should be a warning bar, not critical")
	}
	if !strings.Contains(html, "No data") || strings.Contains(html, "<b>100%</b>") {
		t.Error("days before the monitor existed should be empty, and the outage should dent uptime")
	}
}

func TestIntegrationNoticesMoveFromActiveToHistory(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	slug := uniqueSlug("notices")
	page := f.statusPage(t, fmt.Sprintf(`{"title":"Notices","slug":%q}`, slug))
	id := page["id"].(string)
	rec, out := f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/posts", `{"kind":"incident","title":"Elevated API errors","impact":"major","body":"We are looking into failed requests."}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post status=%d body=%s", rec.Code, rec.Body.String())
	}
	post := data(out)["id"].(string)
	html := f.public(t, "localhost:8900", "/status/"+slug).Body.String()
	if !strings.Contains(html, "Elevated API errors") || !strings.Contains(html, "Major outage") || !strings.Contains(html, "We are looking into failed requests.") {
		t.Fatalf("active notice missing:\n%s", html)
	}

	if rec, _ = f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/posts/"+post+"/updates", `{"status":"completed","body":"wrong kind"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a maintenance status on an incident status=%d, want 422", rec.Code)
	}
	if rec, _ = f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/posts/"+post+"/updates", `{"status":"resolved","body":"Fixed by rolling back the deploy."}`); rec.Code != http.StatusCreated {
		t.Fatalf("update status=%d body=%s", rec.Code, rec.Body.String())
	}
	html = f.public(t, "localhost:8900", "/status/"+slug).Body.String()
	if !strings.Contains(html, "All systems operational") || !strings.Contains(html, "Fixed by rolling back the deploy.") {
		t.Fatalf("resolved notice should move to history:\n%s", html)
	}
	_, out = f.call(t, http.MethodGet, "/api/v1/status-pages/"+id+"/posts", "")
	if updates := out["data"].([]any)[0].(map[string]any)["updates"].([]any); len(updates) != 2 {
		t.Fatalf("updates = %d, want 2", len(updates))
	}

	starts := time.Now().Add(time.Hour).UTC()
	if rec, _ = f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/posts", fmt.Sprintf(`{"kind":"maintenance","title":"Database upgrade","body":"Expect brief read-only periods.","starts_at":%q,"ends_at":%q}`,
		starts.Format(time.RFC3339), starts.Add(time.Hour).Format(time.RFC3339))); rec.Code != http.StatusCreated {
		t.Fatalf("maintenance post status=%d body=%s", rec.Code, rec.Body.String())
	}
	if html = f.public(t, "localhost:8900", "/status/"+slug).Body.String(); !strings.Contains(html, "Database upgrade") || !strings.Contains(html, "All systems operational") {
		t.Fatal("scheduled maintenance should be listed without changing today's status")
	}
}

func TestIntegrationSlugsArePlanLimitedAndUnique(t *testing.T) {
	free := newMonitorFixture(t, "free")
	other := newMonitorFixture(t, "pro")
	other.app = free.app
	slug := uniqueSlug("taken")
	other.statusPage(t, fmt.Sprintf(`{"title":"First","slug":%q}`, slug))
	if rec, _ := free.call(t, http.MethodPost, "/api/v1/status-pages", fmt.Sprintf(`{"title":"Copy","slug":%q}`, slug)); rec.Code != http.StatusConflict {
		t.Fatalf("a taken slug status=%d, want 409", rec.Code)
	}
	page := free.statusPage(t, `{"title":"Acme Payments!"}`)
	if !strings.HasPrefix(page["slug"].(string), "acme-payments") {
		t.Fatalf("derived slug = %v", page["slug"])
	}
	if rec, _ := free.call(t, http.MethodPost, "/api/v1/status-pages", `{"title":"Second"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a second page on Free status=%d, want 429", rec.Code)
	}
	if rec, _ := free.call(t, http.MethodPatch, "/api/v1/status-pages/"+page["id"].(string), `{"custom_domain":"status.acme.test"}`); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("a custom domain on Free status=%d, want 429", rec.Code)
	}
	if rec, _ := free.call(t, http.MethodPatch, "/api/v1/status-pages/"+page["id"].(string), `{"slug":"Bad Slug"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an invalid slug status=%d, want 422", rec.Code)
	}
}

func TestIntegrationCustomDomainsServeOnlyOnceVerified(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	domain := fmt.Sprintf("status-%d.customer.test", time.Now().UnixNano()%1_000_000_000)
	page := f.statusPage(t, fmt.Sprintf(`{"title":"Customer","slug":%q}`, uniqueSlug("customer")))
	id := page["id"].(string)
	rec, out := f.call(t, http.MethodPatch, "/api/v1/status-pages/"+id, fmt.Sprintf(`{"custom_domain":%q}`, strings.ToUpper(domain)))
	if rec.Code != http.StatusOK {
		t.Fatalf("set domain status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cd := data(out)["custom_domain"].(map[string]any); cd["hostname"] != domain || cd["verified"] != false || cd["cname_target"] != "status.local" {
		t.Fatalf("custom_domain view: %+v", cd)
	}
	if res := f.public(t, domain, "/"); res.Code != http.StatusNotFound {
		t.Fatalf("an unverified domain served the page: %d", res.Code)
	}
	ask := func() int {
		req := httptest.NewRequest(http.MethodGet, "/internal/domains/ask?domain="+domain, nil)
		req.Host = "localhost:8900"
		return serve(f.app, req).Code
	}
	if ask() != http.StatusNotFound {
		t.Fatal("TLS was offered for an unverified domain")
	}

	f.app.resolveCNAME = func(context.Context, string) (string, error) { return "elsewhere.example.net.", nil }
	if rec, _ = f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/domain/verify", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "elsewhere.example.net") {
		t.Fatalf("a wrong CNAME verified: %d %s", rec.Code, rec.Body.String())
	}
	f.app.resolveCNAME = func(context.Context, string) (string, error) { return "status.local.", nil }
	if rec, _ = f.call(t, http.MethodPost, "/api/v1/status-pages/"+id+"/domain/verify", ""); rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	if res := f.public(t, domain, "/"); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Customer") {
		t.Fatalf("the verified domain did not serve the page: %d", res.Code)
	}
	if ask() != http.StatusOK {
		t.Fatal("TLS was refused for a verified domain")
	}
	if rec, _ = f.call(t, http.MethodPatch, "/api/v1/status-pages/"+id, `{"published":false}`); rec.Code != http.StatusOK {
		t.Fatal("unpublish failed")
	}
	if res := f.public(t, domain, "/"); res.Code != http.StatusNotFound {
		t.Fatal("an unpublished page is still public")
	}
}

func TestIntegrationASeparateStatusOriginServesPagesAtTheRoot(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.cfg.StatusOrigin = "http://status.local:8900"
	slug := uniqueSlug("root")
	page := f.statusPage(t, fmt.Sprintf(`{"title":"Rooted","slug":%q}`, slug))
	if page["public_url"] != "http://status.local:8900/"+slug {
		t.Fatalf("public_url = %v", page["public_url"])
	}
	if res := f.public(t, "status.local:8900", "/"+slug); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Rooted") {
		t.Fatalf("status origin: %d", res.Code)
	}
	if res := f.public(t, "status.local:8900", "/api/v1/me"); res.Code != http.StatusNotFound {
		t.Fatalf("the status origin exposed the API: %d", res.Code)
	}
}

func TestIntegrationStatusPagesAreTenantIsolated(t *testing.T) {
	victim := newMonitorFixture(t, "pro")
	attacker := newMonitorFixture(t, "pro")
	attacker.app = victim.app
	monitorID := victim.create(t, `{"name":"victim","kind":"http","target":"https://victim.example.com"}`)
	page := victim.statusPage(t, fmt.Sprintf(`{"title":"Victim","slug":%q}`, uniqueSlug("victim")))
	id := page["id"].(string)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/status-pages/" + id, ""},
		{http.MethodPatch, "/api/v1/status-pages/" + id, `{"title":"pwned"}`},
		{http.MethodDelete, "/api/v1/status-pages/" + id, ""},
		{http.MethodPut, "/api/v1/status-pages/" + id + "/components", `{"items":[]}`},
		{http.MethodPost, "/api/v1/status-pages/" + id + "/posts", `{"kind":"incident","title":"fake","body":"fake"}`},
	} {
		if rec, _ := attacker.call(t, tc.method, tc.path, tc.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by another tenant status=%d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	own := attacker.statusPage(t, fmt.Sprintf(`{"title":"Mine","slug":%q}`, uniqueSlug("mine")))
	if rec, _ := attacker.call(t, http.MethodPut, "/api/v1/status-pages/"+own["id"].(string)+"/components", fmt.Sprintf(`{"items":[{"monitor_id":%q}]}`, monitorID)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("listing another tenant's monitor status=%d, want 422", rec.Code)
	}
}
