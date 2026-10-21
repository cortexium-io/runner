package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

// prepareCompletions derives short deterministic Git effects. Model calls and
// combined checks remain ordinary asynchronously owned coordinator attempts.
func (e *Engine) prepareCompletions(ctx context.Context) error {
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return err
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	settings, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	e.Settings = settings
	for _, t := range tasks {
		busy := false
		for _, a := range attempts {
			if a.TaskID == t.ID && (a.Occupying || a.State == "completed") {
				busy = true
			}
		}
		if busy {
			continue
		}
		pending := false
		for _, w := range t.RefWrites {
			if !w.Pending() {
				continue
			}
			pending = true
			if (w.State == "uncertain" && !t.LocalInspect) || (w.State == "prepared" && t.Attention != nil) {
				continue
			}
			if drained && w.State == "prepared" {
				continue
			}
			if err = e.advanceRef(ctx, t, w); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			break
		}
		if pending || drained || t.Stage == core.Completed || t.Stage == core.Stopped || t.Approval == nil || t.Attention != nil || t.NextRole != "" || t.Inclusion != nil {
			continue
		}
		if core.PrerequisiteWait(t, tasks) != "" {
			continue
		}
		if t.Release != nil && t.Candidate == nil {
			if t.PlanHead == "" {
				w := e.refIntent(t, "plan", t.PlanRef(), "", t.Approval.Base)
				if err = e.Store.SaveRefWrite(ctx, t.Revision, w); err != nil && !errors.Is(err, store.ErrConflict) {
					return err
				}
			} else if core.AllIncluded(t, tasks) {
				t.Stage, t.NextRole = core.Reviewing, "combine"
				if err = e.Store.Commit(ctx, t.Revision, t, nil, false); err != nil && !errors.Is(err, store.ErrConflict) {
					return err
				}
			}
			continue
		}
		if t.Stage != core.AwaitingDelivery || t.Candidate == nil {
			continue
		}
		if t.ParentID == "" && e.Settings.Config.Delivery.Mode != "local" {
			continue
		}
		if t.BaseProof == nil || !t.BaseProof.Matches(t) {
			t.NextRole = "refresh"
			if err = e.Store.Commit(ctx, t.Revision, t, nil, false); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			continue
		}
		var w core.RefWrite
		if t.ParentID != "" {
			for _, parent := range tasks {
				if parent.ID == t.ParentID {
					if parent.PlanHead != t.BaseProof.Base {
						t.BaseProof = nil
						t.NextRole = "refresh"
						err = e.Store.Commit(ctx, t.Revision, t, nil, false)
						break
					}
					w = e.refIntent(t, "inclusion", parent.PlanRef(), parent.PlanHead, t.Candidate.Commit)
					w.Release = parent.Release.Preview
				}
			}
			if err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			if w.ID == "" {
				continue
			}
		} else {
			result := currentResult(t)
			if result == nil {
				n := 0
				for _, old := range t.RefWrites {
					if old.Kind == "result" {
						n++
					}
				}
				ref := fmt.Sprintf("refs/heads/runner/result/%s-v%d", t.ID, t.Current().Number)
				if n > 0 {
					ref += fmt.Sprintf("-r%d", n)
				}
				w = e.refIntent(t, "result", ref, "", t.Candidate.Commit)
				w.VerifyRef, w.VerifyCommit = e.Settings.Config.Repository.BaseRef, t.BaseProof.Base
			} else if e.Settings.Config.LocalTarget() == "base" && (localMergeAuthorized(t) || t.LocalInspect || e.Settings.Config.MergePolicy() == "automatic") {
				kind := "base"
				if t.LocalInspect && !localMergeAuthorized(t) {
					kind = "manual"
				}
				w = e.refIntent(t, kind, e.Settings.Config.Repository.BaseRef, t.BaseProof.Base, t.Candidate.Commit)
				if kind == "base" && !localMergeAuthorized(t) {
					w.AutomaticPolicy = e.Settings.Config.Revision()
				}
			} else {
				continue
			}
		}
		if err = e.Store.SaveRefWrite(ctx, t.Revision, w); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return nil
}
func currentResult(t core.Task) *core.RefWrite {
	for i := len(t.RefWrites) - 1; i >= 0; i-- {
		w := t.RefWrites[i]
		if w.Kind == "result" && w.State == "confirmed" && t.Candidate != nil && w.Commit == t.Candidate.Commit {
			return &w
		}
	}
	return nil
}
func (e *Engine) refIntent(t core.Task, kind, ref, old, commit string) core.RefWrite {
	w := core.RefWrite{ID: core.NewID(), Kind: kind, TaskID: t.ID, Version: t.Current().Number, Generation: t.Generation, Ref: ref, Old: old, Commit: commit, Proof: t.BaseProof, State: "prepared"}
	if t.Release != nil {
		w.Release = t.Release.Preview
	}
	return w
}
func (e *Engine) advanceRef(ctx context.Context, t core.Task, w core.RefWrite) error {
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return err
	}
	lock, err := pinned.git().LockRefs(ctx)
	if errors.Is(err, workspace.ErrRefsBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if w.Kind != "plan" && w.Authorized(t) {
		if err = pinned.git().ValidateCandidate(ctx, t); err != nil {
			return e.localAttention(ctx, t, err)
		}
	}
	if w.State == "prepared" {
		if !w.Authorized(t) {
			w.State, w.Detail = "stale", "Local write authority changed before dispatch; inspect or request delivery again."
			return e.Store.SaveRefWrite(ctx, t.Revision, w)
		}
		if w.AutomaticPolicy != "" && e.Settings.Config.MergePolicy() != "automatic" {
			w.State, w.Detail = "rejected", "Automatic merge disabled before dispatch; explicit human permission is required."
			return e.Store.SaveRefWrite(ctx, t.Revision, w)
		}
		if w.Kind == "base" {
			if err = pinned.git().TargetAvailable(ctx, w.Ref); err != nil {
				return e.localAttention(ctx, t, err)
			}
		}
		if w.Kind != "manual" {
			if err = pinned.git().CheckRefIntent(ctx, w); err != nil {
				cause := err
				w.State, w.Detail = "rejected", err.Error()
				if err = e.Store.SaveRefWrite(ctx, t.Revision, w); err != nil {
					return err
				}
				if errors.Is(cause, workspace.ErrRefMoved) {
					return e.mutateLocalRefresh(ctx, t.ID)
				}
				latest, readErr := e.Store.Task(ctx, t.ID)
				if readErr != nil {
					return readErr
				}
				return e.localAttention(ctx, latest, errors.New(w.Detail))
			}
		}
		w.State = "dispatched"
		if err = e.Store.SaveRefWrite(ctx, t.Revision, w); err != nil {
			return err
		}
		t, err = e.Store.Task(ctx, t.ID)
		if err != nil {
			return err
		}
		// Authority is reloaded immediately before the effect. A stop racing with an
		// already dispatched effect cannot complete the task in SaveRefWrite.
		settings, _, readErr := e.Store.Settings(ctx)
		if readErr != nil {
			return readErr
		}
		if !w.Authorized(t) {
			w.State, w.Detail = "stale", "Authority revoked after dispatch reservation; no write sent."
			return e.Store.SaveRefWrite(ctx, t.Revision, w)
		}
		if w.AutomaticPolicy != "" && settings.Config.MergePolicy() != "automatic" {
			w.State, w.Detail = "rejected", "Automatic merge disabled after reservation; no write sent."
			return e.Store.SaveRefWrite(ctx, t.Revision, w)
		}
		if w.Kind != "manual" {
			err = pinned.git().ApplyRef(ctx, w)
			if errors.Is(err, workspace.ErrRefMoved) {
				w.State, w.Detail = "rejected", "Expected ref changed; accepted source needs fresh base verification."
				if err = e.Store.SaveRefWrite(ctx, t.Revision, w); err != nil {
					return err
				}
				return e.mutateLocalRefresh(ctx, t.ID)
			}
			if err != nil {
				w.State, w.Detail = "uncertain", err.Error()+"; use task delivery --reconcile to inspect without repeating the write"
				return e.Store.SaveRefWrite(ctx, t.Revision, w)
			}
		}
	}
	tip, err := pinned.git().Ref(ctx, w.Ref)
	match := err == nil && tip == w.Commit
	if !match && err == nil && w.Kind == "base" {
		match = pinned.git().Contains(ctx, tip, w.Commit) == nil
	}
	if match && w.Kind == "manual" {
		match = pinned.git().Contains(ctx, w.Commit, w.Proof.Base) == nil
	}
	w.ObservedAt = e.now()
	if !match {
		w.State, w.Detail = "uncertain", "Ref does not confirm the exact dispatched result; inspect Git and use task delivery --reconcile. The write will not repeat."
	} else {
		w.State, w.Detail, w.ObservedAt = "confirmed", "Exact local effect observed.", e.now()
	}
	return e.Store.SaveRefWrite(ctx, t.Revision, w)
}
func (e *Engine) localAttention(ctx context.Context, t core.Task, err error) error {
	t.Attention = &core.Attention{Kind: "local_delivery", Detail: err.Error()}
	return e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) mutateLocalRefresh(ctx context.Context, id string) error {
	return e.mutate(ctx, id, func(t *core.Task) (*core.Attempt, error) {
		if t.Stage != core.Stopped && t.Stage != core.Completed {
			t.Attention, t.BaseProof = nil, nil
			t.NextRole = "refresh"
		}
		return nil, nil
	})
}
func (e *Engine) requestLocalIntegration(ctx context.Context, t core.Task, merge bool) error {
	if t.ParentID != "" {
		return fmt.Errorf("child delivery belongs to Plan %s", t.ParentID)
	}
	if e.Settings.Config.LocalTarget() != "base" {
		return errors.New("local result delivery completes automatically; no base merge is configured")
	}
	if t.Stage != core.AwaitingDelivery || t.Candidate == nil || t.BaseProof == nil || currentResult(t) == nil {
		return errors.New("local integration requires an accepted result branch and current-base proof")
	}
	if t.Attention != nil && t.Attention.Kind != "local_delivery" {
		return errors.New("resolve existing attention first")
	}
	t.Attention = nil
	if merge {
		for _, w := range t.RefWrites {
			if w.Kind == "base" && (w.State == "dispatched" || w.State == "uncertain") {
				return errors.New("local merge was dispatched; task integrate inspects without repeating the write")
			}
		}
		t.LocalMerge = t.BaseProof
	} else {
		t.LocalInspect = true
	}
	return e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) executeCombined(ctx context.Context, t core.Task, a core.Attempt) finished {
	fail := func(err error) finished {
		return finished{a, execution.Completion{Stopped: true, Error: err.Error(), Kind: core.ProofGap}}
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return fail(err)
	}
	if !core.AllIncluded(t, tasks) {
		return fail(errors.New("released Plan members are not all included"))
	}
	c, err := e.git().Combined(ctx, t)
	if err != nil {
		return fail(err)
	}
	a.Candidate = &c
	// Observe the destination before expensive final proof. A moved base uses
	// the ordinary bounded refresh/repair path and checks all child guarantees.
	observed, err := e.observeBase(ctx, t)
	if err != nil {
		return fail(err)
	}
	if observed != t.Base() || t.RefreshTarget != nil {
		if err = e.mutate(ctx, t.ID, func(current *core.Task) (*core.Attempt, error) {
			if !a.Authorized(*current) {
				return nil, errors.New("Plan authority changed before final refresh")
			}
			current.Candidate = &c
			return &a, nil
		}); err != nil {
			return fail(err)
		}
		t, err = e.Store.Task(ctx, t.ID)
		if err != nil {
			return fail(err)
		}
		f := e.executeRefresh(ctx, t, a)
		if f.completion.Error != "" || f.attempt.Refreshed != nil || f.attempt.ConflictRepair != nil {
			return f
		}
	}
	files, err := workspace.Manifest(c.Workspace)
	if err != nil {
		return fail(err)
	}
	q := e.Settings.Quality.For(t, true)
	a.CheckFiles = exceptionFiles(q, files)
	a.Checks, err = e.runChecks(ctx, c.Workspace, files, q, filepath.Join(a.EvidenceDir, "quality"), func(p core.Process) error {
		a.Process, a.State = &p, "checking"
		return e.mutate(context.Background(), t.ID, func(current *core.Task) (*core.Attempt, error) {
			if !a.Authorized(*current) {
				return nil, errors.New("combined check authority revoked")
			}
			return &a, nil
		})
	})
	if err != nil {
		return fail(err)
	}
	for _, check := range a.Checks {
		if !check.Stopped {
			return finished{a, execution.Completion{Error: "combined check process ownership unresolved", Kind: core.ProofGap}}
		}
		if check.Status == "unavailable" {
			return fail(fmt.Errorf("combined check %s unavailable: %s", check.CheckID, check.Detail))
		}
	}
	return finished{a, execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Combined source retained with completion checks; independent outcome review required"}}}
}

func (e *Engine) planContext(ctx context.Context, t core.Task) (*execution.PlanContext, error) {
	parent := t
	var err error
	if t.ParentID != "" {
		parent, err = e.Store.Task(ctx, t.ParentID)
		if err != nil {
			return nil, err
		}
	}
	if parent.Release == nil || parent.Proposal == nil {
		return nil, errors.New("released Plan context missing")
	}
	result := &execution.PlanContext{OriginalRequest: parent.Current().Body, SharedBody: parent.Proposal.Body}
	for _, id := range parent.Release.Children {
		child, err := e.Store.Task(ctx, id)
		if err != nil {
			return nil, err
		}
		result.Children = append(result.Children, child)
		if t.ParentID == "" {
			attempts, err := e.Store.Attempts(ctx, id)
			if err != nil {
				return nil, err
			}
			for _, a := range attempts {
				if a.State == "applied" && a.Candidate != nil && child.Candidate != nil && *a.Candidate == *child.Candidate && a.Version == child.Current().Number && a.Generation == child.Generation {
					result.Evidence = append(result.Evidence, a)
				}
			}
		}
	}
	return result, nil
}
func (e *Engine) combinedChecks(ctx context.Context, t core.Task) error {
	attempts, err := e.Store.Attempts(ctx, t.ID)
	if err != nil {
		return err
	}
	for i := len(attempts) - 1; i >= 0; i-- {
		a := attempts[i]
		if (a.Role == "combine" || a.Role == "implementer" || a.Role == "refresh" && a.Refreshed != nil) && a.State == "applied" && a.Candidate != nil && t.Candidate != nil && *a.Candidate == *t.Candidate && a.Generation == t.Generation && a.Version == t.Current().Number {
			return e.Settings.Quality.For(t, a.Role == "combine" && a.Refreshed == nil).ValidateResults(a.Checks, a.CheckFiles)
		}
	}
	return errors.New("combined candidate lacks required check evidence")
}

func localMergeAuthorized(t core.Task) bool {
	return t.LocalMerge != nil && t.BaseProof != nil && core.Digest(t.LocalMerge) == core.Digest(t.BaseProof)
}
