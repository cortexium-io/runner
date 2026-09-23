package engine

import (
	"bytes"
	"context"
	"errors"
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

func TestPlannerCheckpointRefusesChangedDestinationWithoutRepeatingHarness(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_source_resume", Title: "Plan a slice", Body: "Split this request safely.", Repository: "owner/repo", Status: "Plan"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, failCreateAt: 1}
	calls := 0
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), plannerStagesBatchRunner{project: project, plannerCalls: &calls})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.RunCycle(t.Context())
	if err != nil || len(first) != 1 || calls != 2 || first[0].Outcome != execution.OutcomeBlocked {
		t.Fatalf("missing interrupted staged plan: calls=%d results=%+v err=%v", calls, first, err)
	}
	path := service.plannerCheckpointPath(item.ID)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, repo, "new-policy.md", "changed destination requirements\n")
	retry, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemRetry(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	project.failCreateAt = 0
	second, err := service.RunCycle(t.Context())
	if err != nil || len(second) != 1 || second[0].Outcome != execution.OutcomeBlocked || !strings.Contains(second[0].Error, "destination changed since planning") || calls != 2 || project.createCount != 1 {
		t.Fatalf("stale checkpoint repeated paid work or staging: calls=%d creates=%d results=%+v err=%v", calls, project.createCount, second, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("stale source checkpoint discarded or relabelled: %v", err)
	}
}

type sourceInspectingPlanner struct {
	canonicalizingPlannerRunner
	root      string
	inspect   func(string) error
	removeErr error
	fetchErr  error
}

func (r *sourceInspectingPlanner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && strings.Contains(strings.Join(args, " "), " worktree remove ") && r.removeErr != nil {
		return subprocess.Result{}, r.removeErr
	}
	if command == "git" && strings.Contains(strings.Join(args, " "), " fetch ") && r.fetchErr != nil {
		return subprocess.Result{}, r.fetchErr
	}
	return r.canonicalizingPlannerRunner.Run(ctx, command, args, dir, timeout)
}

func (r *sourceInspectingPlanner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	prompt := string(data)
	if _, tail, ok := strings.Cut(prompt, "Runner-approved read-only repository root: "); ok {
		r.root = strings.SplitN(tail, "\n", 2)[0]
		if r.inspect != nil {
			if err := r.inspect(r.root); err != nil {
				return subprocess.Result{}, err
			}
		}
	}
	return r.canonicalizingPlannerRunner.RunBoundedHeadTailInput(ctx, command, args, dir, timeout, strings.NewReader(prompt), limit, marker)
}

func TestPlannerReadsFreshDestinationWithoutMovingDirtyCheckout(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	oldHead := runGitTest(t, repo, "rev-parse", "HEAD")
	update := filepath.Join(t.TempDir(), "update")
	runGitTest(t, "", "clone", remote, update)
	runGitTest(t, update, "config", "user.name", "Test User")
	runGitTest(t, update, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(update, "POLICY.md"), []byte("current focused verification policy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, update, "add", "POLICY.md")
	runGitTest(t, update, "commit", "-m", "current policy")
	runGitTest(t, update, "push", "origin", "main")
	current := strings.TrimSpace(runGitTest(t, update, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("operator's unfinished work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := runGitTest(t, repo, "diff")
	runner := &sourceInspectingPlanner{inspect: func(root string) error {
		if root == repo {
			return errors.New("planner was given the saved checkout")
		}
		data, err := os.ReadFile(filepath.Join(root, "POLICY.md"))
		if err != nil || string(data) != "current focused verification policy\n" {
			return errors.New("planner did not see current destination policy")
		}
		return nil
	}}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanProject(t.Context(), "Inspect the current policy before planning")
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 || plan.PlanningSource == nil || plan.PlanningSource.CommitOID != current || plan.PlanningSource.TreeOID != strings.TrimSpace(runGitTest(t, update, "rev-parse", "HEAD^{tree}")) {
		t.Fatalf("missing exact planning source or unexpected model work: source=%+v calls=%d", plan.PlanningSource, runner.calls)
	}
	if runGitTest(t, repo, "rev-parse", "HEAD") != oldHead || runGitTest(t, repo, "diff") != before {
		t.Fatal("operator checkout moved or changed")
	}
	if _, err := os.Stat(runner.root); !os.IsNotExist(err) {
		t.Fatalf("private snapshot was not cleaned: %v", err)
	}
	for _, prompt := range runner.inputs {
		if !strings.Contains(prompt, current) || !strings.Contains(prompt, plan.PlanningSource.TreeOID) {
			t.Fatal("outline/details lost pinned source identity")
		}
	}
	if err := service.validatePlanningSource(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	items := projectWorkItems(plan)
	if !strings.Contains(github.FormatPlannedItemBody(items[0]), current) {
		t.Fatal("authenticated child context lost source provenance")
	}
	manifest, err := service.deliveryManifest(github.WorkItem{Body: plan.SourceContext}, plan, []github.WorkItem{{ID: "child", ImplementationProfile: "implementer"}})
	if err != nil || !strings.Contains(strings.Join(manifest.Scope, "\n"), current) {
		t.Fatalf("delivery parent lost source provenance: %v", err)
	}

	// A later destination advance is neither a new model call nor permission
	// to relabel the original inspection as current.
	runGitTest(t, update, "commit", "--allow-empty", "-m", "destination advanced")
	runGitTest(t, update, "push", "origin", "main")
	if _, err := service.ApplyProjectPlan(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "destination changed since planning") {
		t.Fatalf("stale proposal was allowed to mutate Project: %v", err)
	}
	if runner.calls != 2 || plan.PlanningSource.CommitOID != current {
		t.Fatal("stale-source refusal replanned or relabelled retained work")
	}
}

func TestPlannerRejectsSnapshotMutationBeforeSynthesis(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runner := &sourceInspectingPlanner{inspect: func(root string) error {
		return os.WriteFile(filepath.Join(root, "README.md"), []byte("changed by harness\n"), 0o644)
	}}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	_, result, err := service.planProjectWithRole(t.Context(), "planner", "Plan one change")
	if err == nil || result.FailureClass != execution.FailureIntegrityViolation || runner.calls != 1 {
		t.Fatalf("source mutation was accepted or synthesized: calls=%d class=%s err=%v", runner.calls, result.FailureClass, err)
	}
	if _, err := os.Stat(runner.root); !os.IsNotExist(err) {
		t.Fatalf("failed snapshot was not cleaned: %v", err)
	}
}

func TestPlannerFetchFailureDoesNotInvokeHarnessOrUseSavedCheckout(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runner := &sourceInspectingPlanner{fetchErr: errors.New("fixture unavailable destination")}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.planProjectWithRole(t.Context(), "planner", "Plan one change")
	if err == nil || !strings.Contains(err.Error(), "fixture unavailable destination") || runner.calls != 0 || runner.root != "" {
		t.Fatalf("planning ran despite failed fresh-source fetch: calls=%d root=%q err=%v", runner.calls, runner.root, err)
	}
}

func TestPlannerRetainsSnapshotWhenHarnessCleanupIsUnresolved(t *testing.T) {
	testPlannerRetainedSnapshot(t, false)
}

func TestPlannerRetainsSnapshotWhenRemovalCleanupIsUnresolved(t *testing.T) {
	testPlannerRetainedSnapshot(t, true)
}

func testPlannerRetainedSnapshot(t *testing.T, duringRemoval bool) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	unresolved := &subprocess.CleanupError{Err: errors.New("fixture surviving owner")}
	runner := &sourceInspectingPlanner{inspect: func(string) error { return unresolved }}
	if duringRemoval {
		runner.inspect, runner.removeErr = nil, unresolved
	}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	_, result, err := service.planProjectWithRole(t.Context(), "planner", "Plan one change")
	if err == nil || result.FailureClass != execution.FailureCleanupUnresolved || !strings.Contains(err.Error(), runner.root) {
		t.Fatalf("unresolved cleanup lost retained path/failure: class=%s err=%v", result.FailureClass, err)
	}
	if _, err := os.Stat(runner.root); err != nil {
		t.Fatalf("live-owned snapshot was removed: %v", err)
	}
	if service.processOwnership.CheckAdmission() == nil {
		t.Fatal("unresolved planning cleanup released admission")
	}
	if duringRemoval && runner.calls != 2 {
		t.Fatalf("fixture did not reach successful planning before cleanup: calls=%d", runner.calls)
	}
	// No real process was launched by this fixture. Remove only its exact
	// retained linked checkout and now-empty private parent.
	runGitTest(t, repo, "worktree", "remove", "--force", runner.root)
	if err := os.Remove(filepath.Dir(runner.root)); err != nil {
		t.Fatal(err)
	}
}
