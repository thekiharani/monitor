package paystack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

func TestFakeServerVerifyTransaction(t *testing.T) {
	fs := NewFakeServer()
	defer fs.Close()

	cfg := config.Config{
		PaystackSecretKey: "secret",
		PaystackPlanCode:  "PLN_test",
		PaystackCurrency:  "KES",
		ProPriceMinor:     50000,
	}
	fs.Verify["bil-1"] = Transaction{
		Status:    "success",
		Reference: "bil-1",
		Amount:    50000,
		Currency:  "KES",
		Metadata:  map[string]any{"account_id": "acc", "plan": "pro"},
		PlanObject: struct {
			Code string `json:"plan_code"`
		}{Code: "PLN_test"},
	}

	client := NewWithBaseURL(cfg, fs.URL())
	tx, err := client.Verify(context.Background(), "bil-1")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidatePayment(tx, "acc", 50000, "KES", "PLN_test", "pro") {
		t.Fatal("expected valid payment")
	}
}

func TestInitializeRejectsUnsafeHostedURL(t *testing.T) {
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePaystackOK(w, InitializeResult{AuthorizationURL: "javascript:alert(1)"})
	}))
	client := NewWithBaseURL(config.Config{PaystackSecretKey: "secret"}, server.URL)
	if _, err := client.Initialize(context.Background(), "person@example.test", "bil-1", "https://app.example.test/callback", "acc-1", "chk-1"); err == nil {
		t.Fatal("expected unsafe hosted URL to be rejected")
	}
}

func TestFakeServerFetchSubscription(t *testing.T) {
	fs := NewFakeServer()
	defer fs.Close()
	cfg := config.Config{PaystackSecretKey: "secret", PaystackPlanCode: "PLN_test", PaystackCurrency: "KES", ProPriceMinor: 50000}
	fs.Subscriptions["SUB_x"] = Subscription{SubscriptionCode: "SUB_x", Status: "cancelled"}

	client := NewWithBaseURL(cfg, fs.URL())
	sub, err := client.FetchSubscription(context.Background(), "SUB_x")
	if err != nil {
		t.Fatal(err)
	}
	status, ok := MapSubscriptionStatus(sub.Status)
	if !ok || status != "canceled" {
		t.Fatalf("status=%q ok=%v", status, ok)
	}
}

func TestListSubscriptionsUsesNumericCustomerID(t *testing.T) {
	fs := NewFakeServer()
	defer fs.Close()
	wanted := Subscription{SubscriptionCode: "SUB_wanted", Status: "active"}
	wanted.Customer.ID = 42
	other := Subscription{SubscriptionCode: "SUB_other", Status: "active"}
	other.Customer.ID = 7
	fs.Subscriptions[wanted.SubscriptionCode] = wanted
	fs.Subscriptions[other.SubscriptionCode] = other

	client := NewWithBaseURL(config.Config{PaystackSecretKey: "secret"}, fs.URL())
	subscriptions, err := client.ListSubscriptions(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 1 || subscriptions[0].SubscriptionCode != wanted.SubscriptionCode {
		t.Fatalf("subscriptions=%+v", subscriptions)
	}
}
