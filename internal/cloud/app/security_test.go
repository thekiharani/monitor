package app

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sessionTTLForTest = 336 * time.Hour

func TestKeyPrefixDoesNotLeakSecret(t *testing.T) {
	const secret = "usk_live_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-"
	prefix := keyPrefix(secret, "usk_live_")

	if !strings.HasPrefix(prefix, "usk_live_") {
		t.Fatalf("prefix=%q, want the environment marker", prefix)
	}
	body := strings.TrimPrefix(prefix, "usk_live_")
	if len(body) > 4 {
		t.Fatalf("prefix exposes %d characters of the secret, want at most 4", len(body))
	}
	if len(prefix) >= len(secret) {
		t.Fatal("prefix is not shorter than the secret")
	}
}

func TestCSRFCookieOutlivesTheBrowserSession(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := setCSRF(rec, sessionTTLForTest); err != nil {
		t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies=%d, want 1", len(cookies))
	}
	c := cookies[0]
	if c.Name != "__Host-csrf" {
		t.Fatalf("name=%q", c.Name)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("MaxAge=%d: a session cookie dies before the session it guards", c.MaxAge)
	}
	if !c.Secure || c.Path != "/" || c.Domain != "" {
		t.Fatalf("cookie does not satisfy the __Host- prefix rules: %+v", c)
	}
	if c.HttpOnly {
		t.Fatal("the CSRF cookie must be readable by the dashboard client")
	}
}

func TestClearCSRFExpiresTheCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	clearCSRF(rec)
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("clearCSRF did not expire the cookie: %+v", cookies)
	}
}

func TestBodyHasKeyDistinguishesNullFromAbsent(t *testing.T) {
	if !bodyHasKey([]byte(`{"title":null}`), "title") {
		t.Error("explicit null must count as present, or the field can never be cleared")
	}
	if bodyHasKey([]byte(`{"url":"https://example.com"}`), "title") {
		t.Error("an absent key must not count as present")
	}
	if bodyHasKey([]byte(`not json`), "title") {
		t.Error("unparseable body must report absent")
	}
}
