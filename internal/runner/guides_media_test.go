package runner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/guidesmedia"
)

func TestGuidesRecordAuthorityAndLegacyZeroCodec(t *testing.T) {
	index, err := guidesmedia.NewIndex("run_import", "attempt_import", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := index.Encode()
	binding, _ := index.Bind(data)
	legacy := Run{
		ID:           "run_old",
		RecordFormat: 3,
		ProjectID:    "jacks-break",
		Process:      Process{ID: "deliver-planned-frontend"},
		Inputs:       map[string]Value{},
	}
	state, definition, err := MarshalRunRecord(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(state), "guides_media") || strings.Contains(string(definition), "guides_media") {
		t.Fatal("legacy codec changed")
	}
	current := legacy
	current.RecordFormat = 6
	current.ID = "run_media"
	current.GuidesMedia = &binding
	current.Process = Process{ID: "publish-guides-media", GuidesMedia: guidesmedia.AssetFormat}
	current.Inputs = map[string]Value{"media_index": {ArtifactID: binding.IndexID}}
	state, definition, err = MarshalRunRecord(current)
	if err != nil {
		t.Fatal(err)
	}
	if len(state) > 20<<10 || len(definition) > 40<<10 {
		t.Fatal("record reserve exceeded")
	}
	restored, err := UnmarshalRunRecord(state, definition)
	if err != nil || *restored.GuidesMedia != binding {
		t.Fatal(restored, err)
	}
	for _, mutate := range []func(*Run){
		func(r *Run) { r.RecordFormat = 3 },
		func(r *Run) { r.GuidesMedia = nil },
		func(r *Run) { r.Inputs = map[string]Value{"media_index": {ArtifactID: "foreign/index"}} },
		func(r *Run) { r.ProjectID = "foreign" },
		func(r *Run) { r.Process.ID = "legacy-bypass" },
		func(r *Run) { r.GuidesMedia.ManifestSHA256 = strings.Repeat("0", 64) },
	} {
		r := Clone(current)
		mutate(&r)
		if _, _, err := MarshalRunRecord(r); err == nil {
			t.Fatal("changed record authority accepted")
		}
	}
}

func TestGuidesExactAssetsAdmissionCustody(t *testing.T) {
	if os.Getenv("RUNNER_GUIDE_MEDIA_ADMISSION") != "1" {
		t.Skip("exact immutable media only in disposable admission fixture")
	}
	policy := DefaultStorage()
	policy.MinFree = 0
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	x, _ := guidesmedia.NewIndex("run_import", "attempt_import", strings.Repeat("a", 64))
	data, _ := x.Encode()
	b, _ := x.Bind(data)
	store := &frontendRecheckStore{artifacts: map[string]Artifact{}}
	put := func(id string, content []byte) {
		t.Helper()
		if err := disk.Write("artifacts/"+id, content, policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		store.artifacts[id] = Artifact{ID: id, RunID: x.RunID, ProjectID: "jacks-break", Bytes: int64(len(content))}
	}
	m, _ := guidesmedia.Approved()
	n := 0
	outputs := map[string]Value{"media_index": {ArtifactID: b.IndexID}, "check": Text("fixture")}
	for _, g := range m.Guides {
		for _, f := range g.Files {
			raw, err := guidesmedia.ReadApproved(t.Context(), f)
			if err != nil {
				t.Fatal(err)
			}
			a := x.Assets[n]
			n++
			put(a.ArtifactID, raw)
			outputs[a.Game+"_"+a.Role] = Value{ArtifactID: a.ArtifactID}
		}
	}
	put(b.IndexID, data)
	profile := &Profile{
		Harness:            "command",
		Mode:               "guides-import",
		Access:             "workspace-read",
		GuidesImportSHA256: x.HelperSHA256,
	}
	imported := Run{
		ID:                x.RunID,
		ProjectID:         "jacks-break",
		RecordFormat:      6,
		Status:            "Completed",
		ResourcesReleased: true,
		Process:           Process{ID: "stage-guides-media", Steps: []Step{{ID: "import", Role: "import"}}},
		Roles:             map[string]Role{"import": {ID: "import", Execution: profile, Outputs: guidesmedia.OutputContract()}},
		Attempts: []Attempt{{
			ID:       x.AttemptID,
			Step:     0,
			State:    Finished,
			Released: true,
			End:      &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: outputs}},
		}},
	}
	root := Run{
		ProjectID: "jacks-break",
		Process: Process{
			ID:          "publish-guides-media",
			GuidesMedia: guidesmedia.AssetFormat,
			Inputs:      map[string]string{"media_index": "artifact"},
		},
		Inputs: map[string]Value{"media_index": {ArtifactID: b.IndexID}},
	}
	c := &Core{ctx: t.Context(), disk: disk, store: store, runs: map[string]Run{x.RunID: imported}}
	if err := c.bindGuidesMedia(&root); err != nil || root.GuidesMedia == nil || *root.GuidesMedia != b {
		t.Fatal(root, err)
	}
	for name, mutate := range map[string]func(*Run){
		"active":        func(r *Run) { r.Status = "Running" },
		"lease":         func(r *Run) { r.ResourcesReleased = false },
		"attempt lease": func(r *Run) { r.Attempts[0].Released = false },
		"cleanup":       func(r *Run) { r.CleanupBlocked = true },
		"profile":       func(r *Run) { r.Roles["import"].Execution.GuidesImportSHA256 = strings.Repeat("b", 64) },
		"output":        func(r *Run) { delete(r.Attempts[0].End.Result.Outputs, "sudoku_video") },
		"failed":        func(r *Run) { r.Attempts[0].End.Kind = "failed" },
		"format":        func(r *Run) { r.RecordFormat = 3 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := Clone(imported)
			mutate(&changed)
			c.runs[x.RunID] = changed
			r := Clone(root)
			r.GuidesMedia = nil
			if c.bindGuidesMedia(&r) == nil {
				t.Fatal("unsettled/changed import accepted")
			}
			c.runs[x.RunID] = imported
		})
	}
	artifact := store.artifacts[x.Assets[0].ArtifactID]
	foreign := artifact
	foreign.RunID = "foreign"
	store.artifacts[artifact.ID] = foreign
	r := Clone(root)
	r.GuidesMedia = nil
	if c.bindGuidesMedia(&r) == nil {
		t.Fatal("foreign raw owner accepted")
	}
	store.artifacts[artifact.ID] = artifact
	changedIndex := append([]byte{}, data...)
	changedIndex[len(changedIndex)-1] = '!'
	disk.Write("artifacts/"+b.IndexID, changedIndex, policy.MaxArtifact)
	if c.bindGuidesMedia(&r) == nil {
		t.Fatal("changed index accepted")
	}
	encoded, _ := json.Marshal(root.GuidesMedia)
	t.Logf("all36 raw artifacts and accepted import/profile/output/settled leases authenticated; binding=%s; no product run", encoded)
}

func TestGuidesUnqualifiedCompleteIntakeCreatesNoRun(t *testing.T) {
	policy := DefaultStorage()
	policy.MinFree = 0
	disk, err := OpenDisk(t.TempDir(), policy)
	if err != nil {
		t.Fatal(err)
	}
	store := &frontendRecheckStore{receipts: map[string]Receipt{}}
	p := Process{ID: "publish-guides-media", GuidesMedia: guidesmedia.AssetFormat, Inputs: map[string]string{}}
	c := &Core{
		ctx:      t.Context(),
		store:    store,
		disk:     disk,
		runs:     map[string]Run{},
		snapshot: Snapshot{Projects: map[string]Project{"jacks-break": {ID: "jacks-break", DefaultProcess: p.ID, Processes: map[string]Process{p.ID: p}}}},
	}
	if _, err := c.Start(StartRequest{RequestID: guidesmedia.RequestID, Project: "jacks-break", Process: p.ID, Inputs: map[string]Value{}}); err == nil || !strings.Contains(err.Error(), "no product run admitted") {
		t.Fatal("unqualified typed process admitted", err)
	}
	if len(c.runs) != 0 || store.saves != 0 {
		t.Fatal("refusal created operational product state")
	}
}
