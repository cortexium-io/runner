package sqlite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestTaskSpecificationsRestoreFromRetainedAttemptsBeforeChildrenExist(t *testing.T) {
	r := normalizedRun(4)
	r.RecordFormat = 9
	r.TaskGraph = &runner.TaskGraphState{
		RootRunID: r.ID,
		TaskID:    "root",
		Brief:     "Retain the original goal",
	}
	for i := range r.Attempts {
		a := &r.Attempts[i]
		tasks := make([]runner.RoleTask, 0, 8)
		for j := range 8 {
			spec := runner.RoleTask{
				ID:         fmt.Sprintf("task-%d-%d", i, j),
				Role:       "owner",
				Brief:      strings.Repeat("Admitted specification retained once. ", 40),
				Acceptance: []string{"Preserve evidence and verify the requested behavior"},
			}
			tasks = append(tasks, spec)
			r.TaskGraph.Children = append(r.TaskGraph.Children, runner.RoleTaskLink{
				Spec:  spec,
				RunID: fmt.Sprintf("reserved-child-%d-%d", i, j),
			})
			// Result identity codec coverage; Core separately owns terminal-child
			// admission. The canonical task ID is already retained in this link.
			r.TaskGraph.Results = append(r.TaskGraph.Results, runner.TaskResultRef{
				ID:    spec.ID,
				RunID: fmt.Sprintf("reserved-child-%d-%d", i, j),
			})
		}
		a.End.Result = &runner.StepResult{
			Outcome:    "ready",
			TaskAction: &runner.TaskAction{Kind: "delegate", Tasks: tasks},
		}
	}
	root := filepath.Join(t.TempDir(), "state")
	s, err := Open(root, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Save(t.Context(), r, nil, nil); err != nil {
		t.Fatal("canonical attempt references should fit unchanged storage bounds", err)
	}
	var data []byte
	if err = s.db.QueryRow("SELECT data FROM runs WHERE id=?", r.ID).Scan(&data); err != nil {
		t.Fatal(err)
	}
	if len(data) >= int(normalizedPolicy().Database.MaxRecord)/2 || strings.Contains(string(data), "Admitted specification retained once") {
		t.Fatal("parent operational state still duplicates the full admitted specifications", len(data))
	}
	var encoded struct {
		Graph struct {
			Results []map[string]any `json:"results"`
		} `json:"task_graph"`
	}
	if json.Unmarshal(data, &encoded) != nil || len(encoded.Graph.Results) != len(r.TaskGraph.Results) {
		t.Fatal("stored result references lost")
	}
	if _, exists := encoded.Graph.Results[0]["id"]; exists {
		t.Fatal("disk still duplicates the canonical child task ID")
	}
	history := allowanceAttemptBytes(t, s)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) || history != allowanceAttemptBytes(t, s) {
		t.Fatal("restart changed an admitted specification or its immutable parent attempts", err)
	}
	broken := runner.Clone(r.Attempts[0])
	broken.End.Kind = "failed"
	bad, err := json.Marshal(broken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec("UPDATE attempts SET data=? WHERE run_id=? AND position=0", bad, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(t.Context()); err == nil {
		t.Fatal("rejected proposal silently replaced an admitted canonical task")
	}
}
