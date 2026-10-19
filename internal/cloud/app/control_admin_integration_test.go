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

	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/usage"
)

type operator struct {
	token       string
	principalID string
}

func newOperator(t *testing.T, db *pgxpool.Pool, capabilities ...string) operator {
	t.Helper()
	ctx := context.Background()
	principalID := uuid.NewV7().String()
	token := "nmon_ctl_admin_" + uuid.NewV7().String()
	if _, err := db.Exec(ctx,
		`INSERT INTO principals(id,kind,display_name) VALUES($1,'service','admin surface test')`, principalID); err != nil {
		t.Fatalf("principal: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO credentials(id,principal_id,kind,audience,name,key_prefix,secret_hash,assurance,expires_at)
		 VALUES($1,$2,'api_key','control_api','admin',$3,$4,'workload',now()+interval '1 hour')`,
		uuid.NewV7().String(), principalID, token[:24], config.HashSecret(token)); err != nil {
		t.Fatalf("credential: %v", err)
	}
	for _, capability := range capabilities {
		if _, err := db.Exec(ctx,
			`INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','admin surface test')`,
			principalID, capability); err != nil {
			t.Fatalf("grant %s: %v", capability, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM audit_events WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM credentials WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID)
	})
	return operator{token: token, principalID: principalID}
}

func controlApp(t *testing.T, db *pgxpool.Pool) *App {
	t.Helper()
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	a.control = control.New(db, a.billing, a.cfg)
	return a
}

func controlCall(t *testing.T, a *App, op operator, method, target string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	req.Host = "localhost:8900"
	req.Header.Set("Authorization", "Bearer "+op.token)
	req.Header.Set("Content-Type", "application/json")
	return serve(a, req)
}

func decodeItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rec.Body.String())
	}
	return out.Data
}

func TestIntegrationControlSeesTeamAndCanRemoveAMember(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.MembersRead, access.MembersRemove)

	ownerEmail := fmt.Sprintf("ctl-own-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("ctl-mem-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, memberEmail)
	accountID := createTestAccount(t, db, ownerEmail)
	createTestAccount(t, db, memberEmail)
	memberPrincipal := principalOf(t, db, memberEmail)
	setRole(t, db, memberPrincipal, accountID, "member")

	rec := controlCall(t, a, op, http.MethodGet, "/api/control/v1/members?account_id="+accountID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("members status=%d body=%s", rec.Code, rec.Body.String())
	}
	items := decodeItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("members = %d, want the owner and one member", len(items))
	}
	sawMember := false
	for _, item := range items {
		if item["email"] == memberEmail && item["role"] == "member" {
			sawMember = true
		}
	}
	if !sawMember {
		t.Error("the operator cannot see who else has access to the account")
	}

	rec = controlCall(t, a, op, http.MethodDelete,
		"/api/control/v1/accounts/"+accountID+"/members/"+memberPrincipal,
		`{"reason":"account takeover reported by the owner"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove status=%d body=%s", rec.Code, rec.Body.String())
	}
	var status string
	if err := db.QueryRow(context.Background(),
		`SELECT status FROM account_memberships WHERE account_id=$1 AND principal_id=$2`, accountID, memberPrincipal,
	).Scan(&status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if status != "removed" {
		t.Errorf("membership status = %q, want removed", status)
	}
}

func TestIntegrationControlCannotRemoveAnOwner(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.MembersRead, access.MembersRemove)

	emailAddr := fmt.Sprintf("ctl-noown-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	ownerPrincipal := principalOf(t, db, emailAddr)

	rec := controlCall(t, a, op, http.MethodDelete,
		"/api/control/v1/accounts/"+accountID+"/members/"+ownerPrincipal,
		`{"reason":"attempting to remove the account owner"}`)
	if rec.Code == http.StatusNoContent {
		t.Fatal("an owner was removed through the control plane; the account should be closed instead")
	}
}

func TestIntegrationControlStatsCarryBusinessMetrics(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.AccountsRead, access.BillingRead,
		access.MembersRead, access.JobsRead, access.CredentialsRead)

	rec := controlCall(t, a, op, http.MethodGet, "/api/control/v1/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{
		"mrr_minor", "paying_accounts", "plans", "active_accounts", "accounts_new_30d",
		"accounts_closed_30d", "closed_pending_erasure", "team_members", "pending_invites",
	} {
		if _, ok := out.Data[key]; !ok {
			t.Errorf("stats is missing %q", key)
		}
	}
	plans, ok := out.Data["plans"].(map[string]any)
	if !ok {
		t.Fatalf("plans = %T, want a distribution", out.Data["plans"])
	}
	for _, plan := range []string{"free", "starter", "pro", "business"} {
		if _, ok := plans[plan]; !ok {
			t.Errorf("plan distribution is missing %q", plan)
		}
	}
}

func TestIntegrationControlStatsHideMetricsTheOperatorCannotSee(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	limited := newOperator(t, db, access.MembersRead)

	rec := controlCall(t, a, limited, http.MethodGet, "/api/control/v1/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status=%d", rec.Code)
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, hidden := range []string{"mrr_minor", "paying_accounts", "plans", "active_accounts", "past_due_billing"} {
		if _, leaked := out.Data[hidden]; leaked {
			t.Errorf("%q was returned to an operator without the capability for it", hidden)
		}
	}
	if _, ok := out.Data["team_members"]; !ok {
		t.Error("a metric the operator may read was withheld")
	}
}

func TestIntegrationControlHealthNeedsItsCapability(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)

	without := newOperator(t, db, access.AccountsRead)
	if rec := controlCall(t, a, without, http.MethodGet, "/api/control/v1/health", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("health without the capability status=%d, want 403", rec.Code)
	}

	with := newOperator(t, db, access.HealthRead)
	rec := controlCall(t, a, with, http.MethodGet, "/api/control/v1/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"db_pool_max", "db_pool_total", "email_queue_depth"} {
		if _, ok := out.Data[key]; !ok {
			t.Errorf("health is missing %q", key)
		}
	}
}

func TestIntegrationControlLifecycleShowsErasureProgress(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.AccountsRead)

	emailAddr := fmt.Sprintf("ctl-life-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	if _, err := db.Exec(context.Background(),
		`UPDATE accounts SET status='closed',deleted_at=now() WHERE id=$1`, accountID); err != nil {
		t.Fatalf("close: %v", err)
	}

	rec := controlCall(t, a, op, http.MethodGet, "/api/control/v1/lifecycle?status=pending", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lifecycle status=%d body=%s", rec.Code, rec.Body.String())
	}
	found := false
	for _, item := range decodeItems(t, rec) {
		if item["account_id"] == accountID {
			found = true
			if item["purged_at"] != nil {
				t.Error("a freshly closed account should not be marked erased")
			}
		}
	}
	if !found {
		t.Error("a closed account awaiting erasure is not visible to the operator")
	}
}

func TestIntegrationControlCollectionsRejectAnUngrantedOperator(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.AccountsRead)

	for _, target := range []string{
		"/api/control/v1/members",
		"/api/control/v1/invites",
	} {
		if rec := controlCall(t, a, op, http.MethodGet, target, ""); rec.Code != http.StatusForbidden {
			t.Errorf("%s status=%d, want 403 without the capability", target, rec.Code)
		}
	}
	if rec := controlCall(t, a, op, http.MethodGet, "/api/control/v1/lifecycle", ""); rec.Code != http.StatusOK {
		t.Errorf("lifecycle should be readable with accounts.read, got %d", rec.Code)
	}
}

func TestIntegrationControlMRRUsesKESBaseForUSDSubscriptions(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.BillingRead)
	readMRR := func() float64 {
		t.Helper()
		rec := controlCall(t, a, op, http.MethodGet, "/api/control/v1/stats", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("stats %d %s", rec.Code, rec.Body)
		}
		return decodeBody(t, rec)["data"].(map[string]any)["mrr_minor"].(float64)
	}
	before := readMRR()
	address := fmt.Sprintf("usd-mrr-%d@example.test", time.Now().UnixNano())
	accountID := createTestAccount(t, db, address)
	t.Cleanup(func() { deleteTestAccount(t, db, address) })
	seedSubscription(t, a, accountID, "pro", "active", "auto", time.Now().Add(24*time.Hour))
	if _, err := db.Exec(context.Background(), `UPDATE billing_subscriptions SET currency='USD',amount_minor=500 WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	if delta := readMRR() - before; delta != 50000 {
		t.Fatalf("KES MRR increased by %v minor units, want50000", delta)
	}
}

func newPlanCatalogApp(t *testing.T) (*App, *paystack.FakeServer, string, string, operator) {
	t.Helper()
	a, fs, accountID, session := newCurrencyBillingApp(t)
	a.plans = usage.NewCatalog(a.cfg)
	a.control = control.New(a.db, a.billing, a.cfg, a.plans)
	op := newOperator(t, a.db, access.BillingRead, access.PlansManage)
	t.Cleanup(func() {
		_, _ = a.db.Exec(context.Background(), `DELETE FROM plan_catalog WHERE editor_id=$1`, op.principalID)
	})
	return a, fs, accountID, session, op
}

func saveCatalogPlan(t *testing.T, a *App, op operator, p usage.Plan) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(control.PlanInput{Plan: p, Reason: "test catalog change"})
	if err != nil {
		t.Fatal(err)
	}
	return controlCall(t, a, op, http.MethodPatch, "/api/control/v1/plans/"+p.Plan, string(body))
}

func TestIntegrationPlanCatalogPersistsAuditsAndEnforcesLimits(t *testing.T) {
	a, fs, accountID, session, op := newPlanCatalogApp(t)
	before, _ := a.plans.Get("starter")
	before.PriceMinor = 15000
	before.KESPlanCode = "PLN_catalog_new_kes"
	before.USDPlanCode = "PLN_catalog_new_usd"
	before.MaxMonitors = 1
	fs.Plans[before.KESPlanCode] = paystack.Plan{PlanCode: before.KESPlanCode, Amount: 15000, Currency: "KES", Interval: "monthly"}
	fs.Plans[before.USDPlanCode] = paystack.Plan{PlanCode: before.USDPlanCode, Amount: 150, Currency: "USD", Interval: "monthly"}
	readOnly := newOperator(t, a.db, access.BillingRead)
	if rec := saveCatalogPlan(t, a, readOnly, before); rec.Code != http.StatusForbidden {
		t.Fatalf("read-only save %d %s", rec.Code, rec.Body)
	}
	if rec := saveCatalogPlan(t, a, op, before); rec.Code != http.StatusNoContent {
		t.Fatalf("save %d %s", rec.Code, rec.Body)
	}
	if rec := saveCatalogPlan(t, a, op, before); rec.Code != http.StatusConflict {
		t.Fatalf("stale save %d %s", rec.Code, rec.Body)
	}
	reloaded := usage.NewCatalog(a.cfg)
	if err := reloaded.Reload(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	after, _ := reloaded.Get("starter")
	if after.PriceMinor != 15000 || after.MaxMonitors != 1 || after.Version != 1 {
		t.Fatalf("persisted=%+v", after)
	}
	pricing, _ := reloaded.Pricing("starter", "USD")
	if pricing.Amount != 150 {
		t.Fatalf("USD price=%d", pricing.Amount)
	}
	var audited int
	if err := a.db.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE principal_id=$1 AND action='plan.updated' AND target_id='starter'`, op.principalID).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if audited != 1 {
		t.Fatalf("audit count=%d", audited)
	}
	setPlan(t, a, accountID, "starter")
	for i := 0; i < 2; i++ {
		req := sessionRequest(t, http.MethodPost, "/api/dashboard/v1/monitors", session, []byte(fmt.Sprintf(`{"name":"catalog-%d","kind":"http","target":"https://example.com"}`, i)))
		rec := serve(a, req)
		want := http.StatusCreated
		if i == 1 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Fatalf("monitor %d status=%d body=%s", i, rec.Code, rec.Body)
		}
	}
}

func TestIntegrationPlanCatalogPreservesPendingPaymentAndRenewalTerms(t *testing.T) {
	a, fs, accountID, session, op := newPlanCatalogApp(t)
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/checkout?plan=pro&method=mpesa", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout %d %s", rec.Code, rec.Body)
	}
	reference := decodeBody(t, rec)["data"].(map[string]any)["reference"].(string)
	changed, _ := a.plans.Get("pro")
	changed.PriceMinor = 80000
	changed.KESPlanCode = "PLN_grandfather_new_kes"
	changed.USDPlanCode = "PLN_grandfather_new_usd"
	fs.Plans[changed.KESPlanCode] = paystack.Plan{PlanCode: changed.KESPlanCode, Amount: 80000, Currency: "KES", Interval: "monthly"}
	fs.Plans[changed.USDPlanCode] = paystack.Plan{PlanCode: changed.USDPlanCode, Amount: 800, Currency: "USD", Interval: "monthly"}
	if rec = saveCatalogPlan(t, a, op, changed); rec.Code != http.StatusNoContent {
		t.Fatalf("catalog %d %s", rec.Code, rec.Body)
	}
	fs.Verify[reference] = paystack.Transaction{ID: 3322, Status: "success", Reference: reference, Amount: 50000, Currency: "KES", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	if _, err := a.verifyBillingCheckout(context.Background(), reference, accountID); err != nil {
		t.Fatalf("original payment rejected: %v", err)
	}
	var price int64
	var providerCode string
	if err := a.db.QueryRow(context.Background(), `SELECT amount_minor,provider_plan_code FROM billing_subscriptions WHERE account_id=$1`, accountID).Scan(&price, &providerCode); err != nil {
		t.Fatal(err)
	}
	if price != 50000 || providerCode != a.cfg.PaystackPlanCode {
		t.Fatalf("existing terms changed: price=%d code=%s", price, providerCode)
	}
	if _, err := a.db.Exec(context.Background(), `UPDATE billing_subscriptions SET current_period_end=now()+interval '1 hour' WHERE account_id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/renew?plan=pro", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("renew %d %s", rec.Code, rec.Body)
	}
	_, price = fs.LastInitializeCurrency()
	if price != 50000 {
		t.Fatalf("renewal repriced to%d", price)
	}
}

func TestIntegrationPlanCatalogValidatesProviderPlansAndQuotas(t *testing.T) {
	a, _, _, _, op := newPlanCatalogApp(t)
	changed, _ := a.plans.Get("pro")
	changed.PriceMinor++
	if rec := saveCatalogPlan(t, a, op, changed); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted reused price code %d", rec.Code)
	}
	changed, _ = a.plans.Get("pro")
	changed.MaxCustomDomains = changed.MaxStatusPages + 1
	if rec := saveCatalogPlan(t, a, op, changed); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted invalid domains %d", rec.Code)
	}
	changed, _ = a.plans.Get("pro")
	changed.KESPlanCode = "PLN_missing"
	if rec := saveCatalogPlan(t, a, op, changed); rec.Code != http.StatusBadRequest {
		t.Fatalf("accepted unverified provider plan %d", rec.Code)
	}
}

func TestIntegrationCatalogPriceChangesDoNotRepriceAutomaticSubscriptions(t *testing.T) {
	a, fs, accountID, session, op := newPlanCatalogApp(t)
	setPlan(t, a, accountID, "pro")
	code := seedSubscriptionWithToken(t, a, accountID, "pro", "active", "auto", time.Now().Add(20*24*time.Hour))
	changed, _ := a.plans.Get("pro")
	changed.PriceMinor = 80000
	changed.KESPlanCode = "PLN_new_auto_kes"
	changed.USDPlanCode = "PLN_new_auto_usd"
	fs.Plans[changed.KESPlanCode] = paystack.Plan{PlanCode: changed.KESPlanCode, Amount: 80000, Currency: "KES", Interval: "monthly"}
	fs.Plans[changed.USDPlanCode] = paystack.Plan{PlanCode: changed.USDPlanCode, Amount: 800, Currency: "USD", Interval: "monthly"}
	if rec := saveCatalogPlan(t, a, op, changed); rec.Code != http.StatusNoContent {
		t.Fatalf("save %d %s", rec.Code, rec.Body)
	}
	sub := paystack.Subscription{SubscriptionCode: code, Status: "active", Amount: 50000, Currency: "KES", NextPaymentDate: time.Now().Add(25 * 24 * time.Hour).Format(time.RFC3339)}
	sub.Plan.Code = a.cfg.PaystackPlanCode
	fs.Subscriptions[code] = sub
	a.reconcileSubscriptions(context.Background())
	var end time.Time
	if err := a.db.QueryRow(context.Background(), `SELECT current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if end.Before(time.Now().Add(24 * 24 * time.Hour)) {
		t.Fatal("existing subscription failed reconciliation after catalog edit")
	}
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/cancel", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel %d %s", rec.Code, rec.Body)
	}
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/resume", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume %d %s", rec.Code, rec.Body)
	}
	_, providerCode, _ := fs.CreatedSubscription()
	if providerCode != a.cfg.PaystackPlanCode {
		t.Fatalf("resumed into new price code %s", providerCode)
	}
	var amount int64
	if err := a.db.QueryRow(context.Background(), `SELECT amount_minor FROM billing_subscriptions WHERE account_id=$1 AND provider_subscription_code<>$2`, accountID, code).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != 50000 {
		t.Fatalf("resumed amount=%d", amount)
	}
}
