package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerificationIndexStagesOnlyLiteralCopiedSource(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".gitignore":     "ignored.js\nnode_modules/\n",
		".gitattributes": "*.js text eol=lf filter=unavailable\n",
		"ignored.js":     "literal\r\nbytes\r\n",
		"odd\tname.js":   "odd filename",
		"line\nbreak.js": "newline filename",
		"quote\"\\é.js":  "quoted Unicode filename",
		"-leading.js":    "leading dash filename",
		"run.sh":         "#!/bin/sh\nexit 0\n",
	}
	paths := []string{}
	for name, contents := range files {
		writeSnapshotTestFile(t, filepath.Join(root, name), contents)
		paths = append(paths, name)
	}
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeSnapshotTestFile(t, filepath.Join(root, "node_modules", "generated.js"), "not source")
	if err := os.Symlink("ignored.js", filepath.Join(root, "link.js")); err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "link.js")
	index, err := PrepareVerificationIndex(t.Context(), root, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "\tignored.js\x00") || !strings.Contains(index, "\todd\tname.js\x00") || strings.Contains(index, "generated.js") {
		t.Fatalf("wrong source inventory: %q", index)
	}
	for name, content := range files {
		if got := runGitTest(t, root, "show", ":"+name); got != content {
			t.Fatalf("source %q was changed or omitted: %q", name, got)
		}
	}
	if !strings.Contains(runGitTest(t, root, "ls-files", "--stage", "--", "run.sh"), "100755 ") {
		t.Fatal("executable mode was lost")
	}
	if got := runGitTest(t, root, "show", ":link.js"); got != "ignored.js" {
		t.Fatalf("symlink was followed: %q", got)
	}
	if got := runGitTest(t, root, "remote"); got != "" {
		t.Fatalf("unexpected remote: %q", got)
	}
	command := exec.CommandContext(t.Context(), "git", "rev-parse", "--verify", "HEAD")
	command.Dir = root
	if err := command.Run(); err == nil {
		t.Fatal("verification invented a candidate commit")
	}
	if _, err := PrepareVerificationIndex(t.Context(), root, paths); err == nil {
		t.Fatal("existing Git directory was reused")
	}
}

func TestVerificationIndexReadAllowsStatRefreshAndStandaloneFormats(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, filepath.Join(root, "app.js"), "literal")
	want, err := PrepareVerificationIndex(t.Context(), root, []string{"app.js"})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"2", "3", "4"} {
		runGitTest(t, root, "update-index", "--index-version", version)
		runGitTest(t, root, "status", "--porcelain")
		if got, err := ReadVerificationIndex(t.Context(), root); err != nil || got != want {
			t.Fatalf("index v%s cache refresh changed inventory: %q %v", version, got, err)
		}
	}
}

func TestVerificationIndexReadRejectsTruncatedIndex(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, filepath.Join(root, "app.js"), "literal")
	if _, err := PrepareVerificationIndex(t.Context(), root, []string{"app.js"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, ".git", "index"), 12); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVerificationIndex(t.Context(), root); err == nil {
		t.Fatal("truncated index was accepted")
	}
}

func TestVerificationIndexReadRejectsExternalSplitIndex(t *testing.T) {
	root := t.TempDir()
	writeSnapshotTestFile(t, filepath.Join(root, "app.js"), "literal")
	if _, err := PrepareVerificationIndex(t.Context(), root, []string{"app.js"}); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, root, "update-index", "--split-index")
	shared, err := filepath.Glob(filepath.Join(root, ".git", "sharedindex.*"))
	if err != nil || len(shared) != 1 {
		t.Fatalf("split index fixture: %v %v", shared, err)
	}
	outside := filepath.Join(t.TempDir(), "external-index")
	if err := os.Rename(shared[0], outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, shared[0]); err != nil {
		t.Fatal(err)
	}
	if inventory, err := ReadVerificationIndex(t.Context(), root); err == nil {
		t.Fatalf("followed an external split index: %q", inventory)
	}
}

func TestVerificationIndexEmptySource(t *testing.T) {
	root := t.TempDir()
	index, err := PrepareVerificationIndex(t.Context(), root, nil)
	if err != nil || index != "" {
		t.Fatalf("empty inventory: %q %v", index, err)
	}
	if got, err := ReadVerificationIndex(t.Context(), root); err != nil || got != index {
		t.Fatalf("read empty inventory: %q %v", got, err)
	}
}

func TestVerificationIndexIgnoresAmbientGitConfiguration(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	config := filepath.Join(outside, "gitconfig")
	writeSnapshotTestFile(t, config, "[init]\n\ttemplateDir = "+outside+"\n[filter \"bad\"]\n\trequired = true\n\tclean = false\n")
	writeSnapshotTestFile(t, filepath.Join(outside, "template-sentinel"), "must not be copied")
	writeSnapshotTestFile(t, filepath.Join(root, "app.js"), "literal\r\n")
	writeSnapshotTestFile(t, filepath.Join(root, ".gitattributes"), "*.js filter=bad\n")
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	t.Setenv("GIT_TEMPLATE_DIR", outside)
	t.Setenv("GIT_DIR", filepath.Join(outside, "redirected"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(outside, "index"))
	t.Setenv("GIT_OBJECT_DIRECTORY", filepath.Join(outside, "objects"))
	if _, err := PrepareVerificationIndex(t.Context(), root, []string{"app.js", ".gitattributes"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, ".git", "template-sentinel"), filepath.Join(outside, "redirected"), filepath.Join(outside, "index"), filepath.Join(outside, "objects")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("ambient Git authority was used: %s: %v", path, err)
		}
	}
}
