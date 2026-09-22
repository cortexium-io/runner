package engine

import (
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestAdmissionUnknownTokenBasisOnlyBlocksTokenBudgets(t *testing.T) {
	now := time.Now().UTC()
	cost := .25
	for _, cache := range []int64{0, 40} {
		attempt := metrics.Attempt{Completed: true, Event: metrics.Event{StartedAt: now, Harness: "unknown",
			Usage: metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 100, CacheReadInputTokens: cache, ReportedCostUSD: &cost}}}
		for _, budget := range []*config.AdmissionBudgetConfig{
			{WindowSeconds: 60, MaxAttempts: 2}, {WindowSeconds: 60, MaxHarnessSeconds: 10},
			{WindowSeconds: 60, MaxReportedCostUSD: floatPtr(.5)},
		} {
			d := EvaluateAdmission(budget, []metrics.Attempt{attempt}, now)
			if !d.Allowed || d.UsageCoveredAttempts != 0 || d.CostCoveredAttempts != 1 || d.ReportedCostUSD == nil || *d.ReportedCostUSD != cost {
				t.Fatalf("unrelated budget blocked or cost lost: %+v", d)
			}
		}
		if d := EvaluateAdmission(&config.AdmissionBudgetConfig{WindowSeconds: 60, MaxReportedTokens: 1000}, []metrics.Attempt{attempt}, now); d.Allowed {
			t.Fatal("unknown units admitted by token budget")
		}
	}
}

// Only recorded counters are copied here, not the private assessments. This
// regression cannot launch a harness or reopen the permanently stopped run.
func TestSavedComparisonCountersUseOneInclusiveTotal(t *testing.T) {
	now := time.Now().UTC()
	observations := []struct{ input, cache, output, total int64 }{
		{62618, 40320, 911, 63529}, {63542, 40832, 920, 64462},
		{63235, 19712, 837, 64072}, {61723, 39936, 879, 62602},
	}
	var attempts []metrics.Attempt
	for _, o := range observations {
		raw := metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: o.input, CacheReadInputTokens: o.cache, OutputTokens: o.output}
		u := normalizedEvalUsage(raw, config.HarnessCodexCLI)
		if total, ok := metrics.ReportedTokens(u); !ok || total != o.total {
			t.Fatalf("total=%d, known=%t, want=%d", total, ok, o.total)
		}
		attempts = append(attempts, metrics.Attempt{Completed: true, Event: metrics.Event{Harness: config.HarnessCodexCLI, StartedAt: now, Usage: raw}})
	}
	d := EvaluateAdmission(&config.AdmissionBudgetConfig{WindowSeconds: 60, MaxReportedTokens: 300000}, attempts, now)
	s := metrics.Summarize(attempts)
	if !d.Allowed || d.ReportedTokens != 254665 || s.ReportedTokens == nil || *s.ReportedTokens != d.ReportedTokens || s.Usage.InputTokens != 251118 || s.Usage.OutputTokens != 3547 || s.Usage.CacheReadInputTokens != 140800 {
		t.Fatalf("admission=%+v summary=%+v", d, s)
	}
	if legacy := d.ReportedTokens + s.Usage.CacheReadInputTokens; legacy != 395465 {
		t.Fatalf("regression fixture drift: %d", legacy)
	}
}
