package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type submoduleSnapshot struct {
	paths    map[string]string
	manifest string
}

type pinnedSubmodulePath struct {
	object []byte
	marker []byte
}

func pinIndexedSubmodulePaths(rootDirectory *securefs.Directory, indexEntries map[string]string, budget *securefs.SnapshotBudget) (map[string]pinnedSubmodulePath, error) {
	gitlinks := indexedGitlinks(indexEntries)
	pinned := make(map[string]pinnedSubmodulePath, len(gitlinks))
	for _, path := range sortedSnapshotKeys(gitlinks) {
		object, err := rootDirectory.HashPathWithBudget(path, budget)
		if err != nil {
			return nil, fmt.Errorf("pin indexed submodule path %q: %w", path, err)
		}
		directory, err := openRelativeSnapshotDirectory(rootDirectory, path)
		if err != nil {
			return nil, fmt.Errorf("open indexed submodule path %q without following links: %w", path, err)
		}
		marker, err := directory.HashPathWithBudget(".git", budget)
		if err != nil {
			_ = directory.Close()
			return nil, fmt.Errorf("pin indexed submodule administrative path %q: %w", path, err)
		}
		if err := directory.Close(); err != nil {
			return nil, fmt.Errorf("close pinned indexed submodule directory %q: %w", path, err)
		}
		pinned[path] = pinnedSubmodulePath{object: object, marker: marker}
	}
	return pinned, nil
}

func captureIndexedSubmodules(
	ctx context.Context,
	run subprocess.Runner,
	rootDirectory *securefs.Directory,
	root string,
	timeout time.Duration,
	indexEntries map[string]string,
	pinned map[string]pinnedSubmodulePath,
	ignoreBranchTracking bool,
	budget *securefs.SnapshotBudget,
) (submoduleSnapshot, error) {
	gitlinks := indexedGitlinks(indexEntries)
	state := submoduleSnapshot{paths: make(map[string]string, len(gitlinks))}
	manifest := strings.Builder{}
	for _, path := range sortedSnapshotKeys(gitlinks) {
		expected, ok := pinned[path]
		if !ok {
			return submoduleSnapshot{}, fmt.Errorf("indexed submodule path %q was not pinned from the index", path)
		}
		objectBefore, err := rootDirectory.HashPathWithBudget(path, budget)
		if err != nil {
			return submoduleSnapshot{}, fmt.Errorf("capture indexed submodule path %q: %w", path, err)
		}
		if !bytes.Equal(expected.object, objectBefore) {
			return submoduleSnapshot{}, fmt.Errorf("%w before capturing indexed submodule path %q", securefs.ErrChanged, path)
		}
		submoduleDirectory, err := openRelativeSnapshotDirectory(rootDirectory, path)
		if err != nil {
			return submoduleSnapshot{}, fmt.Errorf("open indexed submodule path %q without following links: %w", path, err)
		}

		markerBefore, err := submoduleDirectory.HashPathWithBudget(".git", budget)
		if err != nil {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("capture indexed submodule administrative path %q: %w", path, err)
		}
		if !bytes.Equal(expected.marker, markerBefore) {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("%w before capturing indexed submodule administrative path %q", securefs.ErrChanged, path)
		}
		_, _, markerState, markerErr := submoduleDirectory.ReadFile(".git", gitControlFileLimit)
		initialized := markerErr != nil || markerState.Exists
		var nestedFingerprint string
		if initialized {
			nested, captureErr := captureSnapshotState(ctx, run, filepath.Join(root, filepath.FromSlash(path)), timeout, false, ignoreBranchTracking, budget)
			if captureErr != nil {
				_ = submoduleDirectory.Close()
				return submoduleSnapshot{}, fmt.Errorf("capture initialized indexed submodule %q: %w", path, captureErr)
			}
			nestedFingerprint = nested.Fingerprint
		} else if err := submoduleDirectory.VerifyEmpty(); err != nil {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("verify uninitialized indexed submodule %q is empty: %w", path, err)
		}
		markerAfter, markerVerifyErr := submoduleDirectory.HashPathWithBudget(".git", budget)
		if markerVerifyErr != nil {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("verify indexed submodule administrative path %q: %w", path, markerVerifyErr)
		}
		if !bytes.Equal(markerBefore, markerAfter) {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("%w while using indexed submodule administrative path %q", securefs.ErrChanged, path)
		}
		if err := submoduleDirectory.Verify(); err != nil {
			_ = submoduleDirectory.Close()
			return submoduleSnapshot{}, fmt.Errorf("verify indexed submodule directory %q: %w", path, err)
		}
		if err := submoduleDirectory.Close(); err != nil {
			return submoduleSnapshot{}, fmt.Errorf("close indexed submodule directory %q: %w", path, err)
		}
		objectAfter, err := rootDirectory.HashPathWithBudget(path, budget)
		if err != nil {
			return submoduleSnapshot{}, fmt.Errorf("verify indexed submodule path %q: %w", path, err)
		}
		if !bytes.Equal(objectBefore, objectAfter) {
			return submoduleSnapshot{}, fmt.Errorf("%w while using indexed submodule path %q", securefs.ErrChanged, path)
		}

		entry := strings.Builder{}
		entry.WriteString("index=")
		entry.WriteString(gitlinks[path])
		entry.WriteString("\x00worktree=")
		entry.WriteString(digestString(objectBefore))
		entry.WriteString("\x00git-marker=")
		entry.WriteString(digestString(markerBefore))
		if initialized {
			entry.WriteString("\x00initialized=")
			entry.WriteString(nestedFingerprint)
		} else {
			entry.WriteString("\x00uninitialized")
		}
		fingerprint := digestString([]byte(entry.String()))
		state.paths[path] = fingerprint
		manifest.WriteString(path)
		manifest.WriteByte(0)
		manifest.WriteString(fingerprint)
		manifest.WriteByte(0)
	}
	state.manifest = digestString([]byte(manifest.String()))
	return state, nil
}

func indexedGitlinks(indexEntries map[string]string) map[string]string {
	gitlinks := map[string]string{}
	for path, metadata := range indexEntries {
		for _, entry := range strings.Split(metadata, "\x00") {
			if strings.Contains(entry, "mode=160000") {
				gitlinks[path] = metadata
				break
			}
		}
	}
	return gitlinks
}

func openRelativeSnapshotDirectory(root *securefs.Directory, path string) (*securefs.Directory, error) {
	components := strings.Split(path, "/")
	if len(components) == 0 {
		return nil, errors.New("indexed submodule path is empty")
	}
	current := root
	for index, component := range components {
		if component == "" || component == "." || component == ".." {
			if current != root {
				_ = current.Close()
			}
			return nil, errors.New("indexed submodule path contains an invalid component")
		}
		next, err := current.OpenDir(component)
		if err != nil {
			if current != root {
				_ = current.Close()
			}
			return nil, err
		}
		if index > 0 {
			_ = current.Close()
		}
		current = next
	}
	return current, nil
}
