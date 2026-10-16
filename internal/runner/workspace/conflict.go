package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
	gitworkspace "github.com/cortexium-io/runner/internal/workspace"
)

var ErrConflictAttention = errors.New("base conflict requires inspection")

// MergeConflict is returned only for supported text conflicts. Other Git
// failures and conflict types remain inspection failures, never repair input.
type MergeConflict struct {
	Tree  string
	Paths []string
}

func (c *MergeConflict) Error() string {
	return "base refresh has text conflicts: " + strings.Join(c.Paths, ", ")
}

func parseConflict(output string) error {
	fields := strings.Split(output, "\x00")
	invalid := fmt.Errorf("%w: unrecognized Git conflict records; original candidate retained", ErrConflictAttention)
	if len(fields) < 4 || fields[len(fields)-1] != "" {
		return invalid
	}
	conflict := &MergeConflict{Tree: fields[0]}
	i := 1
	paths := map[string]bool{}
	for ; i < len(fields) && fields[i] != ""; i++ {
		path := fields[i]
		if !fs.ValidPath(path) || path == "." || paths[path] {
			return invalid
		}
		paths[path] = true
		conflict.Paths = append(conflict.Paths, path)
	}
	if len(paths) == 0 || i == len(fields) {
		return invalid
	}
	seen := map[string]bool{}
	for i++; i < len(fields)-1; {
		n, err := strconv.Atoi(fields[i])
		if err != nil || n < 1 || n > len(fields)-i-4 {
			return invalid
		}
		names := fields[i+1 : i+1+n]
		kind := fields[i+1+n]
		switch kind {
		case "Auto-merging":
		case "CONFLICT (contents)":
			for _, path := range names {
				if !paths[path] {
					return invalid
				}
				seen[path] = true
			}
		default:
			return fmt.Errorf("%w: unsupported merge conflict %q; original candidate retained; inspect and revise the task", ErrConflictAttention, kind)
		}
		i += n + 3
	}
	if i != len(fields)-1 || len(seen) != len(paths) {
		return invalid
	}
	return conflict
}

// Content conflicts can also involve symlinks or unusual attributes. Only
// ordinary text with explicit markers has the resolution contract used here.
func validateTextConflicts(ctx context.Context, p subprocess.PrivilegedGitProfile, c *MergeConflict) error {
	tree, err := scalar(ctx, p, "rev-parse", "--verify", c.Tree+"^{tree}")
	if err != nil || tree != c.Tree {
		return errors.New("invalid conflict tree")
	}
	for _, path := range c.Paths {
		entry, err := scalar(ctx, p, "ls-tree", "-z", c.Tree, "--", path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(entry, "100644 blob ") && !strings.HasPrefix(entry, "100755 blob ") {
			return fmt.Errorf("%w: unsupported non-file conflict at %s; inspect the retained candidate and revise the task", ErrConflictAttention, path)
		}
		data, err := scalar(ctx, p, "show", c.Tree+":"+path)
		if err != nil {
			return err
		}
		if strings.Contains(data, "\x00") || !strings.Contains("\n"+data, "\n<<<<<<<") || !strings.Contains("\n"+data, "\n>>>>>>>") {
			return fmt.Errorf("%w: unsupported non-text conflict at %s; inspect the retained candidate and revise the task", ErrConflictAttention, path)
		}
	}
	return nil
}

// PrepareConflictRepair leaves conflict contents uncommitted. The source HEAD
// marks completed seeding; model retries then preserve every partial edit. A
// crash before that marker fails closed rather than overwriting unknown files.
func (g Git) PrepareConflictRepair(ctx context.Context, t core.Task) error {
	r := t.ConflictRepair
	if r == nil || t.WorkingBase == nil || *t.WorkingBase != r.Target || t.WorkspaceRepair <= r.SourceWorkspaceRepair {
		return errors.New("conflict repair workspace binding changed")
	}
	source := t
	source.WorkingBase, source.WorkspaceRepair, source.Candidate = r.SourceBase, r.SourceWorkspaceRepair, &r.Candidate
	if err := g.ValidateCandidate(ctx, source); err != nil {
		return err
	}
	if err := g.CheckQuality(ctx, t.Base()); err != nil {
		return err
	}
	m, err := g.Prepare(ctx, t)
	if err != nil {
		return err
	}
	p, err := gitworkspace.NewGitProvider(nil).RefreshProfile(ctx, m.WorktreePath)
	if err != nil {
		return err
	}
	snapshot, err := Snapshot(ctx, m.WorktreePath)
	if err != nil {
		return err
	}
	if snapshot.Head == r.Candidate.Commit {
		return nil
	}
	if snapshot.Head != t.Base() || !snapshot.Clean {
		return errors.New("conflict workspace initialization incomplete or changed; retained for inspection; replace the task after inspecting partial work")
	}
	tree, err := scalar(ctx, p, "rev-parse", "--verify", r.Tree+"^{tree}")
	if err != nil || tree != r.Tree {
		return errors.New("conflict tree changed or unavailable")
	}
	if _, err = scalar(ctx, p, "read-tree", "--reset", "-u", r.Tree); err != nil {
		return err
	}
	// Update only HEAD, after all contents were materialized. The existing
	// candidate constructor adds the new base parent to the resolved commit.
	_, err = scalar(ctx, p, "update-ref", "HEAD", r.Candidate.Commit, t.Base())
	return err
}

// The supported input contains text markers. Reject remaining markers even if
// a permissive project check passes; neither markers nor a no-op form proof.
func CheckConflictResolution(dir string, r core.ConflictRepair) error {
	for _, path := range r.Paths {
		if !fs.ValidPath(path) || path == "." {
			return errors.New("invalid conflict path")
		}
		data, _, _, err := securefs.ReadFile(filepath.Join(dir, filepath.FromSlash(path)), 16<<20)
		if err != nil {
			return fmt.Errorf("inspect conflict resolution %s: %w", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>", "|||||||"} {
				if strings.HasPrefix(line, marker) {
					return fmt.Errorf("unresolved conflict markers in %s; inspect the retained repair", path)
				}
			}
		}
	}
	return nil
}
