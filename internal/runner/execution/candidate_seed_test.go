//go:build darwin || linux

package execution

import (
	"bytes"
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

func retainedCandidateSeed(t *testing.T, repo string) (string, string, []byte) {
	t.Helper()
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	work := filepath.Join(t.TempDir(), "retained-source")
	fixtureGit(t, repo, "worktree", "add", "--detach", work, base)
	if err := os.WriteFile(filepath.Join(work, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, work, "add", "seats.go")
	tree := strings.TrimSpace(string(fixtureGit(t, work, "write-tree")))
	patch := fixtureGit(t, work, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--")
	fixtureGit(t, repo, "worktree", "remove", "--force", work)
	return base, tree, patch
}

func candidateSeedFixture(t *testing.T) (*Candidates, runner.Assignment, string, []byte) {
	t.Helper()
	disk, repo := workspaceDisk(t), sourceRepository(t)
	base, tree, patch := retainedCandidateSeed(t, repo)
	if err := disk.Write("artifacts/retained/changes.patch", patch, disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	w := &Workspaces{disk: disk, read: func(_ context.Context, id string) ([]byte, error) {
		return disk.Read("artifacts/"+id, disk.Policy.MaxArtifact)
	}}
	a := sourceAssignment(disk, repo, "seed_check")
	a.Source.Ref = base
	a.Setup.Profile = runner.Profile{Harness: "command", Access: "workspace-read"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: "retained/changes.patch"}}
	if _, err := w.Prepare(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	if err := w.MarkReady(t.Context(), a.RunID, "verified-review-generation"); err != nil {
		t.Fatal(err)
	}
	a.AttemptID, a.Deadline = "seed_integrate", time.Now().Add(time.Minute)
	a.Setup.Profile = runner.Profile{Harness: "git-candidate", Access: "workspace-read"}
	a.Inputs["verdict"], a.Inputs["review"] = runner.Text("APPROVE"), runner.Text("Reviewed the exact retained source and scoped passing check.")
	c := &Candidates{disk: disk, workspaces: w}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c, a, tree, patch
}

func TestApprovedSeedCandidateCapturesExactSourceAndRejoinsBeforeCleanup(t *testing.T) {
	c, a, tree, patch := candidateSeedFixture(t)
	if err := os.WriteFile(filepath.Join(a.Repository, "doc.go"), []byte("preserved user work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	before := fixtureGit(t, a.Repository, "status", "--porcelain")
	marker := filepath.Join(t.TempDir(), "hook-executed")
	if err := os.MkdirAll(filepath.Join(a.Repository, ".git", "hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.Repository, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\ntouch "+testShellQuote(marker)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	o, err := c.Start(t.Context(), a)
	if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil || o.Source.Tree != tree || o.Source.InputPatchID != a.Inputs["patch"].ArtifactID || o.Source.PatchID != a.AttemptID+"/changes.patch" {
		t.Fatal("approved seed did not retain real capture provenance", o, err)
	}
	got, err := c.disk.Read("artifacts/"+o.Source.PatchID, a.Limits.MaxArtifact)
	if err != nil || !bytes.Equal(got, patch) {
		t.Fatal("seed capture changed reviewed patch bytes", err)
	}
	var text string
	if json.Unmarshal(o.End.Result.Outputs["candidate"].Inline, &text) != nil {
		t.Fatal("candidate result missing")
	}
	head := strings.Split(text, "\n")[1]
	if actual := strings.TrimSpace(string(fixtureGit(t, a.Repository, "rev-parse", head+"^{tree}"))); actual != tree {
		t.Fatal("candidate tree differs from the independently retained seed", actual)
	}
	if parent := strings.TrimSpace(string(fixtureGit(t, a.Repository, "rev-parse", head+"^"))); parent != a.Source.Ref {
		t.Fatal("seed integration changed its reviewed base", parent)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) || !bytes.Equal(before, fixtureGit(t, a.Repository, "status", "--porcelain")) {
		t.Fatal("integration changed user work/index or invoked a hook", err)
	}
	job, err := c.load(a.AttemptID)
	if err != nil || job.Source == nil || job.Capture == nil {
		t.Fatal("source proof was not durable before result handoff", err)
	}
	job.End = nil // Commit/ref and source proof survived a lost final response.
	if err = c.save(job); err != nil {
		t.Fatal(err)
	}
	if err = c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	gate := candidateGitGate(t, "none", false)
	resumed := &Candidates{disk: c.disk, workspaces: c.workspaces}
	defer resumed.Close(context.Background())
	again, err := resumed.Start(t.Context(), a)
	if err != nil || again.End == nil || again.End.Kind != "returned" || again.Source == nil || *again.Source != *o.Source || !bytes.Equal(again.End.Result.Outputs["candidate"].Inline, o.End.Result.Outputs["candidate"].Inline) {
		t.Fatal("restarted seed integration changed accepted source or result", again, err)
	}
	if calls, _ := os.ReadFile(filepath.Join(gate.dir, "calls")); len(calls) != 0 {
		t.Fatal("saved seed integration repeated commit/ref preparation", string(calls))
	}
	if err = resumed.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal("owned committed seed capture did not clean up", err)
	}
	if err = resumed.Release(t.Context(), a.AttemptID); err != nil {
		t.Fatal("seed capture release is not idempotent", err)
	}
	if _, err = os.Stat(filepath.Join(c.disk.Root, "work", a.AttemptID)); !os.IsNotExist(err) {
		t.Fatal("capture/candidate scratch survived owned release", err)
	}
	if got, err = c.disk.Read("artifacts/"+o.Source.PatchID, a.Limits.MaxArtifact); err != nil || !bytes.Equal(got, patch) {
		t.Fatal("cleanup removed retained source evidence", err)
	}
}

func TestSeedCandidateRejectsUnapprovedUnpreparedForeignOrChangedSource(t *testing.T) {
	for _, fault := range []string{
		"missing verdict",
		"blocked review",
		"missing review",
		"different input",
		"unprepared",
		"not ready",
		"missing generation",
		"released",
		"drift",
		"uncaptured file",
		"foreign repository",
		"foreign base",
		"foreign capture",
		"baseline",
	} {
		t.Run(fault, func(t *testing.T) {
			c, a, _, _ := candidateSeedFixture(t)
			r, err := c.workspaces.load(a.RunID)
			if err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "missing verdict":
				delete(a.Inputs, "verdict")
			case "blocked review":
				a.Inputs["verdict"] = runner.Text("BLOCKED")
			case "missing review":
				delete(a.Inputs, "review")
			case "different input":
				a.Inputs["patch"] = runner.Value{ArtifactID: "foreign/changes.patch"}
			case "unprepared":
				a.RunID = "run_no_source"
			case "not ready":
				r.SeedReady = false
			case "missing generation":
				r.ReadyGeneration = ""
			case "released":
				r.Released = true
			case "drift":
				err = os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte("unreviewed source\n"), 0600)
			case "uncaptured file":
				err = os.WriteFile(filepath.Join(r.WorkDir, "unknown.txt"), []byte("preserve"), 0600)
			case "foreign repository":
				a.Repository = sourceRepository(t)
			case "foreign base":
				a.Source.Ref = strings.Repeat("f", 40)
			case "foreign capture":
				r.Capture = &captureReference{AttemptID: "unsettled_other_owner"}
			case "baseline":
				if err = os.WriteFile(filepath.Join(c.disk.Root, "artifacts", r.SeedPatchID), nil, 0600); err == nil {
					err = os.Remove(filepath.Join(r.WorkDir, "seats.go"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = c.workspaces.save(r); err != nil {
				t.Fatal(err)
			}
			o, err := c.Start(t.Context(), a)
			if err != nil || o.End == nil || o.End.Kind != "failed" || o.Source != nil {
				t.Fatal("invalid seed gained capture/integration authority", fault, o, err)
			}
			if !missingCandidateRef(t.Context(), r.Repository, candidateRef(a.RunID)) {
				t.Fatal("rejected seed created a candidate branch")
			}
			if _, err = os.Stat(filepath.Join(c.disk.Root, "artifacts", a.AttemptID, "changes.patch")); !os.IsNotExist(err) {
				t.Fatal("rejected seed was captured before authentication", err)
			}
		})
	}
}

func TestApprovedSeedCandidateStopAndLostBranchReplyPreserveKnownSource(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost-reply", true: "stop"}[stop], func(t *testing.T) {
			c, a, _, _ := candidateSeedFixture(t)
			gate := candidateGitGate(t, "branch", true)
			holdSeedCandidate(t, c, a, gate)
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
			want := "returned"
			if stop {
				want = "stopped"
			}
			if err != nil || o.End == nil || o.End.Kind != want || o.Source == nil {
				t.Fatal("known seed capture/ref did not reconcile", o, err)
			}
			if calls, _ := os.ReadFile(filepath.Join(gate.dir, "calls")); strings.Count(string(calls), "commit-tree\n") != 1 || strings.Count(string(calls), "branch\n") != 1 {
				t.Fatal("seed replay/Stop repeated local effects", string(calls))
			}
			if err = resumed.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal("settled seed capture/ref did not release", err)
			}
		})
	}
}

func holdSeedCandidate(t *testing.T, c *Candidates, a runner.Assignment, gate *captureGitFixture) {
	t.Helper()
	bounded, stop := context.WithDeadline(t.Context(), a.Deadline)
	defer stop()
	observer, detach := context.WithCancelCause(bounded)
	defer detach(context.Canceled)
	type result struct {
		observation runner.Observation
		err         error
	}
	done := make(chan result, 1)
	go func() {
		o, err := c.Start(captureCutoffContext{observer}, a)
		done <- result{o, err}
	}()
	select {
	case <-gate.ready:
	case r := <-done:
		t.Fatalf("seed candidate ended before branch gate: %+v; %v", r.observation, r.err)
	case <-bounded.Done():
		t.Fatal("seed candidate did not reach branch gate within its original deadline", bounded.Err())
	}
	detach(context.DeadlineExceeded)
	select {
	case r := <-done:
		if r.err != nil || r.observation.State != runner.Unknown || !r.observation.Reconciling || r.observation.End != nil {
			t.Fatal("observer ended seed candidate", r.observation, r.err)
		}
	case <-time.After(time.Second):
		t.Fatal("observer did not detach")
	}
}

func TestApprovedSeedCandidateCaptureCleanupPreservesUnknownContent(t *testing.T) {
	for _, kind := range []string{"regular", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			c, a, _, _ := candidateSeedFixture(t)
			o, err := c.Start(t.Context(), a)
			if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil {
				t.Fatal("seed integration failed", o, err)
			}
			root := filepath.Join(c.disk.Root, "work", a.AttemptID)
			unknown := filepath.Join(root, "capture-v1", "keep.txt")
			if kind == "regular" {
				err = os.WriteFile(unknown, []byte("not owned by the capture protocol"), 0600)
			} else {
				err = os.Symlink(filepath.Join(root, "capture-v1", "manifest.json"), unknown)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Release(t.Context(), a.AttemptID); err == nil {
				t.Fatal("cleanup accepted unknown regular capture content")
			}
			for _, path := range []string{unknown, filepath.Join(root, "capture-v1", "manifest.json"), filepath.Join(root, "candidate", "index")} {
				if _, err = os.Stat(path); err != nil {
					t.Fatal("cleanup removed bytes before its whole inventory passed", path, err)
				}
			}
			if err = os.Remove(unknown); err != nil {
				t.Fatal(err)
			}
			if err = c.Release(t.Context(), a.AttemptID); err != nil {
				t.Fatal("known capture scratch did not release", err)
			}
		})
	}
}

func TestApprovedSeedCandidateReplayRejectsChangedProof(t *testing.T) {
	for _, proof := range []string{"seed", "capture", "manifest"} {
		t.Run(proof, func(t *testing.T) {
			c, a, _, _ := candidateSeedFixture(t)
			o, err := c.Start(t.Context(), a)
			if err != nil || o.End == nil || o.End.Kind != "returned" || o.Source == nil {
				t.Fatal("seed integration failed", o, err)
			}
			job, err := c.load(a.AttemptID)
			if err != nil {
				t.Fatal(err)
			}
			job.End = nil
			if err = c.save(job); err != nil {
				t.Fatal(err)
			}
			path := "artifacts/" + a.Inputs["patch"].ArtifactID
			if proof == "capture" {
				path = "artifacts/" + o.Source.PatchID
			} else if proof == "manifest" {
				path = "work/" + a.AttemptID + "/capture-v1/manifest.json"
			}
			if err = os.WriteFile(filepath.Join(c.disk.Root, filepath.FromSlash(path)), []byte("changed proof"), 0600); err != nil {
				t.Fatal(err)
			}
			gate := candidateGitGate(t, "none", false)
			again, err := c.Observe(t.Context(), a.AttemptID)
			if err == nil && again.End != nil && again.End.Kind == "returned" {
				t.Fatal("changed seed proof was accepted", proof, again)
			}
			if calls, _ := os.ReadFile(filepath.Join(gate.dir, "calls")); len(calls) != 0 {
				t.Fatal("invalid replay repeated commit/ref effects", string(calls))
			}
		})
	}
}

func TestRetainedSeedReviewIntegratesThroughCoreWithoutCoding(t *testing.T) {
	repo := sourceRepository(t)
	base, tree, patch := retainedCandidateSeed(t, repo)
	cfg := workspaceSnapshot(t, repo)
	p := cfg.Projects["demo"]
	p.Source = &runner.SourceSettings{Ref: base}
	p.Roles["retain-seed"] = runner.Role{
		ID: "retain-seed", Purpose: "Retain an owned fixture patch.", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"},
		Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(patch)},
	}
	p.Roles["seed-check"] = runner.Role{
		ID: "seed-check", Purpose: "Run the fixed scoped fixture contract.", Inputs: map[string]string{"patch": "artifact"}, Outputs: map[string]string{"check": "text"},
		Execution: &runner.Profile{Harness: "command", Access: "workspace-read", Command: &runner.Command{Args: []string{cfg.Instance.Codex.GoBinary, "test", "-count=1", "-run", "^TestParseSeats$", "./..."}}},
	}
	review := runner.Clone(p.Roles["implement"])
	review.ID, review.Purpose, review.Execution.Access = "seed-review", "Independently review the supplied retained source and actual check.", "workspace-read"
	review.Inputs, review.Outputs = map[string]string{"patch": "artifact", "check": "text"}, map[string]string{"verdict": "text", "summary": "text"}
	p.Roles[review.ID] = review
	p.Roles["integrate"] = runner.Role{
		ID: "integrate", Purpose: "Retain only the independently approved source locally.",
		Inputs: map[string]string{"patch": "artifact", "verdict": "text", "review": "text"}, Outputs: map[string]string{"candidate": "text"},
		Execution: &runner.Profile{Harness: "git-candidate", Access: "workspace-read"},
	}
	p.DefaultProcess = "retained"
	p.Processes = map[string]runner.Process{
		"saved": {ID: "saved", Inputs: map[string]string{}, Steps: []runner.Step{{ID: "save", Role: "retain-seed"}}, Outputs: map[string]runner.Binding{"patch": {FromStep: "save", Output: "patch"}}, MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"},
		"retained": {
			ID: "retained", Inputs: map[string]string{"patch": "artifact"},
			Steps: []runner.Step{
				{ID: "check", Role: "seed-check", Inputs: map[string]runner.Binding{"patch": {FromInput: "patch"}}},
				{ID: "review", Role: "seed-review", Inputs: map[string]runner.Binding{"patch": {FromInput: "patch"}, "check": {FromStep: "check", Output: "check"}}},
				{ID: "integrate", Role: "integrate", Inputs: map[string]runner.Binding{"patch": {FromInput: "patch"}, "verdict": {FromStep: "review", Output: "verdict"}, "review": {FromStep: "review", Output: "summary"}}},
			},
			Outputs: map[string]runner.Binding{"candidate": {FromStep: "integrate", Output: "candidate"}}, MaxAttempts: 18, MaxAttemptsPerStep: 1, AttemptTimeout: "2m",
		},
	}
	cfg.Projects["demo"] = p
	approved := goodTurn()
	approved.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Scoped source and real check approved."},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{approved}}
	script.onTurn = func(dir string) {
		if script.starts != 0 || !bytes.Equal(stagedEvidence(t, dir, "patch"), patch) {
			t.Error("review did not receive the exact retained patch")
		}
		data, err := os.ReadFile(filepath.Join(dir, "seats.go"))
		if err != nil || string(data) != seatsImplementation {
			t.Error("review source differs from restored seed", err)
		}
	}
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	if err := os.WriteFile(sandbox, []byte("#!/bin/sh\nwhile [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	hook := func(s *Service) {
		s.commands.cfg.Binary = sandbox
		s.commands.launchWorker = func(spec string, finished func()) error {
			cmd := exec.Command(os.Args[0], "-test.run=^TestCommandWorkerProcess$", "-test.timeout=2m")
			cmd.Env = append(os.Environ(), "RUNNER_COMMAND_WORKER_SPEC="+spec)
			if err := cmd.Start(); err != nil {
				return err
			}
			go func() { _ = cmd.Wait(); finished() }()
			return nil
		}
	}
	rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script, hook)
	defer rig.close()
	old, err := rig.core.Start(runner.StartRequest{RequestID: "retained-seed", Project: "demo", Process: "saved"})
	if err != nil {
		t.Fatal(err)
	}
	old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
	run, err := rig.core.Start(runner.StartRequest{RequestID: "review-retained-seed", Project: "demo", Inputs: map[string]runner.Value{"patch": old.Outputs["patch"]}})
	if err != nil {
		t.Fatal(err)
	}
	end := waitGoWorkflow(t, rig.core, run.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) && r.ResourcesReleased })
	if end.Status != "Completed" || len(end.Attempts) != 3 || end.Process.MaxAttempts != 18 || script.starts != 1 || strings.Join(script.permissions, ",") != "runner-check" || !end.ResourcesReleased {
		t.Fatal("retained review required coding or lost cleanup", end.Status, end.Reason, len(end.Attempts), script.starts, script.permissions)
	}
	source := end.Attempts[2].Source
	if source == nil || source.Tree != tree || source.PatchID != end.Attempts[2].ID+"/changes.patch" || source.InputPatchID != old.Outputs["patch"].ArtifactID {
		t.Fatal("Core did not retain integration capture provenance", source)
	}
	if _, err := rig.store.Artifact(t.Context(), source.PatchID); err != nil {
		t.Fatal("new capture was not registered before scratch cleanup", err)
	}
	if _, err := os.Stat(filepath.Join(rig.disk.Root, "work", end.Attempts[2].ID)); !os.IsNotExist(err) {
		t.Fatal("integration capture scratch survived complete Core release", err)
	}
	if _, err := os.Stat(filepath.Join(rig.disk.Root, "work", run.ID, "source")); !os.IsNotExist(err) {
		t.Fatal("Core retained the completed source checkout", err)
	}
}
