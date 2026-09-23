package engine

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/cortexium-io/runner/internal/workspace"
)

// Deterministically finish another admitted child's review while this child's
// reviewer is running. Both admissions, review workspaces, acceptance records,
// integration intents and Git operations use the production path.
type overlappingPlanReviewRunner struct {
	*deliveryMilestoneRunner
	beforeSecondAccept func() error
	secondReviews      []string
	combinedReviewed   bool
	refreshDirectory   string
	beforeRefreshFetch func()
}

func (r *overlappingPlanReviewRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *overlappingPlanReviewRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && dir == r.refreshDirectory && containsArgument(args, "fetch") && strings.Contains(strings.Join(args, " "), "+refs/heads/runner/plan-") && r.beforeRefreshFetch != nil {
		run := r.beforeRefreshFetch
		r.beforeRefreshFetch = nil
		run()
	}
	if command == "codex" {
		data, err := os.ReadFile(argumentValue(args, "--output-schema"))
		if err != nil {
			return subprocess.Result{}, err
		}
		var schema struct{ Properties map[string]any }
		if err := json.Unmarshal(data, &schema); err != nil {
			return subprocess.Result{}, err
		}
		root := profileReadRoot(args, dir)
		_, second := os.Stat(filepath.Join(root, "member-2.txt"))
		if (schema.Properties["checks"] != nil || schema.Properties["criteria"] != nil) && second == nil {
			head, err := runEngineTestGit(ctx, []string{"rev-parse", "HEAD"}, root, timeout)
			if err != nil {
				return subprocess.Result{}, err
			}
			r.secondReviews = append(r.secondReviews, strings.TrimSpace(head.Stdout))
			first, _ := os.ReadFile(filepath.Join(root, "member-1.txt"))
			r.combinedReviewed = string(first) == "ready\n"
			if run := r.beforeSecondAccept; run != nil {
				r.beforeSecondAccept = nil
				if err := run(); err != nil {
					return subprocess.Result{}, err
				}
			}
		}
	}
	return r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
}

func independentPlanMembers(t *testing.T) (*deliveryRunFixture, *overlappingPlanReviewRunner, []admittedAction) {
	t.Helper()
	repo, remote := createPublicationRepository(t)
	runner := &overlappingPlanReviewRunner{deliveryMilestoneRunner: &deliveryMilestoneRunner{project: &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}}}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, MaxParallelism: 2, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Args: []string{"-c", "exit 0"}, TimeoutSeconds: 30, InputPaths: []string{"member-1.txt", "member-2.txt"}},
	}})
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	plan := sourcedDirectProjectPlanFixture(t, repo)
	plan.WorkItems[1].Dependencies = []string{}
	children, err := service.ApplyProjectPlan(t.Context(), plan)
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
	f := &deliveryRunFixture{service: service, cfg: cfg, runner: runner.deliveryMilestoneRunner, repo: repo, remote: remote, parentID: children[0].PlanningSourceID}
	prepared, err := service.preparePoll(t.Context(), 2, true, nil)
	if err != nil || len(prepared.claimed) != 2 {
		t.Fatalf("independent implementation admission: %v (%d)", err, len(prepared.claimed))
	}
	// Serial fixture execution keeps the fake transport deterministic; both
	// implementations still start on the same authenticated plan head.
	for _, admitted := range prepared.claimed {
		if result := service.executeClaimedAction(t.Context(), admitted); result.Outcome != execution.OutcomeSucceeded {
			t.Fatalf("implementation failed: %s: %s", result.Summary, result.Error)
		}
	}
	// Seed real signed rejection history so a reset-to-zero bug cannot pass
	// the renewal assertions merely because a new fixture starts at zero.
	second, err := service.source.Authorize(t.Context(), github.WorkItem{ID: children[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.source.TransitionRejection(t.Context(), second, "Agent QA", "agent_qa", "Retained prior card rejection history", 1); err != nil {
		t.Fatal(err)
	}
	prepared, err = service.preparePoll(t.Context(), 2, true, nil)
	if err != nil || len(prepared.claimed) != 2 {
		t.Fatalf("independent QA admission: %v (%d)", err, len(prepared.claimed))
	}
	return f, runner, prepared.claimed
}

func replaceRemotePlanHead(t *testing.T, f *deliveryRunFixture) string {
	t.Helper()
	parent := f.parent(t)
	tree := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", parent.QACommit+"^{tree}"))
	foreign := strings.TrimSpace(runGitTest(t, f.repo, "commit-tree", tree, "-p", parent.QACommit, "-m", "Unapproved external plan update"))
	runGitTest(t, f.repo, "push", f.remote, foreign+":refs/heads/"+parent.Branch)
	return foreign
}

func TestStaleAcceptedPlanMemberRefusesRemoteSubstitution(t *testing.T) {
	f, runner, reviews := independentPlanMembers(t)
	foreign := ""
	runner.beforeSecondAccept = func() error {
		result := f.service.executeClaimedAction(t.Context(), reviews[0])
		if result.Outcome != execution.OutcomeSucceeded {
			return fmt.Errorf("first integration: %s", result.Error)
		}
		foreign = replaceRemotePlanHead(t, f)
		return nil
	}
	result := f.service.executeClaimedAction(t.Context(), reviews[1])
	if result.Outcome != execution.OutcomeBlocked || !strings.Contains(result.Error, "unexpected remote identity") {
		t.Fatalf("unapproved remote became a renewal grant: %s: %s", result.Summary, result.Error)
	}
	if f.parent(t).Phase != github.PlanDeliveryPhase || runner.planPushes != 2 || runner.reviews != 2 || runner.creates != 0 {
		t.Fatal("substitution began integration or additional work")
	}
	if got := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "rev-parse", "refs/heads/"+f.parent(t).Branch)); got != foreign {
		t.Fatal("external remote was overwritten")
	}
	var err error
	f.service, err = New(f.cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RunCycle(t.Context()); err == nil || runner.reviews != 2 || runner.implementations != 2 {
		t.Fatal("restart adopted the substituted remote or executed more work")
	}
}

func TestPlanMemberRefreshPinsHeadThroughItsFinalFetch(t *testing.T) {
	f, runner, reviews := independentPlanMembers(t)
	runner.beforeSecondAccept = func() error {
		result := f.service.executeClaimedAction(t.Context(), reviews[0])
		if result.Outcome != execution.OutcomeSucceeded {
			return fmt.Errorf("first integration: %s", result.Error)
		}
		return nil
	}
	result := f.service.executeClaimedAction(t.Context(), reviews[1])
	if result.Outcome != "warning" {
		t.Fatalf("fixture did not reach stale acceptance: %s", result.Error)
	}
	item, err := f.service.source.InspectRecoveryItem(t.Context(), reviews[1].action.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := f.service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, f.repo, false)
	request.BaseRef = "origin/" + f.parent(t).Branch
	provider := workspace.NewGitProvider(f.service.run)
	before, err := provider.InspectRetainedReview(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runGitTest(t, before.WorktreePath, "rev-parse", "HEAD"))
	runner.refreshDirectory = before.WorktreePath
	foreign := ""
	runner.beforeRefreshFetch = func() { foreign = replaceRemotePlanHead(t, f) }
	results, err := f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || !strings.Contains(results[0].Error, "plan head changed during member refresh") || foreign == "" {
		t.Fatalf("final fetch adopted an unapproved head: %v %#v", err, results)
	}
	after, err := provider.InspectRetainedReview(t.Context(), request)
	if err != nil || after.Identity != before.Identity || strings.TrimSpace(runGitTest(t, before.WorktreePath, "rev-parse", "HEAD")) != head {
		t.Fatalf("refused refresh mutated candidate or its identity: %v", err)
	}
	if runner.implementations != 2 || runner.reviews != 2 || runner.planPushes != 2 || runner.creates != 0 {
		t.Fatal("refused refresh performed model/integration/publication work")
	}
}

func TestStaleAcceptedPlanMemberRenewsCombinedQAAfterRestart(t *testing.T) {
	f, runner, reviews := independentPlanMembers(t)
	first, second := reviews[0], reviews[1]
	if first.action.Item.ID != "PVTI_created_2" || second.action.Item.ID != "PVTI_created_3" {
		t.Fatal("unexpected fixture member ordering")
	}
	runner.beforeSecondAccept = func() error {
		result := f.service.executeClaimedAction(t.Context(), first)
		if result.Outcome != execution.OutcomeSucceeded {
			return fmt.Errorf("first integration failed: %s: %s", result.Summary, result.Error)
		}
		return nil
	}
	result := f.service.executeClaimedAction(t.Context(), second)
	if result.Outcome != "warning" || !strings.Contains(result.Summary, "fresh QA") || runner.combinedReviewed {
		t.Fatalf("obsolete acceptance did not request review renewal: %s: %s (%s)", result.Outcome, result.Summary, result.Error)
	}
	parent := f.parent(t)
	item, err := f.service.source.InspectRecoveryItem(t.Context(), second.action.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if item.Status != "Agent QA" || item.Phase == github.PlanIntegratedPhase || item.QAFailures != second.action.Item.QAFailures || parent.Phase != github.PlanDeliveryPhase {
		t.Fatal("stale acceptance was integrated, rejected or lost its QA eligibility")
	}
	request := f.service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, f.repo, false)
	request.BaseRef = "origin/" + parent.Branch
	provider := workspace.NewGitProvider(f.service.run)
	metadata, err := provider.InspectRetainedReview(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	old, found, err := provider.LoadPublicationAcceptance(t.Context(), metadata, snapshot, workspace.PublicationEvidence{PlanRevision: github.PlanRevision(parent.Body)})
	if err != nil || !found || old.ApprovedBaseOID == parent.QACommit {
		t.Fatalf("old acceptance was not retained at its original head: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(metadata.WorktreePath), ".runner-state", "publications", "v3", old.CommitOID+"*.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("original immutable proof missing: %v %v", paths, err)
	}
	proof, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	// No review is repeated merely to recover the pending renewal. First
	// refresh locally, restart again, then review the actual combined candidate.
	for step := 0; step < 2; step++ {
		f.service, err = New(f.cfg, runner)
		if err != nil {
			t.Fatal(err)
		}
		results, err := f.service.RunCycle(t.Context())
		if err != nil || len(results) != 1 {
			t.Fatalf("renewal step %d: %v %#v", step, err, results)
		}
		want := "warning"
		if step == 1 {
			want = execution.OutcomeSucceeded
		}
		if results[0].Outcome != want {
			t.Fatalf("renewal step %d: %s: %s", step, results[0].Summary, results[0].Error)
		}
	}
	item, err = f.service.source.InspectRecoveryItem(t.Context(), item.ID)
	if err != nil || item.Phase != github.PlanIntegratedPhase || item.QAFailures != second.action.Item.QAFailures || !runner.combinedReviewed || len(runner.secondReviews) != 2 || runner.secondReviews[0] == runner.secondReviews[1] {
		t.Fatalf("combined candidate lacked fresh exact review: %v %#v", err, runner.secondReviews)
	}
	if after, err := os.ReadFile(paths[0]); err != nil || string(after) != string(proof) {
		t.Fatal("renewal rewrote historical acceptance")
	}
	current, err := provider.InspectRetainedReview(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	currentSnapshot, err := f.service.checkoutSnapshotState(t.Context(), current.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := f.service.loadVerificationEvidence(item, github.DelegatedContentFor(item), current, workspace.Candidate{CommitOID: currentSnapshot.Head, TreeOID: currentSnapshot.Tree}, approvedVerificationContract(item.Body))
	if err != nil || len(entries) != 1 || entries[0].SourceCommitOID != old.CommitOID {
		t.Fatalf("old verification was lost or relabelled fresh: %v %#v", err, entries)
	}
	if runner.implementations != 2 || runner.reviews != 3 || runner.planPushes != 3 || runner.creates != 0 {
		t.Fatalf("renewal repeated implementation/publication: implementations=%d reviews=%d pushes=%d PRs=%d", runner.implementations, runner.reviews, runner.planPushes, runner.creates)
	}
}
