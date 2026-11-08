//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type writableQualificationPeer struct {
	codexPeer
	root     string
	calls    *atomic.Int32
	mu       *sync.Mutex
	commands *[]string
}

func (p *writableQualificationPeer) Call(ctx context.Context, method string, args, out any) error {
	if method == "turn/start" {
		if !p.calls.CompareAndSwap(0, 1) {
			return errors.New("one-turn writable qualification allowance exhausted")
		}
		receipt, e := os.OpenFile(filepath.Join(p.root, "turn-requested.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return errors.New("qualification already dispatched or receipt cannot be secured: " + e.Error())
		}
		b, _ := json.Marshal(map[string]any{"model": "gpt-6.1-sol", "reasoning": "xhigh", "requested_at": time.Now().UTC()})
		_, e = receipt.Write(b)
		if e == nil {
			e = receipt.Sync()
		}
		closeErr := receipt.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if method == "thread/delete" && p.calls.Load() > 0 {
		var r struct {
			Thread struct {
				Turns []struct {
					Items []struct {
						Type    string `json:"type"`
						Command string `json:"command"`
					} `json:"items"`
				} `json:"turns"`
			} `json:"thread"`
		}
		if e := p.codexPeer.Call(ctx, "thread/read", map[string]any{"threadId": args.(map[string]string)["threadId"], "includeTurns": true}, &r); e != nil {
			return e
		}
		p.mu.Lock()
		for _, turn := range r.Thread.Turns {
			for _, item := range turn.Items {
				if item.Type == "commandExecution" && len(*p.commands) < 16 {
					*p.commands = append(*p.commands, runner.Clip(item.Command))
				}
			}
		}
		p.mu.Unlock()
	}
	return p.codexPeer.Call(ctx, method, args, out)
}
func qualifyWorkspacePermissions(t *testing.T, ctx context.Context, root string, cfg runner.Snapshot) {
	t.Helper()
	policy := filepath.Join(root, "permission-probe")
	if e := os.MkdirAll(policy, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(policy)
	disk, e := runner.OpenDisk(filepath.Join(policy, "state"), cfg.Instance.Storage)
	if e != nil {
		t.Fatal(e)
	}
	c := &Codex{disk: disk, cfg: cfg.Instance.Codex, ctx: ctx}
	config, e := c.nativeConfiguration(ctx)
	if e != nil {
		t.Fatal(e)
	}
	home := filepath.Join(policy, "config")
	if e = os.Mkdir(home, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	source := filepath.Join(disk.Root, "work", "run_permission", "source")
	if e = os.MkdirAll(source, 0700); e != nil {
		t.Fatal(e)
	}
	for _, file := range append([]string{".git", ".gitattributes"}, cfg.Instance.Codex.ReadOnlyPaths...) {
		if e = os.WriteFile(filepath.Join(source, file), []byte(func() string {
			if file == "go.mod" {
				return "module probe\n\ngo 1.26.0\n"
			}
			if file == ".gitattributes" {
				return "* text=auto\n"
			}
			return "dummy\n"
		}()), 0600); e != nil {
			t.Fatal(e)
		}
	}
	private := filepath.Join(policy, "private-dummy")
	if e = os.WriteFile(private, []byte("dummy"), 0600); e != nil {
		t.Fatal(e)
	}
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: "permission"},
		Workspace:  &workspaceRecord{WorkDir: source},
	}
	env, e := c.workerEnvironment(j)
	if e != nil {
		t.Fatal(e)
	}
	if e = disk.Write("artifacts/permission-dummy", []byte("private artifact"), 128); e != nil {
		t.Fatal(e)
	}
	env["RUNNER_ARTIFACT_STORE_PROBE"] = filepath.Join(disk.Root, "artifacts", "permission-dummy")
	staged := j
	staged.Assignment = runner.Assignment{
		RunID:       "run_permission",
		ProjectID:   "permission",
		AttemptID:   j.Assignment.AttemptID,
		Task:        "Use supplied evidence",
		OutputTypes: map[string]string{"summary": "text"},
		Outcomes:    []string{"blocked"},
		Inputs: map[string]runner.Value{
			"artifact_evidence": {ArtifactID: "permission-dummy"},
			"inline_evidence":   runner.Text(strings.Repeat("retained evidence; ", 500)),
		},
		Setup: runner.ExecutionSetup{Profile: runner.Profile{
			Harness:   "codex",
			Model:     "gpt-6.1-sol",
			Reasoning: "xhigh",
			Access:    "workspace-write",
			MaxInput:  4096,
			MaxOutput: 512,
		}},
		Limits: runner.ExecutionLimits{MaxArtifact: disk.Policy.MaxArtifact},
	}
	if e = c.workspaceInputs(staged); e != nil {
		t.Fatal(e)
	}
	inputs := filepath.Join(source, ".runner-tmp", "inputs", j.Assignment.AttemptID)
	env["RUNNER_EVIDENCE_PROBE"] = filepath.Join(inputs, "artifact_evidence")
	env["RUNNER_INLINE_EVIDENCE_PROBE"] = filepath.Join(inputs, "inline_evidence")
	probe := filepath.Join(env["TMPDIR"], "access-probe.go")
	if e = os.WriteFile(probe, []byte(workspaceProbe), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(env["TMPDIR"], "escape")
	if e = os.Symlink(private, link); e != nil {
		t.Fatal(e)
	}
	binary, _, e := c.goInstallation(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, profile := range []struct{ name, mode string }{{"runner-edit", "write"}, {"runner-check", "read"}} {
		target, e := os.CreateTemp(source, ".runner-probe-")
		if e != nil {
			t.Fatal(e)
		}
		target.Close()
		defer os.Remove(target.Name())
		env["RUNNER_SOURCE_PROBE"] = target.Name()
		argv := append([]string{binary, "run", probe, source, private, profile.mode, link}, cfg.Instance.Codex.ReadOnlyPaths...)
		args := append([]string{"sandbox", "-P", profile.name, "-C", source, "--"}, envCommand(env, argv...)...)
		command := exec.CommandContext(ctx, cfg.Instance.Codex.Binary, args...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + home}
		diagnostic := &diagnosticTail{limit: 4096}
		command.Stdout = diagnostic
		command.Stderr = diagnostic
		if e = command.Run(); e != nil {
			t.Fatalf("zero-turn %s permission probe: %v: %s", profile.name, e, diagnostic.bytes())
		}
	}
	for _, runtime := range []struct{ path, command string }{
		{cfg.Instance.Codex.GoModuleCache, "/bin/ls"},
		{cfg.Instance.Codex.GoCheckBinary, "/bin/cat"},
	} {
		if runtime.path == "" {
			continue
		}
		command := exec.CommandContext(ctx, cfg.Instance.Codex.Binary, "sandbox", "-P", "runner-input", "-C", source, "--", runtime.command, runtime.path)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "CODEX_HOME=" + home}
		tail := &diagnosticTail{limit: 1024}
		command.Stdout, command.Stderr = tail, tail
		err := command.Run()
		out := tail.bytes()
		if err == nil || (!strings.Contains(string(out), "Operation not permitted") && !strings.Contains(string(out), "Permission denied")) {
			t.Fatalf("input-only profile must deny selected workspace runtime access: %v: %s", err, out)
		}
	}
	t.Log("zero-turn native permission probes passed: source/scratch, protected files, private data, Git, symlink escape, selected read-only Go runtimes and network")
}

// This opt-in spends no model turns. Reuse the source/scratch/network permission
// probe and verify the real native service accepts the review permission profile.
func TestNativeReviewPreflight(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_PREFLIGHT") != "1" {
		t.Skip("explicit opt-in for native permission checks; zero model turns")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(filepath.Join(home, ".codex"), "rrp-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	cfg := workspaceSnapshot(t, sourceRepositoryAt(t, filepath.Join(root, "source")))
	cfg.Instance.Codex.AuthFile = filepath.Join(home, ".codex", "auth.json")
	cfg.Instance.Codex.Binary = os.Getenv("RUNNER_PREFLIGHT_CODEX")
	cfg.Instance.Codex.GoModuleCache = filepath.Join(root, "modcache")
	if err := os.Mkdir(cfg.Instance.Codex.GoModuleCache, 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Instance.Codex.GoCheckBinary = filepath.Join(root, "runner-go-check")
	if err := os.WriteFile(cfg.Instance.Codex.GoCheckBinary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if goBinary := os.Getenv("RUNNER_PREFLIGHT_GO"); goBinary != "" {
		cfg.Instance.Codex.GoBinary = goBinary
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	qualifyWorkspacePermissions(t, ctx, root, cfg)
	rig := openNativeRig(t, filepath.Join(root, "state"), cfg, nil)
	defer rig.close()
	peer, err := rig.service.Codex.openPeer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.service.Codex.checkWorkspaceConfig(ctx, peer); err != nil {
		t.Fatal(err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		Active struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
	}
	if err := peer.Call(ctx, "thread/start", map[string]any{
		"model": "gpt-6.1-sol", "approvalPolicy": "never", "permissions": "runner-check",
		"cwd": filepath.Join(root, "source"), "runtimeWorkspaceRoots": []string{filepath.Join(root, "source")},
		"environments": []any{map[string]any{"environmentId": "local", "cwd": filepath.Join(root, "source"), "runtimeWorkspaceRoots": []string{filepath.Join(root, "source")}}},
		"historyMode":  "legacy", "config": map[string]any{"model_reasoning_effort": "xhigh", "features.shell_tool": true},
	}, &thread); err != nil {
		t.Fatal(err)
	}
	if thread.Thread.ID == "" || thread.Active.ID != "runner-check" {
		t.Fatalf("native review profile differs: %+v", thread)
	}
	if err := peer.Call(ctx, "thread/delete", map[string]string{"threadId": thread.Thread.ID}, nil); err != nil && !strings.Contains(err.Error(), "no rollout found") {
		t.Fatal(err)
	}
	t.Log("read-only review profile accepted; no turn/start submitted")
	if intake := os.Getenv("RUNNER_COORDINATOR_HOME"); intake != "" {
		qualifyCoordinatorTools(t, ctx, root, cfg.Instance.Codex.Binary, intake)
	}
}

func qualifyCoordinatorTools(t *testing.T, ctx context.Context, root, binary, home string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := os.ReadFile(os.Getenv("RUNNER_COORDINATOR_TOKEN_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	intakeRoot := filepath.Join(root, "intake")
	if err := os.MkdirAll(filepath.Join(intakeRoot, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	// Keep the production wire's home-identity check intact.
	if err := os.Symlink(home, filepath.Join(intakeRoot, "native")); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(intakeRoot, "jobs", "codex.sock")
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "unix://"+socket, "--strict-config")
	cmd.Dir = filepath.Join(filepath.Dir(home), "empty")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "CODEX_HOME=" + home, "RUNNER_OWNER_TOKEN=" + strings.TrimSpace(string(token))}
	tail := &diagnosticTail{limit: 2048}
	cmd.Stdout, cmd.Stderr = tail, tail
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var peer codexPeer
	var lastError error
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for peer == nil {
		peer, err = dialCodex(ctx, socket, func(rpcMessage) {})
		if err == nil {
			break
		}
		lastError = err
		select {
		case <-ctx.Done():
			t.Fatalf("coordinator readiness: %v; last: %v: %s", ctx.Err(), lastError, tail.bytes())
		case <-tick.C:
		}
	}
	defer peer.Close()
	var config struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := peer.Call(ctx, "config/read", map[string]bool{"includeLayers": false}, &config); err != nil {
		t.Fatal(err)
	}
	var features map[string]bool
	_ = json.Unmarshal(config.Config["features"], &features)
	for _, name := range []string{"shell_tool", "apply_patch_freeform", "multi_agent", "apps", "plugins", "remote_plugin", "view_image"} {
		if enabled, exists := features[name]; !exists || enabled {
			t.Fatalf("coordinator capability %s not explicitly disabled", name)
		}
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := peer.Call(ctx, "thread/start", map[string]any{"model": "gpt-6.1-sol", "approvalPolicy": "never", "sandbox": "read-only", "cwd": filepath.Dir(home) + "/empty", "environments": []any{}, "runtimeWorkspaceRoots": []any{}, "historyMode": "legacy"}, &thread); err != nil {
		t.Fatal(err)
	}
	defer peer.Call(context.Background(), "thread/delete", map[string]string{"threadId": thread.Thread.ID}, nil)
	var inventory struct {
		Data []struct {
			Name  string                     `json:"name"`
			Tools map[string]json.RawMessage `json:"tools"`
		} `json:"data"`
	}
	if err := peer.Call(ctx, "mcpServerStatus/list", map[string]any{"threadId": thread.Thread.ID, "detail": "toolsAndAuthOnly"}, &inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Data) != 1 || inventory.Data[0].Name != "runner" || len(inventory.Data[0].Tools) != 3 {
		t.Fatalf("unexpected coordinator tool inventory: %+v", inventory)
	}
	for _, name := range []string{"runner_run", "runner_result", "runner_status"} {
		if _, ok := inventory.Data[0].Tools[name]; !ok {
			t.Fatalf("coordinator lacks %s", name)
		}
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := peer.Call(ctx, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "runner", "tool": "runner_status", "arguments": map[string]any{}}, &result); err != nil || result.IsError {
		t.Fatalf("coordinator cannot read Runner status: %v, %+v", err, result)
	}
	if err := peer.Call(ctx, "mcpServer/tool/call", map[string]any{"threadId": thread.Thread.ID, "server": "runner", "tool": "runner_configure", "arguments": map[string]any{"file": "not-selected", "field": "concurrency", "value": 1}}, &result); err == nil && !result.IsError {
		t.Fatal("coordinator invoked an unavailable configuration tool")
	}
	t.Log("coordinator exposes only run/result/status; native status call passed; zero model turns")
}
func writableQualificationRoot(t *testing.T) (string, time.Time, context.Context) {
	t.Helper()
	root := os.Getenv("RUNNER_WORKSPACE_QUAL_DIR")
	if root == "" {
		t.Fatal("RUNNER_WORKSPACE_QUAL_DIR must name the authorized private qualification directory")
	}
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("qualification directory must exist with mode 0700")
	}
	if e = writableLocation(root); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(root, "turn-requested.json")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("qualification receipt already exists; no further turn is authorized")
	}
	started := time.Now()
	startFile := filepath.Join(root, "qualification-start.json")
	var start struct {
		At time.Time `json:"started_at"`
	}
	if data, e := os.ReadFile(startFile); e == nil {
		if e = json.Unmarshal(data, &start); e != nil {
			t.Fatal(e)
		}
		started = start.At
	} else if errors.Is(e, os.ErrNotExist) {
		data, _ := json.Marshal(map[string]any{"started_at": started})
		if e = os.WriteFile(startFile, data, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		t.Fatal(e)
	}
	if time.Until(started.Add(15*time.Minute)) < 90*time.Second {
		t.Fatal("qualification finish/cleanup reserve reached; no turn dispatched")
	}
	ctx, cancel := context.WithDeadline(context.Background(), started.Add(15*time.Minute))
	t.Cleanup(cancel)
	return root, started, ctx
}
func TestNativeWritableQualification(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_WORKSPACE") != "1" {
		t.Skip("explicit opt-in required; at most one Sol/xhigh coding turn")
	}
	root, started, ctx := writableQualificationRoot(t)
	repo := sourceRepositoryAt(t, filepath.Join(root, "repository"))
	cfg := workspaceSnapshot(t, repo)
	home, e := os.UserHomeDir()
	if e != nil {
		t.Fatal(e)
	}
	cfg.Instance.Codex.AuthFile = filepath.Join(home, ".codex", "auth.json")
	cfg.Instance.Storage.MinFree = 1 << 30
	cfg.Instance.Storage.Logs.MaxPerAttempt = 8 << 10
	codingAllowance := min(5*time.Minute, time.Until(started.Add(15*time.Minute))-90*time.Second)
	project := cfg.Projects["demo"]
	process := project.Processes["seats"]
	process.AttemptTimeout = codingAllowance.String()
	project.Processes["seats"] = process
	cfg.Projects["demo"] = project
	qualifyWorkspacePermissions(t, ctx, root, cfg)
	var calls atomic.Int32
	var mu sync.Mutex
	var commands []string
	rig := openNativeRig(t, filepath.Join(root, "state"), cfg, nil, func(s *Service) {
		s.Codex.connect = func(ctx context.Context, socket string, notify func(rpcMessage)) (codexPeer, error) {
			p, e := dialCodex(ctx, socket, notify)
			if e != nil {
				return nil, e
			}
			return &writableQualificationPeer{p, root, &calls, &mu, &commands}, nil
		}
	})
	closed := false
	defer func() {
		if !closed {
			rig.close()
		}
	}()
	readyPeer, e := rig.service.Codex.openPeer(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = checkNativeConfig(ctx, readyPeer); e != nil {
		t.Fatal(e)
	}
	var inputOnly struct {
		Thread nativeThread `json:"thread"`
		Active struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
		Sandbox struct {
			Type    string `json:"type"`
			Network bool   `json:"networkAccess"`
		} `json:"sandbox"`
		Roots []string `json:"runtimeWorkspaceRoots"`
	}
	if e = readyPeer.Call(ctx, "thread/start", map[string]any{
		"model":                 "gpt-6.1-sol",
		"approvalPolicy":        "never",
		"permissions":           "runner-input",
		"cwd":                   filepath.Join(rig.disk.Root, "native", "empty"),
		"baseInstructions":      nativeBase,
		"environments":          []any{},
		"runtimeWorkspaceRoots": []any{},
		"historyMode":           "legacy",
	}, &inputOnly); e != nil {
		t.Fatal(e)
	}
	if inputOnly.Active.ID != "runner-input" || inputOnly.Sandbox.Type != "readOnly" || inputOnly.Sandbox.Network || len(inputOnly.Roots) != 0 || string(inputOnly.Thread.Environments) != "[]" {
		t.Fatal("input-only restrictions changed in the workspace-enabled instance")
	}
	rawPeer := readyPeer
	if wrapped, ok := readyPeer.(*writableQualificationPeer); ok {
		rawPeer = wrapped.codexPeer
	}
	if e = rawPeer.Call(ctx, "thread/delete", map[string]string{"threadId": inputOnly.Thread.ID}, nil); e != nil {
		var rpc *rpcError
		if !errors.As(e, &rpc) || rpc.Code != -32600 || rpc.Message != "no rollout found for thread id "+inputOnly.Thread.ID {
			t.Fatal(e)
		}
	}
	t.Log("zero-turn input-only thread restrictions confirmed")
	r, e := rig.core.Start(runner.StartRequest{RequestID: "native-source", Project: "demo", Inputs: map[string]runner.Value{}})
	if e != nil {
		t.Fatal(e)
	}
	var done runner.Run
	for {
		done, e = rig.core.Get(r.ID)
		if e != nil {
			t.Fatal(e)
		}
		if done.Terminal() && (done.CleanupError != "" || (allReleased(done) && (done.Status == "Failed" || done.ResourcesReleased))) {
			break
		}
		if _, e = rig.core.Wait(ctx, r.ID, done.Revision); e != nil {
			t.Fatalf("qualification: %v; %s: %s; retained at %s", e, done.Status, done.Reason, root)
		}
	}
	if done.Status != "Completed" || done.CleanupError != "" || len(done.Attempts) != 2 || calls.Load() != 1 {
		t.Fatalf("writable journey did not finish: %s: %s; calls=%d; retained at %s", done.Status, done.Reason, calls.Load(), root)
	}
	source := done.Attempts[0].Source
	if source == nil || source.PatchID == "" {
		t.Fatal("saved source identity absent")
	}
	patch, e := rig.disk.Read("artifacts/"+source.PatchID, cfg.Instance.Storage.MaxArtifact)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Count(patch, []byte("diff --git ")) != 1 || !bytes.Contains(patch, []byte("diff --git a/seats.go b/seats.go")) {
		t.Fatal("worker changed files outside its requested scope")
	}
	if _, e = os.Stat(source.WorkDir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("original worktree not released")
	}
	restored := filepath.Join(root, "restored")
	fixtureGit(t, repo, "worktree", "add", "--detach", restored, source.Base)
	file := filepath.Join(root, "retained.patch")
	if e = os.WriteFile(file, patch, 0600); e != nil {
		t.Fatal(e)
	}
	fixtureGit(t, restored, "apply", "--index", file)
	if strings.TrimSpace(string(fixtureGit(t, restored, "write-tree"))) != source.Tree {
		t.Fatal("restored source differs from the checked candidate")
	}
	code, e := os.ReadFile(filepath.Join(restored, "seats.go"))
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(root, "qualified-seats.go"), code, 0600); e != nil {
		t.Fatal(e)
	}
	// Do not rerun the suite: restoration only needs to preserve the source bytes.
	mu.Lock()
	recorded := append([]string(nil), commands...)
	mu.Unlock()
	for _, command := range recorded {
		if strings.Contains(command, "go test") {
			t.Error("worker duplicated the configured check")
		}
		if strings.Contains(strings.ToLower(command), "python") {
			t.Error("worker used Python")
		}
	}
	fixtureGit(t, repo, "worktree", "remove", "--force", restored)
	report := map[string]any{
		"status":                     done.Status,
		"elapsed_ms":                 time.Since(started).Milliseconds(),
		"coding_attempt_ms":          done.Attempts[0].FinishedAt.Sub(done.Attempts[0].Started).Milliseconds(),
		"submitted_turns":            calls.Load(),
		"usage":                      done.Attempts[0].Usage,
		"native_peak_bytes":          rig.disk.NativePeak(),
		"managed_content_peak_bytes": rig.disk.Peak(),
		"database_reserved_bytes":    cfg.Instance.Storage.Database.MaxSize,
		"patch_bytes":                len(patch),
		"worker_commands":            recorded,
		"coordination_tokens":        nil,
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "result.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log(string(b))
	t.Logf("qualified source and one-turn receipt: %s", root)
	rig.core.Close()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	if e = rig.service.Close(closeCtx); e != nil {
		t.Fatal(e)
	}
	if e = rig.service.ForgetRun(ctx, done.ID); e != nil {
		t.Fatal(e)
	}
	if e = rig.store.Close(); e != nil {
		t.Fatal(e)
	}
	closed = true
	// The retained code/result/receipt are small. Remove only disposable fixture data.
	for _, name := range []string{"repository", "state", "retained.patch"} {
		if e = os.RemoveAll(filepath.Join(root, name)); e != nil {
			t.Fatal(e)
		}
	}
	report["elapsed_ms"] = time.Since(started).Milliseconds()
	report["cleanup"] = "completed"
	b, _ = json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "result.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
}
