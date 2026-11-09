package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const rejectedPlanningDraft = `[{"id":"observe","inputs":{"task":"Instrument the scoped request without changing its behavior.","paths":["observe_test.go"]}}]`

type planningRecoveryExecutor struct {
	Executor
	validate func(Run, SourceResult) error
	calls    int
}

func (e *planningRecoveryExecutor) ValidateSourceRecovery(_ context.Context, run Run, source SourceResult) error {
	e.calls++
	if e.validate != nil {
		return e.validate(run, source)
	}
	return nil
}

func planningRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, *planningRecoveryExecutor, RecoverPlanningRequest) {
	t.Helper()
	examples := filepath.Join("..", "..", "examples", "jacks-break")
	load := func(name string, value any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, value); err != nil {
			t.Fatal(err)
		}
	}
	var process Process
	load("planned-retained-investigation.json", &process)
	roles := map[string]Role{}
	var loadRoles func([]Step)
	loadRoles = func(steps []Step) {
		for _, step := range steps {
			if step.ForEach != nil {
				loadRoles(step.ForEach.Steps)
			} else if _, loaded := roles[step.Role]; !loaded {
				var role Role
				load(filepath.Join("roles", step.Role+".json"), &role)
				roles[role.ID] = role
			}
		}
	}
	loadRoles(process.Steps)
	policy := DefaultStorage()
	policy.Database.MaxRecord = 40 << 10
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	store := &frontendRecheckStore{artifacts: map[string]Artifact{}, receipts: map[string]Receipt{}}
	exec := &planningRecoveryExecutor{}
	c := &Core{
		ctx: t.Context(), store: store, disk: disk, executor: exec,
		snapshot: Snapshot{Instance: Instance{TaskConcurrency: 2}},
		runs:     map[string]Run{}, inFlight: map[string]bool{}, changed: make(chan struct{}), wake: make(chan struct{}, 1),
	}
	const blockedReason = "The rejected proposal is missing from the admission feedback."
	const invalidReason = "task observe owner input: resolved input/instructions/schema exceed max_input"
	const patchID = "attempt_adopt/changes.patch"
	const baselineID = "attempt_baseline/result"
	base := strings.Repeat("a", 40)
	r := Run{
		RecordFormat: 3, ID: "run_planning", ProjectID: "demo", Repository: filepath.Join(disk.Root, "repository"),
		Source: &SourceSettings{Ref: base}, Process: process, Roles: roles,
		Status: "Failed", Reason: blockedReason, Step: taskStepIndex(process, "plan"), Visit: 5, Revision: 188,
		ResourcesReleased: true, Created: time.Now().UTC(),
		Inputs: map[string]Value{
			"request": Text("Measure the current behavior before attempting repair."),
			"package": Text("./scope"), "tests": Text("^TestScope$"), "benchmark": Text("^BenchmarkScope$"),
			"publication": {Inline: json.RawMessage(`{"key":"accepted-publication"}`)},
			"patch":       {ArtifactID: "attempt_origin/changes.patch"},
		},
		RouteUses: map[string]int{"baseline/continue": 1, "source-adopt/ready": 1, "admit/ready": 1, "plan/ready": 1, "cards/invalid_plan": 1, "plan/blocked": 1},
	}
	r.StepInputs = map[string]Value{"request": r.Inputs["request"], "baseline": {ArtifactID: baselineID}, "feedback": Text(invalidReason)}
	for id, content := range map[string]string{patchID: "retained adopted patch", baselineID: "original measurement baseline"} {
		if err := disk.Write("artifacts/"+id, []byte(content), policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(content))}
	}
	workDir := filepath.Join(disk.Root, "work", r.ID, "source")
	attempt := func(id, step string, visit int, outcome, reason string, outputs map[string]Value) Attempt {
		return Attempt{
			ID: "attempt_" + id, Step: taskStepIndex(process, step), Visit: visit, State: Finished, Released: true,
			End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: outcome, Reason: reason, Outputs: outputs}},
		}
	}
	r.Attempts = []Attempt{
		attempt("baseline", "baseline", 0, "continue", "", map[string]Value{"artifact": {ArtifactID: baselineID}}),
		attempt("adopt", "source-adopt", 1, "ready", "", map[string]Value{"patch": {ArtifactID: patchID}, "receipt": {Inline: json.RawMessage(`{"key":"attempt_adopt","action":"adopt"}`)}}),
		attempt("admit", "admit", 2, "ready", "", nil),
		attempt("original_plan", "plan", 3, "ready", "", map[string]Value{"tasks": Text(rejectedPlanningDraft)}),
		attempt("invalid", "cards", 4, "invalid_plan", invalidReason, map[string]Value{"feedback": Text(invalidReason)}),
		attempt("blocked", "plan", 5, "blocked", blockedReason, map[string]Value{"tasks": Text("[]")}),
	}
	r.Attempts[1].Source = &SourceResult{
		AttemptID: r.Attempts[1].ID, PatchID: patchID, InputPatchID: patchID,
		Repository: r.Repository, WorkDir: workDir, Base: base, Tree: strings.Repeat("b", 40),
	}
	for _, i := range []int{3, 5} {
		a := &r.Attempts[i]
		a.Source = &SourceResult{AttemptID: a.ID, InputPatchID: patchID, Repository: r.Repository, WorkDir: workDir, Base: base}
	}
	r.Attempts[3].Superseded, r.Attempts[4].Superseded = true, true
	c.runs[r.ID] = r
	return c, store, exec, RecoverPlanningRequest{RequestID: "owner-approved-plan-recovery", RunID: r.ID, AttemptID: r.Current().ID, Revision: r.Revision}
}

func TestRecoverPlanningRejoinsFrozenReadOnlyVisitOnce(t *testing.T) {
	c, store, exec, req := planningRecoveryFixture(t)
	before := Clone(c.runs[req.RunID])
	exec.validate = func(r Run, source SourceResult) error {
		if r.Revision != before.Revision || source != *before.Attempts[1].Source {
			return errors.New("recovery validated different retained source")
		}
		return nil
	}
	r, err := c.RecoverPlanning(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "Queued" || r.Step != before.Step || r.Visit != before.Visit+1 || r.Revision != before.Revision+1 || r.Current() != nil || r.ResourcesReleased || r.Decision != nil || len(r.TaskGroups) != 0 || len(c.runs) != 1 {
		t.Fatalf("recovery did not queue just the planner: status=%s step=%d visit=%d", r.Status, r.Step, r.Visit)
	}
	wantRoutes := Clone(before.RouteUses)
	wantRoutes["plan/"+planningRecovery] = 1
	if !reflect.DeepEqual(r.Attempts, before.Attempts) || !reflect.DeepEqual(r.Process, before.Process) || !reflect.DeepEqual(r.Roles, before.Roles) || !reflect.DeepEqual(r.Inputs, before.Inputs) || !reflect.DeepEqual(r.RouteUses, wantRoutes) || c.familyRemaining(r) != 58 {
		t.Fatal("recovery changed saved attempts, workflow, authority, or allowance")
	}
	a, err := c.assignment(r, Attempt{ID: "attempt_replan", Step: r.Step, Deadline: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var feedback string
	if json.Unmarshal(a.Inputs["feedback"].Inline, &feedback) != nil || !strings.Contains(feedback, rejectedPlanningDraft) || !strings.Contains(feedback, before.Attempts[4].End.Result.Reason) || !strings.Contains(feedback, "No cards from this plan were admitted") {
		t.Fatal("planner did not receive the rejected proposal and its causal feedback", feedback)
	}
	if a.Setup.Profile.Access != "workspace-read" || a.Source == nil || *a.Source != *before.Source || !reflect.DeepEqual(a.Inputs["baseline"], before.StepInputs["baseline"]) || !reflect.DeepEqual(a.Inputs["request"], before.StepInputs["request"]) {
		t.Fatal("planner assignment lost retained evidence or its read-only source boundary")
	}
	if replay, err := c.RecoverPlanning(req); err != nil || !reflect.DeepEqual(replay, r) || store.saves != 1 || exec.calls != 1 || len(c.wake) != 1 {
		t.Fatal("identical request did not replay the one persisted visit", err, store.saves, exec.calls)
	}
	req.AttemptID = before.Attempts[3].ID
	if _, err := c.RecoverPlanning(req); err == nil || store.saves != 1 {
		t.Fatal("same request ID accepted a different attempt", err)
	}
}

func TestRecoverPlanningRejectsChangedSavedEvidence(t *testing.T) {
	for _, fault := range []string{"stale revision", "different attempt", "reserved cards", "pending approval", "source changed", "foreign baseline", "feedback changed", "insufficient budget", "grant already used", "source validation failed", "validation race"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, req := planningRecoveryFixture(t)
			r := Clone(c.runs[req.RunID])
			calls := 0
			switch fault {
			case "stale revision":
				req.Revision--
			case "different attempt":
				req.AttemptID = r.Attempts[3].ID
			case "reserved cards":
				r.TaskGroups = map[string]*TaskGroupState{"cards": {StepID: "cards", Children: []TaskChild{{ItemID: "observe", RunID: "run_card"}}}}
			case "pending approval":
				r.Decision = &Decision{ID: "decision_owner", Step: "prepare", Next: "merge"}
			case "source changed":
				r.Current().Source.InputPatchID = "attempt_other/changes.patch"
			case "foreign baseline":
				id := r.StepInputs["baseline"].ArtifactID
				a := store.artifacts[id]
				a.RunID = "run_other"
				store.artifacts[id] = a
			case "feedback changed":
				r.StepInputs["feedback"] = Text("unrelated rejection")
			case "insufficient budget":
				r.Process.MaxAttempts = len(r.Attempts) + 1
			case "grant already used":
				r.RouteUses["plan/"+planningRecovery] = 1
			case "source validation failed":
				calls = 1
				exec.validate = func(Run, SourceResult) error { return errors.New("retained patch digest changed") }
			case "validation race":
				calls = 1
				exec.validate = func(_ Run, _ SourceResult) error {
					c.mu.Lock()
					defer c.mu.Unlock()
					r.Revision++
					c.runs[r.ID] = r
					return nil
				}
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverPlanning(req); err == nil || store.saves != 0 || exec.calls != calls || !reflect.DeepEqual(c.runs[r.ID], r) || len(c.wake) != 0 {
				t.Fatal("unsafe recovery changed or dispatched work", err, store.saves, exec.calls)
			}
		})
	}
}
