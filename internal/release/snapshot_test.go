package release

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestQuiescedSnapshotProtectedWALDirectoryAndRefusals(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "games.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE important(id INTEGER PRIMARY KEY,value TEXT); INSERT INTO important VALUES(1,'preserved')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(source, 0444)
	os.Chmod(dir, 0555)
	defer os.Chmod(dir, 0700)
	destination := filepath.Join(root, "snapshot.db")
	proofCalls := 0
	if err = QuiescedSnapshot(t.Context(), source, destination, func() error { proofCalls++; return nil }); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(source)
	if string(after) != string(before) || proofCalls != 2 {
		t.Fatal("source changed or writer proof was skipped")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatal("source sidecars were created")
	}
	copy, err := sql.Open("sqlite", destination)
	if err != nil {
		t.Fatal(err)
	}
	var value string
	if err = copy.QueryRow("SELECT value FROM important WHERE id=1").Scan(&value); err != nil || value != "preserved" {
		t.Fatal("committed rows missing", err)
	}
	copy.Close()
	marker, err := os.ReadFile(destination + ".verified")
	if err != nil {
		t.Fatal(err)
	}
	var proof SnapshotProof
	if json.Unmarshal(marker, &proof) != nil || proof.SourceSHA != Digest(before) {
		t.Fatal("completion proof differs")
	}
	if err = QuiescedSnapshot(t.Context(), source, destination, func() error { return nil }); err == nil {
		t.Fatal("existing backup overwritten")
	}
	if err = QuiescedSnapshot(t.Context(), source, filepath.Join(root, "unsafe.db"), func() error { return errors.New("writer running") }); err == nil {
		t.Fatal("active writer accepted")
	}
	os.Chmod(dir, 0700)
	os.WriteFile(source+"-wal", []byte("pending"), 0600)
	if err = QuiescedSnapshot(t.Context(), source, filepath.Join(root, "wal.db"), func() error { return nil }); err == nil {
		t.Fatal("pending WAL ignored")
	}
}
