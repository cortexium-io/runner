//go:build darwin || linux

package securefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverFilesRejectsControlFileCreatedDuringTraversal(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "concealed", "deep")
	if err := os.MkdirAll(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	discovery, err := directory.discoverFiles(
		[]string{".gitignore", ".gitattributes", ".gitmodules"},
		nil,
		func(stage, path string) {
			if stage == discoveryStageDirectoryRead && path == "concealed/deep" {
				if writeErr := os.WriteFile(filepath.Join(deep, ".gitattributes"), []byte("*.bin binary\n"), 0o600); writeErr != nil {
					t.Fatal(writeErr)
				}
			}
		},
	)
	if !errors.Is(err, ErrChanged) || discovery != nil {
		t.Fatalf("control-file creation during discovery was certified: discovery=%#v error=%v", discovery, err)
	}
}

func TestDiscoverFilesRejectsNextEntryBeforeConfiguredMaximum(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 2, MaxFileBytes: 10, MaxTotalBytes: 10})
	discovery, err := directory.DiscoverFilesWithBudget([]string{".gitignore"}, nil, budget)
	if err == nil || discovery != nil || !strings.Contains(err.Error(), "maximum entries limit 2") || !strings.Contains(err.Error(), "next count 3") {
		t.Fatalf("entry overflow was accepted: discovery=%#v error=%v", discovery, err)
	}
}

func TestDiscoverFilesRejectsControlFileContentChangedAfterDiscovery(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	discovery, err := directory.DiscoverFiles([]string{".gitignore"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer discovery.Close()
	if err := os.WriteFile(path, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := discovery.Verify(); !errors.Is(err, ErrChanged) {
		t.Fatalf("control-file content change was certified: %v", err)
	}
}

func TestDiscoverFilesBudgetBoundsControlFileVerification(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	budget, err := NewSnapshotBudget(SnapshotLimits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := directory.DiscoverFilesWithBudget([]string{".gitignore"}, nil, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer discovery.Close()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("5")
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := discovery.Verify(); err == nil || !strings.Contains(err.Error(), "maximum individual bytes limit 4") || !strings.Contains(err.Error(), ".gitignore") {
		t.Fatalf("grown control file bypassed the discovery budget: %v", err)
	}
}

func TestDiscoverFilesIsLiteralNoFollowAndSkipsExcludedTrees(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ignored", "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "module"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ignored", "deep", ".gitattributes"), []byte("*.bin binary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "module", ".gitmodules"), []byte("excluded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "ignored"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	discovery, err := directory.DiscoverFiles(
		[]string{".gitignore", ".gitattributes", ".gitmodules"},
		[]string{"module"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer discovery.Close()
	want := []string{".gitignore", "ignored/deep/.gitattributes"}
	got := discovery.Paths()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("discovered paths = %#v, want %#v", got, want)
	}
}
