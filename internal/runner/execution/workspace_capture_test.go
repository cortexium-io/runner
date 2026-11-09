//go:build darwin || linux

package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/control"
)

// The fixture advances the observation deadline only after the selected Git
// stage is blocked. No elapsed-time sleep decides which checkpoint was reached.
type captureCutoffContext struct{ context.Context }

func (c captureCutoffContext) Err() error {
	if errors.Is(context.Cause(c.Context), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return c.Context.Err()
}

type captureGitFixture struct {
	dir   string
	ready chan string
	fifo  *os.File
	hold  *os.File
}

func captureGitGate(t *testing.T, stages string, failure bool) *captureGitFixture {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pipe := filepath.Join(dir, "ready")
	if out, err := exec.Command("mkfifo", pipe).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	f, err := os.OpenFile(pipe, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gate := &captureGitFixture{dir: dir, ready: make(chan string, 16), fifo: f}
	holdPath := filepath.Join(dir, "hold")
	if out, err := exec.Command("mkfifo", holdPath).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	hold, err := os.OpenFile(holdPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	gate.hold = hold
	t.Cleanup(func() { _ = f.Close() })
	t.Cleanup(func() { _ = hold.Close() })
	go func() {
		r := bufio.NewReader(f)
		for {
			line, e := r.ReadString('\n')
			if e != nil {
				return
			}
			gate.ready <- strings.TrimSpace(line)
		}
	}()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
stage=''
case "$GIT_OBJECT_DIRECTORY" in */capture-v1/objects)
 for arg in "$@"; do
  case "$arg" in hash-object) stage=hash;; update-index) stage=index;; write-tree) stage=tree;; --cached) stage=patch;; esac
 done;;
esac
if [ -n "$stage" ]; then
 printf '%%s\n' "$stage" >> %s/calls
 case ' %s ' in *" $stage "*)
  if [ %s = true ]; then exit 7; fi
  if [ ! -e %s/blocked-"$stage" ]; then
   : > %s/blocked-"$stage"
   if [ "$stage" = index ]; then printf owned > "$GIT_INDEX_FILE.lock"; fi
   printf '%%s\n' "$$" > %s/pid
   printf '%%s\n' "$stage" > %s
   IFS= read -r released < %s
  fi;;
 esac
fi
exec %s "$@"
`, quote(dir), stages, fmt.Sprint(failure), quote(dir), quote(dir), quote(dir), quote(pipe), quote(holdPath), quote(real))
	if err = os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return gate
}
func captureEdits(dir string) error {
	for i := 0; i < 27; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("capture-%02d.txt", i)), []byte(fmt.Sprintf("literal source %d\n", i)), 0600); err != nil {
			return err
		}
	}
	return nil
}
func pauseCapture(t *testing.T, w *Workspaces, a runner.Assignment, gate *captureGitFixture) string {
	t.Helper()
	base, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	done := make(chan error, 1)
	go func() { _, err := w.Capture(captureCutoffContext{base}, a); done <- err }()
	var stage string
	select {
	case stage = <-gate.ready:
	case err := <-done:
		t.Fatal("capture did not reach checkpoint", err)
	case <-time.After(5 * time.Second):
		t.Fatal("capture checkpoint did not start")
	}
	cancel(context.DeadlineExceeded)
	select {
	case err := <-done:
		var pending *capturePending
		if !errors.As(err, &pending) {
			t.Fatal("own interrupted capture was not reconciling", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("capture command did not settle")
	}
	return stage
}

func TestCaptureCheckpointsResumeEachGitStage(t *testing.T) {
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_resume")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	if err = captureEdits(r.WorkDir); err != nil {
		t.Fatal(err)
	}
	gate := captureGitGate(t, "hash tree patch", false)
	for _, stage := range []string{"hash", "tree", "patch"} {
		w = &Workspaces{disk: d}
		if got := pauseCapture(t, w, a, gate); got != stage {
			t.Fatal("capture did not advance", got, stage)
		}
		r, err = w.load(a.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Candidate != nil || r.Capture == nil || stage != "hash" && r.Capture.IndexSHA == "" || stage == "patch" && r.Capture.Tree == "" {
			t.Fatal("checkpoint or prior Candidate commit boundary lost", r)
		}
	}
	got, err := (&Workspaces{disk: d}).Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(filepath.Join(gate.dir, "calls"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(log), "hash\n") != 2 || strings.Count(string(log), "index\n") != 1 || strings.Count(string(log), "tree\n") != 2 || strings.Count(string(log), "patch\n") != 2 {
		t.Fatal("completed Git stages were repeated", string(log))
	}
	patch, err := d.Read("artifacts/"+got.PatchID, a.Limits.MaxArtifact)
	if err != nil || !bytes.Contains(patch, []byte("capture-26.txt")) {
		t.Fatal("literal snapshot was not retained", err)
	}
}

func TestCaptureLeaseProtectsLiveWriterAndRebuildsOnlyUncommittedIndex(t *testing.T) {
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_lock")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	if err = captureEdits(r.WorkDir); err != nil {
		t.Fatal(err)
	}
	gate := captureGitGate(t, "index", false)
	pauseCapture(t, w, a, gate)
	root := filepath.Join(d.Root, "work", a.AttemptID, "capture-v1")
	lease, err := os.OpenFile(filepath.Join(root, "lease"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := tryCaptureLease(lease); err != nil || !ok {
		t.Fatal("settled writer retained lease", err)
	}
	_, err = (&Workspaces{disk: d}).Capture(t.Context(), a)
	var pending *capturePending
	if !errors.As(err, &pending) {
		t.Fatal("live writer was not rejoined", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "index.lock")); err != nil || string(data) != "owned" {
		t.Fatal("live writer lock was removed", err)
	}
	lease.Close()
	if _, err = (&Workspaces{disk: d}).Capture(t.Context(), a); err != nil {
		t.Fatal("settled private index could not be rebuilt", err)
	}
}

func TestCaptureDriftRemainsActionableAndKeepsPriorCandidate(t *testing.T) {
	for _, mode := range []string{"source bytes", "source link", "manifest", "literal link", "object", "index", "index lock"} {
		t.Run(mode, func(t *testing.T) {
			d, repo := workspaceDisk(t), sourceRepository(t)
			w := &Workspaces{disk: d}
			a := sourceAssignment(d, repo, "capture_prior")
			r, err := w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			defer w.ForgetRun(context.Background(), a.RunID)
			old, err := w.Capture(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			a.AttemptID = "capture_drift"
			if err = captureEdits(r.WorkDir); err != nil {
				t.Fatal(err)
			}
			gate := captureGitGate(t, "tree", false)
			pauseCapture(t, w, a, gate)
			root := filepath.Join(d.Root, "work", a.AttemptID, "capture-v1")
			r, err = w.load(a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			m, err := w.readCaptureManifest(r, a, "work/"+a.AttemptID+"/capture-v1")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "source bytes":
				err = os.WriteFile(filepath.Join(r.WorkDir, m.Files[0].Path), []byte("changed"), 0600)
			case "source link":
				name := filepath.Join(r.WorkDir, m.Files[0].Path)
				err = os.Remove(name)
				if err == nil {
					err = os.Symlink(filepath.Join(root, "files", "000000"), name)
				}
			case "manifest":
				err = os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"changed":true}`), 0600)
			case "literal link":
				name := filepath.Join(root, "files", "000000")
				err = os.Remove(name)
				if err == nil {
					err = os.Symlink(filepath.Join(r.WorkDir, m.Files[0].Path), name)
				}
			case "object":
				blob := m.Files[0].Blob
				file := filepath.Join(root, "objects", blob[:2], blob[2:])
				err = os.Chmod(file, 0600)
				if err == nil {
					err = os.WriteFile(file, []byte("corrupted"), 0600)
				}
			case "index":
				err = os.WriteFile(filepath.Join(root, "index"), []byte("unverified index"), 0600)
			case "index lock":
				err = os.WriteFile(filepath.Join(root, "index.lock"), []byte("unverified lock"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&Workspaces{disk: d}).Capture(t.Context(), a)
			var pending *capturePending
			if err == nil || errors.As(err, &pending) {
				t.Fatal("real source/journal/link drift was hidden", mode, err)
			}
			r, err = w.load(a.RunID)
			if err != nil || r.Candidate == nil || r.Candidate.PatchID != old.PatchID {
				t.Fatal("incomplete capture replaced prior Candidate", err)
			}
		})
	}
}

func TestCaptureSHA256RepositoryAndDirectoryDeletion(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", "--object-format=sha256", repo).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "unknown option") || strings.Contains(string(out), "unknown hash") {
			t.Skip("Git lacks SHA-256 repositories")
		}
		t.Fatal(err, string(out))
	}
	sourceRepositoryAt(t, repo)
	if err := os.Mkdir(filepath.Join(repo, "gone"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "gone", "file"), []byte("remove directory"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "gone/file")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "directory")
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_sha256")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	if err = os.RemoveAll(filepath.Join(r.WorkDir, "gone")); err != nil {
		t.Fatal(err)
	}
	if err = captureEdits(r.WorkDir); err != nil {
		t.Fatal(err)
	}
	got, err := w.Capture(t.Context(), a)
	if err != nil || len(got.Tree) != 64 {
		t.Fatal("SHA-256 capture failed", got, err)
	}
	if err = w.ReleaseRun(t.Context(), a.RunID); err != nil {
		t.Fatal(err)
	}
	restored, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(restored.WorkDir, "gone")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("deleted directory returned", err)
	}
	if data, err := os.ReadFile(filepath.Join(restored.WorkDir, "capture-26.txt")); err != nil || string(data) != "literal source 26\n" {
		t.Fatal("SHA-256 patch did not restore exact source", err)
	}
}

type shortCaptureExecutor struct {
	*Service
	gate    *captureGitFixture
	mu      sync.Mutex
	pending int
}

func (s *shortCaptureExecutor) Observe(ctx context.Context, id string) (runner.Observation, error) {
	base, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	type result struct {
		o   runner.Observation
		err error
	}
	done := make(chan result, 1)
	go func() { o, err := s.Service.Observe(captureCutoffContext{base}, id); done <- result{o, err} }()
	select {
	case <-s.gate.ready:
		cancel(context.DeadlineExceeded)
	case got := <-done:
		return got.o, got.err
	case <-ctx.Done():
		cancel(ctx.Err())
	}
	got := <-done
	if got.o.Reconciling {
		_, _ = s.gate.hold.Write([]byte("continue\n"))
		s.mu.Lock()
		s.pending++
		s.mu.Unlock()
	}
	return got.o, got.err
}

func TestNativeReturnedResultWaitsForCheckpointedSourceHandoff(t *testing.T) {
	repo := sourceRepository(t)
	cfg := workspaceSnapshot(t, repo)
	project := cfg.Projects["demo"]
	p := project.Processes["seats"]
	p.Steps = p.Steps[:1]
	p.Steps[0].Routes = map[string]runner.Route{"blocked": {Next: "$fail"}}
	p.Outputs = map[string]runner.Binding{"patch": {FromStep: "implement", Output: "patch"}}
	project.Processes["seats"] = p
	cfg.Projects["demo"] = project
	refDisk := workspaceDisk(t)
	refW := &Workspaces{disk: refDisk}
	refA := sourceAssignment(refDisk, repo, "capture_reference")
	ref, err := refW.Prepare(t.Context(), refA)
	if err != nil {
		t.Fatal(err)
	}
	defer refW.ForgetRun(context.Background(), refA.RunID)
	if err = captureEdits(ref.WorkDir); err != nil {
		t.Fatal(err)
	}
	reference, err := refW.Capture(t.Context(), refA)
	if err != nil {
		t.Fatal(err)
	}
	want, err := refDisk.Read("artifacts/"+reference.PatchID, refA.Limits.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var turn nativeTurn
	_ = json.Unmarshal([]byte(`{"id":"native-turn","status":"completed","items":[{"type":"agentMessage","phase":"final_answer","text":"{\"outcome\":\"blocked\",\"outputs\":{\"summary\":\"retained incomplete fixture\"},\"reason\":\"cause unproven\"}"}]}`), &turn)
	script := &scriptedPeer{results: []nativeTurn{turn}, onTurn: func(dir string) {
		if err := captureEdits(dir); err != nil {
			t.Error(err)
		}
	}}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
	rig.core.Close()
	gate := captureGitGate(t, "hash tree patch", false)
	executor := &shortCaptureExecutor{Service: rig.service, gate: gate}
	rig.core, err = runner.New(t.Context(), rig.store, executor, rig.disk, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rig.service.SetWake(rig.core.ExecutionChanged)
	defer rig.close()
	run, err := rig.core.Start(runner.StartRequest{RequestID: "capture-handoff", Project: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	waitSourceRun(t, rig.core, run.ID, func(r runner.Run) bool { return r.Status == "Waiting" && r.Current() != nil && r.Current().Reconciling }, 15*time.Second)
	server := httptest.NewServer(control.Handler(rig.core, strings.Repeat("x", 52), nil))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	session, err := control.Connect(ctx, server.URL+"/mcp", strings.Repeat("x", 52))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	data, err := control.Call(ctx, session, "runner_result", control.ResultArgs{RunID: run.ID, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	var out control.Result
	if json.Unmarshal(data, &out) != nil || out.Status != "Failed" || out.Reason != "cause unproven" || !out.ExecutionSettled || out.CleanupPending {
		t.Fatal("wait escaped capture progress or changed blocked result", string(data))
	}
	final, err := rig.core.Get(run.ID)
	if err != nil || len(final.Attempts) != 1 || final.Reason != "cause unproven" || final.Attempts[0].TimedOut || final.Attempts[0].End.Result.Outcome != "blocked" || final.Attempts[0].Source.Tree != reference.Tree {
		t.Fatal("native outcome/attempt/source identity changed", final, err)
	}
	got, err := rig.disk.Read("artifacts/"+final.Attempts[0].Source.PatchID, cfg.Instance.Storage.MaxArtifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("short observations changed literal patch", err)
	}
	script.mu.Lock()
	starts := script.starts
	script.mu.Unlock()
	executor.mu.Lock()
	pending := executor.pending
	executor.mu.Unlock()
	if starts != 1 || pending != 3 {
		t.Fatal("model repeated or source stages failed to advance", starts, pending)
	}
}

func TestCaptureInterruptedOwnerHelper(t *testing.T) {
	file := os.Getenv("RUNNER_CAPTURE_OWNER_SPEC")
	if file == "" {
		t.Skip("internal interruption helper")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Root       string
		Policy     runner.StoragePolicy
		Assignment runner.Assignment
	}
	if err = json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	d, err := runner.OpenDisk(spec.Root, spec.Policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&Workspaces{disk: d}).Capture(t.Context(), spec.Assignment); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureOwnerCrashRetainsChildLeaseUntilWriterSettles(t *testing.T) {
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_owner_crash")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	if err = captureEdits(r.WorkDir); err != nil {
		t.Fatal(err)
	}
	gate := captureGitGate(t, "index", false)
	data, err := json.Marshal(struct {
		Root       string
		Policy     runner.StoragePolicy
		Assignment runner.Assignment
	}{d.Root, d.Policy, a})
	if err != nil {
		t.Fatal(err)
	}
	spec := filepath.Join(gate.dir, "owner.json")
	if err = os.WriteFile(spec, data, 0600); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command(os.Args[0], "-test.run=^TestCaptureInterruptedOwnerHelper$", "-test.timeout=20s")
	owner.Env = append(os.Environ(), "RUNNER_CAPTURE_OWNER_SPEC="+spec)
	if err = owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Process.Kill() }()
	select {
	case <-gate.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach private index")
	}
	pidData, err := os.ReadFile(filepath.Join(gate.dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatal(err)
	}
	writer, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Kill()
	if err = owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = owner.Wait(); err == nil {
		t.Fatal("owner did not exit abruptly")
	}
	_, err = (&Workspaces{disk: d}).Capture(t.Context(), a)
	var pending *capturePending
	if !errors.As(err, &pending) {
		t.Fatal("orphaned live writer lost its inherited lease", err)
	}
	lock := filepath.Join(d.Root, "work", a.AttemptID, "capture-v1", "index.lock")
	if contents, err := os.ReadFile(lock); err != nil || string(contents) != "owned" {
		t.Fatal("restart removed live writer's lock", err)
	}
	if err = writer.Kill(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		_, err = (&Workspaces{disk: d}).Capture(ctx, a)
		if err == nil {
			break
		}
		if !errors.As(err, &pending) {
			t.Fatal("settled restart could not resume snapshot", err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestCaptureServiceKeepsIntegrityAndOrdinaryFailuresActionable(t *testing.T) {
	for _, mode := range []string{"Git exit", "source drift", "cancelled", "scratch file", "scratch link"} {
		t.Run(mode, func(t *testing.T) {
			d, repo := workspaceDisk(t), sourceRepository(t)
			w := &Workspaces{disk: d}
			a := sourceAssignment(d, repo, "capture_service")
			r, err := w.Prepare(t.Context(), a)
			if err != nil {
				t.Fatal(err)
			}
			defer w.ForgetRun(context.Background(), a.RunID)
			if err = captureEdits(r.WorkDir); err != nil {
				t.Fatal(err)
			}
			a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
			end := &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "blocked", Outputs: map[string]runner.Value{"summary": runner.Text("incomplete")}}}
			s := &Service{workspaces: w, Codex: &Codex{jobs: map[string]nativeJob{a.AttemptID: {Assignment: a, Workspace: &r, End: end}}}}
			switch mode {
			case "source drift":
				gate := captureGitGate(t, "tree", false)
				pauseCapture(t, w, a, gate)
				err = os.WriteFile(filepath.Join(r.WorkDir, "capture-00.txt"), []byte("changed"), 0600)
			case "Git exit":
				captureGitGate(t, "index", true)
			case "scratch file":
				err = os.WriteFile(filepath.Join(r.WorkDir, ".runner-tmp"), []byte("preserve"), 0600)
			case "scratch link":
				err = os.Symlink(t.TempDir(), filepath.Join(r.WorkDir, ".runner-tmp"))
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if mode == "cancelled" {
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			o, err := s.collect(ctx, a.AttemptID, runner.Observation{State: runner.Finished, End: end}, nil)
			if err != nil || o.State != runner.Unknown || o.Reconciling || o.End != nil || o.Reason == "" {
				t.Fatal("real capture failure was hidden as progress", mode, o, err)
			}
		})
	}
}

func TestCaptureResumesLargeIncompressibleBlobWithoutAggregateArtifactCap(t *testing.T) {
	repo := sourceRepository(t)
	data := make([]byte, 16<<20)
	_, _ = rand.New(rand.NewSource(1)).Read(data)
	if err := os.WriteFile(filepath.Join(repo, "large.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "large.bin")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "binary base")
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_large")
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	data[len(data)/2] ^= 1
	if err = os.WriteFile(filepath.Join(r.WorkDir, "large.bin"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.WorkDir, "small.txt"), []byte("aggregate literal bytes exceed artifact limit"), 0600); err != nil {
		t.Fatal(err)
	}
	gate := captureGitGate(t, "tree", false)
	pauseCapture(t, w, a, gate)
	r, err = w.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.readCaptureManifest(r, a, "work/"+a.AttemptID+"/capture-v1")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range m.Files {
		if file.Path == "large.bin" {
			info, err := os.Stat(filepath.Join(d.Root, "work", a.AttemptID, "capture-v1", "objects", file.Blob[:2], file.Blob[2:]))
			if err != nil || info.Size() <= int64(a.Limits.MaxArtifact)+4096 {
				t.Fatal("fixture did not exceed former compressed-object read bound", err)
			}
		}
	}
	got, err := (&Workspaces{disk: d}).Capture(t.Context(), a)
	if err != nil {
		t.Fatal("valid sparse binary edit was rejected on resume", err)
	}
	patch, err := d.Read("artifacts/"+got.PatchID, a.Limits.MaxArtifact)
	if err != nil || len(patch) > 4096 {
		t.Fatal("sparse edit did not remain a small patch", len(patch), err)
	}
}

func savedCaptureService(t *testing.T) (*Service, runner.Assignment) {
	t.Helper()
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "capture_saved_native")
	a.Deadline = time.Now().Add(time.Minute)
	a.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.ForgetRun(context.Background(), a.RunID) })
	if err = captureEdits(r.WorkDir); err != nil {
		t.Fatal(err)
	}
	s := NewService(d, nil, nativeSnapshot().Instance.Codex)
	end := &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "blocked", Outputs: map[string]runner.Value{"summary": runner.Text("cause unproven")}}}
	s.Codex.mu.Lock()
	err = s.Codex.saveLocked(nativeJob{Assignment: a, Workspace: &r, End: end})
	s.Codex.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Close(ctx)
	})
	return s, a
}

func captureLeaseIsHeld(t *testing.T, s *Service, a runner.Assignment) bool {
	t.Helper()
	file := filepath.Join(s.disk.Root, "work", a.AttemptID, "capture-v1", "lease")
	lease, err := os.OpenFile(file, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	owned, err := tryCaptureLease(lease)
	if err != nil {
		t.Fatal(err)
	}
	return !owned
}

func TestCaptureServiceJoinsLongPhaseAcrossObservationDeadlines(t *testing.T) {
	s, a := savedCaptureService(t)
	gate := captureGitGate(t, "hash", false)
	woke := make(chan string, 1)
	s.SetWake(func(id string) {
		select {
		case woke <- id:
		default:
		}
	})
	type observed struct {
		o   runner.Observation
		err error
	}
	done := make(chan observed, 1)
	call, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { o, err := s.Observe(call, a.AttemptID); done <- observed{o, err} }()
	select {
	case <-gate.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("long source phase did not begin")
	}
	first := <-done
	if first.err != nil || first.o.State != runner.Unknown || !first.o.Reconciling || !captureLeaseIsHeld(t, s, a) {
		t.Fatal("five-second observation interrupted or escaped owned capture", first)
	}
	s.captureMu.Lock()
	original := s.captures[a.AttemptID]
	s.captureMu.Unlock()
	if err := s.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("active source capture released")
	}
	short, stop := context.WithTimeout(t.Context(), time.Millisecond)
	defer stop()
	second, err := s.Observe(short, a.AttemptID)
	if err != nil || !second.Reconciling || !captureLeaseIsHeld(t, s, a) {
		t.Fatal("second expired observer restarted source collection", second, err)
	}
	s.captureMu.Lock()
	same := original == s.captures[a.AttemptID]
	s.captureMu.Unlock()
	if !same {
		t.Fatal("observers replaced the owned collector")
	}
	if _, err = gate.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	final, err := s.Observe(t.Context(), a.AttemptID)
	if err != nil || final.State != runner.Finished || final.Source == nil || final.End.Result.Outcome != "blocked" {
		t.Fatal("long phase failed to hand off the saved native result", final, err)
	}
	select {
	case id := <-woke:
		if id != a.AttemptID {
			t.Fatal("wrong capture wake", id)
		}
	case <-time.After(time.Second):
		t.Fatal("source completion did not wake Core")
	}
	calls, err := os.ReadFile(filepath.Join(gate.dir, "calls"))
	if err != nil || strings.Count(string(calls), "hash\n") != 1 {
		t.Fatal("expired observations replayed Git capture", string(calls), err)
	}
}

func observeRetentionCutoff(t *testing.T, s *Service, a runner.Assignment, gate *captureGitFixture) *sourceCaptureWork {
	t.Helper()
	base, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	type observed struct {
		o   runner.Observation
		err error
	}
	done := make(chan observed, 1)
	go func() { o, err := s.Observe(captureCutoffContext{base}, a.AttemptID); done <- observed{o, err} }()
	select {
	case <-gate.ready:
	case got := <-done:
		t.Fatal("retention did not reach source checkpoint", got)
	case <-time.After(10 * time.Second):
		t.Fatal("retention source checkpoint missing")
	}
	cancel(context.DeadlineExceeded)
	got := <-done
	if got.err != nil || got.o.State != runner.Unknown || got.o.End != nil || !got.o.Reconciling || !captureLeaseIsHeld(t, s, a) {
		t.Fatal("observation cutoff interrupted owned retention", got)
	}
	s.captureMu.Lock()
	work := s.captures[a.AttemptID]
	s.captureMu.Unlock()
	if work == nil || !work.retention {
		t.Fatal("stopped work did not start an owned retention collector")
	}
	return work
}

func TestStoppedCaptureSurvivesObservationAndRepeatedStop(t *testing.T) {
	for _, mode := range []string{"before first capture", "active capture", "original deadline expired", "durable stopped end"} {
		t.Run(mode, func(t *testing.T) {
			s, a := savedCaptureService(t)
			if mode == "active capture" {
				gate := captureGitGate(t, "hash", false)
				done := make(chan error, 1)
				go func() { _, err := s.Observe(t.Context(), a.AttemptID); done <- err }()
				select {
				case <-gate.ready:
				case <-time.After(3 * time.Second):
					t.Fatal("active capture did not start")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
				defer cancel()
				if err := s.Stop(ctx, a.AttemptID); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if captureLeaseIsHeld(t, s, a) {
					t.Fatal("Stop did not settle source capture")
				}
			} else if mode == "before first capture" {
				if err := s.Stop(t.Context(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.Codex.mutate(a.AttemptID, func(j *nativeJob) {
					if mode == "original deadline expired" {
						a.Deadline = time.Now().Add(-time.Second)
						j.Assignment.Deadline = a.Deadline
					} else {
						j.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "native stop confirmed"}
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := s.Close(ctx); err != nil {
				t.Fatal(err)
			}
			reopened := NewService(s.disk, nil, nativeSnapshot().Instance.Codex)
			defer reopened.Close(ctx)
			reopened.Codex.mu.Lock()
			j, err := reopened.Codex.loadLocked(a.AttemptID)
			reopened.Codex.mu.Unlock()
			stopped := mode == "before first capture" || mode == "active capture"
			if err != nil || j.StopRequested != stopped || j.End == nil {
				t.Fatal("restart lost durable stop or changed native End", j, err)
			}
			// A restarted collector holds its lease after an observer expires.
			gate := captureGitGate(t, "hash", false)
			work := observeRetentionCutoff(t, reopened, a, gate)
			bound, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			for i := 0; i < 2; i++ {
				if err := reopened.Stop(bound, a.AttemptID); err != nil {
					t.Fatal(err)
				}
			}
			if !captureLeaseIsHeld(t, reopened, a) {
				t.Fatal("repeated Stop interrupted post-stop retention")
			}
			if err := reopened.Release(bound, a.AttemptID); err == nil {
				t.Fatal("active retention was released")
			}
			short, detach := context.WithTimeout(t.Context(), time.Millisecond)
			defer detach()
			if o, err := reopened.Observe(short, a.AttemptID); err != nil || !o.Reconciling || o.State != runner.Unknown {
				t.Fatal("second observer did not join retention", o, err)
			}
			reopened.captureMu.Lock()
			same := reopened.captures[a.AttemptID] == work
			reopened.captureMu.Unlock()
			if !same {
				t.Fatal("observer or Stop replaced the retention worker")
			}
			if _, err := gate.hold.Write([]byte("continue\n")); err != nil {
				t.Fatal(err)
			}
			finish, cancelFinish := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancelFinish()
			o, err := reopened.Observe(finish, a.AttemptID)
			patchID := a.AttemptID + "/changes.patch"
			wantEnd := runner.Clone(j.End)
			if wantEnd.Kind == "returned" {
				wantEnd.Result.Outputs["patch"] = runner.Value{ArtifactID: patchID}
			}
			if err != nil || o.State != runner.Finished || o.Source == nil || o.Source.PatchID != patchID || !reflect.DeepEqual(o.End, wantEnd) {
				t.Fatal("retention did not return the same stopped attempt and source", o, err)
			}
			patch, err := s.disk.Read("artifacts/"+o.Source.PatchID, a.Limits.MaxArtifact)
			if err != nil || bytes.Count(patch, []byte("diff --git ")) != 27 || !bytes.Contains(patch, []byte("+literal source 26\n")) {
				t.Fatal("retained partial patch lost exact source", err)
			}
		})
	}
}

func TestStoppedCaptureExpiryPreservesCheckpointWithoutObserverReplay(t *testing.T) {
	s, a := savedCaptureService(t)
	if err := s.Codex.mutate(a.AttemptID, func(j *nativeJob) { j.End = &runner.ExecutionEnd{Kind: "stopped"} }); err != nil {
		t.Fatal(err)
	}
	gate := captureGitGate(t, "hash", false)
	base, expire := context.WithCancelCause(t.Context())
	defer expire(context.Canceled)
	// Drive the worker's own deadline at the checkpoint, without a five-minute
	// sleep or any test-only execution budget in Service.
	work := &sourceCaptureWork{
		assignment: a,
		done:       make(chan struct{}),
		cancel:     func() { expire(context.Canceled) },
		retention:  true,
	}
	s.captureMu.Lock()
	s.captures = map[string]*sourceCaptureWork{a.AttemptID: work}
	s.captureMu.Unlock()
	go s.runCapture(captureCutoffContext{base}, work)
	select {
	case <-gate.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("retention worker did not checkpoint source")
	}
	expire(context.DeadlineExceeded)
	select {
	case <-work.done:
	case <-time.After(3 * time.Second):
		t.Fatal("retention worker did not settle")
	}
	if captureLeaseIsHeld(t, s, a) {
		t.Fatal("expired retention worker left its Git lease active")
	}
	if _, err := s.disk.Read("work/"+a.AttemptID+"/capture-v1/manifest.json", 8<<20); err != nil {
		t.Fatal("expired retention lost its checkpoint", err)
	}
	for i := 0; i < 2; i++ {
		o, err := s.Observe(t.Context(), a.AttemptID)
		if err != nil || o.State != runner.Unknown || o.Reconciling || o.End != nil || !strings.Contains(o.Reason, "source retention deadline reached") {
			t.Fatal("expired retention was hidden as active progress", o, err)
		}
	}
	s.captureMu.Lock()
	same := s.captures[a.AttemptID] == work
	s.captureMu.Unlock()
	if !same {
		t.Fatal("observer reset the expired retention budget")
	}
}

func TestCaptureCloseSettlesActiveCollectorAndPreservesCheckpoint(t *testing.T) {
	t.Run("ordinary capture", func(t *testing.T) { testCaptureCloseAndRestart(t, false) })
	t.Run("post-stop retention", func(t *testing.T) { testCaptureCloseAndRestart(t, true) })
}

func testCaptureCloseAndRestart(t *testing.T, stopped bool) {
	t.Helper()
	s, a := savedCaptureService(t)
	if stopped {
		if err := s.Codex.mutate(a.AttemptID, func(j *nativeJob) { j.End = &runner.ExecutionEnd{Kind: "stopped"} }); err != nil {
			t.Fatal(err)
		}
	}
	gate := captureGitGate(t, "hash", false)
	done := make(chan error, 1)
	go func() { _, err := s.Observe(t.Context(), a.AttemptID); done <- err }()
	select {
	case <-gate.ready:
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if captureLeaseIsHeld(t, s, a) {
		t.Fatal("Close left an active source writer")
	}
	reopened := NewService(s.disk, nil, nativeSnapshot().Instance.Codex)
	finish, cancelFinish := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelFinish()
	defer reopened.Close(finish)
	o, err := reopened.Observe(finish, a.AttemptID)
	if err != nil || o.State != runner.Finished || o.Source == nil || o.Source.PatchID != a.AttemptID+"/changes.patch" || stopped && o.End.Kind != "stopped" {
		t.Fatal("restart could not resume closed collector's checkpoint", o, err)
	}
}
