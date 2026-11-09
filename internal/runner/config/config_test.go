package config

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"go.yaml.in/yaml/v3"
)

// This suite owns file formats, validation and edits. Coordinator snapshot
// isolation is covered by the core suite, rather than repeated per format.
func fixture(t *testing.T) (*Loader, string) {
	return fixtureFrom(t, "demo")
}
func fixtureFrom(t *testing.T, name string) (*Loader, string) {
	t.Helper()
	source := filepath.Join("..", "..", "..", "testdata", "config", name)
	dir := t.TempDir()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	l, err := New(filepath.Join(dir, "instance.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return l, dir
}

func TestCodexConfigurationIsExplicitAndBounded(t *testing.T) {
	l, dir := fixtureFrom(t, "codex")
	s, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	p := s.Projects["demo"].Roles["summary"].Execution
	if p.Model != "gpt-6.1-sol" || p.Reasoning != "xhigh" || p.Access != "input-only" || s.Instance.Codex.StorageLimit != 16<<20 {
		t.Fatal("selected native setup was not resolved")
	}
	path := filepath.Join(dir, "projects/demo/roles/summary.yaml")
	before, _ := os.ReadFile(path)
	for _, change := range []struct{ file, field, value string }{
		{"projects/demo/roles/summary.yaml", "execution.access", `"workspace-write"`},
		{"projects/demo/roles/summary.yaml", "purpose", `""`},
		{"projects/demo/roles/summary.yaml", "execution.reasoning", `"ultra"`},
		{"projects/demo/roles/summary.yaml", "execution.max_input", `"1MiB"`},
		{"instance.yaml", "codex.storage_limit", `"32MiB"`},
	} {
		if _, err = l.Set(change.file, change.field, json.RawMessage(change.value)); err == nil {
			t.Fatalf("accepted unsupported native change: %+v", change)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rejected native edit changed role file")
	}
}
func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadsAndWatchesMoreThan128ConfigurationFiles(t *testing.T) {
	l, dir := fixture(t)
	baseline, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	projectDir := filepath.Join(dir, "projects", "demo")
	projectPath := filepath.Join(projectDir, "project.yaml")
	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatal(err)
	}
	var project runner.ProjectFile
	if err := decode(data, &project); err != nil {
		t.Fatal(err)
	}
	writeJSON := func(path string, value any) {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		write(t, path, string(data))
	}
	const fileCount = 131
	role := baseline.Projects["demo"].Roles["finish"]
	for i := len(baseline.Files); i < fileCount; i++ {
		role.ID = fmt.Sprintf("extra-%03d", i)
		file := filepath.Join("roles", role.ID+".json")
		project.Roles[role.ID] = runner.RoleBinding{File: file}
		writeJSON(filepath.Join(projectDir, file), role)
	}
	writeJSON(projectPath, project)
	loaded, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Files) != fileCount || loaded.Projects["demo"].Roles[role.ID].ID != role.ID {
		t.Fatalf("expected %d files and role %s, got %d files", fileCount, role.ID, len(loaded.Files))
	}
	role.Purpose += " Updated after registration."
	writeJSON(filepath.Join(projectDir, project.Roles[role.ID].File), role)
	if !l.Changed() {
		t.Fatal("did not detect the change to the added role")
	}
	loaded, err = l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Projects["demo"].Roles[role.ID].Purpose; got != role.Purpose {
		t.Fatalf("reloaded purpose = %q, want %q", got, role.Purpose)
	}
}

func TestInfrastructureRecoveryPolicyIsExplicitAndBounded(t *testing.T) {
	l, _ := fixture(t)
	if _, err := l.Load(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 2, -1, 3} {
		loaded, err := l.Set("projects/demo/processes/greeting.yaml", "infrastructure_retries", json.RawMessage(fmt.Sprint(n)))
		if n < 0 || n > 2 {
			if err == nil {
				t.Fatal("unbounded infrastructure recovery policy accepted", n)
			}
			continue
		}
		if err != nil || loaded.Projects["demo"].Processes["greeting"].InfrastructureRetries != n || loaded.Projects["demo"].Processes["greeting"].MaxAttempts != 4 {
			t.Fatal("infrastructure policy changed the original total cap", n, err)
		}
	}
}

func TestFormatsAndValidatedEdits(t *testing.T) {
	l, dir := fixture(t)
	before, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Files) != 5 || before.Projects["demo"].Roles["finish"].Execution.Harness != "deterministic" {
		t.Fatal("mixed YAML/JSON references were not loaded")
	}
	path := filepath.Join(dir, "instance.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, "# Owner comment\n"+string(b))
	if _, err = l.Set("instance.yaml", "concurrency", json.RawMessage(`3`)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if !strings.Contains(string(b), "# Owner comment") {
		t.Fatal("YAML edit discarded comments")
	}
	unchanged := string(b)
	for _, change := range []struct{ file, field, value string }{
		{"instance.yaml", "concurrency", `0`},
		{"instance.yaml", "storage.database.max_size", `"8MiB"`},
		{"projects/demo/roles/prepare.yaml", "execution.harness", `"codex"`},
		{"unselected.yaml", "concurrency", `2`},
	} {
		if _, err = l.Set(change.file, change.field, json.RawMessage(change.value)); err == nil {
			t.Fatalf("accepted invalid edit: %+v", change)
		}
	}
	b, _ = os.ReadFile(path)
	if string(b) != unchanged {
		t.Fatal("rejected edit changed the file")
	}
	if _, err = l.Set("projects/demo/roles/finish.json", "execution.delay", json.RawMessage(`"2s"`)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(dir, "projects/demo/roles/finish.json"))
	if !json.Valid(b) {
		t.Fatal("JSON edit changed its format")
	}
	var raw any
	instance, _ := os.ReadFile(path)
	if err = yaml.Unmarshal(instance, &raw); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "instance.json"), string(encoded))
	other, _ := New(filepath.Join(dir, "instance.json"))
	loaded, err := other.Load()
	if err != nil || loaded.Instance.Concurrency != 3 || loaded.Instance.Storage != before.Instance.Storage {
		t.Fatalf("JSON and YAML differ: %v", err)
	}
	write(t, path, "")
	if _, err = l.Set("instance.yaml", "concurrency", json.RawMessage(`2`)); err == nil {
		t.Fatal("empty manual edit accepted")
	}
}

func TestInvalidConfigurationAndRepairDetection(t *testing.T) {
	for _, tc := range []struct{ name, file, text string }{
		{"unknown field", "instance.yaml", "projects: []\nmystery: true\n"},
		{"duplicate key", "instance.yaml", "concurrency: 2\nconcurrency: 3\n"},
		{"extra document", "instance.yaml", "projects: []\n---\nprojects: []\n"},
		{
			"bad reference",
			"projects/demo/processes/greeting.yaml",
			"id: greeting\nsteps: [{id: work, role: absent}]\n",
		},
		{
			"bad profile contract",
			"projects/demo/roles/prepare.yaml",
			"id: prepare\ninputs: {name: text}\noutputs: {message: artifact}\nexecution: {harness: deterministic, mode: echo, input: name, output: message}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, dir := fixture(t)
			if _, err := l.Load(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, tc.file)
			old, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			write(t, path, tc.text)
			if !l.Changed() {
				t.Fatal("edit not detected")
			}
			if _, err = l.Load(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if l.Changed() {
				t.Fatal("unchanged invalid file causes repeated reloads")
			}
			write(t, path, string(old))
			if !l.Changed() {
				t.Fatal("repair not detected")
			}
			if _, err = l.Load(); err != nil {
				t.Fatal(err)
			}
		})
	}
	l, dir := fixture(t)
	write(t, filepath.Join(dir, "instance.yaml"), "projects: [new.yaml]\n")
	if _, err := l.Load(); err == nil {
		t.Fatal("missing project accepted")
	}
	write(t, filepath.Join(dir, "new.yaml"), "id: new\n")
	if !l.Changed() {
		t.Fatal("creation of missing reference not detected")
	}
}

// Workspace profiles own local checks; they cannot accidentally acquire
// model/deterministic options or parallelize an unqualified command lifecycle.
func TestWorkspaceConfiguration(t *testing.T) {
	l, _ := fixtureFrom(t, "workspace")
	snapshot, e := l.Load()
	if e != nil {
		t.Fatal(e)
	}
	if snapshot.Projects["seats"].Source.Ref != "HEAD" || snapshot.Projects["seats"].Roles["check"].Execution.Harness != "go-check" {
		t.Fatal("workspace selection lost")
	}
	for _, tc := range []struct {
		name, path, field string
		value             any
	}{
		{"exclusive command ownership", "instance.yaml", "concurrency", 2},
		{"explicit toolchain", "instance.yaml", "codex.go_binary", ""},
		{"protected source paths", "instance.yaml", "codex.read_only_paths", []string{"../private"}},
		{"check has no model", "projects/seats/roles/check.yaml", "execution.model", "gpt-6.1-sol"},
		{"source result is declared", "projects/seats/roles/implement.yaml", "outputs.patch", "text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := json.Marshal(tc.value)
			if _, e = l.Set(tc.path, tc.field, b); e == nil {
				t.Fatal("unsupported workspace setup accepted")
			}
		})
	}
}

func TestGoModuleCacheConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		valid      bool
	}{
		{"default", "", true},
		{"relative", "runtime/modcache", true},
		{"absolute", "absolute", true},
		{"missing", "runtime/missing", false},
		{"file", "runtime/file", false},
		{"root", "/", false},
		{"root symlink", "runtime/root", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, dir := fixtureFrom(t, "workspace")
			cache := filepath.Join(dir, "runtime", "modcache")
			if err := os.MkdirAll(cache, 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "runtime", "file"), "dummy")
			if err := os.Symlink("/", filepath.Join(dir, "runtime", "root")); err != nil {
				t.Fatal(err)
			}
			path := tc.path
			if path == "absolute" {
				path = cache
			}
			if path != "" {
				data, err := os.ReadFile(l.Path)
				if err != nil {
					t.Fatal(err)
				}
				value, _ := json.Marshal(path)
				write(t, l.Path, strings.Replace(string(data), "  go_binary: go\n", "  go_binary: go\n  go_module_cache: "+string(value)+"\n", 1))
			}
			snapshot, err := l.Load()
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "codex.go_module_cache") {
					t.Fatalf("invalid module cache accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if path != "" {
				want, err = filepath.EvalSymlinks(cache)
				if err != nil {
					t.Fatal(err)
				}
			}
			if snapshot.Instance.Codex.GoModuleCache != want {
				t.Fatalf("module cache = %q, want %q", snapshot.Instance.Codex.GoModuleCache, want)
			}
		})
	}
}

func TestGoCheckBinaryConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		valid      bool
	}{
		{"default", "", true},
		{"relative", "runtime/runner-go-check", true},
		{"absolute", "absolute", true},
		{"missing", "runtime/missing", false},
		{"directory", "runtime", false},
		{"not executable", "runtime/plain", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, dir := fixtureFrom(t, "workspace")
			if err := os.Mkdir(filepath.Join(dir, "runtime"), 0700); err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(dir, "runtime", "runner-go-check")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(dir, "runtime", "plain"), "dummy")
			path := tc.path
			if path == "absolute" {
				path = binary
			}
			if path != "" {
				data, err := os.ReadFile(l.Path)
				if err != nil {
					t.Fatal(err)
				}
				value, _ := json.Marshal(path)
				write(t, l.Path, strings.Replace(string(data), "  go_binary: go\n", "  go_binary: go\n  go_check_binary: "+string(value)+"\n", 1))
			}
			snapshot, err := l.Load()
			if !tc.valid {
				if err == nil || !strings.Contains(err.Error(), "codex.go_check_binary") {
					t.Fatalf("invalid Go check executable accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := ""
			if path != "" {
				want, err = filepath.EvalSymlinks(binary)
				if err != nil {
					t.Fatal(err)
				}
			}
			if snapshot.Instance.Codex.GoCheckBinary != want {
				t.Fatalf("Go check executable = %q, want %q", snapshot.Instance.Codex.GoCheckBinary, want)
			}
		})
	}
}

func TestCommandConfiguration(t *testing.T) {
	l, dir := fixtureFrom(t, "workspace")
	write(t, filepath.Join(dir, "projects/seats/roles/check.yaml"), `id: check
purpose: Check the selected source without a model.
inputs: {patch: artifact}
outputs: {check: text}
execution:
  harness: command
  access: workspace-read
  command:
    args: [/usr/bin/true]
    directory: frontend
    env: {TMPDIR: "{scratch}"}
`)
	s, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	if s.Projects["seats"].Roles["check"].Execution.Command.Network {
		t.Fatal("command networking must default to disabled")
	}
	s, err = l.Set("projects/seats/roles/check.yaml", "execution.command.network", json.RawMessage("true"))
	if err != nil || !s.Projects["seats"].Roles["check"].Execution.Command.Network {
		t.Fatal("explicit command networking was not selected", err)
	}
	for _, tc := range []struct {
		name, field string
		value       any
	}{
		{"no implicit shell", "execution.command.args", []string{"true"}},
		{"no source escape", "execution.command.directory", "../other"},
		{"no artifact escape", "execution.command.artifact", "../other"},
		{"no model", "execution.model", "gpt-6.1-sol"},
		{"read only source", "execution.access", "workspace-write"},
		{"artifact contract", "execution.command.artifact", "bundle.tgz"},
		{"environment names", "execution.command.env", map[string]string{"INVALID=NAME": "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.value)
			if _, err := l.Set("projects/seats/roles/check.yaml", tc.field, data); err == nil {
				t.Fatal("invalid command configuration accepted")
			}
		})
	}
}

func TestCommandNetworkingCannotExpandOtherHarnesses(t *testing.T) {
	for _, harness := range []string{"codex", "go-check", "git-candidate", "deterministic"} {
		t.Run(harness, func(t *testing.T) {
			p := runner.Profile{
				Harness: harness,
				Access:  "workspace-read",
				Command: &runner.Command{Network: true},
			}
			if err := ready(p); err == nil {
				t.Fatal("command network setting accepted by another harness")
			}
		})
	}
}

func TestReadOnlyReviewConfiguration(t *testing.T) {
	l, dir := fixtureFrom(t, "workspace")
	write(t, filepath.Join(dir, "projects/seats/roles/check.yaml"), `id: check
purpose: Review a retained candidate without changing source.
inputs: {patch: artifact}
outputs: {check: text}
execution:
  harness: codex
  access: workspace-read
  model: gpt-6.1-sol
  reasoning: xhigh
  max_input: 8KiB
  max_output: 2KiB
`)
	if _, err := l.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Set("projects/seats/roles/check.yaml", "execution.access", json.RawMessage(`"input-only"`)); err == nil {
		t.Fatal("input-only model accepted a source patch")
	}
	if _, err := l.Set("projects/seats/roles/check.yaml", "outputs.check", json.RawMessage(`"artifact"`)); err == nil {
		t.Fatal("read-only review claimed a writable source artifact")
	}
}

func TestWorkspaceWritePatchSeedConfiguration(t *testing.T) {
	l, dir := fixtureFrom(t, "workspace")
	role := filepath.Join(dir, "projects/seats/roles/implement.yaml")
	data, err := os.ReadFile(role)
	if err != nil {
		t.Fatal(err)
	}
	write(t, role, strings.Replace(string(data), "inputs: {}", "inputs: {patch: artifact}", 1))
	process := filepath.Join(dir, "projects/seats/processes/implement-and-check.yaml")
	data, err = os.ReadFile(process)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "inputs: {}", "inputs: {patch: artifact}", 1)
	text = strings.Replace(text, "    inputs: {}", "    inputs: {patch: {from_input: patch}}", 1)
	write(t, process, text)
	if _, err := l.Load(); err != nil {
		t.Fatal("workspace coding seed rejected", err)
	}
	if _, err := l.Set("projects/seats/roles/implement.yaml", "execution.access", json.RawMessage(`"input-only"`)); err == nil || !strings.Contains(err.Error(), "Codex inputs") {
		t.Fatal("input-only profile accepted workspace seed", err)
	}
	if _, err := l.Set("projects/seats/roles/implement.yaml", "inputs.other", json.RawMessage(`"artifact"`)); err == nil || !strings.Contains(err.Error(), "inputs do not match role") {
		t.Fatal("coding accepted an undeclared artifact binding", err)
	}
}

func TestWorkspaceWriteDeclaredEvidenceConfiguration(t *testing.T) {
	l, dir := fixtureFrom(t, "workspace")
	role := filepath.Join(dir, "projects/seats/roles/implement.yaml")
	data, err := os.ReadFile(role)
	if err != nil {
		t.Fatal(err)
	}
	write(t, role, strings.Replace(string(data), "inputs: {}", "inputs: {baseline: artifact}", 1))
	process := filepath.Join(dir, "projects/seats/processes/implement-and-check.yaml")
	data, err = os.ReadFile(process)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Replace(string(data), "inputs: {}", "inputs: {baseline: artifact}", 1)
	text = strings.Replace(text, "    inputs: {}", "    inputs: {baseline: {from_input: baseline}}", 1)
	write(t, process, text)
	if _, err = l.Load(); err != nil {
		t.Fatal("explicit retained evidence rejected for writable role", err)
	}
}
