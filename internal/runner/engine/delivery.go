package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/store"
)

func publicationWait(v Eligibility, c config.Config, t core.Task, attempts []core.Attempt, publications []core.Publication) Eligibility {
	occupied := 0
	for _, a := range attempts {
		if a.Occupying {
			occupied++
		}
	}
	for _, p := range publications {
		if !p.Occupying {
			continue
		}
		occupied++
		if p.Intent.TaskID == t.ID {
			v.WaitKind, v.WaitingFor = "ownership", "publication owns this task"
			return v
		}
	}
	if (v.WaitingFor == "" || v.WaitKind == "capacity") && occupied >= c.Policy.MaxParallelism {
		v.WaitKind, v.WaitingFor = "capacity", fmt.Sprintf("global capacity: %d/%d occupied", occupied, c.Policy.MaxParallelism)
	}
	return v
}

// DeliveryHost owns transport only. Each write must call authorize immediately
// before dispatch; it cannot expand this intent or decide task completion.
type DeliveryHost interface {
	Observe(context.Context, core.PublicationIntent) (core.RemotePublication, error)
	Push(context.Context, core.PublicationIntent, func() error) error
	CreateDraft(context.Context, core.PublicationIntent, func() error) error
}

type activePublication struct {
	intent core.PublicationIntent
	cancel context.CancelFunc
	done   chan error
}

func latestPublication(all []core.Publication, t core.Task) *core.Publication {
	for i := len(all) - 1; i >= 0; i-- {
		p := &all[i]
		if p.Intent.TaskID == t.ID && p.Intent.Version == t.Current().Number && p.Intent.Generation == t.Generation {
			return p
		}
	}
	return nil
}

func (e *Engine) savePublication(ctx context.Context, p *core.Publication) error {
	p.UpdatedAt = e.now()
	if err := e.Store.SavePublication(ctx, *p, false); err != nil {
		return err
	}
	p.Revision++
	return nil
}

func (e *Engine) preparePublications(ctx context.Context, active map[string]*activePublication) error {
	all, err := e.Store.Publications(ctx, "")
	if err != nil {
		return err
	}
	for _, p := range all {
		if active[p.Intent.ID] != nil {
			continue
		}
		t, err := e.Store.Task(ctx, p.Intent.TaskID)
		if err != nil {
			return err
		}
		if !p.Intent.Authorized(t) && (p.State == "queued" || p.Occupying) {
			p.State, p.Detail, p.Occupying = "revoked", "Authority revoked; any already-sent remote effect is retained and is not undone.", false
		} else if p.Occupying {
			// A lost push is safe to inspect and resume under its exact remote lease.
			// CreateRequested permanently prevents replay of an uncertain PR POST.
			p.State, p.Occupying = "queued", false
		} else {
			continue
		}
		if err = e.savePublication(ctx, &p); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	if e.Settings.Config.Delivery.Mode != "github-draft" {
		return nil
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Stage != core.AwaitingDelivery || t.Candidate == nil || t.Approval == nil || t.Attention != nil {
			continue
		}
		previous := latestPublication(all, t)
		if previous != nil {
			if previous.State != "published" || previous.Occupying || previous.Intent.Candidate == *t.Candidate || t.BaseProof == nil || !t.BaseProof.Matches(t) || t.NextRole != "" {
				continue
			}
			o := previous.Observation
			if o == nil || o.Number == 0 || o.State != "OPEN" || !o.Draft || o.BranchCommit != previous.Intent.Candidate.Commit {
				continue
			}
			intent := previous.Intent
			intent.ID, intent.Candidate, intent.ReviewDigest = core.NewID(), *t.Candidate, core.Digest(t.Review)
			intent.Sync = &core.DraftSync{PreviousID: previous.Intent.ID, PublicationID: previous.Intent.PublicationID(), PreviousCommit: previous.Intent.Candidate.Commit, Number: o.Number, URL: o.URL, Base: t.BaseProof.Base, ObservedAt: t.BaseProof.ObservedAt}
			p := core.Publication{State: "queued", Intent: intent, Observation: o, ObservedAt: previous.ObservedAt}
			if err = e.savePublication(ctx, &p); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			continue
		}
		if t.NextRole != "" {
			continue
		}
		if err = core.ValidateResult(core.Result{Outcome: "succeeded", Summary: "accepted review", Checks: t.Review}, t.Proofs()); err != nil {
			return fmt.Errorf("publication review binding: %w", err)
		}
		id := core.NewID()
		title := []rune(TaskTitle(t))
		if len(title) > 160 {
			title = title[:160]
		}
		p := core.Publication{State: "queued", Intent: core.PublicationIntent{
			ID: id, TaskID: t.ID, Version: t.Current().Number, Generation: t.Generation,
			ApprovedBase: t.Approval.Base, Quality: t.Current().Quality, ReviewDigest: core.Digest(t.Review), Candidate: *t.Candidate,
			Repository: e.Settings.Config.Delivery.Repository, BaseBranch: strings.TrimPrefix(e.Settings.Config.Repository.BaseRef, "refs/heads/"),
			Branch: "runner-next/" + id, Title: "Runner: " + string(title),
		}}
		if err = e.savePublication(ctx, &p); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}

func (e *Engine) claimPublication(ctx context.Context) (core.Publication, bool, error) {
	all, err := e.Store.Publications(ctx, "")
	if err != nil {
		return core.Publication{}, false, err
	}
	for _, p := range all {
		if p.State != "queued" || p.Occupying {
			continue
		}
		p.State, p.Occupying, p.UpdatedAt = "running", true, e.now()
		if err = e.Store.SavePublication(ctx, p, true); errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return p, false, err
		}
		p.Revision++
		return p, true, nil
	}
	return core.Publication{}, false, nil
}

func (e *Engine) publicationAuthority(ctx context.Context, p core.PublicationIntent) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	t, err := e.Store.Task(ctx, p.TaskID)
	if err != nil {
		return err
	}
	if !p.Authorized(t) {
		return errors.New("publication authority revoked or acceptance changed")
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return err
	}
	return pinned.git().ValidateCandidate(ctx, t)
}

func (e *Engine) publish(ctx context.Context, p core.Publication) error {
	err := e.publishSteps(ctx, &p)
	p.Occupying = false
	if err != nil {
		p.State, p.Detail = "attention", err.Error()
	}
	t, readErr := e.Store.Task(context.Background(), p.Intent.TaskID)
	if readErr != nil {
		return readErr
	}
	if !p.Intent.Authorized(t) {
		p.State, p.Detail = "revoked", "Authority revoked; any already-sent remote effect is retained and is not undone."
	}
	return e.savePublication(context.Background(), &p)
}

func (e *Engine) observePublication(ctx context.Context, p *core.Publication) error {
	o, err := e.Delivery.Observe(ctx, p.Intent)
	if err != nil {
		return err
	}
	if p.Observation != nil && p.Observation.Number != 0 &&
		(o.Number != p.Observation.Number || o.URL != p.Observation.URL) {
		return errors.New("previously observed PR is missing or has a different identity; retained link is the last confirmed observation")
	}
	now := e.now()
	p.Observation, p.ObservedAt = &o, &now
	return e.savePublication(ctx, p)
}

func publicationOutcome(p *core.Publication) (bool, error) {
	o := p.Observation
	if o.BranchCommit != "" && o.BranchCommit != p.Intent.Candidate.Commit {
		return false, errors.New("remote branch differs from the accepted candidate; no overwrite permitted")
	}
	if o.Number == 0 {
		return false, nil
	}
	if o.State != "OPEN" {
		return false, fmt.Errorf("remote PR is %s; this bundle cannot confirm integration or complete the task", o.State)
	}
	if !o.Draft {
		return false, errors.New("remote PR was marked ready externally; integration verification remains unimplemented")
	}
	if o.BranchCommit != p.Intent.Candidate.Commit {
		return false, errors.New("published PR branch is missing; no automatic recreation")
	}
	p.State, p.Detail = "published", "Draft PR retained; latest-base verification and integration remain pending."
	return true, nil
}

func synchronizationOutcome(p *core.Publication) (bool, error) {
	u, o := p.Intent.Sync, p.Observation
	if o == nil || o.Number != u.Number || o.URL != u.URL {
		return false, errors.New("known draft is missing or changed; synchronization cannot create another PR")
	}
	if o.State != "OPEN" || !o.Draft {
		return false, errors.New("known PR is no longer an open draft; CI and integration remain pending")
	}
	if o.BranchCommit == p.Intent.Candidate.Commit {
		p.State, p.Detail = "published", "Draft synchronized to the verified candidate; CI and integration remain pending."
		return true, nil
	}
	if o.BranchCommit != u.PreviousCommit {
		return false, errors.New("draft branch is missing or differs from the expected previous commit; no overwrite or recreation permitted")
	}
	return false, nil
}

func (e *Engine) synchronizeDraft(ctx context.Context, p *core.Publication, authorize func() error) error {
	if err := e.observePublication(ctx, p); err != nil {
		return err
	}
	if done, err := synchronizationOutcome(p); done || err != nil {
		return err
	}
	if err := authorize(); err != nil {
		return err
	}
	pushErr := e.Delivery.Push(ctx, p.Intent, authorize)
	// A failed response can follow a successful push. Inspect once; a future
	// reconciliation may repeat only the same expected-old lease, never PR creation.
	if err := e.observePublication(ctx, p); err != nil {
		return errors.Join(pushErr, err)
	}
	if done, err := synchronizationOutcome(p); done || err != nil {
		return err
	}
	return errors.Join(pushErr, errors.New("draft update is not confirmed; reconcile the retained operation"))
}

func (e *Engine) publishSteps(ctx context.Context, p *core.Publication) error {
	if e.Delivery == nil {
		return errors.New("code delivery adapter is unavailable")
	}
	authorize := func() error { return e.publicationAuthority(ctx, p.Intent) }
	if p.Intent.Sync != nil {
		return e.synchronizeDraft(ctx, p, authorize)
	}
	if err := e.observePublication(ctx, p); err != nil {
		return err
	}
	if done, err := publicationOutcome(p); done || err != nil {
		return err
	}
	if p.CreateRequested {
		return errors.New("PR creation outcome is uncertain; inspection found no PR. Creation will not be repeated; inspect the remote branch and retained operation identity")
	}
	if err := authorize(); err != nil {
		return err
	}
	if p.Observation.BranchCommit == "" {
		if err := e.Delivery.Push(ctx, p.Intent, authorize); err != nil {
			return err
		}
		if err := e.observePublication(ctx, p); err != nil {
			return err
		}
		if done, err := publicationOutcome(p); done || err != nil {
			return err
		}
		if p.Observation.BranchCommit != p.Intent.Candidate.Commit {
			return errors.New("push outcome is not yet confirmed; reconcile before continuing")
		}
	}
	if err := authorize(); err != nil {
		return err
	}
	p.CreateRequested = true
	if err := e.savePublication(ctx, p); err != nil {
		return err
	}
	createErr := e.Delivery.CreateDraft(ctx, p.Intent, authorize)
	// Even a failed response may have created the object. Inspect once, never
	// repeat the write. Context cancellation leaves recovery to the next run.
	if err := e.observePublication(ctx, p); err != nil {
		return errors.Join(createErr, err)
	}
	if done, err := publicationOutcome(p); done || err != nil {
		return err
	}
	return errors.Join(createErr, errors.New("PR creation outcome is uncertain; no matching PR observed; reconcile by inspection only"))
}

func (e *Engine) ReconcileDelivery(ctx context.Context, id string) error {
	all, err := e.Store.Publications(ctx, id)
	if err != nil {
		return err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	p := latestPublication(all, t)
	if p != nil && p.Intent.Authorized(t) {
		if p.Occupying || p.State == "queued" {
			return nil
		}
		p.State, p.Detail = "queued", "Operator requested remote inspection and safe continuation; uncertain PR creation cannot repeat."
		return e.savePublication(ctx, p)
	}
	return errors.New("no current authorized publication; run reconciliation after local review is accepted")
}
