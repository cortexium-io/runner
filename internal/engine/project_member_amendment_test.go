package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func releasedProjectAmendmentFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, source, _ := stagedPlannerBatchFixture(t, 3)
	approval, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), approval); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(project.remoteItems); i++ {
		item := &project.remoteItems[i]
		item.Activity = config.RunnerActivityWaitingForDependencies
		item.Approval = testApproval(*item)
	}
	repo, _ := createPublicationRepository(t)
	runner := &recoveryTestRunner{project: project}
	service.cfg.ProjectDir, service.run = repo, runner
	service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, runner)
	if err != nil {
		t.Fatal(err)
	}
	return service, project, runner
}

func TestAmendmentOfUnstartedProjectMembersRenewsExactSourceAfterEachChange(t *testing.T) {
	service, project, runner := releasedProjectAmendmentFixture(t)
	for i := 1; i < len(project.remoteItems); i++ {
		before := append([]github.WorkItem(nil), project.remoteItems...)
		item := before[i]
		body := strings.Replace(item.Body, "The child works.", "The child preserves every supported configuration.", 1)
		preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
		if err != nil {
			t.Fatal(err)
		}
		if !preview.Approval.Unstarted || preview.Approval.RetryLane != "ready" || preview.Workspace.ItemID != "" || preview.Candidate.Head != "" {
			t.Fatal("preview lost the unstarted implementation boundary")
		}
		if !reflect.DeepEqual(before, project.remoteItems) {
			t.Fatal("preview mutated the batch")
		}
		after, err := service.ApplyRequirementAmendment(t.Context(), preview)
		if err != nil {
			t.Fatal(err)
		}
		if after.Body != body || after.Status != "Blocked" || after.Phase != "" || after.Branch != "" || after.QAFailures != 0 || after.Activity != item.Activity || after.Approval == item.Approval {
			t.Fatal("amendment changed execution state or failed to renew content authority")
		}
		for j, current := range project.remoteItems {
			switch j {
			case 0:
				if current.Approval == before[j].Approval {
					t.Fatal("source still binds obsolete child content")
				}
				current.Approval = before[j].Approval
			case i:
				current.Body, current.Status, current.Result, current.Approval = before[j].Body, before[j].Status, before[j].Result, before[j].Approval
			}
			if !reflect.DeepEqual(current, before[j]) {
				t.Fatal("amendment changed metadata, history, sibling authority or source fields")
			}
		}
		approval, err := service.PlanProjectItemApproval(t.Context(), before[0].ID)
		if err != nil || approval.Batch == nil || !approval.Batch.Released {
			t.Fatalf("complete released batch no longer authenticates: %v", err)
		}
		request := service.workspaceRequestForItem(after, github.DelegatedContentFor(after).Digest, service.cfg.ProjectDir, false)
		if err := workspace.NewGitProvider(runner).VerifyWorkspaceAbsent(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		if err := service.checkAdoptionEvidenceAbsent(after.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.PlanRequirementAmendment(t.Context(), after.ID, strings.Replace(body, "every supported", "all supported", 1)); err != nil {
			t.Fatalf("amended unstarted member cannot be amended again: %v", err)
		}
	}
	for _, item := range project.remoteItems[1:] {
		retry, err := service.PlanProjectItemRetry(t.Context(), item.ID)
		if err != nil {
			t.Fatal(err)
		}
		after, err := service.ApplyProjectItemRetry(t.Context(), retry)
		if err != nil || after.Status != "Ready" || after.Phase != "ready" || after.Body != item.Body || after.QAFailures != 0 || after.Branch != "" {
			t.Fatalf("ordinary retry lost amended requirements or invented execution: %v", err)
		}
	}
}

func TestUnstartedProjectAmendmentRefusesChangedAuthorityAndExecution(t *testing.T) {
	for _, change := range []string{"source_authority", "source_transition", "sibling_authority", "missing_sibling", "body", "metadata", "workspace", "checkpoint", "verification", "feedback", "branch", "phase", "result", "qa_count", "published", "remote_pr", "qa_commit", "active", "transition", "ambiguous_lane", "worker", "partial_write"} {
		t.Run(change, func(t *testing.T) {
			service, project, runner := releasedProjectAmendmentFixture(t)
			item := &project.remoteItems[1]
			body := strings.Replace(item.Body, "The child works.", "The child preserves every supported configuration.", 1)
			preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source_authority":
				project.remoteItems[0].Approval = ""
			case "source_transition":
				project.remoteItems[0].Transition = "v1"
			case "sibling_authority":
				project.remoteItems[2].Approval = ""
			case "missing_sibling":
				project.remoteItems = project.remoteItems[:3]
			case "body":
				item.Body += "\nIntervening scope change"
			case "metadata":
				preview.Approval.Body = strings.Replace(body, `"planning_batch_size":3`, `"planning_batch_size":2`, 1)
			case "workspace":
				request := service.workspaceRequestForItem(*item, github.DelegatedContentFor(*item).Digest, service.cfg.ProjectDir, false)
				if _, err := workspace.NewGitProvider(runner).Prepare(t.Context(), request); err != nil {
					t.Fatal(err)
				}
			case "checkpoint", "verification", "feedback":
				path := map[string]string{"checkpoint": service.implementationCheckpointPath(item.ID), "verification": service.verificationEvidencePath(item.ID), "feedback": service.reviewFeedbackPath(item.ID)}[change]
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("retained execution evidence"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "branch":
				item.Branch = "runner/retained"
				item.Approval = testApproval(*item)
			case "phase":
				item.Phase = "ready"
				item.Approval = testApproval(*item)
			case "result":
				item.Result = "Prior execution stopped"
				item.Approval = testApproval(*item)
			case "qa_count":
				item.QAFailures = 1
				item.Approval = testApproval(*item)
			case "published":
				item.PullRequest = "https://github.com/owner/repo/pull/2"
				item.Approval = testApproval(*item)
			case "remote_pr":
				branch, err := workspace.RequestBranch(service.workspaceRequestForItem(*item, github.DelegatedContentFor(*item).Digest, service.cfg.ProjectDir, false))
				if err != nil {
					t.Fatal(err)
				}
				runner.pullRequests = `[{"number":2,"url":"https://github.com/owner/repo/pull/2","headRefName":"` + branch + `","headRepository":{"nameWithOwner":"owner/repo"},"baseRefName":"main"}]`
			case "qa_commit":
				item.QACommit = strings.Repeat("a", 40)
				item.Approval = testApproval(*item)
			case "active":
				item.Activity = "Implementing"
				item.Approval = testApproval(*item)
			case "transition":
				item.Transition = "v1"
			case "ambiguous_lane":
				service.cfg.GitHubProject.LaneRoles["other"] = config.WorkRoleImplementer
				service.cfg.GitHubProject.LaneStatuses["other"] = "Ready"
			case "worker":
				lock, err := github.AcquireProcessLock(service.cfg.GitHubProject.GitHubProjectConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			case "partial_write":
				project.failApprovalAt = project.approvalWrites + 1
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if change != "worker" && change != "partial_write" {
				if _, err := service.PlanRequirementAmendment(t.Context(), preview.Approval.Item.ID, preview.Approval.Body); err == nil {
					t.Fatal("unsafe Project-member amendment passed a fresh preview")
				}
			}
			if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil {
				t.Fatal("unsafe or failed Project-member amendment accepted")
			}
			if !reflect.DeepEqual(before, project.remoteItems) {
				t.Fatal("refusal or rollback changed batch authority or card state")
			}
		})
	}
}
