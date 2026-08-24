//go:build darwin || linux

package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTrustedTestConfig(t *testing.T, path string, cfg Config, mode os.FileMode) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestTrustedConfigAcceptsExternalPrivateRegularFile(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktrees := filepath.Join(root, "worktrees")
	operator := filepath.Join(root, "operator")
	for _, dir := range []string{repository, worktrees, operator} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := explicitTestConfig()
	cfg.ProjectDir = repository
	for index := range cfg.Harnesses {
		cfg.Harnesses[index].WorkspaceWriteRoot = worktrees
	}
	path := filepath.Join(operator, "runner.json")
	writeTrustedTestConfig(t, path, cfg, 0o600)
	if _, err := LoadTrustedConfig(path); err != nil {
		t.Fatalf("external trusted config rejected: %v", err)
	}
}

func TestTrustedConfigRejectsUnsafeProvenance(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktrees := filepath.Join(root, "worktrees")
	operator := filepath.Join(root, "operator")
	for _, dir := range []string{repository, worktrees, operator} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg := explicitTestConfig()
	cfg.ProjectDir = repository
	for index := range cfg.Harnesses {
		cfg.Harnesses[index].WorkspaceWriteRoot = worktrees
	}
	valid := filepath.Join(operator, "valid.json")
	writeTrustedTestConfig(t, valid, cfg, 0o600)

	worktreePath := filepath.Join(worktrees, "runner.json")
	writeTrustedTestConfig(t, worktreePath, cfg, 0o600)
	unsafeMode := filepath.Join(operator, "unsafe.json")
	writeTrustedTestConfig(t, unsafeMode, cfg, 0o622)
	symlink := filepath.Join(operator, "linked.json")
	if err := os.Symlink(valid, symlink); err != nil {
		t.Fatal(err)
	}
	hardlink := filepath.Join(operator, "hardlinked.json")
	if err := os.Link(valid, hardlink); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(operator, "directory.json")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "omitted", path: "", want: "explicit --config"},
		{name: "worktree root", path: worktreePath, want: "must be outside"},
		{name: "symlink", path: symlink, want: "regular non-symlinked"},
		{name: "hardlink", path: hardlink, want: "exactly one filesystem link"},
		{name: "non-regular", path: directory, want: "regular non-symlinked"},
		{name: "other-writable", path: unsafeMode, want: "writable by group or other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LoadTrustedConfig(test.path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("unsafe config accepted or wrong error: %v", err)
			}
		})
	}
}

func TestTrustedConfigAcceptsProjectLocalFileWhetherTrackedOrUntracked(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktrees := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(filepath.Join(repository, ".cortexium"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktrees, 0o700); err != nil {
		t.Fatal(err)
	}
	runTrustedConfigGit(t, repository, "init", "--quiet")
	cfg := explicitTestConfig()
	cfg.ProjectDir = repository
	for index := range cfg.Harnesses {
		cfg.Harnesses[index].WorkspaceWriteRoot = worktrees
	}
	path := filepath.Join(repository, ".cortexium", "runner.json")
	writeTrustedTestConfig(t, path, cfg, 0o600)
	if _, err := LoadTrustedConfig(path); err != nil {
		t.Fatalf("untracked project-local config rejected: %v", err)
	}
	runTrustedConfigGit(t, repository, "add", ".cortexium/runner.json")
	if _, err := LoadTrustedConfig(path); err != nil {
		t.Fatalf("tracked project-local config rejected: %v", err)
	}
}

func TestTrustedConfigDestinationAllowsProjectAndRejectsWorktree(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	worktrees := filepath.Join(root, "worktrees")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktrees, 0o700); err != nil {
		t.Fatal(err)
	}
	externalLink := filepath.Join(root, "apparently-external")
	if err := os.Symlink(repository, externalLink); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(externalLink, "not-created", "runner.json")
	if err := ValidateTrustedConfigDestination(candidate, worktrees); err != nil {
		t.Fatalf("project-local destination rejected: %v", err)
	}
	worktreeLink := filepath.Join(root, "worktree-link")
	if err := os.Symlink(worktrees, worktreeLink); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTrustedConfigDestination(filepath.Join(worktreeLink, "runner.json"), worktrees); err == nil || !strings.Contains(err.Error(), "must be outside") {
		t.Fatalf("worktree destination accepted: %v", err)
	}
}

func runTrustedConfigGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
