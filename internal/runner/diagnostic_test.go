package runner

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDiagnosticTailKeepsLatestUTF8WithinJSONBudget(t *testing.T) {
	for _, text := range []string{
		"",
		"stage=browser; timeout",
		strings.Repeat("obsolete-stage ", 200),
		strings.Repeat("诊断🙂", 300),
		strings.Repeat("\x00\n<&\"\\", 300),
		strings.Repeat(string([]byte{0xff, 0x80}), 300),
	} {
		text += "LATEST_STAGE_SENTINEL"
		got := DiagnosticTail(text)
		encoded, err := json.Marshal(got)
		if err != nil || !utf8.ValidString(got) || len(got) > 480 || len(encoded) > 512 || !strings.HasSuffix(got, "LATEST_STAGE_SENTINEL") || DiagnosticTail(got) != got {
			t.Fatalf("diagnostic lost its latest facts or bound: raw=%d json=%d valid=%v err=%v", len(got), len(encoded), utf8.ValidString(got), err)
		}
	}
}

func TestLatestDiagnosticDoesNotReviveOlderFailure(t *testing.T) {
	failure := Attempt{State: Finished, End: &ExecutionEnd{Kind: "stopped", Diagnostic: "stage=browser"}}
	r := Run{Attempts: []Attempt{failure, {State: NotStarted}}}
	if got := r.LatestDiagnostic(); got != failure.End.Diagnostic {
		t.Fatal("pending retry hid settled failure evidence", got)
	}
	r.Attempts = append(r.Attempts, Attempt{State: Finished, End: &ExecutionEnd{Kind: "returned"}})
	if got := r.LatestDiagnostic(); got != "" {
		t.Fatal("successful execution revived stale failure evidence", got)
	}
}

func TestFinishPreservesDiagnosticWithoutChangingTerminalAuthority(t *testing.T) {
	disk, err := OpenDisk(t.TempDir(), DefaultStorage())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name            string
		cancel, timeout bool
		end             ExecutionEnd
		status          string
	}{
		{name: "watchdog", timeout: true, end: ExecutionEnd{Kind: "stopped"}, status: "Failed"},
		{name: "cancellation", cancel: true, end: ExecutionEnd{Kind: "stopped"}, status: "Cancelled"},
		{name: "invalid domain", end: ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "undeclared"}}, status: "Failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := Clone(tc.end)
			end.Diagnostic = strings.Repeat("old stage ", 100) + "stage=browser"
			r := Run{
				ID: "run", Status: "Running", CancelRequested: tc.cancel,
				Process:  Process{MaxAttempts: 1, Steps: []Step{{ID: "check", Role: "checker"}}},
				Roles:    map[string]Role{"checker": {ID: "checker", Execution: &Profile{Harness: "deterministic"}}},
				Attempts: []Attempt{{ID: "attempt", State: Finished, TimedOut: tc.timeout, End: &end}},
			}
			c := &Core{disk: disk, runs: map[string]Run{r.ID: r}}
			arts := c.finish(&r, 0)
			if r.Status != tc.status || r.Attempts[0].End.Kind == "returned" || r.Attempts[0].End.Diagnostic != DiagnosticTail(end.Diagnostic) || r.Attempts[0].End.Retryable || len(r.Outputs) != 0 || len(arts) != 0 || len(r.Attempts) != 1 {
				t.Fatalf("failure facts changed terminal authority or were lost: %+v", r)
			}
		})
	}
}
