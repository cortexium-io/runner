//go:build darwin || linux

package execution

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestSourceInventoryStreamsBeyondOneMiBForCheckoutAndSeed(t *testing.T) {
	repo := sourceRepository(t)
	ctx := t.Context()
	blob, err := git(ctx, repo, nil, []byte("x"), 256, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	const files = 11000
	var entries strings.Builder
	for i := 0; i < files; i++ {
		fmt.Fprintf(&entries, "100644 blob %s\tinventory-%05d-%s.txt%c", strings.TrimSpace(string(blob)), i, strings.Repeat("x", 80), 0)
	}
	tree, err := git(ctx, repo, nil, []byte(entries.String()), 256, "mktree", "-z")
	if err != nil {
		t.Fatal(err)
	}
	treeID := strings.TrimSpace(string(tree))
	metadata, err := git(ctx, repo, nil, nil, 3<<20, "ls-tree", "-rlz", treeID)
	if err != nil || len(metadata) <= 1<<20 {
		t.Fatal("fixture must exceed the former whole-output bound", len(metadata), err)
	}
	if count, err := sourceTreeBytes(ctx, repo, nil, treeID, files, true); err != nil || count != files {
		t.Fatal("streaming inventory changed exact checkout size", count, err)
	}
	if _, err := sourceTreeBytes(ctx, repo, nil, treeID, files-1, true); !errors.Is(err, runner.ErrCapacity) {
		t.Fatal("streaming inventory bypassed content capacity", err)
	}
	commit, err := git(ctx, repo, nil, nil, 256, "-c", "user.name=Runner fixture", "-c", "user.email=fixture@example.invalid", "commit-tree", treeID, "-p", "HEAD", "-m", "large inventory fixture")
	if err != nil {
		t.Fatal(err)
	}
	disk := workspaceDisk(t)
	w := &Workspaces{disk: disk}
	a := sourceAssignment(disk, repo, "inventory_stream")
	a.Source.Ref = strings.TrimSpace(string(commit))
	defer w.ForgetRun(context.Background(), a.RunID)
	r, err := w.Prepare(ctx, a)
	if err != nil {
		t.Fatal("large legal source inventory rejected before checkout", err)
	}
	if count, err := w.validateSeed(ctx, r, nil, disk.Policy.MaxArtifact, false); err != nil || count != files {
		t.Fatal("seed verification retained the whole-inventory buffer limit", count, err)
	}
}

func TestSourceInventoryBoundsRecordsAndPreservesPathPolicy(t *testing.T) {
	entry := "100644 blob " + strings.Repeat("a", 40) + " 3\tname\x00"
	v := &treeInventory{limit: 6, verifyPaths: true}
	for _, part := range [][]byte{[]byte(entry[:17]), []byte(entry[17:] + entry)} {
		if n, err := v.Write(part); err != nil || n != len(part) {
			t.Fatal("record split across reads was rejected", n, err)
		}
	}
	if v.total != 6 || len(v.entry) != 0 {
		t.Fatal("incorrect streamed total", v.total)
	}
	for _, name := range []string{".gitattributes", "dir/.gitmodules", ".runner-tmp", ".runner-tmp/file", ".codex/config.toml"} {
		v := &treeInventory{limit: 100, verifyPaths: true}
		if _, err := v.Write([]byte(strings.Replace(entry, "name", name, 1))); err == nil {
			t.Fatal("protected source inventory path accepted", name)
		}
	}
	for _, data := range [][]byte{[]byte("malformed\x00"), []byte(strings.Replace(entry, "100644", "120000", 1)), bytes.Repeat([]byte{'x'}, (1<<20)+1)} {
		v := &treeInventory{limit: 100, verifyPaths: true}
		if _, err := v.Write(data); err == nil || len(v.entry) > 1<<20 {
			t.Fatal("invalid or oversized inventory record accepted", err, len(v.entry))
		}
	}
}
