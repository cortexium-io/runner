package runner_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Embedding only Executor intentionally has no separate run-resource phase.
type gatedAttemptRelease struct {
	runner.Executor
	entered chan struct{}
	allow   chan struct{}
	once    sync.Once
}

func (e *gatedAttemptRelease) Release(ctx context.Context, id string) error {
	e.once.Do(func() { close(e.entered) })
	select {
	case <-e.allow:
		return e.Executor.Release(ctx, id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestTerminalRunWithoutSeparateResourcesSettlesAfterAttemptReleaseAndRestart(t *testing.T) {
	p, dir := policy(), t.TempDir()
	p.Database.MaxRuns = 0
	s := snapshot(p)
	gate := &gatedAttemptRelease{entered: make(chan struct{}), allow: make(chan struct{})}
	r := openRig(t, dir, s, func(e runner.Executor) runner.Executor {
		gate.Executor = e
		return gate
	})
	defer func() { r.close() }()
	started := begin(t, r.core, "optional-capacity", "demo")
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	select {
	case <-gate.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	pending, err := r.core.Get(started.ID)
	if err != nil || !pending.Terminal() || pending.ResourcesReleased || len(pending.Attempts) != 1 || pending.Attempts[0].Released {
		t.Fatal("run resources settled before attempt release", pending, err)
	}
	close(gate.allow)
	done := await(t, r.core, started.ID, func(r runner.Run) bool { return released(r) && r.ResourcesReleased })
	if done.Status != "Completed" || len(done.Attempts) != 1 {
		t.Fatal("attempt-only executor did not settle terminal resources", done.Status, done.Reason)
	}
	r.close()
	// Reproduce an older settled record without the run-level settlement flag.
	store, err := sqlite.Open(dir, p)
	if err != nil {
		t.Fatal(err)
	}
	done.ResourcesReleased = false
	if err := store.Save(t.Context(), done, nil, nil); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	r = openRig(t, dir, s, nil)
	restored := await(t, r.core, started.ID, func(r runner.Run) bool { return r.ResourcesReleased })
	if len(restored.Attempts) != 1 || restored.Attempts[0].ID != done.Attempts[0].ID || !reflect.DeepEqual(restored.Outputs, done.Outputs) {
		t.Fatal("settlement replay reran or changed accepted work")
	}
}
