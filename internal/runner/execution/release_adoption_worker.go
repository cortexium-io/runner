package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

type sourceAdoptionWork struct {
	done        chan struct{}
	cancel      context.CancelFunc
	observation runner.Observation
	err         error
	prepared    bool
}

func localSourceMode(mode string) bool   { return mode == "readiness" || mode == "publish" }
func releaseSourceMode(mode string) bool { return mode == "source-adopt" || localSourceMode(mode) }
func detachedReleaseMode(mode string) bool {
	return releaseSourceMode(mode) || mode == "cortexium-browser"
}

func (r *Releases) Observe(ctx context.Context, id string) (runner.Observation, error) {
	j, err := r.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	mode := j.Assignment.Setup.Profile.Mode
	if !detachedReleaseMode(mode) || j.End != nil {
		return r.observeRelease(ctx, id)
	}
	if j.Stopped && mode == "source-adopt" {
		return r.finishSourceAdoption(ctx, j, runner.Observation{}, release.Response{}, "ready")
	}
	o, prepared, err := r.joinSourceAdoption(ctx, j)
	if err != nil || !prepared {
		return o, err
	}
	// The trusted external backend keeps its existing request/observation
	// semantics. Only local preparation is detached from the short observer.
	return r.observeRelease(ctx, id)
}

func (r *Releases) SetWake(wake func(string)) {
	r.adoptionMu.Lock()
	r.adoptionWake = wake
	r.adoptionMu.Unlock()
}

func (r *Releases) adoptionLease(j releaseJob) (*os.File, error) {
	name := filepath.Join(r.disk.Root, "jobs", j.Assignment.AttemptID+".source-adopt.lock")
	info, err := os.Lstat(name)
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return nil, errors.New("source adoption lease ownership changed")
	}
	lease, err := os.OpenFile(name, flags, 0600)
	if err != nil {
		return nil, err
	}
	if info != nil {
		current, e := lease.Stat()
		if e != nil || !os.SameFile(info, current) {
			lease.Close()
			return nil, errors.New("source adoption lease identity changed")
		}
	}
	owned, err := tryCaptureLease(lease)
	if err != nil || !owned {
		lease.Close()
		if err != nil {
			return nil, err
		}
		return nil, &capturePending{reason: "same owned source adoption writer is settling"}
	}
	return lease, nil
}

func (r *Releases) joinSourceAdoption(ctx context.Context, j releaseJob) (runner.Observation, bool, error) {
	if j.Assignment.Deadline.IsZero() && ctx.Err() != nil {
		return localSourceError(ctx, ctx.Err()), false, nil
	}
	id := j.Assignment.AttemptID
	r.adoptionMu.Lock()
	work := r.adoptions[id]
	if work != nil {
		select {
		case <-work.done:
			if work.observation.End == nil && (!work.prepared || j.Stopped) && (j.Assignment.Deadline.IsZero() || j.Assignment.Deadline.After(time.Now()) || j.Stopped) && !r.adoptionClosed {
				delete(r.adoptions, id)
				work = nil
			}
		default:
		}
	}
	if work == nil {
		current, err := r.load(id)
		if err != nil || current.Stopped && !localSourceMode(current.Assignment.Setup.Profile.Mode) && current.Assignment.Setup.Profile.Mode != "cortexium-browser" || r.adoptionClosed {
			r.adoptionMu.Unlock()
			if err != nil {
				return runner.Observation{}, false, err
			}
			return runner.Observation{}, false, context.Canceled
		}
		active, cancel := context.WithCancel(ctx)
		if current.Stopped && current.Assignment.Setup.Profile.Mode == "cortexium-browser" {
			cancel()
			active, cancel = context.WithTimeout(context.Background(), 3*time.Minute)
		}
		if !j.Assignment.Deadline.IsZero() && !current.Stopped {
			cancel()
			active, cancel = context.WithDeadline(context.Background(), j.Assignment.Deadline)
		}
		work = &sourceAdoptionWork{done: make(chan struct{}), cancel: cancel}
		if r.adoptions == nil {
			r.adoptions = map[string]*sourceAdoptionWork{}
		}
		r.adoptions[id] = work
		go r.runSourceAdoption(active, work, current)
	}
	r.adoptionMu.Unlock()
	select {
	case <-work.done:
		r.adoptionMu.Lock()
		observation, prepared, err := runner.Clone(work.observation), work.prepared, work.err
		r.adoptionMu.Unlock()
		return observation, prepared, err
	case <-ctx.Done():
		zero := int64(0)
		return runner.Observation{State: runner.Unknown, Reconciling: true, Reason: "same owned release source preparation remains active", NextCheck: time.Now().Add(time.Second), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}, false, nil
	}
}

func releaseSourcePending(reason string) runner.Observation {
	zero := int64(0)
	return runner.Observation{State: runner.Unknown, Reconciling: true, Reason: runner.Clip(reason), NextCheck: time.Now().Add(time.Second), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}
}

func (r *Releases) prepareLocalSource(ctx context.Context, id string) (runner.Observation, bool, error) {
	j, err := r.load(id)
	if err != nil {
		return runner.Observation{}, false, err
	}
	if j.End != nil {
		return runner.Observation{State: runner.Finished, End: j.End, Source: j.Source}, false, nil
	}
	if j.Stopped {
		return r.settleLocalSource(ctx, j)
	}
	switch j.Assignment.Setup.Profile.Mode {
	case "readiness":
		err = r.prepareReleaseSource(ctx, &j)
	case "publish":
		_, err = r.publishCandidate(ctx, &j)
	default:
		return runner.Observation{}, false, errors.New("unsupported local release preparation")
	}
	if err != nil {
		return localSourceError(ctx, err), false, nil
	}
	current, err := r.load(id)
	if err != nil {
		return runner.Observation{}, false, err
	}
	if current.Stopped {
		return r.settleLocalSource(ctx, current)
	}
	return runner.Observation{}, true, nil
}

func localSourceError(ctx context.Context, err error) runner.Observation {
	o := releaseSourcePending(err.Error())
	var pending *capturePending
	var archive *sourceArchivePending
	var push *publicationPushPending
	o.Reconciling = errors.Is(ctx.Err(), context.DeadlineExceeded) && captureWasInterrupted(ctx, err) || errors.As(err, &pending) || errors.As(err, &archive) || errors.As(err, &push)
	return o
}

func (r *Releases) settleLocalSource(ctx context.Context, j releaseJob) (runner.Observation, bool, error) {
	// Old prepared jobs may already have reached the trusted backend. Preserve
	// their same-key reconciliation rather than claiming it was never dispatched.
	beforeBackend := j.LocalPreparationOwned || j.Assignment.Setup.Profile.Mode == "readiness" && !j.SourcePrepared || j.Assignment.Setup.Profile.Mode == "publish" && j.Candidate == ""
	if !beforeBackend || j.OperatorDispatched {
		if j.Assignment.Setup.Profile.Mode == "publish" && j.Candidate == "" {
			if _, err := r.preparePublication(ctx, &j, false); err != nil {
				return localSourceError(ctx, err), false, nil
			}
		}
		return runner.Observation{}, true, nil
	}
	reason := "release source preparation stopped before backend dispatch"
	if j.Assignment.Setup.Profile.Mode == "publish" && (j.PushStarted || !j.LocalPreparationOwned) {
		if _, err := r.preparePublication(ctx, &j, false); err != nil {
			return localSourceError(ctx, err), false, nil
		}
		reason = "publication preparation stopped; exact owned remote ref confirmed at " + j.Candidate + "; backend was not dispatched"
	}
	j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: reason}
	if err := r.save(j); err != nil {
		return runner.Observation{}, false, err
	}
	return runner.Observation{State: runner.Finished, End: j.End, Source: j.Source}, false, nil
}

func (r *Releases) runSourceAdoption(ctx context.Context, work *sourceAdoptionWork, j releaseJob) {
	defer work.cancel()
	var observation runner.Observation
	var err error
	prepared := false
	for {
		var lease *os.File
		lease, err = r.adoptionLease(j)
		if err == nil {
			active := context.WithValue(ctx, captureLeaseKey{}, lease)
			if localSourceMode(j.Assignment.Setup.Profile.Mode) {
				observation, prepared, err = r.prepareLocalSource(active, j.Assignment.AttemptID)
				lease.Close()
				break
			}
			for {
				observation, err = r.observeRelease(active, j.Assignment.AttemptID)
				if err != nil || observation.End != nil || observation.State == runner.Unknown && !observation.Reconciling || ctx.Err() != nil {
					break
				}
				if !waitAdoption(ctx) {
					break
				}
			}
			lease.Close() // Inherited Git descriptors retain ownership after owner exit.
			break
		}
		var pending *capturePending
		if !errors.As(err, &pending) || !waitAdoption(ctx) {
			if errors.As(err, &pending) {
				observation = runner.Observation{State: runner.Unknown, Reconciling: true, Reason: err.Error()}
				err = nil
			}
			break
		}
	}
	r.adoptionMu.Lock()
	work.observation, work.prepared, work.err = observation, prepared, err
	close(work.done)
	wake := r.adoptionWake
	r.adoptionMu.Unlock()
	if wake != nil {
		wake(j.Assignment.AttemptID)
	}
}

func waitAdoption(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *Releases) stopSourceAdoption(ctx context.Context, id string) error {
	r.adoptionMu.Lock()
	work := r.adoptions[id]
	if work != nil {
		work.cancel()
	}
	r.adoptionMu.Unlock()
	if work == nil {
		return nil
	}
	select {
	case <-work.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Releases) Close(ctx context.Context) error {
	r.adoptionMu.Lock()
	r.adoptionClosed = true
	var pending []*sourceAdoptionWork
	for _, work := range r.adoptions {
		work.cancel()
		pending = append(pending, work)
	}
	r.adoptionMu.Unlock()
	for _, work := range pending {
		select {
		case <-work.done:
		case <-ctx.Done():
			return errors.New("source adoption did not settle before service close")
		}
	}
	return nil
}

func (r *Releases) releaseSourceAdoption(ctx context.Context, j releaseJob) error {
	r.adoptionMu.Lock()
	if work := r.adoptions[j.Assignment.AttemptID]; work != nil {
		select {
		case <-work.done:
			delete(r.adoptions, j.Assignment.AttemptID)
		default:
			r.adoptionMu.Unlock()
			return errors.New("cannot release active source adoption")
		}
	}
	r.adoptionMu.Unlock()
	lease, err := r.adoptionLease(j)
	if err != nil {
		return err
	}
	defer lease.Close()
	if r.workspaces != nil {
		if err = r.workspaces.ReleaseAttempt(ctx, j.Assignment); err != nil {
			return err
		}
	}
	return r.disk.Remove("jobs/" + j.Assignment.AttemptID + ".source-adopt.lock")
}
