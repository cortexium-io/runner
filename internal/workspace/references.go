package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const repositoryReferenceCheckTimeout = 10 * time.Second

// RepositoryReferenceCheck records the resolved evidence root used by doctor
// and harness launch. Err is non-nil when the configured pin is not currently
// safe to expose.
type RepositoryReferenceCheck struct {
	Reference      config.RepositoryReference
	ResolvedPath   string
	ResolvedCommit string
	Err            error
}

func CheckRepositoryReferences(ctx context.Context, run subprocess.Runner, references []config.RepositoryReference, protectedRoots []string) []RepositoryReferenceCheck {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	checks := make([]RepositoryReferenceCheck, len(references))
	for index, reference := range references {
		checks[index] = checkRepositoryReference(ctx, run, reference)
	}

	protected := make([]string, 0, len(protectedRoots))
	for _, root := range protectedRoots {
		if resolved := normalizedPath(root); resolved != "" {
			protected = append(protected, resolved)
		}
	}
	for index := range checks {
		if checks[index].Err != nil {
			continue
		}
		for _, root := range protected {
			if pathInsideOrEqualLexical(checks[index].ResolvedPath, root) || pathInsideOrEqualLexical(root, checks[index].ResolvedPath) {
				checks[index].Err = fmt.Errorf("resolved path overlaps protected project or workspace root %q", root)
				break
			}
		}
		if checks[index].Err != nil {
			continue
		}
		for prior := 0; prior < index; prior++ {
			if checks[prior].Err != nil {
				continue
			}
			if pathInsideOrEqualLexical(checks[index].ResolvedPath, checks[prior].ResolvedPath) || pathInsideOrEqualLexical(checks[prior].ResolvedPath, checks[index].ResolvedPath) {
				checks[index].Err = fmt.Errorf("resolved path overlaps repository reference %q at %s", checks[prior].Reference.Name, checks[prior].ResolvedPath)
				break
			}
		}
	}
	return checks
}

func ValidateRepositoryReferences(ctx context.Context, run subprocess.Runner, references []config.RepositoryReference, protectedRoots []string) ([]config.RepositoryReference, error) {
	checks := CheckRepositoryReferences(ctx, run, references, protectedRoots)
	resolved := make([]config.RepositoryReference, 0, len(checks))
	for _, check := range checks {
		if check.Err != nil {
			return nil, fmt.Errorf("repository reference %q is not ready: %w", strings.TrimSpace(check.Reference.Name), check.Err)
		}
		resolved = append(resolved, config.RepositoryReference{
			Name: strings.TrimSpace(check.Reference.Name), Path: check.ResolvedPath, Commit: check.ResolvedCommit,
		})
	}
	return resolved, nil
}

func checkRepositoryReference(ctx context.Context, run subprocess.Runner, reference config.RepositoryReference) RepositoryReferenceCheck {
	check := RepositoryReferenceCheck{Reference: reference}
	path := strings.TrimSpace(reference.Path)
	if path == "" || !filepath.IsAbs(path) {
		check.Err = errors.New("path must be absolute")
		return check
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		check.Err = fmt.Errorf("resolve absolute path: %w", err)
		return check
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		check.Err = fmt.Errorf("resolve path symlinks: %w", err)
		return check
	}
	resolved = filepath.Clean(resolved)
	info, err := os.Stat(resolved)
	if err != nil {
		check.Err = fmt.Errorf("inspect resolved path: %w", err)
		return check
	}
	if !info.IsDir() {
		check.Err = errors.New("resolved path is not a directory")
		return check
	}
	check.ResolvedPath = resolved

	rootResult, err := subprocess.RunGit(ctx, run, []string{"rev-parse", "--show-toplevel"}, resolved, repositoryReferenceCheckTimeout)
	if err != nil {
		check.Err = fmt.Errorf("path is not a Git checkout: %w", err)
		return check
	}
	root := strings.TrimSpace(rootResult.Stdout)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		check.Err = fmt.Errorf("resolve Git checkout root: %w", err)
		return check
	}
	if filepath.Clean(root) != resolved {
		check.Err = fmt.Errorf("path must be the Git checkout root; repository root is %s", filepath.Clean(root))
		return check
	}
	gitMetadata, err := filepath.EvalSymlinks(filepath.Join(resolved, ".git"))
	if err != nil {
		check.Err = fmt.Errorf("resolve checkout Git metadata: %w", err)
		return check
	}
	gitMetadataInfo, err := os.Stat(gitMetadata)
	if err != nil || !gitMetadataInfo.IsDir() || !pathInsideOrEqualLexical(gitMetadata, resolved) {
		check.Err = errors.New("checkout Git metadata must be a directory inside the reference root; linked worktrees and external Git directories are unsupported")
		return check
	}

	headResult, err := subprocess.RunGit(ctx, run, []string{"rev-parse", "--verify", "HEAD^{commit}"}, resolved, repositoryReferenceCheckTimeout)
	if err != nil {
		check.Err = fmt.Errorf("resolve checkout HEAD: %w", err)
		return check
	}
	head := strings.ToLower(strings.TrimSpace(headResult.Stdout))
	want := strings.ToLower(strings.TrimSpace(reference.Commit))
	if head == "" || head != want {
		check.Err = fmt.Errorf("checkout HEAD is %s, want pinned commit %s; update the checkout manually or change the configured pin", displayReferenceValue(head), displayReferenceValue(want))
		return check
	}
	check.ResolvedCommit = head

	statusResult, err := subprocess.RunGit(ctx, run, []string{
		"-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false",
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	}, resolved, repositoryReferenceCheckTimeout)
	if err != nil {
		check.Err = fmt.Errorf("inspect checkout cleanliness: %w", err)
		return check
	}
	if statusResult.Stdout != "" {
		check.Err = errors.New("checkout has tracked or untracked changes; restore it or configure a dedicated clean checkout")
		return check
	}
	return check
}

func displayReferenceValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<unresolved>"
	}
	return value
}
