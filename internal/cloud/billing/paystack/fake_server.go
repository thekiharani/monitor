package paystack

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

type FakeServer struct {
	Server         *httptest.Server
	Plans          map[string]Plan
	Verify         map[string]Transaction
	Subscriptions  map[string]Subscription
	mu             sync.Mutex
	LastInitialize struct {
		Currency string
		Amount   int64
		Channels []string
		Plan     string
	}
	Created struct {
		Customer  string
		Plan      string
		StartDate string
	}
}

func NewFakeServer() *FakeServer {
	fs := &FakeServer{
		Plans:         make(map[string]Plan),
		Verify:        make(map[string]Transaction),
		Subscriptions: make(map[string]Subscription),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/plan/", func(w http.ResponseWriter, r *http.Request) {
		plan, ok := fs.Plans[strings.TrimPrefix(r.URL.Path, "/plan/")]
		if !ok {
			writePaystackErr(w, "plan not found")
			return
		}
		writePaystackOK(w, plan)
	})
	mux.HandleFunc("/transaction/initialize", func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Currency string   `json:"currency"`
			Amount   int64    `json:"amount"`
			Channels []string `json:"channels"`
			Plan     string   `json:"plan"`
		}
		_ = json.UnmarshalRead(r.Body, &request)
		fs.mu.Lock()
		fs.LastInitialize.Currency = request.Currency
		fs.LastInitialize.Amount = request.Amount
		fs.LastInitialize.Channels = request.Channels
		fs.LastInitialize.Plan = request.Plan
		fs.mu.Unlock()
		writePaystackOK(w, InitializeResult{AuthorizationURL: "https://paystack.test/checkout", AccessCode: "access", Reference: "ref"})
	})
	mux.HandleFunc("/transaction/verify/", func(w http.ResponseWriter, r *http.Request) {
		ref := strings.TrimPrefix(r.URL.Path, "/transaction/verify/")
		tx, ok := fs.Verify[ref]
		if !ok {
			writePaystackErr(w, "not found")
			return
		}
		writePaystackOK(w, tx)
	})
	mux.HandleFunc("/subscription", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var request struct {
				Customer  string `json:"customer"`
				Plan      string `json:"plan"`
				StartDate string `json:"start_date"`
			}
			if err := json.UnmarshalRead(r.Body, &request); err != nil {
				writePaystackErr(w, "invalid request")
				return
			}
			fs.mu.Lock()
			fs.Created.Customer = request.Customer
			fs.Created.Plan = request.Plan
			fs.Created.StartDate = request.StartDate
			fs.mu.Unlock()
			writePaystackOK(w, CreatedSubscription{SubscriptionCode: "SUB_created", EmailToken: "token"})
			return
		}
		customerID := r.URL.Query().Get("customer")
		subscriptions := make([]Subscription, 0)
		for _, sub := range fs.Subscriptions {
			if customerID == "" || customerID == fmt.Sprintf("%d", sub.Customer.ID) {
				subscriptions = append(subscriptions, sub)
			}
		}
		writePaystackOK(w, subscriptions)
	})
	mux.HandleFunc("/subscription/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/subscription/")
		if strings.HasSuffix(path, "/manage/link") {
			writePaystackOK(w, map[string]string{"link": "https://paystack.test/manage"})
			return
		}
		sub, ok := fs.Subscriptions[path]
		if !ok {
			writePaystackErr(w, "not found")
			return
		}
		writePaystackOK(w, sub)
	})
	mux.HandleFunc("/subscription/disable", func(w http.ResponseWriter, r *http.Request) {
		writePaystackOK(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/subscription/enable", func(w http.ResponseWriter, r *http.Request) {
		writePaystackOK(w, map[string]string{"status": "ok"})
	})
	fs.Server = httptest.NewServer(mux)
	return fs
}

func (fs *FakeServer) Close() {
	fs.Server.Close()
}

func (fs *FakeServer) URL() string {
	return fs.Server.URL
}

func (fs *FakeServer) CreatedSubscription() (customer, plan, startDate string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.Created.Customer, fs.Created.Plan, fs.Created.StartDate
}

func (fs *FakeServer) LastInitializeChannels() (channels []string, plan string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.LastInitialize.Channels...), fs.LastInitialize.Plan
}

func writePaystackOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]any{"status": true, "message": "ok", "data": data})
}

func writePaystackErr(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]any{"status": false, "message": message})
}

func (fs *FakeServer) LastInitializeCurrency() (string, int64) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.LastInitialize.Currency, fs.LastInitialize.Amount
}
