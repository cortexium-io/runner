package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
)

func formatQAReport(assessment execution.ReviewAssessment, _ []string, _ metrics.Usage) string {
	var report strings.Builder
	fmt.Fprintf(&report, "**Runner QA classification:** %s", reviewVerdictLabel(assessment.Verdict))
	passedCriteria, failedCriteria := reviewStatusCounts(assessment.Criteria)
	fmt.Fprintf(&report, "\n\nRequired criteria: %d passed · %d failed.", passedCriteria, failedCriteria)
	passedRules, failedRules := reviewRuleStatusCounts(assessment.Rules)
	fmt.Fprintf(&report, "\nRepository rules: %d passed · %d failed.", passedRules, failedRules)
	fmt.Fprintf(&report, "\nMaintainability: %s.", boundedReviewStatus(assessment.Maintainability.Status))
	report.WriteString("\n\nDetailed feedback is posted on issue-backed cards and retained locally for retries; Project drafts use the retained feedback only.")

	return strings.TrimSpace(report.String())
}

func formatQAComment(assessment execution.ReviewAssessment) string {
	var report strings.Builder
	fmt.Fprintf(&report, "## Cortexium Runner Agent QA\n\n**Verdict:** %s\n\n%s", reviewVerdictLabel(assessment.Verdict), boundedReviewText(assessment.Summary, 1_000))
	passed, failed := reviewStatusCounts(assessment.Criteria)
	fmt.Fprintf(&report, "\n\nProof obligations: %d passed · %d failed.", passed, failed)
	for _, criterion := range assessment.Criteria {
		if criterion.Status == "passed" {
			continue
		}
		report.WriteString("\n\n### Required change\n\n")
		report.WriteString("**Proof obligation:** ")
		report.WriteString(boundedReviewText(criterion.Criterion, 1_000))
		report.WriteString("\n\n")
		report.WriteString(boundedReviewText(criterion.Summary, 2_000))
		appendReviewEvidence(&report, criterion.Evidence)
	}
	for _, rule := range assessment.Rules {
		if rule.Status == "passed" {
			continue
		}
		for _, finding := range rule.Findings {
			report.WriteString("\n\n### Repository rule finding\n\n")
			report.WriteString(boundedReviewText(finding.Summary, 2_000))
			appendReviewEvidence(&report, finding.Evidence)
		}
	}
	if assessment.Maintainability.Status != "passed" {
		report.WriteString("\n\n### Maintainability\n\n")
		report.WriteString(boundedReviewText(assessment.Maintainability.Summary, 2_000))
		appendReviewEvidence(&report, assessment.Maintainability.Evidence)
	}
	return boundedReviewText(report.String(), 28_000)
}

func appendReviewEvidence(report *strings.Builder, evidence []string) {
	if len(evidence) == 0 {
		return
	}
	report.WriteString("\n\nEvidence:\n")
	for _, item := range evidence {
		if item = boundedReviewText(item, 2_000); item != "" {
			report.WriteString("- ")
			report.WriteString(item)
			report.WriteByte('\n')
		}
	}
}

func qaCommentMarker(itemID, candidateCommit, comment string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(itemID) + "\x00" + strings.TrimSpace(candidateCommit) + "\x00" + strings.TrimSpace(comment)))
	return "<!-- cortexium-runner:qa:" + hex.EncodeToString(digest[:12]) + " -->"
}

func boundedReviewText(value string, limit int) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\x00", "")
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return strings.TrimSpace(value[:cut]) + "…"
}

func reviewStatusCounts(criteria []execution.ReviewCriterionResult) (passed, failed int) {
	for _, criterion := range criteria {
		if criterion.Status == "passed" {
			passed++
		} else {
			failed++
		}
	}
	return passed, failed
}

func reviewRuleStatusCounts(rules []execution.ReviewRuleResult) (passed, failed int) {
	for _, rule := range rules {
		if rule.Status == "passed" {
			passed++
		} else {
			failed++
		}
	}
	return passed, failed
}

func boundedReviewStatus(status string) string {
	if status == "passed" {
		return "passed"
	}
	return "failed"
}

func titleIdentifier(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Unknown"
	}
	words := strings.FieldsFunc(value, func(char rune) bool {
		return char == '_' || char == '-'
	})
	if len(words) == 0 {
		return "Unknown"
	}
	value = strings.Join(words, " ")
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func reviewVerdictLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "accept":
		return "Accepted"
	case "needs_changes":
		return "Changes requested"
	default:
		return titleIdentifier(value)
	}
}
