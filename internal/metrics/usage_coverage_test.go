package metrics

import "testing"

func TestUsageCoverageAggregation(t *testing.T) {
	complete := Usage{Available: true, Coverage: UsageComplete, InputTokens: 12}
	cost := 0.2
	for _, test := range []struct {
		name        string
		left, right Usage
		want        string
		tokens      int64
	}{
		{"identity", Usage{}, complete, UsageComplete, 12},
		{"two complete", complete, complete, UsageComplete, 24},
		{"missing first invocation", Usage{Coverage: UsageUnavailable}, complete, UsagePartial, 12},
		{"missing second invocation", complete, Usage{Coverage: UsageUnavailable}, UsagePartial, 12},
		{"partial", complete, Usage{Available: true, Coverage: UsagePartial, InputTokens: 3}, UsagePartial, 15},
		{"historical", Usage{Available: true, InputTokens: 4}, complete, UsageUnknown, 16},
		{"cost without tokens", Usage{Coverage: UsageComplete, ReportedCostUSD: &cost}, complete, UsagePartial, 12},
		{"tokens without cost", complete, Usage{Coverage: UsageComplete, ReportedCostUSD: &cost}, UsagePartial, 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.left.Add(test.right)
			if got.CoverageStatus() != test.want || got.InputTokens != test.tokens || ValidateUsage(got) != nil {
				t.Fatalf("aggregate: %+v", got)
			}
		})
	}
}

func TestUsageCoverageRoundTripAndSummary(t *testing.T) {
	store := NewStore(t.TempDir() + "/metrics/usage.jsonl")
	for _, test := range []struct {
		id    string
		usage Usage
	}{
		{"complete", Usage{Available: true, Coverage: UsageComplete, InputTokens: 12}},
		{"partial", Usage{Available: true, Coverage: UsagePartial, InputTokens: 3}},
		{"missing", Usage{Coverage: UsageUnavailable}},
	} {
		if err := store.Append(Event{Kind: EventCompleted, AttemptID: test.id, Usage: test.usage}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(history.Attempts)
	if summary.CompleteUsageAttempts != 1 || summary.PartialUsageAttempts != 1 || summary.UsageCoveredAttempts != 2 || summary.Usage.InputTokens != 15 || summary.Usage.Coverage != UsagePartial {
		t.Fatalf("summary: %+v", summary)
	}
}
