package runner

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

// Retain two real one-use grants, both spent, before authorizing the phase.
func correctionPhaseFixture(t *testing.T) (*Core, *frontendRecheckStore, Run, []RecoverCorrectionsRequest, RecoverCorrectionsRequest) {
	t.Helper()
	c, store, _, first, second := successiveCorrectionFixture(t)
	root := c.runs[second.RunID]
	content := `{"policy":"Retain all release gates"}`
	second.Context = &CorrectionContextInput{
		Content: content,
		Bytes:   int64(len(content)),
		SHA256:  correctionDigest([]byte(content)),
		Roles:   []string{root.Process.Steps[taskStepIndex(root.Process, "owner")].Role},
	}
	if _, err := c.RecoverCorrections(second); err != nil {
		t.Fatal(err)
	}
	r := Clone(c.runs[second.RunID])
	finishBudgetCorrectionOwner(t, c, store, &r, 2)
	finishBudgetCorrectionCheck(t, c, store, &r, 2, false)
	if r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 5 {
		t.Fatal("second grant did not stop at five", r.Status, r.Reason)
	}
	r.ResourcesReleased = true
	if err := c.persist(r, nil, nil); err != nil {
		t.Fatal(err)
	}
	r = Clone(c.runs[r.ID])
	phase := Clone(second)
	phase.RequestID, phase.PreviousRequestID = "explicit-three-use-phase", second.RequestID
	phase.Context = nil // Preserve the preceding scoped context.
	bindSuccessiveCorrectionRequest(t, c, r, &phase)
	additional := 3
	phase.AdditionalUses, phase.MaxUses = &additional, 8
	return c, store, r, []RecoverCorrectionsRequest{first, second}, phase
}

func TestCorrectionPhaseOwnsThreeCyclesAndStopsAtEightOrProtectedTail(t *testing.T) {
	for _, passAt := range []int{0, 6, 7, 8} {
		t.Run(fmt.Sprintf("pass-at-%d", passAt), func(t *testing.T) {
			c, store, before, earlier, phase := correctionPhaseFixture(t)
			_, definition, _ := MarshalRunRecord(before)
			if _, err := c.RecoverCorrections(phase); err != nil {
				t.Fatal(err)
			}
			r := Clone(c.runs[before.ID])
			grant := Clone(r.CorrectionRecovery)
			if r.RouteUses["check/changes_requested"] != 6 || grant.PreviousUses != 5 || grant.AdditionalUses != 3 || grant.MaxUses != 8 || len(grant.RouteLimits) != 1 || !reflect.DeepEqual(grant.Previous, before.CorrectionRecovery) || !reflect.DeepEqual(grant.Context, before.CorrectionRecovery.Context) {
				t.Fatal("phase lost linked history/context or changed its explicit count", grant)
			}
			for use := 6; use <= 8; use++ {
				if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "owner" || r.RouteUses["check/changes_requested"] != use {
					t.Fatal("phase did not autonomously route to the next owner", use, r.Status, r.Reason)
				}
				finishBudgetCorrectionOwner(t, c, store, &r, use)
				finishBudgetCorrectionCheck(t, c, store, &r, use, use == passAt)
				if use == passAt {
					if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.Decision != nil {
						t.Fatal("fresh passing check bypassed protected delivery tail", r.Status, r.Reason)
					}
					break
				}
			}
			if passAt == 0 && (r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 8 || r.Reason != "repetition allowance exhausted: check/changes_requested") {
				t.Fatal("phase admitted a ninth correction", r.Status, r.Reason)
			}
			_, afterDefinition, _ := MarshalRunRecord(r)
			if !bytes.Equal(definition, afterDefinition) || !reflect.DeepEqual(before.TaskGroups, r.TaskGroups) || !reflect.DeepEqual(r.CorrectionRecovery, grant) {
				t.Fatal("phase changed frozen definition, child history or grant")
			}
			for i, old := range before.Attempts {
				got := Clone(r.Attempts[i])
				got.Superseded = old.Superseded
				if !reflect.DeepEqual(got, old) {
					t.Fatal("phase rewrote prior attempt", old.ID)
				}
			}
			// Deliberately stale memory must reconcile even through an old receipt.
			c.runs[r.ID] = Clone(before)
			saves := store.saves
			for _, req := range append(earlier, phase) {
				replay, err := c.RecoverCorrections(req)
				if err != nil || !reflect.DeepEqual(replay.CorrectionRecovery, grant) || replay.RouteUses["check/changes_requested"] != r.RouteUses["check/changes_requested"] || store.saves != saves {
					t.Fatal("linked receipt replay lost the phase or spent another use", err)
				}
			}
			changed := Clone(phase)
			*changed.AdditionalUses = 2
			if _, err := c.RecoverCorrections(changed); err == nil || store.saves != saves {
				t.Fatal("changed phase count reused an existing receipt")
			}
		})
	}
}

func TestCorrectionPhaseRejectsInvalidCountsAndAuthority(t *testing.T) {
	for _, mode := range []string{"omitted-multi-use", "zero", "negative", "mismatched-ceiling", "overflow", "within-budget", "legacy", "previous", "unexhausted-previous"} {
		t.Run(mode, func(t *testing.T) {
			c, store, before, _, req := correctionPhaseFixture(t)
			switch mode {
			case "omitted-multi-use":
				req.AdditionalUses = nil
			case "zero":
				*req.AdditionalUses = 0
			case "negative":
				*req.AdditionalUses = -1
			case "mismatched-ceiling":
				req.MaxUses--
			case "overflow":
				*req.AdditionalUses = int(^uint(0) >> 1)
				req.MaxUses = before.RouteUses["check/changes_requested"] + *req.AdditionalUses
			case "within-budget":
				req.WithinBudget = true
			case "legacy":
				req.CurrentRoute = false
			case "previous":
				req.PreviousRequestID = "wrong-grant"
			case "unexhausted-previous":
				before.CorrectionRecovery.AdditionalUses = 3
				before.CorrectionRecovery.MaxUses = 7
				before.CorrectionRecovery.RouteLimits["check/changes_requested"] = 7
			}
			c.runs[before.ID] = Clone(before)
			saves := store.saves
			if _, err := c.RecoverCorrections(req); err == nil || store.saves != saves || !reflect.DeepEqual(c.runs[before.ID], before) {
				t.Fatal("invalid phase mutated saved work", err)
			}
		})
	}
}

func TestCorrectionPhaseMustExhaustBeforeAnotherExplicitGrant(t *testing.T) {
	c, store, _, _, phase := correctionPhaseFixture(t)
	if _, err := c.RecoverCorrections(phase); err != nil {
		t.Fatal(err)
	}
	r := Clone(c.runs[phase.RunID])
	for use := 6; use <= 8; use++ {
		finishBudgetCorrectionOwner(t, c, store, &r, use)
		finishBudgetCorrectionCheck(t, c, store, &r, use, false)
		if use < 8 {
			// A fresh request cannot extend a phase while Core owns its balance.
			early := Clone(phase)
			early.RequestID, early.PreviousRequestID = "unapproved-early-extension", phase.RequestID
			saves := store.saves
			if _, err := c.RecoverCorrections(early); err == nil || store.saves != saves {
				t.Fatal("another grant extended an unspent phase", use, err)
			}
		}
	}
	r.ResourcesReleased = true
	if err := c.persist(r, nil, nil); err != nil {
		t.Fatal(err)
	}
	next := Clone(phase)
	next.RequestID, next.PreviousRequestID = "separate-explicit-grant-after-phase", phase.RequestID
	next.AdditionalUses = nil
	bindSuccessiveCorrectionRequest(t, c, c.runs[r.ID], &next)
	recovered, err := c.RecoverCorrections(next)
	if err != nil || recovered.RouteUses["check/changes_requested"] != 9 || !reflect.DeepEqual(recovered.CorrectionRecovery.Previous, r.CorrectionRecovery) {
		t.Fatal("separately authorized recovery did not recognize exhausted phase count", err)
	}
}
