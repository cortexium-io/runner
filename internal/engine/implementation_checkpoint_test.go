package engine

import (
	"os"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

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
	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, preCandidate, workspace.Candidate{}, output); err != nil {
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
	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, committed, candidate, output); err != nil {
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

	if err := service.saveImplementationCheckpoint(item, content, contextDigest, metadata, committed, candidate, output); err != nil {
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
