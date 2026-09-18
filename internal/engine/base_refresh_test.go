package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestCleanBaseRefreshPreservesEvidenceOriginAndRunsFreshQA(t *testing.T) {
	for _, method := range []string{config.MergeMethodMerge, config.MergeMethodRebase, config.MergeMethodSquash} {
		t.Run(method, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{ID: "PVTI_clean_refresh", Title: "Retained candidate", Body: "## Proof obligations\n- Feature works",
				Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer,
				Branch: "cortexium/task", QAFailures: 2}
			item.Approval = testApproval(item)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: item.QAFailures}
			runner := &candidateInspectingReviewer{project: project}
			service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo,
				GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", MergeMethod: method}}), runner)
			if err != nil {
				t.Fatal(err)
			}
			content := github.DelegatedContentFor(item)
			provider := workspace.NewGitProvider(service.run)
			metadata, err := provider.Prepare(t.Context(), workspace.Request{WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(),
				WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID, DelegatedContentDigest: content.Digest,
				Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			original, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, item.Title, method)
			if err != nil {
				t.Fatal(err)
			}
			criteria := approvedVerificationContract(content.BodySnapshot)
			if err := service.saveVerificationEvidence(item, content, metadata, original, criteria, []string{"Original focused check passed"}); err != nil {
				t.Fatal(err)
			}
			var current workspace.Candidate
			for _, baseFile := range []string{"base-one.txt", "base-two.txt"} {
				advanceRemoteBase(t, repo, baseFile, "new independent base\n")
				results, err := service.RunCycle(t.Context())
				if err != nil || len(results) != 1 || results[0].Outcome != "warning" || runner.head != "" || project.status != "Agent QA" || project.qaFailures != 2 || project.pullRequest != "" {
					t.Fatalf("clean refresh invoked an agent, reset history or published: results=%+v status=%s failures=%d reviewed=%s error=%v", results, project.status, project.qaFailures, runner.head, err)
				}
				metadata, err = service.workspaceForItem(t.Context(), item, content.Digest, repo)
				if err != nil {
					t.Fatal(err)
				}
				current = workspace.Candidate{CommitOID: strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")),
					TreeOID: strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD^{tree}"))}
				entries, err := service.loadVerificationEvidence(item, content, metadata, current, criteria)
				if err != nil || len(entries) != 1 || entries[0].SourceCommitOID != original.CommitOID || entries[0].SourceTreeOID != original.TreeOID || entries[0].Evidence != "Original focused check passed" {
					t.Fatalf("refresh lost or relabelled original evidence: %+v, error=%v", entries, err)
				}
				if current.CommitOID == original.CommitOID || current.TreeOID == original.TreeOID {
					t.Fatal("fixture did not change candidate and tree")
				}
			}
			results, err := service.RunCycle(t.Context())
			if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || project.status != "PR Ready" || runner.head != current.CommitOID || runner.tree != current.TreeOID {
				t.Fatalf("refreshed candidate did not pass through fresh exact-candidate QA: results=%+v reviewed=%s/%s status=%s error=%v", results, runner.head, runner.tree, project.status, err)
			}
		})
	}
}

func TestBaseRefreshRefusesUnrelatedCandidateEvidence(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_refresh_changed", Title: "Retained candidate", Body: "## Proof obligations\n- Feature works",
		Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer, Branch: "cortexium/task"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &reviewForbiddenRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: repo}), runner)
	if err != nil {
		t.Fatal(err)
	}
	content := github.DelegatedContentFor(item)
	provider := workspace.NewGitProvider(service.run)
	metadata, err := provider.Prepare(t.Context(), workspace.Request{WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID, DelegatedContentDigest: content.Digest,
		Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	original, err := provider.ConstructCandidate(t.Context(), metadata, item.Title)
	if err != nil {
		t.Fatal(err)
	}
	criteria := approvedVerificationContract(content.BodySnapshot)
	if err := service.saveVerificationEvidence(item, content, metadata, original, criteria, []string{"Original check passed"}); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "commit", "--allow-empty", "-m", "Unexplained candidate replacement")
	advanceRemoteBase(t, repo, "new-base.txt", "new base\n")
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || runner.reviewCalls != 0 || project.phase != "ready" || !strings.Contains(results[0].Error, "verify evidence before base refresh") {
		t.Fatalf("unrelated candidate evidence was carried into QA: results=%+v calls=%d error=%v", results, runner.reviewCalls, err)
	}
	if results[0].RetryDisposition != string(execution.RetryManual) || results[0].FailureClass != string(execution.FailureIntegrityUnverified) {
		t.Fatalf("unverified evidence did not offer an explicit manual recovery: %+v", results[0])
	}
	if entries, err := service.loadVerificationEvidence(item, content, metadata, original, criteria); err != nil || len(entries) != 1 || entries[0].SourceCommitOID != original.CommitOID {
		t.Fatalf("refused refresh lost or rebound original evidence: %+v error=%v", entries, err)
	}
}
