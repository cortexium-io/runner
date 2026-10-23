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

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func dirtyAmendmentFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata, string) {
	t.Helper()
	service, project, runner, metadata := branchlessReauthorizationFixture(t)
	preview, err := service.PlanProjectItemReauthorization(t.Context(), project.remoteItems[3].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemReauthorization(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	// Keep different staged and unstaged bytes plus a separate untracked file.
	path := filepath.Join(metadata.WorktreePath, "index.txt")
	if err := os.WriteFile(path, []byte("staged bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runEngineTestGit(t.Context(), []string{"add", "index.txt"}, metadata.WorktreePath, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unstaged bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	feedback := service.reviewFeedbackPath(project.remoteItems[3].ID)
	if err := os.MkdirAll(filepath.Dir(feedback), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedback, []byte("original private feedback"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedback+".historical", []byte(`{"attempt":"original","deadline":"2026-10-22T15:00:00Z","correction_used":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(project.remoteItems[3].Body, "The child works.", "The child preserves every original assignment while extending only the approved bounded search.", 1)
	return service, project, runner, metadata, body
}

func TestDirtyAmendmentPreservesGitHistoryAndCompleteLegacyBatch(t *testing.T) {
	service, project, runner, metadata, body := dirtyAmendmentFixture(t)
	before := append([]github.WorkItem(nil), project.remoteItems...)
	item := before[3]
	index, err := runEngineTestGit(t.Context(), []string{"rev-parse", "--git-path", "index"}, metadata.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{strings.TrimSpace(index.Stdout), filepath.Join(metadata.WorktreePath, "unfinished.txt"), filepath.Join(metadata.WorktreePath, "index.txt"), service.reviewFeedbackPath(item.ID), service.reviewFeedbackPath(item.ID) + ".historical"}
	retained := map[string][]byte{}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		retained[path] = content
	}
	preview, err := service.PlanRequirementAmendment(t.Context(), item.ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DirtyRetained || preview.Candidate.Clean || preview.Workspace != metadata.Identity || preview.IndexFingerprint == "" || preview.EvidenceDigests["qa_feedback"] == "" || preview.Approval.PlanningSource.ID != before[0].ID || len(preview.Approval.PlanningMembers) != 5 {
		t.Fatalf("incomplete dirty retained preview: %+v", preview)
	}
	// Preview then cancellation (no apply) must leave every remote and local byte.
	if !reflect.DeepEqual(before, project.remoteItems) {
		t.Fatal("preview changed cards")
	}
	for path, expected := range retained {
		actual, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("preview changed %s: %v", path, err)
		}
	}
	after, err := service.ApplyRequirementAmendment(t.Context(), preview)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "Blocked" || after.Body != body || after.Phase != "ready" || after.Branch != "" || after.QAFailures != 0 {
		t.Fatal("amendment did not leave only the revised action Blocked")
	}
	for i, current := range project.remoteItems {
		switch i {
		case 0:
			if current.Approval == before[i].Approval {
				t.Fatal("source still binds old requirements")
			}
			current.Approval = before[i].Approval
		case 3:
			current.Body, current.Status, current.Result, current.Approval = before[i].Body, before[i].Status, before[i].Result, before[i].Approval
		}
		if !reflect.DeepEqual(current, before[i]) {
			t.Fatalf("source, sibling, metadata or counter changed: %s", current.ID)
		}
	}
	for path, expected := range retained {
		actual, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("amendment changed %s: %v", path, err)
		}
	}
	current, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || !reflect.DeepEqual(current, preview.Candidate) {
		t.Fatalf("HEAD/index/dirty snapshot changed: %v", err)
	}
	identity, err := service.validateReauthorizationWorkspace(t.Context(), after)
	if err != nil || identity.DelegatedContentDigest != github.DelegatedContentFor(after).Digest {
		t.Fatalf("new content identity invalid: %v", err)
	}
	identity.DelegatedContentDigest = metadata.Identity.DelegatedContentDigest
	if identity != metadata.Identity {
		t.Fatal("amendment changed another identity field")
	}
	if _, err := service.source.PlanAmendment(t.Context(), after.ID, item.Body, false); err != nil {
		t.Fatalf("complete revised batch does not authenticate: %v", err)
	}
	if _, err := service.PlanProjectItemRetry(t.Context(), after.ID); err != nil {
		t.Fatalf("separate ordinary retry unavailable: %v", err)
	}
}

func TestDirtyAmendmentRefusesChangedOrUnsafeState(t *testing.T) {
	for _, change := range []string{"identity", "identity content", "assessment", "content", "source", "source counter", "sibling", "sibling counter", "missing member", "active", "transition", "checkpoint", "verification", "feedback", "dirty file", "index", "HEAD", "preview", "PR", "QA commit", "remote PR", "worker", "planning", "ownership"} {
		t.Run(change, func(t *testing.T) {
			service, project, runner, metadata, body := dirtyAmendmentFixture(t)
			preview, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, body)
			if err != nil {
				t.Fatal(err)
			}
			item := &project.remoteItems[3]
			switch change {
			case "identity":
				err = os.Remove(filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json"))
			case "identity content":
				path := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
				content, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.WriteFile(path, []byte(strings.Replace(string(content), metadata.Identity.DelegatedContentDigest, "v1:mismatch", 1)), 0o600)
			case "assessment":
				item.Status, item.Approval = "Needs assessment", ""
			case "content":
				item.Body += "\nConcurrent scope change"
			case "source":
				project.remoteItems[0].Approval = "invalid"
			case "source counter":
				project.remoteItems[0].QAFailures++
			case "sibling":
				project.remoteItems[2].Approval = "invalid"
			case "sibling counter":
				project.remoteItems[2].QAFailures++
				project.remoteItems[2].Approval = testApproval(project.remoteItems[2])
			case "missing member":
				project.remoteItems = append(project.remoteItems[:5], project.remoteItems[6:]...)
			case "active":
				item.Activity = "Implementing"
				item.Approval = testApproval(*item)
			case "transition":
				item.Transition = "v1"
			case "checkpoint", "verification", "feedback":
				path := map[string]string{"checkpoint": service.implementationCheckpointPath(item.ID), "verification": service.verificationEvidencePath(item.ID), "feedback": service.reviewFeedbackPath(item.ID)}[change]
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, []byte("intervening private record"), 0o600)
			case "dirty file":
				err = os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("intervening bytes"), 0o644)
			case "index":
				_, err = runEngineTestGit(t.Context(), []string{"add", "index.txt"}, metadata.WorktreePath, 30*time.Second)
			case "HEAD":
				_, err = runEngineTestGit(t.Context(), []string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "intervening"}, metadata.WorktreePath, 30*time.Second)
			case "preview":
				preview.EvidenceDigests["qa_feedback"] = "forged"
			case "PR":
				item.PullRequest = "https://github.com/owner/repo/pull/20"
				item.Approval = testApproval(*item)
			case "QA commit":
				item.QACommit = preview.Candidate.Head
				item.Approval = testApproval(*item)
			case "remote PR":
				runner.pullRequests = `[{"number":20,"url":"https://github.com/owner/repo/pull/20","headRefName":"` + metadata.BranchName + `","headRepository":{"nameWithOwner":"owner/repo"},"baseRefName":"main"}]`
			case "worker", "planning":
				acquire := github.AcquireProcessLock
				if change == "planning" {
					acquire = github.AcquirePlanningLock
				}
				lock, e := acquire(service.cfg.GitHubProject.GitHubProjectConfig)
				if e != nil {
					t.Fatal(e)
				}
				defer lock.Release()
			case "ownership":
				service.processOwnership.RecordCleanupFailure()
			}
			if err != nil {
				t.Fatal(err)
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil {
				t.Fatal("unsafe dirty amendment accepted")
			}
			if !reflect.DeepEqual(before, project.remoteItems) {
				t.Fatal("refusal wrote remote state")
			}
		})
	}
}

func TestDirtyAmendmentRollsBackFailedWritesWithoutTouchingGit(t *testing.T) {
	for _, failure := range []string{"body", "source approval", "selected approval", "sibling during lock"} {
		t.Run(failure, func(t *testing.T) {
			service, project, runner, metadata, body := dirtyAmendmentFixture(t)
			preview, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, body)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			switch failure {
			case "body":
				project.failBodyEditAt = project.bodyEditWrites + 1
			case "source approval":
				project.failApprovalAt = project.approvalWrites + 1
			case "selected approval":
				project.failApprovalAt = project.approvalWrites + 2
			case "sibling during lock":
				runner.afterLock = func() {
					project.remoteItems[2].QAFailures++
					project.remoteItems[2].Approval = testApproval(project.remoteItems[2])
					before[2] = project.remoteItems[2]
				}
			}
			if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil {
				t.Fatal("failed/changed remote apply succeeded")
			}
			if !reflect.DeepEqual(before, project.remoteItems) {
				t.Fatal("remote rollback lost original cohort or intervening sibling")
			}
			identity, err := service.validateReauthorizationWorkspace(t.Context(), before[3])
			if err != nil || identity != metadata.Identity {
				t.Fatalf("original binding not restored: %v", err)
			}
			snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
			if err != nil || !reflect.DeepEqual(snapshot, preview.Candidate) {
				t.Fatalf("rollback changed retained Git work: %v", err)
			}
		})
	}
}

type dirtyAmendmentUncertainRunner struct {
	*recoveryTestRunner
	afterWrite bool
}

func (r *dirtyAmendmentUncertainRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	if r.afterWrite && command == "gh" && (isDirectProjectItemCall(joined) || isLifecycleItemsCall(joined)) {
		return subprocess.Result{}, errors.New("readback unavailable")
	}
	result, err := r.recoveryTestRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command == "gh" && len(args) > 1 && args[0] == "issue" && args[1] == "edit" {
		r.afterWrite = true
		return result, errors.New("body write response lost")
	}
	return result, err
}

func TestDirtyAmendmentUncertainWriteRetainsBindingAndTransitionForInspection(t *testing.T) {
	service, project, runner, metadata, body := dirtyAmendmentFixture(t)
	uncertain := &dirtyAmendmentUncertainRunner{recoveryTestRunner: runner}
	service.run = uncertain
	var err error
	service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, uncertain)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil || !strings.Contains(err.Error(), "leave Runner stopped") {
		t.Fatalf("uncertain outcome hidden: %v", err)
	}
	item := project.remoteItems[3]
	if item.Body != body || item.Transition != "v1" || item.QAFailures != 0 {
		t.Fatal("uncertain write lost its fence or work")
	}
	// The server may have accepted the body: do not silently restore the old
	// private content binding while remote readback is unavailable.
	identity, err := service.validateReauthorizationWorkspace(t.Context(), item)
	if err != nil || identity.DelegatedContentDigest != github.DelegatedContentFor(item).Digest {
		t.Fatalf("uncertain binding was unsafely rolled back: %v", err)
	}
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || !reflect.DeepEqual(snapshot, preview.Candidate) {
		t.Fatalf("uncertain write changed Git: %v", err)
	}
}

func TestDirtyAmendmentReadbackRecognizesCommittedScopeAndSource(t *testing.T) {
	service, project, runner, metadata, body := dirtyAmendmentFixture(t)
	lost := lostAmendmentResponse{runner}
	service.run = lost
	var err error
	service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, lost)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, body)
	if err != nil {
		t.Fatal(err)
	}
	after, err := service.ApplyRequirementAmendment(t.Context(), preview)
	if err != nil || after.Body != body || after.Status != "Blocked" || project.remoteItems[3].Transition != "" {
		t.Fatalf("committed amendment was not recognized: %v", err)
	}
	if _, err := service.validateReauthorizationWorkspace(t.Context(), after); err != nil {
		t.Fatalf("committed content binding was rolled back: %v", err)
	}
	if _, err := service.source.PlanAmendment(t.Context(), after.ID, preview.Approval.Item.Body, false); err != nil {
		t.Fatalf("committed cohort no longer authenticates: %v", err)
	}
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || !reflect.DeepEqual(snapshot, preview.Candidate) {
		t.Fatalf("committed readback changed Git: %v", err)
	}
}

func TestDirtyAmendmentRollbackRefusesInterveningWork(t *testing.T) {
	service, project, runner, metadata, body := dirtyAmendmentFixture(t)
	preview, err := service.PlanRequirementAmendment(t.Context(), project.remoteItems[3].ID, body)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(metadata.WorktreePath, "unfinished.txt")
	runner.afterLock = func() {
		if err := os.WriteFile(path, []byte("concurrent operator work\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project.failBodyEditAt = project.bodyEditWrites + 1
	if _, err := service.ApplyRequirementAmendment(t.Context(), preview); err == nil || !strings.Contains(err.Error(), "leave Runner stopped") {
		t.Fatalf("unsafe local rollback was hidden: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "concurrent operator work\n" {
		t.Fatalf("rollback overwrote operator work: %v", err)
	}
	if project.remoteItems[3].Body != preview.Approval.Item.Body || project.remoteItems[3].QAFailures != 0 {
		t.Fatal("rollback changed old requirements or allowance")
	}
}
