package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func browser429DiagnosticFixture() map[string]any {
	return map[string]any{"status": "complete", "kind": "reload", "phase": "reload", "path": "/shikaku/fixture", "reload": map[string]any{
		"status": "complete", "path": "/shikaku/fixture", "documentStatus": 200, "responses": []any{
			map[string]any{"method": "GET", "path": "/shikaku/fixture", "status": 200, "outcome": "http-ok"},
			map[string]any{"method": "POST", "path": "/api/games/shikaku", "status": 429, "outcome": "http-error"},
		},
		"headers": map[string]string{"Cookie": "PRIVATE_COOKIE"}, "body": "PRIVATE_BODY",
	}}
}

func TestLegacyBrowserFailureFeedbackKeepsInfrastructureRoutingAndSafeNote(t *testing.T) {
	for _, failure := range []string{"Error: HTTP429 PRIVATE_FAILURE\nPRIVATE_STACK", "AssertionError: saved board differs"} {
		data, err := json.Marshal(map[string]any{"failure": failure, "failureDiagnostic": browser429DiagnosticFixture(), "failureContext": map[string]any{"engine": "chromium", "device": "mobile", "game": "Shikaku", "path": "/shikaku/fixture"}})
		if err != nil {
			t.Fatal(err)
		}
		j := Job{Receipt: Receipt{Action: "qualify"}}
		err = browserReceiptFailure(&j, data, true)
		var source *sourceFailure
		if err == nil || errors.As(err, &source) != strings.HasPrefix(failure, "AssertionError") {
			t.Fatal("source/runtime routing changed", err)
		}
		if j.Receipt.BrowserFailure.Reload.Response.Status != 429 || j.Receipt.BrowserFailure.Phase != "reload" || j.Receipt.BrowserFailure.ReportSHA256 == "" {
			t.Fatal("legacy diagnostics missing", j.Receipt)
		}
		if source == nil {
			if !strings.Contains(err.Error(), "HTTP 429") || !strings.Contains(err.Error(), "chromium/mobile/Shikaku/reload") {
				t.Fatal("safe runtime note missing facts", err)
			}
			j.Receipt.Note = err.Error()
			encoded, _ := json.Marshal(publicReceipt(j.Receipt, true))
			if bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("private data escaped", string(encoded))
			}
		}
		encoded, _ := json.Marshal(j.Receipt.BrowserFailure)
		if len(encoded) > maxBrowserFeedback {
			t.Fatal("public diagnostic budget increased", len(encoded))
		}
	}
}

func TestLegacyBrowserCommandUsesOnlyExistingContextDeadline(t *testing.T) {
	c := &DeliveryConfig{BrowserModule: "/module", BrowserExecutable: "/chromium", WebKitExecutable: "/webkit", Node: "/node"}
	deadline := time.Now().Add(time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	args := browserCommand(ctx, c, "/script", "https://fixture.invalid", "/output")
	want := "RUNNER_BROWSER_DEADLINE_MS=" + strconv.FormatInt(deadline.UnixMilli(), 10)
	found := false
	for _, arg := range args {
		if arg == want {
			found = true
		}
	}
	if !found {
		t.Fatal("existing deadline omitted/extended", args)
	}
	for _, arg := range browserCommand(context.Background(), c, "/script", "https://fixture.invalid", "/output") {
		if strings.HasPrefix(arg, "RUNNER_BROWSER_DEADLINE_MS=") {
			t.Fatal("deadline invented without parent budget", arg)
		}
	}
}

func TestBrowserFailureMalformedContextCannotEraseKnownFailure(t *testing.T) {
	for _, failure := range []string{"Error: HTTP429 PRIVATE_FAILURE", "AssertionError: malformed optional context"} {
		data, err := json.Marshal(map[string]any{"failure": failure, "failureDiagnostic": browser429DiagnosticFixture(), "failureContext": map[string]any{"engine": []string{"PRIVATE_COOKIE"}, "path": "/shikaku/fixture"}})
		if err != nil {
			t.Fatal(err)
		}
		j := Job{Receipt: Receipt{Action: "qualify"}}
		err = browserReceiptFailure(&j, data, true)
		var source *sourceFailure
		if err == nil || errors.As(err, &source) || !strings.Contains(err.Error(), "invalid failure context") {
			t.Fatal("known malformed failure was accepted/rerouted", err)
		}
		f := j.Receipt.BrowserFailure
		if f.Status != "partial" || f.Issue != "invalid failure context" || f.ReportSHA256 == "" || f.Reload.Response.Status != 429 {
			t.Fatal("malformed context lost known evidence", f)
		}
		encoded, _ := json.Marshal(f)
		if len(encoded) > maxBrowserFeedback || bytes.Contains(encoded, []byte("PRIVATE_")) || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("unsafe or oversized failure evidence", string(encoded), err)
		}
	}
	diagnostic := browserDiagnosticFixture()
	wide := strings.Repeat("<>&\\\"界", 100)
	diagnostic["selector"], diagnostic["cell"] = wide, wide
	for _, key := range []string{"expected", "actual"} {
		s := diagnostic[key].(map[string]any)
		for _, field := range []string{"identity", "text", "value", "fill"} {
			s[field] = wide
		}
	}
	data, _ := json.Marshal(map[string]any{"failure": "Error: known runtime failure", "failureDiagnostic": diagnostic, "failureContext": map[string]any{"engine": []int{1}}})
	j := Job{}
	if browserReceiptFailure(&j, data, true) == nil {
		t.Fatal("wide malformed failure disappeared")
	}
	encoded, _ := json.Marshal(j.Receipt.BrowserFailure)
	if len(encoded) > maxBrowserFeedback || j.Receipt.BrowserFailure.Status != "truncated" {
		t.Fatal("malformed context exceeded diagnostic budget", len(encoded), j.Receipt.BrowserFailure)
	}
}

func TestBrowserAdmissionFeedbackKeepsCanonicalNavigationInfrastructureFacts(t *testing.T) {
	diagnostic := browser429DiagnosticFixture()
	diagnostic["kind"], diagnostic["phase"] = "admission", "canonical navigation"
	data, err := json.Marshal(map[string]any{"failure": "BrowserAdmissionError: PRIVATE_FAILURE", "failureDiagnostic": diagnostic})
	if err != nil {
		t.Fatal(err)
	}
	j := Job{Receipt: Receipt{Action: "qualify"}}
	err = browserReceiptFailure(&j, data, true)
	var source *sourceFailure
	if err == nil || errors.As(err, &source) {
		t.Fatal("admission failure became source assertion", err)
	}
	f := j.Receipt.BrowserFailure
	if f.Kind != "admission" || f.Phase != "canonical navigation" || f.Status != "complete" || f.Reload.Response.Status != 429 || !strings.Contains(err.Error(), "canonical navigation") {
		t.Fatal("admission facts lost", f, err)
	}
	encoded, _ := json.Marshal(f)
	if len(encoded) > maxBrowserFeedback || bytes.Contains(encoded, []byte("PRIVATE_")) {
		t.Fatal("admission evidence unsafe/oversized", string(encoded))
	}
}

func TestBrowserAdmissionFeedbackKeepsTransportFailureAndObservedHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name, code, want string
		status           int
	}{
		{"before headers", "net::ERR_ABORTED", "net::ERR_ABORTED", 0},
		{"after headers", "net::ERR_INCOMPLETE_CHUNKED_ENCODING", "net::ERR_INCOMPLETE_CHUNKED_ENCODING", 200},
		{"untrusted text", "net::ERR_ABORTED private data", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := map[string]any{
				"status": "partial", "kind": "admission", "phase": "canonical navigation", "path": "/binary/fixture",
				"reload": map[string]any{"status": "partial", "documentStatus": 200, "responses": []BrowserResponseFeedback{
					{Method: "POST", Path: "/api/games/binary", Status: tc.status, Outcome: "request-failed", NetworkError: tc.code},
				}},
			}
			data, _ := json.Marshal(map[string]any{"failure": "BrowserAdmissionError: request failed", "failureDiagnostic": diagnostic})
			j := Job{}
			err := browserReceiptFailure(&j, data, true)
			var source *sourceFailure
			f := j.Receipt.BrowserFailure.Reload
			if err == nil || errors.As(err, &source) || f.DocumentStatus != 200 || f.Response.Status != tc.status || f.Response.NetworkError != tc.want || f.Response.Outcome != "request-failed" {
				t.Fatal("transport evidence became a source verdict or lost its HTTP facts", err, f)
			}
			note := err.Error()
			if strings.Contains(note, "HTTP 0") || strings.Contains(note, "private data") || tc.want != "" && !strings.Contains(note, tc.want) {
				t.Fatal("invalid or unbounded request failure note", note)
			}
		})
	}
}

func browserDiagnosticFixture() map[string]any {
	return map[string]any{
		"status": "complete", "kind": "visible-state", "phase": "reload",
		"path": "/nonogram/ngm1-5-medium-1234567890", "selector": `.player-board-frame button[data-cell="0"]`, "cell": "0", "differences": 1,
		"expected": map[string]any{"present": true, "identity": "nonogram-board:data-cell:0", "text": "■", "value": "filled", "fill": "", "visible": true},
		"actual":   map[string]any{"present": true, "identity": "nonogram-board:data-cell:0", "text": "", "value": "unknown", "fill": "", "visible": true},
		"reload": map[string]any{"status": "complete", "path": "/nonogram/ngm1-5-medium-1234567890", "documentStatus": 200, "responses": []any{
			map[string]any{"method": "GET", "path": "/nonogram/ngm1-5-medium-1234567890", "status": 200, "resourceType": "document", "outcome": "http-ok"},
			map[string]any{"method": "POST", "path": "/api/games/nonogram", "status": 200, "resourceType": "fetch", "outcome": "http-ok"},
		}},
	}
}

func browserFailureFixture(t *testing.T, diagnostic any) []byte {
	t.Helper()
	r := map[string]any{"failure": "AssertionError [ERR_ASSERTION]: reload did not restore the visible board edit\nprivate full diff and stack", "failureContext": map[string]any{"engine": "chromium", "device": "desktop", "game": "Nonogram", "path": "/nonogram/ngm1-5-medium-1234567890", "width": 1440, "height": 1000, "phase": "touch edit, Undo, Redo, reload"}}
	if diagnostic != nil {
		r["failureDiagnostic"] = diagnostic
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestBrowserFailureEvidenceSurvivesExistingOpaqueReceipt(t *testing.T) {
	d := browserDiagnosticFixture()
	d["rawDOM"] = "PRIVATE_DOM_MUST_NOT_LEAVE_PACKAGE"
	d["headers"] = map[string]string{"Cookie": "PRIVATE_COOKIE_MUST_NOT_LEAVE_PACKAGE"}
	d["responseBody"] = "PRIVATE_BODY_MUST_NOT_LEAVE_PACKAGE"
	data := browserFailureFixture(t, d)
	original := append([]byte{}, data...)
	failure := browserAssertionFailure(data)
	if failure == nil {
		t.Fatal("source assertion missing")
	}
	f := failure.browser
	if f.Status != "complete" || f.Kind != "visible-state" || f.Cell != "0" || f.Expected.Identity != "nonogram-board:data-cell:0" || f.Actual.Identity != f.Expected.Identity || f.Expected.Value != "filled" || f.Expected.Text != "■" || f.Actual.Value != "unknown" || !f.Expected.Recorded || !f.Actual.Recorded || f.Reload.DocumentStatus != 200 || f.Reload.Response.Method != "POST" || f.Reload.Response.Path != "/api/games/nonogram" || f.Reload.Response.Status != 200 {
		t.Fatalf("actionable semantic/admission facts lost: %+v", f)
	}
	sum := sha256.Sum256(data)
	if f.ReportSHA256 != hex.EncodeToString(sum[:]) || !bytes.Equal(data, original) {
		t.Fatal("private evidence was changed or lost its digest")
	}
	r := Receipt{Key: "qualification", RunID: "run", Action: "qualify", Policy: "policy", Source: strings.Repeat("f", 40), Note: failure.reason, BrowserFailure: f}
	public := publicReceipt(r, true)
	encoded, err := json.Marshal(public)
	var roundtrip Receipt
	if err != nil || json.Unmarshal(encoded, &roundtrip) != nil || roundtrip != public {
		t.Fatal("opaque receipt equality/authentication changed", err)
	}
	for _, private := range []string{"PRIVATE_DOM", "PRIVATE_COOKIE", "PRIVATE_BODY", "private full diff and stack"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("private evidence leaked into public feedback", private)
		}
	}
	if len(failure.reason) > 512 {
		t.Fatal("public routing note limit changed")
	}
	for _, action := range []string{"prepare", "restore", "verify"} {
		r.Action = action
		if publicReceipt(r, true).BrowserFailure != (BrowserFailureFeedback{}) {
			t.Fatal("browser diagnostic leaked into unrelated release action", action)
		}
	}
	old := []byte(`{"key":"old","run_id":"oldrun","action":"qualify","policy_sha256":"oldpolicy","note":"old note","source_ref":"oldsource"}`)
	var legacy Receipt
	if json.Unmarshal(old, &legacy) != nil || publicReceipt(legacy, true) != legacy {
		t.Fatal("legacy empty diagnostics changed accepted receipt equality")
	}
	legacyJSON, _ := json.Marshal(legacy)
	if bytes.Contains(legacyJSON, []byte("browser_failure")) {
		t.Fatal("zero diagnostic changed legacy public JSON")
	}
}

func TestBrowserFeedbackRetainsHashiBridgeLineDifference(t *testing.T) {
	d := browserDiagnosticFixture()
	d["path"] = "/hashi/fixture"
	d["selector"] = ".hashi-board svg g"
	d["cell"] = "bridge:0"
	d["expected"] = map[string]any{"lines": 0, "visible": true}
	d["actual"] = map[string]any{"lines": 1, "visible": true}
	f := browserAssertionFailure(browserFailureFixture(t, d))
	if f == nil || f.browser.Status != "complete" || !f.browser.Expected.Lines.Recorded || !f.browser.Actual.Lines.Recorded || f.browser.Expected.Lines.Count != 0 || f.browser.Actual.Lines.Count != 1 || f.browser.Expected == f.browser.Actual {
		t.Fatal("Hashi bridge difference disappeared from complete diagnostics", f)
	}
	encoded, _ := json.Marshal(f.browser)
	if !bytes.Contains(encoded, []byte(`"lines":{"recorded":true,"count":0}`)) || !bytes.Contains(encoded, []byte(`"lines":{"recorded":true,"count":1}`)) || len(encoded) > maxBrowserFeedback {
		t.Fatal("zero/absent bridge count or encoded bound lost", string(encoded))
	}
	for _, lines := range []int{-1, 4097} {
		d["actual"] = map[string]any{"lines": lines, "visible": true}
		f = browserAssertionFailure(browserFailureFixture(t, d))
		if f == nil || f.browser.Status != "partial" || f.browser.Actual.Lines.Recorded {
			t.Fatal("invalid bridge count was presented as complete", lines, f)
		}
	}
}

func TestBrowserFeedbackMissingAndPartialEvidenceIsExplicit(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		change     func(map[string]any) any
	}{
		{"missing", "missing", func(map[string]any) any { return nil }},
		{"malformed", "missing", func(map[string]any) any { return "not a diagnostic object" }},
		{"missing actual", "partial", func(d map[string]any) any { delete(d, "actual"); return d }},
		{"missing admission", "partial", func(d map[string]any) any { delete(d, "reload"); return d }},
		{"no HTTP response", "partial", func(d map[string]any) any { d["reload"].(map[string]any)["responses"] = []any{}; return d }},
		{"unsafe path", "partial", func(d map[string]any) any { d["path"] = "/nonogram/board?private=query"; return d }},
		{"driver truncation", "truncated", func(d map[string]any) any { d["status"] = "truncated"; return d }},
		{"multiple admissions", "truncated", func(d map[string]any) any {
			r := d["reload"].(map[string]any)
			r["responses"] = append(r["responses"].([]any), map[string]any{"method": "POST", "path": "/api/games/nonogram", "status": 422, "outcome": "http-error"})
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := browserAssertionFailure(browserFailureFixture(t, tc.change(browserDiagnosticFixture())))
			if f == nil || f.browser.Status != tc.want {
				t.Fatalf("evidence status=%+v, want %s", f, tc.want)
			}
		})
	}
	d := browserDiagnosticFixture()
	d["actual"] = nil
	f := browserAssertionFailure(browserFailureFixture(t, d))
	if f == nil || f.browser.Status != "complete" || !f.browser.Actual.Recorded || f.browser.Actual.Present {
		t.Fatal("a captured missing cell became missing diagnostic", f)
	}
}

func TestBrowserFeedbackSavedStateUsesOnlyDigestsAndCounts(t *testing.T) {
	d := browserDiagnosticFixture()
	d["kind"] = "saved-state"
	delete(d, "differences")
	d["expected"] = map[string]any{"sha256": strings.Repeat("a", 64), "keys": 1}
	d["actual"] = map[string]any{"sha256": strings.Repeat("b", 64), "keys": 0}
	f := browserAssertionFailure(browserFailureFixture(t, d))
	if f == nil || f.browser.Status != "complete" || !f.browser.Expected.Recorded || !f.browser.Actual.Recorded || f.browser.Expected.SHA256 != strings.Repeat("a", 64) || f.browser.Actual.SHA256 != strings.Repeat("b", 64) || f.browser.Expected.Keys != 1 || f.browser.Actual.Keys != 0 {
		t.Fatal("saved-state hash evidence lost", f)
	}
}

func TestBrowserFeedbackAggregatesTransportAndVisibleDifferenceEvidence(t *testing.T) {
	for _, tc := range []struct {
		transport, summary, want, nested string
		differences                      int
	}{
		{"partial", "complete", "partial", "partial", 1},
		{"missing", "complete", "partial", "missing", 1},
		{"unknown", "complete", "partial", "missing", 1},
		{"partial", "truncated", "truncated", "partial", 1},
		{"complete", "complete", "partial", "complete", 0},
	} {
		t.Run(tc.transport+"/"+tc.summary+"/"+tc.want, func(t *testing.T) {
			d := browserDiagnosticFixture()
			d["status"], d["differences"] = tc.summary, tc.differences
			d["reload"].(map[string]any)["status"] = tc.transport
			f := browserAssertionFailure(browserFailureFixture(t, d))
			if f == nil || f.browser.Status != tc.want || f.browser.Reload.Status != tc.nested || f.browser.Reload.DocumentStatus != 200 || f.browser.Reload.Response.Status != 200 {
				t.Fatal("complete HTTP facts overrode incomplete transport/difference evidence", tc, f)
			}
		})
	}
}

func TestBrowserFeedbackBoundIncludesJSONEscapingAndUTF8(t *testing.T) {
	d := browserDiagnosticFixture()
	wide := strings.Repeat("<>&\"\\界\u2028", 80)
	d["selector"], d["cell"] = wide, wide
	for _, key := range []string{"expected", "actual"} {
		s := d[key].(map[string]any)
		for _, field := range []string{"identity", "text", "value", "fill"} {
			s[field] = wide
		}
	}
	f := browserAssertionFailure(browserFailureFixture(t, d))
	if f == nil {
		t.Fatal("assertion classification changed")
	}
	encoded, err := json.Marshal(f.browser)
	if err != nil || len(encoded) > maxBrowserFeedback || !utf8.Valid(encoded) || f.browser.Status != "truncated" {
		t.Fatalf("unbounded or silent diagnostic loss: %d bytes, %+v, %v", len(encoded), f.browser, err)
	}
	t.Logf("escaped public diagnostic %d/%d bytes", len(encoded), maxBrowserFeedback)
}

func browserGestureDiagnosticFixture() map[string]any {
	return map[string]any{
		"status": "complete", "kind": "gesture", "phase": "touch edit", "path": "/shikaku/fixture",
		"gesture": map[string]any{
			"grid": 5, "first": 0, "last": 6,
			"expected_cells": []int{0, 1, 5, 6}, "actual_cells": []int{5, 6, 10, 11},
			"expected_count": 4, "actual_count": 4,
			"intended_xy":       []float64{58.44, 178.44, 109.22, 229.22},
			"observed_xy":       []float64{58.44, 229.22, 109.22, 280.04},
			"target_cells":      []int{5, 11},
			"board_before_xywh": []float64{33, 153, 254, 254},
			"board_after_xywh":  []float64{33, 153, 254, 254},
			"scroll_xy":         []float64{0, 0, 0, 0}, "event_count": 12, "events_truncated": false, "terminal": true,
			"events": []any{map[string]any{"type": "pointerdown", "DOM": "PRIVATE_DOM", "cookie": "PRIVATE_COOKIE"}},
		},
	}
}

func TestBrowserGestureFeedbackRetainsObservedRowOffsetInComparableReceipt(t *testing.T) {
	for _, phase := range []string{"touch edit", "pointer edit"} {
		d := browserGestureDiagnosticFixture()
		d["phase"] = phase
		failure := browserAssertionFailure(browserFailureFixture(t, d))
		if failure == nil {
			t.Fatal("gesture assertion lost source routing")
		}
		f, g := failure.browser, failure.browser.Gesture
		if f.Status != "complete" || f.Kind != "gesture" || f.Phase != phase || f.Path != "/shikaku/fixture" ||
			g.Grid != 5 || g.Corners != [2]int{0, 6} || g.ExpectedCells != "0,1,5,6" || g.ActualCells != "5,6,10,11" ||
			g.Counts != [2]int{4, 4} || g.Intended != [4]float64{58.4, 178.4, 109.2, 229.2} ||
			g.Observed != [4]float64{58.4, 229.2, 109.2, 280} || g.TargetCells != [2]int{5, 11} ||
			g.Board != [8]float64{33, 153, 254, 254, 33, 153, 254, 254} || g.Events != 12 || !g.Terminal {
			t.Fatal("actionable intended/observed gesture facts lost", f)
		}
		public := publicReceipt(Receipt{Action: "qualify", BrowserFailure: f}, true)
		encoded, err := json.Marshal(public)
		var restored Receipt
		if err != nil || json.Unmarshal(encoded, &restored) != nil || restored != public || bytes.Contains(encoded, []byte("PRIVATE_")) {
			t.Fatal("comparable receipt changed or private gesture data escaped", string(encoded), err)
		}
		feedback, _ := json.Marshal(f)
		if len(feedback) > maxBrowserFeedback {
			t.Fatal("gesture summary exceeds existing receipt budget", len(feedback))
		}
		t.Logf("gesture feedback %d/%d bytes", len(feedback), maxBrowserFeedback)
	}
}

func TestBrowserGestureFeedbackCannotInventCompleteNativeEndpoints(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"no native events", func(g map[string]any) { g["event_count"] = 0 }},
		{"one native event", func(g map[string]any) { g["event_count"] = 1 }},
		{"missing terminal evidence", func(g map[string]any) { delete(g, "terminal") }},
		{"down and move without terminal event", func(g map[string]any) { g["event_count"], g["terminal"] = 2, false }},
		{"native pointer missed board", func(g map[string]any) { g["target_cells"] = []int{-1, 11} }},
		{"missing observed endpoints", func(g map[string]any) { delete(g, "observed_xy") }},
		{"missing count", func(g map[string]any) { delete(g, "actual_count") }},
		{"missing first cell", func(g map[string]any) { delete(g, "first") }},
		{"invalid grid", func(g map[string]any) { g["grid"] = 31 }},
		{"invalid cell", func(g map[string]any) { g["actual_cells"] = []int{5, 6, 10, 25} }},
		{"duplicate cell", func(g map[string]any) { g["actual_cells"] = []int{5, 6, 10, 10} }},
		{"invalid corner", func(g map[string]any) { g["last"] = -1 }},
		{"invalid native target", func(g map[string]any) { g["target_cells"] = []int{5, 25} }},
		{"inconsistent cell count", func(g map[string]any) { g["actual_count"] = 3 }},
		{"out of range coordinate", func(g map[string]any) { g["observed_xy"] = []float64{0, 100001, 0, 0} }},
		{"invalid box", func(g map[string]any) { g["board_after_xywh"] = []float64{0, 0, 0, 254} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := browserGestureDiagnosticFixture()
			tc.change(d["gesture"].(map[string]any))
			failure := browserAssertionFailure(browserFailureFixture(t, d))
			if failure == nil || failure.browser.Status != "partial" || failure.browser.Issue == "" {
				t.Fatal("missing or invalid gesture evidence became complete", failure)
			}
		})
	}

	d := browserGestureDiagnosticFixture()
	raw, _ := json.Marshal(d)
	raw = bytes.Replace(raw, []byte(`58.44`), []byte(`1e999`), 1)
	f := browserFailureFeedback([]byte("full report"), raw, "/shikaku/fixture")
	if f.Status != "partial" || f.Gesture != (BrowserGestureFeedback{}) {
		t.Fatal("overflowing numeric input was accepted", f)
	}
}

func TestBrowserGestureFeedbackMarksPrivateAndPublicTruncation(t *testing.T) {
	for _, wide := range []bool{false, true} {
		d := browserGestureDiagnosticFixture()
		g := d["gesture"].(map[string]any)
		g["events_truncated"] = true
		if wide {
			d["path"] = "/shikaku/" + strings.Repeat("a", 70)
			g["grid"], g["first"], g["last"] = 30, 899, 898
			cells := make([]int, 20)
			for i := range cells {
				cells[i] = 899 - i
			}
			g["expected_cells"], g["actual_cells"] = cells, cells
			g["expected_count"], g["actual_count"] = 900, 900
			g["intended_xy"], g["observed_xy"] = []float64{100000, 100000, 100000, 100000}, []float64{100000, 100000, 100000, 100000}
			g["board_before_xywh"], g["board_after_xywh"] = []float64{100000, 100000, 100000, 100000}, []float64{100000, 100000, 100000, 100000}
			g["scroll_xy"] = []float64{100000, 100000, 100000, 100000}
			g["event_count"] = 100000
		}
		failure := browserAssertionFailure(browserFailureFixture(t, d))
		if failure == nil || failure.browser.Status != "truncated" || failure.browser.ReportSHA256 == "" {
			t.Fatal("gesture truncation lost status or report binding", failure)
		}
		encoded, err := json.Marshal(failure.browser)
		if err != nil || len(encoded) > maxBrowserFeedback || failure.browser.Gesture.Grid == 0 {
			t.Fatal("bounded summary lost essential gesture endpoints", string(encoded), err)
		}
	}
}
