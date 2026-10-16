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
		if t.Release != nil && !core.AllIncluded(t, tasks) {
			r.State, r.Detail = "stale", "Plan members changed before confirmation"
			return nil
		}
		t.Completion = &core.CompletionReceipt{Kind: "github", PublicationID: p.Intent.ID, Commit: r.Observation.MergeCommit, Tree: r.Request.Proof.Candidate.Tree}
		if err = completeChildren(ctx, tx, t, tasks); err != nil {
			return err
		}
		t.Stage, t.NextRole, t.Pending, t.Attention = core.Completed, "", nil, nil
		t.Integration = &core.IntegrationReceipt{PublicationID: p.Intent.ID, Assessment: *r}
		return writeTask(ctx, tx, t.Revision, t)
	}
	return errors.New("integration task missing")
}
