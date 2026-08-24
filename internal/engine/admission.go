package engine

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

type AdmissionDecision struct {
	Configured             bool                          `json:"configured"`
	Allowed                bool                          `json:"allowed"`
	Reason                 string                        `json:"reason,omitempty"`
	WindowStart            time.Time                     `json:"window_start,omitempty"`
	NextEvaluationAt       time.Time                     `json:"next_evaluation_at,omitempty"`
	Attempts               int                           `json:"attempts"`
	CompletedAttempts      int                           `json:"completed_attempts"`
	UsageCoveredAttempts   int                           `json:"usage_covered_attempts"`
	CostCoveredAttempts    int                           `json:"cost_covered_attempts"`
	HarnessDurationSeconds int64                         `json:"harness_duration_seconds"`
	ReportedTokens         int64                         `json:"reported_tokens"`
	ReportedCostUSD        *float64                      `json:"reported_cost_usd,omitempty"`
	RemainingAttempts      int                           `json:"remaining_attempts,omitempty"`
	Budget                 *config.AdmissionBudgetConfig `json:"budget,omitempty"`
}

func EvaluateAdmission(budget *config.AdmissionBudgetConfig, attempts []metrics.Attempt, now time.Time) AdmissionDecision {
	decision := AdmissionDecision{Allowed: true}
	if budget == nil {
		return decision
	}
	copy := *budget
	decision.Configured = true
	decision.Budget = &copy
	decision.WindowStart = now.Add(-time.Duration(budget.WindowSeconds) * time.Second)
	windowAttempts := make([]metrics.Attempt, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.StartedAt.IsZero() || attempt.StartedAt.Before(decision.WindowStart) || attempt.StartedAt.After(now) {
			continue
		}
		windowAttempts = append(windowAttempts, attempt)
		reevaluateAt := attempt.StartedAt.Add(time.Duration(budget.WindowSeconds) * time.Second)
		if decision.NextEvaluationAt.IsZero() || reevaluateAt.Before(decision.NextEvaluationAt) {
			decision.NextEvaluationAt = reevaluateAt
		}
	}
	decision.Attempts = len(windowAttempts)
	var harnessDurationMilliseconds int64
	for _, attempt := range windowAttempts {
		if !attempt.Completed {
			continue
		}
		decision.CompletedAttempts++
		if attempt.HarnessDurationMilliseconds < 0 {
			return decision.deny("cannot verify admission budget because metrics history contains a negative harness duration")
		}
		var ok bool
		harnessDurationMilliseconds, ok = addNonNegative(harnessDurationMilliseconds, attempt.HarnessDurationMilliseconds)
		if !ok {
			return decision.deny("cannot verify admission budget because harness duration overflowed")
		}
		if err := metrics.ValidateUsage(attempt.Usage); err != nil {
			return decision.deny("cannot verify admission budget because metrics history contains invalid usage")
		}
		if attempt.Usage.Available {
			decision.UsageCoveredAttempts++
			reported, ok := reportedTokens(attempt.Usage)
			if !ok {
				return decision.deny("cannot verify admission budget because reported token usage overflowed")
			}
			decision.ReportedTokens, ok = addNonNegative(decision.ReportedTokens, reported)
			if !ok {
				return decision.deny("cannot verify admission budget because reported token usage overflowed")
			}
		}
		if attempt.Usage.ReportedCostUSD != nil {
			decision.CostCoveredAttempts++
			value := *attempt.Usage.ReportedCostUSD
			if decision.ReportedCostUSD != nil {
				value += *decision.ReportedCostUSD
			}
			if math.IsInf(value, 0) || math.IsNaN(value) {
				return decision.deny("cannot verify admission budget because reported cost overflowed")
			}
			decision.ReportedCostUSD = &value
		}
	}
	decision.HarnessDurationSeconds = harnessDurationMilliseconds / 1000
	if budget.MaxAttempts > 0 {
		decision.RemainingAttempts = budget.MaxAttempts - decision.Attempts
		if decision.RemainingAttempts < 0 {
			decision.RemainingAttempts = 0
		}
		if decision.Attempts >= budget.MaxAttempts {
			return decision.deny(fmt.Sprintf("rolling admission budget reached %d attempts", budget.MaxAttempts))
		}
	}
	if budget.MaxHarnessSeconds > 0 && decision.HarnessDurationSeconds >= budget.MaxHarnessSeconds {
		return decision.deny(fmt.Sprintf("rolling admission budget reached %d harness seconds", budget.MaxHarnessSeconds))
	}
	if budget.MaxHarnessSeconds > 0 && decision.CompletedAttempts != decision.Attempts {
		return decision.deny(fmt.Sprintf("cannot verify harness-time budget because %d attempt(s) are unfinished", decision.Attempts-decision.CompletedAttempts))
	}
	if budget.MaxReportedTokens > 0 {
		if decision.UsageCoveredAttempts != decision.Attempts {
			return decision.deny(fmt.Sprintf("cannot verify reported-token budget because %d attempt(s) are unfinished or lack token usage", decision.Attempts-decision.UsageCoveredAttempts))
		}
		if decision.ReportedTokens >= budget.MaxReportedTokens {
			return decision.deny(fmt.Sprintf("rolling admission budget reached %d reported tokens", budget.MaxReportedTokens))
		}
	}
	if budget.MaxReportedCostUSD != nil {
		if decision.CostCoveredAttempts != decision.Attempts {
			return decision.deny(fmt.Sprintf("cannot verify reported-cost budget because %d attempt(s) are unfinished or lack cost usage", decision.Attempts-decision.CostCoveredAttempts))
		}
		if decision.ReportedCostUSD != nil && *decision.ReportedCostUSD >= *budget.MaxReportedCostUSD {
			return decision.deny(fmt.Sprintf("rolling admission budget reached $%.4f reported cost", *budget.MaxReportedCostUSD))
		}
	}
	return decision
}

func reportedTokens(usage metrics.Usage) (int64, bool) {
	total, ok := addNonNegative(usage.InputTokens, usage.CacheReadInputTokens)
	if !ok {
		return 0, false
	}
	total, ok = addNonNegative(total, usage.CacheWriteInputTokens)
	if !ok {
		return 0, false
	}
	return addNonNegative(total, usage.OutputTokens)
}

func addNonNegative(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func (d AdmissionDecision) Summary() string {
	if !d.Configured {
		return "admission budget not configured"
	}
	if d.Allowed {
		return "admission budget has capacity"
	}
	summary := strings.TrimSpace(d.Reason)
	if !d.NextEvaluationAt.IsZero() {
		summary += "; next rolling-window evaluation at " + d.NextEvaluationAt.Local().Format(time.RFC3339)
	}
	return summary
}

func (d AdmissionDecision) deny(reason string) AdmissionDecision {
	d.Allowed = false
	d.Reason = strings.TrimSpace(reason)
	return d
}

func (s *Engine) SetMetricsHistoryReader(reader func() (metrics.ReadResult, error)) {
	s.readMetricsHistory = reader
}

func (s *Engine) AdmissionStatus(now time.Time) (AdmissionDecision, error) {
	if s.cfg.AdmissionBudget == nil {
		return AdmissionDecision{Allowed: true}, nil
	}
	if s.readMetricsHistory == nil {
		return AdmissionDecision{}, errors.New("admission budget requires the local metrics history reader")
	}
	s.admissionMu.Lock()
	metricsErr := s.admissionMetricsErr
	s.admissionMu.Unlock()
	if metricsErr != "" {
		return AdmissionDecision{}, fmt.Errorf("admission budget history became incomplete after a metrics write failure: %s", metricsErr)
	}
	history, err := s.readMetricsHistory()
	if err != nil {
		return AdmissionDecision{}, fmt.Errorf("read admission-budget history: %w", err)
	}
	if history.MalformedRecords > 0 {
		return AdmissionDecision{}, fmt.Errorf("admission budget history contains %d malformed record(s)", history.MalformedRecords)
	}
	decision := EvaluateAdmission(s.cfg.AdmissionBudget, history.Attempts, now)
	if decision.Allowed && s.observeMetrics == nil {
		return AdmissionDecision{}, errors.New("admission budget requires the local metrics observer")
	}
	return decision, nil
}

func (s *Engine) recordAdmissionMetricsError(err error) {
	if err == nil || s.cfg.AdmissionBudget == nil {
		return
	}
	s.admissionMu.Lock()
	if s.admissionMetricsErr == "" {
		s.admissionMetricsErr = err.Error()
	}
	s.admissionMu.Unlock()
}

func (s *Engine) recordAdmissionDecision(decision AdmissionDecision) {
	s.admissionMu.Lock()
	s.lastAdmission = decision
	s.admissionMu.Unlock()
}

func (s *Engine) LastAdmissionDecision() AdmissionDecision {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	return s.lastAdmission
}
