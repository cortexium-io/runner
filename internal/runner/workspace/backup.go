package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// BackupRoots includes Git administration outside a linked checkout. Object
// alternates require another repository and are outside this single-host image.
func BackupRoots(ctx context.Context, c config.Config) ([]string, error) {
	p, err := profile(ctx, c.Repository.Path)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(filepath.Join(p.ObjectDirectory, "info", "alternates")); !os.IsNotExist(err) {
		return nil, errors.New("backup does not support Git object alternates")
	}
	if _, err = scalar(ctx, p, "fsck", "--full", "--no-dangling"); err != nil {
		return nil, err
	}
	return []string{c.Repository.Path, p.GitDirectory, p.CommonDirectory, c.WorktreeDir}, nil
}

func (g Git) ValidateRetained(ctx context.Context, tasks []core.Task, qualities []config.Quality) error {
	if err := g.validate(ctx); err != nil {
		return err
	}
	p, err := profile(ctx, g.Settings.Config.Repository.Path)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		if t.Approval != nil {
			if _, err = scalar(ctx, p, "cat-file", "-e", t.Approval.Base+"^{commit}"); err != nil {
				return err
			}
		}
		if t.Candidate != nil {
			if _, err = scalar(ctx, p, "cat-file", "-e", t.Candidate.Commit+"^{commit}"); err != nil {
				return err
			}
			if _, err = scalar(ctx, p, "cat-file", "-e", t.Candidate.Tree+"^{tree}"); err != nil {
				return err
			}
			if _, err = os.Lstat(t.Candidate.Workspace); err != nil {
				return err
			}
			// A saved implementation/repair can legitimately have unfinished
			// bytes. Accepted candidates must still match their complete binding.
			if (t.Stage == core.AwaitingDelivery || t.Stage == core.Completed) && core.ReviewOutcome(t.Review) == core.Accepted {
				pinned := g
				found := false
				for _, q := range qualities {
					if q.Revision() == t.Current().Quality {
						pinned.Settings.Quality, found = q, true
						break
					}
				}
				if !found {
					return errors.New("candidate quality revision is missing")
				}
				if err = pinned.ValidateCandidate(ctx, t); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
