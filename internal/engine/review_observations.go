package engine

import (
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
