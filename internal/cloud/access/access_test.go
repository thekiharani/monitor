package access

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thekiharani/monitor/internal/cloud/domain"
)

func TestBearer(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer abc":       "abc",
		"bearer abc":       "abc",
		"BEARER abc":       "abc",
		"  Bearer   abc  ": "abc",
		"Basic abc":        "",
		"abc":              "",
		"Bearer a b":       "",
		"":                 "",
	} {
		if got := Bearer(header); got != want {
			t.Errorf("Bearer(%q)=%q, want %q", header, got, want)
		}
	}
}

func TestCapabilitiesReturnsACopy(t *testing.T) {
	first, ok := Capabilities("platform.viewer")
	if !ok || len(first) == 0 {
		t.Fatal("platform.viewer should exist and be non-empty")
	}
	first[0] = "mutated"

	second, _ := Capabilities("platform.viewer")
	if second[0] == "mutated" {
		t.Fatal("Capabilities handed out the shared backing array; a caller can rewrite the permission set")
	}
	if _, ok := Capabilities("no.such.set"); ok {
		t.Fatal("an unknown permission set reported as known")
	}
}

func TestKnownCapabilitiesIsSortedAndDeduplicated(t *testing.T) {
	items := KnownCapabilities()
	if len(items) == 0 {
		t.Fatal("no capabilities")
	}
	if !slices.IsSorted(items) {
		t.Fatalf("not sorted: %v", items)
	}
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if _, dup := seen[item]; dup {
			t.Fatalf("duplicate capability %q", item)
		}
		seen[item] = struct{}{}
		if !strings.HasPrefix(item, "platform.") {
			t.Errorf("capability %q is not platform-scoped", item)
		}
	}
	if slices.Contains(items, PlatformEverything) {
		t.Fatal("the wildcard capability is grantable through the API")
	}
}

func TestPermissionSetsOnlyUseKnownCapabilities(t *testing.T) {
	known := KnownCapabilities()
	for name, set := range PermissionSets {
		if len(set) == 0 {
			t.Errorf("permission set %q is empty", name)
		}
		for _, capability := range set {
			if !slices.Contains(known, capability) {
				t.Errorf("permission set %q grants unknown capability %q", name, capability)
			}
		}
	}
}

func TestPlatformOwnerSupersedesEveryOtherSet(t *testing.T) {
	owner, ok := Capabilities("platform.owner")
	if !ok {
		t.Fatal("platform.owner missing")
	}
	for name, set := range PermissionSets {
		if name == "platform.owner" {
			continue
		}
		for _, capability := range set {
			if !slices.Contains(owner, capability) {
				t.Errorf("platform.owner is missing %q, held by %q", capability, name)
			}
		}
	}
}

func TestViewerSetIsReadOnly(t *testing.T) {
	viewer, _ := Capabilities("platform.viewer")
	mutating := []string{AccountsSuspend, AccountsClose, MonitorsModerate, ProbesManage, BillingReconcile, CredentialsRevoke, JobsRetry, AccessGrant}
	for _, capability := range viewer {
		if slices.Contains(mutating, capability) {
			t.Errorf("platform.viewer grants the mutating capability %q", capability)
		}
	}
}

func TestCredentialExpiry(t *testing.T) {
	now := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	if got := CredentialExpiry(domain.PrincipalKindService, now); !got.Equal(now.Add(90 * 24 * time.Hour)) {
		t.Errorf("service expiry=%v", got)
	}
	human := CredentialExpiry(domain.PrincipalKindHuman, now)
	if !human.Equal(now.Add(12 * time.Hour)) {
		t.Errorf("human expiry=%v", human)
	}
	if !human.Before(CredentialExpiry(domain.PrincipalKindService, now)) {
		t.Error("a human credential must be shorter-lived than a workload one")
	}
}

func TestScopeHelpers(t *testing.T) {
	if got := PlatformScope(); got.Type != ScopePlatform || got.ID != "" {
		t.Fatalf("platform scope=%+v", got)
	}
	if got := AccountScope("acct-1"); got.Type != ScopeAccount || got.ID != "acct-1" {
		t.Fatalf("account scope=%+v", got)
	}
	if nullableScopeID(PlatformScope()) != nil {
		t.Fatal("a platform grant must store a NULL scope_id")
	}
	if nullableScopeID(AccountScope("acct-1")) != "acct-1" {
		t.Fatal("an account grant must store its scope id")
	}
}

func TestPlatformOwnerProvisioningGrantsExactlyThePlatformOwnerSet(t *testing.T) {
	script, err := os.ReadFile("../../../cloud/db/provision/platform_owner.sql")
	if err != nil {
		t.Fatal(err)
	}
	granted := regexp.MustCompile(`\('(platform\.[a-z.]+)'\)`).FindAllStringSubmatch(string(script), -1)
	got := make([]string, 0, len(granted))
	for _, match := range granted {
		got = append(got, match[1])
	}
	want := slices.Clone(PermissionSets["platform.owner"])
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("platform_owner.sql grants %v, want %v", got, want)
	}
}
