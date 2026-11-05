//go:build darwin || linux

package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Real source restoration, commands, measurements, bindings and release gates;
// only model judgments and remote effects are scripted. No product is executed.
func TestRetainedProductRestoresBeforeReadinessAndMeasuresBeforeModel(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	build := func(name, pkg string) string {
		t.Helper()
		binary := filepath.Join(t.TempDir(), name)
		command := exec.Command("go", "build", "-o", binary, pkg)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
		return binary
	}
	provider, measureBinary := build("provider", "./testdata/release-operator"), build("measure", "./cmd/runner-measure")
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	repo := sourceRepository(t)
	benchmark := `package seats
import("encoding/json";"os";"path/filepath";"testing")
func BenchmarkFixtureEvidence(b *testing.B){
 stage,err:=os.ReadFile("stage.txt");if err!=nil{b.Fatal(err)}
 data,err:=json.Marshal(map[string]string{"stage":string(stage)});if err!=nil{b.Fatal(err)}
 if err=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"request.json"),data,0600);err!=nil{b.Fatal(err)}
 b.ReportMetric(1,"samples/op")
}
`
	for name, data := range map[string]string{"seats.go": seatsImplementation, "stage.txt": "baseline", "evidence_test.go": benchmark} {
		if err = os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "original measured source")
	head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	candidate := filepath.Join(t.TempDir(), "candidate")
	fixtureGit(t, repo, "worktree", "add", "--detach", candidate, head)
	for name, data := range map[string]string{"stage.txt": "candidate", "seats.go": strings.ReplaceAll(seatsImplementation, "n<=0", "n<0")} {
		if err = os.WriteFile(filepath.Join(candidate, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, candidate, "add", "--all")
	patch := fixtureGit(t, candidate, "diff", "--cached", "--binary", "--full-index", head, "--")
	fixtureGit(t, repo, "worktree", "remove", "--force", candidate)
	remote := filepath.Join(t.TempDir(), "remote.git")
	fixtureGit(t, repo, "init", "--bare", "--quiet", remote)
	fixtureGit(t, repo, "remote", "add", "origin", remote)
	fixtureGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	cfg := workspaceSnapshot(t, repo)
	cfg.Instance.Storage.Database.MaxRecord = 40 << 10
	cfg.Instance.Storage.Database.MaxSize = 48 << 20
	cfg.Instance.Storage.Database.MaxRuns = 128
	cfg.Instance.Release = &runner.ReleaseSettings{Command: []string{provider}, GitHub: "/usr/bin/true"}
	project := cfg.Projects["demo"]
	examples := filepath.Join(root, "examples", "jacks-break")
	var process runner.Process
	data, err := os.ReadFile(filepath.Join(examples, "retained-product.json"))
	if err != nil || json.Unmarshal(data, &process) != nil {
		t.Fatal("retained process unavailable", err)
	}
	for _, step := range process.Steps {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(examples, "roles", step.Role+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("role unavailable", step.Role, err)
		}
		project.Roles[role.ID] = role
	}
	args := []string{measureBinary, "--go", goBinary, "--source", "{workspace}", "--scratch", "{scratch}", "--package", "{text.package}", "--benchmark", "{text.benchmark}"}
	measure := project.Roles["delivery-measure"]
	measure.Execution.Command = &runner.Command{Args: append(append([]string{}, args...), "--previous", "{text.previous}"), Result: "domain.json", Artifact: "measurements.json"}
	project.Roles[measure.ID] = measure
	baseline := runner.Clone(measure)
	baseline.ID, baseline.Inputs = "original-baseline", map[string]string{"package": "text", "benchmark": "text"}
	baseline.Execution.Command.Args = append(append([]string{}, args...), "--baseline")
	project.Roles[baseline.ID] = baseline
	project.Roles["retain"] = runner.Role{ID: "retain", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(patch)}}
	check := project.Roles["delivery-check"]
	check.Execution.Command = &runner.Command{Args: []string{goBinary, "test", "-count=1", "./...", "-run", "{text.tests}"}, ExitOutcomes: map[int]string{1: "changes_requested"}}
	project.Roles[check.ID] = check
	frontend := project.Roles["frontend-check"]
	frontend.Execution.Command = &runner.Command{Args: []string{"/bin/sh", "-c", `outcome=ready; if [ "$(cat stage.txt)" = candidate ]; then outcome=changes_requested; fi
printf '{"outcome":"%s","reason":"fixture source assertion"}\n' "$outcome" > "$TMPDIR/domain.json"
printf '{"fixture":true,"outcome":"%s"}\n' "$outcome" > "$TMPDIR/frontend-check.json"
printf 'fixture frontend assertion: %s\n' "$outcome"`}, Result: "domain.json", Artifact: "frontend-check.json"}
	project.Roles[frontend.ID] = frontend
	project.Processes = map[string]runner.Process{process.ID: process, "original": {
		ID: "original", Inputs: map[string]string{"package": "text", "benchmark": "text"},
		Steps: []runner.Step{
			{ID: "baseline", Role: baseline.ID, Inputs: map[string]runner.Binding{"package": {FromInput: "package"}, "benchmark": {FromInput: "benchmark"}}, Routes: map[string]runner.Route{"ready": {Next: "retain"}, "continue": {Next: "retain"}}},
			{ID: "retain", Role: "retain", Inputs: map[string]runner.Binding{}, Routes: map[string]runner.Route{"ready": {Next: "$fail"}}},
		},
		MaxAttempts: 2, MaxAttemptsPerStep: 1, AttemptTimeout: "2m",
	}}
	project.DefaultProcess, project.Source = process.ID, &runner.SourceSettings{Ref: head}
	cfg.Projects["demo"] = project
	operator := t.TempDir()
	t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
	t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", head)
	t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
	t.Setenv("RUNNER_RELEASE_FIXTURE_MODE", "complete")
	approved := goodTurn()
	approved.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Fixture source approved"},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{goodTurn(), approved}}
	var originalBytes []byte
	script.onTurn = func(dir string) {
		if script.starts != 0 {
			return
		}
		staged := func(name string) []byte {
			files, err := filepath.Glob(filepath.Join(dir, ".runner-tmp", "inputs", "*", name))
			if err != nil || len(files) != 1 {
				t.Error("owner missing explicit evidence", name, files, err)
				return nil
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Error(err)
				return nil
			}
			return data
		}
		if !bytes.Equal(staged("baseline"), originalBytes) {
			t.Error("original baseline was rerun or replaced with candidate measurements")
		}
		var current struct {
			Baseline bool                              `json:"baseline"`
			Round    int                               `json:"round"`
			Evidence map[string]struct{ Stage string } `json:"evidence"`
		}
		if json.Unmarshal(staged("measurement"), &current) != nil || current.Baseline || current.Round != 1 || current.Evidence["request.json"].Stage != "candidate" {
			t.Error("owner did not receive actual measured restored source", current)
		}
		for name, data := range map[string]string{"seats.go": seatsImplementation, "stage.txt": "repaired"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
				t.Error(err)
			}
		}
	}
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err = os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hook := func(s *Service) {
		s.releases.cfg = cfg.Instance.Release
		s.commands.cfg.Binary = sandbox
		s.commands.launchWorker = func(file string, finished func()) error {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+file)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); finished() }()
			return nil
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script, hook)
	defer rig.close()
	original, err := rig.core.Start(runner.StartRequest{RequestID: "original", Project: "demo", Process: "original", Inputs: map[string]runner.Value{"package": runner.Text("."), "benchmark": runner.Text("^BenchmarkFixtureEvidence$")}})
	if err != nil {
		t.Fatal(err)
	}
	original = waitSourceRun(t, rig.core, original.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
	if original.Status != "Failed" || len(original.Attempts) != 2 || script.starts != 0 || original.Attempts[0].Source.Base != head {
		t.Fatal("original source evidence did not settle without model calls", original.Status, original.Reason)
	}
	baselineID := original.Attempts[0].End.Result.Outputs["artifact"].ArtifactID
	patchID := original.Attempts[1].End.Result.Outputs["patch"].ArtifactID
	originalBytes, err = rig.disk.Read("artifacts/"+baselineID, cfg.Instance.Storage.MaxArtifact)
	if err != nil || !bytes.Contains(originalBytes, []byte(`"baseline":true`)) {
		t.Fatal("original baseline identity unavailable", err)
	}
	r, err := rig.core.Start(runner.StartRequest{RequestID: "retained", Project: "demo", SourceRef: head, Inputs: map[string]runner.Value{
		"request": runner.Text("Correct measured fixture source rejection; preserve the original baseline."), "package": runner.Text("."), "tests": runner.Text("^TestParseSeats$"), "frontend_tests": runner.Text(`["fixture.test.ts"]`), "benchmark": runner.Text("^BenchmarkFixtureEvidence$"),
		"patch": {ArtifactID: patchID}, "original_baseline": {ArtifactID: baselineID},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"prepare", "backup"} {
		waiting := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() || r.Decision != nil }, time.Minute)
		if waiting.Decision == nil || waiting.Decision.Step != step {
			t.Fatal("protected fixture gate missing", step, waiting.Status, waiting.Reason)
		}
		if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "approve-" + step, RunID: r.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
	if end.Status != "Completed" || script.starts != 2 || len(end.Attempts) != 20 || end.Attempts[2].End.Result.Outcome != "changes_requested" || end.Attempts[3].End.Result.Outcome != "changes_requested" {
		t.Fatal("checks did not precede bounded correction/delivery", end.Status, end.Reason, len(end.Attempts), script.starts)
	}
	for i, id := range []string{"retained-measure", "admit", "retained-frontend-check", "retained-check", "owner"} {
		if end.Process.Steps[end.Attempts[i].Step].ID != id {
			t.Fatal("source seed/check/model ordering changed", id)
		}
	}
	lastMeasure := end.Attempts[7].End.Result.Outputs["artifact"].ArtifactID
	measured, err := rig.disk.Read("artifacts/"+lastMeasure, cfg.Instance.Storage.MaxArtifact)
	if err != nil || !bytes.Contains(measured, []byte(`"round":2`)) || bytes.Contains(measured, []byte(`"baseline":true`)) {
		t.Fatal("post-owner measurement reran the original baseline or lost its round", string(measured), err)
	}
	preserved, err := rig.core.Get(original.ID)
	stillOriginal, readErr := rig.disk.Read("artifacts/"+baselineID, cfg.Instance.Storage.MaxArtifact)
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if err != nil || readErr != nil || preserved.Status != "Failed" || !bytes.Equal(stillOriginal, originalBytes) || strings.Count(string(calls), "publish\n") != 1 {
		t.Fatal("continuation changed original history/evidence or duplicated publication", err, readErr, string(calls))
	}
}
