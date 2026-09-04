package engine

import (
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
)

const maxAutomaticRetries = 3

var automaticRetryDelays = [...]time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}

type automaticRetryState struct {
	failures  int
	notBefore time.Time
}

func (s *Engine) nextAutomaticRetry(itemID string, now time.Time) (automaticRetryState, bool) {
	s.automaticRetryMu.Lock()
	defer s.automaticRetryMu.Unlock()
	previous := s.automaticRetries[itemID]
	next := automaticRetryState{failures: previous.failures + 1}
	if next.failures > maxAutomaticRetries {
		return next, false
	}
	next.notBefore = now.Add(automaticRetryDelays[next.failures-1])
	return next, true
}

func (s *Engine) storeAutomaticRetry(itemID string, state automaticRetryState) {
	s.automaticRetryMu.Lock()
	defer s.automaticRetryMu.Unlock()
	if s.automaticRetries == nil {
		s.automaticRetries = map[string]automaticRetryState{}
	}
	s.automaticRetries[itemID] = state
}

func (s *Engine) clearAutomaticRetry(itemID string) {
	s.automaticRetryMu.Lock()
	defer s.automaticRetryMu.Unlock()
	delete(s.automaticRetries, itemID)
}

func (s *Engine) automaticRetryPending(item github.WorkItem, now time.Time) bool {
	s.automaticRetryMu.Lock()
	defer s.automaticRetryMu.Unlock()
	state, found := s.automaticRetries[item.ID]
	if !found {
		return false
	}
	if strings.TrimSpace(item.Activity) != config.RunnerActivityWaitingForHarness {
		delete(s.automaticRetries, item.ID)
		return false
	}
	return now.Before(state.notBefore)
}

func (s *Engine) capPollDelayForAutomaticRetry(delay time.Duration, now time.Time) time.Duration {
	s.automaticRetryMu.Lock()
	defer s.automaticRetryMu.Unlock()
	for _, state := range s.automaticRetries {
		if !state.notBefore.After(now) {
			continue
		}
		until := state.notBefore.Sub(now)
		if until < delay {
			delay = until
		}
	}
	return delay
}

func (s *Engine) finishAutomaticRetry(result RunResult) {
	if result.RetryDisposition != string(execution.RetryAutomatic) {
		s.clearAutomaticRetry(result.Item.ID)
	}
}
