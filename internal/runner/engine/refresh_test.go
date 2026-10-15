package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

type baseAdapter func(context.Context, string, string, string) (string, error)

func (f baseAdapter) CurrentBase(c context.Context, path, repo, branch string) (string, error) {
	return f(c, path, repo, branch)
}

func moveBase(t *testing.T, e *Engine, name, body string) string {
	t.Helper()
	repo := e.Settings.Config.Repository.Path
	if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", name)
	git(t, repo, "commit", "-m", "Move base")
	return git(t, repo, "rev-parse", "HEAD")
}

func TestCurrentBaseRefreshRechecksAcceptanceAndBoundsMovement(t *testing.T) {
	e, original := deliveryFixture(t)
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxBaseRefreshes = 2 })
	e.Bases = baseAdapter(func(ctx context.Context, _, repo, branch string) (string, error) {
		if repo != "owner/repo" || branch != "main" {
			t.Fatal("wrong delivery base")
		}
		_, base, err := workspace.Observe(ctx, e.Settings.Config)
		return base, err
	})
	p := deliveryClaim(t, e)
	p.State, p.Occupying, p.CreateRequested = "published", false, true
	p.Observation = &core.RemotePublication{Number: 7, URL: "https://github.com/owner/repo/pull/7", BranchCommit: original.Candidate.Commit, State: "OPEN", Draft: true}
	if err := e.savePublication(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	if err := e.prepareBaseChecks(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := claim(t, e)
	// Reconciliation while this deterministic reservation is active is harmless.
	if err := e.prepareBaseChecks(t.Context()); err != nil {
		t.Fatal(err)
	}
	complete(t, e, first)
	finish(t, e)
	task := current(t, e, original.ID)
	if task.BaseProof == nil || !task.BaseProof.Matches(task) || task.Usage != original.Usage {
		t.Fatal("unchanged base spent allowance or lost proof", task)
	}

	moveBase(t, e, "base.txt", "one\n")
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	if task.BaseProof != nil || len(task.Review) != 0 || task.NextRole != "audit" || task.Usage.BaseRefreshes != 1 || task.Approval.Base != original.Approval.Base {
		t.Fatal("stale acceptance survived", task)
	}
	if err := e.git().ValidateCandidate(t.Context(), original); err != nil {
		t.Fatal("old candidate changed", err)
	}
	if b, err := os.ReadFile(filepath.Join(task.Candidate.Workspace, "base.txt")); err != nil || string(b) != "one\n" {
		t.Fatal("base not integrated", err)
	}
	complete(t, e, claim(t, e))
	// Movement after the review began cannot qualify the first refreshed candidate.
	latest := moveBase(t, e, "base.txt", "two\n")
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	if task.BaseProof != nil || task.Usage.BaseRefreshes != 2 || task.Base() != latest || task.NextRole != "audit" {
		t.Fatal("movement during review missed", task)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	if task.BaseProof == nil || !task.BaseProof.Matches(task) || task.Usage.Invocations != 4 || task.Usage.Repairs != 0 || task.Usage.Escalations != 0 || task.Stage != core.AwaitingDelivery {
		t.Fatal("refresh qualification/counters", task)
	}
	retained := publication(t, e)
	if retained.Intent != p.Intent || retained.Observation.URL != p.Observation.URL || retained.Intent.Authorized(task) {
		t.Fatal("old draft identity changed or became fresh")
	}

	moveBase(t, e, "base.txt", "three\n")
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	f := e.execute(t.Context(), claim(t, e))
	if !strings.Contains(f.completion.Error, "limit reached") {
		t.Fatal("unbounded refresh", f)
	}
	if err := e.saveCompletion(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	task = current(t, e, task.ID)
	if task.Usage.BaseRefreshes != 2 || task.RefreshTarget != nil || task.BaseProof != nil {
		t.Fatal("exhaustion changed allowance or kept proof")
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxBaseRefreshes = 3 })
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	if current(t, e, task.ID).Usage.BaseRefreshes != 3 {
		t.Fatal("explicit allowance did not continue")
	}
}

func TestRefreshRecoveryRetainsCycleAndRejectsUnsafeResults(t *testing.T) {
	for _, scenario := range []string{"failed check", "interrupted", "unsupported conflict", "revoked", "changed candidate"} {
		t.Run(scenario, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) { c.Policy.MaxBaseRefreshes = 1 })
			task := approved(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			if scenario == "unsupported conflict" {
				moveBase(t, e, "greeting.txt", "binary\x00content\n")
			} else {
				moveBase(t, e, "base.txt", "new base\n")
			}
			if err := e.VerifyBase(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			passing := e.Checker
			if scenario == "failed check" {
				e.Checker = checkAdapter(func(_ context.Context, _ string, c config.QualityCheck, _ string, _ func(core.Process) error) core.CheckObservation {
					return core.CheckObservation{CheckID: c.ID, Status: "failed", Stopped: true, ExitCode: 1}
				})
			}
			f := e.execute(t.Context(), claim(t, e))
			if scenario == "failed check" || scenario == "unsupported conflict" || scenario == "interrupted" {
				if scenario == "interrupted" && f.completion.Error != "" {
					t.Fatal("refresh failed before simulated result loss", f.completion.Error)
				}
				if f.completion.Error == "" && scenario != "interrupted" {
					t.Fatal("unsafe refresh succeeded")
				}
				if scenario != "interrupted" {
					if err := e.saveCompletion(t.Context(), f); err != nil {
						t.Fatal(err)
					}
				}
				reopen(t, e)
				if scenario == "interrupted" {
					if err := e.recover(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				retained := current(t, e, task.ID)
				if retained.Usage.BaseRefreshes != 1 || retained.RefreshTarget == nil || retained.BaseProof != nil {
					t.Fatal("cycle reservation lost", retained)
				}
				if scenario == "unsupported conflict" {
					if retained.Attention == nil || retained.Attention.Kind != core.UnsupportedConflict || retained.ConflictRepair != nil || retained.Usage.Repairs != 0 {
						t.Fatal("unsupported conflict became a repair", retained)
					}
					view, err := e.Show(t.Context(), task.ID)
					if err != nil || strings.Contains(view.NextAction, "verify-base") || !strings.Contains(view.NextAction, "replace") {
						t.Fatal("unsupported conflict advised replay", view, err)
					}
					return
				}
				e.Checker = passing
				if err := e.VerifyBase(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
				complete(t, e, claim(t, e))
				finish(t, e)
				retained = current(t, e, task.ID)
				if retained.Usage.BaseRefreshes != 1 || retained.NextRole != "audit" || retained.Usage.Invocations != 2 {
					t.Fatal("recovery spent a second cycle or skipped review", retained)
				}
				return
			}
			if f.completion.Error != "" {
				t.Fatal(f.completion.Error)
			}
			if scenario == "revoked" {
				if err := e.Stop(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(filepath.Join(f.attempt.Candidate.Workspace, "greeting.txt"), []byte("tampered\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			retained := current(t, e, task.ID)
			if retained.BaseProof != nil || retained.WorkingBase != nil || retained.NextRole == "audit" {
				t.Fatal("revoked or changed result applied", retained)
			}
		})
	}
}

func TestConflictRepairRetainsSourceAndReusesBoundedJourney(t *testing.T) {
	e, source, published := confirmedDraft(t)
	now := time.Now().UTC()
	e.clock = func() time.Time { return now }
	moveBase(t, e, "base.txt", "keep new base behavior\n")
	target := moveBase(t, e, "greeting.txt", "incompatible\n")
	if err := e.VerifyBase(t.Context(), source.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	reopen(t, e)
	finish(t, e)
	task := current(t, e, source.ID)
	if task.ConflictRepair == nil || task.ConflictRepair.Target.Commit != target || task.Usage.BaseRefreshes != 1 || task.Usage.Repairs != 0 || task.WorkingBase != nil || *task.Candidate != *source.Candidate {
		t.Fatal("conflict lost exact source/target or spent repair before admission", task)
	}
	waiting(t, e, task.ID, "repair limit")
	v, err := e.Show(t.Context(), task.ID)
	if err != nil || !strings.Contains(v.Summary, "bounded repair") {
		t.Fatal("conflict status missing", v, err)
	}
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxRepairs, c.Policy.MaxTransientRetries = 1, 1
		c.Profiles["careful"] = core.Profile{Harness: "codex", Model: "stronger", Reasoning: "high", Timeout: "1m"}
		c.Policy.ImplementationLadder = []string{"careful"}
		n := task.Usage.Invocations
		c.Policy.MaxInvocationsPerTask = &n
	})
	waiting(t, e, task.ID, "invocation ceiling")
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxInvocationsPerTask = nil })
	repair := claim(t, e)
	if repair.Kind != "conflict_repair" || repair.Base != target || repair.Repair != 1 || repair.ProfileName != "build" || repair.WorkspaceClaim == source.Candidate.Workspace {
		t.Fatal("wrong repair reservation", repair)
	}
	if got := current(t, e, task.ID); got.RefreshTarget != nil || got.BaseProof != nil || len(got.Review) != 0 || got.Usage.Escalations != 0 {
		t.Fatal("stale acceptance or escalation", got)
	}
	firstCall := true
	e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.ConflictRepair == nil {
			t.Fatal("conflict context lost")
		}
		if r.Attempt.Role == "implementer" {
			b, err := os.ReadFile(filepath.Join(r.Dir, "greeting.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if firstCall {
				firstCall = false
				if !strings.Contains(string(b), "<<<<<<<") || !strings.Contains(string(b), "incompatible") || !strings.Contains(string(b), "hello") {
					t.Fatal("conflict sides missing", string(b))
				}
				if err = os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("partial resolution\n"), 0600); err != nil {
					t.Fatal(err)
				}
				return execution.Completion{Stopped: true, Kind: core.Transient, Error: "provider unavailable after partial edit"}
			}
			if string(b) != "partial resolution\n" {
				t.Fatal("retry reset partial work", string(b))
			}
			if err = os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return pass(r)
	})
	if err = e.saveCompletion(t.Context(), e.execute(t.Context(), repair)); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	now = now.Add(time.Minute)
	retry := claim(t, e)
	if retry.Repair != 1 || retry.Retry != 1 || retry.ProfileName != repair.ProfileName {
		t.Fatal("retry changed pass/profile", retry)
	}
	complete(t, e, retry)
	// Crash after native construction but before the final task/result commit.
	attempts, _ := e.Store.Attempts(t.Context(), task.ID)
	saved := attempts[len(attempts)-1]
	task = current(t, e, task.ID)
	if err = e.advance(t.Context(), &task, &saved); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	finish(t, e)
	task = current(t, e, task.ID)
	if task.NextRole != "audit" || task.Attention != nil {
		t.Fatal("constructed conflict not recovered", task)
	}
	if err = e.git().ValidateCandidate(t.Context(), source); err != nil {
		t.Fatal("original changed", err)
	}
	if parents := git(t, task.Candidate.Workspace, "show", "-s", "--format=%P", "HEAD"); parents != source.Candidate.Commit+" "+target {
		t.Fatal("resolution lost ancestry", parents)
	}
	b, err := os.ReadFile(filepath.Join(task.Candidate.Workspace, "base.txt"))
	if err != nil || string(b) != "keep new base behavior\n" {
		t.Fatal("resolution dropped clean base change", err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	if current(t, e, task.ID).NextRole != "refresh" {
		t.Fatal("repair bypassed current-base check")
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	if task.BaseProof == nil || !task.BaseProof.Matches(task) || task.Usage.Repairs != 1 || task.Usage.Escalations != 0 || task.Usage.BaseRefreshes != 1 {
		t.Fatal("repair lost proof or bounds", task)
	}
	remote := *published.Observation
	e.Delivery = deliveryAdapter{
		observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) { return remote, nil },
		push: func(_ context.Context, p core.PublicationIntent, authorize func() error) error {
			if p.Sync == nil || p.Sync.PreviousCommit != source.Candidate.Commit || p.Sync.Number != remote.Number || p.Branch != published.Intent.Branch {
				t.Fatal("repair lost same draft lease")
			}
			if err := authorize(); err != nil {
				return err
			}
			remote.BranchCommit = p.Candidate.Commit
			return nil
		},
		create: func(context.Context, core.PublicationIntent, func() error) error {
			t.Fatal("repair created another draft")
			return nil
		},
	}
	if err = e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if err = e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	} // fresh CI observation (unconfigured fixture)
	if current(t, e, task.ID).Stage != core.AwaitingDelivery {
		t.Fatal("repair completed task before merge")
	}
	moveBase(t, e, "greeting.txt", "another base change\n")
	if err = e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	waiting(t, e, task.ID, "repair limit")
	if got := current(t, e, task.ID); got.Usage.BaseRefreshes != 2 || got.Usage.Repairs != 1 {
		t.Fatal("second conflict reset allowances", got)
	}
	if err = e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.claim(t.Context()); ok || err != nil {
		t.Fatal("stopped conflict admitted repair", err)
	}
}

func TestConflictRepairRejectsUnresolvedWorkAndPreservesDecisions(t *testing.T) {
	for _, scenario := range []string{"markers", "decision", "partial initialization", "revoked result"} {
		t.Run(scenario, func(t *testing.T) {
			e, source, _ := confirmedDraft(t)
			applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 1 })
			moveBase(t, e, "greeting.txt", "incompatible\n")
			if err := e.VerifyBase(t.Context(), source.ID); err != nil {
				t.Fatal(err)
			}
			complete(t, e, claim(t, e))
			finish(t, e)
			repair := claim(t, e)
			task := current(t, e, source.ID)
			calls, checks := 0, 0
			e.Checker = checkAdapter(func(context.Context, string, config.QualityCheck, string, func(core.Process) error) core.CheckObservation {
				checks++
				t.Fatal("unresolved input reached local checks")
				return core.CheckObservation{}
			})
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				calls++
				result := pass(r)
				if scenario == "decision" || scenario == "revoked result" {
					result.Result.Outcome, result.Result.Summary = "needs_input", "Which greeting does the new base require?"
				}
				return result
			})
			if scenario == "partial initialization" {
				m, err := e.git().Prepare(t.Context(), task)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(filepath.Join(m.WorktreePath, "greeting.txt"), []byte("unknown partial input\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			f := e.execute(t.Context(), repair)
			if scenario == "revoked result" {
				if err := e.Stop(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			reopen(t, e)
			finish(t, e)
			task = current(t, e, task.ID)
			if task.NextRole != "" || task.BaseProof != nil || *task.Candidate != *source.Candidate || task.Usage.Repairs != 1 || checks != 0 {
				t.Fatal("unsafe resolution progressed", task)
			}
			if scenario == "decision" && (task.Attention == nil || task.Attention.Kind != core.Decision || !strings.Contains(task.Attention.Detail, "Which greeting")) {
				t.Fatal("decision was hidden by failing checks", task)
			}
			if scenario == "partial initialization" && (calls != 0 || task.Attention == nil || task.Attention.Kind != core.Integrity) {
				t.Fatal("partial seed launched or reset", task)
			}
			if scenario == "markers" && (task.Attention == nil || !strings.Contains(task.Attention.Detail, "unresolved conflict")) {
				t.Fatal("markers were accepted", task)
			}
			if scenario == "revoked result" && task.Stage != core.Stopped {
				t.Fatal("revoked result applied")
			}
			if err := e.git().ValidateCandidate(t.Context(), source); err != nil {
				t.Fatal("retained source changed", err)
			}
		})
	}
}
