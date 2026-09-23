package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

// Count actual transport reads using the existing signed delivery fixture.
// Schema is preloaded, and the three-item board fits in one response page.
type deliveryReadRunner struct {
	items                    []WorkItem
	completeReads, nodeReads int
	mutations                int
	afterNodeRead            func()
}

func (r *deliveryReadRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	if command != "gh" {
		return subprocess.Result{}, fmt.Errorf("unexpected command %q", command)
	}
	var response any
	switch {
	case strings.Contains(joined, "query=query($project_id:"):
		r.completeReads++
		nodes := make([]any, 0, len(r.items))
		for _, item := range r.items {
			nodes = append(nodes, deliveryReadNode(item))
		}
		response = map[string]any{"data": map[string]any{"node": map[string]any{"items": map[string]any{"nodes": nodes}}}}
	case strings.Contains(joined, "query=query($item_id:"):
		r.nodeReads++
		var node any
		for _, item := range r.items {
			if strings.Contains(joined, "item_id="+item.ID) {
				node = deliveryReadNode(item)
			}
		}
		response = map[string]any{"data": map[string]any{"node": node}}
		if r.afterNodeRead != nil {
			r.afterNodeRead()
		}
	case strings.Contains(joined, "query=mutation(") || strings.HasPrefix(joined, "project item-edit "):
		r.mutations++
		response = map[string]any{"data": map[string]any{}}
	default:
		return subprocess.Result{}, fmt.Errorf("unexpected GitHub command %q", joined)
	}
	encoded, err := json.Marshal(response)
	return subprocess.Result{Stdout: string(encoded)}, err
}

func deliveryReadNode(item WorkItem) map[string]any {
	node := map[string]any{
		"id": item.ID, "status": map[string]string{"name": item.Status},
		"qaFailures": map[string]int{"number": item.QAFailures},
		"content": map[string]any{"title": item.Title, "body": item.Body,
			"repository": map[string]string{"nameWithOwner": item.Repository}},
	}
	for field, value := range map[string]string{
		"approval": item.Approval, "planRelease": item.PlanRelease, "result": item.Result,
		"phase": item.Phase, "transition": item.Transition, "activity": item.Activity,
		"branch": item.Branch, "pullRequest": item.PullRequest, "qaCommit": item.QACommit,
	} {
		node[field] = map[string]string{"text": value}
	}
	return node
}

func deliveryReadFixture(t *testing.T) (*Project, *deliveryReadRunner, AuthorizedAction, AuthorizedAction) {
	t.Helper()
	p, parent, children := deliveryFixture(t)
	// Round-trip the fixture through the real Project decoder, including its
	// approved, visible planning metadata, instead of bypassing transport.
	for i, child := range children {
		planned := PlannedItem{Repository: child.Repository, PlanningSourceID: child.PlanningSourceID,
			PlanningSourceLane: child.PlanningSourceLane, PlanningSourceFingerprint: child.PlanningSourceFingerprint,
			PlanningDestination: child.PlanningDestination, PlanningBatchFingerprint: child.PlanningBatchFingerprint,
			PlanningBatchSize: child.PlanningBatchSize, PlanningItemIndex: child.PlanningItemIndex,
			ImplementationProfile: child.ImplementationProfile, DependencyIDsResolved: true}
		for _, id := range child.Dependencies {
			planned.ResolvedDependencies = append(planned.ResolvedDependencies, PlannedDependency{ItemID: id, Title: id})
		}
		child.Body = appendPlannedItemMetadata(child.Body, planned)
		children[i] = signDeliveryFixture(t, p, child, child.Role, p.laneIDForStatus(child.Status))
	}
	var err error
	parent.PlanRelease, err = p.signPlanningBatch(parent, children, batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	parent.QACommit = strings.Repeat("c", 40)
	parentAction, err := p.signAction(parent, "planner", "backlog")
	if err != nil {
		t.Fatal(err)
	}
	memberAction, err := p.validateAction(children[1])
	if err != nil {
		t.Fatal(err)
	}
	run := &deliveryReadRunner{items: append([]WorkItem{parentAction.Item}, children...)}
	p.run = run
	p.cfg.ApprovalField, p.cfg.ResultField, p.cfg.PhaseField = "Approval", "Result", "Phase"
	p.cfg.QACommitField, p.cfg.QAFailuresField = "QA Commit", "QA Failures"
	p.schema = githubProjectSchema{ProjectID: "PVT_test", Fields: map[string]githubProjectField{}}
	for _, name := range []string{p.approvalFieldName(), p.resultFieldName(), p.phaseFieldName(), p.qaCommitFieldName(), p.qaFailuresFieldName(), p.activityFieldName(), p.transitionFieldName()} {
		p.schema.Fields[normalizeProjectKey(name)] = githubProjectField{ID: "F_" + normalizeProjectKey(name), Name: name, Type: "ProjectV2Field"}
	}
	p.schema.Fields[normalizeProjectKey("Status")] = githubProjectField{ID: "F_status", Name: "Status", Type: "ProjectV2SingleSelectField",
		Options: map[string]githubProjectOption{normalizeProjectKey("Backlog"): {ID: "O_backlog", Name: "Backlog"}}}
	return p, run, parentAction, memberAction
}

func TestDeliveryOperationProjectReads(t *testing.T) {
	// Measured before optimization: each delivery transition below made two
	// complete reads and one exact-item read, then three writes. Authorization
	// operations already used one complete and one exact-item read.
	for _, tc := range []struct {
		name          string
		call          func(*Project, AuthorizedAction, AuthorizedAction) error
		completeReads int
		mutations     int
	}{
		{"authorize", func(p *Project, parent, _ AuthorizedAction) error {
			_, err := p.Authorize(t.Context(), parent.Item)
			return err
		}, 1, 0},
		{"refresh action", func(p *Project, parent, _ AuthorizedAction) error {
			_, err := p.RefreshAction(t.Context(), parent)
			return err
		}, 1, 0},
		{"refresh delegated content", func(p *Project, parent, _ AuthorizedAction) error {
			_, _, err := p.RefreshDelegatedContent(t.Context(), parent)
			return err
		}, 1, 0},
		{"begin repair", func(p *Project, parent, _ AuthorizedAction) error {
			return p.BeginPlanRepair(t.Context(), parent, "v1:"+strings.Repeat("d", 64), strings.Repeat("e", 40), 1)
		}, 1, 3},
		{"begin integration", func(p *Project, parent, member AuthorizedAction) error {
			return p.BeginPlanIntegration(t.Context(), parent, member.Item.ID, strings.Repeat("e", 40), parent.Item.QACommit)
		}, 1, 3},
		{"member integrated", func(p *Project, _, member AuthorizedAction) error {
			return p.TransitionPlanIntegrated(t.Context(), member, strings.Repeat("e", 40))
		}, 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, run, parent, member := deliveryReadFixture(t)
			if err := tc.call(p, parent, member); err != nil {
				t.Fatal(err)
			}
			t.Logf("complete Project reads=%d, exact-item reads=%d, mutations=%d", run.completeReads, run.nodeReads, run.mutations)
			if run.completeReads != tc.completeReads || run.nodeReads != 1 || run.mutations != tc.mutations {
				t.Fatalf("unexpected read/write counts: complete=%d exact-item=%d mutations=%d", run.completeReads, run.nodeReads, run.mutations)
			}
		})
	}
}

func TestPlanRepairRefreshesAfterPriorTransition(t *testing.T) {
	p, run, parent, _ := deliveryReadFixture(t)
	digest, candidate := "v1:"+strings.Repeat("d", 64), strings.Repeat("e", 40)
	if err := p.BeginPlanRepair(t.Context(), parent, digest, candidate, 1); err != nil {
		t.Fatal(err)
	}
	// Operator revocation after a completed Project mutation must not reuse
	// the previous transition's authority or delivery observation.
	run.items[0].Approval = ""
	err := p.BeginPlanRepair(t.Context(), parent, digest, candidate, 1)
	if err == nil || run.nodeReads != 2 || run.completeReads != 1 || run.mutations != 3 {
		t.Fatalf("prior transition reused authority: exact-item=%d complete=%d mutations=%d err=%v", run.nodeReads, run.completeReads, run.mutations, err)
	}
}

func TestPlanMemberTransitionRejectsConcurrentSignedLifecycle(t *testing.T) {
	p, run, _, member := deliveryReadFixture(t)
	run.afterNodeRead = func() {
		run.items[2].Activity = "Changed by another operation"
		run.items[2] = signDeliveryFixture(t, p, run.items[2], "implementer", "ready")
	}
	err := p.TransitionPlanIntegrated(t.Context(), member, strings.Repeat("e", 40))
	if err == nil || run.mutations != 0 {
		t.Fatalf("stale member lifecycle reached transition: mutations=%d err=%v", run.mutations, err)
	}
}

func TestPlanIntegrationRejectsUnapprovedTarget(t *testing.T) {
	p, run, parent, _ := deliveryReadFixture(t)
	err := p.BeginPlanIntegration(t.Context(), parent, "PVTI_unapproved", strings.Repeat("e", 40), parent.Item.QACommit)
	if err == nil || !strings.Contains(err.Error(), "not an approved plan member") || run.mutations != 0 {
		t.Fatalf("operation-specific membership check was skipped: mutations=%d err=%v", run.mutations, err)
	}
}

func TestPlanIntegrationRechecksDeliveryAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Project, *deliveryReadRunner)
	}{
		{"revoked parent", func(_ *Project, r *deliveryReadRunner) { r.items[0].Approval = "" }},
		{"revoked release", func(_ *Project, r *deliveryReadRunner) { r.items[0].PlanRelease = "" }},
		{"revoked sibling", func(_ *Project, r *deliveryReadRunner) { r.items[1].Approval = "" }},
		{"missing sibling", func(_ *Project, r *deliveryReadRunner) { r.items = append(r.items[:1], r.items[2:]...) }},
		{"extra member", func(_ *Project, r *deliveryReadRunner) {
			extra := r.items[1]
			extra.ID = "PVTI_extra"
			r.items = append(r.items, extra)
		}},
		{"edited contract", func(_ *Project, r *deliveryReadRunner) { r.items[1].Body = "Unapproved replacement" }},
		{"transition lock", func(_ *Project, r *deliveryReadRunner) { r.items[0].Transition = transitionLockValue }},
		{"concurrent signed head", func(p *Project, r *deliveryReadRunner) {
			r.items[0].QACommit = strings.Repeat("f", 40)
			r.items[0] = signDeliveryFixture(t, p, r.items[0], "planner", "backlog")
		}},
		{"cancelled", func(p *Project, r *deliveryReadRunner) {
			r.items[0].Phase = PlanCancelledPhase
			r.items[0] = signDeliveryFixture(t, p, r.items[0], "planner", "backlog")
		}},
	} {
		for _, concurrent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/concurrent=%t", tc.name, concurrent), func(t *testing.T) {
				p, run, parent, member := deliveryReadFixture(t)
				if _, present, err := p.DeliveryForItem(t.Context(), parent.Item); err != nil || !present {
					t.Fatalf("initial valid observation: present=%t err=%v", present, err)
				}
				if concurrent {
					run.afterNodeRead = func() { tc.change(p, run) }
				} else {
					tc.change(p, run)
				}
				err := p.BeginPlanIntegration(t.Context(), parent, member.Item.ID, strings.Repeat("e", 40), parent.Item.QACommit)
				if err == nil || run.mutations != 0 {
					t.Fatalf("changed authority reached Project mutation: mutations=%d err=%v", run.mutations, err)
				}
			})
		}
	}
}
