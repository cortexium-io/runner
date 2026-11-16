package runner

import "testing"

func TestReleaseReserveIncludesSharedChildAttempts(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Process.MaxAttempts = 64
	r.Attempts = make([]Attempt, 6)
	route := r.Process.Steps[r.Step].Routes["ready"]
	if err := releaseApprovalReserve(r, true, "ready", route, 54); err != nil {
		t.Fatal("exact shared-family reserve rejected", err)
	}
	if err := releaseApprovalReserve(r, true, "ready", route, 55); err == nil {
		t.Fatal("local attempt count hid child consumption before approval")
	}
	if err := releaseApprovalReserve(r, true, "ready", route); err != nil {
		t.Fatal("legacy local-only call changed", err)
	}
	r.Step = ReleaseBudgetTestStep(r, "stop")
	if err := releaseStopReserve(r, true, 56); err != nil {
		t.Fatal("exact shared Stop reserve rejected", err)
	}
	if err := releaseStopReserve(r, true, 57); err == nil {
		t.Fatal("Stop ignored child attempt consumption")
	}
	r.Attempts = append(r.Attempts, Attempt{Step: r.Step, Visit: r.Visit, State: NotStarted})
	if err := releaseNotStartedStopReserve(r, true, 57); err != nil {
		t.Fatal("allocated Stop was counted twice in shared reserve", err)
	}
	if err := releaseNotStartedStopReserve(r, true, 58); err == nil {
		t.Fatal("undispatched Stop bypassed shared reserve")
	}
}

func TestReleaseReserveRejectsInvalidSharedAttemptAccounting(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Attempts = make([]Attempt, 6)
	route := r.Process.Steps[r.Step].Routes["ready"]
	for _, counts := range [][]int{{-1}, {5}, {6, 7}} {
		if err := releaseApprovalReserve(r, true, "ready", route, counts...); err == nil {
			t.Fatal("invalid shared attempt accounting accepted", counts)
		}
	}
}

func TestReleaseWorkLimitsDisabledKeepProtectedSuffixAndRouteBounds(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Process.MaxAttempts = 1
	r.Attempts = make([]Attempt, 2)
	route := r.Process.Steps[r.Step].Routes["ready"]
	if err := releaseApprovalReserve(r, false, "ready", route, 10); err != nil {
		t.Fatal("dormant aggregate cap blocked the protected approval", err)
	}
	if err := releaseApprovalReserve(r, true, "ready", route, 10); err == nil {
		t.Fatal("explicit aggregate enforcement was ignored")
	}
	if err := releaseApprovalReserve(r, false, "ready", route, invalidFamilyAttempts); err == nil {
		t.Fatal("disabled capacity arithmetic hid invalid family accounting")
	}
	r.Step = ReleaseBudgetTestStep(r, "stop")
	deploy := ReleaseBudgetTestStep(r, "deploy")
	retry := r.Process.Steps[deploy].Routes["retry_safe"]
	retry.MaxTimes = 0
	r.Process.Steps[deploy].Routes["retry_safe"] = retry
	if err := releaseStopReserve(r, false, 10); err == nil {
		t.Fatal("disabled global limit admitted an unbounded protected retry edge")
	}
}
