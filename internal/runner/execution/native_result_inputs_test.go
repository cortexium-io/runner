//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessExactContextTextUsesReferences(t *testing.T) {
	a := harnessAssignment(t)
	goal := strings.Repeat("Complete product requirement. ", 40)
	a.Task, a.Inputs = goal, map[string]runner.Value{"request": runner.Text(goal)}
	a.TaskContext = &runner.TaskContext{Goal: goal, Brief: goal, Acceptance: []string{"Keep this exact criterion"}}
	prompt, _, err := harnessPayload(a)
	if err != nil || strings.Count(prompt, goal) != 1 || !strings.Contains(prompt, `"request":"task_context.goal"`) || !strings.Contains(prompt, `"task_ref":"task_context.goal"`) || !strings.Contains(prompt, "Keep this exact criterion") {
		t.Fatalf("context was repeated or lost: %v %s", err, prompt)
	}
}

func TestHarnessOversizedResultsStayCompleteAcrossRestart(t *testing.T) {
	d := workspaceDisk(t)
	d.Policy.Database.MaxRecord = 32 << 10
	a := harnessAssignment(t)
	a.Limits.MaxRecord = d.Policy.Database.MaxRecord
	a.Setup.Profile.MaxInput, a.Setup.Profile.MaxOutput = 128<<10, 4<<10
	a.OutputTypes = map[string]string{"document": "artifact"}
	a.TaskContext = &runner.TaskContext{Goal: "Evaluate complete child reports and select retained evidence"}
	artifact := "attempt_child/evidence"
	if err := d.Write("artifacts/"+artifact, []byte("canonical child evidence"), a.Limits.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	for i := range 9 {
		a.TaskContext.Results = append(a.TaskContext.Results, runner.RoleTaskResult{
			ID: fmt.Sprintf("child_%d", i), RunID: fmt.Sprintf("run_child_%d", i), Role: "writer", Status: "Completed", Reason: "Complete accepted child work",
			Outputs: map[string]runner.Value{"report": runner.Text(strings.Repeat("x", 15<<10) + "COMPLETE_RESULT_SENTINEL"), "evidence": {ArtifactID: artifact}},
		})
	}
	complete, _ := json.Marshal(a.TaskContext.Results)
	binary := filepath.Join(t.TempDir(), "fake-codex")
	script := `#!/bin/sh
if [ "$1" = --version ]; then printf 'fixture 1\n'; exit 0; fi
cat >/dev/null
grep -q COMPLETE_RESULT_SENTINEL "${TMPDIR%/*}/results.json" || exit 31
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"outcome\":\"ready\",\"outputs\":{\"document\":\"attempt_child/evidence\"},\"reason\":\"Selected complete child evidence\",\"task_action\":null}"}}'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile.Native.Binary = binary
	n := NewNative(d, nil, nil, nil)
	done := make(chan error, 1)
	n.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	if o, err := n.Start(t.Context(), a); err != nil || o.State != runner.Running {
		t.Fatal("oversized context was rejected before file staging", o, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	n.Close()
	reopened := NewNative(d, nil, nil, nil)
	defer reopened.Close()
	job, err := reopened.load(a.AttemptID)
	if err != nil || job.ResultsFile == nil {
		t.Fatal("result file reference was not durable", err)
	}
	data, err := os.ReadFile(job.ResultsFile.Path)
	if err != nil || !bytes.Equal(data, complete) || len(data) <= 128<<10 {
		t.Fatal("complete result aggregate was changed or truncated", err)
	}
	for _, name := range []string{harnessName(a.AttemptID), harnessControl(a.AttemptID) + "/spec.json", harnessControl(a.AttemptID) + "/assignment.json"} {
		data, err := d.Read(name, a.Setup.Profile.MaxInput+d.Policy.Database.MaxRecord)
		if err != nil || bytes.Contains(data, []byte("COMPLETE_RESULT_SENTINEL")) {
			t.Fatal("full results repeated in private metadata", name, err)
		}
	}
	if _, err := readHarnessSpec(filepath.Join(d.Root, harnessControl(a.AttemptID), "spec.json")); err != nil {
		t.Fatal("saved input reference could not be reconciled", err)
	}
	o, err := reopened.Observe(t.Context(), a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" || o.End.Result.Outputs["document"].ArtifactID != artifact {
		t.Fatalf("result forwarding lost authorized artifact: %+v %v", o.End, err)
	}
	proofData, _ := d.Read("artifacts/"+a.AttemptID+"/provenance.json", 64<<10)
	var proof runner.NativeProvenance
	if json.Unmarshal(proofData, &proof) != nil || proof.ResultsInput == nil || proof.ResultsInput.Bytes != int64(len(complete)) || proof.PromptBytes+proof.SchemaBytes > int(a.Setup.Profile.MaxInput) {
		t.Fatal("complete file and prompt measurements missing", string(proofData))
	}
	if err := reopened.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.ResultsFile.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("private results input retained after release", err)
	}
	if _, err := os.Stat(filepath.Join(d.Root, "artifacts", filepath.FromSlash(artifact))); err != nil {
		t.Fatal("canonical accepted child evidence removed", err)
	}
}

func TestHarnessResultsFileRequiresCapacityAndReadCapability(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxInput = 8 << 10
	a.TaskContext = &runner.TaskContext{Goal: "Read the complete reports", Results: []runner.RoleTaskResult{{Outputs: map[string]runner.Value{"report": runner.Text(strings.Repeat("x", 16<<10))}}}}
	a.Limits.MaxArtifact = 4 << 10
	if _, _, _, err := planHarnessAssignment(a, t.TempDir(), 64<<10); err == nil {
		t.Fatal("result file exceeded existing artifact bound")
	}
	a.Limits.MaxArtifact = 1 << 20
	a.Setup.Profile.Native = &runner.NativeProfile{Harness: "claude", Tools: runner.CapabilitySelection{Mode: "none"}, Skills: runner.CapabilitySelection{Mode: "none"}}
	if _, _, _, err := planHarnessAssignment(a, t.TempDir(), 64<<10); err == nil {
		t.Fatal("tools:none was falsely told to read a file")
	}
}

func TestHarnessResultsFileChecksDigestAndArtifactAuthority(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxInput = 8 << 10
	a.TaskContext = &runner.TaskContext{Goal: "Read complete accepted outputs", Results: []runner.RoleTaskResult{{Outputs: map[string]runner.Value{
		"report": runner.Text(strings.Repeat("x", 16<<10)), "document": {ArtifactID: "attempt_child/document"},
	}}}}
	control := t.TempDir()
	compact, file, data, err := planHarnessAssignment(a, control, 64<<10)
	if err != nil || file == nil {
		t.Fatal("expected a bounded immutable result input", err)
	}
	if err := os.WriteFile(file.Path, data, 0400); err != nil {
		t.Fatal(err)
	}
	if err := validateHarnessResults(compact, control, file); err != nil {
		t.Fatal(err)
	}
	changed := *file
	changed.SHA256 = strings.Repeat("0", 64)
	if err := validateHarnessResults(compact, control, &changed); err == nil {
		t.Fatal("changed task-results digest was accepted")
	}
	delete(compact.TaskContext.Results[0].Outputs, "document")
	if err := validateHarnessResults(compact, control, file); err == nil {
		t.Fatal("file introduced artifact authority absent from authorized compact assignment")
	}
}

func TestHarnessDiagnosticHeavyResultsUseCompleteResultsFile(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile.MaxInput = 8 << 10
	a.TaskContext = &runner.TaskContext{Goal: "Diagnose failed child work"}
	for i := range 24 {
		a.TaskContext.Results = append(a.TaskContext.Results, runner.RoleTaskResult{
			ID: fmt.Sprint(i), RunID: fmt.Sprint("run_", i), Status: "Failed",
			Diagnostic: runner.DiagnosticTail(strings.Repeat("old-stage ", 100) + fmt.Sprint("final-stage-", i)),
		})
	}
	complete, _ := json.Marshal(a.TaskContext.Results)
	control := t.TempDir()
	compact, file, data, err := planHarnessAssignment(a, control, 64<<10)
	if err != nil || file == nil || !bytes.Equal(data, complete) {
		t.Fatal("failure facts caused an input halt or were truncated", err)
	}
	for _, result := range compact.TaskContext.Results {
		if result.Diagnostic != "" {
			t.Fatal("diagnostics duplicated in compact context")
		}
	}
	if err := os.WriteFile(file.Path, data, 0400); err != nil {
		t.Fatal(err)
	}
	if err := validateHarnessResults(compact, control, file); err != nil {
		t.Fatal("exact failure results file was not readable", err)
	}
	compact.TaskContext.Results[0].Diagnostic = "duplicated"
	if err := validateHarnessResults(compact, control, file); err == nil {
		t.Fatal("duplicate inline diagnostics bypassed the compact context contract")
	}
}
