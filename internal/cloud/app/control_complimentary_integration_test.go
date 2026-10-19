package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
)

type complimentaryRow struct {
	plan      string
	status    string
	amount    int64
	mode      string
	periodEnd *time.Time
}

func readComplimentary(t *testing.T, db *pgxpool.Pool, accountID string) (complimentaryRow, bool) {
	t.Helper()
	var row complimentaryRow
	err := db.QueryRow(context.Background(),
		`SELECT plan,status,amount_minor,billing_mode,current_period_end FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp' ORDER BY created_at DESC LIMIT 1`,
		accountID).Scan(&row.plan, &row.status, &row.amount, &row.mode, &row.periodEnd)
	if err != nil {
		return complimentaryRow{}, false
	}
	return row, true
}

func TestIntegrationComplimentaryPlanSupersedesPaidBilling(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.BillingComp)

	address := fmt.Sprintf("comp-grant-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	ctx := context.Background()
	paidCode := "sub-" + accountID
	token, err := config.Seal(a.cfg.AppEncryptionKey, "email-token-"+paidCode)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,provider_email_token_encrypted,plan,status,billing_mode,amount_minor,currency,current_period_start,current_period_end)
		 VALUES($1,'paystack',$2,$3,'starter','active','auto',10000,'KES',now()-interval '5 days',now()+interval '25 days')`,
		accountID, paidCode, token); err != nil {
		t.Fatalf("seed paid subscription: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode)
		 VALUES($1,'paystack',$2,'pro',50000,'KES','initialized','upgrade','auto')`,
		accountID, "ref-"+accountID); err != nil {
		t.Fatalf("seed checkout: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatalf("set plan: %v", err)
	}

	rec := controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"plan":"business","reason":"internal workspace for the support team"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}

	if got := accountPlan(t, db, accountID); got != "business" {
		t.Errorf("account plan = %q, want business", got)
	}
	comp, ok := readComplimentary(t, db, accountID)
	if !ok {
		t.Fatal("no complimentary subscription was recorded")
	}
	if comp.plan != "business" || comp.status != "active" || comp.amount != 0 || comp.mode != "comp" {
		t.Errorf("complimentary row = %+v, want an active business row at no charge", comp)
	}
	if comp.periodEnd != nil {
		t.Errorf("period end = %v, want no end date for a perpetual grant", comp.periodEnd)
	}

	var paidStatus string
	var cancelAtPeriodEnd bool
	if err := db.QueryRow(ctx, `SELECT status,cancel_at_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, paidCode).Scan(&paidStatus, &cancelAtPeriodEnd); err != nil {
		t.Fatalf("read paid subscription: %v", err)
	}
	if paidStatus != "canceled" || !cancelAtPeriodEnd {
		t.Errorf("paid subscription status=%q cancel_at_period_end=%v, want it cancelled so the customer stops being charged", paidStatus, cancelAtPeriodEnd)
	}

	var checkoutStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM billing_checkouts WHERE account_id=$1`, accountID).Scan(&checkoutStatus); err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	if checkoutStatus != "expired" {
		t.Errorf("pending checkout status = %q, want expired", checkoutStatus)
	}

	if disabled := a.billing.(*fakeBilling).disabled; len(disabled) != 1 || disabled[0] != paidCode {
		t.Errorf("disabled provider subscriptions = %v, want [%s]", disabled, paidCode)
	}

	var audits int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='billing.complimentary_granted' AND target_id=$1`, accountID).Scan(&audits); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audits != 1 {
		t.Errorf("audit events = %d, want 1", audits)
	}
}

func TestIntegrationComplimentaryPlanIsNotGrantedWhenTheCardCannotBeStopped(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	a.billing.(*fakeBilling).disableErr = errors.New("paystack is unreachable")
	a.control = control.New(db, a.billing, a.cfg)
	op := newOperator(t, db, access.BillingComp)

	address := fmt.Sprintf("comp-provider-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	ctx := context.Background()
	paidCode := "sub-stuck-" + accountID
	token, err := config.Seal(a.cfg.AppEncryptionKey, "email-token-"+paidCode)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,provider_email_token_encrypted,plan,status,billing_mode,amount_minor,currency,current_period_start,current_period_end)
		 VALUES($1,'paystack',$2,$3,'starter','active','auto',10000,'KES',now()-interval '5 days',now()+interval '25 days')`,
		accountID, paidCode, token); err != nil {
		t.Fatalf("seed paid subscription: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatalf("set plan: %v", err)
	}

	rec := controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"plan":"pro","reason":"the provider is down and must not be papered over"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("grant status=%d, want 503 when the card cannot be stopped", rec.Code)
	}
	if _, ok := readComplimentary(t, db, accountID); ok {
		t.Error("a complimentary plan was granted while the card is still being charged")
	}
	if got := accountPlan(t, db, accountID); got != "starter" {
		t.Errorf("account plan = %q, want starter to be unchanged", got)
	}
	var paidStatus string
	if err := db.QueryRow(ctx, `SELECT status FROM billing_subscriptions WHERE provider_subscription_code=$1`, paidCode).Scan(&paidStatus); err != nil {
		t.Fatalf("read paid subscription: %v", err)
	}
	if paidStatus != "active" {
		t.Errorf("paid subscription status = %q, want it left active", paidStatus)
	}
}

func TestIntegrationComplimentaryPlanAcceptsAnEndDate(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.BillingComp)

	address := fmt.Sprintf("comp-expiry-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	ends := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339)
	rec := controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		fmt.Sprintf(`{"plan":"pro","expires_at":%q,"reason":"three month pilot for the partner team"}`, ends))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}
	comp, ok := readComplimentary(t, db, accountID)
	if !ok || comp.periodEnd == nil {
		t.Fatalf("complimentary row = %+v, want an end date", comp)
	}

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	rec = controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		fmt.Sprintf(`{"plan":"pro","expires_at":%q,"reason":"an end date in the past is not a grant"}`, past))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("grant with a past end date status=%d, want 400", rec.Code)
	}
}

func TestIntegrationComplimentaryPlanRequiresItsCapability(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	readOnly := newOperator(t, db, access.BillingRead, access.BillingReconcile)

	address := fmt.Sprintf("comp-cap-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	rec := controlCall(t, a, readOnly, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"plan":"pro","reason":"reconcile alone must not give away a plan"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("grant without the capability status=%d, want 403", rec.Code)
	}
	if _, ok := readComplimentary(t, db, accountID); ok {
		t.Error("a complimentary plan was granted despite the refusal")
	}
	if got := accountPlan(t, db, accountID); got != "free" {
		t.Errorf("account plan = %q, want free", got)
	}
}

func TestIntegrationComplimentaryPlanRevocationReturnsTheAccountToFree(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.BillingComp)

	address := fmt.Sprintf("comp-revoke-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	rec := controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"plan":"pro","reason":"internal workspace for the marketing team"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = controlCall(t, a, op, http.MethodDelete, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"reason":"the team moved to a paid workspace"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := accountPlan(t, db, accountID); got != "free" {
		t.Errorf("account plan = %q, want free", got)
	}
	comp, ok := readComplimentary(t, db, accountID)
	if !ok || comp.status != "canceled" {
		t.Errorf("complimentary row = %+v, want it cancelled", comp)
	}

	rec = controlCall(t, a, op, http.MethodDelete, "/api/control/v1/billing/"+accountID+"/complimentary",
		`{"reason":"there is nothing left to revoke"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("revoking twice status=%d, want 404", rec.Code)
	}
}

func TestIntegrationComplimentaryPlanIsReplacedRatherThanDuplicated(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	op := newOperator(t, db, access.BillingComp)

	address := fmt.Sprintf("comp-replace-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	for _, plan := range []string{"starter", "business"} {
		rec := controlCall(t, a, op, http.MethodPost, "/api/control/v1/billing/"+accountID+"/complimentary",
			fmt.Sprintf(`{"plan":%q,"reason":"internal workspace plan adjustment"}`, plan))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("grant %s status=%d body=%s", plan, rec.Code, rec.Body.String())
		}
	}
	var active int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='comp' AND status='active'`,
		accountID).Scan(&active); err != nil {
		t.Fatalf("count: %v", err)
	}
	if active != 1 {
		t.Errorf("active complimentary subscriptions = %d, want 1", active)
	}
	if got := accountPlan(t, db, accountID); got != "business" {
		t.Errorf("account plan = %q, want business", got)
	}
}

func TestIntegrationComplimentaryPlanExpiryDropsTheAccountToFree(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)

	address := fmt.Sprintf("comp-sweep-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,billing_mode,amount_minor,currency,current_period_start,current_period_end)
		 VALUES($1,'internal',$2,'pro','active','comp',0,'KES',now()-interval '40 days',now()-interval '1 hour')`,
		accountID, "COMP-"+accountID); err != nil {
		t.Fatalf("seed complimentary subscription: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID); err != nil {
		t.Fatalf("set plan: %v", err)
	}

	a.expireBillingGrace(ctx)

	if got := accountPlan(t, db, accountID); got != "free" {
		t.Errorf("account plan = %q, want free once the complimentary plan has expired", got)
	}
	comp, ok := readComplimentary(t, db, accountID)
	if !ok || comp.status != "canceled" {
		t.Errorf("complimentary row = %+v, want it cancelled", comp)
	}
}

func TestIntegrationComplimentaryPlanSurvivesTheBillingGraceSweep(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)

	address := fmt.Sprintf("comp-grace-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)

	ctx := context.Background()
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,billing_mode,amount_minor,currency,current_period_start,current_period_end,grace_until)
		 VALUES($1,'paystack',$2,'starter','past_due','auto',10000,'KES',now()-interval '40 days',now()-interval '10 days',now()-interval '1 hour')`,
		accountID, "sub-lapsed-"+accountID); err != nil {
		t.Fatalf("seed lapsed subscription: %v", err)
	}
	if _, err := db.Exec(ctx,
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,billing_mode,amount_minor,currency,current_period_start)
		 VALUES($1,'internal',$2,'pro','active','comp',0,'KES',now())`,
		accountID, "COMP-"+accountID); err != nil {
		t.Fatalf("seed complimentary subscription: %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID); err != nil {
		t.Fatalf("set plan: %v", err)
	}

	a.expireBillingGrace(ctx)

	if got := accountPlan(t, db, accountID); got != "pro" {
		t.Errorf("account plan = %q, want pro; a lapsed card must not end a complimentary plan", got)
	}
}
