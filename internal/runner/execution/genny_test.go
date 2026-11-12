package execution

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGennySourceIsExplicitAndKeepsOrdinaryInstructions(t *testing.T) {
	ordinary := runner.Profile{Harness: "codex", Access: "workspace-write"}
	if sourceInstructions(ordinary) != workspaceBase || sourcePermission(ordinary) != "runner-edit" {
		t.Fatal("ordinary frozen source profile changed")
	}
	genny := ordinary
	genny.Mode = "genny-source"
	if sourcePermission(genny) != "runner-genny-edit" || !strings.Contains(sourceInstructions(genny), "without --force") || !strings.Contains(sourceInstructions(genny), "only later configured checks") {
		t.Fatal("Genny recipe/check boundaries missing")
	}
	c := &Codex{}
	if err := c.validateGennyRuntime(); err == nil {
		t.Fatal("missing Genny runtime accepted")
	}
}

func TestPinnedGennyRuntimeRejectsChangedOrUnqualifiedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "generator")
	body := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(path, body, 0700); err != nil {
		t.Fatal(err)
	}
	pin := captureDigest(body)
	if err := pinnedExecutable(path, pin); err != nil {
		t.Fatal(err)
	}
	c := &Codex{cfg: &runner.CodexSettings{Genny: &runner.GennyRuntime{Binary: path, SHA256: pin}}}
	if err := c.validateGennyRuntime(); err == nil {
		t.Fatal("unqualified version claim accepted as the pinned Go build")
	}
	if err := os.WriteFile(path, []byte("changed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := pinnedExecutable(path, pin); err == nil {
		t.Fatal("runtime drift accepted")
	}
}

func TestOptionalGennyDriftDoesNotBlockOrdinarySource(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "runner-genny-independence-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	policy := runner.DefaultStorage()
	policy.MinFree = 0
	disk, err := runner.OpenDisk(filepath.Join(root, "state"), policy)
	if err != nil {
		t.Fatal(err)
	}
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	c := &Codex{disk: disk, ctx: t.Context(), cfg: &runner.CodexSettings{
		GoBinary: goBinary,
		Genny:    &runner.GennyRuntime{Binary: filepath.Join(root, "missing-generator"), SHA256: strings.Repeat("0", 64), GoBinary: filepath.Join(root, "missing-go", "bin", "go"), GoSHA256: strings.Repeat("0", 64)},
	}}
	if _, err := c.nativeConfiguration(t.Context()); err != nil {
		t.Fatal("optional missing generator blocked ordinary native startup", err)
	}
	peer := &scriptedPeer{config: c.cfg}
	if err := c.checkWorkspaceConfig(t.Context(), peer); err != nil {
		t.Fatal("optional permission catalog blocked ordinary source", err)
	}
	files := workspaceFiles(c.cfg, "runner-genny-edit", "ordinary-go-root")
	if files[filepath.Dir(filepath.Dir(c.cfg.Genny.GoBinary))] != "read" || files[c.cfg.Genny.Binary] != "read" || files["ordinary-go-root"] != nil {
		t.Fatal("Genny catalog did not match selected separate runtime", files)
	}
	j := nativeJob{Assignment: runner.Assignment{AttemptID: "ordinary"}, Workspace: &workspaceRecord{WorkDir: source}}
	env, err := c.workerEnvironment(j)
	if err != nil || env["RUNNER_GENNY"] != "" || env["GOROOT"] == "" {
		t.Fatal("ordinary source runtime changed", env, err)
	}
	j.Assignment.Setup.Profile = runner.Profile{Mode: "genny-source", Access: "workspace-write", GeneratorSHA256: c.cfg.Genny.SHA256, GeneratorGoSHA256: c.cfg.Genny.GoSHA256}
	if _, err := c.workerEnvironment(j); err == nil {
		t.Fatal("selected missing generator was accepted")
	}
}
