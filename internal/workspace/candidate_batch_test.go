//go:build !windows

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type candidateCountingRunner struct {
	calls, hashes, updates, maxArgs, hashedPaths int
	gitTime                                      time.Duration
}

func (r *candidateCountingRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	start := time.Now()
	result, err := (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	if command == "git" {
		r.gitTime += time.Since(start)
		r.calls++
		argBytes := 0
		for _, arg := range args {
			argBytes += len(arg) + 1
		}
		if argBytes > r.maxArgs {
			r.maxArgs = argBytes
		}
		if slices.Contains(args, "hash-object") {
			r.hashes++
			if separator := slices.Index(args, "--"); separator >= 0 {
				r.hashedPaths += len(args) - separator - 1
			}
		}
		if slices.Contains(args, "update-index") {
			r.updates++
		}
	}
	return result, err
}

func TestConstructCandidateBatchesLargeInventory(t *testing.T) {
	repo := initGitRepo(t)
	const files = 3000
	for i := range files {
		if err := os.WriteFile(filepath.Join(repo, fmt.Sprintf("file-%04d", i)), []byte(fmt.Sprintf("original %d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Large literal inventory")
	metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "large_candidate", BranchPrefix: "runner", BaseRef: "HEAD",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "file-0000"), []byte("modified raw bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(metadata.WorktreePath, "file-0001")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "added.txt"), []byte("new raw bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// This selected test is serial and starts with the global mutex available;
	// the measurements exclude fixture setup and introduce no mutex contention.
	if !privilegedGitMu.TryLock() {
		t.Fatal("unexpected candidate mutex contention in isolated fixture")
	}
	privilegedGitMu.Unlock()
	runner := &candidateCountingRunner{}
	start := time.Now()
	candidate, err := NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Three changed paths")
	elapsed := time.Since(start)
	t.Logf("tracked=%d changed=3 git_calls=%d hash_calls=%d index_calls=%d git_elapsed=%s total_elapsed=%s mutex_contention=none", files+1, runner.calls, runner.hashes, runner.updates, runner.gitTime, elapsed)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.CommitOID == "" {
		t.Fatal("missing constructed candidate")
	}
	if got := runGitTest(t, metadata.WorktreePath, "diff", "--name-status", metadata.BaseRevision, "HEAD"); got != "A\tadded.txt\nM\tfile-0000\nD\tfile-0001\n" {
		t.Fatalf("candidate changed more than the three intended paths: %q", got)
	}
	if got := runGitTest(t, metadata.WorktreePath, "show", "HEAD:file-0000"); got != "modified raw bytes\n" {
		t.Fatalf("wrong literal content: %q", got)
	}
	if got := runGitTest(t, metadata.WorktreePath, "status", "--porcelain"); strings.TrimSpace(got) != "" {
		t.Fatalf("candidate remains dirty: %q", got)
	}
	if runner.hashedPaths != 2*(files+1) {
		t.Fatalf("unchanged raw files or the fresh verification pass were skipped: hashed %d paths", runner.hashedPaths)
	}
	// Bound subprocess scaling, not wall-clock speed on a shared CI host.
	if runner.calls > 200 {
		t.Fatalf("tiny change in a large inventory used %d Git subprocesses; want at most 200", runner.calls)
	}
}

func TestCandidateBatchPreservesLiteralEntriesAndGitlinks(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			repo := t.TempDir()
			runGitTest(t, repo, "init", "--object-format="+format)
			runGitTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Base")
			metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "literal_batch", BranchPrefix: "runner", BaseRef: "HEAD"}))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"line\nwith\ttab\\quote\".bin": "literal\x00raw\r\n", "-leading.bin": "option\x00bytes", ":(glob)*.bin": "pathspec\x00bytes", "commas,stay,literal.bin": "commas\x00bytes", "é-界.bin": "unicode\x00bytes", "raw-\xff.bin": "non-UTF8\x00bytes",
				".gitattributes": "*.bin filter=unavailable\n", "run.sh": "#!/bin/sh\nexit 0\n",
			}
			// Long paths force the byte bound independently of the path-count bound.
			prefix := strings.Repeat("a", 150) + "/" + strings.Repeat("b", 150) + "/"
			for i := range 96 {
				files[prefix+fmt.Sprintf("%03d-", i)+strings.Repeat("c", 100)] = fmt.Sprintf("literal %d\n", i)
			}
			for path, contents := range files {
				full := filepath.Join(metadata.WorktreePath, path)
				if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
					if !utf8.ValidString(path) && errors.Is(err, syscall.EILSEQ) {
						t.Logf("non-UTF-8 filename unavailable on this filesystem: %q", path)
						delete(files, path)
						continue
					}
					t.Fatal(err)
				}
			}
			if err := os.Chmod(filepath.Join(metadata.WorktreePath, "run.sh"), 0o755); err != nil {
				t.Fatal(err)
			}
			linkTarget := "missing target\nwith\ttab"
			if err := os.Symlink(linkTarget, filepath.Join(metadata.WorktreePath, "literal-link")); err != nil {
				t.Fatal(err)
			}
			module := filepath.Join(metadata.WorktreePath, "module")
			if err := os.Mkdir(module, 0o755); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, module, "init", "--object-format="+format)
			runGitTest(t, module, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Submodule base")
			old := strings.TrimSpace(runGitTest(t, module, "rev-parse", "HEAD"))
			runGitTest(t, metadata.WorktreePath, "update-index", "--add", "--cacheinfo", "160000", old, "module")
			runGitTest(t, module, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "Submodule advance")
			moduleHead := strings.TrimSpace(runGitTest(t, module, "rev-parse", "HEAD"))
			runner := &candidateCountingRunner{}
			candidate, err := NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Literal entries")
			if err != nil {
				t.Fatal(err)
			}
			for path, contents := range files {
				if got := runGitTest(t, metadata.WorktreePath, "show", "HEAD:"+path); got != contents {
					t.Fatalf("literal path %q changed: %q", path, got)
				}
			}
			if got := runGitTest(t, metadata.WorktreePath, "show", "HEAD:literal-link"); got != linkTarget {
				t.Fatalf("symlink destination was followed or changed: %q", got)
			}
			index, err := NewGitProvider(nil).privilegedGit(t.Context(), mustCandidateProfile(t, metadata), "ls-files", "--stage", "-v", "-z")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := parseSnapshotIndex(index.Stdout)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(entries["run.sh"], "mode=100755 ") || !strings.HasPrefix(entries["literal-link"], "mode=120000 ") || !strings.HasPrefix(entries["module"], "mode=160000 object="+moduleHead+" ") {
				t.Fatal("executable, symlink or current gitlink identity was lost")
			}
			if len(candidate.CommitOID) != len(old) || len(candidate.TreeOID) != len(old) {
				t.Fatal("repository object format changed")
			}
			if runner.maxArgs > 18*1024 {
				t.Fatalf("Git argv batch exceeded its bounded transport: %d bytes", runner.maxArgs)
			}
		})
	}
}

func mustCandidateProfile(t *testing.T, metadata Metadata) subprocess.PrivilegedGitProfile {
	t.Helper()
	profile, err := derivePrivilegedGitProfile(metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

type candidateHashFailureRunner struct {
	result          subprocess.Result
	err             error
	hashes, updates int
}

func (r *candidateHashFailureRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && slices.Contains(args, "update-index") {
		r.updates++
	}
	if command == "git" && slices.Contains(args, "hash-object") && slices.Contains(args, "-w") {
		r.hashes++
		return r.result, r.err
	}
	return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
}

func TestCandidateBatchHashFailureLeavesIndexAndBranchUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		failure      error
	}{
		{"missing object", "", nil}, {"invalid object", "not-an-object\nnot-an-object\n", nil}, {"command failure", "", fmt.Errorf("hash failed")}, {"cancellation", "", context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initGitRepo(t)
			metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "failed_batch", BranchPrefix: "runner", BaseRef: "HEAD"}))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("retained dirty work\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			indexPath := filepath.Join(metadata.gitDirectory, "index")
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			runner := &candidateHashFailureRunner{result: subprocess.Result{Stdout: tc.output}, err: tc.failure}
			_, constructErr := NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Failed hash batch")
			if constructErr == nil {
				t.Fatal("failed or incomplete batch was accepted")
			}
			if tc.failure != nil && !errors.Is(constructErr, tc.failure) {
				t.Fatalf("hash failure lost its cause: %v", constructErr)
			}
			after, err := os.ReadFile(indexPath)
			if err != nil || string(after) != string(before) || runner.updates != 0 || runner.hashes != 1 {
				t.Fatalf("hash failure changed/retried index work: hashes=%d updates=%d error=%v", runner.hashes, runner.updates, err)
			}
			if head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")); head != metadata.BaseRevision {
				t.Fatal("failed hashing advanced the branch")
			}
		})
	}
}

type candidateVerifyMutationRunner struct {
	path    string
	mutated bool
}

func (r *candidateVerifyMutationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	if err == nil && command == "git" && slices.Contains(args, "write-tree") && !r.mutated {
		r.mutated = true
		err = os.WriteFile(r.path, []byte("intervening bytes\n"), 0o644)
	}
	return result, err
}

func TestCandidateBatchVerificationRehashesAfterStaging(t *testing.T) {
	repo := initGitRepo(t)
	metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "changed_after_staging", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatal(err)
	}
	runner := &candidateVerifyMutationRunner{path: filepath.Join(metadata.WorktreePath, "README.md")}
	if _, err := NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Must rehash"); err == nil || !strings.Contains(err.Error(), "does not match its literal worktree bytes and mode") {
		t.Fatalf("staging hash was reused as fresh verification: %v", err)
	}
}

func TestCandidateBatchRefusesUnsupportedEntriesBeforeAdvancingBranch(t *testing.T) {
	for _, kind := range []string{"uninitialized gitlink", ".gitmodules symlink"} {
		t.Run(kind, func(t *testing.T) {
			repo := initGitRepo(t)
			metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "unsupported_batch", BranchPrefix: "runner", BaseRef: "HEAD"}))
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "uninitialized gitlink":
				if err := os.Mkdir(filepath.Join(metadata.WorktreePath, "module"), 0o755); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, metadata.WorktreePath, "update-index", "--add", "--cacheinfo", "160000", metadata.BaseRevision, "module")
			case ".gitmodules symlink":
				if err := os.Symlink("untrusted target", filepath.Join(metadata.WorktreePath, ".gitmodules")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewGitProvider(nil).ConstructCandidate(t.Context(), metadata, "Refuse unsafe entry"); err == nil {
				t.Fatal("unsupported entry became a candidate")
			}
			if head := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "rev-parse", "HEAD")); head != metadata.BaseRevision {
				t.Fatal("refused batch advanced the branch")
			}
		})
	}
}

func TestCandidateBatchRefusesInventoriedSpecialFile(t *testing.T) {
	repo := initGitRepo(t)
	metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "special_batch", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(metadata.WorktreePath, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Git does not inventory an untracked FIFO. Exercise the path-classification
	// boundary directly: batching must never hand an inventoried special file
	// to hash-object, which could block on opening it.
	if err := NewGitProvider(nil).stageCandidatePaths(t.Context(), mustCandidateProfile(t, metadata), []string{"pipe"}); err == nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("inventoried special file was opened or staged: %v", err)
	}
}

func TestCandidateBatchRetriesOnlyTheContendedLaterIndexWrite(t *testing.T) {
	repo := initGitRepo(t)
	metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "later_index_batch", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatal(err)
	}
	for i := range candidateBatchPaths + 1 {
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, fmt.Sprintf("file-%03d", i)), []byte("raw bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lockPath := filepath.Join(metadata.gitDirectory, "index.lock")
	runner := &indexLockRunner{}
	runner.after = func() {
		switch runner.calls {
		case 1:
			if err := os.WriteFile(lockPath, []byte("owned concurrent refresh\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		case 2:
			if err := os.Remove(lockPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Resume later batch"); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 3 || !reflect.DeepEqual(runner.arguments[1], runner.arguments[2]) || reflect.DeepEqual(runner.arguments[0], runner.arguments[1]) {
		t.Fatalf("index contention did not resume only the exact refused batch: %d calls", runner.calls)
	}
	if got := runGitTest(t, metadata.WorktreePath, "status", "--porcelain"); got != "" {
		t.Fatalf("batch retry lost candidate paths: %q", got)
	}
}

type candidateWindowsArgvRunner struct{ maxChars, indexCalls int }

func (r *candidateWindowsArgvRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		// This fixture is ASCII without quotes or backslashes. Windows adds
		// exactly two quoting characters around arguments containing spaces.
		// Include the executable, full privileged prefix, separators and NUL.
		chars := len("git.exe") + 1
		for _, arg := range args {
			arg = strings.ReplaceAll(arg, os.DevNull, "NUL")
			if strings.ContainsAny(arg, "\"\\") || strings.IndexFunc(arg, func(c rune) bool { return c > 127 }) >= 0 {
				return subprocess.Result{}, fmt.Errorf("unexpected non-fixture Windows argument %q", arg)
			}
			chars += 1 + len(arg)
			if arg == "" || strings.ContainsAny(arg, " \t") {
				chars += 2
			}
		}
		if chars > r.maxChars {
			r.maxChars = chars
		}
		if slices.Contains(args, "update-index") {
			r.indexCalls++
		}
		if chars > 32767 {
			return subprocess.Result{}, fmt.Errorf("calculated Windows command line has %d characters including NUL; limit 32767", chars)
		}
	}
	return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
}

func TestCandidateBatchFitsQuotedWindowsCommandLine(t *testing.T) {
	repo := initEmptyGitRepo(t)
	metadata, err := NewGitProvider(nil).Prepare(t.Context(), boundRequest(Request{WorkingDir: repo, WorktreeRoot: filepath.Join(t.TempDir(), "worktrees"), WorkID: "windows_boundary", BranchPrefix: "runner", BaseRef: "HEAD"}))
	if err != nil {
		t.Fatal(err)
	}
	// All 116 paths fit the former 32 KiB entry-only budget, but the complete
	// quoted Windows command exceeded CreateProcessW's 32767-character limit.
	for i := range 116 {
		name := fmt.Sprintf("%03d ", i) + strings.Repeat("x", 216)
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, name), []byte("literal bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runner := &candidateWindowsArgvRunner{}
	_, err = NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Bound complete quoted arguments")
	t.Logf("calculated_windows_chars_including_NUL=%d index_batches=%d runtime=host_not_Windows", runner.maxChars, runner.indexCalls)
	if err != nil {
		t.Fatal(err)
	}
	if runner.indexCalls < 2 || runner.maxChars > 32767 {
		t.Fatalf("entry batching did not leave Windows prefix/quoting headroom: batches=%d chars=%d", runner.indexCalls, runner.maxChars)
	}
}
