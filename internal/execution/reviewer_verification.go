package execution

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/workspace"
)

// A verification copy has no Git administration or shared build artifacts.
// The canonical candidate remains read-only; only generated files in this
// disposable copy may change. Pin copied source before invoking any harness.
type reviewerVerification struct {
	path     string
	identity os.FileInfo
	files    map[string][]byte
	limits   workspace.SnapshotLimits
}

func prepareReviewerVerification(ctx context.Context, launch *profileWorkspace, limits workspace.SnapshotLimits) (*reviewerVerification, error) {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return nil, err
	}
	source, err := os.OpenRoot(launch.ReadRoot)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	destination, err := securefs.AbsolutePath(filepath.Join(launch.Dir, "verification"))
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return nil, err
	}
	verification := &reviewerVerification{path: destination, files: map[string][]byte{}, limits: limits}
	var total int64
	err = fs.WalkDir(source.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == "." {
			return nil
		}
		target := filepath.Join(destination, filepath.FromSlash(path))
		if err := budget.AddEntry(target); err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		switch {
		case entry.Type().IsRegular():
			// The no-follow reader also rejects a path changed to a symlink
			// after discovery. Never copy credentials through repository links.
			allowance := min(limits.MaxFileBytes, limits.MaxTotalBytes-total)
			content, mode, state, err := securefs.ReadFile(filepath.Join(launch.ReadRoot, filepath.FromSlash(path)), allowance)
			if err != nil {
				return err
			}
			if !state.Exists {
				return fmt.Errorf("candidate file disappeared: %s", path)
			}
			total += int64(len(content))
			if err := os.WriteFile(target, content, mode); err != nil {
				return err
			}
		case entry.Type()&os.ModeSymlink != 0:
			link, err := source.Readlink(path)
			if err != nil {
				return err
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), link))
			if filepath.IsAbs(link) || resolved == ".." || strings.HasPrefix(resolved, ".."+string(filepath.Separator)) {
				return fmt.Errorf("verification copy cannot expose external symlink %s", path)
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported verification source type: %s", path)
		}
		verification.files[path] = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	verification.identity, err = os.Lstat(destination)
	if err != nil {
		return nil, err
	}
	root, err := securefs.OpenDir(destination)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for path := range verification.files {
		digest, err := root.HashPathWithBudget(path, budget)
		if err != nil {
			return nil, err
		}
		verification.files[path] = digest
	}
	launch.VerificationRoot = destination
	return verification, nil
}

func (v *reviewerVerification) verify() error {
	current, err := os.Lstat(v.path)
	if err != nil {
		return err
	}
	if !current.IsDir() || !os.SameFile(v.identity, current) {
		return fmt.Errorf("verification directory was replaced")
	}
	// Generated output may change directory timestamps. Reopen the same
	// directory, then pin it for this no-follow source-integrity read.
	root, err := securefs.OpenDir(v.path)
	if err != nil {
		return err
	}
	defer root.Close()
	budget, err := securefs.NewSnapshotBudget(v.limits)
	if err != nil {
		return err
	}
	for path, before := range v.files {
		after, err := root.HashPathWithBudget(path, budget)
		if err != nil {
			return err
		}
		if !bytes.Equal(before, after) {
			return fmt.Errorf("candidate source changed in verification copy: %s", path)
		}
	}
	return root.Verify()
}

func reviewerVerificationInstruction(launch profileWorkspace) string {
	return fmt.Sprintf(`

Runner-prepared disposable verification copy: %s
This copy contains the canonical candidate's source, without Git administration or the implementer's dependencies/build output. Run the unresolved dynamic checks here, not in the read-only repository or another running checkout. Git/source audit commands still use the canonical read-only root.
You may restore the existing locked dependencies and produce disposable build, cache, and test output in this copy. This is verification setup, not permission to change source, tests, manifests, lockfiles, or add product dependencies. Runner checks every copied source file after this stage and rejects changed source. Do not add substitute source or tests to make a check pass.
Use the repository's existing reproducible setup and focused commands. For npm use npm ci --ignore-scripts --no-audit --no-fund --cache %s; run only specific necessary build steps within the configured sandbox, never an unrestricted install-script workaround. Keep all generated artifacts inside this private workspace. Sandbox safe tools permit loopback and the npm registry plus the fixed public Go module path through proxy.golang.org, the storage.googleapis.com archive redirect, and sum.golang.org for this focused stage, not arbitrary external services; audit-only review receives no package-download access, while host access retains its explicitly configured boundary.
Start a candidate-local application on a free loopback port only if needed, use that exact URL, and stop it before returning. Do not assume a server on a default port belongs to this candidate. Missing node_modules or dist alone is not a blocker before attempting permitted setup. Report concrete unavailable prerequisites or failed setup honestly; never bypass sandbox restrictions or change shared resources.
`, launch.VerificationRoot, filepath.Join(launch.TempDir, "npm-cache"))
}
