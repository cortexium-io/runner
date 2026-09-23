package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const (
	DefaultSnapshotMaxEntries    = 100_000
	DefaultSnapshotMaxFileBytes  = 64 * 1024 * 1024
	DefaultSnapshotMaxTotalBytes = 1024 * 1024 * 1024
)

type SnapshotLimits = securefs.SnapshotLimits

func DefaultSnapshotLimits() SnapshotLimits {
	return SnapshotLimits{MaxEntries: DefaultSnapshotMaxEntries, MaxFileBytes: DefaultSnapshotMaxFileBytes, MaxTotalBytes: DefaultSnapshotMaxTotalBytes}
}

// Snapshot records both the integrity fingerprint and enough Git state to
// explain which repository-relative paths changed between two captures.
// File contents are represented only by hashes and are never retained.
type Snapshot struct {
	Fingerprint    string
	Head           string
	Tree           string
	Branch         string
	Clean          bool
	indexEntries   map[string]string
	worktree       map[string]string
	controlState   map[string]string
	protectedPaths []string
}

// PreparationFingerprint retains every captured Git control and source binding,
// except package-local control files inside the declared untracked dependency
// roots. It is a preparation comparison, never a publication snapshot. Capture
// still discovers and securely reads all controls, including these files.
func (s Snapshot) PreparationFingerprint(dependencies []string) (string, error) {
	if !s.Clean || s.indexEntries == nil || s.controlState == nil {
		return "", errors.New("preparation requires a freshly captured clean snapshot")
	}
	covered := func(name string) bool {
		for _, root := range dependencies {
			if name == root || strings.HasPrefix(name, root+"/") {
				return true
			}
		}
		return false
	}
	for _, root := range dependencies {
		if root == "" || root == "." || filepath.IsAbs(root) || filepath.ToSlash(filepath.Clean(root)) != root || root == ".." || strings.HasPrefix(root, "../") || strings.ContainsAny(root, "\\\x00\r\n") {
			return "", errors.New("invalid preparation dependency root")
		}
		for _, component := range strings.Split(root, "/") {
			if strings.EqualFold(component, ".git") {
				return "", errors.New("preparation cannot exclude Git administration")
			}
		}
	}
	for name := range s.indexEntries {
		if covered(name) {
			return "", errors.New("preparation cannot exclude tracked content")
		}
	}
	digest := sha256.New()
	for _, name := range sortedSnapshotKeys(s.worktree) {
		if !covered(name) {
			writeSnapshotPart(digest, "worktree-path", []byte(name))
			writeSnapshotPart(digest, "worktree-content", []byte(s.worktree[name]))
		}
	}
	var protected []string
	for _, name := range s.protectedPaths {
		if !covered(name) {
			protected = append(protected, name)
		}
	}
	for _, category := range sortedSnapshotKeys(s.controlState) {
		value := s.controlState[category]
		if category == "protected worktree files" {
			value = digestString([]byte(strings.Join(protected, "\x00")))
		}
		writeSnapshotPart(digest, "control-category", []byte(category))
		writeSnapshotPart(digest, "control-state", []byte(value))
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}

// ChangedPaths returns the sorted repository-relative paths whose index entry
// or modified, deleted, or untracked worktree state differs between snapshots.
func (before Snapshot) ChangedPaths(after Snapshot) []string {
	changed := map[string]struct{}{}
	collectChangedSnapshotPaths(changed, before.indexEntries, after.indexEntries)
	collectChangedSnapshotPaths(changed, before.worktree, after.worktree)
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

// ChangedControlState returns stable Git control-state categories whose
// machine-readable representation differs between two captures.
func (before Snapshot) ChangedControlState(after Snapshot) []string {
	changed := map[string]struct{}{}
	collectChangedSnapshotPaths(changed, before.controlState, after.controlState)
	categories := make([]string, 0, len(changed))
	for category := range changed {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	return categories
}

func collectChangedSnapshotPaths(changed map[string]struct{}, before, after map[string]string) {
	for path, value := range before {
		if afterValue, ok := after[path]; !ok || afterValue != value {
			changed[path] = struct{}{}
		}
	}
	for path, value := range after {
		if beforeValue, ok := before[path]; !ok || beforeValue != value {
			changed[path] = struct{}{}
		}
	}
}

func CaptureSnapshotWithLimits(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration, limits SnapshotLimits) (string, error) {
	snapshot, err := CaptureSnapshotStateWithLimits(ctx, run, worktreePath, timeout, limits)
	if err != nil {
		return "", err
	}
	return snapshot.Fingerprint, nil
}

// CaptureCheckoutSnapshotWithLimits records the active checkout without
// treating unrelated branch tracking changes in the shared Git config as
// checkout mutations. All other repository configuration remains part of the
// integrity fingerprint.
func CaptureCheckoutSnapshotWithLimits(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration, limits SnapshotLimits) (string, error) {
	snapshot, err := CaptureCheckoutSnapshotStateWithLimits(ctx, run, worktreePath, timeout, limits)
	if err != nil {
		return "", err
	}
	return snapshot.Fingerprint, nil
}

func CaptureSnapshotStateWithLimits(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration, limits SnapshotLimits) (Snapshot, error) {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return Snapshot{}, err
	}
	return captureSnapshotState(ctx, run, worktreePath, timeout, true, false, budget)
}

func CaptureCheckoutSnapshotStateWithLimits(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration, limits SnapshotLimits) (Snapshot, error) {
	budget, err := securefs.NewSnapshotBudget(limits)
	if err != nil {
		return Snapshot{}, err
	}
	return captureSnapshotState(ctx, run, worktreePath, timeout, true, true, budget)
}

func checkoutRelevantConfig(ctx context.Context, run subprocess.Runner, worktreePath string, content []byte, timeout time.Duration) (string, error) {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	// Parse the securely pinned bytes, not a shared config path that another
	// Git operation may atomically replace while this command is reading it.
	temporary, err := os.MkdirTemp("", "runner-snapshot-config-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	configPath := filepath.Join(temporary, "config")
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		return "", err
	}
	result, err := subprocess.RunGit(ctx, run, []string{"--no-optional-locks", "config", "--file", configPath, "--null", "--list", "--no-includes"}, worktreePath, timeout)
	if err != nil {
		return "", fmt.Errorf("capture checkout Git config: %w", commandError(err, result))
	}
	if result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\x00") {
		return "", errors.New("Git returned unterminated NUL-delimited config entries")
	}
	var relevant strings.Builder
	for _, record := range strings.Split(strings.TrimSuffix(result.Stdout, "\x00"), "\x00") {
		if record == "" {
			continue
		}
		key, value, found := strings.Cut(record, "\n")
		if !found || strings.TrimSpace(key) == "" {
			return "", errors.New("Git returned an invalid NUL-delimited config entry")
		}
		if strings.HasPrefix(strings.ToLower(key), "branch.") {
			continue
		}
		writeSnapshotPart(&relevant, key, []byte(value))
	}
	return relevant.String(), nil
}

// Recapture only an atomically updated, safe common config, once. Its protected
// settings must remain exactly the pinned projection; all other original
// snapshot controls remain pinned. Never retry an entire checkout on ErrChanged.
func (s *gitControlSnapshot) verifyCheckoutConfig(ctx context.Context, run subprocess.Runner, relevant string, timeout time.Duration) error {
	if err := s.commonConfig.verify(); err == nil {
		return nil
	} else if !errors.Is(err, securefs.ErrChanged) {
		return err
	}
	if err := securefs.ValidateOwnedRegularFile(s.commonConfig.state, uint32(os.Geteuid())); err != nil {
		return err
	}
	current, err := readPinnedControlFile(s.commonDirectory, "config", s.budget)
	if err != nil {
		return err
	}
	if err := securefs.ValidateOwnedRegularFile(current.state, uint32(os.Geteuid())); err != nil {
		return err
	}
	if current.mode != s.commonConfig.mode {
		return fmt.Errorf("%w: common Git config permissions changed", securefs.ErrChanged)
	}
	updated, err := checkoutRelevantConfig(ctx, run, s.rootPath, current.content, timeout)
	if err != nil {
		return err
	}
	if updated != relevant {
		return fmt.Errorf("%w: protected common Git config changed", securefs.ErrChanged)
	}
	if err := current.verify(); err != nil {
		return err
	}
	s.commonConfig = current
	return nil
}

func captureSnapshotState(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration, requireWorktreeRegistration, ignoreBranchTracking bool, budget *securefs.SnapshotBudget) (Snapshot, error) {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	requestedRoot, err := securefs.AbsolutePath(worktreePath)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve requested snapshot root: %w", err)
	}
	rootDirectory, err := securefs.OpenDir(requestedRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open requested snapshot root without following links: %w", err)
	}
	defer rootDirectory.Close()
	root := requestedRoot
	control, err := openGitControlSnapshot(rootDirectory, root, budget)
	if err != nil {
		return Snapshot{}, err
	}
	defer control.Close()
	worktreeConfigResult, err := subprocess.RunGit(ctx, run, []string{
		"--no-optional-locks", "config", "--file", filepath.Join(control.commonDirPath, "config"),
		"--no-includes", "--type=bool", "--default=false", "--get", "extensions.worktreeConfig",
	}, root, timeout)
	if err != nil {
		return Snapshot{}, fmt.Errorf("determine whether per-worktree Git config is enabled: %w", commandError(err, worktreeConfigResult))
	}
	worktreeConfigValue, err := snapshotScalar(worktreeConfigResult.Stdout, "extensions.worktreeConfig")
	if err != nil {
		return Snapshot{}, err
	}
	if worktreeConfigValue != "true" && worktreeConfigValue != "false" {
		return Snapshot{}, fmt.Errorf("Git returned invalid extensions.worktreeConfig value %q", worktreeConfigValue)
	}
	if err := control.pinWorktreeConfig(worktreeConfigValue == "true"); err != nil {
		return Snapshot{}, err
	}
	var relevantConfig string
	if ignoreBranchTracking {
		relevantConfig, err = checkoutRelevantConfig(ctx, run, root, control.commonConfig.content, timeout)
		if err != nil {
			return Snapshot{}, err
		}
	}
	indexResult, err := subprocess.RunGit(ctx, run, []string{"--no-optional-locks", "ls-files", "--stage", "-v", "-z"}, root, timeout)
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture snapshot index: %w", commandError(err, indexResult))
	}
	indexEntries, err := parseSnapshotIndexWithBudget(indexResult.Stdout, budget, root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("parse snapshot index: %w", err)
	}
	pinnedSubmodules, err := pinIndexedSubmodulePaths(rootDirectory, indexEntries, budget)
	if err != nil {
		return Snapshot{}, err
	}
	protectedDiscovery, err := rootDirectory.DiscoverFilesWithBudget(
		[]string{".gitignore", ".gitattributes", ".gitmodules"},
		sortedSnapshotKeys(indexedGitlinks(indexEntries)),
		budget,
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("discover protected worktree control files: %w", err)
	}
	defer protectedDiscovery.Close()

	commands := []struct {
		label string
		args  []string
	}{
		{label: "head", args: []string{"rev-parse", "--verify", "HEAD"}},
		{label: "tree", args: []string{"rev-parse", "--verify", "HEAD^{tree}"}},
		{label: "branch", args: []string{"rev-parse", "--symbolic-full-name", "HEAD"}},
		{label: "status", args: []string{"status", "--porcelain=v1", "-z", "--untracked-files=all"}},
		{label: "replacement-refs", args: []string{"for-each-ref", "--format=%(objectname) %(refname)", "refs/replace/"}},
	}
	if requireWorktreeRegistration {
		commands = append(commands, struct {
			label string
			args  []string
		}{label: "worktree", args: []string{"worktree", "list", "--porcelain", "-z"}})
	}
	results := make(map[string]string, len(commands))
	for _, command := range commands {
		args := append([]string{"--no-optional-locks"}, command.args...)
		result, runErr := subprocess.RunGit(ctx, run, args, root, timeout)
		if runErr != nil {
			return Snapshot{}, fmt.Errorf("capture snapshot %s: %w", command.label, commandError(runErr, result))
		}
		results[command.label] = result.Stdout
	}
	results["index"] = indexResult.Stdout
	head, err := snapshotScalar(results["head"], "HEAD")
	if err != nil {
		return Snapshot{}, err
	}
	tree, err := snapshotScalar(results["tree"], "HEAD tree")
	if err != nil {
		return Snapshot{}, err
	}
	branchRef, err := snapshotScalar(results["branch"], "branch")
	if err != nil {
		return Snapshot{}, err
	}
	registration := "indexed-submodule\x00" + root
	if requireWorktreeRegistration {
		registration, err = currentWorktreeRegistration(results["worktree"], root, head, branchRef)
		if err != nil {
			return Snapshot{}, fmt.Errorf("capture snapshot worktree identity: %w", err)
		}
	}
	digest := sha256.New()

	pathsResult, err := subprocess.RunGit(ctx, run, []string{"--no-optional-locks", "ls-files", "--modified", "--deleted", "--others", "--exclude-standard", "-z"}, root, timeout)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list snapshot worktree files: %w", commandError(err, pathsResult))
	}
	paths, err := snapshotPathsWithBudget(pathsResult.Stdout, budget, root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("parse snapshot worktree paths: %w", err)
	}
	worktree := map[string]string{}
	for _, relativePath := range paths {
		content, err := rootDirectory.HashPathWithBudget(relativePath, budget)
		if err != nil {
			return Snapshot{}, fmt.Errorf("capture snapshot path %q: %w", relativePath, err)
		}
		worktree[relativePath] = hex.EncodeToString(content)
	}
	protectedPaths := protectedDiscovery.Paths()
	for relativePath, digest := range protectedDiscovery.Digests() {
		worktree[relativePath] = hex.EncodeToString(digest)
	}
	submodules, err := captureIndexedSubmodules(ctx, run, rootDirectory, root, timeout, indexEntries, pinnedSubmodules, ignoreBranchTracking, budget)
	if err != nil {
		return Snapshot{}, err
	}
	for path, state := range submodules.paths {
		worktree[path] = state
	}
	for _, relativePath := range sortedSnapshotKeys(worktree) {
		writeSnapshotPart(digest, "worktree-path", []byte(relativePath))
		writeSnapshotPart(digest, "worktree-content", []byte(worktree[relativePath]))
	}
	if ignoreBranchTracking {
		if err := control.verifyCheckoutConfig(ctx, run, relevantConfig, timeout); err != nil {
			return Snapshot{}, fmt.Errorf("verify common Git config: %w", err)
		}
	}
	controlState, err := control.Finish(head, registration, results["index"], results["status"], results["replacement-refs"])
	if err != nil {
		return Snapshot{}, err
	}
	if ignoreBranchTracking {
		controlState["common Git config"] = digestString([]byte(relevantConfig))
	}
	controlState["protected worktree files"] = digestString([]byte(strings.Join(protectedPaths, "\x00")))
	controlState["indexed submodules"] = submodules.manifest
	for _, category := range sortedSnapshotKeys(controlState) {
		writeSnapshotPart(digest, "control-category", []byte(category))
		writeSnapshotPart(digest, "control-state", []byte(controlState[category]))
	}
	if err := protectedDiscovery.Verify(); err != nil {
		return Snapshot{}, fmt.Errorf("verify protected worktree control-file discovery: %w", err)
	}
	if err := rootDirectory.Verify(); err != nil {
		return Snapshot{}, fmt.Errorf("verify snapshot repository root: %w", err)
	}
	return Snapshot{
		Fingerprint:    "sha256:" + hex.EncodeToString(digest.Sum(nil)),
		Head:           head,
		Tree:           tree,
		Branch:         snapshotBranchName(branchRef),
		Clean:          results["status"] == "",
		indexEntries:   indexEntries,
		worktree:       worktree,
		controlState:   controlState,
		protectedPaths: protectedPaths,
	}, nil
}

func snapshotBranchName(branchRef string) string {
	return strings.TrimPrefix(branchRef, "refs/heads/")
}

func snapshotScalar(value, label string) (string, error) {
	if !strings.HasSuffix(value, "\n") {
		return "", fmt.Errorf("Git returned an unterminated %s", label)
	}
	value = strings.TrimSuffix(value, "\n")
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("Git returned an empty or ambiguous %s", label)
	}
	return value, nil
}

func snapshotPaths(value string) ([]string, error) {
	return snapshotPathsWithBudget(value, nil, "")
}

func snapshotPathsWithBudget(value string, budget *securefs.SnapshotBudget, root string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	if !strings.HasSuffix(value, "\x00") {
		return nil, errors.New("Git returned unterminated NUL-delimited paths")
	}
	seen := map[string]struct{}{}
	paths := make([]string, 0)
	remaining := value[:len(value)-1]
	for {
		path, rest, found := strings.Cut(remaining, "\x00")
		if !found {
			path = remaining
		}
		if path == "" {
			return nil, errors.New("Git returned an empty path")
		}
		if _, exists := seen[path]; !exists {
			if err := budget.AddEntry(filepath.Join(root, filepath.FromSlash(path))); err != nil {
				return nil, err
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
		if !found {
			break
		}
		remaining = rest
	}
	return paths, nil
}

func parseSnapshotIndex(value string) (map[string]string, error) {
	return parseSnapshotIndexWithBudget(value, nil, "")
}

func parseSnapshotIndexWithBudget(value string, budget *securefs.SnapshotBudget, root string) (map[string]string, error) {
	entries := map[string]string{}
	if value != "" && !strings.HasSuffix(value, "\x00") {
		return nil, errors.New("Git returned unterminated NUL-delimited index entries")
	}
	remaining := strings.TrimSuffix(value, "\x00")
	for remaining != "" {
		entry, rest, found := strings.Cut(remaining, "\x00")
		if !found {
			entry = remaining
		}
		separator := strings.IndexByte(entry, '\t')
		if separator < 0 || separator == len(entry)-1 {
			return nil, errors.New("Git returned an invalid index entry")
		}
		metadata, path := entry[:separator], entry[separator+1:]
		if err := budget.AddEntry(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			return nil, err
		}
		fields := strings.Split(metadata, " ")
		if len(fields) != 4 || len(fields[0]) != 1 || len(fields[1]) != 6 || fields[2] == "" || len(fields[3]) != 1 || fields[3][0] < '0' || fields[3][0] > '3' {
			return nil, errors.New("Git returned an invalid staged index entry")
		}
		if _, err := strconv.ParseUint(fields[1], 8, 32); err != nil {
			return nil, errors.New("Git returned an invalid index mode")
		}
		if _, err := hex.DecodeString(fields[2]); err != nil {
			return nil, errors.New("Git returned an invalid index object ID")
		}
		tag := fields[0][0]
		assumeUnchanged := tag >= 'a' && tag <= 'z'
		if assumeUnchanged {
			tag -= 'a' - 'A'
		}
		skipWorktree := tag == 'S'
		metadata = fmt.Sprintf("mode=%s object=%s stage=%s assume-unchanged=%t skip-worktree=%t", fields[1], fields[2], fields[3], assumeUnchanged, skipWorktree)
		if existing, ok := entries[path]; ok {
			entries[path] = existing + "\x00" + metadata
		} else {
			entries[path] = metadata
		}
		if !found {
			break
		}
		remaining = rest
	}
	return entries, nil
}

func sortedSnapshotKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writeSnapshotPart(destination io.Writer, label string, value []byte) {
	_ = binary.Write(destination, binary.BigEndian, uint64(len(label)))
	_, _ = io.WriteString(destination, label)
	_ = binary.Write(destination, binary.BigEndian, uint64(len(value)))
	_, _ = destination.Write(value)
}

func commandError(err error, result subprocess.Result) error {
	if err == nil {
		return errors.New("Git returned no repository root")
	}
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return err
	}
	if len(detail) > 1000 {
		detail = detail[:1000]
	}
	return fmt.Errorf("%w: %s", err, detail)
}
