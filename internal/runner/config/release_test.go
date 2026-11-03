package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestReleaseConfigurationRequiresBothGatesAndImmutableSequence(t *testing.T) {
	root := filepath.Join("..", "..", "..", "examples", "approved-release")
	var process runner.Process
	data, err := os.ReadFile(filepath.Join(root, "release.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &process); err != nil {
		t.Fatal(err)
	}
	project := runner.Project{Roles: map[string]runner.Role{}}
	for _, step := range process.Steps {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, &role); err != nil {
			t.Fatal(err)
		}
		project.Roles[step.Role] = role
		if err = ready(*role.Execution); err != nil {
			t.Fatal(err)
		}
	}
	if err = validateReleaseProcess(process, project); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*runner.Process)
	}{
		{"merge gate", func(p *runner.Process) {
			r := p.Steps[0].Routes["ready"]
			r.Approval = false
			p.Steps[0].Routes["ready"] = r
		}},
		{"production gate", func(p *runner.Process) {
			r := p.Steps[2].Routes["ready"]
			r.Approval = false
			p.Steps[2].Routes["ready"] = r
		}},
		{"skip backup", func(p *runner.Process) { p.Steps = append(p.Steps[:2], p.Steps[3:]...) }},
		{"retry", func(p *runner.Process) { p.MaxAttemptsPerStep = 2 }},
		{
			"mutable receipt",
			func(p *runner.Process) { p.Steps[3].Inputs["receipt"] = runner.Binding{FromInput: "receipt"} },
		},
		{"start at deploy", func(p *runner.Process) { p.Steps[0].Role = "deploy" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := runner.Clone(process)
			test.change(&p)
			if validateReleaseProcess(p, project) == nil {
				t.Fatal("unsafe release policy accepted")
			}
		})
	}
}

func TestReleaseOperatorCommandIsExplicitAndBounded(t *testing.T) {
	l, _ := fixture(t)
	if _, err := l.Load(); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"relative"}, {"/does/not/exist"}, {"/bin/sh", string([]byte{0})}, {"/bin/sh", string(make([]byte, 4097))}} {
		value, _ := json.Marshal(map[string]any{"command": command})
		if _, err := l.Set("instance.yaml", "release", value); err == nil {
			t.Fatal("invalid release command accepted")
		}
	}
	value, _ := json.Marshal(map[string]any{"command": []string{"/bin/sh"}})
	if _, err := l.Set("instance.yaml", "release", value); err != nil {
		t.Fatal(err)
	}
}
