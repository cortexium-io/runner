package verification

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeClosureBindsFrameworkNotJustWrapper(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Browser.app")
	writeFixture(t, root, "Contents/MacOS/Browser", "small wrapper")
	writeFixture(t, root, "Contents/Frameworks/Engine.framework/Versions/1/Engine", "actual engine")
	if err := os.Symlink("1", filepath.Join(root, "Contents/Frameworks/Engine.framework/Versions/Current")); err != nil {
		t.Fatal(err)
	}
	before, err := ObserveRuntimePaths(t.Context(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "Contents/Frameworks/Engine.framework/Versions/1/Engine", "changed engine")
	after, err := ObserveRuntimePaths(t.Context(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("unchanged wrapper hid changed engine")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveRuntimePaths(t.Context(), []string{root}); err == nil {
		t.Fatal("unbound external runtime closure accepted")
	}
}

func TestRuntimeStreamsFrameworkLargerThanOldReadAllCap(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "Framework")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(129 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveRuntimePaths(t.Context(), []string{name}); err != nil {
		t.Fatal(err)
	}
}
