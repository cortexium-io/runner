package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateReleaseArchiveIsDeterministic(t *testing.T) {
	packageDir := filepath.Join(t.TempDir(), "cortexium-runner-v1.2.3-test")
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cortexium-runner":    "binary contents\n",
		"LICENSE":             "license\n",
		"README.md":           "readme\n",
		"OPERATOR.md":         "offline operator guide\n",
		"runner.example.json": "{}\n",
	}
	for name, content := range files {
		mode := os.FileMode(0o644)
		if name == "cortexium-runner" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(packageDir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(packageDir, "cortexium-runner")

	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := createReleaseArchive("tar.gz", packageDir, first); err != nil {
		t.Fatalf("create first archive: %v", err)
	}
	changedTime := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(file, changedTime, changedTime); err != nil {
		t.Fatal(err)
	}
	if err := createReleaseArchive("tar.gz", packageDir, second); err != nil {
		t.Fatalf("create second archive: %v", err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("archives changed when only source timestamps changed")
	}
	reader, err := gzip.NewReader(bytes.NewReader(firstBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	seen := map[string]bool{}
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeDir {
			continue
		}
		name := filepath.Base(header.Name)
		expected, ok := files[name]
		if !ok || seen[name] || header.Name != filepath.Base(packageDir)+"/"+name || header.Typeflag != tar.TypeReg {
			t.Fatalf("unexpected archive entry: %+v", header)
		}
		content, err := io.ReadAll(archive)
		if err != nil || string(content) != expected {
			t.Fatalf("archive changed %s: %q, %v", name, content, err)
		}
		if name == "cortexium-runner" && header.Mode&0o111 == 0 {
			t.Fatal("binary lost executable permission")
		}
		seen[name] = true
	}
	if len(seen) != len(files) {
		t.Fatalf("archive omitted offline files: %v", seen)
	}
}
