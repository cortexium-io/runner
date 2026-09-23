package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep real Git/filesystem behavior in CLI planning tests; substitute only the
// literal GitHub network endpoint with this fixture's disposable bare remote.
func writePlanningGitFixture(t *testing.T, bin string) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repo, remote := filepath.Join(root, "repo"), filepath.Join(root, "origin.git")
	for _, args := range [][]string{
		{"init", "--bare", remote}, {"init", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "initial"},
		{"-C", repo, "remote", "add", "origin", "https://github.com/example/repo.git"},
		{"-C", repo, "push", remote, "main"},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture git: %v: %s", err, out)
		}
	}
	script := fmt.Sprintf(`#!/bin/bash
args=()
for arg in "$@"; do
  case "$arg" in
    https://github.com/example/repo.git) args+=(%q) ;;
    protocol.https.allow=always) args+=(protocol.file.allow=always) ;;
    *) args+=("$arg") ;;
  esac
done
exec %q "${args[@]}"
`, remote, git)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}
