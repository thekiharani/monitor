package control

import (
	"strings"
	"testing"
)

func TestNewControlToken(t *testing.T) {
	first, err := newControlToken()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newControlToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "nmon_ctl_") {
		t.Fatalf("token missing prefix: %q", first)
	}
	if len(first) <= len("nmon_ctl_") || len(first) > 96 {
		t.Fatalf("unexpected token length %d", len(first))
	}
	if first == second {
		t.Fatalf("tokens must be unique")
	}
	if len(first) < 24 {
		t.Fatalf("token too short for key_prefix: %d", len(first))
	}
}

func TestValidateMintName(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"prod ops", "prod ops", true},
		{"  billing-operator  ", "billing-operator", true},
		{"", "", false},
		{"   ", "", false},
		{strings.Repeat("a", 64), strings.Repeat("a", 64), true},
		{strings.Repeat("a", 65), "", false},
	} {
		got, err := validateMintName(tt.in)
		if (err == nil) != tt.ok {
			t.Fatalf("validateMintName(%q) ok=%v want %v (err=%v)", tt.in, err == nil, tt.ok, err)
		}
		if tt.ok && got != tt.want {
			t.Fatalf("validateMintName(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestValidateMintReason(t *testing.T) {
	for _, tt := range []struct {
		in string
		ok bool
	}{
		{"ab", false},
		{"", false},
		{"valid reason", true},
		{"  provisioning on-call operator  ", true},
		{strings.Repeat("x", 1000), true},
		{strings.Repeat("x", 1001), false},
	} {
		if _, err := validateMintReason(tt.in); (err == nil) != tt.ok {
			t.Fatalf("validateMintReason(%q) ok=%v want %v (err=%v)", tt.in, err == nil, tt.ok, err)
		}
	}
}
