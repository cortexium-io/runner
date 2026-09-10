package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type qaOnlyTestRunner struct {
	project                     *fakeGitHubProjectRunner
	head, base, branch, verdict string
	reviewCalls                 int
	afterReview                 func()
	commands, prompts           []string
}

func (r *qaOnlyTestRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *qaOnlyTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	r.commands = append(r.commands, command+" "+joined)
	if command == "gh" && strings.HasPrefix(joined, "pr view ") {
		payload, _ := json.Marshal(map[string]any{"url": "https://github.com/owner/repo/pull/6", "number": 6, "state": "OPEN", "headRepository": map[string]any{"nameWithOwner": "owner/repo"}, "headRefName": r.branch, "headRefOid": r.head, "baseRefName": "main", "baseRefOid": r.base})
		return subprocess.Result{Stdout: string(payload)}, nil
	}
	if command == "codex" {
		r.reviewCalls++
		r.prompts = append(r.prompts, args[len(args)-1])
		if r.afterReview != nil {
			r.afterReview()
		}
		if r.verdict == "provider_failure" {
			return subprocess.Result{}, errors.New("provider unavailable")
		}
		if r.verdict == "needs_changes" {
			return (reviewerRejectRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
		}
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func qaOnlyFixture(t *testing.T) (*Engine, *qaOnlyTestRunner, workspace.Metadata) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_review_only", Title: "Retained candidate", Body: "## Proof obligations\n- Works", Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/5", Status: "Blocked", Phase: "agent_qa", Branch: "runner/retained", PullRequest: "https://github.com/owner/repo/pull/6", QAFailures: 7}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &qaOnlyTestRunner{project: project, branch: item.Branch}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("ready\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "add", "feature.txt")
	runGitTest(t, metadata.WorktreePath, "commit", "-m", "candidate")
	runner.head = strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	runner.base = metadata.BaseRevision
	project.loadRemoteItems()
	feedback := reviewFeedbackRecord{Version: reviewFeedbackVersion, ItemID: item.ID, DelegatedContentDigest: metadata.Identity.DelegatedContentDigest, Items: []string{"Historical finding: verify the saved state."}}
	encoded, _ := json.Marshal(feedback)
	path := service.reviewFeedbackPath(item.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := workspace.Candidate{CommitOID: runner.head, TreeOID: strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))}
	if err := service.saveVerificationEvidence(item, github.DelegatedContentFor(item), metadata, candidate, []string{"Works"}, []string{"Existing test passed for this candidate."}); err != nil {
		t.Fatal(err)
	}
	return service, runner, metadata
}

func TestQAOnlyReauthorizationPreservesOriginalStateAndConsumesOneReview(t *testing.T) {
	for _, verdict := range []string{"accept", "needs_changes", "provider_failure"} {
		t.Run(verdict, func(t *testing.T) {
			service, runner, metadata := qaOnlyFixture(t)
			runner.verdict = verdict
			// The real recovery includes changed annotations and requirements.
			runner.project.remoteItems[0].Body += "\nOperational pause: current requirements now include restored state."
			before := runner.project.remoteItems[0]
			paths := []string{service.reviewFeedbackPath(before.ID), service.verificationEvidencePath(before.ID), filepath.Join(filepath.Dir(metadata.WorktreePath), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")}
			original := map[string]string{}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				original[path] = string(data)
			}
			var events []metrics.Event
			service.SetMetricsObserver(func(event metrics.Event) error { events = append(events, event); return nil })
			plan, err := service.PlanQAReauthorization(t.Context(), before.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.CurrentContentDigest == plan.Workspace.DelegatedContentDigest || len(plan.Feedback) != 1 || len(plan.Verification) != 0 || runner.reviewCalls != 0 {
				t.Fatalf("invalid preview: %#v", plan)
			}
			result, err := service.RunQAReauthorization(t.Context(), plan)
			if verdict == "provider_failure" {
				if err == nil || result.Outcome != execution.OutcomeBlocked {
					t.Fatalf("failure=%#v %v", result, err)
				}
			} else {
				if err != nil || result.Assessment == nil || result.Assessment.Verdict != verdict {
					t.Fatalf("review=%#v %v", result, err)
				}
			}
			if runner.reviewCalls != 1 || !reflect.DeepEqual(before, runner.project.remoteItems[0]) || result.CandidateOID != runner.head || result.RetryDisposition != "none" {
				t.Fatalf("one-shot review changed workflow or repeated: %#v calls=%d", result, runner.reviewCalls)
			}
			for path, expected := range original {
				data, err := os.ReadFile(path)
				if err != nil || string(data) != expected {
					t.Fatalf("modified retained history %s: %v", path, err)
				}
			}
			if !strings.Contains(runner.prompts[0], "ONE review") || !strings.Contains(runner.prompts[0], before.Body) || !strings.Contains(runner.prompts[0], plan.Feedback[0]) {
				t.Fatal("review omitted bounded authority, new requirements or history")
			}
			if _, err := service.RunQAReauthorization(t.Context(), plan); err == nil || runner.reviewCalls != 1 {
				t.Fatal("one-shot approval replayed")
			}
			started, completed := 0, 0
			for _, event := range events {
				if event.Kind == metrics.EventStarted {
					started++
				}
				if event.Kind == metrics.EventCompleted {
					completed++
				}
			}
			if started != 1 || completed != 1 {
				t.Fatalf("attempt accounting=%d/%d", started, completed)
			}
			for _, command := range runner.commands {
				if strings.Contains(command, "project item-edit") || strings.Contains(command, "updateProjectV2ItemFieldValue") || strings.HasPrefix(command, "gh pr create") || strings.HasPrefix(command, "gh pr merge") || strings.Contains(command, " push ") || strings.Contains(command, " rebase ") {
					t.Fatalf("unauthorized mutation: %s", command)
				}
			}
		})
	}
}

func TestQAOnlyReauthorizationRefusesChangedOrUnreviewableState(t *testing.T) {
	for _, change := range []string{"body", "candidate", "dirty", "PR head", "approval", "phase", "fresh staged", "counter", "workspace binding", "feedback binding", "modified preview", "empty preview", "config", "references", "in flight", "PR changed during review", "workspace changed during review"} {
		t.Run(change, func(t *testing.T) {
			service, runner, metadata := qaOnlyFixture(t)
			plan, err := service.PlanQAReauthorization(t.Context(), "PVTI_review_only")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				runner.project.remoteItems[0].Body += "changed"
			case "candidate":
				runGitTest(t, metadata.WorktreePath, "commit", "--allow-empty", "-m", "another candidate")
			case "dirty":
				if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "dirty"), []byte("change"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "PR head":
				runner.head = strings.Repeat("a", 40)
			case "approval":
				runner.project.remoteItems[0].Approval = "another approval"
			case "phase":
				runner.project.remoteItems[0].Phase = "ready"
			case "fresh staged":
				runner.project.remoteItems[0].Status = "Needs assessment"
			case "counter":
				runner.project.remoteItems[0].QAFailures++
			case "workspace binding":
				path := filepath.Join(filepath.Dir(metadata.WorktreePath), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), metadata.Identity.Repository, "other/repo")), 0o600); err != nil {
					t.Fatal(err)
				}
			case "feedback binding":
				path := service.reviewFeedbackPath(plan.Item.ID)
				data, _ := os.ReadFile(path)
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), metadata.Identity.DelegatedContentDigest, "v1:wrong")), 0o600); err != nil {
					t.Fatal(err)
				}
			case "modified preview":
				plan.Item.Body = "changed preview"
			case "empty preview":
				plan.seal = nil
			case "config":
				service.cfg.MaxParallelism++
			case "references":
				service.cfg.RepositoryReferences = []config.RepositoryReference{{Name: "changed", Path: t.TempDir(), Commit: strings.Repeat("a", 40)}}
			case "in flight":
				lock, err := github.AcquireQAReviewLock(service.cfg.GitHubProject.GitHubProjectConfig, plan.Item.ID)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			case "PR changed during review":
				runner.afterReview = func() { runner.head = strings.Repeat("a", 40) }
			case "workspace changed during review":
				runner.afterReview = func() {
					if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "dirty"), []byte("changed"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			before := runner.project.remoteItems[0]
			result, err := service.RunQAReauthorization(t.Context(), plan)
			if err == nil || result.Assessment != nil || !reflect.DeepEqual(before, runner.project.remoteItems[0]) {
				t.Fatalf("invalid review was authorized or changed card: %#v %v", result, err)
			}
			if !strings.Contains(change, "during review") && runner.reviewCalls != 0 {
				t.Fatal("invalid preview started a reviewer")
			}
		})
	}
}

func TestQAOnlyReviewUsesNewPinnedReferencesWithoutRebindingHistory(t *testing.T) {
	service, runner, metadata := qaOnlyFixture(t)
	reference, _ := createPublicationRepository(t)
	if err := os.WriteFile(filepath.Join(reference, "requirements.md"), []byte("Current reviewed requirements\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, reference, "add", "requirements.md")
	runGitTest(t, reference, "commit", "-m", "reconciled requirements")
	pin := strings.TrimSpace(runGitTest(t, reference, "rev-parse", "HEAD"))
	service.cfg.RepositoryReferences = []config.RepositoryReference{{Name: "product-docs", Path: reference, Commit: pin}}
	runner.project.remoteItems[0].Body += "\nUse product-docs at " + pin
	plan, err := service.PlanQAReauthorization(t.Context(), "PVTI_review_only")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.References) != 1 || plan.References[0].Commit != pin {
		t.Fatalf("new pin absent: %#v", plan.References)
	}
	result, err := service.RunQAReauthorization(t.Context(), plan)
	if err != nil || result.Assessment == nil || result.Assessment.Verdict != "accept" {
		t.Fatalf("review=%#v error=%v", result, err)
	}
	if !strings.Contains(runner.prompts[0], pin) || strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")) != runner.head {
		t.Fatal("review lost its pin or rewrote the candidate")
	}
}

func TestQAOnlyReviewSharesAdmissionWithoutTakingWorkerLock(t *testing.T) {
	for _, mode := range []string{"spare capacity", "full capacity", "budget exhausted"} {
		t.Run(mode, func(t *testing.T) {
			service, runner, _ := qaOnlyFixture(t)
			service.EnableLocalAdmission()
			service.cfg.MaxParallelism = 2
			if mode == "full capacity" {
				service.cfg.MaxParallelism = 1
			}
			if mode == "budget exhausted" {
				service.cfg.AdmissionBudget = &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 1}
				store := metrics.NewStore(filepath.Join(t.TempDir(), "metrics.jsonl"))
				service.SetMetricsObserver(store.Append)
				service.SetMetricsHistoryReader(store.Read)
				event := service.newItemAttempt(runner.project.remoteItems[0])
				event.Kind = metrics.EventStarted
				if err := store.Append(event); err != nil {
					t.Fatal(err)
				}
			}
			worker, err := github.AcquireProcessLock(service.cfg.GitHubProject.GitHubProjectConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Release()
			slot, err := github.AcquireExecutionSlot(service.cfg.GitHubProject.GitHubProjectConfig, service.cfg.MaxParallelism)
			if err != nil {
				t.Fatal(err)
			}
			defer slot.Release()
			plan, err := service.PlanQAReauthorization(t.Context(), "PVTI_review_only")
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.RunQAReauthorization(t.Context(), plan)
			if mode == "spare capacity" {
				if err != nil || runner.reviewCalls != 1 {
					t.Fatalf("worker prevented safe QA: %v", err)
				}
			} else if err == nil || runner.reviewCalls != 0 {
				t.Fatalf("QA bypassed admission: %v", err)
			}
		})
	}
}
