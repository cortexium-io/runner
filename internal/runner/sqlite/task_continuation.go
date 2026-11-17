package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

var _ runner.TaskContinuationStore = (*Store)(nil)

func (s *Store) guardTaskContinuationSave(ctx context.Context, tx *sql.Tx, r runner.Run, previous []byte, continuation bool) error {
	var old runner.Run
	if len(previous) != 0 {
		if err := json.Unmarshal(previous, &old); err != nil {
			return err
		}
	}
	if old.ContinuedBy != nil {
		return errors.New("retained source family is frozen by its task continuation")
	}
	link := r.Task
	if old.Task != nil {
		link = old.Task
	}
	if link != nil {
		var rootData []byte
		if err := tx.QueryRowContext(ctx, "SELECT data FROM runs WHERE id=?", link.Identity.RootRunID).Scan(&rootData); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var root runner.Run
		if len(rootData) != 0 {
			if err := json.Unmarshal(rootData, &root); err != nil {
				return err
			}
		}
		if root.ContinuedBy != nil {
			return errors.New("retained source child is frozen by its root task continuation")
		}
	}
	var before, after *runner.TaskContinuationLineage
	if old.TaskGraph != nil {
		before = old.TaskGraph.Lineage
	}
	if r.TaskGraph != nil {
		after = r.TaskGraph.Lineage
	}
	if !continuation && (r.ContinuedBy != nil || !reflect.DeepEqual(before, after)) {
		return errors.New("retained task lineage can only be introduced atomically and cannot change or be discarded")
	}
	return nil
}

func (s *Store) SaveTaskContinuation(ctx context.Context, origin, target runner.Run, expected []runner.Run, receipt *runner.Receipt) error {
	if len(expected) == 0 || len(expected) > s.runInventoryBound() || expected[0].ID != origin.ID {
		return errors.New("task continuation needs the complete bounded original family")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Authenticate every normalized history and frozen definition in the same
	// transaction that links both roots. Neither a changed child nor a caller's
	// private count can manufacture remaining allowance.
	family := make([]runner.Run, 0, len(expected))
	for _, want := range expected {
		var data, definition []byte
		if err = tx.QueryRowContext(ctx, "SELECT data,definition FROM runs WHERE id=? AND project=?", want.ID, want.ProjectID).Scan(&data, &definition); err != nil {
			return err
		}
		got, err := s.loadRunRecord(ctx, tx, data, definition)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, want) {
			return errors.New("task continuation original family changed before transaction")
		}
		family = append(family, got)
	}
	if err = runner.ValidateTaskContinuation(origin, target, family, receipt); err != nil {
		return err
	}
	// Ensure an omitted or newly inserted source child cannot evade the debit.
	rows, err := tx.QueryContext(ctx, "SELECT id,data FROM runs WHERE project=?", origin.ProjectID)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, member := range family {
		ids[member.ID] = true
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
		if r.Task != nil && r.Task.Identity.RootRunID == origin.ID && !ids[id] {
			err = errors.New("task continuation omitted an original source child")
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
	lineage := target.TaskGraph.Lineage
	for _, id := range []string{lineage.Source.PatchID, lineage.CheckID} {
		var registered runner.Artifact
		if err = tx.QueryRowContext(ctx, "SELECT id,run_id,project,bytes FROM artifacts WHERE id=?", id).Scan(&registered.ID, &registered.RunID, &registered.ProjectID, &registered.Bytes); err != nil {
			return err
		}
		if registered.RunID != origin.ID || registered.ProjectID != origin.ProjectID || registered.Bytes <= 0 {
			return errors.New("task continuation imported artifact ownership differs")
		}
	}
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM runs WHERE id=?", target.ID).Scan(&exists); err != nil || exists != 0 {
		return errors.New("task continuation target already exists")
	}
	state, _, err := runner.MarshalRunRecord(origin)
	if err != nil {
		return err
	}
	if len(state) > int(s.policy.Database.MaxRecord) {
		return runner.ErrCapacity
	}
	// Only the origin state gains the audit. Its definition, attempt rows,
	// existing receipts and artifact references retain their original bytes.
	if _, err = tx.ExecContext(ctx, "UPDATE runs SET data=? WHERE id=?", state, origin.ID); err != nil {
		return err
	}
	if err = s.saveRunAuthorized(ctx, tx, target, nil, receipt, saveAuthorization{continuation: true}); err != nil {
		return err
	}
	return tx.Commit()
}

// Retention may remove a continuation only with both linked families. Artifact
// uses protect source bytes; these identity links also protect failed history
// and prevent a completed new root leaving an unreadable dangling audit.
func (s *Store) guardTaskContinuationDelete(ctx context.Context, tx *sql.Tx, ids []string) error {
	selected := make(map[string]bool, len(ids))
	for _, id := range ids {
		selected[id] = true
	}
	load := func(id string) (runner.Run, error) {
		var data, definition []byte
		err := tx.QueryRowContext(ctx, "SELECT data,definition FROM runs WHERE id=?", id).Scan(&data, &definition)
		if errors.Is(err, sql.ErrNoRows) {
			return runner.Run{}, nil
		}
		var r runner.Run
		if err == nil {
			r, err = s.loadRunRecord(ctx, tx, data, definition)
		}
		return r, err
	}
	for _, id := range ids {
		r, err := load(id)
		if err != nil {
			return err
		}
		linked := false
		if r.Task != nil {
			root, err := load(r.Task.Identity.RootRunID)
			if err != nil {
				return err
			}
			if root.ContinuedBy != nil && !selected[root.ID] {
				return errors.New("continued source child must expire with its linked families")
			}
		}
		if r.ContinuedBy != nil {
			if !selected[r.ContinuedBy.RunID] {
				return errors.New("continued source history must expire with its new task family")
			}
			for _, group := range r.TaskGroups {
				for _, child := range group.Children {
					if !selected[child.RunID] {
						return errors.New("continued source retention omitted an old child")
					}
				}
			}
		}
		if r.TaskGraph != nil {
			root := r
			if root.TaskGraph.RootRunID != r.ID {
				root, err = load(root.TaskGraph.RootRunID)
				if err != nil {
					return err
				}
			}
			linked = root.TaskGraph != nil && root.TaskGraph.Lineage != nil
			if linked && (!selected[root.ID] || !selected[root.TaskGraph.Lineage.OriginRunID]) {
				return errors.New("task continuation must expire with its original source history")
			}
			if linked {
				for _, child := range r.TaskGraph.Children {
					if !selected[child.RunID] {
						return errors.New("continued task retention omitted a descendant")
					}
				}
			}
		}
	}
	return nil
}
