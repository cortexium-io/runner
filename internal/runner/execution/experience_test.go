package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func experienceAssignment(t *testing.T, disk *runner.Disk) runner.Assignment {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/experience/fixture.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := os.ReadFile("../../../testdata/experience/receipt.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = disk.Write("artifacts/fixture/experience.tar.gz", data, 16<<20); err != nil {
		t.Fatal(err)
	}
	roleData, err := os.ReadFile("../../../examples/jacks-break/roles/experience-review.json")
	var role runner.Role
	if err != nil || json.Unmarshal(roleData, &role) != nil {
		t.Fatal(err)
	}
	return runner.Assignment{
		RunID: "fixture_run", AttemptID: "fixture_review", Task: role.Purpose,
		Setup: runner.ExecutionSetup{Profile: *role.Execution},
		Inputs: map[string]runner.Value{
			"request": runner.Text("Inspect actual viewport images and interactions; do not infer usability from passing source checks."),
			"receipt": {Inline: receipt}, "evidence": {ArtifactID: "fixture/experience.tar.gz"},
		},
		OutputTypes: role.Outputs, Outcomes: []string{"ready", "changes_requested", "blocked"},
		Limits: runner.ExecutionLimits{MaxArtifact: 16 << 20, MaxRecord: 40 << 10},
	}
}

func TestExperienceImagesUseAuthenticatedProtectedInputsWithoutTools(t *testing.T) {
	policy := runner.DefaultStorage()
	policy.MaxManaged, policy.MinFree = 256<<20, 1
	policy.Database.MaxSize = 48 << 20
	disk, err := runner.OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	a := experienceAssignment(t, disk)
	if _, _, err := nativePayload(a); err != nil {
		t.Fatal("image captions exceeded the unchanged input allowance", err)
	}
	files, err := stagedInputData(disk, a)
	if err != nil || len(files) != 39 {
		t.Fatal("image evidence was not explicitly staged", len(files), err)
	}
	work := t.TempDir()
	j := nativeJob{Assignment: a, Workspace: &workspaceRecord{WorkDir: work}}
	inputs, err := experienceImageInputs(disk, j)
	if err != nil || len(inputs) != 37 {
		t.Fatal("not every viewport was attached", len(inputs), err)
	}
	for i := 1; i < len(inputs); i++ {
		row := inputs[i].(map[string]any)
		path := row["path"].(string)
		if row["type"] != "localImage" || filepath.Dir(path) != filepath.Join(work, ".runner-tmp", "inputs", a.AttemptID) || files[filepath.Base(path)] == nil {
			t.Fatal("image escaped protected declared inputs", row)
		}
	}
	wrong := a
	wrong.Inputs = runner.Clone(a.Inputs)
	wrong.Inputs["receipt"] = runner.Value{Inline: []byte(`{"key":"foreign","action":"qualify"}`)}
	if _, err := stagedInputData(disk, wrong); err == nil {
		t.Fatal("foreign candidate images reached the model")
	}
	a.Setup.Profile.Mode = ""
	inputs, err = experienceImageInputs(disk, nativeJob{Assignment: a})
	if err != nil || len(inputs) != 0 {
		t.Fatal("ordinary source review gained image input behavior")
	}
}

func TestExperienceGateCannotWaiveNativeEvidenceAndFeedbackRetainsBothReviews(t *testing.T) {
	policy := runner.DefaultStorage()
	policy.MaxManaged, policy.MinFree = 256<<20, 1
	policy.Database.MaxSize = 48 << 20
	disk, err := runner.OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	a := experienceAssignment(t, disk)
	a.Setup.Profile = runner.Profile{Harness: "release", Mode: "experience-gate", Access: "input-only"}
	a.Inputs["verdict"] = runner.Text("APPROVE")
	r := &Releases{disk: disk}
	o, err := r.experienceGate(releaseJob{Assignment: a}, runner.Observation{})
	if err != nil || o.End.Result.Outcome != "blocked" || !strings.Contains(o.End.Result.Reason, "Safari") {
		t.Fatal("model approval bypassed the native gate", o, err)
	}
	a.AttemptID = "fixture_feedback"
	a.Inputs = map[string]runner.Value{"source_review": runner.Text("Source regression missing"), "experience_review": runner.Text("Editing controls covered")}
	o, err = r.experienceFeedback(releaseJob{Assignment: a}, runner.Observation{})
	var summary string
	_ = json.Unmarshal(o.End.Result.Outputs["summary"].Inline, &summary)
	if err != nil || !strings.Contains(summary, "Source regression missing") || !strings.Contains(summary, "Editing controls covered") {
		t.Fatal("correction owner lost review feedback", summary, err)
	}
}
