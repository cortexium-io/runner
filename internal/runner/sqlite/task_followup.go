package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

var _ runner.TaskFollowupStore = (*Store)(nil)

func guardTaskFollowupSave(r runner.Run, previous []byte, authorized bool) error {
	var old runner.Run
	if len(previous) != 0 {
		if err := json.Unmarshal(previous, &old); err != nil {
			return err
		}
	}
	if authorized {
		if len(previous) != 0 || r.Followup == nil {
			return errors.New("linked follow-up admission requires a new retained root")
		}
		return nil
	}
	if !reflect.DeepEqual(old.Followup, r.Followup) {
		return errors.New("follow-up lineage can only be introduced by atomic Start and cannot change or be discarded")
	}
	if old.Followup != nil && (old.ProjectID != r.ProjectID || old.Repository != r.Repository || !reflect.DeepEqual(old.Inputs, r.Inputs)) {
		return errors.New("follow-up project, workspace and inherited inputs cannot change")
	}
	return nil
}

func (s *Store) SaveTaskFollowup(ctx context.Context, target runner.Run, expected []runner.Run, receipt *runner.Receipt) error {
	if target.Followup == nil || len(expected) == 0 || len(expected) > s.runInventoryBound() {
		return errors.New("follow-up requires a complete retained parent family")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Read every matching root/child rather than only the caller's expected IDs:
	// a newly inserted, omitted or foreign child must invalidate the admission.
	rows, err := tx.QueryContext(ctx, "SELECT id,project,data,definition FROM runs ORDER BY id")
	if err != nil {
		return err
	}
	type storedMember struct {
		id, project      string
		data, definition []byte
	}
	var selected []storedMember
	for rows.Next() {
		var id, project string
		var data, definition []byte
		if err = rows.Scan(&id, &project, &data, &definition); err != nil {
			break
		}
		var header runner.Run
		if err = json.Unmarshal(data, &header); err != nil {
			break
		}
		if id != target.Followup.RunID && (header.TaskGraph == nil || header.TaskGraph.RootRunID != target.Followup.RunID) {
			continue
		}
		selected = append(selected, storedMember{id: id, project: project, data: data, definition: definition})
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
	if len(selected) != len(expected) {
		return errors.New("follow-up parent family changed before transaction")
	}
	wanted := make(map[string]runner.Run, len(expected))
	for _, r := range expected {
		if wanted[r.ID].ID != "" {
			return errors.New("follow-up expected parent family has duplicate identities")
		}
		wanted[r.ID] = r
	}
	family := make([]runner.Run, 0, len(selected))
	var parent runner.Run
	for _, member := range selected {
		run, err := s.loadRunRecord(ctx, tx, member.data, member.definition)
		if err != nil {
			return err
		}
		if run.ID != member.id || run.ProjectID != member.project {
			return errors.New("follow-up parent row identity differs")
		}
		if !reflect.DeepEqual(run, wanted[run.ID]) {
			return errors.New("follow-up parent history changed before transaction")
		}
		family = append(family, run)
		if run.ID == target.Followup.RunID {
			parent = run
		}
	}
	if err = runner.ValidateTaskFollowup(target, family, receipt); err != nil {
		return err
	}
	for _, value := range target.Inputs {
		if value.ArtifactID == "" {
			continue
		}
		var artifact runner.Artifact
		if err = tx.QueryRowContext(ctx, "SELECT id,run_id,project,bytes FROM artifacts WHERE id=?", value.ArtifactID).Scan(&artifact.ID, &artifact.RunID, &artifact.ProjectID, &artifact.Bytes); err != nil {
			return err
		}
		if artifact.ProjectID != target.ProjectID && !runner.TaskFollowupAllowsImport(parent, artifact) {
			return errors.New("follow-up imported artifact is not an inherited parent input")
		}
	}
	if err = s.saveRunAuthorized(ctx, tx, target, nil, receipt, saveAuthorization{followup: true}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) guardTaskFollowupDelete(ctx context.Context, tx *sql.Tx, ids []string) error {
	selection, args, err := s.familySelection(ids)
	if err != nil {
		return err
	}
	var external int
	if err := tx.QueryRowContext(ctx, selection+`SELECT EXISTS(
SELECT 1 FROM runs source JOIN family selected ON selected.id=source.id
JOIN runs member ON json_extract(CAST(member.data AS TEXT),'$.task_graph.root_run_id')=json_extract(CAST(source.data AS TEXT),'$.task_graph.root_run_id')
LEFT JOIN family included ON included.id=member.id WHERE included.id IS NULL)`, args...).Scan(&external); err != nil {
		return err
	}
	if external != 0 {
		return errors.New("general task deletion requires its complete retained graph")
	}
	if err := tx.QueryRowContext(ctx, selection+`SELECT EXISTS(
SELECT 1 FROM runs source JOIN family producer ON producer.id=source.id
JOIN runs follower ON json_extract(CAST(follower.data AS TEXT),'$.followup.run_id')=json_extract(CAST(source.data AS TEXT),'$.task_graph.root_run_id')
LEFT JOIN family consumer ON consumer.id=follower.id WHERE consumer.id IS NULL)`, args...).Scan(&external); err != nil {
		return err
	}
	if external != 0 {
		return errors.New("completed parent is retained by a linked follow-up")
	}
	return nil
}
