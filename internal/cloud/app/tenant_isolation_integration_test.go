package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

type tenant struct {
	email     string
	accountID string
	principal string
	apiKey    string
	session   string
	inviteID  string
	keyID     string
}

func newTenant(t *testing.T, a *App, label string) tenant {
	t.Helper()
	db := a.db
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	own := tenant{email: fmt.Sprintf("iso-%s-%d@example.test", label, stamp)}
	own.accountID = createTestAccount(t, db, own.email)
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, `DELETE FROM account_invites WHERE account_id=$1`, own.accountID)
		deleteTestAccount(t, db, own.email)
	})
	if _, err := db.Exec(ctx, `UPDATE accounts SET plan='business' WHERE id=$1`, own.accountID); err != nil {
		t.Fatalf("plan: %v", err)
	}
	own.principal = principalOf(t, db, own.email)
	own.apiKey = createTestAPIKey(t, db, own.accountID)
	own.session = sessionFor(t, db, own.principal, own.accountID)

	if err := db.QueryRow(ctx,
		`INSERT INTO account_invites(account_id,email,role,token_hash,inviter_id,expires_at)
		 VALUES($1,$2,'member',$3,$4,now()+interval '1 day') RETURNING id::text`,
		own.accountID, "invitee-"+label+fmt.Sprint(stamp)+"@example.test",
		config.HashSecret(fmt.Sprintf("invite-%s-%d", label, stamp)), own.principal,
	).Scan(&own.inviteID); err != nil {
		t.Fatalf("invite: %v", err)
	}
	if err := db.QueryRow(ctx,
		`SELECT id::text FROM credentials WHERE account_id=$1 AND audience='customer_api' LIMIT 1`, own.accountID,
	).Scan(&own.keyID); err != nil {
		t.Fatalf("key id: %v", err)
	}
	return own
}

func TestIntegrationCustomerRoutesAreTenantIsolated(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	victim := newTenant(t, a, "victim")
	attacker := newTenant(t, a, "attacker")

	sessionCase := func(name, method, target string, body string, handler http.HandlerFunc, pathValues map[string]string) {
		t.Helper()
		var payload []byte
		if body != "" {
			payload = []byte(body)
		}
		req := sessionRequest(t, method, target, attacker.session, payload)
		for key, value := range pathValues {
			req.SetPathValue(key, value)
		}
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code >= 200 && rec.Code < 300 {
			t.Errorf("%s: attacker reached the victim's resource (status %d) body=%s", name, rec.Code, rec.Body.String())
		}
	}

	sessionCase("revoke foreign invite", http.MethodDelete,
		"/api/dashboard/v1/members/invites/"+victim.inviteID, "", a.revokeInvite,
		map[string]string{"id": victim.inviteID})
	sessionCase("resend foreign invite", http.MethodPost,
		"/api/dashboard/v1/members/invites/"+victim.inviteID+"/resend", "", a.resendInvite,
		map[string]string{"id": victim.inviteID})
	sessionCase("change role in foreign account", http.MethodPatch,
		"/api/dashboard/v1/members/"+victim.principal, `{"role":"member"}`, a.updateMemberRole,
		map[string]string{"id": victim.principal})
	sessionCase("remove member from foreign account", http.MethodDelete,
		"/api/dashboard/v1/members/"+victim.principal, "", a.removeMember,
		map[string]string{"id": victim.principal})
	sessionCase("switch into foreign account", http.MethodPost,
		"/api/dashboard/v1/accounts/"+victim.accountID+"/switch", "", a.switchAccount,
		map[string]string{"id": victim.accountID})
	sessionCase("revoke foreign api key", http.MethodDelete,
		"/api/dashboard/v1/api-keys/"+victim.keyID, "", a.revokeAPIKey,
		map[string]string{"id": victim.keyID})
}

func TestIntegrationCustomerListingsNeverLeakAcrossTenants(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	victim := newTenant(t, a, "lv")
	attacker := newTenant(t, a, "la")

	body := func(handler http.HandlerFunc, target string) string {
		req := testRequest(http.MethodGet, target, nil, a.cfg.APIOrigin)
		req.Header.Set("Authorization", "Bearer "+attacker.apiKey)
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d", target, rec.Code)
		}
		return rec.Body.String()
	}
	sessionBody := func(handler http.HandlerFunc, target string) string {
		rec := httptest.NewRecorder()
		handler(rec, sessionRequest(t, http.MethodGet, target, attacker.session, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	for name, payload := range map[string]string{
		"usage":    body(a.usage, "/api/v1/usage"),
		"api keys": sessionBody(a.dashboardAPIKeys, "/api/dashboard/v1/api-keys"),
		"members":  sessionBody(a.listMembers, "/api/dashboard/v1/members"),
		"accounts": sessionBody(a.listAccounts, "/api/dashboard/v1/accounts"),
	} {
		for label, needle := range map[string]string{
			"account id": victim.accountID,
			"email":      victim.email,
		} {
			if strings.Contains(payload, needle) {
				t.Errorf("%s leaked the victim's %s", name, label)
			}
		}
	}
}
