package workspace

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
	legacy "github.com/cortexium-io/runner/internal/workspace"
)

// PushDraft publishes immutable objects with an absent-ref lease. Reconciliation
// recognizes a successful earlier push; this method never updates an existing ref.
func PushDraft(ctx context.Context, candidate core.Candidate, branch, repositoryURL string, authorize func() error) error {
	return pushDraft(ctx, candidate, branch, repositoryURL, authorize, nil)
}

func pushDraft(ctx context.Context, candidate core.Candidate, branch, repositoryURL string, authorize func() error, runner subprocess.Runner) error {
	if authorize == nil {
		return errors.New("draft publication requires current authority")
	}
	p, err := legacy.NewGitProvider(nil).PublicationProfile(ctx, candidate.Workspace)
	if err != nil {
		return err
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
	verified, err := legacy.NewGitProvider(nil).PublicationProfile(ctx, candidate.Workspace)
	if err != nil {
		return err
	}
	if verified != p {
		return errors.New("publication Git administration changed")
	}
	if err = authorize(); err != nil {
		return err
	}
	args := []string{"push", "--porcelain", "--no-verify", "--force-with-lease=" + ref + ":", repositoryURL, commit + ":" + ref}
	if _, err = subprocess.RunPrivilegedGitNetwork(ctx, runner, p, args, 2*time.Minute); err != nil {
		return fmt.Errorf("draft branch push failed; inspect authentication/network and reconcile the remote identity: %w", err)
	}
	return nil
}
