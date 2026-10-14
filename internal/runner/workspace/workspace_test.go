package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerificationCopyAllowsOutputButRejectsChangedSourceAndLinks(t *testing.T) {
	source := t.TempDir()
	if e := os.WriteFile(filepath.Join(source, "source.go"), []byte("package example\n"), 0600); e != nil {
		t.Fatal(e)
	}
	copy, before, e := Copy(source, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(copy, "test-output.txt"), []byte("PASS"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = VerifyCopy(copy, before); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(copy, "source.go"), []byte("weakened"), 0600); e != nil {
		t.Fatal(e)
	}
	if VerifyCopy(copy, before) == nil {
		t.Fatal("modified verification source accepted")
	}
	b, e := os.ReadFile(filepath.Join(source, "source.go"))
	if e != nil || string(b) != "package example\n" {
		t.Fatal("canonical source changed")
	}
	if e = os.Symlink("/etc/passwd", filepath.Join(source, "redirect")); e != nil {
		t.Fatal(e)
	}
	if _, e = Manifest(source); e == nil {
		t.Fatal("external source link accepted")
	}
}
