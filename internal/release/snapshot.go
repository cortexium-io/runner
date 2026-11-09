package release

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"modernc.org/sqlite"
)

type SnapshotProof struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	SourceSHA   string `json:"source_sha256"`
	SnapshotSHA string `json:"snapshot_sha256"`
}

// SnapshotSelfTest exercises the protected closed-WAL boundary using only a
// fresh owned fixture beneath the configured backup directory.
func SnapshotSelfTest(ctx context.Context, directory string) error {
	root, err := os.MkdirTemp(directory, ".runner-snapshot-probe-")
	if err != nil {
		return err
	}
	protected := filepath.Join(root, "protected")
	defer func() { _ = os.Chmod(protected, 0700); _ = os.RemoveAll(root) }()
	if err = os.Mkdir(protected, 0700); err != nil {
		return err
	}
	source := filepath.Join(protected, "games.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL; CREATE TABLE probe(id INTEGER PRIMARY KEY); INSERT INTO probe VALUES(1)")
	closeErr := db.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Chmod(source, 0444); err != nil {
		return err
	}
	if err = os.Chmod(protected, 0555); err != nil {
		return err
	}
	stopped := func() error { return nil }
	before, err := QuiescedFingerprint(source, stopped)
	if err != nil {
		return err
	}
	destination := filepath.Join(root, "snapshot.db")
	if err = QuiescedSnapshot(ctx, source, destination, stopped); err != nil {
		return err
	}
	after, err := QuiescedFingerprint(source, stopped)
	if err != nil || after != before {
		return errors.New("protected source changed during permission probe")
	}
	if err = QuiescedSnapshot(ctx, source, destination, stopped); err == nil {
		return errors.New("snapshot probe accepted an existing destination")
	}
	copy, err := sql.Open("sqlite", "file:"+destination+"?mode=ro")
	if err != nil {
		return err
	}
	defer copy.Close()
	var rows int
	if err = copy.QueryRowContext(ctx, "SELECT count(*) FROM probe").Scan(&rows); err != nil || rows != 1 {
		return errors.New("snapshot probe lost committed rows")
	}
	return nil
}

// QuiescedFingerprint observes bytes only; it opens no SQLite connection and
// creates no files. Recovery uses it to prove a failed backup did not alter data.
func QuiescedFingerprint(source string, stopped func() error) (string, error) {
	if stopped == nil {
		return "", errors.New("stopped-writer proof is required")
	}
	if err := stopped(); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(source + suffix); !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("closed-data proof requires absent SQLite sidecars")
		}
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("closed-data proof requires a regular database")
	}
	f, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash, err := hashReader(f)
	if err != nil {
		return "", err
	}
	if err = stopped(); err != nil {
		return "", err
	}
	return hash, nil
}

// QuiescedSnapshot uses SQLite's backup API without creating source sidecars.
// Immutable access is permitted only with externally observed stopped writers,
// absent WAL/SHM files and unchanged source bytes before/after the copy.
func QuiescedSnapshot(ctx context.Context, source, destination string, stopped func() error) (err error) {
	if stopped == nil {
		return errors.New("stopped-writer proof is required")
	}
	check := func() error {
		if err := stopped(); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Lstat(source + suffix); !errors.Is(err, os.ErrNotExist) {
				return errors.New("source has SQLite sidecars; immutable backup is forbidden")
			}
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("snapshot source must be an existing regular file")
	}
	hashFile := func(path string) (string, error) {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		return hashReader(f)
	}
	before, err := hashFile(source)
	if err != nil {
		return err
	}
	created, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	created.Close()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(destination)
		}
	}()
	uri := (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	err = conn.Raw(func(raw any) error {
		backup, err := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		}).NewBackup(destination)
		if err != nil {
			return err
		}
		for {
			if ctx.Err() != nil {
				_ = backup.Finish()
				return ctx.Err()
			}
			more, stepErr := backup.Step(256)
			if stepErr != nil {
				_ = backup.Finish()
				return stepErr
			}
			if !more {
				break
			}
		}
		return backup.Finish()
	})
	if err != nil {
		return err
	}
	if err = check(); err != nil {
		return err
	}
	after, err := hashFile(source)
	if err != nil || after != before {
		return errors.New("quiesced source changed during backup")
	}
	copy, err := sql.Open("sqlite", "file:"+destination+"?mode=ro")
	if err != nil {
		return err
	}
	defer copy.Close()
	var integrity string
	if err = copy.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("quiesced snapshot integrity failed")
	}
	copyHash, err := hashFile(destination)
	if err != nil {
		return err
	}
	proof, err := json.Marshal(SnapshotProof{
		Source:      source,
		Destination: destination,
		SourceSHA:   before,
		SnapshotSHA: copyHash,
	})
	if err != nil {
		return err
	}
	if err = writeExclusive(destination+".verified", proof); err != nil {
		return err
	}
	success = true
	return nil
}

func CheckSnapshotWriterStopped(ctx context.Context, engine, id, image, source string) error {
	return CheckSnapshotWriterStoppedForProject(ctx, engine, id, image, source, "games-site")
}

func CheckSnapshotWriterStoppedForProject(ctx context.Context, engine, id, image, source, project string) error {
	if !safeID.MatchString(project) {
		return errors.New("snapshot project identity is invalid")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return err
	}
	b := Live{Config: Config{
		Docker:        docker,
		DockerHost:    "unix:///var/run/docker.sock",
		DeployProject: project,
		DataDir:       filepath.Dir(source),
	}}
	actual, err := b.docker(ctx, "info", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(actual)) != engine {
		return errors.New("snapshot engine identity differs")
	}
	j := Job{Receipt: Receipt{PriorContainer: id, PriorImage: image}}
	c, err := b.existing(ctx, &j)
	if err != nil {
		return err
	}
	if c.State.Running {
		return errors.New("snapshot writer is running")
	}
	return nil
}
