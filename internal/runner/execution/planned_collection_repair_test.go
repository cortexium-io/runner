//go:build darwin || linux

package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestPlannedCollectionRepairRetainsFirstCompleteMeasurementAndStopsAtMerge(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	repo := sourceRepository(t)
	benchmark := `package seats
import("encoding/json";"os";"path/filepath";"strings";"testing")
func BenchmarkFixtureCollection(b *testing.B){
 stage,err:=os.ReadFile("stage.txt");if err!=nil{b.Fatal(err)}
 data,err:=json.Marshal(map[string]any{"stage":string(stage),"samples":[]int{15,25,30}});if err!=nil{b.Fatal(err)}
 if err=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"samples.json"),data,0600);err!=nil{b.Fatal(err)}
 if string(stage)=="noisy"{b.Log(strings.Repeat("verbose source trace ",20000))}
 b.ReportMetric(3,"samples/op")
}

`
	for name, data := range map[string]string{"seats.go": seatsImplementation, "stage.txt": "pristine", "collection_test.go": benchmark} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "collection repair fixture")
	tasks := `[{"id":"prepare","inputs":{"task":"Prepare the fixture collector without changing its source contract.","paths":["stage.txt"]}}]`
	plan, approve := goodTurn(), goodTurn()
	plan.Items[0].Text = string(mustJSON(t, map[string]any{"outcome": "ready", "outputs": map[string]string{"tasks": tasks}, "reason": ""}))
	approve.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Failed collection kept separate from complete before/current evidence"},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{plan, goodTurn(), goodTurn(), goodTurn(), approve}}
	var original, before []byte
	check := func(data []byte, stage string, round int, incomplete bool) {
		t.Helper()
		var report struct {
			Round            int  `json:"round"`
			Failed           bool `json:"failed"`
			CollectionFailed bool `json:"collection_failed"`
			OutputComplete   bool `json:"output_complete"`
			Evidence         map[string]struct {
				Stage   string `json:"stage"`
				Samples []int  `json:"samples"`
			} `json:"evidence"`
		}
		if err := json.Unmarshal(data, &report); err != nil || report.Round != round || report.Failed != incomplete || report.CollectionFailed != incomplete || report.OutputComplete == incomplete || report.Evidence["samples.json"].Stage != stage || len(report.Evidence["samples.json"].Samples) != 3 {
			t.Error("collection failure or complete measurement was relabelled", stage, report, err)
		}
	}
	writeStage := func(dir, stage string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte(stage), 0600); err != nil {
			t.Error(err)
		}
	}
	script.onTurn = func(dir string) {
		switch script.starts {
		case 0:
			original = bytes.Clone(stagedEvidence(t, dir, "baseline"))
			check(original, "pristine", 1, false)
		case 1:
			writeStage(dir, "noisy")
		case 2:
			check(stagedEvidence(t, dir, "measurement"), "noisy", 1, true)
			writeStage(dir, "before")
		case 3:
			before = bytes.Clone(stagedEvidence(t, dir, "measurement"))
			check(before, "before", 1, false)
			writeStage(dir, "after")
		case 4:
			if !bytes.Equal(stagedEvidence(t, dir, "baseline_measurements"), original) || !bytes.Equal(stagedEvidence(t, dir, "before_measurements"), before) {
				t.Error("review lost the pristine or first complete measurement")
			}
			check(stagedEvidence(t, dir, "before_measurements"), "before", 1, false)
			entries := measurementHistoryReports(t, stagedEvidence(t, dir, "measurements"))
			if len(entries) != 2 || !bytes.Equal(entries[0], before) {
				t.Fatal("history lost the first complete report or retained failed collection")
			}
			check(entries[0], "before", 1, false)
			check(entries[1], "after", 2, false)
		default:
			t.Error("unexpected model-protocol visit", script.starts)
		}
	}
	// Use the pilot's existing artifact bound: the retained 256 KiB console
	// prefix plus structured evidence cannot fit the small default fixture cap.
	rig, process, operator := evidenceRig(t, repo, "planned-investigation.json", measure, provider, script, func(*runner.Project) {}, 16<<20)
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{RequestID: "collection-repair", Project: "demo", Inputs: map[string]runner.Value{
		"request": runner.Text("Repair collection first, then preserve complete before/current measurements and all protected decisions."), "package": runner.Text("."), "tests": runner.Text("^TestParseSeats$"), "benchmark": runner.Text("^BenchmarkFixtureCollection$"),
	}})
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
	if end.Status != "Waiting" || end.Decision == nil || end.Decision.Step != "prepare" || end.Decision.Next != "merge" || script.starts != 5 || process.MaxAttempts != 64 || end.Process.MaxAttempts != 64 {
		t.Fatal("complete collection-repair workflow stopped or changed authority", end.Status, end.Reason, end.Decision, script.starts)
	}
	counts := map[string]int{}
	for _, a := range end.Attempts {
		id := end.Process.Steps[a.Step].ID
		counts[id]++
		if id == "measure" && counts[id] == 1 {
			if a.End == nil || a.End.Kind != "returned" || a.End.Result == nil || a.End.Result.Outcome != "changes_requested" {
				t.Fatal("collection error bypassed configured correction", a.End)
			}
		}
		if id == "retain-measurement" {
			data, err := rig.disk.Read("artifacts/"+a.End.Result.Outputs["artifact"].ArtifactID, rig.disk.Policy.MaxArtifact)
			if err != nil || !bytes.Equal(data, before) || a.Superseded {
				t.Fatal("failed collection replaced the complete before artifact", err)
			}
		}
	}
	if counts["baseline"] != 1 || counts["plan"] != 1 || counts["cards"] != 1 || counts["measure"] != 3 || counts["retain-measurement"] != 1 || counts["measurement-history"] != 1 || counts["owner"] != 2 || counts["review"] != 1 {
		t.Fatal("collection repair repeated accepted work or omitted proof", counts)
	}
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "publish\n") != 1 || strings.Contains(string(calls), "merge\n") || strings.Contains(string(calls), "stop\n") {
		t.Fatal("collection repair bypassed a protected approval", string(calls))
	}
}

func TestPlannedBaselineOverflowRetainsBlockedDiagnosticBeforeModelWork(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	repo := sourceRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "seats.go"), []byte(seatsImplementation), 0600); err != nil {
		t.Fatal(err)
	}
	fixture := `package seats
import("os";"path/filepath";"strings";"testing")
func BenchmarkBaselineOverflow(b *testing.B){
 if err:=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"samples.json"),[]byte("{}"),0600);err!=nil{b.Fatal(err)}
 b.Log(strings.Repeat("bounded baseline diagnostic ",12000))
}
`
	if err := os.WriteFile(filepath.Join(repo, "overflow_test.go"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "baseline collection fixture")
	script := &scriptedPeer{}
	rig, _, _ := evidenceRig(t, repo, "planned-investigation.json", measure, provider, script, func(*runner.Project) {}, 16<<20)
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{RequestID: "blocked-baseline", Project: "demo", Inputs: map[string]runner.Value{
		"request": runner.Text("Preserve failed baseline evidence without a model or performance claim."), "package": runner.Text("."), "tests": runner.Text("^TestParseSeats$"), "benchmark": runner.Text("^BenchmarkBaselineOverflow$"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	end := waitGoWorkflow(t, rig.core, r.ID, func(r runner.Run) bool { return allReleased(r) })
	if end.Status != "Failed" || len(end.Attempts) != 1 || script.starts != 0 || end.Attempts[0].End == nil || end.Attempts[0].End.Kind != "returned" || end.Attempts[0].End.Result == nil || end.Attempts[0].End.Result.Outcome != "blocked" {
		t.Fatal("baseline overflow lost its declared blocked outcome", end.Status, end.Reason, script.starts)
	}
	data, err := rig.disk.Read("artifacts/"+end.Attempts[0].End.Result.Outputs["artifact"].ArtifactID, rig.disk.Policy.MaxArtifact)
	var report struct {
		Baseline         bool   `json:"baseline"`
		Failed           bool   `json:"failed"`
		CollectionFailed bool   `json:"collection_failed"`
		OutputComplete   bool   `json:"output_complete"`
		OutputBytes      int64  `json:"output_bytes"`
		Output           string `json:"output"`
	}
	if err != nil || json.Unmarshal(data, &report) != nil || !report.Baseline || !report.Failed || !report.CollectionFailed || report.OutputComplete || report.OutputBytes <= 256<<10 || len(report.Output) > 256<<10 {
		t.Fatal("registered baseline diagnostic claimed completeness or widened output", report, err)
	}
}
