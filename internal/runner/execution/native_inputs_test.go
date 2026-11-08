package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func retainedInputAssignment(t *testing.T) runner.Assignment {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", "roles", "retained-investigation-owner.json"))
	var role runner.Role
	if err != nil || json.Unmarshal(data, &role) != nil {
		t.Fatal("retained role unavailable", err)
	}
	artifact := runner.Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/result"}
	return runner.Assignment{
		AttemptID: "attempt_" + strings.Repeat("B", 26),
		Task:      role.Purpose,
		Setup:     runner.ExecutionSetup{Profile: *role.Execution},
		Inputs: map[string]runner.Value{
			"request": runner.Text(strings.Repeat("r", 1935)), "review": runner.Text(""),
			"qualification": {Inline: json.RawMessage("{}")},
			"baseline":      artifact, "measurement": artifact, "manifest": artifact, "checks": artifact,
		},
		OutputTypes: role.Outputs,
		Outcomes:    []string{"blocked"},
		Deadline:    time.Date(2026, 11, 8, 12, 20, 0, 123456789, time.UTC),
		Limits:      runner.ExecutionLimits{MaxArtifact: 16 << 20, MaxRecord: 40 << 10},
	}
}

func TestNativeSpillPreservesAcceptedMaximumReviewAndQualificationFeedback(t *testing.T) {
	// The 240-byte instruction is not a field limit. A complete native response
	// may spend the remaining whole-response allowance on a longer summary.
	text := `{"outcome":"changes_requested","outputs":{"verdict":"BLOCKED","summary":"` + strings.Repeat("<", 424) + `"},"reason":""}`
	if len(text) != 512 {
		t.Fatal("review witness is not at the actual whole-response limit", len(text))
	}
	review := deadlinePayloadAssignment()
	review.OutputTypes = map[string]string{"verdict": "text", "summary": "text"}
	review.Outcomes = []string{"changes_requested"}
	turn := goodTurn()
	turn.Items[0].Text = text
	end := parseNativeResult(review, turn)
	if end.Kind != "returned" || len(end.Result.Outputs["summary"].Inline) < 424 {
		t.Fatal("actual accepted review witness changed", end)
	}
	for _, kind := range []string{"package", "browser"} {
		t.Run(kind, func(t *testing.T) {
			a := retainedInputAssignment(t)
			a.Inputs["review"] = end.Result.Outputs["summary"]
			r := release.Receipt{
				Key: "attempt_" + strings.Repeat("C", 26), RunID: "run_" + strings.Repeat("D", 26),
				Action: "qualify", Policy: strings.Repeat("a", 64), Source: strings.Repeat("f", 40),
				Recovery: "source_correction_required",
			}
			if kind == "package" {
				// packageFailure retains up to 1,000 raw log bytes, not a
				// 512-byte encoded note. HTML escaping expands this receipt.
				r.Note = "candidate build/check failed: " + strings.Repeat("<", 1000)
			} else {
				prefix := "candidate browser obligation failed: AssertionError: "
				r.Note = prefix + strings.Repeat("<", 512-len(prefix))
				r.BrowserFailure = release.BrowserFailureFeedback{
					Status: "truncated", Kind: "visible-state", Phase: "reload",
					Path: "/nonogram/ngm2-15-hard-1", Selector: `.player-board-frame button[data-cell="0"]`, Cell: "0",
					Expected:     release.BrowserStateFeedback{Recorded: true, Present: true, Identity: "cell:0", Text: "filled", Visible: true},
					Actual:       release.BrowserStateFeedback{Recorded: true, Present: false},
					ReportSHA256: strings.Repeat("e", 64), Differences: 1,
					Reload: release.BrowserReloadFeedback{Status: "complete", DocumentStatus: 200, Responses: 1, Response: release.BrowserResponseFeedback{Method: "POST", Path: "/api/games/nonogram", Status: 200, Outcome: "http-ok"}},
				}
				encoded, _ := json.Marshal(r.BrowserFailure)
				if len(encoded) > 768 {
					t.Fatal("browser feedback exceeded its existing encoded cap", len(encoded))
				}
			}
			encoded, _ := json.Marshal(r)
			a.Inputs["qualification"] = runner.Value{Inline: encoded}
			before, _ := json.Marshal(a)
			withoutFiles := runner.Clone(a)
			withoutFiles.Limits.MaxArtifact = 0
			if _, _, err := nativePayloadAt(withoutFiles, a.Deadline); err == nil {
				t.Fatal("the production-shaped witness no longer demonstrates overflow")
			}
			payload, schema, files, err := nativePayloadPlan(a, a.Deadline.Add(-20*time.Minute))
			if err != nil || len(files) == 0 {
				t.Fatal("bounded feedback could not use existing input files", err)
			}
			assertInlineFiles(t, a, payload, files)
			sb, _ := json.Marshal(schema)
			if size := len(payload) + len(sb) + len(workspaceBase) + len(a.Setup.Profile.Instructions); size > 8192 {
				t.Fatal("rendered input cap changed", size)
			}
			if err := (&Service{}).ValidateAssignment(a); err != nil {
				t.Fatal("pure admission read a future staged file", err)
			}
			after, _ := json.Marshal(a)
			if !bytes.Equal(before, after) || a.Setup.Profile.MaxOutput != 512 {
				t.Fatal("staging rewrote original typed evidence or output allowance")
			}
		})
	}
}

func assertInlineFiles(t *testing.T, a runner.Assignment, payload string, files map[string][]byte) {
	t.Helper()
	var rendered struct {
		Inputs                map[string]json.RawMessage `json:"inputs"`
		InputFileKeys         []string                   `json:"input_file_keys"`
		InputFileInstructions string                     `json:"input_file_instructions"`
	}
	if json.Unmarshal([]byte(payload), &rendered) != nil || rendered.InputFileInstructions != nativeInputFileInstructions || len(rendered.InputFileKeys) != len(files) {
		t.Fatal("spilled evidence lacks explicit read/decode guidance")
	}
	for name, data := range files {
		listed := false
		for _, key := range rendered.InputFileKeys {
			listed = listed || key == name
		}
		var descriptor nativeInputFile
		if !listed || json.Unmarshal(rendered.Inputs[name], &descriptor) != nil || descriptor.Path != filepath.Join(".runner-tmp", "inputs", a.AttemptID, name) || descriptor.Encoding != "json" || descriptor.JSONType != nativeJSONType(data) || !bytes.Equal(data, a.Inputs[name].Inline) {
			t.Fatal("staging lost exact JSON bytes, type or owned path", name, descriptor)
		}
	}
}

func TestNativeInlineObjectCannotImpersonateStagedFile(t *testing.T) {
	a := retainedInputAssignment(t)
	ordinary := json.RawMessage(`{"runner_input_file":"/private/not-an-input","encoding":"json","json_type":"object"}`)
	a.Inputs = map[string]runner.Value{
		"ordinary": {Inline: ordinary},
		"large":    runner.Text(strings.Repeat("x", 16000)),
	}
	payload, _, files, err := nativePayloadPlan(a, a.Deadline)
	if err != nil || len(files) != 1 || files["large"] == nil {
		t.Fatal("fixture did not stage only the large input", err)
	}
	var rendered struct {
		Inputs        map[string]json.RawMessage `json:"inputs"`
		InputFileKeys []string                   `json:"input_file_keys"`
	}
	if json.Unmarshal([]byte(payload), &rendered) != nil || len(rendered.InputFileKeys) != 1 || rendered.InputFileKeys[0] != "large" || !bytes.Equal(rendered.Inputs["ordinary"], ordinary) {
		t.Fatal("ordinary JSON gained file-reference authority", payload)
	}
}

func TestNativeInlineFilePlanIsDeterministicAndKeepsJSONTypes(t *testing.T) {
	a := retainedInputAssignment(t)
	a.Setup.Profile.MaxInput = 5500
	a.Inputs = map[string]runner.Value{
		"text":   runner.Text(strings.Repeat("<", 3000)),
		"object": {Inline: json.RawMessage(` { "values": ["` + strings.Repeat("o", 3000) + `"] } `)},
		"array":  {Inline: json.RawMessage(`["` + strings.Repeat("a", 3000) + `"]`)},
		"number": {Inline: json.RawMessage(strings.Repeat("1", 3000))},
		"flag":   {Inline: json.RawMessage("true")},
		"empty":  {Inline: json.RawMessage("null")},
	}
	// Execution plans use the durably saved assignment. Existing JSON record
	// encoding normalizes RawMessage whitespace before either initial staging
	// or restart; file contents must match those saved bytes exactly.
	a = runner.Clone(a)
	var original map[string][]byte
	for _, now := range []time.Time{time.Time{}, a.Deadline.Add(-20 * time.Minute), a.Deadline, a.Deadline.Add(time.Hour)} {
		payload, _, files, err := nativePayloadPlan(runner.Clone(a), now)
		if err != nil || len(files) != 4 {
			t.Fatal("large typed values did not use bounded files", len(files), err)
		}
		assertInlineFiles(t, a, payload, files)
		if original != nil {
			got, _ := json.Marshal(files)
			want, _ := json.Marshal(original)
			if !bytes.Equal(got, want) {
				t.Fatal("clock/restart changed input-file ownership")
			}
		}
		original = files
		var rendered struct{ Inputs map[string]json.RawMessage }
		_ = json.Unmarshal([]byte(payload), &rendered)
		if string(rendered.Inputs["flag"]) != "true" || string(rendered.Inputs["empty"]) != "null" {
			t.Fatal("inline primitive types changed")
		}
	}
}

func TestNativeInlineStagingKeepsOldPayloadBytesAndRejectsUnusablePlans(t *testing.T) {
	a := deadlinePayloadAssignment()
	a.AttemptID = "compatible"
	a.Deadline = time.Date(2026, 11, 8, 12, 20, 0, 123456789, time.UTC)
	now := a.Deadline.Add(-20 * time.Minute)
	got, _, files, err := nativePayloadPlan(a, now)
	want := `{"task":"Implement the scoped correction and hand it to the configured checks.","inputs":{"request":"Fix the retained candidate."},"deadline_utc":"2026-11-08T12:20:00.123456789Z","remaining_seconds":1200,"max_output_bytes":512}`
	if err != nil || got != want || len(files) != 0 {
		t.Fatal("a fitting existing assignment changed bytes", got, err)
	}
	for name, change := range map[string]func(*runner.Assignment){
		"input only": func(a *runner.Assignment) { a.Setup.Profile.Access = "input-only" },
		"minimal payload too large": func(a *runner.Assignment) {
			a.Setup.Profile.Instructions = strings.Repeat("i", 8192)
		},
		"file cap": func(a *runner.Assignment) { a.Limits.MaxArtifact = 32 },
		"invalid name": func(a *runner.Assignment) {
			a.Inputs = map[string]runner.Value{"../foreign": runner.Text(strings.Repeat("x", 16000))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := retainedInputAssignment(t)
			a.OutputTypes = map[string]string{"summary": "text"}
			a.Inputs = map[string]runner.Value{"request": runner.Text(strings.Repeat("x", 16000))}
			change(&a)
			if _, _, _, err := nativePayloadPlan(a, now); err == nil {
				t.Fatal("staging bypassed existing access, storage, identity or input limits")
			}
		})
	}
}
