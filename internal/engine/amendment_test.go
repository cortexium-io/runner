package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

const amendedProof = "Preview may reflow in a reserved editor gutter; saved email layout stays unchanged."

func amendmentFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, runner, metadata := reauthorizationFixture(t)
	item := &project.remoteItems[0]
	item.Status, item.Phase, item.Result = "Agent QA", "agent_qa", "Implementation complete"
	item.Body = strings.Replace(item.Body, "Complete the implementation", "Keep preview wrapping unchanged.\n\n## Proof obligations\n- Preview wrapping is unchanged.", 1)
	item.Approval = testApproval(*item)
	// The fixture's workspace was created before replacing its initial body.
	identity := metadata.Identity
	newDigest := github.DelegatedContentFor(*item).Digest
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, time.Second*30, service.snapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	request := service.workspaceRequestForItem(*item, identity.DelegatedContentDigest, service.cfg.ProjectDir, false)
	if err := workspace.NewGitProvider(runner).RebindRetainedContent(t.Context(), request, identity, snapshot, newDigest); err != nil {
		t.Fatal(err)
	}
	metadata.Identity.DelegatedContentDigest = newDigest
	body := strings.Replace(item.Body, "Keep preview wrapping unchanged.", amendedProof, 1)
	body = strings.Replace(body, "- Preview wrapping is unchanged.", "- "+amendedProof, 1)
	return service, project, runner, metadata, body
}

func TestAmendmentPreservesCandidateHistoryAndBindsBothRolesToRevisedProof(t *testing.T) {
	service, project, runner, metadata, body := amendmentFixture(t)
	before, sibling := project.remoteItems[0], project.remoteItems[1]
	oldContent := github.DelegatedContentFor(before)
	if err := service.saveReviewFeedback(before, oldContent, execution.ReviewAssessment{Verdict: "needs_changes", Summary: "Preview wrapping changed."}, nil); err != nil {
		t.Fatal(err)
	}
	history, err := os.ReadFile(service.reviewFeedbackPath(before.ID))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.saveVerificationEvidence(before, oldContent, metadata, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree},
		[]string{"Preview wrapping is unchanged."}, []string{"Historical test report: preview width assertions passed."}); err != nil {
		t.Fatal(err)
	}
	oldVerification, err := os.ReadFile(service.verificationEvidencePath(before.ID))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanRequirementAmendment(t.Context(), before.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project.remoteItems[0], before) {
		t.Fatal("preview mutated the card")
	}
	after, err := service.ApplyRequirementAmendment(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "Blocked" || after.Phase != "agent_qa" || after.QAFailures != before.QAFailures || after.Branch != before.Branch || after.Body != body {
		t.Fatalf("amendment changed unrelated state: status=%s phase=%s failures=%d", after.Status, after.Phase, after.QAFailures)
	}
	if !reflect.DeepEqual(project.remoteItems[1], sibling) {
		t.Fatal("amendment rewrote sibling authority")
	}
	newContent := github.DelegatedContentFor(after)
	if newContent.Digest == oldContent.Digest {
		t.Fatal("amendment did not renew the approved-content identity")
	}
	prepared, err := workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(after, newContent.Digest, service.cfg.ProjectDir, false))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.WorktreePath != metadata.WorktreePath || prepared.Identity.DelegatedContentDigest != newContent.Digest {
		t.Fatal("candidate workspace was not retained and rebound")
	}
	snapshot, err = workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, prepared.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || snapshot.Head != plan.Candidate.Head || snapshot.Tree != plan.Candidate.Tree || !snapshot.Clean {
		t.Fatalf("candidate changed: %v", err)
	}
	for _, role := range []string{config.WorkRoleImplementer, config.WorkRoleReviewer} {
		after.Role = role
		assignment := service.assignment(after, newContent, nil, nil)
		if !reflect.DeepEqual(assignment.Spec.RequiredVerification, []string{amendedProof}) || assignment.Spec.ApprovedBodySnapshot != body {
			t.Fatalf("%s received stale requirements", role)
		}
	}
	if record, err := service.loadReviewFeedbackRecord(after, newContent); err != nil || record != nil {
		t.Fatalf("old rejection was reused as current proof: %v", err)
	}
	retained, _ := os.ReadFile(service.reviewFeedbackPath(before.ID))
	if string(retained) != string(history) {
		t.Fatal("historical QA feedback changed")
	}
	if proof, err := service.loadVerificationEvidence(after, newContent, prepared, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}, []string{amendedProof}); err != nil || proof != nil {
		t.Fatalf("old evidence blocks or certifies amended QA: %v", err)
	}
	archived, err := os.ReadFile(service.verificationEvidencePath(before.ID) + ".superseded-" + plan.VerificationDigest)
	if err != nil || string(archived) != string(oldVerification) {
		t.Fatalf("historical proof was not preserved: %v", err)
	}
	retry, err := service.PlanProjectItemRetry(t.Context(), before.ID)
	if err != nil || retry.TargetLaneID != "agent_qa" {
		t.Fatalf("amended card cannot resume QA: %v", err)
	}
	if _, err := service.ApplyRequirementAmendment(t.Context(), plan); err == nil {
		t.Fatal("amendment preview was replayed")
	}
}

func TestAmendmentRejectsUnapprovedOrChangedState(t *testing.T) {
	for _, mutation := range []string{"body", "candidate", "dirty", "preview", "metadata", "unsigned", "published", "acceptance", "verification", "active", "sibling", "worker"} {
		t.Run(mutation, func(t *testing.T) {
			service, project, runner, metadata, body := amendmentFixture(t)
			plan, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[0].ID, body)
			if err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "body":
				project.remoteItems[0].Body += "\noperator edit"
			case "candidate":
				if _, err := runEngineTestGit(t.Context(), []string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "new candidate"}, metadata.WorktreePath, 30*time.Second); err != nil {
					t.Fatal(err)
				}
			case "dirty":
				if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "unreviewed.txt"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "preview":
				plan.NewProof = []string{"silently accept"}
			case "metadata":
				plan.Approval.Body = strings.Replace(body, `"planning_batch_size":2`, `"planning_batch_size":1`, 1)
			case "unsigned":
				project.remoteItems[0].Approval = ""
			case "published":
				project.remoteItems[0].PullRequest = "https://github.com/owner/repo/pull/2"
				project.remoteItems[0].Approval = testApproval(project.remoteItems[0])
			case "acceptance":
				snapshot, err := service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := workspace.NewGitProvider(runner).RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Accepted report", "Accepted candidate"); err != nil {
					t.Fatal(err)
				}
			case "verification":
				item := project.remoteItems[0]
				if err := service.saveVerificationEvidence(item, github.DelegatedContentFor(item), metadata,
					workspace.Candidate{CommitOID: plan.Candidate.Head, TreeOID: plan.Candidate.Tree}, plan.OldProof, []string{"New report after preview"}); err != nil {
					t.Fatal(err)
				}
			case "active":
				project.remoteItems[0].Status = "In Progress"
				project.remoteItems[0].Approval = testApproval(project.remoteItems[0])
			case "sibling":
				project.remoteItems[1].Approval = ""
			case "worker":
				lock, err := github.AcquireProcessLock(service.cfg.GitHubProject.GitHubProjectConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Release()
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if _, err := service.ApplyRequirementAmendment(t.Context(), plan); err == nil {
				t.Fatal("unsafe amendment accepted")
			}
			if !reflect.DeepEqual(project.remoteItems, before) {
				t.Fatal("refusal changed cards")
			}
		})
	}
}

func TestAmendmentEvidenceArchivePreservesInterveningFiles(t *testing.T) {
	for _, change := range []string{"source", "archive", "existing_identical_archive"} {
		t.Run(change, func(t *testing.T) {
			service, project, _, metadata, body := amendmentFixture(t)
			item := project.remoteItems[0]
			plan, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.saveVerificationEvidence(item, github.DelegatedContentFor(item), metadata,
				workspace.Candidate{CommitOID: plan.Candidate.Head, TreeOID: plan.Candidate.Tree}, plan.OldProof, []string{"Retained report"}); err != nil {
				t.Fatal(err)
			}
			path := service.verificationEvidencePath(item.ID)
			digest, err := amendmentEvidenceDigest(path)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			archive := path + ".superseded-" + digest
			changedPath, changedData := archive, []byte("operator-owned content")
			if change == "source" {
				changedPath = path
			} else if change == "existing_identical_archive" {
				changedData = data
			}
			if err := os.WriteFile(changedPath, changedData, 0o600); err != nil {
				t.Fatal(err)
			}
			err = service.archiveAmendedVerification(item.ID, digest)
			if (err == nil) != (change == "existing_identical_archive") {
				t.Fatalf("unexpected archive result: %v", err)
			}
			retained, readErr := os.ReadFile(changedPath)
			if readErr != nil || string(retained) != string(changedData) {
				t.Fatalf("intervening file was not preserved: %v", readErr)
			}
		})
	}
}

func TestAmendmentRollsBackFailedRemoteWrite(t *testing.T) {
	for _, failure := range []string{"body", "approval"} {
		t.Run(failure, func(t *testing.T) {
			service, project, _, metadata, body := amendmentFixture(t)
			before := project.remoteItems[0]
			plan, err := service.PlanRequirementAmendment(t.Context(), before.ID, body)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "body" {
				project.failBodyEditAt = 1
			} else {
				project.failApprovalAt = 1
			}
			if _, err := service.ApplyRequirementAmendment(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "restored") {
				t.Fatalf("failure was not safely restored: %v", err)
			}
			if !reflect.DeepEqual(project.remoteItems[0], before) {
				t.Fatal("failed amendment changed card")
			}
			identity, err := service.validateReauthorizationWorkspace(t.Context(), before)
			if err != nil || identity != metadata.Identity {
				t.Fatalf("failed amendment changed workspace binding: %v", err)
			}
		})
	}
}

type lostAmendmentResponse struct{ *recoveryTestRunner }

func (r lostAmendmentResponse) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := r.recoveryTestRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command == "gh" && strings.Contains(strings.Join(args, " "), "updateProjectV2ItemFieldValue") {
		return result, errors.New("response lost after write")
	}
	return result, err
}

func TestAmendmentReadbackRecognizesCommittedMutation(t *testing.T) {
	service, project, runner, _, body := amendmentFixture(t)
	cfg := completeEngineTestConfig(config.Config{ProjectDir: service.cfg.ProjectDir, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	restarted, err := New(cfg, lostAmendmentResponse{runner})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := restarted.PlanRequirementAmendment(t.Context(), project.remoteItems[0].ID, body)
	if err != nil {
		t.Fatal(err)
	}
	item, err := restarted.ApplyRequirementAmendment(t.Context(), plan)
	if err != nil || item.Body != body || project.remoteItems[0].Transition != "" {
		t.Fatalf("committed amendment not recognized: %v", err)
	}
	if _, err := restarted.validateReauthorizationWorkspace(t.Context(), item); err != nil {
		t.Fatalf("committed identity was rolled back: %v", err)
	}
}

func TestAmendmentCannotReleaseAnUnapprovedBatch(t *testing.T) {
	service, _, _, children := stagedPlannerBatchFixture(t, 2)
	if _, err := service.source.PlanAmendment(t.Context(), children[0].ID, children[0].Body+"\nnew scope"); err == nil {
		t.Fatal("amendment bypassed complete-batch approval")
	}
}

func TestAmendmentUpdatesReleasedSourceBindingWithoutChangingSibling(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, source, children := stagedPlannerBatchFixture(t, 2)
	approval, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), approval); err != nil {
		t.Fatal(err)
	}
	for index := range project.remoteItems {
		item := &project.remoteItems[index]
		if item.ID == children[0].ID {
			item.Phase, item.Branch, item.Status = "ready", "runner/retained", "Blocked"
			item.Approval = testApproval(*item)
		}
	}
	before := append([]github.WorkItem(nil), project.remoteItems...)
	plan, err := service.source.PlanAmendment(t.Context(), children[0].ID, strings.Replace(children[0].Body, "The child works.", "The child preserves saved email while allowing preview reflow.", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.source.ApplyAmendment(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	for index, item := range project.remoteItems {
		if item.ID == children[1].ID && !reflect.DeepEqual(item, before[index]) {
			t.Fatal("amended child changed sibling approval")
		}
		if item.ID == source.ID {
			if item.Approval == before[index].Approval {
				t.Fatal("released source still binds obsolete child content")
			}
			item.Approval = before[index].Approval
			if !reflect.DeepEqual(item, before[index]) {
				t.Fatal("source release changed other fields")
			}
		}
	}
	// A valid second preview proves the released batch still authenticates
	// exactly these children, rather than bypassing batch eligibility.
	if _, err := service.source.PlanAmendment(t.Context(), children[0].ID, children[0].Body); err != nil {
		t.Fatalf("released batch authority is no longer valid: %v", err)
	}
}

func TestAmendmentDoesNotOverwriteAnOperatorEditDuringMutation(t *testing.T) {
	service, project, runner, _, body := amendmentFixture(t)
	plan, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[0].ID, body)
	if err != nil {
		t.Fatal(err)
	}
	runner.afterLock = func() { project.remoteItems[0].Body += "\nConcurrent operator edit." }
	if _, err := service.ApplyRequirementAmendment(t.Context(), plan); err == nil {
		t.Fatal("concurrent edit accepted")
	}
	if !strings.HasSuffix(project.remoteItems[0].Body, "Concurrent operator edit.") || project.bodyEditWrites != 0 {
		t.Fatal("operator edit overwritten")
	}
}
