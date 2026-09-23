package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

// PlanningSource identifies the exact destination source used by a planner.
// Repository is the lowercase GitHub owner/repository identity, not a URL.
type PlanningSource struct {
	Repository        string `json:"repository"`
	DestinationBranch string `json:"destination_branch"`
	CommitOID         string `json:"commit_oid"`
	TreeOID           string `json:"tree_oid"`
}

func (source PlanningSource) Validate() error {
	if !validPlanningRepository(source.Repository) || source.Repository != strings.ToLower(source.Repository) {
		return errors.New("planning source requires a canonical GitHub owner/repository")
	}
	if !validPlanningBranch(source.DestinationBranch) {
		return errors.New("planning source requires an exact destination branch name")
	}
	if !validObjectID(source.CommitOID) || !validObjectID(source.TreeOID) || len(source.CommitOID) != len(source.TreeOID) ||
		strings.Trim(source.CommitOID, "0") == "" || strings.Trim(source.TreeOID, "0") == "" {
		return errors.New("planning source requires full commit and tree object IDs of the same format")
	}
	return nil
}

func validPlanningRepository(repository string) bool {
	if !config.ValidRepositoryName(repository) {
		return false
	}
	parts := strings.Split(repository, "/")
	if parts[1] == "." || parts[1] == ".." {
		return false
	}
	for i, part := range parts {
		for _, c := range part {
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || i == 1 && (c == '_' || c == '.') {
				continue
			}
			return false
		}
	}
	return true
}

func validPlanningBranch(branch string) bool {
	if branch == "" || branch == "@" || branch == "HEAD" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "refs/") ||
		strings.ContainsAny(branch, " ~^:?*[\\") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.HasSuffix(branch, ".") {
		return false
	}
	for _, c := range branch {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(branch, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

// PlanningWorkspace owns a private detached checkout. Source and Path are
// checked against private captured state; changing either invalidates Verify.
// Callers must retain it while subprocess cleanup is unresolved.
type PlanningWorkspace struct {
	Path   string
	Source PlanningSource

	checkout    ReviewWorkspace
	source      PlanningSource
	profile     subprocess.PrivilegedGitProfile
	parentInfo  os.FileInfo
	directories map[string]os.FileInfo
	fingerprint string
	pinRef      string
}

// PreparePlanningWorkspace fetches only the configured destination and
// materializes its pinned commit without changing the operator checkout.
// On an unresolved preparation/cleanup failure it returns the retained handle
// alongside the error so the caller can preserve and later clean it safely.
func (p GitProvider) PreparePlanningWorkspace(ctx context.Context, workingDir, remoteName, destinationBranch, repository string) (workspace PlanningWorkspace, err error) {
	source, profile, ref, err := p.fetchPlanningSource(ctx, workingDir, remoteName, destinationBranch, repository)
	if err != nil {
		return workspace, err
	}
	defer func() {
		var unresolved *subprocess.CleanupError
		if !errors.As(err, &unresolved) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if releaseErr := p.releasePlanningRef(cleanupCtx, profile, ref, source.CommitOID); releaseErr != nil {
				err = errors.Join(err, releaseErr)
			} else {
				workspace.pinRef = ""
			}
		}
	}()
	checkout, prepareErr := p.prepareDetachedWorkspace(ctx, profile, Candidate{CommitOID: source.CommitOID, TreeOID: source.TreeOID})
	if checkout.Path == "" {
		return workspace, prepareErr
	}
	workspace = PlanningWorkspace{Path: checkout.Path, Source: source, checkout: checkout, source: source, pinRef: ref}
	workspace.parentInfo, err = os.Lstat(checkout.parent)
	if err != nil {
		return workspace, errors.Join(prepareErr, err)
	}
	if prepareErr != nil {
		return workspace, prepareErr
	}
	workspace.profile, err = derivePrivilegedGitProfile(checkout.Path)
	if err == nil {
		workspace.directories = make(map[string]os.FileInfo)
		for _, path := range []string{workspace.profile.WorkTree, workspace.profile.GitDirectory, workspace.profile.CommonDirectory, workspace.profile.ObjectDirectory} {
			workspace.directories[path], err = os.Lstat(path)
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		var snapshot Snapshot
		snapshot, err = workspace.capture(ctx)
		workspace.fingerprint = snapshot.Fingerprint
	}
	if err != nil {
		var unresolved *subprocess.CleanupError
		if !errors.As(err, &unresolved) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if cleanupErr := workspace.Cleanup(cleanupCtx); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			} else {
				workspace = PlanningWorkspace{}
			}
		}
	}
	return workspace, err
}

// ObservePlanningSource freshly observes the same destination without creating
// a private worktree or invoking a model. No remote-tracking refs are updated.
func (p GitProvider) ObservePlanningSource(ctx context.Context, workingDir, remoteName, destinationBranch, repository string) (PlanningSource, error) {
	source, profile, ref, err := p.fetchPlanningSource(ctx, workingDir, remoteName, destinationBranch, repository)
	if err != nil {
		return PlanningSource{}, err
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := p.releasePlanningRef(cleanupCtx, profile, ref, source.CommitOID); err != nil {
		return PlanningSource{}, err
	}
	return source, nil
}

func (p GitProvider) fetchPlanningSource(ctx context.Context, workingDir, remoteName, destinationBranch, repository string) (source PlanningSource, profile subprocess.PrivilegedGitProfile, ref string, err error) {
	repository = strings.ToLower(repository)
	if !validPlanningRepository(repository) || !validPlanningBranch(destinationBranch) || remoteName == "" ||
		strings.HasPrefix(remoteName, "-") || strings.ContainsAny(remoteName, " \t\r\n\x00/\\") {
		return source, profile, "", errors.New("planning requires an explicit repository, configured remote and destination branch")
	}
	profile, err = derivePrivilegedGitProfile(workingDir)
	if err != nil {
		return source, profile, "", err
	}
	if err = rejectObjectRedirection(profile); err != nil {
		return source, profile, "", err
	}
	if err = p.rejectReplacementObjects(ctx, profile); err != nil {
		return source, profile, "", err
	}
	if err = p.verifyPlanningRemote(ctx, profile, remoteName, repository); err != nil {
		return source, profile, "", err
	}
	ref = "refs/runner/planning/" + rand.Text()
	defer func() {
		var unresolved *subprocess.CleanupError
		if err != nil && !errors.As(err, &unresolved) {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			err = errors.Join(err, p.releasePlanningRef(cleanupCtx, profile, ref, ""))
		}
	}()
	result, fetchErr := subprocess.RunPrivilegedGitNetwork(ctx, p.run, profile, []string{
		"fetch", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance",
		"https://github.com/" + repository + ".git", "+refs/heads/" + destinationBranch + ":" + ref,
	}, 2*time.Minute)
	if fetchErr != nil {
		return source, profile, ref, fmt.Errorf("fetch planning destination: %w", commandError(fetchErr, result))
	}
	commit, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return source, profile, ref, fmt.Errorf("resolve fetched planning commit: %w", err)
	}
	tree, err := p.privilegedScalar(ctx, profile, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return source, profile, ref, fmt.Errorf("resolve fetched planning tree: %w", err)
	}
	source = PlanningSource{Repository: repository, DestinationBranch: destinationBranch, CommitOID: commit, TreeOID: tree}
	if err = source.Validate(); err != nil {
		return source, profile, ref, err
	}
	err = p.verifyPlanningRemote(ctx, profile, remoteName, repository)
	return source, profile, ref, err
}

func (p GitProvider) releasePlanningRef(ctx context.Context, profile subprocess.PrivilegedGitProfile, ref, expected string) error {
	args := []string{"update-ref", "-d", ref}
	if expected != "" {
		args = append(args, expected)
	}
	result, err := p.privilegedGit(ctx, profile, args...)
	if err != nil {
		return fmt.Errorf("release private planning ref: %w", commandError(err, result))
	}
	return nil
}

func (p GitProvider) verifyPlanningRemote(ctx context.Context, profile subprocess.PrivilegedGitProfile, remote, repository string) error {
	path := filepath.Join(profile.CommonDirectory, "config")
	before, _, _, err := securefs.ReadFile(path, gitControlFileLimit)
	if err != nil {
		return fmt.Errorf("read planning remote configuration: %w", err)
	}
	result, err := p.privilegedGit(ctx, profile, "config", "--file", path, "--no-includes", "--null", "--get-all", "remote."+remote+".url")
	if err != nil {
		return fmt.Errorf("read configured planning remote: %w", commandError(err, result))
	}
	after, _, _, err := securefs.ReadFile(path, gitControlFileLimit)
	if err != nil || !bytes.Equal(before, after) {
		return errors.Join(errors.New("planning remote configuration changed during validation"), err)
	}
	if !strings.HasSuffix(result.Stdout, "\x00") || strings.Count(result.Stdout, "\x00") != 1 {
		return errors.New("planning requires exactly one configured remote URL")
	}
	remoteURL := strings.TrimSuffix(result.Stdout, "\x00")
	var identity string
	if strings.HasPrefix(remoteURL, "git@github.com:") {
		identity = strings.TrimPrefix(remoteURL, "git@github.com:")
	} else {
		parsed, err := url.Parse(remoteURL)
		if err != nil || !strings.EqualFold(parsed.Host, "github.com") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
			return errors.New("planning remote is not a supported GitHub URL")
		}
		if parsed.Scheme == "https" && parsed.User == nil || parsed.Scheme == "ssh" && parsed.User != nil && parsed.User.String() == "git" {
			identity = strings.TrimPrefix(parsed.Path, "/")
		}
	}
	identity = strings.TrimSuffix(identity, ".git")
	if !validPlanningRepository(identity) || !strings.EqualFold(identity, repository) {
		return errors.New("configured planning remote does not match the requested GitHub repository")
	}
	return nil
}

// planningSnapshotRunner keeps the existing bounded checkout/control snapshot
// machinery inside the same config-free Git boundary as materialization.
type planningSnapshotRunner struct {
	provider GitProvider
	profile  subprocess.PrivilegedGitProfile
}

func (run planningSnapshotRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command != "git" || dir != run.profile.WorkTree {
		return subprocess.Result{}, errors.New("planning snapshot command escaped the pinned checkout")
	}
	if len(args) > 0 && args[0] == "--no-optional-locks" {
		args = args[1:]
	}
	return subprocess.RunPrivilegedGit(ctx, run.provider.run, run.profile, args, timeout)
}

func (workspace PlanningWorkspace) capture(ctx context.Context) (Snapshot, error) {
	p := workspace.checkout.provider
	if err := rejectObjectRedirection(workspace.profile); err != nil {
		return Snapshot{}, err
	}
	// Include ignored files: a planner's source is exactly the fetched tree.
	status, err := p.privilegedGit(ctx, workspace.profile, "status", "--porcelain", "--untracked-files=all", "--ignored=matching")
	if err != nil || status.Stdout != "" {
		return Snapshot{}, errors.Join(errors.New("planning workspace is not the exact clean checkout"), err)
	}
	snapshot, err := CaptureCheckoutSnapshotStateWithLimits(ctx, planningSnapshotRunner{p, workspace.profile}, workspace.Path, 30*time.Second, p.limits)
	if err != nil {
		return Snapshot{}, fmt.Errorf("capture planning workspace provenance: %w", err)
	}
	if !snapshot.Clean || snapshot.Head != workspace.source.CommitOID || snapshot.Tree != workspace.source.TreeOID || snapshot.Branch != "HEAD" {
		return Snapshot{}, errors.New("planning checkout no longer matches its detached source commit and tree")
	}
	return snapshot, nil
}

func (workspace PlanningWorkspace) Verify(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if workspace.Path != workspace.checkout.Path || workspace.Source != workspace.source || workspace.fingerprint == "" {
		return errors.New("planning workspace source or path differs from its private provenance")
	}
	if err := workspace.verifyOwnedPath(); err != nil {
		return err
	}
	for path, expected := range workspace.directories {
		actual, err := os.Lstat(path)
		if err != nil || !os.SameFile(expected, actual) {
			return errors.Join(errors.New("planning Git directory identity changed"), err)
		}
	}
	profile, err := derivePrivilegedGitProfile(workspace.Path)
	if err != nil || profile != workspace.profile {
		return errors.Join(errors.New("planning Git administration changed"), err)
	}
	snapshot, err := workspace.capture(ctx)
	if err != nil {
		return err
	}
	if snapshot.Fingerprint != workspace.fingerprint {
		return errors.New("planning workspace provenance changed")
	}
	return nil
}

func (workspace PlanningWorkspace) verifyOwnedPath() error {
	if workspace.parentInfo == nil || workspace.checkout.Path != filepath.Join(workspace.checkout.parent, "candidate") {
		return errors.New("planning workspace has no private path ownership")
	}
	if err := securefs.ValidatePrivateDir(workspace.checkout.parent); err != nil {
		return err
	}
	info, err := os.Lstat(workspace.checkout.parent)
	if err != nil || !os.SameFile(workspace.parentInfo, info) {
		return errors.Join(errors.New("private planning parent was replaced"), err)
	}
	return nil
}

// Cleanup uses only privately retained paths and the caller's bounded context.
// A failed Git removal preserves the directory for later recovery.
func (workspace PlanningWorkspace) Cleanup(ctx context.Context) error {
	if workspace.checkout.Path == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := workspace.verifyOwnedPath(); err != nil {
		return err
	}
	if err := workspace.checkout.cleanupLocked(ctx); err != nil {
		return err
	}
	if workspace.pinRef != "" {
		return workspace.checkout.provider.releasePlanningRef(ctx, workspace.checkout.sourceProfile, workspace.pinRef, workspace.source.CommitOID)
	}
	return nil
}
