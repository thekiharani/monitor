package httpx

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewErrorFallsBackToInternal(t *testing.T) {
	if got := NewError("rate_limited"); got.Status != http.StatusTooManyRequests {
		t.Fatalf("status=%d", got.Status)
	}
	unknown := NewError("no_such_code")
	if unknown.Code != "internal_error" || unknown.Status != http.StatusInternalServerError {
		t.Fatalf("unknown code mapped to %+v", unknown)
	}
}

func TestWriteErrorIncludesRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), RequestIDKey, "req-123"))
	rec := httptest.NewRecorder()

	WriteError(rec, req, NewError("invalid_request"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"]["request_id"] != "req-123" {
		t.Fatalf("request_id=%v: support cannot correlate a report without it", body["error"]["request_id"])
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content-type=%q", rec.Header().Get("Content-Type"))
	}
}

func TestConstantTimeEqual(t *testing.T) {
	if !ConstantTimeEqual("token", "token") {
		t.Error("identical values compared unequal")
	}
	if ConstantTimeEqual("token", "tokeN") {
		t.Error("different values compared equal")
	}
	if ConstantTimeEqual("token", "") {
		t.Error("empty value compared equal")
	}
	if ConstantTimeEqual("", "") != true {
		t.Error("two empty values should compare equal")
	}
}

func TestSafeRequestID(t *testing.T) {
	for value, want := range map[string]bool{
		"0192f0c1-3b2a-7000-8000-0242ac120002": true,
		"abcDEF-123_x":                         true,
		"short":                                false,
		"has space here":                       false,
		"has/slash/here":                       false,
		"":                                     false,
		strings.Repeat("a", 129):               false,
		strings.Repeat("a", 128):               true,
	} {
		if got := SafeRequestID(value); got != want {
			t.Errorf("SafeRequestID(%q)=%v, want %v", value, got, want)
		}
	}
}

func TestNewRequestIDIsAcceptedBySafeRequestID(t *testing.T) {
	id := NewRequestID()
	if !SafeRequestID(id) {
		t.Fatalf("generated id %q fails its own validator", id)
	}
}

func TestHostMatches(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "Monitor.Noria.Co.KE"

	if !HostMatches(req, "https://monitor.noria.co.ke") {
		t.Error("host comparison must be case-insensitive")
	}
	if !HostMatches(req, "https://other.example", "https://monitor.noria.co.ke/") {
		t.Error("a trailing slash on the origin must not defeat the match")
	}
	if HostMatches(req, "https://api.monitor.noria.co.ke") {
		t.Error("a different host matched")
	}
	if HostMatches(req) {
		t.Error("no origins should never match")
	}
}

func TestRFC3339(t *testing.T) {
	if RFC3339(nil) != nil {
		t.Error("nil time must render as JSON null")
	}
	at := time.Date(2026, 9, 2, 10, 30, 0, 0, time.UTC)
	if got := RFC3339(&at); got != "2026-09-02T10:30:00Z" {
		t.Fatalf("got %v", got)
	}
}
