package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Store) SaveSourceHandoff(ctx context.Context, root, child, pending runner.Run, proof runner.UnpublishedCardAssessment, receipt *runner.Receipt) error {
	audit := root.SourceHandoff
	if audit == nil || root.RecordFormat != 8 || audit.Proof == nil || audit.Assessment != nil || root.UnpublishedCardRecovery == nil || receipt == nil || receipt.Kind != "adjudicate_source_handoff" || receipt.Key != audit.Request.RequestID || receipt.RunID != root.ID || audit.Request.RunID != root.ID || audit.Request.ChildRunID != child.ID || child.Task == nil || child.Task.Identity.RootRunID != root.ID || child.Step != 2 || child.Status != "Queued" || len(proof.Unstarted) != 1 {
		return errors.New("invalid atomic source handoff")
	}
	rawProof, err := json.Marshal(proof)
	ref := audit.Proof
	if err != nil || ref.RunID != root.ID || ref.Bytes != int64(len(rawProof)) || ref.Bytes > int64(s.policy.MaxArtifact) || ref.SHA256 != fmt.Sprintf("%x", sha256.Sum256(rawProof)) || ref.ID != proof.GroupAttemptID+"/source-handoff-"+ref.SHA256+".json" {
		return errors.New("invalid immutable source handoff proof artifact")
	}
	if proof.RunID != root.ID || proof.ChildRunID != child.ID || proof.AttemptID != audit.Request.AttemptID || proof.GroupAttemptID != audit.Request.GroupAttemptID || proof.BindingsSHA256 != audit.Request.BindingsSHA256 || proof.ChildRevision != audit.Request.ChildRevision || root.Revision != proof.StoredRevision+1 || child.Revision != proof.ChildRevision+1 {
		return errors.New("source handoff proof differs from its audit/request")
	}
	artifact := runner.Artifact{ID: ref.ID, RunID: root.ID, ProjectID: root.ProjectID, Bytes: ref.Bytes}
	original := proof.Unstarted[0]
	if pending.ID != original.RunID || pending.ID == root.ID || pending.ID == child.ID || pending.Revision != original.Revision+1 || pending.Task == nil || pending.Task.Identity.RootRunID != root.ID || pending.Step != 0 || pending.Status != "Waiting" || pending.CancelRequested || pending.ResourcesReleased || len(pending.Attempts) != 0 {
		return errors.New("invalid source handoff final reservation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing runner.Artifact
	err = tx.QueryRowContext(ctx, "SELECT id,run_id,project,bytes FROM artifacts WHERE id=?", artifact.ID).Scan(&existing.ID, &existing.RunID, &existing.ProjectID, &existing.Bytes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && existing != artifact {
		return errors.New("source handoff proof artifact ownership differs")
	}
	for _, expected := range []struct {
		id       string
		revision int64
	}{
		{root.ID, proof.StoredRevision}, {child.ID, proof.ChildRevision}, {pending.ID, original.Revision},
	} {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT data FROM runs WHERE id=?", expected.id).Scan(&raw); err != nil {
			return err
		}
		var old runner.Run
		if json.Unmarshal(raw, &old) != nil || old.Revision != expected.revision || expected.id == root.ID && (old.RecordFormat != 6 || old.SourceHandoff != nil) {
			return errors.New("source handoff stored revision changed")
		}
	}
	if err = s.saveRun(ctx, tx, pending, nil, nil); err != nil {
		return err
	}
	if err = s.saveRun(ctx, tx, child, nil, nil); err != nil {
		return err
	}
	if err = s.saveRunWithHandoff(ctx, tx, root, []runner.Artifact{artifact}, receipt, true); err != nil {
		return err
	}
	return tx.Commit()
}
