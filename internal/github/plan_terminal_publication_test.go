package github

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestMergedPlanRecoveryUsesProtectedAcceptanceNotCurrentCheckout(t *testing.T) {
	metadata, record, action := acceptedTerminalPlan(t)
	// The remote branch never exists in this fixture. Changed local source and
	// an unavailable gate must not erase an already-confirmed exact merge.
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("later local work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runner := &pullRequestTestRunner{existingOpen: true, viewHead: action.Item.Branch,
		viewOutput: terminalPlanPR(t, record, "MERGED", record.CommitOID, "owner/repo", "main")}
	manager := NewPullRequestManager(runner, staticActionRefresher{})
	details, found, err := manager.RecoverMergedPlanPublication(t.Context(), action, metadata, record, "main")
	if err != nil || !found || details.HeadRefOID != record.CommitOID || details.State != "MERGED" {
		t.Fatalf("exact terminal proof: found=%v details=%#v err=%v", found, details, err)
	}
	guardCalls := 0
	published, found, err := manager.RecoverPlanPublication(t.Context(), action, metadata, record, "main", "origin", func(context.Context, AuthorizedAction) error {
		guardCalls++
		return errors.New("current validation environment unavailable")
	})
	if err != nil || !found || published.CommitSHA != record.CommitOID || guardCalls != 0 {
		t.Fatalf("lost-response terminal recovery reran validation: found=%v guard=%d err=%v", found, guardCalls, err)
	}
	if len(runner.gitCalls) != 0 {
		t.Fatalf("terminal proof fetched, recreated, or inspected a live candidate: %v", runner.gitCalls)
	}
	for _, call := range runner.calls {
		if !strings.HasPrefix(call, "pr list ") && !strings.HasPrefix(call, "pr view ") {
			t.Fatalf("terminal readback mutated GitHub: %s", call)
		}
	}
}

func TestMergedPlanRecoveryFailsClosedAndOpenRemainsGuarded(t *testing.T) {
	metadata, record, action := acceptedTerminalPlan(t)
	for _, tc := range []struct {
		name, state, head, headRepo, base string
		wantErr                           bool
	}{
		{"open", "OPEN", record.CommitOID, "owner/repo", "main", false},
		{"closed is not delivered", "CLOSED", record.CommitOID, "owner/repo", "main", true},
		{"unreviewed last head", "MERGED", strings.Repeat("b", 40), "owner/repo", "main", true},
		{"foreign repository", "MERGED", record.CommitOID, "other/repo", "main", true},
		{"wrong destination", "MERGED", record.CommitOID, "owner/repo", "develop", true},
		{"unknown state", "UNKNOWN", record.CommitOID, "owner/repo", "main", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &pullRequestTestRunner{existingOpen: true, viewHead: action.Item.Branch,
				viewOutput: terminalPlanPR(t, record, tc.state, tc.head, tc.headRepo, tc.base)}
			manager := NewPullRequestManager(runner, staticActionRefresher{})
			_, found, err := manager.RecoverMergedPlanPublication(t.Context(), action, metadata, record, "main")
			if found || (err != nil) != tc.wantErr {
				t.Fatalf("terminal refusal: found=%v err=%v", found, err)
			}
			if tc.state == "OPEN" {
				guardCalls := 0
				_, _, err := manager.RecoverPlanPublication(t.Context(), action, metadata, record, "main", "origin", func(context.Context, AuthorizedAction) error {
					guardCalls++
					return errors.New("required fresh guard refused")
				})
				if err == nil || !strings.Contains(err.Error(), "required fresh guard refused") || guardCalls != 1 {
					t.Fatalf("open PR bypassed normal current-candidate guard: calls=%d err=%v", guardCalls, err)
				}
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*workspace.PublicationRecord)
	}{
		{"no final proof", func(r *workspace.PublicationRecord) { r.VerificationReceipt = "" }},
		{"tampered final proof", func(r *workspace.PublicationRecord) { r.VerificationReceipt = "untrusted" }},
		{"wrong revision", func(r *workspace.PublicationRecord) { r.PlanRevision = "v1:" + strings.Repeat("f", 64) }},
		{"wrong item", func(r *workspace.PublicationRecord) { r.ItemID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := record
			tc.mutate(&changed)
			runner := &pullRequestTestRunner{}
			_, found, err := NewPullRequestManager(runner, staticActionRefresher{}).RecoverMergedPlanPublication(t.Context(), action, metadata, changed, "main")
			if err == nil || found || len(runner.calls) != 0 {
				t.Fatalf("unprotected tuple reached remote recovery: found=%v calls=%v err=%v", found, runner.calls, err)
			}
		})
	}
	runner := &pullRequestTestRunner{}
	_, _, err := NewPullRequestManager(runner, staticActionRefresher{err: errors.New("parent release revoked")}).RecoverMergedPlanPublication(t.Context(), action, metadata, record, "main")
	if err == nil || len(runner.calls) != 0 {
		t.Fatal("revoked parent reached publication recovery")
	}
}

func TestMergedPlanRecoveryDoesNotRequireLocalBranchOrCheckout(t *testing.T) {
	for _, missing := range []string{"local branch", "pruned checkout"} {
		t.Run(missing, func(t *testing.T) {
			metadata, record, action := acceptedTerminalPlan(t)
			runGitTest(t, metadata.RepoRoot, "update-ref", "-d", record.DestinationRef)
			if missing == "pruned checkout" {
				runGitTest(t, metadata.RepoRoot, "worktree", "remove", "--force", metadata.WorktreePath)
			}
			// A restarted coordinator deserializes protected metadata. Private
			// implementation fields deliberately do not survive that round-trip.
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			var restored workspace.Metadata
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			runner := &pullRequestTestRunner{existingOpen: true, viewHead: action.Item.Branch,
				viewOutput: terminalPlanPR(t, record, "MERGED", record.CommitOID, "owner/repo", "main")}
			manager := NewPullRequestManager(runner, staticActionRefresher{})
			_, found, err := manager.RecoverMergedPlanPublication(t.Context(), action, restored, record, "main")
			if err != nil || !found || len(runner.gitCalls) != 0 {
				t.Fatalf("confirmed merge depended on %s: found=%v git=%v err=%v", missing, found, runner.gitCalls, err)
			}
			runner.viewOutput = terminalPlanPR(t, record, "OPEN", record.CommitOID, "owner/repo", "main")
			_, found, err = manager.RecoverMergedPlanPublication(t.Context(), action, restored, record, "main")
			if err != nil || found {
				t.Fatalf("terminal-only proof authorized open publication: found=%v err=%v", found, err)
			}
			_, _, err = manager.RecoverPlanPublication(t.Context(), action, restored, record, "main", "origin", func(context.Context, AuthorizedAction) error { return nil })
			if err == nil {
				t.Fatal("open publication accepted a missing live candidate")
			}
		})
	}
}

func terminalPlanPR(t *testing.T, record workspace.PublicationRecord, state, head, headRepo, base string) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"url": "https://github.com/owner/repo/pull/12", "number": 12, "state": state,
		"headRepository": map[string]string{"nameWithOwner": headRepo},
		"headRefName":    strings.TrimPrefix(record.DestinationRef, "refs/heads/"), "headRefOid": head,
		"baseRefName": base, "baseRefOid": record.ApprovedBaseOID, "mergeCommit": map[string]string{"oid": head},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func acceptedTerminalPlan(t *testing.T) (workspace.Metadata, workspace.PublicationRecord, AuthorizedAction) {
	t.Helper()
	project, parent, children := deliveryFixture(t)
	project.cfg.BaseBranch = "main"
	manifest, _, err := ParsePlanManifest(parent.Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.DestinationBranch = "main"
	parent.Body, err = FormatPlanManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parent.PlanRelease, err = project.signPlanningBatch(parent, children, batchReleasedState, "generation")
	if err != nil {
		t.Fatal(err)
	}
	parent.Status, parent.Phase = "Agent QA", ""
	parent = signDeliveryFixture(t, project, parent, "reviewer", "agent_qa")
	action, err := project.validateAction(parent)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := createPublicationRepository(t)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "terminal_plan",
		ItemID: parent.ID, DelegatedContentDigest: DelegatedContentFor(parent).Digest, Repository: parent.Repository,
		BranchName: parent.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	record, err := provider.RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Independent QA accepted.", "Final plan candidate accepted.", workspace.PublicationEvidence{
		PlanRevision: PlanRevision(parent.Body), VerificationReceipt: "protected completed verification", VerificationDigest: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	return metadata, record, action
}
