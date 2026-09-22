//go:build darwin || linux

package securefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRuntimeReadPermissionPolicy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		uid, gid, mode uint32
		ancestor, want bool
	}{
		{"root", 0, 0, unix.S_IFDIR | 0755, false, true},
		{"user", uint32(os.Geteuid()), 20, unix.S_IFREG | 0755, false, true},
		{"admin directory", 0, 80, unix.S_IFDIR | 0775, false, runtime.GOOS == "darwin"},
		{"admin executable", uint32(os.Geteuid()), 80, unix.S_IFREG | 0775, false, runtime.GOOS == "darwin"},
		{"wrong group", uint32(os.Geteuid()), 79, unix.S_IFDIR | 0775, false, false},
		{"wrong owner", uint32(os.Geteuid() + 1), 80, unix.S_IFDIR | 0755, false, false},
		{"world directory", 0, 80, unix.S_IFDIR | 0777, false, false},
		{"world file", 0, 80, unix.S_IFREG | 0777, false, false},
		{"sticky selected root", 0, 0, unix.S_IFDIR | unix.S_ISVTX | 0777, false, false},
		{"sticky temporary ancestor", 0, 0, unix.S_IFDIR | unix.S_ISVTX | 0777, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stat := unix.Stat_t{Uid: tc.uid, Gid: tc.gid}
			// Stat_t.Mode has a different integer width on Darwin and Linux.
			if tc.mode&unix.S_IFMT == unix.S_IFDIR {
				stat.Mode = unix.S_IFDIR
			} else {
				stat.Mode = unix.S_IFREG
			}
			stat.Mode |= 0755
			if tc.mode&0020 != 0 {
				stat.Mode |= 0020
			}
			if tc.mode&0002 != 0 {
				stat.Mode |= 0002
			}
			if tc.mode&unix.S_ISVTX != 0 {
				stat.Mode |= unix.S_ISVTX
			}
			if got := validateRuntimeReadPermissions(stat, tc.ancestor); (got == nil) != tc.want {
				t.Fatalf("allowed=%v, want %v: %v", got == nil, tc.want, got)
			}
		})
	}
}

func TestRuntimeReadOnlyViewHasNoMutationCapability(t *testing.T) {
	typ := reflect.TypeOf((*ReadOnlyDirectory)(nil))
	want := map[string]bool{"Close": true, "Verify": true, "OpenDir": true, "ReadDirNamesWithBudget": true, "HashFileContent": true}
	if typ.NumMethod() != len(want) {
		t.Fatalf("unexpected read-only methods: %v", typ)
	}
	for i := 0; i < typ.NumMethod(); i++ {
		if !want[typ.Method(i).Name] {
			t.Fatal(typ.Method(i).Name)
		}
	}
}

func TestRuntimeReadAdminPackageKeepsProtectedReadersStrict(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin admin runtime policy")
	}
	root := filepath.Join(t.TempDir(), "Browser.app")
	contents := filepath.Join(root, "Contents")
	if err := os.MkdirAll(contents, 0755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(contents, "Browser")
	if err := os.WriteFile(executable, []byte("runtime bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{root, contents, executable} {
		if err := os.Chown(name, -1, 80); err != nil {
			if errors.Is(err, unix.EPERM) {
				t.Skip("fixture requires membership in the explicitly trusted admin group")
			}
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0775); err != nil {
			t.Fatal(err)
		}
	}
	if d, err := OpenDir(root); err == nil {
		d.Close()
		t.Fatal("protected OpenDir accepted group-writable package")
	}
	if d, err := OpenReadOnlyDir(root); err == nil {
		d.Close()
		t.Fatal("ordinary read view relaxed protected policy")
	}
	dir, err := OpenRuntimeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	child, err := dir.OpenDir("Contents")
	if err != nil {
		t.Fatal(err)
	}
	defer child.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 16, MaxFileBytes: 1024, MaxTotalBytes: 4096})
	names, err := child.ReadDirNamesWithBudget(budget)
	if err != nil || !reflect.DeepEqual(names, []string{"Browser"}) {
		t.Fatalf("list: %v %v", names, err)
	}
	digest, mode, err := child.HashFileContent(t.Context(), "Browser", budget)
	want := sha256.Sum256([]byte("runtime bytes"))
	if err != nil || !bytes.Equal(digest, want[:]) || mode != 0775 {
		t.Fatalf("hash/mode: %x %o %v", digest, mode, err)
	}
	if err := dir.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := child.Verify(); err != nil {
		t.Fatal(err)
	}
	if err := child.directory.AppendFile("new", nil, 0600, 100); err == nil {
		t.Fatal("internal runtime descriptor allowed append")
	}
	if err := child.directory.ReplaceFile("Browser", nil, 0600, FileState{}); err == nil {
		t.Fatal("internal runtime descriptor allowed replacement")
	}
	if err := os.Chmod(executable, 0777); err != nil {
		t.Fatal(err)
	}
	if _, _, err := child.HashFileContent(t.Context(), "Browser", budget); err == nil {
		t.Fatal("world-writable executable accepted")
	}
}

func TestRuntimeReadDetectsAncestorAndChildSubstitution(t *testing.T) {
	for _, mutation := range []string{"ancestor replacement", "ancestor permissions", "child symlink"} {
		t.Run(mutation, func(t *testing.T) {
			base := t.TempDir()
			ancestor := filepath.Join(base, "ancestor")
			root := filepath.Join(ancestor, "runtime")
			if err := os.MkdirAll(filepath.Join(root, "child"), 0755); err != nil {
				t.Fatal(err)
			}
			dir, err := OpenRuntimeDir(root)
			if err != nil {
				t.Fatal(err)
			}
			defer dir.Close()
			switch mutation {
			case "ancestor replacement":
				if err := os.Rename(ancestor, ancestor+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(ancestor, 0755); err != nil {
					t.Fatal(err)
				}
				// The selected directory inode stays identical; only its ancestor changes.
				if err := os.Rename(filepath.Join(ancestor+"-old", "runtime"), root); err != nil {
					t.Fatal(err)
				}
			case "ancestor permissions":
				if err := os.Chmod(ancestor, 0700); err != nil {
					t.Fatal(err)
				}
			case "child symlink":
				dir.directory.beforeOpenDirForTest = func() {
					if err := os.Rename(filepath.Join(root, "child"), filepath.Join(root, "old")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("old", filepath.Join(root, "child")); err != nil {
						t.Fatal(err)
					}
				}
				if child, err := dir.OpenDir("child"); err == nil {
					child.Close()
					t.Fatal("substituted link accepted")
				}
			}
			if err := dir.Verify(); err == nil {
				t.Fatal("runtime mutation accepted")
			}
		})
	}
}

func TestRuntimeReadBoundsAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "engine"), []byte("contents"), 0600); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenRuntimeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 2, MaxFileBytes: 2, MaxTotalBytes: 2})
	if _, _, err := dir.HashFileContent(t.Context(), "engine", budget); err == nil {
		t.Fatal("unbounded runtime read")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := dir.HashFileContent(ctx, "engine", budget); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
