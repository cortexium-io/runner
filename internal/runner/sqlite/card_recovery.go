package sqlite

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) SaveUnpublishedCardRecovery(ctx context.Context, root, child runner.Run, unstarted []runner.Run, receipt *runner.Receipt) error {
	grant := root.UnpublishedCardRecovery
	if grant == nil || root.RecordFormat != 5 && root.RecordFormat != 6 || grant.Request.RunID != root.ID || grant.Request.ChildRunID != child.ID || child.Task == nil || child.Task.Identity.RootRunID != root.ID || receipt == nil || receipt.RunID != root.ID || receipt.Kind != "recover_unpublished_card" || len(grant.Unstarted) != len(unstarted) {
		return errors.New("invalid atomic unpublished card recovery")
	}
	seen := map[string]bool{root.ID: true, child.ID: true}
	for i, pending := range unstarted {
		original := grant.Unstarted[i]
		if pending.ID != original.RunID || seen[pending.ID] || pending.Revision != original.Revision+1 || pending.Task == nil || pending.Task.Identity.RootRunID != root.ID || pending.Status != "Waiting" || pending.CancelRequested || pending.ResourcesReleased || pending.Step != 0 || len(pending.Attempts) != 0 {
			return errors.New("invalid unstarted card recovery reservation")
		}
		seen[pending.ID] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, pending := range unstarted {
		if err = s.saveRun(ctx, tx, pending, nil, nil); err != nil {
			return err
		}
	}
	if err = s.saveRun(ctx, tx, child, nil, nil); err != nil {
		return err
	}
	if err = s.saveRun(ctx, tx, root, nil, receipt); err != nil {
		return err
	}
	return tx.Commit()
}
