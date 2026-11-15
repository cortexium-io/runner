package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func serialFrontendRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, RecoverFrontendCheckRequest) {
	t.Helper()
	c, store, _, req := frontendRecheckFixture(t)
	r := c.runs[req.RunID]
	examples := filepath.Join("..", "..", "examples", "jacks-break")
	load := func(name string, out any) {
		data, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	r.Process = Process{}
	load("retained-catalogue.json", &r.Process)
	r.Step = taskStepIndex(r.Process, "frontend-check")
	// The failed definition predates the dedicated one-worker delivery role.
	r.Process.Steps[r.Step].Role = "frontend-check"
	for _, step := range r.Process.Steps {
		var role Role
		load(filepath.Join("roles", step.Role+".json"), &role)
		r.Roles[step.Role] = role
	}
	owner, check := r.Attempts[len(r.Attempts)-1], r.Attempts[len(r.Attempts)-2]
	owner.Step, owner.Visit, owner.End.Result.Outcome = r.Step-1, 1, "ready"
	check.Step, check.Visit, check.Superseded = r.Step, 2, false
	check.Source.InputPatchID = owner.Source.PatchID
	r.Attempts = make([]Attempt, 8, 10)
	for i := range r.Attempts {
		r.Attempts[i] = Attempt{ID: "attempt_prior_" + string(rune('a'+i)), State: Finished, Released: true, Superseded: true}
	}
	r.Attempts = append(r.Attempts, owner, check)
	r.TaskGroups, r.Visit = nil, check.Visit
	r.CorrectionRecovery = &CorrectionRecovery{MaxUses: 3, RouteLimits: map[string]int{"frontend-check/changes_requested": 3}}
	r.RouteUses = map[string]int{"frontend-check/changes_requested": 3}
	r.Reason = "repetition allowance exhausted: frontend-check/changes_requested"
	r.Inputs["frontend_tests"] = r.Inputs["tests"]
	r.StepInputs = map[string]Value{"patch": {ArtifactID: owner.Source.PatchID}, "tests": r.Inputs["frontend_tests"]}
	req.AttemptID, req.Revision, req.Serial = check.ID, r.Revision, true
	c.runs = map[string]Run{r.ID: r}
	return c, store, req
}

func TestSerialFrontendRecoveryUsesRemainingBudgetAndPinsFutureChecks(t *testing.T) {
	c, store, req := serialFrontendRecoveryFixture(t)
	before := Clone(c.runs[req.RunID])
	r, err := c.RecoverFrontendCheck(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "Queued" || r.Step != before.Step || r.Visit != before.Visit+1 || r.Decision != nil ||
		!reflect.DeepEqual(r.Process, before.Process) || len(r.Attempts) != len(before.Attempts) ||
		!reflect.DeepEqual(r.Attempts[9].End, before.Attempts[9].End) || !r.Attempts[9].Superseded || r.RouteUses["frontend-check/changes_requested"] != 3 {
		t.Fatal("serial recheck changed the release definition, attempt budget or recorded failure")
	}
	for _, visit := range []int{r.Visit, r.Visit + 3} {
		current := Clone(r)
		current.Visit = visit
		assignment, err := c.assignment(current, Attempt{Step: current.Step, Visit: visit})
		if err != nil {
			t.Fatal(err)
		}
		pins := assignment.FrontendCheckPins
		wantPatch := req.PatchSHA256
		if visit != r.Visit {
			wantPatch = ""
		}
		if pins == nil || !pins.Serial || pins.HelperSHA256 != req.HelperSHA256 || pins.PatchSHA256 != wantPatch {
			t.Fatalf("visit %d lost serial provenance: %+v", visit, pins)
		}
	}
	if _, err := c.RecoverFrontendCheck(req); err != nil || store.saves != 1 {
		t.Fatal("recovery did not replay the one recorded grant", err)
	}
}

func TestSerialFrontendRecoveryRefusesToConsumeTheProtectedReserve(t *testing.T) {
	c, store, req := serialFrontendRecoveryFixture(t)
	r := c.runs[req.RunID]
	need, err := readyTail(r, r.Step)
	if err != nil {
		t.Fatal(err)
	}
	r.Process.MaxAttempts = len(r.Attempts) + need - 1
	c.runs[r.ID] = r
	if _, err = c.RecoverFrontendCheck(req); err == nil || store.saves != 0 {
		t.Fatal("recovery consumed the protected reserve", err)
	}
}

func plannedSerialFrontendFixture(t *testing.T) (*Core, *frontendRecheckStore, RecoverFrontendCheckRequest) {
	t.Helper()
	c, store, _, req := frontendRecheckFixture(t)
	r := c.runs[req.RunID]
	old := r.Process
	r.Process = Process{}
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "jacks-break", "planned-frontend.json"))
	if err != nil || json.Unmarshal(data, &r.Process) != nil {
		t.Fatal("load planned frontend definition", err)
	}
	for _, step := range r.Process.Steps {
		if step.ForEach != nil {
			continue
		}
		var role Role
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "jacks-break", "roles", step.Role+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("load planned frontend role", err)
		}
		r.Roles[step.Role] = role
	}
	owner, check := r.Attempts[len(r.Attempts)-1], r.Attempts[len(r.Attempts)-2]
	r.Attempts = r.Attempts[1 : len(r.Attempts)-2] // Fresh delivery has no source-adopt visit.
	for i := range r.Attempts {
		r.Attempts[i].Step = taskStepIndex(r.Process, old.Steps[r.Attempts[i].Step].ID)
	}
	r.Step = taskStepIndex(r.Process, "check")
	owner.Step, owner.Visit, owner.End.Result.Outcome = r.Step-1, 5, "ready"
	check.Step, check.Visit, check.Superseded = r.Step, 6, false
	check.Source.InputPatchID = owner.Source.PatchID
	r.Attempts = append(r.Attempts, owner, check)
	r.Visit, r.Reason = check.Visit, "repetition allowance exhausted: check/changes_requested"
	r.RouteUses = map[string]int{"check/changes_requested": 3}
	r.StepInputs = map[string]Value{"patch": {ArtifactID: owner.Source.PatchID}, "tests": r.Inputs["tests"]}
	req.AttemptID, req.Revision, req.Serial, req.PublicationHead = check.ID, r.Revision, true, ""
	c.runs[r.ID] = r
	for _, entry := range r.TaskGroups["cards"].Children {
		child := c.runs[entry.RunID]
		child.Process.Steps = []Step{{Role: "fixture"}}
		child.Roles = map[string]Role{"fixture": {Execution: &Profile{Harness: "deterministic"}}}
		c.runs[child.ID] = child
	}
	observed, err := c.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	req.Revision = observed.Revision
	return c, store, req
}

func TestPlannedSerialFrontendRecoveryPreservesCandidateCardsAndProtectedPath(t *testing.T) {
	c, store, req := plannedSerialFrontendFixture(t)
	before := Clone(c.runs[req.RunID])
	r, err := c.RecoverFrontendCheck(req)
	if err != nil {
		t.Fatal(err)
	}
	expectedCounters := Clone(before.RouteUses)
	expectedCounters["check/frontend_recheck"]++
	for label, preserved := range map[string]bool{
		"queued check visit": r.Status == "Queued" && r.Step == before.Step && r.Visit == before.Visit+1 && r.Decision == nil,
		"delivery process":   reflect.DeepEqual(r.Process, before.Process),
		"accepted cards":     reflect.DeepEqual(r.TaskGroups, before.TaskGroups),
		"route counters":     reflect.DeepEqual(r.RouteUses, expectedCounters),
		"attempt count":      len(r.Attempts) == len(before.Attempts),
		"saved failure":      reflect.DeepEqual(r.Attempts[len(r.Attempts)-1].End, before.Current().End) && r.Attempts[len(r.Attempts)-1].Superseded,
		"check inputs":       reflect.DeepEqual(r.StepInputs, before.StepInputs),
	} {
		if !preserved {
			t.Error("planned recheck changed", label)
		}
	}
	assignment, err := c.assignment(r, Attempt{Step: r.Step, Visit: r.Visit})
	if err != nil || assignment.FrontendCheckPins == nil || !assignment.FrontendCheckPins.Serial ||
		assignment.FrontendCheckPins.PatchSHA256 != req.PatchSHA256 || assignment.FrontendCheckPins.HelperSHA256 != req.HelperSHA256 {
		t.Fatal("planned recheck lost its qualified serial pins", err)
	}
	if _, err = c.RecoverFrontendCheck(req); err != nil || store.saves != 1 {
		t.Fatal("planned recovery did not replay the one recorded grant", err)
	}
}

func TestPlannedSerialFrontendRecoveryRejectsChangedOrUnsettledAuthority(t *testing.T) {
	for _, kind := range []string{"revision", "active-card", "card-pin", "selectors", "checked-patch", "publication-visit", "publication-head", "nonserial", "reserve"} {
		t.Run(kind, func(t *testing.T) {
			c, store, req := plannedSerialFrontendFixture(t)
			r := c.runs[req.RunID]
			childID := r.TaskGroups["cards"].Children[0].RunID
			child := c.runs[childID]
			switch kind {
			case "revision":
				req.Revision++
			case "active-card":
				child.Status = "Running"
				c.runs[childID] = child
			case "card-pin":
				r.TaskGroups["cards"].Children[0].Candidate.Commit = "changed"
			case "selectors":
				r.Inputs["tests"] = Text(`["src/different.test.ts"]`)
			case "checked-patch":
				r.Current().Source.InputPatchID = "attempt_join/changes.patch"
			case "publication-visit":
				r.Attempts[0].Step = taskStepIndex(r.Process, "publish")
			case "publication-head":
				req.PublicationHead = r.Source.Ref
			case "nonserial":
				req.Serial = false
			case "reserve":
				need, err := readyTail(r, r.Step)
				if err != nil {
					t.Fatal(err)
				}
				r.Process.MaxAttempts = c.familyAttempts(r) + need - 1
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverFrontendCheck(req); err == nil || store.saves != 0 {
				t.Fatal("planned recovery admitted changed authority", kind, err)
			}
		})
	}
}

func TestPlannedSerialFrontendRecoveryRechecksCardRevisionsAfterValidation(t *testing.T) {
	c, store, req := plannedSerialFrontendFixture(t)
	executor := c.executor.(*frontendRecheckExecutor)
	executor.validate = func(r Run, _ SourceResult, _ Profile, _, _ string) error {
		id := r.TaskGroups["cards"].Children[0].RunID
		child := c.runs[id]
		child.Revision++
		c.runs[id] = child
		return nil
	}
	if _, err := c.RecoverFrontendCheck(req); err == nil || store.saves != 0 {
		t.Fatal("planned recovery admitted card drift during validation", err)
	}
}

func TestSerialFrontendReviewReceivesOnlyMatchingAcceptedGateProvenance(t *testing.T) {
	for _, kind := range []string{"accepted", "wrong-patch", "wrong-report", "foreign-report", "rejected-check", "superseded", "before-grant"} {
		t.Run(kind, func(t *testing.T) {
			c, store, req := plannedSerialFrontendFixture(t)
			r, err := c.RecoverFrontendCheck(req)
			if err != nil {
				t.Fatal(err)
			}
			const id = "attempt_serial_pass"
			report := id + "/result"
			if err := c.disk.Write("artifacts/"+report, []byte("full accepted check report"), c.disk.Policy.MaxArtifact); err != nil {
				t.Fatal(err)
			}
			store.artifacts[report] = Artifact{ID: report, RunID: r.ID, ProjectID: r.ProjectID, Bytes: 26}
			passed := Attempt{
				ID: id, Step: r.Step, Visit: r.Visit, State: Finished, Released: true,
				Source: &SourceResult{InputPatchID: r.StepInputs["patch"].ArtifactID},
				End: &ExecutionEnd{Kind: "returned", Result: &StepResult{
					Outcome: "ready", Outputs: map[string]Value{"artifact": {ArtifactID: report}},
				}},
			}
			switch kind {
			case "wrong-patch":
				passed.Source.InputPatchID = "another/changes.patch"
			case "wrong-report":
				passed.End.Result.Outputs["artifact"] = Value{ArtifactID: "another/result"}
			case "foreign-report":
				other := store.artifacts[report]
				other.RunID = "run_other"
				store.artifacts[report] = other
			case "rejected-check":
				passed.End.Result.Outcome = "changes_requested"
			case "superseded":
				passed.Superseded = true
			case "before-grant":
				passed.Visit = r.FrontendCheckRecovery.CheckVisit - 1
			}
			r.Attempts = append(r.Attempts, passed)
			r.Step, r.Visit = taskStepIndex(r.Process, "review"), r.Visit+2
			r.StepInputs["checks"] = Value{ArtifactID: report}
			assignment, err := c.assignment(r, Attempt{Step: r.Step, Visit: r.Visit})
			if kind != "accepted" {
				if err == nil {
					t.Fatal("review received unsupported serial-gate authority", kind)
				}
				return
			}
			if err != nil || !strings.Contains(assignment.Task, req.RequestID) || !strings.Contains(assignment.Task, report) ||
				!strings.Contains(assignment.Task, "five-second limit") || !strings.Contains(assignment.Task, "protected approvals remain independent") {
				t.Fatal("review lost serial provenance or independent acceptance boundary", assignment.Task, err)
			}
		})
	}
}
