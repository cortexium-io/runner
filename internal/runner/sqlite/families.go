package sqlite

import (
	"context"
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

var _ runner.FamilyStore = (*Store)(nil)

// familySelection contains placeholders only; run identities remain parameters.
// Missing rows are allowed so a lost acknowledgement can safely rejoin deletion.
func (s *Store) familySelection(ids []string) (string, []any, error) {
	if len(ids) == 0 || len(ids) > s.maxRuns {
		return "", nil, errors.New("family deletion needs a nonempty bounded run set")
	}
	seen := make(map[string]bool, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		if id == "" || seen[id] {
			return "", nil, errors.New("family run identities must be nonempty and unique")
		}
		seen[id] = true
		args[i] = id
	}
	return "WITH family(id) AS (VALUES " + strings.TrimSuffix(strings.Repeat("(?),", len(ids)), ",") + ") ", args, nil
}

const externalFamilyConsumers = `SELECT EXISTS(
SELECT 1 FROM artifacts a JOIN artifact_uses u ON u.artifact=a.id
JOIN family producer ON producer.id=a.run_id
LEFT JOIN family consumer ON consumer.id=u.run_id
WHERE consumer.id IS NULL)`

func (s *Store) HasExternalConsumers(ctx context.Context, ids []string) (bool, error) {
	selection, args, err := s.familySelection(ids)
	if err != nil {
		return false, err
	}
	var found int
	err = s.db.QueryRowContext(ctx, selection+externalFamilyConsumers, args...).Scan(&found)
	return found != 0, err
}

// DeleteFamily rechecks references in the mutation transaction. Removing family
// uses before owned artifacts permits internal root/child cycles while retaining
// every outside consumer and the artifacts it depends on.
func (s *Store) DeleteFamily(ctx context.Context, ids []string) error {
	selection, args, err := s.familySelection(ids)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var external int
	if err = tx.QueryRowContext(ctx, selection+externalFamilyConsumers, args...).Scan(&external); err != nil {
		return err
	}
	if external != 0 {
		return errors.New("retained family has external artifact consumers")
	}
	for _, statement := range []string{
		"DELETE FROM artifact_uses WHERE run_id IN (SELECT id FROM family)",
		"DELETE FROM artifacts WHERE run_id IN (SELECT id FROM family)",
		"DELETE FROM runs WHERE id IN (SELECT id FROM family)",
	} {
		if _, err = tx.ExecContext(ctx, selection+statement, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}
