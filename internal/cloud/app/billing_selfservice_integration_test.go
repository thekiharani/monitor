package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
)

func newSelfService(t *testing.T, label string) (*App, string, string) {
	t.Helper()
	db := openIntegrationDB(t)
	address := fmt.Sprintf("%s-%d@example.test", label, time.Now().UnixNano())
	fs := paystack.NewFakeServer()
	t.Cleanup(fs.Close)
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, address)
	t.Cleanup(func() { deleteTestAccount(t, db, address) })
	session := newSession(t, db, address)
	return a, accountID, session
}

func setPlan(t *testing.T, a *App, accountID, plan string) {
	t.Helper()
	if _, err := a.db.Exec(context.Background(), `UPDATE accounts SET plan=$2 WHERE id=$1`, accountID, plan); err != nil {
		t.Fatalf("set plan: %v", err)
	}
}

func seedSubscription(t *testing.T, a *App, accountID, plan, status, mode string, periodEnd time.Time) string {
	t.Helper()
	code := "sub-" + uuid.NewV7().String()
	if _, err := a.db.Exec(context.Background(),
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,provider_customer_code,plan,status,billing_mode,amount_minor,currency,current_period_start,current_period_end,effective_at)
		 VALUES($1,'paystack',$2,'CUS_test',$3,$4,$5,50000,'KES',now()-interval '10 days',$6,now()-interval '10 days')`,
		accountID, code, plan, status, mode, periodEnd); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}
	return code
}

func seedSubscriptionWithToken(t *testing.T, a *App, accountID, plan, status, mode string, periodEnd time.Time) string {
	t.Helper()
	code := seedSubscription(t, a, accountID, plan, status, mode, periodEnd)
	encrypted, err := config.Seal(a.cfg.AppEncryptionKey, "email-token-"+code)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	if _, err = a.db.Exec(context.Background(),
		`UPDATE billing_subscriptions SET provider_email_token_encrypted=$2 WHERE provider_subscription_code=$1`,
		code, encrypted); err != nil {
		t.Fatalf("store token: %v", err)
	}
	return code
}

func subscriptionRow(t *testing.T, a *App, code string) (status string, cancelAtPeriodEnd bool, scheduledPlan string) {
	t.Helper()
	if err := a.db.QueryRow(context.Background(),
		`SELECT status,cancel_at_period_end,COALESCE(scheduled_plan,'') FROM billing_subscriptions WHERE provider_subscription_code=$1`,
		code).Scan(&status, &cancelAtPeriodEnd, &scheduledPlan); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	return status, cancelAtPeriodEnd, scheduledPlan
}

func dashboardCall(t *testing.T, a *App, method, path, session string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, sessionRequest(t, method, path, session, nil))
	return rec
}

func TestIntegrationDashboardBillingReportsTheCurrentSubscription(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-view")
	setPlan(t, a, accountID, "pro")
	periodEnd := time.Now().Add(20 * 24 * time.Hour)
	seedSubscription(t, a, accountID, "pro", "active", "auto", periodEnd)

	rec := dashboardCall(t, a, http.MethodGet, "/api/dashboard/v1/billing", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["plan"] != "pro" {
		t.Errorf("plan=%v, want pro", data["plan"])
	}
	subscription, ok := data["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("subscription missing from %s", rec.Body)
	}
	if subscription["status"] != "active" {
		t.Errorf("status=%v, want active", subscription["status"])
	}
	if subscription["billing_mode"] != "auto" {
		t.Errorf("billing_mode=%v, want auto", subscription["billing_mode"])
	}
}

func TestIntegrationDashboardBillingReportsNoSubscriptionOnTheFreePlan(t *testing.T) {
	a, _, session := newSelfService(t, "billing-free")
	rec := dashboardCall(t, a, http.MethodGet, "/api/dashboard/v1/billing", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["subscription"] != nil {
		t.Errorf("subscription=%v, want null for an account that never subscribed", data["subscription"])
	}
}

func TestIntegrationBillingCancelMovesAManualSubscriptionToNonRenewing(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-cancel")
	setPlan(t, a, accountID, "pro")
	code := seedSubscription(t, a, accountID, "pro", "active", "manual", time.Now().Add(20*24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/cancel", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeBody(t, rec)["data"].(map[string]any)["status"] != "non_renewing" {
		t.Errorf("response status=%v, want non_renewing", decodeBody(t, rec)["data"])
	}
	status, cancelAtPeriodEnd, _ := subscriptionRow(t, a, code)
	if status != "non_renewing" || !cancelAtPeriodEnd {
		t.Errorf("stored status=%s cancel_at_period_end=%v, want non_renewing and true", status, cancelAtPeriodEnd)
	}
}

func TestIntegrationBillingCancelRefusesWhenThereIsNoSubscription(t *testing.T) {
	a, _, session := newSelfService(t, "billing-cancel-none")
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/cancel", session)
	if rec.Code == http.StatusOK {
		t.Errorf("cancelling without a subscription returned %d, want a refusal", rec.Code)
	}
}

func TestIntegrationBillingResumeReactivatesAManualSubscription(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-resume")
	setPlan(t, a, accountID, "pro")
	code := seedSubscription(t, a, accountID, "pro", "non_renewing", "manual", time.Now().Add(20*24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/resume", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["status"] != "active" || data["plan"] != "pro" {
		t.Errorf("response = %v, want an active pro subscription", data)
	}
	status, cancelAtPeriodEnd, _ := subscriptionRow(t, a, code)
	if status != "active" || cancelAtPeriodEnd {
		t.Errorf("stored status=%s cancel_at_period_end=%v, want active and false", status, cancelAtPeriodEnd)
	}
}

func TestIntegrationBillingResumeRefusesASubscriptionThatIsNotCancelling(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-resume-active")
	setPlan(t, a, accountID, "pro")
	seedSubscription(t, a, accountID, "pro", "active", "manual", time.Now().Add(20*24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/resume", session)
	if rec.Code == http.StatusOK {
		t.Errorf("resuming an active subscription returned %d, want a refusal", rec.Code)
	}
}

func TestIntegrationBillingChangePlanSchedulesAManualDowngrade(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-downgrade")
	setPlan(t, a, accountID, "pro")
	periodEnd := time.Now().Add(20 * 24 * time.Hour).UTC()
	code := seedSubscription(t, a, accountID, "pro", "active", "manual", periodEnd)

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/change-plan?plan=starter", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["status"] != "scheduled" || data["plan"] != "starter" {
		t.Errorf("response = %v, want starter scheduled", data)
	}

	status, cancelAtPeriodEnd, scheduledPlan := subscriptionRow(t, a, code)
	if status != "non_renewing" || !cancelAtPeriodEnd || scheduledPlan != "starter" {
		t.Errorf("current subscription: status=%s cancel=%v scheduled=%s, want non_renewing/true/starter", status, cancelAtPeriodEnd, scheduledPlan)
	}

	var future int
	if err := a.db.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND plan='starter' AND billing_mode='manual' AND effective_at>now()`,
		accountID).Scan(&future); err != nil {
		t.Fatalf("count scheduled: %v", err)
	}
	if future != 1 {
		t.Errorf("%d future starter subscriptions were created, want exactly 1", future)
	}
	if accountPlan(t, a.db, accountID) != "pro" {
		t.Error("the account left its paid plan before the period ended")
	}
}

func TestIntegrationBillingChangePlanRefusesAnUpgradeAndTheFreePlan(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-plan-invalid")
	setPlan(t, a, accountID, "starter")
	seedSubscription(t, a, accountID, "starter", "active", "manual", time.Now().Add(20*24*time.Hour))

	for name, target := range map[string]string{
		"an upgrade":      "pro",
		"the same plan":   "starter",
		"the free plan":   "free",
		"an unknown plan": "enterprise",
	} {
		rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/change-plan?plan="+target, session)
		if rec.Code == http.StatusOK {
			t.Errorf("changing to %s returned %d, want a refusal", name, rec.Code)
		}
	}
}

func TestIntegrationBillingRenewOpensAManualRenewalCheckout(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-renew")
	setPlan(t, a, accountID, "pro")
	seedSubscription(t, a, accountID, "pro", "active", "manual", time.Now().Add(24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/renew?plan=pro", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["authorization_url"] == "" || data["reference"] == "" {
		t.Errorf("response = %v, want an authorization url and reference", data)
	}

	var checkoutType, mode, status string
	if err := a.db.QueryRow(context.Background(),
		`SELECT checkout_type,billing_mode,status FROM billing_checkouts WHERE account_id=$1 ORDER BY created_at DESC LIMIT 1`,
		accountID).Scan(&checkoutType, &mode, &status); err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	if checkoutType != "renewal" || mode != "manual" || status != "initialized" {
		t.Errorf("checkout type=%s mode=%s status=%s, want renewal/manual/initialized", checkoutType, mode, status)
	}
}

func TestIntegrationBillingRenewRefusesAPlanTheAccountIsNotOn(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-renew-wrong")
	setPlan(t, a, accountID, "pro")
	seedSubscription(t, a, accountID, "pro", "active", "manual", time.Now().Add(24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/renew?plan=business", session)
	if rec.Code == http.StatusOK {
		t.Errorf("renewing a plan the account is not on returned %d, want a refusal", rec.Code)
	}
}

func TestIntegrationManagePaymentReturnsALinkAndRefusesManualBilling(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-manage")
	setPlan(t, a, accountID, "pro")
	code := seedSubscription(t, a, accountID, "pro", "active", "auto", time.Now().Add(20*24*time.Hour))

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/manage-payment", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("auto billing: status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeBody(t, rec)["data"].(map[string]any)["management_url"] == "" {
		t.Error("auto billing returned an empty management url")
	}

	if _, err := a.db.Exec(context.Background(),
		`UPDATE billing_subscriptions SET billing_mode='manual' WHERE provider_subscription_code=$1`, code); err != nil {
		t.Fatalf("switch to manual: %v", err)
	}
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/manage-payment", session)
	if rec.Code == http.StatusOK {
		t.Errorf("manual billing returned %d, want a refusal: there is no provider portal to send them to", rec.Code)
	}
}

func TestIntegrationBillingResumeCreatesAReplacementSubscriptionOnAutomaticBilling(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-resume-auto")
	setPlan(t, a, accountID, "pro")
	periodEnd := time.Now().Add(20 * 24 * time.Hour).UTC()
	cancelled := seedSubscriptionWithToken(t, a, accountID, "pro", "non_renewing", "auto", periodEnd)

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/resume", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeBody(t, rec)["data"].(map[string]any)["status"] != "resume_scheduled" {
		t.Errorf("response = %v, want resume_scheduled", decodeBody(t, rec)["data"])
	}

	_, _, scheduledPlan := subscriptionRow(t, a, cancelled)
	if scheduledPlan != "pro" {
		t.Errorf("the cancelled subscription records scheduled_plan=%q, want pro", scheduledPlan)
	}

	var replacements int
	if err := a.db.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND provider_subscription_code<>$2 AND plan='pro' AND status='active'`,
		accountID, cancelled).Scan(&replacements); err != nil {
		t.Fatalf("count replacements: %v", err)
	}
	if replacements != 1 {
		t.Errorf("%d replacement subscriptions were written, want exactly 1", replacements)
	}
}

func TestIntegrationBillingChangePlanSchedulesAnAutomaticDowngrade(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-downgrade-auto")
	setPlan(t, a, accountID, "pro")
	periodEnd := time.Now().Add(20 * 24 * time.Hour).UTC()
	current := seedSubscriptionWithToken(t, a, accountID, "pro", "active", "auto", periodEnd)

	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/change-plan?plan=starter", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	if decodeBody(t, rec)["data"].(map[string]any)["plan"] != "starter" {
		t.Errorf("response = %v, want starter", decodeBody(t, rec)["data"])
	}

	status, cancelAtPeriodEnd, scheduledPlan := subscriptionRow(t, a, current)
	if status != "non_renewing" || !cancelAtPeriodEnd || scheduledPlan != "starter" {
		t.Errorf("current subscription: status=%s cancel=%v scheduled=%s, want non_renewing/true/starter", status, cancelAtPeriodEnd, scheduledPlan)
	}

	var future int
	if err := a.db.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND plan='starter' AND effective_at>now()`,
		accountID).Scan(&future); err != nil {
		t.Fatalf("count scheduled: %v", err)
	}
	if future != 1 {
		t.Errorf("%d future starter subscriptions exist, want exactly 1", future)
	}
	if accountPlan(t, a.db, accountID) != "pro" {
		t.Error("the account left its paid plan before the period ended")
	}
}

func seedComplimentarySubscription(t *testing.T, a *App, accountID, plan string) {
	t.Helper()
	if _, err := a.db.Exec(context.Background(),
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,billing_mode,amount_minor,currency,current_period_start)
		 VALUES($1,'internal',$2,$3,'active','comp',0,'KES',now())`,
		accountID, "COMP-"+uuid.NewV7().String(), plan); err != nil {
		t.Fatalf("seed complimentary subscription: %v", err)
	}
	setPlan(t, a, accountID, plan)
}

func TestIntegrationDashboardBillingNamesAComplimentaryPlan(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-comp-view")
	seedComplimentarySubscription(t, a, accountID, "pro")

	rec := dashboardCall(t, a, http.MethodGet, "/api/dashboard/v1/billing", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	if data["plan"] != "pro" {
		t.Errorf("plan=%v, want pro", data["plan"])
	}
	subscription, ok := data["subscription"].(map[string]any)
	if !ok {
		t.Fatalf("subscription missing from %s", rec.Body)
	}
	if subscription["billing_mode"] != "comp" {
		t.Errorf("billing_mode=%v, want comp", subscription["billing_mode"])
	}
	if subscription["amount_minor"] != float64(0) {
		t.Errorf("amount_minor=%v, want 0", subscription["amount_minor"])
	}
}

func TestIntegrationComplimentaryPlanOutranksAnOlderProviderSubscription(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-comp-outranks")
	seedComplimentarySubscription(t, a, accountID, "business")
	if _, err := a.db.Exec(context.Background(),
		`UPDATE billing_subscriptions SET effective_at=now()-interval '30 days',created_at=now()-interval '30 days' WHERE account_id=$1 AND billing_mode='comp'`,
		accountID); err != nil {
		t.Fatalf("backdate complimentary subscription: %v", err)
	}
	seedSubscription(t, a, accountID, "pro", "canceled", "auto", time.Now().Add(-24*time.Hour))

	rec := dashboardCall(t, a, http.MethodGet, "/api/dashboard/v1/billing", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
	subscription := decodeBody(t, rec)["data"].(map[string]any)["subscription"].(map[string]any)
	if subscription["billing_mode"] != "comp" {
		t.Errorf("billing_mode=%v, want comp; a newer cancelled provider row must not hide the complimentary plan", subscription["billing_mode"])
	}

	if rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/cancel", session); rec.Code == http.StatusOK {
		t.Error("cancel succeeded on a complimentary plan hidden behind a provider row")
	}
	if rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/manage-payment", session); rec.Code == http.StatusOK {
		t.Error("manage payment succeeded on a complimentary plan hidden behind a provider row")
	}
}

func TestIntegrationComplimentaryPlanRefusesEveryPaidBillingAction(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-comp-guard")
	seedComplimentarySubscription(t, a, accountID, "pro")

	paths := []string{
		"/api/dashboard/v1/billing/checkout?plan=business",
		"/api/dashboard/v1/billing/renew?plan=pro",
		"/api/dashboard/v1/billing/change-plan?plan=starter",
		"/api/dashboard/v1/billing/cancel",
		"/api/dashboard/v1/billing/resume",
		"/api/dashboard/v1/billing/manage-payment",
	}
	for _, path := range paths {
		rec := dashboardCall(t, a, http.MethodPost, path, session)
		if rec.Code == http.StatusOK {
			t.Errorf("%s returned 200 for a complimentary plan, want a refusal", path)
		}
	}

	var checkouts int
	if err := a.db.QueryRow(context.Background(), `SELECT count(*) FROM billing_checkouts WHERE account_id=$1`, accountID).Scan(&checkouts); err != nil {
		t.Fatalf("count checkouts: %v", err)
	}
	if checkouts != 0 {
		t.Errorf("checkouts = %d, want none started for a complimentary plan", checkouts)
	}
	if got := accountPlan(t, a.db, accountID); got != "pro" {
		t.Errorf("account plan = %q, want pro to be unchanged", got)
	}
}

func TestIntegrationComplimentaryPlanDoesNotBlockClosingTheAccount(t *testing.T) {
	a, accountID, session := newSelfService(t, "billing-comp-close")
	seedComplimentarySubscription(t, a, accountID, "pro")

	var ownerEmail string
	if err := a.db.QueryRow(context.Background(),
		`SELECT i.verified_email FROM principal_identities i JOIN account_memberships m ON m.principal_id=i.principal_id WHERE m.account_id=$1`,
		accountID).Scan(&ownerEmail); err != nil {
		t.Fatalf("read owner email: %v", err)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/account/close", session,
		fmt.Appendf(nil, `{"confirm":%q}`, ownerEmail)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body)
	}
	var status, compStatus string
	if err := a.db.QueryRow(context.Background(), `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatalf("read account: %v", err)
	}
	if status != "closed" {
		t.Errorf("account status = %q, want closed", status)
	}
	if err := a.db.QueryRow(context.Background(),
		`SELECT status FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp'`, accountID).Scan(&compStatus); err != nil {
		t.Fatalf("read complimentary subscription: %v", err)
	}
	if compStatus != "canceled" {
		t.Errorf("complimentary subscription status = %q, want canceled", compStatus)
	}
}
