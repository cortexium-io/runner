package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func writeTask(ctx context.Context, tx *sql.Tx, expected int, t core.Task) error {
	t.Revision = expected + 1
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE tasks SET revision=?,data=? WHERE id=? AND revision=?", t.Revision, b, t.ID, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}

// Release commits the full batch and receipt or nothing. A stale parent revision
// rolls back before any child becomes visible, including across connections.
func (s *Store) Release(ctx context.Context, expected int, parent core.Task, children []core.Task) error {
	if !parent.PlanRequest || parent.Proposal == nil || parent.Release == nil || len(parent.Proposal.Questions) != 0 || len(children) != len(parent.Proposal.Cards) || len(parent.Release.Children) != len(children) {
		return errors.New("incomplete plan release")
	}
	if err := core.ValidateProposal(*parent.Proposal); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = writeTask(ctx, tx, expected, parent); err != nil {
		return err
	}
	for _, child := range children {
		b, err := json.Marshal(child)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO tasks(id,revision,data) VALUES(?,?,?)", child.ID, child.Revision, b); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StopPlan revokes child authority in the same transaction as the parent. The
// coordinator uses its ordinary cancellation path; evidence and claims remain.
func (s *Store) StopPlan(ctx context.Context, expected int, parent core.Task) error {
	if parent.Release == nil || parent.Stage != core.Stopped {
		return errors.New("not a released plan stop")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = writeTask(ctx, tx, expected, parent); err != nil {
		return err
	}
	for _, id := range parent.Release.Children {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", id).Scan(&raw); err != nil {
			return err
		}
		var child core.Task
		if err = json.Unmarshal(raw, &child); err != nil {
			return err
		}
		if child.Stage == core.Stopped || child.Stage == core.Completed {
			continue
		}
		child.Generation++
		child.Approval = nil
		child.Stage = core.Stopped
		child.NextRole = ""
		child.Pending = nil
		child.Attention = nil
		if err = writeTask(ctx, tx, child.Revision, child); err != nil {
			return err
		}
	}
	return tx.Commit()
}
