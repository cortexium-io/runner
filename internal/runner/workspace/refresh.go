package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
	gitworkspace "github.com/cortexium-io/runner/internal/workspace"
)

// FetchBase imports only the explicitly configured branch into a unique private
// ref. It does not change the operator's checkout, branch or remote configuration.
func FetchBase(ctx context.Context, path, repository, branch string) (string, error) {
	return fetchBase(ctx, path, repository, branch, nil)
}

func fetchBase(ctx context.Context, path, repository, branch string, runner subprocess.Runner) (string, error) {
	if !config.ValidRepository(repository) {
		return "", errors.New("invalid base repository")
	}
	p, err := gitworkspace.NewGitProvider(nil).PublicationProfile(ctx, path)
	if err != nil {
		return "", err
	}
	remoteRef := "refs/heads/" + branch
	if _, err = scalar(ctx, p, "check-ref-format", remoteRef); err != nil {
		return "", err
	}
	ref := "refs/runner/fetch/" + core.NewID()
	args := []string{"fetch", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--no-auto-maintenance", "https://github.com/" + repository + ".git", "+" + remoteRef + ":" + ref}
	if _, err = subprocess.RunPrivilegedGitNetwork(ctx, runner, p, args, 2*time.Minute); err != nil {
		return "", fmt.Errorf("delivery base fetch failed: %w", err)
	}
	return scalar(ctx, p, "rev-parse", "--verify", ref+"^{commit}")
}

// Refresh constructs a clean merge in a separate retained workspace. The old
// candidate remains untouched; a conflict never becomes an executable candidate.
func (g Git) Refresh(ctx context.Context, t core.Task, target core.BaseBinding) (core.Candidate, error) {
	var candidate core.Candidate
	if err := g.ValidateCandidate(ctx, t); err != nil {
		return candidate, err
	}
	if target.Cycle < 1 || target.Commit == t.Base() {
		return candidate, errors.New("invalid refresh target")
	}
	if err := g.CheckQuality(ctx, target.Commit); err != nil {
		return candidate, fmt.Errorf("base quality changed; adopt an explicit quality agreement: %w", err)
	}
	provider := gitworkspace.NewGitProvider(nil)
	p, err := provider.RefreshProfile(ctx, t.Candidate.Workspace)
	if err != nil {
		return candidate, err
	}
	// merge-tree's exit status, not its output tree, decides whether a merge is clean.
	merged, err := subprocess.RunPrivilegedGit(ctx, nil, p, []string{"merge-tree", "--write-tree", "--name-only", "-z", "--messages", target.Commit, t.Candidate.Commit}, time.Minute)
	if err != nil {
		if merged.ExitCode == 1 && err.Error() == "exit status 1" {
			conflictErr := parseConflict(merged.Stdout)
			if conflict, ok := conflictErr.(*MergeConflict); ok {
				if err := validateTextConflicts(ctx, p, conflict); err != nil {
					return candidate, err
				}
			}
			return candidate, conflictErr
		}
		return candidate, fmt.Errorf("construct clean refresh: %w", err)
	}
	tree, _, _ := strings.Cut(merged.Stdout, "\x00")
	verifiedTree, err := scalar(ctx, p, "rev-parse", "--verify", tree+"^{tree}")
	if err != nil || verifiedTree != tree {
		return candidate, errors.New("invalid refreshed tree")
	}
	next := t
	next.WorkingBase = &target
	m, err := g.Prepare(ctx, next)
	if err != nil {
		return candidate, err
	}
	p, err = provider.RefreshProfile(ctx, m.WorktreePath)
	if err != nil {
		return candidate, err
	}
	snapshot, err := Snapshot(ctx, m.WorktreePath)
	if err != nil || !snapshot.Clean {
		return candidate, errors.New("refresh workspace is not clean; retained for inspection")
	}
	commit := snapshot.Head
	parents := target.Commit
	commitArgs := []string{"commit-tree", tree, "-p", target.Commit}
	if target.Commit != t.Candidate.Commit {
		parents += " " + t.Candidate.Commit
		commitArgs = append(commitArgs, "-p", t.Candidate.Commit)
	}
	commitArgs = append(commitArgs, "--no-gpg-sign", "-m", "Refresh Runner candidate against current base")
	if commit != target.Commit {
		actual, err := scalar(ctx, p, "show", "-s", "--format=%P", commit)
		if err != nil || actual != parents || snapshot.Tree != tree {
			return candidate, errors.New("refresh workspace contains an unexpected commit")
		}
	} else {
		commit, err = scalar(ctx, p, commitArgs...)
		if err != nil {
			return candidate, err
		}
		if _, err = scalar(ctx, p, "reset", "--hard", commit); err != nil {
			return candidate, err
		}
	}
	digest, err := Digest(m.WorktreePath)
	if err != nil {
		return candidate, err
	}
	candidate = core.Candidate{Commit: commit, Tree: tree, Fingerprint: digest, Workspace: m.WorktreePath}
	next.Candidate = &candidate
	return candidate, g.ValidateCandidate(ctx, next)
}
