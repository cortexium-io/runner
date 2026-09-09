package engine

import (
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestReviewObservationsExcludeEvidenceGapsAndPreferences(t *testing.T) {
	review := execution.ReviewAssessment{
		Criteria: []execution.ReviewCriterionResult{
			{Status: "failed", Summary: "Writes begin before hydration."},
			{Status: "blocked", Summary: "The report was unavailable."},
			{Status: "passed", Summary: "Navigation works."},
		},
		Rules: []execution.ReviewRuleResult{{Status: "failed", Findings: []execution.ReviewRuleFinding{
			{Severity: "blocking", Summary: "Tenant ownership check omitted."},
			{Severity: "warning", Summary: "Prefer another variable name."},
		}}},
		Maintainability: execution.ReviewMaintainabilityResult{Status: "blocked", Summary: "Unknown"},
	}
	want := []metrics.ReviewFinding{{Area: "acceptance", Summary: "Writes begin before hydration."},
		{Area: "repository_rules", Summary: "Tenant ownership check omitted."}}
	if got := reviewFindingObservations(review); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
