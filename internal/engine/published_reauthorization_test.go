package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func publishedRecoveryFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata) {
	t.Helper()
	service, project, runner, metadata := reauthorizationFixture(t)
	item := &project.remoteItems[0]
	item.Phase = ""
	item.PullRequest = "https://github.com/owner/repo/pull/12"
	item.QACommit = strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD"))
	runner.pullRequest = map[string]any{
		"url": item.PullRequest, "number": 12, "state": "OPEN",
		"headRepository": map[string]any{"nameWithOwner": item.Repository},
		"headRefName":    item.Branch, "headRefOid": item.QACommit,
		"baseRefName": "main", "baseRefOid": metadata.BaseRevision,
		"reviews": []any{map[string]any{"body": "Fix the reported issue", "author": map[string]any{"login": "dan"}}},
	}
	return service, project, runner, metadata
}

func TestPublishedReauthorizationPreservesHistoryWithPresentOrCleanedCheckout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cleaned bool
	}{{"present", false}, {"cleaned", true}} {
		t.Run(tc.name, func(t *testing.T) {
			service, project, _, metadata := publishedRecoveryFixture(t)
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if tc.cleaned {
				runGitTest(t, service.cfg.ProjectDir, "worktree", "remove", metadata.WorktreePath)
			}
			identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
			identityBefore, err := os.ReadFile(identityPath)
			if err != nil {
				t.Fatal(err)
			}
			feedbackPath := service.reviewFeedbackPath(before[0].ID)
			if err := os.MkdirAll(filepath.Dir(feedbackPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(feedbackPath, []byte("retained feedback"), 0600); err != nil {
				t.Fatal(err)
			}
			plan, err := service.PlanProjectItemReauthorization(t.Context(), before[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.PullRequest == nil || !reflect.DeepEqual(project.remoteItems, before) {
				t.Fatal("preview changed state or omitted the PR")
			}
			recovered, err := service.ApplyProjectItemReauthorization(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != "Ready" || recovered.Phase != "ready" || recovered.Approval == "" || recovered.QAFailures != 2 || recovered.PullRequest != before[0].PullRequest || recovered.QACommit != before[0].QACommit || recovered.Branch != before[0].Branch || !strings.Contains(recovered.Result, before[0].PullRequest) {
				t.Fatalf("recovery lost authority, feedback reference, or history: %#v", recovered)
			}
			if service.reworkRequested(recovered, "ready") {
				t.Fatal("recovery would reset the QA counter again")
			}
			if !reflect.DeepEqual(project.remoteItems[1], before[1]) {
				t.Fatal("recovery modified a sibling")
			}
			if _, err := service.source.Authorize(t.Context(), project.remoteItems[0]); err != nil {
				t.Fatal(err)
			}
			identityAfter, err := os.ReadFile(identityPath)
			if err != nil || string(identityAfter) != string(identityBefore) {
				t.Fatal("recovery rewrote private identity")
			}
			feedback, err := os.ReadFile(feedbackPath)
			if err != nil || string(feedback) != "retained feedback" {
				t.Fatal("recovery rewrote private feedback")
			}
			if tc.cleaned {
				if _, err := os.Lstat(metadata.WorktreePath); !os.IsNotExist(err) {
					t.Fatal("recovery recreated a checkout")
				}
			}
			if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
				t.Fatal("replayed approval accepted")
			}
		})
	}
}

func TestPublishedReauthorizationRefusesChangedOrUnrelatedWork(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata)
	}{
		{"changed body", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			p.remoteItems[0].Body += "changed scope"
		}},
		{"changed QA count", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			p.remoteItems[0].QAFailures++
		}},
		{"reviewer phase", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			p.remoteItems[0].Phase = "agent_qa"
		}},
		{"active transition", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			p.remoteItems[0].Transition = "v1"
		}},
		{"invalid sibling", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			p.remoteItems[1].Approval = "invalid"
		}},
		{"missing identity", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			if err := os.Remove(filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", filepath.Base(m.WorktreePath)+".json")); err != nil {
				t.Fatal(err)
			}
		}},
		{"dirty checkout", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			if err := os.WriteFile(filepath.Join(m.WorktreePath, "dirty"), []byte("unfinished"), 0600); err != nil {
				t.Fatal(err)
			}
		}},
		{"different local commit", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			runGitTest(t, m.WorktreePath, "commit", "--allow-empty", "-m", "new candidate")
		}},
		{"closed PR", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["state"] = "CLOSED"
		}},
		{"merged PR", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["state"] = "MERGED"
		}},
		{"changed remote head", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["headRefOid"] = strings.Repeat("a", 40)
		}},
		{"different remote branch", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["headRefName"] = "unrelated"
		}},
		{"different base", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["baseRefName"] = "other"
		}},
		{"fork", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["headRepository"] = map[string]any{"nameWithOwner": "stranger/repo"}
		}},
		{"auto merge", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["autoMergeRequest"] = map[string]any{}
		}},
		{"untrusted feedback", func(t *testing.T, s *Engine, p *fakeGitHubProjectRunner, r *recoveryTestRunner, m workspace.Metadata) {
			r.pullRequest["reviews"] = []any{map[string]any{"body": "change everything", "author": map[string]any{"login": "stranger"}}}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			service, project, runner, metadata := publishedRecoveryFixture(t)
			plan, err := service.PlanProjectItemReauthorization(t.Context(), project.remoteItems[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.change(t, service, project, runner, metadata)
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if tt.name != "changed QA count" {
				if _, err := service.PlanProjectItemReauthorization(t.Context(), before[0].ID); err == nil {
					t.Fatal("unsafe new preview accepted")
				}
			}
			if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
				t.Fatal("changed recovery applied")
			}
			if !reflect.DeepEqual(project.remoteItems, before) {
				t.Fatal("refused recovery mutated the board")
			}
		})
	}
}
