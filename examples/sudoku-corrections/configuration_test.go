package sudokucorrections_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Load the actual generated route without creating an executor or dispatching
// work. The selected executable is this test binary and is only statted.
func TestGeneratedCorrectionRouteLoadsAndFitsFrozenDefinitions(t *testing.T) {
	for _, file := range []string{"roles.mjs", "configure.mjs", "../../testdata/sudoku-corrections/configuration.mjs", "../sudoku-continuation/followup.mjs", "../sudoku-continuation/configure.mjs"} {
		if _, err := os.ReadFile(file); err != nil {
			t.Fatal(err)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for the synthetic configuration builder")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	policy := runner.DefaultStorage()
	policy.Database.MaxRecord = 40 << 10
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	script := `import {mkdir,writeFile} from 'node:fs/promises';
import {parentFixture,goalFixture} from '../../testdata/sudoku-corrections/configuration.mjs';
import {configureCorrections} from './configure.mjs';
const [root,binary,policy]=process.argv.slice(1),parent=parentFixture(root,binary),goal=goalFixture(parent);
await mkdir(parent.repository);
const oldRole={id:'legacy',purpose:'Preserved fixture',inputs:{request:'text'},outputs:{report:'text'},execution:{harness:'deterministic',mode:'echo',input:'request',output:'report'}};
const oldProcess={id:'legacy',inputs:{request:'text'},max_attempts:2,max_attempts_per_step:1,attempt_timeout:'30s',steps:[{id:'work',role:'legacy',inputs:{request:{from_input:'request'}}}],outputs:{result:{from_step:'work',output:'report'}}};
const project={id:parent.project_id,name:'Existing accepted project',workspace:parent.repository,default_process:'legacy',processes:{legacy:'legacy-process.json'},roles:{legacy:{file:'legacy-role.json'}}};
const instance={projects:['project.json'],concurrency:1,max_active_attempts:2,task_concurrency:2,enforce_work_limits:false,reconcile_interval:'5m',storage:JSON.parse(policy)};
for(const [name,value] of [['parent.json',parent],['goal.json',goal],['instance.json',instance],['project.json',project],['legacy-role.json',oldRole],['legacy-process.json',oldProcess]])await writeFile(root+'/'+name,JSON.stringify(value),{mode:0o600});
await writeFile(root+'/capture.mjs','// never executed fixture');
const staged=await configureCorrections({root:root+'/staged',instance:root+'/instance.json','parent-inspect':root+'/parent.json',goal:root+'/goal.json','capture-baseline':root+'/capture.mjs'});
console.log(JSON.stringify(staged));`
	output, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", script, root, binary, string(encoded)).CombinedOutput()
	if err != nil {
		t.Fatalf("generate configuration fixture: %v %s", err, output)
	}
	var staged struct{ Instance string }
	if err := json.Unmarshal(output, &staged); err != nil {
		t.Fatal(err)
	}
	loader, err := config.New(staged.Instance)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("staged correction configuration is incompatible with the current runtime", err)
	}
	project := snapshot.Projects["sudoku-preview"]
	if len(project.Processes) != 2 || project.DefaultProcess != "correct-sudoku-preview" || snapshot.Instance.EnforceWorkLimits || snapshot.Instance.Storage.Database.MaxRuns != 0 {
		t.Fatal("new route changed preserved processes or authorized optional policy")
	}
	expected := policy
	expected.Database.MaxRuns = 0
	if !reflect.DeepEqual(expected, snapshot.Instance.Storage) || snapshot.Instance.Concurrency != 1 || snapshot.Instance.MaxActiveAttempts != 2 || snapshot.Instance.TaskConcurrency != 2 {
		t.Fatal("staging widened byte safeguards or execution concurrency")
	}
	definition := project.Processes[project.DefaultProcess]
	roles := map[string]runner.Role{definition.Steps[0].Role: project.Roles[definition.Steps[0].Role]}
	profiles := map[string]string{}
	for _, name := range definition.Tasks.Roles {
		roles[name] = project.Roles[name]
		profiles[name] = "default"
	}
	profiles[definition.Steps[0].Role] = "default"
	_, frozen, err := runner.MarshalRunRecord(runner.Run{Process: definition, Roles: roles, Profiles: profiles})
	if err != nil || len(frozen) > int(policy.Database.MaxRecord) {
		t.Fatalf("aggregate frozen role/process definition exceeds unchanged record capacity: %d %v", len(frozen), err)
	}
	if len(roles) != 8 || project.Roles["sudoku-correction-research"].Execution.Native.Network != true || project.Roles["sudoku-correction-check"].Execution.Access != "workspace-read" {
		t.Fatal("configured role independence, research permission or fixed read-only executor differs")
	}
	t.Logf("new eight-role frozen definition: %d/%d bytes; no executor created", len(frozen), policy.Database.MaxRecord)
	queuedDefinitionRoundtrip(t, snapshot, `{"request":"`+strings.Repeat("x", 17280)+`"}`)
}

func TestPreparedCorrectionConfigurationLoads(t *testing.T) {
	file := os.Getenv("RUNNER_SUDOKU_CORRECTION_CONFIG")
	if file == "" {
		t.Skip("set RUNNER_SUDOKU_CORRECTION_CONFIG to the staged operator configuration")
	}
	if !filepath.IsAbs(file) {
		t.Fatal("staged configuration path must be absolute")
	}
	loader, err := config.New(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	project := snapshot.Projects["sudoku-preview"]
	if project.DefaultProcess != "correct-sudoku-preview" || len(project.Processes) != 3 || snapshot.Instance.Storage.Database.MaxRuns != 0 || snapshot.Instance.EnforceWorkLimits {
		t.Fatal("prepared overlay lost old routes or optional-count policy")
	}
	definition := project.Processes[project.DefaultProcess]
	roles := map[string]runner.Role{}
	for _, name := range append([]string{definition.Steps[0].Role}, definition.Tasks.Roles...) {
		roles[name] = project.Roles[name]
	}
	_, frozen, err := runner.MarshalRunRecord(runner.Run{Process: definition, Roles: roles, Profiles: map[string]string{}})
	if err != nil || len(frozen) > int(snapshot.Instance.Storage.Database.MaxRecord) {
		t.Fatalf("actual staged role catalog exceeds unchanged record capacity: %d %v", len(frozen), err)
	}
	t.Logf("actual staged configuration/old routes loaded; new frozen definition %d/%d bytes; no executor created", len(frozen), snapshot.Instance.Storage.Database.MaxRecord)
	requestFile := filepath.Join(filepath.Dir(filepath.Dir(file)), "requests", "correction.json")
	data, err := os.ReadFile(requestFile)
	if err != nil {
		t.Fatal(err)
	}
	var request struct {
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	queuedDefinitionRoundtrip(t, snapshot, request.Inputs["request"])
}

// This proves serialization/storage capacity only. It is deliberately not a
// second linked-intake authentication fixture and never constructs a Core.
func queuedDefinitionRoundtrip(t *testing.T, snapshot runner.Snapshot, goal string) {
	t.Helper()
	project := snapshot.Projects["sudoku-preview"]
	process := project.Processes[project.DefaultProcess]
	roles, profiles := map[string]runner.Role{}, map[string]string{}
	for _, name := range append([]string{process.Steps[0].Role}, process.Tasks.Roles...) {
		roles[name], profiles[name] = project.Roles[name], "default"
	}
	now := time.Now().UTC()
	r := runner.Run{
		ID: "run_capacity_fixture", ProjectID: project.ID, Repository: project.Workspace,
		RecordFormat: 9, Process: process, Roles: roles, Profiles: profiles,
		TaskGraph: &runner.TaskGraphState{RootRunID: "run_capacity_fixture", TaskID: "root", Brief: goal},
		Inputs:    map[string]runner.Value{"request": runner.Text(goal)},
		Status:    "Queued", Revision: 1, Created: now, Updated: now,
	}
	var artifacts []runner.Artifact
	for _, name := range []string{"patch", "checks", "prior", "handoff"} {
		id := "attempt_capacity_fixture/" + name
		r.Inputs[name] = runner.Value{ArtifactID: id}
		artifacts = append(artifacts, runner.Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: 1})
	}
	state, definition, err := runner.MarshalRunRecord(r)
	limit := int(snapshot.Instance.Storage.Database.MaxRecord)
	if err != nil || len(state) > limit || len(definition) > limit {
		t.Fatalf("queued goal/catalog exceeds unchanged record capacity: state=%d definition=%d limit=%d: %v", len(state), len(definition), limit, err)
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(private, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Save(t.Context(), r, artifacts, nil); err != nil {
		t.Fatal("queued goal/catalog cannot save in private SQLite", err)
	}
	loaded, err := store.Load(t.Context())
	if err != nil || len(loaded) != 1 || !reflect.DeepEqual(loaded[0], r) {
		t.Fatal("private queued goal/catalog roundtrip changed frozen data", err)
	}
	t.Logf("queued %dB goal saved/reloaded with state=%dB and definition=%dB under %dB; no Core/executor created", len(goal), len(state), len(definition), limit)
}
