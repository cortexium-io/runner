package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

func membershipAdoption(t *testing.T, f *deliveryRunFixture) (github.PlanAmendmentRequest, github.WorkItem) {
	t.Helper()
	parent := f.parent(t)
	manifest, _, err := github.ParsePlanManifest(parent.Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Amendment++
	item := github.WorkItem{ID: "PVTI_adopted", Title: "Approved extra test", Body: "## Acceptance criteria\n- Protect the exact additional requirement.\n\n## Proof obligations\n- Show the focused regression detects that failure.", Repository: "owner/repo", URL: "https://github.com/owner/repo/issues/999", IssueState: "OPEN", Status: f.service.cfg.GitHubProject.AssessmentStatus}
	f.runner.project.remoteItems = append(f.runner.project.remoteItems, item)
	manifest.Members = append(manifest.Members, github.PlanMember{ID: item.ID, Dependencies: []string{}, ImplementationProfile: "implementer", ProfileDigest: f.service.cfg.GitHubProject.PlanProfileDigests["implementer"], ProfileReason: "Explicitly approved bounded additional requirement"})
	return github.PlanAmendmentRequest{ExpectedRevision: github.PlanRevision(parent.Body), Reason: "Adopt the existing reviewed Assessment issue", Manifest: manifest}, item
}

func TestMembershipAmendmentRetainsIntegratedCodeAndAdoptsExactIssue(t *testing.T) {
	f := newProductionDelivery(t)
	f.integrateMembers(t)
	request, added := membershipAdoption(t, f)
	retiredID := request.Manifest.Members[1].ID
	request.Manifest.Members[1].Retired, request.Manifest.Members[1].RetirementReason = true, "Retire this outcome; its integrated code remains"
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.RetiredMembers) != 1 || !strings.Contains(preview.RetiredMembers[0].OriginalAcceptedDelta, "member-2.txt") || preview.RetiredMembers[0].AcceptedCommit == "" || len(preview.AddedMembers) != 1 || preview.AddedMembers[0].Body != added.Body || !reflect.DeepEqual(preview.Affected, []string{added.ID}) {
		t.Fatalf("membership preview lost adoption/retained delta: %#v", preview)
	}
	head := f.parent(t).QACommit
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.service.source.ValidatePlanDelivery(f.parent(t), items)
	if err != nil || len(d.Children) != 2 || len(d.RetiredChildren) != 1 || d.Parent.QACommit != head {
		t.Fatalf("amended union: %v", err)
	}
	retired := d.RetiredChildren[0]
	if retired.ID != retiredID || retired.Phase != github.PlanRetiredPhase || retired.IssueState != "OPEN" {
		t.Fatal("retired card lost identity or pretended completion")
	}
	if got := strings.TrimSpace(runGitTest(t, "", "--git-dir", f.remote, "show", head+":member-2.txt")); got != "ready" {
		t.Fatal("retirement removed integrated code")
	}
	oldWorkspace := preview.record.Workspaces[2]
	if _, err := os.Stat(filepath.Join(oldWorkspace.Identity.WorktreePath, "member-2.txt")); err != nil {
		t.Fatal("retirement removed retained worktree")
	}
	ready, err := f.service.source.ReadyItems(t.Context(), items, len(items))
	if err != nil || len(ready) != 1 || ready[0].Item.ID != added.ID {
		t.Fatalf("retirement/adoption admission: %v %#v", err, ready)
	}
	progress := f.service.source.PlanningProgress(items)
	if len(progress.RetiredMembers) != 1 {
		t.Fatal("retired scope not displayed distinctly")
	}
	writes := f.runner.project.bodyEditWrites
	repeated, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil || !repeated.AlreadyApplied || repeated.Digest != preview.Digest {
		t.Fatalf("exact repeat not recognized: %v", err)
	}
	if err := f.service.ApplyDeliveryAmendment(t.Context(), repeated); err != nil {
		t.Fatal(err)
	}
	if f.runner.project.bodyEditWrites != writes || f.runner.implementations != 2 || f.runner.reviews != 2 || f.runner.creates != 0 {
		t.Fatal("exact repeat duplicated contract/model/publication work")
	}
}

func TestMembershipAdoptionResumesPartialAttachmentAndRefusesUnknownMembers(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "exact recovery"
		if foreign {
			name = "unknown extra"
		}
		t.Run(name, func(t *testing.T) {
			f := newProductionDelivery(t)
			request, added := membershipAdoption(t, f)
			transport := &amendmentCrashRunner{deliveryMilestoneRunner: f.runner, boundary: "body"}
			var err error
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil || !transport.offline {
				t.Fatalf("partial attachment boundary not reached: %v", err)
			}
			transport.offline = false
			transport.boundary = ""
			items, err := f.service.source.LifecycleItems(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var attached github.WorkItem
			for _, item := range items {
				if item.ID == added.ID {
					attached = item
				}
			}
			if attached.PlanningSourceID != f.parentID || attached.Body == added.Body || attached.Transition == "" {
				t.Fatal("fixture did not stop after actual attachment before authority writes")
			}
			if foreign {
				extra := attached
				extra.ID = "PVTI_unknown"
				extra.URL = "https://github.com/owner/repo/issues/1000"
				f.runner.project.remoteItems = append(f.runner.project.remoteItems, extra)
			}
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.service.preparePoll(t.Context(), 0, true, nil)
			if foreign {
				if err == nil {
					t.Fatal("unknown extra member ignored during partial adoption")
				}
				if f.parent(t).Phase != github.PlanAmendingPhase {
					t.Fatal("unknown membership released the admission fence")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				items, err = f.service.source.LifecycleItems(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				d, err := f.service.source.ValidatePlanDelivery(f.parent(t), items)
				if err != nil || len(d.Children) != 3 {
					t.Fatalf("exact union not recovered: %v", err)
				}
				if f.parent(t).Phase != github.PlanDeliveryPhase {
					t.Fatal("recovery did not finish exact approved intent")
				}
			}
			if f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.creates != 0 {
				t.Fatal("adoption recovery repeated model/publication work")
			}
		})
	}
}

func TestMembershipAdoptionPreservesInterveningAssessmentEdit(t *testing.T) {
	f := newProductionDelivery(t)
	request, added := membershipAdoption(t, f)
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil {
		t.Fatal(err)
	}
	for i := range f.runner.project.remoteItems {
		if f.runner.project.remoteItems[i].ID == added.ID {
			f.runner.project.remoteItems[i].Body += "\nOperator changed the requirement."
		}
	}
	writes := f.runner.project.bodyEditWrites
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil {
		t.Fatal("changed Assessment body was silently adopted")
	}
	if f.runner.project.bodyEditWrites != writes || f.parent(t).Phase != github.PlanDeliveryPhase {
		t.Fatal("stale preview wrote authority or fenced unrelated current work")
	}
}

// Each artifact exists without a private identity. In particular the detached
// registration has no branch and its original path is missing, so checking
// only the filesystem or only a branch cannot prove absence.
func orphanAdoptionArtifact(t *testing.T, f *deliveryRunFixture, item github.WorkItem, artifact string) func() {
	t.Helper()
	request := f.service.workspaceRequestForItem(item, github.DelegatedContentFor(item).Digest, f.repo, false)
	resource, err := workspace.ResourceIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	branch := strings.TrimPrefix(resource, strings.ToLower(item.Repository)+"/")
	root := request.WorktreeRoot
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(f.repo), root)
	}
	if err := securefs.EnsurePrivateDir(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, request.WorkID)
	marker := ""
	switch artifact {
	case "branch":
		runGitTest(t, f.repo, "branch", branch, "HEAD")
	case "registration":
		runGitTest(t, f.repo, "worktree", "add", "--detach", path, "HEAD")
		moved := filepath.Join(t.TempDir(), "retained-worktree")
		if err := os.Rename(path, moved); err != nil {
			t.Fatal(err)
		}
		marker = filepath.Join(moved, "operator-work.txt")
	case "path":
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		marker = filepath.Join(path, "operator-work.txt")
	default:
		t.Fatal("unknown artifact")
	}
	if marker != "" {
		if err := os.WriteFile(marker, []byte("preserve this unrelated work"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	refs := runGitTest(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/")
	registrations := runGitTest(t, f.repo, "worktree", "list", "--porcelain", "-z")
	return func() {
		t.Helper()
		if got := runGitTest(t, f.repo, "for-each-ref", "--format=%(refname) %(objectname)", "refs/heads/"); got != refs {
			t.Fatal("orphan branch was modified")
		}
		if got := runGitTest(t, f.repo, "worktree", "list", "--porcelain", "-z"); got != registrations {
			t.Fatal("orphan registration was modified")
		}
		if _, err := os.Lstat(filepath.Join(root, ".runner-state", request.WorkID+".json")); !os.IsNotExist(err) {
			t.Fatal("fixture no longer has absent private identity")
		}
		if marker != "" {
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != "preserve this unrelated work" {
				t.Fatal("orphan work was modified")
			}
		}
	}
}

func TestMembershipAdoptionPreviewRefusesOrphanArtifacts(t *testing.T) {
	for _, artifact := range []string{"branch", "registration", "path"} {
		t.Run(artifact, func(t *testing.T) {
			f := newProductionDelivery(t)
			request, added := membershipAdoption(t, f)
			preserved := orphanAdoptionArtifact(t, f, added, artifact)
			before := append([]github.WorkItem(nil), f.runner.project.remoteItems...)
			if _, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request); err == nil {
				t.Fatal("missing private identity hid an existing workspace artifact")
			}
			if !reflect.DeepEqual(before, f.runner.project.remoteItems) || f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.creates != 0 {
				t.Fatal("refused preview changed Project authority or executed work")
			}
			preserved()
		})
	}
}

func TestMembershipAdoptionRecoveryRefusesAppearedOrphanArtifacts(t *testing.T) {
	for _, artifact := range []string{"branch", "registration", "path"} {
		t.Run(artifact, func(t *testing.T) {
			f := newProductionDelivery(t)
			request, added := membershipAdoption(t, f)
			transport := &amendmentCrashRunner{deliveryMilestoneRunner: f.runner, boundary: "fence"}
			var err error
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err == nil || !transport.offline {
				t.Fatalf("fixture did not interrupt after admission fence: %v", err)
			}
			transport.offline, transport.boundary = false, ""
			preserved := orphanAdoptionArtifact(t, f, added, artifact)
			writes := f.runner.project.bodyEditWrites
			f.service, err = New(f.cfg, transport)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.service.preparePoll(t.Context(), 0, true, nil); err == nil {
				t.Fatal("fenced recovery ignored an appeared workspace artifact")
			}
			if f.parent(t).Phase != github.PlanAmendingPhase || f.runner.project.bodyEditWrites != writes || f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.creates != 0 {
				t.Fatal("refused recovery released the fence, adopted the item or executed work")
			}
			preserved()
		})
	}
}
