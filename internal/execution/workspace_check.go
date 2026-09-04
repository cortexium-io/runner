package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type workspaceVerifier struct {
	run     subprocess.Runner
	timeout time.Duration
	limits  workspace.SnapshotLimits
}

func newWorkspaceVerifier(run subprocess.Runner, timeout time.Duration, configured ...workspace.SnapshotLimits) workspaceVerifier {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	limits := workspace.DefaultSnapshotLimits()
	if len(configured) > 0 {
		limits = configured[0]
	}
	return workspaceVerifier{run: run, timeout: timeout, limits: limits}
}

func (v workspaceVerifier) Verify(ctx context.Context, metadata workspace.Metadata) error {
	snapshot, err := workspace.CaptureCheckoutSnapshotWithLimits(ctx, v.run, metadata.RepoRoot, v.timeout, v.limits)
	if err != nil {
		return fmt.Errorf("inspect active checkout content: %w", err)
	}
	if snapshot != metadata.SourceSnapshot {
		return errors.New("active checkout changed during workspace-write execution")
	}
	if result, err := v.git(ctx, metadata.WorktreePath, "diff", "--check"); err != nil {
		return fmt.Errorf("task diff failed git diff --check: %w: %s", err, commandFailure(err, result))
	}
	return nil
}

func snapshotLimits(limits config.ResourceLimits) workspace.SnapshotLimits {
	if limits.SnapshotMaxEntries == 0 {
		limits = config.DefaultResourceLimits()
	}
	return workspace.SnapshotLimits{MaxEntries: limits.SnapshotMaxEntries, MaxFileBytes: limits.SnapshotMaxFileBytes, MaxTotalBytes: limits.SnapshotMaxTotalBytes}
}

func (v workspaceVerifier) git(ctx context.Context, dir string, args ...string) (subprocess.Result, error) {
	return subprocess.RunGit(ctx, v.run, args, dir, v.timeout)
}
