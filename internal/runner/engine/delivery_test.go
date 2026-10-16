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

type deliveryAdapter struct {
	observe func(context.Context, core.PublicationIntent) (core.RemotePublication, error)
	push    func(context.Context, core.PublicationIntent, func() error) error
	create  func(context.Context, core.PublicationIntent, func() error) error
}

func (d deliveryAdapter) Observe(c context.Context, p core.PublicationIntent) (core.RemotePublication, error) {
	return d.observe(c, p)
}
func (d deliveryAdapter) Push(c context.Context, p core.PublicationIntent, a func() error) error {
	return d.push(c, p, a)
}
func (d deliveryAdapter) CreateDraft(c context.Context, p core.PublicationIntent, a func() error) error {
	return d.create(c, p, a)
}

func deliveryFixture(t *testing.T, required ...core.CIRequirement) (*Engine, core.Task) {
	t.Helper()
	e := fixture(t, func(c *config.Config) { c.Delivery.Mode = "github-draft"; c.Delivery.Repository = "owner/repo" })
	if len(required) > 0 {
		q := e.Settings.Quality
		q.RequiredCI = required
		if err := e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), q); err != nil {
			t.Fatal(err)
		}
		e.Settings.Quality = q
	}
	task := approved(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	complete(t, e, claim(t, e))
	finish(t, e)
	return e, current(t, e, task.ID)
}
func publication(t *testing.T, e *Engine) core.Publication {
	t.Helper()
	ps, err := e.Store.Publications(t.Context(), "")
	if err != nil || len(ps) != 1 {
		t.Fatalf("publications: %v %v", ps, err)
	}
	return ps[0]
}
func deliveryClaim(t *testing.T, e *Engine) core.Publication {
	t.Helper()
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	p, ok, err := e.claimPublication(t.Context())
	if err != nil || !ok {
		t.Fatalf("claim publication: %t %v", ok, err)
	}
	return p
}

func TestPublicationRecoversLostResponseAndRetainsExactAcceptance(t *testing.T) {
	e, task := deliveryFixture(t)
	p := deliveryClaim(t, e)
	remote := core.RemotePublication{}
	pushes, creates := 0, 0
	e.Delivery = deliveryAdapter{
		observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) { return remote, nil },
		push: func(_ context.Context, i core.PublicationIntent, a func() error) error {
			if err := a(); err != nil {
				return err
			}
			pushes++
			remote.BranchCommit = i.Candidate.Commit
			return nil
		},
		create: func(_ context.Context, i core.PublicationIntent, a func() error) error {
			if !publication(t, e).CreateRequested {
				t.Fatal("remote write preceded durable intent")
			}
			if err := a(); err != nil {
				return err
			}
			creates++
			remote.Number, remote.URL, remote.State, remote.Draft = 7, "https://github.com/owner/repo/pull/7", "OPEN", true
			return errors.New("response lost after creation")
		},
	}
	if err := e.publish(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if got := publication(t, e); got.State != "published" || got.Occupying || got.Observation.URL != remote.URL {
		t.Fatalf("lost response not recovered: %+v", got)
	}
	reopen(t, e)
	if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || creates != 1 {
		t.Fatalf("duplicate remote writes: %d %d", pushes, creates)
	}
	confirmed := remote
	remote = core.RemotePublication{}
	if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if got := publication(t, e); got.State != "attention" || got.Observation.URL != confirmed.URL || creates != 1 {
		t.Fatal("missing PR lost stable identity or repeated creation")
	}
	remote = confirmed
	got := current(t, e, task.ID)
	if got.Stage != core.AwaitingDelivery || got.Usage != task.Usage || *got.Candidate != *task.Candidate {
		t.Fatalf("publication changed execution/completion: %+v", got)
	}
	child := core.Task{Versions: []core.Version{{Dependencies: []core.Dependency{{TaskID: got.ID, Version: got.Current().Number}}}}}
	if core.PrerequisiteWait(child, []core.Task{got}) == "" {
		t.Fatal("draft released dependency")
	}
	remote.State = "MERGED"
	if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if publication(t, e).State != "attention" || current(t, e, task.ID).Stage != core.AwaitingDelivery {
		t.Fatal("remote merge bypassed integration verification")
	}
}

func TestInterruptedCreateIsInspectionOnlyEvenWhenRemoteIsAbsent(t *testing.T) {
	e, task := deliveryFixture(t)
	p := deliveryClaim(t, e)
	p.CreateRequested = true
	if err := e.savePublication(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	writes := 0
	e.Delivery = deliveryAdapter{
		observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) {
			return core.RemotePublication{}, nil
		},
		push:   func(context.Context, core.PublicationIntent, func() error) error { writes++; return nil },
		create: func(context.Context, core.PublicationIntent, func() error) error { writes++; return nil },
	}
	for i := 0; i < 2; i++ {
		if i > 0 {
			if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
			t.Fatal(err)
		}
		got := publication(t, e)
		if got.State != "attention" || !got.CreateRequested || !strings.Contains(got.Detail, "will not be repeated") {
			t.Fatalf("unsafe recovery: %+v", got)
		}
	}
	if writes != 0 {
		t.Fatal("uncertain creation replayed")
	}
	// The immutable private operation cannot be edited to regain POST authority.
	p = publication(t, e)
	p.CreateRequested = false
	if e.Store.SavePublication(t.Context(), p, false) == nil {
		t.Fatal("create intent cleared")
	}
}

func TestPublicationRevocationAndChangedCandidatePreventWrites(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{true: "stop immediately before write", false: "changed candidate"}[stop], func(t *testing.T) {
			e, task := deliveryFixture(t)
			p := deliveryClaim(t, e)
			writes := 0
			e.Delivery = deliveryAdapter{
				observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) {
					if stop {
						if err := e.Stop(t.Context(), task.ID); err != nil {
							t.Fatal(err)
						}
					} else {
						if err := os.WriteFile(filepath.Join(task.Candidate.Workspace, "greeting.txt"), []byte("changed"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return core.RemotePublication{}, nil
				},
				push:   func(context.Context, core.PublicationIntent, func() error) error { writes++; return nil },
				create: func(context.Context, core.PublicationIntent, func() error) error { writes++; return nil },
			}
			if err := e.publish(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			got := publication(t, e)
			if writes != 0 || got.Occupying || got.State == "published" {
				t.Fatalf("revocation/integrity failed: %+v writes=%d", got, writes)
			}
			if stop && (got.State != "revoked" || got.Intent != p.Intent) {
				t.Fatal("revoked identity not retained")
			}
		})
	}
}

func TestSlowPublicationSharesCapacityWithoutBlockingLocalWork(t *testing.T) {
	e, _ := deliveryFixture(t)
	first := deliveryClaim(t, e)
	_ = approved(t, e)
	if _, ok, err := e.claim(t.Context()); err != nil || ok {
		t.Fatalf("model admitted past publication reservation: %t %v", ok, err)
	}
	// Simulate restart with the publication reservation still durable.
	reopen(t, e)
	next := e.Settings
	next.Config.Policy.MaxParallelism = 2
	if err := e.Store.ApplySettings(t.Context(), e.Settings.Config.Revision(), next); err != nil {
		t.Fatal(err)
	}
	e.Settings = next
	remoteStarted, releaseRemote, localStarted := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
	e.Delivery = deliveryAdapter{observe: func(ctx context.Context, _ core.PublicationIntent) (core.RemotePublication, error) {
		close(remoteStarted)
		select {
		case <-releaseRemote:
		case <-ctx.Done():
		}
		return core.RemotePublication{}, errors.New("host unavailable")
	}}
	previous := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, start func(core.Process) error) execution.Completion {
		localStarted <- struct{}{}
		return previous.Execute(ctx, r, start)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	select {
	case <-remoteStarted:
	case <-ctx.Done():
		t.Fatal("publication did not start")
	}
	select {
	case <-localStarted:
	case <-ctx.Done():
		t.Fatal("slow publication blocked local execution")
	}
	if err := e.Store.SetDrained(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	close(releaseRemote)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("drain did not settle")
	}
	got := publication(t, e)
	if got.Intent.ID != first.Intent.ID || got.Occupying || got.State != "attention" {
		t.Fatalf("reservation not recovered: %+v", got)
	}
}

// Keep one confirmed original draft, then use the real refresh/review machinery.
func confirmedDraft(t *testing.T, required ...core.CIRequirement) (*Engine, core.Task, core.Publication) {
	t.Helper()
	e, task := deliveryFixture(t, required...)
	applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxBaseRefreshes = 2 })
	e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) {
		return git(t, e.Settings.Config.Repository.Path, "rev-parse", "HEAD"), nil
	})
	p := deliveryClaim(t, e)
	p.State, p.Occupying, p.CreateRequested = "published", false, true
	p.Observation = &core.RemotePublication{Number: 7, URL: "https://github.com/owner/repo/pull/7", BranchCommit: task.Candidate.Commit, State: "OPEN", Draft: true}
	if err := e.savePublication(t.Context(), &p); err != nil {
		t.Fatal(err)
	}
	return e, task, p
}

func refreshDraft(t *testing.T, e *Engine, id, body string) core.Task {
	t.Helper()
	moveBase(t, e, "base.txt", body)
	if err := e.VerifyBase(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	// Refresh, independent audit, then observe the base again.
	before, err := e.Store.Publications(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	for step := range 3 {
		complete(t, e, claim(t, e))
		finish(t, e)
		if step < 2 {
			if err := e.preparePublications(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			after, err := e.Store.Publications(t.Context(), id)
			if err != nil || len(after) != len(before) {
				t.Fatal("unverified candidate queued for synchronization", after, err)
			}
		}
	}
	task := current(t, e, id)
	if task.BaseProof == nil || !task.BaseProof.Matches(task) {
		t.Fatal("refresh did not retain exact proof")
	}
	return task
}

func TestDraftSynchronizationRetainsChainAndRecoversUncertainPush(t *testing.T) {
	e, task, first := confirmedDraft(t)
	remote := *first.Observation
	pushes := 0
	lostObservation := false
	e.Delivery = deliveryAdapter{
		observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) {
			if lostObservation {
				return core.RemotePublication{}, errors.New("observation unavailable after push")
			}
			return remote, nil
		},
		push: func(_ context.Context, p core.PublicationIntent, authorize func() error) error {
			if err := authorize(); err != nil {
				return err
			}
			if p.Sync == nil || p.Sync.PreviousCommit != remote.BranchCommit || p.PublicationID() != first.Intent.ID || p.Branch != first.Intent.Branch {
				t.Fatal("synchronization lost its exact lease or stable identity")
			}
			pushes++
			remote.BranchCommit = p.Candidate.Commit
			lostObservation = pushes == 1
			return errors.New("lost push response")
		},
		create: func(context.Context, core.PublicationIntent, func() error) error {
			t.Fatal("synchronization created a PR")
			return nil
		},
	}
	previous := first.Intent.ID
	for cycle := 1; cycle <= 2; cycle++ {
		task = refreshDraft(t, e, task.ID, string(rune('0'+cycle)))
		before := task.Usage
		p := deliveryClaim(t, e)
		if p.Intent.Sync == nil || p.Intent.Sync.PreviousID != previous || p.Intent.Sync.Base != task.Base() || !p.Intent.Sync.ObservedAt.Equal(task.BaseProof.ObservedAt) {
			t.Fatal("update did not bind exact base proof and predecessor", p)
		}
		if cycle == 2 {
			// A crash after admission resumes the same immutable update.
			reopen(t, e)
			p = deliveryClaim(t, e)
		}
		if err := e.publish(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		if cycle == 1 {
			ps, _ := e.Store.Publications(t.Context(), task.ID)
			if ps[1].State != "attention" || ps[1].Observation.URL != remote.URL {
				t.Fatal("uncertain update lost identity or silently retried")
			}
			if err := e.VerifyBase(t.Context(), task.ID); err == nil || !strings.Contains(err.Error(), "--reconcile") {
				t.Fatal("uncertain update allowed a new refresh", err)
			}
			reopen(t, e)
			lostObservation = false
			if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
				t.Fatal(err)
			}
		}
		ps, err := e.Store.Publications(t.Context(), task.ID)
		if err != nil || len(ps) != cycle+1 || ps[cycle].State != "published" || ps[cycle].Occupying || core.Digest(ps[0].Intent) != core.Digest(first.Intent) || pushes != cycle {
			t.Fatal("synchronization failed or repeated writes", ps, pushes, err)
		}
		changed := ps[cycle]
		changed.Intent.Sync.PreviousCommit = "external"
		if e.Store.SavePublication(t.Context(), changed, false) == nil {
			t.Fatal("immutable synchronization intent changed")
		}
		task = current(t, e, task.ID)
		if task.Usage != before || task.Stage != core.AwaitingDelivery || remote.BranchCommit != task.Candidate.Commit {
			t.Fatal("synchronization changed counters or completed the task")
		}
		view, err := e.Show(t.Context(), task.ID)
		if err != nil || view.Group != "Retained" || !strings.Contains(view.Summary, "draft confirmed at this candidate") {
			t.Fatal("current draft described as stale", view.Summary, err)
		}
		previous = p.Intent.ID
	}
}

func TestDraftSynchronizationRejectsUnsafeRemoteOrLocalState(t *testing.T) {
	for _, scenario := range []string{"missing PR", "other PR", "missing branch", "external commit", "ready", "closed", "merged", "revoked", "changed candidate", "previous commit retry"} {
		t.Run(scenario, func(t *testing.T) {
			e, task, first := confirmedDraft(t)
			task = refreshDraft(t, e, task.ID, "new base")
			p := deliveryClaim(t, e)
			remote := *first.Observation
			switch scenario {
			case "missing PR":
				remote.Number = 0
			case "other PR":
				remote.Number++
			case "missing branch":
				remote.BranchCommit = ""
			case "external commit":
				remote.BranchCommit = "external"
			case "ready":
				remote.Draft = false
			case "closed":
				remote.State = "CLOSED"
			case "merged":
				remote.State = "MERGED"
			}
			writes := 0
			e.Delivery = deliveryAdapter{
				observe: func(context.Context, core.PublicationIntent) (core.RemotePublication, error) {
					if scenario == "revoked" {
						if err := e.Stop(t.Context(), task.ID); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "changed candidate" {
						if err := os.WriteFile(filepath.Join(task.Candidate.Workspace, "greeting.txt"), []byte("tampered"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					return remote, nil
				},
				push: func(_ context.Context, intent core.PublicationIntent, authorize func() error) error {
					if err := authorize(); err != nil {
						return err
					}
					writes++
					if writes == 2 {
						remote.BranchCommit = intent.Candidate.Commit
					}
					return errors.New("transport interrupted")
				},
				create: func(context.Context, core.PublicationIntent, func() error) error {
					t.Fatal("unexpected PR creation")
					return nil
				},
			}
			if err := e.publish(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			ps, err := e.Store.Publications(t.Context(), task.ID)
			if err != nil || len(ps) != 2 || ps[1].State == "published" || ps[1].Occupying || current(t, e, task.ID).Stage == core.Completed {
				t.Fatal("unsafe update", ps, err)
			}
			if scenario == "previous commit retry" {
				view, err := e.Show(t.Context(), task.ID)
				if err != nil || view.Group != "Needs you" || !strings.Contains(view.NextAction, "--reconcile") {
					t.Fatal("missing recovery advice", view, err)
				}
				if err := e.preparePublications(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
					t.Fatal("failed update retried automatically", err)
				}
				if err := e.ReconcileDelivery(t.Context(), task.ID); err != nil {
					t.Fatal(err)
				}
				if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
					t.Fatal(err)
				}
				ps, _ = e.Store.Publications(t.Context(), task.ID)
				if writes != 2 || ps[1].State != "published" || core.Digest(ps[1].Intent) != core.Digest(p.Intent) {
					t.Fatal("retry changed immutable update", ps, writes)
				}
			} else if writes != 0 {
				t.Fatal("unsafe remote write", writes)
			}
		})
	}
}
