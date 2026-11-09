package execution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Declared artifacts and overflowed inline JSON use the protected input subtree.
// The worker never receives access to the artifact store itself. Patch restoration
// for writable roles remains Workspaces.Prepare's separate source operation.
func (c *Codex) workspaceInputs(j nativeJob) error {
	if j.Assignment.Setup.Profile.Access == "workspace-read" {
		if id := j.Assignment.Inputs["patch"].ArtifactID; id != "" {
			if j.Workspace == nil {
				return errors.New("review source workspace is absent")
			}
			if j.Workspace.Candidate == nil {
				if err := c.verifyReviewSeed(j, id); err != nil {
					return err
				}
			} else if j.Workspace.Candidate.PatchID != id {
				return errors.New("review patch differs from the workspace candidate")
			}
		}
	}
	files, err := stagedInputData(c.disk, j.Assignment)
	if err != nil {
		return err
	}
	dir, err := stagedInputDirectory(j.Workspace.WorkDir, j.Assignment.AttemptID, len(files) != 0)
	if err != nil || dir == "" {
		return err
	}
	if err = verifyStagedInputs(dir, files); err != nil {
		return err
	}
	for name, data := range files {
		file := filepath.Join(dir, name)
		if _, err = os.Lstat(file); err == nil {
			continue // Verified idempotent restoration, never overwrite drift.
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		rel, err := filepath.Rel(c.disk.Root, file)
		if err != nil {
			return err
		}
		if err = c.disk.Write(filepath.ToSlash(rel), data, j.Assignment.Limits.MaxArtifact); err != nil {
			return err
		}
	}
	return nil
}

// A prepared retained seed may be reviewed before another source writer exists.
// It grants no candidate, capture or approval: verify the same ready owned source
// literally, then use the existing read-only input namespace.
func (c *Codex) verifyReviewSeed(j nativeJob, patchID string) error {
	bad := errors.New("review patch is not the same ready owned workspace seed")
	if c.workspaces == nil || j.Workspace == nil || j.Assignment.Source == nil {
		return bad
	}
	c.mu.Lock()
	saved, err := c.loadLocked(j.Assignment.AttemptID)
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(saved.Assignment, j.Assignment) {
		return bad
	}
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if !j.Assignment.Deadline.IsZero() {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, j.Assignment.Deadline)
		defer cancel()
	}
	w := c.workspaces
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, err := w.load(j.Assignment.RunID)
	if err != nil {
		return err
	}
	snapshot := j.Workspace
	repository, err := filepath.EvalSymlinks(j.Assignment.Repository)
	if err != nil {
		return err
	}
	if r.RunID != snapshot.RunID || r.Repository != repository || r.Repository != snapshot.Repository || r.Base != snapshot.Base || r.WorkDir != snapshot.WorkDir || r.Candidate != nil || r.Released || !r.SeedReady || !snapshot.SeedReady || r.SeedPatchID != patchID || snapshot.SeedPatchID != patchID || r.ReadyGeneration != snapshot.ReadyGeneration || r.CleanupIntent != nil || r.SourcePreservation != nil && !r.SourcePreservation.Complete {
		return bad
	}
	base, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", "--end-of-options", j.Assignment.Source.Ref+"^{commit}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(base)) != r.Base {
		return bad
	}
	if err = w.verifyWorkspace(ctx, r); err != nil {
		return err
	}
	_, _, err = w.retainedCandidate(ctx, r, true)
	return err
}

func stagedInputData(disk *runner.Disk, a runner.Assignment) (map[string][]byte, error) {
	files := map[string][]byte{}
	if a.Setup.Profile.Harness == "codex" {
		_, _, inline, err := nativePayloadPlan(a, time.Time{})
		if err != nil {
			return nil, err
		}
		for name, data := range inline {
			files[name] = data
		}
	}
	for name, value := range a.Inputs {
		if value.ArtifactID == "" || name == "patch" && a.Setup.Profile.Access == "workspace-write" {
			continue
		}
		if !fs.ValidPath(name) || strings.Contains(name, "/") {
			return nil, errors.New("invalid staged input name")
		}
		data, err := disk.Read("artifacts/"+value.ArtifactID, a.Limits.MaxArtifact)
		if err != nil {
			return nil, err
		}
		files[name] = data
	}
	return files, nil
}

func stagedInputDirectory(work, attempt string, create bool) (string, error) {
	if !fs.ValidPath(attempt) || strings.Contains(attempt, "/") {
		return "", errors.New("invalid staged input attempt")
	}
	dir := work
	for _, part := range []string{".runner-tmp", "inputs", attempt} {
		dir = filepath.Join(dir, part)
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if !create {
				return "", nil
			}
			if err = os.Mkdir(dir, 0700); err != nil {
				return "", err
			}
		} else if err != nil {
			return "", err
		} else if !info.IsDir() {
			return "", errors.New("staged inputs must not traverse links or non-directories")
		}
	}
	return dir, nil
}

func verifyStagedInputs(dir string, files map[string][]byte) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		want, ok := files[entry.Name()]
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !ok || !info.Mode().IsRegular() {
			return errors.New("staged input ownership differs; preserve and inspect it")
		}
		if info.Size() != int64(len(want)) {
			return errors.New("staged input size differs from its retained artifact; preserve and inspect it")
		}
		file, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
			file.Close()
			return errors.New("staged input changed during verification; preserve and inspect it")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, int64(len(want))+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if !bytes.Equal(data, want) {
			return errors.New("staged input differs from its retained artifact; preserve and inspect it")
		}
	}
	return nil
}
