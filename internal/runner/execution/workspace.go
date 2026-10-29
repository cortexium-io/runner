package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cortexium-io/runner/internal/runner"
)

// Workspaces owns Git resources; it does not schedule attempts or accept code.
type Workspaces struct {
	mu   sync.Mutex
	disk *runner.Disk
}
type workspaceRecord struct {
	RunID           string               `json:"run_id"`
	Repository      string               `json:"repository"`
	Base            string               `json:"base"`
	WorkDir         string               `json:"work_dir"`
	ReadyGeneration string               `json:"ready_generation,omitempty"`
	Released        bool                 `json:"released,omitempty"`
	Candidate       *runner.SourceResult `json:"candidate,omitempty"`
}

func workspaceName(id string) string { return "jobs/" + id + ".workspace.json" }
func (w *Workspaces) load(id string) (workspaceRecord, error) {
	var r workspaceRecord
	if !fs.ValidPath(id) || strings.Contains(id, "/") {
		return r, errors.New("invalid run identity")
	}
	b, e := w.disk.Read(workspaceName(id), w.disk.Policy.Database.MaxRecord)
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.RunID != id || r.WorkDir != filepath.Join(w.disk.Root, "work", id, "source") || !filepath.IsAbs(r.Repository) || r.Base == "" {
		return r, errors.New("invalid owned workspace record")
	}
	return r, nil
}
func (w *Workspaces) save(r workspaceRecord) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return w.disk.Write(workspaceName(r.RunID), b, w.disk.Policy.Database.MaxRecord)
}
func pin(id string) string { return "refs/runner/runs/" + id + "/base" }
func gitEnvironment() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
}

type boundedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		b.err = runner.ErrCapacity
		return 0, b.err
	}
	return b.Buffer.Write(p)
}
func git(ctx context.Context, dir string, extra []string, input []byte, maxBytes int, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.sparseCheckout=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnvironment(), extra...)
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedBuffer{limit: maxBytes}
	diagnostic := &diagnosticTail{limit: 1024}
	cmd.Stdout = out
	cmd.Stderr = diagnostic
	e := cmd.Run()
	if out.err != nil {
		return nil, out.err
	}
	if e != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], e, runner.Clip(string(diagnostic.bytes())))
	}
	return out.Bytes(), nil
}
func (w *Workspaces) Prepare(ctx context.Context, a runner.Assignment) (workspaceRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(a.RunID)
	if errors.Is(e, os.ErrNotExist) {
		if a.Source == nil || a.Source.Ref == "" || a.Repository == "" {
			return r, errors.New("writable execution needs a repository and explicit source ref")
		}
		repo, er := filepath.EvalSymlinks(a.Repository)
		if er != nil {
			return r, er
		}
		top, er := git(ctx, repo, nil, nil, 4096, "rev-parse", "--show-toplevel")
		if er != nil || strings.TrimSpace(string(top)) != repo {
			return r, errors.New("source workspace must name the repository root")
		}
		base, er := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", "--end-of-options", a.Source.Ref+"^{commit}")
		if er != nil {
			return r, er
		}
		r = workspaceRecord{
			RunID:      a.RunID,
			Repository: repo,
			Base:       strings.TrimSpace(string(base)),
			WorkDir:    filepath.Join(w.disk.Root, "work", a.RunID, "source"),
		}
		if er = w.save(r); er != nil {
			return r, er
		}
	} else if e != nil {
		return r, e
	}
	if r.Released {
		return r, errors.New("run workspace was already released")
	}
	if _, e = git(ctx, r.Repository, nil, nil, 1024, "update-ref", pin(a.RunID), r.Base); e != nil {
		return r, e
	}
	if _, e = os.Lstat(filepath.Join(r.WorkDir, ".git")); errors.Is(e, os.ErrNotExist) {
		tree, er := git(ctx, r.Repository, nil, nil, 1<<20, "ls-tree", "-rz", r.Base)
		if er != nil {
			return r, er
		}
		for _, entry := range bytes.Split(tree, []byte{0}) {
			if len(entry) == 0 {
				continue
			}
			parts := bytes.SplitN(entry, []byte{'\t'}, 2)
			if len(parts) != 2 || !(bytes.HasPrefix(parts[0], []byte("100644 ")) || bytes.HasPrefix(parts[0], []byte("100755 "))) || filepath.Base(string(parts[1])) == ".gitattributes" || filepath.Base(string(parts[1])) == ".gitmodules" || string(parts[1]) == ".runner-tmp" || strings.HasPrefix(string(parts[1]), ".runner-tmp/") || strings.HasPrefix(string(parts[1]), ".codex/") {
				return r, errors.New("first writable slice supports regular files without Git attributes/submodules")
			}
		}
		if _, e = git(ctx, r.Repository, nil, nil, 4096, "worktree", "add", "--detach", r.WorkDir, r.Base); e != nil {
			return r, e
		}
	} else if e != nil {
		return r, e
	}
	head, e := git(ctx, r.WorkDir, nil, nil, 256, "rev-parse", "HEAD")
	if e != nil || strings.TrimSpace(string(head)) != r.Base {
		return r, errors.New("owned workspace base differs; preserve and inspect it before recovery")
	}
	common, e := git(ctx, r.WorkDir, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return r, e
	}
	expected, e := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil || !bytes.Equal(common, expected) {
		return r, errors.New("workspace Git ownership differs from the recorded repository")
	}
	return r, nil
}
func (w *Workspaces) Capture(ctx context.Context, a runner.Assignment) (*runner.SourceResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(a.RunID)
	if e != nil {
		return nil, e
	}
	if r.Candidate != nil && r.Candidate.AttemptID == a.AttemptID {
		if r.Candidate.PatchID != "" {
			if _, e = w.disk.ArtifactSize(r.Candidate.PatchID); e != nil {
				return nil, e
			}
		}
		return runner.Clone(r.Candidate), nil
	}
	if info, e := os.Lstat(filepath.Join(r.WorkDir, ".runner-tmp")); e == nil && !info.IsDir() {
		return nil, errors.New("scratch root is not a directory")
	}
	scratch := filepath.Join(w.disk.Root, "work", a.AttemptID, "capture")
	if e = os.MkdirAll(filepath.Join(scratch, "objects"), 0700); e != nil {
		return nil, e
	}
	defer os.RemoveAll(scratch)
	objectDir, e := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if e != nil {
		return nil, e
	}
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(scratch, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(scratch, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.TrimSpace(string(objectDir)),
	}
	if _, e = git(ctx, r.WorkDir, env, nil, 4096, "read-tree", r.Base); e != nil {
		return nil, e
	}
	// Discover changes using the workspace index, then collect literal bytes into
	// the separate index below. Unchanged assets are already in the base tree and
	// must neither consume the patch allowance nor be rehashed one file at a time.
	paths, e := git(ctx, r.WorkDir, nil, nil, 1<<20, "diff-index", "--name-only", "-z", "--no-ext-diff", "--no-textconv", r.Base, "--", ".", ":(exclude).runner-tmp")
	if e != nil {
		return nil, e
	}
	untracked, e := git(ctx, r.WorkDir, nil, nil, 1<<20, "ls-files", "--others", "--exclude-standard", "-z", "--", ".", ":(exclude).runner-tmp")
	if e != nil {
		return nil, e
	}
	if len(paths)+len(untracked) > 1<<20 {
		return nil, runner.ErrCapacity
	}
	paths = append(paths, untracked...)
	seen := map[string]bool{}
	for _, p := range bytes.Split(paths, []byte{0}) {
		name := string(p)
		if name == "" || seen[name] || name == ".runner-tmp" || strings.HasPrefix(name, ".runner-tmp/") {
			continue
		}
		seen[name] = true
		if !fs.ValidPath(name) {
			return nil, errors.New("unsafe source path")
		}
		file := filepath.Join(r.WorkDir, filepath.FromSlash(name))
		info, er := os.Lstat(file)
		if errors.Is(er, os.ErrNotExist) {
			_, e = git(ctx, r.WorkDir, env, nil, 4096, "update-index", "--force-remove", "--", name)
			if e != nil {
				return nil, e
			}
			continue
		}
		if er != nil {
			return nil, er
		}
		if !info.Mode().IsRegular() || info.Size() > int64(a.Limits.MaxArtifact) {
			return nil, errors.New("source contains unsupported or oversized file")
		}
		f, er := os.Open(file)
		if er != nil {
			return nil, er
		}
		data, er := io.ReadAll(io.LimitReader(f, int64(a.Limits.MaxArtifact)+1))
		f.Close()
		if er != nil {
			return nil, er
		}
		if len(data) > int(a.Limits.MaxArtifact) {
			return nil, runner.ErrCapacity
		}
		hash, er := git(ctx, r.WorkDir, env, data, 256, "hash-object", "-w", "--stdin")
		if er != nil {
			return nil, er
		}
		mode := "100644"
		if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		if _, e = git(ctx, r.WorkDir, env, nil, 4096, "update-index", "--add", "--cacheinfo", mode+","+strings.TrimSpace(string(hash))+","+name); e != nil {
			return nil, e
		}
	}
	patch, e := git(ctx, r.WorkDir, env, nil, int(a.Limits.MaxArtifact), "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", r.Base, "--")
	if e != nil {
		return nil, e
	}
	tree, e := git(ctx, r.WorkDir, env, nil, 256, "write-tree")
	if e != nil {
		return nil, e
	}
	result := &runner.SourceResult{
		Tree:       strings.TrimSpace(string(tree)),
		Repository: r.Repository,
		Base:       r.Base,
		WorkDir:    r.WorkDir,
		AttemptID:  a.AttemptID,
	}
	if r.Candidate != nil {
		result.InputPatchID = r.Candidate.PatchID
	}
	if e = w.disk.Refresh(); e != nil {
		return nil, e
	}
	result.PatchID = a.AttemptID + "/changes.patch"
	if e = w.disk.Write("artifacts/"+result.PatchID, patch, a.Limits.MaxArtifact); e != nil {
		return nil, e
	}
	r.Candidate = result
	if e = w.save(r); e != nil {
		return nil, e
	}
	return result, nil
}
func (w *Workspaces) ReleaseRun(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.Released {
		return nil
	}
	if r.Candidate != nil && r.Candidate.PatchID != "" {
		if _, e = w.disk.ArtifactSize(r.Candidate.PatchID); e != nil {
			return e
		}
	}
	if _, e = os.Lstat(r.WorkDir); e == nil {
		if _, e = git(ctx, r.Repository, nil, nil, 4096, "worktree", "remove", "--force", r.WorkDir); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = w.disk.Remove("work/" + id); e != nil {
		return e
	}
	r.Released = true
	return w.save(r)
}
func (w *Workspaces) ForgetRun(ctx context.Context, id string) error {
	if e := w.ReleaseRun(ctx, id); e != nil {
		return e
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if _, e = git(ctx, r.Repository, nil, nil, 4096, "update-ref", "-d", pin(id), r.Base); e != nil {
		return e
	}
	return w.disk.Remove(workspaceName(id))
}

func (w *Workspaces) MarkReady(id, generation string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(id)
	if e != nil {
		return e
	}
	r.ReadyGeneration = generation
	return w.save(r)
}
func (w *Workspaces) ReleaseAttempt(a runner.Assignment) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, e := w.load(a.RunID)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	parent := filepath.Join(r.WorkDir, ".runner-tmp")
	info, e := os.Lstat(parent)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return errors.New("refusing cleanup through a non-directory scratch root")
	}
	if !fs.ValidPath(a.AttemptID) || strings.Contains(a.AttemptID, "/") {
		return errors.New("invalid attempt identity")
	}
	if e = os.RemoveAll(filepath.Join(parent, a.AttemptID)); e != nil {
		return e
	}
	return w.disk.Refresh()
}
