//go:build darwin || linux

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sourceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"a.go":                         "package main\n",
		"a/child.go":                   "package a\n",
		".env.example":                 "EMPTY=\n",
		"docs/benchmarks/evidence.bin": "\x00\x01\xff\x02",
		"nested/.git":                  "ordinary nested file\n",
	} {
		sourceWrite(t, root, name, data)
	}
	return root
}

func sourceWrite(t *testing.T, root, name, data string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSourceSnapshotStableFramedIdentityAndPrivateInventory(t *testing.T) {
	first, second := sourceFixture(t), sourceFixture(t)
	if err := os.Chmod(filepath.Join(second, "a.go"), 0400); err != nil {
		t.Fatal(err)
	}
	stamp := time.Unix(100, 0)
	if err := os.Chtimes(filepath.Join(second, "a.go"), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	a, err := takeSourceSnapshot(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := takeSourceSnapshot(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Identity != b.Identity || a.SHA256 != b.SHA256 || a.DigestMethod != sourceDigestMethod || a.Files != 5 || a.Bytes != 55 || a.Identity != "sha256:"+a.SHA256 || len(a.SHA256) != 64 {
		t.Fatal("source identity included location, timestamps, read-only mode or omitted source files", a.Identity, b.Identity, a.Files, a.Bytes)
	}
	if err = a.verify(t.Context()); err != nil {
		t.Fatal("unchanged source failed verification", err)
	}
	encoded, err := json.Marshal(a)
	var public map[string]any
	if err != nil || json.Unmarshal(encoded, &public) != nil || len(public) != 5 || public["digestMethod"] != sourceDigestMethod || strings.Contains(string(encoded), first) || strings.Contains(string(encoded), "child.go") {
		t.Fatal("private snapshot inventory leaked into the report", string(encoded), err)
	}

	// These names cross directory traversal order: a.go precedes a/child.go in
	// global path order. A fresh independently written tree stays identical.
	third := t.TempDir()
	sourceWrite(t, third, "nested/.git", "ordinary nested file\n")
	sourceWrite(t, third, "docs/benchmarks/evidence.bin", "\x00\x01\xff\x02")
	sourceWrite(t, third, ".env.example", "EMPTY=\n")
	sourceWrite(t, third, "a/child.go", "package a\n")
	sourceWrite(t, third, "a.go", "package main\n")
	c, err := takeSourceSnapshot(t.Context(), third)
	if err != nil || c.SHA256 != a.SHA256 {
		t.Fatal("creation order changed source identity", c.Identity, err)
	}
}

func TestSourceSnapshotExcludesOnlyFixedRootMetadataAndScratch(t *testing.T) {
	root := sourceFixture(t)
	before, err := takeSourceSnapshot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".runner-tmp", ".codex", ".agents", ".aws"} {
		sourceWrite(t, root, name+"/private", "must not be read\n")
		if err = os.Chmod(filepath.Join(root, name), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, name), 0700) })
	}
	if err = os.Symlink(t.TempDir(), filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	after, err := takeSourceSnapshot(t.Context(), root)
	if err != nil || after.SHA256 != before.SHA256 || before.verify(t.Context()) != nil {
		t.Fatal("denied metadata or scratch changed source identity", err)
	}
	for _, name := range []string{".env.example", "docs/benchmarks/evidence.bin", "nested/.git"} {
		sourceWrite(t, root, name, "different source\n")
		changed, err := takeSourceSnapshot(t.Context(), root)
		if err != nil || changed.SHA256 == after.SHA256 {
			t.Fatal("ordinary source path was excluded", name, err)
		}
		after = changed
	}
}

func TestSourceSnapshotNamesOnlyBatchesExcludeMetadataBeforeValidation(t *testing.T) {
	root := t.TempDir()
	for i := range 520 {
		sourceWrite(t, root, fmt.Sprintf("source-%03d.go", i), "x")
	}
	before, err := takeSourceSnapshot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sourceWrite(t, outside, "private", "must not be read\n")
	if err = syscall.Mkfifo(filepath.Join(root, ".git"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(filepath.Join(outside, "private"), filepath.Join(root, ".codex")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(root, ".agents")); err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, root, ".aws/private", "must not be entered\n")
	if err = os.Chmod(filepath.Join(root, ".aws"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, ".aws"), 0700) })
	sourceWrite(t, root, ".runner-tmp/attempt/output", "mutable scratch\n")
	after, err := takeSourceSnapshot(t.Context(), root)
	if err != nil || after.Files != 520 || after.Bytes != 520 || after.SHA256 != before.SHA256 {
		t.Fatal("batched name enumeration included fixed metadata or lost source files", after.Identity, after.Files, after.Bytes, err)
	}
	if err = before.verify(t.Context()); err != nil {
		t.Fatal("excluded metadata changes invalidated the source snapshot", err)
	}
}

func TestSourceSnapshotDetectsSourceChanges(t *testing.T) {
	for _, change := range []string{"content", "add", "delete", "rename", "execute", "directory", "root"} {
		t.Run(change, func(t *testing.T) {
			root := sourceFixture(t)
			before, err := takeSourceSnapshot(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(root, "a.go")
			switch change {
			case "content":
				sourceWrite(t, root, "a.go", "package edit\n") // Same byte count.
				err = os.Chtimes(file, time.Unix(100, 0), time.Unix(100, 0))
			case "add":
				sourceWrite(t, root, "new.go", "package main\n")
			case "delete":
				err = os.Remove(file)
			case "rename":
				err = os.Rename(file, filepath.Join(root, "renamed.go"))
			case "execute":
				err = os.Chmod(file, 0700)
			case "directory":
				err = os.Rename(filepath.Join(root, "a"), filepath.Join(t.TempDir(), "old"))
				if err == nil {
					sourceWrite(t, root, "a/child.go", "package a\n")
				}
			case "root":
				err = os.Rename(root, filepath.Join(t.TempDir(), "old"))
				if err == nil {
					err = os.Mkdir(root, 0700)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = before.verify(t.Context()); err == nil {
				t.Fatal("changed source was verified")
			}
			if change == "content" || change == "add" || change == "delete" || change == "rename" || change == "execute" {
				after, err := takeSourceSnapshot(t.Context(), root)
				if err != nil || after.SHA256 == before.SHA256 {
					t.Fatal("source change did not affect the stamp", err)
				}
			}
		})
	}
}

func TestSourceSnapshotRejectsLinksNonregularFilesAndUnsafeRoots(t *testing.T) {
	for _, kind := range []string{"file symlink", "directory symlink", "hardlink", "fifo", "root symlink", "relative", "unclean"} {
		t.Run(kind, func(t *testing.T) {
			root := sourceFixture(t)
			outside := t.TempDir()
			sourceWrite(t, outside, "secret", "must not be followed\n")
			var err error
			switch kind {
			case "file symlink":
				err = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "escape"))
			case "directory symlink":
				err = os.Symlink(outside, filepath.Join(root, "escape"))
			case "hardlink":
				err = os.Link(filepath.Join(outside, "secret"), filepath.Join(root, "escape"))
			case "fifo":
				err = syscall.Mkfifo(filepath.Join(root, "pipe"), 0600)
			case "root symlink":
				alias := filepath.Join(t.TempDir(), "alias")
				err = os.Symlink(root, alias)
				root = alias
			case "relative":
				root = "."
			case "unclean":
				root += "/../" + filepath.Base(root)
			}
			if err != nil {
				t.Fatal(err)
			}
			if stamp, err := takeSourceSnapshot(t.Context(), root); err == nil || stamp.Identity != "" {
				t.Fatal("unsafe source received an authoritative stamp", stamp.Identity, err)
			}
		})
	}
}

type sourceReadContext struct {
	context.Context
	checks int
	onRead func()
}

func (c *sourceReadContext) Err() error {
	c.checks++
	if c.checks == 2 {
		c.onRead()
	}
	return c.Context.Err()
}

func TestSourceSnapshotStreamingCancellationAndReadDrift(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "large.bin")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(file, 16<<20); err != nil {
		t.Fatal(err)
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	for _, cancelRead := range []bool{true, false} {
		name := "drift"
		if cancelRead {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			before, err := os.Lstat(file)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wrapped := &sourceReadContext{Context: ctx, onRead: cancel}
			if !cancelRead {
				wrapped.onRead = func() {
					if err := os.Truncate(file, 1); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = hashSourceFile(wrapped, dir, "large.bin", before, sha256.New(), make([]byte, 64<<10))
			if err == nil || cancelRead && !errors.Is(err, context.Canceled) {
				t.Fatal("streamed source interruption/drift was accepted", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = takeSourceSnapshot(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled source hashing proceeded", err)
	}
}
