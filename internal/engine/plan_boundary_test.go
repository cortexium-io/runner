package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// Once the irreversible boundary succeeds, all subsequent transport calls
// fail until restart. In particular, failure handling cannot write a new
// Project state that a real process loss would never have written.
type publicationCrashRunner struct {
	*deliveryMilestoneRunner
	afterCreate, crashed, offline, closed bool
}

func (r *publicationCrashRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *publicationCrashRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if r.offline {
		return subprocess.Result{}, context.Canceled
	}
	result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
	if r.closed && command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view" && err == nil {
		var details map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &details); err != nil {
			return result, err
		}
		details["state"] = "CLOSED"
		encoded, _ := json.Marshal(details)
		result.Stdout = string(encoded)
	}
	lostPush := !r.afterCreate && command == "git" && containsArgument(args, "push") && r.planPushes == 4
	lostCreate := r.afterCreate && command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "create"
	if !r.crashed && err == nil && (lostPush || lostCreate) {
		r.crashed, r.offline = true, true
		return result, context.Canceled
	}
	return result, err
}

func TestPlanRefreshedPublicationRecoversBeforeIntegratedHeadCheck(t *testing.T) {
	for _, boundary := range []string{"push_before_create", "create_before_project", "merged_deleted_branch", "closed_deleted_branch", "unrelated_remote"} {
		t.Run(boundary, func(t *testing.T) {
			f := newProductionDelivery(t)
			f.integrateMembers(t)
			parent := f.parent(t)
			metadata, err := f.service.workspaceForItem(t.Context(), parent, github.DelegatedContentFor(parent).Digest, f.repo)
			if err != nil {
				t.Fatal(err)
			}
			advanceRemoteBase(t, f.repo, "base-addition.txt", "new destination behavior\n")
			results, err := f.service.RunCycle(t.Context())
			if err != nil || len(results) != 1 || results[0].Outcome != "warning" {
				t.Fatalf("destination refresh: %v %#v", err, results)
			}
			candidate := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
			if candidate == parent.QACommit {
				t.Fatal("fixture requires refreshed R different from integrated P")
			}
			transport := &publicationCrashRunner{deliveryMilestoneRunner: f.runner, afterCreate: boundary != "push_before_create"}
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.service.RunCycle(t.Context())
			if !transport.crashed || f.runner.planPushes != 4 || f.runner.reviews != 3 {
				t.Fatalf("did not reach exact final publication crash: crashed=%v pushes=%d reviews=%d", transport.crashed, f.runner.planPushes, f.runner.reviews)
			}
			transport.offline = false
			interrupted := f.parent(t)
			if interrupted.QACommit != parent.QACommit || interrupted.PullRequest != "" {
				t.Fatal("crash fixture accidentally recorded the publication transition")
			}
			if head := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", "refs/heads/"+parent.Branch)); head != candidate {
				t.Fatal("final push did not install exact accepted R")
			}
			foreign := ""
			switch boundary {
			case "merged_deleted_branch":
				f.runner.merged = true
				runGitTest(t, "", "--git-dir", f.remote, "update-ref", "refs/heads/main", candidate)
				runGitTest(t, "", "--git-dir", f.remote, "update-ref", "-d", "refs/heads/"+parent.Branch)
			case "closed_deleted_branch":
				transport.closed = true
				runGitTest(t, "", "--git-dir", f.remote, "update-ref", "-d", "refs/heads/"+parent.Branch)
			case "unrelated_remote":
				updater := filepath.Join(t.TempDir(), "external")
				runGitTest(t, "", "clone", "--branch", parent.Branch, f.remote, updater)
				runGitTest(t, updater, "config", "user.name", "External fixture")
				runGitTest(t, updater, "config", "user.email", "external@example.com")
				if err := os.WriteFile(filepath.Join(updater, "unapproved.txt"), []byte("foreign change\n"), 0600); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, updater, "add", "unapproved.txt")
				runGitTest(t, updater, "commit", "-m", "foreign update after Runner publication")
				foreign = strings.TrimSpace(runGitTest(t, updater, "rev-parse", "HEAD"))
				runGitTest(t, updater, "push", "origin", parent.Branch)
			}
			// A new production coordinator must use durable authority/proof,
			// not any in-memory success left by the interrupted instance.
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			results, err = f.service.RunCycle(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			current := f.parent(t)
			if boundary == "unrelated_remote" || boundary == "closed_deleted_branch" {
				if current.Status == "Done" || current.Status == "PR Ready" || current.QACommit != parent.QACommit || current.PullRequest != "" {
					t.Fatalf("unrelated remote became publication authority: %s", current.Status)
				}
				if boundary == "unrelated_remote" && strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", "refs/heads/"+parent.Branch)) != foreign {
					t.Fatal("foreign remote was overwritten")
				}
			} else {
				wantStatus := "PR Ready"
				if boundary == "merged_deleted_branch" {
					wantStatus = "Done"
				}
				if current.Status != wantStatus || current.QACommit != candidate || current.PullRequest != "https://github.com/owner/repo/pull/12" {
					t.Fatalf("exact publication not recovered: status=%s candidate=%s", current.Status, current.QACommit)
				}
				if len(results) != 1 || !results[0].ResumedCheckpoint {
					t.Fatal("recovery did not reuse the protected acceptance")
				}
			}
			if boundary == "merged_deleted_branch" || boundary == "closed_deleted_branch" {
				if _, err := f.service.RunCycle(t.Context()); err != nil {
					t.Fatal(err)
				}
				want := "Done"
				if transport.closed {
					want = "Blocked"
				}
				if got := f.parent(t).Status; got != want {
					t.Fatalf("terminal state = %s, want %s", got, want)
				}
				if refs := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/"+parent.Branch)); refs != "" {
					t.Fatal("recovery recreated a terminal branch")
				}
			}
			if f.runner.planPushes != 4 || f.runner.reviews != 3 || f.runner.implementations != 2 || f.runner.creates != 1 {
				t.Fatalf("duplicate work: pushes=%d reviews=%d implementations=%d PRs=%d", f.runner.planPushes, f.runner.reviews, f.runner.implementations, f.runner.creates)
			}
		})
	}
}

type deliveryRunFixture struct {
	service                *Engine
	cfg                    config.Config
	runner                 *deliveryMilestoneRunner
	repo, remote, parentID string
}

func newProductionDelivery(t *testing.T) *deliveryRunFixture {
	t.Helper()
	repo, remote := createPublicationRepository(t)
	runner := &deliveryMilestoneRunner{project: &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Args: []string{"-c", "test \"$(cat member-1.txt)\" = ready && test \"$(cat member-2.txt)\" = ready"}, TimeoutSeconds: 30, InputPaths: []string{"member-1.txt", "member-2.txt"}},
	}})
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	children, err := service.ApplyProjectPlan(t.Context(), sourcedDirectProjectPlanFixture(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	return &deliveryRunFixture{service: service, cfg: cfg, runner: runner, repo: repo, remote: remote, parentID: children[0].PlanningSourceID}
}

func (f *deliveryRunFixture) parent(t *testing.T) github.WorkItem {
	t.Helper()
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == f.parentID {
			return item
		}
	}
	t.Fatal("delivery parent missing")
	return github.WorkItem{}
}

func (f *deliveryRunFixture) integrateMembers(t *testing.T) {
	t.Helper()
	for cycle := 0; cycle < 4; cycle++ {
		results, err := f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("card cycle %d: %s: %s", cycle, result.Summary, result.Error)
			}
		}
	}
	parent := f.parent(t)
	if parent.Phase != github.PlanDeliveryPhase || f.runner.implementations != 2 || f.runner.reviews != 2 {
		t.Fatal("fixture did not reach integrated pre-QA plan")
	}
	// This is the production deterministic transition used by reconcilePlan;
	// stop here so external changes can be injected before the next admission.
	action, err := f.service.source.Authorize(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.source.Transition(t.Context(), action, "Agent QA", "Review combined outcome", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPlanQARefusesUnexpectedRemoteFastForward(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	parent := f.parent(t)
	updater := filepath.Join(t.TempDir(), "external")
	runGitTest(t, "", "clone", "--branch", parent.Branch, f.remote, updater)
	runGitTest(t, updater, "config", "user.name", "External fixture")
	runGitTest(t, updater, "config", "user.email", "external@example.com")
	if err := os.WriteFile(filepath.Join(updater, "unapproved.txt"), []byte("unapproved remote change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, updater, "add", "unapproved.txt")
	runGitTest(t, updater, "commit", "-m", "unexpected remote plan advance")
	foreign := strings.TrimSpace(runGitTest(t, updater, "rev-parse", "HEAD"))
	runGitTest(t, updater, "push", "origin", parent.Branch)
	_, _ = f.service.RunCycle(t.Context())
	if f.runner.reviews != 2 || f.runner.implementations != 2 || f.runner.creates != 0 || f.runner.planPushes != 3 {
		t.Fatal("unapproved plan head reached review/publication")
	}
	if got := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", "refs/heads/"+parent.Branch)); got != foreign {
		t.Fatal("unexpected remote work was overwritten")
	}
	if current := f.parent(t); current.QACommit != parent.QACommit || current.Status == "Done" || current.Status == "PR Ready" {
		t.Fatal("unexpected head became authenticated delivery state")
	}
}

func TestPlanRefreshedRejectionRepairsFromIntegratedHeadAfterRestart(t *testing.T) {
	f := newProductionDelivery(t)
	f.runner.rejectCombined = true
	f.integrateMembers(t)
	parent := f.parent(t)
	metadata, err := f.service.workspaceForItem(t.Context(), parent, github.DelegatedContentFor(parent).Digest, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, f.repo, "base-addition.txt", "new destination behavior\n")
	results, err := f.service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != "warning" {
		t.Fatalf("expected local refresh before QA: %#v", results)
	}
	refreshed := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	if refreshed == parent.QACommit {
		t.Fatal("fixture failed to produce a local-only refreshed candidate")
	}
	results, err = f.service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != config.WorkflowOutcomeRejected {
		t.Fatalf("expected bounded whole-plan rejection: %#v", results)
	}
	current := f.parent(t)
	if current.QACommit != parent.QACommit || current.QAFailures != 1 {
		t.Fatal("rejected local candidate replaced integrated remote head or lost budget")
	}
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 6 && f.parent(t).Status != "PR Ready"; cycle++ {
		results, err = f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("repair cycle %d: %s: %s", cycle, result.Summary, result.Error)
			}
		}
	}
	current = f.parent(t)
	if current.Status != "PR Ready" || current.QAFailures != 1 || f.runner.creates != 1 || f.runner.implementations != 3 || f.runner.reviews != 5 {
		t.Fatalf("repair did not converge exactly once: parent=%s failures=%d impl=%d QA=%d PR=%d", current.Status, current.QAFailures, f.runner.implementations, f.runner.reviews, f.runner.creates)
	}
	for path, want := range map[string]string{"base-addition.txt": "new destination behavior", "member-1.txt": "ready", "member-2.txt": "ready"} {
		if got := strings.TrimSpace(runGitTest(t, f.repo, "show", current.QACommit+":"+path)); got != want {
			t.Fatalf("lost accepted/base work %s: %s", path, got)
		}
	}
}

func TestPlanDestinationConflictBlocksInsteadOfEnteringImplementation(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	parent := f.parent(t)
	if _, err := f.service.workspaceForItem(t.Context(), parent, github.DelegatedContentFor(parent).Digest, f.repo); err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, f.repo, "member-1.txt", "conflicting destination behavior\n")
	if _, err := f.service.RunCycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	current := f.parent(t)
	if current.Status != "Blocked" || current.Phase != "agent_qa" || !strings.Contains(current.Result, "owning-card") {
		t.Fatalf("unowned conflict was not actionable blocked state: %s/%s %s", current.Status, current.Phase, current.Result)
	}
	if current.QACommit != parent.QACommit || current.QAFailures != parent.QAFailures || f.runner.implementations != 2 || f.runner.reviews != 2 {
		t.Fatal("conflict changed authority/budget or launched extra model work")
	}
}

type deliveryFailedCIRunner struct{ *deliveryMilestoneRunner }

func (r deliveryFailedCIRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view" {
		var payload map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return result, err
		}
		payload["mergeStateStatus"] = "BLOCKED"
		payload["statusCheckRollup"] = []map[string]any{{"__typename": "CheckRun", "name": "protected complete validation", "status": "COMPLETED", "conclusion": "FAILURE"}}
		encoded, err := json.Marshal(payload)
		result.Stdout = string(encoded)
		return result, err
	}
	return result, err
}

func TestPlanFinalPRCIFailureRequiresOwningCardRecovery(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	if results, err := f.service.RunCycle(t.Context()); err != nil || len(results) != 1 || results[0].Error != "" {
		t.Fatalf("publish final fixture PR: %v %#v", err, results)
	}
	parent := f.parent(t)
	if parent.Status != "PR Ready" {
		t.Fatal("fixture did not publish plan")
	}
	f.cfg.GitHubProject.AutoMerge = true
	var err error
	f.service, err = New(f.cfg, deliveryFailedCIRunner{f.runner})
	if err != nil {
		t.Fatal(err)
	}
	results, err := f.service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current := f.parent(t)
	if current.Status != "Blocked" || current.Phase != "agent_qa" || !strings.Contains(current.Result, "owning-card") {
		t.Fatalf("failed final checks stranded plan: %s/%s %s", current.Status, current.Phase, current.Result)
	}
	if current.QACommit != parent.QACommit || current.PullRequest != parent.PullRequest || current.QAFailures != parent.QAFailures {
		t.Fatal("CI blocker lost retained publication or reset allowance")
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || f.runner.implementations != 2 || f.runner.reviews != 3 || f.runner.creates != 1 {
		t.Fatal("unowned CI failure admitted work or reported implementation repair")
	}
}
