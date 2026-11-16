//go:build darwin

package execution

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
)

func TestSourceProjectFixedLauncherAndAssemblyWithPinnedCFT(t *testing.T) {
	file := os.Getenv("RUNNER_FRONTEND_REFERENCE_CONFIG")
	if file == "" || os.Getenv("RUNNER_SOURCE_SHARED_BROWSER") != "1" {
		t.Skip("set RUNNER_FRONTEND_REFERENCE_CONFIG and RUNNER_SOURCE_SHARED_BROWSER=1 for staged fixed-profile source/CFT qualification")
	}
	loader, err := config.New(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	project := snapshot.Projects["jacks-break"]
	s, a := sourceSharedFixture(t)
	w, err := s.workspaces.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(w.WorkDir, "frontend", "verification", "project-experience.mjs")
	if err := os.MkdirAll(filepath.Dir(entry), 0700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../../testdata/frontend-reference/project-verifier.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	writer := runner.Clone(a)
	writer.AttemptID = "attempt_launcher_fixture_writer"
	writer.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	candidate, err := s.workspaces.Capture(t.Context(), writer)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(data []byte) string { hash := sha256.Sum256(data); return hex.EncodeToString(hash[:]) }
	put := func(id string, value any) runner.Value {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.disk.Write("artifacts/"+id, data, s.disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		return runner.Value{ArtifactID: id}
	}
	request := "Synthetic fixture: verify source-owned fixed-launcher transport, not a product."
	patch := runner.Value{ArtifactID: candidate.PatchID}
	reference := put("attempt_reference/result", map[string]any{"version": 1, "kind": "owner-brief", "status": "ready", "request": request, "request_bytes": len(request), "request_sha256": digest([]byte(request))})
	done := make(chan error, 1)
	s.harnesses.launchWorker = func(file string, finished func()) error {
		go func() { done <- RunNativeWorker(context.Background(), file); finished() }()
		return nil
	}
	changed := make(chan string, 8)
	s.SetWake(func(id string) { changed <- id })
	run := func(id, role string, inputs map[string]runner.Value) (runner.Value, []byte) {
		t.Helper()
		a.AttemptID, a.Inputs, a.Setup.Profile = id, inputs, *project.Roles[role].Execution
		defer s.Stop(context.Background(), id)
		if _, err := s.Start(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		o := waitSourceShared(t, s, id, changed)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if o.End.Kind != "returned" || o.End.Result == nil || o.End.Result.Outcome != "ready" {
			t.Fatal("fixed profile did not return ready", o.End)
		}
		artifact := o.End.Result.Outputs["artifact"]
		data, err := s.disk.Read("artifacts/"+artifact.ArtifactID, s.disk.Policy.MaxArtifact)
		if err != nil {
			t.Fatal(err)
		}
		job, err := s.harnesses.load(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Release(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(job.ScratchDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fixed profile retained disposable scratch", err)
		}
		return artifact, data
	}
	projectArtifact, observed := run("attempt_project_fixture", "frontend-project-check", map[string]runner.Value{"patch": patch, "reference": reference})
	files := sourceProjectArchive(t, observed)
	var observations struct {
		SourceWriteDenied bool              `json:"source_write_denied"`
		SharedCacheWrite  bool              `json:"shared_cache_writable"`
		CacheSiblingDeny  bool              `json:"cache_sibling_denied"`
		Browser           string            `json:"browser"`
		Observations      []json.RawMessage `json:"observations"`
	}
	if err := json.Unmarshal(files["observations.json"], &observations); err != nil || !observations.SourceWriteDenied || !observations.SharedCacheWrite || !observations.CacheSiblingDeny || observations.Browser == "" || len(observations.Observations) != 2 || len(files["phone.png"]) < 100 || len(files["desktop.png"]) < 100 {
		t.Fatal("configured child Node did not preserve actual phone/desktop proof", err)
	}
	patchBytes, err := s.disk.Read("artifacts/"+patch.ArtifactID, s.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	// This deterministic judgment qualifies identity/transport, never model vision.
	experience := put("attempt_image_fixture/report", map[string]any{
		"verdict": "approved", "patch_sha256": digest(patchBytes), "owner_request_sha256": digest([]byte(request)),
		"verifier_sha256": digest(fixture), "project_artifact_id": projectArtifact.ArtifactID, "project_sha256": digest(observed),
		"inspected_images": []string{"phone.png", "desktop.png"}, "findings": []string{"Deterministic transport fixture; no native image judgment claimed."},
		"gaps": []string{},
	})
	focused := put("attempt_focus_fixture/result", map[string]any{"status": "passed", "fixture": "Synthetic focused component"})
	_, assembled := run("attempt_assembly_fixture", "frontend-assemble-checks", map[string]runner.Value{"patch": patch, "reference": reference, "focused": focused, "project": projectArtifact, "experience": experience})
	components := sourceProjectArchive(t, assembled)
	if len(components) != 3 || len(components["project-review.json"]) == 0 || len(components["focused.json"]) == 0 {
		t.Fatal("canonical checks duplicated images or omitted a named component")
	}
	focusedBytes, err := s.disk.Read("artifacts/"+focused.ArtifactID, s.disk.Policy.MaxArtifact)
	if err != nil || !bytes.Equal(components["focused.json"], focusedBytes) {
		t.Fatal("focused report was not preserved byte-for-byte", err)
	}
	t.Logf("exact staged launcher + assembly: captured current source, CFT %s phone/desktop interactions, read-only source, compact hash-bound components and scratch cleanup; no product/model judgment", observations.Browser)
}

func sourceProjectArchive(t *testing.T, archive []byte) map[string][]byte {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	r, files := tar.NewReader(z), map[string][]byte{}
	for {
		header, err := r.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name], err = io.ReadAll(io.LimitReader(r, 16<<20))
		if err != nil {
			t.Fatal(err)
		}
	}
}
