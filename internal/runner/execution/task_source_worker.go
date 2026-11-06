package execution

import (
	"context"
	"errors"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// An operation worker belongs to one already admitted attempt. Observers join
// it; they neither restart source assembly nor create another workflow queue.
type taskSourceWork struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

func (s *TaskSources) SetWake(wake func(string)) {
	s.mu.Lock()
	s.wake = wake
	s.mu.Unlock()
}

func (s *TaskSources) joinWorker(ctx context.Context, j taskSourceJob) (runner.Observation, error) {
	id := j.Assignment.AttemptID
	s.mu.Lock()
	work := s.workers[id]
	if work != nil {
		select {
		case <-work.done:
			delete(s.workers, id)
			work = nil
		default:
		}
	}
	if work == nil {
		if s.closed {
			s.mu.Unlock()
			return runner.Observation{}, errors.New("task source service is closing")
		}
		// Re-read after acquiring ownership: a previous observer may have
		// completed while this observer was loading its saved job.
		var err error
		j, err = s.load(id)
		if err != nil || j.End != nil {
			s.mu.Unlock()
			if err != nil {
				return runner.Observation{}, err
			}
			return taskSourceObservation(j), nil
		}
		cleanup := j.Stopped || j.Assignment.Deadline.IsZero() || !j.Assignment.Deadline.After(time.Now())
		var active context.Context
		var cancel context.CancelFunc
		if cleanup {
			active, cancel = context.WithCancel(ctx)
		} else {
			active, cancel = context.WithDeadline(context.Background(), j.Assignment.Deadline)
		}
		if s.workers == nil {
			s.workers = map[string]*taskSourceWork{}
		}
		work = &taskSourceWork{done: make(chan struct{}), cancel: cancel}
		s.workers[id] = work
		go s.runWorker(active, work, j, cleanup)
	}
	s.mu.Unlock()
	select {
	case <-work.done:
		s.mu.Lock()
		err := work.err
		s.mu.Unlock()
		if err != nil {
			return runner.Observation{State: runner.Unknown, Reconciling: true, Reason: runner.Clip("task source is reconciling: " + err.Error())}, nil
		}
		current, err := s.load(id)
		if err != nil {
			return runner.Observation{}, err
		}
		return taskSourceObservation(current), nil
	case <-ctx.Done():
		return runner.Observation{State: runner.Running, Reconciling: true, Reason: "same task source operation remains active; rejoin its saved attempt"}, nil
	}
}

func taskSourceObservation(j taskSourceJob) runner.Observation {
	zero := int64(0)
	return runner.Observation{State: runner.Finished, End: runner.Clone(j.End), Source: runner.Clone(j.Source), Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}}
}

func (s *TaskSources) runWorker(ctx context.Context, work *taskSourceWork, j taskSourceJob, cleanup bool) {
	defer work.cancel()
	var err error
	if j.Stopped && j.PatchID == "" && !j.PinIntent {
		j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "task source stopped before workspace mutation"}
	} else {
		for {
			err = s.execute(ctx, &j)
			var pending *capturePending
			if cleanup || !errors.As(err, &pending) || ctx.Err() != nil {
				break
			}
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
			}
		}
		if err != nil {
			var pending *capturePending
			if ctx.Err() == nil && !errors.As(err, &pending) {
				j.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip("task source blocked: " + err.Error())}
			}
		}
	}
	// Persist the latest checkpoint even after interruption. save merges a
	// concurrent durable Stop and never converts that request back to ready.
	if saveErr := s.save(j); saveErr != nil {
		err = saveErr
	} else if j.End != nil {
		err = nil
	}
	s.mu.Lock()
	work.err = err
	close(work.done)
	wake := s.wake
	s.mu.Unlock()
	if wake != nil {
		wake(j.Assignment.AttemptID)
	}
}

func (s *TaskSources) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	var pending []*taskSourceWork
	for _, work := range s.workers {
		work.cancel()
		pending = append(pending, work)
	}
	s.mu.Unlock()
	for _, work := range pending {
		select {
		case <-work.done:
		case <-ctx.Done():
			return errors.New("task source did not settle before service close")
		}
	}
	return nil
}
