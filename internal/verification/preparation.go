package verification

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func validatePreparationCandidate(ctx context.Context, root string, entry config.VerificationEntrypoint) error {
	if entry.Preparation.Command == "" || len(entry.DependencyPaths) == 0 {
		return errors.New("preparation requires a command and explicit dependency roots")
	}
	root, err := securefs.AbsolutePath(root)
	if err != nil {
		return err
	}
	for i, dep := range entry.DependencyPaths {
		if err := validateInputPath(dep); err != nil {
			return err
		}
		for _, input := range append(append([]string{}, entry.InputPaths...), entry.DependencyPaths[:i]...) {
			if pathsOverlap(dep, input) {
				return errors.New("preparation roots overlap source or another dependency root")
			}
		}
		for _, runtimePath := range entry.RuntimePaths {
			absolute, err := securefs.AbsolutePath(runtimePath)
			if err != nil {
				return err
			}
			if pathsOverlap(filepath.Join(root, dep), absolute) {
				return errors.New("preparation root overlaps runtime identity")
			}
		}
	}
	args := append([]string{"ls-files", "-z", "--"}, entry.DependencyPaths...)
	tracked, err := subprocess.RunGit(ctx, subprocess.OSRunner{}, args, root, 30*time.Second)
	if err != nil || tracked.ExitCode != 0 || tracked.Stdout != "" {
		return errors.New("preparation dependency roots must contain no tracked files")
	}
	// Even excluded caches must not hide an external write route. Do not hash
	// their mutable bookkeeping, but inspect the no-follow directory/link closure.
	budget, _ := securefs.NewSnapshotBudget(workspace.DefaultSnapshotLimits())
	var walk func(string) error
	walk = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		if err := budget.AddEntry(absolute); err != nil {
			return err
		}
		info, err := os.Lstat(absolute)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			dir, err := securefs.OpenDir(absolute)
			if err != nil {
				return err
			}
			defer dir.Close()
			names, err := dir.ReadDirNamesWithBudget(budget)
			if err != nil {
				return err
			}
			for _, name := range names {
				if strings.EqualFold(name, ".git") {
					return errors.New("preparation cannot contain Git administration")
				}
				if err := walk(path.Join(relative, name)); err != nil {
					return err
				}
			}
			return dir.Verify()
		case info.Mode().IsRegular():
			return nil
		case info.Mode()&os.ModeSymlink != 0:
			for _, selected := range entry.DependencyPaths {
				if relative == selected {
					return errors.New("preparation root must not be a symlink")
				}
			}
			target, err := os.Readlink(absolute)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(absolute), target)
			}
			lexical, err := filepath.Rel(root, target)
			if err != nil || !coveredInput(filepath.ToSlash(lexical), entry.DependencyPaths, nil) {
				return errors.New("preparation link traverses outside dependency roots")
			}
			resolved, err := filepath.EvalSymlinks(absolute)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, resolved)
			if err != nil || !coveredInput(filepath.ToSlash(rel), entry.DependencyPaths, nil) {
				return errors.New("preparation link leaves dependency roots")
			}
			after, err := os.Lstat(absolute)
			if err != nil || !os.SameFile(info, after) || info.ModTime() != after.ModTime() {
				return errors.New("preparation link changed during inspection")
			}
			return nil
		default:
			return fmt.Errorf("preparation dependency has unsupported file type at %s", relative)
		}
	}
	for _, dep := range entry.DependencyPaths {
		// Opening the parent refuses symlinked intermediate components, including
		// when the selected dependency leaf does not exist yet.
		parent := filepath.Dir(filepath.Join(root, filepath.FromSlash(dep)))
		dir, err := securefs.OpenDir(parent)
		if err != nil {
			return fmt.Errorf("preparation root parent unavailable: %w", err)
		}
		if err := walk(dep); err != nil {
			_ = dir.Close()
			return err
		}
		err = dir.Verify()
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
