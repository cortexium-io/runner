package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

// These tests own native protocol translation/correlation. They use a scripted
// native peer with real job files and core/store wiring, not a duplicate of the
// core's general lifecycle matrix. Native qualification is separate and opt-in.
type scriptedPeer struct {
	mu           sync.Mutex
	done         chan struct{}
	once         sync.Once
	notify       func(rpcMessage)
	turn         nativeTurn
	thread       string
	config       *runner.CodexSettings
	onTurn       func(string)
	workspace    string
	checks       int
	starts       int
	reads        int
	deleted      int
	lostReply    bool
	readError    bool
	wrongModel   bool
	stayActive   bool
	gate         chan struct{}
	entered      chan struct{}
	deleteErrors []error
	results      []nativeTurn
	permissions  []string
	instructions []string
}

func (p *scriptedPeer) Done() <-chan struct{} { return p.done }
func (p *scriptedPeer) Close()                { p.once.Do(func() { close(p.done) }) }
func (p *scriptedPeer) Call(ctx context.Context, method string, params, out any) error {
	if method == "config/read" && p.gate != nil {
		select {
		case p.entered <- struct{}{}:
		default:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.gate:
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var result any
	switch method {
	case "config/read":
		features := map[string]bool{}
		for _, name := range []string{
			"shell_tool",
			"apply_patch_freeform",
			"apps",
			"hooks",
			"plugins",
			"remote_plugin",
			"multi_agent",
			"goals",
			"memories",
			"view_image",
			"request_permissions",
			"shell_snapshot",
			"skill_mcp_dependency_install",
		} {
			features[name] = false
		}
		features["skip_host_skill_discovery"] = true
		result = map[string]any{"config": map[string]any{
			"features":              features,
			"mcp_servers":           map[string]any{},
			"plugins":               map[string]any{},
			"marketplaces":          map[string]any{},
			"web_search":            "disabled",
			"project_doc_max_bytes": 0,
			"skills":                map[string]any{"include_instructions": false, "bundled": map[string]bool{"enabled": false}},
		}}
		if p.config != nil && p.config.GoBinary != "" {
			c := &Codex{cfg: p.config}
			_, goRoot, e := c.goInstallation(ctx)
			if e != nil {
				return e
			}
			profiles := map[string]any{}
			names := []string{"runner-input", "runner-edit", "runner-check"}
			if p.config.Genny != nil {
				names = append(names, "runner-genny-edit")
			}
			for _, name := range names {
				profiles[name] = map[string]any{"filesystem": workspaceFiles(p.config, name, goRoot), "network": map[string]any{"enabled": false}}
			}
			result.(map[string]any)["config"].(map[string]any)["permissions"] = profiles
		}
	case "model/list":
		result = map[string]any{"data": []any{map[string]any{
			"id":                        "gpt-6.1-sol",
			"supportedReasoningEfforts": []any{map[string]string{"reasoningEffort": "xhigh"}},
		}}}
	case "command/exec":
		args := params.(map[string]any)
		argv := args["command"].([]string)
		for _, arg := range argv {
			if arg == "test" {
				p.checks++
			}
		}
		result = map[string]any{"exitCode": 0, "stdout": "ok fixture", "stderr": ""}
	case "thread/start":
		args := params.(map[string]any)
		permission, _ := args["permissions"].(string)
		p.permissions = append(p.permissions, permission)
		p.instructions = append(p.instructions, args["baseInstructions"].(string))
		if permission == "runner-edit" || permission == "runner-check" || permission == "runner-genny-edit" {
			p.workspace = args["cwd"].(string)
		}
		p.thread = "native-thread"
		model := "gpt-6.1-sol"
		if p.wrongModel {
			model = "other-model"
		}
		result = map[string]any{
			"thread":                map[string]any{"id": p.thread, "historyMode": "legacy", "environments": []any{}},
			"model":                 model,
			"reasoningEffort":       "xhigh",
			"approvalPolicy":        "never",
			"sandbox":               map[string]any{"type": "readOnly", "networkAccess": false},
			"instructionSources":    []any{},
			"runtimeWorkspaceRoots": []any{},
		}
		if p.workspace != "" {
			m := result.(map[string]any)
			m["thread"].(map[string]any)["environments"] = []any{map[string]any{"environmentId": "local", "cwd": p.workspace, "runtimeWorkspaceRoots": []string{p.workspace}}}
			m["activePermissionProfile"] = map[string]any{"id": permission}
			m["runtimeWorkspaceRoots"] = []string{p.workspace}
			m["sandbox"] = map[string]any{"type": "workspaceWrite", "networkAccess": false}
		}
	case "turn/start":
		if p.onTurn != nil {
			p.onTurn(p.workspace)
		}
		p.starts++
		p.turn = nativeTurn{ID: "native-turn", Status: "inProgress"}
		if !p.stayActive {
			p.turn = goodTurn()
			if len(p.results) != 0 {
				p.turn = p.results[0]
				p.results = p.results[1:]
			}
		}
		if p.lostReply {
			return io.ErrUnexpectedEOF
		}
		result = map[string]any{"turn": p.turn}
		if !p.stayActive {
			p.emit("thread/tokenUsage/updated", map[string]any{
				"threadId":   p.thread,
				"turnId":     p.turn.ID,
				"tokenUsage": map[string]any{"total": map[string]int{"inputTokens": 123, "outputTokens": 15, "cachedInputTokens": 0}},
			})
			p.emit("turn/completed", map[string]any{"threadId": p.thread, "turn": p.turn})
		}
	case "thread/read":
		p.reads++
		if p.readError {
			return &rpcError{-32603, "failed to read session metadata: rollout is empty"}
		}
		result = map[string]any{"thread": nativeThread{ID: p.thread, Turns: []nativeTurn{p.turn}}}
	case "thread/resume":
		result = map[string]any{}
	case "turn/interrupt":
		p.turn.Status = "interrupted"
		p.emit("turn/completed", map[string]any{"threadId": p.thread, "turn": p.turn})
		result = map[string]any{}
	case "thread/delete":
		p.deleted++
		if len(p.deleteErrors) > 0 {
			err := p.deleteErrors[0]
			p.deleteErrors = p.deleteErrors[1:]
			return err
		}
		result = map[string]any{}
	default:
		return errors.New("unexpected native method: " + method)
	}
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(result)
	return json.Unmarshal(b, out)
}
func (p *scriptedPeer) emit(method string, params any) {
	b, _ := json.Marshal(params)
	if p.notify != nil {
		p.notify(rpcMessage{Method: method, Params: b})
	}
}
func goodTurn() nativeTurn {
	var t nativeTurn
	_ = json.Unmarshal([]byte(`{"id":"native-turn","status":"completed","items":[{"type":"agentMessage","phase":"final_answer","text":"{\"outcome\":\"ready\",\"outputs\":{\"summary\":\"The meeting is Tuesday at noon.\"},\"reason\":\"\"}"}]}`), &t)
	return t
}
func nativeSnapshot() runner.Snapshot {
	p := runner.DefaultStorage()
	p.MinFree = 0
	p.MaxManaged = 64 << 20
	p.Database.MaxSize = 4 << 20
	p.MaxArtifact = 4096
	p.Logs.MaxPerAttempt = 2048
	role := runner.Role{
		ID:      "summary",
		Purpose: "Summarize the supplied document.",
		Inputs:  map[string]string{"document": "text"},
		Outputs: map[string]string{"summary": "text"},
		Execution: &runner.Profile{
			Harness:   "codex",
			Model:     "gpt-6.1-sol",
			Reasoning: "xhigh",
			Access:    "input-only",
			MaxInput:  4096,
			MaxOutput: 2048,
		},
	}
	finish := runner.Role{
		ID:        "finish",
		Inputs:    map[string]string{"text": "text"},
		Outputs:   map[string]string{"result": "text"},
		Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Input: "text", Output: "result"},
	}
	proc := runner.Process{
		ID:     "summarize",
		Inputs: map[string]string{"document": "text"},
		Steps: []runner.Step{
			{
				ID:     "summary",
				Role:   "summary",
				Inputs: map[string]runner.Binding{"document": {FromInput: "document"}},
			},
			{
				ID:     "finish",
				Role:   "finish",
				Inputs: map[string]runner.Binding{"text": {FromStep: "summary", Output: "summary"}},
			},
		},
		Outputs:            map[string]runner.Binding{"result": {FromStep: "finish", Output: "result"}},
		MaxAttempts:        2,
		MaxAttemptsPerStep: 1,
		AttemptTimeout:     "2m",
	}
	project := runner.Project{
		ID:             "demo",
		DefaultProcess: "summarize",
		Roles:          map[string]runner.Role{"summary": role, "finish": finish},
		Processes:      map[string]runner.Process{"summarize": proc},
		Profiles:       map[string]string{},
	}
	return runner.Snapshot{
		Instance: runner.Instance{
			Concurrency:       1,
			ReconcileInterval: "5m",
			Storage:           p,
			Codex:             &runner.CodexSettings{Binary: "codex", AuthFile: "/unused", StorageLimit: 16 << 20},
		},
		Projects: map[string]runner.Project{"demo": project},
	}
}

type nativeRig struct {
	core    *runner.Core
	service *Service
	store   *sqlite.Store
	disk    *runner.Disk
}

func openNativeRig(t *testing.T, dir string, cfg runner.Snapshot, script *scriptedPeer, hooks ...func(*Service)) *nativeRig {
	t.Helper()
	store, err := sqlite.Open(dir, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := runner.OpenDisk(dir, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(disk, func(ctx context.Context, id string) ([]byte, error) {
		return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	}, cfg.Instance.Codex)
	if script != nil {
		script.config = cfg.Instance.Codex
		script.done = make(chan struct{})
		script.notify = service.Codex.receive
		service.Codex.peer = script
		service.Codex.generation = "scripted-generation"
	}
	for _, hook := range hooks {
		hook(service)
	}
	core, err := runner.New(context.Background(), store, service, disk, cfg)
	if err != nil {
		t.Fatal(err)
	}
	service.SetWake(core.ExecutionChanged)
	return &nativeRig{core, service, store, disk}
}
func (r *nativeRig) close() {
	r.core.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = r.service.Close(ctx)
	_ = r.store.Close()
}
func startSummary(t *testing.T, c *runner.Core, key string) runner.Run {
	t.Helper()
	r, err := c.Start(runner.StartRequest{
		RequestID: key,
		Project:   "demo",
		Inputs:    map[string]runner.Value{"document": runner.Text("The meeting is Tuesday at noon.")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func awaitRun(t *testing.T, c *runner.Core, id string, predicate func(runner.Run) bool) runner.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	for {
		r, err := c.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(r) {
			return r
		}
		if _, err = c.Wait(ctx, id, r.Revision); err != nil {
			t.Fatalf("waiting for native state: %v; last=%s/%s", err, r.Status, r.Reason)
		}
	}
}
func allReleased(r runner.Run) bool {
	if !r.Terminal() {
		return false
	}
	for _, a := range r.Attempts {
		if !a.Released {
			return false
		}
	}
	return true
}
func TestNativeAdapterCorrelationAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		name      string
		lost, bad bool
		want      string
		starts    int
	}{
		{"push completion without fallback polling", false, false, "Completed", 1},
		{"lost launch acknowledgement", true, false, "Completed", 1},
		{"native settings mismatch", false, true, "Failed", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedPeer{lostReply: tc.lost, wrongModel: tc.bad}
			rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), nativeSnapshot(), p)
			defer rig.close()
			run := startSummary(t, rig.core, tc.name)
			done := awaitRun(t, rig.core, run.ID, allReleased)
			if done.Status != tc.want {
				t.Fatalf("%s: %s", done.Status, done.Reason)
			}
			p.mu.Lock()
			starts, deleted := p.starts, p.deleted
			p.mu.Unlock()
			if starts != tc.starts || deleted != 1 {
				t.Fatalf("unexpected native calls: starts=%d, deleted=%d", starts, deleted)
			}
			if tc.want == "Completed" {
				if len(done.Attempts) != 2 || done.Attempts[0].NativeHandle != "native-thread/native-turn" || done.Attempts[0].Effective == nil || string(done.Outputs["result"].Inline) != `"The meeting is Tuesday at noon."` {
					t.Fatalf("native handoff lost facts: %+v", done)
				}
			}
		})
	}
}

// Prelaunch rejections must preserve their readiness reason and release the
// attempt without a durable job, a native turn or reconciliation polling.
func TestNativeReadinessRejectionWithoutJobSettles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*runner.Profile, runner.StoragePolicy)
		want   string
	}{
		{
			name: "input allowance",
			change: func(p *runner.Profile, _ runner.StoragePolicy) {
				p.MaxInput = 128
			},
			want: "resolved input/instructions/schema exceed max_input",
		},
		{
			name: "record allowance",
			change: func(p *runner.Profile, storage runner.StoragePolicy) {
				p.MaxOutput = storage.Database.MaxRecord
			},
			want: "assignment and result do not fit the native record allowance",
		},
		{
			name: "fixed check assignment",
			change: func(p *runner.Profile, _ runner.StoragePolicy) {
				p.Harness = "go-check"
			},
			want: "invalid fixed Go check assignment",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := nativeSnapshot()
			tc.change(cfg.Projects["demo"].Roles["summary"].Execution, cfg.Instance.Storage)
			peer := &scriptedPeer{}
			rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, peer)
			defer rig.close()
			run := startSummary(t, rig.core, tc.name)
			done := awaitRun(t, rig.core, run.ID, allReleased)
			if done.Status != "Failed" || !strings.Contains(done.Reason, "readiness: "+tc.want) || len(done.Attempts) != 1 {
				t.Fatalf("readiness rejection was lost: %+v", done)
			}
			if _, err := rig.disk.Read(nativeName(done.Attempts[0].ID), cfg.Instance.Storage.Database.MaxRecord); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("prelaunch rejection created a job: %v", err)
			}
			peer.mu.Lock()
			thread, starts := peer.thread, peer.starts
			peer.mu.Unlock()
			if thread != "" || starts != 0 {
				t.Fatalf("prelaunch rejection reached the native model: thread=%q starts=%d", thread, starts)
			}
		})
	}
}

func TestNativeLiveTurnDoesNotRequireFlushedHistory(t *testing.T) {
	p := &scriptedPeer{stayActive: true, readError: true}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), nativeSnapshot(), p)
	defer rig.close()
	run := startSummary(t, rig.core, "unflushed-history")
	run = awaitRun(t, rig.core, run.ID, func(r runner.Run) bool {
		return len(r.Attempts) > 0 && r.Attempts[0].NativeHandle == "native-thread/native-turn"
	})
	id := run.Attempts[0].ID
	// Exercise observation's reconciliation synchronously: an accepted turn on
	// the subscribed connection must not depend on its rollout being flushed.
	rig.service.Codex.wg.Add(1)
	rig.service.Codex.reconcile(id)
	o, err := rig.service.Observe(context.Background(), id)
	if err != nil || o.State != runner.Running {
		t.Fatalf("live native turn lost: %+v, %v", o, err)
	}
	p.mu.Lock()
	reads := p.reads
	p.mu.Unlock()
	if reads != 0 {
		t.Fatalf("queried unflushed history %d times despite live subscription", reads)
	}
	p.emit("turn/completed", map[string]any{"threadId": "native-thread", "turn": goodTurn()})
	done := awaitRun(t, rig.core, run.ID, allReleased)
	p.mu.Lock()
	starts := p.starts
	p.mu.Unlock()
	if done.Status != "Completed" || starts != 1 {
		t.Fatalf("completion lost or native work repeated: %s, starts=%d", done.Status, starts)
	}
}

func TestCancellationDuringNativeSetupDoesNotStartTurn(t *testing.T) {
	p := &scriptedPeer{gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), nativeSnapshot(), p)
	defer rig.close()
	run := startSummary(t, rig.core, "cancel-setup")
	select {
	case <-p.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native setup did not start")
	}
	if _, err := rig.core.Cancel(run.ID); err != nil {
		t.Fatal(err)
	}
	// Cancellation is a core fact before the native gate is released. Wait until
	// the adapter has accepted that stop intent, using its notification channel.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		rig.service.Codex.mu.Lock()
		ready := false
		for _, j := range rig.service.Codex.jobs {
			ready = ready || j.StopRequested
		}
		changed := rig.service.Codex.changed
		rig.service.Codex.mu.Unlock()
		if ready {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-changed:
		}
	}
	close(p.gate)
	done := awaitRun(t, rig.core, run.ID, allReleased)
	p.mu.Lock()
	starts := p.starts
	p.mu.Unlock()
	if done.Status != "Cancelled" || starts != 0 {
		t.Fatalf("cancelled setup started model work: %s, %d", done.Status, starts)
	}
}
func TestNativeResultBoundary(t *testing.T) {
	a := runner.Assignment{
		Setup:       runner.ExecutionSetup{Profile: runner.Profile{MaxOutput: 256}},
		OutputTypes: map[string]string{"summary": "text"},
	}
	for _, tc := range []struct{ name, text string }{
		{"invalid JSON", "{broken"},
		{"wrong output", `{"outcome":"ready","outputs":{"other":"text"},"reason":""}`},
		{"trailing data", `{"outcome":"ready","outputs":{"summary":"text"},"reason":""} {}`},
		{"oversized output", strings.Repeat("x", 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := goodTurn()
			turn.Items[0].Text = tc.text
			if end := parseNativeResult(a, turn); end.Kind != "failed" {
				t.Fatal("unusable result accepted")
			}
		})
	}
}
func TestNativeJobCanBeCollectedAfterAdapterRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	cfg := nativeSnapshot()
	p := &scriptedPeer{lostReply: true}
	rig := openNativeRig(t, dir, cfg, p)
	// Save the exact uncertain dispatch position, as if Runner disappeared after
	// the native turn started but before its response/result was committed.
	a := runner.Assignment{
		AttemptID:   "attempt_restart",
		Setup:       runner.ExecutionSetup{Profile: *cfg.Projects["demo"].Roles["summary"].Execution},
		OutputTypes: map[string]string{"summary": "text"},
	}
	j := nativeJob{
		Assignment: a,
		Stage:      "starting_turn",
		ThreadID:   "native-thread",
		Generation: "scripted-generation",
	}
	p.thread = "native-thread"
	p.turn = goodTurn()
	rig.service.Codex.mu.Lock()
	err := rig.service.Codex.saveLocked(j)
	rig.service.Codex.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rig.core.Close()
	rig.service.Codex.cancel()
	rig.service.Codex.wg.Wait()
	_ = rig.store.Close()
	secondPeer := &scriptedPeer{thread: "native-thread", turn: goodTurn()}
	r2 := openNativeRig(t, dir, cfg, secondPeer)
	defer r2.close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		o, err := r2.service.Observe(ctx, a.AttemptID)
		if err != nil {
			t.Fatal(err)
		}
		if o.State == runner.Finished {
			if o.End.Kind != "returned" || o.NativeHandle != "native-thread/native-turn" {
				t.Fatal("restart changed the execution")
			}
			break
		}
		r2.service.Codex.mu.Lock()
		changed := r2.service.Codex.changed
		r2.service.Codex.mu.Unlock()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-changed:
		}
	}
	secondPeer.mu.Lock()
	starts := secondPeer.starts
	reads := secondPeer.reads
	secondPeer.mu.Unlock()
	if starts != 0 || reads == 0 {
		t.Fatalf("recovery must read the existing turn without replacement: starts=%d, reads=%d", starts, reads)
	}
	if err = r2.service.Release(ctx, a.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, nativeName(a.AttemptID))); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("released native correlation retained", err)
	}
}

func TestNativeResourcesKeepLinksOutOfArtifactsAndPreserveAccount(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	cfg := nativeSnapshot()
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	d, err := runner.OpenDisk(state, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(base, "auth.json")
	original := []byte(`{"fixture":"not-a-credential"}`)
	if err = os.WriteFile(auth, original, 0600); err != nil {
		t.Fatal(err)
	}
	artifactLink := filepath.Join(state, "artifacts", "bad")
	if err = os.Symlink(auth, artifactLink); err != nil {
		t.Fatal(err)
	}
	if err = d.Refresh(); err == nil {
		t.Fatal("artifact symlink was accepted")
	}
	if err = os.Remove(artifactLink); err != nil {
		t.Fatal(err)
	}
	cfg.Instance.Codex.AuthFile = auth
	c := NewCodex(d, cfg.Instance.Codex)
	if err = c.prepareHome(); err != nil {
		t.Fatal(err)
	}
	// Native auth refresh may atomically replace its link with a private file.
	cache := filepath.Join(state, "native", "auth.json")
	if err = os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cache, []byte(`{"refreshed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.prepareHome(); err != nil {
		t.Fatal("private native refresh cache rejected", err)
	}
	if err = os.Symlink(auth, filepath.Join(state, "jobs", "codex.sock")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(auth, filepath.Join(state, "native", "runtime-alias")); err != nil {
		t.Fatal(err)
	}
	if err = d.Refresh(); err != nil {
		t.Fatal("native owned aliases rejected", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(auth)
	if err != nil || string(b) != string(original) {
		t.Fatal("cleanup changed the source account file", err)
	}
	if d.NativeUsed() != 0 {
		t.Fatal("eligible private native resources were retained")
	}
}

func TestReleaseReconcilesAnAlreadyDeletedNativeThread(t *testing.T) {
	p := &scriptedPeer{deleteErrors: []error{io.ErrUnexpectedEOF, &rpcError{-32600, "no rollout found for thread id native-thread"}}}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), nativeSnapshot(), p)
	defer rig.close()
	id := "attempt_release"
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: id},
		ThreadID:   "native-thread",
		Stage:      "active",
		End:        &runner.ExecutionEnd{Kind: "stopped"},
	}
	rig.service.Codex.mu.Lock()
	err := rig.service.Codex.saveLocked(j)
	rig.service.Codex.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = rig.service.Release(context.Background(), id); err == nil {
		t.Fatal("lost cleanup response was hidden")
	}
	if err = rig.service.Release(context.Background(), id); err != nil {
		t.Fatal("known native deletion was not reconciled", err)
	}
	if err = rig.service.Release(context.Background(), id); err != nil {
		t.Fatal("repeated release failed", err)
	}
	p.mu.Lock()
	starts, deletes := p.starts, p.deleted
	p.mu.Unlock()
	if starts != 0 || deletes != 2 {
		t.Fatal("cleanup repeated model work or an already confirmed deletion")
	}
}
