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

func TestRemoveFilePreservesUnexpectedProbeTargets(t *testing.T) {
	d, err := runner.OpenDisk(t.TempDir(), policy())
	if err != nil {
		t.Fatal(err)
	}
	const name = "jobs/permission-probe"
	path := filepath.Join(d.Root, name)
	if err := d.Write(name, []byte("probe"), 16); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(name); err != nil || d.Used() != 0 {
		t.Fatal("owned file was not removed from accounting", err, d.Used())
	}
	if err := d.RemoveFile(name); err != nil {
		t.Fatal("already absent probe was not accepted", err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(path, "retain")
	if err := os.WriteFile(other, []byte("retain"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(name); err == nil {
		t.Fatal("file cleanup accepted a directory")
	}
	if bytes, err := os.ReadFile(other); err != nil || string(bytes) != "retain" {
		t.Fatal("file cleanup removed unrelated data", err)
	}
	const link = "jobs/permission-link"
	if err := os.Symlink(other, filepath.Join(d.Root, link)); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveFile(link); err == nil {
		t.Fatal("file cleanup accepted a symlink")
	}
	if target, err := os.Readlink(filepath.Join(d.Root, link)); err != nil || target != other {
		t.Fatal("file cleanup altered the symlink", err)
	}
}
