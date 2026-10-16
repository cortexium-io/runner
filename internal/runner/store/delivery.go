package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

var ErrPublicationPending = errors.New("settle publication with task delivery ID --reconcile before requesting another base refresh")

func (s *Store) Publications(ctx context.Context, task string) ([]core.Publication, error) {
	q := "SELECT data FROM publications"
	var args []any
	if task != "" {
		q += " WHERE task_id=?"
		args = append(args, task)
	}
	rows, err := s.db.QueryContext(ctx, q+" ORDER BY rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Publication{}
	for rows.Next() {
		var b []byte
		var p core.Publication
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePublication keeps immutable intent and monotonic create authority separate
// from observed outcomes. Claims share capacity and task ownership with execution.
func (s *Store) SavePublication(ctx context.Context, p core.Publication, claim bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old core.Publication
	key := ""
	if p.Occupying && p.IntegrationRequest != nil {
		key = strings.ToLower(p.Intent.Repository) + "/" + p.Intent.BaseBranch
	}
	if p.Revision > 0 {
		var b []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM publications WHERE id=?", p.Intent.ID).Scan(&b); err != nil {
			return err
		}
		if err = json.Unmarshal(b, &old); err != nil {
			return err
		}
		if old.Revision != p.Revision {
			return ErrConflict
		}
		if core.Digest(old.Intent) != core.Digest(p.Intent) || old.CreateRequested && !p.CreateRequested || old.MergeDispatch != nil && core.Digest(old.MergeDispatch) != core.Digest(p.MergeDispatch) {
			return errors.New("publication intent cannot change or regain write authority")
		}
	}
	if p.MergeDispatch != nil && old.MergeDispatch == nil && (!old.Occupying || !p.Occupying || old.IntegrationRequest == nil || !old.IntegrationRequest.Merge || core.Digest(p.IntegrationRequest) != core.Digest(old.IntegrationRequest)) {
		return errors.New("merge dispatch requires a reserved authorized request")
	}
	if d := p.MergeDispatch; d != nil && old.MergeDispatch == nil {
		if core.Digest(d.Request) != core.Digest(p.IntegrationRequest) || !d.Admission.Eligible || d.Admission.PullRequestID == "" || d.Admission.ProtectionRuleID == "" || d.RequestedAt.IsZero() {
			return errors.New("merge dispatch lacks exact evidence and admission")
		}
	}
	if p.Revision == 0 {
		var active int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM attempts WHERE occupying=1 AND task_id=?", p.Intent.TaskID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return ErrConflict
		}
		var existing int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM publications WHERE task_id=? AND version=? AND generation=? AND previous_id=?", p.Intent.TaskID, p.Intent.Version, p.Intent.Generation, p.Intent.PreviousID()).Scan(&existing); err != nil {
			return err
		}
		if existing != 0 {
			return ErrConflict
		}
		if u := p.Intent.Sync; u != nil {
			var raw []byte
			if err = tx.QueryRowContext(ctx, "SELECT data FROM publications WHERE id=?", u.PreviousID).Scan(&raw); err != nil {
				return err
			}
			var previous core.Publication
			if err = json.Unmarshal(raw, &previous); err != nil {
				return err
			}
			i, o := previous.Intent, previous.Observation
			if previous.State != "published" || previous.Occupying || o == nil || o.Number <= 0 || o.State != "OPEN" || !o.Draft || o.BranchCommit != i.Candidate.Commit ||
				i.TaskID != p.Intent.TaskID || i.Version != p.Intent.Version || i.Generation != p.Intent.Generation || i.Repository != p.Intent.Repository || i.BaseBranch != p.Intent.BaseBranch || i.Branch != p.Intent.Branch ||
				u.PublicationID != i.PublicationID() || u.PreviousCommit != i.Candidate.Commit || u.Number != o.Number || u.URL != o.URL || u.Base == "" || u.ObservedAt.IsZero() || u.PreviousCommit == p.Intent.Candidate.Commit {
				return errors.New("draft synchronization requires an exact confirmed predecessor")
			}
		}
	}
	if p.Revision == 0 || claim || p.State == "queued" || p.CIRequest != nil || p.IntegrationRequest != nil {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM tasks WHERE id=?", p.Intent.TaskID).Scan(&raw); err != nil {
			return err
		}
		var t core.Task
		if err = json.Unmarshal(raw, &t); err != nil {
			return err
		}
		if !p.Intent.Authorized(t) {
			return ErrConflict
		}
		if p.Revision == 0 && p.Intent.Sync != nil && !p.Intent.Sync.ObservedAt.Equal(t.BaseProof.ObservedAt) {
			return ErrConflict
		}
		if p.CIRequest != nil && (!p.CanAssessCI(t) || core.Digest(p.CIRequest) != core.Digest(t.BaseProof)) {
			return ErrConflict
		}

		if p.IntegrationRequest != nil && (p.CIRequest != nil || !p.IntegrationRequest.Matches(t, old)) {
			return ErrConflict
		}

		if p.IntegrationRequest != nil && p.IntegrationRequest.Merge {
			tasks, err := transactionTasks(ctx, tx)
			if err != nil {
				return err
			}
			if core.PrerequisiteWait(t, tasks) != "" {
				return ErrConflict
			}
			if old.MergeDispatch != nil && (old.IntegrationRequest == nil || core.Digest(old.IntegrationRequest) != core.Digest(p.IntegrationRequest)) {
				return errors.New("merge was already dispatched; use task integrate to inspect only")
			}
		}

		var drained bool
		if err = tx.QueryRowContext(ctx, "SELECT settings,drained FROM metadata WHERE id=1").Scan(&raw, &drained); err != nil {
			return err
		}
		var settings config.Settings
		if err = json.Unmarshal(raw, &settings); err != nil {
			return err
		}
		if settings.Config.Delivery.Mode != "github-draft" || settings.Config.Delivery.Repository != p.Intent.Repository || settings.Config.Repository.BaseRef != "refs/heads/"+p.Intent.BaseBranch {
			return ErrConflict
		}
		if r := p.IntegrationRequest; r != nil && r.AutomaticPolicy != "" && old.MergeDispatch == nil {
			if settings.Config.MergePolicy() != "automatic" {
				return fmt.Errorf("automatic merge policy is disabled: %w", ErrConflict)
			}
			if core.Digest(r) != core.Digest(old.IntegrationRequest) && (r.AutomaticPolicy != settings.Config.Revision() || !old.AutomaticMergeReady(t)) {
				return ErrConflict
			}
		}
		if claim {
			if !p.Occupying || (p.State != "running" && !(p.State == "published" && (p.CIRequest != nil || p.IntegrationRequest != nil))) {
				return errors.New("invalid publication reservation")
			}
			var active, owned int
			if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1)+(SELECT count(*) FROM publications WHERE occupying=1)").Scan(&active); err != nil {
				return err
			}
			if err = tx.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM attempts WHERE occupying=1 AND task_id=?)+(SELECT count(*) FROM publications WHERE occupying=1 AND task_id=?)", t.ID, t.ID).Scan(&owned); err != nil {
				return err
			}
			if key != "" {
				var integrating int
				if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM publications WHERE occupying=1 AND integration_key=?", key).Scan(&integrating); err != nil {
					return err
				}
				if integrating != 0 {
					return ErrConflict
				}
			}
			if drained || active >= settings.Config.Policy.MaxParallelism || owned != 0 {
				return ErrConflict
			}
		}
	}
	if err = completeIntegration(ctx, tx, &p, old); err != nil {
		return err
	}
	expected := p.Revision
	p.Revision++
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if expected == 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO publications(id,task_id,version,generation,revision,occupying,data,previous_id) VALUES(?,?,?,?,?,?,?,?)", p.Intent.ID, p.Intent.TaskID, p.Intent.Version, p.Intent.Generation, p.Revision, p.Occupying, b, p.Intent.PreviousID())
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE publications SET revision=?,occupying=?,data=?,integration_key=? WHERE id=?", p.Revision, p.Occupying, b, key, p.Intent.ID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
