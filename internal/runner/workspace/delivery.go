package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
	gitworkspace "github.com/cortexium-io/runner/internal/workspace"
)

// PushDraft publishes exact objects with an explicit remote lease. An empty
// expected commit requires an absent ref; a nonempty commit permits only that update.
func PushDraft(ctx context.Context, candidate core.Candidate, branch, repositoryURL, expected string, authorize func() error) error {
	return pushDraft(ctx, candidate, branch, repositoryURL, expected, authorize, nil)
}

func pushDraft(ctx context.Context, candidate core.Candidate, branch, repositoryURL, expected string, authorize func() error, runner subprocess.Runner) error {
	if authorize == nil {
		return errors.New("draft publication requires current authority")
	}
	p, err := gitworkspace.NewGitProvider(nil).PublicationProfile(ctx, candidate.Workspace)
	if err != nil {
		return err
	}
	if expected != "" {
		commit, err := scalar(ctx, p, "rev-parse", "--verify", expected+"^{commit}")
		if err != nil || commit != expected {
			return errors.New("expected remote commit must name an exact retained object")
		}
	}
	ref := "refs/heads/" + branch
	if _, err = scalar(ctx, p, "check-ref-format", ref); err != nil {
		return errors.New("invalid publication branch")
	}
	commit, err := scalar(ctx, p, "rev-parse", "--verify", candidate.Commit+"^{commit}")
	if err != nil || commit != candidate.Commit {
		return errors.New("publication commit changed")
	}
	tree, err := scalar(ctx, p, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil || tree != candidate.Tree {
		return errors.New("publication tree changed")
	}
	verified, err := gitworkspace.NewGitProvider(nil).PublicationProfile(ctx, candidate.Workspace)
	if err != nil {
		return err
	}
	if verified != p {
		return errors.New("publication Git administration changed")
	}
	if err = authorize(); err != nil {
		return err
	}
	args := []string{"push", "--porcelain", "--no-verify", "--force-with-lease=" + ref + ":" + expected, repositoryURL, commit + ":" + ref}
	if _, err = subprocess.RunPrivilegedGitNetwork(ctx, runner, p, args, 2*time.Minute); err != nil {
		return fmt.Errorf("draft branch push failed; inspect authentication/network and reconcile the remote identity: %w", err)
	}
	return nil
}
