package execution

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Match the upper bound for Core-owned source cleanup. This budget covers only
// retention after native settlement and grants no additional task execution.
const maxSourceRetention = 5 * time.Minute

// One source collector belongs to the saved native attempt. Observers join it.
// Stop can interrupt ordinary capture, while Close also stops retention.
type sourceCaptureWork struct {
	assignment    runner.Assignment
	done          chan struct{}
	cancel        context.CancelFunc
	source        *runner.SourceResult
	err           error
	retry         bool
	retention     bool
	stopRequested bool
}

func (s *Service) captureSource(ctx context.Context, a runner.Assignment, retention bool) (*runner.SourceResult, error) {
	s.captureMu.Lock()
	work := s.captures[a.AttemptID]
	if work != nil {
		old := work.assignment
		if old.RunID != a.RunID || old.Repository != a.Repository || !old.Deadline.Equal(a.Deadline) || old.Limits.MaxArtifact != a.Limits.MaxArtifact {
			s.captureMu.Unlock()
			return nil, errors.New("saved source capture assignment changed")
		}
		select {
		case <-work.done:
			if !work.retry {
				source, err := runner.Clone(work.source), work.err
				s.captureMu.Unlock()
				return source, err
			}
			retention = true
		default:
			s.captureMu.Unlock()
			return s.joinCapture(ctx, work)
		}
	}
	if s.captureClosed {
		s.captureMu.Unlock()
		return nil, errors.New("source capture service is closing")
	}
	retention = retention || a.Deadline.IsZero() || !a.Deadline.After(time.Now())
	var active context.Context
	var cancel context.CancelFunc
	if retention {
		active, cancel = context.WithTimeout(context.Background(), maxSourceRetention)
	} else {
		active, cancel = context.WithDeadline(context.Background(), a.Deadline)
	}
	if s.captures == nil {
		s.captures = map[string]*sourceCaptureWork{}
	}
	work = &sourceCaptureWork{
		assignment: runner.Clone(a),
		done:       make(chan struct{}),
		cancel:     cancel,
		retention:  retention,
	}
	s.captures[a.AttemptID] = work
	s.captureMu.Unlock()
	go s.runCapture(active, work)
	return s.joinCapture(ctx, work)
}

func (s *Service) runCapture(active context.Context, work *sourceCaptureWork) {
	defer work.cancel()
	var source *runner.SourceResult
	var err error
	for {
		source, err = s.workspaces.Capture(active, work.assignment)
		var pending *capturePending
		if !errors.As(err, &pending) || active.Err() != nil {
			break
		}
		// Rejoin an inherited writer's lease within this collector's own budget.
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-timer.C:
		case <-active.Done():
			timer.Stop()
		}
	}
	s.captureMu.Lock()
	interrupted := captureWasInterrupted(active, err)
	if work.retention && interrupted {
		// Cache a settled retention failure, not a pending observation. The
		// checkpoint stays owned and a later observer cannot reset the budget.
		err = active.Err()
		if errors.Is(err, context.DeadlineExceeded) {
			err = errors.New("source retention deadline reached; preserve the saved capture")
		}
	}
	work.source, work.err = source, err
	work.retry = !work.retention && interrupted && (errors.Is(active.Err(), context.DeadlineExceeded) || work.stopRequested)
	close(work.done)
	wake := s.captureWake
	s.captureMu.Unlock()
	if wake != nil {
		wake(work.assignment.AttemptID)
	}
}

func captureWasInterrupted(ctx context.Context, err error) bool {
	if ctx.Err() == nil || err == nil {
		return false
	}
	var pending *capturePending
	var exit *exec.ExitError
	return errors.Is(err, ctx.Err()) || errors.As(err, &pending) || errors.As(err, &exit) && exit.ExitCode() == -1
}

func (s *Service) joinCapture(ctx context.Context, work *sourceCaptureWork) (*runner.SourceResult, error) {
	select {
	case <-work.done:
		s.captureMu.Lock()
		source, err := runner.Clone(work.source), work.err
		retry := work.retry
		s.captureMu.Unlock()
		if retry {
			return nil, &capturePending{reason: "owned interrupted capture is ready for bounded source retention"}
		}
		return source, err
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ctx.Err()
		}
		return nil, &capturePending{reason: "same owned source capture remains active; rejoin the saved attempt"}
	}
}

func (s *Service) stopCapture(ctx context.Context, id string) error {
	s.captureMu.Lock()
	work := s.captures[id]
	if work == nil || work.retention {
		s.captureMu.Unlock()
		return nil
	}
	select {
	case <-work.done:
		s.captureMu.Unlock()
		return nil
	default:
		work.stopRequested = true
		work.cancel()
	}
	s.captureMu.Unlock()
	select {
	case <-work.done:
		return nil
	case <-ctx.Done():
		return errors.New("source capture did not settle after stop")
	}
}

func (s *Service) captureReleaseReady(id string) error {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	if work := s.captures[id]; work != nil {
		select {
		case <-work.done:
			if work.err != nil {
				return errors.New("cannot release uncommitted source capture")
			}
		default:
			return errors.New("cannot release active source capture")
		}
		delete(s.captures, id)
	}
	return nil
}

func (s *Service) closeCaptures(ctx context.Context) error {
	s.captureMu.Lock()
	s.captureClosed = true
	var pending []*sourceCaptureWork
	for _, work := range s.captures {
		work.cancel()
		pending = append(pending, work)
	}
	s.captureMu.Unlock()
	for _, work := range pending {
		select {
		case <-work.done:
		case <-ctx.Done():
			return errors.New("source capture did not settle before service close")
		}
	}
	return nil
}
