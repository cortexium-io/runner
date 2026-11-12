// Package sqlite persists bounded current state. It is not a transcript/event store.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner"
	_ "modernc.org/sqlite"
)

type Store struct {
	db      *sql.DB
	guard   *os.File
	policy  runner.StoragePolicy
	maxRuns int
}

func Open(dir string, p runner.StoragePolicy) (*Store, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	rootInfo, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !rootInfo.IsDir() || rootInfo.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state directory must be a private directory (0700)")
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return nil, e
	}
	for _, ent := range entries {
		switch ent.Name() {
		case "process-core.sqlite", "process-core.sqlite-journal", "instance.lock", "owner.token", "jobs", "work", "artifacts", "logs", "native", "forensics":
		default:
			return nil, fmt.Errorf("refusing non-process-core state directory: unexpected %s", ent.Name())
		}
	}
	g, e := lock(dir)
	if e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "process-core.sqlite")
	if i, e := os.Lstat(path); e == nil && !i.Mode().IsRegular() {
		g.Close()
		return nil, errors.New("database must be a regular file")
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	db, e := sql.Open("sqlite", uri)
	if e != nil {
		g.Close()
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, guard: g, policy: p, maxRuns: p.Database.MaxRuns}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()
	// DELETE journaling avoids unbounded WAL readers in this single-writer first slice.
	// Reserve 2/3 of the database allowance for journal/index/maintenance overhead.
	pages := int64(p.Database.MaxSize) / 3 / 4096
	for _, q := range []string{
		"PRAGMA page_size=4096",
		"PRAGMA journal_mode=DELETE",
		"PRAGMA synchronous=FULL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=3000",
		"PRAGMA temp_store=MEMORY",
		fmt.Sprintf("PRAGMA max_page_count=%d", pages),
	} {
		if _, e = db.Exec(q); e != nil {
			return nil, e
		}
	}
	var actualPages, pageSize int64
	if e = db.QueryRow("PRAGMA max_page_count").Scan(&actualPages); e != nil {
		return nil, e
	}
	if e = db.QueryRow("PRAGMA page_size").Scan(&pageSize); e != nil {
		return nil, e
	}
	if actualPages > pages || pageSize != 4096 {
		return nil, errors.New("existing database exceeds the configured capacity; restore the previous allowance")
	}
	var version int
	if e = db.QueryRow("PRAGMA user_version").Scan(&version); e != nil {
		return nil, e
	}
	if version < 0 || version > 3 {
		return nil, errors.New("unsupported process-core database format")
	}
	if version == 0 {
		var tables int
		if e = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables); e != nil {
			return nil, e
		}
		if tables != 0 {
			return nil, errors.New("database is not a process-core database; use separate state")
		}
		_, e = db.Exec(`BEGIN;
CREATE TABLE runs(id TEXT PRIMARY KEY, project TEXT NOT NULL, data BLOB NOT NULL, definition BLOB);
CREATE TABLE requests(key TEXT PRIMARY KEY, kind TEXT NOT NULL, args BLOB NOT NULL, run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE);
CREATE TABLE artifacts(id TEXT PRIMARY KEY, run_id TEXT NOT NULL REFERENCES runs(id), project TEXT NOT NULL, bytes INTEGER NOT NULL);
CREATE TABLE artifact_uses(artifact TEXT NOT NULL REFERENCES artifacts(id), run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE, PRIMARY KEY(artifact,run_id));
PRAGMA user_version=2; COMMIT;`)
		if e != nil {
			return nil, e
		}
		version = 2
	}
	if version == 1 {
		if _, e = db.Exec("BEGIN; ALTER TABLE runs ADD COLUMN definition BLOB; PRAGMA user_version=2; COMMIT;"); e != nil {
			return nil, e
		}
		version = 2
	}
	if version == 2 {
		if _, e = db.Exec(`BEGIN;
CREATE TABLE attempts(run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
position INTEGER NOT NULL CHECK(position>=0 AND position<128), id TEXT NOT NULL UNIQUE, data BLOB NOT NULL,
PRIMARY KEY(run_id,position));
PRAGMA user_version=3; COMMIT;`); e != nil {
			return nil, e
		}
	}
	if e = os.Chmod(path, 0600); e != nil {
		return nil, e
	}
	byCapacity := (int64(p.Database.MaxSize)/3 - 128*1024) / (3 * int64(p.Database.MaxRecord))
	if byCapacity < 1 {
		return nil, errors.New("database allowance cannot reserve one run plus recovery space")
	}
	if int64(s.maxRuns) > byCapacity {
		s.maxRuns = int(byCapacity)
	}
	ok = true
	return s, nil
}
func (s *Store) Close() error { e := s.db.Close(); _ = s.guard.Close(); return e }
func (s *Store) Load(ctx context.Context) ([]runner.Run, error) {
	tx, e := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT id,project,data,definition FROM runs ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var out []runner.Run
	var counts []int
	for rows.Next() {
		var id, project string
		var b, definition []byte
		if e = rows.Scan(&id, &project, &b, &definition); e != nil {
			return nil, e
		}
		if len(b) > int(s.policy.Database.MaxRecord) || len(definition) > int(s.policy.Database.MaxRecord) {
			return nil, errors.New("oversized stored run")
		}
		r, e := runner.UnmarshalRunRecord(b, definition)
		if e != nil {
			return nil, e
		}
		if r.ID != id || r.ProjectID != project {
			return nil, errors.New("stored run identity differs")
		}
		count, e := storedAttemptCount(b)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
		counts = append(counts, count)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	for i := range out {
		if e = s.loadAttempts(ctx, tx, &out[i], counts[i]); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return out, nil
}
func (s *Store) Save(ctx context.Context, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.saveRun(ctx, tx, r, artifacts, receipt); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) saveRun(ctx context.Context, tx *sql.Tx, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	b, definition, e := runner.MarshalRunRecord(r)
	if e != nil {
		return e
	}
	if len(b) > int(s.policy.Database.MaxRecord) || len(definition) > int(s.policy.Database.MaxRecord) {
		return fmt.Errorf("%w: run record exceeds %d bytes", runner.ErrCapacity, s.policy.Database.MaxRecord)
	}
	attempts, e := s.attemptRecords(r)
	if e != nil {
		return e
	}
	var previous []byte
	e = tx.QueryRowContext(ctx, "SELECT data FROM runs WHERE id=?", r.ID).Scan(&previous)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if errors.Is(e, sql.ErrNoRows) {
		var n int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&n); e != nil {
			return e
		}
		if n >= s.maxRuns {
			return fmt.Errorf("%w: retained run limit %d", runner.ErrCapacity, s.maxRuns)
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO runs(id,project,data,definition) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,definition=excluded.definition", r.ID, r.ProjectID, b, definition); e != nil {
		return e
	}
	if e = s.saveAttempts(ctx, tx, r, attempts, previous); e != nil {
		return e
	}
	for _, a := range artifacts {
		if _, e = tx.ExecContext(ctx, "INSERT INTO artifacts(id,run_id,project,bytes) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", a.ID, a.RunID, a.ProjectID, a.Bytes); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM artifact_uses WHERE run_id=?", r.ID); e != nil {
		return e
	}
	refs := map[string]bool{}
	add := func(m map[string]runner.Value) {
		for _, v := range m {
			if v.ArtifactID != "" {
				refs[v.ArtifactID] = true
			}
		}
	}
	add(r.Inputs)
	add(r.Outputs)
	for _, a := range r.Attempts {
		if a.End != nil && a.End.Result != nil {
			add(a.End.Result.Outputs)
		}
	}
	for _, group := range r.TaskGroups {
		if group == nil {
			continue
		}
		add(group.Evidence)
		if group.RootPatchID != "" {
			refs[group.RootPatchID] = true
		}
		for _, child := range group.Children {
			if child.Candidate != nil && child.Candidate.PatchID != "" {
				refs[child.Candidate.PatchID] = true
			}
		}
	}
	for id := range refs {
		if _, e = tx.ExecContext(ctx, "INSERT INTO artifact_uses(artifact,run_id) VALUES(?,?)", id, r.ID); e != nil {
			return e
		}
	}
	if receipt != nil {
		if len(receipt.Args) > int(s.policy.Database.MaxRecord) {
			return runner.ErrCapacity
		}
		var n int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM requests WHERE run_id=?", r.ID).Scan(&n); e != nil {
			return e
		}
		if n >= 17 {
			return fmt.Errorf("%w: run request receipt limit reached", runner.ErrCapacity)
		}
		if _, e = tx.ExecContext(ctx, "INSERT INTO requests(key,kind,args,run_id) VALUES(?,?,?,?)", receipt.Key, receipt.Kind, receipt.Args, r.ID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) FindReceipt(ctx context.Context, key string) (runner.Receipt, error) {
	var r runner.Receipt
	e := s.db.QueryRowContext(ctx, "SELECT key,kind,args,run_id FROM requests WHERE key=?", key).Scan(&r.Key, &r.Kind, &r.Args, &r.RunID)
	if errors.Is(e, sql.ErrNoRows) {
		e = runner.ErrNotFound
	}
	return r, e
}
func (s *Store) Artifact(ctx context.Context, id string) (runner.Artifact, error) {
	var a runner.Artifact
	e := s.db.QueryRowContext(ctx, "SELECT id,run_id,project,bytes FROM artifacts WHERE id=?", id).Scan(&a.ID, &a.RunID, &a.ProjectID, &a.Bytes)
	if errors.Is(e, sql.ErrNoRows) {
		e = runner.ErrNotFound
	}
	return a, e
}
func (s *Store) HasConsumers(ctx context.Context, id string) (bool, error) {
	var n int
	e := s.db.QueryRowContext(ctx, `SELECT count(*) FROM artifacts a JOIN artifact_uses u ON u.artifact=a.id WHERE a.run_id=? AND u.run_id!=?`, id, id).Scan(&n)
	return n > 0, e
}
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, "DELETE FROM artifact_uses WHERE run_id=?", id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM artifacts WHERE run_id=?", id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, "DELETE FROM runs WHERE id=?", id); e != nil {
		return e
	}
	return tx.Commit()
}
