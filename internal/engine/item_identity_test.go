package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestAssignmentIdentityPreservesOpaqueItemIDs(t *testing.T) {
	for _, pair := range [][2]string{
		{"PVTI_UPGRADE_ABC", "PVTI_UPGRADE_AbC"},
		{"PVTI_a.b", "PVTI_a-b"},
	} {
		t.Run(pair[0]+"_and_"+pair[1], func(t *testing.T) {
			first := github.WorkItem{ID: pair[0], Repository: "owner/repo"}
			second := github.WorkItem{ID: pair[1], Repository: "owner/repo"}
			repo, _ := createPublicationRepository(t)
			service := &Engine{cfg: config.RuntimeConfig{ProjectDir: repo}}
			requests := []workspace.Request{
				service.workspaceRequestForItem(first, "sha256:first", service.cfg.ProjectDir, true),
				service.workspaceRequestForItem(second, "sha256:second", service.cfg.ProjectDir, true),
			}
			provider := workspace.NewGitProvider(subprocess.OSRunner{})
			root := filepath.Join(t.TempDir(), "worktrees")
			var prepared []workspace.Metadata
			for _, request := range requests {
				request.BaseRef = "HEAD"
				request.WorktreeRoot = root
				metadata, err := provider.Prepare(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				resource, err := workspace.ResourceIdentity(request)
				if err != nil || resource != "owner/repo/"+metadata.BranchName {
					t.Fatalf("resource differs from prepared branch: %q, %#v, %v", resource, metadata, err)
				}
				prepared = append(prepared, metadata)
			}
			if strings.EqualFold(prepared[0].WorktreePath, prepared[1].WorktreePath) || strings.EqualFold(prepared[0].BranchName, prepared[1].BranchName) {
				t.Fatal("distinct opaque IDs share a case-folded workspace or branch")
			}
			if _, err := os.Stat(prepared[0].WorktreePath); err != nil {
				t.Fatalf("second item displaced the first workspace: %v", err)
			}
			if paths, _ := filepath.Glob(filepath.Join(filepath.Dir(prepared[0].WorktreePath), ".runner-quarantine", "*")); len(paths) != 0 {
				t.Fatalf("distinct item caused quarantine: %v", paths)
			}
			first.Branch = prepared[0].BranchName
			if service.assignmentWorkID(first) != requests[0].WorkID {
				t.Fatal("recording the new branch changed its workspace path")
			}
			cleanup, err := provider.Cleanup(t.Context(), workspace.CleanupRequest{
				WorkingDir: repo, WorktreeRoot: root, WorkID: service.assignmentWorkID(first),
				ItemID: first.ID, DelegatedContentDigest: "sha256:first", Repository: first.Repository,
				BranchName: first.Branch, BaseRef: "HEAD",
			})
			if err != nil || !cleanup.WorktreeRemoved {
				t.Fatalf("new branch could not clean up its own workspace: %#v, %v", cleanup, err)
			}
			if _, err := os.Stat(prepared[1].WorktreePath); err != nil {
				t.Fatalf("cleanup displaced the other item: %v", err)
			}
		})
	}
}

func TestAssignmentIdentityRetainsExistingBranchBinding(t *testing.T) {
	service := &Engine{}
	item := github.WorkItem{ID: "PVTI_Existing", Branch: "runner/assignment_pvti_existing"}
	if got := service.assignmentWorkID(item); got != "assignment_pvti_existing" {
		t.Fatalf("existing branch lost its path binding: %q", got)
	}
	item.Branch = "custom/retained"
	if got := service.assignmentWorkID(item); got != "assignment_"+itemRefComponent(item.ID) {
		t.Fatalf("custom branch without a private legacy binding reused a lossy name: %q", got)
	}
}

func TestAssignmentIdentityRetainsUnrecordedLegacyWorkOnlyForExactOwner(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	service := reviewFeedbackTestEngine(root)
	service.cfg.ProjectDir = repo
	item := github.WorkItem{ID: "PVTI_ABC", Repository: "owner/repo"}
	legacy := "assignment_" + safeRefComponent(item.ID)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: legacy, ItemID: item.ID,
		DelegatedContentDigest: "sha256:first", Repository: item.Repository, BranchPrefix: "runner", BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := service.workspaceRequestForItem(item, "sha256:first", repo, true)
	request.BaseRef = "origin/main"
	if spec := service.assignment(item, github.DelegatedContentFor(item), nil, nil).Spec; spec.ID != request.WorkID {
		t.Fatalf("executor and coordinator would prepare different workspaces: %q != %q", spec.ID, request.WorkID)
	}
	reused, err := provider.Prepare(t.Context(), request)
	if err != nil || reused.Identity != prepared.Identity {
		t.Fatalf("unrecorded first attempt lost its exact identity: %#v, %v", reused, err)
	}
	item.ID = "PVTI_AbC"
	request = service.workspaceRequestForItem(item, "sha256:second", repo, true)
	request.BaseRef = "origin/main"
	other, err := provider.Prepare(t.Context(), request)
	if err != nil || strings.EqualFold(other.WorktreePath, prepared.WorktreePath) {
		t.Fatalf("similar ID reused the legacy workspace: %#v, %v", other, err)
	}
	if _, err := os.Stat(prepared.WorktreePath); err != nil {
		t.Fatalf("legacy owner was displaced: %v", err)
	}
}

func TestPrivateStateNamesRetainOnlyExactLegacyOwner(t *testing.T) {
	root := t.TempDir()
	service := reviewFeedbackTestEngine(root)
	first, second := "PVTI_ABC", "PVTI_AbC"
	for _, category := range []struct {
		directory, prefix, owner string
		path                     func(string) string
	}{
		{"implementation", "implementation_", "item_id", service.implementationCheckpointPath},
		{"verification", "verification_", "item_id", service.verificationEvidencePath},
		{"qa-feedback", "review_", "item_id", service.reviewFeedbackPath},
		{"planning", "planning_", "source_id", service.plannerCheckpointPath},
	} {
		t.Run(category.directory, func(t *testing.T) {
			currentFirst := category.path(first)
			currentSecond := category.path(second)
			if strings.EqualFold(currentFirst, currentSecond) {
				t.Fatal("new private state names collide")
			}
			legacy := filepath.Join(filepath.Dir(currentFirst), category.prefix+safeRefComponent(first)+".json")
			if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
				t.Fatal(err)
			}
			contents := []byte(`{"` + category.owner + `":"` + first + `"}`)
			if err := os.WriteFile(legacy, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := category.path(first); got != legacy {
				t.Fatalf("exact owner's legacy record was lost: %q", got)
			}
			if got := category.path(second); got != currentSecond {
				t.Fatalf("different owner could overwrite legacy record: %q", got)
			}
			if err := os.WriteFile(legacy, []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := category.path(first); got != legacy {
				t.Fatal("invalid legacy record was silently bypassed")
			}
		})
	}
}

func TestQARequestRetainsCaseSafeWorkspaceAcrossUpgrade(t *testing.T) {
	for _, test := range []struct {
		name, itemID, workID, legacyOwner string
	}{
		{
			name:   "candidate with no legacy identity",
			itemID: "PVTI_UPGRADE_BACKEND",
			workID: "assignment_pvti_upgrade_backend-6b98d9efc600d5c9b539c43472cf9e0e",
		},
		{
			name:        "candidate beside case-colliding legacy identity",
			itemID:      "PVTI_UPGRADE_AbC",
			workID:      "assignment_pvti_upgrade_abc-dc7513611634b6434a421f2ca41ab48d",
			legacyOwner: "PVTI_UPGRADE_ABC",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			root := filepath.Join(t.TempDir(), "worktrees")
			service := reviewFeedbackTestEngine(root)
			service.cfg.ProjectDir = repo
			provider := workspace.NewGitProvider(subprocess.OSRunner{})
			var legacyPath string
			var legacyBefore []byte
			if test.legacyOwner != "" {
				legacyID := "assignment_" + safeRefComponent(test.legacyOwner)
				if _, err := provider.Prepare(t.Context(), workspace.Request{
					WorkingDir: repo, WorktreeRoot: root, WorkID: legacyID,
					ItemID: test.legacyOwner, Repository: "owner/repo", DelegatedContentDigest: "sha256:library",
					BranchPrefix: "runner", BaseRef: "origin/main",
				}); err != nil {
					t.Fatal(err)
				}
				legacyPath = filepath.Join(root, ".runner-state", legacyID+".json")
				var err error
				legacyBefore, err = os.ReadFile(legacyPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			item := github.WorkItem{
				ID: test.itemID, Repository: "owner/repo", Body: "Approved replacement",
				Branch: "runner/" + test.workID, Phase: "agent_qa",
			}
			content := github.DelegatedContentFor(item)
			prepared, err := provider.Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: root, WorkID: test.workID,
				ItemID: item.ID, Repository: item.Repository, DelegatedContentDigest: content.Digest,
				BranchPrefix: "runner", BranchName: item.Branch, BaseRef: "origin/main",
			})
			if err != nil {
				t.Fatal(err)
			}
			request := service.workspaceRequestForItem(item, content.Digest, repo, false)
			request.BaseRef = "origin/main"
			if request.WorkID != test.workID {
				t.Fatalf("QA looked up a different workspace: %q", request.WorkID)
			}
			if got := service.assignment(item, content, nil, nil).Spec.ID; got != request.WorkID {
				t.Fatalf("QA executor and identity reader disagree: %q != %q", got, request.WorkID)
			}
			identity, err := provider.ValidateRetainedIdentity(t.Context(), request)
			if err != nil || identity != prepared.Identity {
				t.Fatalf("QA lost the completed candidate's binding: %#v, %v", identity, err)
			}
			request.DelegatedContentDigest = "sha256:changed-requirements"
			if _, err := provider.ValidateRetainedIdentity(t.Context(), request); err == nil {
				t.Fatal("workspace naming compatibility bypassed content validation")
			}
			if legacyPath != "" {
				after, err := os.ReadFile(legacyPath)
				if err != nil || string(after) != string(legacyBefore) {
					t.Fatal("QA lookup changed the other item's private identity")
				}
			}
		})
	}
}
