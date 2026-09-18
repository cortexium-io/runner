package workspace

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestCheckoutSnapshotAllowsConcurrentTrackingUpdateOnly(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		allow  bool
	}{
		{name: "branch tracking", allow: true, mutate: func(t *testing.T, repo string) {
			runGitTest(t, repo, "config", "branch.other.remote", "origin")
		}},
		{name: "protected config", mutate: func(t *testing.T, repo string) {
			runGitTest(t, repo, "config", "core.hooksPath", "/changed-hooks")
		}},
		{name: "tracking and protected config", mutate: func(t *testing.T, repo string) {
			runGitTest(t, repo, "config", "branch.other.remote", "origin")
			runGitTest(t, repo, "config", "include.path", "/changed-config")
		}},
		{name: "symlink substitution", mutate: func(t *testing.T, repo string) {
			path := filepath.Join(repo, ".git", "config")
			if err := os.Rename(path, path+".saved"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".saved", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "changed permissions", mutate: func(t *testing.T, repo string) {
			if err := os.Chmod(filepath.Join(repo, ".git", "config"), 0o666); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hard link", mutate: func(t *testing.T, repo string) {
			path := filepath.Join(repo, ".git", "config")
			if err := os.Link(path, path+".alias"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "tracking update cannot hide changed HEAD", mutate: func(t *testing.T, repo string) {
			runGitTest(t, repo, "config", "branch.other.remote", "origin")
			runGitTest(t, repo, "commit", "--allow-empty", "-m", "Unrelated HEAD mutation")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initGitRepo(t)
			before, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, repo, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			runner := &snapshotMutationRunner{
				Runner: subprocess.OSRunner{}, match: "status --porcelain=v1",
				mutate: func() { test.mutate(t, repo) },
			}
			after, err := captureDefaultCheckoutSnapshotState(t.Context(), runner, repo, time.Minute)
			if test.allow {
				if err != nil || after.Fingerprint != before.Fingerprint {
					t.Fatalf("benign tracking update interrupted checkout: %v, before=%s after=%s", err, before.Fingerprint, after.Fingerprint)
				}
			} else if err == nil || after.Fingerprint != "" {
				t.Fatalf("unsafe config mutation was certified: snapshot=%+v error=%v", after, err)
			}
		})
	}
}
