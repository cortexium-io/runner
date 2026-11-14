package execution

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceHandoffReconstructsAndPinsCapturedSourceWithoutEditOwner(t *testing.T) {
	f, a, r, proof := unpublishedSourceFixturePins(t, 4)
	if err := f.service.ValidateUnpublishedCardRecovery(t.Context(), r, proof); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.workspaces.load(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	a.AttemptID = "attempt_handoff_pin"
	a.Setup.Profile = runner.Profile{Harness: "task-source", Mode: "pin", Access: "workspace-read"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: proof.Source.PatchID}, "summary": runner.Text("captured completion; acceptance downstream")}
	a.SourceHandoff = &proof
	job := taskSourceJob{Assignment: a, PinIntent: true}
	if err = f.service.tasks.save(job); err != nil {
		t.Fatal(err)
	}
	job, err = f.service.tasks.load(a.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	a = job.Assignment
	if err = f.service.validateSourceHandoffPin(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	bad := runner.Clone(job)
	bad.Stopped = true
	if err = f.service.validateSourceHandoffPin(t.Context(), bad); err == nil {
		t.Fatal("stopped handoff retained source mutation authority")
	}
	bad = runner.Clone(job)
	bad.Assignment.SourceHandoff.Artifacts[0].SHA256 = strings.Repeat("0", 64)
	if err = f.service.validateSourceHandoffPin(t.Context(), bad); err == nil {
		t.Fatal("handoff skipped captured-byte authentication")
	}
	foreign := "jobs/foreign.command.json"
	if err = f.disk.Write(foreign, []byte(`{"assignment":{"run_id":"run_recover"}}`), f.disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	if err = f.service.validateSourceHandoffPin(t.Context(), job); err == nil {
		t.Fatal("handoff ignored another unsettled source writer")
	}
	if err = f.disk.Remove(foreign); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after ordinary pin/ref persistence, before job completion.
	first, violations, err := f.service.workspaces.pinTaskSource(t.Context(), a)
	if err != nil || len(violations) != 0 {
		t.Fatal("captured source did not reconstruct", err, violations)
	}
	o, err := f.service.Start(t.Context(), a)
	result := taskReturned(t, o, err, "ready")
	var pin runner.TaskCandidate
	if err = json.Unmarshal(result.Outputs["candidate"].Inline, &pin); err != nil || pin != *first {
		t.Fatal("pin replay changed accepted source", err)
	}
	if _, err = os.Lstat(before.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("handoff invented a writable checkout", err)
	}
	if captureDigest(mustRecoveryPatch(t, f, proof.Source.PatchID)) != proof.Artifacts[0].SHA256 {
		t.Fatal("original blocked capture changed")
	}
	last := f.seed(t, "final", "run_final", []string{"tail.txt"}, pin)
	lastSource := f.edit(t, last, "complete", map[string]string{"tail.txt": "final regression fixture\n"})
	lastPin := f.pin(t, last, lastSource, "complete", "ready")
	_, join := f.join(t, append(append(runner.Clone(proof.Pins), pin), *lastPin), "ready")
	if join.Source == nil {
		t.Fatal("ordinary six-pin join omitted handoff source")
	}
	entries, err := os.ReadDir(f.disk.Root + "/jobs")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".codex.json") {
			t.Fatal("handoff dispatched a model owner")
		}
	}
}
