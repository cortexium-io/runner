package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type repositoryRootNotifyingRunner struct {
	delegate subprocess.Runner
	resolved chan struct{}
	once     sync.Once
}

func (r *repositoryRootNotifyingRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := r.delegate.Run(ctx, command, args, dir, timeout)
	if command == "git" && len(args) == 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
		r.once.Do(func() { close(r.resolved) })
	}
	return result, err
}

func TestGitWorkspaceProviderCreatesReusableIsolatedWorktree(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "work_123", BranchPrefix: "agent", BaseRef: "HEAD"})

	created, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	if created.RepoRoot != resolvedRepo || created.BranchName != "agent/work_123" || !testPathInsideOrEqual(created.WorktreePath, root) {
		t.Fatalf("unexpected workspace metadata %#v", created)
	}
	if _, err := os.Stat(filepath.Join(created.WorktreePath, "README.md")); err != nil {
		t.Fatalf("prepared worktree is not usable: %v", err)
	}
	if info, err := os.Stat(root); err != nil {
		t.Fatalf("inspect workspace root: %v", err)
	} else if info.Mode().Perm() != 0o700 {
		t.Fatalf("workspace root mode = %04o, want 0700", info.Mode().Perm())
	}

	reused, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("reuse workspace: %v", err)
	}
	if reused != created {
		t.Fatalf("reused workspace changed identity: created=%#v reused=%#v", created, reused)
	}
	recorded, found, err := readIdentity(activeIdentityPath(root, request.WorkID))
	if err != nil || !found || recorded != created.Identity {
		t.Fatalf("private workspace identity was not recorded exactly: recorded=%#v found=%t error=%v", recorded, found, err)
	}
	if info, err := os.Stat(activeIdentityPath(root, request.WorkID)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("workspace identity record is not private: mode=%v error=%v", info.Mode().Perm(), err)
	}
}

func TestResourceIdentityUsesWorkspaceBranchNaming(t *testing.T) {
	generated, err := ResourceIdentity(Request{WorkID: "assignment_PVTI_123", Repository: "Owner/Repo", BranchPrefix: "runner"})
	if err != nil {
		t.Fatalf("derive generated workspace identity: %v", err)
	}
	if generated != "owner/repo/runner/assignment_pvti_123" {
		t.Fatalf("generated workspace identity = %q", generated)
	}
	persisted, err := ResourceIdentity(Request{WorkID: "assignment_PVTI_456", Repository: "owner/repo", BranchPrefix: "runner", BranchName: "feature/retained"})
	if err != nil {
		t.Fatalf("derive persisted workspace identity: %v", err)
	}
	if persisted != "owner/repo/feature/retained" {
		t.Fatalf("persisted workspace identity = %q", persisted)
	}
}

func TestGitWorkspaceProviderReusesAuthenticatedWorkspaceAfterBaseAdvances(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "base_advanced", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new-base.txt"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "new-base.txt")
	runGitTest(t, repo, "commit", "-m", "Advance base")

	reused, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("reuse authenticated workspace after base advance: %v", err)
	}
	if reused.BaseRevision != prepared.BaseRevision || reused.Identity != prepared.Identity {
		t.Fatalf("prepare advanced identity before authenticated refresh: prepared=%#v reused=%#v", prepared.Identity, reused.Identity)
	}
}

func TestGitWorkspaceProviderRejectsRewrittenBase(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	runGitTest(t, repo, "branch", "runner-base")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "base_rewritten", BranchPrefix: "runner", BaseRef: "runner-base"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD^{tree}"))
	rewritten := strings.TrimSpace(runGitTest(t, repo, "commit-tree", tree, "-m", "Rewrite base"))
	runGitTest(t, repo, "update-ref", "refs/heads/runner-base", rewritten, prepared.BaseRevision)

	if _, err := provider.Prepare(t.Context(), request); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("rewritten base was accepted as a normal advance: %v", err)
	}
}

func TestGitWorkspaceProviderSupportsEmptyBaseCommitWithoutIndex(t *testing.T) {
	repo := initEmptyGitRepo(t)
	if _, err := os.Lstat(filepath.Join(repo, ".git", "index")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty repository unexpectedly has a Git index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".cortexium/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "empty_base", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatalf("prepare workspace from empty base commit: %v", err)
	}
	if prepared.SourceSnapshot == "" || prepared.WorktreePath == "" {
		t.Fatalf("empty base workspace metadata is incomplete: %#v", prepared)
	}
	if _, err := os.Stat(filepath.Join(prepared.WorktreePath, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked project file leaked into the task worktree: %v", err)
	}
}

func testPathInsideOrEqual(path, parent string) bool {
	relative, err := filepath.Rel(normalizedPath(parent), normalizedPath(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}

func TestGitWorkspaceProviderSerializesCleanupWithRepositoryLifecycle(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "work_serialized", BranchPrefix: "agent", BaseRef: "HEAD"})
	prepared, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare workspace: %v", err)
	}

	runner := &repositoryRootNotifyingRunner{delegate: subprocess.OSRunner{}, resolved: make(chan struct{})}
	provider := NewGitProvider(runner)
	unlock := lockRepositoryWorkspace(repo)
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	done := make(chan error, 1)
	go func() {
		_, cleanupErr := provider.Cleanup(t.Context(), cleanupFor(request, prepared.BranchName))
		done <- cleanupErr
	}()

	select {
	case <-runner.resolved:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not resolve the repository root")
	}
	select {
	case cleanupErr := <-done:
		t.Fatalf("cleanup completed while the repository lifecycle lock was held: %v", cleanupErr)
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	locked = false
	select {
	case cleanupErr := <-done:
		if cleanupErr != nil {
			t.Fatalf("cleanup after releasing repository lifecycle lock: %v", cleanupErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not resume after releasing the repository lifecycle lock")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for workspace tests: %v", err)
	}
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	runGitTest(t, dir, "config", "user.email", "runner-test@example.invalid")
	runGitTest(t, dir, "config", "user.name", "Runner Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGitTest(t, dir, "add", "README.md")
	runGitTest(t, dir, "commit", "-m", "Initial commit")
	return dir
}

func initEmptyGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for workspace tests: %v", err)
	}
	dir := t.TempDir()
	runGitTest(t, dir, "init")
	runGitTest(t, dir, "config", "user.email", "runner-test@example.invalid")
	runGitTest(t, dir, "config", "user.name", "Runner Test")
	runGitTest(t, dir, "commit", "--allow-empty", "-m", "Initial commit")
	if err := os.Remove(filepath.Join(dir, ".git", "index")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove temporary empty Git index: %v", err)
	}
	return dir
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}

func boundRequest(request Request) Request {
	request.ItemID = request.WorkID
	request.DelegatedContentDigest = "v1:test-delegated-content"
	request.Repository = "owner/repo"
	return request
}

func cleanupFor(request Request, branch string) CleanupRequest {
	return CleanupRequest{
		WorkingDir: request.WorkingDir, WorktreeRoot: request.WorktreeRoot, WorkID: request.WorkID,
		ItemID: request.ItemID, DelegatedContentDigest: request.DelegatedContentDigest, Repository: request.Repository,
		BranchName: branch, BaseRef: request.BaseRef,
	}
}

func TestGitWorkspaceProviderLeavesDirtyProjectCheckoutUntouched(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "work_dirty", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatalf("prepare task worktree from dirty project checkout: %v", err)
	}
	if !strings.HasPrefix(prepared.SourceSnapshot, "sha256:") {
		t.Fatalf("dirty project content was not snapshotted: %q", prepared.SourceSnapshot)
	}
	if _, err := os.Stat(filepath.Join(prepared.WorktreePath, "dirty.txt")); !os.IsNotExist(err) {
		t.Fatalf("uncommitted project file leaked into task worktree: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "dirty.txt"))
	if err != nil || string(content) != "dirty\n" {
		t.Fatalf("project checkout was changed: content=%q error=%v", content, err)
	}
}

func TestGitWorkspaceProviderQuarantinesUnboundPersistedLifecycleBranch(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "branch", "cortexium/original-task")
	provider := NewGitProvider(subprocess.OSRunner{})
	workspace, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "different_item_id",
		BranchName: "cortexium/original-task", BaseRef: "cortexium/original-task", QuarantineMismatch: true,
	}))
	if err != nil {
		t.Fatalf("replace unbound persisted branch: %v", err)
	}
	if workspace.BranchName != "cortexium/original-task" {
		t.Fatalf("workspace used a regenerated branch instead of the persisted branch: %#v", workspace)
	}
}

func TestGitWorkspaceProviderCleansWorktreeAndReopensRetainedBranch(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_item_123", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare task worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "generated.tmp"), []byte("ephemeral\n"), 0o644); err != nil {
		t.Fatalf("write task artifact: %v", err)
	}
	cleaned, err := provider.Cleanup(t.Context(), cleanupFor(request, prepared.BranchName))
	if err != nil {
		t.Fatalf("cleanup task workspace: %v", err)
	}
	if !cleaned.WorktreeRemoved {
		t.Fatalf("cleanup did not remove task worktree: %#v", cleaned)
	}
	if _, err := os.Lstat(prepared.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task worktree still exists at %s: %v", prepared.WorktreePath, err)
	}
	if branch := strings.TrimSpace(runGitTest(t, repo, "show-ref", "--hash", "refs/heads/"+prepared.BranchName)); branch == "" {
		t.Fatal("cleanup removed the retained task branch")
	}

	reopenRequest := request
	reopenRequest.BranchName = prepared.BranchName
	reopenRequest.BaseRef = "HEAD"
	reopened, err := provider.Prepare(t.Context(), reopenRequest)
	if err != nil {
		t.Fatalf("reopen cleaned task workspace: %v", err)
	}
	if reopened.WorktreePath != prepared.WorktreePath || reopened.BranchName != prepared.BranchName {
		t.Fatalf("reopened task changed identity: before=%#v after=%#v", prepared, reopened)
	}
}

func TestGitWorkspaceProviderCleansWorktreeAfterBaseAdvances(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_merged_item", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare task worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "merged.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "merged.txt")
	runGitTest(t, repo, "commit", "-m", "Advance base after merge")

	cleaned, err := provider.Cleanup(t.Context(), cleanupFor(request, prepared.BranchName))
	if err != nil {
		t.Fatalf("cleanup task workspace after base advanced: %v", err)
	}
	if !cleaned.WorktreeRemoved {
		t.Fatalf("cleanup did not remove task worktree: %#v", cleaned)
	}
	if _, err := os.Lstat(prepared.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("task worktree still exists at %s: %v", prepared.WorktreePath, err)
	}
}

func TestGitWorkspaceProviderRefusesIdentityWithoutRetainedBranch(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "missing_branch", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Cleanup(t.Context(), cleanupFor(request, prepared.BranchName)); err != nil {
		t.Fatalf("remove registered worktree: %v", err)
	}
	runGitTest(t, repo, "branch", "-D", prepared.BranchName)

	if _, err := provider.Prepare(t.Context(), request); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity without its retained branch was accepted: %v", err)
	}
	if _, err := os.Lstat(prepared.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed-closed preparation recreated a worktree: %v", err)
	}
	if output, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+prepared.BranchName).CombinedOutput(); err == nil {
		t.Fatalf("failed-closed preparation recreated the retained branch: %s", output)
	}
	if _, found, err := readIdentity(activeIdentityPath(root, request.WorkID)); err != nil || !found {
		t.Fatalf("failed-closed preparation did not preserve the identity record: found=%t error=%v", found, err)
	}
}

func TestGitWorkspaceProviderQuarantinesIdentityWithoutRetainedBranchBeforeRecreating(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "recreate_missing_branch", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Cleanup(t.Context(), cleanupFor(request, prepared.BranchName)); err != nil {
		t.Fatalf("remove registered worktree: %v", err)
	}
	runGitTest(t, repo, "branch", "-D", prepared.BranchName)

	request.QuarantineMismatch = true
	recreated, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("recreate after quarantining incomplete retained state: %v", err)
	}
	if recreated != prepared {
		t.Fatalf("clean recreation changed the approved identity: prepared=%#v recreated=%#v", prepared, recreated)
	}
	if _, err := os.Stat(filepath.Join(recreated.WorktreePath, "README.md")); err != nil {
		t.Fatalf("clean recreation is unusable: %v", err)
	}
	quarantinedIdentities, err := filepath.Glob(filepath.Join(root, ".runner-state", "quarantine", "recreate_missing_branch-*.json"))
	if err != nil || len(quarantinedIdentities) != 1 {
		t.Fatalf("stale identity was not quarantined exactly once: paths=%v error=%v", quarantinedIdentities, err)
	}
	active, found, err := readIdentity(activeIdentityPath(root, request.WorkID))
	if err != nil || !found || active != recreated.Identity {
		t.Fatalf("clean recreation did not record the active identity: identity=%#v found=%t error=%v", active, found, err)
	}
}

func TestGitWorkspaceProviderCleanupIsIdempotentAndRefusesBranchMismatch(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_item_456", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare task worktree: %v", err)
	}

	_, err = provider.Cleanup(t.Context(), cleanupFor(request, "runner/different-item"))
	if err == nil || !strings.Contains(err.Error(), "does not match expected branch") {
		t.Fatalf("cleanup accepted a mismatched branch: %v", err)
	}
	if _, err := os.Stat(prepared.WorktreePath); err != nil {
		t.Fatalf("mismatched cleanup removed the task worktree: %v", err)
	}

	cleanupRequest := cleanupFor(request, prepared.BranchName)
	if _, err := provider.Cleanup(t.Context(), cleanupRequest); err != nil {
		t.Fatalf("cleanup task workspace: %v", err)
	}
	repeated, err := provider.Cleanup(t.Context(), cleanupRequest)
	if err != nil {
		t.Fatalf("repeat task cleanup: %v", err)
	}
	if repeated.WorktreeRemoved {
		t.Fatalf("idempotent cleanup reported new removals: %#v", repeated)
	}
}

func TestGitWorkspaceProviderCleanupRefusesRetainedBranchWithoutIdentity(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "identityless_branch", BranchPrefix: "runner", BaseRef: "HEAD"})
	branch := "runner/identityless_branch"
	runGitTest(t, repo, "branch", branch)

	_, err := provider.Cleanup(t.Context(), cleanupFor(request, branch))
	if !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "has no private identity record") {
		t.Fatalf("cleanup accepted a retained branch without identity: %v", err)
	}
	if result := runGitTest(t, repo, "show-ref", "--verify", "refs/heads/"+branch); strings.TrimSpace(result) == "" {
		t.Fatalf("rejected cleanup removed retained branch %q", branch)
	}
}

func TestGitWorkspaceProviderRefusesUnownedExistingWorktree(t *testing.T) {
	repo := initGitRepo(t)
	otherRepo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	workID := "shared_id"
	path := filepath.Join(root, workID)
	runGitTest(t, otherRepo, "worktree", "add", "-b", "agent/"+workID, path)
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: workID, BranchPrefix: "agent", BaseRef: "HEAD",
	}))
	if err == nil || !strings.Contains(err.Error(), "not owned by the configured repository") {
		t.Fatalf("unowned worktree was reused: %v", err)
	}
}

func TestGitWorkspaceProviderRejectsRootContainingActiveCheckout(t *testing.T) {
	repo := initGitRepo(t)
	_, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Dir(repo), WorkID: "unsafe_root", BranchPrefix: "agent", BaseRef: "HEAD",
	}))
	if err == nil || !strings.Contains(err.Error(), "must not contain one another") {
		t.Fatalf("workspace root containing active checkout was accepted: %v", err)
	}
}

func TestGitWorkspaceProviderRejectsSymlinkedRootAncestor(t *testing.T) {
	repo := initGitRepo(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	_, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(linked, "worktrees"), WorkID: "unsafe_link", BranchPrefix: "agent", BaseRef: "HEAD",
	}))
	if err == nil {
		t.Fatal("symlinked workspace-root ancestor was accepted")
	}
}

func TestGitWorkspaceProviderRejectsSymlinkedStateDirectory(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".runner-state")); err != nil {
		t.Fatal(err)
	}
	_, err := NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "unsafe_state", BranchPrefix: "agent", BaseRef: "HEAD",
	}))
	if err == nil || !strings.Contains(err.Error(), "read workspace identity") {
		t.Fatalf("symlinked Runner state was accepted: %v", err)
	}
}

func TestGitWorkspaceProviderRejectsEveryIdentityMismatchWithoutChangingWorkspace(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Request, string)
	}{
		{name: "item", mutate: func(request *Request, _ string) { request.ItemID = "different-item" }},
		{name: "delegated content", mutate: func(request *Request, _ string) { request.DelegatedContentDigest = "v1:changed" }},
		{name: "branch", mutate: func(request *Request, _ string) { request.BranchName = "runner/different" }},
		{name: "repository", mutate: func(request *Request, _ string) { request.Repository = "other/repo" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initGitRepo(t)
			root := filepath.Join(t.TempDir(), "worktrees")
			provider := NewGitProvider(subprocess.OSRunner{})
			request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "bound_item", BranchPrefix: "runner", BaseRef: "HEAD"})
			prepared, err := provider.Prepare(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			dirtyPath := filepath.Join(prepared.WorktreePath, "valuable.txt")
			if err := os.WriteFile(dirtyPath, []byte("preserve me\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			test.mutate(&request, repo)
			if _, err := provider.Prepare(t.Context(), request); !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("identity mismatch was accepted: %v", err)
			}
			if content, err := os.ReadFile(dirtyPath); err != nil || string(content) != "preserve me\n" {
				t.Fatalf("rejected mismatch changed existing work: content=%q error=%v", content, err)
			}
		})
	}
}

func TestGitWorkspaceProviderQuarantinesMismatchAndCreatesCleanWorkspace(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "quarantine_item", BranchPrefix: "runner", BaseRef: "HEAD"})
	old, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old.WorktreePath, "valuable.txt"), []byte("inspectable\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	request.DelegatedContentDigest = "v1:reapproved-content"
	request.QuarantineMismatch = true
	current, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatalf("prepare clean workspace after reapproval: %v", err)
	}
	if _, err := os.Stat(filepath.Join(current.WorktreePath, "valuable.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantined content entered the clean workspace: %v", err)
	}
	quarantines, err := filepath.Glob(filepath.Join(root, ".runner-quarantine", "quarantine_item-*"))
	if err != nil || len(quarantines) != 1 {
		t.Fatalf("stale workspace was not quarantined exactly once: paths=%v error=%v", quarantines, err)
	}
	content, err := os.ReadFile(filepath.Join(quarantines[0], "valuable.txt"))
	if err != nil || string(content) != "inspectable\n" {
		t.Fatalf("quarantine did not preserve uncommitted work: content=%q error=%v", content, err)
	}
	recorded, found, err := readIdentity(activeIdentityPath(root, request.WorkID))
	if err != nil || !found || recorded.DelegatedContentDigest != request.DelegatedContentDigest || recorded != current.Identity {
		t.Fatalf("replacement workspace identity is wrong: recorded=%#v current=%#v found=%t error=%v", recorded, current.Identity, found, err)
	}
}

func TestGitWorkspaceProviderQuarantineCollisionDoesNotOverwriteExistingData(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	fixed := time.Date(2026, time.August, 12, 12, 34, 56, 123, time.UTC)
	provider.now = func() time.Time { return fixed }
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "collision_item", BranchPrefix: "runner", BaseRef: "HEAD"})
	old, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old.WorktreePath, "valuable.txt"), []byte("old work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stem := "collision_item-" + fixed.UTC().Format("20060102T150405.000000000Z")
	collision := filepath.Join(root, ".runner-quarantine", stem)
	if err := os.MkdirAll(collision, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(collision, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("do not overwrite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.DelegatedContentDigest = "v1:new-content"
	request.QuarantineMismatch = true
	if _, err := provider.Prepare(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "do not overwrite\n" {
		t.Fatalf("existing quarantine was overwritten: content=%q error=%v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, ".runner-quarantine", stem+"-2", "valuable.txt")); err != nil || string(content) != "old work\n" {
		t.Fatalf("collision-safe quarantine did not preserve stale work: content=%q error=%v", content, err)
	}
}

func TestGitWorkspaceProviderCleanupRefusesIdentityMismatch(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	request := boundRequest(Request{WorkingDir: repo, WorktreeRoot: root, WorkID: "cleanup_identity", BranchPrefix: "runner", BaseRef: "HEAD"})
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := cleanupFor(request, prepared.BranchName)
	cleanup.DelegatedContentDigest = "v1:changed-before-cleanup"
	if _, err := provider.Cleanup(t.Context(), cleanup); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("cleanup accepted mismatched identity: %v", err)
	}
	if _, err := os.Stat(prepared.WorktreePath); err != nil {
		t.Fatalf("mismatched cleanup removed the workspace: %v", err)
	}
}
