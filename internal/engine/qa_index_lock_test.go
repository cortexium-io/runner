//go:build !windows

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestQAIndexLockRecoveryPreservesCandidateAndReviewerLane(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_qa_index_lock", Title: "Retained candidate", Body: "Criteria", Repository: "owner/repo",
		Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/task", QAFailures: 1,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 1}
	runner := &candidateInspectingReviewer{project: project}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, repo, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("retained implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidate(t.Context(), metadata, item.Title)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "--path-format=absolute", "--git-path", "index.lock"))
	lockBytes := []byte("other Git process owns this\n")
	if err := os.WriteFile(lockPath, lockBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 {
		t.Fatalf("locked QA cycle: %#v %v", results, err)
	}
	result := results[0]
	if result.FailureClass != string(execution.FailureIntegrityUnverified) || result.RetryDisposition != string(execution.RetryManual) ||
		project.status != "Blocked" || project.phase != "agent_qa" || project.qaFailures != 1 || runner.head != "" || result.ReviewVerdict != "" || project.pullRequest != "" {
		t.Fatalf("index contention lost the QA lane or ran/published work: result=%#v status=%q phase=%q rejections=%d reviewed_head=%q PR=%q", result, project.status, project.phase, project.qaFailures, runner.head, project.pullRequest)
	}
	if retained, err := os.ReadFile(lockPath); err != nil || string(retained) != string(lockBytes) {
		t.Fatalf("Runner changed the foreign lock: %q %v", retained, err)
	}
	if strings.Contains(project.result, lockPath) || !strings.Contains(project.result, "cortexium-runner retry") {
		t.Fatalf("recovery leaked a private path or omitted retry guidance: %q", project.result)
	}
	// The lock owner releases it; native retry must select QA, not implementation.
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemRetry(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	results, err = service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || results[0].Item.Role != config.WorkRoleReviewer ||
		runner.head != candidate.CommitOID || runner.tree != candidate.TreeOID || project.qaFailures != 1 || project.status != "PR Ready" {
		t.Fatalf("native retry did not review the exact candidate: results=%#v err=%v head=%q tree=%q rejections=%d status=%q", results, err, runner.head, runner.tree, project.qaFailures, project.status)
	}
}
