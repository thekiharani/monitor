package app

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/thekiharani/monitor/internal/cloud/config"
)

func withTrustedProxies(t *testing.T, values ...string) config.Config {
	t.Helper()
	cfg := testConfig()
	for _, value := range values {
		prefix, err := config.ParseTrustedProxy(value)
		if err != nil {
			t.Fatalf("ParseTrustedProxy(%q): %v", value, err)
		}
		cfg.TrustedProxies = append(cfg.TrustedProxies, prefix)
	}
	return cfg
}

func TestClientIPTrustsSingleProxy(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.1")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.112.96.1")

	if got := a.clientIPString(req); got != "203.0.113.7" {
		t.Fatalf("clientIP=%s, want 203.0.113.7", got)
	}
}

func TestClientIPPrefersConnectingIP(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.1")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("CF-Connecting-IP", "198.51.100.9")
	req.Header.Set("X-Forwarded-For", "70.0.0.1, 10.112.96.1")

	if got := a.clientIPString(req); got != "198.51.100.9" {
		t.Fatalf("clientIP=%s, want 198.51.100.9", got)
	}
}

func TestClientIPTrustsProxyByCIDR(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := a.clientIPString(req); got != "203.0.113.7" {
		t.Fatalf("clientIP=%s, want 203.0.113.7", got)
	}
}

func TestClientIPIgnoresHeadersWhenUntrusted(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "172.30.42.1")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("CF-Connecting-IP", "198.51.100.9")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := a.clientIPString(req); got != "10.112.96.1" {
		t.Fatalf("clientIP=%s, want 10.112.96.1", got)
	}
}

func TestClientIPNoProxyConfigured(t *testing.T) {
	a := &App{cfg: testConfig()}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := a.clientIPString(req); got != "10.112.96.1" {
		t.Fatalf("clientIP=%s, want 10.112.96.1", got)
	}
}

func TestClientIPRejectsSpoofedForwardedForPrefix(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.7, 10.112.96.1")

	if got := a.clientIPString(req); got != "203.0.113.7" {
		t.Fatalf("clientIP=%s, want 203.0.113.7 (spoofed 1.2.3.4 must be ignored)", got)
	}
}

func TestClientIPJoinsRepeatedForwardedForHeaders(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Add("X-Forwarded-For", "1.2.3.4")
	req.Header.Add("X-Forwarded-For", "203.0.113.7, 10.112.96.1")

	if got := a.clientIPString(req); got != "203.0.113.7" {
		t.Fatalf("clientIP=%s, want 203.0.113.7", got)
	}
}

func TestClientIPFallsBackWhenEveryHopIsTrusted(t *testing.T) {
	a := &App{cfg: withTrustedProxies(t, "10.112.96.0/24")}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.112.96.1:54321"
	req.Header.Set("X-Forwarded-For", "10.112.96.8, 10.112.96.1")

	if got := a.clientIPString(req); got != "10.112.96.1" {
		t.Fatalf("clientIP=%s, want the peer address", got)
	}
}

func TestClientIPUnmapsIPv4MappedPeer(t *testing.T) {
	a := &App{cfg: testConfig()}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "[::ffff:203.0.113.7]:54321"

	if got := a.clientIPString(req); got != "203.0.113.7" {
		t.Fatalf("clientIP=%s, want 203.0.113.7", got)
	}
}

func TestClientIPStringEmptyForUnparseablePeer(t *testing.T) {
	a := &App{cfg: testConfig()}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "pipe"

	if got := a.clientIPString(req); got != "" {
		t.Fatalf("clientIP=%q, want empty", got)
	}
	if got := a.clientIPValue(req); got != nil {
		t.Fatalf("clientIPValue=%v, want nil", got)
	}
}

func TestValidTrustedProxy(t *testing.T) {
	for value, want := range map[string]bool{
		"10.112.96.1":    true,
		"10.112.96.0/24": true,
		"172.30.42.0/16": true,
		"not-an-ip":      false,
		"10.112.96.999":  false,
		"":               false,
		"203.0.113.0/24": true,
		"::1":            true,
		"2001:db8::/32":  true,
	} {
		if got := config.ValidTrustedProxy(value); got != want {
			t.Fatalf("ValidTrustedProxy(%q)=%v, want %v", value, got, want)
		}
	}
}

func TestParseTrustedProxyMasksHostBits(t *testing.T) {
	prefix, err := config.ParseTrustedProxy("10.112.96.37/24")
	if err != nil {
		t.Fatal(err)
	}
	if want := netip.MustParsePrefix("10.112.96.0/24"); prefix != want {
		t.Fatalf("prefix=%v, want %v", prefix, want)
	}
}
