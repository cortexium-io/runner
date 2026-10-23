package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/subprocess"
)

// Leave headroom for the fixed privileged options, Windows argument quoting,
// separators and terminating NUL, beyond the bounded entry payload.
const candidateBatchPaths = 128
const candidateBatchBytes = 16 * 1024

type candidateIndexEntry struct {
	path, mode, objectID string
	exists               bool
}

// Hash every regular file's literal bytes, including unchanged tracked files.
// Batching changes process count, never the freshness or content checks. Rare
// symlink/gitlink/type cases retain the existing per-path implementation.
func (p GitProvider) candidatePathEntries(ctx context.Context, profile subprocess.PrivilegedGitProfile, paths []string, writeObjects bool) ([]candidateIndexEntry, error) {
	entries := make([]candidateIndexEntry, len(paths))
	pending := make([]int, 0, candidateBatchPaths)
	pendingBytes := 0
	flush := func() error {
		if len(pending) == 0 {
			return nil
		}
		args := []string{"hash-object", "--no-filters"}
		if writeObjects {
			args = append(args, "-w")
		}
		args = append(args, "--")
		for _, i := range pending {
			args = append(args, entries[i].path)
		}
		result, err := p.privilegedGit(ctx, profile, args...)
		if err != nil {
			return fmt.Errorf("hash literal candidate batch: %w", commandError(err, result))
		}
		objects := strings.Split(strings.TrimSuffix(result.Stdout, "\n"), "\n")
		if len(objects) != len(pending) {
			return fmt.Errorf("candidate blob count does not match path batch")
		}
		for j, object := range objects {
			if !validObjectID(object) {
				return fmt.Errorf("Git returned invalid candidate batch object identity")
			}
			entries[pending[j]].objectID = object
		}
		pending, pendingBytes = pending[:0], 0
		return nil
	}
	for i, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries[i].path = path
		absolute := filepath.Join(profile.WorkTree, filepath.FromSlash(path))
		if !pathInsideOrEqualLexical(absolute, profile.WorkTree) {
			return nil, fmt.Errorf("candidate path escapes worktree: %q", path)
		}
		info, err := os.Lstat(absolute)
		if err == nil && info.Mode().IsRegular() {
			entries[i].mode, entries[i].exists = "100644", true
			if info.Mode().Perm()&0o111 != 0 {
				entries[i].mode = "100755"
			}
			if len(pending) == candidateBatchPaths || pendingBytes+len(path)+1 > candidateBatchBytes {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			pending = append(pending, i)
			pendingBytes += len(path) + 1
			continue
		}
		mode, object, exists, err := p.candidatePathEntry(ctx, profile, path, writeObjects)
		if err != nil {
			return nil, err
		}
		entries[i].mode, entries[i].objectID, entries[i].exists = mode, object, exists
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (p GitProvider) stageCandidatePaths(ctx context.Context, profile subprocess.PrivilegedGitProfile, paths []string) error {
	entries, err := p.candidatePathEntries(ctx, profile, paths, true)
	if err != nil {
		return err
	}
	// Remove vanished paths before inserting replacements, including a tracked
	// file becoming a directory containing new untracked paths.
	args := []string{"--force-remove", "--"}
	count, size := 0, 0
	flush := func() error {
		if count == 0 {
			return nil
		}
		if err := p.updateCandidateIndex(ctx, profile, args...); err != nil {
			return fmt.Errorf("remove deleted candidate batch: %w", err)
		}
		args, count, size = args[:2], 0, 0
		return nil
	}
	for _, entry := range entries {
		if entry.exists {
			continue
		}
		if count == candidateBatchPaths || size+len(entry.path)+1 > candidateBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		args = append(args, entry.path)
		count++
		size += len(entry.path) + 1
	}
	if err := flush(); err != nil {
		return err
	}
	args, count, size = []string{"--add"}, 0, 0
	flush = func() error {
		if count == 0 {
			return nil
		}
		if err := p.updateCandidateIndex(ctx, profile, args...); err != nil {
			return fmt.Errorf("stage literal candidate batch: %w", err)
		}
		args, count, size = args[:1], 0, 0
		return nil
	}
	for _, entry := range entries {
		if !entry.exists {
			continue
		}
		entryBytes := len("--cacheinfo") + len(entry.mode) + len(entry.objectID) + len(entry.path) + 4
		if count == candidateBatchPaths || size+entryBytes > candidateBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		args = append(args, "--cacheinfo", entry.mode, entry.objectID, entry.path)
		count++
		size += entryBytes
	}
	return flush()
}
