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
	"github.com/cortexium-io/runner/internal/presentation"
)

func formatQAReport(assessment execution.ReviewAssessment, _ []string, _ metrics.Usage) string {
	var report strings.Builder
	fmt.Fprintf(&report, "**Runner QA classification:** %s", reviewVerdictLabel(assessment.Verdict))
	passedCriteria, failedCriteria := reviewStatusCounts(assessment.Criteria)
	fmt.Fprintf(&report, "\n\nRequired criteria: %d passed · %d failed.", passedCriteria, failedCriteria)
	passedRules, failedRules := reviewRuleStatusCounts(assessment.Rules)
	fmt.Fprintf(&report, "\nRepository rules: %d passed · %d failed.", passedRules, failedRules)
	if !assessment.HasLegacyContract() {
		fmt.Fprintf(&report, "\nRequirements coverage: %s.", boundedReviewStatus(assessment.Requirements.Status))
	}
	fmt.Fprintf(&report, "\nMaintainability: %s.", boundedReviewStatus(assessment.Maintainability.Status))
	report.WriteString("\n\nDetailed feedback is posted on issue-backed cards and retained locally for retries; Project drafts use the retained feedback only.")

	return strings.TrimSpace(report.String())
}

func formatQAComment(assessment execution.ReviewAssessment, candidateCommit string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "## Cortexium Runner Agent QA\n\n**Verdict:** %s", reviewVerdictLabel(assessment.Verdict))
	if candidateCommit = strings.TrimSpace(candidateCommit); candidateCommit != "" {
		fmt.Fprintf(&report, "\n\n**Reviewed commit:** %s", presentation.MarkdownInline(candidateCommit))
	}
	accepted := assessment.Verdict == "accept"
	if accepted {
		report.WriteString("\n\n### Review result")
	}
	fmt.Fprintf(&report, "\n\n%s", boundedReviewText(assessment.Summary, 1_000))
	if assessment.Brief.Rationale != "" {
		report.WriteString("\n\n### Why this change\n\n")
		report.WriteString(boundedReviewText(assessment.Brief.Rationale, 2_000))
		appendReviewBriefNotes(&report, "Consequential assumptions", assessment.Brief.Assumptions, "No consequential assumptions identified.")
		appendReviewBriefNotes(&report, "Verification limits", assessment.Brief.Limitations, "No additional verification caveats identified; acceptance covers the approved scope only.")
	} else if accepted {
		report.WriteString("\n\n### Verification limits\n\nNo separate verification limits were recorded in this review.")
	}
	if assessment.Requirements.Status != "passed" && assessment.Requirements.Status != "" {
		fmt.Fprintf(&report, "\n\n### Requirements coverage — %s\n\n%s", boundedReviewStatus(assessment.Requirements.Status), boundedReviewText(assessment.Requirements.Summary, 2_000))
		appendReviewEvidence(&report, assessment.Requirements.Evidence)
	}
	passed, failed := reviewStatusCounts(assessment.Criteria)
	if accepted {
		report.WriteString("\n\n### Requested outcomes and evidence\n\nEvidence is reported by the reviewer and may include source inspection, reused results, or fresh checks. QA acceptance does not establish merge or deployment status.")
	}
	fmt.Fprintf(&report, "\n\nProof obligations: %d passed · %d failed.", passed, failed)
	for _, criterion := range assessment.Criteria {
		if criterion.Status == "passed" && !accepted {
			continue
		}
		if accepted {
			fmt.Fprintf(&report, "\n\n#### Outcome — %s\n\n**Requested:** ", boundedReviewStatus(criterion.Status))
		} else {
			fmt.Fprintf(&report, "\n\n### Proof obligation — %s\n\n**Proof obligation:** ", boundedReviewStatus(criterion.Status))
		}
		report.WriteString(boundedReviewText(criterion.Criterion, 1_000))
		report.WriteString("\n\n")
		if accepted {
			report.WriteString("**Observed result:** ")
		}
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
	report.WriteString("\n\nEvidence:\n\n")
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
		} else if criterion.Status == "failed" {
			failed++
		}
	}
	return passed, failed
}

func reviewRuleStatusCounts(rules []execution.ReviewRuleResult) (passed, failed int) {
	for _, rule := range rules {
		if rule.Status == "passed" {
			passed++
		} else if rule.Status == "failed" {
			failed++
		}
	}
	return passed, failed
}

func boundedReviewStatus(status string) string {
	switch status {
	case "passed", "failed", "blocked":
		return status
	case "needs_input":
		return "human decision needed"
	default:
		return "unknown"
	}
}

func appendReviewBriefNotes(report *strings.Builder, title string, notes []string, empty string) {
	fmt.Fprintf(report, "\n\n### %s\n\n", title)
	if len(notes) == 0 {
		report.WriteString(empty)
		return
	}
	for _, note := range notes {
		fmt.Fprintf(report, "- %s\n", boundedReviewText(note, 2_000))
	}
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
	case "needs_input":
		return "Human decision needed"
	default:
		return titleIdentifier(value)
	}
}
