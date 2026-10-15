package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

// BaseHost imports the explicit delivery base. It cannot change task authority.
type BaseHost interface {
	CurrentBase(ctx context.Context, path, repository, branch string) (string, error)
}

func (e *Engine) VerifyBase(ctx context.Context, id string) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return err
	}
	publications, err := e.Store.Publications(ctx, id)
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if a.Occupying {
			return errors.New("task execution still owns its reservation")
		}
	}
	for _, p := range publications {
		if p.Occupying {
			return errors.New("publication still owns its reservation")
		}
	}
	return e.mutate(ctx, id, func(t *core.Task) (*core.Attempt, error) {
		if p := latestPublication(publications, *t); p != nil && (p.Occupying || p.State != "published") {
			return nil, errors.New("settle publication with task delivery " + id + " --reconcile before requesting another base refresh")
		}
		if t.Stage != core.AwaitingDelivery || t.Approval == nil || t.Candidate == nil || core.ReviewOutcome(t.Review) != core.Accepted {
			return nil, errors.New("base verification requires an independently accepted candidate")
		}
		if t.Attention != nil {
			refreshFailure := false
			for _, a := range attempts {
				if a.ID == t.Attention.Attempt && a.Role == "refresh" && a.State != "saved_result" {
					refreshFailure = true
				}
			}
			if !refreshFailure {
				return nil, errors.New("resolve the existing attention before base verification")
			}
		}
		t.Attention, t.BaseProof, t.Pending = nil, nil, nil
		t.NextRole = "refresh"
		return nil, nil
	})
}

func (e *Engine) prepareBaseChecks(ctx context.Context) error {
	if e.Settings.Config.Delivery.Mode != "github-draft" {
		return nil
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	publications, err := e.Store.Publications(ctx, "")
	if err != nil {
		return err
	}
	for _, p := range publications {
		if p.State != "published" || p.Occupying {
			continue
		}
		t, err := e.Store.Task(ctx, p.Intent.TaskID)
		if err != nil {
			return err
		}
		if !p.Intent.Authorized(t) || t.NextRole != "" || t.BaseProof != nil {
			continue
		}
		seen := false
		for _, a := range attempts {
			if a.TaskID == t.ID && a.Version == t.Current().Number && a.Generation == t.Generation && a.Role == "refresh" {
				seen = true
			}
		}
		if seen {
			continue
		}
		if err = e.mutate(ctx, t.ID, func(current *core.Task) (*core.Attempt, error) {
			if p.Intent.Authorized(*current) && current.NextRole == "" && current.BaseProof == nil {
				current.NextRole = "refresh"
			}
			return nil, nil
		}); err != nil && !errors.Is(err, store.ErrPublicationPending) {
			return err
		}
	}
	return nil
}

func (e *Engine) executeRefresh(ctx context.Context, t core.Task, a core.Attempt) finished {
	stopped := true
	fail := func(err error) finished {
		return finished{a, execution.Completion{Stopped: stopped, Error: err.Error(), Kind: core.FailureKind("base_verification")}}
	}
	if err := e.git().ValidateCandidate(ctx, t); err != nil {
		return fail(err)
	}
	var err error
	if t.RefreshTarget == nil {
		if e.Settings.Config.Delivery.Mode == "local" {
			_, a.ObservedBase, err = workspace.Observe(ctx, e.Settings.Config)
		} else if e.Bases == nil {
			err = errors.New("delivery base adapter unavailable")
		} else {
			a.ObservedBase, err = e.Bases.CurrentBase(ctx, e.Settings.Config.Repository.Path, e.Settings.Config.Delivery.Repository, strings.TrimPrefix(e.Settings.Config.Repository.BaseRef, "refs/heads/"))
		}
		if err != nil {
			return fail(err)
		}
		a.BaseObservedAt = e.now()
		if a.ObservedBase == "" {
			return fail(errors.New("base observation is missing"))
		}
		if a.ObservedBase != t.Base() {
			err = e.mutate(ctx, t.ID, func(current *core.Task) (*core.Attempt, error) {
				if !a.Authorized(*current) || current.Candidate == nil || *current.Candidate != *t.Candidate {
					return nil, errors.New("refresh authority changed")
				}
				settings, _, err := e.Store.Settings(ctx)
				if err != nil {
					return nil, err
				}
				if current.Usage.BaseRefreshes >= settings.Config.Policy.MaxBaseRefreshes {
					return nil, errors.New("base refresh limit reached; apply more base-refreshes allowance and request task verify-base again")
				}
				current.Usage.BaseRefreshes++
				current.RefreshTarget = &core.BaseBinding{Commit: a.ObservedBase, Cycle: current.Usage.BaseRefreshes}
				current.BaseProof = nil
				return &a, nil
			})
			if err != nil {
				return fail(err)
			}
			t, err = e.Store.Task(ctx, t.ID)
			if err != nil {
				return fail(err)
			}
		}
	}
	if t.RefreshTarget != nil {
		a.Refreshed = t.RefreshTarget
		a.ObservedBase = t.RefreshTarget.Commit
		candidate, err := e.git().Refresh(ctx, t, *t.RefreshTarget)
		if err != nil {
			return fail(err)
		}
		a.Candidate = &candidate
		files, err := workspace.Manifest(candidate.Workspace)
		if err != nil {
			return fail(err)
		}
		a.CheckFiles = map[string]string{}
		for _, ex := range e.Settings.Quality.Exceptions {
			for name := range ex.Files {
				a.CheckFiles[name] = files[name]
			}
		}
		a.Checks, err = e.runChecks(ctx, candidate.Workspace, files, e.Settings.Quality, filepath.Join(a.EvidenceDir, "quality"), func(p core.Process) error {
			a.Process, a.State = &p, "checking"
			return e.mutate(context.Background(), t.ID, func(current *core.Task) (*core.Attempt, error) {
				if !a.Authorized(*current) {
					return nil, errors.New("refresh authority revoked before check")
				}
				return &a, nil
			})
		})
		for _, check := range a.Checks {
			if !check.Stopped {
				stopped = false
			}
		}
		if err != nil {
			return fail(err)
		}
		if err = e.Settings.Quality.ValidateResults(a.Checks, files); err != nil {
			return fail(err)
		}
	}
	return finished{a, execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Base observed; refreshed checks completed when needed"}}}
}

func (e *Engine) advanceRefresh(ctx context.Context, t *core.Task, a *core.Attempt) error {
	if a.Candidate == nil || a.ObservedBase == "" {
		return errors.New("saved base result is incomplete")
	}
	if a.Refreshed != nil {
		if t.RefreshTarget == nil || *t.RefreshTarget != *a.Refreshed || a.Refreshed.Commit != a.ObservedBase {
			return errors.New("saved refresh target changed")
		}
		if err := e.Settings.Quality.ValidateResults(a.Checks, a.CheckFiles); err != nil {
			return err
		}
		next := *t
		next.WorkingBase, next.Candidate = a.Refreshed, a.Candidate
		if err := e.git().ValidateCandidate(ctx, next); err != nil {
			return err
		}
		t.WorkingBase, t.Candidate = a.Refreshed, a.Candidate
		t.RefreshTarget, t.BaseProof, t.Review = nil, nil, nil
		t.Stage, t.NextRole = core.Reviewing, "audit"
	} else {
		if a.BaseObservedAt.IsZero() || t.Base() != a.ObservedBase || t.Candidate == nil || *t.Candidate != *a.Candidate {
			return errors.New("saved base or candidate changed")
		}
		if err := e.git().ValidateCandidate(ctx, *t); err != nil {
			return err
		}
		if err := core.ValidateResult(core.Result{Outcome: "succeeded", Summary: "accepted review", Checks: t.Review}, t.Proofs()); err != nil {
			return err
		}
		if core.ReviewOutcome(t.Review) != core.Accepted {
			return errors.New("base verification lacks accepted review")
		}
		t.BaseProof = &core.BaseProof{Base: a.ObservedBase, Candidate: *t.Candidate, Quality: t.Current().Quality, ReviewDigest: core.Digest(t.Review), ObservedAt: a.BaseObservedAt}
		t.Stage, t.NextRole = core.AwaitingDelivery, ""
	}
	t.Attention = nil
	a.State = "applied"
	return nil
}
