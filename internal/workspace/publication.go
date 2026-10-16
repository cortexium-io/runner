package workspace

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/sandboxpath"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

var privilegedGitMu sync.Mutex

// ErrCandidateIndexBusy means Git could not acquire this worktree's index lock
// after bounded retries. It does not establish a candidate integrity violation.
var ErrCandidateIndexBusy = errors.New("candidate Git index is locked")

type Candidate struct {
	CommitOID string
	TreeOID   string
}

// ReviewWorkspace is a private detached checkout of the exact candidate
// commit. It is created only after implementation ends and is never granted to
// the implementation sandbox, so review input cannot be changed by a child
// process that escaped the implementation process group.
type ReviewWorkspace struct {
	Path          string
	parent        string
	provider      GitProvider
	sourceProfile subprocess.PrivilegedGitProfile
}

// PrepareReviewWorkspace materializes the exact candidate through Runner's
// config-free privileged Git boundary and verifies its commit, tree, and clean
// state before a reviewer can see it.
func (p GitProvider) PrepareReviewWorkspace(ctx context.Context, metadata Metadata, candidate Candidate) (ReviewWorkspace, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if err := validateCandidateMetadata(metadata); err != nil {
		return ReviewWorkspace{}, err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return ReviewWorkspace{}, err
	}
	if !validObjectID(candidate.CommitOID) || !validObjectID(candidate.TreeOID) {
		return ReviewWorkspace{}, errors.New("review workspace requires valid candidate commit and tree object IDs")
	}
	sourceProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return ReviewWorkspace{}, err
	}
	parent, err := newReviewWorkspaceParent(metadata.WorktreePath, metadata.RepoRoot)
	if err != nil {
		return ReviewWorkspace{}, fmt.Errorf("create private review workspace: %w", err)
	}
	path := filepath.Join(parent, "candidate")
	result, err := p.privilegedGit(ctx, sourceProfile, "worktree", "add", "--detach", "--no-checkout", path, candidate.CommitOID)
	if err != nil {
		_ = os.RemoveAll(parent)
		return ReviewWorkspace{}, fmt.Errorf("materialize private review workspace: %w", commandError(err, result))
	}
	review := ReviewWorkspace{Path: path, parent: parent, provider: p, sourceProfile: sourceProfile}
	fail := func(cause error) (ReviewWorkspace, error) {
		if cleanupErr := review.cleanupLocked(ctx); cleanupErr != nil {
			cause = errors.Join(cause, cleanupErr)
		}
		return ReviewWorkspace{}, cause
	}
	reviewProfile, err := derivePrivilegedGitProfile(path)
	if err != nil {
		return fail(err)
	}
	if reviewProfile.CommonDirectory != metadata.commonDirectory || reviewProfile.ObjectDirectory != metadata.objectDirectory {
		return fail(errors.New("private review workspace does not share the prepared candidate object store"))
	}
	reset, err := p.privilegedGit(ctx, reviewProfile, "reset", "--hard", candidate.CommitOID)
	if err != nil {
		return fail(fmt.Errorf("checkout private review candidate: %w", commandError(err, reset)))
	}
	head, err := p.privilegedScalar(ctx, reviewProfile, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fail(fmt.Errorf("verify private review commit: %w", err))
	}
	tree, err := p.privilegedScalar(ctx, reviewProfile, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return fail(fmt.Errorf("verify private review tree: %w", err))
	}
	status, err := p.privilegedGit(ctx, reviewProfile, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return fail(fmt.Errorf("verify private review workspace: %w", commandError(err, status)))
	}
	if head != candidate.CommitOID || tree != candidate.TreeOID || strings.TrimSpace(status.Stdout) != "" {
		return fail(fmt.Errorf("private review workspace is not the exact clean candidate: head=%q tree=%q status=%q", head, tree, status.Stdout))
	}
	return review, nil
}

func reviewWorkspaceRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory for review workspaces: %w", err)
	}
	if !filepath.IsAbs(cache) {
		return "", fmt.Errorf("review workspace root requires an absolute user cache directory: %s", cache)
	}
	return securefs.AbsolutePath(filepath.Join(cache, "cortexium-runner", "review-workspaces"))
}

func newReviewWorkspaceParent(protectedRoots ...string) (string, error) {
	npmRoot, err := sandboxpath.NPMCacheRoot()
	if err != nil {
		return "", err
	}
	protectedRoots = append(append([]string(nil), protectedRoots...), npmRoot)

	root, err := reviewWorkspaceRoot()
	if err != nil {
		return "", err
	}
	resolvedRoot := normalizedPath(root)
	for _, protected := range protectedRoots {
		if strings.TrimSpace(protected) == "" {
			continue
		}
		resolvedProtected := normalizedPath(protected)
		if pathInsideOrEqualLexical(resolvedRoot, resolvedProtected) || pathInsideOrEqualLexical(resolvedProtected, resolvedRoot) {
			return "", fmt.Errorf("private review workspace root %s overlaps protected or sandbox-writable root %s", root, protected)
		}
	}
	if err := securefs.EnsurePrivateDir(root); err != nil {
		return "", fmt.Errorf("create private review workspace root: %w", err)
	}

	parent, err := os.MkdirTemp(root, "review-")
	if err != nil {
		return "", fmt.Errorf("create private review workspace parent: %w", err)
	}
	if err := securefs.ValidatePrivateDir(parent); err != nil {
		_ = os.RemoveAll(parent)
		return "", fmt.Errorf("validate private review workspace parent: %w", err)
	}
	resolvedParent := normalizedPath(parent)
	for _, protected := range protectedRoots {
		if strings.TrimSpace(protected) == "" {
			continue
		}
		resolvedProtected := normalizedPath(protected)
		if pathInsideOrEqualLexical(resolvedParent, resolvedProtected) || pathInsideOrEqualLexical(resolvedProtected, resolvedParent) {
			_ = os.RemoveAll(parent)
			return "", fmt.Errorf("private review workspace parent %s overlaps protected or sandbox-writable root %s", parent, protected)
		}
	}
	return parent, nil
}

// Cleanup removes the linked worktree registration and private checkout.
func (workspace ReviewWorkspace) Cleanup(ctx context.Context) error {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	return workspace.cleanupLocked(ctx)
}

func (workspace ReviewWorkspace) cleanupLocked(ctx context.Context) error {
	if strings.TrimSpace(workspace.parent) == "" || strings.TrimSpace(workspace.Path) == "" {
		return nil
	}
	var cleanupErr error
	result, err := workspace.provider.privilegedGit(ctx, workspace.sourceProfile, "worktree", "remove", "--force", workspace.Path)
	if err != nil {
		cleanupErr = fmt.Errorf("remove private review worktree: %w", commandError(err, result))
	}
	if err := os.RemoveAll(workspace.parent); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove private review workspace: %w", err))
	}
	return cleanupErr
}

type candidateValidationError struct {
	correction string
	cause      error
}

func (e candidateValidationError) Error() string {
	return e.cause.Error()
}

func (e candidateValidationError) Unwrap() error {
	return e.cause
}

// CandidateValidationCorrection returns a Runner-authored, remotely safe
// correction only for ordinary candidate-content failures. Workspace identity
// and Git administration failures deliberately remain local integrity errors.
func CandidateValidationCorrection(err error) (string, bool) {
	var validation candidateValidationError
	if !errors.As(err, &validation) || strings.TrimSpace(validation.correction) == "" {
		return "", false
	}
	return strings.TrimSpace(validation.correction), true
}

func recoverableCandidateError(correction string, cause error) error {
	return candidateValidationError{correction: strings.TrimSpace(correction), cause: cause}
}

// ConstructCandidate stages worktree bytes without Git clean filters, writes a
// commit without hooks or signing, and atomically advances only the task branch.
func (p GitProvider) ConstructCandidate(ctx context.Context, metadata Metadata, message string) (Candidate, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if err := validateCandidateMetadata(metadata); err != nil {
		return Candidate{}, err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return Candidate{}, err
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return Candidate{}, err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return Candidate{}, err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return Candidate{}, err
	}
	branch, err := p.privilegedScalar(ctx, profile, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return Candidate{}, fmt.Errorf("resolve candidate branch: %w", err)
	}
	if branch != "refs/heads/"+metadata.BranchName {
		return Candidate{}, fmt.Errorf("candidate branch %q does not match workspace branch %q", branch, metadata.BranchName)
	}
	flagsResult, err := p.privilegedGit(ctx, profile, "ls-files", "-v", "-z")
	if err != nil {
		return Candidate{}, fmt.Errorf("inspect candidate index flags: %w", commandError(err, flagsResult))
	}
	if err := rejectCandidateIndexFlags(flagsResult.Stdout); err != nil {
		return Candidate{}, err
	}
	pathsResult, err := p.privilegedGit(ctx, profile, "ls-files", "--cached", "-z")
	if err != nil {
		return Candidate{}, fmt.Errorf("list tracked candidate paths: %w", commandError(err, pathsResult))
	}
	paths, err := snapshotPaths(pathsResult.Stdout)
	if err != nil {
		return Candidate{}, fmt.Errorf("parse tracked candidate paths: %w", err)
	}
	untrackedResult, err := p.privilegedGit(ctx, profile, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return Candidate{}, fmt.Errorf("list untracked candidate paths: %w", commandError(err, untrackedResult))
	}
	untracked, err := snapshotPaths(untrackedResult.Stdout)
	if err != nil {
		return Candidate{}, fmt.Errorf("parse untracked candidate paths: %w", err)
	}
	paths = append(paths, untracked...)
	for _, path := range paths {
		if err := p.stageCandidatePath(ctx, profile, path); err != nil {
			return Candidate{}, err
		}
	}
	if result, runErr := p.privilegedGit(ctx, profile, "ls-files", "-u"); runErr != nil {
		return Candidate{}, fmt.Errorf("inspect staged candidate conflicts: %w", commandError(runErr, result))
	} else if result.Stdout != "" {
		return Candidate{}, recoverableCandidateError(
			"Candidate still contains unresolved merge conflicts. Resolve every conflict and rerun `git diff --cached --check` before retrying.",
			errors.New("candidate contains unresolved index conflicts"),
		)
	}
	if result, runErr := p.privilegedGit(ctx, profile, "diff", "--cached", "--check"); runErr != nil {
		cause := fmt.Errorf("check staged candidate: %w", commandError(runErr, result))
		if strings.TrimSpace(result.Stdout) != "" {
			return Candidate{}, recoverableCandidateError(candidateDiffCheckCorrection(result.Stdout), cause)
		}
		return Candidate{}, cause
	} else if strings.TrimSpace(result.Stdout) != "" {
		return Candidate{}, recoverableCandidateError(
			candidateDiffCheckCorrection(result.Stdout),
			errors.New("candidate contains whitespace errors or unresolved conflict markers"),
		)
	}
	treeOID, err := p.privilegedScalar(ctx, profile, "write-tree")
	if err != nil {
		return Candidate{}, fmt.Errorf("write candidate tree: %w", err)
	}
	headOID, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Candidate{}, fmt.Errorf("resolve candidate parent: %w", err)
	}
	headTree, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return Candidate{}, fmt.Errorf("resolve current candidate tree: %w", err)
	}
	mergeHead := ""
	mergeResult, mergeErr := p.privilegedGit(ctx, profile, "rev-parse", "--verify", "MERGE_HEAD")
	if mergeErr == nil {
		mergeHead = strings.TrimSuffix(mergeResult.Stdout, "\n")
		if !validObjectID(mergeHead) || mergeHead != metadata.BaseRevision {
			return Candidate{}, errors.New("candidate merge parent does not match the authenticated base revision")
		}
	} else if mergeResult.ExitCode != 1 && mergeResult.ExitCode != 128 {
		return Candidate{}, fmt.Errorf("inspect candidate merge parent: %w", commandError(mergeErr, mergeResult))
	}
	baseIntegrated := false
	ancestorResult, ancestorErr := p.privilegedGit(ctx, profile, "merge-base", "--is-ancestor", metadata.BaseRevision, headOID)
	if ancestorErr == nil && ancestorResult.ExitCode == 0 {
		baseIntegrated = true
	} else if ancestorResult.ExitCode != 1 {
		return Candidate{}, fmt.Errorf("verify candidate base ancestry: %w", commandError(ancestorErr, ancestorResult))
	}
	needsBaseParent := !baseIntegrated
	if treeOID != headTree || headOID == metadata.BaseRevision || mergeHead != "" || needsBaseParent {
		message = strings.TrimSpace(message)
		if message == "" {
			message = "Implement approved Runner work item"
		}
		commitArgs := []string{"commit-tree", treeOID}
		commitArgs = append(commitArgs, "-p", headOID)
		if mergeHead != "" {
			commitArgs = append(commitArgs, "-p", mergeHead)
		} else if needsBaseParent {
			commitArgs = append(commitArgs, "-p", metadata.BaseRevision)
		}
		commitArgs = append(commitArgs, "--no-gpg-sign", "-m", message)
		commitOID, commitErr := p.privilegedScalar(ctx, profile, commitArgs...)
		if commitErr != nil {
			return Candidate{}, fmt.Errorf("write candidate commit: %w", commitErr)
		}
		if result, updateErr := p.privilegedGit(ctx, profile, "update-ref", "refs/heads/"+metadata.BranchName, commitOID, headOID); updateErr != nil {
			return Candidate{}, fmt.Errorf("advance candidate branch: %w", commandError(updateErr, result))
		}
		headOID = commitOID
		if mergeHead != "" {
			if result, resetErr := p.privilegedGit(ctx, profile, "reset", "--mixed", "HEAD"); resetErr != nil {
				return Candidate{}, fmt.Errorf("clear committed candidate merge state: %w", commandError(resetErr, result))
			}
		}
	}
	verifiedProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return Candidate{}, fmt.Errorf("rederive committed candidate administration: %w", err)
	}
	if verifiedProfile != profile {
		return Candidate{}, errors.New("candidate Git administration changed during construction")
	}
	if err := rejectObjectRedirection(verifiedProfile); err != nil {
		return Candidate{}, err
	}
	if err := p.rejectReplacementObjects(ctx, verifiedProfile); err != nil {
		return Candidate{}, err
	}
	if err := p.verifyCandidateWorktree(ctx, profile); err != nil {
		return Candidate{}, err
	}
	verifiedHead, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Candidate{}, fmt.Errorf("verify committed candidate HEAD: %w", err)
	}
	verifiedTree, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return Candidate{}, fmt.Errorf("verify committed candidate tree: %w", err)
	}
	verifiedBranch, err := p.privilegedScalar(ctx, profile, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return Candidate{}, fmt.Errorf("verify committed candidate branch: %w", err)
	}
	if verifiedHead != headOID || verifiedTree != treeOID || verifiedBranch != "refs/heads/"+metadata.BranchName {
		return Candidate{}, errors.New("committed candidate HEAD, tree, or branch changed during construction")
	}
	return Candidate{CommitOID: headOID, TreeOID: treeOID}, nil
}

func candidateDiffCheckCorrection(output string) string {
	lower := strings.ToLower(output)
	reasons := make([]string, 0, 4)
	for _, candidate := range []struct {
		marker string
		label  string
	}{
		{marker: "trailing whitespace", label: "trailing whitespace"},
		{marker: "space before tab", label: "spaces before tabs"},
		{marker: "new blank line at eof", label: "new blank lines at end of file"},
		{marker: "leftover conflict marker", label: "leftover conflict markers"},
	} {
		if strings.Contains(lower, candidate.marker) {
			reasons = append(reasons, candidate.label)
		}
	}
	reason := "whitespace or conflict-marker errors"
	if len(reasons) > 0 {
		reason = strings.Join(reasons, ", ")
	}
	return "Candidate failed `git diff --cached --check` because it contains " + reason + ". Correct every reported line before retrying."
}

func rejectCandidateIndexFlags(value string) error {
	if value != "" && !strings.HasSuffix(value, "\x00") {
		return errors.New("Git returned unterminated candidate index flags")
	}
	for _, entry := range strings.Split(strings.TrimSuffix(value, "\x00"), "\x00") {
		if entry == "" {
			continue
		}
		if len(entry) < 3 || entry[1] != ' ' {
			return errors.New("Git returned invalid candidate index flags")
		}
		tag := entry[0]
		if tag == 'S' || tag >= 'a' && tag <= 'z' {
			return fmt.Errorf("candidate index path %q uses hidden worktree state", entry[2:])
		}
	}
	return nil
}

func validateCandidateMetadata(metadata Metadata) error {
	identity := metadata.Identity
	if normalizedPath(metadata.WorktreePath) == "" || normalizedPath(metadata.WorktreePath) != identity.WorktreePath ||
		normalizedPath(metadata.RepoRoot) == "" ||
		strings.TrimSpace(metadata.BranchName) != identity.Branch || strings.TrimSpace(metadata.BaseRef) != identity.BaseRef ||
		strings.TrimSpace(metadata.BaseRevision) != identity.BaseRevision || metadata.gitDirectory == "" ||
		metadata.commonDirectory == "" || metadata.objectDirectory == "" {
		return errors.New("candidate metadata does not match the private workspace identity")
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return err
	}
	if profile.GitDirectory != metadata.gitDirectory || profile.CommonDirectory != metadata.commonDirectory || profile.ObjectDirectory != metadata.objectDirectory {
		return errors.New("candidate Git administration does not match the prepared workspace")
	}
	return nil
}

func bindGitAdministration(metadata Metadata) (Metadata, error) {
	worktreeProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return Metadata{}, err
	}
	repositoryProfile, err := derivePrivilegedGitProfile(metadata.RepoRoot)
	if err != nil {
		return Metadata{}, fmt.Errorf("derive repository Git administration: %w", err)
	}
	if worktreeProfile.CommonDirectory != repositoryProfile.CommonDirectory || worktreeProfile.ObjectDirectory != repositoryProfile.ObjectDirectory {
		return Metadata{}, errors.New("workspace Git administration does not belong to the configured repository")
	}
	metadata.gitDirectory = worktreeProfile.GitDirectory
	metadata.commonDirectory = worktreeProfile.CommonDirectory
	metadata.objectDirectory = worktreeProfile.ObjectDirectory
	return metadata, nil
}

func validateRecordedIdentity(metadata Metadata) error {
	worktreeRoot := filepath.Dir(metadata.Identity.WorktreePath)
	workID := filepath.Base(metadata.Identity.WorktreePath)
	recorded, found, err := readIdentity(activeIdentityPath(worktreeRoot, workID))
	if err != nil {
		return fmt.Errorf("read private workspace identity: %w", err)
	}
	if !found || recorded != metadata.Identity {
		return errors.New("candidate metadata is not the active private workspace identity")
	}
	return nil
}

func derivePrivilegedGitProfile(worktreePath string) (subprocess.PrivilegedGitProfile, error) {
	root, err := securefs.AbsolutePath(worktreePath)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, fmt.Errorf("resolve privileged worktree: %w", err)
	}
	rootDirectory, err := securefs.OpenDir(root)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, fmt.Errorf("open privileged worktree without following links: %w", err)
	}
	defer rootDirectory.Close()
	control, err := openGitControlSnapshot(rootDirectory, root, nil)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, fmt.Errorf("derive privileged Git administration: %w", err)
	}
	defer control.Close()
	profile, err := subprocess.NewPrivilegedGitProfile(root, control.gitDirPath, control.commonDirPath, filepath.Join(control.gitDirPath, "index"), filepath.Join(control.commonDirPath, "objects"))
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, err
	}
	objects, err := securefs.OpenDir(profile.ObjectDirectory)
	if err != nil {
		return subprocess.PrivilegedGitProfile{}, fmt.Errorf("open pinned Git object directory: %w", err)
	}
	if err := objects.Close(); err != nil {
		return subprocess.PrivilegedGitProfile{}, fmt.Errorf("close pinned Git object directory: %w", err)
	}
	return profile, nil
}

func rejectObjectRedirection(profile subprocess.PrivilegedGitProfile) error {
	objectInfo := filepath.Join(profile.ObjectDirectory, "info")
	commonInfo := filepath.Join(profile.CommonDirectory, "info")
	for _, directory := range []string{objectInfo, commonInfo} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect privileged Git redirection directory: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("privileged Git refuses redirected control directory %q", directory)
		}
	}
	for _, path := range []string{
		filepath.Join(objectInfo, "alternates"),
		filepath.Join(objectInfo, "http-alternates"),
		filepath.Join(commonInfo, "grafts"),
	} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect privileged Git object redirection: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() != 0 {
			return fmt.Errorf("privileged Git refuses object redirection at %q", path)
		}
	}
	return nil
}

func (p GitProvider) rejectReplacementObjects(ctx context.Context, profile subprocess.PrivilegedGitProfile) error {
	result, err := p.privilegedGit(ctx, profile, "for-each-ref", "--format=%(refname)", "refs/replace/")
	if err != nil {
		return fmt.Errorf("inspect privileged Git replacement objects: %w", commandError(err, result))
	}
	if strings.TrimSpace(result.Stdout) != "" {
		return errors.New("privileged Git refuses replacement objects")
	}
	return nil
}

// PublicationProfile reuses the config-free object boundary for a caller that
// owns its own durable publication authority (for example the local Runner core).
func (p GitProvider) PublicationProfile(ctx context.Context, path string) (subprocess.PrivilegedGitProfile, error) {
	profile, err := derivePrivilegedGitProfile(path)
	if err != nil {
		return profile, err
	}
	if err = rejectObjectRedirection(profile); err != nil {
		return profile, err
	}
	return profile, p.rejectReplacementObjects(ctx, profile)
}

// RefreshProfile also refuses executable merge configuration before a consumer
// constructs a new candidate with the protected Git object boundary.
func (p GitProvider) RefreshProfile(ctx context.Context, path string) (subprocess.PrivilegedGitProfile, error) {
	profile, err := p.PublicationProfile(ctx, path)
	if err == nil {
		err = p.rejectExecutableMergeConfig(ctx, profile)
	}
	return profile, err
}

func (p GitProvider) stageCandidatePath(ctx context.Context, profile subprocess.PrivilegedGitProfile, path string) error {
	mode, objectID, exists, err := p.candidatePathEntry(ctx, profile, path)
	if err != nil {
		return err
	}
	if !exists {
		if err := p.updateCandidateIndex(ctx, profile, "--force-remove", "--", path); err != nil {
			return fmt.Errorf("remove deleted candidate path %q: %w", path, err)
		}
		return nil
	}
	if err := p.updateCandidateIndex(ctx, profile, "--add", "--cacheinfo", mode, objectID, path); err != nil {
		return fmt.Errorf("stage candidate path %q: %w", path, err)
	}
	return nil
}

func (p GitProvider) updateCandidateIndex(ctx context.Context, profile subprocess.PrivilegedGitProfile, args ...string) error {
	for attempt := 0; ; attempt++ {
		result, err := p.privilegedGit(ctx, profile, append([]string{"update-index"}, args...)...)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		firstLine, _, _ := strings.Cut(result.Stderr, "\n")
		// Privileged Git uses the C locale and the exact pinned index path.
		// Do not infer contention from arbitrary diagnostics or another lock.
		lockDiagnostic := "fatal: Unable to create '" + profile.IndexFile + ".lock': File exists."
		// A joined transport, capture-limit, or cleanup error is not a plain
		// Git refusal, even when stderr also contains the expected lock line.
		if result.ExitCode != 128 || err.Error() != "exit status 128" || firstLine != lockDiagnostic {
			return commandError(err, result)
		}
		if attempt == 2 {
			return fmt.Errorf("%w: %w", ErrCandidateIndexBusy, commandError(err, result))
		}
		// A status refresh can briefly own index.lock. Retry only the refused
		// index write, never remove the lock or rerun the implementation model.
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (p GitProvider) candidatePathEntry(ctx context.Context, profile subprocess.PrivilegedGitProfile, path string) (string, string, bool, error) {
	absolute := filepath.Join(profile.WorkTree, filepath.FromSlash(path))
	if !pathInsideOrEqualLexical(absolute, profile.WorkTree) {
		return "", "", false, fmt.Errorf("candidate path escapes worktree: %q", path)
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("inspect candidate path %q: %w", path, err)
	}
	mode := "100644"
	var objectID string
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		mode = "120000"
		target, readErr := os.Readlink(absolute)
		if readErr != nil {
			return "", "", false, fmt.Errorf("read candidate symlink %q: %w", path, readErr)
		}
		objectID, err = p.hashTemporaryBlob(ctx, profile, []byte(target))
	case info.Mode().IsRegular():
		if info.Mode().Perm()&0o111 != 0 {
			mode = "100755"
		}
		objectID, err = p.privilegedScalar(ctx, profile, "hash-object", "-w", "--no-filters", "--", path)
	case info.IsDir():
		gitlink, gitlinkErr := p.candidatePathIsGitlink(ctx, profile, path)
		if gitlinkErr != nil {
			return "", "", false, gitlinkErr
		}
		if !gitlink {
			return "", "", false, nil
		}
		mode = "160000"
		submoduleProfile, deriveErr := derivePrivilegedGitProfile(absolute)
		if deriveErr != nil {
			return "", "", false, fmt.Errorf("candidate directory %q is not an initialized indexed submodule: %w", path, deriveErr)
		}
		objectID, err = p.privilegedScalar(ctx, submoduleProfile, "rev-parse", "--verify", "HEAD")
	default:
		return "", "", false, fmt.Errorf("candidate path %q has unsupported file type %s", path, info.Mode().Type())
	}
	if err != nil {
		return "", "", false, fmt.Errorf("write candidate blob for %q: %w", path, err)
	}
	if !validObjectID(objectID) {
		return "", "", false, fmt.Errorf("Git returned invalid object ID for candidate path %q", path)
	}
	return mode, objectID, true, nil
}

func (p GitProvider) candidatePathIsGitlink(ctx context.Context, profile subprocess.PrivilegedGitProfile, path string) (bool, error) {
	result, err := p.privilegedGit(ctx, profile, "ls-files", "--stage", "-v", "-z", "--", path)
	if err != nil {
		return false, fmt.Errorf("inspect candidate directory %q in the index: %w", path, commandError(err, result))
	}
	entries, err := parseSnapshotIndex(result.Stdout)
	if err != nil {
		return false, fmt.Errorf("parse candidate directory %q index entry: %w", path, err)
	}
	entry, found := entries[path]
	return found && strings.HasPrefix(entry, "mode=160000 "), nil
}

func (p GitProvider) verifyCandidateWorktree(ctx context.Context, profile subprocess.PrivilegedGitProfile) error {
	indexResult, err := p.privilegedGit(ctx, profile, "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return fmt.Errorf("read committed candidate index: %w", commandError(err, indexResult))
	}
	index, err := parseSnapshotIndex(indexResult.Stdout)
	if err != nil {
		return fmt.Errorf("parse committed candidate index: %w", err)
	}
	for _, path := range sortedSnapshotKeys(index) {
		mode, objectID, exists, entryErr := p.candidatePathEntry(ctx, profile, path)
		if entryErr != nil {
			return entryErr
		}
		if !exists {
			return fmt.Errorf("committed candidate path %q is missing from the worktree", path)
		}
		expected := fmt.Sprintf("mode=%s object=%s stage=0 assume-unchanged=false skip-worktree=false", mode, objectID)
		if index[path] != expected {
			return fmt.Errorf("committed candidate path %q does not match its literal worktree bytes and mode", path)
		}
	}
	untrackedResult, err := p.privilegedGit(ctx, profile, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return fmt.Errorf("verify committed candidate untracked paths: %w", commandError(err, untrackedResult))
	}
	if untrackedResult.Stdout != "" {
		return errors.New("committed candidate has untracked non-ignored worktree paths")
	}
	return nil
}

func (p GitProvider) hashTemporaryBlob(ctx context.Context, profile subprocess.PrivilegedGitProfile, content []byte) (string, error) {
	temporary, err := os.CreateTemp("", "runner-git-blob-*")
	if err != nil {
		return "", err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	return p.privilegedScalar(ctx, profile, "hash-object", "-w", "--no-filters", "--", name)
}

func (p GitProvider) privilegedGit(ctx context.Context, profile subprocess.PrivilegedGitProfile, args ...string) (subprocess.Result, error) {
	return subprocess.RunPrivilegedGit(ctx, p.run, profile, args, 30*time.Second)
}

func (p GitProvider) privilegedScalar(ctx context.Context, profile subprocess.PrivilegedGitProfile, args ...string) (string, error) {
	result, err := p.privilegedGit(ctx, profile, args...)
	if err != nil {
		return "", commandError(err, result)
	}
	value := strings.TrimSuffix(result.Stdout, "\n")
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("Git returned an empty or ambiguous scalar")
	}
	return value, nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (p GitProvider) rejectExecutableMergeConfig(ctx context.Context, profile subprocess.PrivilegedGitProfile) error {
	for _, path := range []string{filepath.Join(profile.CommonDirectory, "config"), filepath.Join(profile.GitDirectory, "config.worktree")} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect privileged merge configuration: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("privileged merge configuration %q is not a regular file", path)
		}
		result, configErr := p.privilegedGit(ctx, profile, "config", "--file", path, "--name-only", "--get-regexp", `^(filter\..*|merge\..*\.driver)$`)
		if configErr != nil && result.ExitCode != 1 {
			return fmt.Errorf("inspect executable merge configuration: %w", commandError(configErr, result))
		}
		if strings.TrimSpace(result.Stdout) != "" {
			return errors.New("privileged base refresh refuses configured filters or merge drivers")
		}
	}
	return nil
}
