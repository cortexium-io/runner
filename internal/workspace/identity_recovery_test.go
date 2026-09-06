package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryIdentityRefusesUnsafePrivateRecord(t *testing.T) {
	for _, change := range []string{"symlink", "permissions", "missing worktree", "repository", "base ref"} {
		t.Run(change, func(t *testing.T) {
			repo := initGitRepo(t)
			root := filepath.Join(t.TempDir(), "worktrees")
			provider := NewGitProvider(nil)
			request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "retained", BranchPrefix: "runner", BaseRef: "HEAD"})
			metadata, err := provider.Prepare(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			path := activeIdentityPath(root, request.WorkID)
			switch change {
			case "symlink":
				retained := filepath.Join(t.TempDir(), "identity.json")
				if err := os.Rename(path, retained); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(retained, path); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "missing worktree":
				runGitTest(t, repo, "worktree", "remove", metadata.WorktreePath)
			case "repository":
				request.Repository = "other/repository"
			case "base ref":
				request.BaseRef = "refs/heads/other"
			}
			if _, err := provider.ValidateRetainedIdentity(t.Context(), request); err == nil {
				t.Fatal("unsafe recovery identity accepted")
			}
		})
	}
}
