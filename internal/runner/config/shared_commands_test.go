package config

import (
	"encoding/json"
	"os"
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
	if _, err := loader.Set("projects/demo/processes/greeting.yaml", "tasks", json.RawMessage("null")); err == nil || !strings.Contains(err.Error(), "source-owned shared commands") {
		t.Fatal("shared command silently entered an ordinary source-ref process", err)
	}
	if _, err := loader.Set("projects/demo/roles/prepare.yaml", "execution.access", json.RawMessage(`"workspace-write"`)); err != nil {
		t.Fatal("trusted shared initializer could not select explicit project writes", err)
	}
	if _, err := loader.Set("projects/demo/roles/prepare.yaml", "execution.mode", json.RawMessage(`""`)); err == nil {
		t.Fatal("legacy configured command acquired workspace-write access")
	}
}

func TestSourceSharedCommandRequiresReadOnlyCurrentCandidateBinding(t *testing.T) {
	role := runner.Role{
		ID: "check", Purpose: "Verify the exact retained candidate", Inputs: map[string]string{"patch": "artifact"}, Outputs: map[string]string{"check": "text"},
		Execution: &runner.Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &runner.Command{Args: []string{"/usr/bin/true", "{workspace}", "{artifact.patch}", "{scratch}"}}},
	}
	process := runner.Process{
		ID: "source-check", Inputs: map[string]string{}, MaxAttempts: 2, MaxAttemptsPerStep: 1, AttemptTimeout: "30s",
		Steps: []runner.Step{
			{ID: "write", Role: "writer", Inputs: map[string]runner.Binding{}},
			{ID: "check", Role: "check", Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}}},
		},
		Outputs: map[string]runner.Binding{"check": {FromStep: "check", Output: "check"}},
	}
	project := runner.Project{Source: &runner.SourceSettings{Ref: "main"}, Roles: map[string]runner.Role{
		"check":  role,
		"writer": {ID: "writer", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "codex", Access: "workspace-write"}},
	}}
	for _, kind := range []string{"valid", "no source", "write access", "input patch", "goal", "dependency"} {
		t.Run(kind, func(t *testing.T) {
			p, configured := runner.Clone(process), runner.Clone(project)
			switch kind {
			case "no source":
				configured.Source = nil
			case "write access":
				configured.Roles["check"].Execution.Access = "workspace-write"
			case "input patch":
				p.Inputs["patch"] = "artifact"
				p.Steps[1].Inputs["patch"] = runner.Binding{FromInput: "patch"}
			case "goal":
				configured.Roles["check"].Execution.Command.Args = append(configured.Roles["check"].Execution.Command.Args, "{goal}")
			case "dependency":
				configured.Roles["check"].Execution.Command.Args = append(configured.Roles["check"].Execution.Command.Args, "{dependency.other.artifact}")
			}
			if err := validateProcess(p, configured); (err == nil) != (kind == "valid") {
				t.Fatal("source command authority differs", kind, err)
			}
		})
	}
}

func TestSourceSharedAlternateProfileStillRequiresConcurrencyOne(t *testing.T) {
	loader, dir := fixture(t)
	projectFile := filepath.Join(dir, "projects/demo/project.yaml")
	bytes, err := os.ReadFile(projectFile)
	if err != nil {
		t.Fatal(err)
	}
	var project runner.ProjectFile
	if err := decode(bytes, &project); err != nil {
		t.Fatal(err)
	}
	project.Source = &runner.SourceSettings{Ref: "main"}
	role := runner.Role{
		ID: "prepare", Purpose: "Qualified source check with an explicitly selectable fixed profile", DefaultProfile: "default",
		Inputs: map[string]string{"patch": "artifact"}, Outputs: map[string]string{"check": "text"},
		Profiles: map[string]runner.Profile{
			"default": {Harness: "deterministic", Mode: "echo", Input: "patch", Output: "check"},
			"source":  {Harness: "command", Mode: "shared", Access: "workspace-read", Command: &runner.Command{Args: []string{"/usr/bin/true", "{artifact.patch}"}}},
		},
	}
	process := runner.Process{
		ID: "greeting", Inputs: map[string]string{}, MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "30s",
		Steps:   []runner.Step{{ID: "check", Role: role.ID, Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}}}},
		Outputs: map[string]runner.Binding{"check": {FromStep: "check", Output: "check"}},
	}
	for filename, value := range map[string]any{projectFile: project, filepath.Join(dir, "projects/demo/roles/prepare.yaml"): role, filepath.Join(dir, "projects/demo/processes/greeting.yaml"): process} {
		bytes, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(t, filename, string(bytes))
	}
	if _, err := loader.Load(); err == nil || !strings.Contains(err.Error(), "concurrency one") {
		t.Fatal("alternate source-shared profile escaped ordinary process concurrency qualification", err)
	}
	if _, err := loader.Set("instance.yaml", "concurrency", json.RawMessage("1")); err != nil {
		t.Fatal("source-shared alternate profile was rejected at qualified concurrency", err)
	}
	if !sourceSharedProfiles([]runner.Step{{ForEach: &runner.TaskGroup{Steps: process.Steps}}}, runner.Project{Roles: map[string]runner.Role{role.ID: role}}) {
		t.Fatal("nested source steps lost their selectable profile guard")
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
		{"ignored shared directory", "execution.command.directory", "frontend"},
		{"unknown mode", "execution.mode", "arbitrary"},
		{"legacy with ignored cache grant", "execution.mode", ""},
		{"relative cache root", "execution.command.runtime_write_paths", []string{"relative/cache"}},
		{"host cache root", "execution.command.runtime_write_paths", []string{"/"}},
		{"unbounded cache roots", "execution.command.runtime_write_paths", make([]string, 17)},
		{"model supplied argv", "execution.command.args", []string{"/usr/bin/true", "{text.name}"}},
		{"model supplied input path", "execution.command.args", []string{"/usr/bin/true", "{input.report}"}},
		{"undeclared retained input", "execution.command.args", []string{"/usr/bin/true", "{artifact.report}"}},
		{"inline input is not a retained file", "execution.command.args", []string{"/usr/bin/true", "{artifact.name}"}},
		{"compound artifact path", "execution.command.args", []string{"/usr/bin/true", "/tmp/{artifact.name}"}},
		{"compound dependency path", "execution.command.args", []string{"/usr/bin/true", "{dependency.import.artifact}/../secret"}},
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

func TestSharedCommandAcceptsOnlyWholeDeclaredArtifactFiles(t *testing.T) {
	role := runner.Role{
		Purpose: "Read a declared canonical artifact",
		Inputs:  map[string]string{"report": "artifact"},
		Outputs: map[string]string{"check": "text"},
	}
	profile := runner.Profile{Command: &runner.Command{Args: []string{"/usr/bin/cat", "{artifact.report}", "{dependency.import.artifact}"}}}
	if err := commandRoleContract(role, profile); err != nil {
		t.Fatal("declared artifact file authority was rejected", err)
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
