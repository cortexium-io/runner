package engine

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// MergeHost admits the host's normal protected merge, never a bypass or queue.
// The callback rechecks local authority immediately before the mutation.
type MergeHost interface {
	ObserveMerge(context.Context, core.Publication) (core.MergeObservation, error)
	Merge(context.Context, core.Publication, core.MergeAdmission, func() error) error
}

func (e *Engine) mergeAuthority(ctx context.Context, p core.Publication) error {
	if err := e.publicationAuthority(ctx, p.Intent); err != nil {
		return err
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID == p.Intent.TaskID {
			if p.IntegrationRequest == nil || !p.IntegrationRequest.Merge || !p.IntegrationRequest.Matches(t, p) {
				return errors.New("merge evidence or authority changed")
			}
			if wait := core.PrerequisiteWait(t, tasks); wait != "" {
				return errors.New(wait)
			}
			if p.IntegrationRequest.AutomaticPolicy != "" {
				settings, _, err := e.Store.Settings(ctx)
				if err != nil {
					return err
				}
				if settings.Config.MergePolicy() != "automatic" {
					return errors.New("automatic merge policy is disabled; request a new inspection after enabling it, or use task merge explicitly")
				}
			}
			return ctx.Err()
		}
	}
	return errors.New("merge task missing")
}

func (e *Engine) mergeIntegration(ctx context.Context, p *core.Publication, r *core.IntegrationAssessment) error {
	if e.Merges == nil || e.Bases == nil || e.Integrations == nil {
		return errors.New("merge, base or integration adapter unavailable")
	}
	authorize := func() error { return e.mergeAuthority(ctx, *p) }
	if err := authorize(); err != nil {
		return err
	}
	q, err := e.Store.Quality(ctx, p.Intent.Quality)
	if err != nil {
		return err
	}
	// The second read is after fetching the base, so host policy and CI are fresh
	// at dispatch. Strict host protection handles competing advancement afterward.
	for read := 0; read < 2; read++ {
		o, err := e.Merges.ObserveMerge(ctx, *p)
		r.Observation, r.Admission = &o.IntegrationObservation, &o.Admission
		if err != nil {
			return err
		}
		if o.Remote.Number != p.Observation.Number || o.Remote.URL != p.Observation.URL || o.Remote.BranchCommit != p.Intent.Candidate.Commit {
			return errors.New("merge PR identity or head changed")
		}
		if o.Remote.State == "MERGED" {
			return e.readIntegration(ctx, *p, r)
		}
		if o.Remote.State != "OPEN" || o.Remote.Draft || !o.Admission.Eligible {
			r.State, r.Detail = "blocked", "Merge admission blocked: "+o.Admission.Detail
			return nil
		}
		if o.Base != r.Request.Proof.Base {
			r.State, r.Detail = "stale", "Base moved; return the PR to draft and use task verify-base before fresh review, synchronization and CI."
			return nil
		}
		if state, _ := core.AssessCI(q.RequiredCI, o.Checks); state != "passed" {
			r.State, r.Detail = "blocked", "Selected CI is now "+state+"; resolve and reassess before requesting merge again."
			return nil
		}
		if read == 0 {
			r.BaseTip, err = e.Bases.CurrentBase(ctx, e.Settings.Config.Repository.Path, p.Intent.Repository, p.Intent.BaseBranch)
			if err != nil {
				return err
			}
			if r.BaseTip != r.Request.Proof.Base {
				r.State, r.Detail = "stale", "Fetched base moved; return the PR to draft and use task verify-base before requesting merge again."
				return nil
			}
		}
	}
	if err = authorize(); err != nil {
		return err
	}
	// Persist before sending. Any subsequent restart is observation-only, even
	// when the process died before reaching the transport or the host rejected it.
	r.State, r.Detail, r.ObservedAt = "uncertain", "Conditional merge dispatch reserved; outcome requires inspection.", e.now()
	previous := p.Integration
	p.MergeDispatch = &core.MergeDispatch{Request: r.Request, Admission: *r.Admission, RequestedAt: r.ObservedAt}
	p.Integration = r
	if err = e.savePublication(ctx, p); err != nil {
		p.MergeDispatch, p.Integration = nil, previous
		return err
	}
	writeErr := e.Merges.Merge(ctx, *p, *r.Admission, authorize)
	readErr := e.readIntegration(ctx, *p, r)
	if readErr == nil && r.State == "confirmed" {
		return nil
	}
	return errors.Join(writeErr, readErr)
}
