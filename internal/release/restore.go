package release

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
)

// RestoreSnapshot is invoked only by the approved recovery container, with the
// application/migration writers already observed stopped. It never deletes
// sidecars or guesses how to recover an open/changed database.
func RestoreSnapshot(ctx context.Context, target, backup, targetSHA, backupSHA string) error {
	if filepath.Base(target) != "games.db" || target == backup || len(targetSHA) != 64 || len(backupSHA) != 64 {
		return errors.New("explicit recovery identities are required")
	}
	original, err := os.Lstat(target)
	if err != nil || !original.Mode().IsRegular() {
		return errors.New("restore target must be a regular database")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(target + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("restore refuses existing SQLite sidecars")
		}
	}
	current, err := fileHash(target)
	if err != nil || current != targetSHA {
		return errors.New("restore target changed after inspection")
	}
	data, err := readFile(backup, 256<<20)
	if err != nil {
		return err
	}
	if Digest(data) != backupSHA {
		return errors.New("approved restore snapshot differs")
	}
	f, err := os.CreateTemp(filepath.Dir(target), ".runner-restore-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(original.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if err = preserveRestoreOwner(name, original); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	db, err := sql.Open("sqlite", "file:"+name+"?mode=ro&immutable=1")
	if err != nil {
		return err
	}
	var integrity string
	err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity)
	closeErr = db.Close()
	if err != nil || closeErr != nil || integrity != "ok" {
		return errors.New("approved restore snapshot is not a valid SQLite database")
	}
	current, err = fileHash(target)
	if err != nil || current != targetSHA {
		return errors.New("restore target changed while preparing replacement")
	}
	latest, err := os.Lstat(target)
	if err != nil || !os.SameFile(original, latest) {
		return errors.New("restore target identity changed")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(target + suffix); !errors.Is(err, os.ErrNotExist) {
			return errors.New("restore target gained SQLite sidecars")
		}
	}
	if err = os.Rename(name, target); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
