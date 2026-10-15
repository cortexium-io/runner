package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

const brief = "## Outcome\nChange the greeting.\n## Scope\nOnly greeting.txt.\n## Acceptance\nThe greeting is hello.\n## Verification\nInspect greeting.txt at the file boundary; expected hello. Existing file fixture is sufficient; no framework or mocks.\n"

type adapter func(context.Context, execution.Request, func(core.Process) error) execution.Completion

func (f adapter) Execute(c context.Context, r execution.Request, s func(core.Process) error) execution.Completion {
	return f(c, r, s)
}

type checkAdapter func(context.Context, string, config.QualityCheck, string, func(core.Process) error) core.CheckObservation

func (f checkAdapter) Check(ctx context.Context, dir string, c config.QualityCheck, evidence string, started func(core.Process) error) core.CheckObservation {
	return f(ctx, dir, c, evidence, started)
}
func pass(r execution.Request) execution.Completion {
	checks := []core.Check{}
	for _, p := range r.Proofs {
		checks = append(checks, core.Check{ID: p.ID, Status: "pass", Evidence: "Inspected greeting.txt; exact approved greeting and scope match."})
	}
	return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Approved behavior verified", Checks: checks}}
}
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	b, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s: %v", args, b, e)
	}
	return strings.TrimSpace(string(b))
}
func fixture(t *testing.T, configure ...func(*config.Config)) *Engine {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	repo := filepath.Join(root, "repo")
	if e = os.Mkdir(repo, 0700); e != nil {
		t.Fatal(e)
	}
	git(t, repo, "init", "-b", "main")
	git(t, repo, "config", "user.name", "Test")
	git(t, repo, "config", "user.email", "test@example.invalid")
	if e = os.WriteFile(filepath.Join(repo, "greeting.txt"), []byte("before\n"), 0600); e != nil {
		t.Fatal(e)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "baseline")
	p := core.Profile{Harness: "codex", Model: "test-model", Reasoning: "high", Timeout: "1m"}
	cfg := config.Config{Format: "runner-local-v1", Repository: config.Repository{Path: repo, BaseRef: "refs/heads/main", BaseFreshness: "recorded"}, StateDir: filepath.Join(root, "state"), WorktreeDir: filepath.Join(root, "worktrees"), Profiles: map[string]core.Profile{"build": p, "review": p}, Roles: map[string]string{"implementer": "build", "reviewer": "review"}, Policy: config.Policy{MaxParallelism: 1}}
	cfg.Delivery.Mode = "local"
	for _, change := range configure {
		change(&cfg)
	}
	id, base, e := workspace.Observe(context.Background(), cfg)
	if e != nil {
		t.Fatal(e)
	}
	settings := config.Settings{Config: cfg, RepositoryID: id, Quality: config.Quality{Definition: "Approved greeting", Checks: []config.QualityCheck{{ID: "greeting", Command: "test -f greeting.txt", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"}}, Base: base}}
	db, e := store.Create(context.Background(), cfg.StateDir, settings)
	if e != nil {
		t.Fatal(e)
	}
	app := &Engine{Store: db, Settings: settings, Checker: checkAdapter(func(_ context.Context, _ string, c config.QualityCheck, _ string, _ func(core.Process) error) core.CheckObservation {
		return core.CheckObservation{CheckID: c.ID, Status: "passed", Stopped: true, OutputDigest: "passed"}
	}), Executor: adapter(func(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.Role == "implementer" {
			if e := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); e != nil {
				return execution.Completion{Stopped: true, Error: e.Error()}
			}
		}
		return pass(r)
	})}
	t.Cleanup(func() { app.Store.Close() })
	return app
}
func approved(t *testing.T, e *Engine) core.Task {
	t.Helper()
	ctx := context.Background()
	task, err := e.Add(ctx, brief)
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Show(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Approve(ctx, task.ID, 1, v.Preview)
	if err != nil {
		t.Fatal(err)
	}
	task, err = e.Store.Task(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task
}
func claim(t *testing.T, e *Engine) core.Attempt {
	t.Helper()
	if err := e.Store.SetDrained(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	a, ok, err := e.claim(context.Background())
	if err != nil || !ok {
		t.Fatalf("claim: %t %v", ok, err)
	}
	return a
}
func complete(t *testing.T, e *Engine, a core.Attempt) {
	t.Helper()
	f := e.execute(context.Background(), a)
	if f.completion.Error != "" {
		t.Fatal(f.completion.Error)
	}
	if err := e.saveCompletion(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}
func reopen(t *testing.T, e *Engine) {
	t.Helper()
	e.Store.Close()
	db, err := store.Open(context.Background(), e.Settings.Config.StateDir, false)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = db
}
func finish(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.finishSaved(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func current(t *testing.T, e *Engine, id string) core.Task {
	t.Helper()
	task, err := e.Store.Task(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestSavedImplementationAndReviewContinueAfterReopenWithoutAnotherInvocation(t *testing.T) {
	for _, afterConstruction := range []bool{false, true} {
		t.Run(map[bool]string{false: "before candidate", true: "after candidate"}[afterConstruction], func(t *testing.T) {
			e := fixture(t)
			task := approved(t, e)
			original, err := workspace.Snapshot(context.Background(), e.Settings.Config.Repository.Path)
			if err != nil {
				t.Fatal(err)
			}
			a := claim(t, e)
			complete(t, e, a)
			if afterConstruction {
				as, err := e.Store.Attempts(context.Background(), task.ID)
				if err != nil {
					t.Fatal(err)
				}
				saved := as[0]
				task = current(t, e, task.ID)
				if err = e.advance(context.Background(), &task, &saved); err != nil {
					t.Fatal(err)
				}
			}
			reopen(t, e)
			finish(t, e)
			task = current(t, e, task.ID)
			if task.Stage != core.Reviewing || task.Candidate == nil {
				t.Fatalf("candidate not recovered: %+v", task)
			}
			a = claim(t, e)
			if a.Role != "audit" {
				t.Fatal(a.Role)
			}
			complete(t, e, a)
			reopen(t, e)
			finish(t, e)
			task = current(t, e, task.ID)
			if task.Stage != core.AwaitingDelivery {
				t.Fatalf("review acceptance not recovered: %+v", task)
			}
			as, err := e.Store.Attempts(context.Background(), task.ID)
			if err != nil || len(as) != 2 {
				t.Fatalf("unexpected repeated invocation: %v %v", as, err)
			}
			after, err := workspace.Snapshot(context.Background(), e.Settings.Config.Repository.Path)
			if err != nil || after.Fingerprint != original.Fingerprint {
				t.Fatal("original checkout changed", err)
			}
			if err = e.git().ValidateCandidate(context.Background(), task); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestStalePreviewAndExactRepeatedApproval(t *testing.T) {
	e := fixture(t)
	ctx := context.Background()
	task, err := e.Add(ctx, brief)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := e.Show(ctx, task.ID)
	git(t, e.Settings.Config.Repository.Path, "commit", "--allow-empty", "-m", "new base")
	if _, err = e.Approve(ctx, task.ID, 1, v.Preview); err == nil {
		t.Fatal("stale base approved")
	}
	v, _ = e.Show(ctx, task.ID)
	if _, err = e.Approve(ctx, task.ID, 1, v.Preview); err != nil {
		t.Fatal(err)
	}
	before := current(t, e, task.ID)
	if _, err = e.Approve(ctx, task.ID, 1, v.Preview); err != nil {
		t.Fatal(err)
	}
	after := current(t, e, task.ID)
	if before.Revision != after.Revision || before.Generation != after.Generation {
		t.Fatal("repeat changed authority")
	}
	if _, err = e.Replace(ctx, task.ID, 1, brief+"replacement\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Approve(ctx, task.ID, 1, v.Preview); err == nil {
		t.Fatal("superseded version approved")
	}
}
func TestLateCompletionCannotAdvanceStoppedOrReplacedTask(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "stop", true: "replace"}[replace], func(t *testing.T) {
			e := fixture(t)
			task := approved(t, e)
			a := claim(t, e)
			f := e.execute(context.Background(), a)
			var err error
			if replace {
				_, err = e.Replace(context.Background(), task.ID, 1, brief+"new scope\n")
			} else {
				err = e.Stop(context.Background(), task.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = e.saveCompletion(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			task = current(t, e, task.ID)
			if task.Approval != nil || task.Candidate != nil || task.NextRole != "" {
				t.Fatalf("late result restored authority: %+v", task)
			}
			as, _ := e.Store.Attempts(context.Background(), task.ID)
			if len(as) != 1 || as[0].State != "superseded" || as[0].Result == nil {
				t.Fatal("late evidence not retained")
			}
		})
	}
}
func TestChangedCandidateBlocksSavedAcceptanceAndHiddenRepair(t *testing.T) {
	e := fixture(t)
	task := approved(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	complete(t, e, claim(t, e))
	if err := os.WriteFile(filepath.Join(task.Candidate.Workspace, "greeting.txt"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	finish(t, e)
	task = current(t, e, task.ID)
	if task.Stage == core.AwaitingDelivery || task.Attention == nil || task.Attention.Kind != "saved_result" {
		t.Fatalf("tampered acceptance reused: %+v", task)
	}
	if _, err := e.Resume(context.Background(), task.ID, task.Attention.Attempt, true); err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	task = current(t, e, task.ID)
	if task.Stage == core.AwaitingDelivery {
		t.Fatal("resume bypassed integrity")
	}
	as, _ := e.Store.Attempts(context.Background(), task.ID)
	if len(as) != 2 {
		t.Fatal("hidden repair invocation")
	}
}
func TestReservedAttemptRequiresExplicitSingleRecovery(t *testing.T) {
	for _, planning := range []bool{false, true} {
		t.Run(map[bool]string{false: "implementation", true: "planning"}[planning], func(t *testing.T) {
			e := fixture(t)
			var task core.Task
			if planning {
				task = approvedPlan(t, e, proposal())
			} else {
				task = approved(t, e)
			}
			a := claim(t, e)
			reopen(t, e)
			if err := e.recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.claim(context.Background()); err != nil || ok {
				t.Fatalf("automatic replay: %t %v", ok, err)
			}
			if _, err := e.Resume(context.Background(), task.ID, a.ID, false); err == nil {
				t.Fatal("new invocation did not require authorization")
			}
			if _, err := e.Resume(context.Background(), task.ID, a.ID, true); err != nil {
				t.Fatal(err)
			}
			child := claim(t, e)
			if child.ResumedFrom != a.ID {
				t.Fatal("recovery parent missing")
			}
			if _, err := e.Resume(context.Background(), task.ID, a.ID, true); err != nil {
				t.Fatal(err)
			}
			as, _ := e.Store.Attempts(context.Background(), task.ID)
			if len(as) != 2 {
				t.Fatal("duplicate recovery")
			}

		})
	}
}
func TestCoordinatorResponsiveAndExclusiveWhileExecutionIsHeld(t *testing.T) {
	e := fixture(t)
	task := approved(t, e)
	started := make(chan struct{})
	released := make(chan struct{})
	e.Executor = adapter(func(ctx context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		close(started)
		<-ctx.Done()
		close(released)
		return execution.Completion{Stopped: true, Error: "cancelled"}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("execution did not start")
	}
	if err := e.Run(ctx); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal("second owner admitted", err)
	}
	if _, err := e.Show(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	other, err := e.Add(ctx, brief)
	if err != nil {
		t.Fatal(err)
	}
	if current(t, e, other.ID).Stage != core.Draft {
		t.Fatal("unrelated command blocked")
	}
	if err = e.Stop(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("stop did not cancel active invocation")
	}
	if err = e.Store.SetDrained(ctx, true); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not settle")
	}
	if current(t, e, task.ID).Stage != core.Stopped {
		t.Fatal("late result advanced stopped task")
	}
}
func TestReviewClassifiesMissingProofDefectAndDecisionWithoutRepair(t *testing.T) {
	for _, status := range []string{"gap", "defect", "needs_input", "malformed"} {
		t.Run(status, func(t *testing.T) {
			e := fixture(t)
			task := approved(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				c := pass(r)
				if status == "malformed" {
					c.Result.Checks = nil
				} else {
					c.Result.Checks[0].Status = status
				}
				return c
			})
			a := claim(t, e)
			f := e.execute(context.Background(), a)
			if err := e.saveCompletion(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			task = current(t, e, task.ID)
			if task.Stage == core.AwaitingDelivery {
				t.Fatal("insufficient review accepted")
			}
			if status == "gap" {
				if task.NextRole != "verify" {
					t.Fatal("gap not routed to focused verification")
				}
				return
			}
			if status == "defect" {
				if task.Pending == nil || task.Pending.Kind != "repair" {
					t.Fatal("defect lost its repair reason")
				}
				if _, ok, err := e.claim(context.Background()); err != nil || ok {
					t.Fatal("zero repair allowance admitted work", err)
				}

				if _, err := e.Resume(context.Background(), task.ID, a.ID, true); err == nil {
					t.Fatal("rejected candidate secretly repaired")
				}
			} else if task.Attention == nil {
				t.Fatal("missing attention")
			}
		})
	}
}
func TestSurvivingExecutionRetainsReservation(t *testing.T) {
	e := fixture(t)
	task := approved(t, e)
	a := claim(t, e)
	a.Process = &core.Process{PID: syscall.Getpgrp(), Identity: "intentionally unknown"}
	a.State = "running"
	task = current(t, e, task.ID)
	if err := e.Store.Commit(context.Background(), task.Revision, task, &a, false); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	if err := e.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	as, _ := e.Store.Attempts(context.Background(), task.ID)
	if !as[0].Occupying {
		t.Fatal("surviving group released")
	}
	task = current(t, e, task.ID)
	if task.Attention == nil || task.Attention.Kind != "ownership" {
		t.Fatal("unknown identity not reported")
	}
	if _, err := e.Resume(context.Background(), task.ID, a.ID, true); err == nil {
		t.Fatal("unsafe reclaim")
	}
}

func TestFocusedVerificationRunsOnlyGapAndCannotWeakenSource(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(map[bool]string{false: "preserves source", true: "changes source"}[tamper], func(t *testing.T) {
			e := fixture(t)
			task := approved(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				result := pass(r)
				if r.Attempt.Role == "audit" {
					result.Result.Checks[1].Status = "gap"
					result.Result.Checks[1].Evidence = "Run the exact file assertion in the disposable copy."
				} else {
					if len(r.Proofs) != 1 || r.Proofs[0].ID != "verification" {
						t.Error("focused verification repeated already satisfied proof")
					}
					name, body := "output.txt", "verified hello\n"
					if tamper {
						name, body = "greeting.txt", "weakened\n"
					}
					if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(body), 0600); err != nil {
						t.Error(err)
					}
				}
				return result
			})
			complete(t, e, claim(t, e))
			finish(t, e)
			a := claim(t, e)
			f := e.execute(context.Background(), a)
			if err := e.saveCompletion(context.Background(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			task = current(t, e, task.ID)
			if tamper {
				if task.Stage == core.AwaitingDelivery || task.Attention == nil || task.Attention.Kind != "integrity" {
					t.Fatalf("source change accepted: %+v", task)
				}
				if _, err := e.Resume(context.Background(), task.ID, a.ID, true); err == nil {
					t.Fatal("integrity failure repeated as hidden repair")
				}
			} else if task.Stage != core.AwaitingDelivery {
				t.Fatalf("complete focused proof did not progress: %+v", task)
			}
		})
	}
}
func TestQualityChangeKeepsExistingApprovalPinned(t *testing.T) {
	e := fixture(t)
	task, err := e.Add(t.Context(), brief)
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.Show(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := e.Settings.Quality.Revision()
	next := e.Settings.Quality
	next.Definition = "new requirement"
	if err = e.Store.ApplyQuality(t.Context(), old, next); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Approve(t.Context(), task.ID, 1, view.Preview); err != nil {
		t.Fatal(err)
	}
	if current(t, e, task.ID).Current().Quality != old {
		t.Fatal("default changed existing task authority")
	}
	complete(t, e, claim(t, e))
	reopen(t, e)
	finish(t, e)
	if current(t, e, task.ID).Stage != core.Reviewing {
		t.Fatal("pinned old task could not progress")
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	if current(t, e, task.ID).Stage != core.AwaitingDelivery {
		t.Fatal("review lost pinned agreement after restart")
	}
}
