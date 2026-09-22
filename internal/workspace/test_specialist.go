package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const (
	MaxTestSpecialistFiles = 32
	MaxTestSpecialistBytes = 256 * 1024
)

// TestFileChange is the bounded, Runner-observed delta, not a patch supplied by
// the model. BeforeDigest is empty only for a new file. Git/admin mutations,
// deletions, links and executable-mode changes are never represented.
type TestFileChange struct {
	Path         string `json:"path"`
	BeforeDigest string `json:"before_digest,omitempty"`
	Mode         uint32 `json:"mode"`
	Content      []byte `json:"content"`
}

type specialistFile struct {
	Digest string
	Mode   uint32
	Dir    bool
}

// TestSpecialistWorkspace owns a disposable source copy. It does not share Git
// administration, dependencies or outputs with the retained implementation.
type TestSpecialistWorkspace struct {
	Path              string
	SourceFingerprint string
	source            string
	identity          os.FileInfo
	files             map[string]specialistFile
	index             string
	allowed           []string
	limits            SnapshotLimits
}

// TestSpecialistPathAllowed excludes controls even when explicitly selected.
// The operator classifies exact files as tests/fixtures; this is not semantic
// detection of whether arbitrary source code is a test. Requests only narrow it.
func TestSpecialistPathAllowed(name string, allowed []string) bool {
	if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name ||
		strings.ContainsAny(name, `\:*?[]`) || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		switch strings.ToLower(component) {
		case "..", ".git", ".github", ".codex", ".claude", ".pi", ".runner-state", "node_modules",
			"agents.md", "claude.md", "gemini.md", ".gitignore", ".gitattributes", ".gitmodules",
			"package.json", "package-lock.json", "npm-shrinkwrap.json", ".npmrc", "go.mod", "go.sum",
			"pyproject.toml", "uv.lock", "cargo.toml", "cargo.lock", "makefile", "requirements.txt":
			return false
		}
		if strings.HasPrefix(component, ".") || strings.Contains(strings.ToLower(component), ".config.") || strings.HasPrefix(strings.ToLower(component), "tsconfig") || strings.HasPrefix(strings.ToLower(component), "jsconfig") {
			return false
		}
	}
	for _, root := range allowed {
		if name == root {
			return true
		}
	}
	return false
}

func PrepareTestSpecialistWorkspace(ctx context.Context, run subprocess.Runner, source string, allowed []string, limits SnapshotLimits) (_ *TestSpecialistWorkspace, resultErr error) {
	if len(allowed) == 0 || len(allowed) > MaxTestSpecialistFiles {
		return nil, errors.New("test specialist requires a bounded path selection")
	}
	for _, name := range allowed {
		if !TestSpecialistPathAllowed(name, []string{name}) {
			return nil, fmt.Errorf("invalid test specialist path %q", name)
		}
		if info, err := os.Lstat(filepath.Join(source, filepath.FromSlash(name))); err == nil && !info.Mode().IsRegular() {
			return nil, errors.New("test specialist policy selects files only, never directories or links")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		ignored, err := subprocess.RunGit(ctx, run, []string{"check-ignore", "--quiet", "--", name}, source, 30*time.Second)
		if ignored.ExitCode != 1 || err != nil && ignored.ExitCode != 1 {
			return nil, errors.Join(err, fmt.Errorf("specialist selection must be publishable source, not ignored residue: %s", name))
		}
	}
	before, err := CaptureSnapshotStateWithLimits(ctx, run, source, 30*time.Second, limits)
	if err != nil {
		return nil, err
	}
	result, err := subprocess.RunGit(ctx, run, []string{"--no-optional-locks", "ls-files", "--cached", "--others", "--exclude-standard", "-z"}, source, 30*time.Second)
	if err != nil || result.ExitCode != 0 {
		return nil, errors.Join(err, errors.New("cannot discover specialist source inventory"))
	}
	root, err := os.MkdirTemp("", "runner-test-specialist-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, os.RemoveAll(root))
		}
	}()
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return nil, err
	}
	var copied []string
	seen := map[string]bool{}
	var size int64
	for _, name := range strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !filepath.IsLocal(name) || filepath.ToSlash(filepath.Clean(name)) != name {
			return nil, errors.New("unsafe specialist source path")
		}
		if specialistUnsafeSource(name) {
			return nil, fmt.Errorf("specialist source contains installed dependencies, outputs or credential/control residue: %s", name)
		}
		if err := budget.AddEntry(name); err != nil {
			return nil, err
		}
		content, mode, state, err := securefs.ReadFile(filepath.Join(source, filepath.FromSlash(name)), min(limits.MaxFileBytes, limits.MaxTotalBytes-size))
		if err != nil {
			return nil, fmt.Errorf("read specialist source %s: %w", name, err)
		}
		if !state.Exists { // Preserve a retained deletion, not its indexed bytes.
			continue
		}
		size += int64(len(content))
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(target, content, mode); err != nil {
			return nil, err
		}
		copied = append(copied, name)
	}
	index, err := PrepareVerificationIndex(ctx, root, copied)
	if err != nil {
		return nil, err
	}
	files, err := specialistInventory(ctx, root, limits)
	if err != nil {
		return nil, err
	}
	after, err := CaptureSnapshotStateWithLimits(ctx, run, source, 30*time.Second, limits)
	if err != nil || before.Fingerprint != after.Fingerprint {
		return nil, errors.Join(err, errors.New("implementation changed while preparing specialist source"))
	}
	identity, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	return &TestSpecialistWorkspace{Path: root, source: source, SourceFingerprint: before.Fingerprint,
		identity: identity, files: files, index: index, allowed: slices.Clone(allowed), limits: limits}, nil
}

// Fail closed on recognized residue even when accidentally tracked or not
// ignored. Never force-include dependencies or credential-bearing dotfiles.
// Repository source/instruction files remain context and are pinned read-only.
func specialistUnsafeSource(name string) bool {
	for _, part := range strings.Split(name, "/") {
		lower := strings.ToLower(part)
		switch lower {
		case ".git", "node_modules", ".venv", "venv", "__pycache__", ".runner-state", ".runner-npm-cache",
			"dist", "build", "coverage", "test-results", "playwright-report", ".next", ".nuxt", ".cache",
			".npmrc", ".netrc", ".pypirc", ".aws", ".ssh", ".gnupg", "id_rsa", "id_ed25519":
			return true
		}
		if lower == ".env" || strings.HasPrefix(lower, ".env.") && lower != ".env.example" && lower != ".env.sample" {
			return true
		}
	}
	return false
}

func (w *TestSpecialistWorkspace) Close() error {
	info, err := os.Lstat(w.Path)
	if err != nil || !os.SameFile(w.identity, info) || !info.IsDir() {
		return errors.Join(err, errors.New("specialist workspace was replaced; preserving it"))
	}
	return os.RemoveAll(w.Path)
}

func (w *TestSpecialistWorkspace) Delta(ctx context.Context, run subprocess.Runner) ([]TestFileChange, error) {
	info, err := os.Lstat(w.Path)
	if err != nil || !os.SameFile(w.identity, info) || !info.IsDir() {
		return nil, errors.Join(err, errors.New("specialist workspace was replaced"))
	}
	source, err := CaptureSnapshotStateWithLimits(ctx, run, w.source, 30*time.Second, w.limits)
	if err != nil || source.Fingerprint != w.SourceFingerprint {
		return nil, errors.Join(err, errors.New("original implementation changed during specialist execution"))
	}
	after, err := specialistInventory(ctx, w.Path, w.limits)
	if err != nil {
		return nil, err
	}
	// Validate admin inventory before interpreting even the private Git index.
	for name, before := range w.files {
		current, exists := after[name]
		if !exists || before.Dir != current.Dir || before.Mode != current.Mode {
			return nil, fmt.Errorf("specialist removed or changed file type/mode: %s", name)
		}
		if before != current && !TestSpecialistPathAllowed(name, w.allowed) {
			return nil, fmt.Errorf("specialist changed protected source/control: %s", name)
		}
	}
	var delta []TestFileChange
	for name, current := range after {
		before, exists := w.files[name]
		if exists && before == current {
			continue
		}
		if current.Dir {
			if !specialistPathParent(name, w.allowed) {
				return nil, fmt.Errorf("specialist added undeclared directory: %s", name)
			}
			continue
		}
		if !TestSpecialistPathAllowed(name, w.allowed) {
			return nil, fmt.Errorf("specialist added undeclared file: %s", name)
		}
		content, mode, state, err := securefs.ReadFile(filepath.Join(w.Path, filepath.FromSlash(name)), MaxTestSpecialistBytes)
		if err != nil || !state.Exists || digestSpecialistBytes(content) != current.Digest || uint32(mode) != current.Mode {
			return nil, errors.Join(err, errors.New("specialist delta changed during collection"))
		}
		delta = append(delta, TestFileChange{Path: name, BeforeDigest: before.Digest, Mode: current.Mode, Content: content})
	}
	index, err := ReadVerificationIndex(ctx, w.Path)
	if err != nil || index != w.index {
		return nil, errors.Join(err, errors.New("specialist changed its private Git index"))
	}
	sort.Slice(delta, func(i, j int) bool { return delta[i].Path < delta[j].Path })
	if err := ValidateTestSpecialistDelta(delta, w.allowed); err != nil {
		return nil, err
	}
	return delta, nil
}

func ValidateTestSpecialistDelta(delta []TestFileChange, allowed []string) error {
	if len(delta) > MaxTestSpecialistFiles {
		return errors.New("specialist delta exceeds file limit")
	}
	var total int
	seen := map[string]bool{}
	for _, change := range delta {
		key := strings.ToLower(change.Path)
		if !TestSpecialistPathAllowed(change.Path, allowed) || seen[key] || change.Mode != 0o644 && change.Mode != 0o600 {
			return errors.New("specialist delta contains an unauthorized path, duplicate or mode")
		}
		seen[key] = true
		if change.BeforeDigest != "" {
			decoded, err := hex.DecodeString(change.BeforeDigest)
			if err != nil || len(decoded) != sha256.Size {
				return errors.New("specialist delta has an invalid original digest")
			}
		}
		total += len(change.Content)
		if total > MaxTestSpecialistBytes {
			return errors.New("specialist delta exceeds byte limit")
		}
	}
	return nil
}

// ApplyTestSpecialistDelta only applies a previously protected, validated delta.
// The engine must record application-started before calling. A partial failure
// preserves the workspace and spent checkpoint; callers must not blindly retry.
func ApplyTestSpecialistDelta(ctx context.Context, run subprocess.Runner, source, expected string, delta []TestFileChange, allowed []string, limits SnapshotLimits) (Snapshot, error) {
	if err := ValidateTestSpecialistDelta(delta, allowed); err != nil {
		return Snapshot{}, err
	}
	before, err := CaptureSnapshotStateWithLimits(ctx, run, source, 30*time.Second, limits)
	if err != nil || expected == "" || before.Fingerprint != expected {
		return Snapshot{}, errors.Join(err, errors.New("implementation changed before specialist delta application"))
	}
	for _, change := range delta {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		filename := filepath.Join(source, filepath.FromSlash(change.Path))
		old, mode, state, err := securefs.ReadFile(filename, MaxTestSpecialistBytes)
		if err != nil {
			return Snapshot{}, err
		}
		if state.Exists != (change.BeforeDigest != "") || state.Exists && (digestSpecialistBytes(old) != change.BeforeDigest || uint32(mode) != change.Mode) {
			return Snapshot{}, errors.New("specialist application would overwrite changed source")
		}
		directory, err := securefs.OpenDir(filepath.Dir(filename))
		if errors.Is(err, os.ErrNotExist) {
			if err = securefs.EnsurePrivateDir(filepath.Dir(filename)); err != nil {
				return Snapshot{}, err
			}
			directory, err = securefs.OpenDir(filepath.Dir(filename))
		}
		if err != nil {
			return Snapshot{}, err
		}
		err = directory.ReplaceFile(filepath.Base(filename), change.Content, os.FileMode(change.Mode), state)
		closeErr := directory.Close()
		if err != nil || closeErr != nil {
			return Snapshot{}, errors.Join(err, closeErr)
		}
	}
	after, err := CaptureSnapshotStateWithLimits(ctx, run, source, 30*time.Second, limits)
	if err != nil {
		return Snapshot{}, err
	}
	if before.Head != after.Head || before.Tree != after.Tree || before.Branch != after.Branch {
		return Snapshot{}, errors.New("implementation controls changed during specialist application")
	}
	for _, category := range before.ChangedControlState(after) {
		if category != "worktree status" { // The exact permitted content delta is checked below.
			return Snapshot{}, fmt.Errorf("implementation control changed during specialist application: %s", category)
		}
	}
	for _, name := range before.ChangedPaths(after) {
		if !slices.ContainsFunc(delta, func(change TestFileChange) bool { return change.Path == name }) {
			return Snapshot{}, errors.New("unrelated implementation content changed during specialist application")
		}
	}
	for _, change := range delta {
		content, mode, state, err := securefs.ReadFile(filepath.Join(source, filepath.FromSlash(change.Path)), MaxTestSpecialistBytes)
		if err != nil || !state.Exists || digestSpecialistBytes(content) != digestSpecialistBytes(change.Content) || uint32(mode) != change.Mode {
			return Snapshot{}, errors.Join(err, errors.New("specialist application readback failed"))
		}
	}
	return after, nil
}

func specialistPathParent(name string, allowed []string) bool {
	return slices.ContainsFunc(allowed, func(root string) bool { return strings.HasPrefix(root, name+"/") })
}

func specialistInventory(ctx context.Context, root string, limits SnapshotLimits) (map[string]specialistFile, error) {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return nil, err
	}
	files := map[string]specialistFile{}
	var walk func(string) error
	walk = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		absolute := filepath.Join(root, filepath.FromSlash(relative))
		directory, err := securefs.OpenDir(absolute)
		if err != nil {
			return err
		}
		defer directory.Close()
		names, err := directory.ReadDirNamesWithBudget(budget)
		if err != nil {
			return err
		}
		for _, name := range names {
			rel := path.Join(relative, name)
			if rel == ".git/index" { // Logical index entries are checked separately.
				continue
			}
			info, err := os.Lstat(filepath.Join(absolute, name))
			if err != nil {
				return err
			}
			entry := specialistFile{Mode: uint32(info.Mode().Perm()), Dir: info.IsDir()}
			if info.IsDir() {
				if err := walk(rel); err != nil {
					return err
				}
			} else if info.Mode().IsRegular() {
				digest, mode, err := directory.HashFileContent(ctx, name, budget)
				if err != nil {
					return err
				}
				entry.Digest, entry.Mode = hex.EncodeToString(digest), uint32(mode)
			} else {
				return fmt.Errorf("specialist source contains a link or special file: %s", rel)
			}
			files[rel] = entry
		}
		return directory.Verify()
	}
	err = walk("")
	return files, err
}

func digestSpecialistBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
