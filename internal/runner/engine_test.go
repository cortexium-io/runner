package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// These integration tests own coordinator/persistence/recovery behavior. They
// use the real SQLite and file adapters; configuration/MCP contracts live in
// their owning packages rather than repeating these scenarios per transport.
func policy() runner.StoragePolicy {
	p := runner.DefaultStorage()
	p.MinFree = 0
	p.MaxManaged = 8 << 20
	p.Database.MaxSize = 2 << 20
	p.Database.MaxRuns = 20
	p.MaxArtifact = 16 << 10
	p.Logs.MaxPerAttempt = 1024
	return p
}
func snapshot(p runner.StoragePolicy) runner.Snapshot {
	role := runner.Role{
		ID:        "worker",
		Inputs:    map[string]string{"text": "text"},
		Outputs:   map[string]string{"result": "text"},
		Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Input: "text", Output: "result"},
	}
	proc := runner.Process{
		ID:                 "demo",
		Inputs:             map[string]string{"text": "text"},
		Steps:              []runner.Step{{ID: "work", Role: "worker", Inputs: map[string]runner.Binding{"text": {FromInput: "text"}}}},
		Outputs:            map[string]runner.Binding{"result": {FromStep: "work", Output: "result"}},
		MaxAttempts:        2,
		MaxAttemptsPerStep: 2,
		AttemptTimeout:     "3s",
	}
	project := runner.Project{
		ID:             "demo",
		DefaultProcess: "demo",
		Roles:          map[string]runner.Role{"worker": role},
		Profiles:       map[string]string{},
		Processes:      map[string]runner.Process{"demo": proc},
	}
	return runner.Snapshot{
		Instance: runner.Instance{Concurrency: 2, ReconcileInterval: "20ms", Storage: p},
		Projects: map[string]runner.Project{"demo": project},
	}
}

type rig struct {
	core    *runner.Core
	store   *sqlite.Store
	disk    *runner.Disk
	backend *execution.Deterministic
}

func openRig(t *testing.T, dir string, s runner.Snapshot, wrap func(runner.Executor) runner.Executor) *rig {
	t.Helper()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := sqlite.Open(dir, s.Instance.Storage)
	if e != nil {
		t.Fatal(e)
	}
	disk, e := runner.OpenDisk(dir, s.Instance.Storage)
	if e != nil {
		store.Close()
		t.Fatal(e)
	}
	backend := execution.New(disk, func(ctx context.Context, id string) ([]byte, error) {
		if _, e := store.Artifact(ctx, id); e != nil {
			return nil, e
		}
		return disk.Read("artifacts/"+id, s.Instance.Storage.MaxArtifact)
	})
	var executor runner.Executor = backend
	if wrap != nil {
		executor = wrap(executor)
	}
	core, e := runner.New(context.Background(), store, executor, disk, s)
	if e != nil {
		store.Close()
		t.Fatal(e)
	}
	return &rig{core, store, disk, backend}
}
func (r *rig) close() { r.core.Close(); _ = r.store.Close() }
func begin(t *testing.T, c *runner.Core, key, project string) runner.Run {
	t.Helper()
	r, e := c.Start(runner.StartRequest{RequestID: key, Project: project, Inputs: map[string]runner.Value{"text": runner.Text("Hello")}})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func await(t *testing.T, c *runner.Core, id string, predicate func(runner.Run) bool) runner.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	for {
		r, e := c.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if predicate(r) {
			return r
		}
		if _, e = c.Wait(ctx, id, r.Revision); e != nil {
			t.Fatalf("waiting: %v; last state: %+v", e, r)
		}
	}
}
func terminal(r runner.Run) bool { return r.Terminal() }
func released(r runner.Run) bool {
	if !r.Terminal() {
		return false
	}
	for _, a := range r.Attempts {
		if !a.Released {
			return false
		}
	}
	return true
}

func TestExecutionOutcomesAndAllowances(t *testing.T) {
	for _, tc := range []struct {
		name, mode, want string
		attempts         int
	}{
		{"success", "echo", "Completed", 1},
		{"bounded failure", "fail", "Failed", 2},
		{"escalation", "needs_help", "Completed", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := policy()
			s := snapshot(p)
			project := s.Projects["demo"]
			role := project.Roles["worker"]
			role.Execution.Mode = tc.mode
			if tc.mode == "needs_help" {
				local := *role.Execution
				sol := local
				sol.Mode = "echo"
				role.Execution = nil
				role.DefaultProfile = "local"
				role.Profiles = map[string]runner.Profile{"local": local, "sol": sol}
				proc := project.Processes["demo"]
				proc.Steps[0].Escalation = &runner.Escalation{On: "needs_help", Profile: "sol", MaxTimes: 1, Exhausted: "wait"}
				project.Processes["demo"] = proc
			}
			project.Roles["worker"] = role
			s.Projects["demo"] = project
			r := openRig(t, t.TempDir(), s, nil)
			defer r.close()
			run := begin(t, r.core, "start", "demo")
			done := await(t, r.core, run.ID, released)
			if done.Status != tc.want || len(done.Attempts) != tc.attempts {
				t.Fatalf("got %s/%d attempts: %s", done.Status, len(done.Attempts), done.Reason)
			}
			if done.Status == "Completed" && string(done.Outputs["result"].Inline) != `"Hello"` {
				t.Fatalf("wrong output: %+v", done.Outputs)
			}
			if tc.mode == "needs_help" && done.Attempts[1].Profile != "sol" {
				t.Fatal("did not select complete escalation profile")
			}
			again, e := r.core.Start(runner.StartRequest{RequestID: "start", Project: "demo", Inputs: map[string]runner.Value{"text": runner.Text("Hello")}})
			if e != nil || again.ID != run.ID {
				t.Fatalf("idempotent start: %+v %v", again, e)
			}
			_, e = r.core.Start(runner.StartRequest{
				RequestID: "start",
				Project:   "demo",
				Inputs:    map[string]runner.Value{"text": runner.Text("different")},
			})
			if e == nil {
				t.Fatal("reused request identity accepted different work")
			}
		})
	}
}
func TestManualRetryRetainsHistory(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.MaxAttempts = 3
	p.Processes["demo"] = proc // Step allowance can be exhausted while run allowance remains.
	role := p.Roles["worker"]
	bad := *role.Execution
	bad.Mode = "fail"
	good := *role.Execution
	role.Execution = nil
	role.DefaultProfile = "bad"
	role.Profiles = map[string]runner.Profile{"bad": bad, "good": good}
	p.Roles["worker"] = role
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	run := begin(t, r.core, "initial", "demo")
	await(t, r.core, run.ID, released)
	req := runner.RetryRequest{RequestID: "retry", RunID: run.ID, Profile: "good"}
	if _, e := r.core.Retry(req); e == nil {
		t.Fatal("exhausted allowance silently reset")
	}
	req.ExtraAttempts = 1
	if _, e := r.core.Retry(req); e != nil {
		t.Fatal(e)
	}
	done := await(t, r.core, run.ID, released)
	if done.Status != "Completed" || len(done.Attempts) != 3 {
		t.Fatalf("history lost: %+v", done)
	}
	if _, e := r.core.Retry(req); e != nil {
		t.Fatal(e)
	}
	again, _ := r.core.Get(run.ID)
	if len(again.Attempts) != 3 {
		t.Fatal("duplicate Retry created another attempt")
	}
}

type startUnknown struct {
	runner.Executor
	mu        sync.Mutex
	unknownID string
	starts    map[string]int
	resolve   chan struct{}
}

func (e *startUnknown) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	e.starts[a.AttemptID]++
	first := e.unknownID == ""
	if first {
		e.unknownID = a.AttemptID
	}
	e.mu.Unlock()
	o, err := e.Executor.Start(ctx, a)
	if first && err == nil {
		return runner.Observation{}, errors.New("launch acknowledgement lost")
	}
	return o, err
}
func (e *startUnknown) Observe(ctx context.Context, id string) (runner.Observation, error) {
	e.mu.Lock()
	unknown := id == e.unknownID
	e.mu.Unlock()
	if unknown {
		select {
		case <-e.resolve:
		default:
			return runner.Observation{State: runner.Unknown, Reason: "controlled observation unavailable"}, nil
		}
	}
	return e.Executor.Observe(ctx, id)
}
func TestUnknownExecutionDoesNotDuplicateOrBlockOtherProject(t *testing.T) {
	s := snapshot(policy())
	other := runner.Clone(s.Projects["demo"])
	other.ID = "other"
	s.Projects["other"] = other
	var wrapped *startUnknown
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		wrapped = &startUnknown{Executor: e, starts: map[string]int{}, resolve: make(chan struct{})}
		return wrapped
	})
	defer r.close()
	first := begin(t, r.core, "first", "demo")
	await(t, r.core, first.ID, func(r runner.Run) bool { return r.Status == "Waiting" && r.Current().State == runner.Unknown })
	if _, e := r.core.Retry(runner.RetryRequest{RequestID: "unsafe", RunID: first.ID, ExtraAttempts: 1}); e == nil {
		t.Fatal("unknown execution was retried")
	}
	second := begin(t, r.core, "second", "other")
	if done := await(t, r.core, second.ID, terminal); done.Status != "Completed" {
		t.Fatal(done.Reason)
	}
	wrapped.mu.Lock()
	n := wrapped.starts[wrapped.unknownID]
	wrapped.mu.Unlock()
	if n != 1 {
		t.Fatalf("unknown execution launched %d times", n)
	}
	close(wrapped.resolve)
	r.core.Wake()
	if done := await(t, r.core, first.ID, released); done.Status != "Completed" || len(done.Attempts) != 1 {
		t.Fatalf("reconciliation duplicated work: %+v", done)
	}
}

type deferObserve struct{ runner.Executor }

func (e deferObserve) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	o, err := e.Executor.Start(ctx, a)
	o.NextCheck = time.Now().Add(time.Hour)
	return o, err
}
func TestRestartCollectsExistingResultAndPreservesArtifact(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	prepare := p.Roles["worker"]
	prepare.Execution.Mode = "file"
	prepare.Outputs["result"] = "artifact"
	finish := runner.Clone(prepare)
	finish.ID = "finish"
	finish.Inputs["text"] = "artifact"
	finish.Outputs["result"] = "text"
	finish.Execution.Mode = "echo"
	p.Roles["worker"] = prepare
	p.Roles["finish"] = finish
	proc := p.Processes["demo"]
	proc.Steps = append(proc.Steps, runner.Step{
		ID:     "finish",
		Role:   "finish",
		Inputs: map[string]runner.Binding{"text": {FromStep: "work", Output: "result"}},
	})
	proc.Outputs["result"] = runner.Binding{FromStep: "finish", Output: "result"}
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	dir := t.TempDir()
	r := openRig(t, dir, s, func(e runner.Executor) runner.Executor { return deferObserve{e} })
	run := begin(t, r.core, "restart", "demo")
	active := await(t, r.core, run.ID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	id := active.Current().ID
	r.core.Close()
	if _, e := r.backend.Observe(context.Background(), id); e != nil {
		t.Fatal(e)
	}
	if e := r.store.Close(); e != nil {
		t.Fatal(e)
	}
	r2 := openRig(t, dir, s, nil)
	defer r2.close()
	done := await(t, r2.core, run.ID, released)
	if done.Status != "Completed" || len(done.Attempts) != 2 || done.Attempts[0].ID != id {
		t.Fatalf("restart replayed: %+v", done)
	}
	artifact := done.Attempts[0].End.Result.Outputs["result"].ArtifactID
	b, e := r2.disk.Read("artifacts/"+artifact, s.Instance.Storage.MaxArtifact)
	if e != nil || string(b) != "Hello" {
		t.Fatalf("artifact lost: %q %v", b, e)
	}
	if _, e = os.Stat(filepath.Join(dir, "work", id)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("workspace retained after release: %v", e)
	}
}
func TestCancellationConfirmsStop(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	role := p.Roles["worker"]
	role.Execution.Delay = "1h"
	p.Roles["worker"] = role
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	run := begin(t, r.core, "cancel", "demo")
	await(t, r.core, run.ID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	if _, e := r.core.Cancel(run.ID); e != nil {
		t.Fatal(e)
	}
	done := await(t, r.core, run.ID, released)
	if done.Status != "Cancelled" || done.Attempts[0].End.Kind != "stopped" {
		t.Fatalf("cancellation lacked confirmation: %+v", done)
	}
}
func TestStorageLimitsAndCollection(t *testing.T) {
	p := policy()
	p.KeepRuns = "1ns"
	p.Logs.KeepFor = "1ns"
	s := snapshot(p)
	project := s.Projects["demo"]
	role := project.Roles["worker"]
	role.Execution.LogBytes = 1 << 20
	project.Roles["worker"] = role
	s.Projects["demo"] = project
	dir := t.TempDir()
	r := openRig(t, dir, s, nil)
	defer r.close()
	run := begin(t, r.core, "storage", "demo")
	done := await(t, r.core, run.ID, released)
	a := done.Attempts[0]
	info, e := os.Stat(filepath.Join(dir, "logs", a.ID))
	if e != nil || info.Size() != int64(p.Logs.MaxPerAttempt) {
		t.Fatalf("log not capped: %v %v", info, e)
	}
	if a.DroppedLogBytes != (1<<20)-int64(p.Logs.MaxPerAttempt) {
		t.Fatal("missing truncation accounting")
	}
	before := r.disk.Used()
	if e = r.core.Collect(); e != nil {
		t.Fatal(e)
	}
	if r.disk.Used() >= before {
		t.Fatal("expired managed data not reclaimed")
	}
	if _, e = r.core.Get(run.ID); !errors.Is(e, runner.ErrNotFound) {
		t.Fatal("expired terminal history retained")
	}
	if e = r.disk.Write("artifacts/large/file", make([]byte, int(p.MaxArtifact)+1), p.MaxArtifact); !errors.Is(e, runner.ErrCapacity) {
		t.Fatal("oversized artifact accepted", e)
	}
	req := runner.StartRequest{
		RequestID: "large",
		Project:   "demo",
		Inputs:    map[string]runner.Value{"text": runner.Text(strings.Repeat("x", int(p.Database.MaxRecord)))},
	}
	if _, e = r.core.Start(req); e == nil {
		t.Fatal("oversized input/evidence record accepted")
	}
	var total int64
	_ = filepath.WalkDir(dir, func(path string, entry os.DirEntry, e error) error {
		if e == nil && !entry.IsDir() {
			i, _ := entry.Info()
			total += i.Size()
		}
		return e
	})
	if total > int64(p.MaxManaged) {
		t.Fatal("state files exceeded allowance")
	}
}
func TestStoreOwnershipAndRecordCap(t *testing.T) {
	p := policy()
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := sqlite.Open(dir, p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if other, e := sqlite.Open(dir, p); e == nil {
		other.Close()
		t.Fatal("two coordinators acquired same instance")
	}
	r := runner.Run{ID: "large", ProjectID: "demo", Reason: strings.Repeat("e", int(p.Database.MaxRecord)+1)}
	if e = s.Save(context.Background(), r, nil, nil); !errors.Is(e, runner.ErrCapacity) {
		t.Fatal("oversized persistence accepted", e)
	}
	rows, e := s.Load(context.Background())
	if e != nil || len(rows) != 0 {
		t.Fatal("rejected write changed state")
	}
}
func TestStoreCapacityReservesExistingRunUpdates(t *testing.T) {
	p := policy()
	p.Database.MaxRuns = 1
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	s, e := sqlite.Open(dir, p)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	r := runner.Run{ID: "one", ProjectID: "demo", Status: "Queued"}
	if e = s.Save(ctx, r, nil, nil); e != nil {
		t.Fatal(e)
	}
	if e = s.Save(ctx, runner.Run{ID: "two", ProjectID: "demo"}, nil, nil); !errors.Is(e, runner.ErrCapacity) {
		t.Fatal("retained-run capacity not enforced", e)
	}
	r.Status = "Completed"
	if e = s.Save(ctx, r, nil, nil); e != nil {
		t.Fatal("existing progress could not be finalized", e)
	}
	b, _ := json.Marshal(r)
	if len(b) >= int(p.Database.MaxRecord) {
		t.Fatal("invalid fixture")
	}
}

type corruptResult struct {
	runner.Executor
	value runner.Value
}

func (e corruptResult) Observe(ctx context.Context, id string) (runner.Observation, error) {
	o, err := e.Executor.Observe(ctx, id)
	if o.End != nil && o.End.Result != nil {
		o.End.Result.Outputs["result"] = e.value
	}
	return o, err
}
func TestUnusableExecutorResultsStayBounded(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value runner.Value
	}{
		{"malformed JSON", runner.Value{Inline: json.RawMessage(`{broken`)}},
		{"oversized metadata", runner.Text(strings.Repeat("x", 16<<10))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := snapshot(policy())
			p := s.Projects["demo"]
			proc := p.Processes["demo"]
			proc.MaxAttempts = 1
			p.Processes["demo"] = proc
			s.Projects["demo"] = p
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor { return corruptResult{e, tc.value} })
			defer r.close()
			run := begin(t, r.core, "bad-result", "demo")
			done := await(t, r.core, run.ID, released)
			if done.Status != "Failed" || !strings.Contains(done.Reason, "malformed or oversized") || len(done.Outputs) != 0 {
				t.Fatalf("unusable evidence escaped boundary: %+v", done)
			}
		})
	}
}

func TestRunAllowanceCoversAllSteps(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	proc := p.Processes["demo"]
	proc.MaxAttempts = 1
	proc.Steps = append(proc.Steps, runner.Step{
		ID:     "again",
		Role:   "worker",
		Inputs: map[string]runner.Binding{"text": {FromStep: "work", Output: "result"}},
	})
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	run := begin(t, r.core, "all-steps", "demo")
	done := await(t, r.core, run.ID, func(r runner.Run) bool { return r.Status == "Waiting" })
	if len(done.Attempts) != 1 || done.Step != 1 {
		t.Fatal("a later step exceeded the run allowance")
	}
}

func TestReloadKeepsActiveRunSnapshot(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	role := p.Roles["worker"]
	role.Execution.Delay = "1h"
	p.Roles["worker"] = role
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	old := begin(t, r.core, "old", "demo")
	await(t, r.core, old.ID, func(r runner.Run) bool { return r.Active() })
	next := runner.Clone(s)
	p = next.Projects["demo"]
	role = p.Roles["worker"]
	role.Execution.Delay = ""
	role.Execution.Mode = "fail"
	p.Roles["worker"] = role
	next.Projects["demo"] = p
	r.core.Apply(next, errors.New("invalid manual edit"))
	if r.core.Status().ConfigError == "" {
		t.Fatal("rejected edit invisible")
	}
	r.core.Apply(next, nil)
	fresh := begin(t, r.core, "new", "demo")
	if done := await(t, r.core, fresh.ID, released); done.Status != "Failed" {
		t.Fatal("new run ignored edited profile")
	}
	current, _ := r.core.Get(old.ID)
	if current.Roles["worker"].Execution.Mode != "echo" || current.Roles["worker"].Execution.Delay != "1h" {
		t.Fatal("active snapshot changed")
	}
	if _, err := r.core.Cancel(old.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, old.ID, released)
}

type uncertainStop struct {
	runner.Executor
	mu    sync.Mutex
	stops int
}

func (e *uncertainStop) Stop(context.Context, string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stops++
	return errors.New("stop acknowledgement unavailable")
}
func (e *uncertainStop) Observe(ctx context.Context, id string) (runner.Observation, error) {
	e.mu.Lock()
	settled := e.stops >= 3
	e.mu.Unlock()
	if settled {
		_ = e.Executor.Stop(ctx, id)
	}
	return e.Executor.Observe(ctx, id)
}
func TestStopExhaustionStillReconcilesSettlement(t *testing.T) {
	s := snapshot(policy())
	p := s.Projects["demo"]
	role := p.Roles["worker"]
	role.Execution.Delay = "1h"
	p.Roles["worker"] = role
	s.Projects["demo"] = p
	var wrapped *uncertainStop
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor { wrapped = &uncertainStop{Executor: e}; return wrapped })
	defer r.close()
	run := begin(t, r.core, "stop-recovery", "demo")
	await(t, r.core, run.ID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	if _, err := r.core.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, run.ID, released)
	wrapped.mu.Lock()
	stops := wrapped.stops
	wrapped.mu.Unlock()
	if done.Status != "Cancelled" || stops != 3 {
		t.Fatalf("stop bound/settlement lost: %s, %d stops", done.Status, stops)
	}
}

func TestRetentionPreservesArtifactsWithConsumers(t *testing.T) {
	s := snapshot(policy())
	s.Instance.Storage.KeepRuns = "1ns"
	p := s.Projects["demo"]
	producer := runner.Clone(p.Roles["worker"])
	producer.Execution.Mode = "file"
	producer.Outputs["result"] = "artifact"
	p.Roles["worker"] = producer
	consume := runner.Clone(producer)
	consume.ID = "consumer"
	consume.Inputs["text"] = "artifact"
	consume.Execution.Delay = "1h"
	p.Roles["consumer"] = consume
	proc := runner.Clone(p.Processes["demo"])
	proc.ID = "consume"
	proc.Inputs["text"] = "artifact"
	proc.Steps[0].Role = "consumer"
	p.Processes["consume"] = proc
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	first := begin(t, r.core, "produce", "demo")
	done := await(t, r.core, first.ID, released)
	artifact := done.Outputs["result"]
	second, err := r.core.Start(runner.StartRequest{
		RequestID: "consume",
		Project:   "demo",
		Process:   "consume",
		Inputs:    map[string]runner.Value{"text": artifact},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err = r.core.Get(first.ID); err != nil {
		t.Fatal("producer deleted with a retained consumer", err)
	}
	if _, err = r.disk.ArtifactSize(artifact.ArtifactID); err != nil {
		t.Fatal("consumer artifact deleted", err)
	}
	if _, err = r.core.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, second.ID, released)
	// A producer may be visited before its expired consumer; the next collection
	// then removes it. No arbitrary wait or order assumption is needed.
	for i := 0; i < 2; i++ {
		if err = r.core.Collect(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.disk.ArtifactSize(artifact.ArtifactID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unreferenced expired artifact retained", err)
	}
}

func TestStorageAdmissionLeavesRoomToCancel(t *testing.T) {
	s := snapshot(policy())
	s.Instance.Storage.MaxManaged = s.Instance.Storage.Database.MaxSize + 100<<10
	p := s.Projects["demo"]
	role := p.Roles["worker"]
	role.Execution.Delay = "1h"
	p.Roles["worker"] = role
	s.Projects["demo"] = p
	r := openRig(t, t.TempDir(), s, nil)
	defer r.close()
	first := begin(t, r.core, "admitted", "demo")
	await(t, r.core, first.ID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	second := begin(t, r.core, "capacity-wait", "demo")
	queued := await(t, r.core, second.ID, func(r runner.Run) bool { return strings.Contains(r.Reason, "waiting for managed storage") })
	if len(queued.Attempts) != 0 {
		t.Fatal("storage pressure admitted more execution")
	}
	if _, err := r.core.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	if done := await(t, r.core, first.ID, released); done.Status != "Cancelled" {
		t.Fatal("admitted work could not settle")
	}
	await(t, r.core, second.ID, func(r runner.Run) bool { return r.Current() != nil && r.Current().State == runner.Running })
	if _, err := r.core.Cancel(second.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, second.ID, released)
}

type rejectSourceStore struct {
	runner.Store
	mu     sync.Mutex
	reject bool
}

func (s *rejectSourceStore) Save(ctx context.Context, r runner.Run, arts []runner.Artifact, receipt *runner.Receipt) error {
	s.mu.Lock()
	reject := s.reject
	s.mu.Unlock()
	if reject {
		for _, a := range r.Attempts {
			if a.Source != nil && a.State == runner.Finished {
				return runner.ErrCapacity
			}
		}
	}
	return s.Store.Save(ctx, r, arts, receipt)
}

type sourceObservationExecutor struct {
	runner.Executor
	disk *runner.Disk
}

func (e *sourceObservationExecutor) Observe(ctx context.Context, id string) (runner.Observation, error) {
	o, err := e.Executor.Observe(ctx, id)
	if err == nil && o.State == runner.Finished {
		patch := id + "/source.patch"
		if err = e.disk.Write("artifacts/"+patch, []byte("retained partial source"), 1024); err != nil {
			return runner.Observation{}, err
		}
		o.Source = &runner.SourceResult{
			Repository: "fixture",
			Base:       "recorded-base",
			WorkDir:    "owned-source",
			AttemptID:  id,
			PatchID:    patch,
		}
	}
	return o, err
}

// A SQLite failure after file capture must hold the attempt for reconciliation,
// rather than mark it failed/releasable and lose its retained source reference.
func TestSourceCommitFailureRetainsRecoverableAttempt(t *testing.T) {
	r := openRig(t, t.TempDir(), snapshot(policy()), nil)
	r.core.Close()
	store := &rejectSourceStore{Store: r.store, reject: true}
	core, e := runner.New(context.Background(), store, &sourceObservationExecutor{Executor: r.backend, disk: r.disk}, r.disk, snapshot(policy()))
	if e != nil {
		t.Fatal(e)
	}
	r.core = core
	defer r.close()
	run := begin(t, core, "source-commit", "demo")
	held := await(t, core, run.ID, func(r runner.Run) bool {
		return r.Status == "Waiting" && r.Current() != nil && r.Current().Source != nil
	})
	if !held.Active() || held.Current().Released {
		t.Fatal("source failure released its attempt")
	}
	if _, e = r.disk.ArtifactSize(held.Current().Source.PatchID); e != nil {
		t.Fatal("source file lost", e)
	}
	store.mu.Lock()
	store.reject = false
	store.mu.Unlock()
	core.ExecutionChanged(held.Current().ID)
	done := await(t, core, run.ID, func(r runner.Run) bool { return r.Status == "Completed" })
	if len(done.Attempts) != 1 {
		t.Fatal("source persistence retried development")
	}
	if _, e = r.store.Artifact(context.Background(), done.Attempts[0].Source.PatchID); e != nil {
		t.Fatal("source artifact was not registered", e)
	}
}
