package frontendreference_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// Only the existing internal fixed-command worker runs in child test processes.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "native-worker" {
		if err := execution.RunNativeWorker(context.Background(), os.Args[2]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func configuredFixture(t *testing.T) runner.Snapshot {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for project-owned reference qualification")
	}
	if selected := os.Getenv("RUNNER_FRONTEND_REFERENCE_NODE"); selected != "" {
		node = selected
	}
	node, err = filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	policy := runner.DefaultStorage()
	policy.MinFree = 0
	policy.Database.MaxRecord = 40 << 10
	encoded, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	script := `import {referenceFixture} from '../../testdata/frontend-reference/configuration.mjs';
import {configureReference} from './configure.mjs';
const [root,binary,policy,node]=process.argv.slice(1);
const instance=await referenceFixture(root,{binary,policy:JSON.parse(policy)});
console.log(JSON.stringify(await configureReference({root:root+'/staged',instance,node})));`
	out, err := exec.CommandContext(t.Context(), node, "--input-type=module", "-e", script, root, binary, string(encoded), node).CombinedOutput()
	if err != nil {
		t.Fatalf("generate reference configuration: %v %s", err, out)
	}
	var manifest struct{ Instance string }
	if err := json.Unmarshal(out, &manifest); err != nil {
		t.Fatal(err)
	}
	loader, err := config.New(manifest.Instance)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("future reference and complete protected delivery routes must load", err)
	}
	return snapshot
}

func TestFrontendReferenceConfigurationPreservesCompleteProcessAndFrozenPolicy(t *testing.T) {
	snapshot := configuredFixture(t)
	project := snapshot.Projects["jacks-break"]
	if len(project.Processes) != 2 || project.DefaultProcess != "deliver-planned-frontend" || snapshot.Instance.EnforceWorkLimits || snapshot.Instance.Storage.Database.MaxRuns != 0 {
		t.Fatal("staging changed existing default or policy")
	}
	process := project.Processes["deliver-planned-frontend"]
	if project.Roles[process.Steps[len(process.Steps)-1].Role].Execution.Mode != "verify" || project.Roles["frontend-request-experience-review"].Execution.Mode != "experience-review" {
		t.Fatal("complete qualification/review/release suffix lost")
	}
	assertFrozenDefinition(t, project, snapshot.Instance.Storage.Database.MaxRecord)
}

func assertFrozenDefinition(t *testing.T, project runner.Project, limit runner.Bytes) {
	t.Helper()
	process := project.Processes["deliver-planned-frontend"]
	roles, profiles := map[string]runner.Role{}, map[string]string{}
	var collect func([]runner.Step)
	collect = func(steps []runner.Step) {
		for _, step := range steps {
			if step.ForEach != nil {
				collect(step.ForEach.Steps)
				continue
			}
			roles[step.Role], profiles[step.Role] = project.Roles[step.Role], project.Profiles[step.Role]
		}
	}
	collect(process.Steps)
	_, frozen, err := runner.MarshalRunRecord(runner.Run{Process: process, Roles: roles, Profiles: profiles})
	if err != nil || len(frozen) > int(limit) {
		t.Fatal("new future review prose exceeds unchanged frozen record bound", len(frozen), err)
	}
	t.Logf("complete future frontend definition: %d/%d bytes; no product work dispatched", len(frozen), limit)
}

func preparedSnapshot(t *testing.T) runner.Snapshot {
	t.Helper()
	file := os.Getenv("RUNNER_FRONTEND_REFERENCE_CONFIG")
	if file == "" {
		t.Skip("set RUNNER_FRONTEND_REFERENCE_CONFIG for the staged operator profile")
	}
	loader, err := config.New(file)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := loader.Load()
	if err != nil {
		t.Fatal("staged operator reference overlay failed validation", err)
	}
	return snapshot
}

func TestPreparedFrontendReferenceConfiguration(t *testing.T) {
	snapshot := preparedSnapshot(t)
	project := snapshot.Projects["jacks-break"]
	if project.Processes["stage-frontend-reference"].Tasks == nil || project.Roles["review-evidence-stage"].Execution.Command == nil || project.Roles["frontend-request-experience-review"].Execution.Mode != "experience-review" || project.Roles["experience-gate"].Execution.Mode != "experience-gate" {
		t.Fatal("new reference route, preserved legacy stager or protected review/gate is missing")
	}
	if snapshot.Instance.EnforceWorkLimits || snapshot.Instance.Storage.Database.MaxRuns != 0 {
		t.Fatal("staging replaced optional policy with a total-work count cap")
	}
	assertFrozenDefinition(t, project, snapshot.Instance.Storage.Database.MaxRecord)
	t.Logf("validated staged operator overlay with %d projects and %d unchanged-or-cloned Jack's Break routes; no live state opened", len(snapshot.Projects), len(project.Processes))
}

func TestFrontendReferenceSharedCommandArtifactLifecycleAndProjectReuse(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("this fixed profile qualification uses the configured macOS sandbox")
	}
	snapshot := configuredFixture(t)
	// The fixture retains the full configured route above. Execute only the new
	// fixed stager and a deterministic consumer; no product/model/release call.
	project := snapshot.Projects["jacks-break"]
	if os.Getenv("RUNNER_FRONTEND_REFERENCE_CONFIG") != "" {
		// Qualify the exact staged fixed argv/profile in a disposable workspace;
		// never construct the live service or execute the complete product route.
		prepared := preparedSnapshot(t).Projects[project.ID]
		project.Roles["stage-frontend-reference"] = runner.Clone(prepared.Roles["stage-frontend-reference"])
		project.Processes["stage-frontend-reference"] = runner.Clone(prepared.Processes["stage-frontend-reference"])
	}
	consumer := runner.Role{
		ID: "consume-reference", Purpose: "Model-free proof of same-project retained reference reuse",
		Inputs: map[string]string{"reference": "artifact"}, Outputs: map[string]string{"reference": "text"},
		Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Input: "reference", Output: "reference"},
	}
	project.Roles[consumer.ID] = consumer
	project.Processes[consumer.ID] = runner.Process{
		ID: consumer.ID, Inputs: runner.Clone(consumer.Inputs),
		Steps:       []runner.Step{{ID: "read", Role: consumer.ID, Inputs: map[string]runner.Binding{"reference": {FromInput: "reference"}}}},
		Outputs:     map[string]runner.Binding{"reference": {FromStep: "read", Output: "reference"}},
		MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "1m",
	}
	snapshot.Projects[project.ID] = project
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(root, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	disk, err := runner.OpenDisk(root, snapshot.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	service := execution.NewService(disk, func(_ context.Context, id string) ([]byte, error) {
		return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	}, nil)
	defer service.Close(context.Background())
	core, err := runner.New(t.Context(), store, service, disk, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	service.SetWake(core.ExecutionChanged)
	defer core.Close()
	request := "Owner’s exact brief\r\nUse https://example.invalid/owner-reference; no observed proof is claimed. 🙂\n"
	started, err := core.Start(runner.StartRequest{RequestID: "reference-once", Project: project.ID, Process: "stage-frontend-reference", Inputs: map[string]runner.Value{"request": runner.Text(request)}})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(id string) runner.Run {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		for {
			r, err := core.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if r.Terminal() && r.ResourcesReleased {
				return r
			}
			if _, err := core.Wait(ctx, id, r.Revision); err != nil {
				t.Fatal("fixed reference command did not settle", err, r.Status, r.Reason)
			}
		}
	}
	done := wait(started.ID)
	if done.Status != "Completed" || len(done.Attempts) != 1 || !done.Attempts[0].Released || done.Attempts[0].Effective == nil || done.Attempts[0].Effective.Provenance == "" {
		t.Fatal("reference fixed command did not complete once with recorded provenance", done.Status, done.Reason)
	}
	proof, err := disk.Read("artifacts/"+done.Attempts[0].Effective.Provenance, disk.Policy.MaxArtifact)
	var provenance runner.NativeProvenance
	if err != nil || json.Unmarshal(proof, &provenance) != nil || provenance.Harness != "command/shared" || provenance.Access != "workspace-read" || provenance.Permitted.Network || len(provenance.Permitted.RuntimeWritePaths) != 0 {
		t.Fatal("fixed reference execution acquired model, network or broad write authority", err)
	}
	t.Logf("fixed profile: binary=%s version=%s artifact=%s; model-free, offline, read-only, no runtime write grants", provenance.Binary, provenance.Version, done.Outputs["review_evidence"].ArtifactID)
	id := done.Outputs["review_evidence"].ArtifactID
	bytes, err := disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	var reference struct {
		Kind          string `json:"kind"`
		Status        string `json:"status"`
		Request       string `json:"request"`
		RequestSHA256 string `json:"request_sha256"`
	}
	digest := sha256.Sum256([]byte(request))
	if err != nil || json.Unmarshal(bytes, &reference) != nil || reference.Kind != "owner-brief" || reference.Status != "ready" || reference.Request != request || reference.RequestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("retained reference does not match the exact immutable goal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "work", done.Attempts[0].ID, "harness", "goal.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled stager retained disposable goal input", err)
	}
	before, err := store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := core.Start(runner.StartRequest{RequestID: "reference-once", Project: project.ID, Process: "stage-frontend-reference", Inputs: map[string]runner.Value{"request": runner.Text(request)}})
	if err != nil || replayed.ID != done.ID || len(replayed.Attempts) != 1 {
		t.Fatal("same intake reran the stager", err)
	}
	after, err := store.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("same intake mutated retained evidence", err)
	}
	consumed, err := core.Start(runner.StartRequest{RequestID: "consume-once", Project: project.ID, Process: consumer.ID, Inputs: map[string]runner.Value{"reference": {ArtifactID: id}}})
	if err != nil {
		t.Fatal("same-project reference was unavailable to normal intake", err)
	}
	used := wait(consumed.ID)
	var read string
	if used.Status != "Completed" || json.Unmarshal(used.Outputs["reference"].Inline, &read) != nil || read != string(bytes) {
		t.Fatal("same-project consumer could not read the exact retained reference", used.Status, used.Reason)
	}
	if owner, err := store.Artifact(t.Context(), id); err != nil || owner.RunID != done.ID {
		t.Fatal("reference reuse changed canonical artifact ownership", err)
	}
}
