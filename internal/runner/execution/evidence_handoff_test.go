package execution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func handoffTestArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var data bytes.Buffer
	z := gzip.NewWriter(&data)
	archive := tar.NewWriter(z)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := archive.WriteHeader(&tar.Header{Name: name, Size: int64(len(files[name])), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func handoffExecutionFixture(t *testing.T, mutate func(map[string]any, map[string]any, map[string][]byte, map[string][]byte)) (*Service, runner.Run, runner.RecoverEvidenceHandoffRequest) {
	t.Helper()
	disk := workspaceDisk(t)
	encode := func(value any) []byte {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	store := func(id string, data []byte) {
		if err := disk.Write("artifacts/"+id, data, disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
	}
	request := "Synthetic verification request"
	owner := encode(map[string]any{"version": 1, "kind": "owner-brief", "status": "ready", "request": request, "request_bytes": len(request), "request_sha256": captureDigest([]byte(request))})
	referenceID := "attempt_reference/result"
	store(referenceID, owner)
	patchSHA := captureDigest([]byte("accepted patch"))
	focused := []byte(`{"status":"passed"}`)
	png := make([]byte, 24)
	copy(png, []byte{137, 80, 78, 71, 13, 10, 26, 10})
	binary.BigEndian.PutUint32(png[16:20], 390)
	binary.BigEndian.PutUint32(png[20:24], 844)
	projectFiles := map[string][]byte{"project-evidence.json": []byte(`{"failed":28,"passed":1174}`), "phone.png": png}
	identities := map[string]handoffFile{}
	for name, data := range projectFiles {
		identities[name] = handoffFile{SHA256: captureDigest(data), Bytes: int64(len(data))}
	}
	verifier := map[string]any{"entry": "frontend/verification/project-experience.mjs", "sha256": captureDigest([]byte("verifier"))}
	project := map[string]any{"version": 1, "status": "changes_requested", "files": identities, "candidate": map[string]string{"patch_sha256": patchSHA}, "owner_reference": map[string]string{"sha256": captureDigest(owner), "request_sha256": captureDigest([]byte(request))}, "verifier": verifier}
	aggregateFiles := map[string][]byte{"focused.json": focused, "project-diagnostic.json": []byte(`{"error":"inspect full report"}`)}
	aggregate := map[string]any{
		"version": 1, "kind": "frontend-checks", "status": "changes_requested", "patch_sha256": patchSHA,
		"owner_reference": map[string]any{"artifact_id": referenceID, "sha256": captureDigest(owner), "bytes": len(owner)},
		"focused":         map[string]any{"artifact_id": "attempt_focused/result", "sha256": captureDigest(focused), "bytes": len(focused), "patch_sha256": patchSHA, "component": "focused.json"},
		"verifier":        verifier,
	}
	// Mutate before packing to test independently valid archives with conflicting
	// authoritative metadata, rather than only random corrupt bytes.
	if mutate != nil {
		mutate(aggregate, project, aggregateFiles, projectFiles)
	}
	projectFiles["launcher.json"] = encode(project)
	projectBytes := handoffTestArchive(t, projectFiles)
	evidenceID := "attempt_project/result"
	images := []map[string]any{}
	for name, data := range projectFiles {
		if strings.HasSuffix(name, ".png") {
			images = append(images, map[string]any{"name": name, "sha256": captureDigest(data), "bytes": len(data)})
		}
	}
	aggregate["project"] = map[string]any{"artifact_id": evidenceID, "sha256": captureDigest(projectBytes), "bytes": len(projectBytes), "patch_sha256": patchSHA, "images": images}
	aggregateFiles["launcher.json"] = encode(aggregate)
	aggregateBytes := handoffTestArchive(t, aggregateFiles)
	store(evidenceID, projectBytes)
	store("attempt_check/result", aggregateBytes)
	run := runner.Run{
		ID: "run_source", ProjectID: "test", Inputs: map[string]runner.Value{"request": runner.Text(request), "reference": {ArtifactID: referenceID}},
		Process: runner.Process{Steps: []runner.Step{
			{ID: "project", Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}, "reference": {FromInput: "reference"}}},
			{ID: "check", Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}, "reference": {FromInput: "reference"}, "project": {FromStep: "project", Output: "artifact"}}},
		}},
		Attempts: []runner.Attempt{{ID: "attempt_project", Step: 0}, {ID: "attempt_check", Step: 1}},
	}
	req := runner.RecoverEvidenceHandoffRequest{CheckAttemptID: "attempt_check", PatchSHA256: patchSHA, CheckSHA256: captureDigest(aggregateBytes), Evidence: runner.CorrectionContext{ID: evidenceID, SHA256: captureDigest(projectBytes), Bytes: int64(len(projectBytes)), Roles: []string{"owner"}}}
	return &Service{disk: disk}, run, req
}

func TestEvidenceHandoffValidatesCompleteBoundOldArchive(t *testing.T) {
	service, run, req := handoffExecutionFixture(t, nil)
	if err := service.ValidateEvidenceHandoff(t.Context(), run, req); err != nil {
		t.Fatal(err)
	}
	// Neither source observations nor a prior StepInputs snapshot is invented.
	if run.Attempts[0].Source != nil || run.Attempts[1].Source != nil || run.StepInputs != nil {
		t.Fatal("validator changed historical facts")
	}
}

func TestEvidenceHandoffRejectsArchiveAndReferenceDrift(t *testing.T) {
	for _, mode := range []string{"patch", "owner-hash", "owner-request", "verifier", "manifest-file-hash", "missing-report", "unexpected-file", "focused", "project-binding", "reference-binding", "request", "project-sha", "project-size", "check-sha", "traversal", "trailing", "truncated"} {
		t.Run(mode, func(t *testing.T) {
			service, run, req := handoffExecutionFixture(t, func(a, p map[string]any, af, pf map[string][]byte) {
				switch mode {
				case "patch":
					p["candidate"] = map[string]string{"patch_sha256": captureDigest([]byte("other"))}
				case "owner-hash":
					p["owner_reference"].(map[string]string)["sha256"] = captureDigest([]byte("other"))
				case "owner-request":
					p["owner_reference"].(map[string]string)["request_sha256"] = captureDigest([]byte("other"))
				case "verifier":
					p["verifier"] = map[string]string{"entry": "other.mjs", "sha256": captureDigest([]byte("other"))}
				case "manifest-file-hash":
					p["files"].(map[string]handoffFile)["project-evidence.json"] = handoffFile{SHA256: captureDigest([]byte("other")), Bytes: 29}
				case "missing-report":
					delete(pf, "project-evidence.json")
					delete(p["files"].(map[string]handoffFile), "project-evidence.json")
				case "unexpected-file":
					pf["extra.json"] = []byte(`{}`)
				case "focused":
					af["focused.json"] = []byte(`{"status":"changed"}`)
				case "traversal":
					pf["../outside.json"] = []byte(`{}`)
				}
			})
			switch mode {
			case "project-binding":
				run.Process.Steps[1].Inputs["project"] = runner.Binding{FromStep: "other", Output: "artifact"}
			case "reference-binding":
				run.Process.Steps[0].Inputs["reference"] = runner.Binding{FromInput: "other"}
			case "request":
				run.Inputs["request"] = runner.Text("other request")
			case "project-sha":
				req.Evidence.SHA256 = captureDigest([]byte("other"))
			case "project-size":
				req.Evidence.Bytes++
			case "check-sha":
				req.CheckSHA256 = captureDigest([]byte("other"))
			case "trailing", "truncated":
				raw, err := service.disk.Read("artifacts/"+req.Evidence.ID, service.disk.Policy.MaxArtifact)
				if err != nil {
					t.Fatal(err)
				}
				z, err := gzip.NewReader(bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				var expanded bytes.Buffer
				if _, err := expanded.ReadFrom(z); err != nil {
					t.Fatal(err)
				}
				z.Close()
				changed := expanded.Bytes()
				if mode == "trailing" {
					changed = append(changed, []byte("unowned trailing bytes")...)
				} else {
					changed = changed[:len(changed)-700]
				}
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				writer.Write(changed)
				writer.Close()
				data := compressed.Bytes()
				req.Evidence.Bytes = int64(len(data))
				req.Evidence.SHA256 = captureDigest(data)
				if err := service.disk.Write("artifacts/"+req.Evidence.ID, data, service.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			}
			if err := service.ValidateEvidenceHandoff(t.Context(), run, req); err == nil {
				t.Fatal("invalid bound archive admitted")
			}
		})
	}
}

func TestEvidenceHandoffStagesOnlyExactProtectedArchiveForLegacyAndNative(t *testing.T) {
	service, _, req := handoffExecutionFixture(t, nil)
	a := harnessAssignment(t)
	a.RecoveryEvidence = runner.Clone(&req.Evidence)
	a.Setup.Profile.Harness = "codex"
	a.Setup.Profile.Access = "workspace-write"
	a.Setup.Profile.MaxInput = 8192
	a.Setup.Profile.MaxOutput = 1024
	a.Limits.MaxArtifact = service.disk.Policy.MaxArtifact
	a.Inputs = map[string]runner.Value{"request": runner.Text("Unchanged request")}
	if err := service.disk.Write("work/owned/source/.keep", nil, 1); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(service.disk.Root, "work", "owned", "source")
	job := nativeJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}
	codex := &Codex{disk: service.disk}
	if err := codex.workspaceInputs(job); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(work, ".runner-tmp", "inputs", a.AttemptID, "recovery-evidence.tar.gz")
	content, err := os.ReadFile(staged)
	if err != nil || captureDigest(content) != req.Evidence.SHA256 {
		t.Fatal("legacy owner lacks exact protected archive", err)
	}
	payload, _, err := nativePayloadAt(a, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var legacy nativePayloadRequest
	if err := json.Unmarshal([]byte(payload), &legacy); err != nil || legacy.RecoveryEvidence == nil || legacy.RecoveryEvidence.Path != filepath.Join(".runner-tmp", "inputs", a.AttemptID, "recovery-evidence.tar.gz") || legacy.RecoveryEvidence.SHA256 != req.Evidence.SHA256 || len(legacy.Inputs) != 1 {
		t.Fatal("legacy payload lost exact scoped reference", err)
	}
	a.Setup.Profile.Harness = "native"
	a.Setup.Profile.Access = "workspace-read"
	payload, _, err = harnessPayload(a)
	if err != nil {
		t.Fatal(err)
	}
	var native struct {
		RecoveryEvidence *recoveryContextReference `json:"recovery_evidence"`
	}
	if err := json.Unmarshal([]byte(payload), &native); err != nil || native.RecoveryEvidence == nil || native.RecoveryEvidence.Path != harnessArtifactFiles(a)[req.Evidence.ID] {
		t.Fatal("native reviewer lacks scoped evidence reference", err)
	}
	without := runner.Clone(a)
	without.RecoveryEvidence = nil
	if reflect.DeepEqual(harnessArtifactFiles(a), harnessArtifactFiles(without)) || len(harnessArtifactFiles(without)) != 0 {
		t.Fatal("artifact was exposed without explicit selection")
	}
	changed := runner.Clone(a)
	changed.RecoveryEvidence.SHA256 = strings.Repeat("0", 64)
	if _, err := stagedInputData(service.disk, changed); err == nil {
		t.Fatal("changed evidence staged")
	}
	// A writer cannot smuggle replacement bytes under the reserved staged name.
	a.Inputs["recovery-evidence.tar.gz"] = runner.Text("replace")
	if _, err := stagedInputData(service.disk, a); err == nil {
		t.Fatal("reserved evidence name collision accepted")
	}
}
