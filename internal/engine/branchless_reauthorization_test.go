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

// This is a legacy planning-only release: two accepted children, an interrupted
// branchless third child, and two authenticated dependency waits. Plan delivery
// is deliberately disabled. The independent active card is outside this batch.
func branchlessReauthorizationFixture(t *testing.T) (*Engine, *fakeGitHubProjectRunner, *recoveryTestRunner, workspace.Metadata) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	service, project, source, _ := stagedPlannerBatchFixture(t, 5)
	approval, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), approval); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		item := &project.remoteItems[i]
		item.URL, item.IssueState = fmt.Sprintf("https://github.com/owner/repo/issues/%d", i), "OPEN"
		// Lifecycle queries select id only on DraftIssue, never on Issue.
		item.DraftContentID = ""
		if i < 3 {
			item.Status, item.Phase, item.Role = "Done", "", config.WorkRoleReviewer
			item.Branch, item.PullRequest, item.QACommit = fmt.Sprintf("runner/accepted-%d", i), fmt.Sprintf("https://github.com/owner/repo/pull/%d", i+10), strings.Repeat("a", 40)
		} else {
			item.Status, item.Phase = "Ready", "ready"
			item.Activity = config.RunnerActivityWaitingForDependencies
		}
		if i == 2 {
			item.QAFailures = 1
		}
		item.Approval = testApproval(*item)
	}
	item := &project.remoteItems[3]
	item.Status, item.Approval, item.Activity = "Needs assessment", "", ""
	item.Result = "Interrupted item has invalid Runner authority."
	independent := github.WorkItem{ID: "PVTI_independent", Title: "Independent active work", Body: "Other criteria", Repository: "owner/repo", Status: "In Progress", Phase: "ready", Activity: "Implementing", QAFailures: 1}
	independent.Approval = testApproval(independent)
	project.remoteItems = append(project.remoteItems, independent)
	repo, _ := createPublicationRepository(t)
	runner := &recoveryTestRunner{project: project}
	service.cfg.ProjectDir, service.run = repo, runner
	service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, runner)
	if err != nil {
		t.Fatal(err)
	}
	// Use the same canonical metadata/profile decoding as an actual execution.
	project.remoteItems, err = service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	item = &project.remoteItems[3]
	metadata, err := workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(*item, github.DelegatedContentFor(*item).Digest, repo, false))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("retained implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return service, project, runner, metadata
}

func TestBranchlessReauthorizationPreservesDirtyLegacyBatchAndPrivateEvidence(t *testing.T) {
	service, project, runner, metadata := branchlessReauthorizationFixture(t)
	service.EnableLocalAdmission()
	item := project.remoteItems[3]
	// Existing incomplete checkpoint carries the original deadline and spent
	// correction allowance. Reauthorization must preserve its exact bytes.
	files := map[string][]byte{
		service.implementationCheckpointPath(item.ID): []byte(`{"execution_deadline":"2026-10-22T15:00:00Z","correction_used":true,"incomplete":true}`),
		service.reviewFeedbackPath(item.ID):           []byte("retained private feedback"),
		service.verificationEvidencePath(item.ID):     []byte("retained verification history"),
	}
	identity := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
	files[identity], _ = os.ReadFile(identity)
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := append([]github.WorkItem(nil), project.remoteItems...)
	plan, err := service.PlanProjectItemReauthorization(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Approval.Unstarted || plan.Workspace != metadata.Identity || plan.RetainedSnapshot == nil || plan.RetainedSnapshot.Clean || len(plan.EvidenceDigests) != 3 || plan.Approval.PlanningSource.ID != before[0].ID || len(plan.Approval.PlanningMembers) != 5 {
		t.Fatalf("preview lost retained identity, dirty state or complete legacy batch: %+v", plan)
	}
	if !reflect.DeepEqual(before, project.remoteItems) {
		t.Fatal("preview changed the batch or independent active card")
	}
	if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	for i, current := range project.remoteItems {
		if i == 3 {
			if current.Status != "Ready" || current.Approval == "" || current.Result != plan.Approval.Result || current.Branch != "" || current.Phase != "ready" || current.QAFailures != 0 {
				t.Fatal("recovery did not restore only the retained implementation action")
			}
			current.Status, current.Approval, current.Result = before[i].Status, before[i].Approval, before[i].Result
		}
		if !reflect.DeepEqual(current, before[i]) {
			t.Fatalf("recovery changed source, sibling, active work or another selected field: %s", current.ID)
		}
	}
	for path, expected := range files {
		content, err := os.ReadFile(path)
		if err != nil || !reflect.DeepEqual(content, expected) {
			t.Fatalf("private evidence changed: %s: %v", path, err)
		}
	}
	snapshot, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), runner, metadata.WorktreePath, 30*time.Second, service.snapshotLimits())
	if err != nil || !reflect.DeepEqual(snapshot, *plan.RetainedSnapshot) {
		t.Fatalf("HEAD, index, control state or dirty work changed: %v", err)
	}
	for _, eligibility := range service.source.EvaluateWorkEligibility(project.remoteItems) {
		if eligibility.Item.ID == item.ID && !eligibility.Eligible {
			t.Fatalf("restored legacy batch does not authenticate: %+v", eligibility)
		}
	}
}

func TestBranchlessReauthorizationRefusesUnsafeAndChangedEvidence(t *testing.T) {
	for _, change := range []string{"missing identity", "identity content", "body", "missing member", "source authority", "source transition", "sibling authority", "activity", "transition", "published", "qa identity", "ambiguous lane", "dirty work", "private evidence", "sibling counter", "source result", "remote PR"} {
		t.Run(change, func(t *testing.T) {
			service, project, runner, metadata := branchlessReauthorizationFixture(t)
			item := &project.remoteItems[3]
			plan, err := service.PlanProjectItemReauthorization(t.Context(), item.ID)
			if err != nil {
				t.Fatal(err)
			}
			identity := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", filepath.Base(metadata.WorktreePath)+".json")
			switch change {
			case "missing identity":
				err = os.Remove(identity)
			case "identity content":
				content, readErr := os.ReadFile(identity)
				if readErr != nil {
					t.Fatal(readErr)
				}
				err = os.WriteFile(identity, []byte(strings.Replace(string(content), metadata.Identity.DelegatedContentDigest, "v1:mismatch", 1)), 0o600)
			case "missing member":
				project.remoteItems = append(project.remoteItems[:5], project.remoteItems[6:]...)
			case "body":
				item.Body += "\nUnapproved scope change"
			case "source authority":
				project.remoteItems[0].Approval = "invalid"
			case "source transition":
				project.remoteItems[0].Transition = "v1"
			case "sibling authority":
				project.remoteItems[2].Approval = "invalid"
			case "activity":
				item.Activity = "Implementing"
			case "transition":
				item.Transition = "v1"
			case "published":
				item.PullRequest = "https://github.com/owner/repo/pull/20"
			case "qa identity":
				item.QACommit = strings.Repeat("b", 40)
			case "ambiguous lane":
				service.cfg.GitHubProject.LaneRoles["other"] = config.WorkRoleImplementer
				service.cfg.GitHubProject.LaneStatuses["other"] = "Ready"
			case "dirty work":
				err = os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("intervening work"), 0o644)
			case "private evidence":
				path := service.reviewFeedbackPath(item.ID)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(path, []byte("intervening feedback"), 0o600)
			case "sibling counter":
				project.remoteItems[2].QAFailures++
				project.remoteItems[2].Approval = testApproval(project.remoteItems[2])
			case "source result":
				project.remoteItems[0].Result += "Changed after preview"
			case "remote PR":
				runner.pullRequests = `[{"number":20,"url":"https://github.com/owner/repo/pull/20","headRefName":"` + metadata.BranchName + `","headRepository":{"nameWithOwner":"owner/repo"},"baseRefName":"main"}]`
			}
			if err != nil {
				t.Fatal(err)
			}
			before := append([]github.WorkItem(nil), project.remoteItems...)
			if _, err := service.ApplyProjectItemReauthorization(t.Context(), plan); err == nil {
				t.Fatal("unsafe or intervening evidence was accepted")
			}
			if !reflect.DeepEqual(before, project.remoteItems) {
				t.Fatal("refusal wrote Project state")
			}
		})
	}
}
