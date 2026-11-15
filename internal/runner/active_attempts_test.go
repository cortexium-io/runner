package runner

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestActiveAttemptBudgetCountsPendingAndUnknownOnce(t *testing.T) {
	c := &Core{
		snapshot: Snapshot{Instance: Instance{Concurrency: 32, MaxActiveAttempts: 3}},
		runs: map[string]Run{"owned": {
			ID: "owned",
			Attempts: []Attempt{
				{ID: "running", State: Running},
				{ID: "unknown", State: Unknown},
				{ID: "reserved", State: NotStarted},
				{ID: "finished", State: Finished},
			},
		}},
	}
	if active := c.activeAttempts(); active != 3 || c.taskDispatchAllowed(Run{ID: "candidate"}, active) {
		t.Fatal("unknown or reserved dispatch stopped owning capacity", active)
	}
	c.snapshot.Instance.MaxActiveAttempts = 4
	if !c.taskDispatchAllowed(Run{ID: "candidate"}, c.activeAttempts()) {
		t.Fatal("available global capacity unnecessarily blocked a legacy dispatch")
	}
	c.snapshot.Instance.MaxActiveAttempts = 0
	if !c.taskDispatchAllowed(Run{ID: "candidate"}, c.activeAttempts()) {
		t.Fatal("legacy zero setting introduced a new global limit")
	}
}

func TestNativeRequestReserveUsesInputBoundBeyondDatabaseRecord(t *testing.T) {
	p := DefaultStorage()
	p.Database.MaxRecord = 32 << 10
	disk, err := OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	role := Role{ID: "native", Execution: &Profile{Harness: "native", MaxInput: 128 << 10}}
	c := &Core{disk: disk, runs: map[string]Run{"run": {
		Roles: map[string]Role{role.ID: role}, Process: Process{Steps: []Step{{Role: role.ID}}},
		Attempts: []Attempt{{State: Running}, {State: NotStarted}, {State: Unknown}, {State: Finished}},
	}}}
	if got, want := c.pendingNativeRequestBytes(), int64(3*(128+32)<<10); got != want {
		t.Fatalf("canonical native request reserve was coupled to smaller database records: got %d want %d", got, want)
	}
}

func TestNativeRequestReserveIncludesCompleteTaskResults(t *testing.T) {
	p := DefaultStorage()
	p.Database.MaxRecord = 32 << 10
	disk, err := OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	role := Role{ID: "native", Execution: &Profile{Harness: "native", MaxInput: 128 << 10}}
	root := Run{
		ID:        "root",
		ProjectID: "demo",
		Roles:     map[string]Role{role.ID: role},
		Process: Process{
			Tasks:       &TaskPolicy{MaxTasks: 10},
			MaxAttempts: 24,
			Steps:       []Step{{Role: role.ID}},
		},
		Attempts:  []Attempt{{State: Running}},
		TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root"},
	}
	children := map[string]Run{}
	results := []RoleTaskResult{}
	for i := range 9 {
		id := fmt.Sprintf("child-%d", i)
		child := Run{
			ID:        id,
			ProjectID: root.ProjectID,
			Status:    "Completed",
			Outputs:   map[string]Value{"report": Text(strings.Repeat("x", 15<<10))},
			TaskGraph: &TaskGraphState{
				RootRunID:   root.ID,
				ParentRunID: root.ID,
				TaskID:      id,
				Role:        role.ID,
			},
		}
		children[id] = child
		root.TaskGraph.Children = append(root.TaskGraph.Children, RoleTaskLink{
			RunID: id,
			Spec:  RoleTask{ID: id, Role: role.ID},
		})
		root.TaskGraph.Results = append(root.TaskGraph.Results, TaskResultRef{ID: id, RunID: id})
		results = append(results, graphResult(child))
	}
	children[root.ID] = root
	c := &Core{disk: disk, runs: children}
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) <= int(role.Execution.MaxInput) {
		t.Fatal("fixture no longer exceeds the inline prompt bound")
	}
	want := int64(role.Execution.MaxInput+p.Database.MaxRecord) + int64(len(data))
	if got := c.pendingNativeRequestBytes(); got != want {
		t.Fatalf("complete native task results were omitted from admission reserve: got %d want %d", got, want)
	}
}
