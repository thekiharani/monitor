package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostChecksPreserveLocalAccessAndStatusRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "/failure", http.StatusFound)
		case "/failure":
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, tc := range []struct {
		path    string
		up      bool
		status  int
		message string
	}{
		{"/", true, 200, ""},
		{"/redirect", true, 302, ""},
		{"/failure", false, 503, "returning HTTP 503"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			result := RunHost(context.Background(), client, server.URL+tc.path)
			if result.Up != tc.up || result.Status != tc.status || result.Error != tc.message {
				t.Fatalf("result=%+v", result)
			}
		})
	}
	result := Run(context.Background(), Spec{Kind: KindHTTP, URL: server.URL, PublicOnly: true})
	if result.Up || result.Error != "pointing at a private address" {
		t.Fatalf("Cloud public-only result=%+v", result)
	}
}

func TestHostTCPChecksReachLocalServices(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := RunHost(context.Background(), http.DefaultClient, "tcp://"+listener.Addr().String())
	if !result.Up || result.Error != "" {
		t.Fatalf("result=%+v", result)
	}
}
