package runner_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestForensicsAccountingPreservesSuspectNamesAndCountsLinksWithoutFollowing(t *testing.T) {
	p := runner.DefaultStorage()
	p.MinFree = 0
	d, err := runner.OpenDisk(t.TempDir(), p)
	if err != nil {
		t.Fatal(err)
	}
	name := "forensics/run/attempt/source/.write-original-evidence"
	if err := d.Write(name, []byte("preserved suspect content"), p.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "large"), []byte(strings.Repeat("x", 1<<20)), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.Root, "forensics/run/attempt/source/external")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || d.Used() != int64(len("preserved suspect content"))+info.Size() {
		t.Fatal("forensic bytes were exempted or a link target was followed", d.Used(), err)
	}
	if data, err := d.Read(name, p.MaxArtifact); err != nil || string(data) != "preserved suspect content" {
		t.Fatal("normal temporary-file cleanup deleted forensic evidence", err)
	}
	if _, err := d.Read("forensics/run/attempt/source/external/large", p.MaxArtifact); err == nil {
		t.Fatal("forensic reads escaped through a preserved link")
	}
	if err := os.Rename(filepath.Join(d.Root, "forensics"), filepath.Join(d.Root, "saved-forensics")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(d.Root, "forensics")); err != nil {
		t.Fatal(err)
	}
	if err := d.Refresh(); err == nil {
		t.Fatal("forensics namespace root may not be replaced by a link")
	}
}
