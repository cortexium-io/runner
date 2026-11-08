//go:build darwin || linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryPathsCannotReplaceInputsOrFollowLinks(t *testing.T) {
	for _, kind := range []string{"same input", "case alias", "input link", "input hardlink", "parent link", "output link", "output hardlink", "relative", "directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			dir := directory(t)
			raw := measurementFixture(t, 1, 'a', "complete")
			input := put(t, dir, "report.json", raw)
			output := filepath.Join(dir, "history.json")
			switch kind {
			case "same input":
				output = input
			case "case alias":
				output = filepath.Join(dir, "REPORT.json")
				if _, err := os.Stat(output); os.IsNotExist(err) {
					t.Skip("case-sensitive filesystem has no alias")
				}
			case "input link":
				linked := filepath.Join(dir, "link.json")
				if err := os.Symlink(input, linked); err != nil {
					t.Fatal(err)
				}
				input = linked
			case "input hardlink":
				if err := os.Link(input, filepath.Join(dir, "linked.json")); err != nil {
					t.Fatal(err)
				}
			case "parent link":
				linked := filepath.Join(directory(t), "parent")
				if err := os.Symlink(dir, linked); err != nil {
					t.Fatal(err)
				}
				input = filepath.Join(linked, filepath.Base(input))
			case "output link":
				if err := os.Symlink(input, output); err != nil {
					t.Fatal(err)
				}
			case "output hardlink":
				if err := os.Link(input, output); err != nil {
					t.Fatal(err)
				}
			case "relative":
				input = "report.json"
			case "directory":
				input = dir
			case "oversized":
				input = filepath.Join(dir, "oversized.json")
				file, err := os.Create(input)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(maxBytes + 1); err != nil {
					t.Fatal(err)
				}
				file.Close()
			}
			if _, err := collect("", []string{input}, output); err == nil {
				t.Fatal("unsafe history path accepted", kind)
			}
			if got, err := os.ReadFile(filepath.Join(dir, "report.json")); err != nil || !bytes.Equal(got, raw) {
				t.Fatal("history collection changed an input report", kind, err)
			}
		})
	}
}

func TestHistoryOutputIsPrivateAtomicAndIdempotent(t *testing.T) {
	dir := directory(t)
	input := put(t, dir, "candidate.json", measurementFixture(t, 1, 'a', "before"))
	output := filepath.Join(dir, "history.json")
	if _, err := collect("", []string{input}, output); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 || !unlinked(info) {
		t.Fatal("history output lacks private ordinary-file ownership", err)
	}
	if _, err := collect("", []string{input}, output); err != nil {
		t.Fatal("same explicit collection was not idempotent", err)
	}
	other := put(t, dir, "other.json", measurementFixture(t, 2, 'b', "after"))
	if _, err := collect("", []string{other}, output); err == nil {
		t.Fatal("different evidence silently replaced existing history")
	}
	if after, err := os.ReadFile(output); err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed output replacement lost prior history", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 3 {
		t.Fatal("atomic output left a temporary file", err)
	}
}
