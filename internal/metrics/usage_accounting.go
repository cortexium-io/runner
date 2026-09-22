package metrics

import (
	"errors"
	"math"
)

const (
	// Input includes cache reads/writes; output includes reasoning. Breakdowns
	// are not additional tokens. This is a reported count, not a billing unit.
	InclusiveInputV1     = "inclusive_input_v1"
	AccountingUnresolved = "unresolved"
	AccountingInvalid    = "invalid"
)

// NormalizeUsage is for a provider report or a historical leaf with its SAVED
// harness identity, never today's configuration or a mixed-harness aggregate.
// Historical homogeneous attempt totals use the same units as their harness.
// It never modifies the supplied maps/pointers or rewrites stored history.
func NormalizeUsage(u Usage, harness string) (Usage, error) {
	if err := ValidateUsage(u); err != nil {
		return invalidAccounting(u), err
	}
	u = cloneUsage(u)
	if u.TokenAccounting != "" {
		return u, nil
	}
	switch harness {
	case "codex": // Native input already includes cached input.
	case "claude", "pi": // Native input excludes both cache categories.
		input, ok := inclusiveInput(u.InputTokens, u.CacheReadInputTokens, u.CacheWriteInputTokens)
		if !ok {
			return invalidAccounting(u), errors.New("input token normalization overflow")
		}
		u.InputTokens = input
		for name, model := range u.Models {
			input, ok := inclusiveInput(model.InputTokens, model.CacheReadInputTokens, model.CacheWriteInputTokens)
			if !ok {
				return invalidAccounting(u), errors.New("model input token normalization overflow")
			}
			model.InputTokens = input
			u.Models[name] = model
		}
	default:
		if u.Available || len(u.Models) > 0 {
			u.TokenAccounting = AccountingUnresolved
		}
		return u, nil
	}
	u.TokenAccounting = InclusiveInputV1
	if err := ValidateUsage(u); err != nil {
		return invalidAccounting(u), err
	}
	return u, nil
}

// ReportedTokens is the single total used by admission, summaries and eval.
// Coverage is separate: a partial count is a known lower bound, not a complete
// spend. Unavailable/unknown-basis/invalid usage is never a known zero.
func ReportedTokens(u Usage) (int64, bool) {
	if !u.Available || u.TokenAccounting != InclusiveInputV1 || ValidateUsage(u) != nil {
		return 0, false
	}
	return addCounter(u.InputTokens, u.OutputTokens)
}

func addCounter(a, b int64) (int64, bool) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}

func inclusiveInput(input, read, write int64) (int64, bool) {
	cache, ok := addCounter(read, write)
	if !ok {
		return 0, false
	}
	return addCounter(input, cache)
}

func validateAccounting(u Usage) error {
	switch u.TokenAccounting {
	case "", AccountingUnresolved:
	case InclusiveInputV1:
		if !validTokenBreakdown(u.InputTokens, u.CacheReadInputTokens, u.CacheWriteInputTokens, u.OutputTokens) || u.ReasoningOutputTokens > u.OutputTokens {
			return errors.New("invalid inclusive token breakdown or total")
		}
		for _, model := range u.Models {
			if !validTokenBreakdown(model.InputTokens, model.CacheReadInputTokens, model.CacheWriteInputTokens, model.OutputTokens) {
				return errors.New("invalid inclusive model token breakdown or total")
			}
		}
	default:
		return errors.New("invalid token accounting")
	}
	return nil
}

func validTokenBreakdown(input, read, write, output int64) bool {
	cache, ok := addCounter(read, write)
	_, totalOK := addCounter(input, output)
	return ok && totalOK && cache <= input
}

func combinedAccounting(a, b Usage) string {
	// Cost-only/unavailable reports do not change token units; Add still marks
	// their missing token coverage partial when combined with a token report.
	if !a.Available && len(a.Models) == 0 {
		return b.TokenAccounting
	}
	if !b.Available && len(b.Models) == 0 {
		return a.TokenAccounting
	}
	if a.TokenAccounting == InclusiveInputV1 && b.TokenAccounting == InclusiveInputV1 {
		return InclusiveInputV1
	}
	// Once provenance is lost it cannot be restored by a later caller's harness.
	return AccountingUnresolved
}

func invalidAccounting(u Usage) Usage {
	u = cloneUsage(u)
	u.TokenAccounting = AccountingInvalid
	return u
}

func cloneUsage(u Usage) Usage {
	copyCost := func(cost *float64) *float64 {
		if cost == nil {
			return nil
		}
		value := *cost
		return &value
	}
	u.ReportedCostUSD = copyCost(u.ReportedCostUSD)
	if u.Models != nil {
		models := make(map[string]ModelUsage, len(u.Models))
		for name, model := range u.Models {
			model.ReportedCostUSD = copyCost(model.ReportedCostUSD)
			models[name] = model
		}
		u.Models = models
	}
	return u
}
