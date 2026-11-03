package release

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPackageCannotSubstituteSourceOrReadSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	data := []byte("package reviewed\n")
	os.WriteFile(filepath.Join(root, "main.go"), data, 0600)
	h := sha1.New()
	fmt.Fprintf(h, "blob %d%c", len(data), 0)
	h.Write(data)
	tree := []byte("100644 blob " + hex.EncodeToString(h.Sum(nil)) + "\tmain.go\x00")
	declared := map[string]string{"main.go": Digest(data)}
	if err := verifyPackageSource(root, tree, declared); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "main.go"), []byte("unreviewed replacement"), 0600)
	if err := verifyPackageSource(root, tree, declared); err == nil {
		t.Fatal("unreviewed source accepted")
	}
	private := filepath.Join(t.TempDir(), "sentinel")
	os.WriteFile(private, data, 0600)
	os.Remove(filepath.Join(root, "main.go"))
	os.Symlink(private, filepath.Join(root, "main.go"))
	if _, err := readPackageFile(root, "main.go", 1024); err == nil {
		t.Fatal("collector followed an output symlink")
	}
	if err := verifyPackageSource(root, tree, declared); err == nil {
		t.Fatal("source symlink substitution accepted")
	}
}

func TestFrozenSourceExtractionChecksEveryEntry(t *testing.T) {
	for _, scenario := range []string{"valid", "traversal", "symlink", "wrong bytes", "missing entry"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "source.tar.gz")
			f, _ := os.Create(archive)
			gz := gzip.NewWriter(f)
			writer := tar.NewWriter(gz)
			data := []byte("reviewed bytes")
			name := "file.go"
			kind := byte(tar.TypeReg)
			if scenario == "traversal" {
				name = "../escaped"
			}
			if scenario == "symlink" {
				kind = tar.TypeSymlink
			}
			if scenario == "wrong bytes" {
				data = []byte("changed bytes")
			}
			if scenario != "missing entry" {
				header := &tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: kind}
				if kind == tar.TypeSymlink {
					header.Size = 0
					header.Linkname = "/outside"
				}
				writer.WriteHeader(header)
				if kind == tar.TypeReg {
					writer.Write(data)
				}
			}
			writer.Close()
			gz.Close()
			f.Close()
			err := extractSource(archive, filepath.Join(dir, "out"), map[string]string{"file.go": Digest([]byte("reviewed bytes"))})
			if (err == nil) != (scenario == "valid") {
				t.Fatal(scenario, err)
			}
			if _, err = os.Stat(filepath.Join(dir, "escaped")); !os.IsNotExist(err) {
				t.Fatal("archive escaped its owned destination")
			}
		})
	}
}
