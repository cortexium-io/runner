package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func (s *continuationStore) SaveTaskRecovery(_ context.Context, before, after []Run, receipt *Receipt) error {
	if err := ValidateTaskRecovery(before, after, receipt); err != nil {
		return err
	}
	if s.fail {
		return errors.New("injected atomic recovery failure")
	}
	s.receipts[receipt.Key] = Clone(*receipt)
	s.saves++
	return nil
}

func failedTaskRecoveryFixture(t *testing.T) (*Core, *continuationStore, RecoverTasksRequest) {
	t.Helper()
	c, store, _, original := taskContinuationFixture(t)
	r, err := c.ContinueTask(original)
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]Value{"patch": original.Inputs["patch"], "checks": original.Inputs["checks"]}
	action := &TaskAction{Kind: "delegate", Tasks: []RoleTask{
		{ID: "import", Role: "reader", Brief: "Prepare source", Inputs: inputs},
		{ID: "baseline", Role: "reader", Brief: "Check source", Inputs: inputs, DependsOn: []string{"import"}},
		{ID: "source", Role: "reader", Brief: "Review source", Inputs: inputs, DependsOn: []string{"baseline", "import"}},
		{ID: "visual", Role: "reader", Brief: "Review images", Inputs: inputs, DependsOn: []string{"baseline", "import"}},
		{ID: "accepted", Role: "reader", Brief: "Retain independent work", Inputs: inputs},
	}}
	if err = c.graphAction(&r, &StepResult{Outcome: "ready", TaskAction: action}); err != nil {
		t.Fatal(err)
	}
	r.Status, r.ResourcesReleased, r.Reason = "Failed", true, "prerequisite unavailable"
	r.TaskGraph.Phase = ""
	r.Outputs = map[string]Value{"result": Text("prior failed assessment")}
	r.Attempts = []Attempt{{ID: "attempt_parent_rejection", Step: 0, State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "rejected", Outputs: Clone(r.Outputs)}}}}
	c.runs[r.ID] = r
	for _, link := range r.TaskGraph.Children {
		child := c.graphChild(r, r, link)
		child.Status, child.ResourcesReleased = "Failed", true
		switch link.Spec.ID {
		case "import":
			for i := range 4 {
				child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("attempt_import_%d", i), State: Finished, Released: true,
					End: &ExecutionEnd{Kind: "failed", Reason: "fixed helper needs maintenance"}})
			}
		case "baseline":
			child.Reason = "declared task dependency did not complete: import"
		case "source", "visual":
			child.Reason = "declared task dependency did not complete: baseline"
		case "accepted":
			child.Status = "Completed"
			child.Outputs = map[string]Value{"result": Text("accepted independent result")}
			child.Attempts = []Attempt{{ID: "attempt_accepted", State: Finished, Released: true,
				End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: Clone(child.Outputs)}}}}
		}
		c.runs[child.ID] = child
	}
	r.TaskGraph.Results = graphResultRefs(r)
	c.runs[r.ID] = r
	store.saves = 0
	return c, store, RecoverTasksRequest{RequestID: "maintenance-fixed", RunID: r.ID, Revision: c.outputRun(r).Revision,
		TaskIDs: []string{"import"}, Reason: "Runner prerequisite materialization has been repaired and verified"}
}

func TestTaskRecoveryRearmsOnlySameGenerationWithin42Of64(t *testing.T) {
	c, store, req := failedTaskRecoveryFixture(t)
	root := c.runs[req.RunID]
	before := Clone(c.runs)
	if c.familyAttempts(root) != 42 {
		t.Fatal("fixture must include all historical allowance", c.familyAttempts(root))
	}
	got, err := c.RecoverTasks(req)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "Waiting" || got.TaskGraph.Phase != TaskWaitingChildren || len(got.Outputs) != 0 || got.TaskBudget.Spent != 42 || got.TaskBudget.Remaining != 22 || len(got.TaskGraph.Recoveries) != 1 || store.saves != 1 {
		t.Fatal("recovery changed budget or did not retain failed-parent ownership", got, err)
	}
	for _, link := range root.TaskGraph.Children {
		old, child := before[link.RunID], c.runs[link.RunID]
		if link.Spec.ID == "accepted" {
			if !reflect.DeepEqual(old, Clone(child)) {
				t.Fatal("maintenance replayed accepted work")
			}
			continue
		}
		if child.Status != "Queued" || child.ResourcesReleased || !reflect.DeepEqual(child.Attempts, old.Attempts) || !reflect.DeepEqual(child.Process, old.Process) || !reflect.DeepEqual(child.Inputs, old.Inputs) || child.TaskGraph.TaskID != old.TaskGraph.TaskID {
			t.Fatal("recovery discarded execution facts or changed the plan", child.ID)
		}
		if link.Spec.ID == "import" && child.Visit != old.Visit+1 {
			t.Fatal("failed visit was not renewed within the unchanged total")
		}
	}
	if !reflect.DeepEqual(c.runs[root.ID].Attempts, root.Attempts) || !reflect.DeepEqual(c.runs[root.ID].TaskGraph.Children, root.TaskGraph.Children) {
		t.Fatal("parent assessment history or accepted dependency graph changed")
	}
	if _, err := c.RecoverTasks(req); err != nil || store.saves != 1 {
		t.Fatal("recovery did not replay idempotently", err)
	}
}

func TestTaskRecoveryUnlimitedPreservesSpendingBeyondDormantCeiling(t *testing.T) {
	c, _, req := failedTaskRecoveryFixture(t)
	c.snapshot.Instance.EnforceWorkLimits = false
	r := c.runs[req.RunID]
	for i := range 30 {
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("unlimited_spent_%d", i), State: Finished, Released: true})
	}
	c.runs[r.ID] = r
	req.Revision = c.outputRun(r).Revision
	got, err := c.RecoverTasks(req)
	if err != nil || got.TaskBudget == nil || !got.TaskBudget.Unlimited || got.TaskBudget.Spent != 72 || got.Process.MaxAttempts != 64 {
		t.Fatal("unlimited recovery lost history or kept its dormant cap active", got.TaskBudget, err)
	}
	state, definition, err := MarshalRunRecord(c.runs[r.ID])
	if err != nil {
		t.Fatal("recovery beyond the dormant limit could not persist", err)
	}
	loaded, err := UnmarshalRunRecord(state, definition)
	if err != nil || loaded.TaskGraph.Recoveries[0].Spent != 72 {
		t.Fatal("recovery audit lost its actual debit", err)
	}
}

func TestTaskRecoveryRefusesUnsafeOrUnownedRecoveryWithoutEffects(t *testing.T) {
	for _, fault := range []string{"active", "unknown", "cleanup", "unreleased", "stale", "completed-parent", "cancelled", "retired", "budget", "zero-selected", "duplicate", "store-failure"} {
		t.Run(fault, func(t *testing.T) {
			c, store, req := failedTaskRecoveryFixture(t)
			r := c.runs[req.RunID]
			link := r.TaskGraph.Children[0]
			child := c.runs[link.RunID]
			switch fault {
			case "active":
				c.inFlight[child.ID] = true
			case "unknown":
				child.Attempts[0].State = Unknown
			case "cleanup":
				child.CleanupError = "unsettled ownership"
			case "unreleased":
				child.ResourcesReleased = false
			case "stale":
				req.Revision++
			case "completed-parent":
				r.Status = "Completed"
			case "cancelled":
				child.CancelRequested = true
			case "retired":
				r.TaskGraph.Children[0].Retired = true
			case "budget":
				for i := range 18 {
					r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("spent_%d", i), State: Finished, Released: true})
				}
			case "zero-selected":
				req.TaskIDs = []string{"baseline"}
			case "duplicate":
				req.TaskIDs = []string{"import", "import"}
			case "store-failure":
				store.fail = true
			}
			c.runs[r.ID], c.runs[child.ID] = r, child
			before := Clone(c.runs)
			if _, err := c.RecoverTasks(req); err == nil || store.saves != 0 || !reflect.DeepEqual(Clone(c.runs), before) {
				t.Fatal("unsafe recovery mutated retained work", fault, err)
			}
		})
	}
}

func TestTaskRecoveryAutomaticallyFinishesExistingDependencyChain(t *testing.T) {
	c, _, req := failedTaskRecoveryFixture(t)
	starts := make(chan Assignment, 6)
	c.executor.(*continuationExecutor).start = func(a Assignment) Observation {
		starts <- a
		return Observation{State: Finished, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"result": Text("settled recovered result")}}}}
	}
	c.snapshot.Instance.Concurrency, c.snapshot.Instance.MaxActiveAttempts = 1, 2
	c.snapshot.Instance.ReconcileInterval = "1h"
	c.ctx, c.cancel = context.WithCancel(t.Context())
	c.loopDone = make(chan struct{})
	go c.loop()
	defer c.Close()
	r, err := c.RecoverTasks(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		r, err = c.Get(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		settled := r.Terminal()
		for _, a := range r.Attempts {
			settled = settled && a.Released
		}
		if settled {
			break
		}
		if _, err := c.Wait(ctx, r.ID, r.Revision); err != nil {
			t.Fatal("recovered chain did not resume automatically", err, r.Status, r.Reason)
		}
	}
	if r.Status != "Completed" || r.TaskBudget.Spent != 47 || r.TaskBudget.Remaining != 17 || len(starts) != 5 {
		t.Fatal("recovery bypassed native parent evaluation or reset accounting", r.Status, r.TaskBudget, len(starts))
	}
	for range 5 {
		a := <-starts
		if a.TaskContext.TaskID == "accepted" || a.TaskContext.Maintenance == nil || a.TaskContext.Maintenance.Spent != 42 {
			t.Fatal("accepted work replayed or maintenance provenance lost", a.TaskContext)
		}
	}
}

func TestFixedSharedCommandOrdinaryFailureDoesNotRepeatUnchangedArgv(t *testing.T) {
	for _, mode := range []string{"shared", "shared-retryable", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			c, _, req := failedTaskRecoveryFixture(t)
			r := c.runs[req.RunID]
			r.Status, r.TaskGraph.Children, r.TaskGraph.Results = "Running", nil, nil
			r.Process.MaxAttemptsPerStep, r.Process.InfrastructureRetries = 4, 1
			p := Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &Command{Args: []string{"/usr/bin/false"}}}
			if mode == "legacy" {
				p.Mode, r.TaskGraph, r.RecordFormat = "", nil, 3
				r.Process.Tasks = nil
			}
			role := r.Roles[r.Process.Steps[0].Role]
			role.Execution = &p
			r.Roles[role.ID] = role
			r.Attempts = []Attempt{{ID: "failed_fixed_command", State: Finished, Released: true,
				End: &ExecutionEnd{Kind: "failed", Retryable: mode == "shared-retryable", Reason: "configured command failed"}}}
			c.runs = map[string]Run{r.ID: r}
			c.finish(&r, 0)
			want := "Queued"
			if mode == "shared" {
				want = "Failed"
			}
			if r.Status != want {
				data, _ := json.Marshal(r.Attempts[0].End)
				t.Fatal("fixed command failure retry semantics", mode, r.Status, string(data))
			}
		})
	}
}
