package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type recoveryTestRunner struct {
	project   *fakeGitHubProjectRunner
	afterLock func()
}

func (r *recoveryTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	result, err := r.project.Run(ctx, command, args, dir, timeout)
	if err == nil && argumentValue(args, "--field-id") == "F_transition" && argumentValue(args, "--text") == "v1" && r.afterLock != nil {
		hook := r.afterLock
		r.afterLock = nil
		hook()
	}
	return result, err
}

func TestCLIActionMutationExcludesWorkerRecovery(t *testing.T) {
	for _, operation := range []string{"retry", "approve"} {
		t.Run(operation, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			item := github.WorkItem{ID: "PVTI_recover", Title: "Work", Body: "Criteria", Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/1", Status: "Needs assessment"}
			if operation == "retry" {
				item.Status, item.Phase = "Blocked", "ready"
				item.Approval = testApproval(item)
			}
			project := &fakeGitHubProjectRunner{remoteItems: []github.WorkItem{item}, itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
			runner := &recoveryTestRunner{project: project}
			cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
			cli, err := New(cfg, runner)
			if err != nil {
				t.Fatal(err)
			}
			worker, err := New(cfg, project)
			if err != nil {
				t.Fatal(err)
			}
			cli.EnableLocalAdmission()
			worker.EnableLocalAdmission()
			observed := false
			runner.afterLock = func() {
				observed = true
				before := project.remoteItems[0]
				prepared, err := worker.preparePoll(t.Context(), 0, true, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(prepared.items) != 1 || !reflect.DeepEqual(project.remoteItems[0], before) {
					t.Fatal("background recovery mutated the CLI's live transition or stopped observing")
				}
			}
			if operation == "retry" {
				plan, err := cli.PlanProjectItemRetry(t.Context(), item.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = cli.ApplyProjectItemRetry(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				plan, err := cli.PlanProjectItemApproval(t.Context(), item.ID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = cli.ApplyProjectItemApproval(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !observed || project.remoteItems[0].Approval == "" || project.remoteItems[0].Transition != "" {
				t.Fatal("CLI did not commit and unlock its authenticated transition")
			}
			guard, err := github.AcquirePlanningMutationLock(*cfg.GitHubProject)
			if err != nil {
				t.Fatalf("mutation guard leaked: %v", err)
			}
			guard.Release()
		})
	}
}

func reauthorizationFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	planned := github.PlannedItem{Title: "Retained implementation", Repository: "owner/repo", Summary: "Complete the implementation", AcceptanceCriteria: []string{"Works"},
		PlanningSourceLane: "local_plan", PlanningSourceFingerprint: "v1:source", PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch", PlanningBatchSize: 2, PlanningItemIndex: 1, DependencyIDsResolved: true}
	item := github.WorkItem{ID: "PVTI_recover", Title: planned.Title, Body: github.FormatPlannedItemBody(planned), Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/1",
		Status: "Needs assessment", Phase: "ready", Branch: "runner/retained", QAFailures: 2, Result: "Interrupted Project transition has incomplete Runner authority; review it and run approve again."}
	planned.PlanningItemIndex = 2
	sibling := github.WorkItem{ID: "PVTI_sibling", Title: "Sibling", Body: github.FormatPlannedItemBody(planned), Repository: "owner/repo", Status: "Blocked", Phase: "ready"}
	sibling.Approval = testApproval(sibling)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `,` + projectItemJSON(sibling) + `]}`}
	runner := &recoveryTestRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}}), runner)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item = items[0]
	metadata, err := workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false))
	if err != nil {
		t.Fatal(err)
	}
	project.loadRemoteItems()
	return service, project, runner, metadata
}

func TestReauthorizeRetainedImplementationPreservesWorkAndOnlyReleasesSelectedCard(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, runner, metadata := reauthorizationFixture(t)
	service.EnableLocalAdmission()
	item := project.remoteItems[0]
	sibling := project.remoteItems[1]
	dirty := filepath.Join(metadata.WorktreePath, "unfinished.txt")
	if err := os.WriteFile(dirty, []byte("retain my unfinished fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	feedback := service.reviewFeedbackPath(item.ID)
	if err := os.MkdirAll(filepath.Dir(feedback), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedback, []byte("private feedback must not be reset"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanProjectItemReauthorization(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project.remoteItems[0], item) {
		t.Fatal("preview mutated the card")
	}
	runner.afterLock = func() {
		guard, err := github.AcquirePlanningMutationLock(service.cfg.GitHubProject.GitHubProjectConfig)
		if !errors.Is(err, github.ErrProjectLockBusy) {
			guard.Release()
			t.Fatal("reauthorization did not exclude background recovery")
		}
	}
	recovered, err := service.ApplyProjectItemReauthorization(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != "Ready" || recovered.QAFailures != 2 || recovered.Branch != item.Branch || recovered.Approval == "" || recovered.QACommit != "" {
		t.Fatalf("unexpected recovery state: %+v", recovered)
	}
	if !reflect.DeepEqual(project.remoteItems[1], sibling) {
		t.Fatal("recovery rewrote a sibling")
	}
	if _, err := service.source.Authorize(t.Context(), project.remoteItems[0]); err != nil {
		t.Fatalf("recovered action is not valid: %v", err)
	}
	for path, expected := range map[string]string{dirty: "retain my unfinished fix\n", feedback: "private feedback must not be reset"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != expected {
			t.Fatalf("recovery lost %s: %s %v", path, content, err)
		}
	}
	if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
		t.Fatal("replayed recovery was accepted")
	}
}

func TestReauthorizationFailsClosed(t *testing.T) {
	for _, change := range []string{"body", "branch", "approval", "phase", "pull request", "qa commit", "transition", "sibling approval", "missing sibling", "missing identity", "after preview", "modified preview"} {
		t.Run(change, func(t *testing.T) {
			service, project, _, metadata := reauthorizationFixture(t)
			before := append([]github.WorkItem(nil), project.remoteItems...)
			plan, err := service.PlanProjectItemReauthorization(t.Context(), before[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				project.remoteItems[0].Body += "\nUnapproved scope change."
			case "branch":
				project.remoteItems[0].Branch = "runner/other"
			case "approval":
				project.remoteItems[0].Approval = "invalid"
			case "phase":
				project.remoteItems[0].Phase = "agent_qa"
			case "pull request":
				project.remoteItems[0].PullRequest = "https://github.com/owner/repo/pull/3"
			case "qa commit":
				project.remoteItems[0].QACommit = strings.Repeat("a", 40)
			case "transition":
				project.remoteItems[0].Transition = "v1"
			case "sibling approval":
				project.remoteItems[1].Approval = "invalid"
			case "missing sibling":
				project.remoteItems = project.remoteItems[:1]
			case "missing identity":
				if err := os.Remove(filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")); err != nil {
					t.Fatal(err)
				}
			case "after preview":
				project.remoteItems[0].QAFailures++
			case "modified preview":
				plan.Approval.Item.QAFailures = 0
			}
			callCount := len(project.calls)
			if change != "after preview" && change != "modified preview" {
				if _, err := service.PlanProjectItemReauthorization(t.Context(), before[0].ID); err == nil {
					t.Fatal("invalid recovery preview accepted")
				}
			}
			if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
				t.Fatal("invalid recovery applied")
			}
			for _, call := range project.calls[callCount:] {
				if strings.Contains(call, "project item-edit") || strings.Contains(call, "updateProjectV2ItemFieldValue") {
					t.Fatalf("failed recovery wrote Project state: %s", call)
				}
			}
		})
	}
}
