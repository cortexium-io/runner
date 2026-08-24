package config

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const maxAdmissionDurationSeconds = int64((time.Duration(1<<63 - 1)) / time.Second)

// AdmissionBudgetConfig limits new agent attempts over a rolling local-history
// window. Ceilings are checked before claims; Runner never cancels an in-flight
// harness or skips Agent QA to remain under a ceiling.
type AdmissionBudgetConfig struct {
	WindowSeconds      int64    `json:"window_seconds"`
	MaxAttempts        int      `json:"max_attempts,omitempty"`
	MaxHarnessSeconds  int64    `json:"max_harness_seconds,omitempty"`
	MaxReportedTokens  int64    `json:"max_reported_tokens,omitempty"`
	MaxReportedCostUSD *float64 `json:"max_reported_cost_usd,omitempty"`
}

func validateAdmissionBudget(budget *AdmissionBudgetConfig) error {
	if budget == nil {
		return nil
	}
	if budget.WindowSeconds <= 0 {
		return errors.New("admission_budget.window_seconds must be positive")
	}
	if budget.WindowSeconds > maxAdmissionDurationSeconds || budget.MaxHarnessSeconds > maxAdmissionDurationSeconds {
		return fmt.Errorf("admission_budget time values cannot exceed %d seconds", maxAdmissionDurationSeconds)
	}
	if budget.MaxAttempts < 0 || budget.MaxHarnessSeconds < 0 || budget.MaxReportedTokens < 0 {
		return errors.New("admission_budget ceilings cannot be negative")
	}
	if budget.MaxReportedCostUSD != nil && (*budget.MaxReportedCostUSD <= 0 || math.IsNaN(*budget.MaxReportedCostUSD) || math.IsInf(*budget.MaxReportedCostUSD, 0)) {
		return errors.New("admission_budget.max_reported_cost_usd must be a finite positive number")
	}
	if budget.MaxAttempts == 0 && budget.MaxHarnessSeconds == 0 && budget.MaxReportedTokens == 0 && budget.MaxReportedCostUSD == nil {
		return fmt.Errorf("admission_budget must configure at least one ceiling")
	}
	return nil
}
