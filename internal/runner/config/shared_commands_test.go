package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func sharedCommandFixture(t *testing.T) (*Loader, string) {
	t.Helper()
	loader, dir := fixture(t)
	role := runner.Role{
		ID: "prepare", Purpose: "Run the configured independent API and browser verification",
		Inputs: map[string]string{"name": "text"}, Outputs: map[string]string{"check": "text", "artifact": "artifact"},
		Execution: &runner.Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &runner.Command{
			Args: []string{"/usr/bin/true", "--app", "{workspace}", "--evidence-dir", "{scratch}"},
			Env:  map[string]string{"TMPDIR": "{scratch}"}, Artifact: "evidence.tar.gz", Network: true,
			RuntimeWritePaths: []string{t.TempDir()}, ExitOutcomes: map[int]string{1: "ready"},
		}},
	}
	process := runner.Process{
		ID: "greeting", Inputs: map[string]string{"name": "text"},
		Steps:       []runner.Step{{ID: "verify", Role: "prepare", Inputs: map[string]runner.Binding{"name": {FromInput: "name"}}}},
		Outputs:     map[string]runner.Binding{"check": {FromStep: "verify", Output: "check"}, "artifact": {FromStep: "verify", Output: "artifact"}},
		MaxAttempts: 4, MaxAttemptsPerStep: 1, AttemptTimeout: "30s",
		Tasks: &runner.TaskPolicy{Roles: []string{"prepare"}, MaxTasks: 4, MaxDepth: 2, MaxConcurrent: 2},
	}
	for path, value := range map[string]any{"projects/demo/roles/prepare.yaml": role, "projects/demo/processes/greeting.yaml": process} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, path), string(data))
	}
	return loader, dir
}

func TestSharedCommandConfigurationNeedsNoLegacySourceOrModelRuntime(t *testing.T) {
	loader, _ := sharedCommandFixture(t)
	s, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	profile := s.Projects["demo"].Roles["prepare"].Execution
	if s.Instance.Codex != nil || s.Instance.Concurrency != 2 || s.Projects["demo"].Source != nil || profile.Harness != "command" || profile.Mode != "shared" || profile.Command.Artifact != "evidence.tar.gz" || len(profile.Command.RuntimeWritePaths) != 1 || profile.Command.ExitOutcomes[1] != "ready" {
		t.Fatal("shared fixed verification acquired legacy source/model/runtime requirements")
	}
	if _, err := loader.Set("projects/demo/processes/greeting.yaml", "tasks", json.RawMessage("null")); err == nil || !strings.Contains(err.Error(), "general role-task process") {
		t.Fatal("shared command silently entered an ordinary source-ref process", err)
	}
}

func TestSharedCommandConfigurationPreservesFixedAuthority(t *testing.T) {
	loader, _ := sharedCommandFixture(t)
	if _, err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"model injection", "execution.model", "arbitrary-model"},
		{"source write", "execution.access", "workspace-write"},
		{"ignored shared directory", "execution.command.directory", "frontend"},
		{"unknown mode", "execution.mode", "arbitrary"},
		{"legacy with ignored cache grant", "execution.mode", ""},
		{"relative cache root", "execution.command.runtime_write_paths", []string{"relative/cache"}},
		{"host cache root", "execution.command.runtime_write_paths", []string{"/"}},
		{"unbounded cache roots", "execution.command.runtime_write_paths", make([]string, 17)},
		{"model supplied argv", "execution.command.args", []string{"/usr/bin/true", "{text.name}"}},
		{"model supplied input path", "execution.command.args", []string{"/usr/bin/true", "{input.report}"}},
		{"old source identity", "execution.command.env", map[string]string{"REF": "{source.base}"}},
		{"wrong output contract", "outputs.check", "json"},
		{"missing purpose", "purpose", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, _ := json.Marshal(tc.value)
			if _, err := loader.Set("projects/demo/roles/prepare.yaml", tc.field, encoded); err == nil {
				t.Fatal("unsupported shared-command authority was accepted")
			}
		})
	}
}

func TestNativeLoaderEnforcesPurposeAndRecordOutputBound(t *testing.T) {
	loader, _ := sharedCommandFixture(t)
	if _, err := loader.Load(); err != nil {
		t.Fatal(err)
	}
	native := runner.Profile{Harness: "native", Model: "configured-model", Access: "input-only", MaxInput: 16 << 10, MaxOutput: 8 << 10, Native: &runner.NativeProfile{Harness: "codex", Tools: runner.CapabilitySelection{Mode: "native"}, Skills: runner.CapabilitySelection{Mode: "none"}}}
	data, _ := json.Marshal(native)
	if _, err := loader.Set("projects/demo/roles/prepare.yaml", "execution", data); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.Set("projects/demo/roles/prepare.yaml", "purpose", json.RawMessage(`""`)); err == nil {
		t.Fatal("native role with no purpose passed loader validation")
	}
	if _, err := loader.Set("projects/demo/roles/prepare.yaml", "execution.max_output", json.RawMessage(`"16KiB"`)); err == nil {
		t.Fatal("native result exceeded its owning record bound")
	}
}
