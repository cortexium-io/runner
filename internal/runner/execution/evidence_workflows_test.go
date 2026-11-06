//go:build darwin || linux

package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func evidenceBinary(t *testing.T, pkg string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "helper")
	cmd := exec.Command("go", "build", "-o", file, pkg)
	cmd.Dir = filepath.Join("..", "..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	return file
}

func evidenceRig(t *testing.T, repo, file, measure, provider string, script *scriptedPeer, configure func(*runner.Project)) (*nativeRig, runner.Process, string) {
	t.Helper()
	head := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
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
	root := filepath.Join("..", "..", "..", "examples", "jacks-break")
	var p runner.Process
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil || json.Unmarshal(data, &p) != nil {
		t.Fatal("process unavailable", err)
	}
	for _, step := range p.Steps {
		var role runner.Role
		data, err = os.ReadFile(filepath.Join(root, "roles", step.Role+".json"))
		if err != nil || json.Unmarshal(data, &role) != nil {
			t.Fatal("role unavailable", step.Role, err)
		}
		project.Roles[role.ID] = role
	}
	goBinary := cfg.Instance.Codex.GoBinary
	for _, name := range []string{"delivery-measure", "delivery-source-baseline"} {
		if role, exists := project.Roles[name]; exists {
			args := []string{measure, "--go", goBinary, "--source", "{workspace}", "--scratch", "{scratch}", "--package", "{text.package}", "--benchmark", "{text.benchmark}"}
			if name == "delivery-source-baseline" {
				args = append(args, "--baseline")
			} else {
				args = append(args, "--previous", "{text.previous}")
			}
			role.Execution.Command = &runner.Command{Args: args, Result: "domain.json", Artifact: "measurements.json"}
			project.Roles[name] = role
		}
	}
	if role, exists := project.Roles["delivery-check"]; exists {
		role.Execution.Command = &runner.Command{Args: []string{goBinary, "test", "-count=1", "./...", "-run", "{text.tests}"}, ExitOutcomes: map[int]string{1: "changes_requested"}}
		project.Roles[role.ID] = role
	}
	var goCheck string
	for _, name := range []string{"nonogram-research-check", "nonogram-research-source-check"} {
		if role, exists := project.Roles[name]; exists {
			if goCheck == "" {
				goCheck = evidenceBinary(t, "./cmd/runner-go-check")
			}
			role.Execution.Command = &runner.Command{Args: []string{goCheck, "--go", goBinary, "--package", "{text.package}", "--run", "{text.tests}", "--timeout", "10m", "--scratch", "{scratch}", "--output", "{scratch}/proof.json"}, Artifact: "proof.json", ExitOutcomes: map[int]string{1: "changes_requested", 2: "blocked"}}
			project.Roles[name] = role
		}
	}
	project.Processes, project.DefaultProcess, project.Source = map[string]runner.Process{p.ID: p}, p.ID, &runner.SourceSettings{Ref: head}
	configure(&project)
	cfg.Projects["demo"] = project
	operator := t.TempDir()
	t.Setenv("RUNNER_RELEASE_FIXTURE_STATE", operator)
	t.Setenv("RUNNER_RELEASE_FIXTURE_HEAD", head)
	t.Setenv("RUNNER_RELEASE_FIXTURE_CONNECTED", "1")
	t.Setenv("RUNNER_RELEASE_FIXTURE_MODE", "complete")
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hook := func(s *Service) {
		s.releases.cfg, s.commands.cfg.Binary = cfg.Instance.Release, sandbox
		s.commands.launchWorker = func(spec string, finished func()) error {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+spec)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); finished() }()
			return nil
		}
	}
	return openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script, hook), p, operator
}

func stagedEvidence(t *testing.T, dir, name string) []byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, ".runner-tmp", "inputs", "*", name))
	if err != nil || len(files) != 1 {
		t.Error("missing unique staged evidence", name, files, err)
		return nil
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Error(err)
	}
	return data
}

// The producer's Vitest interpretation is tested by runner-frontend-check.
// Here assertions are scripted from disposable source state; real command,
// artifact, correction, review and protected-release boundaries are exercised.
func TestFrontendFullReportReachesFirstOwnerAndCorrection(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	for _, mode := range []string{"fresh", "retained", "blocked"} {
		t.Run(mode, func(t *testing.T) {
			repo := sourceRepository(t)
			stage := "bad"
			if mode == "blocked" {
				stage = "blocked"
			}
			for name, value := range map[string]string{"stage.txt": stage, "visit.txt": "baseline"} {
				if err := os.WriteFile(filepath.Join(repo, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			fixtureGit(t, repo, "add", "--all")
			fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "frontend source fixture")
			approved := goodTurn()
			approved.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Fixture report independently reviewed"},"reason":""}`
			script := &scriptedPeer{results: []nativeTurn{goodTurn(), goodTurn(), approved}}
			script.onTurn = func(dir string) {
				data := stagedEvidence(t, dir, "checks")
				if script.starts < 2 {
					want := `"generation":"baseline"`
					if script.starts == 1 {
						want = `"generation":"first"`
					}
					if len(data) <= 480 || !bytes.Contains(data, []byte("FIRST_FULL_ASSERTION_DIAGNOSTIC")) || !bytes.Contains(data, []byte(want)) || !bytes.Contains(data, []byte(`"vitest"`)) {
						t.Error("full original assertion detail/current report did not reach owner", script.starts, string(data))
					}
					visit, stage := "first", "bad"
					if script.starts == 1 {
						visit, stage = "second", "good"
					}
					for name, value := range map[string]string{"stage.txt": stage, "visit.txt": visit} {
						if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
							t.Error(err)
						}
					}
				} else if !bytes.Contains(data, []byte(`"status":"passed"`)) || !bytes.Contains(data, []byte(`"generation":"second"`)) {
					t.Error("review did not receive the final actual report", string(data))
				}
			}
			file := "frontend.json"
			if mode == "retained" {
				file = "retained-frontend.json"
			}
			rig, _, operator := evidenceRig(t, repo, file, measure, provider, script, func(p *runner.Project) {
				for _, name := range []string{"frontend-check", "frontend-source-check"} {
					if role, ok := p.Roles[name]; ok {
						role.Execution.Command = &runner.Command{Args: []string{"/bin/sh", "-c", `stage=$(cat stage.txt); generation=$(cat visit.txt); outcome=changes_requested; status=failed
if [ "$stage" = good ]; then outcome=ready; status=passed; fi
if [ "$stage" = blocked ]; then outcome=blocked; status=blocked; fi
printf '{"schema":2,"status":"%s","generation":"%s","tests":["fixture.test.ts"],"vitest":{"testResults":[{"assertionResults":[{"failureMessages":["FIRST_FULL_ASSERTION_DIAGNOSTIC followed by a complete retained stack` + strings.Repeat(" diagnostic", 100) + `"]}]}]}}\n' "$status" "$generation" > "$TMPDIR/frontend-check.json"
printf '{"outcome":"%s","reason":"fixture assertion classification"}\n' "$outcome" > "$TMPDIR/domain.json"
printf 'generic check summary only: %s\n' "$outcome"`}, Result: "domain.json", Artifact: "frontend-check.json"}
						p.Roles[name] = role
					}
				}
				if mode == "retained" {
					// A real nonempty patch is retained through the ordinary artifact
					// producer, without a model call or an invented approved verdict.
					fixtureGit(t, repo, "add", "--all")
					if err := os.WriteFile(filepath.Join(repo, "visit.txt"), []byte("baseline\n"), 0600); err != nil {
						t.Fatal(err)
					}
					patch := fixtureGit(t, repo, "diff", "--binary", "--full-index", "HEAD", "--", "visit.txt")
					fixtureGit(t, repo, "checkout", "--", "visit.txt")
					p.Roles["retain"] = runner.Role{ID: "retain", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(patch)}}
					p.Processes["retain"] = runner.Process{ID: "retain", Inputs: map[string]string{}, Steps: []runner.Step{{ID: "retain", Role: "retain", Routes: map[string]runner.Route{"ready": {Next: "$fail"}}}}, MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
				}
			})
			defer rig.close()
			inputs := map[string]runner.Value{"request": runner.Text("Repair only the complete supplied fixture assertion report."), "package": runner.Text("."), "tests": runner.Text(`["fixture.test.ts"]`), "benchmark": runner.Text("none")}
			if mode == "retained" {
				old, err := rig.core.Start(runner.StartRequest{RequestID: "retain", Project: "demo", Process: "retain", Inputs: map[string]runner.Value{}})
				if err != nil {
					t.Fatal(err)
				}
				old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
				inputs["patch"] = old.Attempts[0].End.Result.Outputs["patch"]
			}
			r, err := rig.core.Start(runner.StartRequest{RequestID: "frontend-report", Project: "demo", Inputs: inputs})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "blocked" {
				end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
				if end.Status != "Failed" || script.starts != 0 || len(end.Attempts) != 2 || !strings.Contains(end.Reason, "fixture assertion classification") {
					t.Fatal("blocked evidence reached model repair", end.Status, end.Reason, script.starts)
				}
				return
			}
			for _, step := range []string{"prepare", "backup"} {
				waiting := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() || r.Decision != nil }, time.Minute)
				if waiting.Decision == nil || waiting.Decision.Step != step {
					t.Fatal("missing protected fixture decision", step, waiting.Status, waiting.Reason)
				}
				if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "approve-" + step, RunID: r.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}); err != nil {
					t.Fatal(err)
				}
			}
			end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
			calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
			if end.Status != "Completed" || script.starts != 3 || strings.Count(string(calls), "publish\n") != 1 {
				t.Fatal("full-report correction/review/release failed", end.Status, end.Reason, script.starts, string(calls))
			}
		})
	}
}

func TestResearchNegativeEvidenceCompletesOnlyAfterIndependentReview(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	for _, tc := range []struct {
		name                 string
		incomplete, retained bool
	}{{"complete-negative", false, false}, {"repair-incomplete", true, false}, {"retained-reference", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			incomplete := tc.incomplete
			repo := sourceRepository(t)
			if err := os.WriteFile(filepath.Join(repo, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
				t.Fatal(err)
			}
			baseline := "package seats\nimport \"testing\"\nfunc BenchmarkFixtureBaseline(b *testing.B){b.ReportMetric(6,\"controls/op\")}\n"
			if err := os.WriteFile(filepath.Join(repo, "baseline_test.go"), []byte(baseline), 0600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, repo, "add", "--all")
			fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "research baseline")
			var priorPatch, priorSource []byte
			collector := func(complete bool) []byte {
				proof := map[string]any{"complete": complete, "outcome": "not_reproduced", "controls": []int{1, 2, 3, 4, 5, 6}, "cold_state": "fresh", "deadline_seconds": 10, "request": map[string]any{"game": "fixture", "exclusions": []string{}}, "initial_seed": 131, "advanced_seeds": []int{131}, "attempts": []map[string]any{{"phase": "fixture", "work": 1, "nodes": 1, "allocated_bytes": 1, "elapsed_ns": 1, "status": "bounded_rejection"}}, "lost_original_seed": true}
				data, _ := json.Marshal(proof)
				return []byte("package seats\nimport(\"encoding/json\";\"os\";\"path/filepath\";\"testing\")\nconst proof=" + strconv.Quote(string(data)) + "\nfunc TestResearchCollectorFixture(t *testing.T){var p struct{Deadline int `json:\"deadline_seconds\"`;Controls []int `json:\"controls\"`};if json.Unmarshal([]byte(proof),&p)!=nil||p.Deadline!=10||len(p.Controls)!=6{t.Fatal(\"collector lost its fixture protocol\")}}\nfunc BenchmarkFixtureResearch(b *testing.B){if err:=os.WriteFile(filepath.Join(os.Getenv(\"RUNNER_MEASURE_EVIDENCE\"),\"request.json\"),[]byte(proof),0600);err!=nil{b.Fatal(err)};b.ReportMetric(6,\"controls/op\")}\n")
			}
			if tc.retained {
				priorSource = collector(true)
				if err := os.WriteFile(filepath.Join(repo, "research_test.go"), priorSource, 0600); err != nil {
					t.Fatal(err)
				}
				fixtureGit(t, repo, "add", "--all")
				priorPatch = fixtureGit(t, repo, "diff", "--cached", "--binary", "--full-index", "HEAD", "--", "research_test.go")
				fixtureGit(t, repo, "reset", "--quiet", "HEAD", "--", "research_test.go")
				if err := os.Remove(filepath.Join(repo, "research_test.go")); err != nil {
					t.Fatal(err)
				}
			}
			complete := goodTurn()
			complete.Items[0].Text = `{"outcome":"ready","outputs":{"conclusion":"NOT_REPRODUCED","summary":"Complete negative fixture; original seed remains unknown"},"reason":""}`
			gap := goodTurn()
			gap.Items[0].Text = `{"outcome":"changes_requested","outputs":{"conclusion":"INCOMPLETE","summary":"Missing complete synthetic phase evidence"},"reason":""}`
			script := &scriptedPeer{results: []nativeTurn{goodTurn(), complete}}
			if incomplete {
				script.results = []nativeTurn{goodTurn(), gap, goodTurn(), complete}
			}
			script.onTurn = func(dir string) {
				checks := stagedEvidence(t, dir, "checks")
				if !bytes.Contains(checks, []byte("TestParseSeats")) {
					t.Error("scoped Go test execution receipts missing", string(checks))
				}
				if tc.retained && script.starts == 0 {
					if _, err := os.Stat(filepath.Join(dir, "research_test.go")); !os.IsNotExist(err) {
						t.Error("reference patch seeded or changed pristine baseline source", err)
					}
					if !bytes.Equal(stagedEvidence(t, dir, "prior_patch"), priorPatch) {
						t.Error("retained patch reference lost its identity")
					}
					baseline := stagedEvidence(t, dir, "baseline")
					if !bytes.Contains(baseline, []byte(`"baseline":true`)) || bytes.Contains(baseline, []byte("BenchmarkFixtureResearch")) {
						t.Error("pristine baseline recollection used the retained collector", string(baseline))
					}
					// Script the ordinary authorized edit turn from the staged
					// reference, not a controller seed or adopted failed verdict.
					if err := os.WriteFile(filepath.Join(dir, "research_test.go"), priorSource, 0600); err != nil {
						t.Error(err)
					}
					return
				}
				if script.starts%2 == 1 {
					data := stagedEvidence(t, dir, "measurements")
					var report struct {
						Baseline       bool   `json:"baseline"`
						EvidenceStatus string `json:"evidence_status"`
						Evidence       map[string]struct {
							Complete bool   `json:"complete"`
							Outcome  string `json:"outcome"`
							Controls []int  `json:"controls"`
							Cold     string `json:"cold_state"`
							Deadline int    `json:"deadline_seconds"`
						} `json:"evidence"`
					}
					if json.Unmarshal(data, &report) != nil || report.Baseline || report.EvidenceStatus != "provided" || report.Evidence["request.json"].Outcome != "not_reproduced" || len(report.Evidence["request.json"].Controls) != 6 || report.Evidence["request.json"].Deadline != 10 || report.Evidence["request.json"].Cold != "fresh" {
						t.Error("negative evidence was lost or relabelled", string(data))
					}
					want := !incomplete || script.starts == 3
					if report.Evidence["request.json"].Complete != want {
						t.Error("provided was mistaken for complete", string(data))
					}
					return
				}
				isComplete := !incomplete || script.starts == 2
				if script.starts == 2 && !bytes.Contains(stagedEvidence(t, dir, "measurements"), []byte(`"complete":false`)) {
					t.Error("repair owner lacked full incomplete evidence")
				}
				if err := os.WriteFile(filepath.Join(dir, "research_test.go"), collector(isComplete), 0600); err != nil {
					t.Error(err)
				}
			}
			file := "research-nonogram.json"
			if tc.retained {
				file = "retained-research-nonogram.json"
			}
			rig, _, operator := evidenceRig(t, repo, file, measure, provider, script, func(p *runner.Project) {
				if tc.retained {
					p.Roles["reference"] = runner.Role{ID: "reference", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(priorPatch)}}
					p.Processes["reference"] = runner.Process{ID: "reference", Inputs: map[string]string{}, Steps: []runner.Step{{ID: "reference", Role: "reference", Routes: map[string]runner.Route{"ready": {Next: "$fail"}}}}, MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
				}
			})
			defer rig.close()
			inputs := map[string]runner.Value{"request": runner.Text("Complete reproducible negative fixture evidence, not a product fix."), "package": runner.Text("."), "tests": runner.Text("^Test(ParseSeats|ResearchCollectorFixture)$"), "baseline_benchmark": runner.Text("^BenchmarkFixtureBaseline$"), "benchmark": runner.Text("^BenchmarkFixtureResearch$")}
			if tc.retained {
				old, err := rig.core.Start(runner.StartRequest{RequestID: "reference", Project: "demo", Process: "reference", Inputs: map[string]runner.Value{}})
				if err != nil {
					t.Fatal(err)
				}
				old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
				inputs["prior_patch"] = old.Attempts[0].End.Result.Outputs["patch"]
			}
			r, err := rig.core.Start(runner.StartRequest{RequestID: "research", Project: "demo", Inputs: inputs})
			if err != nil {
				t.Fatal(err)
			}
			end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) }, time.Minute)
			if end.Status != "Completed" || string(end.Outputs["conclusion"].Inline) != `"NOT_REPRODUCED"` || end.Outputs["patch"].ArtifactID == "" {
				t.Fatal("negative research could not complete truthfully", end.Status, end.Reason, end.Outputs)
			}
			checkProof, err := rig.disk.Read("artifacts/"+end.Outputs["checks"].ArtifactID, rig.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Contains(checkProof, []byte("TestResearchCollectorFixture")) {
				t.Fatal("completion omitted its full scoped execution receipt", err, string(checkProof))
			}
			if _, err = os.Stat(filepath.Join(operator, "calls")); !os.IsNotExist(err) {
				t.Fatal("research dispatched a release operation", err)
			}
			want := 2
			if incomplete {
				want = 4
			}
			if script.starts != want {
				t.Fatal("unexpected research model-protocol turns", script.starts)
			}
		})
	}
}
