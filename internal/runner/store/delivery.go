package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

func (s *Store) Publications(ctx context.Context, task string) ([]core.Publication, error) {
	q := "SELECT data FROM publications"
	var args []any
	if task != "" {
		q += " WHERE task_id=?"
		args = append(args, task)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Publication{}
	for rows.Next() {
		var b []byte
		var p core.Publication
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePublication keeps immutable intent and monotonic create authority separate
// from observed outcomes. Claims share capacity and task ownership with execution.
func (s *Store) SavePublication(ctx context.Context, p core.Publication, claim bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if p.Revision > 0 {
		var b []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM publications WHERE id=?", p.Intent.ID).Scan(&b); err != nil {
			return err
		}
		var old core.Publication
		if err = json.Unmarshal(b, &old); err != nil {
			return err
		}
		if old.Revision != p.Revision {
			return ErrConflict
		}
		if old.Intent != p.Intent || old.CreateRequested && !p.CreateRequested {
			return errors.New("publication intent cannot change or regain create authority")
		}
	}
	if p.Revision == 0 || claim {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", p.Intent.TaskID).Scan(&raw); err != nil {
			return err
		}
		var t core.Task
		if err = json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if !p.Intent.Authorized(t) {
			return ErrConflict
		}
		var drained bool
		if err = tx.QueryRowContext(ctx, "SELECT settings,drained FROM metadata WHERE id=1").Scan(&raw, &drained); err != nil {
			return err
		}
		var settings config.Settings
		if err = json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		if settings.Config.Delivery.Mode != "github-draft" || settings.Config.Delivery.Repository != p.Intent.Repository || settings.Config.Repository.BaseRef != "refs/heads/"+p.Intent.BaseBranch {
			return ErrConflict
		}
		if claim {
			if !p.Occupying || p.State != "running" {
				return errors.New("invalid publication reservation")
			}
			var active, owned int
			if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1)+(SELECT count(*) FROM publications WHERE occupying=1)").Scan(&active); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1 AND task_id=?)+(SELECT count(*) FROM publications WHERE occupying=1 AND task_id=?)", t.ID, t.ID).Scan(&owned); err != nil {
				return err
			}
			if drained || active >= settings.Config.Policy.MaxParallelism || owned != 0 {
				return ErrConflict
			}
		}
	}
	expected := p.Revision
	p.Revision++
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO publications(id,task_id,version,generation,revision,occupying,data) VALUES(?,?,?,?,?,?,?)", p.Intent.ID, p.Intent.TaskID, p.Intent.Version, p.Intent.Generation, p.Revision, p.Occupying, b)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE publications SET revision=?,occupying=?,data=? WHERE id=?", p.Revision, p.Occupying, b, p.Intent.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
