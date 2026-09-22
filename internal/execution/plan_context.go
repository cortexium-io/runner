package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ValidateAssignmentContext rejects inconsistent delivery context before any
// harness or workspace operation. Authentication stays with the coordinator;
// structurally valid model text must never be used to construct PlanContext.
// Empty context is the existing standalone path, not implicit plan acceptance.
func ValidateAssignmentContext(spec Spec) error {
	if spec.ReviewScope != "" && spec.ReviewScope != ReviewScopeCard && spec.ReviewScope != ReviewScopePlan {
		return errors.New("assignment review scope is invalid")
	}
	if spec.VerificationBoundary != "" && spec.VerificationBoundary != VerificationFocused && spec.VerificationBoundary != VerificationComplete {
		return errors.New("assignment verification boundary is invalid")
	}
	if spec.PlanContext == nil {
		if len(spec.PlanMemberBriefs) > 0 {
			return errors.New("member briefs require authenticated plan context")
		}
		if spec.ReviewScope == ReviewScopePlan {
			return errors.New("whole-plan review requires verified plan context")
		}
		return nil
	}
	p := spec.PlanContext
	for name, value := range map[string]string{
		"id": p.ID, "revision": p.Revision, "approved_body": p.ApprovedBody,
		"repository": p.Repository, "destination_branch": p.DestinationBranch, "branch": p.Branch,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("plan context %s is required", name)
		}
	}
	if p.Repository != spec.Repository || p.Branch == p.DestinationBranch {
		return errors.New("plan context repository or branch does not match the delivery boundary")
	}
	if len(p.MemberIDs) == 0 {
		return errors.New("plan context requires the exact member set")
	}
	seen := make(map[string]bool, len(p.MemberIDs))
	for _, member := range p.MemberIDs {
		if strings.TrimSpace(member) == "" || member == p.ID || seen[member] {
			return errors.New("plan context contains an empty, repeated, or parent member")
		}
		seen[member] = true
	}
	if spec.ReviewScope == ReviewScopePlan {
		if !spec.ReviewRequired || spec.ItemID != p.ID || spec.VerificationBoundary != VerificationComplete {
			return errors.New("whole-plan review must target its parent at the complete verification boundary")
		}
		if strings.TrimSpace(p.CompleteVerification) == "" {
			return errors.New("whole-plan review requires the approved coordinator complete-verification entrypoint")
		}
		if len(spec.PlanMemberBriefs) != len(p.MemberIDs) {
			return errors.New("whole-plan review requires every approved member brief")
		}
		briefs := map[string]bool{}
		for _, brief := range spec.PlanMemberBriefs {
			if !seen[brief.ID] || briefs[brief.ID] || strings.TrimSpace(brief.ApprovedBody) == "" {
				return errors.New("whole-plan member briefs are incomplete or ambiguous")
			}
			briefs[brief.ID] = true
		}
		encoded, err := json.Marshal(spec.PlanMemberBriefs)
		if err != nil || len(encoded) > 256*1024 {
			return errors.New("whole-plan member ownership context exceeds the 256 KiB safety bound")
		}
	} else if spec.ReviewScope != ReviewScopeCard || spec.VerificationBoundary != VerificationFocused || !seen[spec.ItemID] {
		return errors.New("plan card assignment must target a member at the focused verification boundary")
	} else if len(spec.PlanMemberBriefs) > 0 {
		return errors.New("card assignment cannot include sibling ownership briefs")
	} else if p.CompleteVerification != "" {
		return errors.New("only whole-plan review may promise a coordinator complete-verification entrypoint")
	}
	return nil
}

func planAssignmentContext(spec Spec) string {
	if spec.PlanContext == nil && spec.ReviewScope == "" && spec.VerificationBoundary == "" {
		return ""
	}
	context, _ := json.Marshal(struct {
		Plan                 *PlanContext         `json:"plan,omitempty"`
		ReviewScope          ReviewScope          `json:"review_scope,omitempty"`
		VerificationBoundary VerificationBoundary `json:"verification_boundary,omitempty"`
	}{spec.PlanContext, spec.ReviewScope, spec.VerificationBoundary})
	return "\n\nRunner-verified delivery context (fixed for this assignment; does not authorize sibling work or new requirements):\n" + string(context) + "\n"
}
