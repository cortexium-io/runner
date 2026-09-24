package github

import (
	"fmt"
	"regexp"
	"strings"
)

// This is a conservative lint for known English-language timing contradictions,
// not a semantic proof that arbitrary prose is executable. Keep it out of
// manifest parsing and released authority: an old bad contract must remain
// inspectable, cancellable and amendable without rewriting its history.
var planningConditionSentences = regexp.MustCompile(`[.;]+`)
var planningConditionClauses = regexp.MustCompile(`\s+(?:and|but|however)\s+`)

var ownDeliveryCondition = regexp.MustCompile(`^(?:(?:the|this|our|current)\s+)?(?:plan|pilot|card|parent|final pr|final pull request|umbrella(?: item)?)(?:'s final (?:pr|pull request))?\s+(?:is|has been|must be|has|must have)\s+(?:(?:actually|already|successfully|automatically|been)\s+)*(?:merged|delivered|published|closed|passed|accepted)\b`)
var actualDeliveryProof = regexp.MustCompile(`^(?:establish|confirm|prove|verify|demonstrate|record|provide|capture|document|show)\b.*\b(?:actual|real|automatic)\b.*\b(?:merged delivery|merged pr|merged pull request)\b`)
var finalDeliveryProof = regexp.MustCompile(`^(?:the\s+)?final delivery evidence\s+(?:(?:must|required to)\s+(?:bind|establish|prove|show|demonstrate|confirm|include)|binds|establishes|proves|shows|demonstrates|confirms|includes|is bound to)\b.*\b(?:whole-plan qa|whole plan qa|complete verification|complete gate|merged pr|merged pull request)\b`)
var laterGateCondition = regexp.MustCompile(`^(?:(?:the|this plan's)\s+)?(?:runner-owned\s+)?(?:post-review\s+)?(?:complete verification|complete gate)\s+(?:has passed|have passed|passes|passed|succeeds|has succeeded|is successful|must pass|must have passed)\b`)
var laterPlanReviewCondition = regexp.MustCompile(`^(?:(?:the|this plan's)\s+)?(?:independent\s+)?(?:whole-plan|whole plan|parent)\s+(?:qa|review)\s+(?:has passed|have passed|passes|passed|has accepted|is accepted|is complete|must pass|must have passed)\b`)
var fixtureProofSubject = regexp.MustCompile(`^(?:tests?|(?:a |the )?fixtures?)\s+(?:prove|proves|demonstrate|demonstrates|show|shows|verify|verifies)\b`)
var fixtureRepositoryContext = regexp.MustCompile(`\b(?:in|into|using)\s+(?:a |the )?(?:(?:temporary|fixture|test)\s+)+repository\b`)
var explicitOwnDelivery = regexp.MustCompile(`\b(?:this|our|current)\s+(?:plan|pilot|card|parent|final pr|final pull request)\b`)

// validatePlanningReviewConditions rejects known future-delivery proof placed
// in pre-review criteria. Call only for new plan-delivery proposals/approvals or
// a proposed amendment, never to reinterpret existing execution authority.
func validatePlanningReviewConditions(field string, conditions []string) error {
	for index, condition := range conditions {
		// Wrapped structured text and its rendered Markdown must lint alike.
		text := strings.Join(strings.Fields(strings.ToLower(condition)), " ")
		for _, sentence := range planningConditionSentences.Split(text, -1) {
			sentence = strings.Trim(sentence, " \t-*`")
			fixture := fixtureProofSubject.MatchString(sentence)
			for _, clause := range planningConditionClauses.Split(sentence, -1) {
				clause = strings.Trim(clause, " \t-*`")
				// A fixture subject can govern several coordinated assertions, but
				// does not exempt an explicit assertion about this plan's delivery.
				if !explicitOwnDelivery.MatchString(clause) && (fixture || fixtureRepositoryContext.MatchString(clause)) {
					continue
				}
				if ownDeliveryCondition.MatchString(clause) || actualDeliveryProof.MatchString(clause) || finalDeliveryProof.MatchString(clause) || laterGateCondition.MatchString(clause) || laterPlanReviewCondition.MatchString(clause) {
					return fmt.Errorf("planning review boundary: %s[%d] appears to require later delivery evidence; card acceptance precedes whole-plan QA, which precedes the Runner-owned complete gate and final PR merge. Clarify candidate/fixture proof versus this plan's actual delivery; preserve explicit pre-QA requirements and resolve conflicting timing through open_decisions or an explicitly reviewed proposal/amendment, not by waiving or silently rescheduling proof", field, index)
				}
			}
		}
	}
	return nil
}

// ValidateDeliveryPlanningProposal lints new executable delivery-plan criteria
// without mutating them or treating the requested downstream outcome as proof.
func ValidateDeliveryPlanningProposal(criteria []string, cards []PlannedItem) error {
	if err := validatePlanningReviewConditions("project_success_criteria", criteria); err != nil {
		return err
	}
	for i, card := range cards {
		if err := validatePlanningReviewConditions(fmt.Sprintf("work_items[%d].acceptance_criteria", i), card.AcceptanceCriteria); err != nil {
			return err
		}
		if err := validatePlanningReviewConditions(fmt.Sprintf("work_items[%d].verification", i), card.Verification); err != nil {
			return err
		}
		// Structured strings may contain Markdown bullets/headings themselves.
		// Apply the same extraction as staged approval before creating any card.
		if err := validatePlanningMemberReviewBoundary(WorkItem{Title: card.Title, Body: FormatPlannedItemBody(card)}); err != nil {
			return err
		}
	}
	return nil
}

// Inspect only executable condition sections, never the original request,
// historical quotations, scope or documented downstream delivery requirements.
func validatePlanningMemberReviewBoundary(item WorkItem) error {
	section := ""
	inOriginalRequest := false
	var conditions []string
	flush := func() error {
		err := validatePlanningReviewConditions(fmt.Sprintf("card %q %s", item.Title, section), conditions)
		conditions = nil
		return err
	}
	for _, line := range strings.Split(item.Body, "\n") {
		line = strings.TrimSpace(line)
		if line == "--- BEGIN ORIGINAL REQUEST ---" {
			inOriginalRequest = true
			continue
		}
		if line == "--- END ORIGINAL REQUEST ---" {
			inOriginalRequest = false
			continue
		}
		if inOriginalRequest {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if err := flush(); err != nil {
				return err
			}
			section = strings.ToLower(strings.TrimSpace(strings.TrimLeft(line, "#")))
			switch section {
			case "acceptance criteria", "proof obligations", "planned verification", "required verification", "project success criteria":
			default:
				section = ""
			}
			continue
		}
		if section == "" || line == "" {
			continue
		}
		newCondition := strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ")
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "- "), "* "))
		for _, checkbox := range []string{"[ ]", "[x]", "[X]"} {
			line = strings.TrimSpace(strings.TrimPrefix(line, checkbox))
		}
		if newCondition || len(conditions) == 0 {
			conditions = append(conditions, line)
		} else {
			conditions[len(conditions)-1] += " " + line
		}
	}
	return flush()
}

func validatePlanningDeliveryReviewBoundary(manifest PlanManifest, children []WorkItem) error {
	if err := validatePlanningReviewConditions("project_success_criteria", manifest.SuccessCriteria); err != nil {
		return err
	}
	retired := map[string]bool{}
	for _, member := range manifest.Members {
		retired[member.ID] = member.Retired
	}
	for _, child := range children {
		if !retired[child.ID] {
			if err := validatePlanningMemberReviewBoundary(child); err != nil {
				return err
			}
		}
	}
	return nil
}
