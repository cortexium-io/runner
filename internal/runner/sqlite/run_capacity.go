package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/cortexium-io/runner/internal/runner"
)

var _ runner.RunCapacityStore = (*Store)(nil)

func (s *Store) AvailableRunSlots(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	return s.availableRunSlots(ctx, tx)
}

func (s *Store) availableRunSlots(ctx context.Context, tx *sql.Tx) (int, error) {
	var pages, free, maximum, pageSize int64
	for _, query := range []struct {
		sql   string
		value *int64
	}{
		{"PRAGMA page_count", &pages},
		{"PRAGMA freelist_count", &free},
		{"PRAGMA max_page_count", &maximum},
		{"PRAGMA page_size", &pageSize},
	} {
		if err := tx.QueryRowContext(ctx, query.sql).Scan(query.value); err != nil {
			return 0, err
		}
	}
	if pageSize != 4096 || pages < free || free < 0 || maximum < pages {
		return 0, errors.New("database page inventory differs from its owned byte allowance")
	}
	maximum = min(maximum, int64(s.policy.Database.MaxSize)/3/pageSize)
	// Retain the existing 128KiB recovery margin and a maximum-sized state,
	// definition and initial attempt for each admitted, unsettled run. Its
	// observed pages already count above; this conservative extra reservation
	// keeps later admissions from consuming its completion/recovery headroom.
	// Settled history consumes only its observed pages.
	remaining := maximum - (pages - free) - (128<<10)/pageSize
	recordPages := (int64(s.policy.Database.MaxRecord)+pageSize-1)/pageSize + 2
	var unsettled int64
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM runs
WHERE json_extract(data,'$.status') NOT IN ('Completed','Failed','Cancelled')
OR coalesce(json_extract(data,'$.resources_released'),0) != 1`).Scan(&unsettled); err != nil {
		return 0, err
	}
	slots := max(int64(0), remaining/(3*recordPages)-unsettled)
	return int(min(int64(math.MaxInt), slots)), nil
}

func (s *Store) runInventoryBound() int {
	if s.maxRuns > 0 {
		return s.maxRuns
	}
	return s.policy.RetainedRunInventoryBound()
}
