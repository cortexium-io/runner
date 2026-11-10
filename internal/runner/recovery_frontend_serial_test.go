package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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
