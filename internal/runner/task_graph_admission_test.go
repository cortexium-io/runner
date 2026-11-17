package runner

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestGeneralTaskAdmissionSeparatesRelationshipsFromResumptionReserve(t *testing.T) {
	p := DefaultStorage()
	p.Database.MaxRuns = 1000
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
	c := &Core{snapshot: Snapshot{Instance: Instance{EnforceWorkLimits: true}}, disk: disk, runs: map[string]Run{root.ID: root}}
	var tasks []RoleTask
	for i := range 4 {
		tasks = append(tasks, RoleTask{ID: fmt.Sprint(i), Role: "worker", Brief: strings.Repeat("b", 700), Inputs: map[string]Value{}})
	}
	if err := c.graphAction(&root, &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: tasks}}); err != nil {
		t.Fatal("separately bounded task rows consumed parent reserve", err)
	}
	portable, _, err := MarshalRunRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := MarshalRunStorageRecord(root)
	if err != nil || len(portable) <= int(p.Database.MaxRecord)/2 || len(record.State) > int(p.Database.MaxRecord)/2 || len(record.TaskLinks) != 4 {
		t.Fatal("wide task records did not separate from operational reserve", err)
	}
	resuming := Clone(root)
	resuming.TaskGraph.Results, resuming.TaskGraph.Phase = graphResultRefs(root), ""
	resuming.Visit++
	resuming.Status, resuming.Reason = "Queued", "evaluating retained child results"
	if err := admitRunRecord(resuming, p.Database.MaxRecord); err != nil {
		t.Fatal("normalized parent cannot resume after current results", err)
	}
	tooLarge := Clone(root)
	tooLarge.TaskGraph.Children[0].Spec.Brief = strings.Repeat("x", int(p.Database.MaxRecord))
	if err := admitRunRecord(tooLarge, p.Database.MaxRecord); err == nil {
		t.Fatal("normalization bypassed the individual relationship row bound")
	}
}

type rejectedResumptionStore struct {
	Store
	resumptionAllowed bool
}

func (s *rejectedResumptionStore) Save(_ context.Context, r Run, _ []Artifact, _ *Receipt) error {
	if !s.resumptionAllowed && r.Status == "Queued" && r.Visit == 1 {
		return ErrCapacity
	}
	return nil
}

func (*rejectedResumptionStore) FindReceipt(context.Context, string) (Receipt, error) {
	return Receipt{}, ErrNotFound
}

func TestGeneralTaskResumptionPersistenceFailureDoesNotAutoWaitForever(t *testing.T) {
	for _, tc := range []struct {
		name      string
		extra     int
		nested    bool
		exhausted bool
	}{
		{name: "root within existing allowance"},
		{name: "explicit root grant", extra: 1},
		{name: "nested planner", nested: true},
		{name: "completion reserve exhausted", exhausted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := DefaultStorage()
			policy.Database.MaxRuns = 1000
			disk, err := OpenDisk(t.TempDir(), policy)
			if err != nil {
				t.Fatal(err)
			}
			root := Run{
				ID:           "root",
				RecordFormat: 9,
				ProjectID:    "demo",
				Status:       "Waiting",
				Process: Process{
					MaxAttempts:        8,
					MaxAttemptsPerStep: 3,
					Steps:              []Step{{ID: "lead", Role: "planner"}},
					Tasks:              &TaskPolicy{},
				},
				TaskGraph: &TaskGraphState{
					RootRunID: "root",
					TaskID:    "root",
					Phase:     TaskWaitingChildren,
					Children:  []RoleTaskLink{{Spec: RoleTask{ID: "child", Role: "worker"}, RunID: "child-run"}},
				},
			}
			child := Run{
				ID:           "child-run",
				RecordFormat: 9,
				ProjectID:    "demo",
				Status:       "Completed",
				TaskGraph: &TaskGraphState{
					RootRunID:   root.ID,
					ParentRunID: root.ID,
					TaskID:      "child",
					Role:        "worker",
				},
			}
			store := &rejectedResumptionStore{}
			c := &Core{
				snapshot: Snapshot{Instance: Instance{EnforceWorkLimits: true}},
				ctx:      t.Context(),
				disk:     disk,
				store:    store,
				changed:  make(chan struct{}),
				wake:     make(chan struct{}, 1),
				runs:     map[string]Run{root.ID: root, child.ID: child},
			}
			if tc.nested {
				outer := Clone(root)
				outer.ID, outer.TaskGraph.RootRunID, outer.TaskGraph.TaskID = "goal", "goal", "root"
				outer.TaskGraph.Children = []RoleTaskLink{{Spec: RoleTask{ID: "planner", Role: "planner"}, RunID: root.ID}}
				root.TaskGraph.RootRunID, root.TaskGraph.ParentRunID = outer.ID, outer.ID
				root.TaskGraph.TaskID, root.TaskGraph.Role = "planner", "planner"
				child.TaskGraph.RootRunID = outer.ID
				c.runs[outer.ID], c.runs[root.ID], c.runs[child.ID] = outer, root, child
			}
			if !c.driveGraph(Clone(root)) {
				t.Fatal("parent resumption did not reach persistence")
			}
			before := Clone(c.runs[root.ID])
			if before.Status != "Failed" || before.TaskGraph.Phase != TaskWaitingChildren || TaskGraphWaiting(before) || !strings.Contains(before.Reason, "parent resumption could not be persisted") {
				t.Fatalf("failed parent resumption lost its continuation: %+v", before)
			}
			before.ResourcesReleased = true
			if tc.exhausted {
				before.Process.MaxAttempts = 1
				before.Attempts = []Attempt{{ID: "used", State: Finished, Released: true}}
			}
			c.runs[root.ID] = Clone(before)
			store.resumptionAllowed = true
			queued, err := c.Retry(RetryRequest{RequestID: "retry-continuation", RunID: root.ID, ExtraAttempts: tc.extra})
			if tc.exhausted {
				if err == nil || !strings.Contains(err.Error(), "completion attempts") || !reflect.DeepEqual(c.runs[root.ID], before) {
					t.Fatal("retry bypassed the continuation reserve or mutated a rejected grant", err)
				}
				return
			}
			if err != nil || queued.Status != "Waiting" || queued.Visit != 0 || queued.ResourcesReleased || queued.Process.MaxAttempts != before.Process.MaxAttempts+tc.extra {
				t.Fatal("retry did not reopen the saved continuation", queued, err)
			}
			c.driveGraph(Clone(c.runs[root.ID]))
			resumed := c.runs[root.ID]
			if resumed.Status != "Queued" || resumed.Visit != 1 || resumed.TaskGraph.Phase != "" || len(resumed.TaskGraph.Results) != 1 || resumed.TaskGraph.Results[0].RunID != child.ID || len(resumed.Attempts) != len(before.Attempts) || !reflect.DeepEqual(c.runs[child.ID], child) {
				t.Fatal("continuation did not retain accepted child work without an extra execution", resumed)
			}
		})
	}
}
