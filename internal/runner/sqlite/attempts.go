package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

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
	if (h.Format != 3 && h.Format != 4 && h.Format != 5 && h.Format != 6 && h.Format != 7 && h.Format != 8 && h.Format != 9) || h.Count == nil || *h.Count < 0 || len(h.Inline) != 0 {
		return 0, errors.New("invalid normalized attempt metadata")
	}
	return *h.Count, nil
}

func (s *Store) attemptRecords(r runner.Run) ([][]byte, error) {
	if r.RecordFormat < 3 {
		return nil, nil
	}
	// Attempts are normalized rows bounded by record and database bytes. A
	// historical process ceiling is not a second physical storage constraint.
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

func (s *Store) loadAttemptRecords(ctx context.Context, tx *sql.Tx, id string, count int) ([]runner.Attempt, error) {
	rows, err := tx.QueryContext(ctx, "SELECT position,id,data FROM attempts WHERE run_id=? ORDER BY position", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var attempts []runner.Attempt
	for rows.Next() {
		var position int
		var id string
		var data []byte
		if err = rows.Scan(&position, &id, &data); err != nil {
			return nil, err
		}
		if count < 0 || position != len(attempts) || position >= count || len(data) > int(s.policy.Database.MaxRecord) {
			return nil, errors.New("stored attempt order, count or size differs")
		}
		var a runner.Attempt
		if err = json.Unmarshal(data, &a); err != nil || id == "" || a.ID != id {
			return nil, errors.New("stored attempt identity or format differs")
		}
		attempts = append(attempts, a)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if count >= 0 {
		if len(attempts) != count {
			return nil, errors.New("normalized attempt history is incomplete")
		}
	}
	return attempts, nil
}

func (s *Store) saveAttempts(ctx context.Context, tx *sql.Tx, r runner.Run, records [][]byte, previous []byte, handoffUpgrade bool) error {
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
		if old.RecordFormat >= 4 {
			before, _ := json.Marshal(old.UnpublishedCardRecovery)
			after, _ := json.Marshal(r.UnpublishedCardRecovery)
			upgrade := handoffUpgrade && old.RecordFormat == 6 && r.RecordFormat == 8 && old.SourceHandoff == nil && r.SourceHandoff != nil
			if r.RecordFormat != old.RecordFormat && !upgrade || string(before) != string(after) {
				return errors.New("one-use card recovery audit cannot change or be discarded")
			}
		}
		if old.SourceHandoff != nil {
			before, _ := json.Marshal(old.SourceHandoff)
			after, _ := json.Marshal(r.SourceHandoff)
			if string(before) != string(after) {
				return errors.New("source handoff audit cannot change or be discarded")
			}
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT position,id,data FROM attempts WHERE run_id=? ORDER BY position", r.ID)
	if err != nil {
		return err
	}
	n := 0
	for rows.Next() {
		var position int
		var id string
		var data []byte
		if err = rows.Scan(&position, &id, &data); err != nil {
			break
		}
		if r.RecordFormat < 3 || position != n || position >= len(r.Attempts) || r.Attempts[position].ID != id {
			err = errors.New("save would discard or replace retained attempt history")
			break
		}
		var old runner.Attempt
		if err = json.Unmarshal(data, &old); err != nil {
			break
		}
		if reflect.DeepEqual(old, r.Attempts[position]) {
			// Preserve exact retained bytes, including legacy JSON encoding,
			// whenever the attempt's public facts are unchanged.
			records[position] = data
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
