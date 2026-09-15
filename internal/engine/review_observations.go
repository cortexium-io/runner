package engine

import (
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
)

// Only concrete failed checks feed recurring project-finding drafts. Blocked
// proof remains an evidence gap, not a confirmed product defect.
func reviewFindingObservations(review execution.ReviewAssessment) []metrics.ReviewFinding {
	var findings []metrics.ReviewFinding
	for _, criterion := range review.Criteria {
		if criterion.Status == "failed" {
			findings = append(findings, metrics.ReviewFinding{Area: "acceptance", Summary: criterion.Summary})
		}
	}
	for _, rule := range review.Rules {
		if rule.Status != "failed" {
			continue
		}
		for _, finding := range rule.Findings {
			if finding.Severity == "blocking" {
				findings = append(findings, metrics.ReviewFinding{Area: "repository_rules", Summary: finding.Summary})
			}
		}
	}
	if review.Maintainability.Status == "failed" {
		findings = append(findings, metrics.ReviewFinding{Area: "maintainability", Summary: review.Maintainability.Summary})
	}
	return findings
}

func reviewDetailObservations(assessment execution.ReviewAssessment) ([]metrics.ReviewDetail, bool) {
	const maximumDetails = 1000
	result := make([]metrics.ReviewDetail, 0, min(maximumDetails, len(assessment.Criteria)+len(assessment.Rules)+1))
	remainingEvidence := maximumDetails
	incomplete := false
	add := func(area, name, status, summary string, evidence []string) {
		if len(result) >= maximumDetails {
			incomplete = true
			return
		}
		boundedEvidence := make([]string, 0, min(100, min(remainingEvidence, len(evidence))))
		for _, value := range evidence {
			if len(boundedEvidence) >= 100 || remainingEvidence == 0 {
				incomplete = true
				break
			}
			var truncated bool
			if value, truncated = boundedHistoryTextWithStatus(value, 8*1024); value != "" {
				boundedEvidence = append(boundedEvidence, value)
				remainingEvidence--
			}
			incomplete = incomplete || truncated
		}
		boundedName, nameTruncated := boundedHistoryTextWithStatus(name, 1024)
		boundedSummary, summaryTruncated := boundedHistoryTextWithStatus(summary, 8*1024)
		incomplete = incomplete || nameTruncated || summaryTruncated
		result = append(result, metrics.ReviewDetail{
			Area: area, Name: boundedName, Status: strings.TrimSpace(status),
			Summary: boundedSummary, Evidence: boundedEvidence,
		})
	}
	for _, criterion := range assessment.Criteria {
		add("acceptance", criterion.Criterion, criterion.Status, criterion.Summary, criterion.Evidence)
	}
	for _, rule := range assessment.Rules {
		name := strings.TrimSpace(rule.RuleSourceID)
		if version := strings.TrimSpace(rule.RuleSourceVersion); version != "" {
			name += "@" + version
		}
		add("repository_rules", name, rule.Status, rule.Summary, nil)
		for index, finding := range rule.Findings {
			add("repository_rules", fmt.Sprintf("%s finding %d (%s)", name, index+1, strings.TrimSpace(finding.Severity)), rule.Status, finding.Summary, finding.Evidence)
		}
	}
	add("maintainability", "maintainability", assessment.Maintainability.Status, assessment.Maintainability.Summary, assessment.Maintainability.Evidence)
	return result, incomplete
}
