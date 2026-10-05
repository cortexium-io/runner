package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// Inject a failed issue-label operation while keeping the normal production
// Project reads, transitions and Git workspace checks.
type unstartedRecoveryRunner struct {
	*deliveryMilestoneRunner
	failLabelRemoval bool
}

func (r *unstartedRecoveryRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	label := argumentValue(args, "--remove-label")
	removing := command == "gh" && len(args) > 2 && args[0] == "issue" && args[1] == "edit" && label != ""
	if removing && r.failLabelRemoval {
		return subprocess.Result{}, errors.New("simulated label removal failure")
	}
	return r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
}

func unstartedRecoveryFixture(t *testing.T, integrated bool) (*deliveryRunFixture, *unstartedRecoveryRunner, github.WorkItem) {
	t.Helper()
	f := newProductionDelivery(t)
	if integrated {
		f.integrateMembers(t)
	}
	request, added := membershipAdoption(t, f)
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == added.ID {
			item := &f.runner.project.remoteItems[i]
			item.Status, item.Approval = f.service.cfg.GitHubProject.AssessmentStatus, ""
		}
	}
	f.runner.project.issueLabels = []string{f.service.cfg.GitHubProject.IntakeLabel, "keep-label"}
	transport := &unstartedRecoveryRunner{deliveryMilestoneRunner: f.runner}
	f.service, err = New(f.cfg, transport)
	if err != nil {
		t.Fatal(err)
	}
	return f, transport, added
}

func TestUnstartedReauthorizationPreservesAcceptedPlanAndChecksAbsentWork(t *testing.T) {
	f, _, added := unstartedRecoveryFixture(t, true)
	before, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	parentProof := f.service.reviewFeedbackPath(f.parentID)
	proof, err := os.ReadFile(parentProof)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := f.service.PlanProjectItemReauthorization(t.Context(), added.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Approval.Unstarted || !plan.Approval.RemoveIntakeLabel || plan.Workspace.WorktreePath != "" || plan.Approval.PlanRevision != github.PlanRevision(f.parent(t).Body) {
		t.Fatal("preview lost exact signed plan or absent-work boundary")
	}
	f.runner.project.issueLabels = []string{"keep-label"}
	if _, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
		t.Fatal("apply ignored reassessment intent that changed after preview")
	}
	f.runner.project.issueLabels = []string{f.service.cfg.GitHubProject.IntakeLabel, "keep-label"}
	for _, path := range []string{f.service.implementationCheckpointPath(added.ID), f.service.verificationEvidencePath(added.ID), f.service.reviewFeedbackPath(added.ID)} {
		if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("retained execution evidence"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
			t.Fatal("apply ignored evidence that appeared after preview")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	request := f.service.workspaceRequestForItem(plan.Approval.Item, github.DelegatedContentFor(plan.Approval.Item).Digest, f.repo, false)
	root := request.WorktreeRoot
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(f.repo), root)
	}
	path := filepath.Join(root, request.WorkID)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
		t.Fatal("apply adopted a workspace that appeared after preview")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == added.ID {
			f.runner.project.remoteItems[i].Result += "\nIntervening operator note"
		}
	}
	if _, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
		t.Fatal("changed preview was accepted")
	}
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == added.ID {
			f.runner.project.remoteItems[i].Result = plan.Approval.Item.Result
		}
	}
	item, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "Ready" || item.Branch != "" || item.QAFailures != 0 || item.QACommit != "" || !reflect.DeepEqual(f.runner.project.issueLabels, []string{"keep-label"}) {
		t.Fatal("recovery did not consume only intake intent or fabricated evidence")
	}
	after, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range before {
		if before[i].ID != added.ID && !reflect.DeepEqual(before[i], after[i]) {
			t.Fatal("recovery changed the parent or another member")
		}
	}
	if got, err := os.ReadFile(parentProof); err != nil || string(got) != string(proof) {
		t.Fatal("recovery rewrote protected amendment or carried acceptance")
	}
	ready, err := f.service.source.ReadyItems(t.Context(), after, len(after))
	if err != nil || len(ready) != 1 || ready[0].Item.ID != added.ID {
		t.Fatalf("selected member not admitted: %v %#v", err, ready)
	}
	if f.runner.implementations != 2 || f.runner.reviews != 2 || f.runner.creates != 0 {
		t.Fatal("reauthorization executed a model or republished accepted work")
	}
}

func TestUnstartedReauthorizationLabelFailureRetainsTransitionLock(t *testing.T) {
	f, transport, added := unstartedRecoveryFixture(t, false)
	plan, err := f.service.PlanProjectItemReauthorization(t.Context(), added.ID)
	if err != nil {
		t.Fatal(err)
	}
	transport.failLabelRemoval = true
	if _, err := f.service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
		t.Fatal("label failure silently issued fresh authority")
	}
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == added.ID && (item.Transition == "" || item.Approval != "" || item.Status != f.service.cfg.GitHubProject.AssessmentStatus || len(f.runner.project.issueLabels) != 2) {
			t.Fatal("failed label removal lost the fence or changed card authority")
		}
	}
	if f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.creates != 0 {
		t.Fatal("failed recovery executed work")
	}
}
