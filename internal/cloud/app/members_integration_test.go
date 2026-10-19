package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

func sessionFor(t *testing.T, db *pgxpool.Pool, principalID, accountID string) string {
	t.Helper()
	secret := fmt.Sprintf("sess-%d-%s", time.Now().UnixNano(), principalID[:8])
	if _, err := db.Exec(context.Background(),
		`INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at)
		 VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`,
		principalID, accountID, config.HashSecret(secret),
	); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return secret
}

func principalOf(t *testing.T, db *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(),
		`SELECT principal_id::text FROM principal_identities WHERE verified_email=$1`, email,
	).Scan(&id); err != nil {
		t.Fatalf("principal for %s: %v", email, err)
	}
	return id
}

func setRole(t *testing.T, db *pgxpool.Pool, principalID, accountID, role string) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,$3)
		 ON CONFLICT(principal_id,account_id) DO UPDATE SET role=EXCLUDED.role,status='active',deleted_at=NULL`,
		principalID, accountID, role,
	); err != nil {
		t.Fatalf("set role %s: %v", role, err)
	}
}

func cleanupInvites(t *testing.T, db *pgxpool.Pool, accountID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(ctx, `DELETE FROM account_invites WHERE account_id=$1`, accountID); err != nil {
		t.Fatalf("cleanup invites: %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM email_jobs WHERE account_id=$1`, accountID); err != nil {
		t.Fatalf("cleanup jobs: %v", err)
	}
}

func TestIntegrationInviteAndAcceptGrantsMembership(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	ownerEmail := fmt.Sprintf("team-owner-%d@example.test", time.Now().UnixNano())
	inviteeEmail := fmt.Sprintf("team-invitee-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, inviteeEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	inviteeAccount := createTestAccount(t, db, inviteeEmail)
	defer cleanupInvites(t, db, ownerAccount)

	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, ownerAccount); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	ownerPrincipal := principalOf(t, db, ownerEmail)
	inviteePrincipal := principalOf(t, db, inviteeEmail)
	ownerSession := sessionFor(t, db, ownerPrincipal, ownerAccount)

	rec := httptest.NewRecorder()
	a.inviteMember(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites", ownerSession,
		[]byte(fmt.Sprintf(`{"email":"%s","role":"admin"}`, inviteeEmail))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite status=%d body=%s", rec.Code, rec.Body.String())
	}

	var jobPayload []byte
	if err := db.QueryRow(context.Background(),
		`SELECT payload FROM email_jobs WHERE account_id=$1 AND template='team_invite'`, ownerAccount,
	).Scan(&jobPayload); err != nil {
		t.Fatalf("invite email was not queued: %v", err)
	}
	var payload struct {
		AcceptURL string `json:"accept_url"`
	}
	if err := json.Unmarshal(jobPayload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	token := payload.AcceptURL[len(a.cfg.AppOrigin+"/app/invite/"):]
	if token == "" {
		t.Fatal("accept url carried no token")
	}

	inviteeSession := sessionFor(t, db, inviteePrincipal, inviteeAccount)
	rec = httptest.NewRecorder()
	req := sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites/"+token+"/accept", inviteeSession, nil)
	req.SetPathValue("token", token)
	a.acceptInvite(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", rec.Code, rec.Body.String())
	}

	var role string
	if err := db.QueryRow(context.Background(),
		`SELECT role FROM account_memberships WHERE principal_id=$1 AND account_id=$2 AND status='active'`,
		inviteePrincipal, ownerAccount,
	).Scan(&role); err != nil {
		t.Fatalf("membership was not created: %v", err)
	}
	if role != "admin" {
		t.Errorf("role = %q, want admin", role)
	}

	rec = httptest.NewRecorder()
	req = sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites/"+token+"/accept", inviteeSession, nil)
	req.SetPathValue("token", token)
	a.acceptInvite(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("re-opening an accepted invite should be idempotent, got %d", rec.Code)
	}
	var memberships int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM account_memberships WHERE principal_id=$1 AND account_id=$2`,
		inviteePrincipal, ownerAccount,
	).Scan(&memberships); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	if memberships != 1 {
		t.Errorf("memberships = %d, want 1", memberships)
	}
}

func TestIntegrationSigningUpWithAPendingInviteJoinsThatWorkspace(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &capturingMail{}
	a := newCoreIntegrationApp(t, db, mail)

	ownerEmail := fmt.Sprintf("host-%d@example.test", time.Now().UnixNano())
	newcomerEmail := fmt.Sprintf("newcomer-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, newcomerEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	defer cleanupInvites(t, db, ownerAccount)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, ownerAccount); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	ownerSession := sessionFor(t, db, principalOf(t, db, ownerEmail), ownerAccount)

	rec := httptest.NewRecorder()
	a.inviteMember(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites", ownerSession,
		fmt.Appendf(nil, `{"email":"%s","role":"member"}`, newcomerEmail)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite status=%d body=%s", rec.Code, rec.Body.String())
	}

	before := accountCount(t, db)

	rec = httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/auth/otp/request", bytes.NewReader(fmt.Appendf(nil, `{"email":"%s"}`, newcomerEmail)), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.requestOTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("otp request status=%d", rec.Code)
	}
	challenge := parseChallengeID(rec.Body.Bytes())
	otp := extractOTP(mail.last.Text)

	rec = httptest.NewRecorder()
	req = testRequest(http.MethodPost, "/auth/otp/verify",
		bytes.NewReader(fmt.Appendf(nil, `{"challenge_id":"%s","email":"%s","code":"%s"}`, challenge, newcomerEmail, otp)),
		a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.verifyOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("otp verify status=%d body=%s", rec.Code, rec.Body.String())
	}

	if after := accountCount(t, db); after != before {
		t.Errorf("accounts went from %d to %d: an invited signup should not create its own account", before, after)
	}
	var role string
	if err := db.QueryRow(context.Background(),
		`SELECT m.role FROM account_memberships m
		 JOIN principal_identities i ON i.principal_id=m.principal_id
		 WHERE i.verified_email=$1`, newcomerEmail,
	).Scan(&role); err != nil {
		t.Fatalf("the newcomer did not join the inviting workspace: %v", err)
	}
	if role != "member" {
		t.Errorf("role = %q, want member", role)
	}
	var pending int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM account_invites WHERE account_id=$1 AND accepted_at IS NULL`, ownerAccount,
	).Scan(&pending); err != nil {
		t.Fatalf("count invites: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending invites = %d, want 0: signing up should consume the invite", pending)
	}
}

func accountCount(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM accounts`).Scan(&count); err != nil {
		t.Fatalf("count accounts: %v", err)
	}
	return count
}

func TestIntegrationInviteCannotBeAcceptedByAnotherAddress(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	ownerEmail := fmt.Sprintf("team-o2-%d@example.test", time.Now().UnixNano())
	targetEmail := fmt.Sprintf("team-t2-%d@example.test", time.Now().UnixNano())
	strangerEmail := fmt.Sprintf("team-s2-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, strangerEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	strangerAccount := createTestAccount(t, db, strangerEmail)
	defer cleanupInvites(t, db, ownerAccount)

	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, ownerAccount); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	ownerPrincipal := principalOf(t, db, ownerEmail)
	ownerSession := sessionFor(t, db, ownerPrincipal, ownerAccount)

	rec := httptest.NewRecorder()
	a.inviteMember(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites", ownerSession,
		[]byte(fmt.Sprintf(`{"email":"%s"}`, targetEmail))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("invite status=%d body=%s", rec.Code, rec.Body.String())
	}
	var jobPayload []byte
	if err := db.QueryRow(context.Background(),
		`SELECT payload FROM email_jobs WHERE account_id=$1 AND template='team_invite'`, ownerAccount,
	).Scan(&jobPayload); err != nil {
		t.Fatalf("queue: %v", err)
	}
	var payload struct {
		AcceptURL string `json:"accept_url"`
	}
	if err := json.Unmarshal(jobPayload, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	token := payload.AcceptURL[len(a.cfg.AppOrigin+"/app/invite/"):]

	strangerSession := sessionFor(t, db, principalOf(t, db, strangerEmail), strangerAccount)
	rec = httptest.NewRecorder()
	req := sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites/"+token+"/accept", strangerSession, nil)
	req.SetPathValue("token", token)
	a.acceptInvite(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger accept status=%d, want 403 body=%s", rec.Code, rec.Body.String())
	}
	var count int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM account_memberships WHERE account_id=$1`, ownerAccount,
	).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("membership count = %d, want 1 (only the owner)", count)
	}
}

func TestIntegrationMemberRoleCannotManageTheTeam(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	ownerEmail := fmt.Sprintf("team-o3-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("team-m3-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, memberEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	memberOwnAccount := createTestAccount(t, db, memberEmail)
	defer cleanupInvites(t, db, ownerAccount)

	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, ownerAccount); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	memberPrincipal := principalOf(t, db, memberEmail)
	setRole(t, db, memberPrincipal, ownerAccount, "member")
	memberSession := sessionFor(t, db, memberPrincipal, ownerAccount)

	rec := httptest.NewRecorder()
	a.inviteMember(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites", memberSession,
		[]byte(`{"email":"someone@example.test"}`)))
	if rec.Code != http.StatusForbidden {
		t.Errorf("member invite status=%d, want 403", rec.Code)
	}

	ownerPrincipal := principalOf(t, db, ownerEmail)
	rec = httptest.NewRecorder()
	req := sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/members/"+ownerPrincipal, memberSession, nil)
	req.SetPathValue("id", ownerPrincipal)
	a.removeMember(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Error("a member removed the owner")
	}

	rec = httptest.NewRecorder()
	req = sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/members/"+ownerPrincipal, memberSession,
		[]byte(`{"role":"member"}`))
	req.SetPathValue("id", ownerPrincipal)
	a.updateMemberRole(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("member role change status=%d, want 403", rec.Code)
	}

	_ = memberOwnAccount
}

func TestIntegrationOwnerCannotBeRemovedOrDemoted(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	ownerEmail := fmt.Sprintf("team-o4-%d@example.test", time.Now().UnixNano())
	adminEmail := fmt.Sprintf("team-a4-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, adminEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	createTestAccount(t, db, adminEmail)
	defer cleanupInvites(t, db, ownerAccount)

	ownerPrincipal := principalOf(t, db, ownerEmail)
	adminPrincipal := principalOf(t, db, adminEmail)
	setRole(t, db, adminPrincipal, ownerAccount, "admin")
	adminSession := sessionFor(t, db, adminPrincipal, ownerAccount)
	ownerSession := sessionFor(t, db, ownerPrincipal, ownerAccount)

	rec := httptest.NewRecorder()
	req := sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/members/"+ownerPrincipal, adminSession, nil)
	req.SetPathValue("id", ownerPrincipal)
	a.removeMember(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Error("an admin removed the owner")
	}

	rec = httptest.NewRecorder()
	req = sessionRequest(t, http.MethodDelete, "/api/dashboard/v1/members/"+ownerPrincipal, ownerSession, nil)
	req.SetPathValue("id", ownerPrincipal)
	a.removeMember(rec, req)
	if rec.Code != http.StatusConflict {
		t.Errorf("owner self-removal status=%d, want 409", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/members/"+adminPrincipal, ownerSession,
		[]byte(`{"role":"member"}`))
	req.SetPathValue("id", adminPrincipal)
	a.updateMemberRole(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("owner demoting an admin status=%d body=%s", rec.Code, rec.Body.String())
	}
	var role string
	if err := db.QueryRow(context.Background(),
		`SELECT role FROM account_memberships WHERE principal_id=$1 AND account_id=$2`, adminPrincipal, ownerAccount,
	).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "member" {
		t.Errorf("role = %q, want member", role)
	}
}

func TestIntegrationSeatLimitCountsPendingInvites(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	ownerEmail := fmt.Sprintf("team-seat-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	ownerAccount := createTestAccount(t, db, ownerEmail)
	defer cleanupInvites(t, db, ownerAccount)

	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, ownerAccount); err != nil {
		t.Fatalf("set plan: %v", err)
	}
	ownerSession := sessionFor(t, db, principalOf(t, db, ownerEmail), ownerAccount)

	invite := func(email string) int {
		rec := httptest.NewRecorder()
		a.inviteMember(rec, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/members/invites", ownerSession,
			fmt.Appendf(nil, `{"email":"%s"}`, email)))
		return rec.Code
	}

	if got := invite("seat1@example.test"); got != http.StatusCreated {
		t.Fatalf("first invite status=%d, want 201", got)
	}
	if got := invite("seat2@example.test"); got != http.StatusCreated {
		t.Fatalf("second invite status=%d, want 201 (starter seats three)", got)
	}
	if got := invite("seat3@example.test"); got != http.StatusTooManyRequests {
		t.Errorf("third invite status=%d, want 429: the owner plus two pending invites fill a starter plan", got)
	}
}

func TestIntegrationSwitchAccountRequiresMembership(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	firstEmail := fmt.Sprintf("switch-a-%d@example.test", time.Now().UnixNano())
	secondEmail := fmt.Sprintf("switch-b-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, firstEmail)
	defer deleteTestAccount(t, db, secondEmail)
	firstAccount := createTestAccount(t, db, firstEmail)
	secondAccount := createTestAccount(t, db, secondEmail)

	firstPrincipal := principalOf(t, db, firstEmail)
	session := sessionFor(t, db, firstPrincipal, firstAccount)

	rec := httptest.NewRecorder()
	req := sessionRequest(t, http.MethodPost, "/api/dashboard/v1/accounts/"+secondAccount+"/switch", session, nil)
	req.SetPathValue("id", secondAccount)
	a.switchAccount(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("switching into a foreign account status=%d, want 404", rec.Code)
	}

	setRole(t, db, firstPrincipal, secondAccount, "member")
	rec = httptest.NewRecorder()
	req = sessionRequest(t, http.MethodPost, "/api/dashboard/v1/accounts/"+secondAccount+"/switch", session, nil)
	req.SetPathValue("id", secondAccount)
	a.switchAccount(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("switching into a joined account status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sessionCookies(rec)) == 0 {
		t.Error("switching accounts did not issue a new session cookie")
	}
}
