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
	"github.com/cortexium-io/runner/internal/saas"
)

// The actual published-generator, compiler, Vue build and browser producer are
// qualified separately on a disposable generated app. This fixture substitutes
// source turns/check observations only, retaining the real Core/SQLite/groups/
// pin/join/command-worker/artifact/review-image/local-integration lifecycle.
func TestLocalSaaSCompleteRouteRepairsThroughFreshIndependentGates(t *testing.T) {
	testLocalSaaSRoute(t, false)
}

func TestNativeLocalSaaSCommandsAndBrowser(t *testing.T) {
	if os.Getenv("RUNNER_SAAS_NATIVE") != "1" {
		t.Skip("explicit zero-model native command/browser qualification")
	}
	testLocalSaaSRoute(t, true)
}

func testLocalSaaSRoute(t *testing.T, native bool) {
	owned := t.TempDir()
	if native {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		owned, err = os.MkdirTemp(filepath.Join(home, ".codex"), "runner-saas-preflight-")
		if err != nil {
			t.Fatal(err)
		}
		t.Log("owned native fixture", owned)
	}
	repo := sourceRepositoryAt(t, filepath.Join(owned, "repo"))
	if native {
		if err := os.CopyFS(filepath.Join(repo, "platform"), os.DirFS(os.Getenv("RUNNER_SAAS_FIXTURE_SOURCE"))); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, repo, "add", "platform")
		fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "synthetic generated SaaS fixture")
	}
	cfg := workspaceSnapshot(t, repo)
	cfg.Instance.TaskConcurrency = 2
	cfg.Instance.Storage.MaxArtifact = 16 << 20
	cfg.Instance.Storage.Database.MaxRecord = 128 << 10
	cfg.Instance.Storage.Database.MaxSize = 48 << 20
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	root := filepath.Join("..", "..", "..", "examples", "cortexium-local")
	if native {
		var declared runner.Instance
		data, err := os.ReadFile(filepath.Join(root, "instance.json"))
		if err != nil || json.Unmarshal(data, &declared) != nil {
			t.Fatal("declared fixture policy", err)
		}
		cfg.Instance.Storage = declared.Storage
	}
	var process runner.Process
	read := func(name string, v any) {
		t.Helper()
		b, e := os.ReadFile(filepath.Join(root, name))
		if e != nil || json.Unmarshal(b, v) != nil {
			t.Fatal(name, e)
		}
	}
	read("processes/develop-local.json", &process)
	project := cfg.Projects["demo"]
	project.Roles = map[string]runner.Role{}
	var roles func([]runner.Step)
	roles = func(steps []runner.Step) {
		for _, s := range steps {
			if s.ForEach != nil {
				roles(s.ForEach.Steps)
				continue
			}
			var r runner.Role
			read("roles/"+s.Role+".json", &r)
			project.Roles[r.ID] = r
		}
	}
	roles(process.Steps)
	// Do not require an installed Genny runtime for ordinary model-free tests.
	// The production example keeps its explicit pinned genny-source profile.
	for _, name := range []string{"planned-card-owner", "planned-repair"} {
		r := project.Roles[name]
		r.Execution.Mode = ""
		r.Execution.GeneratorSHA256 = ""
		r.Execution.GeneratorGoSHA256 = ""
		project.Roles[name] = r
	}
	op := evidenceBinary(t, "./testdata/saas/proof-operator")
	h := strings.Repeat("a", 64)
	brief, e := os.ReadFile("../../../testdata/saas/first-flow-brief.md")
	if e != nil {
		t.Fatal(e)
	}
	a := saas.Authority{Version: 1, BriefCommit: saas.BriefCommit, BriefSHA256: saas.BriefSHA256, GennyCommit: saas.GennyCommit, Base: base, AppDirectory: "platform", Module: "fixture.invalid/local", ProjectName: "RunnerFixture", Brief: string(brief), Contract: saas.Contract, GoMod: "module fixture.invalid/local\n\ngo 1.27.0\n\ntoolchain go1.27.3\n", GoSum: "synthetic locked sum\n", Package: `{"private":true,"dependencies":{"vue":"3.5.45"},"devDependencies":{"vite":"8.3.6","vitest":"5.1.0"}}`, Lock: `{"lockfileVersion":3}`, GoSHA256: h, GoCheckSHA256: h, GennySHA256: h, NodeSHA256: h, ChromeSHA256: h, DriverSHA256: h}
	if native {
		data, err := os.ReadFile(os.Getenv("RUNNER_SAAS_AUTHORITY"))
		if err != nil || json.Unmarshal(data, &a) != nil {
			t.Fatal("native authority", err)
		}
		a.Base = base
		op = os.Getenv("RUNNER_SAAS_HELPER")
		cfg.Instance.Codex.Binary = os.Getenv("RUNNER_PREFLIGHT_CODEX")
		cfg.Instance.Codex.GoModuleCache = os.Getenv("RUNNER_SAAS_MODULE_CACHE")
		var runtime struct {
			Go, Genny, Node, Modules, Chrome string
			BrowserRuntime                   string `json:"browser_runtime"`
			GoCheck                          string `json:"go_check"`
		}
		data, err = os.ReadFile(os.Getenv("RUNNER_SAAS_RUNTIME"))
		if err != nil || json.Unmarshal(data, &runtime) != nil {
			t.Fatal("native runtime", err)
		}
		cfg.Instance.Codex.CommandReadPaths = []string{op, runtime.GoCheck, runtime.Genny, filepath.Dir(filepath.Dir(runtime.Go)), filepath.Dir(filepath.Dir(runtime.Node)), filepath.Dir(runtime.Modules), runtime.BrowserRuntime, filepath.Dir(filepath.Dir(filepath.Dir(runtime.Chrome))), os.Getenv("RUNNER_SAAS_RUNTIME"), cfg.Instance.Codex.GoModuleCache}
	}
	authority := filepath.Join(owned, "authority.json")
	ab, _ := json.Marshal(a)
	if err := os.WriteFile(authority, ab, 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"authority", "local-check", "local-browser"} {
		r := project.Roles[name]
		r.Execution.Command.Args[0] = op
		for i, arg := range r.Execution.Command.Args {
			if arg == "/absolute/path/to/admitted-authority.json" {
				r.Execution.Command.Args[i] = authority
			}
			if native && arg == "/absolute/path/to/saas-runtime.json" {
				r.Execution.Command.Args[i] = os.Getenv("RUNNER_SAAS_RUNTIME")
			}
		}
		project.Roles[name] = r
	}
	if native {
		cfg.Instance.Codex.CommandReadPaths = append(cfg.Instance.Codex.CommandReadPaths, authority)
	}
	project.Processes, project.DefaultProcess, project.Source = map[string]runner.Process{process.ID: process}, process.ID, &runner.SourceSettings{Ref: base}
	cfg.Projects["demo"] = project
	plan := goodTurn()
	plan.Items[0].Text = string(mustJSON(t, map[string]any{"outcome": "ready", "outputs": map[string]string{"tasks": `[{"id":"fixture","inputs":{"task":"Change only the synthetic stage marker.","paths":["stage.txt"]}}]`}, "reason": ""}))
	approve := goodTurn()
	approve.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Synthetic Core handoff verified; not App acceptance"},"reason":""}`
	reject := goodTurn()
	reject.Items[0].Text = `{"outcome":"changes_requested","outputs":{"verdict":"BLOCKED","summary":"Synthetic image-review correction must repeat source checks/review"},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{plan, goodTurn(), goodTurn(), approve, reject, goodTurn(), approve, approve}}
	if native {
		script.results = []nativeTurn{plan, goodTurn(), approve, approve}
	}
	script.onTurn = func(dir string) {
		if native {
			blockUnexpectedRepair := func() {
				blocked := goodTurn()
				blocked.Items[0].Text = `{"outcome":"blocked","outputs":{"summary":"Native fixture prerequisite or observer failed; inspect retained original check/browser reports"},"reason":"qualification does not repair fixture source automatically"}`
				script.results[0] = blocked
			}
			switch script.starts {
			case 0:
				if len(stagedEvidence(t, dir, "authority-brief.md")) == 0 {
					t.Error("missing actual brief")
				}
			case 1:
				if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte("native synthetic fixture"), 0600); err != nil {
					t.Error(err)
				}
			case 2:
				files, _ := filepath.Glob(filepath.Join(dir, ".runner-tmp", "inputs", "*", "saas-reports-go.json"))
				if len(files) != 1 {
					blockUnexpectedRepair()
					return
				}
			case 3:
				files, _ := filepath.Glob(filepath.Join(dir, ".runner-tmp", "inputs", "*", "saas-chrome-desktop-run-viewport.png"))
				if len(files) != 1 {
					blockUnexpectedRepair()
					return
				}
			default:
				t.Error("unexpected native-fixture simulated turn", script.starts)
			}
			return
		}
		switch script.starts {
		case 0:
			if !strings.Contains(string(stagedEvidence(t, dir, "authority-brief.md")), "Agent reviews supplied text") || !strings.Contains(string(stagedEvidence(t, dir, "authority-contract.md")), "cmd/runner-local") {
				t.Error("planner did not receive actual brief/contract")
			}
		case 1, 2, 5:
			stage := map[int]string{1: "bad", 2: "ready", 5: "fixed"}[script.starts]
			if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte(stage), 0600); err != nil {
				t.Error(err)
			}
		case 3, 6:
			if !strings.Contains(string(stagedEvidence(t, dir, "saas-reports-go.json")), "full_report") {
				t.Error("independent source reviewer lost complete report")
			}
		case 4, 7:
			if len(stagedEvidence(t, dir, "saas-chrome-desktop-run-viewport.png")) == 0 {
				t.Error("independent image reviewer received no viewport")
			}
		default:
			t.Error("unexpected simulated model visit", script.starts)
		}
	}
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if native {
		sandbox = cfg.Instance.Codex.Binary
	}
	hook := func(s *Service) {
		s.commands.cfg.Binary = sandbox
		s.commands.launchWorker = func(spec string, finished func()) error {
			timeout := "30s"
			if native {
				timeout = "18m"
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout="+timeout)
			cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+spec)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); finished() }()
			return nil
		}
	}
	rig := openNativeRig(t, filepath.Join(owned, "state"), cfg, script, hook)
	closed := false
	defer func() {
		if !closed {
			rig.close()
		}
	}()
	run, err := rig.core.Start(runner.StartRequest{RequestID: "local-saas-control", Project: "demo", Inputs: map[string]runner.Value{"request": runner.Text("Synthetic complete local route control qualification, not product implementation")}})
	if err != nil {
		t.Fatal(err)
	}
	wait := time.Minute
	if native {
		wait = 12 * time.Minute
	}
	done := waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) }, wait)
	if native {
		dir := os.Getenv("RUNNER_SAAS_PROOF")
		if !filepath.IsAbs(dir) {
			t.Fatal("absolute proof directory required")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(done, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		for _, attempt := range done.Attempts {
			if attempt.End == nil || attempt.End.Result == nil {
				continue
			}
			for name, value := range attempt.End.Result.Outputs {
				if value.ArtifactID == "" {
					continue
				}
				b, err := rig.disk.Read("artifacts/"+value.ArtifactID, 16<<20)
				if err != nil {
					t.Fatal(err)
				}
				filename := done.Process.Steps[attempt.Step].ID + "-" + name + "-" + filepath.Base(value.ArtifactID)
				if err = os.WriteFile(filepath.Join(dir, filename), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	wantTurns := 8
	if native {
		wantTurns = 4
	}
	if done.Status != "Completed" || script.starts != wantTurns || done.Process.MaxAttempts != 64 || len(done.TaskStatuses) != 1 || done.TaskStatuses[0].Status != "Completed" {
		t.Fatal("local route lost mandatory stages/bounds", done.Status, done.Reason, script.starts, done.TaskStatuses)
	}
	if native {
		if done.CleanupBlocked || !done.ResourcesReleased {
			t.Fatal("native fixture resources not settled", done.CleanupError)
		}
		rig.close()
		closed = true
		if err := os.RemoveAll(owned); err != nil {
			t.Fatal(err)
		}
		return
	}
	counts := map[string]int{}
	for _, a := range done.Attempts {
		counts[done.Process.Steps[a.Step].ID]++
	}
	if counts["plan"] != 1 || counts["cards"] != 1 || counts["join"] != 1 || counts["owner"] != 2 || counts["check"] != 3 || counts["source-review"] != 2 || counts["browser"] != 2 || counts["local-review"] != 2 || counts["integrate"] != 1 {
		t.Fatal("correction skipped or replayed accepted work", counts)
	}
	var candidate string
	if json.Unmarshal(done.Outputs["candidate"].Inline, &candidate) != nil || candidate == "" {
		t.Fatal("reviewed local candidate absent")
	}
	if strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD"))) != base {
		t.Fatal("local integration changed original source HEAD")
	}
}
