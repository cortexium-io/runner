//go:build !windows

package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestDirtyAmendmentRebindPreservesGitAndRefusesChangedIndex(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(nil)
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "dirty_amendment", BranchPrefix: "runner", BaseRef: "HEAD"})
	metadata, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(metadata.WorktreePath, "retained.txt")
	if err := os.WriteFile(path, []byte("staged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "add", "retained.txt")
	if err := os.WriteFile(path, []byte("unstaged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := CaptureSnapshotStateWithLimits(t.Context(), nil, metadata.WorktreePath, 30*time.Second, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if before.Clean {
		t.Fatal("fixture is not dirty")
	}
	if err := provider.RebindRetainedContent(t.Context(), request, metadata.Identity, before, "v1:amended"); err != nil {
		t.Fatal(err)
	}
	request.DelegatedContentDigest = "v1:amended"
	identity, err := provider.ValidateRetainedIdentity(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	after, err := CaptureSnapshotStateWithLimits(t.Context(), nil, metadata.WorktreePath, 30*time.Second, DefaultSnapshotLimits())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("rebinding changed Git state: %v", err)
	}
	runGitTest(t, metadata.WorktreePath, "add", "retained.txt")
	changed, err := CaptureSnapshotStateWithLimits(t.Context(), nil, metadata.WorktreePath, 30*time.Second, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if changed.IndexFingerprint() == before.IndexFingerprint() || changed.Head != before.Head {
		t.Fatal("index identity did not distinguish staging from HEAD")
	}
	if err := provider.RebindRetainedContent(t.Context(), request, identity, before, "v1:other"); err == nil {
		t.Fatal("changed index accepted")
	}
	retained, err := provider.ValidateRetainedIdentity(t.Context(), request)
	if err != nil || retained != identity {
		t.Fatalf("failed rebind changed identity: %v", err)
	}
}

func TestDirtyAmendmentRefusesCurrentAndHistoricalPublicationAcceptance(t *testing.T) {
	for _, state := range []string{"absent", "other item", "current", "historical", "malformed"} {
		t.Run(state, func(t *testing.T) {
			repo := initGitRepo(t)
			provider := NewGitProvider(nil)
			metadata, err := provider.Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "dirty_history", BranchPrefix: "runner", BaseRef: "HEAD"}))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := CaptureCheckoutSnapshotStateWithLimits(t.Context(), nil, metadata.WorktreePath, 30*time.Second, DefaultSnapshotLimits())
			if err != nil {
				t.Fatal(err)
			}
			if state == "other item" {
				other, err := provider.Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Dir(metadata.WorktreePath), WorkID: "other_item", BranchPrefix: "runner", BaseRef: "HEAD"}))
				if err != nil {
					t.Fatal(err)
				}
				otherSnapshot, err := CaptureCheckoutSnapshotStateWithLimits(t.Context(), nil, other.WorktreePath, 30*time.Second, DefaultSnapshotLimits())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := provider.RecordPublicationAcceptance(t.Context(), other, otherSnapshot, "Other accepted report", "Other accepted candidate"); err != nil {
					t.Fatal(err)
				}
			} else if state != "absent" {
				if _, err := provider.RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Accepted report", "Accepted candidate"); err != nil {
					t.Fatal(err)
				}
			}
			if state == "historical" {
				runGitTest(t, metadata.WorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "later work")
			}
			if state == "malformed" {
				if err := os.WriteFile(publicationRecordPath(filepath.Dir(metadata.WorktreePath), snapshot.Head), []byte("invalid retained record"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("dirty retained work"), 0o644); err != nil {
				t.Fatal(err)
			}
			err = provider.RequireNoPublicationAcceptance(t.Context(), metadata)
			if (err == nil) != (state == "absent" || state == "other item") {
				t.Fatalf("unexpected history decision: %v", err)
			}
			if state == "current" || state == "historical" {
				if !strings.Contains(err.Error(), "publication acceptance") {
					t.Fatalf("acceptance was not identified: %v", err)
				}
			}
		})
	}
}
