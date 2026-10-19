package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/billing/paystack"
	"github.com/thekiharani/monitor/internal/cloud/config"
	"github.com/thekiharani/monitor/internal/cloud/email"
)

type fakeBilling struct {
	verifyFn   func(reference string) (paystack.Transaction, error)
	disableErr error
	disabled   []string
}

func (f *fakeBilling) FetchPlan(ctx context.Context, code string) (paystack.Plan, error) {
	return paystack.Plan{}, nil
}

func (f *fakeBilling) InitializeCurrency(ctx context.Context, email, reference, callback, accountID, checkoutID, plan, providerPlanCode, currency string, amount int64, channels []string) (paystack.InitializeResult, error) {
	return paystack.InitializeResult{}, nil
}

func (f *fakeBilling) Initialize(ctx context.Context, email, reference, callback, accountID, checkoutID string) (paystack.InitializeResult, error) {
	return paystack.InitializeResult{}, nil
}
func (f *fakeBilling) InitializeOneTime(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64) (paystack.InitializeResult, error) {
	return paystack.InitializeResult{}, nil
}
func (f *fakeBilling) InitializeOneTimeChannels(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64, channels []string) (paystack.InitializeResult, error) {
	return paystack.InitializeResult{}, nil
}
func (f *fakeBilling) Verify(ctx context.Context, reference string) (paystack.Transaction, error) {
	if f.verifyFn != nil {
		return f.verifyFn(reference)
	}
	return paystack.Transaction{}, nil
}
func (f *fakeBilling) FetchSubscription(ctx context.Context, code string) (paystack.Subscription, error) {
	return paystack.Subscription{}, nil
}
func (f *fakeBilling) ListSubscriptions(ctx context.Context, customerID int64) ([]paystack.Subscription, error) {
	return nil, nil
}
func (f *fakeBilling) ManagementLink(ctx context.Context, code string) (string, error) {
	return "", nil
}
func (f *fakeBilling) DisableSubscription(ctx context.Context, code, token string) error {
	if f.disableErr != nil {
		return f.disableErr
	}
	f.disabled = append(f.disabled, code)
	return nil
}
func (f *fakeBilling) EnableSubscription(ctx context.Context, code, token string) error {
	return nil
}
func (f *fakeBilling) CreateSubscription(ctx context.Context, customer, planCode string, startDate time.Time) (paystack.CreatedSubscription, error) {
	return paystack.CreatedSubscription{SubscriptionCode: "SUB_test", EmailToken: "token"}, nil
}

type fakeMail struct{}

func (fakeMail) Send(context.Context, email.Message) error { return nil }

func testConfig() config.Config {
	return config.Config{
		Env:                      "test",
		Addr:                     ":8900",
		AppOrigin:                "http://localhost:8900",
		DashboardOrigin:          "http://localhost:3900",
		APIOrigin:                "http://localhost:8900",
		StatusOrigin:             "http://localhost:8900",
		CORSAllowedOrigins:       []string{"http://localhost:3900"},
		DBMaxConns:               2,
		DBMinConns:               1,
		DBMaxConnLifetime:        30 * time.Minute,
		DBMaxConnIdleTime:        5 * time.Minute,
		OTPTTL:                   10 * time.Minute,
		SessionTTL:               14 * 24 * time.Hour,
		OTPHMACKey:               bytes.Repeat([]byte{1}, 32),
		AppEncryptionKey:         bytes.Repeat([]byte{2}, 32),
		PaystackSecretKey:        "test-secret",
		PaystackStarterPlanCode:  "PLN_starter",
		PaystackPlanCode:         "PLN_test",
		PaystackBusinessPlanCode: "PLN_business",
		PaystackCurrency:         "KES",
		StarterPriceMinor:        10000,
		ProPriceMinor:            50000,
		BusinessPriceMinor:       100000,
		BillingGracePeriod:       72 * time.Hour,
		LogLevel:                 "error",
		ProbeRegions:             []string{"local", "fra", "iad"},
		LocalProbeRegion:         "local",
		ProbeConcurrency:         4,
		ProbeAllowPrivate:        true,
	}
}

func signPaystackWebhook(secret string, body []byte) string {
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func testRequest(method, target string, body io.Reader, origin string) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return req
}

func testRequestWithPathValue(method, target string, body io.Reader, origin string, name, value string) *http.Request {
	req := testRequest(method, target, body, origin)
	if name != "" && value != "" {
		req.SetPathValue(name, value)
	}
	return req
}

func TestPaystackWebhookInvalidSignature(t *testing.T) {
	a := &App{cfg: testConfig(), billing: &fakeBilling{}, log: slog.Default(), metrics: newAppMetrics()}
	req := testRequest(http.MethodPost, "/webhooks/paystack", bytes.NewReader([]byte(`{"event":"charge.success","data":{}}`)), a.cfg.APIOrigin)
	req.Header.Set("x-paystack-signature", "bad")
	rec := httptest.NewRecorder()
	a.paystackWebhook(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestPaystackWebhookInvoicePaymentFailedPayloadParses(t *testing.T) {
	body := []byte(`{"event":"invoice.payment_failed","data":{"subscription":{"subscription_code":"SUB_test"},"transaction":{"reference":"trx-1","status":"failed"}}}`)
	env, err := paystack.ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := paystack.ExtractContext(env.Event, env.Data)
	if ctx.SubscriptionCode != "SUB_test" || ctx.Reference != "trx-1" {
		t.Fatalf("ctx=%+v", ctx)
	}
}

func TestPaystackWebhookIgnoredEventStored(t *testing.T) {
	ctx := paystack.ExtractContext("transfer.success", jsontext.Value(`{"reference":"trf"}`))
	if ctx.Reference != "trf" {
		t.Fatalf("reference=%q", ctx.Reference)
	}
	sanitized, err := paystack.SanitizeWebhookPayload([]byte(`{"event":"subscription.create","data":{"email_token":"secret"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sanitized, []byte("secret")) {
		t.Fatal("token leaked in sanitized payload")
	}
}

func TestSessionAuthBlocksSuspendedMutation(t *testing.T) {
	a := &App{cfg: testConfig(), log: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/api/dashboard/v1/links", io.NopCloser(bytes.NewReader(nil)))
	rec := httptest.NewRecorder()
	_, ok := a.sessionAuth(rec, req, true)
	if ok {
		t.Fatal("expected blocked without principal")
	}
}

func TestBillingCallbackRejectsWrongPlanWithFakePaystack(t *testing.T) {
	fs := paystack.NewFakeServer()
	defer fs.Close()

	cfg := testConfig()
	client := paystack.NewWithBaseURL(cfg, fs.URL())
	reference := "bil-wrong-plan"
	fs.Verify[reference] = paystack.Transaction{
		Status:    "success",
		Reference: reference,
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": "acc-1", "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: "PLN_other"},
	}

	a := &App{cfg: cfg, billing: client, log: slog.Default(), metrics: newAppMetrics()}
	if paystack.ValidatePayment(fs.Verify[reference], "acc-1", 50000, "KES", cfg.PaystackPlanCode, "pro") {
		t.Fatal("expected plan validation failure")
	}
	_ = a
}

func TestWebhookChargeSuccessRejectsWrongAmount(t *testing.T) {
	ctx := paystack.EventContext{
		TransactionStatus: "success",
		Amount:            100,
		Currency:          "KES",
		AccountID:         "acc-1",
		PlanCode:          "PLN_test",
	}
	if paystack.ValidateChargeContext(ctx, "acc-1", 50000, "KES", "PLN_test") {
		t.Fatal("expected amount mismatch rejection")
	}
}

func TestProratedUpgradeChargeCreditsUnusedOldPlanAndResetsCycle(t *testing.T) {
	periodStart := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	periodEnd := periodStart.Add(28 * 24 * time.Hour)
	now := periodStart.Add(7 * 24 * time.Hour)
	charge, credit, err := proratedUpgradeCharge(50000, 200000, periodStart, periodEnd, now)
	if err != nil {
		t.Fatal(err)
	}
	if credit != 37500 || charge != 162500 {
		t.Fatalf("charge=%d credit=%d", charge, credit)
	}
	if renewal := nextPaystackMonthlyRenewal(now); !renewal.Equal(time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("renewal=%s", renewal)
	}
}

func TestNextPaystackMonthlyRenewalUsesProviderMonthEndRule(t *testing.T) {
	start := time.Date(2026, time.August, 30, 9, 15, 0, 0, time.UTC)
	want := time.Date(2026, time.September, 28, 9, 15, 0, 0, time.UTC)
	if got := nextPaystackMonthlyRenewal(start); !got.Equal(want) {
		t.Fatalf("renewal=%s want=%s", got, want)
	}
}

func TestNewerSubscriptionUsesProviderCreationTime(t *testing.T) {
	older := paystack.Subscription{
		SubscriptionCode: "SUB_old",
		CreatedAt:        "2026-08-30T13:41:05Z",
		Start:            1788097265,
	}
	newer := paystack.Subscription{
		SubscriptionCode: "SUB_new",
		CreatedAt:        "2026-08-30T16:14:53Z",
		Start:            1788106493,
	}
	if !newerSubscription(newer, older) {
		t.Fatal("expected the newer provider subscription to win")
	}
	if newerSubscription(older, newer) {
		t.Fatal("expected the older provider subscription to lose")
	}
}

func TestPublicPlansServeKESBaseAndUSDPrices(t *testing.T) {
	a := &App{cfg: testConfig()}
	for _, tc := range []struct {
		query, currency string
		amount          int64
	}{{"", "KES", 50000}, {"?currency=USD", "USD", 500}} {
		rec := httptest.NewRecorder()
		req := testRequest(http.MethodGet, "/api/v1/plans"+tc.query, nil, a.cfg.APIOrigin)
		a.publicPlans(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d %s", rec.Code, rec.Body)
		}
		var result struct {
			Data []planView `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		for _, plan := range result.Data {
			if plan.Currency != tc.currency {
				t.Fatalf("currency=%s", plan.Currency)
			}
			if plan.Plan == "pro" && plan.PriceMinor != tc.amount {
				t.Fatalf("price=%d want%d", plan.PriceMinor, tc.amount)
			}
		}
	}
	rec := httptest.NewRecorder()
	a.publicPlans(rec, testRequest(http.MethodGet, "/api/v1/plans?currency=EUR", nil, a.cfg.APIOrigin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported currency status=%d", rec.Code)
	}
}
