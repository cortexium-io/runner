package workspace

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
	legacy "github.com/cortexium-io/runner/internal/workspace"
)

// ConfirmIntegration checks actual objects imported from the configured base.
// A provider's merged flag alone cannot establish the accepted integration.
func (g Git) ConfirmIntegration(ctx context.Context, proof core.BaseProof, merge, tip string) error {
	p, err := legacy.NewGitProvider(nil).PublicationProfile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	for _, oid := range []string{merge, tip} {
		if len(oid) != 40 && len(oid) != 64 {
			return errors.New("merge or fetched base identity invalid")
		}
		if _, err := hex.DecodeString(oid); err != nil {
			return errors.New("merge or fetched base identity invalid")
		}
	}
	oid, err := scalar(ctx, p, "rev-parse", "--verify", merge+"^{commit}")
	if err != nil || oid != merge {
		return errors.New("merged commit is unavailable in the fetched base")
	}
	parents, err := scalar(ctx, p, "show", "-s", "--format=%P", merge)
	if err != nil {
		return err
	}
	if parents != proof.Base+" "+proof.Candidate.Commit {
		return errors.New("merge parents differ from the verified base and accepted candidate; squash/rebase or a changed base cannot be confirmed")
	}
	tree, err := scalar(ctx, p, "rev-parse", merge+"^{tree}")
	if err != nil || tree != proof.Candidate.Tree {
		return errors.New("merged tree differs from the reviewed candidate")
	}
	if _, err = scalar(ctx, p, "merge-base", "--is-ancestor", merge, tip); err != nil {
		return errors.New("merge is not in the observed configured base ancestry")
	}
	return nil
}
