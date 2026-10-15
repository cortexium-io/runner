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

func TestConfirmedChildrenReleaseDependenciesAndCompletePlan(t *testing.T) {
	for _, scenario := range []string{"complete", "stop after first", "replaced child"} {
		t.Run(scenario, func(t *testing.T) { confirmedPlanJourney(t, scenario) })
	}
}

func confirmedPlanJourney(t *testing.T, scenario string) {
	e := fixture(t, func(c *config.Config) {
		c.Delivery.Mode = "github-draft"
		c.Delivery.Repository = "owner/repo"
		c.Policy.MaxBaseRefreshes = 2
	})
	q := e.Settings.Quality
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	q.RequiredCI = []core.CIRequirement{req}
	if err := e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), q); err != nil {
		t.Fatal(err)
	}
	e.Settings.Quality = q
	plan := approvedPlan(t, e, proposal())
	complete(t, e, claim(t, e))
	finish(t, e)
	view, err := e.Show(t.Context(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = e.Release(t.Context(), plan.ID, 1, view.ReleasePreview)
	if err != nil {
		t.Fatal(err)
	}
	repo := e.Settings.Config.Repository.Path
	e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) {
		return git(t, repo, "rev-parse", "HEAD"), nil
	})
	e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.Role == "implementer" {
			if err := os.WriteFile(filepath.Join(r.Dir, "change-"+r.Attempt.TaskID), []byte("approved change\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return pass(r)
	})
	for i, id := range plan.Release.Children {
		if i == 1 && scenario == "stop after first" {
			if err = e.Stop(t.Context(), plan.ID); err != nil {
				t.Fatal(err)
			}
			if err = e.Store.CompletePlans(t.Context()); err != nil {
				t.Fatal(err)
			}
			if current(t, e, plan.ID).Stage != core.Stopped || current(t, e, plan.Release.Children[0]).Stage != core.Completed || current(t, e, id).Stage != core.Stopped {
				t.Fatal("parent stop erased completion or retained child authority")
			}
			return
		}
		if i == 1 && scenario == "replaced child" {
			if _, err = e.Replace(t.Context(), id, 1, brief); err != nil {
				t.Fatal(err)
			}
			v, err := e.Show(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = e.Approve(t.Context(), id, 2, v.Preview); err != nil {
				t.Fatal(err)
			}
		}
		if i == 0 {
			v, err := e.Show(t.Context(), plan.Release.Children[1])
			if err != nil || !strings.Contains(v.Eligibility.WaitingFor, "awaits confirmed integration") {
				t.Fatal("dependent did not wait", v.Eligibility, err)
			}
		}
		// Implementation and independent audit. A dependent's old approval base may
		// require refresh plus audit, using the existing bounded path.
		for range 2 {
			a := claim(t, e)
			if a.TaskID != id {
				t.Fatal("wrong child admitted")
			}
			complete(t, e, a)
			finish(t, e)
		}
		p := deliveryClaim(t, e)
		p.State, p.Occupying, p.CreateRequested = "published", false, true
		p.Observation = &core.RemotePublication{Number: i + 1, URL: "https://github.com/owner/repo/pull/1", State: "OPEN", Draft: true, BranchCommit: p.Intent.Candidate.Commit}
		if err = e.savePublication(t.Context(), &p); err != nil {
			t.Fatal(err)
		}
		if err = e.VerifyBase(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		for {
			a := claim(t, e)
			complete(t, e, a)
			finish(t, e)
			if got := current(t, e, id); got.BaseProof != nil && got.BaseProof.Matches(got) {
				break
			}
		}
		task := current(t, e, id)
		// Confirm a real synchronization via the same delivery adapter when needed.
		remote := *p.Observation
		e.Delivery = deliveryAdapter{observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) { return remote, nil }, push: func(_ context.Context, intent core.PublicationIntent, auth func() error) error {
			if err := auth(); err != nil {
				return err
			}
			remote.BranchCommit = intent.Candidate.Commit
			return nil
		}}
		e.Checks = checksAdapter(func(_ context.Context, intent core.PublicationIntent) (core.CIObservation, error) {
			return core.CIObservation{Remote: remote, Base: task.BaseProof.Base, Commit: intent.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: "SUCCESS"}}}, nil
		})
		for {
			p = deliveryClaim(t, e)
			if err = e.publish(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			all, _ := e.Store.Publications(t.Context(), id)
			p = *latestPublication(all, task)
			if p.CI != nil && p.CI.State == "passed" {
				break
			}
		}
		o := mergedObservation(t, e, task, p)
		git(t, repo, "reset", "--hard", o.MergeCommit)
		e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) { return o, nil })
		if err = e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
			t.Fatal(err)
		}
		reopen(t, e)
		if err = e.Store.CompletePlans(t.Context()); err != nil {
			t.Fatal(err)
		}
		got := current(t, e, plan.ID)
		if (got.Stage == core.Completed) != (i == 1 && scenario == "complete") {
			t.Fatal("parent completion at wrong boundary", got)
		}
	}
	if err = e.Store.CompletePlans(t.Context()); err != nil {
		t.Fatal(err)
	}
	if scenario == "replaced child" && current(t, e, plan.ID).Stage == core.Completed {
		t.Fatal("replacement completed original release")
	}
	if err = e.Stop(t.Context(), plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range plan.Release.Children {
		if current(t, e, id).Stage != core.Completed {
			t.Fatal("completed child revoked")
		}
	}
}
