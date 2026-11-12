package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/guidesmedia"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestGuidesStagingIsClosedAndCompleteRouteRefuses(t *testing.T) {
	var p runner.Process
	var role runner.Role
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	for file, dest := range map[string]any{"stage-guides-media.json": &p, "roles/guides-media-import.json": &role} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(data, dest); err != nil {
			t.Fatal(err)
		}
	}
	role.Execution.GuidesImportSHA256 = strings.Repeat("a", 64)
	project := runner.Project{ID: "jacks-break", Roles: map[string]runner.Role{role.ID: role}, Profiles: map[string]string{}}
	if err := ready(*role.Execution); err != nil {
		t.Fatal(err)
	}
	if err := validateProcess(p, project); err != nil {
		t.Fatal(err)
	}
	if types(role.Outputs) == nil || !closedGuidesImportRole(role, "") {
		t.Fatal("fixed36-file contract changed the generic8-output bound")
	}
	for name, change := range map[string]func(*runner.Process){
		"retry":    func(p *runner.Process) { p.InfrastructureRetries = 1 },
		"attempts": func(p *runner.Process) { p.MaxAttempts = 2 },
		"deadline": func(p *runner.Process) { p.AttemptTimeout = "21m" },
		"source":   func(p *runner.Process) { p.Inputs["source"] = "text" },
		"outputs":  func(p *runner.Process) { p.Outputs["raw"] = runner.Binding{FromStep: "import", Output: "sudoku_video"} },
		"route":    func(p *runner.Process) { p.Steps[0].Routes["ready"] = runner.Route{Next: "$complete", Approval: true} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := runner.Clone(p)
			change(&changed)
			if validateGuidesProcess(changed, project) == nil {
				t.Fatal("changed staging graph accepted")
			}
		})
	}
	changed := runner.Clone(p)
	changed.ID = "publish-guides-media"
	changed.GuidesMedia = guidesmedia.AssetFormat
	if err := validateProcess(changed, project); err == nil || !strings.Contains(err.Error(), "unqualified") {
		t.Fatal("unqualified complete publication admitted", err)
	}
	for _, change := range []func(*runner.Profile){
		func(p *runner.Profile) { p.Command.Args = append(p.Command.Args, "--source", "/private") },
		func(p *runner.Profile) { p.Command.Network = true },
		func(p *runner.Profile) { p.Command.Env = map[string]string{"TOKEN": "untrusted"} },
		func(p *runner.Profile) { p.GuidesImportSHA256 = "" },
	} {
		p := runner.Clone(*role.Execution)
		change(&p)
		if ready(p) == nil {
			t.Fatal("open importer profile accepted")
		}
	}
	ordinary := runner.Clone(*role.Execution)
	ordinary.Mode = ""
	if ready(ordinary) == nil {
		t.Fatal("import pin leaked into ordinary command")
	}
}
