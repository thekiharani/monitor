package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIntegrationCloseAccountRequiresMatchingConfirmation(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	emailAddr := fmt.Sprintf("close-c-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	session := sessionFor(t, db, principalOf(t, db, emailAddr), accountID)

	rec := httptest.NewRecorder()
	a.closeAccount(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/account/close", session,
		[]byte(`{"confirm":"not-the-right-address@example.test"}`)))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched confirmation status=%d, want 422", rec.Code)
	}
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "active" {
		t.Fatalf("account status = %q after a failed confirmation, want active", status)
	}
}

func TestIntegrationCloseAccountStopsCredentials(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	emailAddr := fmt.Sprintf("close-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	apiKey := createTestAPIKey(t, db, accountID)

	ctx := context.Background()
	session := sessionFor(t, db, principalOf(t, db, emailAddr), accountID)
	rec := httptest.NewRecorder()
	a.closeAccount(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/account/close", session,
		fmt.Appendf(nil, `{"confirm":"%s"}`, emailAddr)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("close status=%d body=%s", rec.Code, rec.Body.String())
	}

	var status string
	if err := db.QueryRow(ctx, `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "closed" {
		t.Errorf("account status = %q, want closed", status)
	}

	keyRec := httptest.NewRecorder()
	keyReq := testRequest(http.MethodGet, "/api/v1/me", nil, a.cfg.APIOrigin)
	keyReq.Header.Set("Authorization", "Bearer "+apiKey)
	a.dashboardMe(keyRec, keyReq)
	if keyRec.Code == http.StatusOK {
		t.Error("an API key still worked after the account was closed")
	}
}

func TestIntegrationCloseAccountRefusesWhileSubscribed(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	emailAddr := fmt.Sprintf("close-sub-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)

	if _, err := db.Exec(context.Background(),
		`INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,effective_at)
		 VALUES($1,'paystack','SUB_test','pro','active',200000,'KES',now())`,
		accountID,
	); err != nil {
		t.Fatalf("seed subscription: %v", err)
	}

	session := sessionFor(t, db, principalOf(t, db, emailAddr), accountID)
	rec := httptest.NewRecorder()
	a.closeAccount(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/account/close", session,
		fmt.Appendf(nil, `{"confirm":"%s"}`, emailAddr)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("close while subscribed status=%d, want 409 body=%s", rec.Code, rec.Body.String())
	}
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM accounts WHERE id=$1`, accountID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "active" {
		t.Errorf("account status = %q, want active", status)
	}
}

func TestIntegrationPurgeRemovesAClosedAccountAndItsData(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()

	doomedEmail := fmt.Sprintf("purge-%d@example.test", time.Now().UnixNano())
	keptEmail := fmt.Sprintf("kept-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, keptEmail)
	doomed := createTestAccount(t, db, doomedEmail)
	kept := createTestAccount(t, db, keptEmail)
	doomedPrincipal := principalOf(t, db, doomedEmail)

	createTestAPIKey(t, db, doomed)
	for _, seed := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO account_monthly_usage(account_id,period,api_requests) VALUES($1,date_trunc('month',now()),3)`, []any{doomed}},
		{`INSERT INTO audit_events(account_id,principal_id,capability,action,target_type,reason) VALUES($1,$2,'account.self.close','account.close','account','closed')`, []any{doomed, doomedPrincipal}},
	} {
		if _, err := db.Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed %q: %v", seed.query, err)
		}
	}

	if _, err := db.Exec(ctx,
		`UPDATE accounts SET status='closed',deleted_at=now()-interval '40 days' WHERE id=$1`, doomed,
	); err != nil {
		t.Fatalf("close account: %v", err)
	}

	a.purgeClosedAccounts(ctx)

	for _, check := range []struct{ label, query string }{
		{"credentials", `SELECT count(*) FROM credentials WHERE account_id=$1`},
		{"memberships", `SELECT count(*) FROM account_memberships WHERE account_id=$1`},
		{"monthly usage", `SELECT count(*) FROM account_monthly_usage WHERE account_id=$1`},
		{"identities", `SELECT count(*) FROM principal_identities WHERE principal_id=(SELECT $1::uuid)`},
	} {
		var count int
		if err := db.QueryRow(ctx, check.query, doomed).Scan(&count); err != nil {
			t.Fatalf("%s: %v", check.label, err)
		}
		if count != 0 {
			t.Errorf("%s left %d rows after purge", check.label, count)
		}
	}
	var identities int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM principal_identities WHERE principal_id=$1`, doomedPrincipal,
	).Scan(&identities); err != nil {
		t.Fatalf("identities: %v", err)
	}
	if identities != 0 {
		t.Error("the email identity should be erased with the account")
	}

	var purged *time.Time
	var status string
	if err := db.QueryRow(ctx, `SELECT status,purged_at FROM accounts WHERE id=$1`, doomed).Scan(&status, &purged); err != nil {
		t.Fatalf("the account tombstone should remain for the audit trail: %v", err)
	}
	if purged == nil || status != "closed" {
		t.Errorf("tombstone status=%q purged_at=%v, want closed and stamped", status, purged)
	}

	var audits int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE account_id=$1`, doomed).Scan(&audits); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audits == 0 {
		t.Error("the append-only audit trail must survive erasure")
	}

	a.purgeClosedAccounts(ctx)
	var reprocessed int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM accounts WHERE id=$1 AND purged_at IS NOT NULL`, doomed,
	).Scan(&reprocessed); err != nil {
		t.Fatalf("recheck: %v", err)
	}
	if reprocessed != 1 {
		t.Error("an erased account should not be picked up again")
	}

	var survivors int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id=$1`, kept).Scan(&survivors); err != nil {
		t.Fatalf("survivor: %v", err)
	}
	if survivors != 1 {
		t.Error("an unrelated account was affected by the purge")
	}
}

func TestIntegrationPurgeWaitsOutTheRetentionWindow(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()

	emailAddr := fmt.Sprintf("recent-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	if _, err := db.Exec(ctx,
		`UPDATE accounts SET status='closed',deleted_at=now()-interval '3 days' WHERE id=$1`, accountID,
	); err != nil {
		t.Fatalf("close: %v", err)
	}

	a.purgeClosedAccounts(ctx)

	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM accounts WHERE id=$1`, accountID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatal("an account closed three days ago must not be purged yet")
	}
}

func TestIntegrationPurgeKeepsAPrincipalWhoStillBelongsElsewhere(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	ctx := context.Background()

	sharedEmail := fmt.Sprintf("shared-%d@example.test", time.Now().UnixNano())
	otherEmail := fmt.Sprintf("other-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, sharedEmail)
	defer deleteTestAccount(t, db, otherEmail)
	ownAccount := createTestAccount(t, db, sharedEmail)
	otherAccount := createTestAccount(t, db, otherEmail)
	shared := principalOf(t, db, sharedEmail)
	setRole(t, db, shared, otherAccount, "member")

	if _, err := db.Exec(ctx,
		`UPDATE accounts SET status='closed',deleted_at=now()-interval '40 days' WHERE id=$1`, otherAccount,
	); err != nil {
		t.Fatalf("close: %v", err)
	}

	a.purgeClosedAccounts(ctx)

	var principals int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM principals WHERE id=$1`, shared).Scan(&principals); err != nil {
		t.Fatalf("principals: %v", err)
	}
	if principals != 1 {
		t.Fatal("a principal who still owns another account must survive the purge")
	}
	var memberships int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM account_memberships WHERE principal_id=$1 AND account_id=$2`, shared, ownAccount,
	).Scan(&memberships); err != nil {
		t.Fatalf("memberships: %v", err)
	}
	if memberships != 1 {
		t.Error("their remaining membership should be untouched")
	}
}
