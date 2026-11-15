package runner_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestGeneralTasksSharedFixedCommandReceivesIntegratedWorkspace(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	p.Roles["checker"] = runner.Role{
		ID: "checker", Purpose: "Run the configured independent check", Inputs: map[string]string{}, Outputs: map[string]string{"check": "text"},
		Execution: &runner.Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &runner.Command{Args: []string{"/usr/bin/true", "{workspace}", "{scratch}"}, RuntimeWritePaths: []string{t.TempDir()}}},
	}
	proc := p.Processes["demo"]
	proc.Tasks.Roles = append(proc.Tasks.Roles, "checker")
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	var failure string
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			if a.TaskContext.TaskID == "root" {
				if len(a.TaskContext.Results) == 0 {
					return graphAction("delegate", runner.RoleTask{ID: "fixed-check", Role: "checker", Brief: "Run the independent checks", Inputs: map[string]runner.Value{}})
				}
				if len(a.TaskContext.Results) != 1 || a.TaskContext.Results[0].Role != "checker" || string(a.TaskContext.Results[0].Outputs["check"].Inline) != `"fixed checks completed"` {
					failure = "root did not receive the canonical configured-check result"
				}
				return graphFinal("assessed the check against the goal")
			}
			if a.WorkDir != p.Workspace || a.Repository != p.Workspace || a.Source != nil || a.Setup.Profile.Mode != "shared" || a.Setup.Profile.Command.Args[1] != "{workspace}" {
				failure = "fixed check did not retain the shared source and configured argv"
			}
			for _, role := range a.TaskContext.AllowedRoles {
				if role.ID == "checker" && (role.Harness != "command" || role.Mode != "shared" || role.Access != "workspace-read" || role.Model != "") {
					failure = "planner was not given the fixed check's actual execution mode"
				}
			}
			encoded, _ := json.Marshal(a.TaskContext.AllowedRoles)
			if strings.Contains(string(encoded), "/usr/bin/true") {
				failure = "role catalog copied private configured command arguments"
			}
			return graphReturned(runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"check": runner.Text("fixed checks completed")}})
		}}
	})
	defer r.close()
	root := begin(t, r.core, "shared-fixed-check", "demo")
	done := await(t, r.core, root.ID, released)
	if failure != "" || done.Status != "Completed" || len(done.TaskStatuses) != 1 {
		t.Fatalf("shared fixed command broke task semantics: %s %s", failure, done.Reason)
	}
}
