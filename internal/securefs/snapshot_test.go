//go:build darwin || linux

package securefs

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

type snapshotHashResult struct {
	digest []byte
	err    error
}

func captureSnapshotPathAtStage(t *testing.T, directory *Directory, path, stage string) (<-chan struct{}, chan<- struct{}, <-chan snapshotHashResult) {
	t.Helper()
	reached := make(chan struct{})
	release := make(chan struct{})
	result := make(chan snapshotHashResult, 1)
	go func() {
		digest, err := directory.hashPath(path, func(current string) {
			if current == stage {
				close(reached)
				<-release
			}
		})
		result <- snapshotHashResult{digest: digest, err: err}
	}()
	return reached, release, result
}

func requireChangedSnapshotResult(t *testing.T, result <-chan snapshotHashResult) {
	t.Helper()
	captured := <-result
	if !errors.Is(captured.err, ErrChanged) {
		t.Fatalf("snapshot race error = %v, want ErrChanged", captured.err)
	}
	if captured.digest != nil {
		t.Fatalf("snapshot race returned a digest: %x", captured.digest)
	}
}

func TestHashPathDetectsSameSizeRegularFileReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
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

	reached, release, result := captureSnapshotPathAtStage(t, directory, "file.txt", snapshotStageRegularOpened)
	<-reached
	if err := os.Rename(path, filepath.Join(root, "original.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)
}

func TestHashPathSnapshotBudgetExactAndIndividualOverflow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "exact"), []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "overflow"), []byte("12345"), 0o600); err != nil {
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
	if digest, err := directory.HashPathWithBudget("exact", budget); err != nil || len(digest) == 0 {
		t.Fatalf("exact-limit payload failed: digest=%x error=%v", digest, err)
	}
	if digest, err := directory.HashPathWithBudget("overflow", budget); err == nil || digest != nil || !strings.Contains(err.Error(), "maximum individual bytes limit 4") || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("individual overflow was accepted: digest=%x error=%v", digest, err)
	}
}

func TestHashPathSnapshotBudgetRejectsAggregateOverflow(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"first": "1234", "second": "567"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 6})
	if _, err := directory.HashPathWithBudget("first", budget); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.HashPathWithBudget("second", budget); err == nil || !strings.Contains(err.Error(), "maximum aggregate bytes limit 6") || !strings.Contains(err.Error(), "second") {
		t.Fatalf("aggregate overflow was accepted: %v", err)
	}
}

func TestHashPathSnapshotBudgetBoundsSymlinkPayload(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink("1234", filepath.Join(root, "exact-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("12345", filepath.Join(root, "overflow-link")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 8})
	if digest, err := directory.HashPathWithBudget("exact-link", budget); err != nil || len(digest) == 0 {
		t.Fatalf("exact-limit symlink failed: digest=%x error=%v", digest, err)
	}
	if digest, err := directory.HashPathWithBudget("overflow-link", budget); err == nil || digest != nil || !strings.Contains(err.Error(), "maximum individual bytes limit 4") {
		t.Fatalf("symlink overflow was accepted: digest=%x error=%v", digest, err)
	}
}

func TestHashPathSnapshotBudgetRejectsGrowthDuringPinnedRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "growing")
	if err := os.WriteFile(path, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	budget, _ := NewSnapshotBudget(SnapshotLimits{MaxEntries: 10, MaxFileBytes: 4, MaxTotalBytes: 8})
	digest, err := directory.hashPathWithBudget("growing", budget, func(stage string) {
		if stage == snapshotStageRegularOpened {
			file, openErr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if openErr != nil {
				t.Fatal(openErr)
			}
			_, writeErr := file.WriteString("5")
			_ = file.Close()
			if writeErr != nil {
				t.Fatal(writeErr)
			}
		}
	})
	if err == nil || digest != nil || !strings.Contains(err.Error(), "maximum individual bytes limit 4") {
		t.Fatalf("growth past limit was accepted: digest=%x error=%v", digest, err)
	}
}

func TestHashPathDetectsRegularFileMutationAfterRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
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

	reached, release, result := captureSnapshotPathAtStage(t, directory, "file.txt", snapshotStageRegularRead)
	<-reached
	if err := os.WriteFile(path, []byte("alter!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)
}

func TestHashPathDetectsSymlinkTargetReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "link")
	if err := os.Symlink("first", path); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	reached, release, result := captureSnapshotPathAtStage(t, directory, "link", snapshotStageSymlinkRead)
	<-reached
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", path); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)
}

func TestHashPathDetectsMissingPathReplacement(t *testing.T) {
	root := t.TempDir()
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	reached, release, result := captureSnapshotPathAtStage(t, directory, "deleted/child.txt", snapshotStageMissingObserved)
	<-reached
	if err := os.Mkdir(filepath.Join(root, "deleted"), 0o700); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)
}

func TestHashPathPinsAncestorsAndNeverFollowsReplacement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	insideDirectory := filepath.Join(root, "nested")
	if err := os.Mkdir(insideDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(insideDirectory, "sentinel"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sentinel"), []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	reached, release, result := captureSnapshotPathAtStage(t, directory, "nested/sentinel", snapshotStageRegularOpened)
	<-reached
	if err := os.Rename(insideDirectory, filepath.Join(root, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, insideDirectory); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)

	if digest, err := directory.HashPath("nested/sentinel"); err == nil || digest != nil {
		t.Fatalf("symlink ancestor was followed or hashed: digest=%x error=%v", digest, err)
	}
}

func TestHashPathDetectsRepositoryRootReplacement(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("inside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "file"), []byte("outside secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	reached, release, result := captureSnapshotPathAtStage(t, directory, "file", snapshotStageRegularOpened)
	<-reached
	if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	close(release)
	requireChangedSnapshotResult(t, result)
}

func TestHashPathStableObjectsAndLiteralNamesAreDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	literal := "line\nwith\ttab\\and-backslash"
	if err := os.WriteFile(filepath.Join(root, "nested", literal), []byte("content\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target\nwith\ttab", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	paths := []string{"nested/" + literal, "nested", "link", "deleted/file"}
	for _, path := range paths {
		first, err := directory.HashPath(path)
		if err != nil {
			t.Fatalf("hash %q: %v", path, err)
		}
		second, err := directory.HashPath(path)
		if err != nil {
			t.Fatalf("hash %q again: %v", path, err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("hash %q is unstable: %x != %x", path, first, second)
		}
	}
}

func TestHashPathRejectsEscapesAmbiguityAndSpecialFiles(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenDir(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()

	for _, path := range []string{"", "/absolute", "../escape", "nested/../escape", "./file", "nested//file", "nested/"} {
		if digest, err := directory.HashPath(path); err == nil || digest != nil {
			t.Fatalf("invalid path %q was hashed: digest=%x error=%v", path, digest, err)
		}
	}
	if digest, err := directory.HashPath("fifo"); err == nil || digest != nil || !strings.Contains(err.Error(), "unsupported file type") {
		t.Fatalf("special file was certified: digest=%x error=%v", digest, err)
	}
}
