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

type checksAdapter func(context.Context, core.PublicationIntent) (core.CIObservation, error)

func (f checksAdapter) ObserveCI(c context.Context, p core.PublicationIntent) (core.CIObservation, error) {
	return f(c, p)
}

func TestCIRepairPreservesDraftAndReusesBoundedRecovery(t *testing.T) {
	req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
	e, task, first := confirmedDraft(t, req)
	now := time.Now().UTC()
	e.clock = func() time.Time { return now }
	if err := e.VerifyBase(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	complete(t, e, claim(t, e))
	finish(t, e)
	task = current(t, e, task.ID)
	source := task
	remote := *first.Observation
	conclusion := "FAILURE"
	e.Checks = checksAdapter(func(_ context.Context, p core.PublicationIntent) (core.CIObservation, error) {
		return core.CIObservation{Remote: remote, Base: task.Base(), Commit: p.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: conclusion}}}, nil
	})
	assess := func() {
		t.Helper()
		if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
			t.Fatal(err)
		}
	}
	assess()
	waiting(t, e, task.ID, "repair limit")
	view, err := e.Show(t.Context(), task.ID)
	if err != nil || !strings.Contains(view.Summary, "bounded repair") || current(t, e, task.ID).CIRepair != nil {
		t.Fatal("inspection changed authority or lost repair advice", view, err)
	}
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxRepairs = 2
		c.Policy.MaxTransientRetries = 1
		c.Profiles["careful"] = core.Profile{Harness: "codex", Model: "stronger", Reasoning: "high", Timeout: "1m"}
		c.Policy.ImplementationLadder = []string{"careful"}
	})
	// Reassessment changes only publication state. Its race must still defeat a
	// previously derived repair in the store's admission transaction.
	projected := ciRepairTask(task, []core.Publication{publication(t, e)})
	projected.Usage.Repairs++
	projected.Usage.Invocations++
	projected.WorkspaceRepair = 1
	projected.BaseProof, projected.Review, projected.Pending = nil, nil, nil
	projected.Stage, projected.NextRole = core.Implementing, ""
	settings, _, _ := e.Store.Settings(t.Context())
	stale := core.Attempt{ID: core.NewID(), TaskID: task.ID, Version: task.Current().Number, Generation: task.Generation, Role: "implementer", Kind: "ci_repair", Base: task.Base(), Occupying: true, WorkspaceClaim: workspaceClaim(settings.Config, projected), PolicyRevision: settings.Config.Revision(), Invocation: projected.Usage.Invocations, CIRepair: projected.CIRepair}
	if err = e.AssessCI(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Commit(t.Context(), task.Revision, projected, &stale, true); !errors.Is(err, store.ErrConflict) {
		t.Fatal("reassessment lost race", err)
	}
	assess()
	repair := claim(t, e)
	if repair.Kind != "ci_repair" || repair.Repair != 1 || repair.ProfileName != "build" || repair.CIRepair == nil || repair.WorkspaceClaim == source.Candidate.Workspace {
		t.Fatal("wrong CI reservation", repair)
	}
	if got := current(t, e, task.ID); got.BaseProof != nil || len(got.Review) != 0 || got.Usage.Escalations != 0 {
		t.Fatal("CI retained acceptance or escalated", got)
	}
	firstCall := true
	e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
		if r.Attempt.CIRepair == nil {
			t.Fatal("repair context lost")
		}
		if r.Attempt.Role == "implementer" {
			body, err := os.ReadFile(filepath.Join(r.Dir, "greeting.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if firstCall {
				firstCall = false
				if string(body) != "hello\n" {
					t.Fatal("repair did not start at failed candidate")
				}
				if err = os.WriteFile(filepath.Join(r.Dir, "greeting.txt"), []byte("hello"), 0600); err != nil {
					t.Fatal(err)
				}
				return execution.Completion{Stopped: true, Kind: core.Transient, Error: "provider unavailable after partial edit"}
			}
			if string(body) != "hello" {
				t.Fatal("retry erased partial repair")
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
	if retry.Repair != 1 || retry.Retry != 1 || retry.ProfileName != repair.ProfileName || retry.CIRepair == nil {
		t.Fatal("retry gained allowance or lost context", retry)
	}
	complete(t, e, retry)
	finish(t, e)
	if err = e.git().ValidateCandidate(t.Context(), source); err != nil {
		t.Fatal("published candidate was modified", err)
	}
	if p := publication(t, e); p.Intent.Candidate != *source.Candidate || p.CurrentCI(current(t, e, task.ID)) {
		t.Fatal("repair reused current CI")
	}
	audit := claim(t, e)
	if audit.Role != "audit" {
		t.Fatal("repair bypassed review")
	}
	complete(t, e, audit)
	finish(t, e)
	refresh := claim(t, e)
	if refresh.Role != "refresh" {
		t.Fatal("repair bypassed current base")
	}
	complete(t, e, refresh)
	finish(t, e)
	task = current(t, e, task.ID)
	if task.BaseProof == nil || !task.BaseProof.Matches(task) || task.Usage.Repairs != 1 || task.Usage.Escalations != 0 || task.Usage.BaseRefreshes != source.Usage.BaseRefreshes {
		t.Fatal("repair changed budgets or lost proof", task)
	}
	e.Delivery = deliveryAdapter{
		observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) { return remote, nil },
		push: func(_ context.Context, p core.PublicationIntent, authorize func() error) error {
			if p.Sync == nil || p.Sync.PreviousCommit != source.Candidate.Commit || p.Sync.Number != remote.Number || p.Branch != first.Intent.Branch {
				t.Fatal("repair lost same-draft lease")
			}
			if err := authorize(); err != nil {
				return err
			}
			remote.BranchCommit = p.Candidate.Commit
			return nil
		},
		create: func(context.Context, core.PublicationIntent, func() error) error {
			t.Fatal("repair created another PR")
			return nil
		},
	}
	if err = e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	conclusion = "SUCCESS"
	assess()
	all, err := e.Store.Publications(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := latestPublication(all, task); got == nil || got.CI.State != "passed" || !got.CurrentCI(task) {
		t.Fatal("new candidate CI not current", got)
	}
	if _, ok, err := e.claim(t.Context()); ok || err != nil {
		t.Fatal("success admitted another repair", err)
	}
	conclusion = "FAILURE"
	if err = e.AssessCI(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	assess()
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 1 })
	waiting(t, e, task.ID, "repair limit")
	applyPolicy(t, e, func(c *config.Config) {
		c.Policy.MaxRepairs = 2
		n := task.Usage.Invocations
		c.Policy.MaxInvocationsPerTask = &n
	})
	waiting(t, e, task.ID, "invocation ceiling")
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxInvocationsPerTask = nil })
	second := claim(t, e)
	if second.Repair != 2 || second.ProfileName != "build" {
		t.Fatal("second failure reset allowance or escalated", second)
	}
	reopen(t, e)
	if err = e.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Resume(t.Context(), task.ID, second.ID, true); err != nil {
		t.Fatal(err)
	}
	resumed := claim(t, e)
	if resumed.Repair != 2 || resumed.CIRepair == nil || resumed.ResumedFrom != second.ID {
		t.Fatal("manual resume reset pass", resumed)
	}
	if err = e.Stop(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err = e.recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := current(t, e, task.ID); got.Stage != core.Stopped || got.Usage.Repairs != 2 || got.Usage.Escalations != 0 {
		t.Fatal("stop lost budgets", got)
	}
}

func TestCIRepairCannotPublishUnchangedOrTamperedSource(t *testing.T) {
	for _, scenario := range []string{"unchanged", "mode only", "tampered"} {
		t.Run(scenario, func(t *testing.T) {
			req := core.CIRequirement{Kind: "check", Name: "test", AppID: 15368}
			e, task, p := confirmedDraft(t, req)
			if err := e.VerifyBase(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			complete(t, e, claim(t, e))
			finish(t, e)
			task = current(t, e, task.ID)
			e.Checks = checksAdapter(func(context.Context, core.PublicationIntent) (core.CIObservation, error) {
				return core.CIObservation{Remote: *p.Observation, Base: task.Base(), Commit: task.Candidate.Commit, Source: "head", Checks: []core.RemoteCheck{{CIRequirement: req, Status: "COMPLETED", Conclusion: "FAILURE"}}}, nil
			})
			if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
				t.Fatal(err)
			}
			applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxRepairs = 1 })
			calls := 0
			e.Executor = adapter(func(_ context.Context, r execution.Request, _ func(core.Process) error) execution.Completion {
				calls++
				if scenario == "mode only" {
					if err := os.Chmod(filepath.Join(r.Dir, "greeting.txt"), 0640); err != nil {
						t.Fatal(err)
					}
				}
				return pass(r)
			})
			if scenario == "tampered" {
				if err := os.WriteFile(filepath.Join(task.Candidate.Workspace, "greeting.txt"), []byte("changed externally"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			a := claim(t, e)
			f := e.execute(t.Context(), a)
			if err := e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			got := current(t, e, task.ID)
			if got.Attention == nil || got.NextRole != "" || got.Usage.Repairs != 1 || got.BaseProof != nil {
				t.Fatal("invalid repair continued", got)
			}
			if scenario == "tampered" && (calls != 0 || got.Attention.Kind != core.Integrity) {
				t.Fatal("tampered source reached implementer")
			}
			if scenario != "tampered" && (calls != 1 || got.Attention.Kind != core.ProofGap || !strings.Contains(got.Attention.Detail, "unchanged")) {
				t.Fatalf("no-op repair not explained: calls=%d attention=%+v", calls, got.Attention)
			}
		})
	}
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
		if state == "passed" {
			// Passing CI queues one integration read, never another CI poll.
			p, ok, err := e.claimPublication(t.Context())
			if err != nil || !ok || p.IntegrationRequest == nil {
				t.Fatal("integration not queued", err)
			}
			if err = e.publish(t.Context(), p); err != nil {
				t.Fatal(err)
			}
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
