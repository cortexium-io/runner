package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
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

func TestOutputViewPreservesSourceAndRejectsBroaderGrants(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "public"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "public/index.html"), []byte("placeholder"), 0600); err != nil {
		t.Fatal(err)
	}
	copy, before, err := Copy(source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputs := []config.CheckOutput{{Path: "public", Placeholders: []string{"public/index.html"}}}
	if err = PrepareOutputs(copy, before.Source, outputs); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(copy, ".runner-output/outputs/public/index.html"), []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = VerifyCopy(copy, before); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(copy, "public/index.html"))
	if err != nil || string(data) != "placeholder" {
		t.Fatal("underlying source replaced", err)
	}
	for _, path := range []string{"public/extra.html", "public/source.go", "public/package.json"} {
		files := map[string]string{}
		for k, v := range before.Source {
			files[k] = v
		}
		files[path] = "600:new"
		if ValidateOutputs(files, outputs) == nil {
			t.Fatal("new tracked file inherited output permission", path)
		}
		outputs[0].Placeholders = append(outputs[0].Placeholders, path)
		if path != "public/extra.html" && ValidateOutputs(files, outputs) == nil {
			t.Fatal("source/manifest accepted as placeholder", path)
		}
		outputs[0].Placeholders = outputs[0].Placeholders[:1]
	}
	if err = os.WriteFile(filepath.Join(copy, "public/index.html"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if VerifyCopy(copy, before) == nil {
		t.Fatal("output grant hid changed underlying source")
	}
	linkCopy, linkBefore, err := Copy(source, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(linkCopy, ".runner-output")); err != nil {
		t.Fatal(err)
	}
	if VerifyCopy(linkCopy, linkBefore) == nil {
		t.Fatal("output root link accepted")
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
