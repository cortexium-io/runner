package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func (s *Store) CheckAuditClock(ctx context.Context, now time.Time) error {
	var observed int64
	if err := s.db.QueryRowContext(ctx, "SELECT audit_clock FROM metadata WHERE id=1").Scan(&observed); err != nil {
		return err
	}
	if now.UnixNano() < observed {
		return errors.New("npm audit clock moved backwards; restore trustworthy time before retrying")
	}
	return nil
}

// A persisted high-water mark makes a backwards wall clock unavailable across
// restart as well. It must catch up; no observation gets a longer validity.
func (s *Store) AuditClock(ctx context.Context, now time.Time) error {
	result, err := s.db.ExecContext(ctx, "UPDATE metadata SET audit_clock=? WHERE id=1 AND audit_clock<=?", now.UnixNano(), now.UnixNano())
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return errors.New("npm audit clock moved backwards; restore trustworthy time before retrying")
	}
	return err
}

func (s *Store) SaveAudit(ctx context.Context, r core.AuditReceipt) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO audit_receipts(id,inputs,policy,image,data) VALUES(?,?,?,?,?)", r.ID, r.Inputs, r.Policy, r.Image, data)
	return err
}

func (s *Store) LatestAudit(ctx context.Context, inputs, policy, image string) (*core.AuditReceipt, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM audit_receipts WHERE inputs=? AND policy=? AND image=? ORDER BY rowid DESC LIMIT 1", inputs, policy, image).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var receipt core.AuditReceipt
	if err = json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}
