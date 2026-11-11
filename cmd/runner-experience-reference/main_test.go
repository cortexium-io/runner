package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestReferenceStagePinsReportAndExcludesUnapprovedContent(t *testing.T) {
	root := t.TempDir()
	report := []byte(`<a href="observation.json">proof</a><a href="operator.env">private</a><a href="https://example.invalid/image.png">external</a><a href="script.mjs">script</a>`)
	if err := os.WriteFile(filepath.Join(root, "report.html"), report, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "observation.json"), []byte(`{"observed":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"operator.env", "script.mjs"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("private material must not be staged"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"valid", "wrong digest", "linked", "oversized", "output exists"} {
		t.Run(mode, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "reference.tar.gz")
			digest := hash(report)
			if mode == "wrong digest" {
				digest = hash([]byte("different"))
			}
			if mode == "linked" {
				if err := os.Rename(filepath.Join(root, "observation.json"), filepath.Join(root, "original.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(root, "original.json"), filepath.Join(root, "observation.json")); err != nil {
					t.Fatal(err)
				}
				defer func() {
					_ = os.Remove(filepath.Join(root, "observation.json"))
					_ = os.Rename(filepath.Join(root, "original.json"), filepath.Join(root, "observation.json"))
				}()
			}
			if mode == "oversized" {
				f, err := os.OpenFile(filepath.Join(root, "observation.json"), os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(9 << 20); err != nil {
					t.Fatal(err)
				}
				f.Close()
				defer os.WriteFile(filepath.Join(root, "observation.json"), []byte(`{"observed":true}`), 0600)
			}
			if mode == "output exists" {
				if err := os.WriteFile(output, []byte("owned prior evidence"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := stage(root, digest, output); (err == nil) != (mode == "valid") {
				t.Fatal("reference boundary differs", err)
			}
			if mode == "valid" {
				f, err := os.Open(output)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				z, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				defer z.Close()
				reader := tar.NewReader(z)
				var names []string
				for {
					header, err := reader.Next()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					names = append(names, header.Name)
				}
				if !slices.Equal(names, []string{"observation.json", "reference.json", "report.html"}) {
					t.Fatal("unapproved private or executable content was staged", names)
				}
			}
		})
	}
}
