package config

import "strings"

// RecommendedReasoning is a setup suggestion, never a runtime fallback or a
// capability claim. Explicit operator settings always take precedence.
func RecommendedReasoning(harness, model string) string {
	model = strings.TrimSpace(model)
	if harness == HarnessPiCLI {
		// Pi identifies models as provider/model. Unknown providers/models retain
		// a modest suggestion; their native catalog remains authoritative.
		_, model, _ = strings.Cut(model, "/")
	}
	switch model {
	case "gpt-6-sol", "gpt-6-luna":
		return "high"
	case "gpt-6-astra", "claude-opus-5-5":
		return "medium"
	}
	if harness == HarnessCodexCLI {
		return "high"
	}
	return "medium"
}
