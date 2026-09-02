package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestRepositoryReferenceCheckResolvesSymlinkAndVerifiesPin(t *testing.T) {
	repository := initGitRepo(t)
	commit := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	link := filepath.Join(t.TempDir(), "legacy-link")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	checks := CheckRepositoryReferences(t.Context(), subprocess.OSRunner{}, []config.RepositoryReference{{
		Name: "legacy", Path: link, Commit: commit,
	}}, nil)
	if len(checks) != 1 || checks[0].Err != nil {
		t.Fatalf("valid reference check = %#v", checks)
	}
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].ResolvedPath != resolved || checks[0].ResolvedCommit != commit {
		t.Fatalf("reference was not resolved and pinned: %#v", checks[0])
	}
}

func TestRepositoryReferenceCheckFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string, *config.RepositoryReference) []string
		want  string
	}{
		{name: "wrong commit", setup: func(_ *testing.T, _ string, reference *config.RepositoryReference) []string {
			reference.Commit = strings.Repeat("a", 40)
			return nil
		}, want: "want pinned commit"},
		{name: "tracked change", setup: func(t *testing.T, repository string, _ *config.RepositoryReference) []string {
			if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("changed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}, want: "tracked or untracked changes"},
		{name: "untracked change", setup: func(t *testing.T, repository string, _ *config.RepositoryReference) []string {
			if err := os.WriteFile(filepath.Join(repository, "extra.txt"), []byte("extra\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return nil
		}, want: "tracked or untracked changes"},
		{name: "not repository root", setup: func(t *testing.T, repository string, reference *config.RepositoryReference) []string {
			nested := filepath.Join(repository, "nested")
			if err := os.Mkdir(nested, 0o755); err != nil {
				t.Fatal(err)
			}
			reference.Path = nested
			return nil
		}, want: "must be the Git checkout root"},
		{name: "protected overlap", setup: func(_ *testing.T, repository string, _ *config.RepositoryReference) []string {
			return []string{filepath.Dir(repository)}
		}, want: "overlaps protected"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := initGitRepo(t)
			reference := config.RepositoryReference{
				Name: "legacy", Path: repository, Commit: strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD")),
			}
			protected := test.setup(t, repository, &reference)
			checks := CheckRepositoryReferences(t.Context(), subprocess.OSRunner{}, []config.RepositoryReference{reference}, protected)
			if len(checks) != 1 || checks[0].Err == nil || !strings.Contains(checks[0].Err.Error(), test.want) {
				t.Fatalf("invalid reference check = %#v, want %q", checks, test.want)
			}
		})
	}
}

func TestRepositoryReferenceCheckRejectsResolvedDuplicateRoots(t *testing.T) {
	repository := initGitRepo(t)
	commit := strings.TrimSpace(runGitTest(t, repository, "rev-parse", "HEAD"))
	link := filepath.Join(t.TempDir(), "same-repository")
	if err := os.Symlink(repository, link); err != nil {
		t.Fatal(err)
	}
	checks := CheckRepositoryReferences(t.Context(), subprocess.OSRunner{}, []config.RepositoryReference{
		{Name: "first", Path: repository, Commit: commit},
		{Name: "second", Path: link, Commit: commit},
	}, nil)
	if len(checks) != 2 || checks[0].Err != nil || checks[1].Err == nil || !strings.Contains(checks[1].Err.Error(), `overlaps repository reference "first"`) {
		t.Fatalf("resolved duplicate roots were not rejected: %#v", checks)
	}
}

func TestRepositoryReferenceCheckRejectsLinkedWorktreeMetadataOutsideRoot(t *testing.T) {
	repository := initGitRepo(t)
	worktree := filepath.Join(t.TempDir(), "linked-reference")
	runGitTest(t, repository, "worktree", "add", "-b", "reference-test", worktree)
	commit := strings.TrimSpace(runGitTest(t, worktree, "rev-parse", "HEAD"))
	checks := CheckRepositoryReferences(t.Context(), subprocess.OSRunner{}, []config.RepositoryReference{{
		Name: "linked", Path: worktree, Commit: commit,
	}}, nil)
	if len(checks) != 1 || checks[0].Err == nil || !strings.Contains(checks[0].Err.Error(), "linked worktrees") {
		t.Fatalf("linked worktree reference was accepted: %#v", checks)
	}
}
