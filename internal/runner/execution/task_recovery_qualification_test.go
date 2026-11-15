package execution

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// This maintenance proof migrates a closed real checkpoint only in disposable
// private state. Every dispatch is intercepted: no Service, model, product
// command, source materialization or browser is constructed or executed.
func TestTaskRecoveryClosedPilotQualification(t *testing.T) {
	descriptor := os.Getenv("RUNNER_TASK_RECOVERY_PREFLIGHT")
	if descriptor == "" {
		t.Skip("set RUNNER_TASK_RECOVERY_PREFLIGHT to the private closed-checkpoint qualification descriptor")
	}
	var q struct {
		State    string `json:"state"`
		Database string `json:"database"`
		Config   string `json:"config"`
		Request  string `json:"request"`
		Report   string `json:"report"`
	}
	descriptorBytes := taskRecoveryProofJSON(t, descriptor, &q)
	for _, path := range []string{q.State, q.Database, q.Config, q.Request, q.Report} {
		if !filepath.IsAbs(path) {
			t.Fatal("private recovery descriptor requires absolute paths")
		}
	}
	state, err := filepath.EvalSymlinks(q.State)
	if err != nil {
		t.Fatal(err)
	}
	reportParent, err := filepath.EvalSymlinks(filepath.Dir(q.Report))
	if err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(reportParent, filepath.Base(q.Report))
	if relative, err := filepath.Rel(state, reportPath); err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		t.Fatal("qualification report must be outside the preserved state")
	}
	var request runner.RecoverTasksRequest
	requestBytes := taskRecoveryProofJSON(t, q.Request, &request)
	if request.RunID != "run_PFI3NBEMY4SOLNU6QCHPWFP5C5" || !slices.Equal(request.TaskIDs, []string{"import"}) {
		t.Fatal("qualification request differs from the prepared failed import recovery")
	}
	loader, err := config.New(q.Config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("current runtime cannot load the preserved configuration", err)
	}
	checkpointSHA := taskRecoveryProofHash(t, q.Database)
	originalDB := filepath.Join(state, "process-core.sqlite")
	originalSHA := taskRecoveryProofHash(t, originalDB)
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(private, "process-core.sqlite")
	input, err := os.Open(q.Database)
	if err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(database, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		input.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	if err := errors.Join(copyErr, input.Close(), output.Close()); err != nil {
		t.Fatal("copy closed checkpoint", err)
	}
	// Only immutable artifacts are linked. No original jobs, native state,
	// credentials, ownership token or writable workspaces enter this proof.
	artifacts := map[string]os.FileInfo{}
	var artifactBytes int64
	if err := filepath.WalkDir(filepath.Join(state, "artifacts"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(state, path)
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
		if err := os.Link(path, target); err != nil {
			return err
		}
		artifacts[relative] = info
		artifactBytes += info.Size()
		return nil
	}); err != nil {
		t.Fatal("link immutable checkpoint artifacts", err)
	}
	store, err := sqlite.Open(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal("migrate private real-history copy", err)
	}
	var core *runner.Core
	t.Cleanup(func() {
		if core != nil {
			core.Close()
		}
		if store != nil {
			store.Close()
		}
	})
	before, err := store.Load(t.Context())
	if err != nil || len(before) != 100 {
		t.Fatal("expected the closed 100-run checkpoint", len(before), err)
	}
	beforeByID := map[string]runner.Run{}
	for _, r := range before {
		beforeByID[r.ID] = r
		if !r.Terminal() || r.Active() {
			t.Fatalf("checkpoint contains nonterminal execution: %s", r.ID)
		}
		for _, attempt := range r.Attempts {
			if attempt.State != runner.Finished || !attempt.Released {
				t.Fatalf("checkpoint contains unsettled historical execution: %s", r.ID)
			}
		}
	}
	owner := beforeByID[request.RunID]
	if owner.Status != "Failed" || owner.TaskGraph == nil || owner.TaskGraph.Lineage == nil || len(owner.TaskGraph.Children) != 4 || len(owner.Attempts) != 2 {
		t.Fatal("prepared owner does not retain the failed four-child generation and two lead visits")
	}
	family := map[string]bool{owner.ID: true}
	importID := ""
	for _, link := range owner.TaskGraph.Children {
		child := beforeByID[link.RunID]
		if link.Retired || child.TaskGraph == nil || child.TaskGraph.ParentRunID != owner.ID || child.Status != "Failed" || !child.ResourcesReleased {
			t.Fatal("prepared current child ownership/cleanup differs")
		}
		family[child.ID] = true
		if link.Spec.ID == "import" {
			importID = child.ID
			if len(child.Attempts) != 4 {
				t.Fatal("expected four retained failed import attempts")
			}
		} else if len(child.Attempts) != 0 {
			t.Fatal("dependency-failed closure contains executed work")
		}
	}
	if importID == "" {
		t.Fatal("prepared failed importer is absent")
	}
	origin := beforeByID[owner.TaskGraph.Lineage.OriginRunID]
	if origin.Status != "Failed" || origin.ContinuedBy == nil || origin.ContinuedBy.RunID != owner.ID {
		t.Fatal("legacy frozen origin/continuation link differs")
	}
	executor := &taskRecoveryProofExecutor{
		starts:   make(chan runner.Assignment, 8),
		observed: make(chan string, 8),
	}
	disk, err := runner.OpenDisk(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	core, err = runner.New(t.Context(), store, executor, disk, snapshot)
	if err != nil {
		t.Fatal("restore private actual history", err)
	}
	current, err := core.Get(owner.ID)
	if err != nil || current.Revision != request.Revision {
		t.Fatal("prepared exact owning revision differs", current.Revision, err)
	}
	taskRecoveryProofBudget(t, current, 42)
	if _, err := core.RecoverTasks(request); err != nil {
		t.Fatal("same-generation recovery rejected the prepared history", err)
	}
	var assignment runner.Assignment
	select {
	case assignment = <-executor.starts:
	case <-time.After(10 * time.Second):
		t.Fatal("private dispatch interception did not observe the recovered importer", core.Status())
	}
	if assignment.RunID != importID || assignment.TaskContext == nil || assignment.TaskContext.TaskID != "import" || assignment.TaskContext.Maintenance == nil || assignment.TaskContext.Maintenance.RequestID != request.RequestID || assignment.TaskContext.RemainingAttempts != 21 {
		t.Fatal("recovery did not dispatch the exact same importer with maintenance context and attempt43")
	}
	current, err = core.Get(owner.ID)
	if err != nil || current.Status != "Waiting" || current.TaskGraph.Phase != runner.TaskWaitingChildren {
		t.Fatal("owning goal did not wait for recovered children", err)
	}
	taskRecoveryProofBudget(t, current, 43)
	taskRecoveryProofAudit(t, current, request, family)
	replayed, err := core.RecoverTasks(request)
	if err != nil || replayed.ID != owner.ID {
		t.Fatal("idempotent replay created or refused the same recovered goal", err)
	}
	if _, err := core.Retry(runner.RetryRequest{RequestID: request.RequestID + "-proof-old-retry", RunID: origin.ID, WithinBudget: true, ExtraAttempts: 1}); err == nil {
		t.Fatal("legacy handed-off family can spend after recovery")
	}
	core.Close()
	core = nil
	after, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	taskRecoveryProofHistory(t, beforeByID, after, family, importID, assignment.AttemptID)
	receipt, err := store.FindReceipt(t.Context(), request.RequestID)
	if err != nil || receipt.Kind != "recover_tasks" || receipt.RunID != owner.ID || string(receipt.Args) != string(requestBytes) && !taskRecoveryProofEqualJSON(receipt.Args, requestBytes) {
		t.Fatal("atomic recovery receipt differs from prepared request", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	store, err = sqlite.Open(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal("reopen privately migrated recovered history", err)
	}
	core, err = runner.New(t.Context(), store, executor, disk, snapshot)
	if err != nil {
		t.Fatal("restart cannot authenticate retained recovery/lineage", err)
	}
	select {
	case attemptID := <-executor.observed:
		if attemptID != assignment.AttemptID {
			t.Fatal("restart observed another execution")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("restart did not reconcile the same intercepted execution")
	}
	replayed, err = core.RecoverTasks(request)
	if err != nil || replayed.ID != owner.ID || replayed.Status != "Waiting" || replayed.TaskGraph.Phase != runner.TaskWaitingChildren {
		t.Fatal("restart lost the same recovered goal or its automatic child wait", err)
	}
	taskRecoveryProofBudget(t, replayed, 43)
	taskRecoveryProofAudit(t, replayed, request, family)
	if _, err := core.Retry(runner.RetryRequest{RequestID: request.RequestID + "-proof-restarted-old-retry", RunID: origin.ID, WithinBudget: true, ExtraAttempts: 1}); err == nil {
		t.Fatal("restarted legacy handed-off family can spend")
	}
	core.Close()
	core = nil
	after, err = store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	taskRecoveryProofHistory(t, beforeByID, after, family, importID, assignment.AttemptID)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = nil
	if executor.count() != 1 || executor.stops != 0 || executor.releases != 0 {
		t.Fatal("qualification attempted extra dispatch, stop or cleanup outside the intercepted execution")
	}
	for relative, info := range artifacts {
		original, errOriginal := os.Lstat(filepath.Join(state, relative))
		linked, errPrivate := os.Lstat(filepath.Join(private, relative))
		if errOriginal != nil || errPrivate != nil || !os.SameFile(info, original) || !os.SameFile(original, linked) || info.Size() != original.Size() || info.Mode() != original.Mode() || !info.ModTime().Equal(original.ModTime()) {
			t.Fatalf("preserved immutable artifact changed or disappeared: %s", relative)
		}
	}
	if taskRecoveryProofHash(t, q.Database) != checkpointSHA || taskRecoveryProofHash(t, originalDB) != originalSHA {
		t.Fatal("qualification changed the original database or closed checkpoint")
	}
	header := make([]byte, 100)
	f, err := os.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	_, readErr := io.ReadFull(f, header)
	if err := errors.Join(readErr, f.Close()); err != nil || binary.BigEndian.Uint32(header[60:64]) != 9 {
		t.Fatal("private operational database did not migrate to schema9", err)
	}
	report := map[string]any{
		"status":                   "passed",
		"history_runs_loaded":      len(before),
		"schema":                   9,
		"descriptor_sha256":        taskRecoveryProofDigest(descriptorBytes),
		"checkpoint_sha256":        checkpointSHA,
		"original_database_sha256": originalSHA,
		"request_sha256":           taskRecoveryProofDigest(requestBytes),
		"request_id":               request.RequestID,
		"owner":                    owner.ID,
		"origin":                   origin.ID,
		"current_children":         len(owner.TaskGraph.Children),
		"recovered_import":         importID,
		"intercepted_attempt":      assignment.AttemptID,
		"budget_before": map[string]int{
			"prior_attempts": 36,
			"spent":          42,
			"limit":          64,
		},
		"budget_after": replayed.TaskBudget,
		"artifacts": map[string]any{
			"count":        len(artifacts),
			"bytes":        artifactBytes,
			"private_copy": "immutable hardlinks only; all original identities retained",
		},
		"history":        "all 100 run IDs, frozen definitions/inputs, old attempts and artifacts retained; only same failed generation reopened",
		"restart":        "same saved execution observed; owner waiting for children; audit/idempotent request and legacy freeze retained",
		"dispatch":       "exactly one intercepted Start, zero native/model/product/browser/materialization executions",
		"original_state": "database unchanged; no Service, jobs, native state, ownership token or credentials copied",
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil || len(encoded) > 16<<10 {
		t.Fatal("bounded qualification report", err)
	}
	if err := os.WriteFile(reportPath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("private 100-run recovery proof passed: same generation, 42→43/64, same execution after restart, no product dispatch")
}

func taskRecoveryProofJSON(t *testing.T, path string, target any) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<10+1))
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, target) != nil {
		t.Fatal("bounded private qualification JSON is invalid", err)
	}
	return data
}

func taskRecoveryProofHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func taskRecoveryProofDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func taskRecoveryProofEqualJSON(a, b []byte) bool {
	var left, right runner.RecoverTasksRequest
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

func taskRecoveryProofBudget(t *testing.T, r runner.Run, spent int) {
	t.Helper()
	want := runner.TaskAttemptBudget{PriorAttempts: 36, Spent: spent, Remaining: 64 - spent, Limit: 64}
	if r.TaskBudget == nil || *r.TaskBudget != want {
		t.Fatalf("frozen aggregate attempt accounting differs: got %+v want %+v", r.TaskBudget, want)
	}
}

func taskRecoveryProofAudit(t *testing.T, r runner.Run, request runner.RecoverTasksRequest, family map[string]bool) {
	t.Helper()
	if len(r.TaskGraph.Recoveries) != 1 {
		t.Fatal("recovery audit was lost or duplicated")
	}
	audit := r.TaskGraph.Recoveries[0]
	if audit.RequestID != request.RequestID || audit.OwnerID != request.RunID || audit.Reason != request.Reason || audit.Spent != 42 || !slices.Equal(audit.TaskIDs, request.TaskIDs) || len(audit.Rearmed) != len(family) || audit.GrantedAt.IsZero() {
		t.Fatal("recovery audit differs from the exact unchanged failed generation")
	}
	seen := map[string]bool{}
	for _, id := range audit.Rearmed {
		if !family[id] || seen[id] {
			t.Fatal("audit contains a different or duplicate recovered run")
		}
		seen[id] = true
	}
}

func taskRecoveryProofHistory(t *testing.T, before map[string]runner.Run, after []runner.Run, family map[string]bool, importer, attemptID string) {
	t.Helper()
	if len(after) != len(before) {
		t.Fatal("recovery allocated or removed historical runs")
	}
	for _, r := range after {
		old, exists := before[r.ID]
		if !exists {
			t.Fatal("recovery allocated another task identity")
		}
		if !family[r.ID] {
			if !reflect.DeepEqual(old, r) {
				t.Fatalf("recovery changed unrelated/original history: %s", r.ID)
			}
			continue
		}
		if !reflect.DeepEqual(r.Process, old.Process) || !reflect.DeepEqual(r.Roles, old.Roles) || !reflect.DeepEqual(r.Profiles, old.Profiles) || !reflect.DeepEqual(r.Inputs, old.Inputs) || !reflect.DeepEqual(r.Source, old.Source) || r.Repository != old.Repository || r.RecordFormat != old.RecordFormat || !reflect.DeepEqual(r.TaskGraph.Children, old.TaskGraph.Children) || !reflect.DeepEqual(r.TaskGraph.Lineage, old.TaskGraph.Lineage) || r.TaskGraph.Revisions != old.TaskGraph.Revisions || !slices.Equal(r.TaskGraph.WritePaths, old.TaskGraph.WritePaths) || !slices.Equal(r.TaskGraph.DependsOn, old.TaskGraph.DependsOn) {
			t.Fatalf("recovery changed frozen policy, task specs, inputs or source authority: %s", r.ID)
		}
		wantAttempts := len(old.Attempts)
		if r.ID == importer {
			wantAttempts++
		}
		if len(r.Attempts) != wantAttempts || !reflect.DeepEqual(r.Attempts[:len(old.Attempts)], old.Attempts) {
			t.Fatalf("recovery changed historical attempts or repeated work: %s", r.ID)
		}
		if r.ID == importer && (r.Attempts[len(old.Attempts)].ID != attemptID || r.Visit != old.Visit+1) {
			t.Fatal("import recovery did not retain the same new execution/visit")
		}
	}
}

type taskRecoveryProofExecutor struct {
	mu       sync.Mutex
	started  int
	stops    int
	releases int
	starts   chan runner.Assignment
	observed chan string
}

func (e *taskRecoveryProofExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	e.started++
	e.mu.Unlock()
	select {
	case e.starts <- a:
	default:
	}
	return e.pending(), nil
}

func (e *taskRecoveryProofExecutor) Observe(_ context.Context, id string) (runner.Observation, error) {
	select {
	case e.observed <- id:
	default:
	}
	return e.pending(), nil
}

func (e *taskRecoveryProofExecutor) Stop(context.Context, string) error {
	e.mu.Lock()
	e.stops++
	e.mu.Unlock()
	return nil
}

func (e *taskRecoveryProofExecutor) Release(context.Context, string) error {
	e.mu.Lock()
	e.releases++
	e.mu.Unlock()
	return nil
}

func (e *taskRecoveryProofExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

func (e *taskRecoveryProofExecutor) pending() runner.Observation {
	return runner.Observation{State: runner.Running, Reason: "private qualification intercepts all execution", NextCheck: time.Now().Add(time.Hour)}
}
