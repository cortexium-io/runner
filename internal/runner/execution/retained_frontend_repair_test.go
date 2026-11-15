//go:build darwin || linux

package execution

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Only the new retained-input/owner/check/review prefix is exercised. The
// unchanged protected release suffix has existing complete workflow proof.
func TestWorkflowRetainedFrontendRepairStartsWithOwnerAndKeepsCheckGate(t *testing.T) {
	provider := evidenceBinary(t, "./testdata/release-operator")
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "review-correction", true: "blocked-check"}[blocked], func(t *testing.T) {
			repo := sourceRepository(t)
			file := filepath.Join(repo, "stage.txt")
			if err := os.WriteFile(file, []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			fixtureGit(t, repo, "add", "--all")
			fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "repair base")
			if err := os.WriteFile(file, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			patch := fixtureGit(t, repo, "diff", "--binary", "--full-index", "HEAD", "--", "stage.txt")
			fixtureGit(t, repo, "checkout", "--", "stage.txt")
			baseline := `{"status":"blocked","cause":"retained unhandled test rejection","assertionsPassed":true}`
			reject := goodTurn()
			reject.Items[0].Text = `{"outcome":"changes_requested","outputs":{"verdict":"BLOCKED","summary":"Add the missing focused regression"},"reason":""}`
			approve := goodTurn()
			approve.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Repaired source and current checks reviewed"},"reason":""}`
			script := &scriptedPeer{results: []nativeTurn{goodTurn(), reject, goodTurn(), approve}}
			script.onTurn = func(dir string) {
				report := stagedEvidence(t, dir, "checks")
				if script.starts == 0 {
					data, err := os.ReadFile(filepath.Join(dir, "stage.txt"))
					if err != nil || string(data) != "retained" || !bytes.Equal(report, []byte(baseline)) {
						t.Error("first owner lost retained source or actual failed diagnostic", string(data), string(report), err)
					}
				} else if !bytes.Contains(report, []byte(`"status":"passed"`)) {
					t.Error("correction/reviewer lost the current full check artifact", string(report))
				}
				if script.starts%2 == 0 {
					if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte("repaired"), 0600); err != nil {
						t.Error(err)
					}
				}
			}
			rig, _, operator := evidenceRig(t, repo, "retained-frontend-repair.json", "", provider, script, func(p *runner.Project) {
				p.Roles["retain-patch"] = runner.Role{ID: "retain-patch", Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(patch)}}
				p.Roles["retain-report"] = runner.Role{ID: "retain-report", Outputs: map[string]string{"artifact": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "artifact", Text: baseline}}
				p.Processes["retain"] = runner.Process{ID: "retain", Steps: []runner.Step{{ID: "patch", Role: "retain-patch"}, {ID: "report", Role: "retain-report", Routes: map[string]runner.Route{"ready": {Next: "$fail"}}}}, MaxAttempts: 2, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
				outcome := "ready"
				if blocked {
					outcome = "blocked"
				}
				role := p.Roles["frontend-check"]
				role.Execution.Command = &runner.Command{Args: []string{"/bin/sh", "-c", `test "$(cat stage.txt)" = repaired || exit 2
printf '{"status":"passed","full":"current candidate assertion proof"}' > "$TMPDIR/check.json"
printf '{"outcome":"` + outcome + `","reason":"fixture current candidate check"}' > "$TMPDIR/domain.json"`}, Result: "domain.json", Artifact: "check.json"}
				p.Roles[role.ID] = role
			})
			defer rig.close()
			old, err := rig.core.Start(runner.StartRequest{RequestID: "retain", Project: "demo", Process: "retain", Inputs: map[string]runner.Value{}})
			if err != nil {
				t.Fatal(err)
			}
			old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
			r, err := rig.core.Start(runner.StartRequest{RequestID: "repair", Project: "demo", Inputs: map[string]runner.Value{"request": runner.Text("Repair the retained frontend fixture; preserve all protected gates."), "tests": runner.Text(`["fixture.test.ts"]`), "patch": old.Attempts[0].End.Result.Outputs["patch"], "baseline_checks": old.Attempts[1].End.Result.Outputs["artifact"]}})
			if err != nil {
				t.Fatal(err)
			}
			end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() || r.Decision != nil }, time.Minute)
			calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
			if blocked {
				if end.Status != "Failed" || script.starts != 1 || strings.Contains(string(calls), "publish\n") {
					t.Fatal("blocked check reached review/publication", end.Status, end.Reason, script.starts, string(calls))
				}
				return
			}
			if end.Decision == nil || end.Decision.Step != "prepare" || script.starts != 4 || strings.Count(string(calls), "publish\n") != 1 || strings.Contains(string(calls), "merge\n") {
				t.Fatal("repair/review loop or protected merge gate failed", end.Status, end.Reason, script.starts, string(calls))
			}
		})
	}
}
