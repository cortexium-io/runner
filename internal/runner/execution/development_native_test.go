//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/control"
)

type workflowNativePeer struct {
	codexPeer
	mu      *sync.Mutex
	receipt string
	calls   *int
}

func (p *workflowNativePeer) Call(ctx context.Context, method string, args, out any) error {
	if method == "turn/start" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if *p.calls >= 4 {
			return fmt.Errorf("four-turn workflow qualification allowance exhausted")
		}
		*p.calls = *p.calls + 1
		if err := workerWrite(p.receipt, []byte(strconv.Itoa(*p.calls))); err != nil {
			return err
		}
	}
	return p.codexPeer.Call(ctx, method, args, out)
}

// A paid, explicitly selected complete process. Normal Go gates remain model
// free. The fixture has no production data, credentials or publication target.
func TestNativeDevelopmentWorkflow(t *testing.T) {
	probe := os.Getenv("RUNNER_DEVELOPMENT_PROBE") == "1"
	if os.Getenv("RUNNER_NATIVE_DEVELOPMENT") != "1" && !probe {
		t.Skip("explicit opt-in: at most four Sol/xhigh turns in one complete workflow")
	}
	root := os.Getenv("RUNNER_WORKFLOW_ROOT")
	if root == "" {
		t.Fatal("RUNNER_WORKFLOW_ROOT must name a fresh private qualification directory")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, "native-turns")
	f, err := os.OpenFile(receipt, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("no-repeat receipt", err)
	}
	f.Close()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	root = mustAbsolute(t, root)
	repo := filepath.Join(root, "site")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "main.go", "main_test.go"} {
		data, err := os.ReadFile(filepath.Join("../../../testdata/development/site", name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(repo, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "init", "--quiet", "--template=")
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "Website fixture")
	helper := filepath.Join(root, "proof")
	build := exec.Command(goBinary, "build", "-o", helper, "../../../testdata/development/proof/main.go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	cfg := developmentSnapshot(t, repo, helper, goBinary)
	cfg = loadDevelopmentFiles(t, root, cfg)
	// Retain a reproducible selected definition, excluding account material.
	public := runner.Clone(cfg)
	public.Instance.Codex.AuthFile = "<normal local account>"
	data, _ := json.MarshalIndent(public, "", "  ")
	os.WriteFile(filepath.Join(root, "selected.json"), data, 0600)
	calls := 0
	var mu sync.Mutex
	hook := func(s *Service) {
		s.Codex.connect = func(ctx context.Context, path string, notify func(rpcMessage)) (codexPeer, error) {
			p, err := dialCodex(ctx, path, notify)
			if err != nil {
				return nil, err
			}
			return &workflowNativePeer{p, &mu, receipt, &calls}, nil
		}
		s.commands.launchWorker = func(file string, finished func()) error {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=3m")
			cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+file)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); finished() }()
			return nil
		}
	}
	var script *scriptedPeer
	if probe {
		approval := goodTurn()
		approval.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Scripted qualification review; fixture tests and benchmark are real."},"reason":""}`
		script = &scriptedPeer{results: []nativeTurn{goodTurn(), goodTurn(), approval}}
		script.onTurn = func(dir string) {
			if script.starts < 2 {
				if err := os.WriteFile(filepath.Join(dir, "greeting.go"), []byte("package main\nimport \"html\"\nfunc greeting(name string)string{if name==\"\"{name=\"visitor\"};return \"Hello, \"+html.EscapeString(name)+\"!\"}\n"), 0600); err != nil {
					t.Error(err)
				}
			}
		}
	}
	rig := openNativeRig(t, filepath.Join(root, "state"), cfg, script, hook)
	defer func() {
		for _, summary := range rig.core.Status().Runs {
			run, _ := rig.core.Get(summary.ID)
			if run.Active() {
				_, _ = rig.core.Cancel(run.ID)
				settlement, stop := context.WithTimeout(context.Background(), 30*time.Second)
				for run.Active() {
					if _, err := rig.core.Wait(settlement, run.ID, run.Revision); err != nil {
						break
					}
					run, _ = rig.core.Get(run.ID)
				}
				stop()
			}
			encoded, _ := json.MarshalIndent(run, "", "  ")
			_ = os.WriteFile(filepath.Join(root, "run.json"), encoded, 0600)
		}
		rig.close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	connect := func() (*httptest.Server, func(string, any, any)) {
		server := httptest.NewServer(control.Handler(rig.core, "qualification-owner", nil))
		session, err := control.Connect(ctx, server.URL, "qualification-owner")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close(); server.Close() })
		call := func(method string, in, out any) {
			t.Helper()
			data, err := control.Call(ctx, session, method, in)
			if err != nil {
				t.Fatal(method, err)
			}
			if err = json.Unmarshal(data, out); err != nil {
				t.Fatal(err)
			}
		}
		return server, call
	}
	_, call := connect()
	var waiting control.Result
	call("runner_run", control.StartArgs{RequestID: "website-development", Project: "demo", Inputs: map[string]any{"request": "Implement greeting(name string) string in greeting.go. Empty name returns Hello, visitor!; other names return Hello, NAME! with NAME escaped as HTML text using the standard library. Preserve main.go, main_test.go and go.mod. No dependencies or unrelated features. Read prior check, benchmark and review feedback if present; a repeated benchmark does not require an unnecessary code change. Runner owns tests, measurements, review, packaging and browser checks."}}, &waiting)
	if waiting.Status != "Waiting" || waiting.Decision == nil {
		t.Fatalf("workflow did not reach delivery approval: %+v", waiting)
	}
	before, _ := rig.core.Get(waiting.ID)
	if before.RouteUses["measure/continue"] != 1 {
		t.Fatal("iterative benchmark did not run", before.RouteUses)
	}
	// Restart while no execution is live. All prior work and decision identity
	// survive; no model receives a status-polling or reporting assignment.
	rig.close()
	rig = openNativeRig(t, filepath.Join(root, "state"), cfg, nil, hook)
	_, call = connect()
	var approved runner.Summary
	call("runner_decide", runner.DecisionRequest{RequestID: "fixture-delivery-approval", RunID: waiting.ID, DecisionID: waiting.Decision.ID, Choice: "approve"}, &approved)
	var done control.Result
	call("runner_result", control.ResultArgs{RunID: waiting.ID, Wait: true}, &done)
	if done.Status != "Completed" || done.CleanupPending || !done.ExecutionSettled || done.Outputs["executable"].ArtifactID == "" {
		t.Fatalf("full workflow failed: %+v", done)
	}
	record, _ := rig.core.Get(done.ID)
	encoded, _ := json.MarshalIndent(record, "", "  ")
	os.WriteFile(filepath.Join(root, "run.json"), encoded, 0600)
	encoded, _ = json.MarshalIndent(done, "", "  ")
	os.WriteFile(filepath.Join(root, "result.json"), encoded, 0600)
	if !probe && (calls < 3 || calls > 4) {
		t.Fatal("unexpected model count", calls)
	}
	// A completed request rejoin must not execute any stage again.
	var rejoined control.Result
	call("runner_result", control.ResultArgs{RunID: done.ID, Wait: true}, &rejoined)
	if rejoined.Attempts != done.Attempts {
		t.Fatal("result rejoin repeated work")
	}
	if probe {
		reused := 0
		for _, a := range record.Attempts {
			if a.ReusedFrom != "" {
				reused++
			}
		}
		if reused != 1 {
			t.Fatal("unchanged candidate verification was repeated", reused)
		}
	}
	t.Logf("complete workflow: attempts=%d model_turns=%d worker_tokens=%d elapsed=%.3fs root=%s", done.Attempts, calls, done.Usage.KnownTotalTokens, done.ElapsedSeconds, root)
}

func loadDevelopmentFiles(t *testing.T, root string, cfg runner.Snapshot) runner.Snapshot {
	t.Helper()
	dir := filepath.Join(root, "config")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value any) {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := cfg.Projects["demo"]
	project := runner.ProjectFile{ID: p.ID, Name: "Development qualification", Workspace: p.Workspace, Source: p.Source, DefaultProcess: p.DefaultProcess, Roles: map[string]runner.RoleBinding{}, Processes: map[string]string{}}
	for name, role := range p.Roles {
		file := name + ".role.json"
		write(file, role)
		project.Roles[name] = runner.RoleBinding{File: file}
	}
	for name, process := range p.Processes {
		file := name + ".process.json"
		write(file, process)
		project.Processes[name] = file
	}
	write("project.json", project)
	instance := cfg.Instance
	instance.Projects = []string{"project.json"}
	write("instance.json", instance)
	loader, err := config.New(filepath.Join(dir, "instance.json"))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	return selected
}
func mustAbsolute(t *testing.T, path string) string {
	t.Helper()
	p, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func developmentSnapshot(t *testing.T, repo, helper, goBinary string) runner.Snapshot {
	t.Helper()
	cfg := workspaceSnapshot(t, repo)
	home, _ := os.UserHomeDir()
	cfg.Instance.Codex.Binary = os.Getenv("RUNNER_WORKFLOW_CODEX")
	if cfg.Instance.Codex.Binary == "" {
		t.Fatal("RUNNER_WORKFLOW_CODEX must select the pinned harness")
	}
	cfg.Instance.Codex.AuthFile = filepath.Join(home, ".codex/auth.json")
	cfg.Instance.Codex.GoBinary = goBinary
	cfg.Instance.Codex.ReadOnlyPaths = []string{"go.mod", "main.go", "main_test.go"}
	cfg.Instance.Codex.CommandReadPaths = []string{helper, os.Getenv("RUNNER_NODE"), os.Getenv("RUNNER_BROWSER_SCRIPT"), filepath.Dir(filepath.Dir(os.Getenv("RUNNER_PLAYWRIGHT_MODULE"))), filepath.Dir(os.Getenv("RUNNER_CHROMIUM")), "/System/Library/CoreServices/SystemAppearance.bundle"}
	cfg.Instance.Storage.Database.MaxRecord = 64 << 10
	cfg.Instance.Storage.Database.MaxSize = 8 << 20
	cfg.Instance.Storage.MaxArtifact = 16 << 20
	p := cfg.Projects["demo"]
	p.Source = &runner.SourceSettings{Ref: "HEAD"}
	p.Roles = map[string]runner.Role{}
	empty := runner.Text("")
	owner := runner.Role{ID: "owner", Purpose: "Complete the requested small website change, using retained feedback and the existing workspace.", Inputs: map[string]string{"request": "text", "check": "text", "measurement": "text", "review": "text"}, Outputs: map[string]string{"summary": "text", "patch": "artifact"}, Execution: &runner.Profile{Harness: "codex", Model: "gpt-6.1-sol", Reasoning: "xhigh", Access: "workspace-write", MaxInput: 8192, MaxOutput: 2048, Instructions: "Use focused reads and make the complete requested change. Preserve protected fixture files. Do not run tests, benchmarks, Git or external work; configured steps own those. Finish with a short concrete summary. No extra features or abstractions."}}
	p.Roles["owner"] = owner
	command := func(name string, inputs map[string]string, cmd *runner.Command, artifact bool) {
		outputs := map[string]string{"check": "text"}
		if artifact {
			outputs["artifact"] = "artifact"
		}
		p.Roles[name] = runner.Role{ID: name, Purpose: "Run the fixed " + name + " command.", Inputs: inputs, Outputs: outputs, Execution: &runner.Profile{Harness: "command", Access: "workspace-read", Command: cmd}}
	}
	command("verify", map[string]string{"patch": "artifact"}, &runner.Command{Args: []string{goBinary, "test", "-mod=readonly", "-run=^TestGreeting$|^TestPage$", "."}, ExitOutcomes: map[int]string{1: "changes_requested"}}, false)
	command("measure", map[string]string{"patch": "artifact", "previous": "text"}, &runner.Command{Args: []string{helper, "measure", goBinary, "{scratch}", "{text.previous}"}, Result: "domain.json", Artifact: "measurements.json"}, true)
	review := runner.Clone(owner)
	review.ID = "review"
	review.Purpose = "Independently review the requested website behavior, retained candidate, actual test results and two benchmark rounds. Do not rerun checks or edit. Return outcome ready and verdict APPROVE when accepted, otherwise changes_requested and verdict BLOCKED with concrete findings."
	review.Inputs = map[string]string{"request": "text", "patch": "artifact", "check": "text", "measurement": "text"}
	review.Outputs = map[string]string{"verdict": "text", "summary": "text"}
	review.Execution.Access = "workspace-read"
	review.Execution.Instructions = "Review only the concrete request, candidate and supplied evidence. Read the retained patch and affected source. Do not repair or invent requirements. Measurements prove execution, not a general performance advantage. Keep the review concise."
	p.Roles["review"] = review
	p.Roles["integrate"] = runner.Role{ID: "integrate", Purpose: "Retain the approved local candidate.", Inputs: map[string]string{"patch": "artifact", "verdict": "text", "review": "text"}, Outputs: map[string]string{"candidate": "text"}, Execution: &runner.Profile{Harness: "git-candidate", Access: "workspace-read"}}
	command("package", map[string]string{"patch": "artifact"}, &runner.Command{Args: []string{helper, "package", goBinary, "{scratch}"}, Artifact: "website"}, true)
	command("smoke", map[string]string{"executable": "artifact"}, &runner.Command{Args: []string{helper, "smoke", goBinary, "{scratch}", "{input.executable}"}, Network: true, Env: map[string]string{"OPENSSL_CONF": "/dev/null", "RUNNER_NODE": os.Getenv("RUNNER_NODE"), "RUNNER_BROWSER_SCRIPT": os.Getenv("RUNNER_BROWSER_SCRIPT"), "RUNNER_PLAYWRIGHT_MODULE": os.Getenv("RUNNER_PLAYWRIGHT_MODULE"), "RUNNER_CHROMIUM": os.Getenv("RUNNER_CHROMIUM")}}, false)
	patch := runner.Binding{FromCandidate: true}
	proc := runner.Process{ID: "develop", Inputs: map[string]string{"request": "text"}, MaxAttempts: 16, MaxAttemptsPerStep: 1, AttemptTimeout: "4m", Steps: []runner.Step{
		{ID: "owner", Role: "owner", Inputs: map[string]runner.Binding{"request": {FromInput: "request"}, "check": {FromStep: "verify", Output: "check", Default: &empty, Previous: true}, "measurement": {FromStep: "measure", Output: "check", Default: &empty, Previous: true}, "review": {FromStep: "review", Output: "summary", Default: &empty, Previous: true}}},
		{ID: "verify", Role: "verify", Reuse: true, Inputs: map[string]runner.Binding{"patch": patch}, Routes: map[string]runner.Route{"ready": {Next: "measure"}, "changes_requested": {Next: "owner", MaxTimes: 1}}},
		{ID: "measure", Role: "measure", Inputs: map[string]runner.Binding{"patch": patch, "previous": {FromStep: "measure", Output: "check", Default: &empty, Previous: true}}, Routes: map[string]runner.Route{"ready": {Next: "review"}, "continue": {Next: "owner", MaxTimes: 1}}},
		{ID: "review", Role: "review", Inputs: map[string]runner.Binding{"request": {FromInput: "request"}, "patch": patch, "check": {FromStep: "verify", Output: "check"}, "measurement": {FromStep: "measure", Output: "check"}}, Routes: map[string]runner.Route{"ready": {Next: "integrate"}, "changes_requested": {Next: "owner", MaxTimes: 1}}},
		{ID: "integrate", Role: "integrate", Inputs: map[string]runner.Binding{"patch": patch, "verdict": {FromStep: "review", Output: "verdict"}, "review": {FromStep: "review", Output: "summary"}}, Routes: map[string]runner.Route{"ready": {Next: "package", Approval: true}}},
		{ID: "package", Role: "package", Inputs: map[string]runner.Binding{"patch": patch}},
		{ID: "smoke", Role: "smoke", Inputs: map[string]runner.Binding{"executable": {FromStep: "package", Output: "artifact"}}},
	}, Outputs: map[string]runner.Binding{"candidate": {FromStep: "integrate", Output: "candidate"}, "measurement": {FromStep: "measure", Output: "artifact"}, "executable": {FromStep: "package", Output: "artifact"}, "browser": {FromStep: "smoke", Output: "check"}}}
	p.DefaultProcess = "develop"
	p.Processes = map[string]runner.Process{"develop": proc}
	cfg.Projects["demo"] = p
	return cfg
}
