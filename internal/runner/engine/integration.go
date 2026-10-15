package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// IntegrationHost observes a known PR and the exact selected CI commit. It has
// no write method and cannot decide local task completion.
type IntegrationHost interface {
	ObserveIntegration(context.Context, core.Publication) (core.IntegrationObservation, error)
}

func (e *Engine) InspectIntegration(ctx context.Context, id string) error {
	return e.requestIntegration(ctx, id, false)
}

func (e *Engine) RequestMerge(ctx context.Context, id string) error {
	return e.requestIntegration(ctx, id, true)
}

func (e *Engine) requestIntegration(ctx context.Context, id string, merge bool) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	if t.Stage == core.Completed {
		return nil
	}
	all, err := e.Store.Publications(ctx, id)
	if err != nil {
		return err
	}
	p := latestPublication(all, t)
	if p == nil || !p.CanInspectIntegration(t) {
		return errors.New("integration inspection requires an accepted published candidate, current-base proof and passed pinned CI")
	}
	if p.Occupying {
		return errors.New("publication still owns its reservation")
	}
	if merge && p.MergeDispatch != nil {
		return errors.New("merge was already dispatched; task integrate inspects the outcome without repeating the write")
	}
	if p.IntegrationRequest != nil && (!merge || p.IntegrationRequest.Merge && p.IntegrationRequest.AutomaticPolicy == "") {
		return nil
	}
	p.State = "published"
	p.IntegrationRequest = &core.IntegrationRequest{Merge: merge, Proof: *t.BaseProof, CIDigest: core.Digest(p.CI)}
	return e.savePublication(ctx, p)
}

func (e *Engine) inspectIntegration(ctx context.Context, p core.Publication) error {
	r := core.IntegrationAssessment{Request: *p.IntegrationRequest, State: "unavailable"}
	var readErr error
	if r.Request.Merge && p.MergeDispatch == nil {
		readErr = e.mergeIntegration(ctx, &p, &r)
	} else {
		readErr = e.readIntegration(ctx, p, &r)
	}
	if readErr != nil {
		r.State, r.Detail = "unavailable", readErr.Error()
	}
	if p.MergeDispatch != nil && r.State != "confirmed" {
		r.State, r.Detail = "uncertain", "Merge dispatch consumed; no write will repeat. "+r.Detail+" Use task integrate to inspect again."
	}
	r.ObservedAt = e.now()
	t, err := e.Store.Task(context.Background(), p.Intent.TaskID)
	if err != nil {
		return err
	}
	if !r.Request.Matches(t, p) {
		r.State, r.Detail = "stale", "Task authority, base proof or CI changed; observation is historical."
	}
	p.Integration, p.IntegrationRequest, p.Occupying = &r, nil, false
	return e.savePublication(context.Background(), &p)
}

func (e *Engine) readIntegration(ctx context.Context, p core.Publication, r *core.IntegrationAssessment) error {
	if err := e.publicationAuthority(ctx, p.Intent); err != nil {
		return err
	}
	if e.Integrations == nil {
		return errors.New("integration observation adapter is unavailable")
	}
	o, err := e.Integrations.ObserveIntegration(ctx, p)
	if err != nil {
		return err
	}
	r.Observation = &o
	if o.Remote.Number != p.Observation.Number || o.Remote.URL != p.Observation.URL || o.Remote.BranchCommit != p.Intent.Candidate.Commit {
		return errors.New("integration PR identity or head differs from the confirmed candidate")
	}
	switch o.Remote.State {
	case "OPEN":
		r.State, r.Detail = "open", "PR remains open. Follow GitHub's review/protection workflow; applied automatic policy may admit a ready PR, otherwise use task merge or merge on GitHub and inspect again."
		if o.Base != r.Request.Proof.Base {
			r.State, r.Detail = "stale", "Open PR base moved; return the PR to draft if needed, then task verify-base before refreshing CI and integration proof."
		}
		return nil
	case "CLOSED":
		r.State, r.Detail = "closed", "PR closed without merge; retained work does not complete the task or satisfy dependencies. Inspect GitHub and revise or stop the task."
		return nil
	case "MERGED":
		if o.MergeCommit == "" || o.MergedAt.IsZero() || o.Remote.Draft {
			return errors.New("merged PR lacks a complete merge identity")
		}
	default:
		return errors.New("unknown integration PR state")
	}
	q, err := e.Store.Quality(ctx, p.Intent.Quality)
	if err != nil {
		return err
	}
	state, _ := core.AssessCI(q.RequiredCI, o.Checks)
	if state != "passed" {
		return errors.New("selected CI evidence is now " + state + "; old passing CI cannot confirm integration")
	}
	if e.Bases == nil {
		return errors.New("configured base adapter is unavailable")
	}
	r.BaseTip, err = e.Bases.CurrentBase(ctx, e.Settings.Config.Repository.Path, p.Intent.Repository, strings.TrimPrefix(e.Settings.Config.Repository.BaseRef, "refs/heads/"))
	if err != nil {
		return err
	}
	if err = e.git().ConfirmIntegration(ctx, r.Request.Proof, o.MergeCommit, r.BaseTip); err != nil {
		return err
	}
	if err = e.publicationAuthority(ctx, p.Intent); err != nil {
		return err
	}
	r.State, r.Detail = "confirmed", "Exact accepted candidate and verified base confirmed in the configured base history; pinned CI rechecked."
	return nil
}
