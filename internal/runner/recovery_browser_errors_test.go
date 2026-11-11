package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type browserErrorRecoveryExecutor struct {
	Executor
	calls    int
	validate func(Run, SourceResult, []byte, string) (json.RawMessage, error)
}

func (e *browserErrorRecoveryExecutor) ValidateBrowserErrorRecovery(_ context.Context, r Run, source SourceResult, report []byte, head string) (json.RawMessage, error) {
	e.calls++
	return e.validate(r, source, report, head)
}

func browserErrorRecoveryFixture(t *testing.T) (*Core, *frontendRecheckStore, *browserErrorRecoveryExecutor, RecoverBrowserErrorsRequest) {
	t.Helper()
	c, store, _, frontend := frontendAssertionRecoveryFixture(t)
	r := c.runs[frontend.RunID]
	source := Clone(r.Attempts[len(r.Attempts)-2].Source)
	report := []byte(`{"outcome":"failed","cases":[{"game":"shikaku","error":"fixture page error"}]}`)
	digest := fmt.Sprintf("%x", sha256.Sum256(report))
	receipt, err := json.Marshal(map[string]any{
		"action":        "qualify",
		"key":           "attempt_browser_qualification",
		"run_id":        r.ID,
		"source_ref":    source.Base,
		"policy_sha256": strings.Repeat("c", 64),
		"note":          "candidate browser obligation failed: fixture browser errors",
		"opaque":        map[string]any{"handle": "unchanged-private-handle", "generation": 7},
		"browser_failure": map[string]any{
			"status":        "missing",
			"report_sha256": digest,
			"issue":         "diagnostic not collected",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	qualification := Attempt{
		ID:       "attempt_browser_qualification",
		Step:     taskStepIndex(r.Process, "qualify"),
		Visit:    r.Visit + 1,
		State:    Finished,
		Released: true,
		Source: &SourceResult{
			AttemptID:    "attempt_browser_qualification",
			InputPatchID: source.PatchID,
			Repository:   source.Repository,
			WorkDir:      source.WorkDir,
			Base:         source.Base,
		},
		End: &ExecutionEnd{
			Kind: "returned",
			Result: &StepResult{
				Outcome: "changes_requested",
				Outputs: map[string]Value{"receipt": {Inline: receipt}},
			},
		},
	}
	r.Step, r.Visit = qualification.Step, qualification.Visit
	r.Attempts = append(r.Attempts, qualification)
	c.finish(&r, len(r.Attempts)-1)
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || !r.Attempts[len(r.Attempts)-1].Superseded {
		t.Fatal("fixture did not route qualification rejection into a new owner", r.Status, r.Reason)
	}
	r.Status, r.Reason = "Waiting", "frontend correction requires the retained browser error details"
	// A nonterminal owner retains its owned workspace even after every attempt
	// has settled and released its execution resources.
	r.ResourcesReleased = false
	c.runs[r.ID] = r
	// The lower-level recovery fixture omits completed-card process metadata.
	// Public Get also projects their attempt usage, so provide the saved steps.
	template := r.Process.Steps[taskStepIndex(r.Process, "cards")].ForEach
	for _, entry := range r.TaskGroups["cards"].Children {
		child := c.runs[entry.RunID]
		child.Process.Steps = Clone(template.Steps)
		child.Roles = Clone(r.Roles)
		child.Step = len(child.Process.Steps)
		c.runs[child.ID] = child
	}
	observed, err := c.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := RecoverBrowserErrorsRequest{
		RequestID:       "recover-current-browser-errors",
		RunID:           r.ID,
		AttemptID:       qualification.ID,
		Revision:        observed.Revision,
		PublicationHead: frontend.PublicationHead,
		Report:          report,
	}
	exec := &browserErrorRecoveryExecutor{}
	exec.validate = func(got Run, captured SourceResult, raw []byte, head string) (json.RawMessage, error) {
		if got.ID != r.ID || captured != *source || !bytes.Equal(raw, report) || head != request.PublicationHead {
			return nil, errors.New("authenticated browser source or publication changed")
		}
		return json.Marshal(map[string]string{"status": "complete", "kind": "page-errors", "report_sha256": digest})
	}
	c.executor = exec
	return c, store, exec, request
}

func TestBrowserErrorRecoveryOnlyReplacesCurrentOwnerEvidence(t *testing.T) {
	c, store, exec, request := browserErrorRecoveryFixture(t)
	before := Clone(c.runs[request.RunID])
	observed, err := c.Get(request.RunID)
	if err != nil || request.Revision != observed.Revision || request.Revision == before.Revision {
		t.Fatal("fixture must bind the public root-plus-card revision, not the private root revision", request.Revision, before.Revision, err)
	}
	_, definition, _ := MarshalRunRecord(before)
	remaining := c.familyRemaining(before)
	r, err := c.RecoverBrowserErrors(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "Waiting" || r.ResourcesReleased || r.ResourcesReleased != before.ResourcesReleased || r.BrowserErrorRecovery == nil ||
		store.saves != 1 || exec.calls != 1 || len(c.wake) != 0 || c.familyRemaining(r) != remaining {
		t.Fatal("evidence recovery dispatched work, changed budget or failed to save its audit", r.Status, store.saves, exec.calls)
	}
	var feedback map[string]json.RawMessage
	if err := json.Unmarshal(r.StepInputs["qualification"].Inline, &feedback); err != nil {
		t.Fatal(err)
	}
	var browser map[string]string
	if err := json.Unmarshal(feedback["browser_failure"], &browser); err != nil || browser["status"] != "complete" || browser["kind"] != "page-errors" ||
		browser["report_sha256"] != fmt.Sprintf("%x", sha256.Sum256(request.Report)) {
		t.Fatal("current owner did not receive the authenticated browser failure", err, browser)
	}
	feedbackSHA := fmt.Sprintf("%x", sha256.Sum256(feedback["browser_failure"]))
	var oldFeedback map[string]json.RawMessage
	if err := json.Unmarshal(before.StepInputs["qualification"].Inline, &oldFeedback); err != nil {
		t.Fatal(err)
	}
	delete(feedback, "browser_failure")
	delete(oldFeedback, "browser_failure")
	if !reflect.DeepEqual(feedback, oldFeedback) {
		t.Fatal("evidence recovery changed an opaque handle or another receipt property")
	}
	_, afterDefinition, _ := MarshalRunRecord(r)
	if !bytes.Equal(definition, afterDefinition) {
		t.Fatal("evidence recovery changed the frozen process or permissions")
	}
	unchanged := Clone(r)
	unchanged.StepInputs["qualification"] = before.StepInputs["qualification"]
	unchanged.BrowserErrorRecovery = before.BrowserErrorRecovery
	unchanged.Revision, unchanged.Updated = before.Revision, before.Updated
	if !reflect.DeepEqual(unchanged, before) {
		t.Fatal("evidence recovery changed attempts, route counters, decisions, source or visit")
	}
	audit := r.BrowserErrorRecovery
	if audit.RequestID != request.RequestID || audit.AttemptID != request.AttemptID || audit.ReportSHA256 != browser["report_sha256"] ||
		audit.PublicationHead != request.PublicationHead || audit.FeedbackSHA256 != feedbackSHA || audit.GrantedAt.IsZero() {
		t.Fatal("browser recovery audit lost its exact evidence identity", audit)
	}
	receipt := store.receipts[request.RequestID]
	if bytes.Contains(receipt.Args, request.Report) || bytes.Contains(receipt.Args, []byte(base64.StdEncoding.EncodeToString(request.Report))) {
		t.Fatal("request receipt retained raw browser report data")
	}
	changed := request
	changed.Report = []byte(`{"outcome":"failed","error":"different raw report"}`)
	if _, err := c.RecoverBrowserErrors(changed); err == nil || store.saves != 1 || exec.calls != 1 {
		t.Fatal("request ID accepted a different browser report", err)
	}
	// Rehydrate the persisted record with a new Core, exercising the same run
	// and receipt codec used on restart without repeating SQLite's own tests.
	state, frozen, err := MarshalRunRecord(c.runs[r.ID])
	if err != nil {
		t.Fatal(err)
	}
	restored, err := UnmarshalRunRecord(state, frozen)
	if err != nil {
		t.Fatal(err)
	}
	reopened := &Core{ctx: c.ctx, store: store, disk: c.disk, runs: map[string]Run{r.ID: restored}}
	replay, err := reopened.RecoverBrowserErrors(request)
	if err != nil || !reflect.DeepEqual(replay, restored) || store.saves != 1 || exec.calls != 1 {
		t.Fatal("record restart replay changed evidence or revalidated/regranted recovery", err)
	}
}

func TestBrowserErrorRecoveryRejectsChangedScopeWithoutMutation(t *testing.T) {
	for _, fault := range []string{"report hash", "revision", "raw root revision", "attempt", "not waiting", "active", "cancelled", "approval", "unsettled cleanup", "unreleased attempt", "already granted", "changed compact receipt", "publication", "source artifact"} {
		t.Run(fault, func(t *testing.T) {
			c, store, exec, request := browserErrorRecoveryFixture(t)
			r := Clone(c.runs[request.RunID])
			calls := 0
			switch fault {
			case "report hash":
				request.Report = []byte(`{"different":"browser report"}`)
			case "revision":
				request.Revision++
			case "raw root revision":
				if request.Revision == r.Revision {
					t.Fatal("fixture must include nonzero accepted-card revisions")
				}
				request.Revision = r.Revision
			case "attempt":
				request.AttemptID = "attempt_other_qualification"
			case "not waiting":
				r.Status = "Queued"
			case "active":
				c.inFlight[r.ID] = true
			case "cancelled":
				r.CancelRequested = true
			case "approval":
				r.Decision = &Decision{ID: "pending_merge"}
			case "unsettled cleanup":
				r.CleanupBlocked, r.CleanupError = true, "owned source cleanup remains blocked"
			case "unreleased attempt":
				r.Attempts[len(r.Attempts)-1].Released = false
			case "already granted":
				r.BrowserErrorRecovery = &BrowserErrorRecovery{}
			case "changed compact receipt":
				var compact map[string]any
				if err := json.Unmarshal(r.StepInputs["qualification"].Inline, &compact); err != nil {
					t.Fatal(err)
				}
				compact["note"] = "a different qualification obligation"
				raw, _ := json.Marshal(compact)
				r.StepInputs["qualification"] = Value{Inline: raw}
			case "publication":
				request.PublicationHead = strings.Repeat("e", 40)
				calls = 1
			case "source artifact":
				owner := r.Attempts[len(r.Attempts)-3].Source
				artifact := store.artifacts[owner.PatchID]
				artifact.RunID = "foreign_run"
				store.artifacts[artifact.ID] = artifact
			}
			c.runs[r.ID] = r
			if _, err := c.RecoverBrowserErrors(request); err == nil || store.saves != 0 || exec.calls != calls || !reflect.DeepEqual(c.runs[r.ID], r) || len(c.wake) != 0 {
				t.Fatal("unsafe browser evidence recovery mutated or dispatched work", err, store.saves, exec.calls)
			}
		})
	}
}

func TestBrowserErrorRecoveryRejectsMalformedTrustedFeedback(t *testing.T) {
	for _, feedback := range []string{`null`, `"not an object"`, `{"status":"missing","kind":"page-errors"}`, `{"status":"complete","kind":"page-errors","report_sha256":"wrong"}`, "oversized"} {
		t.Run(feedback, func(t *testing.T) {
			c, store, exec, request := browserErrorRecoveryFixture(t)
			before := Clone(c.runs[request.RunID])
			payload := json.RawMessage(feedback)
			if feedback == "oversized" {
				var err error
				payload, err = json.Marshal(map[string]string{
					"status":        "complete",
					"kind":          "page-errors",
					"report_sha256": fmt.Sprintf("%x", sha256.Sum256(request.Report)),
					"issue":         strings.Repeat("x", 768),
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			exec.validate = func(Run, SourceResult, []byte, string) (json.RawMessage, error) {
				return payload, nil
			}
			if _, err := c.RecoverBrowserErrors(request); err == nil || store.saves != 0 || exec.calls != 1 || !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("malformed browser feedback was persisted", err, store.saves)
			}
		})
	}
}

func TestBrowserErrorRecoveryDoesNotAuthorizeAnUnsafeRetry(t *testing.T) {
	c, store, _, request := browserErrorRecoveryFixture(t)
	if _, err := c.RecoverBrowserErrors(request); err != nil {
		t.Fatal(err)
	}
	r := Clone(c.runs[request.RunID])
	need, err := readyTail(r, r.Step)
	if err != nil {
		t.Fatal(err)
	}
	childID := r.TaskGroups["cards"].Children[0].RunID
	child := c.runs[childID]
	for range max(0, c.familyRemaining(r)-need+1) {
		child.Attempts = append(child.Attempts, Attempt{State: Finished, Released: true})
	}
	c.runs[childID] = child
	if c.familyRemaining(r) >= need {
		t.Fatal("fixture did not retain too little budget for the protected owner tail")
	}
	_, err = c.Retry(RetryRequest{
		RequestID:     "browser-recovery-does-not-grant-a-retry",
		RunID:         r.ID,
		WithinBudget:  true,
		ExtraAttempts: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "attempt reserve") || store.saves != 1 || !reflect.DeepEqual(c.runs[r.ID], r) {
		t.Fatal("evidence enrichment overrode the protected retry/completion reserve", err, store.saves)
	}
}

func TestBrowserErrorRecoveryRechecksRunAndAcceptedCardRevisions(t *testing.T) {
	for _, changed := range []string{"root", "card"} {
		t.Run(changed, func(t *testing.T) {
			c, store, exec, request := browserErrorRecoveryFixture(t)
			before := Clone(c.runs[request.RunID])
			validate := exec.validate
			exec.validate = func(r Run, source SourceResult, report []byte, head string) (json.RawMessage, error) {
				feedback, err := validate(r, source, report, head)
				c.mu.Lock()
				defer c.mu.Unlock()
				id := r.ID
				if changed == "card" {
					id = r.TaskGroups["cards"].Children[0].RunID
				}
				current := c.runs[id]
				current.Revision++
				c.runs[id] = current
				return feedback, err
			}
			if _, err := c.RecoverBrowserErrors(request); err == nil || store.saves != 0 || exec.calls != 1 {
				t.Fatal("stale browser validation overwrote newer run/card state", err)
			}
			after := c.runs[before.ID]
			if after.BrowserErrorRecovery != nil || !reflect.DeepEqual(after.StepInputs, before.StepInputs) || !reflect.DeepEqual(after.Attempts, before.Attempts) {
				t.Fatal("stale validation changed saved execution evidence")
			}
		})
	}
}
