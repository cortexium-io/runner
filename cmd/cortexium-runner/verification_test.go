package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestVerifyCLIRejectsDifferentRepositoryBeforeLaunchingCheck(t *testing.T) {
	root := t.TempDir()
	runCLIGitTest(t, root, "init")
	runCLIGitTest(t, root, "remote", "add", "origin", "https://github.com/other/repository.git")
	cfg := completeCLITestConfig(root)
	cfg.Verification = map[string]config.VerificationEntrypoint{"focused": {Command: "sh", Args: []string{"-c", "exit 0"}, TimeoutSeconds: 10, InputPaths: []string{"src"}, ToolchainCommands: []string{"sh"}}}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(t.Context(), []string{"verify", "--config", path, "--entrypoint", "focused", "--directory", root}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "does not match the configured repository") {
		t.Fatalf("unrelated candidate was labeled as configured repository: %v", err)
	}
}

func TestVerifyCLIRequiresOperatorConfigProvenanceBeforeDecoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := os.WriteFile(path, []byte("not a configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0666); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run(t.Context(), []string{"verify", "--config", path, "--entrypoint", "complete"}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "writ") {
		t.Fatalf("untrusted execution configuration was decoded: %v", err)
	}
}

func TestVerifyCLIRejectsCommandOverridesAndAmbiguousRecovery(t *testing.T) {
	for _, args := range [][]string{
		{"verify"}, {"verify", "--entrypoint", "complete", "arbitrary-command"},
		{"verify", "--command", "sh"}, {"verify", "--dry-run"}, {"verify", "--recover"},
		{"verify", "--recover", "--dry-run", "--expect-token", "x"},
		{"verify", "--recover", "--dry-run", "--entrypoint", "complete"},
	} {
		var out bytes.Buffer
		if err := run(t.Context(), args, strings.NewReader(""), &out); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestVerifyCLIHelpExposesBoundedEntrypointAndRecovery(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), []string{"verify", "--help"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--entrypoint", "--recover", "--expect-token"} {
		if !strings.Contains(out.String(), flag) {
			t.Fatalf("missing %s", flag)
		}
	}
}
