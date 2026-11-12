package runner

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProgressUsesAuthoritativeStateAndDeadlineNotWaitingOrPollTime(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	started := now.Add(-15 * time.Minute)
	base := Run{
		ID:      "run",
		Status:  "Waiting",
		Created: started.Add(-time.Minute),
		Updated: now,
		Attempts: []Attempt{{
			ID: "attempt", Started: started, Deadline: started.Add(20 * time.Minute), State: Running,
		}},
	}
	for _, test := range []struct {
		name        string
		state       ExecutionState
		reconciling bool
		deadline    time.Time
		execution   string
	}{
		{"valid long work", Running, false, base.Attempts[0].Deadline, "active"},
		{"bounded same work", Unknown, true, base.Attempts[0].Deadline, "reconciling"},
		{"unknown effects", Unknown, false, base.Attempts[0].Deadline, "unknown"},
		{"expired rejoin", Unknown, true, now.Add(-time.Second), "unknown"},
		{"not started", NotStarted, false, base.Attempts[0].Deadline, "pending"},
		{"missing timing", Unknown, true, time.Time{}, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := Clone(base)
			r.Attempts[0].State, r.Attempts[0].Reconciling, r.Attempts[0].Deadline = test.state, test.reconciling, test.deadline
			before := Clone(r)
			p := Summarize(r, now).Progress
			if p.Execution != test.execution || !p.LastProgressAt.Equal(started) || !reflect.DeepEqual(r, before) {
				t.Fatal("progress inferred from Waiting/poll time or changed execution", p)
			}
			if len(p.Attempts) != 1 || p.Attempts[0].DeadlineKnown != !test.deadline.IsZero() || !p.Attempts[0].Deadline.Equal(test.deadline) {
				t.Fatal("deadline was invented or lost", p)
			}
		})
	}
	unknown := Summarize(Run{ID: "legacy", Status: "Waiting"}, now).Progress
	if !unknown.LastProgressAt.IsZero() || unknown.ProgressBasis != "unknown" || unknown.Active != 0 || unknown.Execution != "idle" || unknown.NextAction != "inspect_blocker" {
		t.Fatal("missing timing became a heartbeat/execution", unknown)
	}
}

func TestProgressAggregatesFamilyWithoutHidingIndependentDecisions(t *testing.T) {
	now := time.Now().UTC()
	c := &Core{runs: map[string]Run{}}
	root := Run{
		ID: "root", Status: "Running", Revision: 10, Created: now.Add(-time.Hour),
		Process: Process{Steps: []Step{{ID: "cards"}}},
		TaskGroups: map[string]*TaskGroupState{"cards": {
			StepID: "cards", Items: []TaskItem{{ID: "active"}, {ID: "decision"}},
			Children: []TaskChild{{ItemID: "active", RunID: "active"}, {ItemID: "decision", RunID: "decision"}},
		}},
		Attempts: []Attempt{{State: Finished, Released: true, FinishedAt: now.Add(-30 * time.Minute)}},
	}
	c.runs["active"] = Run{
		ID: "active", Status: "Waiting", Revision: 3, Created: root.Created,
		Attempts: []Attempt{{ID: "long", State: Running, Started: now.Add(-10 * time.Minute), Deadline: now.Add(10 * time.Minute)}},
	}
	c.runs["decision"] = Run{
		ID: "decision", Status: "Waiting", Revision: 4, Created: root.Created,
		Decision: &Decision{ID: "saved-decision", AttemptID: "finished"},
		Attempts: []Attempt{{ID: "finished", State: Finished, Released: true, FinishedAt: now.Add(-time.Minute)}},
	}
	// Usage lookup needs the original frozen role, independent of reporting.
	for id, child := range c.runs {
		child.Task = &TaskRunLink{Identity: TaskIdentity{RootRunID: root.ID, GroupStep: "cards", ItemID: id}}
		child.Process.Steps = []Step{{Role: "fixture"}}
		child.Roles = map[string]Role{"fixture": {Execution: &Profile{Harness: "deterministic"}}}
		c.runs[id] = child
	}
	c.runs[root.ID] = root
	before := Clone(c.runs)
	view := c.outputRun(root)
	out := Summarize(view, now)
	if out.Attempts != 3 || out.Revision != 17 || out.Progress.Active != 1 || out.Progress.Execution != "active" || out.Progress.NextAction != "decide_saved_approval" {
		t.Fatal("family accounting/active execution/decision lost", out)
	}
	decision := false
	for _, a := range out.Progress.Attention {
		decision = decision || a.RunID == "decision" && a.DecisionID == "saved-decision" && a.Kind == "human_decision"
	}
	if !decision || len(out.Progress.Attempts) != 1 || out.Progress.Attempts[0].RunID != "active" || !reflect.DeepEqual(c.runs, before) {
		t.Fatal("projection hid decision/timing or changed private facts")
	}
	if !view.TaskStatuses[0].Active || !view.TaskStatuses[0].Unsettled || view.TaskStatuses[1].Active || view.TaskStatuses[1].Unsettled {
		t.Fatal("child activity was inferred from Waiting/unsettled alone")
	}
	policy := DefaultStorage()
	policy.MinFree = 0
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	c.disk = disk
	for range 3 {
		status := c.Status()
		for _, row := range status.Runs {
			if row.ID == root.ID && (row.Attempts != 3 || row.Revision != 10 || row.Progress.Active != 1 || row.Progress.NextAction != "decide_saved_approval") {
				t.Fatal("status lost aggregate accounting or changed revision convention", row)
			}
		}
	}
	if !reflect.DeepEqual(c.runs, before) {
		t.Fatal("status observer changed private execution/decision facts")
	}
}

func TestProgressBoundsFactsKeepsDecisionsAndExportsNoPrivateSetup(t *testing.T) {
	now := time.Now().UTC()
	r := Run{
		ID: "root", Status: "Running", Created: now,
		Inputs: map[string]Value{"credential": Text("private-credential")},
		Roles:  map[string]Role{"owner": {Purpose: "private-prompt"}},
	}
	for range 20 {
		r.Attempts = append(r.Attempts, Attempt{ID: "private-handle", State: Running, Started: now, Deadline: now.Add(-time.Second), NativeHandle: "private-native-handle"})
	}
	child := runProgress(Run{ID: "child", Decision: &Decision{ID: "decision"}}, now)
	r.TaskStatuses = []TaskStatus{{RunID: "child", Attempts: 2, Progress: &child}}
	p := Summarize(r, now).Progress
	if !p.Truncated || len(p.Attempts) != maxProgressFacts || len(p.Attention) > maxProgressFacts || p.Active != 20 || p.NextAction != "decide_saved_approval" {
		t.Fatal("bounded projection lost counts or decision", p)
	}
	b, err := json.Marshal(p)
	if err != nil || strings.Contains(string(b), "private-credential") || strings.Contains(string(b), "private-prompt") || strings.Contains(string(b), "private-native-handle") || len(b) > 8192 {
		t.Fatal("progress exposed setup/diagnostics or exceeded its bound", err, len(b))
	}
}
