package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func frontendAssertionRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, *frontendRecheckExecutor, RecoverFrontendCheckRequest) {
	t.Helper()
	c, store, exec, timeout := exhaustedFrontendRecoveryFixture(t)
	r, err := c.RecoverFrontendTimeouts(timeout)
	if err != nil {
		t.Fatal(err)
	}
	finishFrontendDiagnosticFixture(t, c, &r, "changes_requested", exhaustedFrontendReportFixture(t, true, true, true))
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" {
		t.Fatal("fixture did not earn its single diagnostic repair", r.Status, r.Reason)
	}
	// Register the diagnostic result as the normal finish/persist boundary does.
	id := r.FrontendTimeoutRecovery.RepairEvidenceID
	n, err := c.disk.ArtifactSize(id)
	if err != nil {
		t.Fatal(err)
	}
	store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: n}
	var source SourceResult
	for _, old := range r.Attempts {
		if old.ID == r.FrontendTimeoutRecovery.OwnerAttemptID {
			source = *old.Source
		}
	}
	owner := Attempt{
		ID:       "attempt_diagnostic_repair",
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
	}
	source.AttemptID, source.InputPatchID, source.PatchID = owner.ID, r.FrontendTimeoutRecovery.PatchID, owner.ID+"/changes.patch"
	source.Tree = strings.Repeat("e", 40)
	owner.Source = &source
	patch := []byte("retained diagnostic repair")
	if err := c.disk.Write("artifacts/"+source.PatchID, patch, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	owner.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{
		"patch":   {ArtifactID: source.PatchID},
		"summary": Text("diagnostic repair retained"),
	}}}
	r.Attempts = append(r.Attempts, owner)
	for _, artifact := range c.finish(&r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "check" {
		t.Fatal("fixture repair did not return to the normal full check", r.Status, r.Reason)
	}
	check := Attempt{
		ID:       "attempt_assertion_check",
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
	}
	check.Source = &SourceResult{
		AttemptID:    check.ID,
		InputPatchID: source.PatchID,
		Repository:   source.Repository,
		WorkDir:      source.WorkDir,
		Base:         source.Base,
	}
	check.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested", Outputs: map[string]Value{
		"artifact": {ArtifactID: check.ID + "/result"},
		"check":    Text("CSS transition assertion still observes all"),
	}}}
	request := timeout.RecoverFrontendCheckRequest
	request.RequestID, request.AttemptID = "assertion-owner-grant", check.ID
	request.PatchSHA256 = fmt.Sprintf("%x", sha256.Sum256(patch))
	writeFrontendAssertionReport(t, c, store, &request, exhaustedFrontendReportFixture(t, false, true, false))
	r.Attempts = append(r.Attempts, check)
	for _, artifact := range c.finish(&r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
	if r.Status != "Failed" || r.Reason != "repetition allowance exhausted: check/changes_requested" {
		t.Fatal("fixture did not stop at the exhausted ordinary correction route", r.Status, r.Reason)
	}
	r.ResourcesReleased = true
	c.runs[r.ID] = r
	request.Revision = r.Revision
	for _, child := range r.TaskGroups["cards"].Children {
		request.Revision += c.runs[child.RunID].Revision
	}
	store.saves, exec.calls = 0, 0
	select {
	case <-c.wake:
	default:
	}
	return c, store, exec, request
}

func writeFrontendAssertionReport(t *testing.T, c *Core, store *frontendRecheckStore, request *RecoverFrontendCheckRequest, report map[string]any) {
	t.Helper()
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	id := request.AttemptID + "/result"
	if err := c.disk.Write("artifacts/"+id, raw, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	r := c.runs[request.RunID]
	store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(raw))}
	request.AssertionSHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
}

func TestRecoverFrontendAssertionUsesOneBoundRepairAndNormalCheck(t *testing.T) {
	c, store, exec, request := frontendAssertionRecoveryFixture(t)
	before := Clone(c.runs[request.RunID])
	r, err := c.RecoverFrontendCheck(request)
	if err != nil {
		t.Fatal(err)
	}
	grant := r.FrontendAssertionRecovery
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || r.Visit != before.Visit+1 || r.ResourcesReleased || r.Decision != nil || grant == nil ||
		grant.RequestID != request.RequestID || grant.CheckAttemptID != request.AttemptID || grant.CheckID != request.AttemptID+"/result" ||
		grant.CheckSHA256 != request.AssertionSHA256 || grant.PatchID != before.Attempts[len(before.Attempts)-2].Source.PatchID || grant.PatchSHA256 != request.PatchSHA256 ||
		grant.HelperSHA256 != request.HelperSHA256 || grant.PublicationHead != request.PublicationHead || grant.RepairVisit != r.Visit || grant.GrantedAt.IsZero() {
		t.Fatal("assertion recovery did not bind one owner visit to the current full report and retained patch", r.Status, r.Reason, grant)
	}
	if !reflect.DeepEqual(r.Process, before.Process) || !reflect.DeepEqual(r.Roles, before.Roles) || !reflect.DeepEqual(r.TaskGroups, before.TaskGroups) ||
		!reflect.DeepEqual(r.FrontendTimeoutRecovery, before.FrontendTimeoutRecovery) || r.FrontendCheckRecovery != nil ||
		c.familyRemaining(r) != c.familyRemaining(before) || r.RouteUses["check/changes_requested"] != 3 || r.RouteUses["check/frontend_timeout_repair"] != 1 || r.RouteUses["check/frontend_assertion_repair"] != 1 {
		t.Fatal("assertion recovery changed frozen work, accepted evidence or ordinary allowances")
	}
	for i, old := range before.Attempts {
		after := Clone(r.Attempts[i])
		after.Superseded = old.Superseded
		if !reflect.DeepEqual(after, old) {
			t.Fatal("assertion recovery rewrote an earlier attempt", old.ID)
		}
	}
	assignment, err := c.assignment(r, Attempt{ID: "attempt_css_owner", Step: r.Step, Visit: r.Visit, Deadline: time.Now().Add(time.Minute)})
	if err != nil || assignment.Inputs["checks"].ArtifactID != grant.CheckID || !strings.Contains(strings.ToLower(assignment.Task), "assertion") {
		t.Fatal("owner did not receive the complete current assertion evidence and focused task", err)
	}
	if replay, err := c.RecoverFrontendCheck(request); err != nil || !reflect.DeepEqual(replay, r) || store.saves != 1 || exec.calls != 1 {
		t.Fatal("assertion request did not replay idempotently", err, store.saves, exec.calls)
	}
	changed := request
	changed.AssertionSHA256 = strings.Repeat("f", 64)
	if _, err := c.RecoverFrontendCheck(changed); err == nil || store.saves != 1 || exec.calls != 1 {
		t.Fatal("same assertion request ID accepted changed evidence", err)
	}
	owner := Attempt{
		ID:       "attempt_css_owner",
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
	}
	owner.Source = Clone(before.Attempts[len(before.Attempts)-2].Source)
	owner.Source.AttemptID, owner.Source.InputPatchID, owner.Source.PatchID = owner.ID, grant.PatchID, owner.ID+"/changes.patch"
	freshPatch := []byte("focused CSS correction")
	if err := c.disk.Write("artifacts/"+owner.Source.PatchID, freshPatch, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	owner.End = &ExecutionEnd{
		Kind: "returned",
		Result: &StepResult{
			Outcome: "ready",
			Outputs: map[string]Value{
				"patch":   {ArtifactID: owner.Source.PatchID},
				"summary": Text("CSS correction"),
			},
		},
	}
	r.Attempts = append(r.Attempts, owner)
	for _, artifact := range c.finish(&r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "check" || r.Decision != nil || r.Visit != grant.RepairVisit+1 {
		t.Fatal("assertion correction skipped its fresh normal check", r.Status, r.Reason)
	}
	assignment, err = c.assignment(r, Attempt{ID: "attempt_css_full_check", Step: r.Step, Visit: r.Visit})
	if err != nil || assignment.FrontendCheckPins == nil || assignment.FrontendCheckPins.HelperSHA256 != request.HelperSHA256 || assignment.FrontendCheckPins.DiagnoseTimeouts ||
		assignment.FrontendCheckPins.Serial || assignment.FrontendCheckPins.PatchSHA256 != fmt.Sprintf("%x", sha256.Sum256(freshPatch)) || assignment.Inputs["patch"].ArtifactID != owner.Source.PatchID {
		t.Fatal("fresh normal check did not bind its qualified helper and newly captured patch", err, assignment.FrontendCheckPins)
	}
	for _, missing := range []Value{{}, {ArtifactID: "attempt_missing/changes.patch"}} {
		changed := Clone(r)
		changed.StepInputs["patch"] = missing
		if _, err := c.assignment(changed, Attempt{ID: "attempt_css_full_check", Step: changed.Step, Visit: changed.Visit}); err == nil {
			t.Fatal("fresh check accepted an absent or unreadable current patch")
		}
	}
	check := Attempt{
		ID:       "attempt_css_full_check",
		Step:     r.Step,
		Visit:    r.Visit,
		State:    Finished,
		Released: true,
		End: &ExecutionEnd{
			Kind: "returned",
			Result: &StepResult{
				Outcome: "changes_requested",
				Outputs: map[string]Value{
					"artifact": {ArtifactID: "attempt_css_full_check/result"},
					"check":    Text("still fails"),
				},
			},
		},
	}
	if err := c.disk.Write("artifacts/attempt_css_full_check/result", []byte("fresh full check still failed"), c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	r.Attempts = append(r.Attempts, check)
	c.finish(&r, len(r.Attempts)-1)
	if r.Status != "Failed" || r.Reason != "repetition allowance exhausted: check/changes_requested" || r.RouteUses["check/frontend_assertion_repair"] != 1 || r.RouteUses["check/changes_requested"] != 3 {
		t.Fatal("assertion recovery granted another correction or fake acceptance", r.Status, r.Reason)
	}
}

func TestRecoverFrontendAssertionRejectsUnboundOrIncompleteEvidence(t *testing.T) {
	faults := []string{
		"wrong attempt",
		"wrong report",
		"wrong patch",
		"absent diagnostic",
		"unspent diagnostic",
		"stale repair",
		"second assertion grant",
		"different repair input",
		"runtime error",
		"timeout",
		"multiple assertions",
		"coverage drift",
		"compiler drift",
		"protected budget",
	}
	for _, fault := range faults {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, request := frontendAssertionRecoveryFixture(t)
			r := Clone(c.runs[request.RunID])
			report := exhaustedFrontendReportFixture(t, false, true, false)
			writeReport := false
			switch fault {
			case "wrong attempt":
				request.AttemptID = "attempt_other"
			case "wrong report":
				request.AssertionSHA256 = strings.Repeat("f", 64)
			case "wrong patch":
				request.PatchSHA256 = strings.Repeat("f", 64)
			case "absent diagnostic":
				r.FrontendTimeoutRecovery = nil
			case "unspent diagnostic":
				r.FrontendTimeoutRecovery.RepairVisit = 0
			case "stale repair":
				r.FrontendTimeoutRecovery.RepairVisit--
			case "second assertion grant":
				r.FrontendAssertionRecovery = &FrontendAssertionRecovery{RequestID: "already used"}
			case "different repair input":
				r.Attempts[len(r.Attempts)-2].Source.InputPatchID = "attempt_join/changes.patch"
			case "runtime error":
				assertion := report["vitest"].(map[string]any)["testResults"].([]map[string]any)[1]["assertionResults"].([]map[string]any)[0]
				assertion["failureMessages"] = []string{"TypeError: unexpected runtime failure"}
				writeReport = true
			case "timeout":
				report = exhaustedFrontendReportFixture(t, true, false, false)
				writeReport = true
			case "multiple assertions":
				assertion := report["vitest"].(map[string]any)["testResults"].([]map[string]any)[2]["assertionResults"].([]map[string]any)[0]
				assertion["status"], assertion["duration"], assertion["failureMessages"] = "failed", 2.3, []string{"AssertionError: another failure"}
				report["failed"] = 2
				v := report["vitest"].(map[string]any)
				v["numFailedTests"], v["numPassedTests"] = 2, 212
				writeReport = true
			case "coverage drift":
				assertion := report["vitest"].(map[string]any)["testResults"].([]map[string]any)[2]["assertionResults"].([]map[string]any)[0]
				assertion["fullName"] = "different unchecked coverage"
				writeReport = true
			case "compiler drift":
				report["typecheck"].(map[string]any)["version"] = "other-compiler"
				writeReport = true
			case "protected budget":
				need, err := readyTail(r, r.Step)
				if err != nil {
					t.Fatal(err)
				}
				r.Process.MaxAttempts = c.familyAttempts(r) + need
			}
			if writeReport {
				writeFrontendAssertionReport(t, c, store, &request, report)
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverFrontendCheck(request); err == nil || store.saves != 0 || exec.calls != 0 || !reflect.DeepEqual(c.runs[r.ID], r) || len(c.wake) != 0 {
				t.Fatal("unsafe assertion recovery mutated or dispatched work", err, store.saves, exec.calls)
			}
		})
	}
}

func TestFrontendAssertionDispatchRechecksEvidenceDigests(t *testing.T) {
	for _, changed := range []string{"report", "patch"} {
		t.Run(changed, func(t *testing.T) {
			c, _, _, request := frontendAssertionRecoveryFixture(t)
			r, err := c.RecoverFrontendCheck(request)
			if err != nil {
				t.Fatal(err)
			}
			id := r.FrontendAssertionRecovery.CheckID
			if changed == "patch" {
				id = r.FrontendAssertionRecovery.PatchID
			}
			if err := c.disk.Write("artifacts/"+id, []byte("changed evidence"), c.disk.Policy.MaxArtifact); err != nil {
				t.Fatal(err)
			}
			if _, err := c.assignment(r, Attempt{ID: "attempt_css_owner", Step: r.Step, Visit: r.Visit}); err == nil {
				t.Fatal("owner dispatch accepted changed assertion evidence")
			}
		})
	}
}
