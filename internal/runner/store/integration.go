package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

func transactionTasks(ctx context.Context, tx *sql.Tx) ([]core.Task, error) {
	rows, err := tx.QueryContext(ctx, "SELECT data FROM tasks ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := []core.Task{}
	for rows.Next() {
		var raw []byte
		var t core.Task
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// Confirmation and ownership release share SavePublication's transaction. A
// stopped/replaced task retains the observation but cannot gain completion.
func completeIntegration(ctx context.Context, tx *sql.Tx, p *core.Publication, old core.Publication) error {
	r := p.Integration
	if r == nil || r.State != "confirmed" {
		return nil
	}
	if p.Occupying || p.IntegrationRequest != nil || r.Observation == nil || old.Observation == nil ||
		r.Observation.Remote.State != "MERGED" || r.Observation.Remote.Draft ||
		r.Observation.Remote.Number != old.Observation.Number || r.Observation.Remote.URL != old.Observation.URL ||
		r.Observation.Remote.BranchCommit != old.Intent.Candidate.Commit || r.Observation.MergeCommit == "" ||
		r.Observation.MergedAt.IsZero() || r.ObservedAt.IsZero() || r.BaseTip == "" {
		return errors.New("incomplete integration receipt")
	}
	tasks, err := transactionTasks(ctx, tx)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.ID != p.Intent.TaskID {
			continue
		}
		if t.Stage == core.Completed && t.Integration != nil && t.Integration.PublicationID == p.Intent.ID && core.Digest(t.Integration.Assessment) == core.Digest(r) {
			return nil
		}
		if !old.Occupying || old.IntegrationRequest == nil || core.Digest(old.IntegrationRequest) != core.Digest(r.Request) || !r.Request.Matches(t, old) || core.PrerequisiteWait(t, tasks) != "" {
			r.State, r.Detail = "stale", "Authority or prerequisite changed before confirmation; observation retained without completion."
			return nil
		}
		t.Stage, t.NextRole, t.Pending, t.Attention = core.Completed, "", nil, nil
		t.Integration = &core.IntegrationReceipt{PublicationID: p.Intent.ID, Assessment: *r}
		return writeTask(ctx, tx, t.Revision, t)
	}
	return errors.New("integration task missing")
}

// Plan completion is deterministic and restartable; no remote read or model is
// involved. Every child must still be the original approved release version.
func (s *Store) CompletePlans(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	tasks, err := transactionTasks(ctx, tx)
	if err != nil {
		return err
	}
	byID := map[string]core.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	for _, parent := range tasks {
		if parent.Release == nil || parent.Stage == core.Stopped || parent.Stage == core.Completed || parent.Approval == nil || parent.Approval.Generation != parent.Generation || len(parent.Release.Children) == 0 {
			continue
		}
		done := true
		for _, id := range parent.Release.Children {
			child, ok := byID[id]
			if !ok || child.ParentID != parent.ID || child.Current().Number != 1 || child.Stage != core.Completed || child.Integration == nil || child.Integration.Assessment.State != "confirmed" {
				done = false
				break
			}
		}
		if done {
			parent.Stage, parent.NextRole, parent.Attention = core.Completed, "", nil
			if err = writeTask(ctx, tx, parent.Revision, parent); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
