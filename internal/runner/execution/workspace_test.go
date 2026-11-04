package execution

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
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

func TestWorkspaceModuleCacheEnvironmentAndReadiness(t *testing.T) {
	cache := t.TempDir()
	c := &Codex{cfg: &runner.CodexSettings{GoBinary: "go"}, ctx: context.Background()}
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: "permission"},
		Workspace:  &workspaceRecord{WorkDir: t.TempDir()},
	}
	env, err := c.workerEnvironment(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["GOMODCACHE"]; ok {
		t.Fatal("unselected module cache added to worker environment")
	}
	c.cfg.GoModuleCache = cache
	env, err = c.workerEnvironment(j)
	if err != nil || env["GOMODCACHE"] != cache || env["GOPROXY"] != "off" || env["GOSUMDB"] != "off" {
		t.Fatal("module cache changed the offline workspace environment", env, err)
	}
	for _, profile := range []string{"runner-input", "runner-edit", "runner-check"} {
		files := workspaceFiles(c.cfg, profile, "/go")
		if profile == "runner-input" {
			if _, ok := files[cache]; ok {
				t.Fatal("input-only profile acquired the module cache")
			}
		} else if files[cache] != "read" {
			t.Fatalf("%s module cache is not read-only", profile)
		}
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if _, err := c.workerEnvironment(j); err == nil || !strings.Contains(err.Error(), "codex.go_module_cache") {
		t.Fatalf("worker accepted a missing module cache: %v", err)
	}
	if _, err := c.nativeConfiguration(context.Background()); err == nil || !strings.Contains(err.Error(), "codex.go_module_cache") {
		t.Fatalf("native setup accepted a missing module cache: %v", err)
	}
}

func TestWorkspaceGoCheckEnvironmentAndReadiness(t *testing.T) {
	c := &Codex{cfg: &runner.CodexSettings{GoBinary: "go"}, ctx: context.Background()}
	j := nativeJob{
		Assignment: runner.Assignment{AttemptID: "permission"},
		Workspace:  &workspaceRecord{WorkDir: t.TempDir()},
	}
	env, err := c.workerEnvironment(j)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["RUNNER_GO_CHECK"]; ok {
		t.Fatal("unselected Go check executable added to worker environment")
	}
	binary := filepath.Join(t.TempDir(), "runner-go-check")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.cfg.GoCheckBinary = binary
	env, err = c.workerEnvironment(j)
	if err != nil || env["RUNNER_GO_CHECK"] != binary {
		t.Fatal("Go check executable not selected", env, err)
	}
	for _, profile := range []string{"runner-input", "runner-edit", "runner-check"} {
		files := workspaceFiles(c.cfg, profile, "/go")
		if profile == "runner-input" {
			if _, ok := files[binary]; ok {
				t.Fatal("input-only profile acquired the Go check executable")
			}
		} else if files[binary] != "read" {
			t.Fatalf("%s Go check executable is not read-only", profile)
		}
	}
	if err := os.Remove(binary); err != nil {
		t.Fatal(err)
	}
	if _, err := c.workerEnvironment(j); err == nil || !strings.Contains(err.Error(), "codex.go_check_binary") {
		t.Fatalf("worker accepted a missing Go check executable: %v", err)
	}
	if _, err := c.nativeConfiguration(context.Background()); err == nil || !strings.Contains(err.Error(), "codex.go_check_binary") {
		t.Fatalf("native setup accepted a missing Go check executable: %v", err)
	}
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

func TestWorkspaceInitializationLockRequiresCompleteCheckoutAndRetainedCleanup(t *testing.T) {
	ctx := context.Background()
	repo, d := sourceRepository(t), workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_initialization")
	r, err := w.Prepare(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "worktree", "lock", "--reason", "initializing", r.WorkDir)
	doc := filepath.Join(r.WorkDir, "doc.go")
	data, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(doc); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Prepare(ctx, a); err == nil || !strings.Contains(err.Error(), "initialization is incomplete") {
		t.Fatal("partial checkout was admitted", err)
	}
	if err = os.WriteFile(doc, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Prepare(ctx, a); err != nil {
		t.Fatal("verified complete initialization could not settle", err)
	}
	if err = os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	candidate, err := w.Capture(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "worktree", "lock", "--reason", "operator inspection", r.WorkDir)
	if err = w.releaseRun(ctx, a.RunID, true); err == nil {
		t.Fatal("cleanup overrode an operator lock")
	}
	fixtureGit(t, repo, "worktree", "unlock", r.WorkDir)
	fixtureGit(t, repo, "worktree", "lock", "--reason", "initializing", r.WorkDir)
	if err = w.releaseRun(ctx, a.RunID, true); err != nil {
		t.Fatal("retained candidate could not release a settled initialization lock", err)
	}
	if _, err = d.ArtifactSize(candidate.PatchID); err != nil {
		t.Fatal("initialization recovery discarded the retained patch", err)
	}
	if _, err = os.Stat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled owned checkout was not removed", err)
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
func waitSourceRun(t *testing.T, c *runner.Core, id string, ready func(runner.Run) bool, timeout ...time.Duration) runner.Run {
	t.Helper()
	duration := 10 * time.Second
	if len(timeout) > 0 {
		duration = timeout[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
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

func TestWritableFailedReleaseAndAuthorizedRetryRestoreCandidate(t *testing.T) {
	repo := sourceRepository(t)
	cfg := workspaceSnapshot(t, repo)
	script := &scriptedPeer{results: []nativeTurn{{ID: "failed-turn", Status: "failed"}}}
	script.onTurn = func(dir string) {
		if script.starts != 0 {
			if data, err := os.ReadFile(filepath.Join(dir, "seats.go")); err != nil || string(data) != seatsImplementation {
				t.Error("authorized retry did not restore the saved candidate", err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
			t.Error(err)
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{RequestID: "failed-source", Project: "demo", Inputs: map[string]runner.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Failed" && allReleased(r) && r.ResourcesReleased })
	first := failed.Attempts[0].Source
	if first == nil || first.PatchID == "" || len(failed.Outputs) != 0 {
		t.Fatalf("failed candidate was lost or accepted: %+v", failed)
	}
	if _, err := os.Stat(first.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled failed checkout was not released", err)
	}
	if _, err := rig.disk.ArtifactSize(first.PatchID); err != nil {
		t.Fatal("failed patch was not retained", err)
	}
	if got := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", pin(r.ID)))); got != first.Base {
		t.Fatal("failed cleanup lost the accepted base")
	}
	if err := os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user index\n"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "doc.go")
	if err := os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user checkout\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index, checkout := fixtureGit(t, repo, "diff", "--cached", "--binary"), fixtureGit(t, repo, "diff", "--binary")
	if _, err := rig.core.Retry(runner.RetryRequest{RequestID: "explicit-retry", RunID: r.ID, ExtraAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	done := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Completed" && allReleased(r) && r.ResourcesReleased })
	if len(done.Attempts) != 3 || done.Attempts[0].End.Kind != "failed" || done.Attempts[1].Source == nil || done.Attempts[1].Source.InputPatchID != first.PatchID || done.Attempts[1].Source.Base != first.Base {
		t.Fatalf("retry rewrote history or lost source provenance: %+v", done)
	}
	script.mu.Lock()
	starts := script.starts
	script.mu.Unlock()
	if starts != 2 {
		t.Fatalf("unexpected model dispatch count: %d", starts)
	}
	if !bytes.Equal(index, fixtureGit(t, repo, "diff", "--cached", "--binary")) || !bytes.Equal(checkout, fixtureGit(t, repo, "diff", "--binary")) {
		t.Fatal("same-run restoration changed the user's index or checkout")
	}
}

func TestWritableFailedUncapturedSourceSurvivesHistoryExpiry(t *testing.T) {
	cfg := workspaceSnapshot(t, sourceRepository(t))
	cfg.Instance.Storage.KeepRuns = "1ns"
	script := &scriptedPeer{results: []nativeTurn{{ID: "failed-turn", Status: "failed"}}}
	// An ignored file is intentionally outside source capture, unlike owned
	// scratch. The failed run must retain it even after ordinary history expiry.
	script.onTurn = func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("uncaptured\n"), 0600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "uncaptured"), []byte("unique\n"), 0600); err != nil {
			t.Error(err)
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{RequestID: "preserved-failure", Project: "demo", Inputs: map[string]runner.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Status == "Failed" && allReleased(r) && r.CleanupError != "" })
	if failed.ResourcesReleased {
		t.Fatal("failed source with unique bytes was released")
	}
	if err := rig.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.core.Get(r.ID); err != nil {
		t.Fatal("history expiry discarded preserved failed work", err)
	}
	if data, err := os.ReadFile(filepath.Join(failed.Attempts[0].Source.WorkDir, "uncaptured")); err != nil || string(data) != "unique\n" {
		t.Fatal("uncaptured failed source was lost", err)
	}
}

func TestWorkspaceFailedCleanupPreservesUncapturedSourceAndMissingProof(t *testing.T) {
	for _, change := range []string{"missing-candidate", "missing-patch", "changed-patch", "tracked-edit", "untracked-file", "ignored-file"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			d, repo := workspaceDisk(t), sourceRepository(t)
			w := &Workspaces{disk: d}
			a := sourceAssignment(d, repo, "attempt_preserved")
			r, err := w.Prepare(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			defer w.ForgetRun(ctx, a.RunID)
			if change == "missing-candidate" {
				// Exercise lost recovery metadata for actual captured edits;
				// an unchanged base no longer needs an empty candidate to release.
				if err := os.WriteFile(filepath.Join(r.WorkDir, "doc.go"), []byte("package seats\n// captured edit\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			candidate, err := w.Capture(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing-candidate":
				r.Candidate = nil
				if err := w.save(r); err != nil {
					t.Fatal(err)
				}
			case "missing-patch":
				if err := d.Remove("artifacts/" + candidate.PatchID); err != nil {
					t.Fatal(err)
				}
			case "changed-patch":
				if err := d.Write("artifacts/"+candidate.PatchID, addedSeedFile("different.txt", "100644"), d.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			default:
				name := "uncaptured.txt"
				if change == "tracked-edit" {
					name = "doc.go"
				}
				if change == "ignored-file" {
					exclude := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "--git-path", "info/exclude")))
					if !filepath.IsAbs(exclude) {
						exclude = filepath.Join(repo, exclude)
					}
					if err := os.MkdirAll(filepath.Dir(exclude), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(exclude, []byte("uncaptured.txt\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(r.WorkDir, name), []byte("unique source\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.releaseRun(ctx, a.RunID, true); err == nil {
				t.Fatal("failed cleanup discarded source without complete recovery proof")
			}
			if saved, err := w.load(a.RunID); err != nil || saved.Released {
				t.Fatal("preserved workspace was marked released", err)
			}
			if _, err := os.Stat(filepath.Join(r.WorkDir, ".git")); err != nil {
				t.Fatal("preserved source checkout is missing", err)
			}
		})
	}
}

func TestWorkspaceProofUsesOnlyDisposableIndexLocks(t *testing.T) {
	ctx := context.Background()
	d, repo := workspaceDisk(t), sourceRepository(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, repo, "attempt_index_locks")
	r, err := w.Prepare(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	defer w.ForgetRun(ctx, a.RunID)
	if _, err := w.Capture(ctx, a); err != nil {
		t.Fatal(err)
	}
	r, err = w.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	indexPath := strings.TrimSpace(string(fixtureGit(t, r.WorkDir, "rev-parse", "--path-format=absolute", "--git-path", "index")))
	mainIndex := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "index")))
	wantIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	// Git ignores optional uppercase extensions. Pad a valid index to the live
	// footprint without thousands of source files, then recompute its checksum.
	files := fixtureGit(t, r.WorkDir, "ls-files")
	extensionSize := 1_400_000 - len(wantIndex) - 8
	extended := append(bytes.Clone(wantIndex[:len(wantIndex)-sha1.Size]), []byte("TEST")...)
	extended = binary.BigEndian.AppendUint32(extended, uint32(extensionSize))
	extended = append(extended, make([]byte, extensionSize)...)
	checksum := sha1.Sum(extended)
	wantIndex = append(extended, checksum[:]...)
	if err := os.WriteFile(indexPath, wantIndex, 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(files, fixtureGit(t, r.WorkDir, "ls-files")) {
		t.Fatal("large index fixture changed tracked source")
	}
	wantMainIndex, err := os.ReadFile(mainIndex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath+".lock", []byte("settled private lock"), 0600); err != nil {
		t.Fatal(err)
	}
	assertUntouched := func() {
		t.Helper()
		for path, want := range map[string][]byte{
			indexPath:           wantIndex,
			mainIndex:           wantMainIndex,
			indexPath + ".lock": []byte("settled private lock"),
		} {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, want) {
				t.Fatalf("proof changed the live index or its lock: %s: %v", path, err)
			}
		}
	}
	configuredLimit := d.Policy.MaxArtifact
	d.Policy.MaxArtifact = 1024
	if _, _, err := w.retainedCandidate(ctx, r, true); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("proof index exceeded the configured artifact budget", err)
	}
	assertUntouched()
	d.Policy.MaxArtifact = configuredLimit
	if _, _, err := w.retainedCandidate(ctx, r, true); err != nil {
		t.Fatal("valid large proof index or real worktree index.lock blocked read-only proof", err)
	}
	assertUntouched()

	// Cancel exactly after an index command takes its disposable lock. The
	// wrapper blocks on a FIFO; no timing sleep or live Git process is involved.
	fixture := t.TempDir()
	for _, name := range []string{"ready", "hold"} {
		if err := exec.Command("mkfifo", filepath.Join(fixture, name)).Run(); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := os.OpenFile(filepath.Join(fixture, "ready"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ready.Close()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shellPath := func(path string) string { return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'" }
	scratchIndex := filepath.Join(d.Root, "work", a.RunID, "seed-validation", "index")
	script := fmt.Sprintf(`#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = read-tree ]; then
    [ "$GIT_INDEX_FILE" = %s ] || exit 91
    printf disposable > "$GIT_INDEX_FILE.lock"
    printf ready > %s
    IFS= read -r gate < %s
    exit 92
  fi
done
exec %s "$@"
`, shellPath(scratchIndex), shellPath(filepath.Join(fixture, "ready")), shellPath(filepath.Join(fixture, "hold")), shellPath(realGit))
	if err := os.WriteFile(filepath.Join(fixture, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", fixture+string(os.PathListSeparator)+originalPath)
	started, result := make(chan error, 1), make(chan error, 1)
	go func() {
		var one [1]byte
		_, err := ready.Read(one[:])
		started <- err
	}()
	cancelled, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	go func() {
		_, _, err := w.retainedCandidate(cancelled, r, true)
		result <- err
	}()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatal("index command did not reach its disposable lock", err)
	case <-cancelled.Done():
		t.Fatal("index command did not reach its disposable lock", cancelled.Err())
	}
	if data, err := os.ReadFile(scratchIndex + ".lock"); err != nil || string(data) != "disposable" {
		t.Fatal("index command did not lock owned scratch", err)
	}
	cancel()
	if err := <-result; err == nil {
		t.Fatal("cancelled proof completed successfully")
	}
	assertUntouched()
	if _, err := os.Stat(filepath.Dir(scratchIndex)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled proof retained disposable index state", err)
	}
	t.Setenv("PATH", originalPath)
	if err := w.releaseRun(ctx, a.RunID, true); err != nil {
		t.Fatal("settled private index.lock blocked owned forced cleanup", err)
	}
	if got, err := os.ReadFile(mainIndex); err != nil || !bytes.Equal(got, wantMainIndex) {
		t.Fatal("owned cleanup changed the original checkout index", err)
	}
}

func TestWorkspaceFailedCleanupAboveCapacityAndRestoreReplay(t *testing.T) {
	for _, missingGitLink := range []bool{false, true} {
		t.Run(fmt.Sprint("partial-checkout=", missingGitLink), func(t *testing.T) {
			ctx := context.Background()
			d, repo := workspaceDisk(t), sourceRepository(t)
			w := &Workspaces{disk: d}
			a := sourceAssignment(d, repo, "attempt_restore")
			r, err := w.Prepare(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			defer w.ForgetRun(ctx, a.RunID)
			if err := os.WriteFile(filepath.Join(r.WorkDir, "saved.txt"), []byte("saved candidate\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := w.Capture(ctx, a); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(r.WorkDir, ".runner-tmp"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, ".runner-tmp", "disposable"), make([]byte, 256<<10), 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.Refresh(); err != nil {
				t.Fatal(err)
			}
			policy := d.Policy
			d.Policy.MaxManaged = d.Policy.Database.MaxSize + runner.Bytes(d.Used()) - 1
			if err := w.releaseRun(ctx, a.RunID, true); err != nil {
				t.Fatal("verified cleanup could not free resources above the monitored cap", err)
			}
			d.Policy = policy
			r, err = w.load(a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			r.Released = false // Removal finished but its metadata save was lost.
			if err := w.save(r); err != nil {
				t.Fatal(err)
			}
			r, err = w.Prepare(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			r.Released = true // The final restore readiness save was interrupted.
			if err := w.save(r); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(r.WorkDir, "partial.txt"), []byte("pre-execution residue"), 0600); err != nil {
				t.Fatal(err)
			}
			if missingGitLink {
				if err := os.Remove(filepath.Join(r.WorkDir, ".git")); err != nil {
					t.Fatal(err)
				}
			}
			reopened := &Workspaces{disk: d}
			r, err = reopened.Prepare(ctx, a)
			if err != nil || r.Released || r.Candidate == nil {
				t.Fatal("interrupted restoration was not recoverable", err)
			}
			if _, err := os.Stat(filepath.Join(r.WorkDir, "partial.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("pre-execution residue survived restore replay", err)
			}
			file := filepath.Join(r.WorkDir, "saved.txt")
			if data, err := os.ReadFile(file); err != nil || string(data) != "saved candidate\n" {
				t.Fatal("restore replay lost the retained candidate", err)
			}
			if err := os.WriteFile(file, []byte("ready edits\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Prepare(ctx, a); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(file); err != nil || string(data) != "ready edits\n" {
				t.Fatal("ready restore replay reset later source edits", err)
			}
		})
	}
}

func TestWorkspaceCapacityRefusesBeforeCheckoutAndCanResume(t *testing.T) {
	ctx := context.Background()
	d, repo := workspaceDisk(t), sourceRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "asset"), make([]byte, 256<<10), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "asset")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "large source")
	w := &Workspaces{disk: d, nativeLimit: 64 << 10}
	a := sourceAssignment(d, repo, "attempt_capacity")
	policy := d.Policy
	d.Policy.MaxManaged = d.Policy.Database.MaxSize + 3*d.Policy.Database.MaxRecord + d.Policy.MaxArtifact + d.Policy.Logs.MaxPerAttempt + 128<<10
	if _, err := w.Prepare(ctx, a); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("oversized checkout was admitted", err)
	}
	r, err := w.load(a.RunID)
	if err != nil || r.Base == "" {
		t.Fatal("capacity refusal lost recoverable preparation identity", err)
	}
	if _, err := os.Stat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("capacity was checked after allocating source", err)
	}
	if worktrees := fixtureGit(t, repo, "worktree", "list", "--porcelain"); bytes.Contains(worktrees, []byte(r.WorkDir)) {
		t.Fatal("capacity refusal registered a disposable checkout")
	}
	if err := w.releaseRun(ctx, a.RunID, true); err != nil {
		t.Fatal("metadata-only failure was held for nonexistent source proof", err)
	}
	d.Policy = policy
	defer w.ForgetRun(ctx, a.RunID)
	if restored, err := w.Prepare(ctx, a); err != nil || restored.Base != r.Base {
		t.Fatal("same preparation could not resume when capacity was available", err)
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
