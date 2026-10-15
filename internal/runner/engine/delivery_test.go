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

func deliveryFixture(t *testing.T) (*Engine, core.Task) {
	t.Helper()
	e := fixture(t, func(c *config.Config) { c.Delivery.Mode = "github-draft"; c.Delivery.Repository = "owner/repo" })
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
