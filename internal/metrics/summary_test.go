package metrics

import "testing"

func TestSummarySeparatesRecordedReviewVerdictsFromAttemptOutcomes(t *testing.T) {
	attempts := []Attempt{
		{Completed: true, Event: Event{Outcome: "blocked", ReviewVerdict: "accept", FailureOperation: "publication_create_pull_request"}},
		{Completed: true, Event: Event{Outcome: "rejected", ReviewVerdict: "needs_changes"}},
		{Completed: true, Event: Event{Outcome: "blocked", ReviewVerdict: "needs_changes"}}, // Exhausted QA is still a rejection.
		{Completed: true, Event: Event{Outcome: "needs_input", ReviewVerdict: "blocked"}},
		{Completed: true, Event: Event{Outcome: "succeeded", Role: "reviewer"}},        // Legacy record: do not infer acceptance.
		{Completed: true, Event: Event{Outcome: "succeeded", ResumedCheckpoint: true}}, // Publication replay is not a new review.
		{Event: Event{ReviewVerdict: "accept"}},                                        // An unfinished attempt cannot supply a completed verdict.
	}
	summary := Summarize(attempts)
	if summary.ReviewVerdictCoveredAttempts != 4 || summary.ReviewAcceptedAttempts != 1 || summary.ReviewChangesRequestedAttempts != 2 || summary.ReviewBlockedAttempts != 1 {
		t.Fatalf("review verdicts were inferred, lost, or replaced by attempt outcomes: %#v", summary)
	}
	if summary.CompletedAttempts != 6 || summary.SucceededAttempts != 2 || summary.BlockedAttempts != 3 || summary.ResumedCheckpointAttempts != 1 {
		t.Fatalf("review accounting changed existing attempt accounting: %#v", summary)
	}
}

func TestSummaryRetainsFailedStagesInsideSuccessWithoutDoubleCountingUsage(t *testing.T) {
	cost := 0.25
	attempts := []Attempt{
		{Completed: true, Event: Event{Outcome: "succeeded", DurationMilliseconds: 1200, HarnessDurationMilliseconds: 900,
			PublicationAttempts: 2, Usage: Usage{Available: true, InputTokens: 42, ReportedCostUSD: &cost}},
			Stages: []Stage{
				{Name: StageWorkspacePrepare, Completed: true, Outcome: StageOutcomeBlocked, DurationMilliseconds: 50},
				{Name: StageWorkspacePrepare, Completed: true, Outcome: StageOutcomeSucceeded, DurationMilliseconds: 60},
				{Name: StageHarnessRun, Completed: true, Outcome: StageOutcomeSucceeded, DurationMilliseconds: 900,
					Usage: Usage{Available: true, InputTokens: 42, ReportedCostUSD: &cost}},
			}},
		{Stages: []Stage{
			{Name: StageWorkspacePrepare, Completed: true, Outcome: StageOutcomeFailed, DurationMilliseconds: 70},
			{Name: StageHarnessRun},
		}},
		{Completed: true, Event: Event{Outcome: "succeeded"}}, // Historical attempt without stage coverage.
	}
	summary := Summarize(attempts)
	if summary.Attempts != 3 || summary.StageCoveredAttempts != 2 || summary.RecoveredStageFailureAttempts != 1 || summary.RecoveredPublicationAttempts != 1 {
		t.Fatalf("lost coverage or recovered failures: %#v", summary)
	}
	if summary.Usage.InputTokens != 42 || summary.Usage.ReportedCostUSD == nil || *summary.Usage.ReportedCostUSD != cost || summary.HarnessInvocations != 1 {
		t.Fatalf("stage usage doubled attempt usage or unfinished invocation counted as complete: %#v", summary)
	}
	if len(summary.Stages) != 2 || summary.Stages[0].Name != StageHarnessRun || summary.Stages[1].Name != StageWorkspacePrepare {
		t.Fatalf("stage totals are missing or unordered: %#v", summary.Stages)
	}
	prepare := summary.Stages[1]
	if prepare.Runs != 3 || prepare.Completed != 3 || prepare.Failed != 1 || prepare.Blocked != 1 || prepare.DurationMilliseconds != 180 {
		t.Fatalf("failed/unfinished attempt stage evidence was lost: %#v", prepare)
	}
	harness := summary.Stages[0]
	if harness.Runs != 2 || harness.Completed != 1 || harness.DurationMilliseconds != 900 || harness.UsageCoveredStages != 1 || harness.CostCoveredStages != 1 {
		t.Fatalf("unfinished stage fabricated time or coverage: %#v", harness)
	}
}
