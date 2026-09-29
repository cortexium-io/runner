package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func passingAuditContent(t *testing.T) reviewerContent {
	t.Helper()
	content, err := decodeReviewerAuditContent(reviewerAssignment(), failingReviewerContent())
	if err != nil {
		t.Fatal(err)
	}
	for key, check := range content.Criteria {
		check.Status = "passed"
		content.Criteria[key] = check
	}
	content.RepositoryRules.Status = "passed"
	return content
}

func encodeReviewTestContent(t *testing.T, content any) string {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRequirementsCheckCannotBeBypassedByPassingProofKeys(t *testing.T) {
	for _, status := range []string{"failed", "needs_input", "check_required"} {
		t.Run(status, func(t *testing.T) {
			content := passingAuditContent(t)
			content.Requirements = reviewerContentCheck{Status: status, Summary: "The original request requires preserving unrelated state.", Evidence: []string{"The happy-path proof does not cover the unchanged consumer."}}
			run := &sharedReviewerHarnessRunner{responses: []string{encodeReviewTestContent(t, content)}}
			if status == "check_required" {
				run.responses = append(run.responses, encodeReviewTestContent(t, reviewerResolutionContent{
					Checks:      map[string]reviewerContentCheck{"C": {Status: "failed", Summary: "The consumer loses state.", Evidence: []string{"The existing consumer test failed."}}},
					Limitations: []string{}, Summary: "Required preservation failed.",
				}))
			}
			cfg := testCodexConfig(t)
			output, err := executeSharedReviewer(t.Context(), config.HarnessCodexCLI, cfg, reviewerAssignment(), run)
			wantVerdict, wantCalls := "needs_changes", 1
			if status == "needs_input" {
				wantVerdict = "needs_input"
				if output.FailureClass != FailureNeedsInput || output.RetryDisposition != RetryManual || output.Outcome != OutcomeNeedsInput {
					t.Fatalf("decision did not request manual input: %#v", output)
				}
			}
			if status == "check_required" {
				wantCalls = 2
				if !strings.Contains(run.inputs[1], `"key":"C"`) || !strings.Contains(run.inputs[1], reviewerAssignment().Spec.ApprovedBodySnapshot) {
					t.Fatal("completeness verification lost its question or approved context")
				}
			}
			if err != nil || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != wantVerdict || len(run.inputs) != wantCalls {
				t.Fatalf("passing checklist bypassed requirements: %#v, calls=%d, error=%v", output, len(run.inputs), err)
			}
		})
	}
}

func TestHumanDecisionRetainsDefectsAndIndependentVerification(t *testing.T) {
	content := passingAuditContent(t)
	content.Requirements = reviewerContentCheck{Status: "needs_input", Summary: "Should values preserve case or be lowercased?", Evidence: []string{"The two approved constraints conflict."}}
	content.Criteria["P1"] = reviewerContentCheck{Status: "failed", Summary: "Independent defect.", Evidence: []string{"The candidate deletes unrelated state."}}
	content.Criteria["P2"] = reviewerContentCheck{Status: "check_required", Summary: "Does the independent check pass?", Evidence: []string{"No existing receipt."}}
	content.Brief.Limitations = []string{"The historical full suite has no receipt."}
	resolution := reviewerResolutionContent{
		Checks:      map[string]reviewerContentCheck{"P2": {Status: "passed", Summary: "Independent check passed.", Evidence: []string{"Focused check passed."}}},
		Limitations: []string{"The prior timeout remains unexplained."}, Summary: "One check resolved.",
	}
	run := &sharedReviewerHarnessRunner{responses: []string{encodeReviewTestContent(t, content), encodeReviewTestContent(t, resolution)}}
	output, err := executeSharedReviewer(t.Context(), config.HarnessCodexCLI, testCodexConfig(t), reviewerAssignment(), run)
	if err != nil || output.ReviewAssessment == nil {
		t.Fatalf("review: %#v, %v", output, err)
	}
	assessment := output.ReviewAssessment
	if output.Outcome != OutcomeNeedsInput || assessment.Verdict != "needs_input" || assessment.Criteria[0].Status != "failed" || assessment.Criteria[1].Status != "passed" || len(run.inputs) != 2 {
		t.Fatalf("decision lost precedence, defects, or independent proof: %#v", output)
	}
	if strings.Contains(run.inputs[1], content.Requirements.Summary) || strings.Contains(run.inputs[1], `"key":"C"`) {
		t.Fatal("human decision was sent to dynamic verification")
	}
	if output.Blocker == nil || *output.Blocker != content.Requirements.Summary || len(assessment.Brief.Limitations) != 2 || assessment.Brief.Rationale != content.Brief.Rationale {
		t.Fatalf("merged review lost the question or brief: %#v", assessment)
	}
}

func TestReviewDecisionPrecedenceAndOutcomeValidation(t *testing.T) {
	assignment := reviewerAssignment()
	for _, area := range []string{"requirements", "criteria", "rules", "maintainability"} {
		t.Run(area, func(t *testing.T) {
			assessment := passingMockReviewAssessment(assignment.Spec)
			assessment.Criteria[0].Status = "failed"
			switch area {
			case "requirements":
				assessment.Requirements.Status = "needs_input"
			case "criteria":
				assessment.Criteria[1].Status = "needs_input"
			case "rules":
				assessment.Rules[0].Status = "needs_input"
			case "maintainability":
				assessment.Maintainability.Status = "needs_input"
			}
			assessment.Verdict = "needs_input"
			if err := validateReviewAssessmentForAssignment(assignment, OutcomeNeedsInput, assessment); err != nil {
				t.Fatal(err)
			}
			for _, outcome := range []string{OutcomeSucceeded, OutcomeBlocked} {
				if err := validateReviewAssessmentForAssignment(assignment, outcome, assessment); err == nil {
					t.Fatal("human decision accepted with incompatible outcome")
				}
			}
			assessment.Verdict = "needs_changes"
			if err := validateReviewAssessmentForAssignment(assignment, OutcomeSucceeded, assessment); err == nil {
				t.Fatal("human decision entered automatic rework")
			}
		})
	}
}

func TestReviewRequiresCompletenessAndDecisionBrief(t *testing.T) {
	for _, field := range []string{"requirements", "brief"} {
		t.Run(field, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal([]byte(encodeReviewTestContent(t, passingAuditContent(t))), &value); err != nil {
				t.Fatal(err)
			}
			delete(value, field)
			if _, err := decodeReviewerAuditContent(reviewerAssignment(), encodeReviewTestContent(t, value)); err == nil {
				t.Fatal("missing required review area was accepted")
			}
		})
	}
	for _, field := range []string{"rationale", "assumptions", "limitations"} {
		var value map[string]any
		if err := json.Unmarshal([]byte(encodeReviewTestContent(t, passingMockReviewAssessment(reviewerAssignment().Spec))), &value); err != nil {
			t.Fatal(err)
		}
		delete(value["brief"].(map[string]any), field)
		var assessment ReviewAssessment
		if err := json.Unmarshal([]byte(encodeReviewTestContent(t, value)), &assessment); err == nil {
			t.Fatalf("brief without %s was accepted", field)
		}
	}
}

func TestLegacyReviewIsHistoricalOnlyAndPreservesItsEncoding(t *testing.T) {
	a := reviewerAssignment()
	assessment := passingMockReviewAssessment(a.Spec)
	assessment.Requirements, assessment.Brief = ReviewCheckResult{}, ReviewBrief{}
	encoded, err := json.Marshal(assessment)
	if err != nil || strings.Contains(string(encoded), `"requirements"`) || strings.Contains(string(encoded), `"brief"`) {
		t.Fatalf("historical review acquired invented fields: %s %v", encoded, err)
	}
	var historical ReviewAssessment
	if err := json.Unmarshal(encoded, &historical); err != nil {
		t.Fatal(err)
	}
	out := Output{Outcome: OutcomeSucceeded, ReviewAssessment: &historical}
	if err := ValidateRetainedReviewOutput(a, out); err != nil {
		t.Fatalf("valid historical acceptance cannot be read: %v", err)
	}
	if ValidateReviewOutput(a, out) == nil || ValidateReviewBaseline(a.Spec, &ReviewBaseline{Assessment: historical}) == nil {
		t.Fatal("historical review authorized fresh acceptance or baseline reuse")
	}
	historical.Criteria[0].Status = "failed"
	if ValidateRetainedReviewOutput(a, out) == nil {
		t.Fatal("legacy compatibility waived the original review checks")
	}
}
