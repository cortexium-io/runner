package workspace

import (
	"context"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// PrepareCIRepair seeds a separate workspace once. Retries keep partial work;
// the candidate already published to the draft is never opened for writing.
func (g Git) PrepareCIRepair(ctx context.Context, t core.Task) error {
	r := t.CIRepair
	if r == nil || t.WorkspaceRepair <= r.SourceWorkspaceRepair || t.Base() != r.Assessment.Proof.Base {
		return errors.New("CI repair workspace binding changed")
	}
	source := t
	source.WorkspaceRepair, source.Candidate = r.SourceWorkspaceRepair, &r.Assessment.Proof.Candidate
	if err := g.ValidateCandidate(ctx, source); err != nil {
		return err
	}
	m, err := g.Prepare(ctx, t)
	if err != nil {
		return err
	}
	snapshot, err := Snapshot(ctx, m.WorktreePath)
	if err != nil {
		return err
	}
	if snapshot.Head == source.Candidate.Commit {
		return nil // Seeded, including interrupted edits. Never reset them.
	}
	if snapshot.Head != t.Base() || !snapshot.Clean {
		return errors.New("CI repair workspace has unexpected content; retained for inspection")
	}
	p, err := profile(ctx, m.WorktreePath)
	if err != nil {
		return err
	}
	_, err = scalar(ctx, p, "reset", "--hard", source.Candidate.Commit)
	return err
}
