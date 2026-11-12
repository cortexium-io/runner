package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
)

// Actual configured-command sandbox and durable worker, zero native service,
// model turns or Safari/simulator actions. Everything is a new synthetic Core.
func TestGuidesClosedImportConfiguredCommand(t *testing.T) {
	binary, helper, sandbox := os.Getenv("RUNNER_GUIDE_CORE"), os.Getenv("RUNNER_GUIDE_IMPORT_HELPER"), os.Getenv("RUNNER_GUIDE_COMMAND_SANDBOX")
	if binary == "" || helper == "" || sandbox == "" {
		t.Skip("explicit exact-asset configured-command qualification")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-guides-command-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	repo := sourceRepositoryAt(t, filepath.Join(root, "synthetic-source"))
	var role runner.Role
	var process runner.Process
	for name, dest := range map[string]any{"roles/guides-media-import.json": &role, "stage-guides-media.json": &process} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, dest); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	helperSHA := guidesmedia.Digest(data)
	if len(data) > 16<<20 {
		t.Fatal("helper cap")
	}
	role.Execution.Command.Args[0] = helper
	role.Execution.GuidesImportSHA256 = helperSHA
	put := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := runner.DefaultStorage()
	policy.MaxManaged = 8 << 30
	policy.Database.MaxRecord = 40 << 10
	put("role.json", role)
	put("process.json", process)
	put("project.json", runner.ProjectFile{
		ID:             "jacks-break",
		Workspace:      repo,
		Source:         &runner.SourceSettings{Ref: "HEAD"},
		DefaultProcess: process.ID,
		Processes:      map[string]string{process.ID: "process.json"},
		Roles:          map[string]runner.RoleBinding{role.ID: {File: "role.json"}},
	})
	put("instance.json", runner.Instance{
		Projects:          []string{"project.json"},
		Concurrency:       1,
		ReconcileInterval: "5m",
		Storage:           policy,
		Codex: &runner.CodexSettings{
			Binary:       sandbox,
			AuthFile:     filepath.Join(root, "unused-no-auth"),
			GoBinary:     filepath.Join(runtime.GOROOT(), "bin", "go"),
			StorageLimit: 32 << 20,
		},
	})
	loader, err := config.New(filepath.Join(root, "instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	launches := 0
	hook := func(s *Service) {
		s.commands.launchWorker = func(file string, done func()) error {
			launches++
			cmd := exec.Command(binary, "command-worker", file)
			setNativeProcessGroup(cmd)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); done() }()
			return nil
		}
	}
	rig := openNativeRig(t, filepath.Join(root, "state"), snapshot, nil, hook)
	defer func() {
		if rig != nil {
			rig.close()
		}
	}()
	request := runner.StartRequest{
		Project:   "jacks-break",
		Process:   process.ID,
		RequestID: guidesmedia.RequestID + "-media",
		Inputs:    map[string]runner.Value{"request": runner.Text(guidesmedia.RequestID)},
	}
	saved, err := rig.core.Start(request)
	if err != nil {
		t.Fatal(err)
	}
	result := waitSourceRun(t, rig.core, saved.ID, func(r runner.Run) bool { return r.Terminal() && r.ResourcesReleased }, 30*time.Second)
	if result.Status != "Completed" || len(result.Attempts) != 1 || launches != 1 || result.RecordFormat != 6 {
		t.Fatal("closed import failed", result.Status, result.Reason, result.Attempts, launches)
	}
	indexID := result.Outputs["media_index"].ArtifactID
	indexBytes, err := rig.disk.Read("artifacts/"+indexID, 32<<10)
	if err != nil {
		t.Fatal(err)
	}
	index, err := guidesmedia.DecodeIndex(indexBytes)
	if err != nil || index.HelperSHA256 != helperSHA || index.RunID != saved.ID {
		t.Fatal(index, err)
	}
	for _, asset := range index.Assets {
		owner, err := rig.store.Artifact(t.Context(), asset.ArtifactID)
		if err != nil || owner.RunID != saved.ID || owner.Bytes != asset.Bytes {
			t.Fatal("raw ownership lost", owner, err)
		}
		content, err := rig.disk.Read("artifacts/"+asset.ArtifactID, 16<<20)
		if err != nil || guidesmedia.Digest(content) != asset.SHA256 {
			t.Fatal("raw bytes lost", err)
		}
	}
	// Normal result replay and service reconstruction cannot dispatch the helper
	// again. The completed import is not a product/publication receipt.
	again, err := rig.core.Start(request)
	if err != nil || again.ID != saved.ID || launches != 1 {
		t.Fatal("request replay duplicated importer", err)
	}
	rig.close()
	rig = nil
	rig = openNativeRig(t, filepath.Join(root, "state"), snapshot, nil, hook)
	replay, err := rig.core.Start(request)
	if err != nil || replay.ID != saved.ID || launches != 1 {
		t.Fatal("restart duplicated importer", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unused-no-auth")); !os.IsNotExist(err) {
		t.Fatal("fixture created private auth")
	}
	if strings.TrimSpace(string(fixtureGit(t, repo, "status", "--porcelain"))) != "" {
		t.Fatal("synthetic original source modified")
	}
	t.Logf("actual closed sandbox command Completed; synthetic Core=%s; helper_sha256=%s; accepted37 artifacts; one launch before/after restart; record6; all36 hashes/owners; settled cleanup; zero model/native Safari/product work", saved.ID, helperSHA)
}
