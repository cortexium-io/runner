package engine

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// ChecksHost reads hosted checks and exact remote identity. It cannot rerun jobs,
// change a PR, choose required checks or decide task completion.
type ChecksHost interface {
	ObserveCI(context.Context, core.PublicationIntent) (core.CIObservation, error)
}

// Project eligibility without mutating authority. The store checks the source
// assessment again when reserving a repair, alongside policy and ownership.
func ciRepairTask(t core.Task, publications []core.Publication) core.Task {
	if p := latestPublication(publications, t); p != nil && p.CanRepairCI(t) {
		t.CIRepair = &core.CIRepair{PublicationID: p.Intent.ID, Assessment: *p.CI, SourceWorkspaceRepair: t.WorkspaceRepair}
		t.NextRole = "implementer"
		t.Pending = &core.Continuation{Kind: "ci_repair", Reason: "Required CI failed; diagnose and repair within the approved scope without model escalation"}
	}
	return t
}

func (e *Engine) AssessCI(ctx context.Context, id string) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	all, err := e.Store.Publications(ctx, id)
	if err != nil {
		return err
	}
	p := latestPublication(all, t)
	if p == nil || !p.CanAssessCI(t) {
		return errors.New("CI assessment requires a confirmed current draft and exact current-base proof")
	}
	if p.CIRequest != nil {
		return nil
	}
	if p.Occupying {
		return errors.New("publication still owns its reservation")
	}
	p.CIRequest = t.BaseProof
	return e.savePublication(ctx, p)
}

func (e *Engine) assessCI(ctx context.Context, p core.Publication) error {
	result := core.CIAssessment{Proof: *p.CIRequest, State: "unavailable"}
	err := e.readCI(ctx, p, &result)
	if err != nil {
		result.State, result.Detail = "unavailable", err.Error()
	}
	result.ObservedAt = e.now()
	t, readErr := e.Store.Task(context.Background(), p.Intent.TaskID)
	if readErr != nil {
		return readErr
	}
	if !p.CanAssessCI(t) || core.Digest(result.Proof) != core.Digest(t.BaseProof) {
		result.State, result.Detail = "stale", "Task authority or current-base proof changed; retained observation is historical."
	}
	p.CI, p.CIRequest, p.Occupying = &result, nil, false
	return e.savePublication(context.Background(), &p)
}

func (e *Engine) readCI(ctx context.Context, p core.Publication, result *core.CIAssessment) error {
	if err := e.publicationAuthority(ctx, p.Intent); err != nil {
		return err
	}
	q, err := e.Store.Quality(ctx, p.Intent.Quality)
	if err != nil {
		return err
	}
	if len(q.RequiredCI) == 0 {
		result.State, result.Detail = "unconfigured", "Pinned quality agreement has no required_ci list; configure and explicitly adopt an agreement before claiming CI success."
		return nil
	}
	if e.Checks == nil {
		return errors.New("hosted checks adapter is unavailable")
	}
	observation, err := e.Checks.ObserveCI(ctx, p.Intent)
	if err != nil {
		return err
	}
	result.Observation = &observation
	o := observation.Remote
	if o.Number != p.Observation.Number || o.URL != p.Observation.URL || o.BranchCommit != p.Intent.Candidate.Commit || o.State != "OPEN" || !o.Draft {
		return errors.New("CI observation differs from the known open draft and candidate; inspect remote identity")
	}
	if observation.Base != result.Proof.Base {
		result.State, result.Detail = "stale", "Remote base moved; request task verify-base before reassessing CI."
		return nil
	}
	if observation.Commit == "" || (observation.Source != "head" && observation.Source != "test-merge") || (observation.Source == "head" && observation.Commit != p.Intent.Candidate.Commit) {
		return errors.New("CI observation has no exact selected commit")
	}
	if err = e.publicationAuthority(ctx, p.Intent); err != nil {
		return err
	}
	result.State, result.Results = core.AssessCI(q.RequiredCI, observation.Checks)
	result.Detail = map[string]string{
		"passed":      "Pinned CI checks passed at this observation; host protections and merge remain unverified.",
		"pending":     "Required CI is still running; reassess after it settles.",
		"missing":     "A required CI result is missing, including jobs from the wrong producer; inspect workflow triggers and reassess.",
		"failed":      "Required CI failed; complete explicit failures are eligible for bounded repair. Inspect other outcomes before reassessment.",
		"unavailable": "Required CI contains unknown or duplicate results; inspect the provider before reassessment.",
	}[result.State]
	return nil
}
