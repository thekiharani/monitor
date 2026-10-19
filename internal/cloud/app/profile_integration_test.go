package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/email"
)

type mailbox struct {
	mu   sync.Mutex
	sent []email.Message
}

func (m *mailbox) Send(_ context.Context, msg email.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *mailbox) to(address string) (email.Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.sent {
		if msg.To == address {
			return msg, true
		}
	}
	return email.Message{}, false
}

func seedChallenge(t *testing.T, db *pgxpool.Pool, key []byte, address, purpose string, principalID *string) (string, string) {
	t.Helper()
	id := uuid.NewV7().String()
	const code = "424242"
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + ":" + code))
	if _, err := db.Exec(context.Background(),
		`INSERT INTO auth_challenges(id,email,code_mac,expires_at,purpose,principal_id)
		 VALUES($1,$2,$3,now()+interval '10 minutes',$4,$5)`,
		id, address, mac.Sum(nil), purpose, principalID); err != nil {
		t.Fatalf("seed challenge: %v", err)
	}
	return id, code
}

func readProfile(t *testing.T, a *App, session string) map[string]any {
	t.Helper()
	rec := serve(a, sessionRequest(t, http.MethodGet, "/api/dashboard/v1/me", session, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Account map[string]any `json:"account"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("me body is not JSON: %v (%s)", err, rec.Body.String())
	}
	return out.Data.Account
}

func forgetPrincipal(t *testing.T, db *pgxpool.Pool, principalID string) {
	t.Helper()
	ctx := context.Background()
	for _, query := range []string{
		`DELETE FROM auth_challenges WHERE principal_id=$1`,
		`DELETE FROM credentials WHERE principal_id=$1`,
		`DELETE FROM account_memberships WHERE principal_id=$1`,
		`DELETE FROM principal_identities WHERE principal_id=$1`,
	} {
		if _, err := db.Exec(ctx, query, principalID); err != nil {
			t.Fatalf("forget principal: %v", err)
		}
	}
}

func TestIntegrationProfileUpdateStoresTheNameAndTimezone(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	address := fmt.Sprintf("profile-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)
	session := sessionFor(t, db, principalOf(t, db, address), accountID)

	rec := serve(a, sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/me", session,
		[]byte(`{"display_name":"  Wanjiru Kamau  ","timezone":"Africa/Nairobi"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /me status=%d body=%s", rec.Code, rec.Body.String())
	}

	profile := readProfile(t, a, session)
	if profile["display_name"] != "Wanjiru Kamau" {
		t.Errorf("display_name = %v, want the trimmed name", profile["display_name"])
	}
	if profile["timezone"] != "Africa/Nairobi" {
		t.Errorf("timezone = %v, want Africa/Nairobi", profile["timezone"])
	}
	if profile["email"] != address {
		t.Errorf("email = %v, want %s", profile["email"], address)
	}

	rec = serve(a, sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/me", session,
		[]byte(`{"display_name":""}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("clearing the name status=%d body=%s", rec.Code, rec.Body.String())
	}
	if profile = readProfile(t, a, session); profile["display_name"] != "" {
		t.Errorf("display_name = %v, want it cleared", profile["display_name"])
	}
	if profile["timezone"] != "Africa/Nairobi" {
		t.Errorf("clearing the name also cleared the timezone: %v", profile["timezone"])
	}
}

func TestIntegrationProfileUpdateRejectsUnusableValues(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	address := fmt.Sprintf("profile-bad-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	accountID := createTestAccount(t, db, address)
	session := sessionFor(t, db, principalOf(t, db, address), accountID)

	cases := []struct {
		name string
		body string
		want string
	}{
		{"unknown zone", `{"timezone":"Mars/Olympus_Mons"}`, "invalid_timezone"},
		{"path traversal zone", `{"timezone":"../../etc/passwd"}`, "invalid_timezone"},
		{"local zone", `{"timezone":"Local"}`, "invalid_timezone"},
		{"overlong name", `{"display_name":"` + strings.Repeat("n", 129) + `"}`, "invalid_display_name"},
		{"control characters", `{"display_name":"line\nbreak"}`, "invalid_display_name"},
		{"nothing to change", `{}`, "invalid_request"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			rec := serve(a, sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/me", session, []byte(testCase.body)))
			if rec.Code < 400 {
				t.Fatalf("status=%d, want a rejection (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), testCase.want) {
				t.Errorf("body = %s, want error %s", rec.Body.String(), testCase.want)
			}
		})
	}
}

func TestIntegrationProfileUpdateCannotRenameAnotherMember(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	ownerEmail := fmt.Sprintf("rls-owner-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("rls-member-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, memberEmail)
	accountID := createTestAccount(t, db, ownerEmail)
	memberAccount := createTestAccount(t, db, memberEmail)
	defer func() { _, _ = db.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, memberAccount) }()

	ownerID := principalOf(t, db, ownerEmail)
	memberID := principalOf(t, db, memberEmail)
	setRole(t, db, memberID, accountID, "admin")

	var affected int64
	if err := a.withTenantAsPrincipal(context.Background(), accountID, memberID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(context.Background(),
			`UPDATE principals SET display_name='seized' WHERE id=$1`, ownerID)
		affected = tag.RowsAffected()
		return execErr
	}); err != nil {
		t.Fatalf("tenant update: %v", err)
	}
	if affected != 0 {
		t.Fatalf("an admin renamed a fellow member: %d rows changed", affected)
	}

	var ownerName string
	if err := db.QueryRow(context.Background(),
		`SELECT COALESCE(display_name,'') FROM principals WHERE id=$1`, ownerID).Scan(&ownerName); err != nil {
		t.Fatalf("read owner name: %v", err)
	}
	if ownerName == "seized" {
		t.Error("the admin's rename landed on the owner")
	}
}

func TestIntegrationWorkspaceRenameNeedsAManagingRole(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	ownerEmail := fmt.Sprintf("ws-owner-%d@example.test", time.Now().UnixNano())
	memberEmail := fmt.Sprintf("ws-member-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, ownerEmail)
	defer deleteTestAccount(t, db, memberEmail)
	accountID := createTestAccount(t, db, ownerEmail)
	memberAccount := createTestAccount(t, db, memberEmail)
	defer func() { _, _ = db.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, memberAccount) }()

	memberID := principalOf(t, db, memberEmail)
	setRole(t, db, memberID, accountID, "member")
	memberSession := sessionFor(t, db, memberID, accountID)
	ownerSession := sessionFor(t, db, principalOf(t, db, ownerEmail), accountID)

	rec := serve(a, sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/account", memberSession,
		[]byte(`{"name":"Member's rename"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member rename status=%d, want 403 (%s)", rec.Code, rec.Body.String())
	}

	rec = serve(a, sessionRequest(t, http.MethodPatch, "/api/dashboard/v1/account", ownerSession,
		[]byte(`{"name":"Noria Labs"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner rename status=%d body=%s", rec.Code, rec.Body.String())
	}
	if profile := readProfile(t, a, ownerSession); profile["name"] != "Noria Labs" {
		t.Errorf("workspace name = %v, want Noria Labs", profile["name"])
	}
	if profile := readProfile(t, a, memberSession); profile["name"] != "Noria Labs" {
		t.Errorf("the member sees workspace name %v, want Noria Labs", profile["name"])
	}
}

func TestIntegrationEmailChangeNeedsACodeSentToTheNewAddress(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &mailbox{}
	a := newCoreIntegrationApp(t, db, mail)

	oldAddress := fmt.Sprintf("move-from-%d@example.test", time.Now().UnixNano())
	newAddress := fmt.Sprintf("move-to-%d@example.test", time.Now().UnixNano())
	accountID := createTestAccount(t, db, oldAddress)
	principalID := principalOf(t, db, oldAddress)
	defer forgetPrincipal(t, db, principalID)
	defer func() { _, _ = db.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, accountID) }()

	session := sessionFor(t, db, principalID, accountID)
	strandedSession := sessionFor(t, db, principalID, accountID)

	rec := serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email", session,
		[]byte(`{"email":"`+newAddress+`"}`)))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request status=%d body=%s", rec.Code, rec.Body.String())
	}
	challenge := parseChallengeID(rec.Body.Bytes())
	if challenge == "" {
		t.Fatalf("no challenge id in %s", rec.Body.String())
	}

	sentToNew, ok := mail.to(newAddress)
	if !ok {
		t.Fatalf("no code was sent to the new address")
	}
	if _, wrong := mail.to(oldAddress); wrong {
		t.Error("the code must not go to the address being replaced")
	}
	code := extractOTP(sentToNew.Text)
	if code == "" {
		t.Fatalf("no code in %s", sentToNew.Text)
	}

	if profile := readProfile(t, a, session); profile["email"] != oldAddress {
		t.Errorf("email changed before the code was entered: %v", profile["email"])
	}

	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email/verify", session,
		[]byte(`{"challenge_id":"`+challenge+`","email":"`+newAddress+`","code":"000000"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong code status=%d, want 401 (%s)", rec.Code, rec.Body.String())
	}

	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email/verify", session,
		[]byte(`{"challenge_id":"`+challenge+`","email":"`+newAddress+`","code":"`+code+`"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	if profile := readProfile(t, a, session); profile["email"] != newAddress {
		t.Errorf("email = %v, want %s", profile["email"], newAddress)
	}

	var subject string
	if err := db.QueryRow(context.Background(),
		`SELECT provider_subject FROM principal_identities WHERE principal_id=$1 AND deleted_at IS NULL`,
		principalID).Scan(&subject); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if subject != newAddress {
		t.Errorf("provider_subject = %q, want the new address so sign-in follows it", subject)
	}

	if _, ok = mail.to(oldAddress); !ok {
		t.Error("the replaced address was not told about the change")
	}

	stranded := serve(a, sessionRequest(t, http.MethodGet, "/api/dashboard/v1/me", strandedSession, nil))
	if stranded.Code != http.StatusUnauthorized {
		t.Errorf("other session status=%d, want 401 after an identity change", stranded.Code)
	}

	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email/verify", session,
		[]byte(`{"challenge_id":"`+challenge+`","email":"`+newAddress+`","code":"`+code+`"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("replayed code status=%d, want 401", rec.Code)
	}
}

func TestIntegrationEmailChangeRefusesAnAddressAlreadyInUse(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &mailbox{}
	a := newCoreIntegrationApp(t, db, mail)

	mine := fmt.Sprintf("taken-mine-%d@example.test", time.Now().UnixNano())
	theirs := fmt.Sprintf("taken-theirs-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, mine)
	defer deleteTestAccount(t, db, theirs)
	accountID := createTestAccount(t, db, mine)
	createTestAccount(t, db, theirs)

	session := sessionFor(t, db, principalOf(t, db, mine), accountID)

	rec := serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email", session,
		[]byte(`{"email":"`+theirs+`"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "email_taken") {
		t.Fatalf("status=%d body=%s, want an email_taken conflict", rec.Code, rec.Body.String())
	}
	if _, sent := mail.to(theirs); sent {
		t.Error("a code was mailed to an address that is already in use")
	}

	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email", session,
		[]byte(`{"email":"`+mine+`"}`)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "email_unchanged") {
		t.Errorf("status=%d body=%s, want an email_unchanged conflict", rec.Code, rec.Body.String())
	}
}

func TestIntegrationEmailChangeRejectsChallengesItDoesNotOwn(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &mailbox{})

	address := fmt.Sprintf("purpose-%d@example.test", time.Now().UnixNano())
	target := fmt.Sprintf("purpose-target-%d@example.test", time.Now().UnixNano())
	intruderEmail := fmt.Sprintf("purpose-intruder-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, address)
	defer deleteTestAccount(t, db, intruderEmail)
	accountID := createTestAccount(t, db, address)
	intruderAccount := createTestAccount(t, db, intruderEmail)

	principalID := principalOf(t, db, address)
	intruderID := principalOf(t, db, intruderEmail)
	session := sessionFor(t, db, principalID, accountID)
	intruderSession := sessionFor(t, db, intruderID, intruderAccount)
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM auth_challenges WHERE email IN ($1,$2)`, target, address)
	}()

	loginChallenge, loginCode := seedChallenge(t, db, a.cfg.OTPHMACKey, target, "login", nil)
	rec := serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email/verify", session,
		[]byte(`{"challenge_id":"`+loginChallenge+`","email":"`+target+`","code":"`+loginCode+`"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a login code changed an email address: status=%d body=%s", rec.Code, rec.Body.String())
	}

	changeChallenge, changeCode := seedChallenge(t, db, a.cfg.OTPHMACKey, target, "email_change", &principalID)
	rec = serve(a, sessionRequest(t, http.MethodPost, "/api/dashboard/v1/me/email/verify", intruderSession,
		[]byte(`{"challenge_id":"`+changeChallenge+`","email":"`+target+`","code":"`+changeCode+`"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("another principal spent someone else's challenge: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var stillMine string
	if err := db.QueryRow(context.Background(),
		`SELECT verified_email FROM principal_identities WHERE principal_id=$1 AND deleted_at IS NULL`,
		principalID).Scan(&stillMine); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if stillMine != address {
		t.Errorf("verified_email = %q, want it unchanged at %q", stillMine, address)
	}
}
