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
	}
	paths := []string{}
	for name, contents := range files {
		writeSnapshotTestFile(t, filepath.Join(root, name), contents)
		paths = append(paths, name)
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
	if got := runGitTest(t, root, "show", ":ignored.js"); got != files["ignored.js"] {
		t.Fatalf("source was filtered: %q", got)
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
