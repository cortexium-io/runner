package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type Request struct {
	WorkingDir             string
	WorktreeRoot           string
	WorkID                 string
	ItemID                 string
	DelegatedContentDigest string
	Repository             string
	BranchPrefix           string
	BranchName             string
	BaseRef                string
	QuarantineMismatch     bool
}

type GitProvider struct {
	run    subprocess.Runner
	now    func() time.Time
	limits SnapshotLimits
}

var repositoryWorkspaceLocks sync.Map

func NewGitProvider(run subprocess.Runner) GitProvider {
	return NewGitProviderWithLimits(run, DefaultSnapshotLimits())
}

func NewGitProviderWithLimits(run subprocess.Runner, limits SnapshotLimits) GitProvider {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	return GitProvider{run: run, now: time.Now, limits: limits}
}

func (p GitProvider) Prepare(ctx context.Context, request Request) (Metadata, error) {
	repoRoot, err := p.repositoryRoot(ctx, request.WorkingDir)
	if err != nil {
		return Metadata{}, err
	}
	unlock := lockRepositoryWorkspace(repoRoot)
	defer unlock()
	sourceSnapshot, err := CaptureCheckoutSnapshotWithLimits(ctx, p.run, repoRoot, 30*time.Second, p.limits)
	if err != nil {
		return Metadata{}, fmt.Errorf("snapshot project checkout content: %w", err)
	}
	baseRef := strings.TrimSpace(request.BaseRef)
	if baseRef == "" {
		return Metadata{}, errors.New("workspace base ref is required")
	}
	baseRaw, err := p.git(ctx, repoRoot, "rev-parse", "--verify", baseRef)
	if err != nil {
		return Metadata{}, fmt.Errorf("resolve base revision: %w", err)
	}
	baseRevision := strings.TrimSpace(baseRaw.Stdout)
	if baseRevision == "" {
		return Metadata{}, errors.New("git did not return a base revision")
	}
	baseRef, err = p.canonicalBaseRef(ctx, repoRoot, baseRef)
	if err != nil {
		return Metadata{}, err
	}

	worktreeRoot, err := resolveWorktreeRoot(repoRoot, request.WorktreeRoot)
	if err != nil {
		return Metadata{}, err
	}
	if err := securefs.EnsurePrivateDir(worktreeRoot); err != nil {
		return Metadata{}, fmt.Errorf("create or validate private workspace-write root: %w", err)
	}

	workID, branchName, err := resolveWorktreeNames(request.WorkID, request.BranchPrefix, request.BranchName)
	if err != nil {
		return Metadata{}, err
	}
	if strings.TrimSpace(request.BranchName) != "" {
		if _, err := p.git(ctx, repoRoot, "check-ref-format", "--branch", branchName); err != nil {
			return Metadata{}, fmt.Errorf("invalid requested workspace branch %q", branchName)
		}
	}
	worktreePath := filepath.Join(worktreeRoot, workID)
	identity, err := newIdentity(request.ItemID, request.DelegatedContentDigest, baseRef, baseRevision, branchName, request.Repository, worktreePath)
	if err != nil {
		return Metadata{}, err
	}
	identityPath := activeIdentityPath(worktreeRoot, workID)
	recorded, hasIdentity, identityErr := readIdentity(identityPath)
	if identityErr != nil {
		return Metadata{}, fmt.Errorf("read workspace identity: %w", identityErr)
	}
	branchExists := p.branchExists(ctx, repoRoot, branchName)
	reusableIdentity := identity
	baseAdvanced := false
	if hasIdentity && recorded != identity {
		baseAdvanced = p.identityTracksBaseAdvance(ctx, repoRoot, branchName, identity, recorded)
		if baseAdvanced {
			reusableIdentity = recorded
		}
	}
	if info, err := os.Lstat(worktreePath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return Metadata{}, errors.New("workspace-write path exists but is not a no-follow directory")
		}
		registeredBranch, registered, registeredErr := p.registeredWorktree(ctx, repoRoot, worktreePath)
		if registeredErr != nil {
			return Metadata{}, registeredErr
		}
		if !registered {
			return Metadata{}, errors.New("workspace-write path exists but is not owned by the configured repository")
		}
		if registeredBranch != branchName || !branchExists || !hasIdentity || (recorded != identity && !baseAdvanced) {
			if !request.QuarantineMismatch {
				return Metadata{}, workspaceIdentityMismatch(identity, recorded, hasIdentity, registeredBranch)
			}
			if err := p.quarantine(ctx, repoRoot, worktreeRoot, workID, worktreePath, registeredBranch, identityPath, hasIdentity); err != nil {
				return Metadata{}, fmt.Errorf("quarantine incompatible workspace: %w", err)
			}
			branchExists = false
			hasIdentity = false
			baseAdvanced = false
			reusableIdentity = identity
		} else {
			existingBase, baseErr := p.git(ctx, worktreePath, "rev-parse", "--verify", "HEAD")
			if baseErr != nil || strings.TrimSpace(existingBase.Stdout) == "" {
				return Metadata{}, errors.New("workspace-write path has no reusable revision")
			}
			return bindGitAdministration(metadataFor(repoRoot, sourceSnapshot, reusableIdentity))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, fmt.Errorf("inspect workspace-write worktree path: %w", err)
	}
	retainedWorkspaceExists := branchExists || hasIdentity
	retainedWorkspaceMatches := branchExists && hasIdentity && (recorded == identity || baseAdvanced)
	if retainedWorkspaceExists && !retainedWorkspaceMatches {
		if !request.QuarantineMismatch {
			return Metadata{}, workspaceIdentityMismatch(identity, recorded, hasIdentity, "")
		}
		if err := p.quarantine(ctx, repoRoot, worktreeRoot, workID, worktreePath, branchName, identityPath, hasIdentity); err != nil {
			return Metadata{}, fmt.Errorf("quarantine incompatible retained workspace branch: %w", err)
		}
		branchExists = false
		hasIdentity = false
		baseAdvanced = false
		reusableIdentity = identity
	}
	if branchExists {
		if _, err := p.git(ctx, repoRoot, "worktree", "add", worktreePath, branchName); err != nil {
			return Metadata{}, fmt.Errorf("reopen isolated git worktree: %w", err)
		}
	} else if _, err := p.git(ctx, repoRoot, "worktree", "add", "-b", branchName, worktreePath, baseRevision); err != nil {
		return Metadata{}, fmt.Errorf("create isolated git worktree: %w", err)
	}
	if err := securefs.ValidatePrivateDir(worktreeRoot); err != nil {
		return Metadata{}, fmt.Errorf("revalidate private workspace root after worktree creation: %w", err)
	}
	if info, err := os.Lstat(worktreePath); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		if err != nil {
			return Metadata{}, fmt.Errorf("verify created workspace-write path: %w", err)
		}
		return Metadata{}, errors.New("created workspace-write path is not a no-follow directory")
	}
	if !hasIdentity {
		if err := writeIdentity(identityPath, identity); err != nil {
			return Metadata{}, fmt.Errorf("record workspace identity: %w", err)
		}
	}
	return bindGitAdministration(metadataFor(repoRoot, sourceSnapshot, reusableIdentity))
}

func resolveWorktreeNames(workID, branchPrefix, branchName string) (string, string, error) {
	workID = safeRefComponent(workID)
	if workID == "" {
		return "", "", errors.New("workspace work id is required")
	}
	prefix := strings.Trim(strings.TrimSpace(branchPrefix), "/")
	branchName = strings.TrimSpace(branchName)
	if branchName == "" && prefix == "" {
		return "", "", errors.New("workspace branch prefix is required when branch name is not provided")
	}
	if branchName == "" {
		branchName = prefix + "/" + workID
	}
	return workID, branchName, nil
}

// identityTracksBaseAdvance recognizes only the normal retained-workspace case:
// every immutable binding still matches, the recorded base is in the current
// base's history, and the task branch still contains that recorded base. The
// caller deliberately returns metadata bound to the recorded identity so the
// normal authenticated refresh path can advance it.
func (p GitProvider) identityTracksBaseAdvance(ctx context.Context, repoRoot, branchName string, current, recorded Identity) bool {
	if current.BaseRevision == recorded.BaseRevision || !validObjectID(current.BaseRevision) || !validObjectID(recorded.BaseRevision) {
		return false
	}
	comparable := recorded
	comparable.BaseRevision = current.BaseRevision
	if comparable != current {
		return false
	}
	if _, err := p.git(ctx, repoRoot, "merge-base", "--is-ancestor", recorded.BaseRevision, current.BaseRevision); err != nil {
		return false
	}
	if _, err := p.git(ctx, repoRoot, "merge-base", "--is-ancestor", recorded.BaseRevision, "refs/heads/"+branchName); err != nil {
		return false
	}
	return true
}

func lockRepositoryWorkspace(repoRoot string) func() {
	key := normalizedPath(repoRoot)
	if key == "" {
		key = filepath.Clean(repoRoot)
	}
	value, _ := repositoryWorkspaceLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	return mutex.Unlock
}

func (p GitProvider) branchExists(ctx context.Context, repoRoot, branch string) bool {
	result, err := p.git(ctx, repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil && result.ExitCode == 0
}

func (p GitProvider) repositoryRoot(ctx context.Context, workingDir string) (string, error) {
	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return "", errors.New("workspace working directory is required")
	}
	repoRootRaw, err := p.git(ctx, workingDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve git repository root: %w", err)
	}
	repoRoot := strings.TrimSpace(repoRootRaw.Stdout)
	if repoRoot == "" {
		return "", errors.New("git did not return a repository root")
	}
	return repoRoot, nil
}

func resolveWorktreeRoot(repoRoot, configuredRoot string) (string, error) {
	worktreeRoot := strings.TrimSpace(configuredRoot)
	if worktreeRoot == "" {
		return "", errors.New("workspace-write root is required")
	}
	if !filepath.IsAbs(worktreeRoot) {
		worktreeRoot = filepath.Join(filepath.Dir(repoRoot), worktreeRoot)
	}
	worktreeRoot, err := securefs.AbsolutePath(worktreeRoot)
	if err != nil {
		return "", fmt.Errorf("resolve workspace-write root: %w", err)
	}
	repoRoot, err = securefs.AbsolutePath(repoRoot)
	if err != nil {
		return "", fmt.Errorf("resolve active checkout: %w", err)
	}
	worktreeRoot = filepath.Clean(worktreeRoot)
	repoRoot = filepath.Clean(repoRoot)
	if pathInsideOrEqualLexical(worktreeRoot, repoRoot) || pathInsideOrEqualLexical(repoRoot, worktreeRoot) {
		return "", errors.New("workspace-write root and active checkout must not contain one another")
	}
	return worktreeRoot, nil
}

func pathInsideOrEqualLexical(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative))
}

func (p GitProvider) registeredWorktree(ctx context.Context, repoRoot, expectedPath string) (string, bool, error) {
	listed, err := p.git(ctx, repoRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", false, fmt.Errorf("list registered worktrees: %w", err)
	}
	records, err := parseWorktreeRegistrations(listed.Stdout)
	if err != nil {
		return "", false, fmt.Errorf("parse registered worktrees: %w", err)
	}
	for _, record := range records {
		if samePath(record.path, expectedPath) {
			return strings.TrimPrefix(record.branch, "refs/heads/"), true, nil
		}
	}
	return "", false, nil
}

func samePath(left, right string) bool {
	leftPath, leftErr := securefs.AbsolutePath(left)
	rightPath, rightErr := securefs.AbsolutePath(right)
	return leftErr == nil && rightErr == nil && leftPath == left && rightPath == right && leftPath == rightPath
}

func normalizedPath(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	current := absolute
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			absolute = resolved
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	return filepath.Clean(absolute)
}

func (p GitProvider) git(ctx context.Context, dir string, args ...string) (subprocess.Result, error) {
	return subprocess.RunGit(ctx, p.run, args, dir, 30*time.Second)
}

func (p GitProvider) canonicalBaseRef(ctx context.Context, repoRoot, baseRef string) (string, error) {
	result, err := p.git(ctx, repoRoot, "rev-parse", "--symbolic-full-name", strings.TrimSpace(baseRef))
	if err != nil {
		return "", fmt.Errorf("resolve full base ref: %w", err)
	}
	resolved := strings.TrimSpace(result.Stdout)
	if !strings.HasPrefix(resolved, "refs/") || strings.ContainsAny(resolved, "\x00\r\n") {
		return "", fmt.Errorf("workspace base %q does not resolve to a full Git ref", baseRef)
	}
	return resolved, nil
}

type Metadata struct {
	RepoRoot        string
	SourceSnapshot  string
	WorktreePath    string
	BranchName      string
	BaseRef         string
	BaseRevision    string
	Identity        Identity
	gitDirectory    string
	commonDirectory string
	objectDirectory string
}

func safeRefComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			result.WriteRune(char)
		} else if result.Len() > 0 && !strings.HasSuffix(result.String(), "-") {
			result.WriteByte('-')
		}
	}
	return strings.Trim(result.String(), "-")
}
