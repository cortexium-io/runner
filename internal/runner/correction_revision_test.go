package runner

import (
	"context"
	"strings"
	"testing"
)

type revisionReplayStore struct {
	Store
	rows []Run
}

func (s revisionReplayStore) Load(context.Context) ([]Run, error) { return Clone(s.rows), nil }

func TestCorrectionCurrentRouteCommittedReplayRestoresPublicFamilyRevision(t *testing.T) {
	c, store, _, req := currentCardCorrectionFixture(t)
	before := Clone(c.runs[req.RunID])
	first, err := c.RecoverCorrections(req)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Run{}
	for _, row := range c.runs {
		rows = append(rows, Clone(row))
	}
	// The receipt and committed row survived, but the process still has the
	// pre-commit root snapshot after losing acknowledgement/reconciliation.
	c.runs[before.ID] = before
	c.store = revisionReplayStore{Store: store, rows: rows}
	replay, err := c.RecoverCorrections(req)
	if err != nil || replay.Revision != first.Revision || replay.Revision != req.Revision+1 || c.runs[before.ID].Revision != before.Revision+1 || store.saves != 1 || replay.RouteUses["check/changes_requested"] != 4 {
		t.Fatal("committed replay returned private revision or spent another credit", err, replay.Revision)
	}
}

type revisionCorrectionExecutor struct {
	*budgetCorrectionExecutor
	change func() error
}

func (e revisionCorrectionExecutor) ValidateSourceRecovery(ctx context.Context, r Run, source SourceResult) error {
	if err := e.budgetCorrectionExecutor.ValidateSourceRecovery(ctx, r, source); err != nil {
		return err
	}
	if e.change != nil {
		return e.change()
	}
	return nil
}

func TestCorrectionCurrentRouteRejectsPrivateOrChangedFamilyRevision(t *testing.T) {
	for _, mode := range []string{"private-revision", "child-during-validation", "root-during-validation"} {
		t.Run(mode, func(t *testing.T) {
			c, store, executor, req := currentCardCorrectionFixture(t)
			before := Clone(c.runs[req.RunID])
			if req.Revision <= before.Revision {
				t.Fatal("fixture has no public source-card revision contribution")
			}
			if mode == "private-revision" {
				req.Revision = before.Revision
			}
			c.executor = revisionCorrectionExecutor{budgetCorrectionExecutor: executor, change: func() error {
				c.mu.Lock()
				defer c.mu.Unlock()
				id := before.ID
				if mode == "child-during-validation" {
					id = before.TaskGroups["cards"].Children[0].RunID
				}
				return c.persist(Clone(c.runs[id]), nil, nil)
			}}
			_, err := c.RecoverCorrections(req)
			if err == nil {
				t.Fatal("stale/private revision granted work")
			}
			if mode == "private-revision" && executor.validations != 0 {
				t.Fatal("private revision reached source validation")
			}
			if mode == "child-during-validation" && (!strings.Contains(err.Error(), "observed family revision") || c.runs[before.ID].Revision != before.Revision) {
				t.Fatal("child change did not fail at public family revision boundary", err)
			}
			if mode == "root-during-validation" && !strings.Contains(err.Error(), "run changed during source validation") {
				t.Fatal("private root optimistic check was removed", err)
			}
			after := c.runs[before.ID]
			if after.CorrectionRecovery != nil || after.Status != "Failed" || after.RouteUses["check/changes_requested"] != 3 {
				t.Fatal("rejected revision consumed a grant")
			}
			if _, err := store.FindReceipt(t.Context(), req.RequestID); err == nil {
				t.Fatal("rejected revision acquired a receipt")
			}
		})
	}
}
