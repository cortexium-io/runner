// Package store owns SQLite transactions and compare-and-set persistence, not workflow decisions.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("state changed; inspect current state and retry")

type Store struct {
	db    *sql.DB
	Dir   string
	guard *Guard
}

const Format = 18

const schema = `
CREATE TABLE metadata (id INTEGER PRIMARY KEY CHECK(id=1), format INTEGER NOT NULL CHECK(format=18), settings BLOB NOT NULL, drained INTEGER NOT NULL DEFAULT 1);
CREATE TABLE quality_history (revision TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE quality_reports (id TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE settings_history (revision TEXT PRIMARY KEY, data BLOB NOT NULL);
CREATE TABLE tasks (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, data BLOB NOT NULL, source_key TEXT UNIQUE);
CREATE TABLE projection (id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL);
CREATE TABLE attempts (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), occupying INTEGER NOT NULL, workspace_claim TEXT NOT NULL, resumed_from TEXT UNIQUE, data BLOB NOT NULL);
CREATE UNIQUE INDEX task_owner ON attempts(task_id) WHERE occupying=1;
CREATE UNIQUE INDEX workspace_owner ON attempts(workspace_claim) WHERE occupying=1;
CREATE TABLE publications (id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), version INTEGER NOT NULL, generation INTEGER NOT NULL, revision INTEGER NOT NULL, occupying INTEGER NOT NULL, data BLOB NOT NULL, integration_key TEXT NOT NULL DEFAULT '', previous_id TEXT NOT NULL, UNIQUE(task_id,version,generation,previous_id));
CREATE UNIQUE INDEX publication_owner ON publications(task_id) WHERE occupying=1;
CREATE UNIQUE INDEX integration_owner ON publications(integration_key) WHERE occupying=1 AND integration_key!='';
`

func Create(ctx context.Context, dir string, settings config.Settings) (*Store, error) {
	if err := securefs.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	guard, err := LockGuard(dir, false, true)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			guard.Close()
		}
	}()
	if err = guard.Invalidate(); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "runner.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	s, err := connect(ctx, dir, false)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.Close()
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schema); err == nil {
		b, _ := json.Marshal(settings)
		_, err = tx.ExecContext(ctx, "INSERT INTO metadata(id,format,settings) VALUES(1,?,?); INSERT INTO settings_history(revision,data) VALUES(?,?)", Format, b, settings.Config.Revision(), b)
	}
	if err == nil && settings.Quality.Revision() != "" {
		b, _ := json.Marshal(settings.Quality)
		_, err = tx.ExecContext(ctx, "INSERT INTO quality_history(revision,data) VALUES(?,?)", settings.Quality.Revision(), b)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.Close()
		return nil, err
	}
	s.guard = guard
	owned = true
	return s, nil
}

// Open never creates or repairs authority. Read-only commands do not run migrations.
func Open(ctx context.Context, dir string, readonly bool) (*Store, error) {
	guard, err := LockGuard(dir, false, false)
	if err != nil {
		return nil, err
	}
	s, err := open(ctx, dir, readonly)
	if err != nil {
		guard.Close()
		return nil, err
	}
	s.guard = guard
	if !readonly {
		if err = s.AllowMutation(ctx); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

func open(ctx context.Context, dir string, readonly bool) (*Store, error) {
	if err := securefs.ValidatePrivateDir(dir); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Join(dir, "runner.db"))
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("authoritative database must be a private, owned regular file with one link")
	}
	s, err := connect(ctx, dir, readonly)
	if err != nil {
		return nil, err
	}
	var format int
	if err = s.db.QueryRowContext(ctx, "SELECT format FROM metadata WHERE id=1").Scan(&format); err != nil {
		s.Close()
		return nil, fmt.Errorf("missing, corrupt or incompatible authoritative store: %w", err)
	}
	if format != Format {
		s.Close()
		return nil, fmt.Errorf("incompatible authoritative store format %d; preserve it and initialize a fresh state directory", format)
	}
	return s, nil
}
func connect(ctx context.Context, dir string, readonly bool) (*Store, error) {
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "runner.db")}
	q := u.Query()
	if readonly {
		q.Set("mode", "ro")
	} else {
		q.Set("mode", "rw")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, Dir: dir}, nil
}
func (s *Store) Close() error {
	err := s.db.Close()
	if s.guard != nil {
		err = errors.Join(err, s.guard.Close())
	}
	return err
}
func (s *Store) Settings(ctx context.Context) (config.Settings, bool, error) {
	var settings config.Settings
	var b []byte
	var drained bool
	err := s.db.QueryRowContext(ctx, "SELECT settings,drained FROM metadata WHERE id=1").Scan(&b, &drained)
	if err == nil {
		err = json.Unmarshal(b, &settings)
	}
	return settings, drained, err
}
func (s *Store) SetDrained(ctx context.Context, v bool) error {
	_, err := s.db.ExecContext(ctx, "UPDATE metadata SET drained=? WHERE id=1", v)
	return err
}
func (s *Store) ApplySettings(ctx context.Context, previous string, next config.Settings) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var b []byte
	if err = next.Config.Validate(); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, "SELECT settings FROM metadata WHERE id=1").Scan(&b); err != nil {
		return err
	}
	var old config.Settings
	if err = json.Unmarshal(b, &old); err != nil {
		return err
	}
	if old.Config.Revision() != previous {
		return ErrConflict
	}
	if !config.SameAuthority(old, next) {
		return errors.New("only execution policy and profiles can change live; repository and quality authority must remain unchanged")
	}
	b, _ = json.Marshal(next)
	if _, err = tx.ExecContext(ctx, "UPDATE metadata SET settings=? WHERE id=1", b); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO settings_history(revision,data) VALUES(?,?)", next.Config.Revision(), b); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Add(ctx context.Context, t core.Task) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO tasks(id,revision,data) VALUES(?,?,?)", t.ID, t.Revision, b)
	return err
}
func (s *Store) Task(ctx context.Context, id string) (core.Task, error) {
	var t core.Task
	var b []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &t)
	}
	return t, err
}
func (s *Store) Tasks(ctx context.Context) ([]core.Task, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM tasks ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Task{}
	for rows.Next() {
		var b []byte
		var t core.Task
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Attempts(ctx context.Context, task string) ([]core.Attempt, error) {
	q := "SELECT data FROM attempts"
	var args []any
	if task != "" {
		q += " WHERE task_id=?"
		args = append(args, task)
	}
	q += " ORDER BY rowid"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Attempt{}
	for rows.Next() {
		var b []byte
		var a core.Attempt
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Commit atomically replaces one expected task revision and, optionally, its attempt.
// Immediate transactions serialize capacity/policy checks; unique indexes protect
// task and workspace ownership even across replacement versions.
func (s *Store) Commit(ctx context.Context, expected int, t core.Task, a *core.Attempt, claim bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if t.NextRole == "refresh" || claim && a != nil && a.Role == "refresh" {
		var raw []byte
		err = tx.QueryRowContext(ctx, "SELECT data FROM publications WHERE task_id=? AND version=? AND generation=? ORDER BY rowid DESC LIMIT 1", t.ID, t.Current().Number, t.Generation).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			var p core.Publication
			if err = json.Unmarshal(raw, &p); err != nil {
				return err
			}
			if p.Occupying || p.State != "published" {
				return ErrPublicationPending
			}
		}
	}
	if a != nil && core.DeterministicRole(a.Role) && t.RefreshTarget != nil {
		var raw, settingsRaw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", t.ID).Scan(&raw); err != nil {
			return err
		}
		var previous core.Task
		if err = json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		if previous.RefreshTarget == nil {
			if err = tx.QueryRowContext(ctx, "SELECT settings FROM metadata WHERE id=1").Scan(&settingsRaw); err != nil {
				return err
			}
			var settings config.Settings
			if err = json.Unmarshal(settingsRaw, &settings); err != nil {
				return err
			}
			if t.Usage.BaseRefreshes != previous.Usage.BaseRefreshes+1 || t.RefreshTarget.Cycle != t.Usage.BaseRefreshes || t.Usage.BaseRefreshes > settings.Config.Policy.MaxBaseRefreshes {
				return ErrConflict
			}
		} else if *previous.RefreshTarget != *t.RefreshTarget || previous.Usage.BaseRefreshes != t.Usage.BaseRefreshes {
			return ErrConflict
		}
	}
	if claim {
		if a == nil || !a.Occupying || a.TaskID != t.ID || a.WorkspaceClaim == "" || !a.Authorized(t) {
			return errors.New("invalid execution claim")
		}
		var raw []byte
		var drained bool
		if err = tx.QueryRowContext(ctx, "SELECT settings,drained FROM metadata WHERE id=1").Scan(&raw, &drained); err != nil {
			return err
		}
		var settings config.Settings
		if err = json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1)+(SELECT count(*) FROM publications WHERE occupying=1)").Scan(&active); err != nil {
			return err
		}
		if drained || settings.Config.Revision() != a.PolicyRevision || active >= settings.Config.Policy.MaxParallelism {
			return ErrConflict
		}
		var owned int
		if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1 AND (task_id=? OR workspace_claim=?))+(SELECT count(*) FROM publications WHERE occupying=1 AND task_id=?)", t.ID, a.WorkspaceClaim, t.ID).Scan(&owned); err != nil {
			return err
		}
		if owned != 0 {
			return ErrConflict
		}
		if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", t.ID).Scan(&raw); err != nil {
			return err
		}
		var previous core.Task
		if err = json.Unmarshal(raw, &previous); err != nil {
			return err
		}
		var prerequisites []core.Task
		ids := []string{}
		if t.Release != nil {
			ids = append(ids, t.Release.Children...)
		}
		if t.ParentID != "" {
			ids = append(ids, t.ParentID)
		}
		for _, dep := range t.Current().Dependencies {
			ids = append(ids, dep.TaskID)
		}
		for _, id := range ids {
			if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", id).Scan(&raw); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrConflict
				}
				return err
			}
			var dependency core.Task
			if err = json.Unmarshal(raw, &dependency); err != nil {
				return err
			}
			prerequisites = append(prerequisites, dependency)
		}
		if t.ParentID != "" && previous.WorkingBase == nil {
			for _, parent := range prerequisites {
				if parent.ID == t.ParentID && (t.WorkingBase == nil || t.WorkingBase.Commit != parent.PlanHead || parent.PlanHead == "") {
					return ErrConflict
				}
			}
		}
		if core.PrerequisiteWait(t, prerequisites) != "" {
			return ErrConflict
		}
		if a.Kind == "check_repair" {
			q := previous.Pending
			if q == nil || q.Kind != "check_repair" || q.FromAttempt != a.ResumedFrom || a.Role != "implementer" ||
				a.ProfileName != q.ProfileName || a.Profile != q.Profile ||
				t.Usage.Repairs != previous.Usage.Repairs+1 || t.Usage.Escalations != previous.Usage.Escalations {
				return ErrConflict
			}
			if err = tx.QueryRowContext(ctx, "SELECT data FROM attempts WHERE id=? AND task_id=?", a.ResumedFrom, t.ID).Scan(&raw); err != nil {
				return err
			}
			var source core.Attempt
			if err = json.Unmarshal(raw, &source); err != nil {
				return err
			}
			if !source.Authorized(previous) || source.Resume != "check_repair" || source.Occupying || source.State != "failed" || source.Failure != core.ProofGap {
				return ErrConflict
			}
		}
		if a.Kind == "conflict_repair" {
			r := previous.ConflictRepair
			if r == nil || !r.Matches(previous) || previous.Pending == nil || previous.Pending.Kind != "conflict_repair" ||
				core.Digest(r) != core.Digest(a.ConflictRepair) || core.Digest(r) != core.Digest(t.ConflictRepair) ||
				t.WorkingBase == nil || *t.WorkingBase != r.Target || t.RefreshTarget != nil || t.CIRepair != nil ||
				t.WorkspaceRepair != previous.Usage.Repairs+1 || t.Usage.Repairs != previous.Usage.Repairs+1 ||
				t.Usage.BaseRefreshes != previous.Usage.BaseRefreshes || t.Usage.Escalations != previous.Usage.Escalations ||
				a.Role != "implementer" || t.BaseProof != nil || len(t.Review) != 0 {
				return ErrConflict
			}
		}
		if a.Kind == "ci_repair" {
			if err = tx.QueryRowContext(ctx, "SELECT data FROM publications WHERE task_id=? AND version=? AND generation=? ORDER BY rowid DESC LIMIT 1", t.ID, t.Current().Number, t.Generation).Scan(&raw); err != nil {
				return err
			}
			var publication core.Publication
			if err = json.Unmarshal(raw, &publication); err != nil {
				return err
			}
			if !publication.CanRepairCI(previous) || a.CIRepair == nil || a.CIRepair.PublicationID != publication.Intent.ID ||
				core.Digest(a.CIRepair.Assessment) != core.Digest(publication.CI) || a.CIRepair.SourceWorkspaceRepair != previous.WorkspaceRepair ||
				core.Digest(t.CIRepair) != core.Digest(a.CIRepair) || t.WorkspaceRepair != previous.Usage.Repairs+1 ||
				t.Usage.Repairs != previous.Usage.Repairs+1 || t.Usage.Escalations != previous.Usage.Escalations || a.Role != "implementer" || t.BaseProof != nil || len(t.Review) != 0 {
				return ErrConflict
			}
		}
		p := settings.Config.Policy
		if previous.Usage.Repairs > p.MaxRepairs && (previous.Pending == nil || previous.Pending.Kind != "resume") {
			return ErrConflict
		}
		if previous.Pending != nil && previous.Pending.Kind == "retry" && a.Retry > p.MaxTransientRetries {
			return ErrConflict
		}
		increment := 1
		if core.DeterministicRole(a.Role) {
			increment = 0
		}
		if t.Usage.Invocations != previous.Usage.Invocations+increment || a.Invocation != t.Usage.Invocations ||
			(!core.DeterministicRole(a.Role) && p.MaxInvocationsPerTask != nil && t.Usage.Invocations > *p.MaxInvocationsPerTask) ||
			t.Usage.Repairs < previous.Usage.Repairs || t.Usage.Repairs > previous.Usage.Repairs+1 ||
			(t.Usage.Repairs > previous.Usage.Repairs && t.Usage.Repairs > p.MaxRepairs) {
			return ErrConflict
		}
	}
	if err = writeTask(ctx, tx, expected, t); err != nil {
		return err
	}
	var b []byte
	if a != nil {
		b, err = json.Marshal(a)
		if err != nil {
			return err
		}
		var resumed any
		if a.ResumedFrom != "" {
			resumed = a.ResumedFrom
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO attempts(id,task_id,occupying,workspace_claim,resumed_from,data) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET occupying=excluded.occupying,data=excluded.data`, a.ID, a.TaskID, a.Occupying, a.WorkspaceClaim, resumed, b)
		if err != nil {
			return fmt.Errorf("attempt reservation/commit: %w", err)
		}
	}
	return tx.Commit()
}
