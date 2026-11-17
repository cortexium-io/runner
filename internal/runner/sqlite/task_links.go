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

// loadRunRecord is the only stored graph decoder. Callers close any inventory
// cursor first, then hydrate both relations before exposing or comparing a Run.
func (s *Store) loadRunRecord(ctx context.Context, tx *sql.Tx, state, definition []byte) (runner.Run, error) {
	if len(state) > int(s.policy.Database.MaxRecord) || len(definition) > int(s.policy.Database.MaxRecord) {
		return runner.Run{}, errors.New("oversized stored run")
	}
	var header struct{ ID string }
	if err := json.Unmarshal(state, &header); err != nil || header.ID == "" {
		return runner.Run{}, errors.New("stored run has no identity")
	}
	count, err := storedAttemptCount(state)
	if err != nil {
		return runner.Run{}, err
	}
	attempts, err := s.loadAttemptRecords(ctx, tx, header.ID, count)
	if err != nil {
		return runner.Run{}, err
	}
	links, err := s.loadTaskLinkRecords(ctx, tx, header.ID, state)
	if err != nil {
		return runner.Run{}, err
	}
	return runner.UnmarshalRunStorageRecord(state, definition, attempts, links)
}

func (s *Store) consumesTaskReservation(ctx context.Context, tx *sql.Tx, child runner.Run) (bool, error) {
	var parentID, rootID string
	if child.TaskGraph != nil {
		parentID, rootID = child.TaskGraph.ParentRunID, child.TaskGraph.RootRunID
	} else if child.Task != nil {
		parentID = child.Task.Identity.RootRunID
		rootID = parentID
	}
	if parentID == "" || rootID == "" || parentID == child.ID || rootID == child.ID {
		return false, nil
	}
	load := func(id string) (runner.Run, error) {
		var project string
		var state, definition []byte
		if err := tx.QueryRowContext(ctx, "SELECT project,data,definition FROM runs WHERE id=?", id).Scan(&project, &state, &definition); err != nil {
			return runner.Run{}, err
		}
		r, err := s.loadRunRecord(ctx, tx, state, definition)
		if err != nil {
			return runner.Run{}, err
		}
		if r.ID != id || r.ProjectID != project {
			return runner.Run{}, errors.New("stored run identity differs")
		}
		return r, nil
	}
	parent, err := load(parentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	root := parent
	if rootID != parentID {
		root, err = load(rootID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	return runner.TaskMaterialization(root, parent, child), nil
}

func (s *Store) loadTaskLinkRecords(ctx context.Context, tx *sql.Tx, id string, state []byte) ([]runner.RoleTaskLinkRecord, error) {
	count, _, _, err := runner.RunStorageLinkCounts(state)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT position,child_run_id,data,result_position FROM role_task_links WHERE parent_run_id=? ORDER BY position", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []runner.RoleTaskLinkRecord
	for rows.Next() {
		var position int
		var row runner.RoleTaskLinkRecord
		if err := rows.Scan(&position, &row.ChildRunID, &row.Data, &row.ResultPosition); err != nil {
			return nil, err
		}
		if count < 0 || position != len(links) || position >= count || runner.TaskLinkRecordBytes(id, row) > int(s.policy.Database.MaxRecord) {
			return nil, errors.New("stored task-link count, order or size differs")
		}
		links = append(links, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count >= 0 && len(links) != count {
		return nil, errors.New("normalized task relationships are incomplete")
	}
	return links, nil
}

func (s *Store) saveTaskLinks(ctx context.Context, tx *sql.Tx, after, before runner.Run, previous []byte, record runner.RunStorageRecord) error {
	links := record.TaskLinks
	var old []runner.RoleTaskLinkRecord
	if len(previous) != 0 {
		count, _, _, err := runner.RunStorageLinkCounts(previous)
		if err != nil {
			return err
		}
		if count < 0 {
			old, err = runner.LegacyTaskLinkRecords(previous)
		} else {
			old, err = s.loadTaskLinkRecords(ctx, tx, after.ID, previous)
		}
		if err != nil {
			return err
		}
	}
	oldChildren := 0
	if before.TaskGraph != nil {
		oldChildren = len(before.TaskGraph.Children)
		if after.TaskGraph == nil || len(after.TaskGraph.Children) < oldChildren {
			return errors.New("save would discard retained task relationships")
		}
		for i, child := range before.TaskGraph.Children {
			next := after.TaskGraph.Children[i]
			if child.RunID != next.RunID || !reflect.DeepEqual(child.Spec, next.Spec) || child.Retired && !next.Retired {
				return errors.New("save would replace or reactivate a retained task reservation")
			}
			// Keep the exact prior canonical specification source, including
			// inline legacy specs, rather than choosing another matching attempt.
			retained, err := runner.RetireTaskLinkRecord(old[i], next.Retired)
			if err != nil {
				return err
			}
			links[i].Data = retained.Data
		}
	}
	for _, row := range links {
		if runner.TaskLinkRecordBytes(after.ID, row) > int(s.policy.Database.MaxRecord) {
			return fmt.Errorf("%w: task-link record exceeds %d bytes", runner.ErrCapacity, s.policy.Database.MaxRecord)
		}
	}
	if after.TaskGraph != nil {
		// Preserved canonical sources must still resolve through the SAME
		// parent's new attempt rows; a caller cannot edit an old proposal and
		// silently strand or replace its already-admitted specifications.
		hydrated, err := runner.UnmarshalRunStorageRecord(record.State, record.Definition, after.Attempts, links)
		if err != nil || !reflect.DeepEqual(hydrated.TaskGraph, runner.Clone(after.TaskGraph)) {
			return errors.New("retained task specification source differs from its parent attempts")
		}
	}
	// Clear only changed memberships before assigning their new positions.
	// Unchanged historical rows neither dirty pages nor grow the journal.
	for i, row := range old {
		if row.ResultPosition != nil && (i >= len(links) || !reflect.DeepEqual(row.ResultPosition, links[i].ResultPosition)) {
			if _, err := tx.ExecContext(ctx, "UPDATE role_task_links SET result_position=NULL WHERE parent_run_id=? AND position=?", after.ID, i); err != nil {
				return err
			}
		}
	}
	// Unmatched legacy references represent current result membership, not
	// reserved children. They may move after newly appended child reservations.
	if _, err := tx.ExecContext(ctx, "DELETE FROM role_task_links WHERE parent_run_id=? AND position>=?", after.ID, oldChildren); err != nil {
		return err
	}
	for i, row := range links {
		if _, err := tx.ExecContext(ctx, `INSERT INTO role_task_links(parent_run_id,position,child_run_id,data,result_position) VALUES(?,?,?,?,?)
ON CONFLICT(parent_run_id,position) DO UPDATE SET data=excluded.data,result_position=excluded.result_position
WHERE role_task_links.data!=excluded.data OR role_task_links.result_position IS NOT excluded.result_position`, after.ID, i, row.ChildRunID, []byte(row.Data), row.ResultPosition); err != nil {
			return err
		}
	}

	return nil
}

// checkTaskLinkAdmission runs after ALL writes, including artifact uses and the
// owner receipt, while the new allocations are still inside one transaction.
func (s *Store) checkTaskLinkAdmission(ctx context.Context, tx *sql.Tx) error {
	// Use actual post-insert SQLite pages, including row/index allocation,
	// instead of charging one maximum-sized record for each tiny link.
	// Explicit pending allocations still own their normal completion reserve.
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM (
SELECT l.child_run_id FROM role_task_links l LEFT JOIN runs child ON child.id=l.child_run_id
WHERE child.id IS NULL AND json_extract(l.data,'$.spec') IS NOT NULL
UNION ALL
SELECT json_extract(link.value,'$.run_id') FROM runs parent,json_each(parent.data,'$.task_graph.children') link
LEFT JOIN runs child ON child.id=json_extract(link.value,'$.run_id')
WHERE json_extract(parent.data,'$.task_link_count') IS NULL AND child.id IS NULL
UNION ALL
SELECT json_extract(link.value,'$.run_id') FROM runs parent,json_each(parent.data,'$.task_groups') task_group,json_each(task_group.value,'$.children') link
LEFT JOIN runs child ON child.id=json_extract(link.value,'$.run_id') WHERE child.id IS NULL
)`).Scan(&pending); err != nil {
		return err
	}
	available, err := s.availableRunSlots(ctx, tx)
	if err != nil {
		return err
	}
	if pending > available {
		return fmt.Errorf("%w: actual task relationship pages leave insufficient pending-run completion reserve", runner.ErrCapacity)
	}
	return nil
}
