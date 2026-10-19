package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/checks"
)

type flakySite struct {
	server  *httptest.Server
	failing atomic.Bool
}

func newFlakySite(t *testing.T) *flakySite {
	t.Helper()
	site := &flakySite{}
	site.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if site.failing.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(site.server.Close)
	return site
}

type hookInbox struct {
	server *httptest.Server
	mu     sync.Mutex
	got    []map[string]any
}

func newHookInbox(t *testing.T) *hookInbox {
	t.Helper()
	inbox := &hookInbox{}
	inbox.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		body["_signature"] = r.Header.Get("X-Noria-Signature")
		inbox.mu.Lock()
		inbox.got = append(inbox.got, body)
		inbox.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(inbox.server.Close)
	return inbox
}

func (h *hookInbox) types() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, body := range h.got {
		out = append(out, body["type"].(string))
	}
	return out
}

func (f monitorFixture) webhookChannel(t *testing.T, inbox *hookInbox, extra string) string {
	t.Helper()
	rec, out := f.call(t, http.MethodPost, "/api/v1/alert-channels", fmt.Sprintf(`{"kind":"webhook","name":"ops hook","url":%q%s}`, inbox.server.URL, extra))
	if rec.Code != http.StatusCreated {
		t.Fatalf("channel status=%d body=%s", rec.Code, rec.Body.String())
	}
	if secret, _ := data(out)["signing_secret"].(string); !strings.HasPrefix(secret, "whsec_") {
		t.Fatalf("the signing secret is not shown on creation: %+v", data(out))
	}
	return data(out)["id"].(string)
}

func (f monitorFixture) check(t *testing.T, id string) {
	t.Helper()
	f.makeDue(t, id)
	f.runDue(t, "local")
	f.app.deliverAlerts(context.Background())
}

func (f monitorFixture) state(t *testing.T, id string) string {
	t.Helper()
	_, out := f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	return data(out)["state"].(string)
}

func TestIntegrationAFailureIsConfirmedBeforeAnIncidentOpensAndAlerts(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	site := newFlakySite(t)
	inbox := newHookInbox(t)
	f.webhookChannel(t, inbox, "")
	id := f.create(t, fmt.Sprintf(`{"name":"site","kind":"http","target":%q}`, site.server.URL))

	f.check(t, id)
	if f.state(t, id) != "up" {
		t.Fatal("first passing check did not mark the monitor up")
	}

	site.failing.Store(true)
	f.makeDue(t, id)
	f.runDue(t, "local")
	if f.state(t, id) != "up" {
		t.Fatal("a single failure took the monitor down without confirmation")
	}
	var nextRun time.Time
	if err := f.db.QueryRow(context.Background(), `SELECT next_run_at FROM monitor_schedules WHERE monitor_id=$1`, id).Scan(&nextRun); err != nil {
		t.Fatal(err)
	}
	if time.Until(nextRun) > confirmRetryDelay+time.Second {
		t.Fatalf("the confirmation retry is %s away, want within %s", time.Until(nextRun), confirmRetryDelay)
	}

	f.check(t, id)
	if f.state(t, id) != "down" {
		t.Fatal("two failures in a row did not take the monitor down")
	}
	rec, out := f.call(t, http.MethodGet, "/api/v1/incidents?status=open", "")
	incidents := out["data"].([]any)
	if rec.Code != http.StatusOK || len(incidents) != 1 || incidents[0].(map[string]any)["cause"] != "returning HTTP 502" {
		t.Fatalf("open incidents: %s", rec.Body.String())
	}
	if got := inbox.types(); len(got) != 1 || got[0] != "monitor.down" {
		t.Fatalf("webhook deliveries = %v, want one down alert", got)
	}

	f.check(t, id)
	if got := inbox.types(); len(got) != 1 {
		t.Fatalf("a monitor that stayed down alerted again: %v", got)
	}

	site.failing.Store(false)
	f.check(t, id)
	if f.state(t, id) != "up" {
		t.Fatal("a passing check did not recover the monitor")
	}
	if got := inbox.types(); len(got) != 2 || got[1] != "monitor.up" {
		t.Fatalf("webhook deliveries = %v, want down then up", got)
	}
	_, out = f.call(t, http.MethodGet, "/api/v1/incidents?status=resolved", "")
	if resolved := out["data"].([]any); len(resolved) != 1 || resolved[0].(map[string]any)["resolved_at"] == nil {
		t.Fatalf("the incident was not resolved: %+v", resolved)
	}
}

func TestIntegrationAMultiRegionMonitorNeedsAMajorityToGoDown(t *testing.T) {
	f := newMonitorFixture(t, "business")
	f.app.ensurePartitions(context.Background())
	id := f.create(t, `{"name":"api","kind":"http","target":"https://api.example.com","regions":["local","fra","iad"]}`)
	ctx := context.Background()
	m := dueCheck{MonitorID: id, AccountID: f.accountID}
	record := func(region string, up bool) {
		t.Helper()
		m.Region = region
		res := checks.Result{Up: up, StatusCode: 200, TotalMs: 12}
		if !up {
			res = checks.Result{Error: "refusing connections"}
		}
		if err := f.app.recordCheck(ctx, m, time.Now().UTC(), res); err != nil {
			t.Fatal(err)
		}
	}
	for _, region := range []string{"local", "fra", "iad"} {
		record(region, true)
	}
	record("local", false)
	record("local", false)
	if f.state(t, id) != "up" {
		t.Fatal("one confirmed region out of three took the monitor down")
	}
	record("fra", false)
	record("fra", false)
	if f.state(t, id) != "down" {
		t.Fatal("two confirmed regions out of three did not take the monitor down")
	}
	var regions []string
	if err := f.db.QueryRow(ctx, `SELECT regions_down FROM incidents WHERE monitor_id=$1 AND resolved_at IS NULL`, id).Scan(&regions); err != nil {
		t.Fatal(err)
	}
	if strings.Join(regions, ",") != "fra,local" {
		t.Fatalf("regions_down = %v", regions)
	}
	record("fra", true)
	if f.state(t, id) != "up" {
		t.Fatal("dropping below the majority did not recover the monitor")
	}
}

func TestIntegrationMaintenanceKeepsIncidentsButSilencesAlerts(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	site := newFlakySite(t)
	inbox := newHookInbox(t)
	f.webhookChannel(t, inbox, "")
	id := f.create(t, fmt.Sprintf(`{"name":"site","kind":"http","target":%q}`, site.server.URL))
	f.check(t, id)

	now := time.Now().UTC()
	rec, _ := f.call(t, http.MethodPost, "/api/v1/maintenance", fmt.Sprintf(`{"title":"Database upgrade","starts_at":%q,"ends_at":%q,"monitor_ids":[%q]}`,
		now.Add(-time.Minute).Format(time.RFC3339), now.Add(time.Hour).Format(time.RFC3339), id))
	if rec.Code != http.StatusCreated {
		t.Fatalf("maintenance status=%d body=%s", rec.Code, rec.Body.String())
	}
	site.failing.Store(true)
	f.check(t, id)
	f.check(t, id)
	if f.state(t, id) != "down" {
		t.Fatal("maintenance hid the outage from the monitor's state")
	}
	site.failing.Store(false)
	f.check(t, id)
	if got := inbox.types(); len(got) != 0 {
		t.Fatalf("alerts went out during maintenance: %v", got)
	}
	_, out := f.call(t, http.MethodGet, "/api/v1/maintenance", "")
	if windows := out["data"].([]any); len(windows) != 1 || windows[0].(map[string]any)["state"] != "active" {
		t.Fatalf("maintenance list: %+v", windows)
	}
}

func TestIntegrationChannelsOnlyHearAboutTheirMonitors(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	site := newFlakySite(t)
	watched := f.create(t, fmt.Sprintf(`{"name":"watched","kind":"http","target":%q}`, site.server.URL+"/a"))
	ignored := f.create(t, fmt.Sprintf(`{"name":"ignored","kind":"http","target":%q}`, site.server.URL+"/b"))
	inbox := newHookInbox(t)
	f.webhookChannel(t, inbox, fmt.Sprintf(`,"all_monitors":false,"monitor_ids":[%q]`, watched))

	site.failing.Store(true)
	for range 2 {
		f.check(t, watched)
		f.check(t, ignored)
	}
	if f.state(t, ignored) != "down" {
		t.Fatal("setup: the ignored monitor should be down too")
	}
	if got := inbox.types(); len(got) != 1 {
		t.Fatalf("deliveries = %v, want only the watched monitor's alert", got)
	}
	if name := inbox.got[0]["data"].(map[string]any)["monitor_name"]; name != "watched" {
		t.Fatalf("alerted about %v", name)
	}
}

func TestIntegrationEveryChannelKindDelivers(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	var telegramCalls atomic.Int32
	telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		telegramCalls.Add(1)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
	}))
	defer telegram.Close()
	f.app.cfg.TelegramBotToken, f.app.cfg.TelegramAPIBase = "T", telegram.URL

	rec, out := f.call(t, http.MethodPost, "/api/v1/alert-channels", `{"kind":"email","name":"on-call","emails":["Ops@Example.test","ops@example.test","pager@example.test"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("email channel: %s", rec.Body.String())
	}
	emailID := data(out)["id"].(string)
	if emails := data(out)["config"].(map[string]any)["emails"].([]any); len(emails) != 2 {
		t.Fatalf("addresses were not normalised and deduplicated: %v", emails)
	}
	rec, out = f.call(t, http.MethodPost, "/api/v1/alert-channels", `{"kind":"telegram","name":"chat","chat_id":"-1001234567"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("telegram channel: %s", rec.Body.String())
	}
	telegramID := data(out)["id"].(string)

	for _, id := range []string{emailID, telegramID} {
		if rec, _ = f.call(t, http.MethodPost, "/api/v1/alert-channels/"+id+"/test", ""); rec.Code != http.StatusAccepted {
			t.Fatalf("test send status=%d", rec.Code)
		}
	}
	f.app.deliverAlerts(context.Background())

	var queued int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM email_jobs WHERE account_id=$1 AND template='monitor_alert'`, f.accountID).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("queued alert emails = %d (err=%v), want one per address", queued, err)
	}
	if telegramCalls.Load() == 0 {
		t.Fatal("the telegram test alert was never sent")
	}
	_, out = f.call(t, http.MethodGet, "/api/v1/alert-channels/"+telegramID+"/deliveries", "")
	if deliveries := out["data"].([]any); len(deliveries) != 1 || deliveries[0].(map[string]any)["status"] != "delivered" {
		t.Fatalf("deliveries: %+v", deliveries)
	}

	for _, body := range []string{
		`{"kind":"slack","name":"x","webhook_url":"https://evil.example.com/services/x"}`,
		`{"kind":"telegram","name":"x","chat_id":"not a chat"}`,
		`{"kind":"email","name":"x","emails":[]}`,
		`{"kind":"sms","name":"x"}`,
	} {
		if rec, _ = f.call(t, http.MethodPost, "/api/v1/alert-channels", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s status=%d, want 422", body, rec.Code)
		}
	}
}

func TestIntegrationHeartbeatsGoDownWhenPingsStopAndRecoverOnTheNextOne(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	rec, out := f.call(t, http.MethodPost, "/api/v1/monitors", `{"name":"nightly backup","kind":"heartbeat","interval_seconds":3600,"grace_seconds":300}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %s", rec.Body.String())
	}
	m := data(out)
	id, pingURL := m["id"].(string), m["ping_url"].(string)
	if m["target"] != nil || !strings.HasPrefix(pingURL, f.app.cfg.APIOrigin+"/ping/") {
		t.Fatalf("heartbeat view: %+v", m)
	}
	_, token, _ := strings.CutLast(pingURL, "/")
	ping := func() int {
		req := httptest.NewRequest(http.MethodPost, "/ping/"+token, nil)
		req.Host = "localhost:8900"
		return serve(f.app, req).Code
	}
	if code := ping(); code != http.StatusOK || f.state(t, id) != "up" {
		t.Fatalf("ping status=%d state=%s", code, f.state(t, id))
	}

	if _, err := f.db.Exec(context.Background(), `UPDATE monitors SET last_checked_at=now()-interval '2 hours', state_changed_at=now()-interval '2 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	f.app.sweepHeartbeats(context.Background())
	if f.state(t, id) != "down" {
		t.Fatal("a missed heartbeat did not take the monitor down")
	}
	if code := ping(); code != http.StatusOK || f.state(t, id) != "up" {
		t.Fatal("the next ping did not recover the monitor")
	}
	req := httptest.NewRequest(http.MethodGet, "/ping/"+strings.Repeat("x", 32), nil)
	req.Host = "localhost:8900"
	if code := serve(f.app, req).Code; code != http.StatusNotFound {
		t.Fatalf("unknown token status=%d", code)
	}
}

func TestIntegrationAnExpiringCertificateWarnsOncePerCertificate(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	inbox := newHookInbox(t)
	f.webhookChannel(t, inbox, "")
	id := f.create(t, `{"name":"tls","kind":"http","target":"https://tls.example.com","cert_warn_days":14}`)
	expires := time.Now().Add(5 * 24 * time.Hour).UTC().Truncate(time.Second)
	for range 3 {
		if err := f.app.recordCheck(context.Background(), dueCheck{MonitorID: id, AccountID: f.accountID, Region: "local"}, time.Now().UTC(),
			checks.Result{Up: true, StatusCode: 200, TotalMs: 5, CertExpiresAt: &expires}); err != nil {
			t.Fatal(err)
		}
		f.app.deliverAlerts(context.Background())
	}
	if got := inbox.types(); len(got) != 1 || got[0] != "monitor.certificate" {
		t.Fatalf("certificate warnings = %v, want exactly one", got)
	}
}

func TestIntegrationAlertingRoutesAreTenantIsolated(t *testing.T) {
	victim := newMonitorFixture(t, "pro")
	attacker := newMonitorFixture(t, "pro")
	attacker.app = victim.app
	inbox := newHookInbox(t)
	channel := victim.webhookChannel(t, inbox, "")
	monitorID := victim.create(t, `{"name":"victim","kind":"http","target":"https://victim.example.com"}`)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/v1/alert-channels/" + channel, `{"url":"https://evil.example.com"}`},
		{http.MethodDelete, "/api/v1/alert-channels/" + channel, ""},
		{http.MethodPost, "/api/v1/alert-channels/" + channel + "/test", ""},
	} {
		if rec, _ := attacker.call(t, tc.method, tc.path, tc.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by another tenant status=%d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	if rec, _ := attacker.call(t, http.MethodPost, "/api/v1/maintenance", fmt.Sprintf(`{"title":"x","starts_at":%q,"ends_at":%q,"monitor_ids":[%q]}`,
		time.Now().Format(time.RFC3339), time.Now().Add(time.Hour).Format(time.RFC3339), monitorID)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("maintenance on another tenant's monitor status=%d, want 422", rec.Code)
	}
	if rec, _ := attacker.call(t, http.MethodPost, "/api/v1/alert-channels", fmt.Sprintf(`{"kind":"webhook","name":"x","url":"https://evil.example.com","all_monitors":false,"monitor_ids":[%q]}`, monitorID)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("subscribing to another tenant's monitor status=%d, want 422", rec.Code)
	}
	rec, _ := attacker.call(t, http.MethodGet, "/api/v1/alert-channels", "")
	if strings.Contains(rec.Body.String(), channel) {
		t.Error("the channel list leaked another tenant's channel")
	}
}
