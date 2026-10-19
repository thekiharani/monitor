package paystack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"crypto/tls"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

type Client struct {
	http                   *http.Client
	secret, plan, currency string
	price                  int64
	baseURL                string
}

type Option func(*Client)

func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.http = h
		}
	}
}

func New(c config.Config, options ...Option) *Client {
	return NewWithBaseURL(c, "https://api.paystack.co", options...)
}

func NewWithBaseURL(c config.Config, baseURL string, options ...Option) *Client {
	client := &Client{
		http:     &http.Client{Timeout: requestTimeout, Transport: newTransport()},
		secret:   c.PaystackSecretKey,
		plan:     c.PaystackPlanCode,
		currency: c.PaystackCurrency,
		price:    c.ProPriceMinor,
		baseURL:  strings.TrimRight(baseURL, "/"),
	}
	for _, option := range options {
		option(client)
	}
	return client
}

const requestTimeout = 10 * time.Second

func newTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 16
	transport.MaxIdleConnsPerHost = 8
	transport.MaxConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSHandshakeTimeout = 5 * time.Second
	transport.ExpectContinueTimeout = time.Second
	transport.ForceAttemptHTTP2 = true
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return transport
}

type response struct {
	Status  bool           `json:"status"`
	Message string         `json:"message"`
	Data    jsontext.Value `json:"data"`
}
type InitializeResult struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}
type CreatedSubscription struct {
	SubscriptionCode string `json:"subscription_code"`
	EmailToken       string `json:"email_token"`
}
type Transaction struct {
	ID         int64          `json:"id"`
	Status     string         `json:"status"`
	Reference  string         `json:"reference"`
	Amount     int64          `json:"amount"`
	Currency   string         `json:"currency"`
	Metadata   map[string]any `json:"metadata"`
	PlanObject struct {
		Code string `json:"plan_code"`
	} `json:"plan_object"`
	Customer struct {
		ID           int64  `json:"id"`
		CustomerCode string `json:"customer_code"`
		Email        string `json:"email"`
	} `json:"customer"`
}
type Subscription struct {
	Customer struct {
		ID           int64  `json:"id"`
		CustomerCode string `json:"customer_code"`
		Email        string `json:"email"`
	} `json:"customer"`
	SubscriptionCode string `json:"subscription_code"`
	EmailToken       string `json:"email_token"`
	Amount           int64  `json:"amount"`
	Currency         string `json:"currency"`
	Plan             struct {
		Code     string `json:"plan_code"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	} `json:"plan"`
	NextPaymentDate string `json:"next_payment_date"`
	Start           int64  `json:"start"`
	CreatedAt       string `json:"createdAt"`
	Status          string `json:"status"`
	Link            string `json:"link"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		var b bytes.Buffer
		if err := json.MarshalWrite(&b, body); err != nil {
			return err
		}
		bodyBytes = b.Bytes()
	}
	var last error
	attempts := 1
	if method == http.MethodGet || method == http.MethodHead {
		attempts = 3
	}
	for attempt := range attempts {
		if attempt > 0 {
			base := time.Duration(1<<(attempt-1)) * 100 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(base + rand.N(base)):
			}
		}
		var rd io.Reader
		if bodyBytes != nil {
			rd = bytes.NewReader(bodyBytes)
		}
		req, reqErr := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
		if reqErr != nil {
			return reqErr
		}
		req.Header.Set("Authorization", "Bearer "+c.secret)
		req.Header.Set("Content-Type", "application/json")
		resp, e := c.http.Do(req)
		if e != nil {
			last = e
			continue
		}
		data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if e != nil {
			last = e
			continue
		}
		var env response
		if e = json.Unmarshal(data, &env); e != nil {
			last = e
			continue
		}
		if resp.StatusCode >= 500 {
			last = fmt.Errorf("paystack server error")
			continue
		}
		if !env.Status {
			return fmt.Errorf("paystack: %s", env.Message)
		}
		if out != nil && len(env.Data) > 0 {
			return json.Unmarshal(env.Data, out)
		}
		return nil
	}
	return last
}
func (c *Client) Initialize(ctx context.Context, email, reference, callback, accountID, checkoutID string) (InitializeResult, error) {
	return c.initialize(ctx, email, reference, callback, accountID, checkoutID, c.plan, c.price, "pro", nil)
}

func (c *Client) InitializePlan(ctx context.Context, email, reference, callback, accountID, checkoutID, plan, providerPlanCode string, amount int64) (InitializeResult, error) {
	return c.initialize(ctx, email, reference, callback, accountID, checkoutID, providerPlanCode, amount, plan, nil)
}

func (c *Client) InitializeOneTime(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64) (InitializeResult, error) {
	return c.initialize(ctx, email, reference, callback, accountID, checkoutID, "", amount, plan, nil)
}

func (c *Client) InitializeOneTimeChannels(ctx context.Context, email, reference, callback, accountID, checkoutID, plan string, amount int64, channels []string) (InitializeResult, error) {
	return c.initialize(ctx, email, reference, callback, accountID, checkoutID, "", amount, plan, channels)
}

func (c *Client) initialize(ctx context.Context, email, reference, callback, accountID, checkoutID, providerPlanCode string, amount int64, plan string, channels []string) (InitializeResult, error) {
	return c.InitializeCurrency(ctx, email, reference, callback, accountID, checkoutID, plan, providerPlanCode, c.currency, amount, channels)
}

func (c *Client) InitializeCurrency(ctx context.Context, email, reference, callback, accountID, checkoutID, plan, providerPlanCode, currency string, amount int64, channels []string) (InitializeResult, error) {
	if currency != "KES" && currency != "USD" {
		return InitializeResult{}, fmt.Errorf("unsupported billing currency %q", currency)
	}
	var out InitializeResult
	payload := map[string]any{"email": email, "amount": amount, "currency": currency, "reference": reference, "callback_url": callback, "metadata": map[string]string{"account_id": accountID, "checkout_id": checkoutID, "plan": plan}}
	if providerPlanCode != "" {
		payload["plan"] = providerPlanCode
	}
	if len(channels) > 0 {
		payload["channels"] = channels
	}
	err := c.do(ctx, http.MethodPost, "/transaction/initialize", payload, &out)
	if err == nil {
		err = validateHostedURL(out.AuthorizationURL)
	}
	return out, err
}
func (c *Client) Verify(ctx context.Context, reference string) (Transaction, error) {
	var out Transaction
	err := c.do(ctx, http.MethodGet, "/transaction/verify/"+url.PathEscape(reference), nil, &out)
	return out, err
}
func (c *Client) FetchSubscription(ctx context.Context, code string) (Subscription, error) {
	var out Subscription
	err := c.do(ctx, http.MethodGet, "/subscription/"+url.PathEscape(code), nil, &out)
	return out, err
}

func (c *Client) ListSubscriptions(ctx context.Context, customerID int64) ([]Subscription, error) {
	query := url.Values{}
	query.Set("customer", fmt.Sprintf("%d", customerID))
	query.Set("perPage", "100")
	var out []Subscription
	err := c.do(ctx, http.MethodGet, "/subscription?"+query.Encode(), nil, &out)
	return out, err
}

func (c *Client) ManagementLink(ctx context.Context, code string) (string, error) {
	var out struct {
		Link string `json:"link"`
	}
	if err := c.do(ctx, http.MethodGet, "/subscription/"+url.PathEscape(code)+"/manage/link", nil, &out); err != nil {
		return "", err
	}
	if err := validateHostedURL(out.Link); err != nil {
		return "", err
	}
	return out.Link, nil
}

func validateHostedURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("paystack returned an invalid hosted URL")
	}
	return nil
}
func (c *Client) DisableSubscription(ctx context.Context, code, token string) error {
	return c.do(ctx, http.MethodPost, "/subscription/disable", map[string]string{"code": code, "token": token}, nil)
}
func (c *Client) EnableSubscription(ctx context.Context, code, token string) error {
	return c.do(ctx, http.MethodPost, "/subscription/enable", map[string]string{"code": code, "token": token}, nil)
}
func (c *Client) CreateSubscription(ctx context.Context, customer, planCode string, startDate time.Time) (CreatedSubscription, error) {
	var out CreatedSubscription
	err := c.do(ctx, http.MethodPost, "/subscription", map[string]string{"customer": customer, "plan": planCode, "start_date": startDate.UTC().Format(time.RFC3339)}, &out)
	if err == nil && (out.SubscriptionCode == "" || out.EmailToken == "") {
		err = fmt.Errorf("paystack returned an incomplete subscription")
	}
	return out, err
}

func VerifySignature(secret string, body, signature []byte) bool {
	if len(signature) == 0 {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	expected := make([]byte, 0, sha512.Size*2)
	expected = hex.AppendEncode(expected, mac.Sum(nil))
	return hmac.Equal(expected, bytes.ToLower(signature))
}

type Plan struct {
	PlanCode string `json:"plan_code"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Interval string `json:"interval"`
}

func (c *Client) FetchPlan(ctx context.Context, code string) (Plan, error) {
	var out Plan
	err := c.do(ctx, http.MethodGet, "/plan/"+url.PathEscape(code), nil, &out)
	return out, err
}
