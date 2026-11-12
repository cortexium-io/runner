package sqlite

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) SaveUnpublishedCardRecovery(ctx context.Context, root, child runner.Run, receipt *runner.Receipt) error {
	grant := root.UnpublishedCardRecovery
	if grant == nil || root.RecordFormat != 4 || grant.Request.RunID != root.ID || grant.Request.ChildRunID != child.ID || child.Task == nil || child.Task.Identity.RootRunID != root.ID || receipt == nil || receipt.RunID != root.ID || receipt.Kind != "recover_unpublished_card" {
		return errors.New("invalid atomic unpublished card recovery")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.saveRun(ctx, tx, child, nil, nil); err != nil {
		return err
	}
	if err = s.saveRun(ctx, tx, root, nil, receipt); err != nil {
		return err
	}
	return tx.Commit()
}
