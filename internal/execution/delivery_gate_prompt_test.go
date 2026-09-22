package execution

import (
	"strings"
	"testing"
)

func TestDeliveryGateGuidanceRequiresVerifiedParentInBothReviewStages(t *testing.T) {
	for _, tc := range []struct {
		name      string
		change    func(*Spec)
		scheduled bool
	}{
		{"parent", func(*Spec) {}, true},
		{"missing context", func(s *Spec) { s.PlanContext = nil }, false},
		{"unvalidated parent", func(s *Spec) { s.PlanContext.Revision = "" }, false},
		{"not reviewer", func(s *Spec) { s.ReviewRequired = false }, false},
		{"card", func(s *Spec) {
			s.ItemID = "child-a"
			s.ReviewScope = ReviewScopeCard
			s.VerificationBoundary = VerificationFocused
			s.PlanContext.CompleteVerification = ""
			s.PlanMemberBriefs = nil
		}, false},
		{"standalone complete", func(s *Spec) { s.PlanContext = nil; s.PlanMemberBriefs = nil; s.ReviewScope = ReviewScopeCard }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := deliveryReviewAssignment()
			tc.change(&a.Spec)
			for _, prompt := range []string{reviewerAuditPrompt(a, "fixture"), reviewerResolutionPrompt(a, "fixture", nil)} {
				if strings.Contains(prompt, "Runner-owned post-review delivery gate:") != tc.scheduled {
					t.Fatal("scheduling guidance crossed its validated parent boundary")
				}
			}
		})
	}
}

func TestDeliveryGateContextDoesNotWaiveExplicitPreQAObligation(t *testing.T) {
	a := deliveryReviewAssignment()
	a.Spec.RequiredVerification = []string{"The approved migration check must pass before QA."}
	prompt := reviewerAuditPrompt(a, "fixture")
	if !strings.Contains(prompt, a.Spec.RequiredVerification[0]) || !strings.Contains(prompt, "explicit approved requirement to perform a check before QA") || !strings.Contains(prompt, "Never mark an unrun check") {
		t.Fatal("explicit obligation or scheduling distinction omitted")
	}
	content, err := decodeReviewerAuditContent(a, `{"criteria":{"P1":{"status":"check_required","summary":"The approved pre-QA migration check lacks proof.","evidence":["No recorded migration check is available."]}},"repository_rules":{"status":"passed","summary":"No source violations","evidence":["Inspected diff"]},"maintainability":{"status":"passed","summary":"Clear change","evidence":["Inspected source"]},"summary":"Focused pre-QA proof needed"}`)
	if err != nil {
		t.Fatal(err)
	}
	unresolved := reviewerUnresolvedChecks(a, content)
	if len(unresolved) != 1 {
		t.Fatalf("scheduled gate swallowed factual proof obligation: %+v", unresolved)
	}
}
