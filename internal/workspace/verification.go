package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// PrepareVerificationIndex gives a new disposable source copy a standalone
// index for Git-based file inventory. It creates no commits, refs, remotes, or
// shared object links. Only the supplied copied paths are staged, including
// files excluded by .gitignore. Literal staging bypasses filters and attributes.
func PrepareVerificationIndex(ctx context.Context, root string, paths []string) (string, error) {
	profile, err := verificationGitProfile(root)
	if err != nil {
		return "", err
	}
	if err := initializeVerificationGit(ctx, profile); err != nil {
		return "", err
	}
	paths = append([]string(nil), paths...)
	sort.Strings(paths)
	if err := stageVerificationPaths(ctx, profile, paths); err != nil {
		return "", err
	}
	return ReadVerificationIndex(ctx, root)
}

func initializeVerificationGit(ctx context.Context, profile subprocess.PrivilegedGitProfile) error {
	if err := os.Mkdir(profile.GitDirectory, 0o700); err != nil {
		return fmt.Errorf("create private verification Git directory: %w", err)
	}
	if result, err := NewGitProvider(nil).privilegedGit(ctx, profile, "init", "--quiet", "--template=", "--initial-branch=runner-verification"); err != nil {
		return fmt.Errorf("initialize verification index: %w", commandError(err, result))
	}
	return nil
}

// Batch Git's literal blob hashing and NUL-delimited index-info transport. Do
// not use git add or update-index --stdin: they can run source-defined filters.
func stageVerificationPaths(ctx context.Context, profile subprocess.PrivilegedGitProfile, paths []string) error {
	temporary, err := os.MkdirTemp("", "runner-verification-blobs-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	modes := make([]string, len(paths))
	var input strings.Builder
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !filepath.IsLocal(path) || filepath.Clean(path) != path || path == "." || strings.ContainsRune(path, '\x00') {
			return fmt.Errorf("invalid verification source path %q", path)
		}
		for _, component := range strings.Split(path, string(filepath.Separator)) {
			if strings.EqualFold(component, ".git") {
				return fmt.Errorf("verification source includes Git administration: %q", path)
			}
		}
		full := filepath.Join(profile.WorkTree, path)
		info, err := os.Lstat(full)
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
			modes[i] = "100644"
			if info.Mode()&0o111 != 0 {
				modes[i] = "100755"
			}
		case info.Mode()&os.ModeSymlink != 0:
			modes[i] = "120000"
			target, err := os.Readlink(full)
			if err != nil {
				return err
			}
			// Hash the link text, never its destination.
			full = filepath.Join(temporary, strconv.Itoa(i))
			if err := os.WriteFile(full, []byte(target), 0o600); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported verification source type: %q", path)
		}
		input.WriteString(quoteVerificationGitPath(full))
		input.WriteByte('\n')
		if input.Len() > subprocess.GitStdoutLimit {
			return fmt.Errorf("verification path inventory exceeds %d bytes", subprocess.GitStdoutLimit)
		}
	}
	var index, expected strings.Builder
	if len(paths) > 0 {
		result, err := subprocess.RunPrivilegedGitInput(ctx, profile, []string{"hash-object", "-w", "--no-filters", "--stdin-paths"}, strings.NewReader(input.String()), 30*time.Second)
		if err != nil {
			return fmt.Errorf("hash verification source: %w", commandError(err, result))
		}
		objects := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
		if len(objects) != len(paths) {
			return fmt.Errorf("verification blob count does not match source inventory")
		}
		for i, object := range objects {
			if !validObjectID(object) {
				return fmt.Errorf("invalid verification blob identity")
			}
			fmt.Fprintf(&index, "%s %s\t%s\x00", modes[i], object, paths[i])
			fmt.Fprintf(&expected, "H %s %s 0\t%s\x00", modes[i], object, paths[i])
			if index.Len() > subprocess.GitStdoutLimit {
				return fmt.Errorf("verification index inventory exceeds %d bytes", subprocess.GitStdoutLimit)
			}
		}
	}
	result, err := subprocess.RunPrivilegedGitInput(ctx, profile, []string{"update-index", "-z", "--index-info"}, strings.NewReader(index.String()), 30*time.Second)
	if err != nil {
		return fmt.Errorf("stage verification source: %w", commandError(err, result))
	}
	// Batch update-index can report success while ignoring unsupported paths
	// (for example a .gitmodules symlink). Never adopt an incomplete baseline.
	result, err = NewGitProvider(nil).privilegedGit(ctx, profile, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return fmt.Errorf("inspect staged verification source: %w", commandError(err, result))
	}
	if result.Stdout != expected.String() {
		return fmt.Errorf("verification index does not contain the exact copied source inventory")
	}
	return nil
}

// --stdin-paths accepts Git's C-quoted format, not JSON/Go Unicode escapes.
// Byte-wise octal quoting preserves tabs, newlines and non-UTF-8 filenames.
func quoteVerificationGitPath(path string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= ' ' && c <= '~' && c != '"' && c != '\\' {
			quoted.WriteByte(c)
		} else {
			fmt.Fprintf(&quoted, "\\%03o", c)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}

// ReadVerificationIndex compares logical entries rather than index cache
// bytes: ordinary Git status refreshes must not invalidate otherwise identical
// verification. The caller separately pins the copied source and Git controls.
func ReadVerificationIndex(ctx context.Context, root string) (string, error) {
	profile, err := verificationGitProfile(root)
	if err != nil {
		return "", err
	}
	// Capture the only mutable input without following links, then give Git a
	// private copy. Never reopen the harness-writable index or its auxiliary
	// files from privileged Git (not even after checking them for symlinks).
	index, _, state, err := securefs.ReadFile(profile.IndexFile, subprocess.GitStdoutLimit)
	if err != nil {
		return "", fmt.Errorf("inspect verification index: %w", err)
	}
	if !state.Exists {
		// Git need not create an index for an empty source inventory. A deleted
		// nonempty index still differs from the caller's pinned logical entries.
		return "", nil
	}
	temporary, err := os.MkdirTemp("", "runner-verification-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	profile, err = verificationGitProfile(temporary)
	if err != nil {
		return "", err
	}
	if err := initializeVerificationGit(ctx, profile); err != nil {
		return "", err
	}
	if err := os.WriteFile(profile.IndexFile, index, 0o600); err != nil {
		return "", err
	}
	p := NewGitProvider(nil)
	result, err := p.privilegedGit(ctx, profile, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return "", fmt.Errorf("read verification index: %w", commandError(err, result))
	}
	// ls-files exposes assume-unchanged/skip-worktree, but an intent-to-add
	// entry for an empty blob looks identical. Include the staged projection
	// against this scratch repository's unborn HEAD to detect that flag too.
	staged, err := p.privilegedGit(ctx, profile, "diff", "--cached", "--raw", "--no-abbrev", "--no-ext-diff", "--no-textconv", "--ita-invisible-in-index", "-z", "--")
	if err != nil {
		return "", fmt.Errorf("read verification staged inventory: %w", commandError(err, staged))
	}
	return result.Stdout + staged.Stdout, nil
}

func verificationGitProfile(root string) (subprocess.PrivilegedGitProfile, error) {
	root, err := securefs.AbsolutePath(root)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, err
	}
	gitDir := filepath.Join(root, ".git")
	return subprocess.NewPrivilegedGitProfile(root, gitDir, gitDir, filepath.Join(gitDir, "index"), filepath.Join(gitDir, "objects"))
}
