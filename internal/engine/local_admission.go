package engine

import (
	"context"
	"errors"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
)

// EnableLocalAdmission connects CLI-owned engines to the shared local Project
// capacity. Configure it before running the engine, alongside its metrics store.
func (s *Engine) EnableLocalAdmission() { s.localAdmission = true }

func (s *Engine) acquireLocalAdmission(ctx context.Context, wait bool) (*github.ProcessLock, error) {
	lock, err := s.acquireLocalGate(ctx, wait, github.AcquireAdmissionLock)
	if err == nil && s.localAdmission {
		// Another process may have appended an attempt since this engine's
		// last observation. Re-read at admission, not during deterministic work.
		s.admissionHistoryMu.Lock()
		s.admissionHistoryGeneration++
		s.admissionHistoryMu.Unlock()
	}
	return lock, err
}

func (s *Engine) acquireLocalGate(ctx context.Context, wait bool, acquire func(config.GitHubProjectConfig) (*github.ProcessLock, error)) (*github.ProcessLock, error) {
	if !s.localAdmission {
		return nil, nil
	}
	project := config.GitHubProjectConfig{Owner: s.cfg.GitHubProject.Owner, Number: s.cfg.GitHubProject.Number}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lock, err := acquire(project)
		if err == nil {
			return lock, nil
		}
		if !wait || !errors.Is(err, github.ErrProjectLockBusy) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Engine) acquireLocalExecutionSlot() (*github.ProcessLock, error) {
	if !s.localAdmission {
		return nil, nil
	}
	return github.AcquireExecutionSlot(config.GitHubProjectConfig{
		Owner: s.cfg.GitHubProject.Owner, Number: s.cfg.GitHubProject.Number,
	}, s.maxParallelism())
}

func (s *Engine) recordAttemptStart(event metrics.Event) error {
	if s.observeMetrics == nil {
		return nil
	}
	event.Kind = metrics.EventStarted
	return s.observeMetrics(event)
}
