package sudokucontinuation_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
)

// Exercise the real example and loader without invoking any configured worker.
// All selected executables are this test binary, which the loader only stats.
func TestGeneratedSudokuContinuationLoadsWithPreservedLegacyProject(t *testing.T) {
	// Node reads these fixtures, so register them with Go's test cache too.
	// Otherwise a changed role/process can reuse a previous loader result.
	for _, name := range []string{"roles.mjs", "process.json", "followup.mjs", "configure.mjs"} {
		if _, err := os.ReadFile(name); err != nil {
			t.Fatal(err)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required to generate the example configuration")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	script := `import {roleDefinitions} from './roles.mjs';
import {followupRoles,followupProcess} from './followup.mjs';
import {readFile} from 'node:fs/promises';
const root=process.argv[1],binary=process.argv[2];
const runtime={
 source:{repository:root+'/retained',base:'a'.repeat(40),tree:'b'.repeat(40)},
 sourceHelper:binary,checker:binary,codex:binary,go:binary,node:binary,
 data:root+'/existing-state',preview:root+'/preview',modules:root+'/modules',
 playwright:root+'/playwright.mjs',chromium:binary,webkit:binary,
 agreement:root+'/agreement.json',frontendHelper:binary,path:'/usr/bin:/bin',
 goCache:root+'/shared-cache',goModuleCache:root+'/shared-modules',opensslConf:'/dev/null',
 nativeWritePaths:[root+'/shared-cache'],checkWritePaths:[root+'/shared-cache',root+'/preview'],
 skills:[root+'/puzzle/SKILL.md',root+'/product/SKILL.md',root+'/interaction/SKILL.md'],
 selectors:['src/board.test.ts'],patchSHA256:'c'.repeat(64),checkSHA256:'d'.repeat(64),
 exclude:'docs/evidence,docs/benchmarks,docs/design,mobile/evidence',include:'docs/benchmarks/fixture.json'
};
const roles=roleDefinitions(runtime),processDefinition=JSON.parse(await readFile('process.json','utf8'));
const parent={id:'run_accepted',project_id:'sudoku-preview',status:'Completed',resources_released:true,revision:7,
 repository:root,attempts:[{id:'attempt_accepted',state:'finished',released:true}],task_graph:{root_run_id:'run_accepted'},
 inputs:{patch:{artifact_id:'attempt_original/changes.patch'},checks:{artifact_id:'attempt_check/result'}},
 roles:Object.fromEntries(roles.map(role=>[role.id,role])),process:processDefinition};
const followup=followupProcess(parent),extraRoles=followupRoles(parent,{checker:binary,preview:root+'/followup-preview',
 requestFile:root+'/followup-request.json',priorSHA256:'e'.repeat(64)});
process.stdout.write(JSON.stringify({roles:[...roles,...extraRoles],process:processDefinition,followup}));`
	output, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", script, root, binary).CombinedOutput()
	if err != nil {
		t.Fatalf("generate example documents: %s: %v", output, err)
	}
	var documents struct {
		Roles    []runner.Role  `json:"roles"`
		Process  runner.Process `json:"process"`
		Followup runner.Process `json:"followup"`
	}
	if err = json.Unmarshal(output, &documents); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	roles := map[string]runner.RoleBinding{}
	for _, role := range documents.Roles {
		write(role.ID+".json", role)
		roles[role.ID] = runner.RoleBinding{File: role.ID + ".json"}
	}
	write("process.json", documents.Process)
	write("followup-process.json", documents.Followup)
	write("preview.json", runner.ProjectFile{
		ID: "sudoku-preview", Name: "Configured preview", Workspace: root,
		DefaultProcess: documents.Process.ID,
		Processes:      map[string]string{documents.Process.ID: "process.json", documents.Followup.ID: "followup-process.json"}, Roles: roles,
	})
	write("legacy-role.json", runner.Role{
		ID: "legacy", Inputs: map[string]string{"request": "text"}, Outputs: map[string]string{"result": "text"},
		Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Input: "request", Output: "result"},
	})
	write("legacy-process.json", runner.Process{
		ID: "legacy", Inputs: map[string]string{"request": "text"}, MaxAttempts: 2, MaxAttemptsPerStep: 1, AttemptTimeout: "30s",
		Steps:   []runner.Step{{ID: "work", Role: "legacy", Inputs: map[string]runner.Binding{"request": {FromInput: "request"}}}},
		Outputs: map[string]runner.Binding{"result": {FromStep: "work", Output: "result"}},
	})
	write("legacy-project.json", runner.ProjectFile{
		ID: "legacy", Workspace: root, DefaultProcess: "legacy",
		Processes: map[string]string{"legacy": "legacy-process.json"}, Roles: map[string]runner.RoleBinding{"legacy": {File: "legacy-role.json"}},
	})
	policy := runner.DefaultStorage()
	policy.Database.MaxRecord = 40 << 10
	write("instance.json", runner.Instance{
		Projects:    []string{filepath.Join(root, "legacy-project.json"), "preview.json"},
		Concurrency: 1, MaxActiveAttempts: 2, ReconcileInterval: "1s", Storage: policy,
	})
	loader, err := config.New(filepath.Join(root, "instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loader.Load()
	if err != nil {
		t.Fatal("generated example is incompatible with Runner configuration:", err)
	}
	project := loaded.Projects["sudoku-preview"]
	command := project.Roles["sudoku-check"].Execution.Command
	if command.Result != "domain.json" || len(command.ExitOutcomes) != 0 || command.Artifact != "evidence.tar.gz" ||
		loaded.Instance.Concurrency != 1 || loaded.Instance.MaxActiveAttempts != 2 || len(loaded.Projects) != 2 ||
		project.Processes[documents.Process.ID].Tasks.MaxTasks != 20 {
		t.Fatal("loader changed the example's fixed result, admission or task contract")
	}
	admission := project.Roles["sudoku-followup-admit"].Execution.Command
	if len(project.Processes) != 2 || len(project.Processes[documents.Followup.ID].Inputs) != 5 ||
		admission.Args[2] != "admit-followup" || admission.Result != "domain.json" || admission.Artifact != "handoff.json" || admission.Network ||
		project.Roles["sudoku-followup-developer"].Execution.Model != project.Roles["sudoku-developer"].Execution.Model {
		t.Fatal("loader changed linked follow-up admission, inputs or native profile")
	}
}
