package runner

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestGeneralTaskAdmissionReservesFutureResultReferenceState(t *testing.T) {
	p := DefaultStorage()
	p.MinFree, p.Database.MaxRecord = 0, 4096
	disk, err := OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	root := Run{
		ID: "root", ProjectID: "demo", RecordFormat: 9, Repository: t.TempDir(), Status: "Running",
		Process:   Process{ID: "general", MaxAttempts: 24, Tasks: &TaskPolicy{Roles: []string{"worker"}, MaxTasks: 16, MaxDepth: 4, MaxConcurrent: 2}},
		Roles:     map[string]Role{"worker": {ID: "worker", Purpose: "Produce a result", Inputs: map[string]string{}, Outputs: map[string]string{"report": "text"}, Execution: &Profile{Harness: "deterministic"}}},
		TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root", Brief: "Complete the goal"},
	}
	c := &Core{disk: disk, runs: map[string]Run{root.ID: root}}
	var tasks []RoleTask
	found := false
	for padding := 1; padding < 512; padding++ {
		candidate := Clone(root)
		tasks = nil
		for i := range 4 {
			spec := RoleTask{ID: fmt.Sprint(i), Role: "worker", Brief: strings.Repeat("b", padding), Inputs: map[string]Value{}}
			tasks = append(tasks, spec)
			candidate.TaskGraph.Children = append(candidate.TaskGraph.Children, RoleTaskLink{Spec: spec, RunID: fmt.Sprintf("run_%026d", i)})
		}
		candidate.TaskGraph.Phase = TaskWaitingChildren
		candidate.Status, candidate.Reason = "Waiting", "waiting for role tasks"
		if admitRunRecord(candidate, p.Database.MaxRecord) != nil {
			continue
		}
		resuming := Clone(candidate)
		resuming.TaskGraph.Results, resuming.TaskGraph.Phase = graphResultRefs(candidate), ""
		resuming.Visit++
		resuming.Status, resuming.Reason = "Queued", "evaluating retained child results"
		if admitRunRecord(resuming, p.Database.MaxRecord) != nil {
			found = true
			break
		}
		tasks = nil
	}
	if !found {
		t.Fatal("fixture did not reach the result-reference recovery boundary")
	}
	err = c.graphAction(&root, &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: tasks}})
	if err == nil || !strings.Contains(err.Error(), "parent resumption") || len(root.TaskGraph.Children) != 0 || len(c.runs) != 1 {
		t.Fatalf("unresumable metadata fan-out was admitted: %v", err)
	}
}

type rejectedResumptionStore struct{ Store }

func (rejectedResumptionStore) Save(_ context.Context, r Run, _ []Artifact, _ *Receipt) error {
	if r.Status == "Queued" && r.Visit == 1 {
		return ErrCapacity
	}
	return nil
}

func TestGeneralTaskResumptionPersistenceFailureDoesNotAutoWaitForever(t *testing.T) {
	root := Run{
		ID: "root", ProjectID: "demo", Status: "Waiting", Process: Process{Tasks: &TaskPolicy{}},
		TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root", Phase: TaskWaitingChildren, Children: []RoleTaskLink{{Spec: RoleTask{ID: "child", Role: "worker"}, RunID: "child-run"}}},
	}
	child := Run{ID: "child-run", ProjectID: "demo", Status: "Completed", TaskGraph: &TaskGraphState{RootRunID: root.ID, ParentRunID: root.ID, TaskID: "child", Role: "worker"}}
	c := &Core{ctx: t.Context(), store: rejectedResumptionStore{}, changed: make(chan struct{}), runs: map[string]Run{root.ID: root, child.ID: child}}
	if !c.driveGraph(root) {
		t.Fatal("parent resumption did not reach persistence")
	}
	if saved := c.runs[root.ID]; saved.Status != "Failed" || TaskGraphWaiting(saved) || !strings.Contains(saved.Reason, "parent resumption could not be persisted") || len(saved.Attempts) != 0 {
		t.Fatalf("failed parent resumption became an automatic wait or consumed execution: %+v", saved)
	}
}
