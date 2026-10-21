package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
)

type integrationAdapter func(context.Context, core.Publication) (core.IntegrationObservation, error)

func (f integrationAdapter) ObserveIntegration(ctx context.Context, p core.Publication) (core.IntegrationObservation, error) {
	return f(ctx, p)
}

// Extend the existing real-Git delivery fixture through base verification and CI.
func integrationReady(t *testing.T) (*Engine, core.Task, core.Publication) {
	t.Helper()
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	e, task, p := confirmedDraft(t, req)
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	e.Checks = checksAdapter(func(context.Context, core.PublicationIntent) (core.CIObservation, error) {
		return core.CIObservation{Remote: *p.Observation, Base: task.Base(), Commit: task.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: "SUCCESS"}}}, nil
	})
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	return e, task, publication(t, e)
}

func mergedObservation(t *testing.T, e *Engine, task core.Task, p core.Publication) core.IntegrationObservation {
	t.Helper()
	merge := git(t, e.Settings.Config.Repository.Path, "commit-tree", task.Candidate.Tree, "-p", task.BaseProof.Base, "-p", task.Candidate.Commit, "-m", "human merge")
	remote := *p.Observation
	remote.State, remote.Draft = "MERGED", false
	return core.IntegrationObservation{Remote: remote, MergeCommit: merge, MergedAt: time.Now().UTC(), Checks: p.CI.Observation.Checks}
}

func TestIntegrationRequiresExactMergeAndFreshCI(t *testing.T) {
	e, task, p := integrationReady(t)
	original := mergedObservation(t, e, task, p)
	repo := e.Settings.Config.Repository.Path
	goodTip := git(t, repo, "commit-tree", task.Candidate.Tree, "-p", original.MergeCommit, "-m", "later base commit")
	// The merge need not be the latest commit, but must be in that base's history.
	tip := goodTip
	e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) { return tip, nil })
	observation := original
	var readErr error
	calls := 0
	e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) {
		calls++
		return observation, readErr
	})
	before := task.Usage
	for i, scenario := range []string{"open draft", "open ready", "base moved", "closed", "outage", "wrong PR", "failed CI", "changed tree", "changed base", "squash", "not in base", "confirmed"} {
		t.Run(scenario, func(t *testing.T) {
			observation, tip, readErr = original, goodTip, nil
			want := "unavailable"
			switch scenario {
			case "open draft", "open ready":
				observation.Remote.State, observation.Remote.Draft, observation.Base = "OPEN", scenario == "open draft", task.BaseProof.Base
				want = "open"
			case "base moved":
				observation.Remote.State, observation.Base = "OPEN", goodTip
				want = "stale"
			case "closed":
				observation.Remote.State = "CLOSED"
				want = "closed"
			case "outage":
				readErr = errors.New("host unavailable")
			case "wrong PR":
				observation.Remote.Number++
			case "failed CI":
				observation.Checks = append([]core.RemoteCheck(nil), original.Checks...)
				observation.Checks[0].Conclusion = "FAILURE"
			case "changed tree":
				tree := git(t, repo, "rev-parse", task.BaseProof.Base+"^{tree}")
				observation.MergeCommit = git(t, repo, "commit-tree", tree, "-p", task.BaseProof.Base, "-p", task.Candidate.Commit, "-m", "changed merge")
				tip = observation.MergeCommit
			case "changed base":
				observation.MergeCommit = git(t, repo, "commit-tree", task.Candidate.Tree, "-p", goodTip, "-p", task.Candidate.Commit, "-m", "new base merge")
				tip = observation.MergeCommit
			case "squash":
				observation.MergeCommit = task.Candidate.Commit
			case "not in base":
				tip = task.BaseProof.Base
			case "confirmed":
				want = "confirmed"
			}
			if i > 0 {
				if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			claimed := deliveryClaim(t, e)
			if claimed.IntegrationRequest == nil {
				t.Fatal("did not reserve integration")
			}
			if i == 0 {
				reopen(t, e)
				claimed = deliveryClaim(t, e)
			} // lost read safely repeats
			if err := e.publish(t.Context(), claimed); err != nil {
				t.Fatal(err)
			}
			got := publication(t, e)
			now := current(t, e, task.ID)
			if got.Integration == nil || got.Integration.State != want || got.Occupying || got.IntegrationRequest != nil || now.Usage != before {
				t.Fatal("incorrect assessment or ownership", got, now)
			}
			if (now.Stage == core.Completed) != (want == "confirmed") || (now.Integration != nil) != (want == "confirmed") {
				t.Fatal("incorrect completion", now)
			}
			view, err := e.Show(t.Context(), task.ID)
			if err != nil || !strings.Contains(view.Summary, want) {
				t.Fatal("missing operator outcome", view.Summary, err)
			}
			if err = e.preparePublications(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.claimPublication(t.Context()); ok || err != nil {
				t.Fatal("inspection polled", err)
			}
		})
	}
	if calls != 12 {
		t.Fatal("unexpected reads", calls)
	}
	reopen(t, e)
	completed := current(t, e, task.ID)
	attempts, err := e.Store.Attempts(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range attempts {
		if a.Authorized(completed) {
			t.Fatal("completed task retained execution authority", a.ID)
		}
	}
	if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Replace(t.Context(), task.ID, 1, brief); err == nil {
		t.Fatal("completed task replaced")
	}
	if core.Digest(current(t, e, task.ID)) != core.Digest(completed) {
		t.Fatal("completed evidence changed")
	}
	// Replaying a committed receipt neither duplicates completion nor revives work.
	receipt := publication(t, e)
	if err := e.savePublication(t.Context(), &receipt); err != nil {
		t.Fatal(err)
	}
	if current(t, e, task.ID).Revision != completed.Revision {
		t.Fatal("receipt applied twice")
	}
}

func TestIntegrationSerializesWithoutBlockingLocalWorkAndStopWins(t *testing.T) {
	e, task, p := integrationReady(t)
	claimed := deliveryClaim(t, e)
	// An independent task with the same accepted evidence is sufficient to exercise
	// the transactional repository/base reservation; no second model journey needed.
	other := task
	other.ID, other.Revision = core.NewID(), 0
	if err := e.Store.Add(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	second := p
	second.Intent.ID, second.Intent.TaskID, second.Revision = core.NewID(), other.ID, 0
	second.IntegrationRequest = nil
	if err := e.savePublication(t.Context(), &second); err != nil {
		t.Fatal(err)
	}
	if err := e.InspectIntegration(t.Context(), other.ID); err != nil {
		t.Fatal(err)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxParallelism = 2 })
	if _, ok, err := e.claimPublication(t.Context()); ok || err != nil {
		t.Fatal("same base admitted concurrently", err)
	}
	localTask := approved(t, e)
	// Restart recovers the lost read and then the actual coordinator proves that a
	// slow host read does not stall an unrelated implementation.
	reopen(t, e)
	started, release, local := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	e.Integrations = integrationAdapter(func(ctx context.Context, _ core.Publication) (core.IntegrationObservation, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return core.IntegrationObservation{}, errors.New("read interrupted")
	})
	previous := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, start func(core.Process) error) execution.Completion {
		select {
		case local <- struct{}{}:
		default:
		}
		return previous.Execute(ctx, r, start)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("integration not resumed")
	}
	select {
	case <-local:
	case <-ctx.Done():
		t.Fatal("integration blocked implementation")
	}
	if err := e.Store.SetDrained(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not settle")
	}
	all, err := e.Store.Publications(t.Context(), task.ID)
	if err != nil || len(all) != 1 || all[0].Intent.ID != claimed.Intent.ID || all[0].Integration.State != "stale" || all[0].Occupying {
		t.Fatal("stop lost ownership/evidence", all, err)
	}
	if current(t, e, task.ID).Integration != nil || current(t, e, localTask.ID).Usage.Invocations == 0 {
		t.Fatal("wrong task progress")
	}
	if _, ok, err := e.claimPublication(t.Context()); ok || err != nil {
		t.Fatal("drained integration admitted", err)
	}
	if err := e.Store.SetDrained(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if next, ok, err := e.claimPublication(t.Context()); !ok || err != nil || next.Intent.ID != second.Intent.ID {
		t.Fatal("reservation not released", next, err)
	}
}

func TestIntegrationCompletionRechecksAuthorityInStore(t *testing.T) {
	e, task, p := integrationReady(t)
	o := mergedObservation(t, e, task, p)
	claimed := deliveryClaim(t, e)
	result := core.IntegrationAssessment{Request: *claimed.IntegrationRequest, State: "confirmed", ObservedAt: time.Now().UTC(), BaseTip: o.MergeCommit, Observation: &o}
	if _, err := e.Replace(t.Context(), task.ID, 1, brief); err != nil {
		t.Fatal(err)
	}
	claimed.Integration, claimed.IntegrationRequest, claimed.Occupying = &result, nil, false
	if err := e.savePublication(t.Context(), &claimed); err != nil {
		t.Fatal(err)
	}
	if got := current(t, e, task.ID); got.Integration != nil || got.Stage == core.Completed {
		t.Fatal("replacement gained completion")
	}
	if publication(t, e).Integration.State != "stale" {
		t.Fatal("late receipt not retained as stale")
	}
	// The queued request also cannot reclaim a changed task.
	p.IntegrationRequest = &result.Request
	if err := e.Store.SavePublication(t.Context(), p, true); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale request admitted", err)
	}
}

// driveLocal performs coordinator reconciliation without timing-based waits.
func driveLocal(t *testing.T, e *Engine, id string, until func(core.Task) bool) {
	t.Helper()
	if err := e.Store.SetDrained(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	for range 70 {
		finish(t, e)
		if err := e.prepareCompletions(t.Context()); err != nil {
			t.Fatal(err)
		}
		now := current(t, e, id)
		if until(now) {
			return
		}
		a, ok, err := e.claim(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			if err := e.saveCompletion(t.Context(), e.execute(t.Context(), a)); err != nil {
				t.Fatal(err)
			}
		}
	}
	tasks, _ := e.Store.Tasks(t.Context())
	t.Fatalf("journey did not reach boundary: %+v", tasks)
}
func releasePlan(t *testing.T, e *Engine, p core.PlanningResult) core.Task {
	t.Helper()
	plan := approvedPlan(t, e, p)
	complete(t, e, claim(t, e))
	finish(t, e)
	v, err := e.Show(t.Context(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = e.Release(t.Context(), plan.ID, 1, v.ReleasePreview)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func TestLocalReadyDestinationsPreserveCheckout(t *testing.T) {
	for _, mode := range []string{"result", "human", "automatic", "checked out", "manual"} {
		t.Run(mode, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) {
				if mode != "result" {
					c.Delivery.LocalTarget = "base"
				}
				if mode == "automatic" {
					c.Policy.Merge = "automatic"
				}
			})
			repo := e.Settings.Config.Repository.Path
			base := git(t, repo, "rev-parse", "HEAD")
			if mode != "result" && mode != "checked out" {
				git(t, repo, "switch", "-c", "operator")
			}
			if err := os.WriteFile(filepath.Join(repo, "greeting.txt"), []byte("operator changes\n"), 0600); err != nil {
				t.Fatal(err)
			}
			task := approved(t, e)
			driveLocal(t, e, task.ID, func(t core.Task) bool { return t.Stage == core.Completed || currentResult(t) != nil })
			now := current(t, e, task.ID)
			if mode != "result" && mode != "automatic" {
				if now.Stage == core.Completed {
					t.Fatal("completed without permission")
				}
				if mode == "manual" {
					git(t, repo, "update-ref", "refs/heads/main", now.Candidate.Commit, base)
					if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
						t.Fatal(err)
					}
				} else if err := e.RequestMerge(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
			}
			driveLocal(t, e, task.ID, func(t core.Task) bool { return t.Stage == core.Completed || t.Attention != nil })
			now = current(t, e, task.ID)
			if mode == "checked out" {
				if now.Stage == core.Completed || now.Attention == nil || !strings.Contains(now.Attention.Detail, "checked out") {
					t.Fatal(now)
				}
				git(t, repo, "switch", "-c", "operator")
				if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
				driveLocal(t, e, task.ID, func(t core.Task) bool { return t.Stage == core.Completed })
				now = current(t, e, task.ID)
			}
			if now.Completion == nil || now.Usage.Invocations != 2 {
				t.Fatal("missing receipt or redundant model call", now)
			}
			wantBase := now.Candidate.Commit
			if mode == "result" {
				wantBase = base
			}
			if git(t, repo, "rev-parse", "main") != wantBase {
				t.Fatal("wrong target")
			}
			bytes, err := os.ReadFile(filepath.Join(repo, "greeting.txt"))
			if err != nil || string(bytes) != "operator changes\n" {
				t.Fatal("operator checkout changed", err)
			}
			reopen(t, e)
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			if current(t, e, task.ID).Revision != now.Revision {
				t.Fatal("completion replayed")
			}
		})
	}
}
func TestCombinedPlanVerifiesOutcomeAndDeliversOnlyParent(t *testing.T) {
	for _, scenario := range []string{"local", "repair", "base moved", "missing capability", "failed check", "proof gap", "github", "stopped member", "artifacts"} {
		t.Run(scenario, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) {
				c.Policy.MaxBaseRefreshes = 3
				c.Policy.MaxRepairs = 1
				if scenario == "github" {
					c.Delivery.Mode = "github-draft"
					c.Delivery.Repository = "owner/repo"
				}
			})
			q := e.Settings.Quality
			if scenario == "github" {
				q.RequiredCI = []core.CIRequirement{{Kind: "check", Name: "test", AppID: 15368}}
			}
			q.Checks = append([]config.QualityCheck(nil), q.Checks...)
			q.Checks[0].At = "task"
			q.Checks = append(q.Checks, config.QualityCheck{ID: "combined", At: "completion", Command: "test -f usage.txt", Directory: ".", Prerequisites: []string{"sh"}, Timeout: "1m"})
			if scenario == "artifacts" {
				for i := range q.Checks {
					q.Checks[i].Artifacts = []string{".runner-output/reports"}
				}
			}
			if err := e.Store.ApplyQuality(t.Context(), q.Revision(), q); err == nil {
				t.Fatal("stale agreement applied")
			}
			if err := e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), q); err != nil {
				t.Fatal(err)
			}
			e.Settings.Quality = q
			plan := releasePlan(t, e, proposal())
			if scenario == "github" {
				e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) {
					return git(t, e.Settings.Config.Repository.Path, "rev-parse", "main"), nil
				})
			}
			checks := map[string]int{}
			capabilityAvailable := false
			e.Checker = checkAdapter(func(_ context.Context, dir string, c config.QualityCheck, evidence string, _ func(core.Process) error) core.CheckObservation {
				checks[c.ID]++
				if scenario == "artifacts" {
					return artifactCheck(t, dir, c, evidence)
				}
				status := "passed"
				detail := ""
				if scenario == "missing capability" && !capabilityAvailable && c.ID == "combined" {
					status = "unavailable"
					detail = "missing required tool"
				}
				exit := 0
				if scenario == "failed check" && c.ID == "combined" {
					status = "failed"
					exit = 1
				}
				return core.CheckObservation{CheckID: c.ID, Status: status, ExitCode: exit, Stopped: true, Detail: detail, OutputDigest: "proof"}
			})
			audits := 0
			focused := 0
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				if r.Plan == nil {
					t.Fatal("missing shared context")
				}
				if r.Attempt.Role == "implementer" {
					if r.Task.ParentID == "" {
						if err := os.WriteFile(filepath.Join(r.Dir, "usage.txt"), []byte("hello combined\n"), 0600); err != nil {
							t.Fatal(err)
						}
					} else if r.Task.PlanKey == "greeting" {
						if err := os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello\n"), 0600); err != nil {
							t.Fatal(err)
						}
					} else {
						data, err := os.ReadFile(filepath.Join(r.Dir, "greeting.txt"))
						if err != nil || string(data) != "hello\n" {
							t.Fatal("dependent did not see included source", err)
						}
						value := "hello combined\n"
						if scenario == "repair" {
							value = "missing combined behavior\n"
						}
						if err := os.WriteFile(filepath.Join(r.Dir, "usage.txt"), []byte(value), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				result := pass(r)
				if r.Task.ID == plan.ID && r.Attempt.Role == "verify" {
					focused++
					if len(r.Proofs) != 1 {
						t.Fatal("unfocused combined verification")
					}
				}
				if r.Task.ID == plan.ID && r.Attempt.Role == "audit" {
					audits++
					if scenario == "artifacts" {
						proof := append([]core.CheckObservation(nil), r.QualityEvidence...)
						for _, child := range r.Plan.Evidence {
							proof = append(proof, child.Checks...)
						}
						if len(proof) != 3 {
							t.Fatal("combined and child artifact evidence missing", proof)
						}
						for _, check := range proof {
							if check.Artifacts == nil || !strings.Contains(strings.Join(r.ReadRoots, "\n"), check.Artifacts.Directory) {
								t.Fatal("combined review lacks retained artifact grant", r.ReadRoots)
							}
						}
					}
					if scenario == "proof gap" {
						result.Result.Outcome = "blocked"
						result.Result.Checks[1].Status = "gap"
						result.Result.Checks[1].Evidence = "Run the released exact-output example check on this candidate"
					}
					if len(r.Plan.Evidence) == 0 {
						t.Fatal("combined audit lacks labelled child evidence")
					}
					if scenario == "repair" && audits == 1 {
						result.Result.Checks[0].Status = "defect"
						result.Result.Checks[0].Evidence = "usage.txt omits the combined greeting required by the released scope"
					}
				}
				return result
			})
			// Inclusion, not mere child review, releases the dependent.
			driveLocal(t, e, plan.Release.Children[0], func(t core.Task) bool { return t.Inclusion != nil })
			first := current(t, e, plan.Release.Children[0])
			if first.Stage != core.AwaitingDelivery || first.Completion != nil {
				t.Fatal("child delivered prematurely")
			}
			if err := e.RequestMerge(t.Context(), first.ID); err == nil {
				t.Fatal("child merge accepted")
			}
			if _, err := e.Replace(t.Context(), first.ID, 1, brief); err == nil {
				t.Fatal("included child replace accepted")
			}
			if scenario == "stopped member" {
				if err := e.Stop(t.Context(), plan.Release.Children[1]); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if err := e.prepareCompletions(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				if current(t, e, plan.ID).Stage == core.Completed {
					t.Fatal("stopped member completed parent")
				}
				return
			}
			if scenario == "base moved" {
				git(t, e.Settings.Config.Repository.Path, "commit", "--allow-empty", "-m", "new destination base")
			}
			reopen(t, e)
			driveLocal(t, e, plan.ID, func(t core.Task) bool {
				return t.Stage == core.Completed || t.Attention != nil || scenario == "github" && t.Stage == core.AwaitingDelivery && t.NextRole == ""
			})
			parent := current(t, e, plan.ID)
			if scenario == "failed check" {
				if parent.Attention == nil || parent.Stage == core.Completed || audits != 1 || parent.Usage.Repairs != 0 {
					t.Fatal("passing prose overrode failed required check", parent)
				}
				return
			}
			if scenario == "missing capability" {
				if parent.Attention == nil || parent.Usage.Repairs != 0 || audits != 0 {
					t.Fatal(parent)
				}
				before := parent.Usage.Invocations
				capabilityAvailable = true
				if _, err := e.Resume(t.Context(), parent.ID, parent.Attention.Attempt, false); err != nil {
					t.Fatal(err)
				}
				driveLocal(t, e, parent.ID, func(t core.Task) bool { return t.Stage == core.Completed })
				parent = current(t, e, parent.ID)
				if parent.Usage.Invocations != before+1 || parent.Usage.Repairs != 0 || parent.Usage.Escalations != 0 || audits != 1 || checks["combined"] != 2 {
					t.Fatal("restored capability repeated model work or consumed repair", parent.Usage, checks, audits)
				}
				return
			}
			if checks["combined"] != 1 && scenario != "repair" {
				t.Fatal("completion check repeated per child", checks)
			}
			if scenario == "base moved" && (parent.Usage.BaseRefreshes != 1 || audits != 1 || checks["greeting"] != 3 || checks["combined"] != 1) {
				t.Fatal("final proof ran before refresh or missed child checks", parent.Usage, checks, audits)
			}
			if scenario == "repair" && (parent.Usage.Repairs != 1 || audits != 2 || checks["greeting"] != 3 || checks["combined"] != 2) {
				t.Fatal("incorrect combined repair allowance/checks", parent.Usage, checks, audits)
			}
			if scenario == "github" {
				repo := e.Settings.Config.Repository.Path
				e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) {
					return git(t, repo, "rev-parse", "main"), nil
				})
				p := deliveryClaim(t, e)
				if p.Intent.TaskID != plan.ID {
					t.Fatal("published child")
				}
				p.State, p.Occupying, p.CreateRequested = "published", false, true
				p.Observation = &core.RemotePublication{Number: 1, URL: "https://github.com/owner/repo/pull/1", State: "OPEN", Draft: true, BranchCommit: p.Intent.Candidate.Commit}
				if err := e.savePublication(t.Context(), &p); err != nil {
					t.Fatal(err)
				}
				if err := e.VerifyBase(t.Context(), plan.ID); err != nil {
					t.Fatal(err)
				}
				complete(t, e, claim(t, e))
				finish(t, e)
				parent = current(t, e, plan.ID)
				e.Checks = checksAdapter(func(context.Context, core.PublicationIntent) (core.CIObservation, error) {
					return core.CIObservation{Remote: *p.Observation, Base: parent.Base(), Commit: parent.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: q.RequiredCI[0], Status: "COMPLETED", Conclusion: "SUCCESS"}}}, nil
				})
				if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
					t.Fatal(err)
				}
				pubs, _ := e.Store.Publications(t.Context(), plan.ID)
				p = *latestPublication(pubs, parent)
				o := mergedObservation(t, e, parent, p)
				git(t, repo, "reset", "--hard", o.MergeCommit)
				e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) { return o, nil })
				if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
					t.Fatal(err)
				}
				all, _ := e.Store.Publications(t.Context(), "")
				if len(all) != 1 {
					t.Fatal("Plan published more than one PR", all)
				}
				parent = current(t, e, plan.ID)
			}
			if scenario == "proof gap" && (focused != 1 || parent.Usage.Repairs != 0) {
				t.Fatal("gap repaired source or repeated review", focused, parent.Usage)
			}
			if parent.Stage != core.Completed || parent.Completion == nil || audits == 0 {
				t.Fatal("missing final outcome proof", parent)
			}
			for _, id := range plan.Release.Children {
				child := current(t, e, id)
				if child.Stage != core.Completed || child.Completion == nil || child.Completion.ParentID != plan.ID || child.Inclusion == nil || child.Integration != nil {
					t.Fatal("child completion lost parent binding", child)
				}
			}
		})
	}
}

func TestLocalRefRecoveryDoesNotRepeatOrForgeDelivery(t *testing.T) {
	for _, scenario := range []string{"prepared", "applied", "not sent", "changed", "preexisting", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			e := fixture(t)
			task := approved(t, e)
			for range 2 {
				complete(t, e, claim(t, e))
				finish(t, e)
			}
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			complete(t, e, claim(t, e))
			finish(t, e)
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			task = current(t, e, task.ID)
			w := task.RefWrites[0]
			if err := e.VerifyBase(t.Context(), task.ID); err == nil {
				t.Fatal("pending ref write lost its proof through manual refresh")
			}
			if w.State != "prepared" {
				t.Fatal(w)
			}
			repo := e.Settings.Config.Repository.Path
			if scenario == "preexisting" {
				git(t, repo, "update-ref", w.Ref, w.Commit)
			} else if scenario != "prepared" {
				w.State = "dispatched"
				if err := e.Store.SaveRefWrite(t.Context(), task.Revision, w); err != nil {
					t.Fatal(err)
				}
				if scenario != "not sent" {
					if err := e.git().ApplyRef(t.Context(), w); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "changed" {
					git(t, repo, "update-ref", w.Ref, task.Base(), w.Commit)
				}
				if scenario == "stopped" {
					if err := e.Stop(t.Context(), task.ID); err != nil {
						t.Fatal(err)
					}
				}
			}
			reopen(t, e)
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			got := current(t, e, task.ID)
			success := scenario == "prepared" || scenario == "applied"
			if (got.Stage == core.Completed) != success || (got.Completion != nil) != success {
				t.Fatalf("%s: %s %+v", scenario, got.Stage, got.RefWrites)
			}
			if got.Usage.Invocations != 2 {
				t.Fatal("recovery spent model allowance")
			}
			if scenario == "not sent" {
				if _, err := e.git().Ref(t.Context(), w.Ref); err == nil {
					t.Fatal("uncertain write repeated")
				}
			}
			if scenario == "stopped" && got.Stage != core.Stopped {
				t.Fatal("late effect revived stop")
			}
		})
	}
}

func TestPlanConcurrentChildrenRefreshBeforeInclusion(t *testing.T) {
	e := fixture(t, func(c *config.Config) { c.Policy.MaxParallelism = 2; c.Policy.MaxBaseRefreshes = 3 })
	p := proposal()
	p.Cards = append(p.Cards, core.PlannedCard{Key: "independent", Body: brief})
	plan := releasePlan(t, e, p)
	e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.Role == "implementer" {
			if r.Task.PlanKey == "usage" {
				if b, err := os.ReadFile(filepath.Join(r.Dir, "greeting.done")); err != nil || string(b) != "included\n" {
					t.Fatal("dependent base lacks prerequisite", err)
				}
			}
			if err := os.WriteFile(filepath.Join(r.Dir, r.Task.PlanKey+".done"), []byte("included\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return pass(r)
	})
	for range 2 {
		if err := e.prepareCompletions(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	first, independent := claim(t, e), claim(t, e)
	if first.TaskID != plan.Release.Children[0] || independent.TaskID != plan.Release.Children[2] || first.Base != independent.Base {
		t.Fatal("independent work serialized or wrong base")
	}
	// Keep the independent workspace claim while the first child is included.
	complete(t, e, first)
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	for range 2 {
		if err := e.prepareCompletions(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	dependent := claim(t, e)
	if dependent.TaskID != plan.Release.Children[1] || dependent.Base == first.Base {
		t.Fatal("dependent did not bind admitted Plan head")
	}
	complete(t, e, dependent)
	finish(t, e)
	complete(t, e, independent)
	finish(t, e)
	driveLocal(t, e, plan.ID, func(t core.Task) bool { return t.Stage == core.Completed || t.Attention != nil })
	parent := current(t, e, plan.ID)
	if parent.Stage != core.Completed {
		t.Fatal(parent.Attention)
	}
	if current(t, e, independent.TaskID).Usage.BaseRefreshes == 0 {
		t.Fatal("stale sibling included without refresh")
	}
	for _, name := range []string{"greeting", "usage", "independent"} {
		if git(t, e.Settings.Config.Repository.Path, "show", parent.Candidate.Commit+":"+name+".done") != "included" {
			t.Fatal("sibling lost", name)
		}
	}
}

func TestLocalAutomaticPolicyFencesUnsentMerge(t *testing.T) {
	for _, scenario := range []string{"unsent", "applied"} {
		t.Run(scenario, func(t *testing.T) {
			e := fixture(t, func(c *config.Config) { c.Delivery.LocalTarget = "base"; c.Policy.Merge = "automatic" })
			repo := e.Settings.Config.Repository.Path
			git(t, repo, "switch", "-c", "operator")
			task := approved(t, e)
			base := task.Base()
			driveLocal(t, e, task.ID, func(t core.Task) bool { return currentResult(t) != nil })
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			now := current(t, e, task.ID)
			last := now.RefWrites[len(now.RefWrites)-1]
			if last.Kind != "base" || last.State != "prepared" || last.AutomaticPolicy == "" {
				t.Fatal(last)
			}
			if scenario == "applied" {
				last.State = "dispatched"
				if err := e.Store.SaveRefWrite(t.Context(), now.Revision, last); err != nil {
					t.Fatal(err)
				}
				if err := e.git().ApplyRef(t.Context(), last); err != nil {
					t.Fatal(err)
				}
			}
			applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
			reopen(t, e)
			if err := e.prepareCompletions(t.Context()); err != nil {
				t.Fatal(err)
			}
			now = current(t, e, task.ID)
			if scenario == "applied" {
				if now.Stage != core.Completed || now.Completion == nil || now.Completion.WriteID != last.ID || git(t, repo, "rev-parse", "main") != last.Commit {
					t.Fatal("disabled policy lost confirmation of an already applied write", now)
				}
				return
			}
			if now.Stage == core.Completed || git(t, repo, "rev-parse", "main") != base {
				t.Fatal("disabled automatic policy wrote target")
			}
			if err := e.RequestMerge(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			driveLocal(t, e, task.ID, func(t core.Task) bool { return t.Stage == core.Completed })
			now = current(t, e, task.ID)
			if now.Completion == nil || now.RefWrites[len(now.RefWrites)-1].AutomaticPolicy != "" {
				t.Fatal("explicit permission confused with policy", now)
			}
		})
	}
}
