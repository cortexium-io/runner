package runner

import "testing"

func TestReleaseReserveIncludesSharedChildAttempts(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Process.MaxAttempts = 64
	r.Attempts = make([]Attempt, 6)
	route := r.Process.Steps[r.Step].Routes["ready"]
	if err := releaseApprovalReserve(r, "ready", route, 54); err != nil {
		t.Fatal("exact shared-family reserve rejected", err)
	}
	if err := releaseApprovalReserve(r, "ready", route, 55); err == nil {
		t.Fatal("local attempt count hid child consumption before approval")
	}
	if err := releaseApprovalReserve(r, "ready", route); err != nil {
		t.Fatal("legacy local-only call changed", err)
	}
	r.Step = ReleaseBudgetTestStep(r, "stop")
	if err := releaseStopReserve(r, 56); err != nil {
		t.Fatal("exact shared Stop reserve rejected", err)
	}
	if err := releaseStopReserve(r, 57); err == nil {
		t.Fatal("Stop ignored child attempt consumption")
	}
	r.Attempts = append(r.Attempts, Attempt{Step: r.Step, Visit: r.Visit, State: NotStarted})
	if err := releaseNotStartedStopReserve(r, 57); err != nil {
		t.Fatal("allocated Stop was counted twice in shared reserve", err)
	}
	if err := releaseNotStartedStopReserve(r, 58); err == nil {
		t.Fatal("undispatched Stop bypassed shared reserve")
	}
}

func TestReleaseReserveRejectsInvalidSharedAttemptAccounting(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Attempts = make([]Attempt, 6)
	route := r.Process.Steps[r.Step].Routes["ready"]
	for _, counts := range [][]int{{-1}, {5}, {6, 7}} {
		if err := releaseApprovalReserve(r, "ready", route, counts...); err == nil {
			t.Fatal("invalid shared attempt accounting accepted", counts)
		}
	}
}
