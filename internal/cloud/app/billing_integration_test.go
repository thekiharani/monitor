package app

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"uuid"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/control"
	"github.com/thekiharani/monitor/internal/cloud/postgres"
)

func openTenantIntegrationDB(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("TEST_APP_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping tenant integration test")
	}
	cfg := config.Config{
		AppDatabaseURL:    url,
		DBMaxConns:        4,
		DBMinConns:        1,
		DBMaxConnLifetime: 30 * time.Minute,
		DBMaxConnIdleTime: 5 * time.Minute,
	}
	db, err := postgres.OpenTenant(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open tenant db: %v", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		db.Close()
		t.Fatalf("TEST_APP_DATABASE_URL is unavailable: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func openIntegrationDB(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping isolated integration test")
	}
	cfg := config.Config{
		DatabaseURL:       url,
		DBMaxConns:        2,
		DBMinConns:        1,
		DBMaxConnLifetime: 30 * time.Minute,
		DBMaxConnIdleTime: 5 * time.Minute,
	}
	db, err := postgres.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Ping(context.Background()); err != nil {
		db.Close()
		t.Fatalf("TEST_DATABASE_URL is unavailable: %v", err)
	}
	clearAuthThrottle(t, db)
	t.Cleanup(func() { db.Close() })
	return db
}

func clearAuthThrottle(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	if _, err := db.Exec(context.Background(),
		`DELETE FROM auth_challenges WHERE request_ip << inet '192.0.2.0/24' OR request_ip IS NULL`); err != nil {
		t.Fatalf("clear auth throttle: %v", err)
	}
}

func integrationConfig(baseURL string) config.Config {
	c := testConfig()
	c.DatabaseURL = os.Getenv("TEST_DATABASE_URL")
	c.AppOrigin = "http://localhost:8900"
	c.DashboardOrigin = "http://localhost:3900"
	c.APIOrigin = "http://localhost:8900"
	c.StatusOrigin = "http://status.local:8900"
	c.BillingGracePeriod = 72 * time.Hour
	return c
}

func newIntegrationApp(t *testing.T, db *pgxpool.Pool, fs *paystack.FakeServer) *App {
	cfg := integrationConfig(fs.URL())
	provider := paystack.NewWithBaseURL(cfg, fs.URL())
	a := &App{
		cfg:         cfg,
		db:          db,
		tenant:      openTenantIntegrationDB(t),
		mail:        fakeMail{},
		billing:     provider,
		log:         slog.New(slog.NewJSONHandler(io.Discard, nil)),
		limiters:    newLimiterSet(),
		metrics:     newAppMetrics(),
		emailWake:   make(chan struct{}, 1),
		statusPages: newStatusCache(),
		stop:        make(chan struct{}),
	}
	a.control = control.New(db, provider, cfg)
	return a
}

func grantOperator(t *testing.T, db *pgxpool.Pool, capabilities ...string) (string, string) {
	t.Helper()
	ctx := context.Background()
	principalID := uuid.NewV7().String()
	if _, err := db.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,'service',$2)`, principalID, "test-operator-"+principalID[:8]); err != nil {
		t.Fatalf("create operator principal: %v", err)
	}
	raw := "nmon_ctl_" + strings.Repeat("a", 8) + principalID[:8] + fmt.Sprintf("%d", time.Now().UnixNano())
	if _, err := db.Exec(ctx,
		`INSERT INTO credentials(principal_id,kind,audience,name,key_prefix,secret_hash,assurance,expires_at) VALUES($1,'api_key','control_api','test',$2,$3,'workload',now()+interval '1 hour')`,
		principalID, raw[:13], config.HashSecret(raw)); err != nil {
		t.Fatalf("create operator credential: %v", err)
	}
	for _, capability := range capabilities {
		if _, err := db.Exec(ctx,
			`INSERT INTO access_grants(principal_id,capability,scope_type,reason) VALUES($1,$2,'platform','integration test')`,
			principalID, capability); err != nil {
			t.Fatalf("grant %s: %v", capability, err)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM audit_events WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(context.Background(), `DELETE FROM access_grants WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(context.Background(), `DELETE FROM credentials WHERE principal_id=$1`, principalID)
		_, _ = db.Exec(context.Background(), `DELETE FROM principals WHERE id=$1`, principalID)
	})
	return principalID, raw
}

func newSession(t *testing.T, db *pgxpool.Pool, email string) string {
	t.Helper()
	ctx := context.Background()
	var principalID, accountID string
	if err := db.QueryRow(ctx,
		`SELECT i.principal_id::text,m.account_id::text FROM principal_identities i JOIN account_memberships m ON m.principal_id=i.principal_id WHERE i.verified_email=$1`,
		email).Scan(&principalID, &accountID); err != nil {
		t.Fatalf("look up principal: %v", err)
	}
	secret := "sess-" + uuid.NewV7().String()
	if _, err := db.Exec(ctx,
		`INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at) VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`,
		principalID, accountID, config.HashSecret(secret)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	return secret
}

func newAPIKey(t *testing.T, db *pgxpool.Pool, email string) string {
	t.Helper()
	ctx := context.Background()
	var principalID, accountID string
	if err := db.QueryRow(ctx,
		`SELECT i.principal_id::text,m.account_id::text FROM principal_identities i JOIN account_memberships m ON m.principal_id=i.principal_id WHERE i.verified_email=$1`,
		email).Scan(&principalID, &accountID); err != nil {
		t.Fatalf("look up principal: %v", err)
	}
	secret := "usk_test_" + uuid.NewV7().String()
	if _, err := db.Exec(ctx,
		`INSERT INTO credentials(principal_id,account_id,kind,audience,name,key_prefix,secret_hash,expires_at) VALUES($1,$2,'api_key','customer_api','test',$3,$4,now()+interval '1 hour')`,
		principalID, accountID, secret[:13], config.HashSecret(secret)); err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return secret
}

func createTestAccount(t *testing.T, db *pgxpool.Pool, email string) string {
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatalf("begin account: %v", err)
	}
	defer tx.Rollback(ctx)
	principalID, id := uuid.NewV7().String(), uuid.NewV7().String()
	if _, err = tx.Exec(ctx, `INSERT INTO principals(id,kind,display_name) VALUES($1,'human',$2)`, principalID, email); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO principal_identities(principal_id,provider,provider_subject,verified_email) VALUES($1,'email_otp',$2,$2)`, principalID, email)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO accounts(id,status,plan) VALUES($1,'active','free')`, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO account_memberships(principal_id,account_id,role) VALUES($1,$2,'owner')`, principalID, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	return id
}

func deleteTestAccount(t *testing.T, db *pgxpool.Pool, email string) {
	ctx := context.Background()
	var accountID, principalID string
	if err := db.QueryRow(ctx, `SELECT m.account_id::text,i.principal_id::text FROM principal_identities i JOIN account_memberships m ON m.principal_id=i.principal_id WHERE i.verified_email=$1`, email).Scan(&accountID, &principalID); err != nil {
		return
	}
	queries := []string{
		`DELETE FROM billing_events WHERE account_id=$1`,
		`DELETE FROM email_jobs WHERE account_id=$1`,
		`DELETE FROM billing_checkouts WHERE account_id=$1`,
		`DELETE FROM billing_subscriptions WHERE account_id=$1`,
		`DELETE FROM account_monthly_usage WHERE account_id=$1`,
		`DELETE FROM credentials WHERE account_id=$1`,
		`DELETE FROM status_post_updates WHERE account_id=$1`,
		`DELETE FROM status_posts WHERE account_id=$1`,
		`DELETE FROM status_page_items WHERE account_id=$1`,
		`DELETE FROM status_pages WHERE account_id=$1`,
		`DELETE FROM notifications WHERE account_id=$1`,
		`DELETE FROM monitor_channels WHERE account_id=$1`,
		`DELETE FROM alert_channels WHERE account_id=$1`,
		`DELETE FROM maintenance_windows WHERE account_id=$1`,
		`DELETE FROM incidents WHERE account_id=$1`,
		`DELETE FROM check_results WHERE account_id=$1`,
		`DELETE FROM monitor_daily WHERE account_id=$1`,
		`DELETE FROM monitor_schedules WHERE account_id=$1`,
		`DELETE FROM monitors WHERE account_id=$1`,
		`DELETE FROM account_invites WHERE account_id=$1`,
		`DELETE FROM auth_challenges WHERE email=$1`,
		`DELETE FROM account_memberships WHERE account_id=$1`,
		`DELETE FROM accounts WHERE id=$1`,
	}
	for _, query := range queries {
		arg := any(accountID)
		if strings.Contains(query, "email=$1") {
			arg = email
		}
		if _, err := db.Exec(ctx, query, arg); err != nil {
			t.Fatalf("cleanup account: %v", err)
		}
	}
	_, _ = db.Exec(ctx, `DELETE FROM principal_identities WHERE principal_id=$1`, principalID)
	_, _ = db.Exec(ctx, `DELETE FROM principals WHERE id=$1`, principalID)
}

func insertTestCheckout(t *testing.T, db *pgxpool.Pool, accountID, reference string, amount int64, currency string) {
	_, err := db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status) VALUES($1,'paystack',$2,'pro',$3,$4,'initialized')`, accountID, reference, amount, currency)
	if err != nil {
		t.Fatalf("insert checkout: %v", err)
	}
}

func insertTestSubscription(t *testing.T, db *pgxpool.Pool, accountID, subCode string) {
	_, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID)
	if err == nil {
		_, err = db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency) VALUES($1,'paystack',$2,'pro','active',50000,'KES')`, accountID, subCode)
	}
	if err != nil {
		t.Fatalf("insert subscription: %v", err)
	}
}

func accountPlan(t *testing.T, db *pgxpool.Pool, accountID string) string {
	var plan string
	if err := db.QueryRow(context.Background(), `SELECT plan FROM accounts WHERE id=$1`, accountID).Scan(&plan); err != nil {
		t.Fatalf("read plan: %v", err)
	}
	return plan
}

func checkoutStatus(t *testing.T, db *pgxpool.Pool, reference string) string {
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&status); err != nil {
		t.Fatalf("read checkout: %v", err)
	}
	return status
}

func subscriptionStatus(t *testing.T, db *pgxpool.Pool, subCode string) string {
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM billing_subscriptions WHERE provider_subscription_code=$1`, subCode).Scan(&status); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	return status
}

func subscriptionGrace(t *testing.T, db *pgxpool.Pool, subCode string) *time.Time {
	var grace *time.Time
	if err := db.QueryRow(context.Background(), `SELECT grace_until FROM billing_subscriptions WHERE provider_subscription_code=$1`, subCode).Scan(&grace); err != nil {
		t.Fatalf("read subscription grace: %v", err)
	}
	return grace
}

func countBillingEvents(t *testing.T, db *pgxpool.Pool, eventType, accountID string) int {
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM billing_events WHERE event_type=$1 AND account_id=$2`, eventType, accountID).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func postPaystackWebhook(t *testing.T, a *App, body []byte) int {
	rec := httptest.NewRecorder()
	req := testRequest(http.MethodPost, "/webhooks/paystack", bytes.NewReader(body), a.cfg.APIOrigin)
	req.Header.Set("x-paystack-signature", signPaystackWebhook(a.cfg.PaystackSecretKey, body))
	a.paystackWebhook(rec, req)
	return rec.Code
}

func validChargeWebhook(accountID, reference, planCode string) []byte {
	payload := map[string]any{
		"event": "charge.success",
		"data": map[string]any{
			"reference": reference,
			"status":    "success",
			"amount":    50000,
			"currency":  "KES",
			"id":        12345,
			"metadata": map[string]string{
				"account_id": accountID,
				"plan":       "pro",
			},
			"plan_object": map[string]string{"plan_code": planCode},
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func validManualChargeWebhook(accountID, reference string, amount int64) []byte {
	return validManualChargeWebhookID(accountID, reference, amount, 12346)
}

func validManualChargeWebhookID(accountID, reference string, amount, transactionID int64) []byte {
	payload := map[string]any{
		"event": "charge.success",
		"data": map[string]any{
			"reference": reference,
			"status":    "success",
			"amount":    amount,
			"currency":  "KES",
			"id":        transactionID,
			"metadata": map[string]string{
				"account_id": accountID,
				"plan":       "pro",
			},
		},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func TestIntegrationBillingCheckoutInsertsCheckoutWithMode(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-checkout-mode-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	sessionToken := "checkout-mode-session-" + uuid.NewV7().String()
	var principalID string
	if err := db.QueryRow(context.Background(), `SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`, accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at) VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`, principalID, accountID, config.HashSecret(sessionToken)); err != nil {
		t.Fatal(err)
	}
	authRequest := func(target string) *http.Request {
		req := testRequest(http.MethodPost, target, nil, a.cfg.AppOrigin)
		req.Header.Set("X-CSRF-Token", "csrf-token")
		req.AddCookie(&http.Cookie{Name: "__Host-session", Value: sessionToken})
		req.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "csrf-token"})
		return req
	}
	for _, tc := range []struct {
		method   string
		expected string
	}{
		{method: "card", expected: "auto"},
		{method: "mpesa", expected: "manual"},
	} {
		if _, err := db.Exec(context.Background(), `DELETE FROM billing_checkouts WHERE account_id=$1`, accountID); err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		a.billingCheckout(rec, authRequest("/api/dashboard/v1/billing/checkout?plan=pro&method="+tc.method))
		if rec.Code != http.StatusOK {
			t.Fatalf("method=%s status=%d body=%s", tc.method, rec.Code, rec.Body.String())
		}
		var mode string
		if err := db.QueryRow(context.Background(), `SELECT billing_mode FROM billing_checkouts WHERE account_id=$1 ORDER BY created_at DESC LIMIT 1`, accountID).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if mode != tc.expected {
			t.Fatalf("method=%s mode=%s", tc.method, mode)
		}
		channels, plan := fs.LastInitializeChannels()
		if tc.method == "mpesa" {
			if len(channels) != 1 || channels[0] != "mobile_money" {
				t.Fatalf("method=mpesa channels=%#v", channels)
			}
			if plan != "" {
				t.Fatalf("method=mpesa plan=%q", plan)
			}
		} else if len(channels) != 0 {
			t.Fatalf("method=card channels=%#v", channels)
		}
	}
}

func TestIntegrationManualMpesaSignupActivatesLocally(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	reference := fmt.Sprintf("bil-mpesa-%d", time.Now().UnixNano())
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode) VALUES($1,'paystack',$2,'pro',50000,'KES','initialized','subscription','manual')`, accountID, reference); err != nil {
		t.Fatal(err)
	}
	if code := postPaystackWebhook(t, a, validManualChargeWebhook(accountID, reference, 50000)); code != http.StatusOK {
		t.Fatalf("webhook status=%d", code)
	}
	if checkoutStatus(t, db, reference) != "successful" {
		t.Fatal("expected successful checkout")
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatalf("plan=%s", accountPlan(t, db, accountID))
	}
	var mode, status string
	var periodEnd *time.Time
	if err := db.QueryRow(context.Background(), `SELECT billing_mode,status,current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, manualSubscriptionCode(reference)).Scan(&mode, &status, &periodEnd); err != nil {
		t.Fatal(err)
	}
	if mode != "manual" || status != "active" || periodEnd == nil {
		t.Fatalf("mode=%s status=%s period_end=%v", mode, status, periodEnd)
	}
}

func TestIntegrationManualMpesaRenewalExtendsPeriod(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-renew-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("MANUAL-bil-mpesa-ren-%d", time.Now().UnixNano())
	start := time.Now().UTC().Add(-25 * 24 * time.Hour)
	initialEnd := time.Now().UTC().Add(24 * time.Hour)
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack',$2,'pro','active',50000,'KES',$3,$4,'manual')`, accountID, code, start, initialEnd); err != nil {
		t.Fatal(err)
	}
	reference := fmt.Sprintf("bil-mpesa-ren-%d", time.Now().UnixNano())
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode) VALUES($1,'paystack',$2,'pro',50000,'KES','initialized','renewal','manual')`, accountID, reference); err != nil {
		t.Fatal(err)
	}
	if code := postPaystackWebhook(t, a, validManualChargeWebhook(accountID, reference, 50000)); code != http.StatusOK {
		t.Fatalf("webhook status=%d", code)
	}
	var status string
	var periodEnd *time.Time
	if err := db.QueryRow(context.Background(), `SELECT status,current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&status, &periodEnd); err != nil {
		t.Fatal(err)
	}
	if status != "active" || periodEnd == nil || !periodEnd.After(initialEnd.Add(20*24*time.Hour)) {
		t.Fatalf("status=%s period_end=%v", status, periodEnd)
	}
	firstRenewedEnd := *periodEnd
	if webhookStatus := postPaystackWebhook(t, a, validManualChargeWebhookID(accountID, reference, 50000, 12347)); webhookStatus != http.StatusOK {
		t.Fatalf("duplicate webhook status=%d", webhookStatus)
	}
	if err := db.QueryRow(context.Background(), `SELECT current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&periodEnd); err != nil {
		t.Fatal(err)
	}
	if periodEnd == nil || !periodEnd.Equal(firstRenewedEnd) {
		t.Fatalf("duplicate webhook extended renewal again: first=%v duplicate=%v", firstRenewedEnd, periodEnd)
	}
}

func TestIntegrationManualUpgradeFromAutoSourceRetiresProviderSubscription(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-ausrc-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	token, err := config.Seal(a.cfg.AppEncryptionKey, "card-token")
	if err != nil {
		t.Fatal(err)
	}
	sourceCode := fmt.Sprintf("SUB_auto_source_%d", time.Now().UnixNano())
	now := time.Now().UTC()
	periodStart, periodEnd := now.Add(-7*24*time.Hour), now.Add(21*24*time.Hour)
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack','CUS_auto',$2,$3,'starter','active',10000,'KES',$4,$5,'auto')`, accountID, sourceCode, token, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	renewalAt := nextPaystackMonthlyRenewal(now)
	reference := fmt.Sprintf("bil-mpesa-ausrc-%d", time.Now().UnixNano())
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,source_subscription_code,renewal_at) VALUES($1,'paystack',$2,'pro',45000,'KES','initialized','upgrade','manual',50000,5000,$3,$4)`, accountID, reference, sourceCode, renewalAt); err != nil {
		t.Fatal(err)
	}
	if webhookStatus := postPaystackWebhook(t, a, validManualChargeWebhook(accountID, reference, 45000)); webhookStatus != http.StatusOK {
		t.Fatalf("webhook status=%d", webhookStatus)
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatalf("plan=%s", accountPlan(t, db, accountID))
	}
	if status := subscriptionStatus(t, db, sourceCode); status != "non_renewing" {
		t.Fatalf("source status=%s", status)
	}
	var manualCode, plan, mode string
	var amount int64
	if err := db.QueryRow(context.Background(), `SELECT provider_subscription_code,plan,billing_mode,amount_minor FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='manual'`, accountID).Scan(&manualCode, &plan, &mode, &amount); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manualCode, "MANUAL-") || plan != "pro" || amount != 50000 {
		t.Fatalf("code=%s plan=%s amount=%d", manualCode, plan, amount)
	}
}

func TestIntegrationManualUpgradeProvisionsLocally(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-upgrade-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	sourceCode := fmt.Sprintf("MANUAL-bil-mpesa-src-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(time.Second)
	periodStart, periodEnd := now.Add(-7*24*time.Hour), now.Add(21*24*time.Hour)
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack',$2,'starter','active',10000,'KES',$3,$4,'manual')`, accountID, sourceCode, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	renewalAt := nextPaystackMonthlyRenewal(now)
	reference := fmt.Sprintf("bil-mpesa-upgrade-%d", time.Now().UnixNano())
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,source_subscription_code,renewal_at) VALUES($1,'paystack',$2,'pro',45000,'KES','initialized','upgrade','manual',50000,5000,$3,$4)`, accountID, reference, sourceCode, renewalAt); err != nil {
		t.Fatal(err)
	}
	fs.Verify[reference] = paystack.Transaction{ID: 12348, Status: "success", Reference: reference, Amount: 45000, Currency: "KES", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	checkoutType, err := a.verifyBillingCheckout(context.Background(), reference, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if checkoutType != "upgrade" {
		t.Fatalf("checkout_type=%s", checkoutType)
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatalf("plan=%s", accountPlan(t, db, accountID))
	}
	var plan, status string
	var amount int64
	var upgradedEnd *time.Time
	if err := db.QueryRow(context.Background(), `SELECT plan,status,amount_minor,current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, sourceCode).Scan(&plan, &status, &amount, &upgradedEnd); err != nil {
		t.Fatal(err)
	}
	if plan != "pro" || status != "active" || amount != 50000 || upgradedEnd == nil || !upgradedEnd.Equal(renewalAt) {
		t.Fatalf("plan=%s status=%s amount=%d end=%v", plan, status, amount, upgradedEnd)
	}
}

func TestIntegrationManualRenewalReminderEmailsPayLink(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-remind-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	code := fmt.Sprintf("MANUAL-bil-mpesa-remind-%d", time.Now().UnixNano())
	start := time.Now().UTC().Add(-27 * 24 * time.Hour)
	end := time.Now().UTC().Add(2 * 24 * time.Hour)
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack',$2,'pro','active',50000,'KES',$3,$4,'manual')`, accountID, code, start, end); err != nil {
		t.Fatal(err)
	}
	a.remindManualRenewals(context.Background())
	var reminded *time.Time
	if err := db.QueryRow(context.Background(), `SELECT renewal_reminded_at FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&reminded); err != nil || reminded == nil {
		t.Fatalf("renewal_reminded_at=%v err=%v", reminded, err)
	}
	var template string
	var payload []byte
	if err := db.QueryRow(context.Background(), `SELECT template,payload FROM email_jobs WHERE account_id=$1 AND template='billing_renewal' ORDER BY created_at DESC LIMIT 1`, accountID).Scan(&template, &payload); err != nil {
		t.Fatalf("email job missing: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	link, _ := decoded["pay_link"].(string)
	if !strings.Contains(link, "/app/billing?renew=pro") {
		t.Fatalf("pay_link=%s", link)
	}
}

func TestIntegrationManualExpiryMovesToGraceAndCanceled(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-grace-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	code := fmt.Sprintf("MANUAL-bil-mpesa-grace-%d", time.Now().UnixNano())
	start := time.Now().UTC().Add(-40 * 24 * time.Hour)
	end := time.Now().UTC().Add(-24 * time.Hour)
	if _, err := db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_subscription_code,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode,grace_until) VALUES($1,'paystack',$2,'pro','past_due',50000,'KES',$3,$4,'manual',now()-interval '1 hour')`, accountID, code, start, end); err != nil {
		t.Fatal(err)
	}
	a.expireBillingGrace(context.Background())
	if accountPlan(t, db, accountID) != "free" {
		t.Fatalf("plan=%s", accountPlan(t, db, accountID))
	}
	if status := subscriptionStatus(t, db, code); status != "canceled" {
		t.Fatalf("status=%s", status)
	}
}

func TestIntegrationBillingCallbackSuccess(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-callback-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := "bil-callback-test"
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	fs.Verify[reference] = paystack.Transaction{
		ID:        999,
		Status:    "success",
		Reference: reference,
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": accountID, "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: a.cfg.PaystackPlanCode},
	}

	rec := httptest.NewRecorder()
	req := testRequest(http.MethodGet, "/billing/paystack/callback?reference="+reference, nil, a.cfg.AppOrigin)
	a.billingCallback(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d", rec.Code)
	}
	if !bytes.Contains([]byte(rec.Header().Get("Location")), []byte("payment=success")) {
		t.Fatalf("location=%s", rec.Header().Get("Location"))
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatal("expected pro plan")
	}
	if checkoutStatus(t, db, reference) != "successful" {
		t.Fatal("expected successful checkout")
	}
}

func TestIntegrationProratedUpgradeProvisionsFreshSubscription(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-upgrade-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	token, err := config.Seal(a.cfg.AppEncryptionKey, "old-token")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	periodStart, periodEnd := now.Add(-7*24*time.Hour), now.Add(21*24*time.Hour)
	sourceCode := fmt.Sprintf("SUB_upgrade_source_%d", time.Now().UnixNano())
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end) VALUES($1,'paystack','CUS_upgrade',$2,$3,'starter','active',10000,'KES',$4,$5)`, accountID, sourceCode, token, periodStart, periodEnd); err != nil {
		t.Fatal(err)
	}
	charge, credit, err := proratedUpgradeCharge(10000, 50000, periodStart, periodEnd, now)
	if err != nil {
		t.Fatal(err)
	}
	renewalAt := nextPaystackMonthlyRenewal(now)
	reference := fmt.Sprintf("bil-upgrade-%d", time.Now().UnixNano())
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,plan_amount_minor,credit_minor,source_subscription_code,renewal_at) VALUES($1,'paystack',$2,'pro',$3,'KES','initialized','upgrade',50000,$4,$5,$6)`, accountID, reference, charge, credit, sourceCode, renewalAt); err != nil {
		t.Fatal(err)
	}
	transaction := paystack.Transaction{ID: 9876, Status: "success", Reference: reference, Amount: charge, Currency: "KES", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	transaction.Customer.CustomerCode = "CUS_upgrade"
	fs.Verify[reference] = transaction

	checkoutType, err := a.verifyBillingCheckout(context.Background(), reference, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if checkoutType != "upgrade" || accountPlan(t, db, accountID) != "pro" {
		t.Fatalf("checkout_type=%s plan=%s", checkoutType, accountPlan(t, db, accountID))
	}
	if status := subscriptionStatus(t, db, sourceCode); status != "non_renewing" {
		t.Fatalf("source status=%s", status)
	}
	var targetPlan, targetStatus string
	var targetAmount int64
	var targetPeriodEnd time.Time
	if err := db.QueryRow(context.Background(), `SELECT plan,status,amount_minor,current_period_end FROM billing_subscriptions WHERE provider_subscription_code='SUB_created' AND account_id=$1`, accountID).Scan(&targetPlan, &targetStatus, &targetAmount, &targetPeriodEnd); err != nil {
		t.Fatal(err)
	}
	if targetPlan != "pro" || targetStatus != "active" || targetAmount != 50000 || !targetPeriodEnd.Equal(renewalAt) {
		t.Fatalf("target=%s/%s amount=%d renewal=%s", targetPlan, targetStatus, targetAmount, targetPeriodEnd)
	}
	var provisioned bool
	if err := db.QueryRow(context.Background(), `SELECT provisioned_at IS NOT NULL FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&provisioned); err != nil || !provisioned {
		t.Fatalf("provisioned=%v err=%v", provisioned, err)
	}
	customer, planCode, startDate := fs.CreatedSubscription()
	if customer != "CUS_upgrade" || planCode != a.cfg.PaystackPlanCode || startDate != renewalAt.Format(time.RFC3339) {
		t.Fatalf("created customer=%s plan=%s start=%s", customer, planCode, startDate)
	}
}

func TestIntegrationRecoverSubscriptionFromSuccessfulCheckout(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-recovery-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	reference := "bil-recovery-test"
	_, err := db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,provider_transaction_id,plan,amount_minor,currency,status,completed_at) VALUES($1,'paystack',$2,'123','pro',50000,'KES','successful',now())`, accountID, reference)
	if err != nil {
		t.Fatal(err)
	}
	transaction := paystack.Transaction{
		Status:    "success",
		Reference: reference,
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": accountID, "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: a.cfg.PaystackPlanCode},
	}
	transaction.Customer.ID = 42
	transaction.Customer.CustomerCode = "CUS_recovery"
	fs.Verify[reference] = transaction
	older := paystack.Subscription{SubscriptionCode: "SUB_recovery_old", EmailToken: "old-token", Amount: 50000, Currency: "KES", CreatedAt: "2026-08-30T13:41:05Z", Start: 1788097265, NextPaymentDate: "2026-09-28T13:41:00Z", Status: "non-renewing"}
	newer := paystack.Subscription{SubscriptionCode: "SUB_recovery", EmailToken: "token", Amount: 50000, Currency: "KES", CreatedAt: "2026-08-30T16:14:53Z", Start: 1788106493, NextPaymentDate: "2026-09-28T16:14:00Z", Status: "active"}
	for _, subscription := range []*paystack.Subscription{&older, &newer} {
		subscription.Customer.ID = transaction.Customer.ID
		subscription.Customer.CustomerCode = transaction.Customer.CustomerCode
		subscription.Plan.Code = a.cfg.PaystackPlanCode
		subscription.Plan.Amount = 50000
		subscription.Plan.Currency = "KES"
		fs.Subscriptions[subscription.SubscriptionCode] = *subscription
	}

	if err = a.recoverSubscription(context.Background(), accountID); err != nil {
		t.Fatal(err)
	}
	if status := subscriptionStatus(t, db, newer.SubscriptionCode); status != "active" {
		t.Fatalf("status=%q", status)
	}
	var recoveredCode string
	var recoveredStart *time.Time
	if err = db.QueryRow(context.Background(), `SELECT provider_subscription_code,current_period_start FROM billing_subscriptions WHERE account_id=$1`, accountID).Scan(&recoveredCode, &recoveredStart); err != nil {
		t.Fatal(err)
	}
	if recoveredCode != newer.SubscriptionCode || recoveredStart == nil || recoveredStart.Unix() != newer.Start {
		t.Fatalf("recovered code=%q start=%v", recoveredCode, recoveredStart)
	}
}

func TestIntegrationBillingCallbackWrongCurrency(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-wrong-curr-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := "bil-wrong-currency"
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	fs.Verify[reference] = paystack.Transaction{
		Status:    "success",
		Reference: reference,
		Amount:    50000,
		Currency:  "USD",
		Metadata:  map[string]any{"account_id": accountID, "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: a.cfg.PaystackPlanCode},
	}

	rec := httptest.NewRecorder()
	req := testRequest(http.MethodGet, "/billing/paystack/callback?reference="+reference, nil, a.cfg.AppOrigin)
	a.billingCallback(rec, req)

	if !bytes.Contains([]byte(rec.Header().Get("Location")), []byte("payment=failed")) {
		t.Fatalf("location=%s", rec.Header().Get("Location"))
	}
	if accountPlan(t, db, accountID) != "free" {
		t.Fatal("plan should remain free")
	}
}

func TestIntegrationWebhookChargeSuccess(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-webhook-charge-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := "bil-webhook-charge"
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")

	body := validChargeWebhook(accountID, reference, a.cfg.PaystackPlanCode)
	if status := postPaystackWebhook(t, a, body); status != http.StatusOK {
		t.Fatalf("webhook status=%d", status)
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatal("expected pro")
	}
}

func TestIntegrationWebhookDuplicateIsIdempotent(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-dup-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := "bil-dup"
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	body := validChargeWebhook(accountID, reference, a.cfg.PaystackPlanCode)

	if postPaystackWebhook(t, a, body) != http.StatusOK {
		t.Fatal("first webhook failed")
	}
	if postPaystackWebhook(t, a, body) != http.StatusOK {
		t.Fatal("duplicate webhook failed")
	}
	if countBillingEvents(t, db, "charge.success", accountID) != 1 {
		t.Fatal("expected single billing event row")
	}
}

func TestIntegrationWebhookInvoicePaymentFailed(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-failed-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	subCode := "SUB_failed_test"
	insertTestSubscription(t, db, accountID, subCode)

	body, _ := json.Marshal(map[string]any{
		"event": "invoice.payment_failed",
		"data": map[string]any{
			"subscription": map[string]string{"subscription_code": subCode},
			"transaction":  map[string]string{"reference": "trx-fail", "status": "failed"},
		},
	})
	if postPaystackWebhook(t, a, body) != http.StatusOK {
		t.Fatal("webhook failed")
	}
	if subscriptionStatus(t, db, subCode) != "past_due" {
		t.Fatalf("status=%s", subscriptionStatus(t, db, subCode))
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatal("plan should stay pro during grace")
	}
}

func TestIntegrationWebhookInvoiceUpdateRestoresActive(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-restore-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	subCode := "SUB_restore"
	insertTestSubscription(t, db, accountID, subCode)
	_, err := db.Exec(context.Background(), `UPDATE billing_subscriptions SET status='past_due',grace_until=now()+interval '1 day' WHERE provider_subscription_code=$1`, subCode)
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"event": "invoice.update",
		"data": map[string]any{
			"paid":         true,
			"subscription": map[string]string{"subscription_code": subCode},
			"transaction": map[string]any{
				"reference": "trx-paid",
				"status":    "success",
				"metadata":  map[string]string{"account_id": accountID, "plan": "pro"},
			},
		},
	})
	if postPaystackWebhook(t, a, body) != http.StatusOK {
		t.Fatal("webhook failed")
	}
	if subscriptionStatus(t, db, subCode) != "active" {
		t.Fatal("expected active subscription")
	}
}

func TestIntegrationReconcilePendingCheckout(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-reconcile-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := "bil-reconcile"
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	fs.Verify[reference] = paystack.Transaction{
		ID:        4242,
		Status:    "success",
		Reference: reference,
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": accountID, "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: a.cfg.PaystackPlanCode},
	}

	a.reconcilePendingCheckouts(context.Background())
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatal("reconcile should activate plan")
	}
	if checkoutStatus(t, db, reference) != "successful" {
		t.Fatal("checkout should be successful")
	}
}

func TestIntegrationReconcileAttentionStartsGrace(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-attention-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	subCode := fmt.Sprintf("SUB_attention_%d", time.Now().UnixNano())
	insertTestSubscription(t, db, accountID, subCode)
	sub := paystack.Subscription{
		SubscriptionCode: subCode,
		Status:           "attention",
		Amount:           50000,
		Currency:         "KES",
		NextPaymentDate:  "2026-09-29T00:00:00Z",
	}
	sub.Plan.Code = a.cfg.PaystackPlanCode
	sub.Plan.Amount = 50000
	sub.Plan.Currency = "KES"
	fs.Subscriptions[subCode] = sub

	a.reconcileSubscriptions(context.Background())
	if subscriptionStatus(t, db, subCode) != "past_due" {
		t.Fatal("attention must reconcile to past_due")
	}
	if subscriptionGrace(t, db, subCode) == nil {
		t.Fatal("attention must start a billing grace period")
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatal("account must remain pro during billing grace")
	}
}

func TestIntegrationWebhookSubscriptionDisable(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-disable-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	_, err := db.Exec(context.Background(), `UPDATE accounts SET plan='pro' WHERE id=$1`, accountID)
	if err != nil {
		t.Fatal(err)
	}
	subCode := "SUB_disable"
	insertTestSubscription(t, db, accountID, subCode)

	body, _ := json.Marshal(map[string]any{
		"event": "subscription.disable",
		"data": map[string]any{
			"subscription_code": subCode,
			"status":            "complete",
		},
	})
	if postPaystackWebhook(t, a, body) != http.StatusOK {
		t.Fatal("webhook failed")
	}
	if accountPlan(t, db, accountID) != "free" {
		t.Fatal("expected free plan after disable")
	}
	if subscriptionStatus(t, db, subCode) != "complete" {
		t.Fatal("expected complete subscription")
	}
}

func TestIntegrationWebhookSubscriptionCreateLinksByCustomerEmail(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-sub-create-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)

	accountID := createTestAccount(t, db, email)
	reference := fmt.Sprintf("bil-sub-create-%d", time.Now().UnixNano())
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	if _, err := db.Exec(context.Background(), `UPDATE billing_checkouts SET status='successful',completed_at=now() WHERE provider_reference=$1`, reference); err != nil {
		t.Fatal(err)
	}

	subCode := fmt.Sprintf("SUB_create_%d", time.Now().UnixNano())
	body, _ := json.Marshal(map[string]any{
		"event": "subscription.create",
		"data": map[string]any{
			"subscription_code": subCode,
			"email_token":       "provider-secret-token",
			"status":            "active",
			"amount":            50000,
			"currency":          "KES",
			"next_payment_date": "2026-09-29T00:00:00Z",
			"customer": map[string]string{
				"customer_code": "CUS_create",
				"email":         email,
			},
			"plan": map[string]any{
				"plan_code": a.cfg.PaystackPlanCode,
				"amount":    50000,
				"currency":  "KES",
			},
		},
	})
	if status := postPaystackWebhook(t, a, body); status != http.StatusOK {
		t.Fatalf("webhook status=%d", status)
	}
	if subscriptionStatus(t, db, subCode) != "active" || accountPlan(t, db, accountID) != "pro" {
		t.Fatal("subscription create did not activate the linked account")
	}
	var encrypted string
	var storedPayload []byte
	if err := db.QueryRow(context.Background(), `SELECT provider_email_token_encrypted FROM billing_subscriptions WHERE provider_subscription_code=$1`, subCode).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if encrypted == "provider-secret-token" || !strings.HasPrefix(encrypted, "v1:") {
		t.Fatalf("subscription token was not encrypted: %q", encrypted)
	}
	if err := db.QueryRow(context.Background(), `SELECT payload::text FROM billing_events WHERE event_type='subscription.create' AND account_id=$1`, accountID).Scan(&storedPayload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(storedPayload, []byte("provider-secret-token")) || !bytes.Contains(storedPayload, []byte("[redacted]")) {
		t.Fatalf("stored webhook payload was not sanitized: %s", storedPayload)
	}
}

func TestIntegrationWebhookSubscriptionCreateRejectsWrongPlan(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-sub-plan-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	reference := fmt.Sprintf("bil-sub-plan-%d", time.Now().UnixNano())
	insertTestCheckout(t, db, accountID, reference, 50000, "KES")
	if _, err := db.Exec(context.Background(), `UPDATE billing_checkouts SET status='successful',completed_at=now() WHERE provider_reference=$1`, reference); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{
		"event": "subscription.create",
		"data": map[string]any{
			"subscription_code": "SUB_wrong_plan",
			"email_token":       "provider-secret-token",
			"amount":            50000,
			"currency":          "KES",
			"customer":          map[string]string{"email": email},
			"plan":              map[string]any{"plan_code": "PLN_other", "amount": 50000, "currency": "KES"},
		},
	})
	if status := postPaystackWebhook(t, a, body); status != http.StatusServiceUnavailable {
		t.Fatalf("webhook status=%d", status)
	}
	if accountPlan(t, db, accountID) != "free" {
		t.Fatal("invalid subscription must not activate pro")
	}
	var status string
	if err := db.QueryRow(context.Background(), `SELECT status FROM billing_events WHERE event_type='subscription.create' ORDER BY received_at DESC LIMIT 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("event status=%s", status)
	}
}

func TestIntegrationManualUpgradeIsProvisionedOnce(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-once-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	token, err := config.Seal(a.cfg.AppEncryptionKey, "card-token")
	if err != nil {
		t.Fatal(err)
	}
	sourceCode := fmt.Sprintf("SUB_once_%d", time.Now().UnixNano())
	now := time.Now().UTC()
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack','CUS_once',$2,$3,'starter','active',10000,'KES',$4,$5,'auto')`,
		accountID, sourceCode, token, now.Add(-7*24*time.Hour), now.Add(21*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reference := fmt.Sprintf("bil-mpesa-once-%d", time.Now().UnixNano())
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,source_subscription_code,renewal_at) VALUES($1,'paystack',$2,'pro',45000,'KES','initialized','upgrade','manual',50000,5000,$3,$4)`,
		accountID, reference, sourceCode, nextPaystackMonthlyRenewal(now)); err != nil {
		t.Fatal(err)
	}

	if code := postPaystackWebhook(t, a, validManualChargeWebhook(accountID, reference, 45000)); code != http.StatusOK {
		t.Fatalf("first webhook status=%d", code)
	}

	var provisioned *time.Time
	if err := db.QueryRow(context.Background(), `SELECT provisioned_at FROM billing_checkouts WHERE provider_reference=$1`, reference).Scan(&provisioned); err != nil {
		t.Fatal(err)
	}
	if provisioned == nil {
		t.Fatal("provisioned_at was not stamped, so the reconciliation sweep will re-provision this upgrade")
	}

	for range 3 {
		if err := a.completeUpgradeCheckout(context.Background(), reference, "CUS_once"); err != nil {
			t.Fatalf("replay returned an error: %v", err)
		}
	}

	var manualCount int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='manual'`, accountID).Scan(&manualCount); err != nil {
		t.Fatal(err)
	}
	if manualCount != 1 {
		t.Fatalf("manual subscription rows=%d after replay, want 1", manualCount)
	}
	if accountPlan(t, db, accountID) != "pro" {
		t.Fatalf("plan=%s, want pro", accountPlan(t, db, accountID))
	}
}

func TestIntegrationReconcileUpgradeCheckoutsIsQuietAfterProvisioning(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-mpesa-sweep-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	if _, err := db.Exec(context.Background(), `UPDATE accounts SET plan='starter' WHERE id=$1`, accountID); err != nil {
		t.Fatal(err)
	}
	token, err := config.Seal(a.cfg.AppEncryptionKey, "card-token")
	if err != nil {
		t.Fatal(err)
	}
	sourceCode := fmt.Sprintf("SUB_sweep_%d", time.Now().UnixNano())
	now := time.Now().UTC()
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_subscriptions(account_id,provider,provider_customer_code,provider_subscription_code,provider_email_token_encrypted,plan,status,amount_minor,currency,current_period_start,current_period_end,billing_mode) VALUES($1,'paystack','CUS_sweep',$2,$3,'starter','active',10000,'KES',$4,$5,'auto')`,
		accountID, sourceCode, token, now.Add(-7*24*time.Hour), now.Add(21*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	reference := fmt.Sprintf("bil-mpesa-sweep-%d", time.Now().UnixNano())
	if _, err = db.Exec(context.Background(), `INSERT INTO billing_checkouts(account_id,provider,provider_reference,plan,amount_minor,currency,status,checkout_type,billing_mode,plan_amount_minor,credit_minor,source_subscription_code,renewal_at) VALUES($1,'paystack',$2,'pro',45000,'KES','initialized','upgrade','manual',50000,5000,$3,$4)`,
		accountID, reference, sourceCode, nextPaystackMonthlyRenewal(now)); err != nil {
		t.Fatal(err)
	}
	if code := postPaystackWebhook(t, a, validManualChargeWebhook(accountID, reference, 45000)); code != http.StatusOK {
		t.Fatalf("webhook status=%d", code)
	}

	var pending int
	if err := db.QueryRow(context.Background(),
		`SELECT count(*) FROM billing_checkouts WHERE account_id=$1 AND checkout_type='upgrade' AND status='successful' AND provisioned_at IS NULL`,
		accountID).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d upgrade checkouts still look unprovisioned to the sweep", pending)
	}

	a.reconcileUpgradeCheckouts(context.Background())

	var manualCount int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM billing_subscriptions WHERE account_id=$1 AND billing_mode='manual'`, accountID).Scan(&manualCount); err != nil {
		t.Fatal(err)
	}
	if manualCount != 1 {
		t.Fatalf("manual subscription rows=%d after a reconciliation pass, want 1", manualCount)
	}
}

func checkoutFixture(t *testing.T, db *pgxpool.Pool, a *App, accountID string) func(query string) *httptest.ResponseRecorder {
	t.Helper()
	sessionToken := "checkout-session-" + uuid.NewV7().String()
	var principalID string
	if err := db.QueryRow(context.Background(), `SELECT principal_id::text FROM account_memberships WHERE account_id=$1 AND role='owner'`, accountID).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(context.Background(), `INSERT INTO credentials(principal_id,account_id,kind,audience,secret_hash,expires_at) VALUES($1,$2,'session','customer_session',$3,now()+interval '1 hour')`, principalID, accountID, config.HashSecret(sessionToken)); err != nil {
		t.Fatal(err)
	}
	return func(query string) *httptest.ResponseRecorder {
		req := testRequest(http.MethodPost, "/api/dashboard/v1/billing/checkout?"+query, nil, a.cfg.AppOrigin)
		req.Header.Set("X-CSRF-Token", "csrf-token")
		req.AddCookie(&http.Cookie{Name: "__Host-session", Value: sessionToken})
		req.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "csrf-token"})
		rec := httptest.NewRecorder()
		a.billingCheckout(rec, req)
		return rec
	}
}

func latestCheckout(t *testing.T, db *pgxpool.Pool, accountID string) (reference, plan, mode, status string) {
	t.Helper()
	if err := db.QueryRow(context.Background(),
		`SELECT provider_reference,plan,COALESCE(billing_mode,'auto'),status FROM billing_checkouts WHERE account_id=$1 ORDER BY created_at DESC LIMIT 1`,
		accountID).Scan(&reference, &plan, &mode, &status); err != nil {
		t.Fatalf("read latest checkout: %v", err)
	}
	return
}

func TestIntegrationAbandonedCheckoutDoesNotBlockADifferentPlan(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-abandon-plan-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	checkout := checkoutFixture(t, db, a, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("first checkout status=%d body=%s", rec.Code, rec.Body)
	}
	abandoned, _, _, _ := latestCheckout(t, db, accountID)

	fs.Verify[abandoned] = paystack.Transaction{Status: "abandoned", Reference: abandoned}

	rec := checkout("plan=starter&method=card")
	if rec.Code != http.StatusOK {
		t.Fatalf("second checkout status=%d body=%s", rec.Code, rec.Body)
	}

	if got := checkoutStatus(t, db, abandoned); got != "expired" {
		t.Fatalf("abandoned checkout status=%s, want expired", got)
	}
	reference, plan, _, status := latestCheckout(t, db, accountID)
	if reference == abandoned {
		t.Fatal("no new checkout was created")
	}
	if plan != "starter" || status != "initialized" {
		t.Fatalf("new checkout plan=%s status=%s", plan, status)
	}
}

func TestIntegrationAbandonedCheckoutDoesNotBlockADifferentMethod(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-abandon-method-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	checkout := checkoutFixture(t, db, a, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("card checkout status=%d body=%s", rec.Code, rec.Body)
	}
	abandoned, _, _, _ := latestCheckout(t, db, accountID)
	fs.Verify[abandoned] = paystack.Transaction{Status: "abandoned", Reference: abandoned}

	if rec := checkout("plan=pro&method=mpesa"); rec.Code != http.StatusOK {
		t.Fatalf("mpesa checkout status=%d body=%s", rec.Code, rec.Body)
	}

	if got := checkoutStatus(t, db, abandoned); got != "expired" {
		t.Fatalf("abandoned checkout status=%s, want expired", got)
	}
	_, plan, mode, _ := latestCheckout(t, db, accountID)
	if plan != "pro" || mode != "manual" {
		t.Fatalf("new checkout plan=%s mode=%s, want pro/manual", plan, mode)
	}
	channels, _ := fs.LastInitializeChannels()
	if len(channels) != 1 || channels[0] != "mobile_money" {
		t.Fatalf("channels=%#v, want mobile_money", channels)
	}
}

func TestIntegrationSameCheckoutIsReused(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-reuse-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	checkout := checkoutFixture(t, db, a, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("first checkout status=%d", rec.Code)
	}
	first, _, _, _ := latestCheckout(t, db, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("repeat checkout status=%d body=%s", rec.Code, rec.Body)
	}

	var count int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM billing_checkouts WHERE account_id=$1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("checkout rows=%d, want 1 reused", count)
	}
	if second, _, _, _ := latestCheckout(t, db, accountID); second != first {
		t.Fatalf("reference changed from %s to %s", first, second)
	}
}

func TestIntegrationPaidCheckoutIsSettledNotDiscarded(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-paid-race-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	checkout := checkoutFixture(t, db, a, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("first checkout status=%d", rec.Code)
	}
	paid, _, _, _ := latestCheckout(t, db, accountID)

	paidTransaction := paystack.Transaction{
		ID:        99001,
		Status:    "success",
		Reference: paid,
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": accountID, "plan": "pro"},
	}
	paidTransaction.PlanObject.Code = a.cfg.PaystackPlanCode
	fs.Verify[paid] = paidTransaction

	rec := checkout("plan=starter&method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "billing_checkout_completed") {
		t.Fatalf("body=%s, want billing_checkout_completed", rec.Body)
	}
	if got := checkoutStatus(t, db, paid); got != "successful" {
		t.Fatalf("paid checkout status=%s, want successful", got)
	}
	if got := accountPlan(t, db, accountID); got != "pro" {
		t.Fatalf("plan=%s, want the plan that was paid for", got)
	}
}

func TestIntegrationUnverifiableCheckoutIsNotDiscarded(t *testing.T) {
	db := openIntegrationDB(t)
	email := fmt.Sprintf("billing-unverifiable-%d@example.test", time.Now().UnixNano())
	defer deleteTestAccount(t, db, email)

	fs := paystack.NewFakeServer()
	defer fs.Close()
	a := newIntegrationApp(t, db, fs)
	accountID := createTestAccount(t, db, email)
	checkout := checkoutFixture(t, db, a, accountID)

	if rec := checkout("plan=pro&method=card"); rec.Code != http.StatusOK {
		t.Fatalf("first checkout status=%d", rec.Code)
	}
	open, _, _, _ := latestCheckout(t, db, accountID)

	rec := checkout("plan=starter&method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want 409", rec.Code, rec.Body)
	}
	if got := checkoutStatus(t, db, open); got != "initialized" {
		t.Fatalf("checkout status=%s, want it left untouched", got)
	}
}

func newCurrencyBillingApp(t *testing.T) (*App, *paystack.FakeServer, string, string) {
	t.Helper()
	db := openIntegrationDB(t)
	address := fmt.Sprintf("currency-%d@example.test", time.Now().UnixNano())
	fs := paystack.NewFakeServer()
	t.Cleanup(fs.Close)
	a := newIntegrationApp(t, db, fs)
	a.cfg.PaystackStarterUSDPlanCode = "PLN_starter_usd"
	a.cfg.PaystackProUSDPlanCode = "PLN_pro_usd"
	a.cfg.PaystackBusinessUSDPlanCode = "PLN_business_usd"
	accountID := createTestAccount(t, db, address)
	t.Cleanup(func() { deleteTestAccount(t, db, address) })
	return a, fs, accountID, newSession(t, db, address)
}

func TestIntegrationUSDCheckoutVerifiesCurrencyAndProcessesSubscription(t *testing.T) {
	a, fs, accountID, session := newCurrencyBillingApp(t)
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/checkout?plan=pro&currency=USD&method=card", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("checkout %d %s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	reference := data["reference"].(string)
	currency, amount := fs.LastInitializeCurrency()
	channels, planCode := fs.LastInitializeChannels()
	if currency != "USD" || amount != 500 || len(channels) != 1 || channels[0] != "card" || planCode != a.cfg.PaystackProUSDPlanCode || data["currency"] != "USD" {
		t.Fatalf("currency=%s amount=%d channels=%v plan=%s response=%v", currency, amount, channels, planCode, data)
	}
	transaction := paystack.Transaction{ID: 9876, Status: "success", Reference: reference, Amount: amount, Currency: "KES", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	transaction.PlanObject.Code = planCode
	fs.Verify[reference] = transaction
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/verify?reference="+reference, session)
	if rec.Code == http.StatusOK || accountPlan(t, a.db, accountID) != "free" {
		t.Fatalf("accepted KES transaction for USD checkout: %d %s", rec.Code, rec.Body)
	}
	transaction.Currency = "USD"
	fs.Verify[reference] = transaction
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/verify?reference="+reference, session)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify %d %s", rec.Code, rec.Body)
	}
	body, _ := json.Marshal(map[string]any{"event": "subscription.create", "data": map[string]any{"subscription_code": "SUB_usd_" + accountID, "email_token": "usd-token", "status": "active", "amount": amount, "currency": "USD", "next_payment_date": time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339), "metadata": map[string]string{"account_id": accountID}, "customer": map[string]string{"customer_code": "CUS_usd"}, "plan": map[string]any{"plan_code": planCode, "amount": amount, "currency": "USD"}}})
	if status := postPaystackWebhook(t, a, body); status != http.StatusOK {
		t.Fatalf("USD subscription webhook status=%d", status)
	}
	var storedCurrency string
	var storedAmount int64
	if err := a.db.QueryRow(context.Background(), `SELECT currency,amount_minor FROM billing_subscriptions WHERE account_id=$1`, accountID).Scan(&storedCurrency, &storedAmount); err != nil {
		t.Fatal(err)
	}
	if storedCurrency != "USD" || storedAmount != 500 || accountPlan(t, a.db, accountID) != "pro" {
		t.Fatalf("stored=%s %d", storedCurrency, storedAmount)
	}
	rec = dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/checkout?plan=business&currency=KES", session)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("currency switch status=%d body=%s", rec.Code, rec.Body)
	}
}

func TestIntegrationBillingRejectsUnsupportedCurrencyAndUSDMpesa(t *testing.T) {
	for _, query := range []string{"currency=EUR", "currency=USD&method=mpesa"} {
		t.Run(query, func(t *testing.T) {
			a, _, accountID, session := newCurrencyBillingApp(t)
			rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/checkout?plan=starter&"+query, session)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d %s", rec.Code, rec.Body)
			}
			var count int
			if err := a.db.QueryRow(context.Background(), `SELECT count(*) FROM billing_checkouts WHERE account_id=$1`, accountID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("invalid checkout persisted")
			}
		})
	}
}

func TestIntegrationUSDRenewalPreservesCurrency(t *testing.T) {
	a, fs, accountID, session := newCurrencyBillingApp(t)
	setPlan(t, a, accountID, "pro")
	code := seedSubscription(t, a, accountID, "pro", "active", "manual", time.Now().Add(time.Hour))
	if _, err := a.db.Exec(context.Background(), `UPDATE billing_subscriptions SET currency='USD',amount_minor=500 WHERE provider_subscription_code=$1`, code); err != nil {
		t.Fatal(err)
	}
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/renew?plan=pro", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("renew %d %s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	currency, amount := fs.LastInitializeCurrency()
	channels, _ := fs.LastInitializeChannels()
	if currency != "USD" || amount != 500 || len(channels) != 1 || channels[0] != "card" {
		t.Fatalf("currency=%s amount=%d channels=%v", currency, amount, channels)
	}
	reference := data["reference"].(string)
	transaction := paystack.Transaction{ID: 9988, Status: "success", Reference: reference, Amount: 500, Currency: "USD", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	fs.Verify[reference] = transaction
	if _, err := a.verifyBillingCheckout(context.Background(), reference, accountID); err != nil {
		t.Fatal(err)
	}
	var storedCurrency string
	var end time.Time
	if err := a.db.QueryRow(context.Background(), `SELECT currency,current_period_end FROM billing_subscriptions WHERE provider_subscription_code=$1`, code).Scan(&storedCurrency, &end); err != nil {
		t.Fatal(err)
	}
	if storedCurrency != "USD" || end.Before(time.Now().Add(27*24*time.Hour)) {
		t.Fatalf("renewal currency=%s ends=%v", storedCurrency, end)
	}
}

func TestIntegrationUSDPlanChangesAndResumePreserveCurrency(t *testing.T) {
	for _, mode := range []string{"auto", "manual"} {
		t.Run(mode, func(t *testing.T) {
			a, fs, accountID, session := newCurrencyBillingApp(t)
			setPlan(t, a, accountID, "business")
			code := seedSubscriptionWithToken(t, a, accountID, "business", "active", mode, time.Now().Add(20*24*time.Hour))
			if _, err := a.db.Exec(context.Background(), `UPDATE billing_subscriptions SET currency='USD',amount_minor=1000 WHERE provider_subscription_code=$1`, code); err != nil {
				t.Fatal(err)
			}
			rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/change-plan?plan=pro", session)
			if rec.Code != http.StatusOK {
				t.Fatalf("change %d %s", rec.Code, rec.Body)
			}
			var currency string
			var amount int64
			if err := a.db.QueryRow(context.Background(), `SELECT currency,amount_minor FROM billing_subscriptions WHERE account_id=$1 AND plan='pro'`, accountID).Scan(&currency, &amount); err != nil {
				t.Fatal(err)
			}
			if currency != "USD" || amount != 500 {
				t.Fatalf("scheduled=%s %d", currency, amount)
			}
			if mode == "auto" {
				_, plan, _ := fs.CreatedSubscription()
				if plan != a.cfg.PaystackProUSDPlanCode {
					t.Fatalf("provider plan=%s", plan)
				}
			}
		})
	}
	a, fs, accountID, session := newCurrencyBillingApp(t)
	setPlan(t, a, accountID, "pro")
	code := seedSubscriptionWithToken(t, a, accountID, "pro", "non_renewing", "auto", time.Now().Add(20*24*time.Hour))
	if _, err := a.db.Exec(context.Background(), `UPDATE billing_subscriptions SET currency='USD',amount_minor=500 WHERE provider_subscription_code=$1`, code); err != nil {
		t.Fatal(err)
	}
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/resume", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("resume %d %s", rec.Code, rec.Body)
	}
	_, plan, _ := fs.CreatedSubscription()
	if plan != a.cfg.PaystackProUSDPlanCode {
		t.Fatalf("resumed provider plan=%s", plan)
	}
	var currency string
	if err := a.db.QueryRow(context.Background(), `SELECT currency FROM billing_subscriptions WHERE account_id=$1 AND provider_subscription_code<>$2`, accountID, code).Scan(&currency); err != nil {
		t.Fatal(err)
	}
	if currency != "USD" {
		t.Fatalf("resumed currency=%s", currency)
	}
}

func TestIntegrationUSDUpgradeProratesAndProvisionsInUSD(t *testing.T) {
	a, fs, accountID, session := newCurrencyBillingApp(t)
	setPlan(t, a, accountID, "starter")
	code := seedSubscriptionWithToken(t, a, accountID, "starter", "active", "auto", time.Now().Add(20*24*time.Hour))
	if _, err := a.db.Exec(context.Background(), `UPDATE billing_subscriptions SET currency='USD',amount_minor=100 WHERE provider_subscription_code=$1`, code); err != nil {
		t.Fatal(err)
	}
	rec := dashboardCall(t, a, http.MethodPost, "/api/dashboard/v1/billing/checkout?plan=pro", session)
	if rec.Code != http.StatusOK {
		t.Fatalf("upgrade %d %s", rec.Code, rec.Body)
	}
	data := decodeBody(t, rec)["data"].(map[string]any)
	reference := data["reference"].(string)
	currency, amount := fs.LastInitializeCurrency()
	credit := data["credit_minor"].(float64)
	if currency != "USD" || float64(amount)+credit != 500 || credit <= 0 {
		t.Fatalf("currency=%s charge=%d credit=%v", currency, amount, credit)
	}
	transaction := paystack.Transaction{ID: 9877, Status: "success", Reference: reference, Amount: amount, Currency: "USD", Metadata: map[string]any{"account_id": accountID, "plan": "pro"}}
	transaction.Customer.CustomerCode = "CUS_test"
	fs.Verify[reference] = transaction
	if _, err := a.verifyBillingCheckout(context.Background(), reference, accountID); err != nil {
		t.Fatal(err)
	}
	var storedCurrency string
	var storedAmount int64
	if err := a.db.QueryRow(context.Background(), `SELECT currency,amount_minor FROM billing_subscriptions WHERE account_id=$1 AND plan='pro' AND status='active'`, accountID).Scan(&storedCurrency, &storedAmount); err != nil {
		t.Fatal(err)
	}
	_, providerPlan, _ := fs.CreatedSubscription()
	if storedCurrency != "USD" || storedAmount != 500 || providerPlan != a.cfg.PaystackProUSDPlanCode || accountPlan(t, a.db, accountID) != "pro" {
		t.Fatalf("currency=%s amount=%d provider_plan=%s", storedCurrency, storedAmount, providerPlan)
	}
}
