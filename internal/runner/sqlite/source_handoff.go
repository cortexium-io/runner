package sqlite

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) SaveSourceHandoff(ctx context.Context, root, child, pending runner.Run, receipt *runner.Receipt) error {
	audit := root.SourceHandoff
	if audit == nil || root.RecordFormat != 7 || root.UnpublishedCardRecovery == nil || receipt == nil || receipt.Kind != "adjudicate_source_handoff" || receipt.Key != audit.Request.RequestID || receipt.RunID != root.ID || audit.Request.RunID != root.ID || audit.Request.ChildRunID != child.ID || child.Task == nil || child.Task.Identity.RootRunID != root.ID || child.Step != 2 || child.Status != "Queued" || len(audit.Assessment.Unstarted) != 1 {
		return errors.New("invalid atomic source handoff")
	}
	original := audit.Assessment.Unstarted[0]
	if pending.ID != original.RunID || pending.ID == root.ID || pending.ID == child.ID || pending.Revision != original.Revision+1 || pending.Task == nil || pending.Task.Identity.RootRunID != root.ID || pending.Step != 0 || pending.Status != "Waiting" || pending.CancelRequested || pending.ResourcesReleased || len(pending.Attempts) != 0 {
		return errors.New("invalid source handoff final reservation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, expected := range []struct {
		id       string
		revision int64
	}{
		{root.ID, audit.Assessment.StoredRevision}, {child.ID, audit.Assessment.ChildRevision}, {pending.ID, original.Revision},
	} {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM runs WHERE id=?", expected.id).Scan(&raw); err != nil {
			return err
		}
		var old runner.Run
		if json.Unmarshal(raw, &old) != nil || old.Revision != expected.revision || expected.id == root.ID && (old.RecordFormat != 6 || old.SourceHandoff != nil) {
			return errors.New("source handoff stored revision changed")
		}
	}
	if err = s.saveRun(ctx, tx, pending, nil, nil); err != nil {
		return err
	}
	if err = s.saveRun(ctx, tx, child, nil, nil); err != nil {
		return err
	}
	if err = s.saveRunWithHandoff(ctx, tx, root, nil, receipt, true); err != nil {
		return err
	}
	return tx.Commit()
}
