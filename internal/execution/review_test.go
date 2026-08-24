package execution

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReviewAssessmentMustMatchFrozenCriteriaAndRuleProvenance(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	packet.ReviewRequired = true
	assignment := Assignment{Spec: packet}

	assessment := passingMockReviewAssessment(packet)
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, assessment); err != nil {
		t.Fatalf("validate passing reviewer assessment: %v", err)
	}
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, nil); err == nil {
		t.Fatal("successful reviewer outcome must fail closed without review_assessment")
	}

	failing := passingMockReviewAssessment(packet)
	failing.Rules[0].Status = "failed"
	failing.Rules[0].Findings = []ReviewRuleFinding{{
		Severity: "blocking",
		Summary:  "The touched root file gained an unrelated responsibility.",
		Evidence: []string{"frontend/src/App.vue"},
	}}
	failing.Verdict = "needs_changes"
	failing.Summary = "Mandatory rule compliance failed."
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, failing); err != nil {
		t.Fatalf("a completed needs-changes review must use succeeded outcome: %v", err)
	}
	failing.Rules[0].RuleSourceID = "rules_wrong"
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, failing); err == nil {
		t.Fatal("provenance-mismatched assessment must remain invalid")
	}
}

func TestReviewAssessmentVerdictAndOutcomeMustAgreeBothWays(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	packet.ReviewRequired = true
	assignment := Assignment{Spec: packet}

	for _, outcome := range []string{
		OutcomeNeedsInput,
		OutcomeBlocked,
	} {
		t.Run(outcome+"_with_accept", func(t *testing.T) {
			assessment := passingMockReviewAssessment(packet)
			if err := validateReviewAssessmentForAssignment(assignment, outcome, assessment); err == nil ||
				!strings.Contains(err.Error(), "completed reviewer verdict requires a succeeded outcome") {
				t.Fatalf("expected non-success accept result to fail closed, got %v", err)
			}
		})
	}

	assessment := passingMockReviewAssessment(packet)
	assessment.Rules[0].Status = "failed"
	assessment.Rules[0].Findings = []ReviewRuleFinding{{
		Severity: "blocking",
		Summary:  "A mandatory rule failed.",
		Evidence: []string{"internal/execution/review.go"},
	}}
	assessment.Verdict = "needs_changes"
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, assessment); err != nil {
		t.Fatalf("expected succeeded needs-changes review to validate, got %v", err)
	}
}

func TestReviewAssessmentBlockedRequiresNoKnownFailureAndBlockedOutcome(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	packet.ReviewRequired = true
	assignment := Assignment{Spec: packet}
	assessment := passingMockReviewAssessment(packet)
	assessment.Criteria[0].Status = "blocked"
	assessment.Criteria[0].Summary = "Required evidence was unavailable."
	assessment.Criteria[0].Evidence = []string{"The approved read capability returned permission denied."}
	assessment.Verdict = "blocked"
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeBlocked, assessment); err != nil {
		t.Fatalf("validate blocked reviewer assessment: %v", err)
	}
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, assessment); err == nil {
		t.Fatal("blocked verdict was accepted with succeeded outcome")
	}
	assessment.Criteria[1].Status = "failed"
	assessment.Verdict = "blocked"
	if err := validateReviewAssessmentForAssignment(assignment, OutcomeBlocked, assessment); err == nil || !strings.Contains(err.Error(), "needs_changes") {
		t.Fatalf("known failure did not override blocked status: %v", err)
	}
}

func TestReviewAssessmentJSONRequiresMandatoryNestedFieldsToBePresent(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	assessment := passingMockReviewAssessment(packet)
	assessment.Rules[0].Findings = []ReviewRuleFinding{{
		Severity: "warning",
		Summary:  "A non-blocking concern was reviewed.",
		Evidence: []string{"internal/execution/review.go"},
	}}

	tests := map[string]func(map[string]any){
		"criterion evidence": func(value map[string]any) {
			delete(value["criteria"].([]any)[0].(map[string]any), "evidence")
		},
		"rule findings": func(value map[string]any) {
			delete(value["rules"].([]any)[0].(map[string]any), "findings")
		},
		"finding summary": func(value map[string]any) {
			rule := value["rules"].([]any)[0].(map[string]any)
			delete(rule["findings"].([]any)[0].(map[string]any), "summary")
		},
		"maintainability status": func(value map[string]any) {
			delete(value["maintainability"].(map[string]any), "status")
		},
	}

	encoded, err := json.Marshal(assessment)
	if err != nil {
		t.Fatalf("marshal complete assessment: %v", err)
	}
	for name, omitField := range tests {
		t.Run(name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(encoded, &value); err != nil {
				t.Fatalf("decode assessment fixture: %v", err)
			}
			omitField(value)
			missingFieldJSON, err := json.Marshal(value)
			if err != nil {
				t.Fatalf("marshal assessment with omitted field: %v", err)
			}
			var decoded ReviewAssessment
			if err := json.Unmarshal(missingFieldJSON, &decoded); err == nil ||
				!strings.Contains(err.Error(), "required field") {
				t.Fatalf("expected omitted mandatory field to be rejected, got %v", err)
			}
		})
	}
}
