package runner

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestRunRecordDerivesOnlyCanonicalRootGoal(t *testing.T) {
	goal := "The complete agreed goal remains available after every reload."
	for _, tc := range []struct {
		name, brief, parent, root string
		format                    int
		graph                     bool
		stored                    string
	}{
		{name: "derived root", brief: goal, root: "root", format: 9, graph: true},
		{name: "explicit root brief", brief: "A separate explicit brief", root: "root", format: 9, graph: true, stored: "A separate explicit brief"},
		{name: "child repeats goal", brief: goal, parent: "parent", root: "parent", format: 9, graph: true, stored: goal},
		{name: "legacy", format: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run{ID: "root", RecordFormat: tc.format, Inputs: map[string]Value{"request": Text(goal)}}
			if tc.graph {
				r.TaskGraph = &TaskGraphState{RootRunID: tc.root, ParentRunID: tc.parent, TaskID: "root", Brief: tc.brief}
				r.Process.Tasks = &TaskPolicy{}
			}
			before, graph := Clone(r), r.TaskGraph
			state, definition, err := MarshalRunRecord(r)
			if err != nil {
				t.Fatal(err)
			}
			if r.TaskGraph != graph || !reflect.DeepEqual(r, before) {
				t.Fatal("record serialization mutated its caller or graph alias")
			}
			var stored Run
			if err := json.Unmarshal(state, &stored); err != nil {
				t.Fatal(err)
			}
			if tc.graph && stored.TaskGraph.Brief != tc.stored || !tc.graph && stored.TaskGraph != nil {
				t.Fatalf("unexpected stored brief: %+v", stored.TaskGraph)
			}
			got, err := UnmarshalRunRecord(state, definition)
			if err != nil || !reflect.DeepEqual(got, before) {
				t.Fatal("public run changed after record round trip", err)
			}
			if tc.name == "derived root" {
				// Earlier format-nine records stored the same goal twice. Reading
				// them is lossless; only their next ordinary save normalizes it.
				old := recordWithRootBrief(t, state, goal)
				got, err = UnmarshalRunRecord(old, definition)
				if err != nil || !reflect.DeepEqual(got, before) {
					t.Fatal("older inline root goal changed on load", err)
				}
			}
		})
	}
}

func recordWithRootBrief(t *testing.T, state []byte, brief string) []byte {
	t.Helper()
	var raw, graph map[string]json.RawMessage
	if json.Unmarshal(state, &raw) != nil || json.Unmarshal(raw["task_graph"], &graph) != nil {
		t.Fatal("invalid graph record fixture")
	}
	graph["brief"], _ = json.Marshal(brief)
	raw["task_graph"], _ = json.Marshal(graph)
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGeneralTaskGoalRichFourTaskAdmissionUsesCanonicalGoal(t *testing.T) {
	p := DefaultStorage()
	p.Database.MaxRuns = 1000
	p.MinFree, p.Database.MaxRecord = 0, 40<<10
	disk, err := OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	goal := strings.Repeat("g", 5744)
	root := Run{
		ID: "root", ProjectID: "demo", RecordFormat: 9, Repository: t.TempDir(), Status: "Running",
		Inputs: map[string]Value{"request": Text(goal)},
		Process: Process{
			ID: "general", MaxAttempts: 64,
			Steps: []Step{{ID: "work", Role: "worker"}},
			Tasks: &TaskPolicy{Roles: []string{"worker"}, MaxTasks: 20, MaxDepth: 2, MaxConcurrent: 2},
		},
		Roles: map[string]Role{"worker": {
			ID: "worker", Purpose: "Complete the assigned scoped work", Inputs: map[string]string{},
			Outputs: map[string]string{"report": "text"}, Execution: &Profile{Harness: "deterministic"},
		}},
		TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root", Brief: goal},
	}
	c := &Core{disk: disk, runs: map[string]Run{root.ID: root}}
	var proposed []RoleTask
	for i := range 12 {
		spec := RoleTask{
			ID: fmt.Sprintf("task-%02d", i), Role: "worker", Brief: strings.Repeat("b", 400),
			Acceptance: []string{strings.Repeat("a", 125), strings.Repeat("c", 125)},
		}
		if i >= 8 {
			proposed = append(proposed, spec)
			continue
		}
		id := fmt.Sprintf("run_%026d", i)
		root.TaskGraph.Children = append(root.TaskGraph.Children, RoleTaskLink{Spec: spec, RunID: id})
		c.runs[id] = Run{
			ID: id, ProjectID: root.ProjectID, Repository: root.Repository, Status: "Completed",
			TaskGraph: &TaskGraphState{RootRunID: root.ID, ParentRunID: root.ID, TaskID: spec.ID, Role: spec.Role},
		}
	}
	c.runs[root.ID] = Clone(root)
	if err := c.graphAction(&root, &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: proposed}}); err != nil {
		t.Fatal("ordinary four-task frontier was rejected despite available state headroom", err)
	}
	state, definition, err := MarshalRunRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	old := recordWithRootBrief(t, state, goal)
	if len(old) <= int(p.Database.MaxRecord)/2 || len(state) > int(p.Database.MaxRecord)/2 || root.Process.MaxAttempts != 64 || root.Process.Tasks.MaxTasks != 20 {
		t.Fatalf("fixture failed to reproduce redundant-goal admission: old=%d canonical=%d limit=%d", len(old), len(state), p.Database.MaxRecord/2)
	}
	restored, err := UnmarshalRunRecord(state, definition)
	if err != nil || !reflect.DeepEqual(restored, root) || graphGoal(restored.Inputs) != goal {
		t.Fatal("admitted graph lost public goal or ownership on reload", err)
	}
	c.runs[root.ID] = restored
	ctx, err := c.graphContext(restored)
	if err != nil || ctx.Goal != goal || ctx.Brief != goal {
		t.Fatal("reconstituted root did not supply the complete task context", err)
	}
	t.Logf("four-task frontier: duplicated=%d canonical=%d half-record limit=%d", len(old), len(state), p.Database.MaxRecord/2)
}
