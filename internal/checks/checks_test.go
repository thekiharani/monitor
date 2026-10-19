package checks

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func target(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("all systems operational")) })
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	mux.HandleFunc("/moved", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusFound) })
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("X-Token") != "secret" || r.Header.Get("User-Agent") != userAgent {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestHTTPChecksJudgeStatusRedirectsAndKeywords(t *testing.T) {
	server := target(t)
	for _, tc := range []struct {
		name string
		spec Spec
		up   bool
		err  string
		code int
	}{
		{"a 2xx is up", Spec{Kind: KindHTTP, URL: server.URL + "/ok"}, true, "", 200},
		{"a 5xx is down", Spec{Kind: KindHTTP, URL: server.URL + "/broken"}, false, "returning HTTP 503", 503},
		{"an expected 5xx is up", Spec{Kind: KindHTTP, URL: server.URL + "/broken", ExpectedStatuses: []int{503}}, true, "", 503},
		{"an unfollowed redirect counts as its own status", Spec{Kind: KindHTTP, URL: server.URL + "/moved"}, true, "", 302},
		{"a redirect outside the expected codes is down", Spec{Kind: KindHTTP, URL: server.URL + "/moved", ExpectedStatuses: []int{200}}, false, "returning HTTP 302", 302},
		{"a followed redirect lands on the target", Spec{Kind: KindHTTP, URL: server.URL + "/moved", FollowRedirects: true, ExpectedStatuses: []int{200}}, true, "", 200},
		{"a redirect loop gives up", Spec{Kind: KindHTTP, URL: server.URL + "/loop", FollowRedirects: true}, false, "redirecting too many times", 0},
		{"a present keyword is up", Spec{Kind: KindKeyword, URL: server.URL + "/ok", Keyword: "operational"}, true, "", 200},
		{"a missing keyword is down", Spec{Kind: KindKeyword, URL: server.URL + "/ok", Keyword: "outage"}, false, `missing "outage"`, 200},
		{"a keyword that must be absent is down when present", Spec{Kind: KindKeyword, URL: server.URL + "/ok", Keyword: "operational", KeywordAbsent: true}, false, `containing "operational"`, 200},
		{"method, headers and body are sent", Spec{Kind: KindHTTP, URL: server.URL + "/echo", Method: http.MethodPost, Headers: map[string]string{"X-Token": "secret"}, Body: "{}", ExpectedStatuses: []int{201}}, true, "", 201},
		{"a slow target times out", Spec{Kind: KindHTTP, URL: server.URL + "/slow", Timeout: 200 * time.Millisecond}, false, "not responding", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Run(context.Background(), tc.spec)
			if got.Up != tc.up || got.Error != tc.err || got.StatusCode != tc.code {
				t.Fatalf("up=%v err=%q code=%d, want up=%v err=%q code=%d", got.Up, got.Error, got.StatusCode, tc.up, tc.err, tc.code)
			}
			if tc.up && (got.TotalMs <= 0 || got.TTFBMs <= 0 || got.ConnectMs <= 0) {
				t.Errorf("timings missing on an up check: %+v", got)
			}
		})
	}
}

func TestHTTPSChecksReportTheCertificateAndRejectAnUntrustedOne(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()

	got := Run(context.Background(), Spec{Kind: KindHTTP, URL: server.URL})
	if got.Up || !strings.Contains(got.Error, "unknown authority") {
		t.Fatalf("a self-signed certificate passed: %+v", got)
	}
}

func TestTCPChecksConnectOrReportRefusal(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	open := listener.Addr().String()
	if got := Run(context.Background(), Spec{Kind: KindTCP, URL: "tcp://" + open}); !got.Up || got.ConnectMs <= 0 {
		t.Fatalf("open port: %+v", got)
	}
	listener.Close()
	if got := Run(context.Background(), Spec{Kind: KindTCP, URL: "tcp://" + open}); got.Up || got.Error != "refusing connections" {
		t.Fatalf("closed port: %+v", got)
	}
}

func TestPublicOnlyRefusesPrivateTargetsAtDialTime(t *testing.T) {
	server := target(t)
	for _, spec := range []Spec{
		{Kind: KindHTTP, URL: server.URL + "/ok", PublicOnly: true},
		{Kind: KindTCP, URL: "tcp://" + strings.TrimPrefix(server.URL, "http://"), PublicOnly: true},
		{Kind: KindHTTP, URL: "http://169.254.169.254/latest/meta-data/", PublicOnly: true},
	} {
		if got := Run(context.Background(), spec); got.Up || got.Error != "pointing at a private address" {
			t.Errorf("%s reached a private address: %+v", spec.URL, got)
		}
	}
}

func TestPublicAddrClassifiesReservedRanges(t *testing.T) {
	for raw, public := range map[string]bool{
		"8.8.8.8": true, "2606:4700:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.18.0.5": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.1.1": false, "::1": false, "fc00::1": false,
		"::ffff:127.0.0.1": false, "0.0.0.0": false, "203.0.113.9": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(raw)); got != public {
			t.Errorf("PublicAddr(%s)=%v, want %v", raw, got, public)
		}
	}
}

func TestNormalizeTargetAcceptsOnlyWhatTheKindCanCheck(t *testing.T) {
	for _, tc := range []struct {
		kind, raw, want string
		ok              bool
	}{
		{KindHTTP, " https://example.com/health ", "https://example.com/health", true},
		{KindKeyword, "http://example.com", "http://example.com", true},
		{KindHTTP, "ftp://example.com", "", false},
		{KindHTTP, "https://user:pass@example.com", "", false},
		{KindHTTP, "example.com", "", false},
		{KindTCP, "db.example.com:5432", "tcp://db.example.com:5432", true},
		{KindTCP, "tcp://db.example.com:5432", "tcp://db.example.com:5432", true},
		{KindTCP, "db.example.com", "", false},
		{KindTCP, "db.example.com:99999", "", false},
		{KindTCP, "https://db.example.com:443", "", false},
	} {
		got, err := NormalizeTarget(tc.kind, tc.raw)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("NormalizeTarget(%s, %q)=%q,%v want %q ok=%v", tc.kind, tc.raw, got, err, tc.want, tc.ok)
		}
	}
}
