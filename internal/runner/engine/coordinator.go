package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/securefs"
)

type finished struct {
	attempt    core.Attempt
	completion execution.Completion
}
type active struct {
	attempt core.Attempt
	cancel  context.CancelFunc
	done    chan finished
}

func (e *Engine) Run(ctx context.Context) error {
	lock, err := execution.Lock(filepath.Join(e.Settings.Config.StateDir, "coordinator.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := e.validate(ctx); err != nil {
		return err
	}
	if err = e.recover(ctx); err != nil {
		return err
	}
	if err = e.Store.SetDrained(ctx, false); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var current *active
	defer func() {
		if current != nil {
			current.cancel()
			f := <-current.done
			_ = e.saveCompletion(context.Background(), f)
		}
	}()
	// A cancelled owner stops admission, cancels its own execution, and waits for
	// ownership evidence before leaving. A hard crash is handled by recover.
	stopping := false
	for {
		if ctx.Err() != nil && !stopping {
			stopping = true
			if err = e.Store.SetDrained(context.Background(), true); err != nil {
				return err
			}
		}
		workCtx := context.Background()
		if current == nil {
			if err = e.recover(workCtx); err != nil {
				return err
			}
		}
		if current != nil {
			t, err := e.Store.Task(workCtx, current.attempt.TaskID)
			if err != nil {
				return err
			}
			if stopping || !current.attempt.Authorized(t) {
				current.cancel()
			}
			select {
			case f := <-current.done:
				current.cancel()
				current = nil
				if err = e.saveCompletion(workCtx, f); err != nil {
					return err
				}
			default:
			}
		}
		if err = e.finishSaved(workCtx); err != nil {
			return err
		}
		_, drained, err := e.Store.Settings(workCtx)
		if err != nil {
			return err
		}
		attempts, err := e.Store.Attempts(workCtx, "")
		if err != nil {
			return err
		}
		occupied := false
		for _, a := range attempts {
			if a.Occupying {
				occupied = true
			}
		}
		if current == nil && drained {
			if !occupied {
				return nil
			}
			if stopping {
				return errors.New("stopped coordinator with unresolved process ownership; reservation retained")
			}
		}
		if current == nil && !drained && !occupied {
			a, ok, err := e.claim(workCtx)
			if err != nil {
				return err
			}
			if ok {
				callCtx, cancel := context.WithCancel(context.Background())
				current = &active{attempt: a, cancel: cancel, done: make(chan finished, 1)}
				done := current.done
				go func() { done <- e.execute(callCtx, a) }()
			}
		}
		if stopping {
			<-ticker.C
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

// mutate reloads the task when a concurrent operator command won the CAS. The
// callback must preserve newer task authority, even when recording late output.
func (e *Engine) mutate(ctx context.Context, id string, change func(*core.Task) (*core.Attempt, error)) error {
	for i := 0; i < 20; i++ {
		t, err := e.Store.Task(ctx, id)
		if err != nil {
			return err
		}
		a, err := change(&t)
		if err != nil {
			return err
		}
		err = e.Store.Commit(ctx, t.Revision, t, a, false)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		return err
	}
	return store.ErrConflict
}
func (e *Engine) recover(ctx context.Context) error {
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if !a.Occupying {
			continue
		}
		if a.Process != nil && !execution.Gone(*a.Process) {
			t, readErr := e.Store.Task(ctx, a.TaskID)
			if readErr != nil {
				return readErr
			}
			if !a.Authorized(t) || (t.Attention != nil && t.Attention.Kind == "ownership" && t.Attention.Attempt == a.ID) {
				continue
			}
			if err = e.mutate(ctx, a.TaskID, func(t *core.Task) (*core.Attempt, error) {
				if a.Authorized(*t) {
					t.Attention = &core.Attention{Kind: "ownership", Detail: "A surviving process group may still mutate its workspace. Ownership is retained; no automatic replay or unrelated PID kill.", Attempt: a.ID}
				}
				return &a, nil
			}); err != nil {
				return err
			}
			continue
		}
		a.Occupying = false
		a.State = "interrupted"
		a.Error = "Coordinator stopped before a durable completed result; explicit recovery is required."
		now := time.Now().UTC()
		a.FinishedAt = &now
		if err = e.mutate(ctx, a.TaskID, func(t *core.Task) (*core.Attempt, error) {
			if a.Authorized(*t) {
				t.Attention = &core.Attention{Kind: "interrupted", Detail: a.Error, Attempt: a.ID}
			}
			return &a, nil
		}); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) claim(ctx context.Context) (core.Attempt, bool, error) {
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return core.Attempt{}, false, err
	}
	for _, t := range tasks {
		if t.Approval == nil || t.NextRole == "" || t.Attention != nil || t.Stage == core.Stopped {
			continue
		}
		if err := e.validate(ctx); err != nil {
			return core.Attempt{}, false, err
		}
		if t.Current().Quality != e.Settings.Quality.Revision() {
			return core.Attempt{}, false, errors.New("approved quality revision changed")
		}
		if err := e.git().CheckQuality(ctx, t.Approval.Base); err != nil {
			return core.Attempt{}, false, err
		}
		name, p := e.Settings.Config.Role(t.NextRole)
		a := core.Attempt{ID: core.NewID(), TaskID: t.ID, Version: t.Current().Number, Generation: t.Generation, Role: t.NextRole, ProfileName: name, Profile: p, PolicyRevision: e.Settings.Config.Revision(), Quality: t.Current().Quality, Base: t.Approval.Base, State: "reserved", Occupying: true, StartedAt: time.Now().UTC(), Candidate: t.Candidate}
		prior, err := e.Store.Attempts(ctx, t.ID)
		if err != nil {
			return a, false, err
		}
		children := map[string]bool{}
		for _, old := range prior {
			children[old.ResumedFrom] = true
		}
		for _, old := range prior {
			if old.Resume == "new" && old.Authorized(t) && old.Role == a.Role && !children[old.ID] {
				a.ResumedFrom = old.ID
				a.ProfileName = old.ProfileName
				a.Profile = old.Profile
			}
		}
		a.EvidenceDir = filepath.Join(e.Settings.Config.StateDir, "attempts", a.ID)
		t.NextRole = ""
		if a.Role == "implementer" {
			t.Stage = core.Implementing
		} else {
			t.Stage = core.Reviewing
		}
		err = e.Store.Commit(ctx, t.Revision, t, &a, true)
		if errors.Is(err, store.ErrConflict) {
			return a, false, nil
		}
		return a, err == nil, err
	}
	return core.Attempt{}, false, nil
}
func required(t core.Task, role string) []core.Proof {
	if role != "verify" {
		return t.Current().Proofs
	}
	proofs := []core.Proof{}
	for _, p := range t.Current().Proofs {
		for _, c := range t.Review {
			if c.ID == p.ID && c.Status == "gap" {
				proofs = append(proofs, p)
			}
		}
	}
	return proofs
}
func (e *Engine) execute(ctx context.Context, a core.Attempt) finished {
	fail := func(err error) finished { return finished{a, execution.Completion{Stopped: true, Error: err.Error()}} }
	t, err := e.Store.Task(ctx, a.TaskID)
	if err != nil {
		return fail(err)
	}
	if !a.Authorized(t) {
		return fail(errors.New("authority revoked before preparation"))
	}
	original, err := workspace.Snapshot(ctx, e.Settings.Config.Repository.Path)
	if err != nil {
		return fail(err)
	}
	r := execution.Request{Attempt: a, Task: t, Quality: e.Settings.Quality, Proofs: required(t, a.Role)}
	var copyManifest workspace.CopyState
	if a.Role == "implementer" {
		m, err := e.git().Prepare(ctx, t)
		if err != nil {
			return fail(err)
		}
		r.Dir = m.WorktreePath
		r.ReadRoots, err = e.git().ReadRoots(ctx, r.Dir)
		if err != nil {
			return fail(err)
		}
	} else {
		review, err := e.git().Review(ctx, t)
		if err != nil {
			return fail(err)
		}
		// Retain evidence copies on failure and crash. They are never authority and
		// cannot be mistaken for the canonical candidate after restart.
		if a.Role == "audit" {
			r.Dir, err = os.MkdirTemp("", "runner-audit-")
			if err == nil {
				r.Dir, err = filepath.EvalSymlinks(r.Dir)
			}
			if err != nil {
				return fail(err)
			}
			r.ReadRoots, err = e.git().ReadRoots(ctx, review.Path)
			if err != nil {
				return fail(err)
			}
		} else {
			r.Dir, copyManifest, err = workspace.Copy(review.Path, "")
			if err != nil {
				return fail(err)
			}
		}
		prior, err := e.Store.Attempts(ctx, t.ID)
		if err != nil {
			return fail(err)
		}
		for _, old := range prior {
			if old.Role == "implementer" && old.State == "applied" && old.Authorized(t) {
				events := filepath.Join(old.EvidenceDir, "events.jsonl")
				_, _, state, err := securefs.ReadFile(events, 32<<20)
				if os.IsNotExist(err) {
					continue
				}
				if err != nil {
					return fail(err)
				}
				if state.Exists {
					r.ReadRoots = append(r.ReadRoots, events)
				}
			}
		}
	}
	a.ExecutionDir = r.Dir
	a.ReadRoots = append([]string(nil), r.ReadRoots...)
	r.Attempt = a
	if err = e.mutate(ctx, a.TaskID, func(t *core.Task) (*core.Attempt, error) {
		if !a.Authorized(*t) {
			return nil, errors.New("authority revoked before execution")
		}
		return &a, nil
	}); err != nil {
		return fail(err)
	}
	started := func(p core.Process) error {
		a.Process = &p
		a.State = "running"
		return e.mutate(context.Background(), a.TaskID, func(t *core.Task) (*core.Attempt, error) {
			if !a.Authorized(*t) {
				return nil, errors.New("authority revoked before launch")
			}
			return &a, nil
		})
	}
	completion := e.Executor.Execute(ctx, r, started)
	if completion.Stopped {
		after, err := workspace.Snapshot(context.Background(), e.Settings.Config.Repository.Path)
		if err != nil || after.Fingerprint != original.Fingerprint {
			completion.Result = nil
			completion.Error = "original checkout changed during execution"
			completion.Kind = "integrity"
		}
		if a.Role == "verify" {
			if err = workspace.VerifyCopy(r.Dir, copyManifest); err != nil {
				completion.Result = nil
				completion.Error = err.Error()
				completion.Kind = "integrity"
			}
		}
		if completion.Result != nil {
			if err = core.ValidateResult(*completion.Result, r.Proofs); err != nil {
				completion.Result = nil
				completion.Error = "invalid result: " + err.Error()
				completion.Kind = "missing_proof"
			}
		}
		if completion.Result != nil && a.Role == "implementer" {
			a.SourceDigest, a.SourceHead, err = e.git().Binding(context.Background(), t)
			if err != nil {
				completion.Result = nil
				completion.Error = err.Error()
				completion.Kind = "integrity"
			}
		}
	}
	return finished{a, completion}
}
func (e *Engine) saveCompletion(ctx context.Context, f finished) error {
	a := f.attempt
	a.Occupying = !f.completion.Stopped
	a.Result = f.completion.Result
	a.Error = f.completion.Error
	if f.completion.Stopped {
		now := time.Now().UTC()
		a.FinishedAt = &now
	}
	kind := "interrupted"
	a.State = "interrupted"
	if !f.completion.Stopped {
		kind = "ownership"
		a.State = "uncertain"
	} else if f.completion.Kind != "" {
		kind = f.completion.Kind
		a.State = "failed"
	} else if a.Result != nil {
		a.State = "completed"
	}
	return e.mutate(ctx, a.TaskID, func(t *core.Task) (*core.Attempt, error) {
		if a.Authorized(*t) && a.State != "completed" {
			t.Attention = &core.Attention{Kind: kind, Detail: a.Error, Attempt: a.ID}
		}
		return &a, nil
	})
}
func (e *Engine) finishSaved(ctx context.Context) error {
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if a.State != "completed" || a.Result == nil || a.Occupying {
			continue
		}
		t, err := e.Store.Task(ctx, a.TaskID)
		if err != nil {
			return err
		}
		if !a.Authorized(t) {
			a.State = "superseded"
			if err = e.Store.Commit(ctx, t.Revision, t, &a, false); errors.Is(err, store.ErrConflict) {
				continue
			}
			if err != nil {
				return err
			}
			continue
		}
		advanceErr := e.advance(ctx, &t, &a)
		if advanceErr != nil {
			a.State = "saved_result"
			a.Error = advanceErr.Error()
			t.Attention = &core.Attention{Kind: "saved_result", Detail: advanceErr.Error(), Attempt: a.ID}
		}
		err = e.Store.Commit(ctx, t.Revision, t, &a, false)
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) advance(ctx context.Context, t *core.Task, a *core.Attempt) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	if a.Quality != e.Settings.Quality.Revision() {
		return errors.New("saved quality binding changed")
	}
	if err := core.ValidateResult(*a.Result, required(*t, a.Role)); err != nil {
		return err
	}
	checks := a.Result.Checks
	outcome := core.ReviewOutcome(checks)
	if a.Result.Outcome == "needs_input" {
		outcome = "needs_input"
	} else if a.Result.Outcome != "succeeded" && outcome == "accepted" {
		outcome = "gap"
	}
	if a.Role == "implementer" {
		d, head, err := e.git().Binding(ctx, *t)
		if err != nil {
			return err
		}
		if d != a.SourceDigest {
			return errors.New("saved implementation content binding changed")
		}
		// Before construction, HEAD must still match the saved implementation.
		// A constructing state records Runner's own idempotent commit boundary.
		if a.SourceHead != head {
			if !a.Constructing {
				return errors.New("saved implementation HEAD changed")
			}
			if err := e.git().ValidateConstruction(ctx, *t, *a); err != nil {
				return err
			}
		}
		if outcome != "accepted" {
			a.State = "applied"
			t.Attention = &core.Attention{Kind: outcome, Detail: a.Result.Summary, Attempt: a.ID}
			return nil
		}
		if !a.Constructing {
			a.Constructing = true
			if err = e.Store.Commit(ctx, t.Revision, *t, a, false); err != nil {
				return err
			}
			t.Revision++
		}
		candidate, err := e.git().Construct(ctx, *t, *a)
		if err != nil {
			return err
		}
		a.Candidate = &candidate
		t.Candidate = &candidate
		t.Review = checks
		t.Stage = core.Reviewing
		t.NextRole = "audit"
	} else {
		if a.Candidate == nil || t.Candidate == nil || *a.Candidate != *t.Candidate {
			return errors.New("saved review candidate binding changed")
		}
		if err := e.git().ValidateCandidate(ctx, *t); err != nil {
			return err
		}
		if a.Role == "verify" {
			for _, c := range checks {
				for i, old := range t.Review {
					if old.ID == c.ID {
						t.Review[i] = c
					}
				}
			}
			checks = t.Review
			outcome = core.ReviewOutcome(checks)
			if a.Result.Outcome == "needs_input" {
				outcome = "needs_input"
			} else if a.Result.Outcome != "succeeded" && outcome == "accepted" {
				outcome = "gap"
			}
		} else {
			t.Review = checks
		}
		switch outcome {
		case "accepted":
			t.Stage = core.AwaitingDelivery
			t.NextRole = ""
			t.Attention = nil
		case "gap":
			if a.Role == "audit" && a.Result.Outcome == "succeeded" {
				t.NextRole = "verify"
			} else {
				t.Attention = &core.Attention{Kind: "missing_proof", Detail: a.Result.Summary, Attempt: a.ID}
			}
		default:
			t.Attention = &core.Attention{Kind: outcome, Detail: a.Result.Summary, Attempt: a.ID}
		}
	}
	a.State = "applied"
	return nil
}
