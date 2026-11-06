package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

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
