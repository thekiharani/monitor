package paystack

import (
	"encoding/json/v2"
	"testing"
)

func TestExtractContextInvoicePaymentFailed(t *testing.T) {
	body := []byte(`{
		"event":"invoice.payment_failed",
		"data":{
			"paid":false,
			"status":"failed",
			"subscription":{"subscription_code":"SUB_abc","email_token":"secret-token"},
			"transaction":{"reference":"trx-1","status":"failed","amount":50000,"currency":"KES"}
		}
	}`)
	env, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExtractContext(env.Event, env.Data)
	if ctx.SubscriptionCode != "SUB_abc" {
		t.Fatalf("subscription_code=%q", ctx.SubscriptionCode)
	}
	if ctx.Reference != "trx-1" {
		t.Fatalf("reference=%q", ctx.Reference)
	}
	if ctx.EmailToken != "secret-token" {
		t.Fatalf("email_token=%q", ctx.EmailToken)
	}
}

func TestExtractContextInvoiceUpdatePaid(t *testing.T) {
	body := []byte(`{
		"event":"invoice.update",
		"data":{
			"paid":true,
			"status":"success",
			"period_start":"2026-01-01T00:00:00Z",
			"period_end":"2026-02-01T00:00:00Z",
			"subscription":{"subscription_code":"SUB_renew"},
			"transaction":{"reference":"trx-2","status":"success","amount":50000,"currency":"KES","metadata":{"account_id":"acc-1","plan":"pro"}}
		}
	}`)
	env, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExtractContext(env.Event, env.Data)
	if !ctx.Paid || ctx.SubscriptionCode != "SUB_renew" {
		t.Fatalf("paid=%v sub=%q", ctx.Paid, ctx.SubscriptionCode)
	}
	if ctx.AccountID != "acc-1" || ctx.PeriodStart == nil || ctx.PeriodEnd == nil {
		t.Fatalf("account=%q periods=%v/%v", ctx.AccountID, ctx.PeriodStart, ctx.PeriodEnd)
	}
}

func TestExtractContextExpiringCardsArray(t *testing.T) {
	body := []byte(`{"event":"subscription.expiring_cards","data":[{"subscription":{"subscription_code":"SUB_one"}},{"subscription":{"subscription_code":"SUB_two"}}]}`)
	env, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExtractContext(env.Event, env.Data)
	if len(ctx.ExpiringSubscriptions) != 2 {
		t.Fatalf("subs=%v", ctx.ExpiringSubscriptions)
	}
}

func TestExtractAndValidateSubscriptionCreate(t *testing.T) {
	body := []byte(`{
		"event":"subscription.create",
		"data":{
			"subscription_code":"SUB_new",
			"email_token":"token",
			"status":"active",
			"amount":50000,
			"currency":"KES",
			"next_payment_date":"2026-09-29T00:00:00Z",
			"customer":{"customer_code":"CUS_one","email":"person@example.test"},
			"plan":{"plan_code":"PLN_123","amount":50000,"currency":"KES"}
		}
	}`)
	env, err := ParseWebhook(body)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ExtractContext(env.Event, env.Data)
	if ctx.CustomerEmail != "person@example.test" || ctx.CustomerCode != "CUS_one" || ctx.PeriodEnd == nil {
		t.Fatalf("context=%+v", ctx)
	}
	if !ValidateSubscriptionContext(ctx, 50000, "KES", "PLN_123") {
		t.Fatalf("expected valid subscription: %+v", ctx)
	}
	ctx.PlanCode = ""
	if ValidateSubscriptionContext(ctx, 50000, "KES", "PLN_123") {
		t.Fatal("expected missing plan code to fail")
	}
}

func TestSanitizeWebhookPayloadRedactsToken(t *testing.T) {
	body := []byte(`{"event":"subscription.create","data":{"subscription_code":"SUB_x","email_token":"plain-token"}}`)
	out, err := SanitizeWebhookPayload(body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	data := parsed["data"].(map[string]any)
	if data["email_token"] != "[redacted]" {
		t.Fatalf("token not redacted: %v", data["email_token"])
	}
}

func TestParseWebhookRejectsDuplicateFields(t *testing.T) {
	body := []byte(`{"event":"charge.success","event":"subscription.disable","data":{}}`)
	if _, err := ParseWebhook(body); err == nil {
		t.Fatal("expected duplicate webhook field to be rejected")
	}
}

func TestValidatePayment(t *testing.T) {
	tx := Transaction{
		Status:    "success",
		Reference: "bil-1",
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": "acc-1", "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: "PLN_123"},
	}
	if !ValidatePayment(tx, "acc-1", 50000, "KES", "PLN_123", "pro") {
		t.Fatal("expected valid payment")
	}
	if ValidatePayment(tx, "acc-2", 50000, "KES", "PLN_123", "pro") {
		t.Fatal("expected account mismatch")
	}
	if ValidatePayment(tx, "acc-1", 50000, "KES", "PLN_other", "pro") {
		t.Fatal("expected plan mismatch")
	}
}

func TestMapSubscriptionStatus(t *testing.T) {
	if status, ok := MapSubscriptionStatus("cancelled"); !ok || status != "canceled" {
		t.Fatalf("cancelled -> %q %v", status, ok)
	}
	if status, ok := MapSubscriptionStatus("attention"); !ok || status != "past_due" {
		t.Fatalf("attention -> %q %v", status, ok)
	}
	if status, ok := MapSubscriptionStatus("complete"); !ok || status != "complete" {
		t.Fatalf("complete -> %q %v", status, ok)
	}
}
