package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

// Map only the literal network URL after the production privileged boundary
// has built its arguments and environment. All Git behavior remains native.
type planningTestRunner struct {
	remote        string
	mu            sync.Mutex
	fetches       []string
	afterFetch    func(context.Context, int) error
	afterCheckout func() error
	fetchErr      error
	removeErr     error
}

func (r *planningTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command != "git" {
		return subprocess.Result{}, errors.New("unexpected non-Git planning command")
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " worktree remove ") && r.removeErr != nil {
		return subprocess.Result{}, r.removeErr
	}
	if strings.Contains(joined, " fetch ") && r.fetchErr != nil {
		return subprocess.Result{}, r.fetchErr
	}
	fetchIndex := -1
	if strings.Contains(joined, " fetch ") {
		r.mu.Lock()
		fetchIndex = len(r.fetches)
		r.fetches = append(r.fetches, joined)
		r.mu.Unlock()
	}
	mapped := append([]string(nil), args...)
	for i := range mapped {
		if mapped[i] == "https://github.com/owner/repo.git" {
			mapped[i] = r.remote
		}
		if mapped[i] == "protocol.https.allow=always" {
			mapped[i] = "protocol.file.allow=always"
		}
	}
	result, err := (subprocess.OSRunner{}).Run(ctx, command, mapped, dir, timeout)
	if err == nil && strings.Contains(joined, " reset --hard ") && r.afterCheckout != nil {
		err = r.afterCheckout()
	}
	if err == nil && fetchIndex >= 0 && r.afterFetch != nil {
		err = r.afterFetch(ctx, fetchIndex)
	}
	return result, err
}

type planningFixture struct {
	repo, remote, updater string
	a, b, tree            string
	runner                *planningTestRunner
	provider              GitProvider
}

func newPlanningFixture(t *testing.T) planningFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	f := planningFixture{repo: initGitRepo(t), remote: filepath.Join(root, "origin.git"), updater: filepath.Join(root, "updater")}
	runGitTest(t, f.repo, "branch", "-M", "main")
	f.a = strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD"))
	runGitTest(t, "", "init", "--bare", f.remote)
	runGitTest(t, f.repo, "remote", "add", "origin", "https://github.com/Owner/Repo.git")
	runGitTest(t, f.repo, "push", f.remote, "main")
	runGitTest(t, f.repo, "update-ref", "refs/remotes/origin/main", f.a)
	runGitTest(t, "", "clone", "-b", "main", f.remote, f.updater)
	runGitTest(t, f.updater, "config", "user.email", "runner-test@example.invalid")
	runGitTest(t, f.updater, "config", "user.name", "Runner Test")
	writeSnapshotTestFile(t, filepath.Join(f.updater, ".gitignore"), "ignored.txt\n")
	f.b = f.advance(t, "remote B\n")
	f.tree = strings.TrimSpace(runGitTest(t, f.updater, "rev-parse", "HEAD^{tree}"))
	f.runner = &planningTestRunner{remote: f.remote}
	f.provider = NewGitProvider(f.runner)
	return f
}

func (f planningFixture) advance(t *testing.T, content string) string {
	t.Helper()
	writeSnapshotTestFile(t, filepath.Join(f.updater, "README.md"), content)
	runGitTest(t, f.updater, "add", "--all")
	runGitTest(t, f.updater, "commit", "-m", "Advance destination")
	runGitTest(t, f.updater, "push", "origin", "main")
	return strings.TrimSpace(runGitTest(t, f.updater, "rev-parse", "HEAD"))
}

func (f planningFixture) prepare(t *testing.T) PlanningWorkspace {
	t.Helper()
	w, err := f.provider.PreparePlanningWorkspace(t.Context(), f.repo, "origin", "main", "Owner/Repo")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(w.checkout.parent); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := w.Cleanup(ctx); err != nil {
				t.Errorf("cleanup planning fixture: %v", err)
			}
		}
	})
	return w
}

func TestPlanningSourceValidationAndJSON(t *testing.T) {
	source := PlanningSource{"owner/repo", "release/main", strings.Repeat("a", 40), strings.Repeat("b", 40)}
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"repository":"owner/repo","destination_branch":"release/main","commit_oid":"` + source.CommitOID + `","tree_oid":"` + source.TreeOID + `"}`
	if string(encoded) != want {
		t.Fatalf("source JSON = %s, want %s", encoded, want)
	}
	for _, test := range []struct {
		name string
		edit func(*PlanningSource)
	}{
		{"missing", func(s *PlanningSource) { *s = PlanningSource{} }},
		{"URL", func(s *PlanningSource) { s.Repository = "https://github.com/owner/repo" }},
		{"case", func(s *PlanningSource) { s.Repository = "Owner/Repo" }},
		{"URL escape", func(s *PlanningSource) { s.Repository = "owner/repo?other" }},
		{"commit", func(s *PlanningSource) { s.CommitOID = "HEAD" }},
		{"zero commit", func(s *PlanningSource) { s.CommitOID = strings.Repeat("0", 40) }},
		{"tree", func(s *PlanningSource) { s.TreeOID = strings.Repeat("B", 40) }},
		{"mixed hashes", func(s *PlanningSource) { s.TreeOID = strings.Repeat("b", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := source
			test.edit(&changed)
			if changed.Validate() == nil {
				t.Fatalf("invalid source accepted: %+v", changed)
			}
		})
	}
	for _, branch := range []string{"", "HEAD", "refs/heads/main", "-main", "main:other", "main..other", "main*", "main@{1}", "main/", ".main", "main.lock", "main//other", "main\nother"} {
		changed := source
		changed.DestinationBranch = branch
		if changed.Validate() == nil {
			t.Errorf("invalid branch accepted: %q", branch)
		}
	}
	source.CommitOID, source.TreeOID = strings.Repeat("a", 64), strings.Repeat("b", 64)
	if err := source.Validate(); err != nil {
		t.Fatalf("SHA256 source rejected: %v", err)
	}
}

func TestPlanningWorkspaceFreshRemotePreservesDirtyOperatorCheckout(t *testing.T) {
	f := newPlanningFixture(t)
	writeSnapshotTestFile(t, filepath.Join(f.repo, "README.md"), "staged operator edit\n")
	runGitTest(t, f.repo, "add", "README.md")
	writeSnapshotTestFile(t, filepath.Join(f.repo, "README.md"), "unstaged operator edit\n")
	writeSnapshotTestFile(t, filepath.Join(f.repo, "untracked.txt"), "operator untracked\n")
	before, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, f.repo, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(f.repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	w := f.prepare(t)
	want := PlanningSource{"owner/repo", "main", f.b, f.tree}
	if w.Source != want || w.Source.CommitOID == f.a {
		t.Fatalf("planning source = %+v, want remote B %+v", w.Source, want)
	}
	if got := runGitTest(t, w.Path, "show", "HEAD:README.md"); got != "remote B\n" {
		t.Fatalf("planning content = %q", got)
	}
	if err := w.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := f.advance(t, "remote C\n")
	observed, err := f.provider.ObservePlanningSource(t.Context(), f.repo, "origin", "main", "owner/repo")
	if err != nil || observed.CommitOID != c {
		t.Fatalf("fresh observation = %+v, error %v", observed, err)
	}
	if err := w.Verify(t.Context()); err != nil || w.Source != want {
		t.Fatalf("a later remote fetch changed the pinned workspace: %+v, %v", w.Source, err)
	}
	if err := w.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.checkout.parent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private parent remains after cleanup: %v", err)
	}
	after, err := captureDefaultCheckoutSnapshotState(t.Context(), subprocess.OSRunner{}, f.repo, 30*time.Second)
	if err != nil || before.Fingerprint != after.Fingerprint {
		t.Fatalf("operator checkout changed: before=%+v after=%+v error=%v", before, after, err)
	}
	afterIndex, err := os.ReadFile(filepath.Join(f.repo, ".git", "index"))
	if err != nil || string(index) != string(afterIndex) {
		t.Fatalf("operator index changed: %v", err)
	}
	if got := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "refs/heads/main", "refs/remotes/origin/main")); got != f.a+"\n"+f.a {
		t.Fatalf("operator branch or tracking ref changed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".git", "FETCH_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared FETCH_HEAD was written: %v", err)
	}
	if got := runGitTest(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/runner/planning/"); got != "" {
		t.Fatalf("temporary planning refs remain: %s", got)
	}
}

func TestPlanningSourceConcurrentFetchesPinIndependentRefsWithoutWorktrees(t *testing.T) {
	f := newPlanningFixture(t)
	firstFetched, resumeFirst := make(chan struct{}), make(chan struct{})
	f.runner.afterFetch = func(ctx context.Context, index int) error {
		if index != 0 {
			return nil
		}
		close(firstFetched)
		select {
		case <-resumeFirst:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	type observation struct {
		source PlanningSource
		err    error
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	first := make(chan observation, 1)
	go func() {
		source, err := f.provider.ObservePlanningSource(ctx, f.repo, "origin", "main", "owner/repo")
		first <- observation{source, err}
	}()
	select {
	case <-firstFetched:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	c := f.advance(t, "concurrent remote C\n")
	second, err := f.provider.ObservePlanningSource(ctx, f.repo, "origin", "main", "owner/repo")
	close(resumeFirst)
	if err != nil || second.CommitOID != c {
		t.Fatalf("second observation = %+v, error %v", second, err)
	}
	got := <-first
	if got.err != nil || got.source.CommitOID != f.b {
		t.Fatalf("concurrent fetch displaced first source B: %+v", got)
	}
	if len(f.runner.fetches) != 2 || f.runner.fetches[0] == f.runner.fetches[1] {
		t.Fatalf("fetches did not use unique refs: %v", f.runner.fetches)
	}
	for _, fetch := range f.runner.fetches {
		for _, required := range []string{"--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "https://github.com/owner/repo.git +refs/heads/main:refs/runner/planning/"} {
			if !strings.Contains(fetch, required) {
				t.Errorf("fetch lacks %q: %s", required, fetch)
			}
		}
	}
	if got := runGitTest(t, f.repo, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("Observe created a private worktree: %s", got)
	}
	root, err := reviewWorkspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Observe materialized private workspace storage: %v", err)
	}
}

func TestPlanningFetchFailureAndMissingDestinationFailClosed(t *testing.T) {
	for _, test := range []struct {
		name   string
		branch string
		err    error
	}{
		{"missing destination", "missing", nil},
		{"fetch failure", "main", errors.New("network unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPlanningFixture(t)
			f.runner.fetchErr = test.err
			w, err := f.provider.PreparePlanningWorkspace(t.Context(), f.repo, "origin", test.branch, "owner/repo")
			if err == nil || w.Path != "" {
				t.Fatalf("stale local source used after failed fetch: %+v, %v", w, err)
			}
			source, err := f.provider.ObservePlanningSource(t.Context(), f.repo, "origin", test.branch, "owner/repo")
			if err == nil || source != (PlanningSource{}) {
				t.Fatalf("failed observation produced source: %+v, %v", source, err)
			}
			if got := runGitTest(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/runner/planning/"); got != "" {
				t.Fatalf("failed fetch retained ref: %s", got)
			}
		})
	}
}

func TestPlanningWorkspaceVerifyRefusesMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, planningFixture, *PlanningWorkspace)
	}{
		{"tracked", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			writeSnapshotTestFile(t, filepath.Join(w.Path, "README.md"), "changed\n")
		}},
		{"untracked", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			writeSnapshotTestFile(t, filepath.Join(w.Path, "new.txt"), "changed\n")
		}},
		{"ignored", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			writeSnapshotTestFile(t, filepath.Join(w.Path, "ignored.txt"), "changed\n")
		}},
		{"clean different commit", func(t *testing.T, f planningFixture, w *PlanningWorkspace) {
			runGitTest(t, w.Path, "reset", "--hard", f.a)
		}},
		{"attached HEAD", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			runGitTest(t, w.Path, "switch", "-c", "unexpected")
		}},
		{"hidden index edit", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			runGitTest(t, w.Path, "update-index", "--assume-unchanged", "README.md")
			writeSnapshotTestFile(t, filepath.Join(w.Path, "README.md"), "concealed\n")
		}},
		{"configuration", func(t *testing.T, f planningFixture, _ *PlanningWorkspace) {
			runGitTest(t, f.repo, "config", "runner.changed", "true")
		}},
		{"source DTO", func(_ *testing.T, _ planningFixture, w *PlanningWorkspace) {
			w.Source.Repository = "other/repo"
		}},
		{"public path", func(_ *testing.T, f planningFixture, w *PlanningWorkspace) {
			w.Path = f.repo
		}},
		{"Git administration redirect", func(t *testing.T, f planningFixture, w *PlanningWorkspace) {
			marker := filepath.Join(w.Path, ".git")
			original, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.WriteFile(marker, original, 0o644) })
			writeSnapshotTestFile(t, marker, "gitdir: "+filepath.Join(f.repo, ".git")+"\n")
		}},
		{"same path directory replacement", func(t *testing.T, _ planningFixture, w *PlanningWorkspace) {
			path := w.profile.ObjectDirectory
			if err := os.Rename(path, path+"-original"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = os.Remove(path)
				_ = os.Rename(path+"-original", path)
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPlanningFixture(t)
			w := f.prepare(t)
			test.mutate(t, f, &w)
			if err := w.Verify(t.Context()); err == nil {
				t.Fatal("Verify accepted mutated checkout or provenance")
			}
		})
	}
}

func TestPlanningWorkspaceCleanupPreservesOnFailureAndOnlyRemovesPrivateCheckout(t *testing.T) {
	f := newPlanningFixture(t)
	w := f.prepare(t)
	other := f.prepare(t)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := w.Cleanup(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cleanup = %v", err)
	}
	f.runner.removeErr = &subprocess.CleanupError{Err: errors.New("child still running")}
	if err := w.Cleanup(t.Context()); err == nil {
		t.Fatal("unresolved cleanup succeeded")
	}
	f.runner.removeErr = nil
	if err := w.Verify(t.Context()); err != nil {
		t.Fatalf("failed cleanup removed or changed retained workspace: %v", err)
	}
	w.Path = f.repo // Exported metadata cannot select the cleanup target.
	if err := w.Cleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := other.Verify(t.Context()); err != nil {
		t.Fatalf("cleanup damaged sibling planning workspace: %v", err)
	}
	if got := strings.TrimSpace(runGitTest(t, f.repo, "rev-parse", "HEAD")); got != f.a {
		t.Fatalf("cleanup touched operator checkout: %s", got)
	}
}

func TestPlanningPreparationFailureRetainsOnlyUnresolvedWorkspace(t *testing.T) {
	for _, test := range []struct {
		name     string
		failure  error
		cancel   bool
		retained bool
	}{
		{"ordinary failure", errors.New("checkout response failed"), false, false},
		{"cancelled after checkout", context.Canceled, true, false},
		{"unresolved checkout process", &subprocess.CleanupError{Err: errors.New("child survived")}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPlanningFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			f.runner.afterCheckout = func() error {
				if test.cancel {
					cancel()
				}
				return test.failure
			}
			w, err := f.provider.PreparePlanningWorkspace(ctx, f.repo, "origin", "main", "owner/repo")
			if !errors.Is(err, test.failure) || (w.Path != "") != test.retained {
				t.Fatalf("preparation path=%q error=%v, want retained=%t failure=%v", w.Path, err, test.retained, test.failure)
			}
			if test.retained {
				if _, err := os.Stat(w.Path); err != nil {
					t.Fatalf("unresolved workspace was deleted: %v", err)
				}
				if err := w.Cleanup(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			root, err := reviewWorkspaceRoot()
			if err != nil {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed preparation leaked private parents: %v, %v", entries, err)
			}
			if got := runGitTest(t, f.repo, "for-each-ref", "--format=%(refname)", "refs/runner/planning/"); got != "" {
				t.Fatalf("failed preparation leaked private refs: %s", got)
			}
		})
	}
}

func TestPlanningRemoteIdentityValidation(t *testing.T) {
	for _, test := range []struct {
		url  string
		want bool
	}{
		{"https://github.com/Owner/Repo.git", true},
		{"git@github.com:owner/repo.git", true},
		{"ssh://git@github.com/owner/repo.git", true},
		{"https://github.com/other/repo.git", false},
		{"https://attacker.invalid/owner/repo.git", false},
		{"https://credential@github.com/owner/repo.git", false},
		{"https://github.com/owner/repo.git?redirect=other", false},
	} {
		t.Run(test.url, func(t *testing.T) {
			f := newPlanningFixture(t)
			runGitTest(t, f.repo, "remote", "set-url", "origin", test.url)
			source, err := f.provider.ObservePlanningSource(t.Context(), f.repo, "origin", "main", "owner/repo")
			if (err == nil) != test.want {
				t.Fatalf("source=%+v error=%v, want success=%t", source, err, test.want)
			}
			if !test.want && len(f.runner.fetches) != 0 {
				t.Fatal("unverified repository identity reached network fetch")
			}
		})
	}
}
