package github

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func deliveryFixture(t *testing.T) (*Project, WorkItem, []WorkItem) {
	t.Helper()
	gate := config.VerificationEntrypoint{Command: "npm", Args: []string{"run", "validate:complete"}, TimeoutSeconds: 7200}
	profileDigest := "v1:" + strings.Repeat("a", 64)
	p := NewProject(config.ProjectConfig{
		GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 1, IntakeRepository: "owner/repo", BaseBranch: "develop"},
		PlanDelivery:        true, PlanVerificationID: "complete", PlanVerificationDigest: gate.Digest(),
		PlanProfileDigests: map[string]string{"implementer": profileDigest},
		BacklogStatus:      "Backlog", ReadyStatus: "Ready", RunningStatus: "In progress", QAStatus: "Agent QA", DoneStatus: "Done", BlockedStatus: "Blocked",
		InitialRole: "planner", InitialLaneID: "plan", AgentStatuses: []string{"Ready", "Agent QA"},
		LaneStatuses: map[string]string{"backlog": "Backlog", "ready": "Ready", "agent_qa": "Agent QA", "done": "Done", "blocked": "Blocked"},
		LaneRoles:    map[string]string{"ready": "implementer", "agent_qa": "reviewer"},
	}, nil)
	parent := WorkItem{ID: "PVTI_parent", Title: "Approved outcome", Body: "Deliver the requested outcome only.", Repository: "owner/repo"}
	fingerprint := PlanningSourceFingerprint(parent)
	children := []WorkItem{
		{ID: "PVTI_a", Title: "First", Body: "Implement first behavior.", Repository: "owner/repo", Status: "Backlog", Phase: PlanIntegratedPhase, Branch: "runner/a", QACommit: strings.Repeat("a", 40), PlanningSourceID: parent.ID, PlanningSourceLane: "plan", PlanningSourceFingerprint: fingerprint, PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: 1, ImplementationProfile: "implementer"},
		{ID: "PVTI_b", Title: "Second", Body: "Implement dependent behavior.", Repository: "owner/repo", Dependencies: []string{"PVTI_a"}, Status: "Ready", PlanningSourceID: parent.ID, PlanningSourceLane: "plan", PlanningSourceFingerprint: fingerprint, PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: 2, ImplementationProfile: "implementer"},
	}
	manifest := PlanManifest{Version: 1, Request: parent.Body, Outcome: "One complete outcome", SuccessCriteria: []string{"Both behaviors work together"}, Scope: []string{"No unrelated changes"}, Repository: parent.Repository, DestinationBranch: "develop", CompleteVerification: "complete", VerificationDigest: gate.Digest(), Members: []PlanMember{
		{ID: children[0].ID, Dependencies: canonicalDelegatedDependencies(nil), ImplementationProfile: "implementer", ProfileDigest: profileDigest, ProfileReason: "Bounded behavior"},
		{ID: children[1].ID, Dependencies: []string{children[0].ID}, ImplementationProfile: "implementer", ProfileDigest: profileDigest, ProfileReason: "Dependent bounded behavior"},
	}}
	var err error
	parent.Body, err = FormatPlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if PlanningSourceFingerprint(parent) != fingerprint {
		t.Fatal("adding proposed delivery contract changed original planning provenance")
	}
	parent.PlanRelease, err = p.signPlanningBatch(parent, children, batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	parent.Status, parent.Phase, parent.Branch = "Backlog", PlanDeliveryPhase, PlanBranch(parent.ID)
	parent = signDeliveryFixture(t, p, parent, "planner", "backlog")
	children[0] = signDeliveryFixture(t, p, children[0], "reviewer", "backlog")
	children[1] = signDeliveryFixture(t, p, children[1], "implementer", "ready")
	return p, parent, children
}

func signDeliveryFixture(t *testing.T, p *Project, item WorkItem, role, state string) WorkItem {
	t.Helper()
	action, err := p.signAction(item, role, state)
	if err != nil {
		t.Fatal(err)
	}
	return action.Item
}

func TestPlanReleaseBindsManifestAndMembersIndependentlyOfLifecycle(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	all := append([]WorkItem{parent}, children...)
	if _, err := p.ValidatePlanDelivery(parent, all); err != nil {
		t.Fatal(err)
	}
	if len(parent.PlanRelease) > maxProjectTextFieldBytes {
		t.Fatal("release exceeds Project field bound")
	}
	changed := parent
	changed.Status, changed.Phase = "Agent QA", ""
	changed = signDeliveryFixture(t, p, changed, "reviewer", "agent_qa")
	if _, err := p.ValidatePlanDelivery(changed, append([]WorkItem{changed}, children...)); err != nil {
		t.Fatalf("ordinary parent lifecycle invalidated immutable release: %v", err)
	}
	for name, mutate := range map[string]func(*WorkItem, []WorkItem){
		"shared outcome": func(p *WorkItem, _ []WorkItem) {
			p.Body = strings.Replace(p.Body, "One complete outcome", "Different outcome", 1)
		},
		"profile reason": func(p *WorkItem, _ []WorkItem) {
			p.Body = strings.Replace(p.Body, "Bounded behavior", "Unapproved reason", 1)
		},
		"destination": func(p *WorkItem, _ []WorkItem) {
			p.Body = strings.Replace(p.Body, `"destination_branch":"develop"`, `"destination_branch":"main"`, 1)
		},
		"child content":    func(_ *WorkItem, c []WorkItem) { c[0].Body = "Broadened scope" },
		"child profile":    func(_ *WorkItem, c []WorkItem) { c[0].ImplementationProfile = "implementer_astra" },
		"child dependency": func(_ *WorkItem, c []WorkItem) { c[1].Dependencies = nil },
		"child identity":   func(_ *WorkItem, c []WorkItem) { c[0].ID = "PVTI_replacement" },
		"cancelled":        func(p *WorkItem, _ []WorkItem) { p.Phase = PlanCancelledPhase },
	} {
		t.Run(name, func(t *testing.T) {
			current := parent
			cs := append([]WorkItem(nil), children...)
			mutate(&current, cs)
			// Even a current signed lifecycle update cannot authorize manifest drift.
			current = signDeliveryFixture(t, p, current, "planner", "backlog")
			if _, err := p.ValidatePlanDelivery(current, append([]WorkItem{current}, cs...)); err == nil {
				t.Fatal("changed plan retained release authority")
			}
		})
	}
}

func TestPlanDependencyEligibilityDoesNotLeakAcrossRequesters(t *testing.T) {
	for _, externalFirst := range []bool{false, true} {
		t.Run(map[bool]string{true: "external_first", false: "internal_first"}[externalFirst], func(t *testing.T) {
			p, parent, children := deliveryFixture(t)
			external := WorkItem{ID: "PVTI_external", Dependencies: []string{children[0].ID}}
			index := newWorkItemIndex(append([]WorkItem{parent, external}, children...))
			if externalFirst && p.dependenciesSatisfiedIn(external, index) {
				t.Fatal("external work admitted before plan delivery")
			}
			if !p.dependenciesSatisfiedIn(children[1], index) {
				t.Fatal("integrated same-plan prerequisite did not admit dependant")
			}
			if p.dependenciesSatisfiedIn(external, index) {
				t.Fatal("same-plan success poisoned external dependency cache")
			}
			children[1].Status, children[1].Phase, children[1].Branch, children[1].QACommit = "Backlog", PlanIntegratedPhase, "runner/b", strings.Repeat("b", 40)
			children[1] = signDeliveryFixture(t, p, children[1], "reviewer", "backlog")
			parent.Status, parent.Phase, parent.PullRequest, parent.QACommit = "Done", "", "https://github.com/owner/repo/pull/10", strings.Repeat("c", 40)
			parent = signDeliveryFixture(t, p, parent, "reviewer", "done")
			index = newWorkItemIndex(append([]WorkItem{parent, external}, children...))
			if !p.dependenciesSatisfiedIn(external, index) {
				t.Fatal("confirmed delivered plan did not release external dependant")
			}
		})
	}
}

func TestPlanReleaseRefusesDisabledRolloutMissingMemberAndChangedGate(t *testing.T) {
	p, parent, children := deliveryFixture(t)
	if _, err := p.ValidatePlanDelivery(parent, []WorkItem{parent, children[0]}); err == nil {
		t.Fatal("missing member accepted")
	}
	p.cfg.PlanVerificationDigest = "v1:" + strings.Repeat("f", 64)
	if _, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...)); err == nil {
		t.Fatal("changed command settings accepted")
	}
	p.cfg.PlanDelivery = false
	if _, err := p.ValidatePlanDelivery(parent, append([]WorkItem{parent}, children...)); err == nil {
		t.Fatal("disabled rollout admitted plan")
	}
}
