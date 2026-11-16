// Package sqlite persists bounded current state. It is not a transcript/event store.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	if version < 0 || version > 15 {
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
		version = 3
	}
	if version == 3 {
		// The record-format validator retains the old 128-attempt contract.
		// Widen only the physical normalized row range for opted-in format nine.
		if _, e = db.Exec(`BEGIN;
CREATE TABLE attempts_v4(run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
position INTEGER NOT NULL CHECK(position>=0 AND position<32768), id TEXT NOT NULL UNIQUE, data BLOB NOT NULL,
PRIMARY KEY(run_id,position));
INSERT INTO attempts_v4(run_id,position,id,data) SELECT run_id,position,id,data FROM attempts;
DROP TABLE attempts;
ALTER TABLE attempts_v4 RENAME TO attempts;
PRAGMA user_version=4; COMMIT;`); e != nil {
			return nil, e
		}
		version = 4
	}
	if version == 4 {
		// Older workers ignore native context policy and result-file references.
		// Mark the operational boundary without rewriting retained run bytes.
		if _, e = db.Exec("PRAGMA user_version=5"); e != nil {
			return nil, e
		}
		version = 5
	}
	if version == 5 {
		// Native ownership now precedes disposable inputs and outlives their
		// release. Older readers cannot interpret these interrupted phases.
		if _, e = db.Exec("PRAGMA user_version=6"); e != nil {
			return nil, e
		}
		version = 6
	}
	if version == 6 {
		// FIFO capture identity and primary completion timestamps are part of
		// recovery. An older service must not ignore either proof boundary.
		if _, e = db.Exec("PRAGMA user_version=7"); e != nil {
			return nil, e
		}
		version = 7
	}
	if version == 7 {
		// Retained task continuations freeze an old family and debit its spent
		// allowance into a new root. Older services must not ignore this audit.
		if _, e = db.Exec("PRAGMA user_version=8"); e != nil {
			return nil, e
		}
		version = 8
	}
	if version == 8 {
		// Same-generation maintenance recovery has an immutable audit. Older
		// services must not overwrite it while interpreting rearmed failures.
		if _, e = db.Exec("PRAGMA user_version=9"); e != nil {
			return nil, e
		}
		version = 9
	}
	if version == 9 {
		// Root task briefs can now derive from canonical inputs on load. An
		// older reader must not expose an empty goal or persist it as authority.
		if _, e = db.Exec("PRAGMA user_version=10"); e != nil {
			return nil, e
		}
		version = 10
	}
	if version == 10 {
		// Root owner grants amend the effective ceiling while preserving the
		// original continuation authority. Older readers cannot interpret them.
		if _, e = db.Exec("PRAGMA user_version=11"); e != nil {
			return nil, e
		}
		version = 11
	}
	if version == 11 {
		// Explicit definition amendments preserve prior guidance in artifacts.
		// Older writers must not discard this authority or compact task specs.
		if _, e = db.Exec("PRAGMA user_version=12"); e != nil {
			return nil, e
		}
		version = 12
	}
	if version == 12 {
		// Aggregate work limits are now optional. Keep all rows byte-for-byte,
		// and let record/database bytes bound storage rather than a hidden
		// execution-count ceiling. Older binaries must not reopen this state.
		if _, e = db.Exec(`BEGIN;
CREATE TABLE attempts_v13(run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
position INTEGER NOT NULL CHECK(position>=0), id TEXT NOT NULL UNIQUE, data BLOB NOT NULL,
PRIMARY KEY(run_id,position));
INSERT INTO attempts_v13(run_id,position,id,data) SELECT run_id,position,id,data FROM attempts;
DROP TABLE attempts;
ALTER TABLE attempts_v13 RENAME TO attempts;
PRAGMA user_version=13; COMMIT;`); e != nil {
			return nil, e
		}
		version = 13
	}
	if version == 13 {
		// Linked follow-ups retain completed source families without reopening
		// their result. Older readers must not ignore this retention authority.
		if _, e = db.Exec(`BEGIN;
CREATE INDEX followup_parent ON runs(json_extract(CAST(data AS TEXT),'$.followup.run_id'));
CREATE INDEX task_graph_root ON runs(json_extract(CAST(data AS TEXT),'$.task_graph.root_run_id'));
PRAGMA user_version=14; COMMIT;`); e != nil {
			return nil, e
		}
		version = 14
	}
	if version == 14 {
		// Role-only amendments preserve the attempt allowance and a paused
		// root's source ownership. Older writers cannot interpret this audit.
		if _, e = db.Exec("PRAGMA user_version=15"); e != nil {
			return nil, e
		}
	}
	if e = os.Chmod(path, 0600); e != nil {
		return nil, e
	}
	s.maxRuns = p.RetainedRunCapacity()
	if p.Database.MaxRuns > 0 && s.maxRuns < 1 {
		return nil, errors.New("database allowance cannot reserve one run plus recovery space")
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
	return s.saveRunWithHandoff(ctx, tx, r, artifacts, receipt, false)
}

func (s *Store) saveRunWithHandoff(ctx context.Context, tx *sql.Tx, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt, handoffUpgrade bool) error {
	return s.saveRunAuthorized(ctx, tx, r, artifacts, receipt, saveAuthorization{handoffUpgrade: handoffUpgrade})
}

type saveAuthorization struct {
	handoffUpgrade bool
	continuation   bool
	recovery       bool
	followup       bool
}

func (s *Store) saveRunAuthorized(ctx context.Context, tx *sql.Tx, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt, authority saveAuthorization) error {
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
	var previous, previousDefinition []byte
	e = tx.QueryRowContext(ctx, "SELECT data,definition FROM runs WHERE id=?", r.ID).Scan(&previous, &previousDefinition)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if err := s.guardTaskContinuationSave(ctx, tx, r, previous, authority.continuation); err != nil {
		return err
	}
	if err := guardTaskRecoverySave(r, previous, authority.recovery); err != nil {
		return err
	}
	if err := guardTaskFollowupSave(r, previous, authority.followup); err != nil {
		return err
	}
	if err := s.guardTaskAllowanceSave(ctx, tx, r, previous, previousDefinition, artifacts, receipt); err != nil {
		return err
	}
	newRun := errors.Is(e, sql.ErrNoRows)
	needsReservation := newRun
	if !newRun && (!r.Terminal() || !r.ResourcesReleased) {
		var before runner.Run
		if err := json.Unmarshal(previous, &before); err != nil {
			return err
		}
		needsReservation = before.Terminal() && before.ResourcesReleased
	}
	if newRun && s.maxRuns > 0 {
		var n int
		if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&n); e != nil {
			return e
		}
		if n >= s.maxRuns {
			return fmt.Errorf("%w: retained run limit %d", runner.ErrCapacity, s.maxRuns)
		}
	}
	if needsReservation {
		remaining, err := s.availableRunSlots(ctx, tx)
		if err != nil {
			return err
		}
		if remaining == 0 {
			return fmt.Errorf("%w: database byte headroom cannot reserve newly admitted work and its initial execution", runner.ErrCapacity)
		}
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO runs(id,project,data,definition) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,definition=excluded.definition", r.ID, r.ProjectID, b, definition); e != nil {
		return e
	}
	if e = s.saveAttempts(ctx, tx, r, attempts, previous, authority.handoffUpgrade); e != nil {
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
	if r.TaskGraph != nil {
		for _, grant := range r.TaskGraph.AllowanceGrants {
			if grant.PreviousDefinition != nil {
				refs[grant.PreviousDefinition.ID] = true
			}
		}
		if lineage := r.TaskGraph.Lineage; lineage != nil {
			refs[lineage.Source.PatchID], refs[lineage.CheckID] = true, true
		}
		for _, child := range r.TaskGraph.Children {
			add(child.Spec.Inputs)
		}
	}
	if r.SourceHandoff != nil && r.SourceHandoff.Proof != nil {
		refs[r.SourceHandoff.Proof.ID] = true
	}
	for _, a := range r.Attempts {
		if a.State == runner.Finished && a.Effective != nil && a.Effective.Provenance != "" {
			var exists int
			if e = tx.QueryRowContext(ctx, "SELECT count(*) FROM artifacts WHERE id=? AND run_id=?", a.Effective.Provenance, r.ID).Scan(&exists); e != nil {
				return e
			}
			if exists == 1 {
				refs[a.Effective.Provenance] = true
			}
		}
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
		if r.TaskGraph == nil && n >= 17 {
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
	e := s.db.QueryRowContext(ctx, `SELECT EXISTS(
SELECT 1 FROM artifacts a JOIN artifact_uses u ON u.artifact=a.id WHERE a.run_id=? AND u.run_id!=?
UNION ALL SELECT 1 FROM runs source JOIN runs follower
ON json_extract(CAST(follower.data AS TEXT),'$.followup.run_id')=json_extract(CAST(source.data AS TEXT),'$.task_graph.root_run_id')
WHERE source.id=?)`, id, id, id).Scan(&n)
	return n > 0, e
}
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, e := s.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = guardTaskContinuationDelete(ctx, tx, []string{id}); e != nil {
		return e
	}
	if e = s.guardTaskFollowupDelete(ctx, tx, []string{id}); e != nil {
		return e
	}
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
