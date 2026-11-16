package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

var _ runner.TaskRecoveryStore = (*Store)(nil)

func guardTaskRecoverySave(r runner.Run, previous []byte, allowed bool) error {
	var old runner.Run
	if len(previous) != 0 {
		if err := json.Unmarshal(previous, &old); err != nil {
			return err
		}
	}
	var before, after []runner.TaskRecoveryAudit
	if old.TaskGraph != nil {
		before = old.TaskGraph.Recoveries
	}
	if r.TaskGraph != nil {
		after = r.TaskGraph.Recoveries
	}
	return runner.ValidateTaskRecoveryAudit(before, after, allowed)
}

func (s *Store) SaveTaskRecovery(ctx context.Context, expected, after []runner.Run, receipt *runner.Receipt) error {
	if len(expected) == 0 || len(expected) > s.maxRuns || len(after) == 0 || len(after) > len(expected) {
		return errors.New("task recovery needs a complete bounded family and affected run set")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	before := make([]runner.Run, 0, len(expected))
	known := make(map[string]bool, len(expected))
	for _, want := range expected {
		var data, definition []byte
		if err = tx.QueryRowContext(ctx, "SELECT data,definition FROM runs WHERE id=? AND project=?", want.ID, want.ProjectID).Scan(&data, &definition); err != nil {
			return err
		}
		got, err := runner.UnmarshalRunRecord(data, definition)
		if err != nil {
			return err
		}
		count, err := storedAttemptCount(data)
		if err != nil {
			return err
		}
		if err = s.loadAttempts(ctx, tx, &got, count); err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) || known[got.ID] {
			return errors.New("task recovery family changed before transaction")
		}
		known[got.ID] = true
		before = append(before, got)
	}
	if err = runner.ValidateTaskRecovery(before, after, receipt); err != nil {
		return err
	}
	rootID, projectID := before[0].TaskGraph.RootRunID, before[0].ProjectID
	rows, err := tx.QueryContext(ctx, "SELECT id,data FROM runs WHERE project=?", projectID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			break
		}
		var r runner.Run
		if err = json.Unmarshal(data, &r); err != nil {
			break
		}
		if r.TaskGraph != nil && r.TaskGraph.RootRunID == rootID && !known[id] {
			err = errors.New("task recovery omitted an owned graph member")
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	for _, r := range after {
		var ownReceipt *runner.Receipt
		if r.ID == receipt.RunID {
			ownReceipt = receipt
		}
		if err = s.saveRunAuthorized(ctx, tx, r, nil, ownReceipt, saveAuthorization{recovery: true}); err != nil {
			return err
		}
	}
	return tx.Commit()
}
