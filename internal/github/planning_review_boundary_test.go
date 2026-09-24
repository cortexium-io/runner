package github

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanningReviewBoundaryKnownContradictions(t *testing.T) {
	for _, condition := range []string{
		"The pilot is actually merged to develop through normal Runner delivery.",
		"This plan's final PR has been merged into develop.",
		"Establish actual automatic merged delivery to develop and the final completion check for the parent from the merged PR and real gate evidence.",
		"Independent card QA validates focused behavior. Final delivery evidence must bind whole-plan QA and Runner-owned locked preparation/complete verification.",
		"Whole-plan QA and complete verification have passed and the final PR is merged to develop.",
		"Whole-plan QA has passed.",
		"Runner-owned complete verification passes.",
		"The umbrella item is closed.",
		"Fixture checks pass and this plan is already delivered.",
		"Tests demonstrate fixture behavior and this plan is already delivered.",
		"The final PR\nhas been merged into develop.",
		"This card is not delivered, but the final PR must be merged.",
	} {
		t.Run(condition, func(t *testing.T) {
			err := validatePlanningReviewConditions("proof_obligations", []string{condition})
			if err == nil || !strings.Contains(err.Error(), "proof_obligations[0]") {
				t.Fatalf("missed known contradiction: %v", err)
			}
		})
	}
}

func TestPlanningReviewBoundaryPreservesCandidateProofAndConstraints(t *testing.T) {
	conditions := []string{
		"A fixture demonstrates that confirmed merges complete the parent.",
		"Tests demonstrate that the final PR is merged once, even after a restart.",
		"Tests prove duplicate reconciliation is harmless and the final PR is merged only once.",
		"Verify actual automatic merge into a temporary fixture repository.",
		"Verify actual delivery to develop is not required before QA.",
		"An actual Git merge in a temporary repository exercises reconciliation.",
		"Implement automatic merge reconciliation with exact candidate verification.",
		"The reviewer correctly rejects missing evidence.",
		"The final PR is not merged; documentation truthfully marks delivery pending.",
		"The final PR is not required for focused card acceptance.",
		"Final delivery evidence remains pending until after acceptance.",
		"Final delivery evidence is not a prerequisite for whole-plan QA.",
		"Final delivery evidence remains pending until after whole-plan QA.",
		"Whole-plan QA remains pending, not a card acceptance prerequisite.",
		"Run the explicitly approved host-only check before QA; do not reschedule it.",
		"Historical parent #123 was merged; retain its original receipt as historical proof.",
	}
	before := append([]string(nil), conditions...)
	if err := validatePlanningReviewConditions("verification", conditions); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, conditions) {
		t.Fatal("lint rewrote requirements")
	}
}

func TestPlanningMemberBoundaryUsesOnlyLocalConditionSections(t *testing.T) {
	bad := "The pilot is actually merged to develop through normal Runner delivery."
	item := WorkItem{Title: "Pilot", Body: FormatPlannedItemBody(PlannedItem{
		Summary: "Implement focused behavior", AcceptanceCriteria: []string{"Behavior works"}, Verification: []string{"Focused behavior is demonstrated"},
		ProjectConstraints: []string{bad}, ProjectSource: "## Proof obligations\n- " + bad,
	}) + "\n\n## Downstream delivery requirements — not card acceptance prerequisites\n- " + bad}
	if err := validatePlanningMemberReviewBoundary(item); err != nil {
		t.Fatalf("historical request or downstream scope treated as pre-QA proof: %v", err)
	}
	for _, section := range []string{"Acceptance criteria", "Proof obligations", "Required verification", "Planned verification"} {
		changed := item
		changed.Body += "\n## " + section + "\n- [ ] This plan's final PR\n  has been merged into develop."
		if err := validatePlanningMemberReviewBoundary(changed); err == nil {
			t.Fatalf("missed multiline %s", section)
		}
	}
}

func TestPlanningBoundaryRejectsBeforeStagingWrites(t *testing.T) {
	p, _, children := deliveryFixture(t) // nil transport: no remote calls allowed
	bad := "The final PR\nhas been merged into develop."
	for _, requirement := range []string{bad, "[ ] The final PR has been merged into develop.", "Focused behavior works\n- This plan is already delivered."} {
		if _, err := p.CreateStaged(t.Context(), PlannedItem{Title: "Pilot", Verification: []string{requirement}}); err == nil || !strings.Contains(err.Error(), "review boundary") {
			t.Fatalf("creation did not reject before remote calls: %v", err)
		}
	}
	if err := p.StageDeliveryPlanningApproval(t.Context(), AuthorizedAction{}, children, PlanManifest{SuccessCriteria: []string{bad}}, "staged"); err == nil || !strings.Contains(err.Error(), "review boundary") {
		t.Fatalf("parent staging did not reject before remote calls: %v", err)
	}
	if err := ValidateDeliveryPlanningProposal([]string{"Whole-plan QA has passed."}, nil); err == nil {
		t.Fatal("parent QA acceptance required proof of its own acceptance")
	}
}

func TestPlanningBoundaryChecksPreviouslyStagedBatchButNotReleasedAuthority(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	manifest, _, err := ParsePlanManifest(parent.Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SuccessCriteria = []string{"This plan's final PR has been merged into develop."}
	parent.Body, err = FormatPlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parent.PlanRelease, err = p.signPlanningBatch(parent, children, batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	parent = signDeliveryFixture(t, p, parent, "planner", "backlog")
	d, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...))
	if err != nil {
		t.Fatalf("new lint invalidated old released authority: %v", err)
	}
	request := PlanAmendmentRequest{ExpectedRevision: d.Revision, Reason: "Clarify pre-review candidate proof", Manifest: d.Manifest}
	request.Manifest.Amendment++
	request.Manifest.SuccessCriteria = []string{"The candidate works; actual delivery remains pending."}
	state, err := p.buildPlanAmendment(d, request)
	if err != nil {
		t.Fatalf("old contract cannot be amended: %v", err)
	}
	if err := p.ValidatePlanAmendmentState(state); err != nil {
		t.Fatal(err)
	}
	if err := validatePlanningDeliveryReviewBoundary(request.Manifest, state.After[1:]); err != nil {
		t.Fatal(err)
	}
	// A saved, already-authorized amendment remains replayable after an upgrade.
	request.Manifest.SuccessCriteria = manifest.SuccessCriteria
	request.Manifest.Scope = []string{"Changed scope in a historical amendment"}
	priorIntent, err := p.buildPlanAmendment(d, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ValidatePlanAmendmentState(priorIntent); err != nil {
		t.Fatalf("new lint changed protected recovery semantics: %v", err)
	}
	if err := validatePlanningDeliveryReviewBoundary(request.Manifest, priorIntent.After[1:]); err == nil {
		t.Fatal("same contract would pass admission as a new amendment")
	}

	p.cfg.AssessmentStatus = "Assessment"
	p.cfg.PlanningDestinations = map[string]string{"plan": "Ready"}
	parent.Status, parent.Phase, parent.PlanRelease, parent.Branch = "Assessment", PlanningApprovalPhase, "", ""
	for i := range children {
		children[i].Status, children[i].Phase, children[i].Approval, children[i].Branch, children[i].QACommit = "Assessment", "", "", "", ""
	}
	parent.Approval, err = p.signPlanningBatch(parent, children, batchStagedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.planBatchApproval(append([]WorkItem{parent}, children...), parent); err == nil || !strings.Contains(err.Error(), "review boundary") {
		t.Fatalf("old staged proposal bypassed new approval guard: %v", err)
	}
	manifest.SuccessCriteria = []string{"Candidate behavior works"}
	parent.Body, err = FormatPlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	children[0].Body += "\n\n## Proof obligations\n- Whole-plan QA has passed."
	parent.Approval, err = p.signPlanningBatch(parent, children, batchStagedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.planBatchApproval(append([]WorkItem{parent}, children...), parent); err == nil || !strings.Contains(err.Error(), "review boundary") {
		t.Fatalf("staged child bypassed new approval guard: %v", err)
	}
}

func TestPlanningBoundaryDoesNotUndoHistoricalCompletion(t *testing.T) {
	p, items := completedDeliveryFixture(t)
	manifest, _, err := ParsePlanManifest(items[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SuccessCriteria = []string{"The final PR is merged into develop."}
	items[0].Body, err = FormatPlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	items[0].PlanRelease, err = p.signPlanningBatch(items[0], items[1:], batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	items[0] = signDeliveryFixture(t, p, items[0], "reviewer", "done")
	before := append([]WorkItem(nil), items...)
	if _, err := p.requireCompletedPreRolloutWork(items); err != nil {
		t.Fatalf("new lint invalidated delivered history: %v", err)
	}
	if !p.dependenciesSatisfied(WorkItem{ID: "external", Dependencies: []string{items[0].ID}}, items) || !reflect.DeepEqual(items, before) {
		t.Fatal("historical delivery authority changed")
	}
}
