package engine

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestRenameCheckpointRequiresFreshInvocationWithoutRenewingAllowances(t *testing.T) {
	for _, change := range []string{"future deadline", "expired deadline", "changed bytes", "changed branch", "changed requirements"} {
		t.Run(change, func(t *testing.T) {
			service := reviewFeedbackTestEngine(t.TempDir())
			item := github.WorkItem{ID: "PVTI_rename", Repository: "owner/new"}
			content := github.DelegatedContentFor(github.WorkItem{Body: "Approved requirements", Repository: item.Repository})
			metadata := workspace.Metadata{WorktreePath: "/retained/worktree", BranchName: "runner/retained", BaseRevision: "base", Identity: workspace.Identity{Repository: item.Repository}}
			snapshot := workspace.Snapshot{Fingerprint: "exact-bytes", Branch: metadata.BranchName}
			deadline := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)
			if change == "expired deadline" {
				deadline = time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC)
			}
			output := execution.Output{Outcome: execution.OutcomeSucceeded, Summary: "Historical output", WorkDone: []string{"Historical work"}, Verification: []string{"Historical proof"}}
			if err := service.saveImplementationCheckpoint(item, content, "old-context", metadata, snapshot, workspace.Candidate{}, output, deadline, true); err != nil {
				t.Fatal(err)
			}
			record, err := service.readImplementationCheckpoint(item.ID)
			if err != nil {
				t.Fatal(err)
			}
			record.RenameRequiresProof = true
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(service.implementationCheckpointPath(item.ID), encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "changed bytes":
				snapshot.Fingerprint = "different-bytes"
			case "changed branch":
				snapshot.Branch = "runner/other"
			case "changed requirements":
				content.Digest = "different-requirements"
			}
			loaded, reused, err := service.loadImplementationCheckpoint(item, content, "fresh-context", metadata, snapshot, []string{"Run checks again"})
			if change == "future deadline" || change == "expired deadline" {
				if err != nil || reused || !loaded.ExecutionDeadline.Equal(deadline) || !loaded.CorrectionUsed || loaded.Output.Outcome != "" || loaded.Candidate.CommitOID != "" {
					t.Fatalf("rename reused proof or replenished allowances: %+v reused=%t error=%v", loaded, reused, err)
				}
			} else if err == nil || reused {
				t.Fatal("rename marker authorized a changed workspace or requirement")
			}
		})
	}
}

func TestImplementationCheckpointResumesOnlyExactTaskContextAndWorkspace(t *testing.T) {
	service := reviewFeedbackTestEngine(t.TempDir())
	item := github.WorkItem{ID: "PVTI_checkpoint", Repository: "owner/repo", PullRequest: "https://github.com/owner/repo/pull/2"}
	content := github.DelegatedContentFor(github.WorkItem{ID: item.ID, Body: "Approved body"})
	criteria := []string{"focused behavior is proven", "meaningful failure is proven"}
	feedback := []string{"Keep the existing public API."}
	comments := []string{"@maintainer: Preserve the operator-owned fixture."}
	contextDigest := implementationContextDigest(content, item, feedback, comments, criteria)
	metadata := workspace.Metadata{
		WorktreePath: "/tmp/runner-worktree", BranchName: "runner/checkpoint", BaseRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Identity: workspace.Identity{Repository: "owner/repo"},
	}
	preCandidate := workspace.Snapshot{
		Fingerprint: "sha256:pre-candidate", Head: metadata.BaseRevision,
		Tree: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branch: metadata.BranchName,
	}
	output := execution.Output{
		Outcome: execution.OutcomeSucceeded, Summary: "Implemented the approved behavior.",
		WorkDone:     []string{"Updated the focused implementation."},
		Verification: []string{"focused check passed", "failure-path check passed"},
	}
	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, preCandidate, workspace.Candidate{}, output, time.Time{}, false); err != nil {
		t.Fatalf("save pre-candidate checkpoint: %v", err)
	}
	info, err := os.Stat(service.implementationCheckpointPath(item.ID))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint is not a private file: info=%v error=%v", info, err)
	}
	loaded, found, err := service.loadImplementationCheckpoint(item, content, contextDigest, metadata, preCandidate, criteria)
	if err != nil || !found || loaded.Candidate.CommitOID != "" || loaded.Output.Summary != output.Summary {
		t.Fatalf("load exact pre-candidate checkpoint: checkpoint=%#v found=%v error=%v", loaded, found, err)
	}

	candidate := workspace.Candidate{
		CommitOID: "cccccccccccccccccccccccccccccccccccccccc",
		TreeOID:   "dddddddddddddddddddddddddddddddddddddddd",
	}
	committed := workspace.Snapshot{
		Fingerprint: "sha256:committed", Head: candidate.CommitOID, Tree: candidate.TreeOID,
		Branch: metadata.BranchName, Clean: true,
	}
	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, committed, candidate, output, time.Time{}, false); err != nil {
		t.Fatalf("save committed checkpoint: %v", err)
	}
	loaded, found, err = service.loadImplementationCheckpoint(item, content, contextDigest, metadata, committed, criteria)
	if err != nil || !found || loaded.Candidate != candidate || loaded.Output.Usage.Available || loaded.Output.HarnessDurationMilliseconds != 0 {
		t.Fatalf("load exact committed checkpoint without duplicating usage: checkpoint=%#v found=%v error=%v", loaded, found, err)
	}

	tamperedWorkspace := committed
	tamperedWorkspace.Head = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, found, err := service.loadImplementationCheckpoint(item, content, contextDigest, metadata, tamperedWorkspace, criteria); err == nil || found {
		t.Fatalf("candidate-mismatched checkpoint was accepted: found=%v error=%v", found, err)
	}

	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, committed, candidate, output, time.Time{}, false); err != nil {
		t.Fatal(err)
	}
	changedContext := implementationContextDigest(content, item, feedback, append(comments, "@maintainer: New direction."), criteria)
	if _, found, err := service.loadImplementationCheckpoint(item, content, changedContext, metadata, committed, criteria); err != nil || found {
		t.Fatalf("changed human context reused a completed harness result: found=%v error=%v", found, err)
	}
	if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); !os.IsNotExist(err) {
		t.Fatalf("stale checkpoint was not removed: %v", err)
	}
}
