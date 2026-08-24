package main

import (
	"bytes"
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
	file := filepath.Join(packageDir, "cortexium-runner")
	if err := os.WriteFile(file, []byte("binary contents\n"), 0o755); err != nil {
		t.Fatal(err)
	}

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
}
