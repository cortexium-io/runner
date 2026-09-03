package engine

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestEvaluateAdmissionAppliesRollingAttemptCapacity(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	budget := &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 2}
	attempts := []metrics.Attempt{
		{Event: metrics.Event{AttemptID: "old", StartedAt: now.Add(-2 * time.Hour)}},
		{Event: metrics.Event{AttemptID: "current", StartedAt: now.Add(-time.Minute)}},
	}
	decision := EvaluateAdmission(budget, attempts, now)
	if !decision.Allowed || decision.Attempts != 1 || decision.RemainingAttempts != 1 {
		t.Fatalf("unexpected admission decision: %#v", decision)
	}
	attempts = append(attempts, metrics.Attempt{Event: metrics.Event{AttemptID: "current_2", StartedAt: now.Add(-30 * time.Second)}})
	decision = EvaluateAdmission(budget, attempts, now)
	if decision.Allowed || !strings.Contains(decision.Reason, "2 attempts") || decision.NextEvaluationAt.IsZero() {
		t.Fatalf("attempt ceiling did not pause admission: %#v", decision)
	}
}

func TestEvaluateAdmissionFailsClosedWhenReportedUsageIsMissing(t *testing.T) {
	now := time.Now().UTC()
	attempt := metrics.Attempt{Event: metrics.Event{AttemptID: "attempt", StartedAt: now.Add(-time.Minute)}, Completed: true}
	for _, budget := range []*config.AdmissionBudgetConfig{
		{WindowSeconds: 3600, MaxReportedTokens: 1000},
		{WindowSeconds: 3600, MaxReportedCostUSD: floatPtr(1)},
	} {
		decision := EvaluateAdmission(budget, []metrics.Attempt{attempt}, now)
		if decision.Allowed || !strings.Contains(decision.Reason, "cannot verify") {
			t.Fatalf("missing usage did not fail closed: %#v", decision)
		}
	}
}

func TestEvaluateAdmissionFailsClosedWhenUsageBasedHistoryIsUnfinished(t *testing.T) {
	now := time.Now().UTC()
	unfinished := metrics.Attempt{Event: metrics.Event{AttemptID: "unfinished", StartedAt: now.Add(-time.Minute)}}
	for _, budget := range []*config.AdmissionBudgetConfig{
		{WindowSeconds: 3600, MaxHarnessSeconds: 60},
		{WindowSeconds: 3600, MaxReportedTokens: 1000},
		{WindowSeconds: 3600, MaxReportedCostUSD: floatPtr(1)},
	} {
		decision := EvaluateAdmission(budget, []metrics.Attempt{unfinished}, now)
		if decision.Allowed || !strings.Contains(decision.Reason, "unfinished") {
			t.Fatalf("unfinished usage history did not fail closed: budget=%#v decision=%#v", budget, decision)
		}
	}
}

func TestEvaluateAdmissionAppliesDurationTokenAndCostCeilings(t *testing.T) {
	now := time.Now().UTC()
	cost := 0.75
	attempt := metrics.Attempt{Event: metrics.Event{
		AttemptID: "attempt", StartedAt: now.Add(-time.Minute), HarnessDurationMilliseconds: 120_000,
		Usage: metrics.Usage{Available: true, InputTokens: 100, CacheReadInputTokens: 50, OutputTokens: 25, ReportedCostUSD: &cost},
	}, Completed: true}
	tests := []*config.AdmissionBudgetConfig{
		{WindowSeconds: 3600, MaxHarnessSeconds: 120},
		{WindowSeconds: 3600, MaxReportedTokens: 175},
		{WindowSeconds: 3600, MaxReportedCostUSD: floatPtr(0.75)},
	}
	for _, budget := range tests {
		if decision := EvaluateAdmission(budget, []metrics.Attempt{attempt}, now); decision.Allowed {
			t.Fatalf("ceiling did not pause admission: budget=%#v decision=%#v", budget, decision)
		}
	}
}

func TestEvaluateAdmissionDoesNotLoseSubsecondHarnessDuration(t *testing.T) {
	now := time.Now().UTC()
	attempts := []metrics.Attempt{
		{Event: metrics.Event{AttemptID: "one", StartedAt: now.Add(-time.Minute), HarnessDurationMilliseconds: 600}, Completed: true},
		{Event: metrics.Event{AttemptID: "two", StartedAt: now.Add(-30 * time.Second), HarnessDurationMilliseconds: 600}, Completed: true},
	}
	decision := EvaluateAdmission(&config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxHarnessSeconds: 1}, attempts, now)
	if decision.Allowed || decision.HarnessDurationSeconds != 1 {
		t.Fatalf("subsecond durations bypassed admission ceiling: %#v", decision)
	}
}

func TestEvaluateAdmissionFailsClosedForInvalidOrOverflowingUsage(t *testing.T) {
	now := time.Now().UTC()
	attempt := func(id string, duration int64, usage metrics.Usage) metrics.Attempt {
		return metrics.Attempt{Event: metrics.Event{AttemptID: id, StartedAt: now.Add(-time.Minute), HarnessDurationMilliseconds: duration, Usage: usage}, Completed: true}
	}
	tests := []struct {
		name     string
		budget   *config.AdmissionBudgetConfig
		attempts []metrics.Attempt
	}{
		{name: "negative duration", budget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxHarnessSeconds: 60}, attempts: []metrics.Attempt{attempt("one", -1, metrics.Usage{})}},
		{name: "duration overflow", budget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxHarnessSeconds: 60}, attempts: []metrics.Attempt{attempt("one", math.MaxInt64, metrics.Usage{}), attempt("two", 1, metrics.Usage{})}},
		{name: "negative tokens", budget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxReportedTokens: 100}, attempts: []metrics.Attempt{attempt("one", 0, metrics.Usage{Available: true, InputTokens: -1})}},
		{name: "token overflow", budget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxReportedTokens: 100}, attempts: []metrics.Attempt{attempt("one", 0, metrics.Usage{Available: true, InputTokens: math.MaxInt64, OutputTokens: 1})}},
		{name: "cost overflow", budget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxReportedCostUSD: floatPtr(1)}, attempts: []metrics.Attempt{attempt("one", 0, metrics.Usage{ReportedCostUSD: floatPtr(math.MaxFloat64)}), attempt("two", 0, metrics.Usage{ReportedCostUSD: floatPtr(math.MaxFloat64)})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := EvaluateAdmission(test.budget, test.attempts, now)
			if decision.Allowed || !strings.Contains(decision.Reason, "cannot verify") {
				t.Fatalf("invalid metrics did not fail closed: %#v", decision)
			}
		})
	}
}

func TestAdmissionStatusFailsClosedForMalformedOrUnwritableHistory(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{AdmissionBudget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 2}}}
	service.SetMetricsObserver(func(metrics.Event) error { return nil })
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
		return metrics.ReadResult{MalformedRecords: 1}, nil
	})
	if _, err := service.AdmissionStatus(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed history did not fail closed: %v", err)
	}

	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) { return metrics.ReadResult{}, nil })
	service.SetMetricsObserver(func(metrics.Event) error { return errors.New("disk full") })
	if err := service.observeMetrics(metrics.Event{Kind: metrics.EventStarted, AttemptID: "attempt"}); err == nil {
		t.Fatal("failing metrics observer unexpectedly succeeded")
	}
	if _, err := service.AdmissionStatus(time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "write failure") {
		t.Fatalf("unwritable history did not fail closed: %v", err)
	}
}

func TestAdmissionStatusReusesHistoryUntilAttemptStateChanges(t *testing.T) {
	service := &Engine{cfg: config.RuntimeConfig{AdmissionBudget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 2}}}
	service.SetMetricsObserver(func(metrics.Event) error { return nil })
	reads := 0
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
		reads++
		return metrics.ReadResult{}, nil
	})
	now := time.Now().UTC()
	if _, err := service.AdmissionStatus(now); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdmissionStatus(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Fatalf("unchanged admission history was read %d times, want 1", reads)
	}
	if err := service.observeMetrics(metrics.Event{Kind: metrics.EventStarted, AttemptID: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AdmissionStatus(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Fatalf("new attempt did not invalidate admission history: reads=%d", reads)
	}
}

func floatPtr(value float64) *float64 { return &value }
