//go:build darwin || linux

package execution

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestPlannedInvestigationRetainsFirstMeasurementAcrossCorrectionAndReview(t *testing.T) {
	measure, provider := evidenceBinary(t, "./cmd/runner-measure"), evidenceBinary(t, "./testdata/release-operator")
	repo := sourceRepository(t)
	benchmark := `package seats
import("encoding/json";"os";"path/filepath";"testing")
func BenchmarkFixtureHandoff(b *testing.B){
 stage,err:=os.ReadFile("stage.txt");if err!=nil{b.Fatal(err)}
 samples:=[]map[string]any{}
 for i,size:=range []int{15,25,30}{
  outcome:="passed";if string(stage)=="before"{outcome="deadline"}
  samples=append(samples,map[string]any{"size":size,"seed":101+i,"phase":"admission","work":1,"outcome":outcome})
 }
 data,err:=json.Marshal(map[string]any{"stage":string(stage),"samples":samples});if err!=nil{b.Fatal(err)}
 if err=os.WriteFile(filepath.Join(os.Getenv("RUNNER_MEASURE_EVIDENCE"),"samples.json"),data,0600);err!=nil{b.Fatal(err)}
 b.ReportMetric(3,"samples/op")
}
`
	for name, data := range map[string]string{"seats.go": seatsImplementation, "stage.txt": "pristine", "handoff_test.go": benchmark} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "--quiet", "-m", "measurement handoff fixture")

	tasks := `[{"id":"prepare","inputs":{"task":"Prepare the fixture candidate stage; preserve the benchmark and all source contracts.","paths":["stage.txt"]}}]`
	plan := goodTurn()
	plan.Items[0].Text = string(mustJSON(t, map[string]any{"outcome": "ready", "outputs": map[string]string{"tasks": tasks}, "reason": ""}))
	reject, approve := goodTurn(), goodTurn()
	reject.Items[0].Text = `{"outcome":"changes_requested","outputs":{"verdict":"BLOCKED","summary":"Complete the final scripted review obligation"},"reason":"review fixture correction"}`
	approve.Items[0].Text = `{"outcome":"ready","outputs":{"verdict":"APPROVE","summary":"Original, before and current evidence independently reviewed"},"reason":""}`
	script := &scriptedPeer{results: []nativeTurn{plan, goodTurn(), goodTurn(), reject, goodTurn(), approve}}
	var original, before []byte
	readReport := func(data []byte, stage string, round int, baseline bool) {
		t.Helper()
		var report struct {
			Round    int  `json:"round"`
			Baseline bool `json:"baseline"`
			Evidence map[string]struct {
				Stage   string `json:"stage"`
				Samples []struct {
					Size, Seed, Work int
					Phase, Outcome   string
				} `json:"samples"`
			} `json:"evidence"`
		}
		if json.Unmarshal(data, &report) != nil || report.Round != round || report.Baseline != baseline || report.Evidence["samples.json"].Stage != stage || len(report.Evidence["samples.json"].Samples) != 3 {
			t.Error("full measurement identity or three-sample evidence changed", stage, string(data))
			return
		}
		for i, sample := range report.Evidence["samples.json"].Samples {
			outcome := "passed"
			if stage == "before" {
				outcome = "deadline"
			}
			if sample.Size != []int{15, 25, 30}[i] || sample.Seed != 101+i || sample.Work != 1 || sample.Phase != "admission" || sample.Outcome != outcome {
				t.Error("a before/current sample was omitted or relabelled", stage, sample)
			}
		}
	}
	script.onTurn = func(dir string) {
		switch script.starts {
		case 0:
			original = bytes.Clone(stagedEvidence(t, dir, "baseline"))
			readReport(original, "pristine", 1, true)
		case 1:
			if !bytes.Equal(stagedEvidence(t, dir, "baseline"), original) {
				t.Error("accepted card lost the pristine baseline")
			}
			if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte("before"), 0600); err != nil {
				t.Error(err)
			}
		case 2, 4:
			current := stagedEvidence(t, dir, "measurement")
			stage, round := "before", 1
			if script.starts == 4 {
				stage, round = "after", 2
			}
			readReport(current, stage, round, false)
			if script.starts == 2 {
				before = bytes.Clone(current)
			}
			if !bytes.Equal(stagedEvidence(t, dir, "baseline"), original) {
				t.Error("correction replaced the original baseline")
			}
			stage = "after"
			if script.starts == 4 {
				stage = "reviewed"
			}
			if err := os.WriteFile(filepath.Join(dir, "stage.txt"), []byte(stage), 0600); err != nil {
				t.Error(err)
			}
		case 3, 5:
			if !bytes.Equal(stagedEvidence(t, dir, "baseline_measurements"), original) || !bytes.Equal(stagedEvidence(t, dir, "before_measurements"), before) {
				t.Error("independent review lost immutable original or complete first-candidate evidence")
			}
			readReport(stagedEvidence(t, dir, "before_measurements"), "before", 1, false)
			stage, round := "after", 2
			if script.starts == 5 {
				stage, round = "reviewed", 3
			}
			readReport(stagedEvidence(t, dir, "measurements"), stage, round, false)
			if !bytes.Contains(stagedEvidence(t, dir, "checks"), []byte("TestParseSeats")) {
				t.Error("review lost the full scoped check receipt")
			}
		default:
			t.Error("unexpected model-protocol turn", script.starts)
		}
	}
	rig, process, operator := evidenceRig(t, repo, "planned-investigation.json", measure, provider, script, func(*runner.Project) {})
	defer rig.close()
	r, err := rig.core.Start(runner.StartRequest{
		RequestID: "planned-measurement-handoff",
		Project:   "demo",
		Inputs: map[string]runner.Value{
			"request":   runner.Text("Preserve all three first-candidate samples through correction and independent review."),
			"package":   runner.Text("."),
			"tests":     runner.Text("^TestParseSeats$"),
			"benchmark": runner.Text("^BenchmarkFixtureHandoff$"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	end := waitSourceRun(t, rig.core, r.ID, func(r runner.Run) bool {
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
	}, time.Minute)
	if end.Status != "Waiting" || end.Decision == nil || end.Decision.Step != "prepare" || end.Decision.Next != "merge" || script.starts != 6 || process.MaxAttempts != 64 || process.MaxAttemptsPerStep != 1 || process.AttemptTimeout != "20m" {
		t.Fatal("planned correction did not reach the original protected handoff", end.Status, end.Reason, end.Decision, script.starts)
	}
	counts := map[string]int{}
	var first, retained runner.Value
	for _, a := range end.Attempts {
		id := end.Process.Steps[a.Step].ID
		counts[id]++
		if id == "baseline" || id == "retain-measurement" || id == "measure" && counts[id] == 1 {
			artifact := a.End.Result.Outputs["artifact"]
			data, err := rig.disk.Read("artifacts/"+artifact.ArtifactID, rig.disk.Policy.MaxArtifact)
			want := before
			if id == "baseline" {
				want = original
			}
			if err != nil || !bytes.Equal(data, want) {
				t.Fatal("retained producer bytes changed", id, err)
			}
			if id == "measure" {
				first = artifact
			}
			if id == "retain-measurement" {
				retained = artifact
				if a.Superseded {
					t.Fatal("correction superseded the stable before artifact")
				}
			}
		}
	}
	if first.ArtifactID == "" || retained.ArtifactID == "" || first.ArtifactID == retained.ArtifactID || counts["baseline"] != 1 || counts["plan"] != 1 || counts["cards"] != 1 || counts["retain-measurement"] != 1 || counts["owner"] != 2 || counts["measure"] != 3 || counts["review"] != 2 || len(end.TaskStatuses) != 1 {
		t.Fatal("before evidence was rerun, aliased, or accepted cards were repeated", counts, end.TaskStatuses)
	}
	calls, _ := os.ReadFile(filepath.Join(operator, "calls"))
	if strings.Count(string(calls), "publish\n") != 1 || strings.Contains(string(calls), "merge\n") || strings.Contains(string(calls), "stop\n") {
		t.Fatal("copy handoff changed publication count or bypassed approval", string(calls))
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPlannedInvestigationFullReviewEvidenceFitsUnchangedNativeBudget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "examples", "jacks-break", "roles", "planned-investigation-review.json"))
	var role runner.Role
	if err != nil || json.Unmarshal(data, &role) != nil {
		t.Fatal(err)
	}
	values := map[string]runner.Value{}
	for name, kind := range role.Inputs {
		if kind == "artifact" {
			values[name] = runner.Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/" + name + ".json"}
		} else if name == "request" {
			values[name] = runner.Text(strings.Repeat("bounded evidence request; ", 80))
		} else {
			values[name] = runner.Text(strings.Repeat("s", 240))
		}
	}
	if len(values) != 8 || role.Inputs["before_measurements"] != "artifact" || role.Execution.Access != "workspace-read" || role.Execution.MaxInput != 8<<10 || role.Execution.MaxOutput != 512 {
		t.Fatal("full evidence handoff increased role authority or limits")
	}
	a := runner.Assignment{
		AttemptID:   "attempt_" + strings.Repeat("B", 26),
		Task:        role.Purpose,
		Setup:       runner.ExecutionSetup{Profile: *role.Execution},
		Inputs:      values,
		OutputTypes: role.Outputs,
		Outcomes:    []string{"ready", "changes_requested", "blocked"},
		Deadline:    time.Date(2026, 11, 7, 12, 20, 0, 0, time.UTC),
	}
	payload, schema, err := nativePayloadAt(a, a.Deadline.Add(-20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	encoded := mustJSON(t, schema)
	t.Logf("full review evidence native input %d/%d bytes", len(payload)+len(encoded)+len(reviewBase)+len(role.Execution.Instructions), role.Execution.MaxInput)
}
