//go:build darwin || linux

package execution

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Real Core/SQLite/workspaces/commands/artifacts/native contracts and Git CAS;
// source assertions, native judgments and remote provider effects are fixtures.
// This is one old frozen run, not adoption, a replacement run or a new PR.
func TestGovernedSourceRecoveryPreservesRunAndAdvancesExistingDraftWithin35(t *testing.T) {
	measure := evidenceBinary(t, "./cmd/runner-measure")
	provider := evidenceBinary(t, "./testdata/correction-recovery-provider")
	for _, preserve := range []bool{false, true} {
		name := "settled-checkout"
		if preserve {
			name = "preserved-legacy-partial-checkout"
		}
		t.Run(name, func(t *testing.T) { governedSourceRecoveryJourney(t, measure, provider, preserve) })
	}
}

func governedSourceRecoveryJourney(t *testing.T, measure, provider string, preserve bool) {
	t.Helper()
	repo := sourceRepository(t)
	for name, value := range map[string]string{"stage.txt": "good", "visit.txt": "pristine"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "recovery fixture")
	// Retain a real seed artifact in a separate model-free failed fixture. Only
	// its patch bytes are consumed; no judgment or release approval is adopted.
	if err := os.WriteFile(filepath.Join(repo, "visit.txt"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := fixtureGit(t, repo, "diff", "--binary", "--full-index", "HEAD", "--", "visit.txt")
	fixtureGit(t, repo, "checkout", "--", "visit.txt")
	approved := goodTurn()
	approved.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Current fixture proof independently reviewed"},"reason":""}`
	reviewCorrection := goodTurn()
	reviewCorrection.Items[0].Text = `{"outcome":"changes_requested","outputs":{"verdict":"BLOCKED","summary":"Add the final fixture regression marker"},"reason":"Missing fixture regression marker"}`
	script := &scriptedPeer{results: []nativeTurn{goodTurn(), approved, goodTurn(), goodTurn(), goodTurn(), goodTurn(), reviewCorrection, goodTurn(), approved}}
	script.onTurn = func(dir string) {
		i := script.starts
		checks := stagedEvidence(t, dir, "checks")
		if i == 3 || i == 4 || i == 5 {
			want := map[int]string{3: "qualification-repair", 4: "saved-exhausted", 5: "last-check-correction"}[i]
			if !bytes.Contains(checks, []byte(`"generation":"`+want+`"`)) || !bytes.Contains(checks, []byte("EXACT_SOURCE_FAILURE")) {
				t.Error("saved/fresh complete check feedback did not reach correction owner", i, string(checks))
			}
		}
		if preserve && i == 4 {
			if body, err := os.ReadFile(filepath.Join(dir, "seats.go")); err != nil || string(body) != seatsImplementation {
				t.Error("normal owner preparation did not reconstruct exact retained candidate after forensic preservation", err)
			}
		}
		if i == 1 || i == 6 || i == 8 {
			return
		} // Independent reviews do not edit.
		stage, visit := "good", "initial-owner"
		switch i {
		case 2:
			stage, visit = "bad", "qualification-repair"
		case 3:
			stage, visit = "bad", "saved-exhausted"
		case 4:
			stage, visit = "bad", "last-check-correction"
		case 5:
			visit = "after-governed-repair"
		case 7:
			visit = "review-corrected"
		}
		for name, value := range map[string]string{"seats.go": seatsImplementation, "stage.txt": stage, "visit.txt": visit} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
				t.Error(err)
			}
		}
	}
	rig, _, operator := evidenceRig(t, repo, "retained-frontend.json", measure, provider, script, func(p *runner.Project) {
		// Freeze the old one-use policy, reproducing the actually exhausted run.
		process := p.Processes[p.DefaultProcess]
		for i := range process.Steps {
			if route, ok := process.Steps[i].Routes["changes_requested"]; ok && route.MaxTimes > 0 {
				route.MaxTimes = 1
				process.Steps[i].Routes["changes_requested"] = route
			}
		}
		p.Processes[p.DefaultProcess] = process
		fault := ""
		if preserve {
			// Fault injection in disposable Runner-owned source simulates an
			// old cleanup interruption, without accepting the changed tree.
			fault = "\nif [ \"$generation\" = saved-exhausted ]; then rm seats.go; fi\n"
		}
		for _, name := range []string{"frontend-check", "frontend-source-check"} {
			if role, ok := p.Roles[name]; ok {
				role.Execution.Command = &runner.Command{Args: []string{"/bin/sh", "-c", `stage=$(cat stage.txt); generation=$(cat visit.txt); outcome=ready; status=passed
if [ "$stage" = bad ]; then outcome=changes_requested; status=failed; fi
printf '{"schema":2,"status":"%s","generation":"%s","vitest":{"assertions":[{"failureMessages":["EXACT_SOURCE_FAILURE: retained assertion fixture"]}]}}\n' "$status" "$generation" > "$TMPDIR/frontend-check.json"
printf '{"outcome":"%s","reason":"fixture source assertion"}\n' "$outcome" > "$TMPDIR/domain.json"
printf 'bounded assertion summary\n'` + fault}, Result: "domain.json", Artifact: "frontend-check.json"}
				p.Roles[name] = role
			}
		}
		p.Roles["retain"] = runner.Role{ID: "retain", Inputs: map[string]string{}, Outputs: map[string]string{"patch": "artifact"}, Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "patch", Text: string(patch)}}
		p.Processes["retain"] = runner.Process{ID: "retain", Inputs: map[string]string{}, Steps: []runner.Step{{ID: "retain", Role: "retain", Routes: map[string]runner.Route{"ready": {Next: "$fail"}}}}, MaxAttempts: 1, MaxAttemptsPerStep: 1, AttemptTimeout: "1m"}
	})
	defer rig.close()
	old, err := rig.core.Start(runner.StartRequest{RequestID: "retained-fixture-source", Project: "demo", Process: "retain", Inputs: map[string]runner.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) })
	inputs := map[string]runner.Value{"request": runner.Text(strings.Repeat("Bounded source correction acceptance. ", 55)), "package": runner.Text("."), "tests": runner.Text(`["fixture.test.ts"]`), "benchmark": runner.Text("none"), "patch": old.Attempts[0].End.Result.Outputs["patch"]}
	r, err := rig.core.Start(runner.StartRequest{RequestID: "governed-source-recovery", Project: "demo", Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	failed := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
		return r.Terminal() && allReleased(r) && (r.ResourcesReleased || r.CleanupBlocked)
	}, time.Minute)
	if failed.Status != "Failed" || !strings.Contains(failed.Reason, "repetition allowance exhausted") || failed.RouteUses["check/changes_requested"] != 1 || len(failed.Attempts) != 14 || script.starts != 4 {
		t.Fatal("fixture did not reach saved source-check exhaustion", failed.Status, failed.Reason, len(failed.Attempts), script.starts, failed.RouteUses)
	}
	current := failed.Attempts[len(failed.Attempts)-1]
	if current.End == nil || current.End.Kind != "returned" || current.End.Result.Outcome != "changes_requested" || failed.Process.Steps[current.Step].ID != "check" || current.Source == nil {
		t.Fatal("exhaustion lost the settled actual source failure")
	}
	var firstPub struct {
		RunID                string `json:"run_id"`
		Base, Head           string
		PR, Creates, Updates int
	}
	data, err := os.ReadFile(filepath.Join(operator, "publication.json"))
	if err != nil || json.Unmarshal(data, &firstPub) != nil || firstPub.Creates != 1 || firstPub.Updates != 0 {
		t.Fatal("initial draft not retained", string(data), err)
	}
	grant := runner.RecoverCorrectionsRequest{RequestID: "authorized-three-total", RunID: r.ID, AttemptID: current.ID, MaxUses: 3, PreserveSource: preserve}
	if preserve {
		withoutPreservation := grant
		withoutPreservation.RequestID = "no-preservation-grant"
		withoutPreservation.PreserveSource = false
		if !failed.CleanupBlocked || failed.ResourcesReleased {
			t.Fatal("fault did not preserve old partial-cleanup boundary")
		}
		if _, err = rig.core.RecoverCorrections(withoutPreservation); err == nil {
			t.Fatal("partial source was accepted without explicit preservation")
		}
	}
	if _, err = rig.core.RecoverCorrections(grant); err != nil {
		t.Fatal(err)
	}
	if _, err = rig.core.RecoverCorrections(grant); err != nil {
		t.Fatal("idempotent recovery replay", err)
	}
	waiting := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
		return r.Terminal() || r.Decision != nil || r.Status == "Waiting" && !r.Active()
	}, time.Minute)
	if waiting.Decision == nil || waiting.Decision.Step != "prepare" || waiting.CorrectionRecovery == nil || waiting.CorrectionRecovery.AttemptID != current.ID || waiting.CorrectionRecovery.MaxUses != 3 || waiting.RouteUses["check/changes_requested"] != 3 || waiting.RouteUses["review/changes_requested"] != 1 {
		calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
		t.Fatal("same-run governed repair did not reach fresh approval", waiting.Status, waiting.Reason, waiting.RouteUses, "step", waiting.Process.Steps[waiting.Step].ID, string(calls))
	}
	if !reflect.DeepEqual(waiting.Process, failed.Process) || !reflect.DeepEqual(waiting.Roles, failed.Roles) || !reflect.DeepEqual(waiting.Inputs, failed.Inputs) || waiting.Process.MaxAttempts != 35 || waiting.VisitExtraAttempts != 0 {
		t.Fatal("recovery mutated frozen definitions/inputs or global allowance")
	}
	if !reflect.DeepEqual(waiting.CorrectionRecovery.RouteLimits, map[string]int{"check/changes_requested": 3, "review/changes_requested": 3, "qualify/changes_requested": 3}) || waiting.CorrectionRecovery.Reason != failed.Reason || waiting.CorrectionRecovery.GrantedAt.IsZero() || waiting.CorrectionRecovery.PatchID != failed.StepInputs["patch"].ArtifactID || waiting.Process.Steps[waiting.Attempts[len(failed.Attempts)].Step].ID != "owner" {
		t.Fatal("audit lost exact candidate/edges or recovery repeated the failed check")
	}
	if preserve {
		proof := waiting.CorrectionRecovery.Preservation
		if proof == nil || len(proof.ManifestSHA256) != 64 {
			t.Fatal("preservation lacks audited manifest proof")
		}
		path := proof.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(rig.disk.Root, path)
		}
		manifest, readErr := os.ReadFile(filepath.Join(filepath.Dir(path), "manifest.json"))
		manifestSHA := sha256.Sum256(manifest)
		if readErr != nil || proof.ManifestSHA256 != hex.EncodeToString(manifestSHA[:]) {
			t.Fatal("forensic manifest differs from audited receipt", readErr)
		}
		if _, err = os.Lstat(filepath.Join(path, "seats.go")); !os.IsNotExist(err) {
			t.Fatal("forensic tree silently repaired missing source", err)
		}
		for name, want := range map[string]string{"stage.txt": "bad", "visit.txt": "saved-exhausted"} {
			body, readErr := os.ReadFile(filepath.Join(path, name))
			if readErr != nil || string(body) != want {
				t.Fatal("partial source evidence was changed or dropped", name, readErr)
			}
		}
		patch, readErr := rig.disk.Read("artifacts/"+failed.StepInputs["patch"].ArtifactID, rig.disk.Policy.MaxArtifact)
		if readErr != nil {
			t.Fatal(readErr)
		}
		sum := sha256.Sum256(patch)
		if proof.PatchSHA256 != hex.EncodeToString(sum[:]) {
			t.Fatal("forensic receipt does not bind original accepted candidate")
		}
	} else if waiting.CorrectionRecovery.Preservation != nil {
		t.Fatal("normal recovery invented a forensic preservation")
	}
	secondGrant := grant
	secondGrant.RequestID = "unapproved-second-grant"
	if _, err = rig.core.RecoverCorrections(secondGrant); err == nil {
		t.Fatal("second request granted another correction allowance")
	}
	for i, was := range failed.Attempts {
		now := waiting.Attempts[i]
		if was.ID != now.ID || !reflect.DeepEqual(was.End, now.End) || !reflect.DeepEqual(was.Source, now.Source) {
			t.Fatal("saved failed history was replaced", i)
		}
		if was.Step >= 3 && !now.Superseded {
			t.Fatal("stale source evidence was not invalidated", i)
		}
	}
	data, err = os.ReadFile(filepath.Join(operator, "publication.json"))
	var finalPub struct {
		RunID                string `json:"run_id"`
		Base, Head           string
		PR, Creates, Updates int
	}
	if err != nil || json.Unmarshal(data, &finalPub) != nil || finalPub.Creates != 1 || finalPub.Updates != 1 || finalPub.PR != firstPub.PR || finalPub.RunID != r.ID || finalPub.Head == firstPub.Head || finalPub.Base != firstPub.Base {
		t.Fatal("correction created a duplicate PR or lost exact head/base", string(data), err)
	}
	remoteHead := strings.Fields(string(fixtureGit(t, repo, "ls-remote", "--refs", "origin", candidateRef(r.ID))))
	if len(remoteHead) != 2 || remoteHead[0] != finalPub.Head {
		t.Fatal("real CAS did not advance owned branch to current reviewed head", remoteHead)
	}
	prepare := waiting.Attempts[len(waiting.Attempts)-1]
	if !bytes.Contains(prepare.End.Result.Outputs["receipt"].Inline, []byte(finalPub.Head)) || bytes.Contains(prepare.End.Result.Outputs["receipt"].Inline, []byte(firstPub.Head)) {
		t.Fatal("approval gate reused old-head qualification")
	}
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "publish\n") != 2 || strings.Count(string(calls), "qualify\n") != 2 || strings.Contains(string(calls), "merge\n") {
		t.Fatal("protection or qualification bypassed", string(calls))
	}
	if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "stale-approval", RunID: r.ID, DecisionID: "old-head-decision", Choice: "approve"}); err == nil {
		t.Fatal("changed head accepted stale approval identity")
	}
	for _, step := range []string{"prepare", "backup"} {
		w := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() || r.Decision != nil }, time.Minute)
		if w.Decision == nil || w.Decision.Step != step {
			t.Fatal("missing independent fixture approval", step, w.Status, w.Reason)
		}
		if _, err = rig.core.Decide(runner.DecisionRequest{RequestID: "fixture-approve-" + step, RunID: r.ID, DecisionID: w.Decision.ID, Choice: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool { return r.Terminal() && allReleased(r) && r.ResourcesReleased }, time.Minute)
	if end.Status != "Completed" || len(end.Attempts) != 35 || script.starts != 9 {
		t.Fatal("complete governed path must fit the actual unchanged 35-attempt budget", end.Status, end.Reason, len(end.Attempts), script.starts)
	}
	if !reflect.DeepEqual(end.CorrectionRecovery, waiting.CorrectionRecovery) {
		t.Fatal("preservation/audit receipt changed during ordinary delivery")
	}
	if preserve {
		path := end.CorrectionRecovery.Preservation.Path
		body, readErr := os.ReadFile(filepath.Join(path, "stage.txt"))
		if readErr != nil || string(body) != "bad" {
			t.Fatal("final cleanup changed preserved source evidence", readErr)
		}
		if _, err = os.Lstat(filepath.Join(path, "seats.go")); !os.IsNotExist(err) {
			t.Fatal("final cleanup changed partial forensic tree", err)
		}
	}
	compact, _, err := runner.MarshalRunRecord(end)
	if err != nil || len(compact) > 40<<10 {
		t.Fatal("35 attempts plus audit exceed pilot record cap", len(compact), err)
	}
	t.Logf("same run complete: %d/35 attempts, %d/%d compact history bytes, one PR with one CAS advancement", len(end.Attempts), len(compact), 40<<10)
}
