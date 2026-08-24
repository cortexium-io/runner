package engine

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/metrics"

	"github.com/cortexium-io/runner/internal/execution"
)

func TestFormatQAReportPublishesOnlyBoundedRunnerClassifications(t *testing.T) {
	assessment := execution.ReviewAssessment{
		Verdict: "accept",
		Summary: "This deliberately long free-form reviewer summary must not become the pull request body.",
		Criteria: []execution.ReviewCriterionResult{
			{
				Criterion: "source_acceptance_criteria", Status: "passed", Summary: "All criteria passed.",
				Evidence: []string{"go test ./... passed", "Browser checks completed\nwithout errors"},
			},
		},
		Rules: []execution.ReviewRuleResult{
			{
				RuleSourceID: "repository_instructions", RuleSourceVersion: "current", Status: "passed", Summary: "Repository rules passed.",
				Findings: []execution.ReviewRuleFinding{{Severity: "warning", Summary: "One observation.", Evidence: []string{"No acceptance criterion is affected."}}},
			},
		},
		Maintainability: execution.ReviewMaintainabilityResult{
			Status: "passed", Summary: "The change is maintainable.", Evidence: []string{"Responsibilities remain clear."},
		},
	}

	report := formatQAReport(assessment, []string{"go test ./...: passed", "git diff --check\npassed"}, metrics.Usage{})
	for _, expected := range []string{
		"**Runner QA classification:** Accepted",
		"Required criteria: 1 passed · 0 failed.",
		"Repository rules: 1 passed · 0 failed.",
		"Maintainability: passed.",
		"posted on issue-backed cards",
	} {
		if !strings.Contains(report, expected) {
			t.Fatalf("formatted report omitted %q:\n%s", expected, report)
		}
	}
	for _, raw := range []string{assessment.Summary, "All criteria passed", "Browser checks", "Repository rules passed", "git diff --check"} {
		if strings.Contains(report, raw) {
			t.Fatalf("formatted report exposed model-authored text %q:\n%s", raw, report)
		}
	}
}

func TestTitleIdentifierHandlesEmptySeparators(t *testing.T) {
	if got := titleIdentifier("---"); got != "Unknown" {
		t.Fatalf("titleIdentifier separators = %q, want Unknown", got)
	}
}

func TestFormatQACommentMakesRequiredChangesReadable(t *testing.T) {
	assessment := execution.ReviewAssessment{
		Verdict: "needs_changes", Summary: "One behavior still needs correction.",
		Criteria: []execution.ReviewCriterionResult{
			{Criterion: "Retries preserve the original job.", Status: "failed", Summary: "A retry creates a second job.", Evidence: []string{"Focused retry check returned two IDs."}},
			{Criterion: "Tenant boundaries remain intact.", Status: "passed", Summary: "Passed.", Evidence: []string{"Existing tenant test."}},
		},
		Maintainability: execution.ReviewMaintainabilityResult{Status: "passed", Summary: "Readable.", Evidence: []string{"Focused diff."}},
	}
	comment := formatQAComment(assessment)
	for _, expected := range []string{"## Cortexium Runner Agent QA", "Changes requested", "1 passed · 1 failed", "Retries preserve the original job", "A retry creates a second job", "Focused retry check returned two IDs"} {
		if !strings.Contains(comment, expected) {
			t.Fatalf("QA comment omitted %q:\n%s", expected, comment)
		}
	}
	if strings.Contains(comment, "Tenant boundaries remain intact") {
		t.Fatalf("QA changes comment repeated passing detail:\n%s", comment)
	}
	if marker := qaCommentMarker("item", "commit", comment); !strings.HasPrefix(marker, "<!-- cortexium-runner:qa:") || marker != qaCommentMarker("item", "commit", comment) || marker == qaCommentMarker("item", "other", comment) || marker == qaCommentMarker("item", "commit", comment+" changed") {
		t.Fatalf("QA marker is not stable and candidate-bound: %q", marker)
	}
}
