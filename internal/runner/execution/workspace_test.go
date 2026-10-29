package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

const seatsImplementation = `package seats
import("errors";"strconv")
func ParseSeats(text string)(int,error){n,err:=strconv.Atoi(text);if err!=nil{return 0,err};if n<=0{return 0,errors.New("seat count must be positive")};return n,nil}
`

func sourceRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	return sourceRepositoryAt(t, repo)
}
func sourceRepositoryAt(t *testing.T, repo string) string {
	t.Helper()
	if e := os.MkdirAll(repo, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"go.mod", "doc.go", "seats_test.go"} {
		data, e := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "seats", name))
		if e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(repo, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	fixtureGit(t, repo, "init", "--quiet", "--template=")
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "fixture")
	return repo
}
func fixtureGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	b, e := git(context.Background(), dir, nil, nil, 1<<20, args...)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func sourceAssignment(d *runner.Disk, repo, id string) runner.Assignment {
	return runner.Assignment{
		RunID:      "run_fixture",
		AttemptID:  id,
		Repository: repo,
		Source:     &runner.SourceSettings{Ref: "HEAD"},
		Limits: runner.ExecutionLimits{
			MaxArtifact: d.Policy.MaxArtifact,
			MaxRecord:   d.Policy.Database.MaxRecord,
			MaxLog:      d.Policy.Logs.MaxPerAttempt,
		},
	}
}
func workspaceDisk(t *testing.T) *runner.Disk {
	t.Helper()
	p := runner.DefaultStorage()
	p.MinFree = 0
	d, e := runner.OpenDisk(t.TempDir(), p)
	if e != nil {
		t.Fatal(e)
	}
	return d
}

// The collector suite owns all file-format cases. Protocol tests need only a new Go file.
func TestWorkspaceRetainsChangesAndBaseAcrossCleanup(t *testing.T) {
	repo := sourceRepository(t)
	for name, data := range map[string][]byte{
		"delete.txt": []byte("old"),
		"binary":     {0, 1, 2},
		"executable": []byte("#!/bin/sh\n"),
		".gitignore": []byte("ignored/\n"),
		// Existing assets larger than the patch allowance must remain untouched.
		"large-asset": bytes.Repeat([]byte("unchanged"), 8192),
	} {
		if e := os.WriteFile(filepath.Join(repo, name), data, 0644); e != nil {
			t.Fatal(e)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--quiet", "-m", "format")
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_source")
	a.Limits.MaxArtifact = 32 << 10
	r, e := w.Prepare(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	// Local user edits and branch movement must not enter or reset this run.
	os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user edits"), 0600)
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "--quiet", "-m", "advance")
	againBase, e := w.Prepare(context.Background(), a)
	if e != nil || againBase.Base != r.Base {
		t.Fatal("branch movement changed the selected base", e)
	}
	if e = os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0644); e != nil {
		t.Fatal(e)
	}
	os.Remove(filepath.Join(r.WorkDir, "delete.txt"))
	os.WriteFile(filepath.Join(r.WorkDir, "binary"), []byte{0, 9, 255}, 0644)
	os.Chmod(filepath.Join(r.WorkDir, "executable"), 0755)
	os.MkdirAll(filepath.Join(r.WorkDir, "ignored"), 0700)
	os.WriteFile(filepath.Join(r.WorkDir, "ignored", "cache"), []byte("ignored"), 0600)
	os.MkdirAll(filepath.Join(r.WorkDir, ".runner-tmp", "cache"), 0700)
	os.WriteFile(filepath.Join(r.WorkDir, ".runner-tmp", "cache", "data"), []byte("cache"), 0600)
	got, e := w.Capture(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	patch, e := d.Read("artifacts/"+got.PatchID, d.Policy.MaxArtifact)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(patch, []byte(".runner-tmp")) || bytes.Contains(patch, []byte("ignored/cache")) {
		t.Fatal("scratch retained as source")
	}
	old, e := w.Capture(context.Background(), a)
	if e != nil || old.PatchID != got.PatchID {
		t.Fatal("capture must reuse its artifact", e)
	}
	if e = w.ReleaseRun(context.Background(), a.RunID); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(r.WorkDir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("workspace retained", e)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	fixtureGit(t, repo, "worktree", "add", "--detach", restored, got.Base)
	defer fixtureGit(t, repo, "worktree", "remove", "--force", restored)
	file := filepath.Join(t.TempDir(), "changes.patch")
	os.WriteFile(file, patch, 0600)
	fixtureGit(t, restored, "apply", "--index", file)
	for name, want := range map[string][]byte{"seats.go": []byte(seatsImplementation), "binary": {0, 9, 255}} {
		actual, e := os.ReadFile(filepath.Join(restored, name))
		if e != nil || !bytes.Equal(actual, want) {
			t.Fatalf("restored %s differs: %v", name, e)
		}
	}
	if _, e = os.Stat(filepath.Join(restored, "delete.txt")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("deletion not retained")
	}
	info, e := os.Stat(filepath.Join(restored, "executable"))
	if e != nil || info.Mode()&0111 == 0 {
		t.Fatal("executable bit not retained")
	}
	user, _ := os.ReadFile(filepath.Join(repo, "doc.go"))
	if string(user) != "user edits" {
		t.Fatal("user checkout changed")
	}
	if e = w.ForgetRun(context.Background(), a.RunID); e != nil {
		t.Fatal(e)
	}
	if e = w.ForgetRun(context.Background(), a.RunID); e != nil {
		t.Fatal(e)
	}
}
func TestWorkspaceCaptureFailurePreservesPartialWork(t *testing.T) {
	repo := sourceRepository(t)
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_partial")
	r, e := w.Prepare(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	defer w.ForgetRun(context.Background(), a.RunID)
	os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0600)
	a.Limits.MaxArtifact = 32
	if _, e = w.Capture(context.Background(), a); e == nil {
		t.Fatal("oversized source unexpectedly collected")
	}
	if _, e = os.Stat(filepath.Join(r.WorkDir, "seats.go")); e != nil {
		t.Fatal("partial work lost")
	}
	// A new manager reconciles the same workspace/base after collection interruption.
	recovered := &Workspaces{disk: d}
	a.Limits.MaxArtifact = d.Policy.MaxArtifact
	again, e := recovered.Prepare(context.Background(), a)
	if e != nil || again.Base != r.Base || again.WorkDir != r.WorkDir {
		t.Fatal("workspace replaced", e)
	}
	saved, e := recovered.Capture(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	// Simulate loss of the workspace metadata update after durable artifact write.
	record, e := recovered.load(a.RunID)
	if e != nil {
		t.Fatal(e)
	}
	record.Candidate = nil
	if e = recovered.save(record); e != nil {
		t.Fatal(e)
	}
	againSaved, e := (&Workspaces{disk: d}).Capture(context.Background(), a)
	if e != nil || againSaved.PatchID != saved.PatchID {
		t.Fatal("collection recovery did not reuse its identity", e)
	}
	entries, e := os.ReadDir(filepath.Join(d.Root, "artifacts", a.AttemptID))
	if e != nil || len(entries) != 1 {
		t.Fatal("duplicate retained payload", e)
	}
}
func workspaceSnapshot(t *testing.T, repo string) runner.Snapshot {
	t.Helper()
	s := nativeSnapshot()
	goPath, e := exec.LookPath("go")
	if e != nil {
		t.Fatal(e)
	}
	s.Instance.Codex.GoBinary = goPath
	s.Instance.Codex.ReadOnlyPaths = []string{"go.mod", "doc.go", "seats_test.go"}
	s.Instance.Storage.MaxManaged = 512 << 20
	s.Instance.Storage.MaxArtifact = 32 << 10
	s.Instance.Codex.StorageLimit = 32 << 20
	project := s.Projects["demo"]
	project.Workspace = repo
	project.Source = &runner.SourceSettings{Ref: "HEAD"}
	coding := project.Roles["summary"]
	coding.ID = "implement"
	coding.Purpose = "Add seats.go implementing ParseSeats(text string) (int,error): standard decimal integer conversion, positive results succeed; invalid or non-positive input returns zero and an error. Follow the supplied seats_test.go contract. Add no other files or dependencies. Do not run tests or Git. Return a brief summary."
	coding.Inputs = map[string]string{}
	coding.Outputs = map[string]string{"summary": "text", "patch": "artifact"}
	coding.Execution.Access = "workspace-write"
	coding.Execution.MaxInput = 8192
	check := runner.Role{
		ID:        "check",
		Purpose:   "Run the fixed Go contract suite.",
		Inputs:    map[string]string{"patch": "artifact"},
		Outputs:   map[string]string{"check": "text"},
		Execution: &runner.Profile{Harness: "go-check", Access: "workspace-read"},
	}
	project.Roles = map[string]runner.Role{"implement": coding, "check": check}
	p := runner.Process{
		ID:     "seats",
		Inputs: map[string]string{},
		Steps: []runner.Step{
			{ID: "implement", Role: "implement"},
			{
				ID:     "check",
				Role:   "check",
				Inputs: map[string]runner.Binding{"patch": {FromStep: "implement", Output: "patch"}},
			},
		},
		Outputs:            map[string]runner.Binding{"patch": {FromStep: "implement", Output: "patch"}, "check": {FromStep: "check", Output: "check"}},
		MaxAttempts:        2,
		MaxAttemptsPerStep: 1,
		AttemptTimeout:     "5m",
	}
	project.DefaultProcess = "seats"
	project.Processes = map[string]runner.Process{"seats": p}
	s.Projects["demo"] = project
	return s
}
func waitSourceRun(t *testing.T, c *runner.Core, id string, ready func(runner.Run) bool) runner.Run {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		r, e := c.Get(id)
		if e != nil {
			t.Fatal(e)
		}
		if ready(r) {
			return r
		}
		if _, e = c.Wait(ctx, id, r.Revision); e != nil {
			t.Fatalf("run %s: %s (%s): %v", id, r.Status, r.Reason, e)
		}
	}
}
func TestWritableLostReplyAndOneCheck(t *testing.T) {
	repo := sourceRepository(t)
	cfg := workspaceSnapshot(t, repo)
	script := &scriptedPeer{lostReply: true}
	script.onTurn = func(dir string) {
		if e := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(seatsImplementation), 0600); e != nil {
			t.Error(e)
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
	defer rig.close()
	r, e := rig.core.Start(runner.StartRequest{RequestID: "source", Project: "demo", Inputs: map[string]runner.Value{}})
	if e != nil {
		t.Fatal(e)
	}
	done := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) && r.ResourcesReleased })
	if done.Status != "Completed" || len(done.Attempts) != 2 || done.Attempts[0].Source == nil {
		t.Fatalf("unexpected run: %+v", done)
	}
	script.mu.Lock()
	starts, checks := script.starts, script.checks
	script.mu.Unlock()
	if starts != 1 || checks != 1 {
		t.Fatalf("starts/checks = %d/%d", starts, checks)
	}
	patch, e := rig.disk.Read("artifacts/"+done.Outputs["patch"].ArtifactID, cfg.Instance.Storage.MaxArtifact)
	if e != nil || !strings.Contains(string(patch), "seats.go") {
		t.Fatal("missing saved new source", e)
	}
	if _, e = os.Stat(done.Attempts[0].Source.WorkDir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("completed worktree not cleaned")
	}
}

func TestWritableUnknownCancellationAndRestartPreservePartialSource(t *testing.T) {
	repo := sourceRepository(t)
	cfg := workspaceSnapshot(t, repo)
	script := &scriptedPeer{lostReply: true, stayActive: true, readError: true}
	script.onTurn = func(dir string) {
		if e := os.WriteFile(filepath.Join(dir, "seats.go"), []byte("package seats\n// unfinished\n"), 0600); e != nil {
			t.Error(e)
		}
	}
	state := filepath.Join(t.TempDir(), "state")
	rig := openNativeRig(t, state, cfg, script)
	current := rig
	defer func() { current.close() }()
	r, e := rig.core.Start(runner.StartRequest{RequestID: "partial", Project: "demo", Inputs: map[string]runner.Value{}})
	if e != nil {
		t.Fatal(e)
	}
	unknown := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Waiting" && r.Active() })
	if _, e = rig.core.Retry(runner.RetryRequest{RequestID: "no-duplicate", RunID: r.ID, ExtraAttempts: 1}); e == nil {
		t.Fatal("unknown execution admitted a replacement")
	}
	if len(unknown.Attempts) != 1 {
		t.Fatal("duplicate attempt")
	}
	script.mu.Lock()
	script.readError = false
	script.mu.Unlock()
	if _, e = rig.core.Cancel(r.ID); e != nil {
		t.Fatal(e)
	}
	rig.close()
	current = openNativeRig(t, state, cfg, script)
	done := waitSourceRun(t, current.core, r.ID, func(r runner.Run) bool { return r.Status == "Cancelled" && allReleased(r) && r.ResourcesReleased })
	source := done.Attempts[0].Source
	if source == nil || source.PatchID == "" {
		t.Fatal("partial source was not retained")
	}
	patch, e := current.disk.Read("artifacts/"+source.PatchID, cfg.Instance.Storage.MaxArtifact)
	if e != nil || !bytes.Contains(patch, []byte("unfinished")) {
		t.Fatal("partial edits lost", e)
	}
	script.mu.Lock()
	starts, checks := script.starts, script.checks
	script.mu.Unlock()
	if starts != 1 || checks != 0 {
		t.Fatalf("unexpected starts/checks: %d/%d", starts, checks)
	}
}
