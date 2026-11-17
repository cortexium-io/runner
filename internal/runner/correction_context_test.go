package runner

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

func currentCardCorrectionFixture(t *testing.T) (*Core, *frontendRecheckStore, *budgetCorrectionExecutor, RecoverCorrectionsRequest) {
	t.Helper()
	c, store, executor, req := budgetCorrectionFixture(t)
	c.snapshot.Instance.EnforceWorkLimits = false
	before := Clone(c.runs[req.RunID])
	// The budgeting fixture omits frozen child execution metadata. Public result
	// rendering needs the ordinary seed/owner/pin step identities for its usage.
	for _, link := range before.TaskGroups["cards"].Children {
		child := Clone(c.runs[link.RunID])
		child.Process = Process{Steps: []Step{
			{ID: "seed", Role: "fixture"},
			{ID: "owner", Role: "fixture"},
			{ID: "pin", Role: "fixture"},
		}}
		child.Roles = map[string]Role{"fixture": {Execution: &Profile{Harness: "deterministic"}}}
		for i := range child.Attempts {
			child.Attempts[i].Step = i
		}
		c.runs[child.ID] = child
	}
	req.WithinBudget, req.CurrentRoute = false, true
	req.Revision = c.outputRun(before).Revision
	key := before.Process.Steps[before.Step].ID + "/changes_requested"
	req.MaxUses = before.RouteUses[key] + 1
	patchID := before.Current().Source.InputPatchID
	checkID := before.Current().End.Result.Outputs["artifact"].ArtifactID
	for id, target := range map[string]*string{patchID: &req.PatchSHA256, checkID: &req.CheckSHA256} {
		data, err := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
		if err != nil {
			t.Fatal(err)
		}
		*target = correctionDigest(data)
	}
	return c, store, executor, req
}

func TestCorrectionCurrentRouteFreshCheckRejoinsProtectedTail(t *testing.T) {
	c, store, _, req := currentCardCorrectionFixture(t)
	before := Clone(c.runs[req.RunID])
	if req.Revision <= before.Revision {
		t.Fatal("fixture does not include source-card revisions")
	}
	_, definition, _ := MarshalRunRecord(before)
	r, err := c.RecoverCorrections(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Revision != c.outputRun(c.runs[r.ID]).Revision || r.Revision != req.Revision+1 {
		t.Fatal("grant returned a private or pre-commit revision", r.Revision)
	}
	finishBudgetCorrectionOwner(t, c, store, &r, 1)
	finishBudgetCorrectionCheck(t, c, store, &r, 1, true)
	if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.Decision != nil || r.RouteUses[before.Process.Steps[before.Step].ID+"/changes_requested"] != 4 {
		t.Fatal("current-route grant bypassed fresh checks or the protected delivery tail", r.Status, r.Reason)
	}
	_, after, _ := MarshalRunRecord(r)
	if !bytes.Equal(definition, after) || !reflect.DeepEqual(before.TaskGroups, r.TaskGroups) || len(r.CorrectionRecovery.RouteLimits) != 1 {
		t.Fatal("current-route continuation changed frozen work or another gate")
	}
}

func TestCorrectionContextIsLimitedToDeclaredFutureRecipients(t *testing.T) {
	disk, err := OpenDisk(t.TempDir(), DefaultStorage())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"runtime":"RUNNER_PROJECT_FRONTEND_MODULES"}`)
	proof := &CorrectionContext{
		ID:     "root/correction-context-" + correctionDigest(content) + ".json",
		Bytes:  int64(len(content)),
		SHA256: correctionDigest(content),
		Roles:  []string{"owner", "review"},
	}
	if err := disk.Write("artifacts/"+proof.ID, content, MaxCorrectionContext); err != nil {
		t.Fatal(err)
	}
	store := &frontendRecheckStore{artifacts: map[string]Artifact{proof.ID: {
		ID:        proof.ID,
		RunID:     "root",
		ProjectID: "demo",
		Bytes:     int64(len(content)),
	}}}
	c := &Core{ctx: context.Background(), disk: disk, store: store}
	r := Run{
		ID: "root", ProjectID: "demo",
		Process:            Process{Steps: []Step{{Role: "owner"}, {Role: "review"}, {Role: "other"}}},
		CorrectionRecovery: &CorrectionRecovery{CurrentRoute: true, FromVisit: 8, Context: proof},
	}
	for _, tc := range []struct {
		name        string
		step, visit int
		want        bool
	}{
		{"new owner", 0, 8, true},
		{"new review", 1, 10, true},
		{"old owner", 0, 7, false},
		{"unrelated role", 2, 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.correctionAssignmentContext(r, Attempt{Step: tc.step, Visit: tc.visit})
			if err != nil || (got != nil) != tc.want {
				t.Fatal("context recipient or lifetime escaped", got, err)
			}
		})
	}
	if err := disk.Write("artifacts/"+proof.ID, []byte("changed"), MaxCorrectionContext); err != nil {
		t.Fatal(err)
	}
	if _, err := c.correctionAssignmentContext(r, Attempt{Step: 0, Visit: 8}); err == nil {
		t.Fatal("changed context reached a new actor")
	}
}

func TestCurrentCorrectionRouteExhaustsAtFourWithoutExtendingOtherRoutes(t *testing.T) {
	r := Run{
		Process: Process{Steps: []Step{
			{ID: "owner", Role: "owner"},
			{
				ID:     "check",
				Role:   "check",
				Inputs: map[string]Binding{"patch": {FromCandidate: true}},
				Routes: map[string]Route{"changes_requested": {Next: "owner", MaxTimes: 3}},
			},
			{
				ID:     "review",
				Role:   "review",
				Inputs: map[string]Binding{"patch": {FromCandidate: true}},
				Routes: map[string]Route{"changes_requested": {Next: "owner", MaxTimes: 1}},
			},
		}},
		Roles: map[string]Role{
			"owner":  {Execution: &Profile{Harness: "codex", Access: "workspace-write"}},
			"check":  {Execution: &Profile{Harness: "command", Access: "workspace-read", Command: &Command{Args: []string{"/fixture/check"}}}},
			"review": {Execution: &Profile{Harness: "codex", Access: "workspace-read"}},
		},
		Step: 1, Status: "Failed", RouteUses: map[string]int{"check/changes_requested": 3},
		CorrectionRecovery: &CorrectionRecovery{
			CurrentRoute: true,
			Route:        "check/changes_requested",
			PreviousUses: 3,
			MaxUses:      4,
			RouteLimits:  map[string]int{"check/changes_requested": 4},
		},
	}
	a := &Attempt{End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested"}}}
	c := &Core{}
	before := Clone(r.Process)
	c.route(&r, a)
	if r.Status != "Queued" || r.Step != 0 || r.RouteUses["check/changes_requested"] != 4 {
		t.Fatal("one extra use was not granted", r.Status, r.Reason)
	}
	r.Step, r.Status = 1, "Failed"
	c.route(&r, a)
	if r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 4 || r.Reason != "repetition allowance exhausted: check/changes_requested" || !reflect.DeepEqual(before, r.Process) {
		t.Fatal("local grant allowed a fifth use or changed the definition")
	}
	if routeLimit(r, 2, "changes_requested", r.Process.Steps[2].Routes["changes_requested"]) != 1 {
		t.Fatal("another route received an allowance")
	}
}
