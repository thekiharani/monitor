package domain

import "testing"

func TestApplicationOwnedValuesRejectUnknownValues(t *testing.T) {
	if !IsPlan(PlanPro) || IsPlan("enterprise") {
		t.Fatal("plan vocabulary is not enforced in code")
	}
	if !IsCredentialAudience(CredentialAudienceControlAPI) || IsCredentialAudience("public") {
		t.Fatal("credential audience vocabulary is not enforced in code")
	}
}

func TestWorkspaceAndBillingPoliciesMatchTheDocumentedRoleTable(t *testing.T) {
	cases := []struct {
		role                        string
		members, workspace, billing bool
	}{
		{MembershipRoleOwner, true, true, true},
		{MembershipRoleAdmin, true, true, false},
		{MembershipRoleMember, false, false, false},
		{"", false, false, false},
	}
	for _, c := range cases {
		if got := CanManageMembers(c.role); got != c.members {
			t.Errorf("CanManageMembers(%q)=%v, want %v", c.role, got, c.members)
		}
		if got := CanManageWorkspace(c.role); got != c.workspace {
			t.Errorf("CanManageWorkspace(%q)=%v, want %v", c.role, got, c.workspace)
		}
		if got := CanManageBilling(c.role); got != c.billing {
			t.Errorf("CanManageBilling(%q)=%v, want %v", c.role, got, c.billing)
		}
	}
}
