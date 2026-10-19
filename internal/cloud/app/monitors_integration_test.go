package app

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/probe"
)

type monitorFixture struct {
	app       *App
	db        *pgxpool.Pool
	accountID string
	key       string
}

func newMonitorFixture(t *testing.T, plan string) monitorFixture {
	t.Helper()
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	email := fmt.Sprintf("monitors-%d@example.test", time.Now().UnixNano())
	t.Cleanup(func() { deleteTestAccount(t, db, email) })
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan=$2 WHERE id=$1`, accountID, plan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	return monitorFixture{app: a, db: db, accountID: accountID, key: newAPIKey(t, db, email)}
}

func (f monitorFixture) call(t *testing.T, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var payload []byte
	if body != "" {
		payload = []byte(body)
	}
	f.app.limiters = newLimiterSet()
	if _, err := f.db.Exec(context.Background(), `DELETE FROM rate_buckets WHERE bucket_key=$1`, "account:"+f.accountID); err != nil {
		t.Fatalf("reset rate limit: %v", err)
	}
	rec := serve(f.app, bearerRequest(t, method, path, f.key, payload))
	out := map[string]any{}
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func (f monitorFixture) create(t *testing.T, body string) string {
	t.Helper()
	rec, out := f.call(t, http.MethodPost, "/api/v1/monitors", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	return out["data"].(map[string]any)["id"].(string)
}

func (f monitorFixture) runDue(t *testing.T, region string) int {
	t.Helper()
	due, err := f.app.claimDueChecks(context.Background(), region, 100, "")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	ran := 0
	for _, d := range due {
		if d.AccountID != f.accountID || !d.Eligible {
			continue
		}
		f.app.runAndRecord(context.Background(), d)
		ran++
	}
	return ran
}

func (f monitorFixture) makeDue(t *testing.T, monitorID string) {
	t.Helper()
	if _, err := f.db.Exec(context.Background(), `UPDATE monitor_schedules SET next_run_at=now()-interval '1 second' WHERE monitor_id=$1`, monitorID); err != nil {
		t.Fatalf("make due: %v", err)
	}
}

func data(out map[string]any) map[string]any { return out["data"].(map[string]any) }

func TestIntegrationMonitorLifecycleOverTheBearerAPI(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())

	id := f.create(t, `{"name":"  Website ","kind":"http","target":"https://example.com/health"}`)
	rec, out := f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status=%d", rec.Code)
	}
	m := data(out)
	if m["name"] != "Website" || m["state"] != "pending" || m["interval_seconds"].(float64) != 60 || m["method"] != "GET" || m["follow_redirects"] != true {
		t.Fatalf("defaults not applied: %+v", m)
	}
	if regions := m["regions"].([]any); len(regions) != 1 || regions[0] != "local" {
		t.Fatalf("regions=%v, want the built-in probe's region", regions)
	}

	rec, out = f.call(t, http.MethodGet, "/api/v1/monitors", "")
	if rec.Code != http.StatusOK || len(out["data"].([]any)) != 1 {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec, out = f.call(t, http.MethodPatch, "/api/v1/monitors/"+id, `{"name":"Homepage","paused":true}`)
	if rec.Code != http.StatusOK || data(out)["state"] != "paused" || data(out)["paused"] != true {
		t.Fatalf("pause status=%d body=%s", rec.Code, rec.Body.String())
	}
	var schedules int
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM monitor_schedules WHERE monitor_id=$1`, id).Scan(&schedules); err != nil || schedules != 0 {
		t.Fatalf("a paused monitor kept %d schedules (err=%v)", schedules, err)
	}

	rec, out = f.call(t, http.MethodPatch, "/api/v1/monitors/"+id, `{"paused":false,"regions":["local","fra"]}`)
	if rec.Code != http.StatusOK || data(out)["state"] != "pending" {
		t.Fatalf("resume status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := f.db.QueryRow(context.Background(), `SELECT count(*) FROM monitor_schedules WHERE monitor_id=$1`, id).Scan(&schedules); err != nil || schedules != 2 {
		t.Fatalf("resumed monitor has %d schedules, want one per region (err=%v)", schedules, err)
	}

	if rec, _ = f.call(t, http.MethodPatch, "/api/v1/monitors/"+id, `{"kind":"tcp"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("changing the kind status=%d, want 422", rec.Code)
	}

	if rec, _ = f.call(t, http.MethodDelete, "/api/v1/monitors/"+id, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d", rec.Code)
	}
	if rec, _ = f.call(t, http.MethodGet, "/api/v1/monitors/"+id, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("a deleted monitor is still readable: %d", rec.Code)
	}
}

func TestIntegrationMonitorInputIsValidatedAgainstKindAndPlan(t *testing.T) {
	f := newMonitorFixture(t, "free")
	for _, tc := range []struct {
		name, body, code string
	}{
		{"missing kind", `{"name":"x","target":"https://example.com"}`, "invalid_monitor"},
		{"unknown kind", `{"name":"x","kind":"ping","target":"https://example.com"}`, "invalid_monitor"},
		{"http target without a scheme", `{"name":"x","kind":"http","target":"example.com"}`, "invalid_monitor"},
		{"tcp target without a port", `{"name":"x","kind":"tcp","target":"db.example.com"}`, "invalid_monitor"},
		{"keyword monitor without a keyword", `{"name":"x","kind":"keyword","target":"https://example.com"}`, "invalid_monitor"},
		{"header with a newline", `{"name":"x","kind":"http","target":"https://example.com","headers":{"X-A":"b\r\nX-B: c"}}`, "invalid_monitor"},
		{"status code out of range", `{"name":"x","kind":"http","target":"https://example.com","expected_statuses":[700]}`, "invalid_monitor"},
		{"a long timeout inside the interval", `{"name":"x","kind":"http","target":"https://example.com","interval_seconds":300,"timeout_ms":60000}`, ""},
		{"unknown region", `{"name":"x","kind":"http","target":"https://example.com","regions":["mars"]}`, "invalid_monitor"},
		{"interval below the plan minimum", `{"name":"x","kind":"http","target":"https://example.com","interval_seconds":60}`, "plan_limit_exceeded"},
		{"more regions than the plan allows", `{"name":"x","kind":"http","target":"https://example.com","regions":["local","fra"]}`, "plan_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, out := f.call(t, http.MethodPost, "/api/v1/monitors", tc.body)
			if tc.code == "" {
				if rec.Code != http.StatusCreated {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				return
			}
			errBody, _ := out["error"].(map[string]any)
			if rec.Code < 400 || errBody["code"] != tc.code {
				t.Fatalf("status=%d body=%s, want %s", rec.Code, rec.Body.String(), tc.code)
			}
		})
	}
}

func TestIntegrationMonitorCountStopsAtThePlanLimit(t *testing.T) {
	f := newMonitorFixture(t, "free")
	for i := range 10 {
		f.create(t, fmt.Sprintf(`{"name":"m%d","kind":"tcp","target":"db.example.com:%d","paused":true}`, i, 5000+i))
	}
	rec, out := f.call(t, http.MethodPost, "/api/v1/monitors", `{"name":"eleventh","kind":"tcp","target":"db.example.com:6000"}`)
	if rec.Code != http.StatusTooManyRequests || out["error"].(map[string]any)["code"] != "plan_limit_exceeded" {
		t.Fatalf("eleventh monitor status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationTheBuiltInProbeChecksRecordsAndRollsUp(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	var failing atomic.Bool
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("healthy"))
	}))
	defer site.Close()

	id := f.create(t, fmt.Sprintf(`{"name":"site","kind":"keyword","target":%q,"keyword":"healthy"}`, site.URL))
	if ran := f.runDue(t, "local"); ran != 1 {
		t.Fatalf("the probe ran %d checks, want 1", ran)
	}
	if ran := f.runDue(t, "local"); ran != 0 {
		t.Fatalf("a check that just ran was claimed again (%d)", ran)
	}
	_, out := f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	if m := data(out); m["state"] != "up" || m["last_status_code"].(float64) != 200 || m["last_response_ms"] == nil {
		t.Fatalf("after a passing check: %+v", m)
	}

	failing.Store(true)
	f.makeDue(t, id)
	f.runDue(t, "local")
	f.makeDue(t, id)
	f.runDue(t, "local")
	_, out = f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	if m := data(out); m["state"] != "down" || m["last_error"] != "returning HTTP 503" {
		t.Fatalf("after a failing check: %+v", m)
	}

	rec, out := f.call(t, http.MethodGet, "/api/v1/monitors/"+id+"/results", "")
	if rec.Code != http.StatusOK || len(out["data"].([]any)) != 3 {
		t.Fatalf("results status=%d body=%s", rec.Code, rec.Body.String())
	}
	newest := out["data"].([]any)[0].(map[string]any)
	if newest["up"] != false || newest["region"] != "local" {
		t.Fatalf("results are not newest first: %+v", newest)
	}

	rec, out = f.call(t, http.MethodGet, "/api/v1/monitors/"+id+"/daily", "")
	items := data(out)["items"].([]any)
	if rec.Code != http.StatusOK || len(items) != 1 {
		t.Fatalf("daily status=%d body=%s", rec.Code, rec.Body.String())
	}
	if day := items[0].(map[string]any); day["checks"].(float64) != 3 || day["failures"].(float64) != 2 {
		t.Fatalf("daily rollup: %+v", day)
	}

	rec, out = f.call(t, http.MethodGet, "/api/v1/monitors/"+id+"/series?range=1h", "")
	if rec.Code != http.StatusOK || len(data(out)["points"].([]any)) == 0 {
		t.Fatalf("series status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec, _ = f.call(t, http.MethodGet, "/api/v1/monitors/"+id+"/series?range=1y", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown range status=%d", rec.Code)
	}

	_, out = f.call(t, http.MethodGet, "/api/v1/monitors", "")
	if uptime := out["data"].([]any)[0].(map[string]any)["uptime"].(map[string]any); int(uptime["1d"].(float64)) != 33 {
		t.Fatalf("list uptime: %+v", uptime)
	}
}

func TestIntegrationProbesSkipSuspendedAccountsAndPausedMonitors(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	id := f.create(t, `{"name":"db","kind":"tcp","target":"db.example.com:5432"}`)
	if _, err := f.db.Exec(context.Background(), `UPDATE accounts SET status='suspended' WHERE id=$1`, f.accountID); err != nil {
		t.Fatal(err)
	}
	due, err := f.app.claimDueChecks(context.Background(), "local", 100, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.MonitorID == id && d.Eligible {
			t.Fatal("a suspended account's monitor was handed to the probe")
		}
	}
}

func TestIntegrationResultPartitionsAreCreatedAheadAndDroppedWhenExpired(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()
	a.ensurePartitions(ctx)
	ahead := partitionName(time.Now().UTC().AddDate(0, 0, partitionsAhead))
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, ahead).Scan(&exists); err != nil || !exists {
		t.Fatalf("partition %s missing (err=%v)", ahead, err)
	}

	old := time.Now().UTC().AddDate(0, 0, -rawResultRetention-5).Truncate(24 * time.Hour)
	if _, err := db.Exec(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s PARTITION OF check_results FOR VALUES FROM ('%s') TO ('%s')`,
		partitionName(old), old.Format(time.RFC3339), old.AddDate(0, 0, 1).Format(time.RFC3339))); err != nil {
		t.Fatal(err)
	}
	a.dropExpiredPartitions(ctx)
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, partitionName(old)).Scan(&exists); err != nil || exists {
		t.Fatalf("expired partition survived (err=%v)", err)
	}
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, ahead).Scan(&exists); err != nil || !exists {
		t.Fatal("a current partition was dropped")
	}
}

func TestIntegrationMonitorRoutesAreTenantIsolated(t *testing.T) {
	victim := newMonitorFixture(t, "pro")
	attacker := newMonitorFixture(t, "pro")
	attacker.app = victim.app
	id := victim.create(t, `{"name":"victim-secret-site","kind":"http","target":"https://victim.example.com"}`)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/monitors/" + id, ""},
		{http.MethodPatch, "/api/v1/monitors/" + id, `{"name":"pwned"}`},
		{http.MethodDelete, "/api/v1/monitors/" + id, ""},
		{http.MethodGet, "/api/v1/monitors/" + id + "/results", ""},
		{http.MethodGet, "/api/v1/monitors/" + id + "/series", ""},
		{http.MethodGet, "/api/v1/monitors/" + id + "/daily", ""},
	} {
		if rec, _ := attacker.call(t, tc.method, tc.path, tc.body); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s by another tenant status=%d, want 404", tc.method, tc.path, rec.Code)
		}
	}
	rec, _ := attacker.call(t, http.MethodGet, "/api/v1/monitors", "")
	if strings.Contains(rec.Body.String(), "victim-secret-site") || strings.Contains(rec.Body.String(), id) {
		t.Error("the monitor list leaked another tenant's monitor")
	}
	_, out := victim.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	if data(out)["name"] != "victim-secret-site" {
		t.Errorf("the victim's monitor changed: %+v", data(out))
	}
}

func TestIntegrationRequestSecretsAreSealedAtRestAndMaskedInResponses(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	id := f.create(t, `{"name":"private api","kind":"http","target":"https://example.com/private","method":"POST",
		"headers":{"authorization":"Bearer s3cret-value","Accept":"application/json"},"body":"{\"probe\":true}"}`)

	var sealed string
	if err := f.db.QueryRow(context.Background(), `SELECT request_encrypted FROM monitors WHERE id=$1`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed, "s3cret") || strings.Contains(sealed, "probe") {
		t.Fatalf("request is readable at rest: %s", sealed)
	}

	_, out := f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	headers := data(out)["headers"].(map[string]any)
	if headers["Authorization"] != maskedHeaderValue || headers["Accept"] != "application/json" || data(out)["body"] != `{"probe":true}` {
		t.Fatalf("response headers=%v body=%v", headers, data(out)["body"])
	}

	rec, _ := f.call(t, http.MethodPatch, "/api/v1/monitors/"+id, `{"headers":{"Authorization":"********","Accept":"text/plain"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := f.db.QueryRow(context.Background(), `SELECT request_encrypted FROM monitors WHERE id=$1`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	request, err := openMonitorRequest(f.app.cfg.AppEncryptionKey, &sealed)
	if err != nil || request.Headers["Authorization"] != "Bearer s3cret-value" || request.Headers["Accept"] != "text/plain" {
		t.Fatalf("a masked value did not keep the stored secret: %+v err=%v", request.Headers, err)
	}

	rec, out = f.call(t, http.MethodPatch, "/api/v1/monitors/"+id, `{"headers":{"X-Api-Key":"********"}}`)
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Fatalf("a masked value for a new header was accepted: status=%d body=%v", rec.Code, out)
	}
}

func TestIntegrationARemoteProbeChecksOnlyItsRegionAndConfirmsOutages(t *testing.T) {
	f := newMonitorFixture(t, "pro")
	f.app.ensurePartitions(context.Background())
	ctx := context.Background()
	token := probe.TokenPrefix + strings.ReplaceAll(uuid.NewV7().String(), "-", "")
	var probeID string
	if err := f.db.QueryRow(ctx, `INSERT INTO probes(region,name,key_prefix,secret_hash) VALUES('fra','fra-1',$1,$2) RETURNING id::text`,
		token[:15], config.HashSecret(token)).Scan(&probeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec(ctx, `DELETE FROM probes WHERE id=$1`, probeID) })

	id := f.create(t, `{"name":"edge","kind":"http","target":"https://example.com","regions":["local","fra"],"headers":{"Authorization":"Bearer edge-secret"}}`)
	probeCall := func(path string, body any) (*httptest.ResponseRecorder, map[string]any) {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(f.app, bearerRequest(t, http.MethodPost, path, token, payload))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	rec, out := probeCall(probe.ClaimPath, probe.Claim{Limit: 50, Version: "test"})
	if rec.Code != http.StatusOK || out["region"] != "fra" {
		t.Fatalf("claim status=%d body=%s", rec.Code, rec.Body.String())
	}
	var claimed map[string]any
	for _, item := range out["checks"].([]any) {
		if check := item.(map[string]any); check["monitor_id"] == id {
			claimed = check
		}
	}
	if claimed == nil || claimed["headers"].(map[string]any)["Authorization"] != "Bearer edge-secret" {
		t.Fatalf("the probe did not receive the monitor with its real headers: %+v", claimed)
	}

	failure := probe.Batch{Results: []probe.Report{
		{MonitorID: id, CheckedAt: time.Now().UTC(), Up: false, StatusCode: 503, Error: "returning HTTP 503", TotalMs: 120},
	}}
	accepted := func(batch probe.Batch) float64 {
		t.Helper()
		rec, out := probeCall(probe.ResultsPath, batch)
		if rec.Code != http.StatusOK {
			t.Fatalf("results status=%d body=%s", rec.Code, rec.Body.String())
		}
		return out["accepted"].(float64)
	}
	if got := accepted(failure); got != 1 {
		t.Fatalf("the claimed result was not accepted (%v)", got)
	}
	if got := accepted(failure); got != 0 {
		t.Fatal("a claim was reported twice")
	}
	f.makeDue(t, id)
	if got := accepted(failure); got != 0 {
		t.Fatal("a probe reported a check it never claimed")
	}
	if rec, _ := probeCall(probe.ClaimPath, probe.Claim{Limit: 50}); rec.Code != http.StatusOK {
		t.Fatalf("second claim status=%d", rec.Code)
	}
	if got := accepted(failure); got != 1 {
		t.Fatalf("the second claimed result was not accepted (%v)", got)
	}
	_, out = f.call(t, http.MethodGet, "/api/v1/monitors/"+id, "")
	if data(out)["state"] != "down" {
		t.Fatalf("half the regions failing twice should confirm an outage: %+v", data(out))
	}
	var regionsDown []string
	if err := f.db.QueryRow(ctx, `SELECT regions_down FROM incidents WHERE monitor_id=$1 AND resolved_at IS NULL`, id).Scan(&regionsDown); err != nil || len(regionsDown) != 1 || regionsDown[0] != "fra" {
		t.Fatalf("open incident regions=%v err=%v", regionsDown, err)
	}

	rec, out = probeCall(probe.ResultsPath, probe.Batch{Results: []probe.Report{
		{MonitorID: id, CheckedAt: time.Now().Add(-time.Hour).UTC(), Up: true},
	}})
	if rec.Code != http.StatusOK || out["accepted"].(float64) != 0 {
		t.Fatalf("a stale report was accepted: %s", rec.Body.String())
	}

	other := f.create(t, `{"name":"local only","kind":"http","target":"https://example.com","regions":["local"]}`)
	rec, out = probeCall(probe.ResultsPath, probe.Batch{Results: []probe.Report{{MonitorID: other, CheckedAt: time.Now().UTC(), Up: false}}})
	if rec.Code != http.StatusOK || out["accepted"].(float64) != 0 {
		t.Fatalf("a probe reported for a region it does not serve: %s", rec.Body.String())
	}

	if _, err := f.db.Exec(ctx, `UPDATE probes SET revoked_at=now() WHERE id=$1`, probeID); err != nil {
		t.Fatal(err)
	}
	if rec, _ = probeCall(probe.ClaimPath, probe.Claim{Limit: 1}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a revoked probe claimed work: status=%d", rec.Code)
	}
}

func TestIntegrationOperatorsHearOnceWhenAProbeGoesQuietAndWhenItReturns(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()
	operator := fmt.Sprintf("probe-ops-%d@example.test", time.Now().UnixNano())
	principalID := uuid.NewV7().String()
	var probeID string
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM email_jobs WHERE to_email=$1`, operator)
		_, _ = db.Exec(ctx, `DELETE FROM probes WHERE id=$1`, probeID)
		_, _ = db.Exec(ctx, `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM principal_identities WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID)
	})
	for _, statement := range []string{
		`INSERT INTO principals(id,kind,display_name) VALUES($1,'person','Probe operator')`,
		`INSERT INTO principal_identities(principal_id,provider,provider_subject,verified_email) VALUES($1,'email_otp',$2,$2)`,
		`INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,'platform.probes.manage','platform','probe watch test')`,
	} {
		args := []any{principalID}
		if strings.Contains(statement, "$2") {
			args = append(args, operator)
		}
		if _, err := db.Exec(ctx, statement, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.QueryRow(ctx, `INSERT INTO probes(region,name,key_prefix,secret_hash,last_seen_at) VALUES('fra','fra-watch','nmon_prb_x',$1,now()-interval '10 minutes') RETURNING id::text`,
		config.HashSecret(uuid.NewV7().String())).Scan(&probeID); err != nil {
		t.Fatal(err)
	}
	alerts := func() []map[string]any {
		t.Helper()
		rows, err := db.Query(ctx, `SELECT payload FROM email_jobs WHERE to_email=$1 AND template='probe_status' ORDER BY created_at`, operator)
		if err != nil {
			t.Fatal(err)
		}
		payloads, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
		if err != nil {
			t.Fatal(err)
		}
		return payloads
	}

	for range 2 {
		if err := a.alertProbeTransitions(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := alerts()
	if len(got) != 1 || got[0]["probe_name"] != "fra-watch" || got[0]["label"] != "Region offline" {
		t.Fatalf("offline alerts=%+v, want exactly one region-offline alert", got)
	}

	if _, err := db.Exec(ctx, `UPDATE probes SET last_seen_at=now() WHERE id=$1`, probeID); err != nil {
		t.Fatal(err)
	}
	if err := a.alertProbeTransitions(ctx); err != nil {
		t.Fatal(err)
	}
	if got = alerts(); len(got) != 2 || got[1]["label"] != "Probe back" {
		t.Fatalf("after recovery alerts=%+v", got)
	}
}
