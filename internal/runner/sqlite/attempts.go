package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner"
)

// The process validator already caps a run at 128 attempts. The storage bound
// also applies when loading state, before trusting its configured definition.
const maxStoredAttempts = 128

type attemptRecordHeader struct {
	Format int             `json:"record_format"`
	Count  *int            `json:"attempt_count"`
	Inline json.RawMessage `json:"attempts"`
}

func storedAttemptCount(state []byte) (int, error) {
	var h attemptRecordHeader
	if err := json.Unmarshal(state, &h); err != nil {
		return 0, err
	}
	if h.Format == 0 {
		if h.Count != nil {
			return 0, errors.New("legacy run has normalized attempt metadata")
		}
		return -1, nil
	}
	if (h.Format != 3 && h.Format != 4) || h.Count == nil || *h.Count < 0 || *h.Count > maxStoredAttempts || len(h.Inline) != 0 {
		return 0, errors.New("invalid normalized attempt metadata")
	}
	return *h.Count, nil
}

func (s *Store) attemptRecords(r runner.Run) ([][]byte, error) {
	if r.RecordFormat < 3 {
		return nil, nil
	}
	if len(r.Attempts) > maxStoredAttempts {
		return nil, fmt.Errorf("%w: normalized attempt count exceeds %d", runner.ErrCapacity, maxStoredAttempts)
	}
	records := make([][]byte, len(r.Attempts))
	seen := map[string]bool{}
	for i, a := range r.Attempts {
		if a.ID == "" || seen[a.ID] {
			return nil, errors.New("normalized attempts need unique nonempty identities")
		}
		seen[a.ID] = true
		data, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}
		if len(data) > int(s.policy.Database.MaxRecord) {
			return nil, fmt.Errorf("%w: attempt record exceeds %d bytes", runner.ErrCapacity, s.policy.Database.MaxRecord)
		}
		records[i] = data
	}
	return records, nil
}

func (s *Store) loadAttempts(ctx context.Context, tx *sql.Tx, r *runner.Run, count int) error {
	rows, err := tx.QueryContext(ctx, "SELECT position,id,data FROM attempts WHERE run_id=? ORDER BY position", r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var attempts []runner.Attempt
	for rows.Next() {
		var position int
		var id string
		var data []byte
		if err = rows.Scan(&position, &id, &data); err != nil {
			return err
		}
		if count < 0 || position != len(attempts) || position >= count || len(data) > int(s.policy.Database.MaxRecord) {
			return errors.New("stored attempt order, count or size differs")
		}
		var a runner.Attempt
		if err = json.Unmarshal(data, &a); err != nil || id == "" || a.ID != id {
			return errors.New("stored attempt identity or format differs")
		}
		attempts = append(attempts, a)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count >= 0 {
		if len(attempts) != count {
			return errors.New("normalized attempt history is incomplete")
		}
		r.Attempts = attempts
	}
	return nil
}

func (s *Store) saveAttempts(ctx context.Context, tx *sql.Tx, r runner.Run, records [][]byte, previous []byte) error {
	oldCount := 0
	if previous != nil {
		var err error
		oldCount, err = storedAttemptCount(previous)
		if err != nil {
			return err
		}
		if (oldCount >= 0) != (r.RecordFormat >= 3) {
			return errors.New("run record format cannot change during save")
		}
		var old runner.Run
		if err = json.Unmarshal(previous, &old); err != nil {
			return err
		}
		if old.RecordFormat == 4 {
			before, _ := json.Marshal(old.UnpublishedCardRecovery)
			after, _ := json.Marshal(r.UnpublishedCardRecovery)
			if r.RecordFormat != 4 || string(before) != string(after) {
				return errors.New("one-use card recovery audit cannot change or be discarded")
			}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT position,id FROM attempts WHERE run_id=? ORDER BY position", r.ID)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var position int
		var id string
		if err = rows.Scan(&position, &id); err != nil {
			break
		}
		if r.RecordFormat < 3 || position != n || position >= len(r.Attempts) || r.Attempts[position].ID != id {
			err = errors.New("save would discard or replace retained attempt history")
			break
		}
		n++
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
	if oldCount >= 0 && n != oldCount {
		return errors.New("saved normalized attempt history is incomplete")
	}
	for i, data := range records {
		// Unchanged settled rows do not dirty pages or enlarge the rollback
		// journal on each observation of the current attempt.
		if _, err = tx.ExecContext(ctx, `INSERT INTO attempts(run_id,position,id,data) VALUES(?,?,?,?)
ON CONFLICT(run_id,position) DO UPDATE SET data=excluded.data WHERE attempts.data!=excluded.data`, r.ID, i, r.Attempts[i].ID, data); err != nil {
			return err
		}
	}
	return nil
}
