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
process.stdout.write(JSON.stringify({roles:roleDefinitions(runtime),process:JSON.parse(await readFile('process.json','utf8'))}));`
	output, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", script, root, binary).CombinedOutput()
	if err != nil {
		t.Fatalf("generate example documents: %s: %v", output, err)
	}
	var documents struct {
		Roles   []runner.Role  `json:"roles"`
		Process runner.Process `json:"process"`
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
	write("preview.json", runner.ProjectFile{
		ID: "sudoku-preview", Name: "Configured preview", Workspace: root,
		DefaultProcess: documents.Process.ID,
		Processes:      map[string]string{documents.Process.ID: "process.json"}, Roles: roles,
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
}
