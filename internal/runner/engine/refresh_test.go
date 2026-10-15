package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
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
	for _, scenario := range []string{"failed check", "interrupted", "conflict", "revoked", "changed candidate"} {
		t.Run(scenario, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) { c.Policy.MaxBaseRefreshes = 1 })
			task := approved(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			complete(t, e, claim(t, e))
			finish(t, e)
			if scenario == "conflict" {
				moveBase(t, e, "greeting.txt", "incompatible\n")
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
			if scenario == "failed check" || scenario == "conflict" || scenario == "interrupted" {
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
				if scenario == "conflict" {
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
