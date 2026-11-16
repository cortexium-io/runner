//go:build darwin || linux

package execution

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestCommandRejectionsRetainDiagnosticWithoutAcceptingOutputs(t *testing.T) {
	for _, tc := range []struct {
		name, kind, domain, artifact string
		exit                         int
	}{
		{name: "stopped", kind: "stopped", artifact: "evidence.txt"},
		{name: "nonzero exit", kind: "returned", exit: 7},
		{name: "missing result", kind: "returned", domain: "absent.json"},
		{name: "invalid domain", kind: "returned", domain: "invalid.json"},
		{name: "missing artifact", kind: "returned", artifact: "absent.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workspaceDisk(t)
			a := harnessAssignment(t)
			a.Setup.Profile.Command = &runner.Command{Result: tc.domain, Artifact: tc.artifact}
			scratch := t.TempDir()
			for name, text := range map[string]string{"invalid.json": "{}", "evidence.txt": "not accepted after stop"} {
				if err := os.WriteFile(filepath.Join(scratch, name), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
			}
			diagnostic := strings.Repeat("obsolete stage ", 100) + "LATEST_STAGE"
			end, err := collectCommandResult(d, a, commandResult{AttemptID: a.AttemptID, Kind: tc.kind, ExitCode: tc.exit, Diagnostic: diagnostic}, scratch)
			if err != nil || end == nil || end.Kind == "returned" || end.Result != nil || end.Retryable || end.Diagnostic != runner.DiagnosticTail(diagnostic) {
				t.Fatal("collector lost failure facts or accepted failed output", end, err)
			}
			if _, err := d.ArtifactSize(a.AttemptID + "/result"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejection collected product evidence", err)
			}
		})
	}
}

func TestSharedTerminalReceiptPreservesOnlyAuthenticatedDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		name, kind, final, want string
	}{
		{name: "stopped", kind: "stopped", final: `{"attempt_id":"attempt","kind":"returned","diagnostic":"stage=browser"}`, want: "stage=browser"},
		{name: "failed", kind: "failed", final: `{"attempt_id":"attempt","kind":"returned","diagnostic":"stage=build"}`, want: "stage=build"},
		{name: "absent old receipt", kind: "stopped"},
		{name: "malformed optional result", kind: "stopped", final: `[]`},
		{name: "foreign identity", kind: "stopped", final: `{"attempt_id":"foreign","kind":"returned","diagnostic":"foreign facts"}`},
		{name: "invalid inner kind", kind: "stopped", final: `{"attempt_id":"attempt","kind":"unknown","diagnostic":"invalid facts"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := workspaceDisk(t)
			a := harnessAssignment(t)
			a.AttemptID = "attempt"
			a.Setup.Profile = runner.Profile{
				Harness: "command", Mode: "shared", Access: "workspace-read",
				Command: &runner.Command{Args: []string{"/bin/true"}, Artifact: "evidence.txt"},
			}
			n := NewNative(d, nil, nil, nil)
			job := harnessJob{Assignment: a, Stage: "running", StopReason: "owner stop"}
			if err := n.saveJob(job); err != nil {
				t.Fatal(err)
			}
			receipt := harnessReceipt{AttemptID: a.AttemptID, Kind: tc.kind, Reason: "worker stopped", Final: json.RawMessage(tc.final), ModelLaunched: true, ChildrenSettled: true}
			data, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Write(harnessControl(a.AttemptID)+"/result.json", data, d.Policy.Database.MaxRecord); err != nil {
				t.Fatal(err)
			}
			n.Close()
			reopened := NewNative(d, nil, nil, nil)
			defer reopened.Close()
			reopened.launchWorker = func(string, func()) error { t.Error("settled receipt launched replacement work"); return nil }
			o, err := reopened.Observe(t.Context(), a.AttemptID)
			if err != nil || o.State != runner.Finished || o.End == nil || o.End.Kind != tc.kind || o.End.Result != nil || o.End.Retryable || o.End.Diagnostic != tc.want {
				t.Fatal("receipt diagnostics changed settlement or result authority", o, err)
			}
			if tc.kind == "stopped" && o.End.Reason != "owner stop" {
				t.Fatal("diagnostic replaced the owning stop reason", o.End)
			}
			if _, err := d.ArtifactSize(a.AttemptID + "/result"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("stopped receipt collected product artifacts", err)
			}
			saved, err := reopened.load(a.AttemptID)
			if err != nil || saved.End.Diagnostic != tc.want {
				t.Fatal("terminal job lost its diagnostic", err)
			}
			if err := reopened.Release(t.Context(), a.AttemptID); err != nil || reopened.Owns(a.AttemptID) {
				t.Fatal("diagnostics prevented normal release", err)
			}
		})
	}
}

func TestSharedCommandFinalKeepsLatestBoundedStage(t *testing.T) {
	a := harnessAssignment(t)
	diagnostic := strings.Repeat("OLD_STAGE\n", 500) + "LATEST_STAGE"
	var result commandResult
	if err := json.Unmarshal(sharedCommandFinal(a, nil, []byte(diagnostic)), &result); err != nil || result.Diagnostic != runner.DiagnosticTail(diagnostic) || !strings.HasSuffix(result.Diagnostic, "LATEST_STAGE") || result.AttemptID != a.AttemptID {
		t.Fatal("shared command retained an obsolete prefix instead of the latest stage", result, err)
	}
}
