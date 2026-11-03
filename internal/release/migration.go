package release

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	_ "modernc.org/sqlite"
)

const migrationName = "app/0017_remove_ready_supply.sql"
const migrationChecksum = "b4933b29a4f7662a9ff2e1e0c866072783c4439c556f543730f039514c538cd4"

var removedTables = []string{"ready_supply_capacity", "ready_supply_choices", "ready_supply_records"}

type databaseState struct {
	Tables   map[string]string
	Ledger   map[string][2]string
	LockID   int
	LockTime string
}

// Reads only private consistent snapshots, never an open application database.
// Contents are hashed in memory; receipts contain no application rows.
func inspectDatabase(file string) (databaseState, error) {
	s := databaseState{Tables: map[string]string{}, Ledger: map[string][2]string{}}
	db, err := sql.Open("sqlite", "file:"+file+"?mode=ro")
	if err != nil {
		return s, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return s, errors.New("snapshot integrity failed")
	}
	fk, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return s, err
	}
	hasForeignFailure := fk.Next()
	fkErr := fk.Err()
	fk.Close()
	if hasForeignFailure || fkErr != nil {
		return s, errors.New("snapshot foreign-key check failed")
	}
	rows, err := db.Query("SELECT name,sql FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return s, err
	}
	type table struct{ name, definition string }
	var tables []table
	for rows.Next() {
		var t table
		if err = rows.Scan(&t.name, &t.definition); err != nil {
			rows.Close()
			return s, err
		}
		tables = append(tables, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return s, err
	}
	for _, t := range tables {
		if t.name == "_genny_migrations" || t.name == "_genny_migration_lock" {
			continue
		}
		quoted := `"` + strings.ReplaceAll(t.name, `"`, `""`) + `"`
		data, err := db.Query("SELECT * FROM " + quoted + " LIMIT 0")
		if err != nil {
			return s, err
		}
		columns, err := data.Columns()
		data.Close()
		if err != nil {
			return s, err
		}
		order := make([]string, len(columns))
		for i := range columns {
			order[i] = fmt.Sprint(i + 1)
		}
		data, err = db.Query("SELECT * FROM " + quoted + " ORDER BY " + strings.Join(order, ","))
		if err != nil {
			return s, err
		}
		h := sha256.New()
		_, _ = h.Write([]byte(t.definition + "\n"))
		for data.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = data.Scan(pointers...); err != nil {
				data.Close()
				return s, err
			}
			encoded, e := json.Marshal(values)
			if e != nil {
				data.Close()
				return s, e
			}
			_, _ = h.Write(encoded)
			_, _ = h.Write([]byte{10})
		}
		err = data.Err()
		data.Close()
		if err != nil {
			return s, err
		}
		s.Tables[t.name] = hex.EncodeToString(h.Sum(nil))
	}
	ledger, err := db.Query("SELECT name,checksum,CAST(applied_at AS TEXT) FROM _genny_migrations ORDER BY name")
	if err != nil {
		return s, err
	}
	for ledger.Next() {
		var name, checksum, at string
		if err = ledger.Scan(&name, &checksum, &at); err != nil {
			ledger.Close()
			return s, err
		}
		s.Ledger[name] = [2]string{checksum, at}
	}
	err = ledger.Err()
	ledger.Close()
	if err != nil {
		return s, err
	}
	if err = db.QueryRow("SELECT id,CAST(locked_at AS TEXT) FROM _genny_migration_lock").Scan(&s.LockID, &s.LockTime); err != nil {
		return s, err
	}
	var locks int
	if err = db.QueryRow("SELECT COUNT(*) FROM _genny_migration_lock").Scan(&locks); err != nil || locks != 1 || s.LockID != 1 {
		return s, errors.New("migration lock identity differs")
	}
	return s, nil
}
func validateMigrationInput(file string) error {
	s, err := inspectDatabase(file)
	if err != nil {
		return err
	}
	if _, exists := s.Ledger[migrationName]; exists {
		return errors.New("migration is already applied")
	}
	for _, table := range removedTables {
		if _, exists := s.Tables[table]; !exists {
			return errors.New("expected stock table is missing")
		}
	}
	return nil
}
func verifyMigration(beforeFile, afterFile string) error {
	before, err := inspectDatabase(beforeFile)
	if err != nil {
		return err
	}
	after, err := inspectDatabase(afterFile)
	if err != nil {
		return err
	}
	for name, hash := range before.Tables {
		if slices.Contains(removedTables, name) {
			if _, exists := after.Tables[name]; exists {
				return errors.New("stock table was retained")
			}
			continue
		}
		if after.Tables[name] != hash {
			return errors.New("application table changed")
		}
	}
	if len(after.Tables) != len(before.Tables)-len(removedTables) {
		return errors.New("unexpected table inventory after migration")
	}
	if len(after.Ledger) != len(before.Ledger)+1 {
		return errors.New("migration ledger length differs")
	}
	for name, row := range before.Ledger {
		if after.Ledger[name] != row {
			return errors.New("existing migration ledger row changed")
		}
	}
	added := after.Ledger[migrationName]
	if added[0] != migrationChecksum || added[1] == "" {
		return errors.New("new migration ledger identity differs")
	}
	if after.LockID != before.LockID || after.LockTime < before.LockTime {
		return errors.New("migration lock identity/time regressed")
	}
	return nil
}
