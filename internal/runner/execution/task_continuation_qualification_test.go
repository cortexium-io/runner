package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// This opt-in maintenance proof reads the quiesced pilot's real capture/pins,
// but all Core/store writes and dispatches use disposable private state. It
// never starts a model, product command or browser.
func TestTaskContinuationClosedPilotQualification(t *testing.T) {
	file := os.Getenv("RUNNER_CONTINUATION_PREFLIGHT")
	if file == "" {
		t.Skip("set RUNNER_CONTINUATION_PREFLIGHT to the private closed-checkpoint qualification descriptor")
	}
	var q struct {
		State    string `json:"state"`
		Database string `json:"database"`
		Config   string `json:"config"`
		Request  string `json:"request"`
		Report   string `json:"report"`
	}
	data, err := os.ReadFile(file)
	if err != nil || json.Unmarshal(data, &q) != nil {
		t.Fatal("private preflight descriptor is invalid", err)
	}
	loader, err := config.New(q.Config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("combined runtime cannot load preserved configuration", err)
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := os.Open(q.Database)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := os.OpenFile(filepath.Join(private, "process-core.sqlite"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	_, err = io.Copy(copy, db)
	db.Close()
	closeErr := copy.Close()
	if err != nil || closeErr != nil {
		t.Fatal("copy closed checkpoint", err, closeErr)
	}
	// Artifacts are immutable. Hardlinks avoid duplicating historical payloads;
	// private Core cleanup unlinks only its own names. Jobs are not shared.
	if err := filepath.WalkDir(filepath.Join(q.State, "artifacts"), func(source string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(q.State, source)
		if err != nil {
			return err
		}
		target := filepath.Join(private, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("checkpoint artifact is not a regular file: %s", relative)
		}
		return os.Link(source, target)
	}); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal("combined reader cannot migrate private historical state", err)
	}
	defer store.Close()
	before, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Use the real retained source/capture metadata for read-only authentication.
	// Do not OpenDisk here: its housekeeping belongs to the live owner, not this
	// private proof. The actual service's Start/Observe/Release are never called.
	originalDisk := &runner.Disk{Root: q.State, Policy: snapshot.Instance.Storage}
	// Construct only the validator: a full Service loads native jobs and its
	// Close would own shutdown/cleanup in the original directory.
	source := &Service{
		disk: originalDisk,
		workspaces: &Workspaces{
			disk: originalDisk,
			read: func(_ context.Context, id string) ([]byte, error) {
				return originalDisk.Read("artifacts/"+id, snapshot.Instance.Storage.MaxArtifact)
			},
		},
	}
	executor := &continuationPreflightExecutor{source: source, started: make(chan struct{})}
	disk, err := runner.OpenDisk(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	core, err := runner.New(t.Context(), store, executor, disk, snapshot)
	if err != nil {
		t.Fatal("combined Core cannot restore actual history", err)
	}
	defer core.Close()
	var wire struct {
		runner.ContinueTaskRequest
		Inputs    map[string]string `json:"inputs"`
		Artifacts map[string]string `json:"artifacts"`
	}
	data, err = os.ReadFile(q.Request)
	if err != nil || json.Unmarshal(data, &wire) != nil {
		t.Fatal("prepared owner request is invalid", err)
	}
	request := wire.ContinueTaskRequest
	request.Inputs = map[string]runner.Value{}
	for name, text := range wire.Inputs {
		request.Inputs[name] = runner.Text(text)
	}
	for name, id := range wire.Artifacts {
		request.Inputs[name] = runner.Value{ArtifactID: id}
	}
	continued, err := core.ContinueTask(request)
	if err != nil {
		t.Fatal("real source/capture/check authority cannot continue privately", err)
	}
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("private dispatch interception did not observe the queued continuation")
	}
	continued, err = core.Get(continued.ID)
	if err != nil || continued.TaskBudget == nil || continued.TaskBudget.PriorAttempts != 36 || continued.TaskBudget.Limit != 64 || continued.TaskBudget.Spent != 37 {
		t.Fatal("actual family accounting was not carried forward", continued.TaskBudget, err)
	}
	origin, err := core.Get(request.RunID)
	if err != nil || origin.Status != "Failed" || origin.ContinuedBy == nil || origin.ContinuedBy.RunID != continued.ID {
		t.Fatal("private original authority was not preserved/frozen", err)
	}
	if _, err := core.Retry(runner.RetryRequest{RequestID: "private-proof-old-retry", RunID: origin.ID, WithinBudget: true, ExtraAttempts: 1}); err == nil {
		t.Fatal("handed-off original can spend again")
	}
	replayed, err := core.ContinueTask(request)
	if err != nil || replayed.ID != continued.ID {
		t.Fatal("same owner request created another continuation", err)
	}
	core.Close()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal("reopen continued historical state", err)
	}
	defer store.Close()
	core, err = runner.New(t.Context(), store, executor, disk, snapshot)
	if err != nil {
		t.Fatal("restart did not authenticate persisted continuation links", err)
	}
	defer core.Close()
	replayed, err = core.ContinueTask(request)
	if err != nil || replayed.ID != continued.ID || replayed.TaskBudget == nil || replayed.TaskBudget.Spent != 37 {
		t.Fatal("restart did not retain the same continuation and debit", err)
	}
	if _, err := core.Retry(runner.RetryRequest{RequestID: "private-proof-restarted-old-retry", RunID: origin.ID, WithinBudget: true, ExtraAttempts: 1}); err == nil {
		t.Fatal("restarted original can spend again")
	}
	report := map[string]any{
		"status": "passed", "configuration": q.Config, "source_state": q.State,
		"history_runs_loaded": len(before), "origin": origin.ID,
		"private_continuation": continued.ID, "budget": continued.TaskBudget,
		"source_capture_and_pins": "authenticated by actual Service.ValidateSourceRecovery",
		"dispatch":                "intercepted; no model, command, browser or product mutation",
		"restart":                 "persisted real-history links authenticated; replay/debit and old-family freeze retained",
		"original_history":        "Failed result retained; original retry refused in private state",
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(q.Report, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("private history/capture/budget proof passed; descriptor SHA256 %x", sha256.Sum256(data))
}

type continuationPreflightExecutor struct {
	source  *Service
	started chan struct{}
	once    sync.Once
}

func (e *continuationPreflightExecutor) Start(context.Context, runner.Assignment) (runner.Observation, error) {
	e.once.Do(func() { close(e.started) })
	return e.pending(), nil
}
func (e *continuationPreflightExecutor) Observe(context.Context, string) (runner.Observation, error) {
	return e.pending(), nil
}
func (e *continuationPreflightExecutor) Stop(context.Context, string) error    { return nil }
func (e *continuationPreflightExecutor) Release(context.Context, string) error { return nil }
func (e *continuationPreflightExecutor) pending() runner.Observation {
	return runner.Observation{State: runner.Unknown, Reason: "private qualification forbids execution", NextCheck: time.Now().Add(time.Hour)}
}
func (e *continuationPreflightExecutor) ValidateSourceRecovery(ctx context.Context, r runner.Run, source runner.SourceResult) error {
	if _, err := os.Lstat(source.WorkDir); !os.IsNotExist(err) || !r.ResourcesReleased {
		return fmt.Errorf("read-only pilot proof requires the released absent workspace checkpoint")
	}
	return e.source.ValidateSourceRecovery(ctx, r, source)
}
