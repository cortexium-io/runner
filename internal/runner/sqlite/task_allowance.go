package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) guardTaskAllowanceSave(ctx context.Context, tx *sql.Tx, after, before runner.Run, definition []byte, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	var oldGrants, newGrants []runner.TaskAllowanceGrant
	if before.TaskGraph != nil {
		oldGrants = before.TaskGraph.AllowanceGrants
	}
	if after.TaskGraph != nil {
		newGrants = after.TaskGraph.AllowanceGrants
	}
	var family []runner.Run
	if !reflect.DeepEqual(oldGrants, newGrants) && before.TaskGraph != nil {
		var err error
		family, err = s.loadTaskAllowanceFamily(ctx, tx, before)
		if err != nil {
			return err
		}
		for _, member := range family {
			if member.ID == before.ID {
				before = member
			}
		}
	}
	if err := runner.ValidateTaskAllowanceChange(before, after, receipt, family); err != nil {
		return err
	}
	if len(newGrants) == len(oldGrants)+1 {
		if proof := newGrants[len(oldGrants)].PreviousDefinition; proof != nil {
			if proof.Bytes != int64(len(definition)) || proof.SHA256 != fmt.Sprintf("%x", sha256.Sum256(definition)) {
				return errors.New("previous task definition proof differs from the exact stored definition")
			}
			want := runner.Artifact{ID: proof.ID, RunID: before.ID, ProjectID: before.ProjectID, Bytes: proof.Bytes}
			if len(artifacts) != 1 || artifacts[0] != want {
				return errors.New("definition amendment requires its exact previous definition artifact registration")
			}
			var registered runner.Artifact
			err := tx.QueryRowContext(ctx, "SELECT id,run_id,project,bytes FROM artifacts WHERE id=?", proof.ID).Scan(&registered.ID, &registered.RunID, &registered.ProjectID, &registered.Bytes)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && registered != want {
				return errors.New("previous task definition artifact is already owned by another record")
			}
		}
	}
	return nil
}

func (s *Store) loadTaskAllowanceFamily(ctx context.Context, tx *sql.Tx, root runner.Run) ([]runner.Run, error) {
	rows, err := tx.QueryContext(ctx, "SELECT data,definition FROM runs WHERE project=?", root.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type storedMember struct{ data, definition []byte }
	var selected []storedMember
	for rows.Next() {
		var item storedMember
		if err = rows.Scan(&item.data, &item.definition); err != nil {
			return nil, err
		}
		var header struct {
			TaskGraph *struct {
				RootRunID string `json:"root_run_id"`
			} `json:"task_graph"`
		}
		if err = json.Unmarshal(item.data, &header); err != nil {
			return nil, err
		}
		if header.TaskGraph != nil && header.TaskGraph.RootRunID == root.ID {
			selected = append(selected, item)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	var family []runner.Run
	for _, item := range selected {
		member, err := s.loadRunRecord(ctx, tx, item.data, item.definition)
		if err != nil {
			return nil, err
		}
		family = append(family, member)
	}
	return family, nil
}
