package execution

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// One process proof owns coding -> check -> read-only review -> candidate
// integration. A rejected review must not publish a candidate or redo checks.
func TestReviewedCandidateProcess(t *testing.T) {
	for _, tc := range []struct {
		name, verdict string
		checkedOut    bool
		tasks         bool
	}{
		{name: "approved", verdict: "APPROVE"},
		{name: "checked out by user", verdict: "APPROVE", checkedOut: true},
		{name: "blocked", verdict: "BLOCKED"},
		{name: "dependent tasks", verdict: "APPROVE", tasks: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict := tc.verdict
			repo := sourceRepository(t)
			cfg := workspaceSnapshot(t, repo)
			project := cfg.Projects["demo"]
			const shared = "Stable general guidance, then shared project knowledge. "
			coding := project.Roles["implement"]
			coding.Execution.Instructions = shared + "Implement this task."
			project.Roles["implement"] = coding
			reviewer := runner.Clone(project.Roles["implement"])
			reviewer.ID = "review"
			reviewer.Purpose = "Review the original request and retained patch. Return APPROVE or BLOCKED."
			reviewer.Inputs = map[string]string{"patch": "artifact", "check": "text"}
			reviewer.Outputs = map[string]string{"verdict": "text", "summary": "text"}
			reviewer.Execution.Access = "workspace-read"
			reviewer.Execution.Instructions = shared + "Review this task."
			project.Roles["review"] = reviewer
			project.Roles["integrate"] = runner.Role{
				ID:        "integrate",
				Purpose:   "Retain an approved candidate on its own local branch.",
				Inputs:    map[string]string{"patch": "artifact", "verdict": "text", "review": "text"},
				Outputs:   map[string]string{"candidate": "text"},
				Execution: &runner.Profile{Harness: "git-candidate", Access: "workspace-read"},
			}
			process := project.Processes["seats"]
			process.Steps = append(process.Steps,
				runner.Step{
					ID: "review", Role: "review",
					Inputs: map[string]runner.Binding{
						"patch": {FromStep: "implement", Output: "patch"},
						"check": {FromStep: "check", Output: "check"},
					},
				},
				runner.Step{
					ID: "integrate", Role: "integrate",
					Inputs: map[string]runner.Binding{
						"patch":   {FromStep: "implement", Output: "patch"},
						"verdict": {FromStep: "review", Output: "verdict"},
						"review":  {FromStep: "review", Output: "summary"},
					},
				},
			)
			expectedSource := seatsImplementation
			if tc.tasks {
				project.Roles["plan"] = runner.Role{ID: "plan", Outputs: map[string]string{"result": "text"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "echo", Output: "result", Text: `[{"id":"second","depends_on":["first"],"inputs":{}},{"id":"first","inputs":{}}]`}}
				group := runner.Step{ID: "tasks", ForEach: &runner.TaskGroup{Items: runner.Binding{FromStep: "plan", Output: "result"}, Inputs: map[string]string{}, Steps: []runner.Step{{ID: "implement", Role: "implement"}}, Output: runner.Binding{FromStep: "implement", Output: "patch"}}}
				process.Steps = append([]runner.Step{{ID: "plan", Role: "plan"}, group}, process.Steps[1:]...)
				for i := range process.Steps {
					if process.Steps[i].Inputs != nil {
						if _, ok := process.Steps[i].Inputs["patch"]; ok {
							process.Steps[i].Inputs["patch"] = runner.Binding{FromCandidate: true}
						}
					}
				}
				expectedSource = strings.ReplaceAll(seatsImplementation, "n<=0", "!positive(n)")
				process.Outputs["patch"] = runner.Binding{FromCandidate: true}
			}
			process.MaxAttempts = 7
			process.Outputs["candidate"] = runner.Binding{FromStep: "integrate", Output: "candidate"}
			project.Processes["seats"] = process
			cfg.Projects["demo"] = project
			result, _ := json.Marshal(map[string]any{"outcome": "ready", "outputs": map[string]string{"verdict": verdict, "summary": "Concrete review finding"}, "reason": ""})
			review := goodTurn()
			review.ID = "native-review"
			review.Items[0].Text = string(result)
			script := &scriptedPeer{results: []nativeTurn{goodTurn(), review}}
			if tc.tasks {
				script.results = []nativeTurn{goodTurn(), goodTurn(), review}
			}
			script.onTurn = func(dir string) {
				if tc.tasks && script.starts == 0 {
					if err := os.WriteFile(filepath.Join(dir, "positive.go"), []byte("package seats\nfunc positive(n int)bool{return n>0}\n"), 0600); err != nil {
						t.Error(err)
					}
				} else if script.starts == 0 || tc.tasks && script.starts == 1 {
					if err := os.WriteFile(filepath.Join(dir, "seats.go"), []byte(expectedSource), 0600); err != nil {
						t.Error(err)
					}
				} else {
					files, err := filepath.Glob(filepath.Join(dir, ".runner-tmp", "*", "inputs", "patch"))
					if err != nil || len(files) != 1 {
						t.Errorf("review lacks its retained patch: %v, %v", files, err)
					}
				}
			}
			// Neither branch movement nor uncommitted user work may be overwritten.
			os.WriteFile(filepath.Join(repo, "doc.go"), []byte("user work"), 0600)
			before := string(fixtureGit(t, repo, "status", "--porcelain"))
			rig := openNativeRig(t, filepath.Join(t.TempDir(), "state"), cfg, script)
			defer rig.close()
			r, err := rig.core.Start(runner.StartRequest{RequestID: "review", Project: "demo", Inputs: map[string]runner.Value{}})
			if err != nil {
				t.Fatal(err)
			}
			done := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
			expectedAttempts, expectedTurns, expectedPermissions := 4, 2, "runner-edit,runner-check"
			if tc.tasks {
				expectedAttempts, expectedTurns, expectedPermissions = 7, 3, "runner-edit,runner-edit,runner-check"
			}
			if len(done.Attempts) != expectedAttempts || script.starts != expectedTurns || script.checks != 1 || strings.Join(script.permissions, ",") != expectedPermissions {
				t.Fatalf("unexpected dispatch: %d attempts, %d turns, %d checks, %v", len(done.Attempts), script.starts, script.checks, script.permissions)
			}
			for _, instructions := range script.instructions {
				if !strings.HasPrefix(instructions, shared) {
					t.Fatal("role/runtime guidance precedes reusable shared context")
				}
			}
			ref := "refs/heads/codex/runner/" + r.ID
			if verdict == "APPROVE" {
				if done.Status != "Completed" || !strings.Contains(string(done.Outputs["candidate"].Inline), ref) {
					t.Fatalf("approved work not integrated: %s %s", done.Status, done.Reason)
				}
				if got := string(fixtureGit(t, repo, "show", ref+":seats.go")); got != expectedSource {
					t.Fatal("candidate branch does not contain reviewed source")
				}
			} else if done.Status != "Failed" || !strings.Contains(done.Reason, "APPROVE") {
				t.Fatalf("blocked review did not stop: %s %s", done.Status, done.Reason)
			} else if _, err := git(context.Background(), repo, nil, nil, 256, "rev-parse", "--verify", ref); err == nil {
				t.Fatal("blocked candidate published a branch")
			}
			if got := string(fixtureGit(t, repo, "status", "--porcelain")); got != before {
				t.Fatal("integration changed the user's checkout/index")
			}
			if verdict == "APPROVE" {
				if tc.tasks && !strings.Contains(string(fixtureGit(t, repo, "show", ref+":positive.go")), "func positive") {
					t.Fatal("predecessor source lost")
				}
				if tc.checkedOut {
					fixtureGit(t, repo, "checkout", strings.TrimPrefix(ref, "refs/heads/"))
				}
				if err := rig.service.ForgetRun(context.Background(), r.ID); err != nil {
					t.Fatal(err)
				}
				if missingCandidateRef(context.Background(), repo, ref) == tc.checkedOut {
					t.Fatal("retention must remove unused owned branches and preserve checked-out user branches")
				}
			} else if !strings.Contains(done.Reason, "Concrete review finding") {
				t.Fatal("blocked review detail was hidden from the coordinator")
			}
		})
	}
}

// Replaying saved integration intent after losing its result reuses the commit;
// an existing branch containing other work is never moved or overwritten.
func TestCandidateIntegrationRecoveryAndConflict(t *testing.T) {
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, sourceRepository(t), "attempt_candidate")
	a.Deadline = time.Now().UTC().Add(time.Minute)
	r, err := w.Prepare(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation), 0600)
	source, err := w.Capture(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	a.Setup.Profile = runner.Profile{Harness: "git-candidate", Access: "workspace-read"}
	a.Inputs = map[string]runner.Value{"patch": {ArtifactID: source.PatchID}, "verdict": runner.Text("APPROVE")}
	c := &Candidates{disk: d, workspaces: w}
	first, err := c.Start(context.Background(), a)
	if err != nil || first.End == nil || first.End.Kind != "returned" {
		t.Fatalf("integration failed: %+v, %v", first, err)
	}
	if err = c.save(candidateJob{Assignment: a}); err != nil { // Lost result after durable Git ref.
		t.Fatal(err)
	}
	replayed, err := (&Candidates{disk: d, workspaces: &Workspaces{disk: d}}).Observe(context.Background(), a.AttemptID)
	if err != nil || string(replayed.End.Result.Outputs["candidate"].Inline) != string(first.End.Result.Outputs["candidate"].Inline) {
		t.Fatal("integration replay changed its candidate", err)
	}
	var original string
	json.Unmarshal(first.End.Result.Outputs["candidate"].Inline, &original)
	oldHash := strings.Split(original, "\n")[1]
	if err = os.WriteFile(filepath.Join(r.WorkDir, "seats.go"), []byte(seatsImplementation+"\n// Qualified correction.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	owner := a
	owner.AttemptID = "attempt_correction_owner"
	owner.Setup.Profile = runner.Profile{Harness: "codex", Access: "workspace-write"}
	corrected, err := w.Capture(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	a.AttemptID = "attempt_corrected_candidate"
	a.Deadline = a.Deadline.Add(time.Second)
	a.Inputs["patch"] = runner.Value{ArtifactID: corrected.PatchID}
	next, err := c.Start(t.Context(), a)
	if err != nil || next.End.Kind != "returned" {
		t.Fatal("owned correction did not advance", err, next)
	}
	if got := string(fixtureGit(t, r.Repository, "show", candidateRef(a.RunID)+":seats.go")); !strings.Contains(got, "Qualified correction") {
		t.Fatal("corrected source was not integrated")
	}
	if got := strings.TrimSpace(string(fixtureGit(t, r.Repository, "rev-parse", "refs/runner/candidates/"+a.RunID+"/attempt_candidate"))); got != oldHash {
		t.Fatal("earlier candidate history was lost")
	}
	ref := "refs/heads/codex/runner/" + a.RunID
	fixtureGit(t, r.Repository, "update-ref", ref, r.Base)
	c.save(candidateJob{Assignment: a})
	blocked, err := c.Observe(context.Background(), a.AttemptID)
	if err != nil || blocked.End.Kind != "failed" || !strings.Contains(blocked.End.Reason, "different work") {
		t.Fatalf("branch conflict was not surfaced: %+v %v", blocked, err)
	}
	if got := strings.TrimSpace(string(fixtureGit(t, r.Repository, "rev-parse", ref))); got != r.Base {
		t.Fatal("existing branch was overwritten")
	}
	if err := w.ForgetRun(context.Background(), a.RunID); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(fixtureGit(t, r.Repository, "rev-parse", ref))); got != r.Base {
		t.Fatal("retention deleted a branch advanced by its user")
	}
}

func TestCandidateCancellationBeforeBranchCreation(t *testing.T) {
	d := workspaceDisk(t)
	w := &Workspaces{disk: d}
	a := sourceAssignment(d, sourceRepository(t), "attempt_cancel")
	if _, err := w.Prepare(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	c := &Candidates{disk: d, workspaces: w}
	if err := c.save(candidateJob{Assignment: a}); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(context.Background(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	o, err := c.Observe(context.Background(), a.AttemptID)
	if err != nil || o.End.Kind != "stopped" || !missingCandidateRef(context.Background(), a.Repository, candidateRef(a.RunID)) {
		t.Fatalf("cancellation created a candidate: %+v, %v", o, err)
	}
}
