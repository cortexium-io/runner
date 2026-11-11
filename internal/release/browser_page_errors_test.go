package release

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func legacyBrowserPageErrorsFixture() map[string]any {
	return map[string]any{
		"contract":        2,
		"complete":        false,
		"headless":        true,
		"mockedResponses": false,
		"failure": "AssertionError [ERR_ASSERTION]: browser errors\n+ actual - expected\n\n+ [\n" +
			"+   'ResizeObserver loop completed with undelivered notifications.',\n" +
			"+   'ResizeObserver loop completed with undelivered notifications.'\n+ ]\n- []\n\n" +
			"    at PageReadiness.assertNoErrors (file:///runner/browser_render.mjs:232:33)\n" +
			"    at file:///runner/PRIVATE_STACK.mjs:148:17",
		"failureContext": map[string]any{
			"engine": "webkit",
			"device": "mobile",
			"game":   "Kakuro",
			"width":  320,
			"height": 568,
			"phase":  "screenshot",
			"path":   "/kakuro/fixture",
		},
		"failureText": "PRIVATE_BODY",
	}
}

func TestRecoverBrowserErrorsRetainsCompleteLegacyAssertionEvidence(t *testing.T) {
	r := legacyBrowserPageErrorsFixture()
	data, _ := json.Marshal(r)
	f, err := RecoverBrowserErrorFeedback(data)
	if err != nil || f.Status != "complete" || f.Kind != "page-errors" || f.Phase != "page errors" || f.Path != "/kakuro/fixture" ||
		f.PageErrors.Count != 2 || f.PageErrors.ResizeObserverUndelivered != 2 || f.PageErrors.ResizeObserverLoopLimit != 0 || f.PageErrors.Unclassified != 0 {
		t.Fatal("complete classified assertion was not recovered", f, err)
	}
	sum := sha256.Sum256(data)
	messages, _ := json.Marshal([]string{"ResizeObserver loop completed with undelivered notifications.", "ResizeObserver loop completed with undelivered notifications."})
	errorSum := sha256.Sum256(messages)
	if f.ReportSHA256 != hex.EncodeToString(sum[:]) || f.PageErrors.SHA256 != hex.EncodeToString(errorSum[:]) {
		t.Fatal("recovered evidence did not bind the report and complete error list")
	}
	encoded, _ := json.Marshal(f)
	if len(encoded) > maxBrowserFeedback || bytes.Contains(encoded, []byte("PRIVATE_")) {
		t.Fatal("private report text escaped its bounded feedback", string(encoded))
	}
	j := Job{Receipt: Receipt{Action: "qualify"}}
	err = browserReceiptFailure(&j, data, true)
	var source *sourceFailure
	if err == nil || !errors.As(err, &source) || source.browser != f || j.Receipt.BrowserFailure != f || strings.Contains(err.Error(), "PRIVATE_") {
		t.Fatal("known browser errors were ignored or lost their complete source feedback", j.Receipt.BrowserFailure, err)
	}
	r["failureDiagnostic"] = map[string]any{"status": "complete", "kind": "page-errors", "phase": "page errors", "page_errors": f.PageErrors}
	structured, _ := json.Marshal(r)
	fresh := browserAssertionFailure(structured)
	if fresh == nil || fresh.browser.PageErrors != f.PageErrors || fresh.browser.Status != "complete" || fresh.browser.Kind != f.Kind || fresh.browser.Path != f.Path {
		t.Fatal("structured feedback and recovered legacy assertion disagree", fresh, f)
	}
}

func TestUnknownBrowserErrorsRemainFailedWithExplicitMissingEvidence(t *testing.T) {
	r := legacyBrowserPageErrorsFixture()
	r["failure"] = strings.Replace(r["failure"].(string), "ResizeObserver loop completed with undelivered notifications.", "PRIVATE_SECRET https://fixture.invalid/?token=PRIVATE_TOKEN", 1)
	data, _ := json.Marshal(r)
	for _, report := range [][]byte{data, []byte("{")} {
		j := Job{Receipt: Receipt{Action: "qualify"}}
		err := browserReceiptFailure(&j, report, true)
		f := j.Receipt.BrowserFailure
		encoded, _ := json.Marshal(f)
		if err == nil || f.Status != "missing" || f.ReportSHA256 == "" || bytes.Contains(encoded, []byte("PRIVATE_")) || strings.Contains(err.Error(), "PRIVATE_") {
			t.Fatal("unknown or malformed browser errors were ignored, guessed or echoed", string(encoded), err)
		}
	}
}

func TestRecoverBrowserErrorsRejectsIncompleteOrAmbiguousLegacyEvidence(t *testing.T) {
	for _, name := range []string{"unclassified", "omitted", "abbreviated", "missing comma", "trailing comma", "wrong expected", "wrong assertion", "wrong stack", "wrong phase", "wrong game", "query path", "missing context", "missing headless", "mocked", "complete", "structured contradiction"} {
		t.Run(name, func(t *testing.T) {
			r := legacyBrowserPageErrorsFixture()
			failure := r["failure"].(string)
			context := r["failureContext"].(map[string]any)
			switch name {
			case "unclassified":
				r["failure"] = strings.Replace(failure, "ResizeObserver loop completed with undelivered notifications.", "PRIVATE_SECRET https://fixture.invalid/?token=PRIVATE_TOKEN", 1)
			case "omitted":
				r["failure"] = strings.Replace(failure, "+ ]", "+   ... 3 more items\n+ ]", 1)
			case "abbreviated":
				r["failure"] = strings.Replace(failure, "notifications.'", "notificati... 10 more characters'", 1)
			case "missing comma":
				r["failure"] = strings.Replace(failure, "notifications.',", "notifications.'", 1)
			case "trailing comma":
				r["failure"] = strings.Replace(failure, "notifications.'\n+ ]", "notifications.',\n+ ]", 1)
			case "wrong expected":
				r["failure"] = strings.Replace(failure, "- []", "- [ 'expected' ]", 1)
			case "wrong assertion":
				r["failure"] = strings.Replace(failure, "browser errors", "saved board differs", 1)
			case "wrong stack":
				r["failure"] = strings.Replace(failure, "PageReadiness.assertNoErrors", "AnotherAssertion", 1)
			case "wrong phase":
				context["phase"] = "PRIVATE_CONTEXT"
			case "wrong game":
				context["game"] = "Shikaku"
			case "query path":
				context["path"] = "/kakuro/fixture?token=PRIVATE_TOKEN"
			case "missing context":
				delete(r, "failureContext")
			case "missing headless":
				delete(r, "headless")
			case "mocked":
				r["mockedResponses"] = true
			case "complete":
				r["complete"] = true
			case "structured contradiction":
				r["failureDiagnostic"] = map[string]any{"kind": "page-errors", "status": "complete"}
			}
			data, _ := json.Marshal(r)
			if f, err := RecoverBrowserErrorFeedback(data); err == nil || f != (BrowserFailureFeedback{}) || strings.Contains(err.Error(), "PRIVATE_") {
				t.Fatal("ambiguous or private evidence was accepted or echoed", f, err)
			}
		})
	}
	for _, data := range [][]byte{nil, []byte("{"), bytes.Repeat([]byte(" "), (128<<10)+1)} {
		if _, err := RecoverBrowserErrorFeedback(data); err == nil {
			t.Fatal("invalid or oversized report was accepted")
		}
	}
}

func TestBrowserPageErrorsBoundedClassifiedAndUnclassifiedFeedback(t *testing.T) {
	counts := BrowserPageErrorsFeedback{Count: 2, ResizeObserverUndelivered: 1, Unclassified: 1, SHA256: strings.Repeat("a", 64)}
	for _, name := range []string{"known", "unknown", "invalid count", "negative count", "oversized count", "invalid hash", "private path", "truncated", "maximum"} {
		t.Run(name, func(t *testing.T) {
			c, path, status := counts, "/kakuro/fixture", "complete"
			want := "partial"
			switch name {
			case "known":
				c.ResizeObserverUndelivered, c.Unclassified, want = 2, 0, "complete"
			case "invalid count":
				c.Count++
			case "negative count":
				c.Unclassified = -1
			case "oversized count":
				c.Count, c.ResizeObserverUndelivered, c.Unclassified = 1<<32, 1<<32, 0
			case "invalid hash":
				c.SHA256 = "PRIVATE_HASH"
			case "private path":
				path = "/kakuro/fixture?token=PRIVATE_TOKEN"
			case "truncated":
				status, want = "truncated", "truncated"
			case "maximum":
				c.Count, c.ResizeObserverUndelivered, c.Unclassified = 1<<32-1, 1<<32-2, 1
			}
			raw, _ := json.Marshal(map[string]any{"status": status, "kind": "page-errors", "phase": "PRIVATE_PHASE", "selector": "PRIVATE_SELECTOR", "expected": "PRIVATE_EXPECTED", "page_errors": c})
			f := browserFailureFeedback([]byte("PRIVATE_REPORT"), raw, path)
			encoded, _ := json.Marshal(f)
			if f.Status != want || f.Kind != "page-errors" || f.Phase != "page errors" || len(encoded) > maxBrowserFeedback || bytes.Contains(encoded, []byte("PRIVATE_")) {
				t.Fatal("page-error classification was unsafe or unbounded", string(encoded))
			}
			if name == "unknown" && (f.PageErrors.Unclassified != 1 || f.PageErrors.SHA256 != counts.SHA256) {
				t.Fatal("unknown error detail was silently omitted")
			}
		})
	}
}

func TestBrowserPageErrorActualReportPreflight(t *testing.T) {
	path := os.Getenv("RUNNER_BROWSER_ERROR_REPORT")
	if path == "" {
		t.Skip("opt-in retained browser-error report preflight")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := RecoverBrowserErrorFeedback(data)
	if err != nil || f.Status != "complete" || f.PageErrors.Count != 2 || f.PageErrors.ResizeObserverUndelivered != 2 || f.PageErrors.Unclassified != 0 {
		t.Fatal("actual retained report did not produce complete matched evidence", f, err)
	}
}
