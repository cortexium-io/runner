package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareRunnerConfigGitignoreAddsExactUncommittedRule(t *testing.T) {
	repository := t.TempDir()
	runConfigGitTest(t, repository, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runConfigGitTest(t, repository, "add", ".gitignore")
	runConfigGitTest(t, repository, "-c", "user.name=Runner Test", "-c", "user.email=runner@example.invalid", "commit", "--quiet", "-m", "baseline")
	configPath := filepath.Join(repository, ".cortexium", "runner.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := prepareRunnerConfigGitignore(t.Context(), configPath, repository, &output); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(repository, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	want := "node_modules/\n# Cortexium Runner local configuration\n/.cortexium/runner.json\n"
	if string(content) != want {
		t.Fatalf(".gitignore = %q, want %q", content, want)
	}
	if !strings.Contains(output.String(), "was not staged or committed") {
		t.Fatalf("output did not explain Git state: %s", output.String())
	}
	status := runConfigGitTest(t, repository, "status", "--short")
	if status != "M .gitignore" && status != " M .gitignore" {
		t.Fatalf("git status = %q, want unstaged .gitignore modification", status)
	}
	if err := prepareRunnerConfigGitignore(t.Context(), configPath, repository, &output); err != nil {
		t.Fatalf("idempotent prepare: %v", err)
	}
	contentAgain, err := os.ReadFile(filepath.Join(repository, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contentAgain, content) {
		t.Fatalf("idempotent prepare changed .gitignore: %q", contentAgain)
	}
}

func runConfigGitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
