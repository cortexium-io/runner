package sudokucorrections_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Real Core + private SQLite, with every execution intercepted. No native CLI,
// product command, source writer, browser or live state is used by this journey.
func TestWorkflowArtifactOwnerGoalResearchRepair(t *testing.T) {
	snapshot := generatedCorrectionSnapshot(t)
	project := snapshot.Projects["sudoku-preview"]
	document, err := os.ReadFile(filepath.Join(filepath.Dir(project.Workspace), "goal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if file := os.Getenv("RUNNER_SUDOKU_OWNER_GOAL"); file != "" {
		document, err = os.ReadFile(file)
		if err != nil || len(document) != 17294 || fmt.Sprintf("%x", sha256.Sum256(document)) != "5d9a1e19d68c08b532aec0ce066e28c8592325364a779e0ffba2b41f0fdeaa6a" {
			t.Fatal("original owner document bytes/digest differ", err)
		}
	} else {
		document = append(document, bytes.Repeat([]byte(" "), 17294-len(document))...)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(document))
	pointer := fmt.Sprintf(`{"kind":"owner-goal-reference","sha256":%q,"bytes":%d,"purpose":"Read the unchanged owner_goal artifact; metadata fixture only"}`, digest, len(document))
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	disk, err := runner.OpenDisk(root, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sqlite.Open(root, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	store := &goalCapacityStore{Store: database, limit: int(snapshot.Instance.Storage.Database.MaxRecord)}
	defer func() { _ = store.Close() }()
	seed := runner.Run{
		ID: "run_fixture_inputs", ProjectID: project.ID, RecordFormat: 3,
		Process: project.Processes["legacy"], Roles: map[string]runner.Role{"legacy": project.Roles["legacy"]},
		Inputs: map[string]runner.Value{"request": runner.Text("model-free fixture inputs")},
		Status: "Completed", ResourcesReleased: true, Revision: 1,
	}
	inputs := map[string]runner.Value{"request": runner.Text(pointer)}
	var artifacts []runner.Artifact
	for name, id := range map[string]string{
		"patch": "attempt_patch/changes.patch", "checks": "attempt_frontend/result",
		"prior": "attempt_accepted_check/result", "handoff": "attempt_handoff/result",
	} {
		data := []byte("metadata fixture only: " + name)
		if err := disk.Write("artifacts/"+id, data, disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		inputs[name] = runner.Value{ArtifactID: id}
		artifacts = append(artifacts, runner.Artifact{ID: id, RunID: seed.ID, ProjectID: seed.ProjectID, Bytes: int64(len(data))})
	}
	if err := store.Save(t.Context(), seed, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	executor := &goalFixtureExecutor{disk: disk, document: document, pointer: pointer, calls: map[string]int{}}
	core, err := runner.New(t.Context(), store, executor, disk, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	staged, err := core.Start(runner.StartRequest{RequestID: "stage-original-owner-document", Project: project.ID,
		Process: "stage-sudoku-goal", Inputs: map[string]runner.Value{"request": runner.Text(string(document))}})
	if err != nil {
		t.Fatal(err)
	}
	staged = awaitGoalFixture(t, core, staged.ID)
	if staged.Status != "Completed" || len(staged.Attempts) != 1 || len(staged.TaskGraph.Children) != 0 {
		t.Fatalf("one-command stager did not finish exactly once: %s %s", staged.Status, staged.Reason)
	}
	inputs["owner_goal"] = staged.Outputs["goal"]
	request := runner.StartRequest{RequestID: "artifact-owner-goal-repair-journey", Project: project.ID, Process: "correct-sudoku-preview", Inputs: inputs}
	started, err := core.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	done := awaitGoalFixture(t, core, started.ID)
	if done.Status != "Completed" || len(done.TaskGraph.Children) != 10 || len(done.Attempts) != 6 {
		t.Fatalf("planned+repair metadata journey failed: %s %s children=%d attempts=%d", done.Status, done.Reason, len(done.TaskGraph.Children), len(done.Attempts))
	}
	replayed, err := core.Start(request)
	if err != nil || replayed.ID != done.ID || len(replayed.Attempts) != len(done.Attempts) {
		t.Fatal("idempotent main request replay changed execution", err)
	}
	core.Close()
	executor.mu.Lock()
	if executor.failure != "" || executor.calls["sharing-research"] != 4 || executor.calls["sharing-research-2"] != 1 || executor.calls["baseline"] != 1 {
		t.Fatalf("fixture ownership/repair: %s calls=%v", executor.failure, executor.calls)
	}
	executor.mu.Unlock()
	store.mu.Lock()
	if !store.planned || !store.repaired || store.maxMain > store.limit/4 {
		t.Fatalf("main planned/repair state lacks headroom: plan=%v repair=%v peak=%d half=%d", store.planned, store.repaired, store.maxMain, store.limit/2)
	}
	t.Logf("preserved %dB owner document %s; actual four-task plan + failed-research redelegation peak state=%dB/%dB half-record; definition=%dB/%dB", len(document), digest, store.maxMain, store.limit/2, store.maxDefinition, store.limit)
	store.mu.Unlock()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(root, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range saved {
		if r.ID == done.ID {
			found = len(r.TaskGraph.Children) == 10 && len(r.Attempts) == 6 && r.Inputs["owner_goal"].ArtifactID == staged.Outputs["goal"].ArtifactID
		}
	}
	owner, err := disk.Read("artifacts/"+staged.Outputs["goal"].ArtifactID, disk.Policy.MaxArtifact)
	if !found || err != nil || !bytes.Equal(owner, document) {
		t.Fatal("reopen lost planned/repaired history or exact canonical owner document", err)
	}
}

func awaitGoalFixture(t *testing.T, core *runner.Core, id string) runner.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for {
		r, err := core.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Terminal() && r.ResourcesReleased {
			return r
		}
		if _, err := core.Wait(ctx, id, r.Revision); err != nil {
			t.Fatal("event-driven metadata fixture wait", err)
		}
	}
}

type goalCapacityStore struct {
	*sqlite.Store
	mu            sync.Mutex
	limit         int
	maxMain       int
	maxDefinition int
	planned       bool
	repaired      bool
}

func (s *goalCapacityStore) Save(ctx context.Context, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	state, definition, err := runner.MarshalRunRecord(r)
	if err != nil || len(state) > s.limit/2 || len(definition) > s.limit {
		return fmt.Errorf("fixture record capacity state=%d definition=%d: %v", len(state), len(definition), err)
	}
	s.mu.Lock()
	if r.Process.ID == "correct-sudoku-preview" && r.TaskGraph.ParentRunID == "" {
		s.maxMain = max(s.maxMain, len(state))
		s.maxDefinition = max(s.maxDefinition, len(definition))
		s.planned = s.planned || len(r.TaskGraph.Children) == 4
		s.repaired = s.repaired || len(r.TaskGraph.Children) == 7
	}
	s.mu.Unlock()
	return s.Store.Save(ctx, r, artifacts, receipt)
}

type goalFixtureExecutor struct {
	mu       sync.Mutex
	disk     *runner.Disk
	document []byte
	pointer  string
	ownerID  string
	calls    map[string]int
	failure  string
}

func (e *goalFixtureExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	finish := func(result runner.StepResult) (runner.Observation, error) {
		data, _ := json.Marshal(result)
		if a.Setup.Profile.Harness == "native" && len(data) > int(a.Setup.Profile.MaxOutput) {
			e.failure = "fixture response exceeded unchanged native output envelope"
		}
		return runner.Observation{State: runner.Finished, NativeHandle: a.AttemptID, End: &runner.ExecutionEnd{Kind: "returned", Result: &result}}, nil
	}
	artifact := func(name string, data []byte) runner.Value {
		id := a.AttemptID + "/" + name
		if err := e.disk.Write("artifacts/"+id, data, e.disk.Policy.MaxArtifact); err != nil {
			e.failure = err.Error()
		}
		return runner.Value{ArtifactID: id}
	}
	if a.Setup.Profile.Harness == "command" && a.Setup.Profile.Command.Artifact == "owner-goal.json" {
		if len(a.Inputs) != 0 {
			e.failure = "stager duplicated owner bytes in unused role inputs"
		}
		if a.TaskContext.Goal != string(e.document) {
			e.failure = "stager did not receive original owner bytes once"
		}
		owner := artifact("owner-goal.json", e.document)
		e.ownerID = owner.ArtifactID
		return finish(runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"check": runner.Text("owner-document-only; metadata fixture"), "artifact": owner}})
	}
	if a.Inputs["owner_goal"].ArtifactID != e.ownerID || a.TaskContext.Goal != e.pointer {
		e.failure = "task lost unchanged owner artifact or duplicated full document inline"
	}
	id := a.TaskContext.TaskID
	e.calls[id]++
	owner := map[string]runner.Value{"owner_goal": {ArtifactID: e.ownerID}}
	task := func(id, role string, deps ...string) runner.RoleTask {
		spec := runner.RoleTask{ID: id, Role: role, Brief: "Complete the assigned metadata fixture task using the canonical owner document.", Inputs: runner.Clone(owner), DependsOn: deps,
			Acceptance: []string{"Preserve exact owner document identity", "Return declared compact summary and canonical evidence"}}
		if strings.HasPrefix(id, "developer") {
			spec.WritePaths = []string{"frontend/src", "docs/sudoku-interface.md"}
		}
		if strings.HasPrefix(id, "test-author") {
			spec.WritePaths = []string{"frontend/verification/sudoku-corrections.mjs", "frontend/verification/sudoku-corrections"}
		}
		return spec
	}
	delegate := func(tasks ...runner.RoleTask) (runner.Observation, error) {
		return finish(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "delegate", Tasks: tasks}})
	}
	if id == "root" {
		switch e.calls[id] {
		case 1:
			baseline := task("baseline", "sudoku-correction-baseline")
			baseline.Inputs["prior"], baseline.Inputs["handoff"] = a.Inputs["prior"], a.Inputs["handoff"]
			return delegate(baseline)
		case 2:
			return delegate(task("sharing-research", "sudoku-correction-research", "baseline"),
				task("developer", "sudoku-correction-developer", "baseline", "sharing-research"),
				task("test-author", "sudoku-correction-test-author", "baseline", "sharing-research"))
		case 3:
			return delegate(task("sharing-research-2", "sudoku-correction-research", "baseline"),
				task("developer-2", "sudoku-correction-developer", "baseline", "sharing-research-2"),
				task("test-author-2", "sudoku-correction-test-author", "baseline", "sharing-research-2"))
		case 4:
			check := task("check", "sudoku-correction-check", "baseline", "developer-2", "test-author-2")
			check.Inputs["prior"], check.Inputs["checks"] = a.Inputs["prior"], a.Inputs["checks"]
			return delegate(check)
		case 5:
			return delegate(task("source-review", "sudoku-correction-source-review", "baseline", "check"), task("ux-review", "sudoku-correction-ux-review", "baseline", "check"))
		default:
			return finish(runner.StepResult{Outcome: "accepted", Outputs: map[string]runner.Value{"report": runner.Text("Configuration/record repair fixture completed; no product acceptance claimed.")}})
		}
	}
	if id == "sharing-research" {
		return runner.Observation{State: runner.Finished, NativeHandle: a.AttemptID, End: &runner.ExecutionEnd{Kind: "failed", Reason: "fixture: research final response exceeded existing8192-byte bound"}}, nil
	}
	outputs := map[string]runner.Value{}
	for name, kind := range a.OutputTypes {
		if kind == "artifact" {
			detail, _ := json.Marshal(map[string]any{"fixture_only": true, "owner_goal_sha256": fmt.Sprintf("%x", sha256.Sum256(e.document)), "detail": strings.Repeat("full citations/requirements belong in the artifact; ", 500)})
			outputs[name] = artifact(name+".json", detail)
		} else {
			outputs[name] = runner.Text("Metadata fixture summary; detailed evidence remains in the declared artifact. No product checks executed.")
		}
	}
	return finish(runner.StepResult{Outcome: "ready", Outputs: outputs})
}

func (*goalFixtureExecutor) Observe(context.Context, string) (runner.Observation, error) {
	return runner.Observation{}, errors.New("unexpected observation; fixture executions finish synchronously")
}
func (*goalFixtureExecutor) Stop(context.Context, string) error    { return nil }
func (*goalFixtureExecutor) Release(context.Context, string) error { return nil }
