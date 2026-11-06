package execution

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

type captureLeaseKey struct{}
type capturePending struct{ reason string }

func (e *capturePending) Error() string { return e.reason }

type captureReference struct {
	AttemptID string `json:"attempt_id"`
	Digest    string `json:"digest"`
	Files     int    `json:"files"`
	PatchSHA  string `json:"patch_sha256,omitempty"`
	IndexSHA  string `json:"index_sha256,omitempty"`
	IndexSize int64  `json:"index_size,omitempty"`
	Tree      string `json:"tree,omitempty"`
}
type captureFile struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Size int    `json:"size"`
	SHA  string `json:"sha256"`
	Blob string `json:"blob"`
}
type captureManifest struct {
	RunID       string            `json:"run_id"`
	AttemptID   string            `json:"attempt_id"`
	Repository  string            `json:"repository"`
	Base        string            `json:"base"`
	WorkDir     string            `json:"work_dir"`
	ParentPatch string            `json:"parent_patch"`
	Limit       runner.Bytes      `json:"artifact_limit"`
	Files       []captureFile     `json:"files"`
	TaskSeed    *taskCaptureScope `json:"task_seed,omitempty"`
}

type taskCaptureScope struct {
	Identity   runner.TaskIdentity `json:"identity"`
	Tree       string              `json:"tree"`
	WritePaths []string            `json:"write_paths"`
}

func captureDigest(data []byte) string            { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func captureManifestLimit(files int) runner.Bytes { return runner.Bytes((8 << 20) + files*256) }
func captureObjectBound(size int) int64           { return int64(size) + int64(size)/1000 + 4096 }

// Capture checkpoints literal source, then Git objects. Its mutable index is
// always rebuilt from that verified snapshot; an interrupted index is not proof.
func (w *Workspaces) Capture(ctx context.Context, a runner.Assignment) (result *runner.SourceResult, err error) {
	if err = w.lockContext(ctx); err != nil {
		return nil, err
	}
	defer w.mu.Unlock()
	if !fs.ValidPath(a.AttemptID) || strings.Contains(a.AttemptID, "/") {
		return nil, errors.New("invalid capture attempt identity")
	}
	r, err := w.load(a.RunID)
	if err != nil {
		return nil, err
	}
	if info, e := os.Lstat(filepath.Join(r.WorkDir, ".runner-tmp")); e == nil && !info.IsDir() {
		return nil, errors.New("scratch root is not a directory")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if r.Candidate != nil && r.Candidate.AttemptID == a.AttemptID {
		data, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, a.Limits.MaxArtifact)
		if err != nil {
			return nil, err
		}
		if r.Capture != nil && r.Capture.AttemptID == a.AttemptID && r.Capture.PatchSHA != "" && captureDigest(data) != r.Capture.PatchSHA {
			return nil, errors.New("retained capture artifact changed; preserve it")
		}
		return runner.Clone(r.Candidate), nil
	}
	defer func() {
		var exit *exec.ExitError
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) && (errors.Is(err, context.DeadlineExceeded) || errors.As(err, &exit) && exit.ExitCode() == -1) {
			err = &capturePending{reason: "owned source capture reached its observation deadline; rejoin the same snapshot"}
		}
	}()
	rel := "work/" + a.AttemptID + "/capture-v1"
	root := filepath.Join(w.disk.Root, filepath.FromSlash(rel))
	if err = captureDirectory(w.disk.Root, "work", a.AttemptID, "capture-v1"); err != nil {
		return nil, err
	}
	if r.Capture == nil || r.Capture.AttemptID != a.AttemptID {
		entries, e := os.ReadDir(root)
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if entry.Name() != "lease" && entry.Name() != "manifest.json" {
				return nil, errors.New("unreferenced capture scratch has unknown content; preserve it")
			}
		}
	}
	leasePath := filepath.Join(root, "lease")
	if info, e := os.Lstat(leasePath); e == nil && !info.Mode().IsRegular() {
		return nil, errors.New("capture lease is not a regular owned file")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	lease, err := os.OpenFile(leasePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// Never LOCK_UN: inherited Git descriptors keep this lease after owner exit.
	defer lease.Close()
	owned, err := tryCaptureLease(lease)
	if err != nil {
		return nil, err
	}
	if !owned {
		if r.Capture == nil || r.Capture.AttemptID != a.AttemptID {
			return nil, errors.New("capture writer ownership is not established")
		}
		if _, err = w.readCaptureManifest(r, a, rel); err != nil {
			return nil, err
		}
		return nil, &capturePending{reason: "same owned source capture writer is settling"}
	}
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	if err = w.verifyWorkspace(ctx, r); err != nil {
		return nil, err
	}
	var manifest captureManifest
	if r.Capture != nil && r.Capture.AttemptID == a.AttemptID {
		manifest, err = w.readCaptureManifest(r, a, rel)
	} else {
		if r.Capture != nil && (r.Candidate == nil || r.Candidate.AttemptID != r.Capture.AttemptID) {
			return nil, errors.New("another capture snapshot is unresolved; preserve it")
		}
		manifest, err = w.newCaptureManifest(ctx, r, a)
		if err == nil {
			data, _ := json.Marshal(manifest)
			// A crash before the workspace reference committed may leave the
			// exact manifest. It can be reused, never overwritten with drift.
			if prior, e := w.disk.Read(rel+"/manifest.json", captureManifestLimit(len(manifest.Files))); e == nil && !bytes.Equal(prior, data) {
				return nil, errors.New("uncommitted capture manifest differs; preserve it")
			} else if e != nil && !errors.Is(e, os.ErrNotExist) {
				return nil, e
			}
			err = w.disk.Write(rel+"/manifest.json", data, captureManifestLimit(len(manifest.Files)))
			if err == nil {
				r.Capture = &captureReference{AttemptID: a.AttemptID, Digest: captureDigest(data), Files: len(manifest.Files)}
				err = w.save(r)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if err = w.verifyCaptureSource(ctx, r, a, manifest); err != nil {
		return nil, err
	}
	if err = captureDirectory(root, "files"); err != nil {
		return nil, err
	}
	if err = captureDirectory(root, "objects"); err != nil {
		return nil, err
	}
	objectDir, err := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(root, "index"), "GIT_OBJECT_DIRECTORY=" + filepath.Join(root, "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.TrimSpace(string(objectDir)), "GIT_OPTIONAL_LOCKS=0"}
	var batch []string
	var expected []string
	var reserve int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if e := w.captureCapacity(reserve); e != nil {
			return e
		}
		out, e := git(ctx, r.WorkDir, env, nil, len(batch)*(len(r.Base)+2), append([]string{"hash-object", "-w", "--no-filters", "--"}, batch...)...)
		if e != nil {
			return e
		}
		if strings.TrimSpace(string(out)) != strings.Join(expected, "\n") {
			return errors.New("capture Git objects differ from frozen literal bytes")
		}
		batch, expected, reserve = nil, nil, 0
		return w.disk.Refresh()
	}
	for i, file := range manifest.Files {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if file.Mode == "0" {
			continue
		}
		name := fmt.Sprintf("%s/files/%06d", rel, i)
		data, e := w.disk.Read(name, a.Limits.MaxArtifact)
		if errors.Is(e, os.ErrNotExist) {
			var mode string
			data, mode, e = captureSourceFile(r.WorkDir, file.Path, a.Limits.MaxArtifact)
			if e == nil && (mode != file.Mode || captureDigest(data) != file.SHA) {
				return nil, errors.New("source changed before its capture snapshot was saved")
			}
			if e == nil {
				e = w.disk.Write(name, data, a.Limits.MaxArtifact)
			}
		}
		if e != nil {
			return nil, e
		}
		if len(data) != file.Size || captureDigest(data) != file.SHA {
			return nil, errors.New("capture literal snapshot changed; preserve it")
		}
		object := fmt.Sprintf("%s/objects/%s/%s", rel, file.Blob[:2], file.Blob[2:])
		compressed, e := w.disk.Read(object, runner.Bytes(captureObjectBound(file.Size)))
		if e == nil {
			zr, e := zlib.NewReader(bytes.NewReader(compressed))
			if e != nil {
				return nil, e
			}
			body, e := io.ReadAll(io.LimitReader(zr, int64(len(data))+64))
			ce := zr.Close()
			if e != nil {
				return nil, e
			}
			if ce != nil {
				return nil, ce
			}
			if !bytes.Equal(body, append([]byte(fmt.Sprintf("blob %d%c", len(data), 0)), data...)) {
				return nil, errors.New("checkpointed capture object changed; preserve it")
			}
			continue
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		batch = append(batch, filepath.Join(w.disk.Root, filepath.FromSlash(name)))
		expected = append(expected, file.Blob)
		reserve += captureObjectBound(len(data))
		if len(batch) == 64 {
			if err = flush(); err != nil {
				return nil, err
			}
		}
	}
	if err = flush(); err != nil {
		return nil, err
	}

	// A saved index is accepted only by its committed hash.
	if r.Capture.IndexSHA == "" {
		if err = resetCaptureIndex(root, "index"); err != nil {
			return nil, err
		}
		indexPath, e := git(ctx, r.WorkDir, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "index")
		if e != nil {
			return nil, e
		}
		info, e := os.Stat(strings.TrimSpace(string(indexPath)))
		if e != nil {
			return nil, e
		}
		if err = w.captureCapacity(2*info.Size() + int64(a.Limits.MaxArtifact) + 8192); err != nil {
			return nil, err
		}
		if _, err = git(ctx, r.WorkDir, env, nil, 4096, "read-tree", r.Base); err != nil {
			return nil, err
		}
		var entries strings.Builder
		for _, file := range manifest.Files {
			blob := file.Blob
			if file.Mode == "0" {
				blob = strings.Repeat("0", len(r.Base))
			}
			fmt.Fprintf(&entries, "%s %s\t%s%c", file.Mode, blob, file.Path, 0)
		}
		if _, err = git(ctx, r.WorkDir, env, []byte(entries.String()), 4096, "update-index", "-z", "--index-info"); err != nil {
			return nil, err
		}
		if err = w.disk.Refresh(); err != nil {
			return nil, err
		}
		index, e := w.disk.Read(rel+"/index", w.disk.Policy.MaxManaged-w.disk.Policy.Database.MaxSize)
		if e != nil {
			return nil, e
		}
		r.Capture.IndexSHA, r.Capture.IndexSize = captureDigest(index), int64(len(index))
		if err = w.save(r); err != nil {
			return nil, err
		}
	}
	if _, e := os.Lstat(filepath.Join(root, "index.lock")); !errors.Is(e, os.ErrNotExist) {
		return nil, errors.New("checkpointed capture index has an unexpected lock")
	}
	index, err := w.disk.Read(rel+"/index", runner.Bytes(r.Capture.IndexSize))
	if err != nil {
		return nil, err
	}
	if int64(len(index)) != r.Capture.IndexSize || captureDigest(index) != r.Capture.IndexSHA {
		return nil, errors.New("checkpointed capture index changed; preserve it")
	}
	if r.Capture.Tree == "" {
		// write-tree gets a disposable copy, preserving the committed index.
		if err = resetCaptureIndex(root, "tree-index"); err != nil {
			return nil, err
		}
		if err = w.disk.Write(rel+"/tree-index", index, runner.Bytes(r.Capture.IndexSize)); err != nil {
			return nil, err
		}
		treeEnv := append(append([]string{}, env...), "GIT_INDEX_FILE="+filepath.Join(root, "tree-index"))
		tree, e := git(ctx, r.WorkDir, treeEnv, nil, 256, "write-tree")
		if e != nil {
			return nil, e
		}
		r.Capture.Tree = strings.TrimSpace(string(tree))
		if len(r.Capture.Tree) != len(r.Base) {
			return nil, errors.New("captured Git tree identity differs")
		}
		if err = w.save(r); err != nil {
			return nil, err
		}
	}
	patch, err := git(ctx, r.WorkDir, env, nil, int(a.Limits.MaxArtifact), "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", r.Base, "--")
	if err != nil {
		return nil, err
	}
	if err = w.verifyCaptureSource(ctx, r, a, manifest); err != nil {
		return nil, err
	}
	if err = w.disk.Refresh(); err != nil {
		return nil, err
	}
	result = &runner.SourceResult{Tree: r.Capture.Tree, Repository: r.Repository, Base: r.Base, WorkDir: r.WorkDir, AttemptID: a.AttemptID, InputPatchID: manifest.ParentPatch, PatchID: a.AttemptID + "/changes.patch"}
	if prior, e := w.disk.Read("artifacts/"+result.PatchID, a.Limits.MaxArtifact); e == nil && !bytes.Equal(prior, patch) {
		return nil, errors.New("retained patch differs from the owned snapshot; preserve it")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if err = w.disk.Write("artifacts/"+result.PatchID, patch, a.Limits.MaxArtifact); err != nil {
		return nil, err
	}
	r.Candidate = result
	r.Capture.PatchSHA = captureDigest(patch)
	if err = w.save(r); err != nil {
		return nil, err
	}
	return result, nil
}

func captureDirectory(root string, parts ...string) error {
	for _, part := range parts {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			if err = os.Mkdir(root, 0700); err != nil {
				return err
			}
			info, err = os.Lstat(root)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("capture directory ownership changed")
		}
	}
	return nil
}

func captureSourceFile(root, name string, limit runner.Bytes) ([]byte, string, error) {
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		root = filepath.Join(root, part)
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			return nil, "0", nil
		}
		if err != nil {
			return nil, "", err
		}
		if !info.IsDir() {
			return nil, "", errors.New("source parent is not a regular directory")
		}
	}
	file := filepath.Join(root, parts[len(parts)-1])
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "0", nil
	}
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, "", errors.New("source contains unsupported or oversized file")
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", errors.New("source changed while opening capture")
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, "", err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if len(data) > int(limit) || !info.ModTime().Equal(after.ModTime()) || info.Size() != after.Size() {
		return nil, "", errors.New("source changed while reading capture")
	}
	mode := "100644"
	if info.Mode()&0111 != 0 {
		mode = "100755"
	}
	return data, mode, nil
}

func capturePaths(ctx context.Context, r workspaceRecord) ([]string, error) {
	info, err := os.Lstat(r.WorkDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("source root ownership changed during capture")
	}
	paths, err := git(ctx, r.WorkDir, nil, nil, 1<<20, "diff-index", "--name-only", "-z", "--no-ext-diff", "--no-textconv", r.Base, "--", ".", ":(exclude).runner-tmp")
	if err != nil {
		return nil, err
	}
	other, err := git(ctx, r.WorkDir, nil, nil, 1<<20, "ls-files", "--others", "--exclude-standard", "-z", "--", ".", ":(exclude).runner-tmp")
	if err != nil {
		return nil, err
	}
	if len(paths)+len(other) > 1<<20 {
		return nil, runner.ErrCapacity
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range bytes.Split(append(paths, other...), []byte{0}) {
		name := string(raw)
		if name == "" || seen[name] {
			continue
		}
		if !fs.ValidPath(name) {
			return nil, errors.New("unsafe source path")
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func (w *Workspaces) newCaptureManifest(ctx context.Context, r workspaceRecord, a runner.Assignment) (captureManifest, error) {
	m := captureManifest{RunID: a.RunID, AttemptID: a.AttemptID, Repository: r.Repository, Base: r.Base, WorkDir: r.WorkDir, Limit: a.Limits.MaxArtifact, ParentPatch: r.SeedPatchID}
	if a.TaskSource != nil && a.TaskSource.Identity.ItemID != "" {
		seed := r.TaskSources[a.TaskSource.Identity.GroupStep]
		if seed == nil || !seed.Installed || !sameTaskAuthority(seed.Authority, *a.TaskSource) {
			return m, errors.New("task capture scope differs from its immutable seed")
		}
		m.TaskSeed = &taskCaptureScope{Identity: seed.Authority.Identity, Tree: seed.SeedTree, WritePaths: runner.Clone(seed.Authority.WritePaths)}
	}
	if r.Candidate != nil {
		m.ParentPatch = r.Candidate.PatchID
	}
	paths, err := capturePaths(ctx, r)
	if err != nil {
		return m, err
	}
	for _, name := range paths {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		data, mode, e := captureSourceFile(r.WorkDir, name, a.Limits.MaxArtifact)
		if e != nil {
			return m, e
		}
		file := captureFile{Path: name, Mode: mode, Size: len(data)}
		if mode != "0" {
			file.SHA = captureDigest(data)
			h := sha1.New()
			if len(r.Base) == 64 {
				h = sha256.New()
			}
			fmt.Fprintf(h, "blob %d%c", len(data), 0)
			h.Write(data)
			file.Blob = hex.EncodeToString(h.Sum(nil))
		}
		m.Files = append(m.Files, file)
	}
	return m, nil
}

func (w *Workspaces) readCaptureManifest(r workspaceRecord, a runner.Assignment, rel string) (captureManifest, error) {
	var m captureManifest
	if r.Capture.Files < 0 || r.Capture.Files > 1<<19 || (len(r.Base) != 40 && len(r.Base) != 64) {
		return m, errors.New("invalid capture manifest size")
	}
	for _, field := range []struct {
		value string
		width int
	}{{r.Capture.Digest, 64}, {r.Capture.IndexSHA, 64}, {r.Capture.PatchSHA, 64}, {r.Capture.Tree, len(r.Base)}} {
		if field.value == "" {
			continue
		}
		if _, e := hex.DecodeString(field.value); e != nil || len(field.value) != field.width {
			return m, errors.New("capture checkpoint identity is invalid")
		}
	}
	if r.Capture.IndexSHA == "" && (r.Capture.IndexSize != 0 || r.Capture.Tree != "") || r.Capture.IndexSHA != "" && (r.Capture.IndexSize < 1 || r.Capture.IndexSize > int64(w.disk.Policy.MaxManaged)) {
		return m, errors.New("capture index checkpoint is invalid")
	}
	data, err := w.disk.Read(rel+"/manifest.json", captureManifestLimit(r.Capture.Files))
	if err != nil {
		return m, err
	}
	if captureDigest(data) != r.Capture.Digest || json.Unmarshal(data, &m) != nil || m.RunID != a.RunID || m.AttemptID != a.AttemptID || m.Repository != r.Repository || m.Base != r.Base || m.WorkDir != r.WorkDir || m.Limit != a.Limits.MaxArtifact || len(m.Files) != r.Capture.Files {
		return m, errors.New("capture manifest identity changed; preserve it")
	}
	var paths int
	for i, file := range m.Files {
		paths += len(file.Path) + 1
		_, shaErr := hex.DecodeString(file.SHA)
		_, blobErr := hex.DecodeString(file.Blob)
		if !fs.ValidPath(file.Path) || i > 0 && m.Files[i-1].Path >= file.Path || file.Size < 0 || file.Size > int(a.Limits.MaxArtifact) || (file.Mode != "0" && file.Mode != "100644" && file.Mode != "100755") || file.Mode != "0" && (len(file.SHA) != 64 || len(file.Blob) != len(r.Base) || shaErr != nil || blobErr != nil) || file.Mode == "0" && (file.Size != 0 || file.SHA != "" || file.Blob != "") {
			return m, errors.New("capture manifest file is invalid")
		}
	}
	if paths > 1<<20 {
		return m, runner.ErrCapacity
	}
	return m, nil
}

func resetCaptureIndex(root, name string) error {
	for _, file := range []string{name + ".lock", name} {
		path := filepath.Join(root, file)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("capture index or lock is not an owned regular file")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspaces) verifyCaptureSource(ctx context.Context, r workspaceRecord, a runner.Assignment, m captureManifest) error {
	paths, err := capturePaths(ctx, r)
	if err != nil {
		return err
	}
	if len(paths) != len(m.Files) {
		return errors.New("source path inventory changed during capture")
	}
	for i, file := range m.Files {
		if err = ctx.Err(); err != nil {
			return err
		}
		if paths[i] != file.Path {
			return errors.New("source path inventory changed during capture")
		}
		data, mode, e := captureSourceFile(r.WorkDir, file.Path, a.Limits.MaxArtifact)
		if e != nil {
			return e
		}
		if mode != file.Mode || len(data) != file.Size || mode != "0" && captureDigest(data) != file.SHA {
			return errors.New("source bytes or mode changed during capture; preserve the snapshot")
		}
	}
	return nil
}

func (w *Workspaces) captureCapacity(extra int64) error {
	if err := w.disk.Refresh(); err != nil {
		return err
	}
	if w.disk.Used()+extra > int64(w.disk.Policy.MaxManaged-w.disk.Policy.Database.MaxSize)-4096 {
		return runner.ErrCapacity
	}
	free, err := runner.FreeBytes(w.disk.Root)
	if err != nil {
		return err
	}
	if free < int64(w.disk.Policy.MinFree)+extra {
		return runner.ErrCapacity
	}
	return nil
}
