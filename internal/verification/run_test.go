package verification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func requestFixture(t *testing.T, args ...string) Request {
	t.Helper()
	root, entry := fixture(t)
	if len(args) > 0 {
		entry.Args = args
	}
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	return Request{Entrypoint: "complete", Entry: entry, Directory: root, Repository: "owner/repo", AttemptID: "attempt", Boundary: execution.VerificationComplete,
		Observe: func(ctx context.Context) (Observation, error) {
			return ObserveCandidate(ctx, root, base, "approved", entry)
		},
	}
}

// Claim contention/crash behavior has real process fixtures in subprocess. This
// seam avoids touching the real operator's shared slot in input/receipt tests;
// the command, Git reads and descendant supervisor here remain real.
func ownedTestGrant(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
	ctx, _, err := subprocess.PrepareHarness(ctx)
	return ctx, nil, err
}

func TestRunProducesObservedReceiptAndDistinctIntervals(t *testing.T) {
	request := requestFixture(t)
	result, err := run(t.Context(), request, ownedTestGrant)
	if err != nil {
		t.Fatal(err)
	}
	if result.Receipt.Outcome != "passed" || !result.Receipt.CleanupResolved || result.Digest == "" {
		t.Fatalf("incomplete receipt: %+v", result)
	}
	if result.Receipt.WaitMilliseconds == nil || result.Receipt.RunMilliseconds == nil || result.Receipt.CleanupMilliseconds == nil {
		t.Fatal("observed timings unavailable")
	}
	if result.Receipt.RunStartedAt == nil || result.Receipt.RunFinishedAt == nil || result.Receipt.RunStartedAt.Before(result.Receipt.StartedAt) || result.Receipt.RunFinishedAt.After(result.Receipt.FinishedAt) {
		t.Fatal("actual supervised run interval unavailable or outside invocation")
	}
	if *result.Receipt.WaitMilliseconds+*result.Receipt.RunMilliseconds+*result.Receipt.CleanupMilliseconds > result.Receipt.FinishedAt.Sub(result.Receipt.StartedAt).Milliseconds() {
		t.Fatal("phase intervals overlap")
	}
	if result.Receipt.ExecutionID == result.Receipt.AttemptID {
		t.Fatal("execution identity collapsed into attempt")
	}
}

func TestRunRefusesChangedCandidateAtGrantBeforeCommand(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "launched")
	request := requestFixture(t, "-c", "touch \"$1\"", "fixture", marker)
	grant := func(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
		writeFixture(t, request.Directory, "src/source.txt", "changed")
		return ownedTestGrant(ctx)
	}
	result, err := run(t.Context(), request, grant)
	if err == nil {
		t.Fatal("changed candidate executed")
	}
	if result.Receipt.Outcome != "failed" || result.Receipt.WaitMilliseconds == nil || result.Receipt.RunMilliseconds != nil || !result.Receipt.CleanupResolved || result.Digest == "" {
		t.Fatalf("pre-execution refusal lost observed accounting: %+v", result)
	}
	if result.Receipt.RunStartedAt != nil || result.Receipt.RunFinishedAt != nil {
		t.Fatal("refusal fabricated a command execution interval")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("command launched before validation")
	}
}

func TestRunFailureAndCancellationNeverProducePassingProof(t *testing.T) {
	t.Run("command", func(t *testing.T) {
		request := requestFixture(t, "-c", "exit 7")
		result, err := run(t.Context(), request, ownedTestGrant)
		if err == nil || result.Receipt.Outcome != "failed" || !result.Receipt.CleanupResolved {
			t.Fatalf("failure lost: %+v %v", result, err)
		}
	})
	t.Run("wait", func(t *testing.T) {
		request := requestFixture(t)
		result, err := run(t.Context(), request, func(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
			return ctx, nil, context.Canceled
		})
		if !errors.Is(err, context.Canceled) || result.Receipt.Outcome != "canceled" || result.Receipt.WaitMilliseconds == nil || result.Receipt.RunMilliseconds != nil {
			t.Fatalf("wait accounting lost: %+v %v", result, err)
		}
	})
	t.Run("canceled at grant", func(t *testing.T) {
		request := requestFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		result, err := run(ctx, request, func(ctx context.Context) (context.Context, *subprocess.HeavyClaim, error) {
			cancel()
			return ownedTestGrant(ctx)
		})
		if !errors.Is(err, context.Canceled) || result.Receipt.Outcome != "canceled" || result.Receipt.WaitMilliseconds == nil || result.Receipt.RunMilliseconds != nil {
			t.Fatalf("grant cancellation accounting lost: %+v %v", result, err)
		}
	})
	t.Run("output changed", func(t *testing.T) {
		request := requestFixture(t, "-c", "printf bad > src/source.txt")
		result, err := run(t.Context(), request, ownedTestGrant)
		if err == nil || result.Receipt.Outcome == "passed" {
			t.Fatal("source mutation certified")
		}
	})
}
