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
	if version < 0 || version > 21 {
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
		version = 15
	}
	if version == 15 {
		// Settled command diagnostics are retained attempt evidence. Older
		// writers would silently discard the additive JSON field on save.
		if _, e = db.Exec("PRAGMA user_version=16"); e != nil {
			return nil, e
		}
		version = 16
	}
	if version == 16 {
		// Explicit result inputs and command diagnostic artifacts must survive
		// old readers without being silently discarded on their next write.
		if _, e = db.Exec("PRAGMA user_version=17"); e != nil {
			return nil, e
		}
		version = 17
	}
	if version == 17 {
		// A finite current-route grant and its scoped context must not be
		// silently dropped by older writers.
		if _, e = db.Exec("PRAGMA user_version=18"); e != nil {
			return nil, e
		}
		version = 18
	}
	if version == 18 {
		// Disk-only result references derive omitted IDs from retained child
		// links. Older writers cannot restore this normalized representation.
		if _, e = db.Exec("PRAGMA user_version=19"); e != nil {
			return nil, e
		}
		version = 19
	}
	if version == 19 {
		if _, e = db.Exec(`BEGIN;
CREATE TABLE role_task_links(parent_run_id TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
position INTEGER NOT NULL CHECK(position>=0),child_run_id TEXT NOT NULL,data BLOB NOT NULL,
result_position INTEGER CHECK(result_position IS NULL OR result_position>=0),
PRIMARY KEY(parent_run_id,position),UNIQUE(parent_run_id,child_run_id),UNIQUE(parent_run_id,result_position));
PRAGMA user_version=20; COMMIT;`); e != nil {
			return nil, e
		}
		version = 20
	}
	if version == 20 {
		// Earlier writers cannot retain model-only fixed-process definition audits.
		if _, e = db.Exec("PRAGMA user_version=21"); e != nil {
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
	type storedRun struct {
		id, project       string
		state, definition []byte
	}
	var stored []storedRun
	for rows.Next() {
		var item storedRun
		if e = rows.Scan(&item.id, &item.project, &item.state, &item.definition); e != nil {
			return nil, e
		}
		stored = append(stored, item)
	}
	if e = rows.Err(); e != nil {
		return nil, e
	}
	if e = rows.Close(); e != nil {
		return nil, e
	}
	var orphan int
	if e = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM role_task_links l LEFT JOIN runs r ON r.id=l.parent_run_id WHERE r.id IS NULL)").Scan(&orphan); e != nil {
		return nil, e
	}
	if orphan != 0 {
		return nil, errors.New("orphan normalized task relationships")
	}
	var out []runner.Run
	for _, item := range stored {
		r, err := s.loadRunRecord(ctx, tx, item.state, item.definition)
		if err != nil {
			return nil, err
		}
		if r.ID != item.id || r.ProjectID != item.project {
			return nil, errors.New("stored run identity differs")
		}
		out = append(out, r)
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
	record, e := runner.MarshalRunStorageRecord(r)
	b, definition := record.State, record.Definition
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
	var before runner.Run
	if len(previous) != 0 {
		before, e = s.loadRunRecord(ctx, tx, previous, previousDefinition)
		if e != nil {
			return e
		}
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
	if err := s.guardTaskAllowanceSave(ctx, tx, r, before, previousDefinition, artifacts, receipt); err != nil {
		return err
	}
	newRun := errors.Is(e, sql.ErrNoRows)
	needsReservation := newRun
	if newRun && receipt == nil && len(artifacts) == 0 {
		consumes, err := s.consumesTaskReservation(ctx, tx, r)
		if err != nil {
			return err
		}
		needsReservation = !consumes
	}
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
		// A materializing child already has its parent reservation row on disk.
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
	if e = s.saveTaskLinks(ctx, tx, r, before, previous, record); e != nil {
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
	for grant := r.CorrectionRecovery; grant != nil; grant = grant.Previous {
		for audit := grant.EvidenceHandoff; audit != nil; audit = audit.Previous {
			refs[audit.Request.Evidence.ID] = true
		}
		if grant.Context != nil {
			refs[grant.Context.ID] = true
		}
	}
	for _, grant := range runner.RoleDefinitionGrants(r) {
		if grant.PreviousDefinition != nil {
			refs[grant.PreviousDefinition.ID] = true
		}
	}
	if r.TaskGraph != nil {
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
		if a.End != nil && a.End.DiagnosticArtifact != nil {
			refs[a.End.DiagnosticArtifact.ID] = true
		}
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
	oldChildren := 0
	if before.TaskGraph != nil {
		oldChildren = len(before.TaskGraph.Children)
	}
	if r.TaskGraph != nil && len(r.TaskGraph.Children) > oldChildren {
		if err := s.checkTaskLinkAdmission(ctx, tx); err != nil {
			return err
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
	if e = s.guardTaskContinuationDelete(ctx, tx, []string{id}); e != nil {
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
