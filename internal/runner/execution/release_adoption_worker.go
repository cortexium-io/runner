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
}

func (r *Releases) Observe(ctx context.Context, id string) (runner.Observation, error) {
	j, err := r.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if j.Assignment.Setup.Profile.Mode != "source-adopt" || j.End != nil {
		return r.observeRelease(ctx, id)
	}
	if j.Stopped {
		return r.finishSourceAdoption(ctx, j, runner.Observation{}, release.Response{}, "ready")
	}
	return r.joinSourceAdoption(ctx, j)
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

func (r *Releases) joinSourceAdoption(ctx context.Context, j releaseJob) (runner.Observation, error) {
	id := j.Assignment.AttemptID
	r.adoptionMu.Lock()
	work := r.adoptions[id]
	if work != nil {
		select {
		case <-work.done:
			if work.observation.End == nil && j.Assignment.Deadline.After(time.Now()) && !r.adoptionClosed {
				delete(r.adoptions, id)
				work = nil
			}
		default:
		}
	}
	if work == nil {
		current, err := r.load(id)
		if err != nil || current.Stopped || r.adoptionClosed {
			r.adoptionMu.Unlock()
			if err != nil {
				return runner.Observation{}, err
			}
			return runner.Observation{}, context.Canceled
		}
		active, cancel := context.WithCancel(ctx)
		if !j.Assignment.Deadline.IsZero() {
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
		observation, err := runner.Clone(work.observation), work.err
		r.adoptionMu.Unlock()
		return observation, err
	case <-ctx.Done():
		zero := int64(0)
		return runner.Observation{State: runner.Unknown, Reconciling: true, Reason: "same authenticated source adoption remains active", NextCheck: time.Now().Add(time.Second), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}, nil
	}
}

func (r *Releases) runSourceAdoption(ctx context.Context, work *sourceAdoptionWork, j releaseJob) {
	defer work.cancel()
	var observation runner.Observation
	var err error
	for {
		var lease *os.File
		lease, err = r.adoptionLease(j)
		if err == nil {
			active := context.WithValue(ctx, captureLeaseKey{}, lease)
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
	work.observation, work.err = observation, err
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

func (r *Releases) releaseSourceAdoption(j releaseJob) error {
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
		if err = r.workspaces.ReleaseAttempt(j.Assignment); err != nil {
			return err
		}
	}
	return r.disk.Remove("jobs/" + j.Assignment.AttemptID + ".source-adopt.lock")
}
