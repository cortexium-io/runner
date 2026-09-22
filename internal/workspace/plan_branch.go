package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// EnsurePlanBranch creates a deterministic branch once at the coordinator's
// authenticated initial head. A lost push response is recovered by observing
// that exact head. A different remote head is never silently adopted.
func (p GitProvider) EnsurePlanBranch(ctx context.Context, directory, repository, remote, branch, expected string, refreshAuthority func() error) error {
	return p.preparePlanBranch(ctx, directory, repository, remote, branch, expected, "", refreshAuthority, true)
}

// VerifyPlanBranch never recreates a missing branch. Review and publication
// must still observe the coordinator's authenticated integration head.
func (p GitProvider) VerifyPlanBranch(ctx context.Context, directory, repository, remote, branch, expected string, refreshAuthority func() error) error {
	return p.preparePlanBranch(ctx, directory, repository, remote, branch, expected, "", refreshAuthority, false)
}

// VerifyPlanBranchAdvance proves an exact authenticated forward integration,
// not an unrelated replacement, before renewing a member's obsolete review.
func (p GitProvider) VerifyPlanBranchAdvance(ctx context.Context, directory, repository, remote, branch, previous, expected string, refreshAuthority func() error) error {
	if !validObjectID(previous) || previous == expected {
		return errors.New("plan review renewal requires a distinct prior head")
	}
	return p.preparePlanBranch(ctx, directory, repository, remote, branch, expected, previous, refreshAuthority, false)
}

func (p GitProvider) preparePlanBranch(ctx context.Context, directory, repository, remote, branch, expected, previous string, refreshAuthority func() error, create bool) error {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if !config.ValidRepositoryName(repository) || !validObjectID(expected) || !strings.HasPrefix(branch, "runner/plan-") || remote == "" || refreshAuthority == nil {
		return errors.New("plan branch requires exact repository, deterministic branch, head and authority")
	}
	profile, err := derivePrivilegedGitProfile(directory)
	if err != nil {
		return err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return err
	}
	if _, err := p.privilegedGit(ctx, profile, "check-ref-format", "refs/heads/"+branch); err != nil {
		return err
	}
	url := "https://github.com/" + repository + ".git"
	ref := "refs/heads/" + branch
	result, err := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{"ls-remote", "--heads", url, ref}, time.Minute)
	if err != nil {
		return fmt.Errorf("inspect plan branch: %w", commandError(err, result))
	}
	value := strings.Fields(result.Stdout)
	if len(value) != 0 && (len(value) != 2 || value[0] != expected || value[1] != ref) {
		return errors.New("plan branch has an unexpected remote identity; refusing to overwrite or adopt it")
	}
	if err := refreshAuthority(); err != nil {
		return err
	}
	if len(value) == 0 {
		if !create {
			return errors.New("authenticated plan branch is missing; refusing to recreate it during review")
		}
		resolved, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", expected+"^{commit}")
		if err != nil || resolved != expected {
			return errors.New("initial plan commit is unavailable")
		}
		result, err := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{"push", "--porcelain", "--no-verify", "--force-with-lease=" + ref + ":", url, expected + ":" + ref}, 2*time.Minute)
		if err != nil {
			return fmt.Errorf("create plan branch: %w", commandError(err, result))
		}
	}
	tracking := "refs/remotes/" + remote + "/" + branch
	result, err = subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", url, "+" + ref + ":" + tracking}, 2*time.Minute)
	if err != nil {
		return fmt.Errorf("fetch exact plan head: %w", commandError(err, result))
	}
	head, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", tracking)
	if err != nil || head != expected {
		return errors.New("plan branch changed while being prepared")
	}
	if previous != "" {
		result, err := p.privilegedGit(ctx, profile, "merge-base", "--is-ancestor", previous, expected)
		if err != nil || result.ExitCode != 0 {
			return errors.New("authenticated plan head is not a forward integration of the previously reviewed base")
		}
	}
	return refreshAuthority()
}

// MergePlanHead brings only the authenticated integrated head into a retained
// plan candidate. A prior destination refresh may mean neither is a fast-forward.
// Both histories are retained and the combined result still requires fresh QA.
func (p GitProvider) MergePlanHead(ctx context.Context, metadata Metadata, expected string, refreshAuthority func() error) error {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if !validObjectID(expected) || refreshAuthority == nil {
		return errors.New("plan merge requires an exact authenticated head")
	}
	if err := validateCandidateMetadata(metadata); err != nil {
		return err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return err
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return err
	}
	if err := p.rejectExecutableMergeConfig(ctx, profile); err != nil {
		return err
	}
	status, err := p.privilegedGit(ctx, profile, "status", "--porcelain", "--untracked-files=all")
	if err != nil || strings.TrimSpace(status.Stdout) != "" {
		return errors.Join(errors.New("retained plan candidate must be clean before integrating accepted repairs"), err)
	}
	if err := refreshAuthority(); err != nil {
		return err
	}
	result, err := p.privilegedGit(ctx, profile, "merge", "--no-edit", "--no-verify", expected)
	if err != nil {
		return fmt.Errorf("combine retained destination refresh with accepted plan repairs; retained conflicts require explicit owning-card recovery: %w", commandError(err, result))
	}
	return refreshAuthority()
}
