package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func (s *Store) ImportedTask(ctx context.Context, source core.Source) (core.Task, bool, error) {
	var task core.Task
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM tasks WHERE source_key=?", source.Key()).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return task, false, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &task)
	}
	return task, err == nil, err
}

// Import captures once, atomically. Concurrent/repeated imports return the
// original task rather than resetting content, approvals or allowances.
func (s *Store) Import(ctx context.Context, task core.Task) (core.Task, error) {
	if task.Source == nil || task.Source.Provider == "" || task.Source.Connection == "" || task.Source.ID == "" || task.Source.Revision == "" || task.Stage != core.Draft || task.Approval != nil {
		return core.Task{}, errors.New("import requires an identified source and an unapproved draft")
	}
	data, err := json.Marshal(task)
	if err != nil {
		return core.Task{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Task{}, err
	}
	defer tx.Rollback()
	key := task.Source.Key()
	if _, err = tx.ExecContext(ctx, "INSERT INTO tasks(id,revision,data,source_key) VALUES(?,?,?,?) ON CONFLICT(source_key) DO NOTHING", task.ID, task.Revision, data, key); err != nil {
		return core.Task{}, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE source_key=?", key).Scan(&data); err != nil {
		return core.Task{}, err
	}
	var captured core.Task
	if err = json.Unmarshal(data, &captured); err != nil {
		return core.Task{}, err
	}
	return captured, tx.Commit()
}

func (s *Store) Projection(ctx context.Context) (core.ProjectionStatus, error) {
	var status core.ProjectionStatus
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM projection WHERE id=1").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &status)
	}
	return status, err
}

func (s *Store) RecordProjection(ctx context.Context, status core.ProjectionStatus) error {
	data, err := json.Marshal(status)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO projection(id,data) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", data)
	return err
}
