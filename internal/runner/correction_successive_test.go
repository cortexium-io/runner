package runner

import (
	"bytes"
	"reflect"
	"testing"
)

// Exercise a real first grant and failed correction before requesting another.
func successiveCorrectionFixture(t *testing.T) (*Core, *frontendRecheckStore, Run, RecoverCorrectionsRequest, RecoverCorrectionsRequest) {
	t.Helper()
	c, store, _, first := currentCardCorrectionFixture(t)
	if _, err := c.RecoverCorrections(first); err != nil {
		t.Fatal(err)
	}
	r := Clone(c.runs[first.RunID])
	finishBudgetCorrectionOwner(t, c, store, &r, 1)
	finishBudgetCorrectionCheck(t, c, store, &r, 1, false)
	if r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 4 {
		t.Fatal("first grant did not stop at its exact bound", r.Status, r.Reason)
	}
	r.ResourcesReleased = true // Simulate terminal lifecycle cleanup settlement.
	c.runs[r.ID] = Clone(r)
	next := Clone(first)
	next.RequestID, next.PreviousRequestID = "second-explicit-one-use-grant", first.RequestID
	next.AttemptID, next.Revision, next.MaxUses = r.Current().ID, c.outputRun(r).Revision, 5
	for id, target := range map[string]*string{r.StepInputs["patch"].ArtifactID: &next.PatchSHA256, r.Current().End.Result.Outputs["artifact"].ArtifactID: &next.CheckSHA256} {
		data, err := c.disk.Read("artifacts/"+id, c.disk.Policy.MaxArtifact)
		if err != nil {
			t.Fatal(err)
		}
		*target = correctionDigest(data)
	}
	return c, store, r, first, next
}

func TestCorrectionSuccessiveGrantPreservesHistoryReplayAndExactBound(t *testing.T) {
	for _, passes := range []bool{false, true} {
		name := "fails-at-five"
		if passes {
			name = "passes-to-protected-tail"
		}
		t.Run(name, func(t *testing.T) {
			c, store, before, first, next := successiveCorrectionFixture(t)
			previous := Clone(before.CorrectionRecovery)
			_, definition, _ := MarshalRunRecord(before)
			if _, err := c.RecoverCorrections(next); err != nil {
				t.Fatal(err)
			}
			r := Clone(c.runs[before.ID])
			if !reflect.DeepEqual(r.CorrectionRecovery.Previous, previous) || r.RouteUses["check/changes_requested"] != 5 || r.CorrectionRecovery.MaxUses != 5 || r.CorrectionRecovery.PreviousUses != 4 {
				t.Fatal("second grant lost first audit or changed spending")
			}
			finishBudgetCorrectionOwner(t, c, store, &r, 2)
			finishBudgetCorrectionCheck(t, c, store, &r, 2, passes)
			if passes {
				if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.Decision != nil {
					t.Fatal("fresh check bypassed protected delivery tail", r.Status, r.Reason)
				}
			} else if r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 5 {
				t.Fatal("grant admitted a sixth correction", r.Status, r.Reason)
			}
			c.runs[r.ID] = Clone(r)
			saves := store.saves
			for _, req := range []RecoverCorrectionsRequest{first, next} {
				replay, err := c.RecoverCorrections(req)
				if err != nil || !reflect.DeepEqual(replay.CorrectionRecovery, r.CorrectionRecovery) || replay.RouteUses["check/changes_requested"] != 5 || store.saves != saves {
					t.Fatal("old or new receipt replay spent or replaced a grant", err)
				}
			}
			_, afterDefinition, _ := MarshalRunRecord(r)
			if !bytes.Equal(definition, afterDefinition) || !reflect.DeepEqual(before.TaskGroups, r.TaskGroups) {
				t.Fatal("second grant changed frozen definition or card history")
			}
			for i, old := range before.Attempts {
				got := Clone(r.Attempts[i])
				got.Superseded = old.Superseded
				if !reflect.DeepEqual(got, old) {
					t.Fatal("second grant rewrote prior attempt", old.ID)
				}
			}
		})
	}
}

func TestCorrectionSuccessiveGrantRejectsChangedAuthority(t *testing.T) {
	for _, mode := range []string{"missing-previous", "wrong-previous", "two-uses", "stale", "patch", "check", "old-rejection", "old-candidate", "other-route", "cleanup"} {
		t.Run(mode, func(t *testing.T) {
			c, store, before, _, next := successiveCorrectionFixture(t)
			switch mode {
			case "missing-previous":
				next.PreviousRequestID = ""
			case "wrong-previous":
				next.PreviousRequestID = "another-grant"
			case "two-uses":
				next.MaxUses++
			case "stale":
				next.Revision--
			case "patch":
				next.PatchSHA256 = next.CheckSHA256
			case "check":
				next.CheckSHA256 = next.PatchSHA256
			case "old-rejection":
				before.CorrectionRecovery.AttemptID = next.AttemptID
			case "old-candidate":
				before.CorrectionRecovery.PatchID = before.StepInputs["patch"].ArtifactID
			case "other-route":
				before.CorrectionRecovery.Route = "review/changes_requested"
			case "cleanup":
				before.ResourcesReleased = false
			}
			c.runs[before.ID] = Clone(before)
			saves := store.saves
			if _, err := c.RecoverCorrections(next); err == nil {
				t.Fatal("changed successive-grant authority accepted")
			}
			if !reflect.DeepEqual(c.runs[before.ID], before) || store.saves != saves {
				t.Fatal("rejected second grant mutated saved work")
			}
		})
	}
}
