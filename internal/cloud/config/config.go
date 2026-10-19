package config

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/domain"
)

type Config struct {
	Env, Addr, AppOrigin, DashboardOrigin, APIOrigin, StatusOrigin, DatabaseURL, AppDatabaseURL              string
	CORSAllowedOrigins                                                                                       []string
	DBMaxConns, DBMinConns                                                                                   int32
	DBMaxConnLifetime, DBMaxConnIdleTime                                                                     time.Duration
	OTPTTL, SessionTTL                                                                                       time.Duration
	OTPHMACKey, AppEncryptionKey                                                                             []byte
	EmailProvider, SMTPHost, SMTPFromEmail                                                                   string
	SMTPPort                                                                                                 int
	AWSRegion, SESFromEmail                                                                                  string
	PaystackSecretKey, PaystackPlanCode, PaystackStarterPlanCode, PaystackBusinessPlanCode, PaystackCurrency string
	PaystackStarterUSDPlanCode, PaystackProUSDPlanCode, PaystackBusinessUSDPlanCode                          string
	StarterPriceMinor, ProPriceMinor, BusinessPriceMinor                                                     int64
	BillingGracePeriod                                                                                       time.Duration
	PprofEnabled                                                                                             bool
	PprofAddr, LogLevel                                                                                      string
	TrustedProxy                                                                                             string
	TrustedProxies                                                                                           []netip.Prefix
	HSTSMaxAge                                                                                               time.Duration
	HSTSHeader                                                                                               string
	CloudWatchEnabled                                                                                        bool
	CloudWatchRegion, CloudWatchLogGroup, CloudWatchLogStream                                                string
	CloudWatchCreateGroup                                                                                    bool
	CloudWatchBatchSize, CloudWatchBufferSize, CloudWatchRetentionDays                                       int
	CloudWatchFlushInterval                                                                                  time.Duration
	ProbeRegions                                                                                             []string
	LocalProbeRegion                                                                                         string
	ProbeConcurrency                                                                                         int
	ProbeAllowPrivate                                                                                        bool
	TelegramBotToken, TelegramBotName, TelegramAPIBase                                                       string
}

type PlanPricing struct {
	Code     string
	Amount   int64
	Currency string
}

func (c Config) Pricing(plan string, currencies ...string) (PlanPricing, bool) {
	currency := "KES"
	if len(currencies) > 0 {
		currency = currencies[0]
	}
	var pricing PlanPricing
	switch plan {
	case domain.PlanStarter:
		pricing = PlanPricing{Code: c.PaystackStarterPlanCode, Amount: c.StarterPriceMinor}
	case domain.PlanPro:
		pricing = PlanPricing{Code: c.PaystackPlanCode, Amount: c.ProPriceMinor}
	case domain.PlanBusiness:
		pricing = PlanPricing{Code: c.PaystackBusinessPlanCode, Amount: c.BusinessPriceMinor}
	default:
		return PlanPricing{}, false
	}
	if currency == "USD" {
		pricing.Code = map[string]string{"starter": c.PaystackStarterUSDPlanCode, "pro": c.PaystackProUSDPlanCode, "business": c.PaystackBusinessUSDPlanCode}[plan]
		pricing.Amount = pricing.Amount/100 + (pricing.Amount%100+50)/100
	} else if currency != "KES" {
		return PlanPricing{}, false
	}
	pricing.Currency = currency
	return pricing, true
}

func (c Config) PaymentsEnabled() bool { return c.PaystackSecretKey != "" }

func Load() (Config, error) {
	if err := validateExplicitValues(); err != nil {
		return Config{}, err
	}
	appOrigin := env("APP_ORIGIN", "http://localhost:8910")
	c := Config{
		Env: env("APP_ENV", "development"), Addr: env("APP_ADDR", ":8910"),
		AppOrigin: appOrigin, DashboardOrigin: env("DASHBOARD_ORIGIN", "http://localhost:3910"), APIOrigin: env("API_ORIGIN", "http://localhost:8910"), StatusOrigin: env("STATUS_ORIGIN", "http://localhost:8910"),
		CORSAllowedOrigins: splitCSV(os.Getenv("CORS_ALLOWED_ORIGINS")),
		DatabaseURL:        os.Getenv("DATABASE_URL"), AppDatabaseURL: os.Getenv("APP_DATABASE_URL"), DBMaxConns: envInt32("DB_MAX_CONNS", 20), DBMinConns: envInt32("DB_MIN_CONNS", 2),
		DBMaxConnLifetime: envDuration("DB_MAX_CONN_LIFETIME", 30*time.Minute), DBMaxConnIdleTime: envDuration("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),
		OTPTTL: envDuration("OTP_TTL", 10*time.Minute), SessionTTL: envDuration("SESSION_TTL", 14*24*time.Hour),
		EmailProvider: env("EMAIL_PROVIDER", "ses"), SMTPHost: os.Getenv("SMTP_HOST"), SMTPPort: envInt("SMTP_PORT", 1025), SMTPFromEmail: os.Getenv("SMTP_FROM_EMAIL"),
		AWSRegion: os.Getenv("AWS_REGION"), SESFromEmail: os.Getenv("SES_FROM_EMAIL"), PaystackSecretKey: os.Getenv("PAYSTACK_SECRET_KEY"), PaystackPlanCode: os.Getenv("PAYSTACK_PRO_PLAN_CODE"), PaystackStarterPlanCode: os.Getenv("PAYSTACK_STARTER_PLAN_CODE"), PaystackBusinessPlanCode: os.Getenv("PAYSTACK_BUSINESS_PLAN_CODE"), PaystackCurrency: env("PAYSTACK_CURRENCY", "KES"),
		StarterPriceMinor: envInt64("STARTER_PRICE_KES_MINOR", 0), ProPriceMinor: envInt64("PRO_PRICE_KES_MINOR", 0), BusinessPriceMinor: envInt64("BUSINESS_PRICE_KES_MINOR", 0), BillingGracePeriod: envDuration("BILLING_GRACE_PERIOD", 72*time.Hour),
		PprofEnabled: envBool("PPROF_ENABLED", false), PprofAddr: env("PPROF_ADDR", "127.0.0.1:6060"), LogLevel: env("LOG_LEVEL", "info"), TrustedProxy: os.Getenv("TRUSTED_PROXY"),
		HSTSMaxAge:        envDuration("HSTS_MAX_AGE", 365*24*time.Hour),
		CloudWatchEnabled: envBool("CLOUDWATCH_ENABLED", false), CloudWatchRegion: os.Getenv("AWS_REGION"), CloudWatchLogGroup: os.Getenv("CLOUDWATCH_LOG_GROUP"), CloudWatchLogStream: env("CLOUDWATCH_LOG_STREAM", "{app}-{env}-{hostname}-{date}"),
		CloudWatchCreateGroup: envBool("CLOUDWATCH_LOG_CREATE_GROUP", true), CloudWatchBatchSize: envInt("CLOUDWATCH_BATCH_SIZE", 25), CloudWatchBufferSize: envInt("CLOUDWATCH_BUFFER_SIZE", 1000), CloudWatchFlushInterval: envDuration("CLOUDWATCH_FLUSH_INTERVAL", 5*time.Second), CloudWatchRetentionDays: envInt("CLOUDWATCH_LOG_RETENTION_DAYS", 30),
	}
	c.ProbeRegions = splitCSV(env("PROBE_REGIONS", "local"))
	c.PaystackStarterUSDPlanCode = os.Getenv("PAYSTACK_STARTER_USD_PLAN_CODE")
	c.PaystackProUSDPlanCode = os.Getenv("PAYSTACK_PRO_USD_PLAN_CODE")
	c.PaystackBusinessUSDPlanCode = os.Getenv("PAYSTACK_BUSINESS_USD_PLAN_CODE")
	seenPlanCodes := map[string]bool{}
	for _, code := range []string{c.PaystackStarterPlanCode, c.PaystackPlanCode, c.PaystackBusinessPlanCode} {
		if code != "" {
			seenPlanCodes[code] = true
		}
	}
	for name, code := range map[string]string{"PAYSTACK_STARTER_USD_PLAN_CODE": c.PaystackStarterUSDPlanCode, "PAYSTACK_PRO_USD_PLAN_CODE": c.PaystackProUSDPlanCode, "PAYSTACK_BUSINESS_USD_PLAN_CODE": c.PaystackBusinessUSDPlanCode} {
		if code == "" {
			continue
		}
		if seenPlanCodes[code] {
			return Config{}, fmt.Errorf("%s must use a distinct USD plan code", name)
		}
		seenPlanCodes[code] = true
	}
	c.LocalProbeRegion = env("LOCAL_PROBE_REGION", "local")
	c.ProbeConcurrency = envInt("PROBE_CONCURRENCY", 32)
	c.ProbeAllowPrivate = envBool("PROBE_ALLOW_PRIVATE", false)
	c.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	c.TelegramBotName = strings.TrimPrefix(os.Getenv("TELEGRAM_BOT_NAME"), "@")
	c.TelegramAPIBase = env("TELEGRAM_API_BASE", "https://api.telegram.org")
	var err error
	if c.OTPHMACKey, err = secret("OTP_HMAC_KEY"); err != nil {
		return Config{}, err
	}
	if c.AppEncryptionKey, err = secret("APP_ENCRYPTION_KEY"); err != nil {
		return Config{}, err
	}
	for name, value := range map[string]string{"APP_ORIGIN": c.AppOrigin, "DASHBOARD_ORIGIN": c.DashboardOrigin, "API_ORIGIN": c.APIOrigin, "STATUS_ORIGIN": c.StatusOrigin} {
		if err := validateOrigin(name, value); err != nil {
			return Config{}, err
		}
	}
	for _, origin := range c.CORSAllowedOrigins {
		if err := validateOrigin("CORS_ALLOWED_ORIGINS", origin); err != nil {
			return Config{}, err
		}
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	if c.AppDatabaseURL == "" {
		return Config{}, errors.New("APP_DATABASE_URL is required")
	}
	if c.AppDatabaseURL == c.DatabaseURL {
		return Config{}, errors.New("APP_DATABASE_URL must use the unprivileged role, not the same connection as DATABASE_URL")
	}
	if c.DBMinConns > c.DBMaxConns {
		return Config{}, errors.New("DB_MIN_CONNS must not exceed DB_MAX_CONNS")
	}
	if c.TrustedProxies, err = parseTrustedProxies(c.TrustedProxy); err != nil {
		return Config{}, err
	}
	if c.HSTSMaxAge < 0 {
		return Config{}, errors.New("HSTS_MAX_AGE must not be negative")
	}
	if !strings.HasPrefix(c.AppOrigin, "https://") {
		c.HSTSMaxAge = 0
	}
	if c.HSTSMaxAge > 0 {
		c.HSTSHeader = "max-age=" + strconv.FormatInt(int64(c.HSTSMaxAge.Seconds()), 10) + "; includeSubDomains; preload"
	}
	if c.EmailProvider != "ses" && c.EmailProvider != "mailpit" {
		return Config{}, errors.New("EMAIL_PROVIDER must be ses or mailpit")
	}
	if c.LogLevel != "debug" && c.LogLevel != "info" && c.LogLevel != "warn" && c.LogLevel != "error" {
		return Config{}, errors.New("LOG_LEVEL must be debug, info, warn, or error")
	}
	if c.Env != "development" && c.Env != "staging" && c.Env != "production" {
		return Config{}, errors.New("APP_ENV must be development, staging, or production")
	}
	if c.CloudWatchBatchSize > 10000 {
		return Config{}, errors.New("CLOUDWATCH_BATCH_SIZE must not exceed 10000")
	}
	if c.CloudWatchBufferSize < c.CloudWatchBatchSize {
		return Config{}, errors.New("CLOUDWATCH_BUFFER_SIZE must be at least CLOUDWATCH_BATCH_SIZE")
	}
	if !validCloudWatchRetention(c.CloudWatchRetentionDays) {
		return Config{}, errors.New("CLOUDWATCH_LOG_RETENTION_DAYS is not supported by CloudWatch Logs")
	}
	if c.CloudWatchEnabled && (c.CloudWatchRegion == "" || c.CloudWatchLogGroup == "" || c.CloudWatchLogStream == "") {
		return Config{}, errors.New("AWS_REGION, CLOUDWATCH_LOG_GROUP, and CLOUDWATCH_LOG_STREAM are required when CloudWatch logging is enabled")
	}
	if c.Env == "production" {
		if c.EmailProvider != "ses" {
			return Config{}, errors.New("EMAIL_PROVIDER must be ses in production")
		}
		for name, value := range map[string]string{"DATABASE_URL": c.DatabaseURL, "AWS_REGION": c.AWSRegion, "SES_FROM_EMAIL": c.SESFromEmail} {
			if value == "" {
				return Config{}, fmt.Errorf("%s is required in production", name)
			}
		}
		if c.PaymentsEnabled() {
			for name, value := range map[string]string{"PAYSTACK_STARTER_PLAN_CODE": c.PaystackStarterPlanCode, "PAYSTACK_PRO_PLAN_CODE": c.PaystackPlanCode, "PAYSTACK_BUSINESS_PLAN_CODE": c.PaystackBusinessPlanCode} {
				if value == "" {
					return Config{}, fmt.Errorf("%s is required in production once PAYSTACK_SECRET_KEY is set", name)
				}
			}
			if c.StarterPriceMinor <= 0 || c.ProPriceMinor <= 0 || c.BusinessPriceMinor <= 0 {
				return Config{}, errors.New("STARTER_PRICE_KES_MINOR, PRO_PRICE_KES_MINOR, and BUSINESS_PRICE_KES_MINOR must be positive in production once PAYSTACK_SECRET_KEY is set")
			}
		}
		if c.PaystackCurrency != "KES" {
			return Config{}, errors.New("PAYSTACK_CURRENCY must be KES in production")
		}
		if !strings.HasPrefix(c.AppOrigin, "https://") || !strings.HasPrefix(c.DashboardOrigin, "https://") || !strings.HasPrefix(c.APIOrigin, "https://") || !strings.HasPrefix(c.StatusOrigin, "https://") {
			return Config{}, errors.New("production origins must use HTTPS")
		}
	}
	if c.OTPTTL <= 0 || c.SessionTTL <= 0 || c.DBMaxConnLifetime <= 0 || c.DBMaxConnIdleTime <= 0 || c.BillingGracePeriod <= 0 || c.CloudWatchFlushInterval <= 0 {
		return Config{}, errors.New("duration and buffer configuration must be positive")
	}
	if len(c.ProbeRegions) == 0 || len(c.ProbeRegions) > 16 {
		return Config{}, errors.New("PROBE_REGIONS must name 1 to 16 regions")
	}
	for _, region := range c.ProbeRegions {
		if !validRegion(region) {
			return Config{}, fmt.Errorf("PROBE_REGIONS has an invalid region %q", region)
		}
	}
	if c.LocalProbeRegion != "" && !slices.Contains(c.ProbeRegions, c.LocalProbeRegion) {
		return Config{}, errors.New("LOCAL_PROBE_REGION must be one of PROBE_REGIONS, or empty to run no built-in probe")
	}
	if c.ProbeConcurrency < 1 || c.ProbeConcurrency > 1024 {
		return Config{}, errors.New("PROBE_CONCURRENCY must be between 1 and 1024")
	}
	if c.ProbeAllowPrivate && c.Env == "production" {
		return Config{}, errors.New("PROBE_ALLOW_PRIVATE must not be set in production")
	}
	if c.EmailProvider == "mailpit" && (c.SMTPHost == "" || c.SMTPFromEmail == "") {
		return Config{}, errors.New("SMTP_HOST and SMTP_FROM_EMAIL are required for mailpit")
	}
	return c, nil
}

func validCloudWatchRetention(days int) bool {
	return slices.Contains([]int{1, 3, 5, 7, 14, 30, 60, 90, 120, 150, 180, 365, 400, 545, 731, 1096, 1827, 2192, 2557, 2922, 3288, 3653}, days)
}

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	parts := splitCSV(value)
	out := make([]netip.Prefix, 0, len(parts))
	for _, part := range parts {
		prefix, err := ParseTrustedProxy(part)
		if err != nil {
			return nil, fmt.Errorf("TRUSTED_PROXY entry %q must be a valid IP address or CIDR block", part)
		}
		out = append(out, prefix)
	}
	return out, nil
}

func ParseTrustedProxy(value string) (netip.Prefix, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Prefix{}, errors.New("empty trusted proxy")
	}
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr.Unmap(), addr.Unmap().BitLen()), nil
}

func ValidTrustedProxy(value string) bool {
	_, err := ParseTrustedProxy(value)
	return err == nil
}

func validateOrigin(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("%s must be an absolute HTTP(S) origin without credentials, path, query, or fragment", name)
	}
	return nil
}

func validateExplicitValues() error {
	positiveInts := []string{"DB_MAX_CONNS", "DB_MIN_CONNS", "SMTP_PORT", "CLOUDWATCH_BATCH_SIZE", "CLOUDWATCH_BUFFER_SIZE", "CLOUDWATCH_LOG_RETENTION_DAYS"}
	for _, name := range positiveInts {
		if raw := os.Getenv(name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return fmt.Errorf("%s must be a positive integer", name)
			}
		}
	}
	for _, name := range []string{"STARTER_PRICE_KES_MINOR", "PRO_PRICE_KES_MINOR", "BUSINESS_PRICE_KES_MINOR"} {
		if raw := os.Getenv(name); raw != "" {
			if value, err := strconv.ParseInt(raw, 10, 64); err != nil || value < 0 {
				return fmt.Errorf("%s must be a non-negative integer", name)
			}
		}
	}
	for _, name := range []string{"DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME", "OTP_TTL", "SESSION_TTL", "BILLING_GRACE_PERIOD", "CLOUDWATCH_FLUSH_INTERVAL", "HSTS_MAX_AGE"} {
		if raw := os.Getenv(name); raw != "" {
			if value, err := time.ParseDuration(raw); err != nil || value <= 0 {
				return fmt.Errorf("%s must be a positive Go duration", name)
			}
		}
	}
	for _, name := range []string{"PPROF_ENABLED", "CLOUDWATCH_ENABLED", "CLOUDWATCH_LOG_CREATE_GROUP"} {
		if raw := os.Getenv(name); raw != "" {
			if _, err := strconv.ParseBool(raw); err != nil {
				return fmt.Errorf("%s must be a boolean", name)
			}
		}
	}
	return nil
}

func secret(name string) ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	b, err := base64.RawStdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be unpadded base64: %w", name, err)
	}
	if name == "OTP_HMAC_KEY" && len(b) < 32 {
		return nil, fmt.Errorf("%s must contain at least 32 bytes", name)
	}
	if name == "APP_ENCRYPTION_KEY" && len(b) != 32 {
		return nil, fmt.Errorf("%s must decode to exactly 32 bytes", name)
	}
	return b, nil
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func envInt(k string, fallback int) int {
	if v, e := strconv.Atoi(os.Getenv(k)); e == nil && v > 0 {
		return v
	}
	return fallback
}
func envInt32(k string, fallback int32) int32 { return int32(envInt(k, int(fallback))) }
func envInt64(k string, fallback int64) int64 {
	if v, e := strconv.ParseInt(os.Getenv(k), 10, 64); e == nil {
		return v
	}
	return fallback
}
func envBool(k string, fallback bool) bool {
	if v, e := strconv.ParseBool(os.Getenv(k)); e == nil {
		return v
	}
	return fallback
}
func envDuration(k string, fallback time.Duration) time.Duration {
	if v, e := time.ParseDuration(os.Getenv(k)); e == nil {
		return v
	}
	return fallback
}
func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}
func HashSecret(value string) []byte { h := sha256.Sum256([]byte(value)); return h[:] }

func validRegion(region string) bool {
	if region == "" || len(region) > 32 {
		return false
	}
	for _, r := range region {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
