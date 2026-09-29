package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ReviewCriterionResult struct {
	Criterion string   `json:"criterion"`
	Status    string   `json:"status"`
	Summary   string   `json:"summary"`
	Evidence  []string `json:"evidence"`
}

type ReviewRuleFinding struct {
	Severity string   `json:"severity"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

type ReviewRuleResult struct {
	RuleSourceID      string              `json:"rule_source_id"`
	RuleSourceVersion string              `json:"rule_source_version"`
	Status            string              `json:"status"`
	Summary           string              `json:"summary"`
	Findings          []ReviewRuleFinding `json:"findings"`
}

type ReviewCheckResult struct {
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

// ReviewBrief explains the decision using approved context and inspected evidence.
// Limitations are enduring caveats, not checks awaiting focused verification.
type ReviewBrief struct {
	Rationale   string   `json:"rationale"`
	Assumptions []string `json:"assumptions"`
	Limitations []string `json:"limitations"`
}

type ReviewAssessment struct {
	RepairTargets   []PlanRepairTarget      `json:"repair_targets,omitempty"`
	Requirements    ReviewCheckResult       `json:"requirements,omitzero"`
	Brief           ReviewBrief             `json:"brief,omitzero"`
	Criteria        []ReviewCriterionResult `json:"criteria"`
	Rules           []ReviewRuleResult      `json:"rules"`
	Maintainability ReviewCheckResult       `json:"maintainability"`
	Verdict         string                  `json:"verdict"`
	Summary         string                  `json:"summary"`
}

func (result *ReviewCriterionResult) UnmarshalJSON(data []byte) error {
	type criterionResult ReviewCriterionResult
	return decodeRequiredJSONObject(data, (*criterionResult)(result), "criterion", "status", "summary", "evidence")
}

func (finding *ReviewRuleFinding) UnmarshalJSON(data []byte) error {
	type ruleFinding ReviewRuleFinding
	return decodeRequiredJSONObject(data, (*ruleFinding)(finding), "severity", "summary", "evidence")
}

func (result *ReviewRuleResult) UnmarshalJSON(data []byte) error {
	type ruleResult ReviewRuleResult
	return decodeRequiredJSONObject(data, (*ruleResult)(result), "rule_source_id", "rule_source_version", "status", "summary", "findings")
}

func (result *ReviewCheckResult) UnmarshalJSON(data []byte) error {
	type checkResult ReviewCheckResult
	return decodeRequiredJSONObject(data, (*checkResult)(result), "status", "summary", "evidence")
}

func (brief *ReviewBrief) UnmarshalJSON(data []byte) error {
	type reviewBrief ReviewBrief
	return decodeRequiredJSONObject(data, (*reviewBrief)(brief), "rationale", "assumptions", "limitations")
}

func (assessment *ReviewAssessment) UnmarshalJSON(data []byte) error {
	type reviewAssessment ReviewAssessment
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	required := []string{"criteria", "rules", "maintainability", "verdict", "summary"}
	// Read historical private records without inventing a completeness check.
	// Fresh harness output and baseline reuse still require the current contract.
	if fields["requirements"] != nil || fields["brief"] != nil {
		required = append(required, "requirements", "brief")
	}
	return decodeRequiredJSONObject(data, (*reviewAssessment)(assessment), required...)
}

// HasLegacyContract identifies assessments recorded before requirements coverage
// and decision briefs were introduced. It never makes them valid fresh output.
func (assessment ReviewAssessment) HasLegacyContract() bool {
	return assessment.Requirements.Status == "" && assessment.Requirements.Summary == "" && assessment.Requirements.Evidence == nil &&
		assessment.Brief.Rationale == "" && assessment.Brief.Assumptions == nil && assessment.Brief.Limitations == nil
}

func decodeRequiredJSONObject(data []byte, destination any, requiredFields ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, field := range requiredFields {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("required field %q is missing", field)
		}
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func validateReviewAssessmentForAssignment(assignment Assignment, outcome string, assessment *ReviewAssessment) error {
	return validateReviewAssessment(assignment, outcome, assessment, false)
}

func validateReviewAssessment(assignment Assignment, outcome string, assessment *ReviewAssessment, retained bool) error {
	reviewRequired := assignment.Spec.ReviewRequired
	if !reviewRequired {
		if assessment != nil {
			return errors.New("review_assessment is only valid for a reviewer-required assignment")
		}
		return nil
	}
	if assessment == nil {
		return errors.New("reviewer-required assignment must return review_assessment")
	}
	if strings.TrimSpace(assessment.Summary) == "" {
		return errors.New("review_assessment.summary is required")
	}
	if assessment.Verdict != "accept" && assessment.Verdict != "needs_changes" && assessment.Verdict != "blocked" && assessment.Verdict != "needs_input" {
		return errors.New("review_assessment.verdict is invalid")
	}
	if len(assessment.Criteria) != len(assignment.Spec.RequiredVerification) {
		return errors.New("review_assessment.criteria must cover every required_verification entry exactly once")
	}
	expectedCriteria := make(map[string]struct{}, len(assignment.Spec.RequiredVerification))
	for _, criterion := range assignment.Spec.RequiredVerification {
		expectedCriteria[strings.TrimSpace(criterion)] = struct{}{}
	}
	seenCriteria := map[string]struct{}{}
	for i, criterion := range assessment.Criteria {
		name := strings.TrimSpace(criterion.Criterion)
		if _, ok := expectedCriteria[name]; !ok {
			return fmt.Errorf("review_assessment.criteria[%d] is not required by the assignment", i)
		}
		if _, duplicate := seenCriteria[name]; duplicate {
			return fmt.Errorf("review_assessment.criteria[%d] is duplicated", i)
		}
		seenCriteria[name] = struct{}{}
		if err := validateReviewCheck(criterion.Status, criterion.Summary, criterion.Evidence, fmt.Sprintf("review_assessment.criteria[%d]", i)); err != nil {
			return err
		}
	}

	if len(assessment.Rules) != 1 {
		return errors.New("review_assessment.rules must contain the repository instructions check exactly once")
	}
	for i, rule := range assessment.Rules {
		if strings.TrimSpace(rule.RuleSourceID) != "repository_instructions" || strings.TrimSpace(rule.RuleSourceVersion) != "current" {
			return fmt.Errorf("review_assessment.rules[%d] does not match approved RuleSet provenance", i)
		}
		if rule.Status != "passed" && rule.Status != "failed" && rule.Status != "blocked" && rule.Status != "needs_input" {
			return fmt.Errorf("review_assessment.rules[%d].status is invalid", i)
		}
		if strings.TrimSpace(rule.Summary) == "" {
			return fmt.Errorf("review_assessment.rules[%d].summary is required", i)
		}
		hasBlockingFinding := false
		for findingIndex, finding := range rule.Findings {
			if finding.Severity != "blocking" && finding.Severity != "warning" {
				return fmt.Errorf("review_assessment.rules[%d].findings[%d].severity is invalid", i, findingIndex)
			}
			if strings.TrimSpace(finding.Summary) == "" || len(finding.Evidence) == 0 {
				return fmt.Errorf("review_assessment.rules[%d].findings[%d] requires summary and evidence", i, findingIndex)
			}
			for evidenceIndex, evidence := range finding.Evidence {
				if strings.TrimSpace(evidence) == "" {
					return fmt.Errorf("review_assessment.rules[%d].findings[%d].evidence[%d] is empty", i, findingIndex, evidenceIndex)
				}
			}
			hasBlockingFinding = hasBlockingFinding || finding.Severity == "blocking"
		}
		if rule.Status == "passed" && hasBlockingFinding {
			return fmt.Errorf("review_assessment.rules[%d] cannot pass with a blocking finding", i)
		}
		if rule.Status == "failed" && !hasBlockingFinding {
			return fmt.Errorf("review_assessment.rules[%d] must include a blocking finding when failed", i)
		}
	}
	if err := validateReviewCheck(
		assessment.Maintainability.Status,
		assessment.Maintainability.Summary,
		assessment.Maintainability.Evidence,
		"review_assessment.maintainability",
	); err != nil {
		return err
	}
	if !retained || !assessment.HasLegacyContract() {
		if err := validateReviewCheck(assessment.Requirements.Status, assessment.Requirements.Summary, assessment.Requirements.Evidence, "review_assessment.requirements"); err != nil {
			return err
		}
		if err := validateReviewBrief(assessment.Brief); err != nil {
			return err
		}
	}
	wantVerdict := derivedReviewerVerdict(*assessment)
	if assessment.Verdict != wantVerdict {
		return fmt.Errorf("review_assessment verdict must be %s for the reported checks", wantVerdict)
	}
	if assessment.Verdict == "needs_input" {
		if outcome != OutcomeNeedsInput {
			return errors.New("needs_input reviewer verdict requires needs_input outcome")
		}
	} else if assessment.Verdict == "blocked" {
		if outcome == OutcomeSucceeded {
			return errors.New("blocked reviewer verdict requires needs_input or blocked outcome")
		}
	} else if outcome != OutcomeSucceeded {
		return errors.New("completed reviewer verdict requires a succeeded outcome")
	}
	statuses := map[string]string{"C": assessment.Requirements.Status, "R": assessment.Rules[0].Status, "M": assessment.Maintainability.Status}
	for index, criterion := range assignment.Spec.RequiredVerification {
		for _, check := range assessment.Criteria {
			if strings.TrimSpace(check.Criterion) == strings.TrimSpace(criterion) {
				statuses[reviewerCriterionKey(index)] = check.Status
			}
		}
	}
	return validatePlanRepairTargets(assignment.Spec, assessment.RepairTargets, statuses)
}

// ValidateReviewBaseline checks that private historical review data still has
// the exact structure expected by the current proof contract. Baseline data is
// evidence only; this validation does not grant review or execution authority.
func ValidateReviewBaseline(spec Spec, baseline *ReviewBaseline) error {
	if baseline == nil {
		return errors.New("review baseline is missing")
	}
	if baseline.CommentContext == nil {
		return errors.New("review baseline comment context is missing")
	}
	for index, comment := range baseline.CommentContext {
		if strings.TrimSpace(comment) == "" {
			return fmt.Errorf("review baseline comment context %d is empty", index)
		}
	}
	if baseline.Assessment.Verdict != "needs_changes" {
		return errors.New("review baseline must contain a completed rejected assessment")
	}
	spec.ReviewRequired = true
	return validateReviewAssessmentForAssignment(Assignment{Spec: spec}, OutcomeSucceeded, &baseline.Assessment)
}

func validateReviewCheck(status string, summary string, evidence []string, field string) error {
	if status != "passed" && status != "failed" && status != "blocked" && status != "needs_input" {
		return fmt.Errorf("%s.status is invalid", field)
	}
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("%s.summary is required", field)
	}
	if len(evidence) == 0 {
		return fmt.Errorf("%s.evidence is required", field)
	}
	for i, item := range evidence {
		if strings.TrimSpace(item) == "" {
			return fmt.Errorf("%s.evidence[%d] is empty", field, i)
		}
	}
	return nil
}

func validateReviewBrief(brief ReviewBrief) error {
	if strings.TrimSpace(brief.Rationale) == "" {
		return errors.New("review brief rationale is required")
	}
	if err := validateReviewNotes(brief.Assumptions, "brief.assumptions"); err != nil {
		return err
	}
	return validateReviewNotes(brief.Limitations, "brief.limitations")
}

func validateReviewNotes(notes []string, field string) error {
	if notes == nil {
		return fmt.Errorf("%s must be an explicit array", field)
	}
	for _, note := range notes {
		if strings.TrimSpace(note) == "" {
			return fmt.Errorf("%s contains an empty entry", field)
		}
	}
	return nil
}
