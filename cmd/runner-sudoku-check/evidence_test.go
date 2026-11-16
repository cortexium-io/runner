package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBrowserPartialEvidenceRetainsDiagnosticsWithoutAcceptance(t *testing.T) {
	data := []byte(`{
  "schema":1,"status":"failed","complete":false,"browser_closed":false,
  "failure":"collection interrupted",
  "prerequisites":[{"engine":"webkit","id":"public-code-reopen","status":"blocked","failure":"public reopen: http200 cells0\nlocator.waitFor: timeout\nCall log: waiting for admitted board"}],
  "journeys":[{"engine":"webkit","id":"normal-ui-admission","status":"passed"}],
  "unrun":[{"engine":"webkit","blocked_by":"public-code-reopen","scope":"Sudoku matrix","journeys":["entry-pin-cancel-history"]}],
  "errors":[{"message":"first concrete runtime failure"}]
}`)
	var p report
	if err := recordBrowserReport(&p, data, errors.New("owned browser command interrupted")); err != nil {
		t.Fatal(err)
	}
	if p.Status != "blocked" || p.Cleanup.BrowserClosed || p.Browser["complete"] != false || p.Browser["sha256"] != digest(data) {
		t.Fatal("partial evidence was lost or accepted", p.Status, p.Browser)
	}
	first := p.Browser["first_failure"].(map[string]string)
	if first["id"] != "public-code-reopen" || !strings.Contains(first["failure"], "Call log:") || !strings.Contains(p.Failure, "interrupted") {
		t.Fatal("first locator/prerequisite diagnostics were hidden", first, p.Failure)
	}
	encoded, err := json.Marshal(p.Browser)
	if err != nil || !strings.Contains(string(encoded), "first concrete runtime failure") || !strings.Contains(string(encoded), "public-code-reopen") {
		t.Fatal("partial report cannot preserve runtime and unrun facts", err)
	}
}

func TestBrowserPassCannotOmitTheFixedCoverage(t *testing.T) {
	var p report
	if err := recordBrowserReport(&p, []byte(`{"schema":1,"status":"passed","complete":true,"browser_closed":true,"coverage":{"complete":false}}`), nil); err != nil {
		t.Fatal(err)
	}
	if p.Status != "blocked" || !strings.Contains(p.Failure, "fixed coverage") {
		t.Fatal("missing acceptance rows were reported as passed")
	}
}

func TestBrowserPassCannotOmitCurrentOwnerFeedbackProof(t *testing.T) {
	var p report
	data := []byte(`{"schema":1,"status":"passed","complete":true,"browser_closed":true,"coverage":{"complete":true},"owner_feedback":{"complete":false,"layout_observations":146}}`)
	if err := recordBrowserReport(&p, data, nil); err != nil {
		t.Fatal(err)
	}
	if p.Status != "blocked" || !strings.Contains(p.Failure, "owner-feedback") || p.Browser["owner_feedback"] == nil {
		t.Fatal("current owner requirements were lost or accepted without complete proof", p.Status, p.Browser)
	}
}

func TestBrowserBestEffortDiagnosticsRemainVisibleWithoutChangingAcceptance(t *testing.T) {
	data := []byte(`{
  "schema":1,"status":"passed","complete":true,"browser_closed":true,"coverage":{"complete":true},
  "diagnostics":[{"classification":"best-effort-telemetry","contract":"authored trackAnalyticsEvent boundary","method":"POST","path":"/api/analytics/events","status":400,"event_name":"hint_apply","product_area":"sudoku"}]
}`)
	var p report
	if err := recordBrowserReport(&p, data, nil); err != nil {
		t.Fatal(err)
	}
	if p.Status != "passed" || p.Browser["errors"] != 0 || p.Browser["diagnostics"] != 1 {
		t.Fatal("optional telemetry diagnostic changed functional acceptance or disappeared", p.Status, p.Browser)
	}
	first := p.Browser["first_diagnostic"].(map[string]any)
	if first["status"] != 400 || first["event_name"] != "hint_apply" || first["contract"] != "authored trackAnalyticsEvent boundary" {
		t.Fatal("protected summary lost diagnostic attribution", first)
	}
}
