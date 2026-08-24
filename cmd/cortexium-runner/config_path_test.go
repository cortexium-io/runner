package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveRunnerConfigPathUsesRepositoryDefaultFromSubdirectory(t *testing.T) {
	repository := t.TempDir()
	command := exec.Command("git", "init", "--quiet", repository)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	nested := filepath.Join(repository, "nested", "directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(canonicalRepository, filepath.FromSlash(defaultRunnerConfigPath))
	if got := resolveRunnerConfigPath("", nested); got != want {
		t.Fatalf("resolved config = %q, want %q", got, want)
	}
	if got := resolveRunnerConfigPath("custom/config.json", nested); got != filepath.Clean("custom/config.json") {
		t.Fatalf("explicit config changed to %q", got)
	}
}
