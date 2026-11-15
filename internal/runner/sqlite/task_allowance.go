package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) guardTaskAllowanceSave(ctx context.Context, tx *sql.Tx, after runner.Run, previous, definition []byte, receipt *runner.Receipt) error {
	var before runner.Run
	if len(previous) != 0 {
		var err error
		before, err = runner.UnmarshalRunRecord(previous, definition)
		if err != nil {
			return err
		}
	}
	var oldGrants, newGrants []runner.TaskAllowanceGrant
	if before.TaskGraph != nil {
		oldGrants = before.TaskGraph.AllowanceGrants
	}
	if after.TaskGraph != nil {
		newGrants = after.TaskGraph.AllowanceGrants
	}
	spent := 0
	if !reflect.DeepEqual(oldGrants, newGrants) && before.TaskGraph != nil {
		count, err := storedAttemptCount(previous)
		if err != nil {
			return err
		}
		if err = s.loadAttempts(ctx, tx, &before, count); err != nil {
			return err
		}
		spent, err = storedGraphAttempts(ctx, tx, before)
		if err != nil {
			return err
		}
	}
	return runner.ValidateTaskAllowanceChange(before, after, receipt, spent)
}

func storedGraphAttempts(ctx context.Context, tx *sql.Tx, root runner.Run) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.data,count(a.id) FROM runs r
LEFT JOIN attempts a ON a.run_id=r.id WHERE r.project=? GROUP BY r.id`, root.ProjectID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	spent := 0
	if root.TaskGraph.Lineage != nil {
		spent = root.TaskGraph.Lineage.PriorAttempts
	}
	for rows.Next() {
		var data []byte
		var actual int
		if err = rows.Scan(&data, &actual); err != nil {
			return 0, err
		}
		var member runner.Run
		if err = json.Unmarshal(data, &member); err != nil {
			return 0, err
		}
		if member.TaskGraph == nil || member.TaskGraph.RootRunID != root.ID {
			continue
		}
		count, err := storedAttemptCount(data)
		if err != nil {
			return 0, err
		}
		if count != actual || actual > runner.MaxGeneralTaskAttempts-spent {
			return 0, errors.New("goal allowance grant requires complete retained family attempt history")
		}
		spent += actual
	}
	return spent, rows.Err()
}
