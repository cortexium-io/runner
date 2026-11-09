package release

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedRestorePreservesPermissionsAndRefusesDrift(t *testing.T) {
	for _, scenario := range []string{"restore", "target changed", "backup changed", "sidecar", "invalid snapshot"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "games.db")
			backup := filepath.Join(dir, "backup.db")
			for i, file := range []string{target, backup} {
				db, err := sql.Open("sqlite", file)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec("CREATE TABLE records(id INTEGER); INSERT INTO records VALUES(?)", i+1)
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			os.Chmod(target, 0640)
			if scenario == "invalid snapshot" {
				os.WriteFile(backup, []byte("invalid SQLite"), 0600)
			}
			targetHash, _ := fileHash(target)
			backupHash, _ := fileHash(backup)
			before, _ := os.ReadFile(target)
			switch scenario {
			case "target changed":
				targetHash = strings.Repeat("0", 64)
			case "backup changed":
				backupHash = strings.Repeat("0", 64)
			case "sidecar":
				os.WriteFile(target+"-wal", []byte("pending WAL"), 0600)
			}
			err := RestoreSnapshot(t.Context(), target, backup, targetHash, backupHash)
			after, _ := os.ReadFile(target)
			if scenario != "restore" {
				if err == nil || string(before) != string(after) {
					t.Fatal("unsafe restoration", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(backup)
			info, _ := os.Stat(target)
			if string(after) != string(data) || info.Mode().Perm() != 0640 {
				t.Fatal("restoration bytes or permissions differ")
			}
		})
	}
}
