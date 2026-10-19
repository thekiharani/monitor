package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"log/slog"
	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/access"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/email"
)

type capturingMail struct {
	last email.Message
}

func (m *capturingMail) Send(_ context.Context, msg email.Message) error {
	m.last = msg
	return nil
}

func extractOTP(text string) string {
	re := regexp.MustCompile(`\b(\d{6})\b`)
	if m := re.FindStringSubmatch(text); len(m) == 2 {
		return m[1]
	}
	return ""
}

func newCoreIntegrationApp(t *testing.T, db *pgxpool.Pool, mail email.Sender) *App {
	cfg := integrationConfig("")
	cfg.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	a := &App{
		cfg:         cfg,
		db:          db,
		tenant:      openTenantIntegrationDB(t),
		mail:        mail,
		billing:     &fakeBilling{},
		log:         slog.Default(),
		limiters:    newLimiterSet(),
		metrics:     newAppMetrics(),
		alertWake:   make(chan struct{}, 1),
		statusPages: newStatusCache(),
		stop:        make(chan struct{}),
	}
	return a
}

func createTestAPIKey(t *testing.T, db *pgxpool.Pool, accountID string) string {
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		t.Fatalf("rand: %v", err)
	}
	raw := "usk_test_" + base64.RawURLEncoding.EncodeToString(rawBytes)
	id := uuid.NewV7()
	prefix := raw[:min(24, len(raw))]
	var principalID string
	if err := db.QueryRow(context.Background(), `SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`, accountID).Scan(&principalID); err != nil {
		t.Fatalf("find principal: %v", err)
	}
	_, err := db.Exec(context.Background(), `INSERT INTO credentials(id,principal_id,account_id,kind,audience,name,key_prefix,secret_hash,expires_at) VALUES($1,$2,$3,'api_key','customer_api','integration',$4,$5,now()+interval '1 year')`, id.String(), principalID, accountID, prefix, config.HashSecret(raw))
	if err != nil {
		t.Fatalf("insert api key: %v", err)
	}
	return raw
}

func parseChallengeID(body []byte) string {
	var out struct {
		ChallengeID string `json:"challenge_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return ""
	}
	return out.ChallengeID
}

func sessionCookies(rec *httptest.ResponseRecorder) []*http.Cookie {
	var session, csrf *http.Cookie
	for _, c := range rec.Result().Cookies() {
		switch c.Name {
		case "__Host-session":
			session = c
		case "__Host-csrf":
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		return nil
	}
	return []*http.Cookie{session, csrf}
}

func TestIntegrationOTPLogin(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &capturingMail{}
	a := newCoreIntegrationApp(t, db, mail)

	emailAddr := fmt.Sprintf("otp-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)

	reqBody := []byte(fmt.Sprintf(`{"email":"%s"}`, emailAddr))
	rec := httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/auth/otp/request", bytes.NewReader(reqBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.requestOTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request status=%d body=%s", rec.Code, rec.Body.String())
	}
	challengeID := parseChallengeID(rec.Body.Bytes())
	if challengeID == "" {
		t.Fatal("missing challenge_id")
	}
	otp := extractOTP(mail.last.Text)
	if len(otp) != 6 {
		t.Fatalf("otp from mail: %q", mail.last.Text)
	}

	verifyBody := []byte(fmt.Sprintf(`{"challenge_id":"%s","email":"%s","code":"%s"}`, challengeID, emailAddr, otp))
	rec = httptest.NewRecorder()
	req = testRequest(http.MethodPost, "/auth/otp/verify", bytes.NewReader(verifyBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.verifyOTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify status=%d body=%s", rec.Code, rec.Body.String())
	}
	if cookies := sessionCookies(rec); cookies == nil {
		t.Fatal("expected session and csrf cookies")
	}
}

func TestIntegrationOTPVerifyFailure(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &capturingMail{}
	a := newCoreIntegrationApp(t, db, mail)

	emailAddr := fmt.Sprintf("otp-bad-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)

	reqBody := []byte(fmt.Sprintf(`{"email":"%s"}`, emailAddr))
	rec := httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/auth/otp/request", bytes.NewReader(reqBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.requestOTP(rec, req)
	challengeID := parseChallengeID(rec.Body.Bytes())

	verifyBody := []byte(fmt.Sprintf(`{"challenge_id":"%s","email":"%s","code":"000000"}`, challengeID, emailAddr))
	rec = httptest.NewRecorder()
	req = testRequest(http.MethodPost, "/auth/otp/verify", bytes.NewReader(verifyBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.verifyOTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestIntegrationAPIKeyQuota(t *testing.T) {
	db := openIntegrationDB(t)
	mail := &capturingMail{}
	a := newCoreIntegrationApp(t, db, mail)

	emailAddr := fmt.Sprintf("quota-keys-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)

	reqBody := []byte(fmt.Sprintf(`{"email":"%s"}`, emailAddr))
	rec := httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/auth/otp/request", bytes.NewReader(reqBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.requestOTP(rec, req)
	challengeID := parseChallengeID(rec.Body.Bytes())
	otp := extractOTP(mail.last.Text)

	verifyBody := []byte(fmt.Sprintf(`{"challenge_id":"%s","email":"%s","code":"%s"}`, challengeID, emailAddr, otp))
	rec = httptest.NewRecorder()
	req = testRequest(http.MethodPost, "/auth/otp/verify", bytes.NewReader(verifyBody), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	a.verifyOTP(rec, req)
	cookies := sessionCookies(rec)
	csrf := cookies[1].Value

	for i := range 2 {
		rec = httptest.NewRecorder()
		body := []byte(`{"name":"key"}`)
		req = testRequest(http.MethodPost, "/api/dashboard/v1/api-keys", bytes.NewReader(body), a.cfg.AppOrigin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		a.createAPIKey(rec, req)
		if i == 0 && rec.Code != http.StatusCreated {
			t.Fatalf("first key status=%d body=%s", rec.Code, rec.Body.String())
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second key status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
}

func TestIntegrationSuspendedAccountCannotMutateDashboard(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	emailAddr := fmt.Sprintf("suspended-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET status='suspended' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	sessionToken := "suspended-session-token"
	var principalID string
	if err := db.QueryRow(context.Background(), `SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`, accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at) VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`, principalID, accountID, config.HashSecret(sessionToken)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/api/dashboard/v1/api-keys", bytes.NewReader([]byte(`{"name":"blocked"}`)), a.cfg.AppOrigin)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", "csrf-token")
	req.AddCookie(&http.Cookie{Name: "__Host-session", Value: sessionToken})
	req.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "csrf-token"})
	a.createAPIKey(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationASignedInVisitorIsNeverShownTheLoginPage(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})

	emailAddr := fmt.Sprintf("signed-in-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, emailAddr)
	accountID := createTestAccount(t, db, emailAddr)

	var principalID string
	if err := db.QueryRow(context.Background(),
		`SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`,
		accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	sessionToken := "already-signed-in-token"
	if _, err := db.Exec(context.Background(),
		`INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at)
		 VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`,
		principalID, accountID, config.HashSecret(sessionToken)); err != nil {
		t.Fatal(err)
	}

	signedIn := testRequest(http.MethodGet, "/login", nil, a.cfg.DashboardOrigin)
	signedIn.AddCookie(&http.Cookie{Name: "__Host-session", Value: sessionToken})
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, signedIn)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /login while signed in = %d, want 303", rec.Code)
	}
	if location := rec.Header().Get("Location"); location != "/app" {
		t.Errorf("redirected to %q, want /app", location)
	}
	if cache := rec.Header().Get("Cache-Control"); cache != "no-store" {
		t.Errorf("Cache-Control = %q; a cached redirect would strand somebody who signs out", cache)
	}

	expired := testRequest(http.MethodGet, "/login", nil, a.cfg.DashboardOrigin)
	expired.AddCookie(&http.Cookie{Name: "__Host-session", Value: "not-a-session"})
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, expired)
	if rec.Code == http.StatusSeeOther {
		t.Error("a stale cookie redirected away from the login page, so signing back in would be impossible")
	}
}

func TestIntegrationAnOperatorReachesTheControlPlaneWithTheSessionTheyAlreadyHave(t *testing.T) {
	db := openIntegrationDB(t)
	a := newCoreIntegrationApp(t, db, &capturingMail{})
	a.control = control.New(db, a.billing, a.cfg)

	emailAddr := fmt.Sprintf("operator-%d@example.test", time.Now().UnixNano())
	accountID := createTestAccount(t, db, emailAddr)

	var principalID string
	if err := db.QueryRow(context.Background(),
		`SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`,
		accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		deleteTestAccount(t, db, emailAddr)
	}()

	sessionToken := "operator-session-" + uuid.NewV7().String()
	var credentialID string
	if err := db.QueryRow(context.Background(),
		`INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at)
		 VALUES($1,$2,'session','customer_session',$3,now()+interval '14 days') RETURNING id::text`,
		principalID, accountID, config.HashSecret(sessionToken)).Scan(&credentialID); err != nil {
		t.Fatal(err)
	}

	withSession := func(origin, target string) *http.Request {
		r := testRequest(http.MethodGet, target, nil, origin)
		r.AddCookie(&http.Cookie{Name: "__Host-session", Value: sessionToken})
		return r
	}
	controlSessionRequest := func() *http.Request {
		return withSession(a.cfg.APIOrigin, "/api/control/v1/session")
	}
	serveLoginPage := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, withSession(a.cfg.DashboardOrigin, target))
		return rec
	}

	beforeAnyGrant := httptest.NewRecorder()
	a.controlSession(beforeAnyGrant, controlSessionRequest())
	if beforeAnyGrant.Code != http.StatusForbidden {
		t.Fatalf("a signed-in visitor without grants got %d, want 403 so the console can say so rather than ask them to sign in again", beforeAnyGrant.Code)
	}

	if _, err := db.Exec(context.Background(),
		`INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','integration operator grant')`,
		principalID, access.AccountsRead); err != nil {
		t.Fatal(err)
	}

	granted := httptest.NewRecorder()
	a.controlSession(granted, controlSessionRequest())
	if granted.Code != http.StatusOK {
		t.Fatalf("a granted operator got %d from the control session, want 200 with no second verification", granted.Code)
	}

	if rec := serveLoginPage("/login?next=/control"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/control" {
		t.Fatalf("GET /login?next=/control = %d to %q, want 303 to /control", rec.Code, rec.Header().Get("Location"))
	}

	if _, err := db.Exec(context.Background(),
		`UPDATE credentials SET created_at=now()-interval '13 hours' WHERE id=$1`, credentialID); err != nil {
		t.Fatal(err)
	}

	stale := httptest.NewRecorder()
	a.controlSession(stale, controlSessionRequest())
	if stale.Code != http.StatusUnauthorized {
		t.Fatalf("a session older than the control window got %d, want 401 so the console sends them to sign in", stale.Code)
	}
	if rec := serveLoginPage("/login?next=/control"); rec.Code == http.StatusSeeOther {
		t.Fatalf("a stale operator was redirected to %q instead of being offered the sign-in form, so the control plane is unreachable", rec.Header().Get("Location"))
	}
	if rec := serveLoginPage("/login"); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/app" {
		t.Fatalf("GET /login = %d to %q; the same session still works for the dashboard", rec.Code, rec.Header().Get("Location"))
	}
}
