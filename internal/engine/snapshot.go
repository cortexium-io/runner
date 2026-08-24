package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/workspace"
)

func (s *Engine) workspaceSnapshotState(ctx context.Context, worktreePath string) (workspace.Snapshot, error) {
	return workspace.CaptureSnapshotStateWithLimits(ctx, s.run, worktreePath, 30*time.Second, s.snapshotLimits())
}

func (s *Engine) snapshotLimits() workspace.SnapshotLimits {
	limits := s.cfg.ResourceLimits
	return workspace.SnapshotLimits{MaxEntries: limits.SnapshotMaxEntries, MaxFileBytes: limits.SnapshotMaxFileBytes, MaxTotalBytes: limits.SnapshotMaxTotalBytes}
}

func snapshotChangeError(message string, before, after workspace.Snapshot) error {
	var detail strings.Builder
	detail.WriteString(strings.TrimSpace(message))
	paths := before.ChangedPaths(after)
	controlState := before.ChangedControlState(after)
	if len(paths) > 0 {
		detail.WriteString("\n\nChanged paths:")
		for _, path := range paths {
			fmt.Fprintf(&detail, "\n- %s", strconv.Quote(path))
		}
	}
	if before.Head != after.Head {
		fmt.Fprintf(&detail, "\n\nHEAD changed from %s to %s.", displaySnapshotValue(before.Head), displaySnapshotValue(after.Head))
	}
	if before.Tree != after.Tree {
		fmt.Fprintf(&detail, "\n\nHEAD tree changed from %s to %s.", displaySnapshotValue(before.Tree), displaySnapshotValue(after.Tree))
	}
	if before.Branch != after.Branch {
		fmt.Fprintf(&detail, "\n\nBranch changed from %s to %s.", displaySnapshotValue(before.Branch), displaySnapshotValue(after.Branch))
	}
	if len(controlState) > 0 {
		detail.WriteString("\n\nChanged Git control state:")
		for _, category := range controlState {
			fmt.Fprintf(&detail, "\n- %s", category)
		}
	}
	if len(paths) == 0 && before.Head == after.Head && before.Tree == after.Tree && before.Branch == after.Branch && len(controlState) == 0 {
		detail.WriteString("\n\nRepository snapshot metadata changed without an identifiable category.")
	}
	return errors.New(detail.String())
}

func displaySnapshotValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(none)"
	}
	return strconv.Quote(value)
}
