package paystack

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"time"
)

type WebhookEnvelope struct {
	Event string         `json:"event"`
	Data  jsontext.Value `json:"data"`
}

type EventContext struct {
	Reference             string
	AccountID             string
	SubscriptionCode      string
	EmailToken            string
	Amount                int64
	Currency              string
	Paid                  bool
	InvoiceStatus         string
	TransactionStatus     string
	PlanCode              string
	CustomerCode          string
	CustomerEmail         string
	TransactionID         int64
	PeriodStart           *time.Time
	PeriodEnd             *time.Time
	ExpiringSubscriptions []string
}

func ParseWebhook(body []byte) (WebhookEnvelope, error) {
	var env WebhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return WebhookEnvelope{}, err
	}
	return env, nil
}

func ExtractContext(eventType string, raw jsontext.Value) EventContext {
	if eventType == "subscription.expiring_cards" {
		return extractExpiringCards(raw)
	}
	obj, ok := unmarshalObject(raw)
	if !ok {
		return EventContext{}
	}
	ctx := EventContext{
		Reference:         firstString(obj, "reference"),
		SubscriptionCode:  firstString(obj, "subscription_code"),
		EmailToken:        firstString(obj, "email_token"),
		Amount:            firstInt64(obj, "amount"),
		Currency:          firstString(obj, "currency"),
		Paid:              firstBool(obj, "paid"),
		InvoiceStatus:     firstString(obj, "status"),
		TransactionStatus: firstString(obj, "status"),
		PlanCode:          planCodeFrom(obj),
		CustomerCode:      customerCodeFrom(obj),
		TransactionID:     firstInt64(obj, "id"),
		PeriodStart:       parsePaystackTime(firstString(obj, "period_start")),
		PeriodEnd:         parsePaystackTime(firstString(obj, "period_end")),
	}
	if ctx.PeriodEnd == nil {
		ctx.PeriodEnd = parsePaystackTime(firstString(obj, "next_payment_date"))
	}
	if plan := nestedMap(obj, "plan"); plan != nil {
		if ctx.Amount == 0 {
			ctx.Amount = firstInt64(plan, "amount")
		}
		if ctx.Currency == "" {
			ctx.Currency = firstString(plan, "currency")
		}
	}
	if sub := nestedMap(obj, "subscription"); sub != nil {
		if ctx.SubscriptionCode == "" {
			ctx.SubscriptionCode = firstString(sub, "subscription_code")
		}
		if ctx.EmailToken == "" {
			ctx.EmailToken = firstString(sub, "email_token")
		}
	}
	if tx := nestedMap(obj, "transaction"); tx != nil {
		if ctx.Reference == "" {
			ctx.Reference = firstString(tx, "reference")
		}
		if ctx.TransactionStatus == "" {
			ctx.TransactionStatus = firstString(tx, "status")
		}
		if ctx.Amount == 0 {
			ctx.Amount = firstInt64(tx, "amount")
		}
		if ctx.Currency == "" {
			ctx.Currency = firstString(tx, "currency")
		}
		if ctx.TransactionID == 0 {
			ctx.TransactionID = firstInt64(tx, "id")
		}
		if ctx.PlanCode == "" {
			ctx.PlanCode = planCodeFrom(tx)
		}
		if meta := nestedMap(tx, "metadata"); meta != nil {
			if ctx.AccountID == "" {
				ctx.AccountID = firstString(meta, "account_id")
			}
		}
	}
	if meta := nestedMap(obj, "metadata"); meta != nil && ctx.AccountID == "" {
		ctx.AccountID = firstString(meta, "account_id")
	}
	if customer := nestedMap(obj, "customer"); customer != nil {
		ctx.CustomerEmail = firstString(customer, "email")
	}
	if ctx.Paid || strings.EqualFold(ctx.InvoiceStatus, "success") || strings.EqualFold(ctx.InvoiceStatus, "paid") {
		ctx.Paid = true
	}
	return ctx
}

func extractExpiringCards(raw jsontext.Value) EventContext {
	ctx := EventContext{}
	var items []jsontext.Value
	if err := json.Unmarshal(raw, &items); err == nil {
		for _, item := range items {
			if obj, ok := unmarshalObject(item); ok {
				if code := subscriptionCodeFrom(obj); code != "" {
					ctx.ExpiringSubscriptions = append(ctx.ExpiringSubscriptions, code)
				}
			}
		}
		return ctx
	}
	obj, ok := unmarshalObject(raw)
	if !ok {
		return ctx
	}
	for _, key := range []string{"subscriptions", "data", "expiring"} {
		if arr, ok := obj[key].([]any); ok {
			for _, item := range arr {
				if m, ok := item.(map[string]any); ok {
					if code := subscriptionCodeFrom(m); code != "" {
						ctx.ExpiringSubscriptions = append(ctx.ExpiringSubscriptions, code)
					}
				}
			}
		}
	}
	if code := subscriptionCodeFrom(obj); code != "" {
		ctx.ExpiringSubscriptions = append(ctx.ExpiringSubscriptions, code)
	}
	return ctx
}

var redactedKeys = map[string]struct{}{
	"email_token":    {},
	"email":          {},
	"phone":          {},
	"first_name":     {},
	"last_name":      {},
	"customer_email": {},
	"receiver_bank":  {},
	"account_name":   {},
	"account_number": {},
	"authorization":  {},
	"last4":          {},
	"bin":            {},
	"signature":      {},
	"card_type":      {},
	"exp_month":      {},
	"exp_year":       {},
	"log":            {},
}

func SanitizeWebhookPayload(body []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body, err
	}
	redactSensitive(value)
	return json.Marshal(value)
}

func redactSensitive(value any) {
	switch x := value.(type) {
	case map[string]any:
		for key, child := range x {
			if _, sensitive := redactedKeys[strings.ToLower(key)]; sensitive {
				x[key] = "[redacted]"
				continue
			}
			redactSensitive(child)
		}
	case []any:
		for i := range x {
			redactSensitive(x[i])
		}
	}
}

func ValidatePayment(tx Transaction, accountID string, amount int64, currency, expectedPlanCode, expectedPlan string) bool {
	if tx.Status != "success" {
		return false
	}
	if tx.Reference == "" || tx.Amount != amount || tx.Currency != currency {
		return false
	}
	metaAccount, _ := tx.Metadata["account_id"].(string)
	metaPlan, _ := tx.Metadata["plan"].(string)
	if metaAccount != accountID || metaPlan != expectedPlan {
		return false
	}
	if expectedPlanCode != "" && tx.PlanObject.Code != expectedPlanCode {
		return false
	}
	return true
}

func ValidateChargeContext(ctx EventContext, accountID string, amount int64, currency, expectedPlanCode string) bool {
	if ctx.TransactionStatus != "success" {
		return false
	}
	if ctx.Amount != amount || ctx.Currency != currency {
		return false
	}
	if ctx.AccountID != accountID {
		return false
	}
	if expectedPlanCode != "" && ctx.PlanCode != expectedPlanCode {
		return false
	}
	return true
}

func ValidateSubscriptionContext(ctx EventContext, amount int64, currency, expectedPlanCode string) bool {
	if ctx.SubscriptionCode == "" || ctx.EmailToken == "" {
		return false
	}
	if ctx.Amount != amount || ctx.Currency != currency {
		return false
	}
	return expectedPlanCode == "" || ctx.PlanCode == expectedPlanCode
}

func MapSubscriptionStatus(providerStatus string) (string, bool) {
	switch providerStatus {
	case "active":
		return "active", true
	case "non-renewing":
		return "non_renewing", true
	case "cancelled", "canceled":
		return "canceled", true
	case "complete", "completed":
		return "complete", true
	case "attention":
		return "past_due", true
	default:
		return "", false
	}
}

func unmarshalObject(raw jsontext.Value) (map[string]any, bool) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, false
	}
	return obj, true
}

func nestedMap(obj map[string]any, key string) map[string]any {
	if v, ok := obj[key].(map[string]any); ok {
		return v
	}
	return nil
}

func firstString(obj map[string]any, key string) string {
	if v, ok := obj[key].(string); ok {
		return v
	}
	return ""
}

func firstBool(obj map[string]any, key string) bool {
	if v, ok := obj[key].(bool); ok {
		return v
	}
	return false
}

func firstInt64(obj map[string]any, key string) int64 {
	switch v := obj[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

func planCodeFrom(obj map[string]any) string {
	if plan := nestedMap(obj, "plan_object"); plan != nil {
		return firstString(plan, "plan_code")
	}
	if plan := nestedMap(obj, "plan"); plan != nil {
		return firstString(plan, "plan_code")
	}
	return ""
}

func customerCodeFrom(obj map[string]any) string {
	if customer := nestedMap(obj, "customer"); customer != nil {
		return firstString(customer, "customer_code")
	}
	return ""
}

func subscriptionCodeFrom(obj map[string]any) string {
	if code := firstString(obj, "subscription_code"); code != "" {
		return code
	}
	if subscription := nestedMap(obj, "subscription"); subscription != nil {
		return firstString(subscription, "subscription_code")
	}
	return ""
}

func parsePaystackTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		utc := t.UTC()
		return &utc
	}
	return nil
}

func ParseTime(raw string) *time.Time {
	return parsePaystackTime(raw)
}
