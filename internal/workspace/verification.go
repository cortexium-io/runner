package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// PrepareVerificationIndex gives a new disposable source copy a standalone
// index for Git-based file inventory. It creates no commits, refs, remotes, or
// shared object links. Only the supplied copied paths are staged, including
// files excluded by .gitignore. Literal staging bypasses filters and attributes.
func PrepareVerificationIndex(ctx context.Context, root string, paths []string) (string, error) {
	profile, err := verificationGitProfile(root)
	if err != nil {
		return "", err
	}
	if err := os.Mkdir(profile.GitDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create private verification Git directory: %w", err)
	}
	p := NewGitProvider(nil)
	if result, err := p.privilegedGit(ctx, profile, "init", "--quiet", "--template=", "--initial-branch=runner-verification"); err != nil {
		return "", fmt.Errorf("initialize verification index: %w", commandError(err, result))
	}
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	for _, path := range paths {
		if err := p.stageCandidatePath(ctx, profile, path); err != nil {
			return "", err
		}
	}
	return ReadVerificationIndex(ctx, root)
}

// ReadVerificationIndex compares logical entries rather than index cache
// bytes: ordinary Git status refreshes must not invalidate otherwise identical
// verification. The caller separately pins the copied source and Git controls.
func ReadVerificationIndex(ctx context.Context, root string) (string, error) {
	profile, err := verificationGitProfile(root)
	if err != nil {
		return "", err
	}
	// Refuse redirected or oversized indexes before invoking Git after a harness.
	if _, _, _, err := securefs.ReadFile(profile.IndexFile, subprocess.GitStdoutLimit); err != nil {
		return "", fmt.Errorf("inspect verification index: %w", err)
	}
	p := NewGitProvider(nil)
	result, err := p.privilegedGit(ctx, profile, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return "", fmt.Errorf("read verification index: %w", commandError(err, result))
	}
	return result.Stdout, nil
}

func verificationGitProfile(root string) (subprocess.PrivilegedGitProfile, error) {
	root, err := securefs.AbsolutePath(root)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, err
	}
	gitDir := filepath.Join(root, ".git")
	return subprocess.NewPrivilegedGitProfile(root, gitDir, gitDir, filepath.Join(gitDir, "index"), filepath.Join(gitDir, "objects"))
}
