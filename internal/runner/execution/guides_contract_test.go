package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestGuidesSourcePermissionAndChangedTestDiscovery(t *testing.T) {
	cfg := &runner.CodexSettings{GuidesMediaManifest: guidesmedia.ManifestSHA256}
	files := workspaceFiles(cfg, "runner-guides-edit", "/go")
	roots := files[":workspace_roots"].(map[string]any)
	if roots["."] != "write" || roots[guideAssetRoot] != "read" || roots[".git"] != "deny" || sourcePermission(runner.Profile{Mode: "guides-source", Access: "workspace-write"}) != "runner-guides-edit" {
		t.Fatal("guide source boundary differs", roots)
	}
	ordinary := workspaceFiles(cfg, "runner-edit", "/go")[":workspace_roots"].(map[string]any)
	if ordinary[guideAssetRoot] != nil {
		t.Fatal("legacy profile changed")
	}
	x, _ := guidesmedia.NewIndex("run_import", "attempt_import", strings.Repeat("a", 64))
	data, _ := x.Encode()
	binding, _ := x.Bind(data)
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_ordinary_code")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	test := "frontend/src/components/guides/guide-media.test.ts"
	os.MkdirAll(filepath.Dir(filepath.Join(r.WorkDir, test)), 0700)
	os.WriteFile(filepath.Join(r.WorkDir, test), []byte("// synthetic changed regression\n"), 0600)
	captured, err := w.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	code, err := d.Read("artifacts/"+captured.PatchID, d.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	// This unit isolates the consumer codec after its caller's normal source
	// authentication; the exact-asset fixture owns full-tree reconstruction.
	envelope, err := guidesmedia.EncodePatch(r.Base, captured.Tree, binding, code, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	r.GuidesMedia = &binding
	selection, err := frontendCandidateTests(t.Context(), r, runner.Text(`["src/selected.test.ts"]`), envelope)
	if err != nil || !strings.Contains(selection, "src/components/guides/guide-media.test.ts") {
		t.Fatal("typed code envelope hid regression discovery", selection, err)
	}
}

func TestGuidesCommandHelperIdentityIsSeparateFromFrontend(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "runner-guides-import")
	payload := []byte("#!/bin/sh\nexit 0\n")
	os.WriteFile(name, payload, 0700)
	digest := guidesmedia.Digest(payload)
	if _, err := guidesImportHelperBytes(name, digest); err != nil {
		t.Fatal(err)
	}
	if _, err := frontendCheckHelperBytes(name, digest); err == nil {
		t.Fatal("legacy frontend helper accepted guide helper")
	}
	if _, err := guidesImportHelperBytes(name, strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed guide helper accepted")
	}
	renamed := filepath.Join(dir, "renamed")
	os.WriteFile(renamed, payload, 0700)
	if _, err := guidesImportHelperBytes(renamed, digest); err == nil {
		t.Fatal("arbitrary executable name accepted")
	}
	link := filepath.Join(t.TempDir(), "runner-guides-import")
	os.Symlink(name, link)
	if _, err := guidesImportHelperBytes(link, digest); err == nil {
		t.Fatal("guide helper symlink accepted")
	}
}

func TestGuidesOldWorkerRefusesNewHelperBeforeDispatch(t *testing.T) {
	old := os.Getenv("RUNNER_GUIDES_OLD_READER")
	if old == "" {
		t.Skip("explicit old binary command-worker compatibility proof")
	}
	bytes, err := os.ReadFile(old)
	if err != nil || guidesmedia.Digest(bytes) != "f6c9c314840308062a921f0de6165b2cafa16488c1dbaf2e17a603bf38741be8" {
		t.Fatal("old worker identity differs", err)
	}
	root := t.TempDir()
	marker := filepath.Join(root, "must-not-execute")
	helper := filepath.Join(root, "runner-guides-import")
	script := []byte("#!/bin/sh\ntouch '" + marker + "'\n")
	os.WriteFile(helper, script, 0700)
	control := filepath.Join(root, "control")
	os.Mkdir(control, 0700)
	os.WriteFile(filepath.Join(control, "stop"), []byte{0}, 0600)
	spec := commandSpec{
		AttemptID:    "attempt_fixture",
		Binary:       "/bin/false",
		Args:         []string{helper},
		Control:      control,
		Directory:    root,
		Deadline:     time.Now().Add(time.Minute),
		MaxLog:       512,
		HelperSHA256: guidesmedia.Digest(script),
		HelperKind:   "guides-import",
	}
	data, _ := json.Marshal(spec)
	file := filepath.Join(control, "spec.json")
	os.WriteFile(file, data, 0600)
	cmd := exec.CommandContext(t.Context(), old, "command-worker", file)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(output), err)
	}
	resultBytes, err := os.ReadFile(filepath.Join(control, "result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var result commandResult
	if err = json.Unmarshal(resultBytes, &result); err != nil || result.ExitCode != 1 || !strings.Contains(result.Diagnostic, "frontend check recovery requires") {
		t.Fatal("old worker did not refuse new kind before dispatch", result, err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("old worker launched new helper")
	}
	t.Log("old qualified worker ignored no integrity gate: legacy helper-name refusal prevented execution; no rollback may use old reader with record6")
}
