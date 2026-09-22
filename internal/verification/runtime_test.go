package verification

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
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

func TestRuntimeAndToolchainObserveAdminPackageWithoutWeakeningInputs(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin admin runtime policy")
	}
	root := filepath.Join(t.TempDir(), "Browser.app")
	executable := filepath.Join(root, "Contents", "Browser")
	writeFixture(t, root, "Contents/Browser", "observed executable, never run")
	writeFixture(t, root, "Contents/Frameworks/Engine", "runtime engine")
	if err := os.Symlink("Engine", filepath.Join(root, "Contents/Frameworks/Current")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{root, filepath.Join(root, "Contents"), executable} {
		if err := os.Chown(name, -1, 80); err != nil {
			if os.IsPermission(err) {
				t.Skip("fixture requires membership in trusted admin group")
			}
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0775); err != nil {
			t.Fatal(err)
		}
	}
	before, err := ObserveRuntimePaths(t.Context(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	entry := config.VerificationEntrypoint{Command: executable, ToolchainCommands: []string{executable}, RuntimePaths: []string{root}}
	environment, err := observeEnvironment(t.Context(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collectInputs(t.Context(), root, []string{"Contents"}, nil); err == nil {
		t.Fatal("runtime policy leaked into ordinary candidate/dependency inputs")
	}
	writeFixture(t, root, "Contents/Frameworks/Engine", "changed runtime engine")
	after, err := ObserveRuntimePaths(t.Context(), []string{root})
	if err != nil || before == after {
		t.Fatalf("changed closure: %v %s", err, after)
	}
	nextEnvironment, err := observeEnvironment(t.Context(), entry)
	if err != nil || environment == nextEnvironment {
		t.Fatalf("changed toolchain closure: %v", err)
	}
	if err := os.Chmod(executable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := ObserveRuntimePaths(t.Context(), []string{root}); err == nil {
		t.Fatal("world-writable runtime accepted")
	}
	if _, err := observeEnvironment(t.Context(), entry); err == nil {
		t.Fatal("world-writable toolchain accepted")
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
