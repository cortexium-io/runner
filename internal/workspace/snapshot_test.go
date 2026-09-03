package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func captureDefaultSnapshot(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration) (string, error) {
	return CaptureSnapshotWithLimits(ctx, run, worktreePath, timeout, DefaultSnapshotLimits())
}

func captureDefaultSnapshotState(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration) (Snapshot, error) {
	return CaptureSnapshotStateWithLimits(ctx, run, worktreePath, timeout, DefaultSnapshotLimits())
}

func captureDefaultCheckoutSnapshotState(ctx context.Context, run subprocess.Runner, worktreePath string, timeout time.Duration) (Snapshot, error) {
	return CaptureCheckoutSnapshotStateWithLimits(ctx, run, worktreePath, timeout, DefaultSnapshotLimits())
}

func TestSnapshotIndexRejectsEntryBeforeAggregateMapGrowth(t *testing.T) {
	object := strings.Repeat("a", 40)
	value := "H 100644 " + object + " 0\tone\x00H 100644 " + object + " 0\ttwo\x00H 100644 " + object + " 0\tthree\x00"
	budget, err := securefs.NewSnapshotBudget(SnapshotLimits{MaxEntries: 2, MaxFileBytes: 10, MaxTotalBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := parseSnapshotIndexWithBudget(value, budget, "/repo")
	if err == nil || entries != nil || !strings.Contains(err.Error(), "maximum entries limit 2") || !strings.Contains(err.Error(), "next count 3") {
		t.Fatalf("index entry overflow was accepted: entries=%#v error=%v", entries, err)
	}
}

type snapshotMutationRunner struct {
	subprocess.Runner
	match  string
	once   sync.Once
	mutate func()
}

func (r *snapshotMutationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && strings.Contains(strings.Join(args, " "), r.match) {
		r.once.Do(r.mutate)
	}
	return r.Runner.Run(ctx, command, args, dir, timeout)
}

type snapshotPathOutputRunner struct {
	subprocess.Runner
	paths string
}

func (r snapshotPathOutputRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && strings.Join(args, " ") == "--no-optional-locks ls-files --modified --deleted --others --exclude-standard -z" {
		return subprocess.Result{Stdout: r.paths}, nil
	}
	return r.Runner.Run(ctx, command, args, dir, timeout)
}

func TestCaptureSnapshotDetectsUntrackedContentChangeWithSameStatus(t *testing.T) {
	repo := initGitRepo(t)
	path := filepath.Join(repo, "untracked.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture initial snapshot: %v", err)
	}
	if err := os.WriteFile(path, []byte("after!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture changed snapshot: %v", err)
	}
	if before == after {
		t.Fatal("snapshot ignored changed untracked content with the same porcelain status")
	}
}

func TestCaptureSnapshotIsStableForSymlinksDeletedFilesAndLiteralNames(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal(err)
	}
	literal := "line\nwith\ttab\\and-backslash.txt"
	if err := os.WriteFile(filepath.Join(repo, literal), []byte("literal content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing target\nwith tab", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}

	first, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture first snapshot: %v", err)
	}
	second, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture unchanged snapshot: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("unchanged snapshot changed: %q != %q", first.Fingerprint, second.Fingerprint)
	}
	if changed := first.ChangedPaths(second); len(changed) != 0 {
		t.Fatalf("unchanged snapshot reported paths: %#v", changed)
	}
	for _, path := range []string{"README.md", literal, "link"} {
		if _, ok := first.worktree[path]; !ok {
			t.Fatalf("snapshot omitted literal or deleted path %q: %#v", path, first.worktree)
		}
	}
}

func TestSnapshotPathsRequireLiteralNULTerminatedRelativePaths(t *testing.T) {
	valid := "line\nname\x00tab\tname\x00back\\slash\x00"
	if got, err := snapshotPaths(valid); err != nil || !reflect.DeepEqual(got, []string{"line\nname", "tab\tname", "back\\slash"}) {
		t.Fatalf("parse literal paths = %#v, error=%v", got, err)
	}
	if got, err := snapshotPaths("duplicate\x00duplicate\x00"); err != nil || !reflect.DeepEqual(got, []string{"duplicate"}) {
		t.Fatalf("valid Git category duplicate was not normalized: %#v, %v", got, err)
	}
	for _, value := range []string{"unterminated", "absolute\x00/escape\x00", "empty\x00\x00"} {
		paths, err := snapshotPaths(value)
		if value == "absolute\x00/escape\x00" {
			if err != nil {
				t.Fatalf("absolute path should be rejected by descriptor traversal, not NUL parsing: %v", err)
			}
			continue
		}
		if err == nil || paths != nil {
			t.Fatalf("ambiguous path output %q was accepted: %#v, %v", value, paths, err)
		}
	}
}

func TestCaptureSnapshotRejectsSymlinkedRootAndUnsafeGitPaths(t *testing.T) {
	repo := initGitRepo(t)
	linkedRoot := filepath.Join(t.TempDir(), "linked-root")
	if err := os.Symlink(repo, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if fingerprint, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, linkedRoot, 30*time.Second); err == nil || fingerprint != "" {
		t.Fatalf("symlinked repository root was accepted: fingerprint=%q error=%v", fingerprint, err)
	}

	outside := filepath.Join(filepath.Dir(repo), "outside-sentinel")
	if err := os.WriteFile(outside, []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.Symlink(filepath.Dir(repo), filepath.Join(repo, "linked")); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{outside, "../outside-sentinel", "linked/outside-sentinel", "nested//file"} {
		runner := snapshotPathOutputRunner{Runner: subprocess.OSRunner{}, paths: path + "\x00"}
		if fingerprint, err := captureDefaultSnapshot(t.Context(), runner, repo, 30*time.Second); err == nil || fingerprint != "" {
			t.Fatalf("unsafe Git path %q was accepted: fingerprint=%q error=%v", path, fingerprint, err)
		}
	}
}

func TestSnapshotChangedPathsReportsReviewerArtifacts(t *testing.T) {
	repo := initGitRepo(t)
	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture initial state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "review.spec.js"), []byte("test('review', () => {})\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	results := filepath.Join(repo, "test-results", "playthrough", "error-context.md")
	if err := os.MkdirAll(filepath.Dir(results), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(results, []byte("Game Paused\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture changed state: %v", err)
	}
	want := []string{"review.spec.js", "test-results/playthrough/error-context.md"}
	if got := before.ChangedPaths(after); !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %#v, want %#v", got, want)
	}
}

func TestSnapshotChangedPathsReportsCommittedFiles(t *testing.T) {
	repo := initGitRepo(t)
	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "review-output.txt"), []byte("unreviewed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "review-output.txt")
	runGitTest(t, repo, "commit", "-m", "add review output")
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := before.ChangedPaths(after), []string{"review-output.txt"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("changed paths = %#v, want %#v", got, want)
	}
}

func TestCaptureSnapshotDetectsCleanCommitAndBranchChanges(t *testing.T) {
	repo := initGitRepo(t)
	initial, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture initial snapshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("committed change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "commit", "-m", "change content")
	committed, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture committed snapshot: %v", err)
	}
	if initial == committed {
		t.Fatal("snapshot ignored a clean HEAD change")
	}
	runGitTest(t, repo, "branch", "same-commit")
	runGitTest(t, repo, "switch", "same-commit")
	branched, err := captureDefaultSnapshot(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture branch snapshot: %v", err)
	}
	if committed == branched {
		t.Fatal("snapshot ignored a branch change at the same commit")
	}
	runGitTest(t, repo, "switch", "--detach")
	detached, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture detached snapshot: %v", err)
	}
	repeated, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("repeat detached snapshot: %v", err)
	}
	if detached.Branch != "HEAD" || detached.Fingerprint != repeated.Fingerprint {
		t.Fatalf("detached snapshot was incomplete or unstable: branch=%q first=%q second=%q", detached.Branch, detached.Fingerprint, repeated.Fingerprint)
	}
}

func TestCaptureSnapshotAcceptsBranchWithAmbiguousShortName(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "branch", "-m", "ambiguous")
	runGitTest(t, repo, "tag", "ambiguous")

	first, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture branch whose short name is ambiguous: %v", err)
	}
	second, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("repeat branch whose short name is ambiguous: %v", err)
	}
	if first.Branch != "ambiguous" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("ambiguous short branch snapshot was incomplete or unstable: branch=%q first=%q second=%q", first.Branch, first.Fingerprint, second.Fingerprint)
	}
}

func TestCaptureSnapshotRecordsAssumeUnchangedAndSkipWorktreeFlags(t *testing.T) {
	repo := initGitRepo(t)
	baseline, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if entry := baseline.indexEntries["README.md"]; !strings.Contains(entry, "mode=100644 object=") || !strings.Contains(entry, " stage=0 ") {
		t.Fatalf("index entry omitted mode, object ID, or stage: %q", entry)
	}

	runGitTest(t, repo, "update-index", "--assume-unchanged", "README.md")
	assumed, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Fingerprint == assumed.Fingerprint || !strings.Contains(assumed.indexEntries["README.md"], "assume-unchanged=true skip-worktree=false") {
		t.Fatalf("assume-unchanged flag was not recorded: before=%q after=%q entry=%q", baseline.Fingerprint, assumed.Fingerprint, assumed.indexEntries["README.md"])
	}
	if got := baseline.ChangedControlState(assumed); !containsString(got, "Git index") {
		t.Fatalf("assume-unchanged drift categories = %#v", got)
	}

	runGitTest(t, repo, "update-index", "--no-assume-unchanged", "README.md")
	runGitTest(t, repo, "update-index", "--skip-worktree", "README.md")
	skipped, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Fingerprint == skipped.Fingerprint || !strings.Contains(skipped.indexEntries["README.md"], "assume-unchanged=false skip-worktree=true") {
		t.Fatalf("skip-worktree flag was not recorded: before=%q after=%q entry=%q", baseline.Fingerprint, skipped.Fingerprint, skipped.indexEntries["README.md"])
	}
}

func TestCaptureSnapshotPreservesLiteralTrackedPathsAndScalarWhitespace(t *testing.T) {
	repo := initGitRepo(t)
	literal := " leading space\tand\nnewline trailing "
	if err := os.WriteFile(filepath.Join(repo, literal), []byte("literal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--", literal)
	runGitTest(t, repo, "commit", "-m", "add literal path")
	snapshot, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.indexEntries[literal]; !ok {
		t.Fatalf("literal index path was not preserved: %#v", snapshot.indexEntries)
	}
	if got, err := snapshotScalar("  value\ninside  \n", "test value"); err != nil || got != "  value\ninside  " {
		t.Fatalf("single-value protocol whitespace was changed: value=%q error=%v", got, err)
	}
}

func TestCaptureSnapshotIncludesCommonAndEnabledPerWorktreeConfig(t *testing.T) {
	repo := initGitRepo(t)
	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "snapshot.common", "one")
	afterCommon, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == afterCommon.Fingerprint || !containsString(before.ChangedControlState(afterCommon), "common Git config") {
		t.Fatalf("common config mutation did not identify its control-state category: %#v", before.ChangedControlState(afterCommon))
	}

	runGitTest(t, repo, "config", "extensions.worktreeConfig", "true")
	runGitTest(t, repo, "config", "--worktree", "snapshot.local", "one")
	beforeWorktree, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "--worktree", "snapshot.local", "two")
	afterWorktree, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if beforeWorktree.Fingerprint == afterWorktree.Fingerprint || !containsString(beforeWorktree.ChangedControlState(afterWorktree), "per-worktree Git config") {
		t.Fatalf("per-worktree config mutation did not identify its control-state category: %#v", beforeWorktree.ChangedControlState(afterWorktree))
	}
}

func TestCheckoutSnapshotIgnoresOnlyBranchTrackingConfig(t *testing.T) {
	repo := initGitRepo(t)
	fullBefore, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkoutBefore, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "branch.unrelated.remote", "origin")
	runGitTest(t, repo, "config", "branch.unrelated.merge", "refs/heads/unrelated")
	fullAfter, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	checkoutAfter, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if fullBefore.Fingerprint == fullAfter.Fingerprint {
		t.Fatal("complete snapshot ignored branch tracking config")
	}
	if checkoutBefore.Fingerprint != checkoutAfter.Fingerprint {
		t.Fatalf("checkout snapshot changed for unrelated branch tracking: before=%q after=%q", checkoutBefore.Fingerprint, checkoutAfter.Fingerprint)
	}
	runGitTest(t, repo, "config", "core.hooksPath", filepath.Join(t.TempDir(), "hooks"))
	checkoutSecurityChanged, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if checkoutAfter.Fingerprint == checkoutSecurityChanged.Fingerprint {
		t.Fatal("checkout snapshot ignored security-relevant config")
	}
}

func TestCaptureSnapshotUsesLiteralCommonConfigForWorktreeConfigEnablement(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "config", "extensions.worktreeConfig", "true")
	runGitTest(t, repo, "config", "--worktree", "snapshot.local", "one")
	externalConfig := filepath.Join(t.TempDir(), "included-config")
	if err := os.WriteFile(externalConfig, []byte("[extensions]\n\tworktreeConfig = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "--add", "include.path", externalConfig)
	if got := snapshotScalarForTest(t, runGitTest(t, repo, "config", "--worktree", "--get", "snapshot.local")); got != "one" {
		t.Fatalf("Git stopped using enabled per-worktree config after external include: %q", got)
	}

	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "config", "--worktree", "snapshot.local", "two")
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint == after.Fingerprint || !containsString(before.ChangedControlState(after), "per-worktree Git config") {
		t.Fatalf("externally overridden probe concealed enabled per-worktree config drift: %#v", before.ChangedControlState(after))
	}
}

func TestCaptureSnapshotDistinguishesDisabledAndEnabledMissingWorktreeConfig(t *testing.T) {
	repo := initGitRepo(t)
	disabled, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := disabled.controlState["per-worktree Git config"]; got != "disabled" {
		t.Fatalf("disabled per-worktree config state = %q", got)
	}

	runGitTest(t, repo, "config", "extensions.worktreeConfig", "true")
	enabled, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := enabled.controlState["per-worktree Git config"]; got != "enabled:missing" {
		t.Fatalf("enabled missing per-worktree config state = %q", got)
	}
	if !containsString(disabled.ChangedControlState(enabled), "per-worktree Git config") {
		t.Fatalf("enablement change was not identified: %#v", disabled.ChangedControlState(enabled))
	}
}

func TestCaptureSnapshotIgnoresDisabledPerWorktreeConfig(t *testing.T) {
	repo := initGitRepo(t)
	config := filepath.Join(repo, ".git", "config.worktree")
	if err := os.WriteFile(config, []byte("[snapshot]\n\tlocal = one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte("[snapshot]\n\tlocal = two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if before.Fingerprint != after.Fingerprint {
		t.Fatalf("disabled per-worktree config changed snapshot: %q != %q; categories=%#v", before.Fingerprint, after.Fingerprint, before.ChangedControlState(after))
	}
}

func TestCaptureSnapshotLinkedWorktreeIdentityIsStableAndIgnoresOtherRegistrations(t *testing.T) {
	repo := initGitRepo(t)
	root := t.TempDir()
	linked := filepath.Join(root, " linked\tworktree\ntrailing ")
	runGitTest(t, repo, "worktree", "add", "-b", "snapshot-linked", linked)

	first, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, linked, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	second, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, linked, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("unchanged linked worktree snapshot was unstable: %q != %q", first.Fingerprint, second.Fingerprint)
	}

	unrelated := filepath.Join(root, "unrelated")
	runGitTest(t, repo, "worktree", "add", "-b", "snapshot-unrelated", unrelated)
	afterUnrelated, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, linked, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint != afterUnrelated.Fingerprint {
		t.Fatalf("unrelated worktree registration changed current identity: %q != %q", first.Fingerprint, afterUnrelated.Fingerprint)
	}
}

func TestCurrentWorktreeRegistrationPreservesLiteralRoot(t *testing.T) {
	root := "/tmp/ leading\tworktree\ntrailing "
	record := "worktree " + root + "\x00HEAD abc123\x00branch refs/heads/literal\x00\x00"
	got, err := currentWorktreeRegistration(record, root, "abc123", "refs/heads/literal")
	if err != nil || got != strings.TrimSuffix(record, "\x00\x00") {
		t.Fatalf("literal worktree registration = %q, error=%v", got, err)
	}
}

func TestCaptureSnapshotRejectsAdministrativeFileReplacement(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*testing.T, string) string
		want      string
	}{
		{name: "common config", configure: func(_ *testing.T, repo string) string {
			return filepath.Join(repo, ".git", "config")
		}, want: "verify common Git config"},
		{name: "per-worktree config", configure: func(t *testing.T, repo string) string {
			runGitTest(t, repo, "config", "extensions.worktreeConfig", "true")
			runGitTest(t, repo, "config", "--worktree", "snapshot.local", "one")
			return filepath.Join(repo, ".git", "config.worktree")
		}, want: "verify per-worktree Git config"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := initGitRepo(t)
			config := test.configure(t, repo)
			runner := &snapshotMutationRunner{
				Runner: subprocess.OSRunner{},
				match:  "status --porcelain=v1",
				mutate: func() {
					content, err := os.ReadFile(config)
					if err != nil {
						t.Errorf("read config before replacement: %v", err)
						return
					}
					if err := os.Rename(config, config+".replaced"); err != nil {
						t.Errorf("rename config: %v", err)
						return
					}
					if err := os.WriteFile(config, content, 0o600); err != nil {
						t.Errorf("replace config: %v", err)
					}
				},
			}
			if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("administrative replacement was certified: snapshot=%#v error=%v", snapshot, err)
			}
		})
	}
}

func TestCaptureSnapshotRejectsSymbolicHEADReferenceMutation(t *testing.T) {
	repo := initGitRepo(t)
	previous, target, loose := addSnapshotCommit(t, repo)
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			if err := os.WriteFile(loose, []byte(previous+"\n"), 0o600); err != nil {
				t.Errorf("mutate loose symbolic reference %q: %v", target, err)
			}
		},
	}

	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "verify symbolic HEAD reference") {
		t.Fatalf("symbolic HEAD reference mutation was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotAllowsUnrelatedSiblingBranchMutation(t *testing.T) {
	repo := initGitRepo(t)
	runGitTest(t, repo, "checkout", "-b", "runner/current")
	current := strings.TrimSuffix(runGitTest(t, repo, "rev-parse", "--verify", "HEAD"), "\n")
	sibling := filepath.Join(repo, ".git", "refs", "heads", "runner", "unrelated")
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			if err := os.WriteFile(sibling, []byte(current+"\n"), 0o600); err != nil {
				t.Errorf("create unrelated sibling branch: %v", err)
			}
		},
	}

	snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second)
	if err != nil || snapshot.Fingerprint == "" || snapshot.Branch != "runner/current" {
		t.Fatalf("unrelated sibling branch blocked current snapshot: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotPinsPackedSymbolicHEADReferenceFallback(t *testing.T) {
	repo := initGitRepo(t)
	previous, target, loose := addSnapshotCommit(t, repo)
	current := strings.TrimSuffix(runGitTest(t, repo, "rev-parse", "--verify", "HEAD"), "\n")
	runGitTest(t, repo, "pack-refs", "--all")
	if _, err := os.Stat(loose); !os.IsNotExist(err) {
		t.Fatalf("loose reference still exists after pack-refs: %v", err)
	}

	first, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture packed-reference snapshot: %v", err)
	}
	second, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil || first.Fingerprint != second.Fingerprint {
		t.Fatalf("packed-reference snapshot was unstable: first=%q second=%q error=%v", first.Fingerprint, second.Fingerprint, err)
	}

	packedPath := filepath.Join(repo, ".git", "packed-refs")
	packed, err := os.ReadFile(packedPath)
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := []byte(current + " " + target + "\n")
	newRecord := []byte(previous + " " + target + "\n")
	if !strings.Contains(string(packed), string(oldRecord)) {
		t.Fatalf("packed reference has no current HEAD record %q", oldRecord)
	}
	runner := &snapshotMutationRunner{
		Runner: subprocess.OSRunner{},
		match:  "ls-files --modified --deleted --others",
		mutate: func() {
			changed := strings.Replace(string(packed), string(oldRecord), string(newRecord), 1)
			if err := os.WriteFile(packedPath, []byte(changed), 0o600); err != nil {
				t.Errorf("mutate packed symbolic reference: %v", err)
			}
		},
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), runner, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "verify packed-reference fallback") {
		t.Fatalf("packed HEAD reference mutation was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func TestCaptureSnapshotFollowsStableSymbolicReferenceChain(t *testing.T) {
	repo := initGitRepo(t)
	target := strings.TrimSuffix(runGitTest(t, repo, "symbolic-ref", "HEAD"), "\n")
	const alias = "refs/heads/snapshot-alias"
	runGitTest(t, repo, "symbolic-ref", alias, target)
	runGitTest(t, repo, "symbolic-ref", "HEAD", alias)

	first, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture symbolic-reference chain: %v", err)
	}
	second, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("repeat symbolic-reference chain snapshot: %v", err)
	}
	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("unchanged symbolic-reference chain was unstable: %q != %q", first.Fingerprint, second.Fingerprint)
	}

	const secondAlias = "refs/heads/snapshot-alias-two"
	runGitTest(t, repo, "symbolic-ref", secondAlias, target)
	if err := os.WriteFile(filepath.Join(repo, ".git", filepath.FromSlash(alias)), []byte("ref: "+secondAlias+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retargeted, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second)
	if err != nil {
		t.Fatalf("capture retargeted symbolic-reference chain: %v", err)
	}
	if second.Fingerprint == retargeted.Fingerprint {
		t.Fatal("symbolic-reference chain target changed without changing the snapshot")
	}
}

func addSnapshotCommit(t *testing.T, repo string) (previous, target, loose string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "commit", "-m", "Second commit")
	previous = strings.TrimSuffix(runGitTest(t, repo, "rev-parse", "--verify", "HEAD^"), "\n")
	target = strings.TrimSuffix(runGitTest(t, repo, "symbolic-ref", "HEAD"), "\n")
	return previous, target, filepath.Join(repo, ".git", filepath.FromSlash(target))
}

func TestCaptureSnapshotRejectsSymlinkedCommonConfig(t *testing.T) {
	repo := initGitRepo(t)
	config := filepath.Join(repo, ".git", "config")
	original := config + ".original"
	if err := os.Rename(config, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(original, config); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := captureDefaultSnapshotState(t.Context(), subprocess.OSRunner{}, repo, 30*time.Second); err == nil || snapshot.Fingerprint != "" || !strings.Contains(err.Error(), "pin common Git config") {
		t.Fatalf("symlinked common config was certified: snapshot=%#v error=%v", snapshot, err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func snapshotScalarForTest(t *testing.T, value string) string {
	t.Helper()
	parsed, err := snapshotScalar(value, "test Git value")
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
