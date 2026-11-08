package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type candidateWork struct {
	done        chan struct{}
	cancel      context.CancelFunc
	observation runner.Observation
	err         error
}

func (c *Candidates) SetWake(wake func(string)) { c.mu.Lock(); c.wake = wake; c.mu.Unlock() }
func (c *Candidates) candidateLease(job candidateJob) (*os.File, error) {
	id := job.Assignment.AttemptID
	if !taskComponent(id) || !taskComponent(job.Assignment.RunID) {
		return nil, errors.New("candidate lease identity is invalid")
	}
	path := filepath.Join(c.disk.Root, "jobs", id+".candidate.lock")
	info, err := os.Lstat(path)
	flags := os.O_RDWR
	if errors.Is(err, os.ErrNotExist) {
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		return nil, errors.New("candidate lease ownership changed")
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, err
	}
	if info != nil {
		actual, e := f.Stat()
		if e != nil || !os.SameFile(info, actual) {
			f.Close()
			return nil, errors.New("candidate lease identity changed")
		}
	}
	owned, err := tryCaptureLease(f)
	if err != nil || !owned {
		f.Close()
		if err != nil {
			return nil, err
		}
		return nil, &capturePending{reason: "same owned candidate writer is settling"}
	}
	return f, nil
}
func candidatePending(reason string) runner.Observation {
	zero := int64(0)
	return runner.Observation{State: runner.Unknown, Reconciling: true, Reason: runner.Clip(reason), NextCheck: time.Now().Add(time.Second), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}
}
func (c *Candidates) joinCandidate(ctx context.Context, job candidateJob) (runner.Observation, error) {
	id := job.Assignment.AttemptID
	c.mu.Lock()
	work := c.workers[id]
	if work != nil {
		select {
		case <-work.done:
			delete(c.workers, id)
			work = nil
		default:
		}
	}
	if work == nil {
		current, err := c.load(id)
		if err != nil || current.End != nil || c.closed {
			c.mu.Unlock()
			if err != nil {
				return runner.Observation{}, err
			}
			if current.End != nil {
				return candidateObservation(current), nil
			}
			return runner.Observation{}, errors.New("candidate service is closing")
		}
		active, cancel := context.WithCancel(ctx)
		if !current.Assignment.Deadline.IsZero() && !current.Stopped && current.Assignment.Deadline.After(time.Now()) {
			cancel()
			active, cancel = context.WithDeadline(context.Background(), current.Assignment.Deadline)
		}
		work = &candidateWork{done: make(chan struct{}), cancel: cancel}
		if c.workers == nil {
			c.workers = map[string]*candidateWork{}
		}
		c.workers[id] = work
		go c.runCandidate(active, work, current)
	}
	c.mu.Unlock()
	select {
	case <-work.done:
		c.mu.Lock()
		o, err := runner.Clone(work.observation), work.err
		c.mu.Unlock()
		return o, err
	case <-ctx.Done():
		return candidatePending("same reviewed candidate preparation remains active; rejoin its saved attempt"), nil
	}
}
func (c *Candidates) runCandidate(ctx context.Context, work *candidateWork, job candidateJob) {
	defer work.cancel()
	var o runner.Observation
	var err error
	for {
		var lease *os.File
		lease, err = c.candidateLease(job)
		if err == nil {
			active := context.WithValue(ctx, captureLeaseKey{}, lease)
			current, e := c.load(job.Assignment.AttemptID)
			if e != nil {
				err = e
			} else if current.Stopped {
				var reason string
				reason, err = c.workspaces.reconcileStoppedCandidate(active, current.Assignment)
				if err == nil {
					current.End = &runner.ExecutionEnd{Kind: "stopped", Reason: reason}
					err = c.saveCandidateResult(active, current)
				}
			} else {
				var text string
				var complete bool
				text, complete, err = c.workspaces.completedCandidate(active, current.Assignment)
				if err == nil && !complete {
					if !current.Assignment.Deadline.IsZero() && !current.Assignment.Deadline.After(time.Now()) {
						err = &capturePending{reason: "candidate deadline elapsed without a verified completed local intent; no new integration dispatched"}
					} else {
						text, err = c.workspaces.retainCandidate(active, current.Assignment)
					}
				}
				if err == nil {
					current.End = &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"candidate": runner.Text(text)}}}
					err = c.saveCandidateResult(active, current)
				} else if ctx.Err() == nil {
					var pending *capturePending
					if !errors.As(err, &pending) {
						current.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip(err.Error())}
						err = c.saveCandidateResult(active, current)
					}
				}
			}
			lease.Close()
			break
		}
		var pending *capturePending
		if !errors.As(err, &pending) || !waitAdoption(ctx) {
			break
		}
	}
	if err != nil {
		var pending *capturePending
		o = candidatePending(err.Error())
		o.Reconciling = captureWasInterrupted(ctx, err) || errors.As(err, &pending)
	} else {
		var current candidateJob
		current, err = c.load(job.Assignment.AttemptID)
		if err == nil {
			o = candidateObservation(current)
		}
	}
	c.mu.Lock()
	work.observation, work.err = o, err
	if o.State == runner.Unknown {
		work.err = nil
	}
	close(work.done)
	wake := c.wake
	c.mu.Unlock()
	if wake != nil {
		wake(job.Assignment.AttemptID)
	}
}

// This is result collection, including after the original deadline: it may
// authenticate an already-applied intent but never creates objects or refs.
func (w *Workspaces) completedCandidate(ctx context.Context, a runner.Assignment) (string, bool, error) {
	if err := w.lockContext(ctx); err != nil {
		return "", false, err
	}
	defer w.mu.Unlock()
	r, err := w.load(a.RunID)
	if err != nil {
		return "", false, err
	}
	hash := r.CandidatePins[a.AttemptID]
	if hash == "" {
		return "", false, nil
	}
	if err = candidateApproval(a); err != nil {
		return "", false, err
	}
	if _, err = w.candidatePatch(ctx, r, a); err != nil {
		return "", false, err
	}
	if err = validateCandidateObject(ctx, r, hash); err != nil {
		return "", false, err
	}
	for _, ref := range []string{"refs/runner/candidates/" + a.RunID + "/" + a.AttemptID, candidateRef(a.RunID)} {
		actual, e := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
		if e != nil {
			if missingCandidateRef(ctx, r.Repository, ref) {
				return "", false, nil
			}
			return "", false, e
		}
		if strings.TrimSpace(string(actual)) != hash {
			if ref == candidateRef(a.RunID) && r.CandidateCommit != "" && strings.TrimSpace(string(actual)) == r.CandidateCommit && r.CandidateCommit != hash {
				return "", false, nil
			}
			return "", false, errors.New("candidate branch or history pin already contains different work; preserve it")
		}
	}
	if r.CandidateCommit != hash {
		r.CandidateCommit = hash
		if err = w.save(r); err != nil {
			return "", false, err
		}
	}
	return candidateRef(a.RunID) + "\n" + hash + "\nLocal reviewed candidate; not merged, pushed or deployed.", true, nil
}
func (c *Candidates) Close(ctx context.Context) error {
	c.mu.Lock()
	c.closed = true
	var pending []*candidateWork
	for _, work := range c.workers {
		work.cancel()
		pending = append(pending, work)
	}
	c.mu.Unlock()
	for _, work := range pending {
		select {
		case <-work.done:
		case <-ctx.Done():
			return errors.New("candidate integration did not settle before close")
		}
	}
	return nil
}

// The local writer lease has settled before this read-only reconciliation.
// Missing/new/previous refs are distinguished without creating any ref or
// repeating candidate construction after cancellation.
func (w *Workspaces) reconcileStoppedCandidate(ctx context.Context, a runner.Assignment) (string, error) {
	if err := w.lockContext(ctx); err != nil {
		return "", err
	}
	defer w.mu.Unlock()
	r, err := w.load(a.RunID)
	if err != nil {
		return "", err
	}
	hash := r.CandidatePins[a.AttemptID]
	if hash == "" {
		return "candidate integration stopped before branch change", nil
	}
	if err = candidateApproval(a); err != nil {
		return "", err
	}
	if _, err = w.candidatePatch(ctx, r, a); err != nil {
		return "", err
	}
	if err = validateCandidateObject(ctx, r, hash); err != nil {
		return "", err
	}
	ref := candidateRef(a.RunID)
	actual, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
	if err != nil {
		if missingCandidateRef(ctx, r.Repository, ref) && r.CandidateCommit == "" {
			return "candidate integration stopped before branch creation; retained local intent", nil
		}
		return "", err
	}
	current := strings.TrimSpace(string(actual))
	if current == hash {
		r.CandidateCommit = hash
		if err = w.save(r); err != nil {
			return "", err
		}
		return "candidate integration stopped; exact owned local commit confirmed at " + hash, nil
	}
	if r.CandidateCommit != "" && current == r.CandidateCommit {
		return "candidate integration stopped before branch advance; retained local intent", nil
	}
	return "", errors.New("candidate branch differs from saved intent; preserve it for inspection")
}
