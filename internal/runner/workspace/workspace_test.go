package workspace

import (
	"os"
	"path/filepath"
	"strings"
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

// Malformed/unknown records must never turn a Git conflict into a model repair.
// NUL separation preserves filenames which line/whitespace parsing would lose.
func TestConflictRecordsFailClosedAndPreservePaths(t *testing.T) {
	path := "space and\nnewline.txt"
	record := "tree\x00" + path + "\x00\x001\x00" + path + "\x00CONFLICT (contents)\x00diagnostic\x00"
	conflict, ok := parseConflict(record).(*MergeConflict)
	if !ok || len(conflict.Paths) != 1 || conflict.Paths[0] != path {
		t.Fatal("quoted path lost", conflict)
	}
	for _, output := range []string{
		"tree\x00\x00",
		strings.TrimSuffix(record, "\x00"),
		strings.Replace(record, "diagnostic\x00", "", 1),
		strings.Replace(record, "CONFLICT (contents)", "CONFLICT (binary)", 1),
		strings.Replace(record, "CONFLICT (contents)", "CONFLICT (new-type)", 1),
		strings.Replace(record, "\x001\x00", "\x009999\x00", 1),
		strings.ReplaceAll(record, path, "../escape"),
		strings.Replace(record, "CONFLICT (contents)", "Auto-merging", 1),
	} {
		if _, ok := parseConflict(output).(*MergeConflict); ok {
			t.Fatal("unsafe conflict admitted", output)
		}
	}
}
