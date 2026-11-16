package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func canonicalReport(id string) string {
	prefix := "canonical-report-" + id + ":"
	return prefix + strings.Repeat("x", (12<<10)-len(prefix))
}

func TestGeneralTasksLargeChildReportsRemainCanonical(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.Storage.Database.MaxRecord = 32 << 10
	s.Instance.Storage.Database.MaxSize = 8 << 20
	s.Instance.Storage.MaxManaged = 32 << 20
	p := s.Projects["demo"]
	planner := p.Roles["planner"]
	planner.Execution.MaxInput = 128 << 10
	p.Roles["planner"] = planner
	s.Projects["demo"] = p
	var contextBytes int
	var contextError string
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			if a.TaskContext.TaskID != "root" {
				return graphFinal(canonicalReport(a.TaskContext.TaskID))
			}
			if len(a.TaskContext.Results) == 0 {
				tasks := []runner.RoleTask{}
				for i := range 4 {
					tasks = append(tasks, graphTask(fmt.Sprint(i), "writer"))
				}
				return graphAction("delegate", tasks...)
			}
			encoded, _ := json.Marshal(a.TaskContext)
			contextBytes = len(encoded)
			if len(a.TaskContext.Results) != 4 || contextBytes <= int(a.Limits.MaxRecord) || contextBytes > int(a.Setup.Profile.MaxInput) {
				contextError = "four complete reports did not fit the configured context independently of the database record limit"
			}
			for _, result := range a.TaskContext.Results {
				var report string
				if json.Unmarshal(result.Outputs["result"].Inline, &report) != nil || report != canonicalReport(result.ID) || result.Status != "Completed" {
					contextError = "parent context did not materialize a complete canonical accepted child report"
				}
			}
			return graphFinal("accepted four full reports")
		}}
	})
	defer r.close()
	root := begin(t, r.core, "large-child-reports", "demo")
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || len(done.Attempts) != 2 || contextError != "" || contextBytes <= int(s.Instance.Storage.Database.MaxRecord) {
		t.Fatalf("large reports prevented parent resumption: %s, %s, context=%d", done.Reason, contextError, contextBytes)
	}
	loaded, err := r.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range loaded {
		if saved.ID != root.ID {
			continue
		}
		state, _, err := runner.MarshalRunRecord(saved)
		if err != nil || len(state) >= int(s.Instance.Storage.Database.MaxRecord)/2 || len(saved.TaskGraph.Results) != 4 {
			t.Fatalf("parent did not retain bounded identity references: %v size=%d", err, len(state))
		}
		for _, ref := range saved.TaskGraph.Results {
			if ref.RunID == "" || strings.Contains(string(state), canonicalReport(ref.ID)[:64]) {
				t.Fatal("parent persisted another copy of its child's canonical report")
			}
		}
		refs, _ := json.Marshal(saved.TaskGraph.Results)
		if strings.Contains(string(refs), "outputs") || strings.Contains(string(refs), "status") || strings.Contains(string(refs), "role") {
			t.Fatal("retained result reference duplicated mutable result facts", string(refs))
		}
	}
}

func TestGeneralTaskOldDevelopmentResultSnapshotsDecodeAsReferences(t *testing.T) {
	r := runner.Run{
		ID: "root", RecordFormat: 9, Process: runner.Process{Tasks: &runner.TaskPolicy{}},
		TaskGraph: &runner.TaskGraphState{RootRunID: "root", TaskID: "root", Results: []runner.TaskResultRef{{ID: "child", RunID: "child-run"}}},
	}
	state, definition, err := runner.MarshalRunRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	var graph map[string]json.RawMessage
	if json.Unmarshal(state, &raw) != nil || json.Unmarshal(raw["task_graph"], &graph) != nil {
		t.Fatal("could not prepare development-format fixture")
	}
	graph["results"], _ = json.Marshal([]runner.RoleTaskResult{{ID: "child", RunID: "child-run", Role: "writer", Status: "Completed", Outputs: map[string]runner.Value{"result": runner.Text("obsolete copied child report")}}})
	raw["task_graph"], _ = json.Marshal(graph)
	oldState, _ := json.Marshal(raw)
	restored, err := runner.UnmarshalRunRecord(oldState, definition)
	if err != nil || len(restored.TaskGraph.Results) != 1 || restored.TaskGraph.Results[0] != (runner.TaskResultRef{ID: "child", RunID: "child-run"}) {
		t.Fatal("old development format lost its child identity", err)
	}
	canonical, _, err := runner.MarshalRunRecord(restored)
	if err != nil || strings.Contains(string(canonical), "obsolete copied child report") {
		t.Fatal("old copied report survived normalization to canonical child references", err)
	}
}

func TestGeneralTaskRepairOwnerReceivesSettledFailureDiagnostic(t *testing.T) {
	s := graphSnapshot(t)
	diagnostic := "stage=browser; last observed command failed"
	var ownerFailure string
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			if a.TaskContext.TaskID != "root" {
				return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "stopped", Reason: "configured stop", Diagnostic: diagnostic}}
			}
			if len(a.TaskContext.Results) == 0 {
				return graphAction("delegate", graphTask("check", "writer"))
			}
			result := a.TaskContext.Results[0]
			if result.Status != "Failed" || result.Diagnostic != diagnostic || len(result.Outputs) != 0 {
				ownerFailure = "failure diagnostic was lost or became accepted product output"
			}
			return graphFinal("failure evidence received")
		}}
	})
	defer r.close()
	root := begin(t, r.core, "failure-diagnostic", "demo")
	done := await(t, r.core, root.ID, released)
	if ownerFailure != "" || done.Status != "Completed" || len(done.TaskStatuses) != 1 || done.TaskStatuses[0].Diagnostic != diagnostic || done.TaskStatuses[0].Status != "Failed" {
		t.Fatal("repair/status projection lost canonical failure facts", ownerFailure, done.TaskStatuses)
	}
}

type rejectTerminalDiagnosticStore struct {
	runner.Store
	rejected atomic.Bool
}

func (s *rejectTerminalDiagnosticStore) Save(ctx context.Context, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	if r.TaskGraph != nil && r.TaskGraph.TaskID == "check" && r.Terminal() && s.rejected.CompareAndSwap(false, true) {
		return errors.New("fixture transient terminal write refusal")
	}
	return s.Store.Save(ctx, r, artifacts, receipt)
}

func TestGeneralTaskFailedTerminalSaveRetainsDiagnosticThroughCleanupAndReopen(t *testing.T) {
	s := graphSnapshot(t)
	dir := t.TempDir()
	r := openRig(t, dir, s, nil)
	r.core.Close()
	store := &rejectTerminalDiagnosticStore{Store: r.store}
	diagnostic := "stage=browser; output observed before terminal save"
	var received bool
	e := graphExecutor{r.backend, func(a runner.Assignment) runner.Observation {
		if a.TaskContext.TaskID != "root" {
			return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "stopped", Reason: "deadline", Diagnostic: diagnostic}}
		}
		if len(a.TaskContext.Results) == 0 {
			return graphAction("delegate", graphTask("check", "writer"))
		}
		result := a.TaskContext.Results[0]
		received = result.Status == "Failed" && result.Diagnostic == diagnostic && strings.Contains(result.Reason, "result persistence rejected") && len(result.Outputs) == 0
		return graphFinal("failure evidence received")
	}}
	core, err := runner.New(t.Context(), store, e, r.disk, s)
	if err != nil {
		r.close()
		t.Fatal(err)
	}
	r.core = core
	defer r.close()
	root := begin(t, core, "diagnostic-save-refusal", "demo")
	done := await(t, core, root.ID, released)
	if !store.rejected.Load() || !received || done.Status != "Completed" || len(done.TaskStatuses) != 1 || done.TaskStatuses[0].Attempts != 1 || done.TaskStatuses[0].CleanupPending {
		t.Fatal("terminal fallback lost facts, replaced work, or failed cleanup", done)
	}
	core.Close()
	if err := r.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	rows, err := reopened.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range rows {
		if child.TaskGraph.TaskID != "check" {
			continue
		}
		if len(child.Attempts) != 1 || !child.Attempts[0].Released || child.Attempts[0].End.Diagnostic != diagnostic || child.Attempts[0].End.Kind != "failed" || len(child.Outputs) != 0 {
			t.Fatal("fallback/release/reopen changed canonical failure evidence", child)
		}
		return
	}
	t.Fatal("same failed child was not retained")
}
