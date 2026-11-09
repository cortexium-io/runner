package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestCommittedSourceBaselineNeedsNoFabricatedPatchAndKeepsAdmission(t *testing.T) {
	_, project := deliveryFixture(t)
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var process runner.Process
	data, err := os.ReadFile(filepath.Join(root, "investigate-source.json"))
	if err != nil || json.Unmarshal(data, &process) != nil {
		t.Fatal("source investigation unavailable", err)
	}
	for _, id := range []string{"delivery-source-baseline", "investigation-owner", "investigation-review"} {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", id+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("source investigation role unavailable", id, err)
		}
		project.Roles[id] = role
	}
	validate := func(p runner.Process) error {
		return errors.Join(validateProcess(p, project), validateReleaseProcess(p, project))
	}
	if _, exists := process.Inputs["patch"]; exists {
		t.Fatal("committed baseline requires a fabricated patch")
	}
	if err = validate(process); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*runner.Process){
		func(p *runner.Process) { p.Steps[0].Inputs["package"] = runner.Binding{FromCandidate: true} },
		func(p *runner.Process) { p.Steps[0].Inputs["patch"] = runner.Binding{FromInput: "patch"} },
		func(p *runner.Process) { p.Inputs["patch"] = "text" },
		func(p *runner.Process) { p.Steps[0].Routes["ready"] = runner.Route{Next: "owner"} },
		func(p *runner.Process) { p.Steps[1].Routes["ready"] = runner.Route{Next: "publish"} },
	} {
		bad := runner.Clone(process)
		mutate(&bad)
		if validate(bad) == nil {
			t.Fatal("baseline accepted an undeclared source or bypassed admission")
		}
	}
}
