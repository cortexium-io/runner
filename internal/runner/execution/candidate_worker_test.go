//go:build darwin || linux

package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestCandidateWorkerLegacyCompletedIntentCollectsAfterDeadlineWithoutRefWrites(t *testing.T) {
	c, a := candidateWorkerFixture(t)
	a.Deadline = time.Now().Add(-time.Second)
	// Model the old adapter's effects before its result was saved. Its commit
	// timestamp is deterministic from the original frozen assignment.
	text, err := c.workspaces.retainCandidate(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(candidateJob{Assignment: a})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.disk.Write(candidateName(a.AttemptID), raw, c.disk.Policy.Database.MaxRecord); err != nil {
		t.Fatal(err)
	}
	g := candidateGitGate(t, "none", false)
	o, err := c.Observe(t.Context(), a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" || string(o.End.Result.Outputs["candidate"].Inline) != string(runner.Text(text).Inline) {
		t.Fatal("completed legacy intent was not collected", o, err)
	}
	data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
	if len(data) != 0 {
		t.Fatal("late collection rebuilt/mutated candidate", string(data))
	}
	saved, _ := c.load(a.AttemptID)
	if !saved.Assignment.Deadline.Equal(a.Deadline) {
		t.Fatal("late collection changed deadline")
	}
}

func TestCandidateWorkerReleasePreservesReplacedScratchSymlink(t *testing.T) {
	c, a := candidateWorkerFixture(t)
	if o, err := c.Start(t.Context(), a); err != nil || o.End == nil || o.End.Kind != "returned" {
		t.Fatal(o, err)
	}
	root := filepath.Join(c.disk.Root, "work", a.AttemptID)
	if err := os.Rename(root, root+"-preserved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	dir := filepath.Join(outside, "candidate")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "index")
	if err := os.WriteFile(sentinel, []byte("preserve external content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	if err := c.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("replaced scratch was traversed")
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "preserve external content" {
		t.Fatal("external content changed", err)
	}
	if info, err := os.Lstat(root); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("scratch symlink removed", err)
	}
}

func candidateWorkerFixture(t *testing.T) (*Candidates, runner.Assignment) {
	t.Helper()
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, sourceRepository(t), "attempt_candidate_worker")
	a.Deadline = time.Now().UTC().Add(time.Minute)
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := w.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile = runner.Profile{Harness: "git-candidate", Access: "workspace-read"}
	a.AttemptID = "attempt_candidate_integrate"
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}, "verdict": runner.Text("APPROVE")}
	c := &Candidates{disk: d, workspaces: w}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c, a
}
func candidateGitGate(t *testing.T, stage string, after bool) *captureGitFixture {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	g := adoptionCheckoutGate(t)
	script := "#!/bin/sh\nstage=''\nfor arg do\n case \"$arg\" in read-tree|commit-tree) stage=$arg;; refs/heads/codex/runner/*) if [ \"$previous\" = update-ref ]; then stage=branch; fi;; esac\n previous=$arg\ndone\n"
	script += "if [ -n \"$stage\" ]; then printf '%s\\n' \"$stage\" >> " + testShellQuote(filepath.Join(g.dir, "calls")) + "; fi\n"
	script += "if [ \"$stage\" = " + testShellQuote(stage) + " ] && [ ! -e " + testShellQuote(filepath.Join(g.dir, "blocked")) + " ]; then\n : > " + testShellQuote(filepath.Join(g.dir, "blocked")) + "\n"
	if after {
		script += testShellQuote(real) + " \"$@\"\n status=$?\n if [ \"$status\" != 0 ]; then exit \"$status\"; fi\n"
	}
	script += " printf '%s\\n' \"$stage\" > " + testShellQuote(filepath.Join(g.dir, "ready")) + "\n IFS= read -r release < " + testShellQuote(filepath.Join(g.dir, "hold")) + "\n"
	if after {
		script += " exit 0\n"
	}
	script += "fi\nexec " + testShellQuote(real) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(g.dir, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return g
}
func holdCandidate(t *testing.T, c *Candidates, a runner.Assignment, g *captureGitFixture) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(context.Canceled)
	done := make(chan runner.Observation, 1)
	go func() {
		o, err := c.Start(captureCutoffContext{ctx}, a)
		if err != nil {
			t.Error(err)
		}
		done <- o
	}()
	select {
	case <-g.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate phase did not reach gate")
	}
	cancel(context.DeadlineExceeded)
	select {
	case o := <-done:
		if o.State != runner.Unknown || !o.Reconciling || o.End != nil {
			t.Fatal("observer ended candidate", o)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
}
func TestCandidateWorkerJoinsSlowPreparationAndWakesCore(t *testing.T) {
	c, a := candidateWorkerFixture(t)
	g := candidateGitGate(t, "read-tree", false)
	wake := make(chan string, 1)
	c.SetWake(func(id string) { wake <- id })
	holdCandidate(t, c, a, g)
	c.mu.Lock()
	original := c.workers[a.AttemptID]
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if o, err := c.Observe(ctx, a.AttemptID); err != nil || !o.Reconciling {
		t.Fatal(o, err)
	}
	c.mu.Lock()
	same := original == c.workers[a.AttemptID]
	c.mu.Unlock()
	if !same {
		t.Fatal("observer restarted original worker")
	}
	if err := c.Release(t.Context(), a.AttemptID); err == nil {
		t.Fatal("active candidate released")
	}
	if _, err := g.hold.Write([]byte("continue\n")); err != nil {
		t.Fatal(err)
	}
	o, err := c.Observe(t.Context(), a.AttemptID)
	if err != nil || o.End == nil || o.End.Kind != "returned" {
		t.Fatal(o, err)
	}
	select {
	case id := <-wake:
		if id != a.AttemptID {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("missing Core callback")
	}
	data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
	if strings.Count(string(data), "read-tree\n") != 1 || strings.Count(string(data), "commit-tree\n") != 1 || strings.Count(string(data), "branch\n") != 1 {
		t.Fatal("candidate preparation repeated", string(data))
	}
	if err = c.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
}
func TestCandidateWorkerRestartReusesAppliedIntentAndStopCreatesNoRefs(t *testing.T) {
	for _, stage := range []string{"read-tree", "branch"} {
		for _, stop := range []bool{false, true} {
			t.Run(stage+map[bool]string{false: "-restart", true: "-stop"}[stop], func(t *testing.T) {
				c, a := candidateWorkerFixture(t)
				g := candidateGitGate(t, stage, stage == "branch")
				holdCandidate(t, c, a, g)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if stop {
					if err := c.Stop(ctx, a.AttemptID); err != nil {
						t.Fatal(err)
					}
				}
				if err := c.Close(ctx); err != nil {
					t.Fatal(err)
				}
				resumed := &Candidates{disk: c.disk, workspaces: c.workspaces}
				defer resumed.Close(context.Background())
				o, err := resumed.Observe(t.Context(), a.AttemptID)
				if err != nil || o.End == nil {
					t.Fatal(o, err)
				}
				if stop {
					if o.End.Kind != "stopped" {
						t.Fatal(o.End)
					}
					if missingCandidateRef(t.Context(), a.Repository, candidateRef(a.RunID)) != (stage == "read-tree") {
						t.Fatal("Stop created/lost a candidate branch")
					}
				} else if o.End.Kind != "returned" {
					t.Fatal(o.End)
				}
				data, _ := os.ReadFile(filepath.Join(g.dir, "calls"))
				if stage == "branch" && (strings.Count(string(data), "commit-tree\n") != 1 || strings.Count(string(data), "branch\n") != 1) {
					t.Fatal("recovery duplicated commit/ref operation", string(data))
				}
				saved, _ := resumed.load(a.AttemptID)
				if !saved.Assignment.Deadline.Equal(a.Deadline) {
					t.Fatal("recovery changed deadline")
				}
				if err = resumed.Release(t.Context(), a.AttemptID); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
func TestCandidateWorkerApprovalAndPatchIdentityRemainRequired(t *testing.T) {
	for _, kind := range []string{"approval", "patch"} {
		t.Run(kind, func(t *testing.T) {
			c, a := candidateWorkerFixture(t)
			if kind == "approval" {
				a.Inputs["verdict"] = runner.Text("BLOCKED")
			} else {
				raw, err := c.disk.Read("artifacts/"+a.Inputs["patch"].ArtifactID, a.Limits.MaxArtifact)
				if err != nil {
					t.Fatal(err)
				}
				if err = c.disk.Write("artifacts/"+a.Inputs["patch"].ArtifactID, append(raw, []byte("\n")...), a.Limits.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			}
			o, err := c.Start(t.Context(), a)
			if err != nil || o.End == nil || o.End.Kind != "failed" || !missingCandidateRef(t.Context(), a.Repository, candidateRef(a.RunID)) {
				t.Fatal("unaccepted source integrated", o, err)
			}
		})
	}
}
func TestCandidateWorkerExpiredDeadlineAndCancelledMutexWait(t *testing.T) {
	c, a := candidateWorkerFixture(t)
	c.workspaces.mu.Lock()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := c.workspaces.Prepare(ctx, a); done <- err }()
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Prepare ignored cancellation while waiting for source mutex")
	}
	observer, detach := context.WithCancel(t.Context())
	detach()
	o, err := c.Start(observer, a)
	if err != nil || o.End != nil || !o.Reconciling {
		t.Fatal("candidate observer did not detach from mutex wait", o, err)
	}
	closeCtx, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	if err = c.Close(closeCtx); err != nil {
		t.Fatal("candidate Close waited for unrelated source lock", err)
	}
	c.workspaces.mu.Unlock()
	if !missingCandidateRef(t.Context(), a.Repository, candidateRef(a.RunID)) {
		t.Fatal("cancelled candidate created branch")
	}
	expired, assignment := candidateWorkerFixture(t)
	assignment.Deadline = time.Now().Add(-time.Second)
	o, err = expired.Start(t.Context(), assignment)
	if err != nil || o.End != nil || !o.Reconciling {
		t.Fatal("expired candidate gained preparation allowance", o, err)
	}
	if !missingCandidateRef(t.Context(), assignment.Repository, candidateRef(assignment.RunID)) {
		t.Fatal("expired candidate created branch")
	}
}
func TestCandidateWorkerServiceCloseCancelsPrepareMutexWait(t *testing.T) {
	c, a := candidateWorkerFixture(t)
	service := NewService(c.disk, nil, &runner.CodexSettings{StorageLimit: 16 << 20})
	service.workspaces.mu.Lock()
	a.AttemptID = "attempt_blocked_command"
	a.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read", Command: &runner.Command{Args: []string{"/usr/bin/true"}}}
	if _, err := service.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	capture := runner.Clone(a)
	capture.AttemptID = "attempt_blocked_capture"
	capture.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	observer, detach := context.WithCancel(t.Context())
	detach()
	if _, err := service.captureSource(observer, capture, false); err == nil {
		t.Fatal("capture observer did not detach")
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() { done <- service.Close(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Service.Close deadlocked on source mutex")
	}
	service.workspaces.mu.Unlock()
}
