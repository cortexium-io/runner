package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type budgetCorrectionExecutor struct {
	validations int
	starts      atomic.Int32
}

func (e *budgetCorrectionExecutor) ValidateSourceRecovery(context.Context, Run, SourceResult) error {
	e.validations++
	return nil
}
func (e *budgetCorrectionExecutor) Start(context.Context, Assignment) (Observation, error) {
	e.starts.Add(1)
	return Observation{State: Unknown}, nil
}
func (*budgetCorrectionExecutor) Observe(context.Context, string) (Observation, error) {
	return Observation{State: Unknown}, nil
}
func (*budgetCorrectionExecutor) Stop(context.Context, string) error    { return nil }
func (*budgetCorrectionExecutor) Release(context.Context, string) error { return nil }

func budgetCorrectionFixture(t *testing.T) (*Core, *frontendRecheckStore, *budgetCorrectionExecutor, RecoverCorrectionsRequest) {
	t.Helper()
	c, store, _, assertion := frontendAssertionRecoveryFixture(t)
	r := c.runs[assertion.RunID]
	// Earlier setup phases are immaterial to this retained-source fixture. Keep
	// its cards, join, source/check history and original 42 ceiling, with enough
	// headroom to prove more than one complete correction/check cycle.
	r.Attempts = r.Attempts[4:]
	c.runs[r.ID] = r
	exec := &budgetCorrectionExecutor{}
	c.executor = exec
	req := RecoverCorrectionsRequest{
		RequestID:    "budget-bounded-owner-grant",
		RunID:        r.ID,
		AttemptID:    r.Current().ID,
		WithinBudget: true,
	}
	return c, store, exec, req
}

func budgetCorrectionArtifact(t *testing.T, c *Core, store *frontendRecheckStore, r Run, id string, data []byte) Value {
	t.Helper()
	if err := c.disk.Write("artifacts/"+id, data, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(data))}
	return Value{ArtifactID: id}
}

func finishBudgetCorrectionOwner(t *testing.T, c *Core, store *frontendRecheckStore, r *Run, number int) {
	t.Helper()
	if r.Process.Steps[r.Step].ID != "owner" {
		t.Fatal("fixture is not at the correction owner", r.Status, r.Reason)
	}
	var source *SourceResult
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		if old := r.Attempts[i].Source; old != nil && old.PatchID != "" {
			source = Clone(old)
			break
		}
	}
	if source == nil {
		t.Fatal("fixture lost its retained correction source")
	}
	owner := Attempt{
		ID:       fmt.Sprintf("attempt_budget_owner_%d", number),
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
	}
	source.AttemptID, source.InputPatchID, source.PatchID = owner.ID, source.PatchID, owner.ID+"/changes.patch"
	owner.Source = source
	owner.End = &ExecutionEnd{
		Kind: "returned",
		Result: &StepResult{
			Outcome: "ready",
			Outputs: map[string]Value{
				"summary": Text("complete regression diagnosed and corrected"),
				"patch":   budgetCorrectionArtifact(t, c, store, *r, source.PatchID, []byte(fmt.Sprintf("correction%d", number))),
			},
		},
	}
	r.Attempts = append(r.Attempts, owner)
	for _, artifact := range c.finish(r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "check" {
		t.Fatal("correction skipped its fresh ordinary check", r.Status, r.Reason)
	}
}

func finishBudgetCorrectionCheck(t *testing.T, c *Core, store *frontendRecheckStore, r *Run, number int, pass bool) {
	t.Helper()
	owner := r.Attempts[len(r.Attempts)-1].Source
	check := Attempt{
		ID:       fmt.Sprintf("attempt_budget_check_%d", number),
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
	}
	check.Source = &SourceResult{
		AttemptID:    check.ID,
		InputPatchID: owner.PatchID,
		Repository:   owner.Repository,
		Base:         owner.Base,
		WorkDir:      owner.WorkDir,
	}
	outcome := "changes_requested"
	if pass {
		outcome = "ready"
	}
	data, err := json.Marshal(exhaustedFrontendReportFixture(t, false, !pass, false))
	if err != nil {
		t.Fatal(err)
	}
	check.End = &ExecutionEnd{
		Kind: "returned",
		Result: &StepResult{
			Outcome: outcome,
			Outputs: map[string]Value{
				"check":    Text("complete full-suite receipt"),
				"artifact": budgetCorrectionArtifact(t, c, store, *r, check.ID+"/result", data),
			},
		},
	}
	r.Attempts = append(r.Attempts, check)
	for _, artifact := range c.finish(r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
}

func TestCorrectionRecoveryWithinBudgetFinishesRepeatedCorrections(t *testing.T) {
	c, store, exec, req := budgetCorrectionFixture(t)
	before := Clone(c.runs[req.RunID])
	_, definition, _ := MarshalRunRecord(before)
	remaining := c.familyRemaining(before)
	r, err := c.RecoverCorrections(req)
	if err != nil {
		t.Fatal(err)
	}
	audit := Clone(r.CorrectionRecovery)
	if audit == nil || !audit.WithinBudget || audit.MaxUses != 0 || audit.RemainingAttempts != remaining || len(audit.RouteLimits) != 1 ||
		audit.RouteLimits["check/changes_requested"] != before.RouteUses["check/changes_requested"]+remaining || r.Process.MaxAttempts != 42 ||
		r.RouteUses["check/changes_requested"] != 4 || c.familyRemaining(r) != remaining || r.Decision != nil {
		t.Fatal("budget policy reset counters, changed the frozen ceiling or granted unrelated routes", audit, r.Status)
	}
	if replay, err := c.RecoverCorrections(req); err != nil || !reflect.DeepEqual(replay.CorrectionRecovery, audit) ||
		replay.Step != r.Step || replay.Visit != r.Visit || !reflect.DeepEqual(replay.Attempts, r.Attempts) || store.saves != 1 || exec.validations != 1 {
		t.Fatal("budget policy did not replay its single audit grant", err, store.saves, exec.validations)
	}
	changed := req
	changed.WithinBudget, changed.MaxUses = false, 3
	if _, err := c.RecoverCorrections(changed); err == nil || store.saves != 1 || exec.validations != 1 {
		t.Fatal("same request ID accepted a different correction policy", err)
	}
	assignment, err := c.assignment(r, Attempt{ID: "attempt_budget_owner_1", Step: r.Step, Visit: r.Visit})
	if err != nil || assignment.Inputs["checks"].ArtifactID != before.Current().ID+"/result" || !strings.Contains(assignment.Task, "complete failing test") {
		t.Fatal("budgeted owner lost its full report or complete-regression diagnosis instruction", err)
	}
	finishBudgetCorrectionOwner(t, c, store, &r, 1)
	finishBudgetCorrectionCheck(t, c, store, &r, 1, false)
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || r.RouteUses["check/changes_requested"] != 5 {
		t.Fatal("remaining budget did not admit the next internal correction", r.Status, r.Reason)
	}
	finishBudgetCorrectionOwner(t, c, store, &r, 2)
	finishBudgetCorrectionCheck(t, c, store, &r, 2, true)
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.Decision != nil || c.familyRemaining(r) != remaining-4 {
		t.Fatal("passing full check bypassed downstream work or spent unrecorded attempts", r.Status, r.Reason)
	}
	_, finalDefinition, _ := MarshalRunRecord(r)
	if !bytes.Equal(definition, finalDefinition) || !reflect.DeepEqual(r.CorrectionRecovery, audit) || !reflect.DeepEqual(r.TaskGroups, before.TaskGroups) ||
		!reflect.DeepEqual(r.FrontendTimeoutRecovery, before.FrontendTimeoutRecovery) {
		t.Fatal("internal corrections changed the original definition, accepted cards or historical grant")
	}
	for i, old := range before.Attempts {
		got := Clone(r.Attempts[i])
		got.Superseded = old.Superseded
		if !reflect.DeepEqual(got, old) {
			t.Fatal("internal correction rewrote retained attempt evidence", old.ID)
		}
	}
	review := taskStepIndex(r.Process, "review")
	route := r.Process.Steps[review].Routes["changes_requested"]
	if routeLimit(r, review, "changes_requested", route) != route.MaxTimes {
		t.Fatal("budget grant expanded an unrelated review route")
	}
}

func TestCorrectionRecoveryWithinBudgetPreservesFamilyCompletionReserve(t *testing.T) {
	c, store, _, req := budgetCorrectionFixture(t)
	r, err := c.RecoverCorrections(req)
	if err != nil {
		t.Fatal(err)
	}
	finishBudgetCorrectionOwner(t, c, store, &r, 1)
	need, err := readyTail(r, taskStepIndex(r.Process, "owner"))
	if err != nil {
		t.Fatal(err)
	}
	childID := r.TaskGroups["cards"].Children[0].RunID
	child := c.runs[childID]
	// Charge completed child work so the next failed check leaves one fewer
	// attempt than its correction plus full protected completion path needs.
	for range c.familyRemaining(r) - need {
		child.Attempts = append(child.Attempts, Attempt{ID: fmt.Sprintf("attempt_child_extra_%d", len(child.Attempts)), State: Finished, Released: true})
	}
	c.runs[childID] = child
	uses := r.RouteUses["check/changes_requested"]
	finishBudgetCorrectionCheck(t, c, store, &r, 1, false)
	if r.Status != "Failed" || !strings.Contains(r.Reason, "preserves completion reserve") || c.familyRemaining(r) != need-1 ||
		r.RouteUses["check/changes_requested"] != uses || r.Process.MaxAttempts != 42 || r.Current().ID != "attempt_budget_check_1" {
		t.Fatal("budget recovery consumed its protected reserve or ignored child attempts", r.Status, r.Reason)
	}
}

func TestCorrectionRecoveryWithinBudgetQueuedRootWithoutGroupsReservesTail(t *testing.T) {
	c, _, exec, req := budgetCorrectionFixture(t)
	r := c.runs[req.RunID]
	for id, child := range c.runs {
		if child.Task != nil {
			delete(c.runs, id)
		}
	}
	r.TaskGroups = nil
	c.runs[r.ID] = r
	r, err := c.RecoverCorrections(req)
	if err != nil {
		t.Fatal(err)
	}
	reserve, err := c.taskCompletionReserve(r)
	if err != nil || reserve < 1 {
		t.Fatal("budgeted plain root did not reserve its downstream work", reserve, err)
	}
	for c.familyRemaining(r) > reserve {
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("attempt_spent_%d", len(r.Attempts)), State: Finished, Released: true})
	}
	c.runs[r.ID] = r
	c.snapshot.Instance.Concurrency = 1
	c.snapshot.Instance.ReconcileInterval = "1s"
	count := len(r.Attempts)
	c.tick()
	c.wg.Wait()
	stopped := c.runs[r.ID]
	if stopped.Status != "Waiting" || !strings.Contains(stopped.Reason, "reserved completion attempts") || len(stopped.Attempts) != count || exec.starts.Load() != 0 || stopped.Process.MaxAttempts != 42 {
		t.Fatal("plain root admitted an owner that would consume its completion reserve", stopped.Status, stopped.Reason, exec.starts.Load())
	}
}

func TestCorrectionRecoveryWithinBudgetRejectsOtherAuthority(t *testing.T) {
	for _, fault := range []string{"mixed modes", "missing mode", "preservation", "child", "approval", "release effect", "unknown effect", "pending decision", "protected budget"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, req := budgetCorrectionFixture(t)
			r := Clone(c.runs[req.RunID])
			switch fault {
			case "mixed modes":
				req.MaxUses = 3
			case "missing mode":
				req.WithinBudget = false
			case "preservation":
				req.PreserveSource = true
			case "child":
				r.Task = &TaskRunLink{Identity: TaskIdentity{RootRunID: "another_root"}}
			case "approval":
				route := r.Process.Steps[r.Step].Routes["changes_requested"]
				route.Approval = true
				r.Process.Steps[r.Step].Routes["changes_requested"] = route
			case "release effect":
				role := r.Roles[r.Process.Steps[r.Step].Role]
				role.Execution = &Profile{Harness: "release", Access: "input-only", Mode: "deploy"}
				r.Roles[r.Process.Steps[r.Step].Role] = role
			case "unknown effect":
				r.Current().State = Unknown
			case "pending decision":
				r.Decision = &Decision{ID: "saved_owner_decision"}
			case "protected budget":
				need, err := readyTail(r, taskStepIndex(r.Process, "owner"))
				if err != nil {
					t.Fatal(err)
				}
				childID := r.TaskGroups["cards"].Children[0].RunID
				child := c.runs[childID]
				for range c.familyRemaining(r) - need + 1 {
					child.Attempts = append(child.Attempts, Attempt{State: Finished, Released: true})
				}
				c.runs[childID] = child
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverCorrections(req); err == nil || store.saves != 0 || exec.validations != 0 || !reflect.DeepEqual(c.runs[r.ID], r) {
				t.Fatal("budget policy bypassed an authority or protected-budget boundary", err, store.saves, exec.validations)
			}
		})
	}
}
