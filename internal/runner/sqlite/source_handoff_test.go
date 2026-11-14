package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceHandoffAtomicAuditUpgradeAndPreviousReader(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, err := Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	root, child, pending := normalizedRun(4), normalizedRun(2), normalizedRun(0)
	root.ID, root.Revision, root.Status, root.RecordFormat = "handoff-root", 126, "Failed", 6
	root.UnpublishedCardRecovery = &runner.UnpublishedCardRecovery{Request: runner.RecoverUnpublishedCardRequest{RunID: root.ID}, Unstarted: make([]runner.UnstartedCardRecovery, 5)}
	child.ID, child.Revision, child.Status, child.Step = "handoff-child", 61, "Failed", 1
	child.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: root.ID}}
	for i := range child.Attempts {
		child.Attempts[i].ID = "handoff-child-" + child.Attempts[i].ID
	}
	pending.ID, pending.Revision, pending.Status = "handoff-final", 3, "Cancelled"
	pending.Task = &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: root.ID}}
	pending.CancelRequested, pending.ResourcesReleased = true, true
	for _, r := range []runner.Run{root, child, pending} {
		if err = s.Save(t.Context(), r, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prior := runner.Clone(root.UnpublishedCardRecovery)
	receipt := &runner.Receipt{Key: "handoff-approved", Kind: "adjudicate_source_handoff", RunID: root.ID, Args: json.RawMessage(`{"bound":true}`)}
	root.RecordFormat, root.Revision, root.Status = 8, 127, "Running"
	proof := runner.UnpublishedCardAssessment{RunID: root.ID, ChildRunID: child.ID, GroupAttemptID: "original-group", StoredRevision: 126, ChildRevision: 61, Unstarted: []runner.UnstartedCardRecovery{{RunID: pending.ID, Revision: 3, Updated: pending.Updated}}}
	rawProof, _ := json.Marshal(proof)
	digest := fmt.Sprintf("%x", sha256.Sum256(rawProof))
	root.SourceHandoff = &runner.SourceHandoffAudit{
		Request: runner.AdjudicateSourceHandoffRequest{RecoverUnpublishedCardRequest: runner.RecoverUnpublishedCardRequest{RequestID: receipt.Key, RunID: root.ID, ChildRunID: child.ID, ChildRevision: 61, GroupAttemptID: proof.GroupAttemptID}},
		Proof:   &runner.CardRecoveryArtifact{ID: "original-group/source-handoff-" + digest + ".json", RunID: root.ID, Bytes: int64(len(rawProof)), SHA256: digest},
		Scope:   runner.SourceHandoffScope,
	}
	child.Revision, child.Step, child.Status = 62, 2, "Queued"
	pending.Revision, pending.Status, pending.CancelRequested, pending.ResourcesReleased = 4, "Waiting", false, false
	if err = s.Save(t.Context(), root, nil, receipt); err == nil {
		t.Fatal("ordinary Save bypassed the atomic handoff boundary")
	}
	broken := runner.Clone(root)
	broken.Reason = strings.Repeat("x", int(s.policy.Database.MaxRecord)+1)
	if err = s.SaveSourceHandoff(t.Context(), broken, child, pending, proof, receipt); err == nil {
		t.Fatal("failed root save committed children")
	}
	after, err := s.Load(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("partial handoff survived rollback", err)
	}
	if _, err = s.FindReceipt(t.Context(), receipt.Key); err == nil {
		t.Fatal("failed handoff retained receipt")
	}
	if _, err = s.Artifact(t.Context(), root.SourceHandoff.Proof.ID); err == nil {
		t.Fatal("failed handoff registered proof artifact")
	}
	if _, err = s.db.Exec("INSERT INTO artifacts(id,run_id,project,bytes) VALUES(?,?,?,?)", root.SourceHandoff.Proof.ID, child.ID, child.ProjectID, len(rawProof)); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSourceHandoff(t.Context(), root, child, pending, proof, receipt); err == nil {
		t.Fatal("atomic grant adopted a foreign proof registration")
	}
	if _, err = s.db.Exec("DELETE FROM artifacts WHERE id=?", root.SourceHandoff.Proof.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveSourceHandoff(t.Context(), root, child, pending, proof, receipt); err != nil {
		t.Fatal(err)
	}
	artifact, err := s.Artifact(t.Context(), root.SourceHandoff.Proof.ID)
	if err != nil || artifact.RunID != root.ID || artifact.ProjectID != root.ProjectID || artifact.Bytes != int64(len(rawProof)) {
		t.Fatal("proof artifact registration is not atomic/owned", err)
	}
	if err = s.Save(t.Context(), root, nil, nil); err != nil {
		t.Fatal(err)
	}
	var uses int
	if err = s.db.QueryRow("SELECT count(*) FROM artifact_uses WHERE artifact=? AND run_id=?", artifact.ID, root.ID).Scan(&uses); err != nil || uses != 1 {
		t.Fatal("normal root save lost immutable proof retention", uses, err)
	}
	for _, kind := range []string{"old-audit", "handoff-audit", "downgrade"} {
		bad := runner.Clone(root)
		switch kind {
		case "old-audit":
			bad.UnpublishedCardRecovery.Spent++
		case "handoff-audit":
			bad.SourceHandoff.Scope = "changed"
		case "downgrade":
			bad.RecordFormat = 6
			bad.SourceHandoff = nil
		}
		if err = s.Save(t.Context(), bad, nil, nil); err == nil {
			t.Fatal("immutable history changed", kind)
		}
	}
	s.Close()
	s = nil
	if old := os.Getenv("RUNNER_SOURCE_HANDOFF_OLD_READER"); old != "" {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, old, "serve", "--config", filepath.Join("..", "..", "..", "examples", "demo", "instance.yaml"), "--data", dir, "--listen", "127.0.0.1:0").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unsupported run record format") {
			t.Fatalf("old reader accepted new audit: %s %v", out, err)
		}
	}
	s, err = Open(dir, normalizedPolicy())
	if err != nil {
		t.Fatal(err)
	}
	after, err = s.Load(t.Context())
	if err != nil || len(after) != 3 {
		t.Fatal("restart lost handoff", err)
	}
	for _, r := range after {
		if r.ID == root.ID && (!reflect.DeepEqual(r.UnpublishedCardRecovery, prior) || !reflect.DeepEqual(r.SourceHandoff, root.SourceHandoff)) {
			t.Fatal("restart lost either audit")
		}
	}
	got, err := s.FindReceipt(t.Context(), receipt.Key)
	if err != nil || !reflect.DeepEqual(got, *receipt) {
		t.Fatal("restart lost receipt", err)
	}
	if err = s.SaveSourceHandoff(t.Context(), root, child, pending, proof, receipt); err == nil {
		t.Fatal("store repeated handoff after restart")
	}
}
