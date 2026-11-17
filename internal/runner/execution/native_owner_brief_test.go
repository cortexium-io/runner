package execution

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestHarnessOwnerBriefPreservesReviewInputAndAuthority(t *testing.T) {
	disk := workspaceDisk(t)
	request := strings.Repeat("Examine the exact candidate screenshots. ", 80)
	const referenceID = "attempt_reference/result"
	a := runner.Assignment{
		AttemptID: "attempt_review", Task: "Review the retained screenshots",
		OutputDir: filepath.Join(disk.Root, "artifacts", "attempt_review"),
		WorkDir:   filepath.Join(disk.Root, "work", "attempt_review"),
		Limits:    runner.ExecutionLimits{MaxArtifact: disk.Policy.MaxArtifact},
		Setup: runner.ExecutionSetup{Profile: runner.Profile{
			Harness: "native", Model: "fixture", Access: "workspace-read", MaxInput: 4096, MaxOutput: 512,
			Native: &runner.NativeProfile{
				Harness: "codex", Tools: runner.CapabilitySelection{Mode: "native"},
				Skills: runner.CapabilitySelection{Mode: "none"},
			},
		}},
		Inputs: map[string]runner.Value{
			"request": runner.Text(request), "reference": {ArtifactID: referenceID},
			"project": {ArtifactID: "attempt_project/result"},
		},
		OutputTypes: map[string]string{"report": "artifact", "summary": "text"},
	}
	control := filepath.Join(disk.Root, harnessControl(a.AttemptID))
	if _, _, err := harnessPayload(a); !errors.Is(err, errHarnessInputBudget) {
		t.Fatalf("expected inline request to exceed the budget: %v", err)
	}
	store := func(value string) {
		t.Helper()
		data, err := json.Marshal(ownerBrief{Version: 1, Kind: "owner-brief", Status: "ready", Request: value,
			RequestSHA256: captureDigest([]byte(value)), RequestBytes: int64(len(value))})
		if err != nil {
			t.Fatal(err)
		}
		if err := disk.Write("artifacts/"+referenceID, data, disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
	}
	store(request)
	if err := validateHarnessOwnerBrief(disk, a); err != nil {
		t.Fatal(err)
	}
	if err := planHarnessOwnerBrief(a, control, a.Setup.Profile.MaxInput+disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	prompt, schema, err := harnessPayload(a, harnessPayloadOptions{Scratch: filepath.Join(control, "tmp"), RequestByReference: true})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Inputs          map[string]json.RawMessage `json:"inputs"`
		InputReferences map[string]string          `json:"input_references"`
	}
	if err := json.Unmarshal([]byte(prompt), &payload); err != nil {
		t.Fatal(err)
	}
	if len(prompt)+len(schema) > int(a.Setup.Profile.MaxInput) || payload.Inputs["request"] != nil ||
		payload.Inputs["project"] == nil || !strings.Contains(string(payload.Inputs["reference"]), referenceID) ||
		!strings.HasPrefix(payload.InputReferences["request"], "inputs.reference.path") {
		t.Fatal("review context lost evidence or exceeded its configured budget")
	}
	spec := harnessSpec{Assignment: a, Root: disk.Root, Control: control, Directory: a.WorkDir, RequestByReference: true}
	permissions := harnessFilesystem(spec)
	if !strings.Contains(permissions, filepath.Join(disk.Root, "artifacts", referenceID)+`"="read"`) {
		t.Fatal("owner brief was not in the existing read-only evidence scope")
	}
	store("A different request with its own valid hash")
	if err := validateHarnessOwnerBrief(disk, a); err == nil {
		t.Fatal("owner brief replacement was accepted")
	}
	a.Setup.Profile.Access = "workspace-write"
	if canReferenceOwnerBrief(a) {
		t.Fatal("request reference escaped the source reader scope")
	}
}
