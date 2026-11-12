package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func cardPreparationFixture(t *testing.T) (*taskSourceFixture, runner.Assignment, *scriptedPeer) {
	t.Helper()
	f, a, _, proof := unpublishedSourceFixture(t)
	cfg := workspaceSnapshot(t, f.repo)
	f.disk.Policy.Database.MaxRecord = 40 << 10
	c := NewCodex(f.disk, cfg.Instance.Codex)
	c.workspaces = f.service.workspaces
	c.validateCardRecovery = f.service.validateRecoveredCardPreparation
	f.service.Codex = c
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := f.service.Close(ctx); err != nil {
			t.Error("owned fixture service cleanup", err)
		}
	})
	peer := &scriptedPeer{done: make(chan struct{}), config: cfg.Instance.Codex, notify: c.receive}
	c.peer = peer
	c.generation = "fixture-generation"
	role := cfg.Projects["demo"].Roles["implement"]
	a.AttemptID = "attempt_card_preparation"
	a.Setup.Profile = *role.Execution
	a.Task = role.Purpose
	a.OutputTypes = role.Outputs
	a.Inputs = map[string]runner.Value{}
	a.UnpublishedCardRecovery = &proof
	a.Limits.MaxRecord = f.disk.Policy.Database.MaxRecord
	a.Deadline = time.Now().UTC().Add(20 * time.Minute)
	return f, a, peer
}

func awaitCardPreparation(t *testing.T, c *Codex, id string) runner.Observation {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		c.mu.Lock()
		changed := c.changed
		c.mu.Unlock()
		o, e := c.Observe(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if o.State == runner.Finished {
			return o
		}
		select {
		case <-changed:
		case <-time.After(25 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("preparation did not settle", o.Reason)
		}
	}
}
func peerStarts(p *scriptedPeer) int { p.mu.Lock(); defer p.mu.Unlock(); return p.starts }

func TestUnpublishedCardSlowPreparationRejoinsOneDurableDispatch(t *testing.T) {
	f, a, peer := cardPreparationFixture(t)
	c := f.service.Codex
	entered, gate := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	real := c.validateCardRecovery
	c.validateCardRecovery = func(ctx context.Context, a runner.Assignment) error {
		calls.Add(1)
		close(entered)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-gate:
		}
		return real(ctx, a)
	}
	start := time.Now()
	observer, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	o, e := f.service.Start(observer, a)
	cancel()
	if e != nil || o.State != runner.Running || time.Since(start) >= time.Second {
		t.Fatal("Start waited for full validation", o, e)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("owned preparation not started")
	}
	original, e := c.loadForPreparationTest(a.AttemptID)
	if e != nil {
		t.Fatal(e)
	}
	if original.Stage != cardRecoveryValidationStage || original.Workspace != nil || original.ThreadID != "" || original.TurnID != "" || !original.Assignment.Deadline.Equal(a.Deadline) {
		t.Fatal("wrong durable preparation", original.Stage)
	}
	// Deliberately exceed the ordinary 5s observer budget while the same owned
	// worker stays behind a deterministic gate, without expiring its 20m deadline.
	delay := time.NewTimer(5100 * time.Millisecond)
	defer delay.Stop()
	var longestJoin time.Duration
	for range 3 {
		ctx, done := context.WithTimeout(t.Context(), 50*time.Millisecond)
		joinStart := time.Now()
		o, e = c.Start(ctx, a)
		done()
		longestJoin = max(longestJoin, time.Since(joinStart))
		if e != nil || o.State != runner.Running || time.Since(joinStart) >= time.Second {
			t.Fatal(o, e)
		}
		ctx, done = context.WithTimeout(t.Context(), 50*time.Millisecond)
		joinStart = time.Now()
		o, e = c.Observe(ctx, a.AttemptID)
		done()
		longestJoin = max(longestJoin, time.Since(joinStart))
		if e != nil || o.State != runner.Running || time.Since(joinStart) >= time.Second {
			t.Fatal("Observe waited for validation", o, e)
		}
	}
	<-delay.C
	if peerStarts(peer) != 0 || calls.Load() != 1 {
		t.Fatal("duplicate or early native dispatch")
	}
	changed := runner.Clone(a)
	changed.Task += " foreign"
	if _, e = c.Start(t.Context(), changed); e == nil {
		t.Fatal("changed durable assignment accepted")
	}
	close(gate)
	o = awaitCardPreparation(t, c, a.AttemptID)
	if o.End == nil || o.End.Kind != "returned" || peerStarts(peer) != 1 || calls.Load() != 1 {
		t.Fatal("normal single dispatch failed", o.End, peerStarts(peer))
	}
	for range 2 {
		if _, e = c.Start(t.Context(), a); e != nil {
			t.Fatal(e)
		}
	}
	if peerStarts(peer) != 1 {
		t.Fatal("terminal replay started another native turn")
	}
	t.Logf("one preparation exceeded5s; short Start/Observe maximum=%s; elapsed=%s", longestJoin, time.Since(start))
}

func (c *Codex) loadForPreparationTest(id string) (nativeJob, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadLocked(id)
}

func TestUnpublishedCardPreparationFailureDriftCancelAndDeadlineNeverDispatch(t *testing.T) {
	for _, kind := range []string{"drift", "foreign job", "cancel", "deadline"} {
		t.Run(kind, func(t *testing.T) {
			f, a, peer := cardPreparationFixture(t)
			c := f.service.Codex
			entered, gate := make(chan struct{}), make(chan struct{})
			real := c.validateCardRecovery
			c.validateCardRecovery = func(ctx context.Context, a runner.Assignment) error {
				close(entered)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-gate:
				}
				return real(ctx, a)
			}
			if kind == "deadline" {
				a.Deadline = time.Now().UTC().Add(150 * time.Millisecond)
			}
			if _, e := f.service.Start(t.Context(), a); e != nil {
				t.Fatal(e)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("preparation not entered")
			}
			switch kind {
			case "drift":
				if e := f.disk.Write("artifacts/"+a.UnpublishedCardRecovery.Source.PatchID, []byte("changed after grant"), f.disk.Policy.MaxArtifact); e != nil {
					t.Fatal(e)
				}
			case "foreign job":
				if e := f.disk.Write("jobs/foreign.codex.json", []byte(`{"assignment":{"run_id":"run_recover"}}`), f.disk.Policy.Database.MaxRecord); e != nil {
					t.Fatal(e)
				}
			case "cancel":
				if e := c.Stop(t.Context(), a.AttemptID); e != nil {
					t.Fatal(e)
				}
			case "deadline":
				time.Sleep(170 * time.Millisecond)
			}
			close(gate)
			o := awaitCardPreparation(t, c, a.AttemptID)
			if o.End == nil || o.End.Kind == "returned" || o.End.Retryable || peerStarts(peer) != 0 {
				t.Fatal("failed/cancelled validation dispatched native turn", kind, o.End)
			}
			if kind == "cancel" && o.End.Kind != "stopped" {
				t.Fatal("cancellation lost", o.End)
			}
			j, e := c.loadForPreparationTest(a.AttemptID)
			if e != nil || j.Workspace != nil || j.ThreadID != "" || j.TurnID != "" {
				t.Fatal("pre-dispatch failure created native resources", e)
			}
			if _, e = c.Start(t.Context(), a); e != nil {
				t.Fatal(e)
			}
			if peerStarts(peer) != 0 {
				t.Fatal("failed visit replayed")
			}
			if kind == "foreign job" {
				_ = f.disk.RemoveFile("jobs/foreign.codex.json")
			}
			if e = c.Release(t.Context(), a.AttemptID); e != nil {
				t.Fatal("owned preparation did not clean", e)
			}
			if _, e = os.Stat(f.disk.Root + "/work/" + a.AttemptID); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("owned temporary lease remained", e)
			}
		})
	}
}

func TestUnpublishedCardPreparationRestartWaitsForInheritedLease(t *testing.T) {
	f, a, peer := cardPreparationFixture(t)
	c := f.service.Codex
	cfg := c.cfg
	shutdown, stop := context.WithTimeout(t.Context(), time.Second)
	if err := c.Close(shutdown); err != nil {
		t.Fatal(err)
	}
	stop()
	// The previous owner's atomic checkpoint preceded its worker. Reopening
	// observes the same job; an inherited child lease forbids overlapping work.
	c = &Codex{disk: f.disk, jobs: map[string]nativeJob{}, changed: make(chan struct{})}
	c.mu.Lock()
	e := c.saveLocked(nativeJob{Assignment: a, Stage: cardRecoveryValidationStage})
	c.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	f.service = NewService(f.disk, nil, cfg)
	c = f.service.Codex
	peer = &scriptedPeer{done: make(chan struct{}), config: cfg, notify: c.receive}
	c.peer, c.generation = peer, "reopened-generation"
	_, lease, e := f.service.workspaces.taskLease(a)
	if e != nil {
		t.Fatal(e)
	}
	var calls atomic.Int32
	real := c.validateCardRecovery
	c.validateCardRecovery = func(ctx context.Context, a runner.Assignment) error { calls.Add(1); return real(ctx, a) }
	if _, e = c.Observe(t.Context(), a.AttemptID); e != nil {
		t.Fatal(e)
	}
	// Join the failed lease acquisition without arbitrary elapsed-time guesses.
	for {
		c.mu.Lock()
		running := c.preparing[a.AttemptID]
		j, _ := c.loadLocked(a.AttemptID)
		changed := c.changed
		c.mu.Unlock()
		if !running && j.Reason != "" {
			if j.End != nil {
				t.Fatal("held lease falsely settled")
			}
			break
		}
		select {
		case <-changed:
		case <-time.After(10 * time.Millisecond):
		}
	}
	if calls.Load() != 0 || peerStarts(peer) != 0 {
		t.Fatal("inherited helper lease bypassed")
	}
	if e = c.Release(t.Context(), a.AttemptID); e == nil {
		t.Fatal("unsettled inherited worker released")
	}
	lease.Close()
	o := awaitCardPreparation(t, c, a.AttemptID)
	if o.End == nil || o.End.Kind != "returned" || calls.Load() != 1 || peerStarts(peer) != 1 {
		t.Fatal("same durable job failed to rejoin", o.End, calls.Load())
	}
	j, e := c.loadForPreparationTest(a.AttemptID)
	if e != nil || !reflect.DeepEqual(j.Assignment, a) || j.Reason != "" {
		t.Fatal("restart changed original assignment/deadline", e)
	}
}

func TestUnpublishedCardPreparationIdentityExceptionIsExact(t *testing.T) {
	f, a, _ := cardPreparationFixture(t)
	for _, change := range []func(*runner.Assignment){
		func(a *runner.Assignment) { a.Setup.Profile.Harness = "command" },
		func(a *runner.Assignment) { a.Setup.Profile.Access = "workspace-read" },
		func(a *runner.Assignment) { a.TaskSource = nil },
		func(a *runner.Assignment) { a.TaskSource.Identity.ItemID = "foreign" },
	} {
		bad := runner.Clone(a)
		change(&bad)
		o, err := f.service.Start(t.Context(), bad)
		if err != nil || o.End == nil || o.End.Kind != "failed" {
			t.Fatal("invalid recovery profile/authority routed to another harness", o, err)
		}
		if _, err := os.Stat(f.disk.Root + "/" + nativeName(a.AttemptID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid recovery allocated a native job", err)
		}
	}
	j := nativeJob{Assignment: a, Stage: cardRecoveryValidationStage}
	cases := []func(*nativeJob){
		func(j *nativeJob) { j.Assignment.RunID = "foreign" },
		func(j *nativeJob) { j.Assignment.Task += "foreign" },
		func(j *nativeJob) { j.Stage = "active" },
		func(j *nativeJob) { j.ThreadID = "foreign" },
		func(j *nativeJob) { j.TurnID = "foreign" },
		func(j *nativeJob) { j.Workspace = &workspaceRecord{} },
		func(j *nativeJob) { j.StopRequested = true },
	}
	for _, mutate := range cases {
		bad := runner.Clone(j)
		mutate(&bad)
		raw, _ := json.Marshal(bad)
		if recoveryPreparationOwns(raw, a) {
			t.Fatal("unsafe own-job exception")
		}
		if err := f.disk.Write(nativeName(a.AttemptID), raw, f.disk.Policy.Database.MaxRecord); err != nil {
			t.Fatal(err)
		}
		if err := f.service.validateRecoveredCardPreparation(t.Context(), a); err == nil {
			t.Fatal("inconsistent durable claim passed actual source validation")
		}
	}
	raw, _ := json.Marshal(j)
	if !recoveryPreparationOwns(raw, a) {
		t.Fatal("exact preparation not recognized")
	}
	if err := f.disk.RemoveFile(nativeName(a.AttemptID)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.validateRecoveredCardPreparation(t.Context(), a); err == nil {
		t.Fatal("missing durable claim accepted")
	}
}
