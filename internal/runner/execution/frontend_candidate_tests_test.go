package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestFrontendCandidateTestsIncludeChangedRegressions(t *testing.T) {
	repo := sourceRepository(t)
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"unchanged.test.ts", "selected.test.ts", "deleted.test.ts"} {
		write("frontend/src/"+name, "// baseline\n")
	}
	fixtureGit(t, repo, "add", "--all")
	fixtureGit(t, repo, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgSign=false", "commit", "-qm", "frontend baseline")
	base := strings.TrimSpace(string(fixtureGit(t, repo, "rev-parse", "HEAD")))
	write("frontend/src/selected.test.ts", "// changed\n")
	write("frontend/src/new-puzzle.test.ts", "// new regression\n")
	write("frontend/scripts/tool.test.js", "// new tool regression\n")
	write("frontend/src/page.vue", "<template />\n")
	write("testdata/ignored.test.ts", "// outside frontend\n")
	fixtureGit(t, repo, "rm", "--quiet", "frontend/src/deleted.test.ts")
	fixtureGit(t, repo, "add", "--all")
	patch := fixtureGit(t, repo, "diff", "--cached", "--binary", "--no-renames", base)

	w := workspaceRecord{Repository: repo, WorkDir: repo, Base: base}
	input := runner.Text(`["src/unchanged.test.ts","src/selected.test.ts"]`)
	want := `["src/unchanged.test.ts","src/selected.test.ts","scripts/tool.test.js","src/new-puzzle.test.ts"]`
	got, err := frontendCandidateTests(t.Context(), w, input, patch)
	if err != nil || got != want {
		t.Fatalf("retained seed: got %q, want %q, error %v", got, want, err)
	}
	w.Candidate = &runner.SourceResult{Tree: strings.TrimSpace(string(fixtureGit(t, repo, "write-tree")))}
	fixtureGit(t, repo, "reset", "--quiet", "HEAD", "--", ".")
	got, err = frontendCandidateTests(t.Context(), w, input, patch)
	if err != nil || got != want {
		t.Fatalf("captured tree: got %q, want %q, error %v", got, want, err)
	}

	var names []string
	for i := 0; i < 32; i++ {
		names = append(names, "src/"+strings.Repeat("a", i+1)+".test.ts")
	}
	selected, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = frontendCandidateTests(t.Context(), w, runner.Text(string(selected)), patch); err == nil {
		t.Fatal("combined selection exceeding the existing limit was accepted")
	}
}

func TestFrontendCandidateTestsUsePrivateCapturePatch(t *testing.T) {
	w, a := seedWorkspaceFixture(t, nil)
	a.Inputs = nil
	r, err := w.Prepare(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(r.WorkDir, "frontend", "src")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "captured.test.ts"), []byte("// captured regression\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := w.Capture(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = git(t.Context(), r.Repository, nil, nil, 4096, "cat-file", "-e", source.Tree); err == nil {
		t.Fatal("regression requires a tree kept outside the repository object store")
	}
	patch, err := w.disk.Read("artifacts/"+source.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	r, err = w.load(a.RunID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := frontendCandidateTests(t.Context(), r, runner.Text(`["src/selected.test.ts"]`), patch)
	if err != nil || got != `["src/selected.test.ts","src/captured.test.ts"]` {
		t.Fatalf("private capture selection = %q, error %v", got, err)
	}
}
