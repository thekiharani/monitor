package usage

import (
	"slices"
	"testing"
)

func TestLimitsForUnknownPlanFallsBackToFree(t *testing.T) {
	free := LimitsFor("free")
	for _, plan := range []string{"", "enterprise", "FREE", "pro "} {
		if got := LimitsFor(plan); got != free {
			t.Errorf("LimitsFor(%q)=%+v, want the free limits", plan, got)
		}
	}
}

func TestPaidPlansAreStrictlyMoreGenerous(t *testing.T) {
	order := CustomerPlans
	for i := 1; i < len(order); i++ {
		lower, higher := LimitsFor(order[i-1]), LimitsFor(order[i])
		if higher.MaxMonitors <= lower.MaxMonitors {
			t.Errorf("%s monitors (%d) not above %s (%d)", order[i], higher.MaxMonitors, order[i-1], lower.MaxMonitors)
		}
		if higher.MinIntervalSeconds >= lower.MinIntervalSeconds {
			t.Errorf("%s interval (%d) not below %s (%d)", order[i], higher.MinIntervalSeconds, order[i-1], lower.MinIntervalSeconds)
		}
		if higher.RetentionDays <= lower.RetentionDays {
			t.Errorf("%s retention (%d) not above %s (%d)", order[i], higher.RetentionDays, order[i-1], lower.RetentionDays)
		}
		if higher.MonthlyAPIRequests <= lower.MonthlyAPIRequests {
			t.Errorf("%s quota (%d) not above %s (%d)", order[i], higher.MonthlyAPIRequests, order[i-1], lower.MonthlyAPIRequests)
		}
		if higher.RequestsPerSecond <= lower.RequestsPerSecond {
			t.Errorf("%s rate (%v) not above %s (%v)", order[i], higher.RequestsPerSecond, order[i-1], lower.RequestsPerSecond)
		}
	}
}

func TestControlProfileHasNoResourceAllowance(t *testing.T) {
	control := LimitsFor("control")
	if control.MaxMonitors != 0 || control.MonthlyAPIRequests != 0 || control.MaxAPIKeys != 0 || control.MaxStatusPages != 0 {
		t.Fatalf("the control profile grants resources: %+v", control)
	}
	if control.RequestsPerSecond <= 0 || control.Burst <= 0 {
		t.Fatalf("the control profile must still rate limit: %+v", control)
	}
}

func TestCustomerPlansAreExactlyTheBillablePlans(t *testing.T) {
	for _, plan := range CustomerPlans {
		if _, ok := plans[plan]; !ok {
			t.Errorf("CustomerPlans lists %q, which has no limits", plan)
		}
	}
	for plan := range plans {
		if plan == "control" {
			continue
		}
		if !slices.Contains(CustomerPlans, plan) {
			t.Errorf("plan %q has limits but is missing from CustomerPlans, so retention will never prune it", plan)
		}
	}
}

func TestLongestRetentionCoversEveryPlan(t *testing.T) {
	longest := LongestRetention()
	for _, plan := range CustomerPlans {
		if days := LimitsFor(plan).RetentionDays; days > longest {
			t.Errorf("%s keeps %d days, past the %d-day ceiling", plan, days, longest)
		}
	}
}

func TestEveryPlanCanMonitorAlertAndPublish(t *testing.T) {
	for _, plan := range CustomerPlans {
		limits := LimitsFor(plan)
		if limits.MaxMonitors < 1 || limits.MaxAlertChannels < 1 || limits.MaxStatusPages < 1 || limits.MaxRegions < 1 {
			t.Errorf("%s cannot use the core product: %+v", plan, limits)
		}
	}
}
