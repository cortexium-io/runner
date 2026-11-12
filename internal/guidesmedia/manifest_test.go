package guidesmedia

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApprovedManifestAndWholeSetBindings(t *testing.T) {
	m, err := Approved()
	if err != nil || len(m.Guides) != 9 {
		t.Fatal(m, err)
	}
	x, err := NewIndex("run_import", "attempt_import", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	data, err := x.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := x.Bind(data)
	if err != nil || b.ValidateShape() != nil {
		t.Fatal(err)
	}
	if _, err = b.Validate(data); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Index){
		"hash":      func(x *Index) { x.Assets[0].SHA256 = strings.Repeat("b", 64) },
		"path":      func(x *Index) { x.Assets[0].Path = "../escape" },
		"mime":      func(x *Index) { x.Assets[0].MIMEType = "text/html" },
		"role":      func(x *Index) { x.Assets[0].Role = "script" },
		"owner":     func(x *Index) { x.Assets[0].ArtifactID = "foreign/asset" },
		"bytes":     func(x *Index) { x.Assets[0].Bytes++ },
		"missing":   func(x *Index) { x.Assets = x.Assets[:35] },
		"duplicate": func(x *Index) { x.Assets[1] = x.Assets[0] },
		"request":   func(x *Index) { x.RequestID = "other" },
		"manifest":  func(x *Index) { x.ManifestSHA256 = strings.Repeat("c", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			var changed Index
			json.Unmarshal(data, &changed)
			change(&changed)
			d, _ := json.Marshal(changed)
			if _, err := DecodeIndex(d); err == nil {
				t.Fatal("changed set accepted")
			}
		})
	}
	for _, d := range [][]byte{
		append(append([]byte{}, data...), []byte(" {}")...),
		bytes.Replace(data, []byte(`"format":`), []byte(`"foreign":1,"format":`), 1),
	} {
		if _, err := DecodeIndex(d); err == nil {
			t.Fatal("open codec accepted")
		}
	}
	base, tree := strings.Repeat("1", 40), strings.Repeat("2", 40)
	patch, err := EncodePatch(base, tree, b, []byte("ordinary code diff\n"), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DecodePatch(patch, base, b, 16<<20); err != nil {
		t.Fatal(err)
	}
	wrong := b
	wrong.ImportRunID = "foreign"
	if _, err = DecodePatch(patch, base, wrong, 16<<20); err == nil {
		t.Fatal("foreign set accepted")
	}
	if _, err = DecodePatch(patch, strings.Repeat("3", 40), b, 16<<20); err == nil {
		t.Fatal("base drift accepted")
	}
	if _, err = EncodePatch(base, tree, b, make([]byte, 16<<20), 16<<20); err == nil {
		t.Fatal("code envelope cap expanded")
	}
}

func TestStableRawReadAndExclusiveOutput(t *testing.T) {
	root := t.TempDir()
	file := File{RelativePath: "raw/file.mp4", Bytes: 4, SHA256: Digest([]byte("data"))}
	os.Mkdir(filepath.Join(root, "raw"), 0700)
	os.WriteFile(filepath.Join(root, file.RelativePath), []byte("data"), 0600)
	if _, err := readFile(t.Context(), root, file); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readFile(canceled, root, file); err == nil {
		t.Fatal("cancel ignored")
	}
	if _, err := ReadApproved(t.Context(), file); err == nil {
		t.Fatal("arbitrary raw input accepted")
	}
	os.WriteFile(filepath.Join(root, file.RelativePath), []byte("evil"), 0600)
	if _, err := readFile(t.Context(), root, file); err == nil {
		t.Fatal("changed raw accepted")
	}
	os.Remove(filepath.Join(root, file.RelativePath))
	os.Symlink(filepath.Join(root, "target"), filepath.Join(root, file.RelativePath))
	os.WriteFile(filepath.Join(root, "target"), []byte("data"), 0600)
	if _, err := readFile(t.Context(), root, file); err == nil {
		t.Fatal("symlink input accepted")
	}
	if err := WriteNew(root, "out", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := WriteNew(root, "out", []byte("second")); err == nil {
		t.Fatal("existing output overwritten")
	}
	if err := WriteNew(root, "../escape", nil); err == nil {
		t.Fatal("output traversal accepted")
	}
}

func TestMediaOnlyCodeSurface(t *testing.T) {
	for _, path := range []string{
		"frontend/src/components/guides/LearningWalkthrough.vue",
		"frontend/src/pages/how-to-play/kakuro/HowToPlayKakuroPage.vue",
		"docs/printed-learning.md",
	} {
		if !CodePathAllowed(path) {
			t.Fatal(path)
		}
	}
	for _, path := range []string{
		"frontend/src/components/guides/../board.vue",
		"frontend/src/engines/nonogram.ts",
		"frontend/src/components/privacy/CMP.vue",
		"frontend/public/assets/learning/guides/sudoku/video.mp4",
		"frontend/src/components/guides/Unlisted.vue",
	} {
		if CodePathAllowed(path) {
			t.Fatal(path)
		}
	}
}
