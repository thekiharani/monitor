package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorIncludesTypeAndContext(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	Error(logger, "database read failed", errors.New("connection refused"), "component", "billing_api", "operation", "read_subscription")
	record := output.String()
	for _, expected := range []string{`"msg":"database read failed"`, `"component":"billing_api"`, `"operation":"read_subscription"`, `"error":"connection refused"`, `"error_type":"*errors.errorString"`} {
		if !strings.Contains(record, expected) {
			t.Fatalf("log record missing %q: %s", expected, record)
		}
	}
}

func TestRequestAttrsExcludeSecretsAndQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "https://example.test/api/dashboard/v1/billing?token=secret", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Cookie", "session=secret")
	attrs := RequestAttrs(r)
	for _, attr := range attrs {
		if value, ok := attr.(string); ok && strings.Contains(value, "secret") {
			t.Fatalf("request attrs leaked secret: %#v", attrs)
		}
	}
	for i := 0; i+1 < len(attrs); i += 2 {
		if attrs[i] == "path" && attrs[i+1] != "/api/dashboard/v1/billing" {
			t.Fatalf("unexpected request path: %#v", attrs[i+1])
		}
	}
}
