package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func timeoutReportFixture(t *testing.T, fails int, diagnose bool) map[string]any {
	t.Helper()
	file := "src/board.test.ts"
	path := "/owned/scratch/frontend/" + file
	var assertions, restored, cases []map[string]any
	for i := range 8 {
		name := fmt.Sprintf("checks board geometry %d", i)
		a := map[string]any{"fullName": name, "status": "passed"}
		if fails&(1<<i) != 0 {
			duration := float64(5100 + i)
			a["status"], a["duration"], a["failureMessages"] = "failed", duration, []string{"Error: STACK_TRACE_ERROR\n    at task"}
			restored = append(restored, map[string]any{"file": file, "full_name": name, "duration_ms": duration, "error_index": 0, "message": "Test timed out in 5000ms."})
			cases = append(cases, map[string]any{
				"file": file, "full_name": name, "timeout_ms": 5000, "original_duration_ms": duration, "diagnostic_duration_ms": 1500, "status": "isolated_pass",
				"profiles": []map[string]any{
					{"process": "supervisor", "samples": 5, "idle_samples": 5},
					{"process": "worker", "samples": 5, "project_samples": 1, "stacks": []map[string]any{
						{"samples": 1, "frames": []map[string]any{{"kind": "project", "file": "frontend/src/board.ts", "line": 42}}},
					}},
				},
			})
		}
		assertions = append(assertions, a)
	}
	failed := len(restored)
	status, exit := "passed", 0
	if failed > 0 {
		status, exit = "failed", 1
	}
	p := map[string]any{
		"schema": 2, "status": status, "tests": []string{file}, "executed": len(assertions), "failed": failed, "skipped": 0, "exit_code": exit,
		"typecheck": map[string]any{
			"status": "passed", "complete": true, "exit_code": 0, "node": "/owned/node", "tool": "/owned/modules/vue-tsc/bin/vue-tsc.js",
			"tool_sha256": strings.Repeat("a", 64), "version": "3.3.11", "typescript_version": "6.0.3",
			"project": "tsconfig.json", "project_sha256": strings.Repeat("b", 64),
		},
		"files": []map[string]any{{"selected": file, "reported": []string{path}}},
		"vitest": map[string]any{"numTotalTests": len(assertions), "numPassedTests": len(assertions) - failed, "numFailedTests": failed, "success": failed == 0,
			"testResults": []map[string]any{{"name": path, "assertionResults": assertions}},
		},
		"vitest_diagnostics": map[string]any{"failures": restored},
	}
	if diagnose {
		p["frontend_timeout_diagnostics"] = map[string]any{"schema": 1, "status": "complete", "method": "node-v8-cpu-sampling-isolated-case-v1", "cases": cases}
	}
	return p
}

func frontendTimeoutRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, *frontendRecheckExecutor, RecoverFrontendTimeoutsRequest) {
	t.Helper()
	c, store, exec, request := frontendRecheckFixture(t)
	r := Clone(c.runs[request.RunID])
	r.Process.MaxAttempts = 43
	r.Inputs["package"], r.Inputs["benchmark"] = Text("./..."), Text("none")
	r.RouteUses["owner/frontend_recheck"], r.RouteUses["check/changes_requested"] = 1, 3
	r.Attempts[5].End.Result.Outputs["feedback"] = Text("")
	old := r.Current()
	old.Superseded = true
	r.FrontendCheckRecovery = &FrontendCheckRecovery{
		RequestID: "original-owner-grant", AttemptID: old.ID, CheckStep: "check", CheckVisit: 8,
		PatchID: old.Source.PatchID, PatchSHA256: request.PatchSHA256, HelperSHA256: request.HelperSHA256,
		PublicationHead: request.PublicationHead,
	}
	write := func(id string, bytes []byte) Value {
		if err := c.disk.Write("artifacts/"+id, bytes, c.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(bytes))}
		return Value{ArtifactID: id}
	}
	check := Clone(r.Attempts[6])
	check.ID, check.Visit, check.Source.AttemptID, check.Source.InputPatchID = "attempt_first_recheck", 8, "attempt_first_recheck", old.Source.PatchID
	check.End.Result.Outputs["artifact"] = write(check.ID+"/result", []byte("initial recovery check failure"))
	r.Attempts = append(r.Attempts, check)
	checked := Clone(*old)
	checked.ID, checked.Visit, checked.Source.AttemptID = "attempt_checked_owner", 9, "attempt_checked_owner"
	checked.Source.InputPatchID, checked.Source.PatchID = old.Source.PatchID, checked.ID+"/changes.patch"
	checked.Source.Tree = strings.Repeat("f", 40)
	checked.End.Result = &StepResult{Outcome: "ready", Outputs: map[string]Value{"summary": Text("ready"), "patch": write(checked.Source.PatchID, []byte("checked candidate"))}}
	r.Attempts = append(r.Attempts, checked)
	prior := Clone(check)
	prior.ID, prior.Visit, prior.Source.AttemptID, prior.Source.InputPatchID = "attempt_timeout_check", 10, "attempt_timeout_check", checked.Source.PatchID
	previous, _ := json.Marshal(timeoutReportFixture(t, 127, false))
	prior.End.Result.Outputs["artifact"] = write(prior.ID+"/result", previous)
	r.Attempts = append(r.Attempts, prior)
	owner := Clone(*old)
	owner.ID, owner.Visit, owner.Source.AttemptID, owner.Superseded = "attempt_blocked_owner", 11, "attempt_blocked_owner", false
	owner.Source.InputPatchID, owner.Source.PatchID, owner.Source.Tree = checked.Source.PatchID, owner.ID+"/changes.patch", checked.Source.Tree
	patch := []byte("current unchanged candidate")
	owner.End.Result.Outputs["patch"] = write(owner.Source.PatchID, patch)
	r.Attempts = append(r.Attempts, owner)
	r.Visit, r.StepInputs["checks"] = owner.Visit, prior.End.Result.Outputs["artifact"]
	c.runs[r.ID] = r
	request.RequestID, request.AttemptID = "timeout-owner-grant", owner.ID
	request.HelperSHA256, request.PatchSHA256 = strings.Repeat("d", 64), fmt.Sprintf("%x", sha256.Sum256(patch))
	return c, store, exec, RecoverFrontendTimeoutsRequest{RecoverFrontendCheckRequest: request, CheckAttemptID: prior.ID, CheckSHA256: fmt.Sprintf("%x", sha256.Sum256(previous))}
}

func finishFrontendDiagnosticFixture(t *testing.T, c *Core, r *Run, outcome string, proof map[string]any) {
	t.Helper()
	var owner *SourceResult
	for _, a := range r.Attempts {
		if a.ID == r.FrontendTimeoutRecovery.OwnerAttemptID {
			owner = a.Source
		}
	}
	if owner == nil {
		t.Fatal("diagnostic fixture lacks its authorized owner capture")
	}
	a := Attempt{ID: "attempt_diagnostic", Step: r.Step, Visit: r.Visit, State: Finished, Released: true}
	a.Source = &SourceResult{AttemptID: a.ID, InputPatchID: owner.PatchID, Repository: owner.Repository, Base: owner.Base, WorkDir: owner.WorkDir}
	if outcome == "failed" {
		a.End = &ExecutionEnd{Kind: "failed", Reason: "fixture runtime failure", Retryable: true}
	} else {
		id := a.ID + "/result"
		raw, _ := json.Marshal(proof)
		if err := c.disk.Write("artifacts/"+id, raw, c.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		a.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: outcome, Outputs: map[string]Value{"artifact": {ArtifactID: id}, "check": Text("diagnostic receipt")}}}
	}
	r.Attempts = append(r.Attempts, a)
	c.finish(r, len(r.Attempts)-1)
}

func TestRecoverFrontendTimeoutsUsesOneAuditedRepairWithinFrozenBudget(t *testing.T) {
	c, store, exec, req := frontendTimeoutRecoveryFixture(t)
	before := Clone(c.runs[req.RunID])
	r, err := c.RecoverFrontendTimeouts(req)
	if err != nil {
		t.Fatal(err)
	}
	grant := r.FrontendTimeoutRecovery
	if r.Status != "Queued" || grant == nil || grant.CheckVisit != r.Visit || grant.RepairVisit != 0 || grant.PriorCheckAttemptID != req.CheckAttemptID || grant.PriorCheckSHA256 != req.CheckSHA256 ||
		!reflect.DeepEqual(r.FrontendCheckRecovery, before.FrontendCheckRecovery) || !reflect.DeepEqual(r.Attempts, before.Attempts) || !reflect.DeepEqual(r.TaskGroups, before.TaskGroups) || c.familyRemaining(r) != 19 {
		t.Fatal("diagnostic grant changed saved work, old grant or frozen budget")
	}
	if replay, err := c.RecoverFrontendTimeouts(req); err != nil || !reflect.DeepEqual(replay, r) || store.saves != 1 || exec.calls != 1 {
		t.Fatal("diagnostic grant did not replay idempotently", err, store.saves, exec.calls)
	}
	// A different assertion times out within the same frozen selection. The old
	// seven still execute and the isolated comparison has corroborated samples.
	finishFrontendDiagnosticFixture(t, c, &r, "changes_requested", timeoutReportFixture(t, 128, true))
	grant = r.FrontendTimeoutRecovery
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || grant.RepairVisit != r.Visit || r.StepInputs["checks"].ArtifactID != grant.RepairEvidenceID || !frontendRecheckHex(grant.RepairEvidenceSHA, 64) ||
		r.RouteUses["check/frontend_timeout_repair"] != 1 || r.RouteUses["check/changes_requested"] != 3 || c.familyRemaining(r) != 18 || r.Decision != nil {
		t.Fatal("complete diagnostic did not queue the one distinct repair", r.Status, r.Reason, grant)
	}
	owner := Attempt{ID: "attempt_repair", Step: r.Step, Visit: r.Visit, State: Finished, Released: true}
	owner.Source = &SourceResult{AttemptID: owner.ID, PatchID: owner.ID + "/changes.patch"}
	owner.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"patch": {ArtifactID: owner.Source.PatchID}, "summary": Text("repair")}}}
	if err := c.disk.Write("artifacts/"+owner.Source.PatchID, []byte("repair"), c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	r.Attempts = append(r.Attempts, owner)
	c.finish(&r, len(r.Attempts)-1)
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "check" || r.Visit == grant.CheckVisit || c.familyRemaining(r) != 17 {
		t.Fatal("repair did not return to ordinary configured check", r.Status, r.Reason)
	}
	finishFrontendDiagnosticFixture(t, c, &r, "changes_requested", timeoutReportFixture(t, 128, true))
	if r.Status != "Failed" || r.Reason != "repetition allowance exhausted: check/changes_requested" || r.RouteUses["check/frontend_timeout_repair"] != 1 || r.RouteUses["check/changes_requested"] != 3 {
		t.Fatal("further rejection unexpectedly repeated repair", r.Status, r.Reason)
	}
}

func TestRecoverFrontendTimeoutsRejectsUnboundEvidenceAndInsufficientReserve(t *testing.T) {
	for _, fault := range []string{"budget", "wrong report", "source tree drift", "normal allowance remains", "first grant unconsumed", "second grant"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, req := frontendTimeoutRecoveryFixture(t)
			r := c.runs[req.RunID]
			switch fault {
			case "budget":
				r.Process.MaxAttempts--
			case "wrong report":
				req.CheckSHA256 = strings.Repeat("f", 64)
			case "source tree drift":
				r.Current().Source.Tree = strings.Repeat("a", 40)
			case "normal allowance remains":
				r.RouteUses["check/changes_requested"]--
			case "first grant unconsumed":
				r.FrontendCheckRecovery.CheckVisit = 7
			case "second grant":
				r.FrontendTimeoutRecovery = &FrontendTimeoutRecovery{CheckVisit: 12}
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverFrontendTimeouts(req); err == nil || store.saves != 0 || exec.calls != 0 || len(c.wake) != 0 {
				t.Fatal("ineligible diagnostic recovery changed the saved run", err, store.saves, exec.calls)
			}
		})
	}
}

func TestLegacyFrontendTimeoutRecoveryAcceptsOneThroughEightTimeouts(t *testing.T) {
	for _, mask := range []int{1, 255} {
		c, store, _, req := frontendTimeoutRecoveryFixture(t)
		r := c.runs[req.RunID]
		raw, _ := json.Marshal(timeoutReportFixture(t, mask, false))
		id := req.CheckAttemptID + "/result"
		if err := c.disk.Write("artifacts/"+id, raw, c.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(raw))}
		req.CheckSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
		if _, err := c.RecoverFrontendTimeouts(req); err != nil {
			t.Fatal("legacy recovery still required exactly seven failures", mask, err)
		}
	}
}

func TestFrontendTimeoutDiagnosticOnlyAdvancesCompleteOwnedProof(t *testing.T) {
	for _, scenario := range []string{"full pass", "missing old assertion", "changed prior passing assertion", "mismatched timing", "no profile", "no worker samples", "retryable runtime failure"} {
		t.Run(scenario, func(t *testing.T) {
			c, _, _, req := frontendTimeoutRecoveryFixture(t)
			r, err := c.RecoverFrontendTimeouts(req)
			if err != nil {
				t.Fatal(err)
			}
			outcome, proof := "changes_requested", timeoutReportFixture(t, 128, true)
			switch scenario {
			case "full pass":
				outcome, proof = "ready", timeoutReportFixture(t, 0, false)
			case "missing old assertion":
				outcome, proof = "ready", timeoutReportFixture(t, 0, false)
				proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]["assertionResults"].([]map[string]any)[0]["fullName"] = "replacement"
			case "changed prior passing assertion":
				outcome, proof = "ready", timeoutReportFixture(t, 0, false)
				proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]["assertionResults"].([]map[string]any)[7]["fullName"] = "replacement"
			case "mismatched timing":
				proof["frontend_timeout_diagnostics"].(map[string]any)["cases"].([]map[string]any)[0]["original_duration_ms"] = 9999
			case "no profile":
				delete(proof, "frontend_timeout_diagnostics")
			case "no worker samples":
				entry := proof["frontend_timeout_diagnostics"].(map[string]any)["cases"].([]map[string]any)[0]
				entry["profiles"] = entry["profiles"].([]map[string]any)[:1]
			case "retryable runtime failure":
				outcome = "failed"
			}
			finishFrontendDiagnosticFixture(t, c, &r, outcome, proof)
			if scenario == "full pass" {
				if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.FrontendTimeoutRecovery.RepairVisit != 0 {
					t.Fatal("full uninstrumented pass did not follow normal route", r.Status, r.Reason)
				}
			} else if r.Status != "Failed" || r.FrontendTimeoutRecovery.RepairVisit != 0 || r.RouteUses["check/frontend_timeout_repair"] != 0 {
				t.Fatal("incomplete proof spent repair authority or repeated diagnostic", r.Status, r.Reason)
			}
		})
	}
}
