//go:build !windows

package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestConstructCandidatePinsLinkedWorktreeAndIgnoresExecutableGitConfiguration(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "privileged_candidate", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	feature := []byte("literal worktree bytes\n")
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), feature, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, ".gitattributes"), []byte("feature.txt filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "executed")
	executable := filepath.Join(t.TempDir(), "malicious-git-control")
	script := "#!/bin/sh\nprintf 'executed:%s\\n' \"$*\" > " + strconvQuoteShell(marker) + "\ncat\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.Mkdir(hooks, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-commit", "reference-transaction"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\nprintf hook > "+strconvQuoteShell(marker)+"\nexit 91\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repo, "config", "extensions.worktreeConfig", "true")
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "filter.evil.clean", executable)
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "filter.evil.required", "true")
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "core.hooksPath", hooks)
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "core.fsmonitor", executable)
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "commit.gpgSign", "true")
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "gpg.program", executable)
	includedConfig := filepath.Join(t.TempDir(), "included.gitconfig")
	if err := os.WriteFile(includedConfig, []byte("[url \"https://attacker.invalid/\"]\n\tinsteadOf = https://github.com/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "include.path", includedConfig)
	runGitTest(t, prepared.WorktreePath, "config", "--worktree", "remote.origin.url", "https://attacker.invalid/repository")
	globalConfig := filepath.Join(t.TempDir(), "global.gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[core]\n\thooksPath = "+hooks+"\n[filter \"evil\"]\n\tclean = "+executable+"\n\trequired = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "redirected-git-dir"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(t.TempDir(), "redirected-index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(t.TempDir(), "redirected-objects"))
	if err := os.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	profile, err := derivePrivilegedGitProfile(prepared.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	configResult, err := subprocess.RunPrivilegedGit(t.Context(), subprocess.OSRunner{}, profile, []string{"config", "--show-origin", "--list"}, 5*time.Second)
	if err != nil {
		t.Fatalf("inspect privileged effective config: %v: %s", err, configResult.Stderr)
	}
	for _, forbidden := range []string{includedConfig, executable, hooks, "attacker.invalid", "filter.evil", "remote.origin"} {
		if strings.Contains(configResult.Stdout, forbidden) {
			t.Fatalf("privileged Git retained repository configuration %q:\n%s", forbidden, configResult.Stdout)
		}
	}

	candidate, err := provider.ConstructCandidate(t.Context(), prepared, "Sanitized candidate")
	if err != nil {
		t.Fatalf("construct sanitized candidate: %v", err)
	}
	if !validObjectID(candidate.CommitOID) || !validObjectID(candidate.TreeOID) {
		t.Fatalf("candidate has invalid object identity: %#v", candidate)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		content, _ := os.ReadFile(marker)
		t.Fatalf("privileged candidate executed repository Git behavior: marker=%q error=%v", content, err)
	}
	for _, key := range []string{"GIT_CONFIG_GLOBAL", "GIT_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY"} {
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	if got := runGitTest(t, prepared.WorktreePath, "show", "HEAD:feature.txt"); got != string(feature) {
		t.Fatalf("candidate tree contains filtered bytes %q, want %q", got, feature)
	}
	if status := runGitTest(t, prepared.WorktreePath, "-c", "core.fsmonitor=false", "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("candidate is not clean: %q", status)
	}
}

func TestConstructCandidateStagesResolvedMergeAfterHarnessClearsMergeState(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "resolved_merge", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "README.md"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "add", "README.md")
	runGitTest(t, prepared.WorktreePath, "commit", "-m", "Candidate")
	candidateParent := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("new base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "commit", "-m", "Advance base")
	baseParent := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	if output, err := exec.Command("git", "-C", prepared.WorktreePath, "merge", "--no-edit", baseParent).CombinedOutput(); err == nil {
		t.Fatalf("test merge unexpectedly had no conflict: %s", output)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "README.md"), []byte("resolved candidate and base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "reset", "--mixed", "HEAD")
	if _, err := exec.Command("git", "-C", prepared.WorktreePath, "rev-parse", "--verify", "MERGE_HEAD").CombinedOutput(); err == nil {
		t.Fatal("test setup retained MERGE_HEAD")
	}

	replacement := prepared.Identity
	replacement.BaseRevision = baseParent
	if err := replaceIdentity(activeIdentityPath(root, "resolved_merge"), prepared.Identity, replacement); err != nil {
		t.Fatal(err)
	}
	prepared, err = bindGitAdministration(metadataFor(prepared.RepoRoot, prepared.SourceSnapshot, replacement))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidate(t.Context(), prepared, "Resolve refreshed base")
	if err != nil {
		t.Fatalf("construct candidate after the harness cleared merge state: %v", err)
	}
	parents := strings.Fields(runGitTest(t, prepared.WorktreePath, "rev-list", "--parents", "-n", "1", candidate.CommitOID))
	if len(parents) != 3 || parents[1] != candidateParent || parents[2] != baseParent {
		t.Fatalf("candidate merge parents = %v, want candidate %s and base %s", parents, candidateParent, baseParent)
	}
	if status := runGitTest(t, prepared.WorktreePath, "status", "--porcelain", "--untracked-files=all"); status != "" {
		t.Fatalf("resolved candidate is not clean: %q", status)
	}
}

func TestConstructCandidateSupportsSHA256RepositoryWithConfigurationDisabled(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for workspace tests: %v", err)
	}
	repo := t.TempDir()
	runGitTest(t, repo, "init", "--object-format=sha256")
	runGitTest(t, repo, "config", "user.email", "runner-test@example.invalid")
	runGitTest(t, repo, "config", "user.name", "Runner Test")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "commit", "-m", "Initial commit")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "sha256_candidate", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("sha256 candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidate(t.Context(), prepared, "SHA-256 candidate")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.CommitOID) != 64 || len(candidate.TreeOID) != 64 {
		t.Fatalf("SHA-256 candidate returned unexpected object identities: %#v", candidate)
	}
}

func TestPublicationAcceptanceRecordsExactTupleExclusively(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "accepted_tuple", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "accepted.txt"), []byte("accepted bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	candidate, err := provider.ConstructCandidate(t.Context(), prepared, "Accepted tuple")
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, prepared.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	record, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted)
	if err != nil {
		t.Fatal(err)
	}
	if record.CommitOID != candidate.CommitOID || record.TreeOID != candidate.TreeOID || record.ItemID != prepared.Identity.ItemID ||
		record.DelegatedContentDigest != prepared.Identity.DelegatedContentDigest || record.ApprovedBaseRef != prepared.BaseRef ||
		record.ApprovedBaseOID != prepared.BaseRevision || record.Repository != prepared.Identity.Repository ||
		record.DestinationRef != "refs/heads/"+prepared.BranchName || record.AcceptanceSnapshot != accepted.Fingerprint {
		t.Fatalf("publication tuple lost accepted identity: %#v", record)
	}
	replayed, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted)
	if err != nil || replayed != record {
		t.Fatalf("exact publication tuple replay changed the record: record=%#v error=%v", replayed, err)
	}
	path := filepath.Join(root, ".runner-state", "publications", candidate.CommitOID+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("inspect publication record: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("publication record mode = %v, want 0600", info.Mode().Perm())
	}
	conflict := record
	conflict.Repository = "attacker/repository"
	content, err := json.Marshal(conflict)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted); err == nil || !strings.Contains(err.Error(), "different immutable tuple") {
		t.Fatalf("publication record collision was accepted: %v", err)
	}
}

func TestPublicationAcceptanceRejectsChangedHeadOrTree(t *testing.T) {
	repo := initGitRepo(t)
	root := filepath.Join(t.TempDir(), "worktrees")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "changed_acceptance", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "accepted.txt"), []byte("accepted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Accepted"); err != nil {
		t.Fatal(err)
	}
	accepted, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, prepared.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "after-qa.txt"), []byte("unreviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "add", "--all")
	runGitTest(t, prepared.WorktreePath, "commit", "-m", "unreviewed change")
	if _, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted); err == nil || !strings.Contains(err.Error(), "snapshot, HEAD, tree, or branch changed") {
		t.Fatalf("changed acceptance snapshot was recorded: %v", err)
	}
}

func TestCandidateRejectsForeignGitAdministrationAfterPreparation(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "foreign_admin", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	foreign := initGitRepo(t)
	runGitTest(t, foreign, "branch", "-m", prepared.BranchName)
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, ".git"), []byte("gitdir: "+filepath.Join(foreign, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Foreign administration"); err == nil || !strings.Contains(err.Error(), "does not match the prepared workspace") {
		t.Fatalf("foreign Git administration was accepted: %v", err)
	}
}

func TestPublicationAcceptanceRejectsCandidateUnrelatedToApprovedBase(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "unrelated_base", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD^{tree}"))
	root := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "commit-tree", tree, "-m", "Unrelated root"))
	runGitTest(t, prepared.WorktreePath, "update-ref", "refs/heads/"+prepared.BranchName, root)
	runGitTest(t, prepared.WorktreePath, "reset", "--hard", root)
	accepted, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, prepared.WorktreePath, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted); err == nil || !strings.Contains(err.Error(), "not descended from the approved base") {
		t.Fatalf("candidate unrelated to approved base was accepted: %v", err)
	}
}

func TestConstructCandidateRejectsHiddenIndexState(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "hidden_index", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "update-index", "--skip-worktree", "README.md")
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Hidden index"); err == nil || !strings.Contains(err.Error(), "hidden worktree state") {
		t.Fatalf("candidate accepted skip-worktree index state: %v", err)
	}
}

func TestConstructCandidateRejectsReplacementObjects(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "replacement_object", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	replacement := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "commit-tree", "HEAD^{tree}", "-m", "replacement"))
	runGitTest(t, prepared.WorktreePath, "replace", "HEAD", replacement)
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Replacement object"); err == nil || !strings.Contains(err.Error(), "refuses replacement objects") {
		t.Fatalf("candidate accepted replacement objects: %v", err)
	}
}

func TestConstructCandidateRejectsObjectAlternates(t *testing.T) {
	repo := initGitRepo(t)
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "object_alternates", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := derivePrivilegedGitProfile(prepared.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.ObjectDirectory, "info", "alternates"), []byte(t.TempDir()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Object alternates"); err == nil || !strings.Contains(err.Error(), "refuses object redirection") {
		t.Fatalf("candidate accepted object alternates: %v", err)
	}
}

func TestConstructCandidateHandlesTrackedFileReplacedByDirectory(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "mv", "README.md", "old.txt")
	runGitTest(t, repo, "commit", "-m", "Track replaceable path")
	provider := NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "file_to_directory", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(prepared.WorktreePath, "old.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(prepared.WorktreePath, "old.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "old.txt", "nested.txt"), []byte("nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), prepared, "Replace file with directory"); err != nil {
		t.Fatal(err)
	}
	if got := runGitTest(t, prepared.WorktreePath, "show", "HEAD:old.txt/nested.txt"); got != "nested\n" {
		t.Fatalf("candidate did not record replacement directory: %q", got)
	}
}

func strconvQuoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
