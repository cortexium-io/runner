//go:build darwin || linux

package execution

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestInvestigationPayloadFitsExistingLimit(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", "roles", "investigation-owner.json"))
	var role runner.Role
	if err != nil || json.Unmarshal(data, &role) != nil {
		t.Fatal("investigation role unavailable", err)
	}
	a := runner.Assignment{
		Task:        role.Purpose,
		Setup:       runner.ExecutionSetup{Profile: *role.Execution},
		OutputTypes: role.Outputs,
		Inputs: map[string]runner.Value{
			"request":       runner.Text(strings.Repeat("r", 2200)),
			"baseline":      runner.Text(strings.Repeat("b", 1400)),
			"check":         runner.Text(strings.Repeat("c", 256)),
			"measurement":   runner.Text(strings.Repeat("m", 1024)),
			"review":        runner.Text(strings.Repeat("v", 240)),
			"qualification": runner.Value{Inline: []byte(`{"key":"retained_qualification","head":"` + strings.Repeat("a", 40) + `"}`)},
		},
	}
	if _, _, err = nativePayload(a); err != nil {
		t.Fatal("bounded investigation feedback exceeds the unchanged profile", err)
	}
}

// The core/native/command/Git/provider boundary is real; judgments and remote
// effects are scripted. The provider never contacts GitHub or production.
func TestConnectedDeliveryCorrectionAndRecoveryDecisions(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "provider")
	build := exec.Command("go", "build", "-o", binary, "./testdata/release-operator")
	build.Dir = filepath.Join("..", "..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
	for _, mode := range []string{"complete", "qa", "backup", "migration", "retry", "approval", "uncertain", "all", "baseline"} {
		t.Run(mode, func(t *testing.T) {
			repo := sourceRepository(t)
			head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
			remote := filepath.Join(t.TempDir(), "remote.git")
			fixtureGit(t, repo, "init", "--bare", "--quiet", remote)
			fixtureGit(t, repo, "remote", "add", "origin", remote)
			fixtureGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
			cfg := workspaceSnapshot(t, repo)
			cfg.Instance.Storage.Database.MaxRecord = 40 << 10
			cfg.Instance.Storage.Database.MaxSize = 48 << 20
			cfg.Instance.Storage.Database.MaxRuns = 128
			cfg.Instance.Release = &runner.ReleaseSettings{Command: []string{binary}, GitHub: "/usr/bin/true"}
			project := cfg.Projects["demo"]
			root := filepath.Join("..", "..", "..", "examples", "jacks-break")
			var process runner.Process
			processFile := "process.json"
			if mode == "baseline" {
				processFile = "investigate-retained.json"
			}
			data, err := os.ReadFile(filepath.Join(root, processFile))
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(data, &process); err != nil {
				t.Fatal(err)
			}
			for _, step := range process.Steps {
				var role runner.Role
				data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(data, &role); err != nil {
					t.Fatal(err)
				}
				project.Roles[role.ID] = role
			}
			check := project.Roles["delivery-check"]
			goBinary, err := exec.LookPath("go")
			if err != nil {
				t.Fatal(err)
			}
			check.Execution.Command = &runner.Command{Args: []string{goBinary, "test", "-count=1", "./..."}, ExitOutcomes: map[int]string{1: "changes_requested"}}
			project.Roles[check.ID] = check
			measure := project.Roles["delivery-measure"]
			measure.Execution.Command = &runner.Command{Args: []string{"/bin/sh", "-c", `printf '%s\n' '{"round":1,"fixture":true}' > "$TMPDIR/measurements.json"
printf '%s\n' '{"outcome":"ready","reason":"scripted measurement; real measurement is qualified separately"}' > "$TMPDIR/domain.json"
printf 'fixture measurement retained\n'`}, Artifact: "measurements.json", Result: "domain.json"}
			project.Roles[measure.ID] = measure
			if mode == "baseline" {
				helper := filepath.Join(t.TempDir(), "measure")
				cmd := exec.Command("go", "build", "-o", helper, "./cmd/runner-measure")
				cmd.Dir = filepath.Join("..", "..", "..")
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatal(err, string(output))
				}
				args := []string{helper, "--go", goBinary, "--source", "{workspace}", "--scratch", "{scratch}", "--package", ".", "--benchmark", "^BenchmarkBaseline$"}
				baseline := project.Roles["delivery-baseline"]
				baseline.Execution.Command = &runner.Command{Args: append(append([]string{}, args...), "--baseline"), Artifact: "measurements.json", Result: "domain.json"}
				project.Roles[baseline.ID] = baseline
				measure.Execution.Command = &runner.Command{Args: append(args, "--previous", "{text.previous}"), Artifact: "measurements.json", Result: "domain.json"}
				project.Roles[measure.ID] = measure
			}
			if mode == "all" {
				measure.Execution.Command.Args = []string{"/bin/sh", "-c", `outcome=ready; round=2
if [ -z "$1" ]; then outcome=continue; round=1; fi
printf '%s\n' '{"fixture":true}' > "$TMPDIR/measurements.json"
printf '{"outcome":"%s","reason":"scripted measurement"}\n' "$outcome" > "$TMPDIR/domain.json"
printf 'runner-measure round=%s selector=fixture skipped=false\n' "$round"`, "fixture", "{text.previous}"}
				project.Roles[measure.ID] = measure
			}
			project.Processes = map[string]runner.Process{process.ID: process}
			project.DefaultProcess = process.ID
			if mode == "baseline" {
				seedRole := runner.Clone(project.Roles["investigation-owner"])
				seedRole.ID, seedRole.Inputs = "scaffold", map[string]string{}
				project.Roles[seedRole.ID] = seedRole
				project.Processes["scaffold"] = runner.Process{
					ID:     "scaffold",
					Inputs: map[string]string{},
					Steps: []runner.Step{{
						ID:     "scaffold",
						Role:   "scaffold",
						Routes: map[string]runner.Route{"blocked": {Next: "$fail"}},
					}},
					MaxAttempts:        1,
					MaxAttemptsPerStep: 1,
					AttemptTimeout:     "2m",
				}
			}
			project.Source = &runner.SourceSettings{Ref: "refs/remotes/origin/main"}
			cfg.Projects["demo"] = project
			operator := t.TempDir()
			t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
			t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", head)
			t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
			t.Setenv("RUNNER_RELEASE_FIXTURE_MODE", mode)
			approved := goodTurn()
			approved.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Fixture source approved"},"reason":""}`
			script := &scriptedPeer{results: []nativeTurn{goodTurn(), approved}}
			if mode == "baseline" {
				blocked := goodTurn()
				blocked.Items[0].Text = `{"outcome":"blocked","outputs":{"summary":"Scaffold retained; measurements required"},"reason":"Missing initial measurement"}`
				script.results = []nativeTurn{blocked, goodTurn(), goodTurn(), approved}
			}
			if mode == "qa" {
				script.results = append(script.results, goodTurn(), approved)
			}
			if mode == "all" {
				correction := goodTurn()
				text, _ := json.Marshal(map[string]any{
					"outcome": "changes_requested",
					"outputs": map[string]string{"verdict": "BLOCKED", "summary": strings.Repeat("Concrete fixture correction. ", 8)},
					"reason":  "",
				})
				correction.Items[0].Text = string(text)
				wide := goodTurn()
				text, _ = json.Marshal(map[string]any{
					"outcome": "ready",
					"outputs": map[string]string{"summary": strings.Repeat("Scoped source completion. ", 9)},
					"reason":  "",
				})
				wide.Items[0].Text = string(text)
				text, _ = json.Marshal(map[string]any{
					"outcome": "ready",
					"outputs": map[string]string{"verdict": "APPROVE", "summary": strings.Repeat("Scoped proof verified. ", 10)},
					"reason":  "",
				})
				approved.Items[0].Text = string(text)
				script.results = []nativeTurn{wide, wide, wide, correction, wide, approved, wide, approved}
			}
			script.onTurn = func(dir string) {
				if mode == "baseline" {
					file := filepath.Join(dir, "baseline_test.go")
					if script.starts == 0 {
						if err := os.WriteFile(file, []byte("package seats\nimport \"testing\"\nfunc BenchmarkBaseline(b *testing.B){b.Fatal(\"observed baseline failure\")}\n"), 0600); err != nil {
							t.Error(err)
						}
						if err := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
							t.Error(err)
						}
					} else if script.starts == 1 {
						data, err := os.ReadFile(file)
						if err != nil || !strings.Contains(string(data), "observed baseline failure") {
							t.Error("retained scaffold was not restored before investigation", err)
						}
					} else if script.starts == 2 {
						if err := os.WriteFile(file, []byte("package seats\nimport \"testing\"\nfunc BenchmarkBaseline(b *testing.B){for i:=0;i<b.N;i++{if _,err:=ParseSeats(\"2\");err!=nil{b.Fatal(err)}}}\n"), 0600); err != nil {
							t.Error(err)
						}
					}
					return
				}
				if script.starts == 0 || mode == "qa" && script.starts == 2 || mode == "all" && (script.starts == 1 || script.starts == 2 || script.starts == 4 || script.starts == 6) {
					body := seatsImplementation
					if mode == "all" {
						if script.starts == 0 {
							body = strings.ReplaceAll(body, "n<=0", "n<0")
						}
						body += strings.Repeat("\n// Fixture correction.", script.starts+1) + "\n"
					}
					if script.starts == 2 {
						body += "\n// Corrected qualification fixture.\n"
					}
					if err := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(body), 0600); err != nil {
						t.Error(err)
					}
				}
			}
			sandbox := filepath.Join(t.TempDir(), "sandbox")
			os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700)
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
			state := filepath.Join(t.TempDir(), "state")
			rig := openNativeRig(t, state, cfg, script, hook)
			defer func() { rig.close() }()
			inputs := map[string]runner.Value{
				"request":   runner.Text("Implement positive seat parsing"),
				"package":   runner.Text("."),
				"tests":     runner.Text(""),
				"benchmark": runner.Text("none"),
			}
			var old runner.Run
			if mode == "baseline" {
				old, err = rig.core.Start(runner.StartRequest{RequestID: "scaffold", Project: "demo", Process: "scaffold", Inputs: map[string]runner.Value{}})
				if err != nil {
					t.Fatal(err)
				}
				old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
				if old.Status != "Failed" || len(old.Attempts) != 1 || old.Attempts[0].Source == nil {
					t.Fatal("failed scaffold was not retained", old)
				}
				inputs["patch"] = runner.Value{ArtifactID: old.Attempts[0].Source.PatchID}
				inputs["benchmark"] = runner.Text("^BenchmarkBaseline$")
			}
			r, err := rig.core.Start(runner.StartRequest{
				RequestID: "connected",
				Project:   "demo",
				Inputs:    inputs,
			})
			if err != nil {
				t.Fatal(err)
			}
			approve := func(step, key string) {
				t.Helper()
				waiting := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
					return r.Terminal() || r.Status == "Waiting" && (r.Decision != nil && r.Decision.Step == step || strings.Contains(r.Reason, "history capacity"))
				}, time.Minute)
				if waiting.Decision == nil {
					if mode == "all" {
						data, _ := json.Marshal(waiting)
						os.WriteFile("/tmp/runner-combined-history.json", data, 0600)
						t.Log("combined history bytes", len(data), "attempts", len(waiting.Attempts))
					}
					t.Fatalf("expected %s decision; got %s: %s", step, waiting.Status, waiting.Reason)
				}
				if _, err := rig.core.Decide(runner.DecisionRequest{RequestID: key, RunID: r.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}); err != nil {
					t.Fatal(err)
				}
			}
			approve("prepare", "merge")
			approve("backup", "production")
			if mode == "migration" {
				approve("inspect-migration", "restore")
			}
			if mode == "approval" || mode == "all" {
				approve("inspect", "recover")
			}
			if mode == "uncertain" {
				waiting := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Waiting" && r.Decision == nil })
				last := waiting.Attempts[len(waiting.Attempts)-1].ID
				rig.close()
				rig = openNativeRig(t, state, cfg, nil, hook)
				retained, err := rig.core.Get(r.ID)
				if err != nil || retained.Attempts[len(retained.Attempts)-1].ID != last {
					t.Fatal("uncertain deployment was replaced", err)
				}
				os.WriteFile(filepath.Join(operator, "settle"), []byte("observed"), 0600)
				rig.core.ExecutionChanged(last)
			}
			end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
				return r.Terminal() && allReleased(r) || r.Status == "Waiting" && strings.Contains(r.Reason, "history capacity")
			})
			want := "Completed"
			if mode == "backup" || mode == "migration" {
				want = "Failed"
			}
			if end.Status != want {
				if mode == "all" {
					data, _ := json.Marshal(end)
					os.WriteFile("/tmp/runner-combined-history.json", data, 0600)
					compact, _, _ := runner.MarshalRunRecord(end)
					t.Log("combined history bytes", len(compact), "attempts", len(end.Attempts))
				}
				t.Fatal(end.Status, end.Reason)
			}
			calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
			text := string(calls)
			if mode == "backup" && (strings.Contains(text, "migrate\n") || !strings.Contains(end.Reason, "healthy")) {
				t.Fatal("backup recovery advanced or hid service state", text, end.Reason)
			}
			if mode == "migration" && (!strings.Contains(text, "restore\n") || strings.Contains(text, "deploy\n")) {
				t.Fatal("restoration was bypassed or release advanced", text)
			}
			if mode == "retry" || mode == "approval" {
				if strings.Count(text, "deploy\n") != 2 {
					t.Fatal("recovery exceeded its configured dispatch", text)
				}
			}
			if mode == "uncertain" && strings.Count(text, "deploy\n") != 1 {
				t.Fatal("lost reply duplicated deployment", text)
			}
			if mode == "qa" {
				if script.starts != 4 || strings.Count(text, "publish\n") != 2 || strings.Count(text, "qualify\n") != 2 {
					t.Fatal("qualification repair was externally coordinated or duplicated", text, script.starts)
				}
			}
			if mode == "all" && (len(end.Attempts) != 35 || script.starts != 8 || strings.Count(text, "deploy\n") != 3) {
				t.Fatal("combined configured correction/recovery path differs", len(end.Attempts), script.starts, text)
			}
			if mode == "baseline" {
				first := end.Attempts[0]
				feedback := string(first.End.Result.Outputs["check"].Inline)
				if first.Step != 0 || first.End.Result.Outcome != "continue" || !strings.Contains(feedback, "failed=true") || !strings.Contains(feedback, "observed baseline failure") || script.starts != 4 {
					t.Fatal("baseline did not precede investigation and configured iteration", first, script.starts)
				}
				data, err := rig.disk.Read("artifacts/"+first.End.Result.Outputs["artifact"].ArtifactID, rig.disk.Policy.MaxArtifact)
				if err != nil || !strings.Contains(string(data), "observed baseline failure") {
					t.Fatal("failed baseline evidence was lost", err, string(data))
				}
				failedMeasure := end.Attempts[4]
				if failedMeasure.End.Result.Outcome != "continue" || !strings.Contains(string(failedMeasure.End.Result.Outputs["check"].Inline), "failed=true") {
					t.Fatal("failed candidate measurement bypassed its configured correction", failedMeasure)
				}
				preserved, err := rig.core.Get(old.ID)
				if err != nil || preserved.Status != "Failed" || preserved.Attempts[0].Source.PatchID != old.Attempts[0].Source.PatchID || end.Attempts[2].Source.InputPatchID != old.Attempts[0].Source.PatchID {
					t.Fatal("original failure or cumulative source provenance changed", err)
				}
			}
			encoded, _, _ := runner.MarshalRunRecord(end)
			if len(encoded) > 40<<10 {
				t.Fatal("connected history exceeded the unchanged pilot record budget", len(encoded))
			}
		})
	}
}
