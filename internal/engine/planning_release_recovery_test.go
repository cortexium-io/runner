package engine

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// Inject failures before or after the server applies one source write. The
// latter models an ambiguous transport failure without arbitrary timing.
type planningReleaseRunner struct {
	project    *fakeGitHubProjectRunner
	sourceID   string
	failField  string
	afterWrite bool
	observe    func()
}

func (r *planningReleaseRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	write := command == "gh" && len(args) >= 2 && args[0] == "project" && args[1] == "item-edit"
	fail := write && argumentValue(args, "--id") == r.sourceID && argumentValue(args, "--field-id") == r.failField && r.failField != ""
	if fail && !r.afterWrite {
		r.failField = ""
		return subprocess.Result{Stderr: "injected source completion failure", ExitCode: 1}, errors.New("injected source completion failure")
	}
	result, err := r.project.Run(ctx, command, args, dir, timeout)
	if write && err == nil && r.observe != nil {
		r.observe()
	}
	if fail && err == nil {
		r.failField = ""
		return subprocess.Result{Stderr: "injected lost completion response", ExitCode: 1}, errors.New("injected lost completion response")
	}
	return result, err
}

func usePlanningReleaseRunner(t *testing.T, service *Engine, run *planningReleaseRunner) {
	t.Helper()
	source, err := github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, run)
	if err != nil {
		t.Fatal(err)
	}
	service.source = source
	service.run = run
}

func TestPlanningReleaseRecoversEveryCompletionWrite(t *testing.T) {
	for _, field := range []string{"F_status", "F_approval", "F_phase", "F_activity", "F_result"} {
		for _, after := range []bool{false, true} {
			name := field + "/before"
			if after {
				name = field + "/after"
			}
			t.Run(name, func(t *testing.T) {
				service, project, source, _ := stagedPlannerBatchFixture(t, 2)
				// Include activity cleanup in the completion transition.
				for i := range project.remoteItems {
					if project.remoteItems[i].ID == source.ID {
						project.remoteItems[i].Activity = "Awaiting approval"
					}
				}
				run := &planningReleaseRunner{project: project, sourceID: source.ID, failField: field, afterWrite: after}
				usePlanningReleaseRunner(t, service, run)
				preview, err := service.PlanProjectItemApproval(t.Context(), source.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.ApplyProjectItemApproval(t.Context(), preview); err == nil {
					t.Fatal("completion failure reported success")
				}
				retry, err := service.PlanProjectItemApproval(t.Context(), source.ID)
				if err != nil {
					t.Fatalf("retained batch cannot resume: %v", err)
				}
				committed := field != "F_status" && (field != "F_approval" || after)
				if retry.Batch.Released != committed {
					t.Fatalf("released=%t, want %t", retry.Batch.Released, committed)
				}
				ready, err := service.source.Poll(t.Context(), 10)
				if err != nil {
					t.Fatal(err)
				}
				if !committed && len(ready) != 0 {
					t.Fatal("uncommitted batch became executable")
				}
				if committed && len(ready) != 2 {
					t.Fatalf("committed batch lost executable authority: %v", ready)
				}
				if !committed && strings.Contains(retry.Item.Result, "Approved and released") {
					t.Fatal("success published before release commit")
				}
				if !committed {
					if _, err := service.source.RecoverInterrupted(t.Context()); err != nil {
						t.Fatalf("worker could not restore staging: %v", err)
					}
				}
				// Refresh after observation so the approval preview is still exact.
				retry, err = service.PlanProjectItemApproval(t.Context(), source.ID)
				if err != nil {
					t.Fatal(err)
				}
				approved, err := service.ApplyProjectItemApproval(t.Context(), retry)
				if err != nil {
					t.Fatalf("resume retained batch: %v", err)
				}
				if approved.Status != "Done" || approved.Phase != "" || approved.Activity != "" || !strings.Contains(approved.Result, "Approved and released") {
					t.Fatalf("incomplete source: %+v", approved)
				}
				ready, err = service.source.Poll(t.Context(), 10)
				if err != nil || len(ready) != 2 {
					t.Fatalf("batch did not release: ready=%v err=%v", ready, err)
				}
				for i, child := range preview.Batch.Children {
					if !reflect.DeepEqual(child.Item.Body, ready[i].Item.Body) || child.Item.ID != ready[i].Item.ID {
						t.Fatal("recovery changed exact child content or identity")
					}
				}
			})
		}
	}
}

func TestPlanningReleaseRecoversLegacyMissingPhase(t *testing.T) {
	for _, status := range []string{"Needs assessment", "Done"} {
		t.Run(status, func(t *testing.T) {
			service, project, source, _ := stagedPlannerBatchFixture(t, 2)
			for i := range project.remoteItems {
				if project.remoteItems[i].ID == source.ID {
					project.remoteItems[i].Status = status
					project.remoteItems[i].Phase = ""
					project.remoteItems[i].Result = "Approved and released the complete normalized planning batch of 2 work items."
				}
			}
			plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Batch.Released {
				t.Fatal("success text substituted for authenticated release")
			}
			if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err != nil {
				t.Fatal(err)
			}
			ready, err := service.source.Poll(t.Context(), 10)
			if err != nil || len(ready) != 2 {
				t.Fatalf("legacy batch remained blocked: %v %v", ready, err)
			}
		})
	}
}

func TestPlanningReleaseRecoveryRejectsChangedRetainedBatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeGitHubProjectRunner)
	}{
		{name: "source", change: func(p *fakeGitHubProjectRunner) { p.remoteItems[0].Body += "changed" }},
		{name: "body", change: func(p *fakeGitHubProjectRunner) { p.remoteItems[1].Body += "changed" }},
		{name: "missing", change: func(p *fakeGitHubProjectRunner) { p.remoteItems = p.remoteItems[:2] }},
		{name: "approval", change: func(p *fakeGitHubProjectRunner) { p.remoteItems[1].Approval = "forged" }},
		{name: "activity", change: func(p *fakeGitHubProjectRunner) { p.remoteItems[1].Activity = "Implementing" }},
		{name: "runtime", change: func(p *fakeGitHubProjectRunner) { p.remoteItems[1].Branch = "runner/already-started" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, project, source, _ := stagedPlannerBatchFixture(t, 2)
			project.remoteItems[0].Phase = ""
			test.change(project)
			project.calls = nil
			if _, err := service.PlanProjectItemApproval(t.Context(), source.ID); err == nil {
				t.Fatal("changed batch accepted")
			}
			for _, call := range project.calls {
				if strings.Contains(call, "project item-edit") {
					t.Fatal("recovery preview wrote Project state")
				}
			}
		})
	}
}

func TestPlanningReleaseRecoveryResumesPartialCleanup(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	project.failStatusWrites = map[int]bool{4: true, 5: true}
	project.failClearApprovalAt = 1
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil {
		t.Fatal("expected partial release failure")
	}
	plan, err = service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatalf("partial cleanup cannot resume: %v", err)
	}
	project.failStatusWrites = nil
	project.failClearApprovalAt = 0
	if _, err = service.ApplyProjectItemApproval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestPlanningReleaseExcludesWorkerWritesThroughoutRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	service.EnableLocalAdmission()
	run := &planningReleaseRunner{project: project, sourceID: source.ID}
	usePlanningReleaseRunner(t, service, run)
	worker := &Engine{cfg: service.cfg, source: newTestGitHubProjectSource(service.cfg.GitHubProject, project), run: project}
	worker.EnableLocalAdmission()
	observations := 0
	run.observe = func() {
		before := append([]github.WorkItem(nil), project.remoteItems...)
		calls := len(project.calls)
		// Exercise both the recovery poll and ordinary subsequent coordinator poll.
		for _, recover := range []bool{true, false} {
			prepared, err := worker.preparePoll(t.Context(), 10, recover, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(prepared.items) != 3 || len(prepared.claimed) != 0 {
				t.Fatalf("worker did not remain observation-only: %+v", prepared)
			}
		}
		if !reflect.DeepEqual(before, project.remoteItems) {
			t.Fatal("worker modified live release")
		}
		for _, call := range project.calls[calls:] {
			if strings.Contains(call, "project item-edit") {
				t.Fatal("worker wrote during live release")
			}
		}
		observations++
	}
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyProjectItemApproval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if observations < 8 {
		t.Fatalf("insufficient release interleavings: %d", observations)
	}
}

func TestPlanningReleaseCompletionPreservesStartedChildren(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	run := &planningReleaseRunner{project: project, sourceID: source.ID, failField: "F_result"}
	usePlanningReleaseRunner(t, service, run)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyProjectItemApproval(t.Context(), plan); err == nil {
		t.Fatal("expected completion failure")
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("authenticated release was lost: %v %v", ready, err)
	}
	if _, err = service.source.Claim(t.Context(), ready[0], "ready"); err != nil {
		t.Fatal(err)
	}
	before := append([]github.WorkItem(nil), project.remoteItems...)
	plan, err = service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil || !plan.Batch.Released {
		t.Fatalf("started release cannot finish: %v", err)
	}
	project.calls = nil
	if _, err = service.ApplyProjectItemApproval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	for i, item := range before {
		if item.ID != source.ID && !reflect.DeepEqual(item, project.remoteItems[i]) {
			t.Fatal("source completion rewrote started child")
		}
	}
	for _, call := range project.calls {
		if strings.Contains(call, "project item-edit") && !strings.Contains(call, "--id "+source.ID) {
			t.Fatalf("completion wrote child state: %s", call)
		}
	}
}

func TestPlanningReleasePartialSnapshotRemainsRecoverableWithoutRecoveryPoll(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range project.remoteItems {
		item := &project.remoteItems[i]
		if item.PlanningItemIndex == 1 {
			item.Status = "Ready" // A failed rollback cleared approval but not status.
		}
		if item.PlanningItemIndex == 2 {
			item.Status = "Ready"
			item.Approval = plan.Batch.Children[1].Assertion
		}
	}
	before := append([]github.WorkItem(nil), project.remoteItems...)
	prepared, err := service.preparePoll(t.Context(), 10, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.claimed) != 0 || !reflect.DeepEqual(before, project.remoteItems) {
		t.Fatal("non-recovery poll mutated or claimed partial release")
	}
	if _, err := service.source.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PlanProjectItemApproval(t.Context(), source.ID); err != nil {
		t.Fatalf("partial snapshot lost recoverability: %v", err)
	}
}

func TestPlanningReleaseRecoversRetainedDependencyActivity(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	project.remoteItems[0].Phase = ""
	project.remoteItems[1].Activity = config.RunnerActivityWaitingForDependencies
	before := project.remoteItems[1]
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	child := project.remoteItems[1]
	if child.Activity != "" || child.Status != "Ready" || child.Body != before.Body || child.ID != before.ID {
		t.Fatal("stale dependency activity did not recover without changing reviewed work")
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("retained batch was not executable: %v %v", ready, err)
	}
}

func TestPlanningReleaseWorkerRestoresRetainedDependencyActivity(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	project.remoteItems[0].Phase = ""
	project.remoteItems[1].Activity = config.RunnerActivityWaitingForDependencies
	before := project.remoteItems[1]
	if _, err := service.source.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	child := project.remoteItems[1]
	if child.Activity != "" || child.Status != "Needs assessment" || child.Approval != "" || child.Body != before.Body {
		t.Fatal("worker recovery did not restore exact unapproved staging")
	}
	if _, err := service.PlanProjectItemApproval(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
}
