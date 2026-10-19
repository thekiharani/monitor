package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func setRequiredTestSecrets(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://monitor:test@localhost:5432/monitor_test?sslmode=disable")
	t.Setenv("APP_DATABASE_URL", "postgres://monitor_444333:test@localhost:5432/monitor_test?sslmode=disable")
	t.Setenv("OTP_HMAC_KEY", base64.RawStdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyz012345")))
	t.Setenv("APP_ENCRYPTION_KEY", base64.RawStdEncoding.EncodeToString([]byte("01234567890123456789012345678901")))
}

func TestLoadRejectsMalformedExplicitValues(t *testing.T) {
	setRequiredTestSecrets(t)
	for name, value := range map[string]string{
		"DB_MAX_CONNS":  "not-a-number",
		"OTP_TTL":       "ten-minutes",
		"PPROF_ENABLED": "sometimes",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("expected %s validation error, got %v", name, err)
			}
		})
	}
}

func TestLoadRejectsInvalidOriginsAndPoolBounds(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("DASHBOARD_ORIGIN", "https://example.com/dashboard")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DASHBOARD_ORIGIN") {
		t.Fatalf("expected dashboard origin error, got %v", err)
	}

	t.Setenv("DASHBOARD_ORIGIN", "https://example.com")
	t.Setenv("DB_MIN_CONNS", "5")
	t.Setenv("DB_MAX_CONNS", "2")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("expected pool bounds error, got %v", err)
	}
}

func TestLoadRejectsUnknownEnvironment(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("APP_ENV", "prodution")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENV") {
		t.Fatalf("expected APP_ENV validation error, got %v", err)
	}
}

func TestLoadValidatesCloudWatchConfiguration(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("CLOUDWATCH_ENABLED", "true")
	t.Setenv("AWS_REGION", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "AWS_REGION") {
		t.Fatalf("expected required CloudWatch configuration error, got %v", err)
	}

	t.Setenv("AWS_REGION", "af-south-1")
	t.Setenv("CLOUDWATCH_LOG_GROUP", "/monitor/test")
	t.Setenv("CLOUDWATCH_LOG_STREAM", "{app}-{env}-{date}")
	t.Setenv("CLOUDWATCH_BATCH_SIZE", "50")
	t.Setenv("CLOUDWATCH_BUFFER_SIZE", "25")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "CLOUDWATCH_BUFFER_SIZE") {
		t.Fatalf("expected CloudWatch buffer validation error, got %v", err)
	}
}

func TestLoadRequiresCryptographicSecrets(t *testing.T) {
	t.Setenv("OTP_HMAC_KEY", "")
	t.Setenv("APP_ENCRYPTION_KEY", "")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "OTP_HMAC_KEY") {
		t.Fatalf("expected OTP key error, got %v", err)
	}

	t.Setenv("OTP_HMAC_KEY", base64.RawStdEncoding.EncodeToString([]byte("abcdefghijklmnopqrstuvwxyz012345")))
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "APP_ENCRYPTION_KEY") {
		t.Fatalf("expected encryption key error, got %v", err)
	}
}

func TestHSTSOnlyOnHTTPSOrigins(t *testing.T) {
	t.Setenv("APP_ORIGIN", "http://localhost:8900")
	t.Setenv("API_ORIGIN", "http://localhost:8900")
	t.Setenv("STATUS_ORIGIN", "http://localhost:8900")
	t.Setenv("DASHBOARD_ORIGIN", "http://localhost:3900")
	t.Setenv("HSTS_MAX_AGE", "8760h")
	setRequiredTestSecrets(t)

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HSTSMaxAge != 0 || c.HSTSHeader != "" {
		t.Fatalf("HSTS emitted for an http origin: %v %q", c.HSTSMaxAge, c.HSTSHeader)
	}

	t.Setenv("APP_ORIGIN", "https://monitor.noria.co.ke")
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HSTSHeader == "" {
		t.Fatal("HSTS missing for an https origin")
	}
	if !strings.Contains(c.HSTSHeader, "includeSubDomains") {
		t.Fatalf("HSTS header=%q", c.HSTSHeader)
	}
}

func TestTrustedProxyList(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("TRUSTED_PROXY", "10.112.96.0/24, 172.30.42.1 , ::1")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 3 {
		t.Fatalf("parsed %d prefixes, want 3", len(c.TrustedProxies))
	}

	t.Setenv("TRUSTED_PROXY", "10.112.96.0/24,not-an-ip")
	if _, err := Load(); err == nil {
		t.Fatal("a malformed TRUSTED_PROXY entry was accepted")
	}
}

func TestLoadRejectsSharedTenantConnection(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("APP_DATABASE_URL", "postgres://monitor:test@localhost:5432/monitor_test?sslmode=disable")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unprivileged role") {
		t.Errorf("Load() with APP_DATABASE_URL equal to DATABASE_URL = %v, want an unprivileged-role error", err)
	}
}

func TestProductionRunsWithoutPaymentsUntilPaystackIsConfigured(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("APP_ENV", "production")
	for _, name := range []string{"APP_ORIGIN", "API_ORIGIN", "STATUS_ORIGIN", "DASHBOARD_ORIGIN"} {
		t.Setenv(name, "https://monitor.noria.co.ke")
	}
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("SES_FROM_EMAIL", "alerts@monitor.noria.co.ke")
	t.Setenv("PAYSTACK_SECRET_KEY", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("production without payments should load: %v", err)
	}
	if c.PaymentsEnabled() {
		t.Fatal("payments reported enabled without a Paystack key")
	}

	t.Setenv("PAYSTACK_SECRET_KEY", "sk_live_test")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PAYSTACK_") {
		t.Fatalf("a Paystack key without plan codes must be refused, got %v", err)
	}
}

func TestPricingDerivesUSDFromKESBase(t *testing.T) {
	c := Config{StarterPriceMinor: 50000, ProPriceMinor: 200000, BusinessPriceMinor: 500000, PaystackStarterPlanCode: "KES_starter", PaystackPlanCode: "KES_pro", PaystackBusinessPlanCode: "KES_business", PaystackStarterUSDPlanCode: "USD_starter", PaystackProUSDPlanCode: "USD_pro", PaystackBusinessUSDPlanCode: "USD_business"}
	for _, tc := range []struct {
		plan     string
		kes, usd int64
	}{{"starter", 50000, 500}, {"pro", 200000, 2000}, {"business", 500000, 5000}} {
		base, ok := c.Pricing(tc.plan)
		dollar, usdOK := c.Pricing(tc.plan, "USD")
		if !ok || !usdOK || base.Amount != tc.kes || base.Currency != "KES" || dollar.Amount != tc.usd || dollar.Currency != "USD" || dollar.Code != "USD_"+tc.plan {
			t.Errorf("%s base=%+v USD=%+v", tc.plan, base, dollar)
		}
	}
	c.StarterPriceMinor = 50050
	if p, _ := c.Pricing("starter", "USD"); p.Amount != 501 {
		t.Errorf("USD cents=%d want501", p.Amount)
	}
	if _, ok := c.Pricing("starter", "EUR"); ok {
		t.Fatal("accepted unsupported currency")
	}
}

func TestLoadRejectsReusingKESPlanForUSD(t *testing.T) {
	setRequiredTestSecrets(t)
	t.Setenv("PAYSTACK_STARTER_PLAN_CODE", "PLN_kes")
	t.Setenv("PAYSTACK_STARTER_USD_PLAN_CODE", "PLN_kes")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "distinct USD plan code") {
		t.Fatalf("shared plan code accepted: %v", err)
	}
}
