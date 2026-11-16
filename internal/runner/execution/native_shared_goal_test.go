//go:build darwin || linux

package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSharedGoalBindingPreservesOnlyRequestedAuthoritativeInput(t *testing.T) {
	a := harnessAssignment(t)
	a.Setup.Profile = runner.Profile{
		Harness: "command", Mode: "shared", Access: "workspace-read",
		Command: &runner.Command{Args: []string{"/bin/cat", "{goal}"}},
	}
	a.TaskContext = &runner.TaskContext{
		RootRunID: a.RunID, Goal: "Complete unchanged owner goal", Brief: "Narrower child brief",
		AllowedRoles: []runner.TaskRoleContract{{ID: "unused-catalog"}},
		Results:      []runner.RoleTaskResult{{ID: "unrelated", Outputs: map[string]runner.Value{"report": runner.Text("not requested")}}},
	}
	prepared, err := sharedAssignment(a)
	if err != nil || prepared.TaskContext.Goal != a.TaskContext.Goal || len(prepared.TaskContext.AllowedRoles) != 0 || len(prepared.TaskContext.Results) != 0 {
		t.Fatal("command goal lost authority or retained unrelated context", err)
	}
	prepared.Setup.Profile.Command.Args[1] = "literal"
	prepared, err = sharedAssignment(prepared)
	if err != nil || prepared.TaskContext.Goal != "" {
		t.Fatal("unselected goal was copied into fixed command context", err)
	}
	for _, bad := range []string{"prefix-{goal}", "cat {goal}"} {
		a.Setup.Profile.Command.Args[1] = bad
		if _, err := sharedAssignment(a); err == nil {
			t.Fatal("embedded goal binding interpolated a configured shell body", bad)
		}
	}
}

func TestSharedGoalStagingRejectsChangedReadOnlyInput(t *testing.T) {
	d := workspaceDisk(t)
	a := harnessAssignment(t)
	a.Setup.Profile = runner.Profile{
		Harness: "command", Mode: "shared", Access: "workspace-read",
		Command: &runner.Command{Args: []string{"/bin/cat", "{goal}"}},
	}
	a.TaskContext = &runner.TaskContext{Goal: "Exact opaque owner goal, preserved once"}
	n := &Native{disk: d}
	control := harnessControl(a.AttemptID)
	if err := n.stageSharedGoal(a, control); err != nil {
		t.Fatal(err)
	}
	spec := harnessSpec{Assignment: a, Control: filepath.Join(d.Root, control), Root: d.Root, Storage: d.Policy}
	path := sharedGoalPath(a, spec.Control)
	if err := validateSharedGoal(spec); err != nil || sharedValue("{goal}", spec) != path {
		t.Fatal("exact staged goal was not bound", err)
	}
	if err := n.stageSharedGoal(a, control); err != nil {
		t.Fatal("staging replay changed the same immutable input", err)
	}
	if !strings.Contains(harnessFilesystem(spec), `"`+path+`"="read"`) {
		t.Fatal("sandbox did not grant the exact goal read")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateSharedGoal(spec); err == nil {
		t.Fatal("writable goal input accepted")
	}
	if err := os.WriteFile(path, []byte("different goal"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := n.stageSharedGoal(a, control); err == nil {
		t.Fatal("replay silently replaced a changed frozen input")
	}
	if err := validateSharedGoal(spec); err == nil {
		t.Fatal("changed goal input accepted before execution")
	}
}
