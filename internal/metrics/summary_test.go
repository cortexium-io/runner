package metrics

import "testing"

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
