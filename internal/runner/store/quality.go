package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

func (s *Store) Quality(ctx context.Context, revision string) (config.Quality, error) {
	var q config.Quality
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM quality_history WHERE revision=?", revision).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &q)
	}
	if err == nil && q.Revision() != revision {
		err = errors.New("quality history identity mismatch")
	}
	return q, err
}
func (s *Store) SaveQualityReport(ctx context.Context, r config.QualityReport) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO quality_reports(id,data) VALUES(?,?)", r.ID, data)
	return err
}
func (s *Store) QualityReport(ctx context.Context, id string) (config.QualityReport, error) {
	var r config.QualityReport
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM quality_reports WHERE id=?", id).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &r)
	}
	return r, err
}

// ApplyQuality changes only the default. Existing task authority is untouched.
func (s *Store) ApplyQuality(ctx context.Context, previous string, q config.Quality) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRowContext(ctx, "SELECT settings FROM metadata WHERE id=1").Scan(&data); err != nil {
		return err
	}
	var current config.Settings
	if err = json.Unmarshal(data, &current); err != nil {
		return err
	}
	if current.Quality.Revision() != previous {
		return ErrConflict
	}
	if q.Revision() == "" {
		return errors.New("cannot apply an empty quality agreement")
	}
	data, err = json.Marshal(q)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO quality_history(revision,data) VALUES(?,?)", q.Revision(), data); err != nil {
		return err
	}
	current.Quality = q
	data, err = json.Marshal(current)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET settings=? WHERE id=1", data); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AdoptQuality(ctx context.Context, expected int, t core.Task) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRowContext(ctx, "SELECT settings FROM metadata WHERE id=1").Scan(&data); err != nil {
		return err
	}
	var current config.Settings
	if err = json.Unmarshal(data, &current); err != nil {
		return err
	}
	if current.Quality.Revision() != t.Current().Quality {
		return ErrConflict
	}
	if err = writeTask(ctx, tx, expected, t); err != nil {
		return err
	}
	return tx.Commit()
}
