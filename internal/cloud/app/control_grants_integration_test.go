package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"uuid"

	"github.com/thekiharani/monitor/internal/cloud/access"
)

func grantCount(t *testing.T, a *App, principalID string) int {
	t.Helper()
	var count int
	if err := a.db.QueryRow(context.Background(),
		`SELECT count(*) FROM access_grants WHERE principal_id=$1 AND revoked_at IS NULL`, principalID,
	).Scan(&count); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	return count
}

func TestIntegrationControlGrantRequiresTheGrantCapability(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	reader := newOperator(t, db, access.AccessRead)
	subject := newOperator(t, db)

	body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"platform","reason":"escalation attempt"}`,
		subject.principalID, access.AccountsRead)
	rec := controlCall(t, a, reader, http.MethodPost, "/api/control/v1/access-grants", body)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("granting without access.grant status=%d, want 403 body=%s", rec.Code, rec.Body.String())
	}
	if got := grantCount(t, a, subject.principalID); got != 0 {
		t.Errorf("a capability was granted despite the refusal (%d grants)", got)
	}
}

func TestIntegrationControlGrantRejectsUnknownCapabilities(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant)
	subject := newOperator(t, db)

	for _, capability := range []string{"platform.everything", "platform.accounts.read; DROP TABLE accounts", "", "*"} {
		body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"platform","reason":"invalid capability probe"}`,
			subject.principalID, capability)
		rec := controlCall(t, a, granter, http.MethodPost, "/api/control/v1/access-grants", body)
		if rec.Code == http.StatusCreated {
			t.Errorf("capability %q was accepted", capability)
		}
	}
	if got := grantCount(t, a, subject.principalID); got != 0 {
		t.Errorf("an unknown capability was persisted (%d grants)", got)
	}
}

func TestIntegrationControlGrantAndRevokeAreAudited(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant, access.AccessRead)
	subject := newOperator(t, db)
	ctx := context.Background()

	body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"platform","reason":"onboarding a new support operator"}`,
		subject.principalID, access.AccountsRead)
	rec := controlCall(t, a, granter, http.MethodPost, "/api/control/v1/access-grants", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := grantCount(t, a, subject.principalID); got != 1 {
		t.Fatalf("grants = %d, want 1", got)
	}

	var grantID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM access_grants WHERE principal_id=$1 AND revoked_at IS NULL`, subject.principalID,
	).Scan(&grantID); err != nil {
		t.Fatalf("read grant: %v", err)
	}
	var audited int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='access.granted' AND target_id=$1`,
		subject.principalID+":"+access.AccountsRead,
	).Scan(&audited); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audited != 1 {
		t.Errorf("grant audit events = %d, want 1", audited)
	}

	rec = controlCall(t, a, granter, http.MethodPost,
		"/api/control/v1/access-grants/"+grantID+"/revoke", `{"reason":"operator left the team"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := grantCount(t, a, subject.principalID); got != 0 {
		t.Errorf("grants after revoke = %d, want 0", got)
	}
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='access.revoked' AND target_id=$1`, grantID,
	).Scan(&audited); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audited != 1 {
		t.Errorf("revoke audit events = %d, want 1", audited)
	}
}

func TestIntegrationControlGrantedCapabilityTakesEffectAndStopsOnRevoke(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant)
	subject := newOperator(t, db)
	ctx := context.Background()

	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("subject should start with no access, got %d", rec.Code)
	}

	body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"platform","reason":"granting team visibility"}`,
		subject.principalID, access.MembersRead)
	if rec := controlCall(t, a, granter, http.MethodPost, "/api/control/v1/access-grants", body); rec.Code != http.StatusCreated {
		t.Fatalf("grant status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members", ""); rec.Code != http.StatusOK {
		t.Fatalf("the granted capability did not take effect, got %d", rec.Code)
	}

	var grantID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM access_grants WHERE principal_id=$1 AND revoked_at IS NULL`, subject.principalID,
	).Scan(&grantID); err != nil {
		t.Fatalf("read grant: %v", err)
	}
	if rec := controlCall(t, a, granter, http.MethodPost,
		"/api/control/v1/access-grants/"+grantID+"/revoke", `{"reason":"access no longer required"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke failed")
	}
	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members", ""); rec.Code != http.StatusForbidden {
		t.Errorf("access survived revocation, got %d", rec.Code)
	}
}

func TestIntegrationControlAccountScopedGrantCannotReachAnotherAccount(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant)
	subject := newOperator(t, db)

	firstEmail := fmt.Sprintf("scope-a-%d@example.test", time.Now().UnixNano())
	secondEmail := fmt.Sprintf("scope-b-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, firstEmail)
	defer deleteTestAccount(t, db, secondEmail)
	allowed := createTestAccount(t, db, firstEmail)
	forbidden := createTestAccount(t, db, secondEmail)

	body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"account","scope_id":"%s","reason":"scoped support access"}`,
		subject.principalID, access.MembersRead, allowed)
	if rec := controlCall(t, a, granter, http.MethodPost, "/api/control/v1/access-grants", body); rec.Code != http.StatusCreated {
		t.Fatalf("scoped grant status=%d body=%s", rec.Code, rec.Body.String())
	}

	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members?account_id="+allowed, ""); rec.Code != http.StatusOK {
		t.Errorf("the scoped account should be readable, got %d", rec.Code)
	}
	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members?account_id="+forbidden, ""); rec.Code != http.StatusForbidden {
		t.Errorf("another account was readable with an account-scoped grant, got %d", rec.Code)
	}
	if rec := controlCall(t, a, subject, http.MethodGet, "/api/control/v1/members", ""); rec.Code != http.StatusForbidden {
		t.Errorf("the unscoped platform listing was readable with an account-scoped grant, got %d", rec.Code)
	}
}

func TestIntegrationControlRevokeCredentialIsGatedAndAudited(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	reader := newOperator(t, db, access.CredentialsRead)
	revoker := newOperator(t, db, access.CredentialsRead, access.CredentialsRevoke)

	emailAddr := fmt.Sprintf("cred-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	apiKey := createTestAPIKey(t, db, accountID)
	ctx := context.Background()
	var credentialID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM credentials WHERE account_id=$1 AND audience='customer_api'`, accountID,
	).Scan(&credentialID); err != nil {
		t.Fatalf("read credential: %v", err)
	}

	if rec := controlCall(t, a, reader, http.MethodPost,
		"/api/control/v1/credentials/"+credentialID+"/revoke", `{"reason":"should not be permitted"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("revoke without the capability status=%d, want 403", rec.Code)
	}

	works := func() int {
		rec := httptest.NewRecorder()
		req := testRequest(http.MethodGet, "/api/v1/me", nil, a.cfg.APIOrigin)
		req.Header.Set("Authorization", "Bearer "+apiKey)
		a.dashboardMe(rec, req)
		return rec.Code
	}
	if got := works(); got != http.StatusOK {
		t.Fatalf("the key should work before revocation, got %d", got)
	}

	if rec := controlCall(t, a, revoker, http.MethodPost,
		"/api/control/v1/credentials/"+credentialID+"/revoke", `{"reason":"key leaked in a public repository"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := works(); got == http.StatusOK {
		t.Error("a revoked key still authenticated")
	}
	var audited int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='credential.revoked' AND target_id=$1`, credentialID,
	).Scan(&audited); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audited != 1 {
		t.Errorf("credential revoke audit events = %d, want 1", audited)
	}
}

func TestIntegrationControlRetryJobIsGatedAndAudited(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	reader := newOperator(t, db, access.JobsRead)
	retrier := newOperator(t, db, access.JobsRead, access.JobsRetry)

	emailAddr := fmt.Sprintf("job-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	ctx := context.Background()

	var jobID string
	if err := db.QueryRow(ctx,
		`INSERT INTO email_jobs(account_id,to_email,template,payload,attempts,next_attempt_at,last_error)
		 VALUES($1,$2,'login_code','{}'::jsonb,5,now()+interval '1 day','smtp timeout') RETURNING id::text`,
		accountID, emailAddr,
	).Scan(&jobID); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	if rec := controlCall(t, a, reader, http.MethodPost,
		"/api/control/v1/email-jobs/"+jobID+"/retry", `{"reason":"should not be permitted"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("retry without the capability status=%d, want 403", rec.Code)
	}

	if rec := controlCall(t, a, retrier, http.MethodPost,
		"/api/control/v1/email-jobs/"+jobID+"/retry", `{"reason":"provider outage has been resolved"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("retry status=%d body=%s", rec.Code, rec.Body.String())
	}
	var attempts int
	var nextAttempt time.Time
	if err := db.QueryRow(ctx,
		`SELECT attempts,next_attempt_at FROM email_jobs WHERE id=$1`, jobID,
	).Scan(&attempts, &nextAttempt); err != nil {
		t.Fatalf("read job: %v", err)
	}
	if nextAttempt.After(time.Now().Add(time.Minute)) {
		t.Errorf("retry did not bring the job forward: next attempt %s", nextAttempt)
	}
	var audited int
	if err := db.QueryRow(ctx,
		`SELECT count(*) FROM audit_events WHERE action='email_job.retried' AND target_id=$1`, jobID,
	).Scan(&audited); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if audited != 1 {
		t.Errorf("retry audit events = %d, want 1", audited)
	}
}

func TestIntegrationControlGrantRejectsAMalformedPrincipal(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant)

	for _, principal := range []string{"not-a-uuid", "", uuid.NewV7().String() + "x"} {
		body := fmt.Sprintf(`{"principal_id":"%s","capability":"%s","scope_type":"platform","reason":"malformed principal probe"}`,
			principal, access.AccountsRead)
		if rec := controlCall(t, a, granter, http.MethodPost, "/api/control/v1/access-grants", body); rec.Code == http.StatusCreated {
			t.Errorf("principal %q was accepted", principal)
		}
	}
}

func TestIntegrationControlOperatorCannotRevokeTheirOwnDelegation(t *testing.T) {
	db := openIntegrationDB(t)
	a := controlApp(t, db)
	granter := newOperator(t, db, access.AccessGrant, access.AccessRead)
	ctx := context.Background()

	var grantID string
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM access_grants WHERE principal_id=$1 AND capability=$2 AND revoked_at IS NULL`,
		granter.principalID, access.AccessGrant,
	).Scan(&grantID); err != nil {
		t.Fatalf("read own grant: %v", err)
	}

	rec := controlCall(t, a, granter, http.MethodPost,
		"/api/control/v1/access-grants/"+grantID+"/revoke", `{"reason":"attempting to revoke my own delegation"}`)
	if rec.Code == http.StatusNoContent {
		t.Fatal("an operator revoked their own access delegation capability")
	}
	if got := grantCount(t, a, granter.principalID); got != 2 {
		t.Errorf("grants = %d, want both to survive", got)
	}
}
