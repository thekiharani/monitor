package paystack

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

func TestVerifySignatureHexAndBase64(t *testing.T) {
	body := []byte(`{"event":"charge.success"}`)
	secret := "secret"
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	if !VerifySignature(secret, body, []byte(hex.EncodeToString(expected))) {
		t.Fatal("hex signature rejected")
	}
}

func TestInitializeOneTimeDoesNotAttachPlan(t *testing.T) {
	var request map[string]any
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			t.Fatal(err)
		}
		writePaystackOK(w, InitializeResult{AuthorizationURL: "https://checkout.paystack.com/test", AccessCode: "access", Reference: "ref"})
	}))
	client := NewWithBaseURL(config.Config{PaystackSecretKey: "secret", PaystackCurrency: "KES"}, server.URL, WithHTTPClient(server.Client()))
	if _, err := client.InitializeOneTime(context.Background(), "customer@example.test", "ref", "https://app.example.test/callback", "account", "checkout", "pro", 162500); err != nil {
		t.Fatal(err)
	}
	if _, exists := request["plan"]; exists {
		t.Fatalf("one-time upgrade unexpectedly included plan: %#v", request)
	}
	if request["amount"] != float64(162500) {
		t.Fatalf("amount=%v", request["amount"])
	}
}
func TestInitializeOneTimeChannelsRestrictsCheckout(t *testing.T) {
	var request map[string]any
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.UnmarshalRead(r.Body, &request); err != nil {
			t.Fatal(err)
		}
		writePaystackOK(w, InitializeResult{AuthorizationURL: "https://checkout.paystack.com/test", AccessCode: "access", Reference: "ref"})
	}))
	client := NewWithBaseURL(config.Config{PaystackSecretKey: "secret", PaystackCurrency: "KES"}, server.URL, WithHTTPClient(server.Client()))
	if _, err := client.InitializeOneTimeChannels(context.Background(), "customer@example.test", "ref", "https://app.example.test/callback", "account", "checkout", "pro", 50000, []string{"mobile_money"}); err != nil {
		t.Fatal(err)
	}
	if _, exists := request["plan"]; exists {
		t.Fatalf("channel-restricted checkout unexpectedly included plan: %#v", request)
	}
	channels, ok := request["channels"].([]any)
	if !ok || len(channels) != 1 || channels[0] != "mobile_money" {
		t.Fatalf("channels=%#v", request["channels"])
	}
}
