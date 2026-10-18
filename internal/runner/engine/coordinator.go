package engine

import (
	"context"
	"errors"
	"fmt"
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
	// The coordinator refreshes its own policy snapshot. Workers receive copies,
	// so live edits cannot race or alter an admitted invocation's settings.
	owner := *e
	e = &owner
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
	current := map[string]*active{}
	publications := map[string]*activePublication{}
	var projectionDone chan error
	var projectionCancel context.CancelFunc
	var nextProjection time.Time
	defer func() {
		if projectionDone != nil {
			projectionCancel()
		}
		for _, call := range publications {
			call.cancel()
		}
		for _, call := range current {
			call.cancel()
		}
		if projectionDone != nil {
			<-projectionDone
		}
		for _, call := range current {
			f := <-call.done
			_ = e.saveCompletion(context.Background(), f)
		}
		for _, call := range publications {
			<-call.done
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
		settings, _, readErr := e.Store.Settings(workCtx)
		if readErr != nil {
			return readErr
		}
		e.Settings = settings
		if projectionDone != nil {
			select {
			case <-projectionDone:
				projectionCancel()
				projectionDone = nil
				nextProjection = e.now().Add(30 * time.Second)
			default:
			}
		}
		allTasks, readErr := e.Store.Tasks(workCtx)
		if readErr != nil {
			return readErr
		}
		for id, call := range publications {
			t, readErr := e.Store.Task(workCtx, call.intent.TaskID)
			if readErr != nil {
				return readErr
			}
			if stopping || !call.intent.Authorized(t) || core.PrerequisiteWait(t, allTasks) != "" {
				call.cancel()
			}
			select {
			case deliveryErr := <-call.done:
				call.cancel()
				delete(publications, id)
				if deliveryErr != nil {
					return deliveryErr
				}
			default:
			}
		}
		if err = e.recoverExcept(workCtx, current); err != nil {
			return err
		}
		for id, call := range current {
			t, readErr := e.Store.Task(workCtx, call.attempt.TaskID)
			if readErr != nil {
				return readErr
			}
			if stopping || !call.attempt.Authorized(t) || core.PrerequisiteWait(t, allTasks) != "" {
				call.cancel()
			}
			select {
			case f := <-call.done:
				call.cancel()
				delete(current, id)
				if err = e.saveCompletion(workCtx, f); err != nil {
					return err
				}
			default:
			}
		}
		if err = e.finishSaved(workCtx); err != nil {
			return err
		}
		if err = e.prepareCompletions(workCtx); err != nil {
			return err
		}
		if err = e.prepareBaseChecks(workCtx); err != nil {
			return err
		}
		if err = e.preparePublications(workCtx, publications); err != nil {
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
		if len(current) == 0 && len(publications) == 0 && drained {
			if !occupied {
				return nil
			}
			if stopping {
				return errors.New("stopped coordinator with unresolved process ownership; reservation retained")
			}
		}
		if !drained && !stopping {
			if e.Settings.Config.Issues != nil && e.Progress != nil && projectionDone == nil && !e.now().Before(nextProjection) {
				projectionCtx, cancel := context.WithCancel(context.Background())
				projectionCancel = cancel
				done := make(chan error, 1)
				projectionDone = done
				worker := *e
				go func() { done <- worker.SyncOverview(projectionCtx) }()
			}
			for {
				p, ok, claimErr := e.claimPublication(workCtx)
				if claimErr != nil {
					return claimErr
				}
				if !ok {
					break
				}
				callCtx, cancel := context.WithCancel(context.Background())
				call := &activePublication{intent: p.Intent, cancel: cancel, done: make(chan error, 1)}
				publications[p.Intent.ID] = call
				worker := *e
				go func() { call.done <- worker.publish(callCtx, p) }()
			}
			for {
				a, ok, claimErr := e.claim(workCtx)
				if claimErr != nil {
					return claimErr
				}
				if !ok {
					break
				}
				callCtx, cancel := context.WithCancel(context.Background())
				call := &active{attempt: a, cancel: cancel, done: make(chan finished, 1)}
				current[a.ID] = call
				worker := *e
				go func() { call.done <- worker.execute(callCtx, a) }()
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
func (e *Engine) recover(ctx context.Context) error { return e.recoverExcept(ctx, nil) }
func (e *Engine) recoverExcept(ctx context.Context, owned map[string]*active) error {
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return err
	}
	for _, a := range attempts {
		if !a.Occupying || owned[a.ID] != nil {
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
		a.Failure = core.Interrupted
		a.Error = "Coordinator stopped before a durable completed result; explicit recovery is required."
		now := e.now()
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
	settings, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return core.Attempt{}, false, err
	}
	e.Settings = settings
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return core.Attempt{}, false, err
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return core.Attempt{}, false, err
	}
	publications, err := e.Store.Publications(ctx, "")
	if err != nil {
		return core.Attempt{}, false, err
	}
	for _, t := range tasks {
		t = ciRepairTask(t, publications)
		v := publicationWait(eligible(settings.Config, t, attempts, drained, e.now(), tasks...), settings.Config, t, attempts, publications)
		if v.WaitingFor != "" {
			continue
		}
		if err = e.validate(ctx); err != nil {
			return core.Attempt{}, false, err
		}
		if t.ParentID != "" && t.WorkingBase == nil {
			for _, parent := range tasks {
				if parent.ID == t.ParentID && parent.PlanHead != "" {
					t.WorkingBase = &core.BaseBinding{Commit: parent.PlanHead}
				}
			}
			if t.WorkingBase == nil {
				continue
			}
		}
		pinned, err := e.taskEngine(ctx, t)
		if err != nil {
			return core.Attempt{}, false, err
		}
		if err = pinned.git().CheckQuality(ctx, t.Base()); err != nil {
			return core.Attempt{}, false, err
		}
		a := core.Attempt{
			ID: core.NewID(), TaskID: t.ID, Version: t.Current().Number, Generation: t.Generation,
			Role: t.NextRole, ProfileName: v.ProfileName, Profile: v.Profile, PolicyRevision: settings.Config.Revision(),
			Quality: t.Current().Quality, Base: t.Base(), Candidate: t.Candidate, CIRepair: t.CIRepair, ConflictRepair: t.ConflictRepair,
			State: "reserved", Occupying: true, StartedAt: e.now(),
			Kind: "stage", Reason: v.Reason, Repair: t.Usage.Repairs,
		}
		if q := t.Pending; q != nil {
			a.Kind = q.Kind
			a.ResumedFrom = q.FromAttempt
			if q.Kind == "repair" || q.Kind == "check_repair" || q.Kind == "ci_repair" || q.Kind == "conflict_repair" {
				t.Usage.Repairs++
				if q.Kind == "repair" {
					t.Usage.Escalations++
				} else if q.Kind != "check_repair" {
					t.WorkspaceRepair = t.Usage.Repairs
					t.BaseProof, t.Review = nil, nil
				}
				if q.Kind == "conflict_repair" {
					t.WorkingBase = &t.ConflictRepair.Target
					t.RefreshTarget, t.CIRepair, a.CIRepair = nil, nil, nil
					a.Base = t.Base()
				} else if q.Kind == "ci_repair" {
					t.ConflictRepair, a.ConflictRepair = nil, nil
				}
				a.Repair = t.Usage.Repairs
			} else {
				a.Retry = q.Retry
				a.Repair = q.Repair
			}
		}
		a.WorkspaceClaim = workspaceClaim(settings.Config, t)
		if !core.DeterministicRole(a.Role) {
			t.Usage.Invocations++
		}
		a.Invocation = t.Usage.Invocations
		a.EvidenceDir = filepath.Join(settings.Config.StateDir, "attempts", a.ID)
		t.Pending = nil
		t.NextRole = ""
		if a.Role == "refresh" {
			t.Stage = core.AwaitingDelivery
		} else if core.PlanningRole(a.Role) {
			t.Stage = core.Planning
		} else if a.Role == "implementer" {
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
	if core.PlanningRole(role) {
		return nil
	}
	if role != "verify" {
		return t.Proofs()
	}
	proofs := []core.Proof{}
	for _, p := range t.Proofs() {
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
	artifactFailure := func(err error) finished {
		return finished{a, execution.Completion{Stopped: true, Error: err.Error(), Kind: core.Integrity}}
	}
	t, err := e.Store.Task(ctx, a.TaskID)
	if err != nil {
		return fail(err)
	}
	if !a.Authorized(t) {
		return fail(errors.New("authority revoked before preparation"))
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return fail(err)
	}
	e = pinned
	if err = securefs.EnsurePrivateDir(a.EvidenceDir); err != nil {
		return fail(err)
	}
	if a.Role == "refresh" {
		return e.executeRefresh(ctx, t, a)
	}
	if a.Role == "combine" {
		return e.executeCombined(ctx, t, a)
	}
	original, err := workspace.Snapshot(ctx, e.Settings.Config.Repository.Path)
	if err != nil {
		return fail(err)
	}
	r := execution.Request{Attempt: a, Task: t, Quality: e.Settings.Quality, Proofs: required(t, a.Role)}
	var verifyArtifacts []func() error
	grantCheckEvidence := func(check core.CheckObservation, source string) error {
		for _, spec := range e.Settings.Quality.Checks {
			if spec.ID == check.CheckID && len(spec.Artifacts) > 0 && check.Artifacts == nil {
				return fmt.Errorf("check %s: required retained artifacts unavailable", check.CheckID)
			}
		}
		roots, err := execution.CheckReadRoots(check, source)
		if err != nil {
			return fmt.Errorf("check %s retained artifacts: %w", check.CheckID, err)
		}
		r.ReadRoots = append(r.ReadRoots, roots...)
		if check.Artifacts != nil {
			verifyArtifacts = append(verifyArtifacts, func() error {
				_, err := execution.CheckReadRoots(check, source)
				return err
			})
		}
		return nil
	}
	if !core.PlanningRole(a.Role) && (t.ParentID != "" || t.Release != nil) {
		r.Plan, err = e.planContext(ctx, t)
		if err != nil {
			return fail(err)
		}
	}
	var copyManifest workspace.CopyState
	if core.PlanningRole(a.Role) {
		source, err := workspace.BaselineCopy(ctx, e.Settings.Config, a.Base)
		if err != nil {
			return fail(err)
		}
		r.ReadRoots = []string{source}
		r.Dir, err = os.MkdirTemp("", "runner-plan-")
		if err != nil {
			return fail(err)
		}
		r.Dir, err = filepath.EvalSymlinks(r.Dir)
		if err != nil {
			return fail(err)
		}
	} else if a.Role == "implementer" {
		if a.Kind == "check_repair" || a.Kind == "retry" || a.Kind == "resume" {
			prior, err := e.Store.Attempts(ctx, t.ID)
			if err != nil {
				return fail(err)
			}
			old, reason := checkRepairOrigin(prior, a)
			if a.Kind == "check_repair" && old == nil {
				return fail(errors.New("check repair source attempt missing"))
			}
			if old != nil {
				if a.Kind == "check_repair" {
					if err := e.checkRepairSource(ctx, t, *old); err != nil {
						return finished{a, execution.Completion{Stopped: true, Kind: core.Integrity, Error: err.Error()}}
					}
				}
				r.CheckRepairReason, r.QualityEvidence = reason, old.Checks
				for _, c := range old.Checks {
					if err := grantCheckEvidence(c, old.SourceDigest); err != nil {
						return artifactFailure(err)
					}
				}
			}
		}
		if t.ConflictRepair != nil && t.Candidate != nil && *t.Candidate == t.ConflictRepair.Candidate {
			if err := e.git().PrepareConflictRepair(ctx, t); err != nil {
				return finished{a, execution.Completion{Stopped: true, Kind: core.Integrity, Error: err.Error()}}
			}
		}
		if t.CIRepair != nil && t.Candidate != nil && *t.Candidate == t.CIRepair.Assessment.Proof.Candidate {
			if err := e.git().PrepareCIRepair(ctx, t); err != nil {
				return finished{a, execution.Completion{Stopped: true, Kind: core.Integrity, Error: err.Error()}}
			}
		}
		if a.Kind == "repair" {
			if err := e.git().ValidateCandidate(ctx, t); err != nil {
				return finished{a, execution.Completion{Stopped: true, Kind: core.Integrity, Error: err.Error()}}
			}
		}
		m, err := e.git().Prepare(ctx, t)
		if err != nil {
			return fail(err)
		}
		r.Dir = m.WorktreePath
		roots, rootsErr := e.git().ReadRoots(ctx, r.Dir)
		r.ReadRoots = append(r.ReadRoots, roots...)
		err = rootsErr
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
			if (old.Role == "implementer" || old.Role == "refresh" || old.Role == "combine") && old.State == "applied" && old.Version == t.Current().Number && old.Generation == t.Generation && old.Candidate != nil && t.Candidate != nil && *old.Candidate == *t.Candidate {
				r.QualityEvidence = old.Checks
				for _, check := range old.Checks {
					if err := grantCheckEvidence(check, t.Candidate.Fingerprint); err != nil {
						return artifactFailure(err)
					}
				}
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
	if r.Plan != nil && a.Role != "implementer" {
		for _, old := range r.Plan.Evidence {
			events := filepath.Join(old.EvidenceDir, "events.jsonl")
			_, _, state, readErr := securefs.ReadFile(events, 32<<20)
			if readErr != nil && !os.IsNotExist(readErr) {
				return fail(readErr)
			}
			if readErr == nil && state.Exists {
				r.ReadRoots = append(r.ReadRoots, events)
			}
			for _, check := range old.Checks {
				if err := grantCheckEvidence(check, old.Candidate.Fingerprint); err != nil {
					return artifactFailure(err)
				}
			}
		}
	}
	for _, check := range e.Settings.Quality.Baseline {
		if check.Evidence != "" {
			if _, _, _, err := securefs.ReadFile(check.Evidence, 8<<20); err != nil {
				return fail(fmt.Errorf("pinned baseline evidence unavailable: %w", err))
			}
			source := ""
			if check.Artifacts != nil {
				source = check.Artifacts.SourceDigest // Pinned baseline, not current-candidate proof.
			}
			if err := grantCheckEvidence(check, source); err != nil {
				return artifactFailure(err)
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
	launchState := "running"
	started := func(p core.Process) error {
		a.Process = &p
		a.State = launchState
		return e.mutate(context.Background(), a.TaskID, func(t *core.Task) (*core.Attempt, error) {
			if !a.Authorized(*t) {
				return nil, errors.New("authority revoked before launch")
			}
			return &a, nil
		})
	}
	completion := e.Executor.Execute(ctx, r, started)
	if completion.Stopped {
		for _, verify := range verifyArtifacts {
			if err := verify(); err != nil {
				completion.Result, completion.Error, completion.Kind = nil, err.Error(), core.Integrity
				break
			}
		}
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
			if err = core.ValidateRoleResult(a.Role, *completion.Result, r.Proofs); err != nil {
				completion.Result = nil
				completion.Error = "invalid result: " + err.Error()
				completion.Kind = "missing_proof"
			}
		}
		if completion.Result != nil && a.Role == "implementer" && completion.Error == "" {
			files, checkErr := workspace.Manifest(r.Dir)
			if checkErr == nil {
				a.SourceDigest = core.Digest(files)
				snapshot, snapshotErr := workspace.Snapshot(ctx, r.Dir)
				if snapshotErr != nil {
					checkErr = snapshotErr
				} else {
					a.SourceHead = snapshot.Head
				}
			}
			conflictDecision := a.ConflictRepair != nil && !canReviewImplementation(*completion.Result)
			if checkErr == nil && a.ConflictRepair != nil && !conflictDecision {
				checkErr = workspace.CheckConflictResolution(r.Dir, *a.ConflictRepair)
			}
			if checkErr == nil && !conflictDecision {
				launchState = "checking"
				a.CheckFiles = map[string]string{}
				for _, ex := range e.Settings.Quality.Exceptions {
					for name := range ex.Files {
						a.CheckFiles[name] = files[name]
					}
				}
				a.Checks, checkErr = e.runChecks(ctx, r.Dir, files, e.Settings.Quality.For(t, false), filepath.Join(a.EvidenceDir, "quality"), started)
				for _, check := range a.Checks {
					if !check.Stopped {
						completion.Stopped = false
					}
				}
				if checkErr == nil {
					checkErr = e.Settings.Quality.For(t, false).ValidateResults(a.Checks, files)
				}
			}
			if checkErr != nil {
				completion.Error = checkErr.Error()
				completion.Kind = core.ProofGap
			}
		}
		if completion.Result != nil && a.Role == "implementer" && completion.Stopped {
			digest, head, bindingErr := e.git().Binding(context.Background(), t)
			if bindingErr != nil || digest != a.SourceDigest || head != a.SourceHead {
				err = errors.New("implementation source or HEAD changed during quality checks")
				if bindingErr != nil {
					err = bindingErr
				}
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
		now := e.now()
		a.FinishedAt = &now
	}
	kind := core.Interrupted
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
	a.Failure = kind
	if a.State == "completed" {
		a.Failure = ""
	}
	return e.mutate(ctx, a.TaskID, func(t *core.Task) (*core.Attempt, error) {
		if a.Authorized(*t) && a.State != "completed" {
			if kind == core.Transient && f.completion.Stopped {
				t.Pending = &core.Continuation{Kind: "retry", FromAttempt: a.ID, Reason: a.Error, ProfileName: a.ProfileName, Profile: a.Profile, Retry: a.Retry + 1, Repair: a.Repair, NotBefore: a.FinishedAt.Add(retryDelay(a.Retry + 1))}
				t.NextRole = a.Role
				t.Attention = nil
			} else {
				t.Attention = &core.Attention{Kind: kind, Detail: a.Error, Attempt: a.ID}
			}
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

// A completed source handoff may still need independent proof. Source integrity,
// conflict markers and required quality checks remain separate mandatory gates.
func canReviewImplementation(r core.Result) bool {
	outcome := core.ReviewOutcome(r.Checks)
	return r.Outcome == "succeeded" && (outcome == core.Accepted || outcome == "gap")
}

func (e *Engine) advance(ctx context.Context, t *core.Task, a *core.Attempt) error {
	if err := e.validate(ctx); err != nil {
		return err
	}
	pinned, err := e.taskEngine(ctx, *t)
	if err != nil {
		return err
	}
	e = pinned
	if a.Quality != e.Settings.Quality.Revision() {
		return errors.New("saved quality binding changed")
	}
	if a.Role == "refresh" {
		return e.advanceRefresh(ctx, t, a)
	}
	if a.Role == "combine" {
		if a.Refreshed != nil || a.ConflictRepair != nil {
			return e.advanceRefresh(ctx, t, a)
		}
		if a.Candidate == nil {
			return errors.New("combined candidate missing")
		}
		t.Candidate = a.Candidate
		if err := e.git().ValidateCandidate(ctx, *t); err != nil {
			return err
		}
		t.Stage, t.NextRole, t.Attention = core.Reviewing, "audit", nil
		a.State = "applied"
		return nil
	}
	if core.PlanningRole(a.Role) {
		return advancePlanning(t, a)
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
		if a.ConflictRepair != nil && !canReviewImplementation(*a.Result) {
			a.State = "applied"
			t.Attention = &core.Attention{Kind: core.FailureKind(outcome), Detail: a.Result.Summary, Attempt: a.ID}
			return nil
		}
		if a.ConflictRepair != nil {
			if err := workspace.CheckConflictResolution(a.WorkspaceClaim, *a.ConflictRepair); err != nil {
				return err
			}
		}
		if err := e.Settings.Quality.For(*t, false).ValidateResults(a.Checks, a.CheckFiles); err != nil {
			return err
		}
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
		if !canReviewImplementation(*a.Result) {
			a.State = "applied"
			t.Attention = &core.Attention{Kind: core.FailureKind(outcome), Detail: a.Result.Summary, Attempt: a.ID}
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
		if a.CIRepair != nil && candidate.Tree == a.CIRepair.Assessment.Proof.Candidate.Tree {
			a.State = "applied"
			t.Attention = &core.Attention{Kind: core.ProofGap, Detail: "CI repair left source unchanged; inspect the failure and clarify missing diagnostic evidence before replacing the task", Attempt: a.ID}
			return nil
		}
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
		if outcome == "accepted" && t.Release != nil {
			if err := e.combinedChecks(ctx, *t); err != nil {
				t.Attention = &core.Attention{Kind: core.ProofGap, Detail: err.Error(), Attempt: a.ID}
				t.NextRole = ""
				a.State = "applied"
				return nil
			}
		}
		switch outcome {
		case "accepted":
			t.Stage = core.AwaitingDelivery
			t.NextRole = ""
			if t.WorkingBase != nil || t.CIRepair != nil {
				t.NextRole = "refresh"
			}
			t.Attention = nil
		case "gap":
			if a.Role == "audit" && len(required(*t, "verify")) != 0 {
				t.NextRole = "verify"
			} else {
				t.Attention = &core.Attention{Kind: "missing_proof", Detail: a.Result.Summary, Attempt: a.ID}
			}
		case "defect":
			t.NextRole = "implementer"
			t.Pending = &core.Continuation{Kind: "repair", FromAttempt: a.ID, Reason: a.Result.Summary}
			t.Attention = nil
		default:
			t.Attention = &core.Attention{Kind: core.FailureKind(outcome), Detail: a.Result.Summary, Attempt: a.ID}
		}
	}
	a.State = "applied"
	return nil
}
