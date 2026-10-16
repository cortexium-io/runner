//go:build darwin || linux

package securefs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsurePrivateDirCreatesPrivateTree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "one", "two")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatalf("create private directory tree: %v", err)
	}
	for path := filepath.Dir(root); ; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("created directory %s has mode %04o, want 0700", path, info.Mode().Perm())
		}
		if path == filepath.Dir(root) {
			break
		}
	}
}

func TestEnsurePrivateDirRejectsPermissiveTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worktrees")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(root); err == nil || !strings.Contains(err.Error(), "mode 0755") {
		t.Fatalf("permissive private root was accepted: %v", err)
	}
}

func TestEnsurePrivateDirRejectsNonDirectoryTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "worktrees")
	if err := os.WriteFile(target, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(target); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("non-directory private root was accepted: %v", err)
	}
}

func TestValidateDirectoryRejectsForeignOwnedPrivateTarget(t *testing.T) {
	foreignUID := uint32(os.Geteuid()) + 1
	stat := unix.Stat_t{Uid: foreignUID, Mode: unix.S_IFDIR | 0o700}
	if err := validateDirectory(stat, true); err == nil || !strings.Contains(err.Error(), "neither root nor the effective uid") {
		t.Fatalf("foreign-owned private root was accepted: %v", err)
	}
}

func TestEnsurePrivateDirRejectsSymlinkAncestor(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(filepath.Join(link, "worktrees")); err == nil {
		t.Fatal("symlinked workspace ancestor was accepted")
	}
}

func TestEnsurePrivateDirAcceptsStickyTemporaryAncestor(t *testing.T) {
	base := t.TempDir()
	sticky := filepath.Join(base, "shared-temp")
	if err := os.Mkdir(sticky, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sticky, os.ModeSticky|0o777); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(sticky, "private-worktrees")
	if err := EnsurePrivateDir(root); err != nil {
		t.Fatalf("sticky temporary ancestor was rejected: %v", err)
	}
}

func TestPinnedFileDetectsNamedReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	pinned, err := directory.OpenFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err := os.Rename(path, filepath.Join(root, ".gitignore.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, err := pinned.ReadAll(1024)
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("replacement was not detected: content=%q error=%v", content, err)
	}
	if string(content) == "replacement\n" {
		t.Fatal("pinned descriptor read replacement content")
	}
}

func TestReplaceFileRejectsChangedExpectedObject(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, _, initial, err := directory.ReadFile(".gitignore", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, ".gitignore.old")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := directory.ReplaceFile(".gitignore", []byte("runner\n"), 0o644, initial); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed file was replaced: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "changed\n" {
		t.Fatalf("failed replacement altered current file: content=%q error=%v", content, err)
	}
}

func TestReplaceFileRejectsChangeAtCommitPoint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, _, initial, err := directory.ReadFile(".gitignore", 1024)
	if err != nil {
		t.Fatal(err)
	}
	directory.beforeReplaceCommitForTest = func() {
		if err := os.Rename(path, filepath.Join(root, ".gitignore.old")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("changed-at-commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := directory.ReplaceFile(".gitignore", []byte("runner\n"), 0o644, initial); !errors.Is(err, ErrChanged) {
		t.Fatalf("commit-point replacement was not rejected: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "changed-at-commit\n" {
		t.Fatalf("failed conditional replacement altered current file: content=%q error=%v", content, err)
	}
}

func TestReplaceFileRejectsNewFileAtCommitPoint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, _, initial, err := directory.ReadFile(".gitignore", 1024)
	if err != nil {
		t.Fatal(err)
	}
	directory.beforeReplaceCommitForTest = func() {
		if err := os.WriteFile(path, []byte("appeared-at-commit\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := directory.ReplaceFile(".gitignore", []byte("runner\n"), 0o644, initial); !errors.Is(err, ErrChanged) {
		t.Fatalf("commit-point creation was not rejected: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "appeared-at-commit\n" {
		t.Fatalf("failed no-replace commit altered current file: content=%q error=%v", content, err)
	}
}

func TestReplaceFileRejectsInPlaceContentChangeAtCommitPoint(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(path, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	_, _, initial, err := directory.ReadFile(".gitignore", 1024)
	if err != nil {
		t.Fatal(err)
	}
	directory.beforeReplaceCommitForTest = func() {
		if err := os.WriteFile(path, []byte("alter!\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
	}
	if err := directory.ReplaceFile(".gitignore", []byte("runner\n"), 0o644, initial); !errors.Is(err, ErrChanged) {
		t.Fatalf("equal-size in-place commit-point mutation was not rejected: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "alter!\n" {
		t.Fatalf("failed content-bound replacement altered current file: content=%q error=%v", content, err)
	}
}

func TestOpenDirRejectsChildReplacementBetweenInspectionAndOpen(t *testing.T) {
	root := t.TempDir()
	childPath := filepath.Join(root, "child")
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	directory.beforeOpenDirForTest = func() {
		if err := os.Rename(childPath, filepath.Join(root, "original")); err != nil {
			t.Errorf("rename inspected child: %v", err)
			return
		}
		if err := os.Mkdir(childPath, 0o700); err != nil {
			t.Errorf("replace inspected child: %v", err)
		}
	}
	opened, err := directory.OpenDir("child")
	if opened != nil {
		_ = opened.Close()
		t.Fatal("opened replacement child directory")
	}
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("replacement race error = %v, want ErrChanged", err)
	}
}

func TestOpenDirRejectsSymlinkChild(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "child")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	opened, err := directory.OpenDir("child")
	if opened != nil {
		_ = opened.Close()
		t.Fatal("opened symlink child directory")
	}
	if err == nil {
		t.Fatal("symlink child was accepted")
	}
}

func TestVerifyEmptyUsesPinnedDirectory(t *testing.T) {
	root := t.TempDir()
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := directory.VerifyEmpty(); err != nil {
		t.Fatalf("verify empty directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "hidden"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := directory.VerifyEmpty(); err == nil {
		t.Fatalf("non-empty pinned directory was accepted: %v", err)
	}
}
