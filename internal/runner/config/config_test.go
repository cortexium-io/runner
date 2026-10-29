package config

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// Workspace profiles own one fixed local check; they cannot accidentally acquire
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
