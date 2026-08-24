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

type ReviewMaintainabilityResult struct {
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
}

type ReviewAssessment struct {
	Criteria        []ReviewCriterionResult     `json:"criteria"`
	Rules           []ReviewRuleResult          `json:"rules"`
	Maintainability ReviewMaintainabilityResult `json:"maintainability"`
	Verdict         string                      `json:"verdict"`
	Summary         string                      `json:"summary"`
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

func (result *ReviewMaintainabilityResult) UnmarshalJSON(data []byte) error {
	type maintainabilityResult ReviewMaintainabilityResult
	return decodeRequiredJSONObject(data, (*maintainabilityResult)(result), "status", "summary", "evidence")
}

func (assessment *ReviewAssessment) UnmarshalJSON(data []byte) error {
	type reviewAssessment ReviewAssessment
	return decodeRequiredJSONObject(data, (*reviewAssessment)(assessment), "criteria", "rules", "maintainability", "verdict", "summary")
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
	if assessment.Verdict != "accept" && assessment.Verdict != "needs_changes" && assessment.Verdict != "blocked" {
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
	hasFailedCheck := false
	hasBlockedCheck := false
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
		hasFailedCheck = hasFailedCheck || criterion.Status == "failed"
		hasBlockedCheck = hasBlockedCheck || criterion.Status == "blocked"
	}

	if len(assessment.Rules) != 1 {
		return errors.New("review_assessment.rules must contain the repository instructions check exactly once")
	}
	for i, rule := range assessment.Rules {
		if strings.TrimSpace(rule.RuleSourceID) != "repository_instructions" || strings.TrimSpace(rule.RuleSourceVersion) != "current" {
			return fmt.Errorf("review_assessment.rules[%d] does not match approved RuleSet provenance", i)
		}
		if rule.Status != "passed" && rule.Status != "failed" && rule.Status != "blocked" {
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
		hasFailedCheck = hasFailedCheck || rule.Status == "failed"
		hasBlockedCheck = hasBlockedCheck || rule.Status == "blocked"
	}
	if err := validateReviewCheck(
		assessment.Maintainability.Status,
		assessment.Maintainability.Summary,
		assessment.Maintainability.Evidence,
		"review_assessment.maintainability",
	); err != nil {
		return err
	}
	hasFailedCheck = hasFailedCheck || assessment.Maintainability.Status == "failed"
	hasBlockedCheck = hasBlockedCheck || assessment.Maintainability.Status == "blocked"
	wantVerdict := "accept"
	if hasFailedCheck {
		wantVerdict = "needs_changes"
	} else if hasBlockedCheck {
		wantVerdict = "blocked"
	}
	if assessment.Verdict != wantVerdict {
		return fmt.Errorf("review_assessment verdict must be %s for the reported checks", wantVerdict)
	}
	if assessment.Verdict == "blocked" {
		if outcome == OutcomeSucceeded {
			return errors.New("blocked reviewer verdict requires needs_input or blocked outcome")
		}
	} else if outcome != OutcomeSucceeded {
		return errors.New("completed reviewer verdict requires a succeeded outcome")
	}
	return nil
}

func validateReviewCheck(status string, summary string, evidence []string, field string) error {
	if status != "passed" && status != "failed" && status != "blocked" {
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
