package execution

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// Only declared retained artifacts are copied into the protected input subtree.
// The worker never receives access to the artifact store itself. Patch restoration
// for writable roles remains Workspaces.Prepare's separate source operation.
func (c *Codex) workspaceInputs(j nativeJob) error {
	if j.Assignment.Setup.Profile.Access == "workspace-read" {
		if id := j.Assignment.Inputs["patch"].ArtifactID; id != "" && (j.Workspace.Candidate == nil || j.Workspace.Candidate.PatchID != id) {
			return errors.New("review patch differs from the workspace candidate")
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

func stagedInputData(disk *runner.Disk, a runner.Assignment) (map[string][]byte, error) {
	files := map[string][]byte{}
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
