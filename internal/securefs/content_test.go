//go:build darwin || linux

package securefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestHashFileContentStreamsAndBoundsTwoPasses(t *testing.T) {
	root := t.TempDir()
	content := bytes.Repeat([]byte("framework"), 20000)
	if err := os.WriteFile(filepath.Join(root, "engine"), content, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 2, MaxFileBytes: int64(len(content)), MaxTotalBytes: int64(len(content))})
	digest, mode, err := dir.HashFileContent(t.Context(), "engine", budget)
	expected := sha256.Sum256(content)
	if err != nil || !bytes.Equal(digest, expected[:]) || mode != 0700 || budget.total != int64(len(content)) {
		t.Fatalf("digest/mode/logical budget: %x %o %d %v", digest, mode, budget.total, err)
	}
	small, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 2, MaxFileBytes: 10, MaxTotalBytes: 10})
	if _, _, err := dir.HashFileContent(t.Context(), "engine", small); err == nil {
		t.Fatal("oversized content accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := dir.HashFileContent(ctx, "engine", budget); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSnapshotRegularOpenRefusesFIFOSubstitution(t *testing.T) {
	root := t.TempDir()
	name := filepath.Join(root, "file")
	if err := os.WriteFile(name, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	var before unix.Stat_t
	if err := unix.Fstatat(dir.fd, "file", &before, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(name, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := hashRegularAt(dir.fd, "file", name, before, nil, nil); err == nil {
		t.Fatal("FIFO substitution accepted")
	}
}
