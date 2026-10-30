package runner_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

// Dependency/cache links are normal disposable work. Count link bytes without
// following them; controlled reads/writes and retained artifacts still reject them.
func TestWorkLinksDoNotEscapeAccountingOrControlledIO(t *testing.T) {
	d, err := runner.OpenDisk(t.TempDir(), policy())
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	file := filepath.Join(out, "file")
	if err = os.WriteFile(file, make([]byte, 65536), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(out, filepath.Join(d.Root, "work", "dependencies")); err != nil {
		t.Fatal(err)
	}
	if err = d.Refresh(); err != nil {
		t.Fatal(err)
	}
	if d.Used() >= 65536 {
		t.Fatal("accounting followed a dependency link")
	}
	if err = d.Write("work/dependencies/file", []byte("overwrite"), 1024); err == nil {
		t.Fatal("controlled write followed link")
	}
	if _, err = d.Read("work/dependencies/file", 65536); err == nil {
		t.Fatal("controlled read followed link")
	}
	if err = d.Remove("work/dependencies"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(file); err != nil || len(data) != 65536 {
		t.Fatal("cleanup followed link", err)
	}
}
