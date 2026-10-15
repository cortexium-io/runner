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
)

type checksAdapter func(context.Context, core.PublicationIntent) (core.CIObservation, error)

func (f checksAdapter) ObserveCI(c context.Context, p core.PublicationIntent) (core.CIObservation, error) {
	return f(c, p)
}

func TestCIAssessmentPinsEvidenceAndRequiresExplicitReassessment(t *testing.T) {
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	e, task, first := confirmedDraft(t, req)
	if err := e.AssessCI(t.Context(), task.ID); err == nil {
		t.Fatal("unverified base assessed")
	}
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	before := task.Usage
	// A new default cannot weaken this task's required hosted checks.
	next := e.Settings.Quality
	next.RequiredCI = nil
	if err := e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), next); err != nil {
		t.Fatal(err)
	}
	observed := core.CIObservation{Remote: *first.Observation, Base: task.Base(), Commit: task.Candidate.Commit, Source: "head"}
	calls := 0
	var readError error
	e.Checks = checksAdapter(func(context.Context, core.PublicationIntent) (core.CIObservation, error) {
		calls++
		return observed, readError
	})
	for i, state := range []string{"missing", "pending", "failed", "unavailable", "passed", "stale", "unavailable"} {
		observed.Remote = *first.Observation
		observed.Base = task.Base()
		readError = nil
		observed.Checks = []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: "SUCCESS"}}
		switch state {
		case "missing":
			observed.Checks = nil
		case "pending":
			observed.Checks[0].Status = "IN_PROGRESS"
		case "failed":
			observed.Checks[0].Conclusion = "FAILURE"
		case "stale":
			observed.Base = "moved"
		case "unavailable":
			if i == 3 {
				readError = errors.New("host unavailable")
			} else {
				observed.Remote.Number++
			}
		}
		if i > 0 {
			if err := e.AssessCI(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
		}
		if publication(t, e).CurrentCI(task) {
			t.Fatal("queued reassessment still current")
		}
		p := deliveryClaim(t, e)
		if p.State != "published" || p.CIRequest == nil {
			t.Fatal("CI lost publication identity")
		}
		if i == 1 {
			reopen(t, e)
			p = deliveryClaim(t, e)
		}
		if err := e.publish(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		got := publication(t, e)
		if got.CI == nil || got.CI.State != state || got.Occupying || got.CIRequest != nil || got.State != "published" || core.Digest(got.Intent) != core.Digest(first.Intent) || current(t, e, task.ID).Usage != before || current(t, e, task.ID).Stage != core.AwaitingDelivery {
			t.Fatal("CI changed authority or classified incorrectly", got)
		}
		view, err := e.Show(t.Context(), task.ID)
		if err != nil || !strings.Contains(view.Summary, "CI "+state) {
			t.Fatal("missing CI advice", view, err)
		}
		if err = e.preparePublications(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := e.claimPublication(t.Context()); ok || err != nil {
			t.Fatal("CI polled without explicit request", err)
		}
	}
	if calls != 7 {
		t.Fatal("unexpected CI calls", calls)
	}
	// A queued read may yield to refresh, but its old proof cannot be claimed.
	if err := e.AssessCI(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
		t.Fatal("stale CI request claimed", err)
	}
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if publication(t, e).CIRequest != nil || publication(t, e).CurrentCI(current(t, e, task.ID)) {
		t.Fatal("refresh retained current CI authority")
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	observed.Remote = *first.Observation
	observed.Base = task.Base()
	if err := os.WriteFile(filepath.Join(task.Candidate.Workspace, "greeting.txt"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if publication(t, e).CI.State != "unavailable" || calls != 7 {
		t.Fatal("changed candidate reached provider")
	}
}

func TestCIReadSharesCapacityAndStopRetainsOnlyHistoricalEvidence(t *testing.T) {
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	e, task, p := confirmedDraft(t, req)
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	queued := deliveryClaim(t, e)
	_ = approved(t, e)
	if _, ok, err := e.claim(t.Context()); err != nil || ok {
		t.Fatal("CI exceeded shared capacity", err)
	}
	reopen(t, e)
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxParallelism = 2 })
	started, release, local := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	e.Checks = checksAdapter(func(ctx context.Context, _ core.PublicationIntent) (core.CIObservation, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return core.CIObservation{Remote: *p.Observation, Base: task.Base(), Commit: task.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: "SUCCESS"}}}, nil
	})
	previous := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, start func(core.Process) error) execution.Completion {
		local <- struct{}{}
		return previous.Execute(ctx, r, start)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("CI did not recover")
	}
	select {
	case <-local:
	case <-ctx.Done():
		t.Fatal("slow CI blocked unrelated work")
	}
	if err := e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.SetDrained(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("CI drain did not settle")
	}
	got := publication(t, e)
	if got.Intent.ID != queued.Intent.ID || got.CI == nil || got.CI.State != "stale" || got.Occupying || got.CurrentCI(current(t, e, task.ID)) {
		t.Fatal("stopped CI established current evidence", got)
	}
}
