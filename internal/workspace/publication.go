package workspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const publicationRecordVersion = 2

var privilegedGitMu sync.Mutex

var ErrPublicationBaseChanged = errors.New("publication base revision changed")

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
	parent, err := os.MkdirTemp("", "cortexium-runner-review-")
	if err != nil {
		return ReviewWorkspace{}, fmt.Errorf("create private review workspace: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		_ = os.RemoveAll(parent)
		return ReviewWorkspace{}, fmt.Errorf("protect private review workspace: %w", err)
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

type BaseRefresh struct {
	Updated       bool
	Conflicted    bool
	CommitOID     string
	ConflictFiles []string
	Summary       string
}

// PublicationPushPolicy controls whether publication may rewrite an existing
// remote branch. ExpectedRemoteOID is required for rebase-mode rewrites and is
// enforced with an exact Git force-with-lease comparison.
type PublicationPushPolicy struct {
	MergeMethod       string
	ExpectedRemoteOID string
}

// PublicationRecord is the immutable local authorization created only after
// Agent QA accepts an unchanged clean candidate.
type PublicationRecord struct {
	Version                int    `json:"version"`
	ItemID                 string `json:"item_id"`
	DelegatedContentDigest string `json:"delegated_content_digest"`
	CommitOID              string `json:"commit_oid"`
	TreeOID                string `json:"tree_oid"`
	ApprovedBaseRef        string `json:"approved_base_ref"`
	ApprovedBaseOID        string `json:"approved_base_oid"`
	Repository             string `json:"repository"`
	DestinationRef         string `json:"destination_ref"`
	AcceptanceSnapshot     string `json:"acceptance_snapshot"`
	AcceptanceReport       string `json:"acceptance_report"`
	AcceptanceComment      string `json:"acceptance_comment"`
}

// ConstructCandidate stages worktree bytes without Git clean filters, writes a
// commit without hooks or signing, and atomically advances only the task branch.
func (p GitProvider) ConstructCandidate(ctx context.Context, metadata Metadata, message string) (Candidate, error) {
	return p.ConstructCandidateForMergeMethod(ctx, metadata, message, config.MergeMethodMerge)
}

// ConstructCandidateForMergeMethod constructs a QA candidate compatible with
// the configured GitHub merge method. A divergent rebase-mode candidate is
// recorded directly on the authenticated base so its history remains linear.
func (p GitProvider) ConstructCandidateForMergeMethod(ctx context.Context, metadata Metadata, message, mergeMethod string) (Candidate, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	mergeMethod = config.NormalizeMergeMethod(mergeMethod)
	if !config.ValidMergeMethod(mergeMethod) {
		return Candidate{}, errors.New("candidate construction requires merge, rebase, or squash merge method")
	}
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
		if mergeMethod == config.MergeMethodRebase && needsBaseParent {
			commitArgs = append(commitArgs, "-p", metadata.BaseRevision)
		} else {
			commitArgs = append(commitArgs, "-p", headOID)
			if mergeHead != "" {
				commitArgs = append(commitArgs, "-p", mergeHead)
			} else if needsBaseParent {
				commitArgs = append(commitArgs, "-p", metadata.BaseRevision)
			}
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

func (p GitProvider) stageCandidatePath(ctx context.Context, profile subprocess.PrivilegedGitProfile, path string) error {
	mode, objectID, exists, err := p.candidatePathEntry(ctx, profile, path)
	if err != nil {
		return err
	}
	if !exists {
		if result, removeErr := p.privilegedGit(ctx, profile, "update-index", "--force-remove", "--", path); removeErr != nil {
			return fmt.Errorf("remove deleted candidate path %q: %w", path, commandError(removeErr, result))
		}
		return nil
	}
	if result, updateErr := p.privilegedGit(ctx, profile, "update-index", "--add", "--cacheinfo", mode, objectID, path); updateErr != nil {
		return fmt.Errorf("stage candidate path %q: %w", path, commandError(updateErr, result))
	}
	return nil
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

// RecordPublicationAcceptance creates or reuses only the exact immutable tuple
// for the unchanged QA snapshot. A commit-key collision with any different
// identity is rejected.
func (p GitProvider) RecordPublicationAcceptance(ctx context.Context, metadata Metadata, accepted Snapshot, report, comment string) (PublicationRecord, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if strings.TrimSpace(report) == "" || strings.TrimSpace(comment) == "" {
		return PublicationRecord{}, errors.New("publication acceptance requires the reviewer report and durable comment")
	}
	record, path, err := p.validatedPublicationAcceptance(ctx, metadata, accepted, report, comment, true)
	if err != nil {
		return PublicationRecord{}, err
	}
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return PublicationRecord{}, err
	}
	if err := securefs.WriteFileExclusive(path, content.Bytes(), 0o600); err == nil {
		return record, nil
	} else if !errors.Is(err, os.ErrExist) {
		return PublicationRecord{}, fmt.Errorf("write publication record exclusively: %w", err)
	}
	existing, err := readPublicationRecord(path)
	if err != nil {
		return PublicationRecord{}, fmt.Errorf("read existing publication record: %w", err)
	}
	if existing != record {
		return PublicationRecord{}, fmt.Errorf("publication commit %s is already bound to a different immutable tuple", record.CommitOID)
	}
	return existing, nil
}

// LoadPublicationAcceptance returns an existing exact acceptance without
// creating one. It revalidates the live candidate before allowing Runner to
// skip another reviewer invocation after an interrupted publication.
func (p GitProvider) LoadPublicationAcceptance(ctx context.Context, metadata Metadata, accepted Snapshot) (PublicationRecord, bool, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	record, path, err := p.validatedPublicationAcceptance(ctx, metadata, accepted, "", "", false)
	if err != nil {
		return PublicationRecord{}, false, err
	}
	existing, err := readPublicationRecord(path)
	if errors.Is(err, os.ErrNotExist) {
		return PublicationRecord{}, false, nil
	}
	if err != nil {
		return PublicationRecord{}, false, fmt.Errorf("read existing publication record: %w", err)
	}
	record.AcceptanceReport = existing.AcceptanceReport
	record.AcceptanceComment = existing.AcceptanceComment
	if existing != record {
		return PublicationRecord{}, false, errors.New("existing publication acceptance does not match the current approved candidate")
	}
	return existing, true, nil
}

// HasPriorPublicationAcceptance authenticates a remote branch head that Runner
// published for the same item, delegated content, repository, and destination.
// Unlike LoadPublicationAcceptance, it deliberately does not bind the old
// record to the current candidate or base: rebase-mode recovery has already
// replaced that local history and uses this proof only as an exact lease
// anchor for the still-published predecessor.
func (p GitProvider) HasPriorPublicationAcceptance(_ context.Context, metadata Metadata, commitOID string) (bool, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	if err := validateCandidateMetadata(metadata); err != nil {
		return false, err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return false, err
	}
	return priorPublicationAcceptance(metadata, commitOID)
}

func priorPublicationAcceptance(metadata Metadata, commitOID string) (bool, error) {
	commitOID = strings.TrimSpace(commitOID)
	if !validObjectID(commitOID) {
		return false, nil
	}
	worktreeRoot := filepath.Dir(metadata.Identity.WorktreePath)
	if err := securefs.ValidatePrivateDir(worktreeRoot); err != nil {
		return false, fmt.Errorf("validate private publication state root: %w", err)
	}
	record, err := readPublicationRecord(filepath.Join(worktreeRoot, ".runner-state", "publications", commitOID+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read prior publication acceptance: %w", err)
	}
	expectedDestination := "refs/heads/" + metadata.BranchName
	if record.ItemID != metadata.Identity.ItemID || record.DelegatedContentDigest != metadata.Identity.DelegatedContentDigest ||
		record.CommitOID != commitOID || record.Repository != metadata.Identity.Repository || record.DestinationRef != expectedDestination ||
		record.ApprovedBaseRef != metadata.BaseRef || !validObjectID(record.ApprovedBaseOID) {
		return false, nil
	}
	return true, nil
}

func (p GitProvider) validatedPublicationAcceptance(ctx context.Context, metadata Metadata, accepted Snapshot, report, comment string, createRecordRoot bool) (PublicationRecord, string, error) {
	if err := validateCandidateMetadata(metadata); err != nil {
		return PublicationRecord{}, "", err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return PublicationRecord{}, "", err
	}
	if accepted.Fingerprint == "" || !accepted.Clean || !validObjectID(accepted.Head) || !validObjectID(accepted.Tree) || accepted.Branch != metadata.BranchName {
		return PublicationRecord{}, "", errors.New("accepted QA snapshot is not a clean candidate with valid matching HEAD, tree, and branch")
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return PublicationRecord{}, "", err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return PublicationRecord{}, "", err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return PublicationRecord{}, "", err
	}
	current, err := CaptureCheckoutSnapshotStateWithLimits(ctx, p.run, metadata.WorktreePath, 30*time.Second, p.limits)
	if err != nil {
		return PublicationRecord{}, "", fmt.Errorf("recapture accepted candidate: %w", err)
	}
	if !current.Clean || current.Fingerprint != accepted.Fingerprint || current.Head != accepted.Head || current.Tree != accepted.Tree || current.Branch != accepted.Branch {
		return PublicationRecord{}, "", errors.New("accepted candidate snapshot, HEAD, tree, or branch changed before publication authorization")
	}
	verifiedProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return PublicationRecord{}, "", fmt.Errorf("rederive accepted candidate administration: %w", err)
	}
	if verifiedProfile != profile {
		return PublicationRecord{}, "", errors.New("accepted candidate Git administration changed before publication authorization")
	}
	if err := rejectObjectRedirection(verifiedProfile); err != nil {
		return PublicationRecord{}, "", err
	}
	if err := p.rejectReplacementObjects(ctx, verifiedProfile); err != nil {
		return PublicationRecord{}, "", err
	}
	actualHead, err := p.privilegedScalar(ctx, verifiedProfile, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return PublicationRecord{}, "", fmt.Errorf("verify accepted candidate HEAD: %w", err)
	}
	actualTree, err := p.privilegedScalar(ctx, verifiedProfile, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return PublicationRecord{}, "", fmt.Errorf("verify accepted candidate tree: %w", err)
	}
	actualBranch, err := p.privilegedScalar(ctx, verifiedProfile, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return PublicationRecord{}, "", fmt.Errorf("verify accepted candidate branch: %w", err)
	}
	if actualHead != accepted.Head || actualTree != accepted.Tree || actualBranch != "refs/heads/"+accepted.Branch {
		return PublicationRecord{}, "", errors.New("accepted snapshot does not identify the literal candidate commit, tree, and branch")
	}
	if result, ancestorErr := p.privilegedGit(ctx, verifiedProfile, "merge-base", "--is-ancestor", metadata.BaseRevision, accepted.Head); ancestorErr != nil || result.ExitCode != 0 {
		return PublicationRecord{}, "", errors.New("accepted candidate is not descended from the approved base")
	}
	report = strings.TrimSpace(report)
	comment = strings.TrimSpace(comment)
	if (report == "") != (comment == "") || strings.ContainsAny(report+comment, "\x00") {
		return PublicationRecord{}, "", errors.New("publication acceptance report and comment must both be present or both be omitted and cannot contain NUL")
	}
	record := PublicationRecord{
		Version: publicationRecordVersion, ItemID: metadata.Identity.ItemID,
		DelegatedContentDigest: metadata.Identity.DelegatedContentDigest,
		CommitOID:              accepted.Head, TreeOID: accepted.Tree,
		ApprovedBaseRef: metadata.BaseRef, ApprovedBaseOID: metadata.BaseRevision,
		Repository: metadata.Identity.Repository, DestinationRef: "refs/heads/" + metadata.BranchName,
		AcceptanceSnapshot: accepted.Fingerprint, AcceptanceReport: report, AcceptanceComment: comment,
	}
	worktreeRoot := filepath.Dir(metadata.Identity.WorktreePath)
	if err := securefs.ValidatePrivateDir(worktreeRoot); err != nil {
		return PublicationRecord{}, "", fmt.Errorf("validate private publication state root: %w", err)
	}
	recordRoot := filepath.Join(worktreeRoot, ".runner-state", "publications")
	if createRecordRoot {
		if err := securefs.EnsurePrivateDir(recordRoot); err != nil {
			return PublicationRecord{}, "", fmt.Errorf("create private publication record directory: %w", err)
		}
	}
	return record, filepath.Join(recordRoot, record.CommitOID+".json"), nil
}

func readPublicationRecord(path string) (PublicationRecord, error) {
	content, _, state, err := securefs.ReadFile(path, 64*1024)
	if err != nil {
		return PublicationRecord{}, err
	}
	if !state.Exists {
		return PublicationRecord{}, os.ErrNotExist
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var record PublicationRecord
	if err := decoder.Decode(&record); err != nil {
		return PublicationRecord{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return PublicationRecord{}, errors.New("publication record contains trailing data")
	}
	if record.Version != publicationRecordVersion || !validObjectID(record.CommitOID) || !validObjectID(record.TreeOID) ||
		strings.TrimSpace(record.AcceptanceReport) == "" || strings.TrimSpace(record.AcceptanceComment) == "" || strings.ContainsAny(record.AcceptanceReport+record.AcceptanceComment, "\x00") {
		return PublicationRecord{}, fmt.Errorf("invalid publication record version or object identity %s", strconv.Quote(record.CommitOID))
	}
	return record, nil
}

// PublishAccepted validates and pushes only an immutable QA publication tuple.
// The authority callback runs after the final base/tree checks and immediately
// before the exact OID-to-ref push.
func (p GitProvider) PublishAccepted(ctx context.Context, metadata Metadata, record PublicationRecord, remoteName, baseBranch string, pushPolicy PublicationPushPolicy, refreshAuthority func() error) error {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	pushPolicy.MergeMethod = config.NormalizeMergeMethod(pushPolicy.MergeMethod)
	pushPolicy.ExpectedRemoteOID = strings.TrimSpace(pushPolicy.ExpectedRemoteOID)
	if !config.ValidMergeMethod(pushPolicy.MergeMethod) {
		return errors.New("publication requires merge, rebase, or squash merge method")
	}
	if pushPolicy.ExpectedRemoteOID != "" && (pushPolicy.MergeMethod != config.MergeMethodRebase || !validObjectID(pushPolicy.ExpectedRemoteOID)) {
		return errors.New("publication remote lease is only valid for a rebase-mode rewrite with an exact expected commit")
	}
	if err := validateCandidateMetadata(metadata); err != nil {
		return err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return err
	}
	remoteName = strings.TrimSpace(remoteName)
	baseBranch = strings.TrimSpace(baseBranch)
	if remoteName == "" || baseBranch == "" || strings.ContainsAny(remoteName+baseBranch, "\x00\r\n") {
		return errors.New("publication requires an explicit Git remote and base branch")
	}
	expectedBaseRef := "refs/remotes/" + remoteName + "/" + baseBranch
	expectedDestination := "refs/heads/" + metadata.BranchName
	if record.Version != publicationRecordVersion || record.ItemID != metadata.Identity.ItemID ||
		record.DelegatedContentDigest != metadata.Identity.DelegatedContentDigest || record.ApprovedBaseRef != metadata.BaseRef ||
		record.ApprovedBaseOID != metadata.BaseRevision || record.Repository != metadata.Identity.Repository ||
		record.DestinationRef != expectedDestination || record.ApprovedBaseRef != expectedBaseRef ||
		!validObjectID(record.CommitOID) || !validObjectID(record.TreeOID) || record.AcceptanceSnapshot == "" ||
		strings.TrimSpace(record.AcceptanceReport) == "" || strings.TrimSpace(record.AcceptanceComment) == "" {
		return errors.New("publication tuple does not match the approved workspace, base, repository, or destination")
	}
	if !config.ValidRepositoryName(record.Repository) {
		return errors.New("publication tuple repository must use owner/repository format")
	}
	recordRoot := filepath.Join(filepath.Dir(metadata.Identity.WorktreePath), ".runner-state", "publications")
	persisted, err := readPublicationRecord(filepath.Join(recordRoot, record.CommitOID+".json"))
	if err != nil {
		return fmt.Errorf("read immutable publication tuple: %w", err)
	}
	if persisted != record {
		return errors.New("provided publication tuple differs from its immutable private record")
	}
	current, err := CaptureCheckoutSnapshotStateWithLimits(ctx, p.run, metadata.WorktreePath, 30*time.Second, p.limits)
	if err != nil {
		return fmt.Errorf("recapture publication candidate: %w", err)
	}
	if !current.Clean || current.Fingerprint != record.AcceptanceSnapshot || current.Head != record.CommitOID || current.Tree != record.TreeOID || current.Branch != metadata.BranchName {
		return errors.New("publication candidate no longer matches the accepted QA snapshot and tuple")
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return err
	}
	commitOID, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", record.CommitOID+"^{commit}")
	if err != nil || commitOID != record.CommitOID {
		return errors.New("accepted publication commit is missing or resolves to a different object")
	}
	treeOID, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", record.CommitOID+"^{tree}")
	if err != nil || treeOID != record.TreeOID {
		return errors.New("accepted publication commit resolves to a different tree")
	}
	if result, ancestorErr := p.privilegedGit(ctx, profile, "merge-base", "--is-ancestor", record.ApprovedBaseOID, record.CommitOID); ancestorErr != nil || result.ExitCode != 0 {
		return errors.New("accepted publication commit is no longer bound to the approved base")
	}
	repositoryURL := "https://github.com/" + record.Repository + ".git"
	refspec := "+refs/heads/" + baseBranch + ":" + record.ApprovedBaseRef
	if result, fetchErr := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", repositoryURL, refspec}, 2*time.Minute); fetchErr != nil {
		return fmt.Errorf("fetch publication base: %w", commandError(fetchErr, result))
	}
	currentBase, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", record.ApprovedBaseRef)
	if err != nil {
		return fmt.Errorf("resolve refreshed publication base: %w", err)
	}
	if currentBase != record.ApprovedBaseOID {
		return fmt.Errorf("%w: accepted %s, fetched %s", ErrPublicationBaseChanged, record.ApprovedBaseOID, currentBase)
	}
	remoteAlreadyAccepted := false
	if pushPolicy.ExpectedRemoteOID != "" {
		remoteTrackingRef := "refs/runner/publication-destination"
		remoteRefspec := "+" + record.DestinationRef + ":" + remoteTrackingRef
		remoteResult, remoteErr := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", repositoryURL, remoteRefspec}, 2*time.Minute)
		if remoteErr != nil {
			return fmt.Errorf("inspect publication destination: %w", commandError(remoteErr, remoteResult))
		}
		remoteOID, resolveErr := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", remoteTrackingRef)
		if resolveErr != nil || !validObjectID(remoteOID) {
			return errors.New("resolve publication destination identity")
		}
		if remoteOID == record.CommitOID {
			remoteAlreadyAccepted = true
		} else if remoteOID != pushPolicy.ExpectedRemoteOID {
			priorAccepted, priorErr := priorPublicationAcceptance(metadata, remoteOID)
			if priorErr != nil {
				return priorErr
			}
			if !priorAccepted {
				return fmt.Errorf("publication destination changed externally: expected %s, found %s", pushPolicy.ExpectedRemoteOID, remoteOID)
			}
			// A previous Project transition may have failed after this exact
			// accepted commit was published. Its immutable private record is a
			// stronger lease anchor than the stale Project QA field.
			pushPolicy.ExpectedRemoteOID = remoteOID
		}
	}
	resolvedTree, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", record.CommitOID+"^{tree}")
	if err != nil || resolvedTree != record.TreeOID {
		return errors.New("accepted publication tree changed immediately before push")
	}
	verifiedProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return fmt.Errorf("rederive publication Git administration: %w", err)
	}
	if verifiedProfile != profile {
		return errors.New("publication Git administration changed before push")
	}
	if refreshAuthority == nil {
		return errors.New("publication requires a final authority refresh")
	}
	if err := refreshAuthority(); err != nil {
		return err
	}
	if remoteAlreadyAccepted {
		return nil
	}
	pushRefspec := record.CommitOID + ":" + record.DestinationRef
	pushArgs := []string{"push", "--porcelain", "--no-verify"}
	if pushPolicy.ExpectedRemoteOID != "" {
		pushArgs = append(pushArgs, "--force-with-lease="+record.DestinationRef+":"+pushPolicy.ExpectedRemoteOID)
	}
	pushArgs = append(pushArgs, repositoryURL, pushRefspec)
	if result, pushErr := subprocess.RunPrivilegedGitNetwork(ctx, p.run, verifiedProfile, pushArgs, 2*time.Minute); pushErr != nil {
		return fmt.Errorf("push accepted publication commit: %w", commandError(pushErr, result))
	}
	return nil
}

// RefreshBase updates only the local candidate workspace under the privileged
// Git profile. Its resulting tree has no publication authority and must pass
// the normal implementation, integrity, and QA path before publication.
func (p GitProvider) RefreshBase(ctx context.Context, metadata Metadata, remoteName, baseBranch string) (BaseRefresh, error) {
	return p.RefreshBaseForMergeMethod(ctx, metadata, remoteName, baseBranch, config.MergeMethodMerge)
}

func (p GitProvider) RefreshBaseForMergeMethod(ctx context.Context, metadata Metadata, remoteName, baseBranch, mergeMethod string) (BaseRefresh, error) {
	return p.refreshBase(ctx, metadata, remoteName, baseBranch, mergeMethod, true)
}

// RefreshLocalBase updates a candidate that has not been published yet. Unlike
// RefreshBase, it does not require the task branch to exist on the remote.
func (p GitProvider) RefreshLocalBase(ctx context.Context, metadata Metadata, remoteName, baseBranch string) (BaseRefresh, error) {
	return p.RefreshLocalBaseForMergeMethod(ctx, metadata, remoteName, baseBranch, config.MergeMethodMerge)
}

func (p GitProvider) RefreshLocalBaseForMergeMethod(ctx context.Context, metadata Metadata, remoteName, baseBranch, mergeMethod string) (BaseRefresh, error) {
	return p.refreshBase(ctx, metadata, remoteName, baseBranch, mergeMethod, false)
}

func (p GitProvider) refreshBase(ctx context.Context, metadata Metadata, remoteName, baseBranch, mergeMethod string, fetchBranch bool) (BaseRefresh, error) {
	privilegedGitMu.Lock()
	defer privilegedGitMu.Unlock()
	mergeMethod = config.NormalizeMergeMethod(mergeMethod)
	if !config.ValidMergeMethod(mergeMethod) {
		return BaseRefresh{}, errors.New("base refresh requires merge, rebase, or squash merge method")
	}
	if err := validateCandidateMetadata(metadata); err != nil {
		return BaseRefresh{}, err
	}
	if err := validateRecordedIdentity(metadata); err != nil {
		return BaseRefresh{}, err
	}
	remoteName = strings.TrimSpace(remoteName)
	baseBranch = strings.TrimSpace(baseBranch)
	if metadata.BaseRef != "refs/remotes/"+remoteName+"/"+baseBranch || !config.ValidRepositoryName(metadata.Identity.Repository) {
		return BaseRefresh{}, errors.New("base refresh does not match the approved workspace base and repository")
	}
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		return BaseRefresh{}, err
	}
	if err := rejectObjectRedirection(profile); err != nil {
		return BaseRefresh{}, err
	}
	if err := p.rejectReplacementObjects(ctx, profile); err != nil {
		return BaseRefresh{}, err
	}
	if err := p.rejectExecutableMergeConfig(ctx, profile); err != nil {
		return BaseRefresh{}, err
	}
	status, err := p.privilegedGit(ctx, profile, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return BaseRefresh{}, fmt.Errorf("inspect base-refresh workspace: %w", commandError(err, status))
	}
	if strings.TrimSpace(status.Stdout) != "" {
		return BaseRefresh{}, errors.New("cannot refresh a candidate with uncommitted changes")
	}
	repositoryURL := "https://github.com/" + metadata.Identity.Repository + ".git"
	baseRefspec := "+refs/heads/" + baseBranch + ":" + metadata.BaseRef
	branchRef := "refs/remotes/" + remoteName + "/" + metadata.BranchName
	args := []string{"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", repositoryURL, baseRefspec}
	if fetchBranch {
		args = append(args, "+refs/heads/"+metadata.BranchName+":"+branchRef)
	}
	if result, fetchErr := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, args, 2*time.Minute); fetchErr != nil {
		return BaseRefresh{}, fmt.Errorf("fetch base-refresh refs: %w", commandError(fetchErr, result))
	}
	if fetchBranch {
		if result, mergeErr := p.privilegedGit(ctx, profile, "merge", "--ff-only", branchRef); mergeErr != nil {
			return BaseRefresh{}, fmt.Errorf("fast-forward local candidate branch: %w", commandError(mergeErr, result))
		}
	}
	currentBase, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", metadata.BaseRef)
	if err != nil || !validObjectID(currentBase) {
		return BaseRefresh{}, errors.New("resolve refreshed base revision")
	}
	advanceIdentity := func() error {
		if currentBase == metadata.BaseRevision {
			return nil
		}
		replacement := metadata.Identity
		replacement.BaseRevision = currentBase
		worktreeRoot := filepath.Dir(metadata.Identity.WorktreePath)
		workID := filepath.Base(metadata.Identity.WorktreePath)
		if err := replaceIdentity(activeIdentityPath(worktreeRoot, workID), metadata.Identity, replacement); err != nil {
			return fmt.Errorf("advance private workspace base identity: %w", err)
		}
		return nil
	}
	if result, ancestorErr := p.privilegedGit(ctx, profile, "merge-base", "--is-ancestor", metadata.BaseRef, "HEAD"); ancestorErr == nil && result.ExitCode == 0 {
		if err := advanceIdentity(); err != nil {
			return BaseRefresh{}, err
		}
		return BaseRefresh{Updated: currentBase != metadata.BaseRevision, CommitOID: currentBase, Summary: "Pull request branch already contains the current base branch."}, nil
	}
	mergeArgs := []string{"merge", "--no-edit", metadata.BaseRef}
	if mergeMethod == config.MergeMethodRebase {
		mergeArgs = []string{"merge", "--no-commit", "--no-ff", metadata.BaseRef}
	}
	mergeResult, mergeErr := p.privilegedGit(ctx, profile, mergeArgs...)
	if mergeErr != nil {
		conflicts, _ := p.privilegedGit(ctx, profile, "diff", "--name-only", "--diff-filter=U")
		files := compactPublicationLines(conflicts.Stdout)
		if len(files) == 0 {
			return BaseRefresh{}, fmt.Errorf("merge refreshed base locally: %w", commandError(mergeErr, mergeResult))
		}
		if err := advanceIdentity(); err != nil {
			return BaseRefresh{}, err
		}
		return BaseRefresh{Conflicted: true, ConflictFiles: files, Summary: "Base branch update has merge conflicts: " + strings.Join(files, ", ")}, nil
	}
	commitOID, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", "HEAD")
	if err != nil || !validObjectID(commitOID) {
		return BaseRefresh{}, errors.New("resolve local base-refresh commit")
	}
	if mergeMethod == config.MergeMethodRebase {
		treeOID, treeErr := p.privilegedScalar(ctx, profile, "write-tree")
		if treeErr != nil {
			return BaseRefresh{}, fmt.Errorf("write rebase-compatible refresh tree: %w", treeErr)
		}
		rebasedOID, commitErr := p.privilegedScalar(ctx, profile, "commit-tree", treeOID, "-p", currentBase, "--no-gpg-sign", "-m", "Refresh candidate onto current base")
		if commitErr != nil {
			return BaseRefresh{}, fmt.Errorf("write rebase-compatible refresh commit: %w", commitErr)
		}
		if result, updateErr := p.privilegedGit(ctx, profile, "update-ref", "refs/heads/"+metadata.BranchName, rebasedOID, commitOID); updateErr != nil {
			return BaseRefresh{}, fmt.Errorf("advance rebase-compatible refresh branch: %w", commandError(updateErr, result))
		}
		if result, resetErr := p.privilegedGit(ctx, profile, "reset", "--mixed", "HEAD"); resetErr != nil {
			return BaseRefresh{}, fmt.Errorf("clear rebase-compatible refresh merge state: %w", commandError(resetErr, result))
		}
		commitOID = rebasedOID
	}
	verifiedProfile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil || verifiedProfile != profile {
		return BaseRefresh{}, errors.New("base-refresh Git administration changed during update")
	}
	if err := advanceIdentity(); err != nil {
		return BaseRefresh{}, err
	}
	return BaseRefresh{Updated: true, CommitOID: commitOID, Summary: defaultPublicationSummary(strings.TrimSpace(mergeResult.Stdout), "Base branch update remains local pending implementation and QA.")}, nil
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

func compactPublicationLines(value string) []string {
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func defaultPublicationSummary(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
