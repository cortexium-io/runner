package github

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type rateLimitRunner struct {
	result subprocess.Result
	err    error
	calls  int
}

func (r *rateLimitRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.calls++
	if command != "gh" || len(args) != 2 || args[0] != "api" || args[1] != "rate_limit" {
		return subprocess.Result{}, errors.New("unexpected rate-limit command")
	}
	return r.result, r.err
}

func TestRateLimitRetryDelayUsesResourceResetAndSecondaryMinimum(t *testing.T) {
	now := time.Unix(1_000, 0)
	run := &rateLimitRunner{result: subprocess.Result{Stdout: `{"resources":{"core":{"remaining":42,"reset":1100},"graphql":{"remaining":0,"reset":1120}}}`}}
	delay, limited := RateLimitRetryDelay(t.Context(), run, errors.New("GraphQL: API rate limit exceeded"), now)
	if !limited || delay != 2*time.Minute+time.Second || run.calls != 1 {
		t.Fatalf("primary limit delay = %s limited=%t calls=%d", delay, limited, run.calls)
	}

	run = &rateLimitRunner{result: subprocess.Result{Stdout: `{"resources":{"core":{"remaining":42,"reset":1100},"graphql":{"remaining":42,"reset":1120}}}`}}
	delay, limited = RateLimitRetryDelay(t.Context(), run, errors.New("secondary rate limit exceeded"), now)
	if !limited || delay != time.Minute {
		t.Fatalf("secondary limit delay = %s limited=%t", delay, limited)
	}
}

func TestRateLimitRetryDelayIgnoresOrdinaryErrors(t *testing.T) {
	run := &rateLimitRunner{}
	if delay, limited := RateLimitRetryDelay(t.Context(), run, errors.New("repository not found"), time.Now()); limited || delay != 0 || run.calls != 0 {
		t.Fatalf("ordinary error triggered rate-limit lookup: delay=%s limited=%t calls=%d", delay, limited, run.calls)
	}
}
