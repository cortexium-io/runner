package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
)

func TestFrontendTimeoutActualReportPreflight(t *testing.T) {
	path, selection := os.Getenv("RUNNER_FRONTEND_TIMEOUT_REPORT"), os.Getenv("RUNNER_FRONTEND_TIMEOUT_TESTS")
	if path == "" && selection == "" {
		t.Skip("opt-in retained frontend report parser preflight")
	}
	if path == "" || selection == "" {
		t.Fatal("both report filename and JSON test selection are required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		t.Fatal("retained report could not be read within the existing artifact bound", err)
	}
	var report frontendTimeoutReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	executed, timeouts, err := report.execution(Text(selection))
	if err != nil || len(timeouts) < 1 || len(timeouts) > 8 || report.Failed <= len(timeouts) {
		t.Fatal("retained report does not establish complete mixed assertion/timeout execution", err)
	}
	count := 0
	for _, n := range executed {
		count += n
	}
	t.Logf("validated executed=%d failures=%d timeouts=%d", count, report.Failed, len(timeouts))
}

func exhaustedFrontendReportFixture(t *testing.T, timeout, assertion, diagnose bool) map[string]any {
	t.Helper()
	fails := 0
	if timeout {
		fails = 1
	}
	p := timeoutReportFixture(t, fails, diagnose && timeout)
	files := []string{"src/player.test.ts", "src/hashi.test.ts", "src/new.test.ts", "src/setup.test.ts", "src/kakuro.test.ts", "src/shikaku.test.ts"}
	old := p["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]["assertionResults"].([]map[string]any)
	suites, identities := make([]map[string]any, len(files)), make([]map[string]any, len(files))
	for i, file := range files {
		path := "/owned/scratch/frontend/" + file
		suites[i] = map[string]any{"name": path, "assertionResults": []map[string]any{}}
		identities[i] = map[string]any{"selected": file, "reported": []string{path}}
	}
	for i := range 214 {
		a := map[string]any{"fullName": fmt.Sprintf("checks board geometry %d", i), "status": "passed"}
		if i == 18 || i == 24 || i == 30 {
			// Match parameterized Vitest output with four passed occurrences
			// sharing the same fullName in one selected file.
			a["fullName"] = "checks board geometry 12"
		}
		if i == 0 && timeout {
			a = old[0]
		}
		if i == 1 && assertion {
			a["status"], a["duration"], a["failureMessages"] = "failed", 3.4, []string{"AssertionError: expected transition duration to equal 0\n at the selected source assertion"}
		}
		suite := suites[i%len(files)]
		suite["assertionResults"] = append(suite["assertionResults"].([]map[string]any), a)
	}
	if timeout {
		p["vitest_diagnostics"].(map[string]any)["failures"].([]map[string]any)[0]["file"] = files[0]
		if diagnose {
			p["frontend_timeout_diagnostics"].(map[string]any)["cases"].([]map[string]any)[0]["file"] = files[0]
		}
	}
	if assertion {
		fails++
	}
	status, exit := "passed", 0
	if fails > 0 {
		status, exit = "failed", 1
	}
	p["tests"], p["files"], p["executed"], p["failed"], p["status"], p["exit_code"] = files, identities, 214, fails, status, exit
	v := p["vitest"].(map[string]any)
	v["testResults"], v["numTotalTests"], v["numPassedTests"], v["numFailedTests"], v["success"] = suites, 214, 214-fails, fails, fails == 0
	return p
}

func exhaustedFrontendRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, *frontendRecheckExecutor, RecoverFrontendTimeoutsRequest) {
	t.Helper()
	c, store, exec, request := frontendRecheckFixture(t)
	r := Clone(c.runs[request.RunID])
	r.Process.MaxAttempts = 42
	r.Inputs["package"], r.Inputs["benchmark"] = Text("./..."), Text("none")
	r.Attempts[5].End.Result.Outputs["feedback"] = Text("")
	r.RouteUses["check/changes_requested"] = 3
	owner := r.Current()
	owner.End.Result.Outcome, owner.End.Result.Reason = "ready", ""
	check := Clone(r.Attempts[len(r.Attempts)-2])
	check.ID, check.Visit, check.Superseded = "attempt_exhausted_check", owner.Visit+1, false
	check.Source.AttemptID, check.Source.InputPatchID = check.ID, owner.Source.PatchID
	proof := exhaustedFrontendReportFixture(t, true, true, false)
	selection, _ := json.Marshal(proof["tests"])
	r.Inputs["tests"] = Text(string(selection))
	raw, _ := json.Marshal(proof)
	id := check.ID + "/result"
	if err := c.disk.Write("artifacts/"+id, raw, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(raw))}
	check.End.Result.Outputs["artifact"] = Value{ArtifactID: id}
	r.Step, r.Visit = check.Step, check.Visit
	r.StepInputs = map[string]Value{"patch": {ArtifactID: owner.Source.PatchID}, "tests": r.Inputs["tests"]}
	r.Attempts = append(r.Attempts, check)
	r.Reason = "repetition allowance exhausted: check/changes_requested"
	c.runs[r.ID] = r
	request.RequestID, request.AttemptID = "exhausted-check-diagnostic", check.ID
	return c, store, exec, RecoverFrontendTimeoutsRequest{RecoverFrontendCheckRequest: request, CheckAttemptID: check.ID, CheckSHA256: fmt.Sprintf("%x", sha256.Sum256(raw))}
}

func TestRecoverFrontendTimeoutsRechecksExactExhaustedMixedCheck(t *testing.T) {
	c, store, exec, req := exhaustedFrontendRecoveryFixture(t)
	before := Clone(c.runs[req.RunID])
	r, err := c.RecoverFrontendTimeouts(req)
	if err != nil {
		t.Fatal(err)
	}
	grant := r.FrontendTimeoutRecovery
	owner := before.Attempts[len(before.Attempts)-2]
	if r.Status != "Queued" || r.Step != before.Step || r.Visit != before.Visit+1 || grant == nil || grant.OwnerAttemptID != owner.ID || grant.PatchID != owner.Source.PatchID ||
		grant.PriorCheckAttemptID != req.AttemptID || r.FrontendCheckRecovery != nil || !reflect.DeepEqual(r.Attempts, before.Attempts) ||
		!reflect.DeepEqual(r.Process, before.Process) || !reflect.DeepEqual(r.Roles, before.Roles) || !reflect.DeepEqual(r.TaskGroups, before.TaskGroups) ||
		r.RouteUses["check/changes_requested"] != 3 || c.familyRemaining(r) != c.familyRemaining(before) {
		t.Fatal("exhausted-check recovery changed frozen work or bound the failed checker as owner", r.Status, grant)
	}
	if replay, err := c.RecoverFrontendTimeouts(req); err != nil || !reflect.DeepEqual(replay, r) || exec.calls != 1 || store.saves != 1 {
		t.Fatal("exhausted-check handoff did not replay idempotently", err, exec.calls, store.saves)
	}
	finishFrontendDiagnosticFixture(t, c, &r, "changes_requested", exhaustedFrontendReportFixture(t, true, true, true))
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || grant.RepairVisit != r.Visit || r.RouteUses["check/frontend_timeout_repair"] != 1 || r.RouteUses["check/changes_requested"] != 3 || r.Decision != nil {
		t.Fatal("complete mixed full check and timeout profile did not earn the distinct repair", r.Status, r.Reason)
	}
}

func TestExhaustedFrontendDiagnosticHandlesAssertionOnlyAndFullPass(t *testing.T) {
	for _, assertion := range []bool{false, true} {
		t.Run(fmt.Sprint(assertion), func(t *testing.T) {
			c, _, _, req := exhaustedFrontendRecoveryFixture(t)
			r, err := c.RecoverFrontendTimeouts(req)
			if err != nil {
				t.Fatal(err)
			}
			outcome, next := "ready", "measure"
			if assertion {
				outcome, next = "changes_requested", "owner"
			}
			finishFrontendDiagnosticFixture(t, c, &r, outcome, exhaustedFrontendReportFixture(t, false, assertion, false))
			if r.Status != "Queued" || r.Process.Steps[r.Step].ID != next || (r.FrontendTimeoutRecovery.RepairVisit != 0) != assertion {
				t.Fatal("conclusive full execution required a fabricated timeout profile or misrouted", r.Status, r.Reason)
			}
		})
	}
}

func TestRecoverExhaustedFrontendTimeoutsRejectsChangedAuthority(t *testing.T) {
	for _, fault := range []string{"remaining route", "wrong attempt", "wrong report", "ready owner missing", "wrong check source", "revision", "card mutation", "release gate", "budget"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, req := exhaustedFrontendRecoveryFixture(t)
			r := c.runs[req.RunID]
			switch fault {
			case "remaining route":
				r.RouteUses["check/changes_requested"]--
			case "wrong attempt":
				req.CheckAttemptID = "attempt_other"
			case "wrong report":
				req.CheckSHA256 = req.HelperSHA256
			case "ready owner missing":
				r.Attempts[len(r.Attempts)-2].End.Result.Outcome = "blocked"
			case "wrong check source":
				r.Current().Source.InputPatchID = "another/changes.patch"
			case "revision":
				req.Revision++
			case "card mutation":
				child := c.runs[r.TaskGroups["cards"].Children[0].RunID]
				child.Status = "Failed"
				c.runs[child.ID] = child
			case "release gate":
				r.Process.Steps[taskStepIndex(r.Process, "prepare")].Routes["ready"] = Route{Next: "merge"}
			case "budget":
				r.Process.MaxAttempts = len(r.Attempts) + 12
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverFrontendTimeouts(req); err == nil || store.saves != 0 || exec.calls != 0 {
				t.Fatal("unbound or unbudgeted recovery changed the run", err, store.saves, exec.calls)
			}
		})
	}
}

func TestExhaustedFrontendDiagnosticRejectsIncompleteOrRuntimeEvidence(t *testing.T) {
	for _, fault := range []string{"missing assertion", "duplicate count drift", "failed duplicate", "wrong compiler", "runtime error", "missing profile", "stale profile", "false full pass"} {
		t.Run(fault, func(t *testing.T) {
			c, _, _, req := exhaustedFrontendRecoveryFixture(t)
			r, err := c.RecoverFrontendTimeouts(req)
			if err != nil {
				t.Fatal(err)
			}
			proof := exhaustedFrontendReportFixture(t, true, true, true)
			outcome := "changes_requested"
			switch fault {
			case "missing assertion":
				suite := proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]
				suite["assertionResults"] = suite["assertionResults"].([]map[string]any)[1:]
			case "duplicate count drift":
				suite := proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]
				suite["assertionResults"].([]map[string]any)[3]["fullName"] = "replacement parameterized case"
			case "failed duplicate":
				suite := proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[0]
				suite["assertionResults"].([]map[string]any)[2]["fullName"] = "checks board geometry 0"
			case "wrong compiler":
				proof["typecheck"].(map[string]any)["version"] = "other"
			case "runtime error":
				suite := proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[1]
				suite["assertionResults"].([]map[string]any)[0]["failureMessages"] = []string{"Error: worker connection closed"}
			case "missing profile":
				delete(proof, "frontend_timeout_diagnostics")
			case "stale profile":
				p := exhaustedFrontendReportFixture(t, false, true, false)
				p["frontend_timeout_diagnostics"] = proof["frontend_timeout_diagnostics"]
				proof = p
			case "false full pass":
				outcome = "ready"
			}
			finishFrontendDiagnosticFixture(t, c, &r, outcome, proof)
			if r.Status != "Failed" || r.FrontendTimeoutRecovery.RepairVisit != 0 || r.RouteUses["check/frontend_timeout_repair"] != 0 {
				t.Fatal("incomplete or nonconclusive evidence spent repair authority", r.Status, r.Reason)
			}
		})
	}
}

func TestMixedFrontendReportAuthenticatesRestoredAssertionSidecar(t *testing.T) {
	for _, wrongDuration := range []bool{false, true} {
		proof := exhaustedFrontendReportFixture(t, true, true, true)
		assertion := proof["vitest"].(map[string]any)["testResults"].([]map[string]any)[1]["assertionResults"].([]map[string]any)[0]
		message := assertion["failureMessages"].([]string)[0]
		assertion["failureMessages"] = []string{"Error: STACK_TRACE_ERROR\n at Vitest placeholder"}
		duration := 3.4
		if wrongDuration {
			duration++
		}
		diagnostics := proof["vitest_diagnostics"].(map[string]any)
		diagnostics["failures"] = append(diagnostics["failures"].([]map[string]any), map[string]any{
			"file": "src/hashi.test.ts", "full_name": assertion["fullName"], "duration_ms": duration, "error_index": 0, "message": message,
		})
		raw, _ := json.Marshal(proof)
		var report frontendTimeoutReport
		if err := json.Unmarshal(raw, &report); err != nil {
			t.Fatal(err)
		}
		selection, _ := json.Marshal(proof["tests"])
		executed, timeouts, err := report.execution(Text(string(selection)))
		if wrongDuration && err == nil || !wrongDuration && (err != nil || len(executed) != 211 || executed[frontendTimeoutCaseKey{"src/player.test.ts", "checks board geometry 12"}] != 4 || len(timeouts) != 1) {
			t.Fatal("restored assertion identity/timing was not authenticated alongside timeout subset", err)
		}
	}
}
