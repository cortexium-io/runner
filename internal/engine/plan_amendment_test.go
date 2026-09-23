package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/execution"
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
	feedbackPath := f.service.reviewFeedbackPath(f.parentID)
	history, err := os.ReadFile(feedbackPath)
	if err != nil {
		t.Fatal(err)
	}
	if feedback.PlanAmendment == nil || !feedback.PlanAmendment.Completed || feedback.DelegatedContentDigest != github.DelegatedContentFor(before).Digest || feedback.DelegatedContentDigest == github.DelegatedContentFor(after).Digest {
		t.Fatal("completed amendment did not retain feedback under its original contract")
	}
	archivePath := fmt.Sprintf("%s.amended-%x", feedbackPath, sha256.Sum256(history))
	// A crash after archival but before the fresh acceptance save must be
	// restartable without replacing the original bytes or repeating work.
	for attempt := 0; attempt < 2; attempt++ {
		if err := f.service.archiveDeliveryAmendmentHistory(f.parentID); err != nil {
			t.Fatal(err)
		}
		f.service, err = New(f.cfg, f.runner)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{feedbackPath, archivePath} {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, history) {
				t.Fatalf("archive/restart changed original feedback: %s: %v", path, err)
			}
		}
	}
	// Production admission must resume the changed child from current combined
	// state; it may not reuse its superseded implementation checkpoint/proof.
	for cycle := 0; cycle < 4 && f.parent(t).Status != "PR Ready"; cycle++ {
		results, err := f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("amended delivery cycle %d: %s: %s", cycle, result.Summary, result.Error)
			}
		}
	}
	parent = f.parent(t)
	if parent.Status != "PR Ready" || parent.PullRequest != "https://github.com/owner/repo/pull/12" || parent.QAFailures != 2 || f.runner.implementations != 3 || f.runner.reviews != 4 || f.runner.creates != 1 {
		t.Fatalf("amended delivery did not publish once with its original QA budget: status=%s failures=%d implementation=%d review=%d PR=%d", parent.Status, parent.QAFailures, f.runner.implementations, f.runner.reviews, f.runner.creates)
	}
	items, err = f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d, err = f.service.source.ValidatePlanDelivery(parent, items)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range d.Children {
		if child.Phase != github.PlanIntegratedPhase || child.QAFailures != 2 {
			t.Fatalf("amended member lost integration or historical QA count: %s phase=%s failures=%d", child.ID, child.Phase, child.QAFailures)
		}
	}
	current, err := f.service.readReviewFeedbackRecord(parent)
	if err != nil || current == nil || current.PlanVerification == nil {
		t.Fatalf("new parent acceptance missing: %v", err)
	}
	p := current.PlanVerification
	if current.DelegatedContentDigest != github.DelegatedContentFor(parent).Digest || current.PlanAmendment != nil || current.PlanRepair != nil || current.Baseline != nil || len(current.Items) != 0 || p.QAFailures != 2 || p.Assignment.Spec.PlanContext.Revision != preview.Revision || p.Assignment.Spec.DelegatedContentDigest != current.DelegatedContentDigest || p.Assignment.Spec.ApprovedBodySnapshot != github.DelegatedContentFor(parent).BodySnapshot {
		t.Fatal("new parent progress copied historical feedback or lost its fresh contract/count binding")
	}
	if p.Gate == nil || p.Gate.Receipt == nil || p.Gate.Historical || p.Gate.Receipt.Outcome != "passed" || p.Gate.Invocation.Outcome != "passed" || !p.Gate.Invocation.CleanupResolved || p.Failure != nil || p.Classification != nil || p.Publication == nil || p.Publication.PlanRevision != preview.Revision || p.Publication.CommitOID != parent.QACommit || p.Candidate.Head != parent.QACommit || p.Publication.VerificationDigest != p.EnvelopeDigest {
		t.Fatal("amended publication lacks fresh accepted whole-plan QA and passing complete proof")
	}
	for _, path := range []string{"member-1.txt", "member-2.txt"} {
		if got := strings.TrimSpace(runGitTest(t, f.repo, "show", parent.QACommit+":"+path)); got != "ready" {
			t.Fatalf("published amendment lost approved member behavior: %s=%q", path, got)
		}
	}
	if got, err := os.ReadFile(archivePath); err != nil || !bytes.Equal(got, history) {
		t.Fatalf("publication lost exact original feedback/amendment bytes: %v", err)
	}
	pushes := f.runner.planPushes
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RunCycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := retainedPlanProgress(t, f); !reflect.DeepEqual(got, p) || f.runner.implementations != 3 || f.runner.reviews != 4 || f.runner.creates != 1 || f.runner.planPushes != pushes {
		t.Fatal("published amendment restart repeated model, complete-gate, or publication work")
	}
	// Reuse the production acceptance for cheap boundary cases. Each case
	// restores independent historical bytes; no model or gate is rerun.
	testAmendedPlanAcceptanceSave(t, f, history, p)
}

func testAmendedPlanAcceptanceSave(t *testing.T, f *deliveryRunFixture, history []byte, accepted *planVerificationProgress) {
	t.Helper()
	parent := f.parent(t)
	progressBytes, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	path := f.service.reviewFeedbackPath(parent.ID)
	for _, tc := range []struct {
		name    string
		change  func(*reviewFeedbackRecord, *github.DelegatedContent, *planVerificationProgress)
		archive bool
		valid   bool
	}{
		{name: "exact completed amendment", valid: true},
		{name: "missing amendment", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, _ *planVerificationProgress) {
			r.PlanAmendment = nil
		}},
		{name: "unfinished amendment", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, _ *planVerificationProgress) {
			r.PlanAmendment.Completed = false
		}},
		{name: "missing before authority", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, _ *planVerificationProgress) {
			r.PlanAmendment.State.Before = nil
		}},
		{name: "tampered before authority", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, _ *planVerificationProgress) {
			r.PlanAmendment.State.Before[0].Body += "\nUnapproved requirement"
		}},
		{name: "tampered after authority", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, _ *planVerificationProgress) {
			r.PlanAmendment.State.After[0].Body += "\nUnapproved requirement"
		}},
		{name: "unrelated new contract", change: func(_ *reviewFeedbackRecord, c *github.DelegatedContent, p *planVerificationProgress) {
			c.Digest = "v1:unrelated-current-contract"
			p.Assignment.Spec.DelegatedContentDigest = c.Digest
		}},
		{name: "stale assignment digest", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, p *planVerificationProgress) {
			p.Assignment.Spec.DelegatedContentDigest = r.DelegatedContentDigest
		}},
		{name: "stale assignment body", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, p *planVerificationProgress) {
			p.Assignment.Spec.ApprovedBodySnapshot = github.DelegatedContentFor(r.PlanAmendment.State.Before[0]).BodySnapshot
		}},
		{name: "stale assignment revision", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, p *planVerificationProgress) {
			p.Assignment.Spec.PlanContext.Revision = r.PlanAmendment.State.Request.ExpectedRevision
		}},
		{name: "retained prior progress", change: func(r *reviewFeedbackRecord, _ *github.DelegatedContent, p *planVerificationProgress) {
			r.PlanVerification = p
		}},
		{name: "archive interference", archive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var record reviewFeedbackRecord
			var progress planVerificationProgress
			if err := json.Unmarshal(history, &record); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(progressBytes, &progress); err != nil {
				t.Fatal(err)
			}
			// Model the first accepted-QA save, before any new gate or publication.
			progress.Gate, progress.Publication = nil, nil
			progress.EnvelopeDigest = ""
			content := github.DelegatedContentFor(parent)
			if tc.change != nil {
				tc.change(&record, &content, &progress)
			}
			if err := progress.validate(); err != nil {
				t.Fatalf("case must reach contract replacement boundary with valid accepted QA: %v", err)
			}
			if err := f.service.writeReviewFeedback(record); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			archivePath := fmt.Sprintf("%s.amended-%x", path, sha256.Sum256(before))
			// Remove only this fixture's earlier exact archive, so a successful
			// save must itself preserve history before replacing live feedback.
			if err := os.Remove(archivePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			foreign := []byte("Independent operator archive; preserve these bytes.\n")
			if tc.archive {
				if err := os.WriteFile(archivePath, foreign, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			archives, err := filepath.Glob(path + ".amended-*")
			if err != nil {
				t.Fatal(err)
			}
			implementations, reviews, creates, pushes := f.runner.implementations, f.runner.reviews, f.runner.creates, f.runner.planPushes
			project := append([]github.WorkItem(nil), f.runner.project.remoteItems...)
			for attempt := 0; attempt < 2; attempt++ {
				err := f.service.savePlanVerification(parent, content, &progress)
				if (err == nil) != tc.valid {
					t.Fatalf("save attempt %d: valid=%v err=%v", attempt, tc.valid, err)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if !tc.valid && !bytes.Equal(got, before) {
					t.Fatal("refused acceptance overwrote original feedback/amendment bytes")
				}
				if tc.valid {
					fresh, err := f.service.readReviewFeedbackRecord(parent)
					if err != nil || fresh == nil || fresh.DelegatedContentDigest != content.Digest || fresh.PlanAmendment != nil || fresh.PlanRepair != nil || fresh.Baseline != nil || len(fresh.Items) != 0 || !reflect.DeepEqual(fresh.PlanVerification, &progress) {
						t.Fatalf("fresh save reused historical proof or lost accepted QA: %v", err)
					}
					if archived, err := os.ReadFile(archivePath); err != nil || !bytes.Equal(archived, before) {
						t.Fatalf("fresh save did not archive original bytes: %v", err)
					}
				} else {
					if after, err := filepath.Glob(path + ".amended-*"); err != nil || !reflect.DeepEqual(after, archives) {
						t.Fatalf("refused save created historical acceptance: %v", err)
					}
					if tc.archive {
						if archived, err := os.ReadFile(archivePath); err != nil || !bytes.Equal(archived, foreign) {
							t.Fatalf("refused save overwrote interfering archive: %v", err)
						}
					}
				}
				if f.runner.implementations != implementations || f.runner.reviews != reviews || f.runner.creates != creates || f.runner.planPushes != pushes || !reflect.DeepEqual(project, f.runner.project.remoteItems) || progress.Gate != nil || progress.Publication != nil || progress.EnvelopeDigest != "" {
					t.Fatal("save boundary repeated model/gate/publication work or changed Project")
				}
				f.service, err = New(f.cfg, f.runner)
				if err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDeliveryConsecutiveAmendmentsPublishFreshAcceptance(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	original := f.parent(t)
	originalDigest := github.DelegatedContentFor(original).Digest
	var histories [][]byte
	var revision string
	for amendment := 0; amendment < 2; amendment++ {
		preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, amendmentForLastMember(t, f))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err != nil {
			t.Fatal(err)
		}
		revision = preview.Revision
		record, err := f.service.readReviewFeedbackRecord(f.parent(t))
		if err != nil || record == nil || record.PlanAmendment == nil || !record.PlanAmendment.Completed || record.PlanVerification != nil || record.DelegatedContentDigest != originalDigest {
			t.Fatalf("amendment %d replaced original historical feedback: %v", amendment+1, err)
		}
		if amendment == 1 && github.DelegatedContentFor(record.PlanAmendment.State.Before[0]).Digest == originalDigest {
			t.Fatal("fixture must retain R0 feedback beside the completed R1-to-R2 intent")
		}
		history, err := os.ReadFile(f.service.reviewFeedbackPath(f.parentID))
		if err != nil {
			t.Fatal(err)
		}
		histories = append(histories, history)
		f.service, err = New(f.cfg, f.runner)
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.runner.implementations != 2 || f.runner.reviews != 2 || f.runner.creates != 0 {
		t.Fatal("consecutive amendments performed model or publication work")
	}
	for cycle := 0; cycle < 4 && f.parent(t).Status != "PR Ready"; cycle++ {
		results, err := f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("consecutive amendment cycle %d: %s: %s", cycle, result.Summary, result.Error)
			}
		}
	}
	parent := f.parent(t)
	if parent.Status != "PR Ready" || parent.QAFailures != original.QAFailures || parent.PullRequest != "https://github.com/owner/repo/pull/12" || f.runner.implementations != 3 || f.runner.reviews != 4 || f.runner.creates != 1 {
		t.Fatalf("consecutive amendments did not publish once: status=%s failures=%d implementation=%d review=%d PR=%d", parent.Status, parent.QAFailures, f.runner.implementations, f.runner.reviews, f.runner.creates)
	}
	record, err := f.service.readReviewFeedbackRecord(parent)
	if err != nil || record == nil || record.PlanVerification == nil || record.DelegatedContentDigest != github.DelegatedContentFor(parent).Digest || record.DelegatedContentDigest == originalDigest || record.PlanAmendment != nil || record.PlanRepair != nil || record.Baseline != nil || len(record.Items) != 0 {
		t.Fatalf("R2 acceptance copied R0/R1 feedback or amendment evidence: %v", err)
	}
	p := record.PlanVerification
	if p.Assignment.Spec.PlanContext.Revision != revision || p.Assignment.Spec.DelegatedContentDigest != record.DelegatedContentDigest || p.Assignment.Spec.ApprovedBodySnapshot != github.DelegatedContentFor(parent).BodySnapshot || p.Gate == nil || p.Gate.Receipt == nil || p.Gate.Historical || p.Gate.Receipt.Outcome != "passed" || p.Failure != nil || p.Classification != nil || p.Publication == nil || p.Publication.PlanRevision != revision || p.Publication.CommitOID != parent.QACommit {
		t.Fatal("consecutive amendments did not obtain fresh whole-plan proof for R2")
	}
	for _, history := range histories {
		path := fmt.Sprintf("%s.amended-%x", f.service.reviewFeedbackPath(f.parentID), sha256.Sum256(history))
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, history) {
			t.Fatalf("successive amendment lost exact historical feedback/intent bytes: %v", err)
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
