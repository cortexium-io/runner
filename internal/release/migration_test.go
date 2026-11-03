package release

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrationPreservesApplicationDataAndExistingLedger(t *testing.T) {
	dir := t.TempDir()
	before := filepath.Join(dir, "before.db")
	db, err := sql.Open("sqlite", before)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"CREATE TABLE application(id INTEGER PRIMARY KEY,value TEXT)",
		"INSERT INTO application VALUES(1,'retained')",
		"CREATE TABLE _genny_migrations(name TEXT PRIMARY KEY,checksum TEXT,applied_at TEXT)",
		"INSERT INTO _genny_migrations VALUES('old','old-checksum','2026-01-01')",
		"CREATE TABLE _genny_migration_lock(id INTEGER PRIMARY KEY,locked_at INTEGER)",
		"INSERT INTO _genny_migration_lock VALUES(1,100)",
		"CREATE TABLE ready_supply_capacity(id INTEGER)",
		"CREATE TABLE ready_supply_choices(id INTEGER)",
		"CREATE TABLE ready_supply_records(id INTEGER)",
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if err = validateMigrationInput(before); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"correct", "application row", "old ledger", "wrong checksum", "extra table"} {
		t.Run(change, func(t *testing.T) {
			after := filepath.Join(dir, change+".db")
			data, _ := os.ReadFile(before)
			os.WriteFile(after, data, 0600)
			d, e := sql.Open("sqlite", after)
			if e != nil {
				t.Fatal(e)
			}
			for _, name := range removedTables {
				if _, e = d.Exec("DROP TABLE " + name); e != nil {
					t.Fatal(e)
				}
			}
			_, e = d.Exec("INSERT INTO _genny_migrations VALUES(?,?,?)", migrationName, migrationChecksum, "2026-11-03")
			if e != nil {
				t.Fatal(e)
			}
			d.Exec("UPDATE _genny_migration_lock SET locked_at=101")
			switch change {
			case "application row":
				d.Exec("UPDATE application SET value='lost'")
			case "old ledger":
				d.Exec("UPDATE _genny_migrations SET applied_at='changed' WHERE name='old'")
			case "wrong checksum":
				d.Exec("UPDATE _genny_migrations SET checksum='wrong' WHERE name=?", migrationName)
			case "extra table":
				d.Exec("CREATE TABLE unexpected(id INTEGER)")
			}
			d.Close()
			err := verifyMigration(before, after)
			if (err == nil) != (change == "correct") {
				t.Fatal(change, err)
			}
		})
	}
}
