package release

import (
	"context"
	"errors"
	"fmt"
)

// The operator already authenticated the retained publication in its journal.
// Adoption adds no source, publication or approval authority: it only starts a
// complete qualification/release process for that same still-open PR/head/base.
func (b *Live) adopt(ctx context.Context, j *Job) error {
	if j.Request.SourceContinuation {
		if err := b.continuedPR(ctx, j, j.Receipt.Head); err != nil {
			var proof *proofFailure
			if errors.As(err, &proof) {
				return fail(j, err.Error())
			}
			return err
		}
		return done(j, fmt.Sprintf("Reviewed PR #%d authenticated for exact retained-source continuation; source checks, review, qualification and protected approvals remain fresh", j.Receipt.PR))
	}
	var pr struct {
		State  string
		Merged bool
		Head   struct{ SHA string }
		Base   struct {
			SHA string
			Ref string
		}
	}
	if err := b.gh(ctx, fmt.Sprintf("repos/%s/pulls/%d", b.Config.Repository, j.Receipt.PR), &pr); err != nil {
		return err
	}
	if pr.State != "open" || pr.Merged || pr.Head.SHA != j.Receipt.Head || pr.Base.SHA != j.Receipt.Base || pr.Base.Ref != b.Config.Branch {
		return fail(j, "reviewed publication changed or closed; preserve the original workflow and request a new source handoff")
	}
	return done(j, fmt.Sprintf("Retained reviewed PR #%d adopted without source edits or publication; full qualification and fresh merge/production approvals remain required", j.Receipt.PR))
}
