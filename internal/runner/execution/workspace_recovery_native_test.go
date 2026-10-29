//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type recoveryReady struct {
	RunID      string `json:"run_id"`
	AttemptID  string `json:"attempt_id"`
	ThreadID   string `json:"thread_id"`
	TurnID     string `json:"turn_id"`
	Generation string `json:"generation"`
	WorkDir    string `json:"work_dir"`
	Base       string `json:"base"`
}

type recoveryPeer struct {
	codexPeer
	resumed chan struct{}
	once    *sync.Once
}

func (p *recoveryPeer) Call(ctx context.Context, method string, args, out any) error {
	err := p.codexPeer.Call(ctx, method, args, out)
	if method == "thread/resume" && err == nil {
		p.once.Do(func() { close(p.resumed) })
	}
	return err
}

func recoverySnapshot(t *testing.T, root string) runner.Snapshot {
	t.Helper()
	cfg := workspaceSnapshot(t, filepath.Join(root, "repository"))
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Instance.Codex.AuthFile = filepath.Join(home, ".codex", "auth.json")
	cfg.Instance.Storage.MinFree = 1 << 30
	project := cfg.Projects["demo"]
	role := project.Roles["implement"]
	role.Purpose += ` After creating seats.go, execute this shell command exactly once to signal the recovery exercise and wait for operator cancellation:
mkfifo "$TMPDIR/recovery.fifo" && exec 3<> "$TMPDIR/recovery.fifo" && printf '%s\n' "$$" > "$TMPDIR/recovery.pid" && printf 'RUNNER_RECOVERY_READY\n' && IFS= read -r -t 120 gate <&3
Wait for this command to end before returning your summary. Do not execute any further commands.`
	project.Roles["implement"] = role
	cfg.Projects["demo"] = project
	return cfg
}

// This child owns the real Runner core/adapter/store. SIGKILL must bypass their
// graceful Close methods, while the native service and its blocked command survive.
func writableRecoveryChild(t *testing.T, root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var calls atomic.Int32
	var commandsMu sync.Mutex
	var commands []string
	rig := openNativeRig(t, filepath.Join(root, "state"), recoverySnapshot(t, root), nil, func(s *Service) {
		s.Codex.connect = func(ctx context.Context, socket string, notify func(rpcMessage)) (codexPeer, error) {
			peer, err := dialCodex(ctx, socket, notify)
			if err != nil {
				return nil, err
			}
			return &writableQualificationPeer{peer, root, &calls, &commandsMu, &commands}, nil
		}
	})
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{RequestID: "recovery", Project: "demo", Inputs: map[string]runner.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	// Code-mode tool output need not produce commandExecution notifications.
	// Poll the explicit readiness file, not a presumed tool event or elapsed delay.
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		r, err = rig.core.Get(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Terminal() {
			t.Fatalf("worker ended before crash injection: %s: %s", r.Status, r.Reason)
		}
		if a := r.Current(); a != nil && a.Source != nil && strings.Contains(a.NativeHandle, "/") {
			pidFile := filepath.Join(a.Source.WorkDir, ".runner-tmp", a.ID, "recovery.pid")
			pidText, e := os.ReadFile(pidFile)
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidText)))
			if e == nil && parseErr == nil && processAlive(pid) {
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("worker did not reach recovery barrier: %s: %s; %s", r.Status, r.Reason, rig.service.Codex.tail.bytes())
		case <-poll.C:
		}
	}
	rig.service.Codex.mu.Lock()
	j, err := rig.service.Codex.loadLocked(r.Attempts[0].ID)
	rig.service.Codex.mu.Unlock()
	if err != nil || j.Workspace == nil || j.TurnID == "" || j.End != nil {
		t.Fatal("active writable identity absent at barrier", err)
	}
	pipe := os.NewFile(3, "recovery-ready")
	if err = json.NewEncoder(pipe).Encode(recoveryReady{
		RunID:      r.ID,
		AttemptID:  j.Assignment.AttemptID,
		ThreadID:   j.ThreadID,
		TurnID:     j.TurnID,
		Generation: j.Generation,
		WorkDir:    j.Workspace.WorkDir,
		Base:       j.Workspace.Base,
	}); err != nil {
		t.Fatal(err)
	}
	_ = pipe.Close()
	<-ctx.Done()
	t.Fatal("parent did not inject the process exit")
}

// One live scenario proves abrupt owner-process recovery, rather than repeating
// the scripted failure matrix or the successful coding/check qualification.
func TestNativeWritableRecovery(t *testing.T) {
	if os.Getenv("RUNNER_NATIVE_RECOVERY") != "1" {
		t.Skip("explicit opt-in required; one Sol/xhigh coding turn and one abrupt Runner exit")
	}
	if os.Getenv("RUNNER_RECOVERY_CHILD") == "1" {
		writableRecoveryChild(t, os.Getenv("RUNNER_WORKSPACE_QUAL_DIR"))
		return
	}
	root, started, ctx := writableQualificationRoot(t)
	repo := sourceRepositoryAt(t, filepath.Join(root, "repository"))
	readReady, writeReady, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readReady.Close()
	defer writeReady.Close()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestNativeWritableRecovery$", "-test.timeout=6m")
	command.Env = append(os.Environ(), "RUNNER_RECOVERY_CHILD=1")
	command.ExtraFiles = []*os.File{writeReady}
	diagnostic := &diagnosticTail{limit: 4096}
	command.Stdout, command.Stderr = diagnostic, diagnostic
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
	_ = writeReady.Close()
	var ready recoveryReady
	decoded := make(chan error, 1)
	go func() { decoded <- json.NewDecoder(io.LimitReader(readReady, 4096)).Decode(&ready) }()
	select {
	case err = <-decoded:
		if err != nil {
			t.Fatalf("Runner child: %v; %s; retained at %s", err, diagnostic.bytes(), root)
		}
	case <-ctx.Done():
		t.Fatal("qualification deadline before fault injection; retained at", root)
	}
	source, err := os.ReadFile(filepath.Join(ready.WorkDir, "seats.go"))
	if err != nil || len(source) == 0 {
		t.Fatal("worker edits absent before crash", err)
	}
	pidFile := filepath.Join(ready.WorkDir, ".runner-tmp", ready.AttemptID, "recovery.pid")
	pidText, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil || !processAlive(pid) {
		t.Fatal("native command was not live before crash", err)
	}
	if err = command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = command.Wait(); err == nil {
		t.Fatal("Runner exited gracefully instead of being killed")
	}
	crashed := time.Now()
	var calls atomic.Int32
	var commandsMu sync.Mutex
	var commands []string
	resumed := make(chan struct{})
	var resumedOnce sync.Once
	rig := openNativeRig(t, filepath.Join(root, "state"), recoverySnapshot(t, root), nil, func(s *Service) {
		s.Codex.connect = func(ctx context.Context, socket string, notify func(rpcMessage)) (codexPeer, error) {
			peer, err := dialCodex(ctx, socket, notify)
			if err != nil {
				return nil, err
			}
			limited := &writableQualificationPeer{peer, root, &calls, &commandsMu, &commands}
			return &recoveryPeer{limited, resumed, &resumedOnce}, nil
		}
	})
	closed := false
	defer func() {
		if !closed {
			rig.close()
		}
	}()
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 30*time.Second)
	defer recoveryCancel()
	select {
	case <-resumed:
	case <-recoveryCtx.Done():
		last, _ := rig.core.Get(ready.RunID)
		t.Fatalf("reattachment: %v; %s: %s; retained at %s", recoveryCtx.Err(), last.Status, last.Reason, root)
	}
	recovered := waitSourceRun(t, rig.core, ready.RunID, func(r runner.Run) bool {
		return r.Status == "Running" && r.Current() != nil && r.Current().State == runner.Running
	})
	a := recovered.Current()
	if len(recovered.Attempts) != 1 || a.ID != ready.AttemptID || a.NativeHandle != ready.ThreadID+"/"+ready.TurnID || a.Source == nil || a.Source.Base != ready.Base || a.Source.WorkDir != ready.WorkDir || calls.Load() != 0 {
		t.Fatal("restart replaced the original attempt, native identity or source")
	}
	rig.service.Codex.connMu.Lock()
	generation := rig.service.Codex.generation
	rig.service.Codex.connMu.Unlock()
	if generation != ready.Generation || !processAlive(pid) {
		t.Fatal("the original native service/command did not survive Runner's exit")
	}
	if _, err = rig.core.Retry(runner.RetryRequest{RequestID: "blocked-retry", RunID: ready.RunID, ExtraAttempts: 1}); err == nil {
		t.Fatal("active execution accepted a replacement")
	}
	reattachedMS := time.Since(crashed).Milliseconds()
	if _, err = rig.core.Cancel(ready.RunID); err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, ready.RunID, func(r runner.Run) bool {
		return r.Terminal() && (r.CleanupError != "" || (allReleased(r) && r.ResourcesReleased))
	})
	if done.Status != "Cancelled" || done.CleanupError != "" || len(done.Attempts) != 1 || calls.Load() != 0 || processAlive(pid) {
		t.Fatalf("cancellation/settlement: %s: %s; cleanup=%s; native command alive=%t", done.Status, done.Reason, done.CleanupError, processAlive(pid))
	}
	saved := done.Attempts[0].Source
	if saved == nil || saved.PatchID == "" {
		t.Fatal("partial source was not retained")
	}
	patch, err := rig.disk.Read("artifacts/"+saved.PatchID, rig.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	patchFile := filepath.Join(root, "retained.patch")
	if err = os.WriteFile(patchFile, patch, 0600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	fixtureGit(t, repo, "worktree", "add", "--detach", restored, saved.Base)
	fixtureGit(t, restored, "apply", "--index", patchFile)
	restoredSource, err := os.ReadFile(filepath.Join(restored, "seats.go"))
	if err != nil || !bytes.Equal(restoredSource, source) {
		t.Fatal("retained patch lost the edits present before the crash", err)
	}
	fixtureGit(t, repo, "worktree", "remove", "--force", restored)
	report := map[string]any{
		"status":              done.Status,
		"elapsed_ms":          time.Since(started).Milliseconds(),
		"reattachment_ms":     reattachedMS,
		"submitted_turns":     1,
		"replacement_turns":   calls.Load(),
		"attempts":            len(done.Attempts),
		"identity_preserved":  true,
		"source_preserved":    true,
		"child_settled":       true,
		"patch_bytes":         len(patch),
		"usage":               done.Attempts[0].Usage,
		"coordination_tokens": nil,
	}
	rig.core.Close()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer closeCancel()
	if err = rig.service.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err = rig.service.ForgetRun(ctx, done.ID); err != nil {
		t.Fatal(err)
	}
	if err = rig.store.Close(); err != nil {
		t.Fatal(err)
	}
	closed = true
	for _, name := range []string{"state", "repository", "retained.patch"} {
		if err = os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	report["cleanup"] = "completed"
	report["elapsed_ms"] = time.Since(started).Milliseconds()
	b, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "result.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
	t.Log("one-turn receipt and recovery result:", root)
}
