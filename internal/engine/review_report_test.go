package engine

import (
	"strings"
	"testing"
	"unicode/utf8"

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
		Maintainability: execution.ReviewCheckResult{
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
		Maintainability: execution.ReviewCheckResult{Status: "passed", Summary: "Readable.", Evidence: []string{"Focused diff."}},
	}
	comment := formatQAComment(assessment, "reviewed-commit")
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

func TestAcceptedReviewCommentExplainsDecisionWithoutExpandingProjectReport(t *testing.T) {
	assessment := execution.ReviewAssessment{
		Verdict: "accept", Summary: "Approved behavior established.",
		Requirements: execution.ReviewCheckResult{Status: "passed"}, Maintainability: execution.ReviewCheckResult{Status: "passed"},
		Brief: execution.ReviewBrief{
			Rationale:   "Preserve operator edits during task updates.",
			Assumptions: []string{"The approved contract permits only one local writer."},
			Limitations: []string{"No host-only release receipt was provided; this card covers local behavior."},
		},
	}
	comment := formatQAComment(assessment, "reviewed-commit")
	for _, expected := range []string{"Why this change", assessment.Brief.Rationale, "Consequential assumptions", assessment.Brief.Assumptions[0], "Verification limits", assessment.Brief.Limitations[0]} {
		if !strings.Contains(comment, expected) {
			t.Fatalf("human handoff omitted %q: %s", expected, comment)
		}
		if strings.Contains(formatQAReport(assessment, nil, metrics.Usage{}), expected) {
			t.Fatalf("Project classification leaked free-form text: %q", expected)
		}
	}
}

func TestReviewCommentKeepsMissingEvidenceSeparateFromDefects(t *testing.T) {
	assessment := execution.ReviewAssessment{Verdict: "needs_input", Requirements: execution.ReviewCheckResult{Status: "needs_input", Summary: "Which constraint takes priority?"}, Maintainability: execution.ReviewCheckResult{Status: "passed"}, Criteria: []execution.ReviewCriterionResult{
		{Criterion: "Existing behavior", Status: "passed"}, {Criterion: "Native release", Status: "blocked", Summary: "Receipt unavailable."},
	}}
	comment := formatQAComment(assessment, "reviewed-commit")
	for _, expected := range []string{"Human decision needed", "Which constraint takes priority?", "1 passed · 0 failed", "Proof obligation — blocked", "Receipt unavailable."} {
		if !strings.Contains(comment, expected) {
			t.Fatalf("handoff misclassified unresolved work: %s", comment)
		}
	}
}

func TestAcceptedQACommentShowsOutcomesEvidenceAndLimitsForReviewedCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	assessment := execution.ReviewAssessment{
		Verdict: "accept", Summary: "Documented the PATCH repair in unchanged and customized applications.",
		Requirements:    execution.ReviewCheckResult{Status: "passed"},
		Maintainability: execution.ReviewCheckResult{Status: "passed"},
		Brief: execution.ReviewBrief{
			Rationale:   "Allow older applications to receive the repair without losing custom behavior.",
			Assumptions: []string{"The repair is manually reviewed."},
			Limitations: []string{"Deployment and published-module installation were not tested."},
		},
		Criteria: []execution.ReviewCriterionResult{
			{Criterion: "Preserve omitted values and custom behavior.", Status: "passed", Summary: "Both applications preserve omitted values and the custom title rule.", Evidence: []string{"Reused the retained HTTP tests; the refresh did not change the exercised PATCH implementation."}},
			{Criterion: "Expose conflicts before mutation.", Status: "passed", Summary: "A deliberate conflict was detected before writes.", Evidence: []string{"Source inspection confirms preview returns before the write path.", "Retained file comparisons show unchanged migration SQL."}},
		},
	}
	comment := formatQAComment(assessment, commit)
	for _, expected := range []string{
		"**Reviewed commit:** " + commit, "### Review result", assessment.Summary,
		assessment.Brief.Rationale, assessment.Brief.Assumptions[0], assessment.Brief.Limitations[0],
		"### Requested outcomes and evidence", "Evidence is reported by the reviewer", "QA acceptance does not establish merge or deployment status.",
		"Proof obligations: 2 passed · 0 failed.",
	} {
		if !strings.Contains(comment, expected) {
			t.Fatalf("completion report omitted %q:\n%s", expected, comment)
		}
	}
	for _, criterion := range assessment.Criteria {
		if !strings.Contains(comment, "**Observed result:** "+criterion.Summary) {
			t.Fatalf("observed outcome is not distinguished from the request: %s", comment)
		}
		for _, expected := range append([]string{criterion.Criterion, criterion.Summary}, criterion.Evidence...) {
			if !strings.Contains(comment, expected) {
				t.Fatalf("completion report lost outcome evidence %q:\n%s", expected, comment)
			}
		}
	}
	if strings.Contains(comment, "Required change") {
		t.Fatalf("accepted outcomes were presented as required changes:\n%s", comment)
	}
	if strings.Index(comment, assessment.Brief.Limitations[0]) > strings.Index(comment, "### Requested outcomes and evidence") {
		t.Fatal("verification limits must remain visible before potentially lengthy evidence")
	}
	classification := formatQAReport(assessment, nil, metrics.Usage{})
	if strings.Contains(classification, assessment.Criteria[0].Evidence[0]) || strings.Contains(classification, assessment.Brief.Limitations[0]) {
		t.Fatalf("completion details leaked into the Project classification: %s", classification)
	}
}

func TestAcceptedQACommentBoundsEvidenceAndDoesNotInventMissingLimits(t *testing.T) {
	assessment := execution.ReviewAssessment{
		Verdict: "accept", Summary: "Accepted historical assessment.",
		Maintainability: execution.ReviewCheckResult{Status: "passed"},
		Criteria:        []execution.ReviewCriterionResult{{Criterion: "Recorded outcome", Status: "passed", Summary: "Supported by retained evidence."}},
	}
	for range 30 {
		assessment.Criteria[0].Evidence = append(assessment.Criteria[0].Evidence, strings.Repeat("é", 2_000))
	}
	comment := formatQAComment(assessment, "")
	if !strings.Contains(comment, "No separate verification limits were recorded") || strings.Contains(comment, "No additional verification caveats identified") {
		t.Fatalf("missing limits were treated as known empty limits: %s", comment[:300])
	}
	if len(comment) > 28_003 || !utf8.ValidString(comment) || !strings.HasSuffix(comment, "…") {
		t.Fatalf("report truncation lost its bound or UTF-8 validity: bytes=%d", len(comment))
	}
}
