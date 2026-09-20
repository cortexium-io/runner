package engine

import (
	"errors"
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

func TestAgentQACapturesAndChecksSelectedEvidence(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		name := "read-only evidence reaches QA"
		if tamper {
			name = "changed evidence prevents publication"
		}
		t.Run(name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{ID: "PVTI_evidence", Title: "Review candidate", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/task", QAFailures: 1}
			item.Approval = testApproval(item)
			prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
				ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prepared.WorktreePath, ".gitignore"), []byte("test-results/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(prepared.WorktreePath, "test-results"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "test-results", "receipt.json"), []byte(`{"outcome":"passed"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 1}
			var evidenceRoot string
			runner := &integrityMutatingReviewer{project: project, mutate: func(candidate string) error {
				evidenceRoot = filepath.Join(filepath.Dir(candidate), "evidence")
				if _, err := os.Stat(filepath.Join(candidate, "test-results")); !os.IsNotExist(err) {
					return errors.New("evidence contaminated candidate source")
				}
				receipt := filepath.Join(evidenceRoot, "files", "test-results", "receipt.json")
				data, err := os.ReadFile(receipt)
				if err != nil || string(data) != `{"outcome":"passed"}` {
					return errors.New("selected retained evidence was not supplied")
				}
				if tamper {
					return os.WriteFile(receipt, []byte(`{"outcome":"forged"}`), 0o600)
				}
				return nil
			}}
			cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, ReviewEvidencePaths: []string{"test-results"}, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
			service, err := New(cfg, runner)
			if err != nil {
				t.Fatal(err)
			}
			results, err := service.RunCycle(t.Context())
			if err != nil || len(results) != 1 || evidenceRoot == "" {
				t.Fatalf("review did not run: %#v, %v", results, err)
			}
			if tamper {
				if results[0].FailureClass != string(execution.FailureIntegrityViolation) || project.status != "Blocked" || project.phase != "agent_qa" || project.qaFailures != 1 || project.pullRequest != "" || !strings.Contains(results[0].Summary, "Retained QA evidence") {
					t.Fatalf("changed evidence was trusted or consumed a rejection: %#v; status=%s phase=%s failures=%d", results, project.status, project.phase, project.qaFailures)
				}
			} else if results[0].Outcome != execution.OutcomeSucceeded || project.status != "PR Ready" {
				t.Fatalf("valid evidence did not complete review: %#v", results)
			}
			if _, err := os.Stat(evidenceRoot); !os.IsNotExist(err) {
				t.Fatalf("private evidence snapshot survived cleanup: %v", err)
			}
		})
	}
}
