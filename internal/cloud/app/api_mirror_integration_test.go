package app

import (
	"bytes"
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

	"github.com/thekiharani/monitor/internal/cloud/config"
)

func bearerRequest(t *testing.T, method, path, key string, body []byte) *http.Request {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Host = "localhost:8900"
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return req
}

func bearerKeyFor(t *testing.T, db *pgxpool.Pool, principalID, accountID string) string {
	t.Helper()
	secret := "usk_test_" + uuid.NewV7().String()
	if _, err := db.Exec(context.Background(),
		`INSERT INTO credentials(principal_id,account_id,kind,audience,name,key_prefix,secret_hash,expires_at)
		 VALUES($1,$2,'api_key','customer_api','mirror test',$3,$4,now()+interval '1 hour')`,
		principalID, accountID, secret[:13], config.HashSecret(secret)); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return secret
}

func TestIntegrationBearerKeyInheritsItsMembershipRole(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	ownerEmail := fmt.Sprintf("mirror-owner-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("mirror-member-%d@example.test", time.Now().UnixNano())
	inviteeEmail := fmt.Sprintf("mirror-invitee-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, memberEmail)

	accountID := createTestAccount(t, db, ownerEmail)
	createTestAccount(t, db, memberEmail)
	defer cleanupInvites(t, db, accountID)
	setPlan(t, a, accountID, "pro")

	ownerPrincipal := principalOf(t, db, ownerEmail)
	memberPrincipal := principalOf(t, db, memberEmail)
	setRole(t, db, memberPrincipal, accountID, "member")

	memberKey := bearerKeyFor(t, db, memberPrincipal, accountID)
	ownerKey := bearerKeyFor(t, db, ownerPrincipal, accountID)

	rec := serve(a, bearerRequest(t, http.MethodPost, "/api/v1/members/invites", memberKey,
		[]byte(fmt.Sprintf(`{"email":"%s","role":"member"}`, inviteeEmail))))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden_role") {
		t.Fatalf("member key invite status=%d body=%s, want 403 forbidden_role", rec.Code, rec.Body.String())
	}

	rec = serve(a, bearerRequest(t, http.MethodPost, "/api/v1/members/invites", ownerKey,
		[]byte(fmt.Sprintf(`{"email":"%s","role":"member"}`, inviteeEmail))))
	if rec.Code != http.StatusCreated {
		t.Fatalf("owner key invite status=%d body=%s, want 201", rec.Code, rec.Body.String())
	}
}

func TestIntegrationBearerKeyRunsTheWorkspaceSurface(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	ownerEmail := fmt.Sprintf("mirror-workspace-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	accountID := createTestAccount(t, db, ownerEmail)
	setPlan(t, a, accountID, "pro")
	key := bearerKeyFor(t, db, principalOf(t, db, ownerEmail), accountID)

	rec := serve(a, bearerRequest(t, http.MethodPatch, "/api/v1/account", key, []byte(`{"name":"Renamed by the API"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	var stored string
	if err := db.QueryRow(context.Background(), `SELECT COALESCE(name,'') FROM accounts WHERE id=$1`, accountID).Scan(&stored); err != nil {
		t.Fatalf("read workspace name: %v", err)
	}
	if stored != "Renamed by the API" {
		t.Errorf("workspace name = %q, want it renamed through the bearer surface", stored)
	}

	rec = serve(a, bearerRequest(t, http.MethodPatch, "/api/v1/me", key, []byte(`{"display_name":"Api Caller","timezone":"Africa/Nairobi"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("profile status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(a, bearerRequest(t, http.MethodGet, "/api/v1/members", key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("members status=%d body=%s", rec.Code, rec.Body.String())
	}
	var members struct {
		Data struct {
			CanManage bool   `json:"can_manage"`
			YourRole  string `json:"your_role"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &members); err != nil {
		t.Fatalf("decode members: %v", err)
	}
	if members.Data.YourRole != "owner" || !members.Data.CanManage {
		t.Errorf("role=%q can_manage=%v, want the key to carry its owner membership", members.Data.YourRole, members.Data.CanManage)
	}

	rec = serve(a, bearerRequest(t, http.MethodPatch, "/api/v1/account", key, []byte(`{"name":"Bearer workspace"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = serve(a, bearerRequest(t, http.MethodGet, "/api/v1/billing", key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("billing status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationSignInOnTheAPIOriginIssuesAWorkingKey(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &mailbox{}
	a := newCoreIntegrationApp(t, db, mail)

	address := fmt.Sprintf("mirror-bootstrap-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM auth_challenges WHERE email=$1`, address)
	}()

	rec := serve(a, bearerRequest(t, http.MethodPost, "/api/v1/auth/otp/request", "", []byte(`{"email":"`+address+`"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("otp request status=%d body=%s", rec.Code, rec.Body.String())
	}
	var challenge struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &challenge); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	message, sent := mail.to(address)
	if !sent {
		t.Fatal("no sign-in code was mailed")
	}
	code := extractOTP(message.Text)
	if code == "" {
		code = extractOTP(message.HTML)
	}
	if code == "" {
		t.Fatal("no six-digit code in the sign-in email")
	}

	rec = serve(a, bearerRequest(t, http.MethodPost, "/api/v1/auth/otp/verify", "",
		[]byte(`{"challenge_id":"`+challenge.ChallengeID+`","email":"`+address+`","code":"`+code+`","key_name":"cli"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("otp verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 0 {
		t.Errorf("the API origin set %d cookies; a bearer sign-in must not start a browser session", len(cookies))
	}
	var issued struct {
		Data struct {
			Key       string `json:"key"`
			Name      string `json:"name"`
			AccountID string `json:"account_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issued key: %v", err)
	}
	if !strings.HasPrefix(issued.Data.Key, "usk_") || issued.Data.Name != "cli" {
		t.Fatalf("issued key = %+v, want a named usk_ key", issued.Data)
	}

	rec = serve(a, bearerRequest(t, http.MethodGet, "/api/v1/me", issued.Data.Key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the bootstrapped key could not read its own account: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationBearerKeysAreRejectedOnTheSessionSurface(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	address := fmt.Sprintf("mirror-boundary-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)
	key := bearerKeyFor(t, db, principalOf(t, db, address), accountID)

	for _, path := range []string{"/api/dashboard/v1/me", "/api/dashboard/v1/members", "/api/dashboard/v1/billing"} {
		rec := serve(a, bearerRequest(t, http.MethodGet, path, key, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a bearer key status=%d, want 401; the dashboard surface stays cookie-only", path, rec.Code)
		}
	}
}

func TestIntegrationKeysForAnotherWorkspaceRequireMembershipThere(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	mine := fmt.Sprintf("mirror-mine-%d@example.test", time.Now().UnixNano())
	theirs := fmt.Sprintf("mirror-theirs-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, mine)
	defer deleteTestAccount(t, db, theirs)

	myAccount := createTestAccount(t, db, mine)
	theirAccount := createTestAccount(t, db, theirs)
	setPlan(t, a, myAccount, "pro")
	setPlan(t, a, theirAccount, "pro")

	myPrincipal := principalOf(t, db, mine)
	key := bearerKeyFor(t, db, myPrincipal, myAccount)

	rec := serve(a, bearerRequest(t, http.MethodPost, "/api/v1/api-keys", key,
		[]byte(`{"name":"reach","account_id":"`+theirAccount+`"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s, want 404 for a workspace the caller does not belong to", rec.Code, rec.Body.String())
	}

	setRole(t, db, myPrincipal, theirAccount, "admin")
	rec = serve(a, bearerRequest(t, http.MethodPost, "/api/v1/api-keys", key,
		[]byte(`{"name":"reach","account_id":"`+theirAccount+`"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s, want 201 once the membership exists", rec.Code, rec.Body.String())
	}
	var issued struct {
		Data struct {
			Key       string `json:"key"`
			AccountID string `json:"account_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issued key: %v", err)
	}
	if issued.Data.AccountID != theirAccount {
		t.Errorf("account_id = %s, want the requested workspace %s", issued.Data.AccountID, theirAccount)
	}

	rec = serve(a, bearerRequest(t, http.MethodGet, "/api/v1/me", issued.Data.Key, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the second-workspace key does not authenticate: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var profile struct {
		Data struct {
			Account map[string]any `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode profile: %v", err)
	}
	if profile.Data.Account["id"] != theirAccount {
		t.Errorf("key resolves to account %v, want %s", profile.Data.Account["id"], theirAccount)
	}
	if profile.Data.Account["role"] != "admin" {
		t.Errorf("key role = %v, want the admin membership it was minted under", profile.Data.Account["role"])
	}
}

func TestIntegrationWorkspaceAndBillingFollowTheRoleTable(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	ownerEmail := fmt.Sprintf("mirror-gate-owner-%d@example.test", time.Now().UnixNano())
	adminEmail := fmt.Sprintf("mirror-gate-admin-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("mirror-gate-member-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, adminEmail)
	defer deleteTestAccount(t, db, memberEmail)

	accountID := createTestAccount(t, db, ownerEmail)
	createTestAccount(t, db, adminEmail)
	createTestAccount(t, db, memberEmail)
	setPlan(t, a, accountID, "pro")

	adminPrincipal := principalOf(t, db, adminEmail)
	memberPrincipal := principalOf(t, db, memberEmail)
	setRole(t, db, adminPrincipal, accountID, "admin")
	setRole(t, db, memberPrincipal, accountID, "member")

	adminKey := bearerKeyFor(t, db, adminPrincipal, accountID)
	memberKey := bearerKeyFor(t, db, memberPrincipal, accountID)

	rec := serve(a, bearerRequest(t, http.MethodPatch, "/api/v1/account", memberKey, []byte(`{"name":"Member rename"}`)))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden_role") {
		t.Errorf("member rename status=%d body=%s, want 403 forbidden_role", rec.Code, rec.Body.String())
	}
	rec = serve(a, bearerRequest(t, http.MethodPatch, "/api/v1/account", adminKey, []byte(`{"name":"Admin rename"}`)))
	if rec.Code != http.StatusOK {
		t.Errorf("admin rename status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}

	for name, key := range map[string]string{"member": memberKey, "admin": adminKey} {
		rec = serve(a, bearerRequest(t, http.MethodPost, "/api/v1/billing/cancel", key, nil))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "forbidden_role") {
			t.Errorf("%s billing cancel status=%d body=%s, want 403 forbidden_role", name, rec.Code, rec.Body.String())
		}
	}

	rec = serve(a, bearerRequest(t, http.MethodGet, "/api/v1/billing", memberKey, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("member billing read status=%d, want 200; reading the plan is open to every role", rec.Code)
	}

	rec = serve(a, bearerRequest(t, http.MethodPost, "/api/v1/api-keys", memberKey, []byte(`{"name":"member key"}`)))
	if rec.Code != http.StatusCreated {
		t.Errorf("member key create status=%d body=%s, want 201; every role may hold a key", rec.Code, rec.Body.String())
	}
}
