package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/subprocess"
)

func specialistSource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGitTest(t, root, "init", "--quiet")
	runGitTest(t, root, "config", "user.name", "Fixture")
	runGitTest(t, root, "config", "user.email", "fixture@example.invalid")
	writeSnapshotTestFile(t, filepath.Join(root, ".gitignore"), "node_modules/\noutput/\n")
	writeSnapshotTestFile(t, filepath.Join(root, "app.go"), "package app\n")
	writeSnapshotTestFile(t, filepath.Join(root, "tests", "app_test.go"), "package tests\n")
	runGitTest(t, root, "add", ".")
	runGitTest(t, root, "commit", "-qm", "fixture")
	writeSnapshotTestFile(t, filepath.Join(root, "app.go"), "package app\n// retained dirty implementation\n")
	writeSnapshotTestFile(t, filepath.Join(root, "node_modules", "private.txt"), "not specialist input")
	return root
}

func TestTestSpecialistCopiesRetainedSourceAndAppliesOnlyObservedTests(t *testing.T) {
	source := specialistSource(t)
	run := subprocess.OSRunner{}
	w, err := PrepareTestSpecialistWorkspace(t.Context(), run, source, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}, DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	content, err := os.ReadFile(filepath.Join(w.Path, "app.go"))
	if err != nil || !strings.Contains(string(content), "retained dirty implementation") {
		t.Fatalf("lost candidate: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("copied ignored dependencies: %v", err)
	}
	if refs := runGitTest(t, w.Path, "remote"); refs != "" {
		t.Fatalf("private workspace has remotes: %s", refs)
	}
	if delta, err := w.Delta(t.Context(), run); err != nil || len(delta) != 0 {
		t.Fatalf("no-change result: %#v %v", delta, err)
	}
	writeSnapshotTestFile(t, filepath.Join(w.Path, "tests", "app_test.go"), "package tests\n// regression\n")
	writeSnapshotTestFile(t, filepath.Join(w.Path, "tests", "new_test.go"), "package tests\n// focused new check\n")
	delta, err := w.Delta(t.Context(), run)
	if err != nil || len(delta) != 2 || delta[0].BeforeDigest == "" || delta[1].BeforeDigest != "" {
		t.Fatalf("wrong delta: %#v %v", delta, err)
	}
	if _, err := ApplyTestSpecialistDelta(t.Context(), run, source, w.SourceFingerprint, delta, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}, DefaultSnapshotLimits()); err != nil {
		t.Fatal(err)
	}
	for _, change := range delta {
		got, err := os.ReadFile(filepath.Join(source, change.Path))
		if err != nil || string(got) != string(change.Content) {
			t.Fatalf("test not applied: %s %v", got, err)
		}
	}
	if _, err := ApplyTestSpecialistDelta(t.Context(), run, source, w.SourceFingerprint, delta, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}, DefaultSnapshotLimits()); err == nil {
		t.Fatal("reapplied delta against stale source")
	}
}

func TestTestSpecialistRefusesUnauthorizedPrivateChanges(t *testing.T) {
	for _, name := range []string{"app.go", "new-production.go", "tests/AGENTS.md", "tests/package.json", ".git/config", ".git/commondir"} {
		t.Run(name, func(t *testing.T) {
			w, err := PrepareTestSpecialistWorkspace(t.Context(), subprocess.OSRunner{}, specialistSource(t), []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}, DefaultSnapshotLimits())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			writeSnapshotTestFile(t, filepath.Join(w.Path, name), "unauthorized")
			if _, err := w.Delta(t.Context(), subprocess.OSRunner{}); err == nil {
				t.Fatal("accepted unauthorized source/control delta")
			}
		})
	}
}

func TestTestSpecialistRejectsDeletionLinksModesAndChangedOriginal(t *testing.T) {
	for _, scenario := range []string{"deleted", "symlink", "mode", "original", "index"} {
		t.Run(scenario, func(t *testing.T) {
			source := specialistSource(t)
			w, err := PrepareTestSpecialistWorkspace(t.Context(), subprocess.OSRunner{}, source, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}, DefaultSnapshotLimits())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = w.Close() })
			file := filepath.Join(w.Path, "tests", "app_test.go")
			switch scenario {
			case "deleted":
				err = os.Remove(file)
			case "symlink":
				err = os.Symlink(filepath.Join(source, "app.go"), filepath.Join(w.Path, "tests", "escape_test.go"))
			case "mode":
				err = os.Chmod(file, 0o755)
			case "original":
				writeSnapshotTestFile(t, filepath.Join(source, "app.go"), "operator change")
			case "index":
				runGitTest(t, w.Path, "update-index", "--assume-unchanged", "tests/app_test.go")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Delta(t.Context(), subprocess.OSRunner{}); err == nil {
				t.Fatal("accepted unsafe delta")
			}
		})
	}
}

func TestTestSpecialistDeltaBoundsAndControlSelection(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "tests/../app.go", "tests/.git/config", "tests/.env", "tests/CLAUDE.md", "tests/node_modules/pkg.js"} {
		if TestSpecialistPathAllowed(name, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}) {
			t.Fatalf("accepted %s", name)
		}
	}
	valid := TestFileChange{Path: "tests/focused_test.go", Mode: 0o644, Content: []byte("test")}
	if err := ValidateTestSpecialistDelta([]TestFileChange{valid}, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTestSpecialistDelta([]TestFileChange{valid, valid}, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}); err == nil {
		t.Fatal("duplicate delta accepted")
	}
	valid.Content = make([]byte, MaxTestSpecialistBytes+1)
	if err := ValidateTestSpecialistDelta([]TestFileChange{valid}, []string{"tests/app_test.go", "tests/new_test.go", "tests/focused_test.go"}); err == nil {
		t.Fatal("oversized delta accepted")
	}
}

func TestTestSpecialistRefusesDiscoveredResidueAndNonFileSelections(t *testing.T) {
	for _, name := range []string{"node_modules/pkg.js", "test-results/report.json", ".env.local", ".npmrc"} {
		t.Run(name, func(t *testing.T) {
			source := specialistSource(t)
			writeSnapshotTestFile(t, filepath.Join(source, name), "must not reach specialist")
			// Even explicit Git discovery must not force credentials/dependencies
			// into the source-only copy.
			runGitTest(t, source, "add", "-f", "--", name)
			if w, err := PrepareTestSpecialistWorkspace(t.Context(), subprocess.OSRunner{}, source, []string{"tests/app_test.go"}, DefaultSnapshotLimits()); err == nil {
				w.Close()
				t.Fatal("copied recognized unsafe residue")
			}
		})
	}
	for _, name := range []string{"tests", "tests/directory.json", "output/ignored_test.go"} {
		t.Run(name, func(t *testing.T) {
			source := specialistSource(t)
			if name == "tests/directory.json" {
				if err := os.Mkdir(filepath.Join(source, name), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if w, err := PrepareTestSpecialistWorkspace(t.Context(), subprocess.OSRunner{}, source, []string{name}, DefaultSnapshotLimits()); err == nil {
				w.Close()
				t.Fatal("non-file/ignored selection accepted")
			}
		})
	}
}
