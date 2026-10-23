package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	for _, phase := range []string{"", "ready"} {
		for _, change := range []string{"source_authority", "source_transition", "sibling_authority", "missing_sibling", "body", "metadata", "workspace", "checkpoint", "verification", "feedback", "archived_verification", "legacy_feedback", "remote_branch", "branch", "phase", "result", "qa_count", "published", "remote_pr", "qa_commit", "active", "transition", "ambiguous_lane", "worker", "partial_write"} {
			t.Run(phase+"/"+change, func(t *testing.T) {
				service, project, runner := releasedProjectAmendmentFixture(t)
				item := &project.remoteItems[1]
				item.Phase = phase
				item.Approval = testApproval(*item)
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
				case "remote_branch":
					runner.remoteRefs = `[{"ref":"refs/heads/runner/previous-push"}]`
				case "archived_verification", "legacy_feedback":
					path := service.verificationEvidencePath(item.ID) + ".superseded-original"
					if change == "legacy_feedback" {
						path = filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "qa-feedback", "review_"+safeRefComponent(item.ID)+".json")
					}
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("retained historical execution"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "branch":
					item.Branch = "runner/retained"
					item.Approval = testApproval(*item)
				case "phase":
					item.Phase = "review"
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
}

func TestReadyPhaseAmendmentPreservesRecoveredFiveMemberBatch(t *testing.T) {
	service, project, runner, metadata, retainedBody := dirtyAmendmentFixture(t)
	retained, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, retainedBody)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyRequirementAmendment(t.Context(), retained); err != nil {
		t.Fatal(err)
	}
	// Reproduce the original pre-execution amendment followed by an ordinary
	// retry. Retry writes ready while retaining this result; it never executes.
	fourth := &project.remoteItems[4]
	fourth.Status, fourth.Phase, fourth.Activity = "Blocked", "", ""
	fourth.Result = "Operator amended the approved requirements before execution. QA failure count retained; explicit retry required."
	fourth.Approval = testApproval(*fourth)
	previousResult := fourth.Result
	retry, err := service.PlanProjectItemRetry(t.Context(), fourth.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemRetry(t.Context(), retry); err != nil {
		t.Fatal(err)
	}
	if project.remoteItems[4].Phase != "ready" || project.remoteItems[4].Result != previousResult {
		t.Fatal("ordinary retry lost phase/result")
	}
	identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
	identity, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{4, 5} {
		before := append([]github.WorkItem(nil), project.remoteItems...)
		item := before[index]
		body := strings.Replace(item.Body, "The child works.", "The child preserves the complete original collection.", 1)
		preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
		if err != nil {
			t.Fatal(err)
		}
		if !preview.Approval.Unstarted || preview.Approval.Item.Phase != "ready" || len(preview.Approval.PlanningMembers) != 5 || len(preview.AbsentArtifacts) == 0 || preview.Workspace.ItemID != "" || preview.DirtyRetained {
			t.Fatal("ready phase was misclassified or lost absence/cohort proof")
		}
		if !reflect.DeepEqual(before, project.remoteItems) {
			t.Fatal("preview/cancellation wrote state")
		}
		after, err := service.ApplyRequirementAmendment(t.Context(), preview)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != "Blocked" || after.Phase != "ready" || after.Activity != item.Activity || after.Branch != "" || after.QAFailures != 0 || after.Body != body {
			t.Fatal("amendment changed execution/history")
		}
		for j, current := range project.remoteItems {
			switch j {
			case 0:
				current.Approval = before[j].Approval
			case index:
				current.Body, current.Status, current.Result, current.Approval = before[j].Body, before[j].Status, before[j].Result, before[j].Approval
			}
			if !reflect.DeepEqual(current, before[j]) {
				t.Fatalf("changed unrelated source/member/history: %s", current.ID)
			}
		}
		batch, err := service.PlanProjectItemApproval(t.Context(), before[0].ID)
		if err != nil || batch.Batch == nil || !batch.Batch.Released {
			t.Fatalf("original complete batch no longer authenticates: %v", err)
		}
		request := service.workspaceRequestForItem(after, github.DelegatedContentFor(after).Digest, service.cfg.ProjectDir, false)
		if err := service.verifyAmendmentWorkspaceAbsent(t.Context(), workspace.NewGitProvider(runner), request); err != nil {
			t.Fatal(err)
		}
		if err := service.checkAdoptionEvidenceAbsent(after.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.PlanProjectItemRetry(t.Context(), after.ID); err != nil {
			t.Fatalf("separate ordinary retry unavailable: %v", err)
		}
	}
	if project.remoteItems[2].QAFailures != 1 || project.remoteItems[6].QAFailures != 1 {
		t.Fatal("existing QA failures changed")
	}
	actualIdentity, err := os.ReadFile(identityPath)
	if err != nil || !reflect.DeepEqual(identity, actualIdentity) {
		t.Fatalf("retained member identity changed: %v", err)
	}
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || !reflect.DeepEqual(snapshot, retained.Candidate) {
		t.Fatalf("retained member HEAD/index/work bytes changed: %v", err)
	}
}

func TestReadyPhaseAmendmentKeepsOriginalAssessmentFence(t *testing.T) {
	service, project, _, _ := branchlessReauthorizationFixture(t)
	for _, index := range []int{4, 5} {
		item := project.remoteItems[index]
		if _, err := service.PlanRequirementAmendment(t.Context(), item.ID, strings.Replace(item.Body, "The child works.", "The child preserves all existing collection entries.", 1)); err == nil || !strings.Contains(err.Error(), "every card in the planning batch") {
			t.Fatalf("missing approval fence bypassed: %v", err)
		}
	}
}

func TestReadyPhaseAmendmentRefusesOrphanedCurrentAndLegacyArtifacts(t *testing.T) {
	for _, artifact := range []string{"branch", "workspace", "identity"} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%t", artifact, legacy), func(t *testing.T) {
				service, project, _ := releasedProjectAmendmentFixture(t)
				item := &project.remoteItems[1]
				item.Phase, item.Activity = "ready", ""
				item.Approval = testApproval(*item)
				preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, strings.Replace(item.Body, "The child works.", "The child preserves all existing collection entries.", 1))
				if err != nil {
					t.Fatal(err)
				}
				workID := "assignment_" + itemRefComponent(item.ID)
				if legacy {
					workID = "assignment_" + safeRefComponent(item.ID)
				}
				switch artifact {
				case "branch":
					if _, err := runEngineTestGit(t.Context(), []string{"branch", "runner/" + workID}, service.cfg.ProjectDir, 30*time.Second); err != nil {
						t.Fatal(err)
					}
				case "workspace":
					if err := os.MkdirAll(filepath.Join(service.implementationWorkspaceRoot(), workID), 0o700); err != nil {
						t.Fatal(err)
					}
				case "identity":
					path := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", workID+".json")
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(`{"item_id":"wrong-owner"}`), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				before := append([]github.WorkItem(nil), project.remoteItems...)
				if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil {
					t.Fatal("partial/mismatched retained artifact was adopted")
				}
				if !reflect.DeepEqual(before, project.remoteItems) {
					t.Fatal("refusal changed remote state")
				}
			})
		}
	}
}

func TestReadyPhaseAmendmentRefusesPublicationAfterWorkspaceRemoval(t *testing.T) {
	service, project, runner := releasedProjectAmendmentFixture(t)
	item := &project.remoteItems[1]
	item.Phase, item.Activity = "ready", ""
	item.Approval = testApproval(*item)
	preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, strings.Replace(item.Body, "The child works.", "The child preserves all existing collection entries.", 1))
	if err != nil {
		t.Fatal(err)
	}
	provider := workspace.NewGitProvider(runner)
	request := service.workspaceRequestForItem(*item, github.DelegatedContentFor(*item).Digest, service.cfg.ProjectDir, false)
	metadata, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Accepted report", "Accepted candidate"); err != nil {
		t.Fatal(err)
	}
	if _, err := runEngineTestGit(t.Context(), []string{"worktree", "remove", metadata.WorktreePath}, service.cfg.ProjectDir, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := runEngineTestGit(t.Context(), []string{"branch", "-D", metadata.BranchName}, service.cfg.ProjectDir, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(metadata.WorktreePath), ".runner-state", request.WorkID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := service.verifyAmendmentWorkspaceAbsent(t.Context(), provider, request); err != nil {
		t.Fatal(err)
	}
	before := append([]github.WorkItem(nil), project.remoteItems...)
	if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil || !strings.Contains(err.Error(), "publication acceptance") {
		t.Fatalf("orphaned publication was adopted as unstarted: %v", err)
	}
	if !reflect.DeepEqual(before, project.remoteItems) {
		t.Fatal("refusal wrote state")
	}
}

func TestReadyPhaseAmendmentHandlesLostAndUncertainMutationResponses(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("uncertain=%t", uncertain), func(t *testing.T) {
			service, project, runner := releasedProjectAmendmentFixture(t)
			item := &project.remoteItems[1]
			item.Phase = "ready"
			item.Approval = testApproval(*item)
			if uncertain {
				service.run = &dirtyAmendmentUncertainRunner{recoveryTestRunner: runner}
			} else {
				service.run = lostAmendmentResponse{runner}
			}
			var err error
			service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, service.run)
			if err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(item.Body, "The child works.", "The child preserves all existing collection entries.", 1)
			preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
			if err != nil {
				t.Fatal(err)
			}
			after, err := service.ApplyRequirementAmendment(t.Context(), preview)
			current := project.remoteItems[1]
			if uncertain {
				if err == nil || !strings.Contains(err.Error(), "leave Runner stopped") || current.Transition != "v1" || current.Body != body {
					t.Fatalf("uncertain mutation lost fence or inspection requirement: %v", err)
				}
			} else if err != nil || after.Status != "Blocked" || current.Transition != "" || current.Body != body {
				t.Fatalf("committed mutation was not recognized: %v", err)
			}
			if current.Phase != "ready" || current.QAFailures != 0 || current.Activity != preview.Approval.Item.Activity {
				t.Fatal("response recovery changed execution/history")
			}
		})
	}
}

func TestReadyPhaseWithRetainedWorkspaceStillUsesDirtyAmendment(t *testing.T) {
	service, project, _, metadata, body := dirtyAmendmentFixture(t)
	item := &project.remoteItems[3]
	// This public shape also fits a never-started ordinary retry. The private
	// identity and workspace must keep it on the existing retained-work path.
	item.Result = "Operator amended the approved requirements before execution. QA failure count retained; explicit retry required."
	item.Approval = testApproval(*item)
	preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Approval.Unstarted || !preview.DirtyRetained || preview.Workspace != metadata.Identity || len(preview.AbsentArtifacts) != 0 {
		t.Fatal("retained implementation was classified from public Ready phase/result")
	}
}
