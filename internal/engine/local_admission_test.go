package engine

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type waitingPlannerRunner struct {
	canonicalizingPlannerRunner
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *waitingPlannerRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, limit int, marker string) (subprocess.Result, error) {
	r.once.Do(func() {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	if err := ctx.Err(); err != nil {
		return subprocess.Result{}, err
	}
	return r.canonicalizingPlannerRunner.RunBoundedHeadTailInput(ctx, command, args, dir, timeout, input, limit, marker)
}

func TestLocalPlanningSharesCapacityAndFreshBudgetHistory(t *testing.T) {
	for _, mode := range []string{"spare capacity", "full capacity", "budget exhausted"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			repo := t.TempDir()
			for _, args := range [][]string{{"init", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/owner/repo.git"}} {
				if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git: %v: %s", err, output)
				}
			}
			cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, MaxParallelism: 2,
				GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
			if mode == "full capacity" {
				cfg.MaxParallelism = 1
			}
			if mode == "budget exhausted" {
				cfg.AdmissionBudget = &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 1}
			}
			historyPath := filepath.Join(t.TempDir(), "history.jsonl")
			newService := func(runner subprocess.Runner) *Engine {
				t.Helper()
				service, err := New(cfg, runner)
				if err != nil {
					t.Fatal(err)
				}
				store := metrics.NewStore(historyPath)
				service.SetMetricsObserver(store.Append)
				service.SetMetricsHistoryReader(store.Read)
				service.EnableLocalAdmission()
				return service
			}
			waiting := &waitingPlannerRunner{started: make(chan struct{}), release: make(chan struct{})}
			first := newService(waiting)
			second := newService(&canonicalizingPlannerRunner{})
			// Warm the second process's cache before the first reserves an attempt.
			if decision, err := second.AdmissionStatus(time.Now().UTC()); err != nil || !decision.Allowed {
				t.Fatalf("initial admission: %+v %v", decision, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			completed := make(chan error, 1)
			go func() { _, err := first.PlanProject(ctx, "Build a feature"); completed <- err }()
			select {
			case <-waiting.started:
			case <-ctx.Done():
				t.Fatal("first planner did not start")
			}
			_, secondErr := second.PlanProject(ctx, "Build another feature")
			close(waiting.release)
			if err := <-completed; err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "spare capacity":
				if secondErr != nil {
					t.Fatalf("independent planner was blocked: %v", secondErr)
				}
			case "full capacity":
				if !errors.Is(secondErr, github.ErrProjectLockBusy) {
					t.Fatalf("capacity was bypassed: %v", secondErr)
				}
				if _, err := second.PlanProject(ctx, "Capacity is free again"); err != nil {
					t.Fatal(err)
				}
			case "budget exhausted":
				if secondErr == nil || !strings.Contains(secondErr.Error(), "admission paused") {
					t.Fatalf("stale budget cache allowed another attempt: %v", secondErr)
				}
			}
		})
	}
}

func TestLocalAdmissionWaitCanBeCanceled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service := &Engine{cfg: config.RuntimeConfig{GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{Owner: "owner", Number: 4}}}}
	service.EnableLocalAdmission()
	guard, err := service.acquireLocalAdmission(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if lock, err := service.acquireLocalAdmission(ctx, true); !errors.Is(err, context.Canceled) {
		_ = lock.Release()
		t.Fatalf("canceled admission did not stop: %v", err)
	}
}

func TestLocalBatchMutationDefersRecoveryButNotObservation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	item := github.WorkItem{ID: "PVTI_staging", Title: "Being staged", Body: "Criteria", Status: "Needs assessment", Transition: "v1"}
	project := &fakeGitHubProjectRunner{remoteItems: []github.WorkItem{item}, itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	service.EnableLocalAdmission()
	guard, err := github.AcquirePlanningMutationLock(*cfg.GitHubProject)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	prepared, err := service.preparePoll(t.Context(), 0, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.items) != 1 || project.remoteItems[0].Transition != "v1" {
		t.Fatalf("active staging was recovered or observation stopped: %+v", prepared)
	}
	for _, call := range project.calls {
		if strings.Contains(call, "project item-edit") {
			t.Fatalf("worker mutated a live CLI transition: %s", call)
		}
	}
	if err := guard.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.preparePoll(t.Context(), 0, true, nil); err != nil {
		t.Fatal(err)
	}
	if project.remoteItems[0].Transition != "" {
		t.Fatal("interrupted-state recovery did not resume after mutation guard released")
	}
}

func TestWorkerLeavesReadyCardQueuedWhenCLIUsesLastExecutionSlot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	item := github.WorkItem{ID: "PVTI_ready", Title: "Queued work", Body: "Criteria", Repository: "owner/repo", Status: "Ready"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{remoteItems: []github.WorkItem{item}, itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), MaxParallelism: 1, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	service.EnableLocalAdmission()
	slot, err := github.AcquireExecutionSlot(*cfg.GitHubProject, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	prepared, err := service.preparePoll(t.Context(), 1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.claimed) != 0 || project.remoteItems[0].Status != "Ready" {
		t.Fatalf("capacity exhaustion claimed or blocked the card: %+v", prepared)
	}
	if err := slot.Release(); err != nil {
		t.Fatal(err)
	}
	prepared, err = service.preparePoll(t.Context(), 1, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, admitted := range prepared.claimed {
		defer admitted.slot.Release()
	}
	if len(prepared.claimed) != 1 {
		t.Fatalf("worker did not claim the card once capacity returned: %+v", prepared)
	}
}
