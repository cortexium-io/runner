//go:build darwin || linux

package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

func TestRetainedInvestigationRepairsCollectionAndPreservesOriginalPRAndEvidence(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	repo := sourceRepository(t)
	benchmark := `package seats
import("encoding/json";"os";"path/filepath";"strconv";"testing")
func BenchmarkRetainedEvidence(b *testing.B){
 stage,e:=os.ReadFile("stage.txt");if e!=nil{b.Fatal(e)}
 round,e:=strconv.Atoi(os.Getenv("RUNNER_MEASURE_ROUND"));if e!=nil||round<1{b.Fatal("missing round")}
 identity,sha,method:=os.Getenv("RUNNER_MEASURE_SOURCE_IDENTITY"),os.Getenv("RUNNER_MEASURE_SOURCE_SHA256"),os.Getenv("RUNNER_MEASURE_SOURCE_DIGEST_METHOD")
 if identity==""||len(sha)!=64||method==""{b.Fatal("missing independently bound source")}
 if string(stage)!="missing"{
  data,e:=json.Marshal(map[string]any{"stage":string(stage),"round":round,"identity":identity,"sha256":sha,"digestMethod":method});if e!=nil{b.Fatal(e)}
  if e=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"samples.json"),data,0600);e!=nil{b.Fatal(e)}
 }
 b.ReportMetric(1,"samples/op")
}
`
	for name, value := range map[string]string{"seats.go": seatsImplementation, "stage.txt": "main", "retained_test.go": benchmark} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "retained main")
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "stage.txt"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "stage.txt")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "original published candidate")
	published := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	if err := os.WriteFile(filepath.Join(repo, "stage.txt"), []byte("missing"), 0600); err != nil {
		t.Fatal(err)
	}
	patch := fixtureGit(t, repo, "diff", "--binary", "--full-index", base, "--", "stage.txt")
	fixtureGit(t, repo, "reset", "--hard", base)
	baseline := []byte(`{"baseline":true,"controls":["original-six"],"uncertainty":"historical scope remains explicit"}`)
	manifest := []byte(`{"schema":1,"status":"ready","accepted":["kept-a","kept-b"],"fixture":true}`)
	approve := goodTurn()
	approve.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Original evidence, exact source-bound before/current and retained PR authority checked"},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{goodTurn(), goodTurn(), approve}}
	var before []byte
	check := func(data []byte, stage string, round int, incomplete bool) {
		t.Helper()
		var report struct {
			Round            int                                             `json:"round"`
			Failed           bool                                            `json:"failed"`
			CollectionFailed bool                                            `json:"collection_failed"`
			SourceVerified   bool                                            `json:"source_verified"`
			Source           struct{ Identity, SHA256, DigestMethod string } `json:"source"`
			Evidence         map[string]struct {
				Stage                          string
				Round                          int
				Identity, SHA256, DigestMethod string
			} `json:"evidence"`
		}
		if err := json.Unmarshal(data, &report); err != nil || report.Round != round || report.Failed != incomplete || report.CollectionFailed != incomplete || !report.SourceVerified || len(report.Source.SHA256) != 64 {
			t.Error("incomplete or unbound measurement was relabelled", report, err)
			return
		}
		if incomplete {
			if len(report.Evidence) != 0 {
				t.Error("invented sidecars", report)
			}
			return
		}
		sample := report.Evidence["samples.json"]
		if sample.Stage != stage || sample.Round != round || sample.Identity != report.Source.Identity || sample.SHA256 != report.Source.SHA256 || sample.DigestMethod != report.Source.DigestMethod {
			t.Error("child source/round differs from authoritative outer report", sample, report.Source)
		}
	}
	script.onTurn = func(dir string) {
		if script.starts < 2 && !bytes.Equal(stagedEvidence(t, dir, "baseline"), baseline) {
			t.Error("owner lost original baseline")
		}
		if !bytes.Equal(stagedEvidence(t, dir, "manifest"), manifest) {
			t.Error("accepted manifest regenerated")
		}
		if files, _ := filepath.Glob(filepath.Join(dir, ".runner-tmp", "inputs", "*", "publication")); len(files) != 0 {
			t.Error("opaque publication authority entered native inputs")
		}
		switch script.starts {
		case 0:
			check(stagedEvidence(t, dir, "measurement"), "", 1, true)
			if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte("before"), 0600); err != nil {
				t.Error(err)
			}
		case 1:
			before = bytes.Clone(stagedEvidence(t, dir, "measurement"))
			check(before, "before", 1, false)
			if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte("after"), 0600); err != nil {
				t.Error(err)
			}
		case 2:
			if !bytes.Equal(stagedEvidence(t, dir, "baseline_measurements"), baseline) || !bytes.Equal(stagedEvidence(t, dir, "before_measurements"), before) {
				t.Error("review lost original or first complete evidence")
			}
			check(stagedEvidence(t, dir, "measurements"), "after", 2, false)
		default:
			t.Error("unexpected model visit", script.starts)
		}
	}
	rig, process, operator := evidenceRig(t, repo, "retained-investigation.json", measure, provider, script, func(p *runner.Project) {
		p.Source.Ref = base
		texts := []string{string(patch), string(baseline), string(manifest)}
		steps := make([]runner.Step, 3)
		for i, name := range []string{"saved-patch", "saved-baseline", "saved-manifest"} {
			p.Roles[name] = runner.Role{
				ID: name, Purpose: "Retain owned fixture source/evidence.",
				Inputs: map[string]string{}, Outputs: map[string]string{"artifact": "artifact"},
				Execution: &runner.Profile{Harness: "deterministic", Mode: "file", Output: "artifact", Text: texts[i]},
			}
			steps[i] = runner.Step{ID: name, Role: name}
		}
		steps[2].Routes = map[string]runner.Route{"ready": {Next: "$fail"}}
		p.Processes["saved"] = runner.Process{
			ID: "saved", Inputs: map[string]string{}, Steps: steps,
			MaxAttempts: 3, MaxAttemptsPerStep: 1, AttemptTimeout: "1m",
		}
	}, 16<<20)
	defer rig.close()
	old, err := rig.core.Start(runner.StartRequest{RequestID: "save-retained", Project: "demo", Process: "saved", Inputs: map[string]runner.Value{}})
	if err != nil {
		t.Fatal(err)
	}
	old = waitSourceRun(t, rig.core, old.ID, func(r runner.Run) bool { return allReleased(r) && r.ResourcesReleased })
	if old.Status != "Failed" {
		t.Fatal("failed history fixture not retained", old.Status)
	}
	fixtureGit(t, repo, "push", "origin", published+":"+candidateRef(old.ID))
	q := release.Request{Project: "demo", RunID: old.ID, Key: "original-publication", Action: "publish", Source: base, Candidate: published}
	cmd := exec.Command(provider)
	cmd.Stdin = bytes.NewReader(mustJSON(t, q))
	raw, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var response release.Response
	if json.Unmarshal(raw, &response) != nil || response.State != "finished" || response.Failed {
		t.Fatal("original publication fixture failed", string(raw))
	}
	inputs := map[string]runner.Value{
		"request":           runner.Text("Continue the retained fixture without planning/cards. Correct collection first, retain complete before/current evidence, and stop at protected merge."),
		"package":           runner.Text("."),
		"tests":             runner.Text("^TestParseSeats$"),
		"benchmark":         runner.Text("^BenchmarkRetainedEvidence$"),
		"patch":             old.Attempts[0].End.Result.Outputs["artifact"],
		"original_baseline": old.Attempts[1].End.Result.Outputs["artifact"],
		"manifest":          old.Attempts[2].End.Result.Outputs["artifact"],
		"publication":       {Inline: response.Receipt},
	}
	r, err := rig.core.Start(runner.StartRequest{RequestID: "complete-retained", Project: "demo", Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	end := waitGoWorkflow(t, rig.core, r.ID, func(r runner.Run) bool {
		if r.Terminal() {
			return true
		}
		if r.Decision == nil {
			return false
		}
		for _, a := range r.Attempts {
			if !a.Released {
				return false
			}
		}
		return true
	})
	if end.Status != "Waiting" || end.Decision == nil || end.Decision.Step != "prepare" || end.Decision.Next != "merge" || script.starts != 3 || end.Process.MaxAttempts != 35 || process.MaxAttempts != 35 {
		t.Fatal("retained workflow failed or changed authority", end.Status, end.Reason, end.Decision, script.starts)
	}
	counts := map[string]int{}
	for _, a := range end.Attempts {
		id := end.Process.Steps[a.Step].ID
		counts[id]++
		if id == "retained-measure" && a.End.Result.Outcome != "changes_requested" {
			t.Fatal("incomplete first report did not use collector repair", a.End)
		}
		if id == "retain-measurement" {
			data, err := rig.disk.Read("artifacts/"+a.End.Result.Outputs["artifact"].ArtifactID, rig.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(data, before) || a.Superseded {
				t.Fatal("first failed report became before evidence", err)
			}
		}
	}
	if counts["owner"] != 2 || counts["retained-measure"] != 1 || counts["measure"] != 2 || counts["retain-measurement"] != 1 || counts["review"] != 1 {
		t.Fatal("retained work or measurement round repeated", counts)
	}
	finalOld, _ := rig.core.Get(old.ID)
	if !reflect.DeepEqual(old, finalOld) {
		t.Fatal("continuation rewrote failed source/evidence history")
	}
	for _, a := range end.Attempts {
		if end.Process.Steps[a.Step].ID == "publish" {
			var receipt release.Receipt
			if json.Unmarshal(a.End.Result.Outputs["receipt"].Inline, &receipt) != nil || receipt.PR != 1 || receipt.PublicationRunID != old.ID {
				t.Fatal("publication changed its original PR owner", receipt)
			}
		}
	}
	refs := strings.Fields(string(fixtureGit(t, repo, "ls-remote", "origin", "refs/heads/codex/runner/*")))
	if len(refs) != 2 || refs[1] != candidateRef(old.ID) {
		t.Fatal("continuation created a duplicate PR ref", refs)
	}
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "adopt\n") != 1 || strings.Count(string(calls), "publish\n") != 2 || strings.Contains(string(calls), "merge\n") || strings.Contains(string(calls), "stop\n") {
		t.Fatal("retained handoff bypassed protected approval or duplicated publication", string(calls))
	}
}
