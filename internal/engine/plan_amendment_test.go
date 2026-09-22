package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func amendmentForLastMember(t *testing.T, f *deliveryRunFixture) github.PlanAmendmentRequest {
	t.Helper()
	parent := f.parent(t)
	manifest, _, err := github.ParsePlanManifest(parent.Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Amendment++
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	last := manifest.Members[len(manifest.Members)-1].ID
	var body string
	for _, item := range items {
		if item.ID == last {
			body = strings.Replace(item.Body, "## Acceptance criteria", "Additional approved requirement: preserve the first behavior.\n\n## Acceptance criteria", 1)
		}
	}
	if body == "" {
		t.Fatal("fixture member absent")
	}
	return github.PlanAmendmentRequest{ExpectedRevision: github.PlanRevision(parent.Body), Reason: "Refine only the dependent outcome", Manifest: manifest, MemberBodies: map[string]string{last: body}}
}

func TestDeliveryAmendmentCarriesOnlyUnaffectedOriginalAcceptance(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	prior, _ := f.service.source.LifecycleItems(t.Context())
	for _, item := range prior {
		action, err := f.service.source.Authorize(t.Context(), item)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.source.TransitionRejection(t.Context(), action, item.Status, item.Phase, "Retained historical finding", 2); err != nil {
			t.Fatal(err)
		}
	}
	parent := f.parent(t)
	if err := f.service.writeReviewFeedback(reviewFeedbackRecord{Version: reviewFeedbackVersion, ItemID: parent.ID, DelegatedContentDigest: github.DelegatedContentFor(parent).Digest, Items: []string{"Prior explicit feedback remains historical"}}); err != nil {
		t.Fatal(err)
	}
	request := amendmentForLastMember(t, f)
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Affected) != 1 || len(preview.CarriedAcceptance) != 1 {
		t.Fatalf("wrong selective effect: affected=%v carried=%v", preview.Affected, preview.CarriedAcceptance)
	}
	before := f.parent(t)
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	after := f.parent(t)
	if after.QACommit != before.QACommit || after.QAFailures != before.QAFailures || after.Status != "Backlog" || after.Phase != github.PlanDeliveryPhase {
		t.Fatal("parent acceptance invalidation lost integrated head/history")
	}
	items, _ := f.service.source.LifecycleItems(t.Context())
	d, err := f.service.source.ValidatePlanDelivery(after, items)
	if err != nil {
		t.Fatal(err)
	}
	feedback, err := f.service.readReviewFeedbackRecord(after)
	if err != nil || feedback == nil || feedback.Items[0] != "Prior explicit feedback remains historical" {
		t.Fatal("amendment discarded feedback")
	}
	for i, old := range preview.record.State.Before[1:] {
		var current github.WorkItem
		for _, item := range d.Children {
			if item.ID == old.ID {
				current = item
			}
		}
		if old.ID == preview.Affected[0] {
			if current.Status != "Ready" || current.QACommit != "" || current.Body == old.Body || current.QAFailures != 2 || current.Result != old.Result {
				t.Fatal("affected member kept acceptance")
			}
			continue
		}
		if !reflect.DeepEqual(current, old) {
			t.Fatal("unaffected lifecycle changed")
		}
		retained := preview.record.Workspaces[i+1]
		provider := workspace.NewGitProviderWithLimits(f.service.run, f.service.snapshotLimits())
		req, err := f.service.amendmentWorkspaceRequest(t.Context(), preview.record.State, i+1, github.DelegatedContentFor(old).Digest)
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := provider.InspectRetainedReview(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := f.service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		carried, found, err := provider.LoadPublicationAcceptance(t.Context(), metadata, snapshot, workspace.PublicationEvidence{PlanRevision: d.Revision})
		if err != nil || !found || carried.AcceptanceReport != retained.Acceptance.AcceptanceReport || carried.CarriedAcceptanceDigest != workspace.PublicationAcceptanceDigest(*retained.Acceptance) || carried.CarriedFromRevision != request.ExpectedRevision {
			t.Fatalf("original historical acceptance not preserved: found=%v err=%v", found, err)
		}
	}
	if f.runner.implementations != 2 || f.runner.reviews != 2 || f.runner.creates != 0 {
		t.Fatal("amendment performed model/publication work")
	}
	encoded, _ := json.Marshal(preview)
	if strings.Contains(string(encoded), `"Approval"`) || strings.Contains(string(encoded), `"PlanRelease"`) {
		t.Fatal("preview leaked authority")
	}
	// Production admission must resume the changed child from current combined
	// state; it may not reuse its superseded implementation checkpoint/proof.
	result, err := f.service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result {
		if r.Outcome == "blocked" {
			t.Fatalf("amended child could not resume: %s %s", r.Summary, r.Error)
		}
	}
}

type amendmentCrashRunner struct {
	*deliveryMilestoneRunner
	boundary string
	offline  bool
}

func (r *amendmentCrashRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}
func (r *amendmentCrashRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if r.offline {
		return subprocess.Result{}, context.Canceled
	}
	result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
	joined := strings.Join(args, " ")
	if err == nil && command == "gh" && (r.boundary == "body" && strings.HasPrefix(joined, "issue edit ") || r.boundary == "fence" && strings.Contains(joined, github.PlanAmendingPhase) || r.boundary == "finish" && strings.Contains(joined, github.PlanDeliveryPhase)) {
		r.offline = true
		return result, context.Canceled
	}
	return result, err
}

func TestDeliveryAmendmentResumesExactIntentAfterInterruptedWrites(t *testing.T) {
	for _, boundary := range []string{"fence", "body", "finish"} {
		t.Run(boundary, func(t *testing.T) {
			f := newProductionDelivery(t)
			f.integrateMembers(t)
			request := amendmentForLastMember(t, f)
			transport := &amendmentCrashRunner{deliveryMilestoneRunner: f.runner, boundary: boundary}
			var err error
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil || !transport.offline {
				t.Fatalf("crash not reached: %v", err)
			}
			transport.offline = false
			transport.boundary = ""
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			// Real coordinator recovery; zero admission proves it doesn't replay any
			// model work to finish an approved deterministic transaction.
			if _, err := f.service.preparePoll(t.Context(), 0, true, nil); err != nil {
				t.Fatal(err)
			}
			parent := f.parent(t)
			if github.PlanRevision(parent.Body) != preview.Revision || parent.Transition != "" || parent.Phase != github.PlanDeliveryPhase {
				t.Fatal("recovery did not finish exact new revision")
			}
			feedback, err := f.service.readReviewFeedbackRecord(parent)
			if err != nil || feedback == nil || !feedback.PlanAmendment.Completed {
				t.Fatal("durable intent not completed")
			}
			if f.runner.implementations != 2 || f.runner.reviews != 2 || f.runner.creates != 0 {
				t.Fatal("recovery repeated model work")
			}
		})
	}
}

func TestDeliveryAmendmentPreservesUnexpectedOperatorChange(t *testing.T) {
	f := newProductionDelivery(t)
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, amendmentForLastMember(t, f))
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == preview.Affected[0] {
			f.runner.project.remoteItems[i].Body += "\nOperator change"
		}
	}
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil {
		t.Fatal("overwrote changed preview")
	}
	if _, err := f.service.readReviewFeedbackRecord(f.parent(t)); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDeliveryAmendmentRecoveryRefusesOperatorChangeInsideFence(t *testing.T) {
	f := newProductionDelivery(t)
	transport := &amendmentCrashRunner{deliveryMilestoneRunner: f.runner, boundary: "body"}
	var err error
	f.service, err = New(f.cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, amendmentForLastMember(t, f))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil || !transport.offline {
		t.Fatal("fixture did not interrupt inside fence")
	}
	transport.offline = false
	transport.boundary = ""
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == preview.Affected[0] {
			f.runner.project.remoteItems[i].Result = "Intervening operator decision"
		}
	}
	before := append([]github.WorkItem(nil), f.runner.project.remoteItems...)
	f.service, err = New(f.cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.preparePoll(t.Context(), 0, true, nil); err == nil {
		t.Fatal("recovery overwrote intervention")
	}
	if !reflect.DeepEqual(before, f.runner.project.remoteItems) {
		t.Fatal("refused recovery still mutated Project")
	}
	if f.runner.implementations != 0 || f.runner.reviews != 0 {
		t.Fatal("refused recovery admitted work")
	}
}

// Inject at the fresh item inspection after the whole-plan check, not before
// the check. Transport remains real at the production Project boundary.
type amendmentInspectionRunner struct {
	*deliveryMilestoneRunner
	reads, mutateAt int
	itemID, field   string
	mutated         bool
	afterMutation   []github.WorkItem
	writes          int
	writesBefore    int
}

func (r *amendmentInspectionRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *amendmentInspectionRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	if command == "gh" && isLifecycleItemsCall(joined) {
		r.reads++
		if r.reads == r.mutateAt {
			for i := range r.project.remoteItems {
				item := &r.project.remoteItems[i]
				if item.ID != r.itemID {
					continue
				}
				if r.field == "body" {
					item.Body += "\nIntervening operator requirement"
				} else {
					item.Result = "Intervening operator result"
				}
				r.mutated = true
			}
			r.afterMutation = append([]github.WorkItem(nil), r.project.remoteItems...)
		}
	}
	if command == "gh" && (strings.HasPrefix(joined, "project item-edit ") || strings.HasPrefix(joined, "issue edit ") || isBatchProjectUpdateCall(joined)) {
		if r.mutated {
			r.writes++
		} else {
			r.writesBefore++
		}
	}
	return r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
}

func TestDeliveryAmendmentRechecksFreshItemBeforeWriting(t *testing.T) {
	for _, operation := range []string{"fence", "contracts", "finish", "transition readback"} {
		for _, field := range []string{"body", "result"} {
			t.Run(operation+"/"+field, func(t *testing.T) {
				f := newProductionDelivery(t)
				preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, amendmentForLastMember(t, f))
				if err != nil {
					t.Fatal(err)
				}
				state := preview.record.State
				if operation == "contracts" || operation == "finish" {
					if err := f.service.source.FencePlanAmendment(t.Context(), state); err != nil {
						t.Fatal(err)
					}
				}
				if operation == "finish" {
					if err := f.service.source.WritePlanAmendmentContracts(t.Context(), state); err != nil {
						t.Fatal(err)
					}
				}
				transport := &amendmentInspectionRunner{deliveryMilestoneRunner: f.runner, mutateAt: 2, itemID: f.parentID, field: field}
				if operation == "contracts" {
					transport.itemID = state.Before[1].ID
				} else if operation == "finish" {
					transport.mutateAt = len(state.After) + 1 // check, exact children, fresh parent
				} else if operation == "transition readback" {
					transport.mutateAt = 3 // check, fresh parent, newly locked parent
				}
				f.service, err = New(f.cfg, transport)
				if err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "fence", "transition readback":
					err = f.service.source.FencePlanAmendment(t.Context(), state)
				case "contracts":
					err = f.service.source.WritePlanAmendmentContracts(t.Context(), state)
				case "finish":
					err = f.service.source.FinishPlanAmendment(t.Context(), state)
				}
				if !transport.mutated || err == nil {
					t.Fatalf("fresh operator change was not refused: injected=%v err=%v", transport.mutated, err)
				}
				if transport.writes != 0 || !reflect.DeepEqual(transport.afterMutation, f.runner.project.remoteItems) {
					t.Fatalf("mutated Project after fresh operator change: writes=%d", transport.writes)
				}
				if operation == "transition readback" && transport.writesBefore != 1 {
					t.Fatalf("contract fields changed before lock readback: writes=%d", transport.writesBefore)
				}
			})
		}
	}
}
