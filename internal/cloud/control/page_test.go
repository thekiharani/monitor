package control

import (
	"errors"
	"testing"
)

func TestParsePage(t *testing.T) {
	limit, offset, err := parsePage(0, 0)
	if err != nil || limit != 10 || offset != 0 {
		t.Fatalf("defaults limit=%d offset=%d err=%v", limit, offset, err)
	}
	limit, offset, err = parsePage(50, 100)
	if err != nil || limit != 50 || offset != 100 {
		t.Fatalf("page limit=%d offset=%d err=%v", limit, offset, err)
	}
	if _, _, err = parsePage(10, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid got %v", err)
	}
	if _, _, err = parsePage(10, 100_001); !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for large offset got %v", err)
	}
}

func TestValidateAccountTransition(t *testing.T) {
	for _, test := range []struct {
		name        string
		before      string
		after       string
		wantInvalid bool
	}{
		{name: "active to suspended", before: "active", after: "suspended"},
		{name: "suspended to active", before: "suspended", after: "active"},
		{name: "active to closed", before: "active", after: "closed"},
		{name: "closed retry", before: "closed", after: "closed"},
		{name: "closed to active", before: "closed", after: "active", wantInvalid: true},
		{name: "closed to suspended", before: "closed", after: "suspended", wantInvalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateAccountTransition(test.before, test.after)
			if errors.Is(err, ErrInvalid) != test.wantInvalid {
				t.Fatalf("validateAccountTransition(%q, %q) err=%v", test.before, test.after, err)
			}
		})
	}
}
