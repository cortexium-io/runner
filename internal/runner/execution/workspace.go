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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Workspaces owns Git resources; it does not schedule attempts or accept code.
type Workspaces struct {
	mu            sync.Mutex
	disk          *runner.Disk
	read          func(context.Context, string) ([]byte, error)
	readOnlyPaths []string
	nativeLimit   runner.Bytes
}
type workspaceRecord struct {
	RunID              string                          `json:"run_id"`
	Repository         string                          `json:"repository"`
	Base               string                          `json:"base"`
	WorkDir            string                          `json:"work_dir"`
	SeedPatchID        string                          `json:"seed_patch_id,omitempty"`
	SeedReady          bool                            `json:"seed_ready,omitempty"`
	ReadyGeneration    string                          `json:"ready_generation,omitempty"`
	Released           bool                            `json:"released,omitempty"`
	Candidate          *runner.SourceResult            `json:"candidate,omitempty"`
	CandidateCommit    string                          `json:"candidate_commit,omitempty"`
	CandidatePins      map[string]string               `json:"candidate_pins,omitempty"`
	Capture            *captureReference               `json:"capture,omitempty"`
	CleanupIntent      *workspaceCleanupIntent         `json:"cleanup_intent,omitempty"`
	SourcePreservation *sourcePreservation             `json:"source_preservation,omitempty"`
	TaskSources        map[string]*taskWorkspaceRecord `json:"task_sources,omitempty"`
	AdoptedSource      *adoptedSourceRecord            `json:"adopted_source,omitempty"`
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
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1",
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
func (b *boundedBuffer) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(struct{ io.Writer }{b}, r)
}
func gitCommand(ctx context.Context, dir string, extra []string, input []byte, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"-c",
		"core.hooksPath=/dev/null",
		"-c",
		"core.fsmonitor=false",
		"-c",
		"core.sparseCheckout=false",
		"-c",
		"core.attributesFile=/dev/null",
		"-c",
		"core.autocrlf=false",
		"-c",
		"core.eol=lf",
	}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitEnvironment(), extra...)
	cmd.Stdin = bytes.NewReader(input)
	if lease, ok := ctx.Value(captureLeaseKey{}).(*os.File); ok {
		cmd.ExtraFiles = []*os.File{lease}
		setNativeProcessGroup(cmd)
		cmd.Cancel = func() error { return killCommandGroup(cmd) }
		cmd.WaitDelay = time.Second
	}
	return cmd
}

func git(ctx context.Context, dir string, extra []string, input []byte, maxBytes int, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, dir, extra, input, args...)
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
	if err := w.lockContext(ctx); err != nil {
		return workspaceRecord{}, err
	}
	defer w.mu.Unlock()
	seed := ""
	if (a.Setup.Profile.Harness == "codex" && a.Setup.Profile.Access == "workspace-write") ||
		(a.Setup.Profile.Harness == "command" && a.Setup.Profile.Access == "workspace-read") ||
		(a.Setup.Profile.Harness == "task-source" && (a.Setup.Profile.Mode == "seed" || a.Setup.Profile.Mode == "join")) ||
		(a.Setup.Profile.Harness == "release" && a.Setup.Profile.Mode == "source-adopt") {
		seed = a.Inputs["patch"].ArtifactID
	}
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
			RunID:       a.RunID,
			Repository:  repo,
			Base:        strings.TrimSpace(string(base)),
			WorkDir:     filepath.Join(w.disk.Root, "work", a.RunID, "source"),
			SeedPatchID: seed,
		}
		if er = w.save(r); er != nil {
			return r, er
		}
	} else if e != nil {
		return r, e
	}
	if r.CleanupIntent != nil || r.SourcePreservation != nil && !r.SourcePreservation.Complete {
		return r, errors.New("workspace cleanup or source preservation is unresolved; reconcile its owned operation before preparation")
	}
	// Released stays durable until restoration is ready. An interrupted restore
	// may rebuild only its owned pre-execution checkout; ready edits are untouched.
	restoring := r.Released
	if !restoring && r.Candidate != nil {
		if _, e = os.Lstat(r.WorkDir); errors.Is(e, os.ErrNotExist) {
			// Removal can finish before its Released save. Persist the same
			// restore barrier before allocating anything for the missing checkout.
			r.Released = true
			if e = w.disk.Refresh(); e != nil {
				return r, e
			}
			if e = w.save(r); e != nil {
				return r, e
			}
			restoring = true
		} else if e != nil {
			return r, e
		}
	}
	if seed != "" && seed != r.SeedPatchID && (r.Candidate == nil || seed != r.Candidate.PatchID) {
		return r, errors.New("workspace seed differs from its saved preparation; preserve existing source")
	}
	if r.SeedPatchID != "" && !r.SeedReady && (r.Candidate != nil || r.ReadyGeneration != "") {
		return r, errors.New("seed readiness conflicts with later workspace evidence; preserve and inspect it")
	}
	if _, e = git(ctx, r.Repository, nil, nil, 1024, "update-ref", pin(a.RunID), r.Base); e != nil {
		return r, e
	}
	var patch []byte
	var patchBytes int64
	if restoring {
		patch, patchBytes, e = w.retainedCandidate(ctx, r, false)
		if e != nil {
			return r, e
		}
	} else if r.SeedPatchID != "" && !r.SeedReady {
		patch, e = w.readSeed(ctx, r, a.Limits.MaxArtifact)
		if e != nil {
			return r, e
		}
		if a.Limits.MaxArtifact < 1 || len(patch) > int(a.Limits.MaxArtifact) {
			return r, errors.New("workspace seed patch exceeds the artifact limit")
		}
		if patchBytes, e = w.validateSeed(ctx, r, patch, a.Limits.MaxArtifact, false); e != nil {
			return r, e
		}
	}
	if restoring || (r.SeedPatchID != "" && !r.SeedReady) {
		if e = w.rebuildSeedWorkspace(ctx, r); e != nil {
			return r, e
		}
	}
	if _, e = os.Lstat(filepath.Join(r.WorkDir, ".git")); errors.Is(e, os.ErrNotExist) {
		sourceBytes, er := sourceTreeBytes(ctx, r.Repository, nil, r.Base, w.disk.Policy.MaxManaged, true)
		if er != nil {
			return r, er
		}
		if e = w.checkoutCapacity(a, max(sourceBytes, patchBytes)); e != nil {
			return r, e
		}
		if _, e = git(ctx, r.Repository, nil, nil, 4096, "worktree", "add", "--detach", r.WorkDir, r.Base); e != nil {
			return r, e
		}
	} else if e != nil {
		return r, e
	}
	if e = w.verifyWorkspace(ctx, r); e != nil {
		return r, e
	}
	if r.Candidate != nil && !restoring && (a.Setup.Profile.Harness == "codex" || a.Setup.Profile.Harness == "command") {
		if _, _, e = w.retainedCandidate(ctx, r, true); e != nil {
			return r, e
		}
	}
	if restoring || r.Candidate == nil && r.ReadyGeneration == "" {
		if e = w.settleInitialization(ctx, r, true); e != nil {
			return r, e
		}
	}
	if restoring || (r.SeedPatchID != "" && !r.SeedReady) {
		if len(patch) != 0 {
			if _, e = git(ctx, r.WorkDir, nil, patch, 4096, "apply", "--index", "--binary"); e != nil {
				return r, e
			}
		}
		if restoring {
			r.Released = false
			r.ReadyGeneration = ""
		} else {
			r.SeedReady = true
		}
		if e = w.disk.Refresh(); e != nil {
			return r, e
		}
		if e = w.save(r); e != nil {
			return r, e
		}
	}
	return r, nil
}

func treeEntrySize(meta []byte) (int64, error) {
	fields := strings.Fields(string(meta))
	if len(fields) != 4 {
		return 0, errors.New("invalid source tree entry")
	}
	size, e := strconv.ParseInt(fields[3], 10, 64)
	if e != nil || size < 0 {
		return 0, errors.New("invalid source tree size")
	}
	return size, nil
}

// Admission must cover the checkout before Git allocates it, plus the same
// result/log/metadata and native headroom retained by core attempt admission.
func (w *Workspaces) checkoutCapacity(a runner.Assignment, sourceBytes int64) error {
	if e := w.disk.Refresh(); e != nil {
		return e
	}
	p := w.disk.Policy
	reserve := int64(3*p.Database.MaxRecord+p.MaxArtifact+p.Logs.MaxPerAttempt) + 8192
	if a.Setup.Profile.Harness == "command" {
		reserve += int64(p.Database.MaxRecord) + 16384
	}
	if a.Setup.Profile.Harness == "command" || a.Setup.Profile.Harness == "codex" {
		for _, input := range a.Inputs {
			if input.ArtifactID != "" {
				reserve += int64(p.MaxArtifact)
			}
		}
	}
	reserve += max(0, int64(w.nativeLimit)-w.disk.NativeUsed())
	available := int64(p.MaxManaged-p.Database.MaxSize) - w.disk.Used() - reserve
	if sourceBytes > available {
		return fmt.Errorf("%w: source checkout and recovery reserve", runner.ErrCapacity)
	}
	free, e := runner.FreeBytes(w.disk.Root)
	if e != nil {
		return e
	}
	if sourceBytes > free-int64(p.MinFree)-reserve {
		return fmt.Errorf("%w: source checkout free-space reserve", runner.ErrCapacity)
	}
	return nil
}

func (w *Workspaces) verifyWorkspace(ctx context.Context, r workspaceRecord) error {
	info, e := os.Lstat(r.WorkDir)
	if e != nil {
		return e
	}
	if !info.IsDir() {
		return &runner.CleanupBlockedError{Reason: "owned workspace is not a directory; preserve it"}
	}
	info, e = os.Lstat(filepath.Join(r.WorkDir, ".git"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e != nil || !info.Mode().IsRegular() {
		return &runner.CleanupBlockedError{Reason: "owned workspace Git link is missing or not a regular file; preserve it"}
	}
	head, e := git(ctx, r.WorkDir, nil, nil, 256, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(head)) != r.Base {
		return &runner.CleanupBlockedError{Reason: "owned workspace base differs; preserve and inspect it before recovery"}
	}
	common, e := git(ctx, r.WorkDir, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return e
	}
	expected, e := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if e != nil {
		return e
	}
	if !bytes.Equal(common, expected) {
		return &runner.CleanupBlockedError{Reason: "workspace Git ownership differs from the recorded repository; preserve it"}
	}
	_, e = workspaceAttributes(ctx, r)
	return e
}

// A cancelled Git checkout can leave its own initialization lock behind.
// Before admitting work, require the complete unchanged base. Cleanup already
// proves a failed candidate is retained before removing its owned checkout.
// Other lock reasons always require operator inspection.
func (w *Workspaces) settleInitialization(ctx context.Context, r workspaceRecord, requireClean bool) error {
	path, err := git(ctx, r.WorkDir, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "locked")
	if err != nil {
		return err
	}
	f, err := os.Open(strings.TrimSpace(string(path)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, 64))
	f.Close()
	if err != nil {
		return err
	}
	if string(data) != "initializing\n" {
		return errors.New("owned workspace has an operator lock; preserve and inspect it")
	}
	if requireClean {
		if _, err = git(ctx, r.WorkDir, nil, nil, 4096, "diff", "--quiet", r.Base, "--"); err != nil {
			return errors.New("owned checkout initialization is incomplete; preserve it before admitting work")
		}
	}
	_, err = git(ctx, r.Repository, nil, nil, 4096, "worktree", "unlock", r.WorkDir)
	return err
}

// Only a registered, owned pre-model worktree may be rebuilt after an interrupted
// seed application. Once SeedReady is durable, Prepare never enters this path.
func (w *Workspaces) rebuildSeedWorkspace(ctx context.Context, r workspaceRecord) error {
	list, e := git(ctx, r.Repository, nil, nil, 1<<20, "worktree", "list", "--porcelain", "-z")
	if e != nil {
		return e
	}
	registered := false
	for _, field := range bytes.Split(list, []byte{0}) {
		registered = registered || string(field) == "worktree "+r.WorkDir
	}
	info, e := os.Lstat(r.WorkDir)
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e == nil && (!info.IsDir() || !registered) {
		return errors.New("unrecognized seed preparation directory; preserve and inspect it")
	}
	if !registered {
		return nil
	}
	if info != nil {
		if _, e = os.Lstat(filepath.Join(r.WorkDir, ".git")); errors.Is(e, os.ErrNotExist) {
			// Git cannot remove a half-created checkout whose .git link is
			// missing. The exact repository registration and pre-model record
			// establish ownership; RemoveAll does not follow child symlinks.
			if e = os.RemoveAll(r.WorkDir); e != nil {
				return e
			}
		} else if e != nil {
			return e
		} else if e = w.verifyWorkspace(ctx, r); e != nil {
			return e
		}
		if _, e = os.Lstat(filepath.Join(r.WorkDir, ".git")); e == nil {
			if e = w.settleInitialization(ctx, r, false); e != nil {
				return e
			}
		}
	}
	_, e = git(ctx, r.Repository, nil, nil, 4096, "worktree", "remove", "--force", r.WorkDir)
	return e
}

// Parse the seed in a disposable index/object directory before it may touch the
// owned worktree. Git supplies decoded paths and modes, including both sides of
// renames; no patch headers or filenames are interpreted by a shell.
func (w *Workspaces) validateSeed(ctx context.Context, r workspaceRecord, patch []byte, limit runner.Bytes, verifyCheckout bool) (int64, error) {
	return w.validateSeedWithinTask(ctx, r, patch, limit, verifyCheckout, nil)
}

func (w *Workspaces) validateSeedWithinTask(ctx context.Context, r workspaceRecord, patch []byte, limit runner.Bytes, verifyCheckout bool, task *taskWorkspaceRecord) (int64, error) {
	if err := validateAttributeSources(ctx, r.Repository); err != nil {
		return 0, err
	}
	if _, _, err := rootAttributes(ctx, r.Repository, nil, r.Base); err != nil {
		return 0, err
	}
	root := filepath.Join(w.disk.Root, "work", r.RunID, "seed-validation")
	if e := os.RemoveAll(root); e != nil {
		return 0, e
	}
	defer os.RemoveAll(root)
	if e := os.MkdirAll(filepath.Join(root, "objects"), 0700); e != nil {
		return 0, e
	}
	objects, e := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if e != nil {
		return 0, e
	}
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(root, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(root, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.TrimSpace(string(objects)),
	}
	if verifyCheckout {
		// Copy bounded stat evidence without asking Git to lock the live index.
		// Even read-tree --index-output locks its input index, so every index
		// operation must start with the disposable GIT_INDEX_FILE already set.
		indexPath, er := git(ctx, r.WorkDir, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "index")
		if er != nil {
			return 0, er
		}
		file, er := os.Open(strings.TrimSpace(string(indexPath)))
		if er != nil {
			return 0, er
		}
		info, er := file.Stat()
		if er != nil || !info.Mode().IsRegular() {
			file.Close()
			return 0, errors.New("owned workspace index is not a regular readable file")
		}
		indexLimit := int64(w.disk.Policy.MaxArtifact)
		data, er := io.ReadAll(io.LimitReader(file, indexLimit+1))
		file.Close()
		if er != nil {
			return 0, er
		}
		if int64(len(data)) > indexLimit {
			return 0, runner.ErrCapacity
		}
		if er = os.WriteFile(filepath.Join(root, "index"), data, 0600); er != nil {
			return 0, er
		}
		if _, e = git(ctx, r.WorkDir, env, nil, 4096, "read-tree", "-i", "-m", r.Base); e != nil {
			return 0, e
		}
	} else if _, e = git(ctx, r.Repository, env, nil, 4096, "read-tree", r.Base); e != nil {
		return 0, e
	}
	if len(patch) != 0 {
		if _, e = git(ctx, r.Repository, env, patch, 4096, "apply", "--cached", "--binary"); e != nil {
			return 0, e
		}
	}
	raw, e := git(ctx, r.Repository, env, nil, 1<<20, "diff", "--cached", "--raw", "--no-abbrev", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", r.Base, "--")
	if e != nil {
		return 0, e
	}
	fields := bytes.Split(raw, []byte{0})
	var hashes strings.Builder
	for i := 0; i+1 < len(fields); i += 2 {
		meta := strings.Fields(string(fields[i]))
		name := string(fields[i+1])
		if len(meta) != 5 || !fs.ValidPath(name) || name == "." || (meta[1] != "000000" && meta[1] != "100644" && meta[1] != "100755") {
			return 0, errors.New("workspace seed contains an unsafe path or unsupported file mode")
		}
		for _, part := range strings.Split(name, "/") {
			switch strings.ToLower(part) {
			case ".git", ".codex", ".agents", ".aws", ".runner-tmp", ".gitattributes", ".gitmodules":
				return 0, errors.New("workspace seed changes a protected source path")
			}
		}
		folded := strings.ToLower(name)
		for _, path := range w.readOnlyPaths {
			protected := strings.ToLower(path)
			if folded == protected || strings.HasPrefix(folded, protected+"/") || strings.HasPrefix(protected, folded+"/") {
				return 0, errors.New("workspace seed changes a configured read-only source path")
			}
		}
		if meta[1] != "000000" {
			hashes.WriteString(meta[3] + "\n")
		}
	}
	sizes, e := git(ctx, r.Repository, env, []byte(hashes.String()), 1<<20, "cat-file", "--batch-check=%(objectsize)")
	if e != nil {
		return 0, e
	}
	for _, line := range strings.Fields(string(sizes)) {
		size, e := strconv.ParseInt(line, 10, 64)
		if e != nil || size < 0 || size > int64(limit) {
			return 0, errors.New("workspace seed contains an oversized file")
		}
	}
	tree, e := git(ctx, r.Repository, env, nil, 256, "write-tree")
	if e != nil {
		return 0, e
	}
	if r.Candidate != nil && strings.TrimSpace(string(tree)) != r.Candidate.Tree {
		return 0, &runner.CleanupBlockedError{Reason: "retained patch does not reconstruct the saved candidate; preserve the workspace"}
	}
	if task != nil {
		// Captured trees can live only in this disposable object directory until
		// normal pinning. Scope inspection must use that same authenticated index.
		changed, err := git(ctx, r.Repository, env, nil, 1<<20, "diff-tree", "--no-commit-id", "--name-only", "--no-renames", "-r", "-z", task.SeedTree, strings.TrimSpace(string(tree)), "--")
		if err != nil {
			return 0, err
		}
		for _, name := range bytes.Split(changed, []byte{0}) {
			if len(name) > 0 && !taskPathOwned(string(name), task.Authority.WritePaths) {
				return 0, errors.New("captured card changed source outside its frozen write scope")
			}
		}
	}
	sourceBytes, e := sourceTreeBytes(ctx, r.Repository, env, strings.TrimSpace(string(tree)), w.disk.Policy.MaxManaged, false)
	if e != nil {
		return 0, e
	}
	if verifyCheckout {
		attributes, er := workspaceAttributes(ctx, r)
		if er != nil {
			return 0, er
		}
		if attributes {
			changed, er := literalAttributeChanges(ctx, r, env, strings.TrimSpace(string(tree)))
			if er != nil {
				return 0, er
			}
			if len(changed) != 0 {
				return 0, &runner.CleanupBlockedError{Reason: "workspace literal bytes differ from retained source; preserve it for inspection"}
			}
		}
		if _, e = git(ctx, r.WorkDir, env, nil, 4096, "diff", "--quiet", "--no-ext-diff", "--no-textconv", "--", ".", ":(exclude).runner-tmp"); e != nil {
			var exit *exec.ExitError
			if errors.As(e, &exit) && exit.ExitCode() == 1 {
				return 0, &runner.CleanupBlockedError{Reason: "workspace differs from the retained candidate; preserve it for inspection"}
			}
			return 0, fmt.Errorf("workspace differs from the retained candidate or cannot be verified; preserve it: %w", e)
		}
		// Include ignored files: only the owned .runner-tmp tree is disposable
		// without a source capture. Unrecognized generated files stay recoverable.
		untracked, er := git(ctx, r.WorkDir, env, nil, 1<<20, "ls-files", "--others", "-z", "--", ".", ":(exclude).runner-tmp")
		if er != nil {
			return 0, er
		}
		if len(untracked) != 0 {
			return 0, &runner.CleanupBlockedError{Reason: "workspace has uncaptured files; preserve it"}
		}
	}
	return sourceBytes, nil
}

func (w *Workspaces) retainedCandidate(ctx context.Context, r workspaceRecord, verifyCheckout bool) ([]byte, int64, error) {
	c := r.Candidate
	if c == nil {
		// A read-only first step has no captured candidate. Its unchanged base
		// or ready seed can still reconstruct the source without losing edits.
		var patch []byte
		if r.SeedPatchID != "" {
			if !r.SeedReady {
				return nil, 0, &runner.CleanupBlockedError{Reason: "workspace seed is not recoverable; preserve it"}
			}
			var err error
			patch, err = w.readSeed(ctx, r, w.disk.Policy.MaxArtifact)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, runner.ErrNotFound) {
					return nil, 0, &runner.CleanupBlockedError{Reason: "retained workspace seed is missing; preserve the workspace"}
				}
				return nil, 0, err
			}
			if len(patch) > int(w.disk.Policy.MaxArtifact) {
				return nil, 0, runner.ErrCapacity
			}
		}
		sourceBytes, err := w.validateSeed(ctx, r, patch, w.disk.Policy.MaxArtifact, verifyCheckout)
		return patch, sourceBytes, err
	}
	if c.PatchID == "" || c.Tree == "" || c.AttemptID == "" || c.Repository != r.Repository || c.Base != r.Base || c.WorkDir != r.WorkDir {
		return nil, 0, &runner.CleanupBlockedError{Reason: "failed workspace lacks a reconstructible retained candidate; preserve it"}
	}
	patch, e := w.disk.Read("artifacts/"+c.PatchID, w.disk.Policy.MaxArtifact)
	if e != nil {
		if errors.Is(e, os.ErrNotExist) {
			return nil, 0, &runner.CleanupBlockedError{Reason: "retained candidate patch is missing; preserve the workspace"}
		}
		return nil, 0, e
	}
	if r.Capture != nil && r.Capture.AttemptID == c.AttemptID && r.Capture.PatchSHA != "" && captureDigest(patch) != r.Capture.PatchSHA {
		return nil, 0, &runner.CleanupBlockedError{Reason: "retained candidate patch differs from its committed capture; preserve the workspace"}
	}
	sourceBytes, e := w.validateSeed(ctx, r, patch, w.disk.Policy.MaxArtifact, verifyCheckout)
	return patch, sourceBytes, e
}
func (w *Workspaces) ReleaseRun(ctx context.Context, id string) error {
	return w.releaseRun(ctx, id, false)
}

func (w *Workspaces) releaseRun(ctx context.Context, id string, preserveCandidate bool) error {
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, e := w.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.SourcePreservation != nil && !r.SourcePreservation.Complete {
		return cleanupBlocked("workspace source preservation must settle before cleanup")
	}
	releaseCapture, e := w.cleanupQuiescent(r)
	if e != nil {
		return e
	}
	defer releaseCapture()
	lease, e := w.lockCleanup(r)
	if e != nil {
		return e
	}
	defer lease.Close()
	ctx = context.WithValue(ctx, captureLeaseKey{}, lease)
	if r.CleanupIntent != nil {
		if preserveCandidate && r.CleanupIntent.AllowIgnored {
			return cleanupBlocked("workspace cleanup mode changed after its deletion intent")
		}
		return w.resumeCleanup(ctx, r)
	}
	if r.Released {
		// A restore interrupted before readiness still has Released set. No
		// execution was admitted into that disposable preparation checkout.
		if _, e = os.Lstat(r.WorkDir); e == nil {
			if e = w.rebuildSeedWorkspace(ctx, r); e != nil {
				return e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e = w.disk.Remove("work/" + id); e != nil {
			return e
		}
		return w.disk.Refresh()
	}
	var retainedPatch []byte
	validated := false
	if preserveCandidate {
		if _, e = os.Lstat(r.WorkDir); e == nil {
			if e = w.verifyWorkspace(ctx, r); e != nil {
				return e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		} else if r.Candidate == nil {
			// Capacity/readiness may fail before any checkout or candidate exists.
			// Keep the saved base as fresh preparation for an explicit Retry.
			return w.rebuildSeedWorkspace(ctx, r)
		}
		if retainedPatch, _, e = w.retainedCandidate(ctx, r, e == nil); e != nil {
			return e
		}
		validated = true
	}
	if r.Candidate != nil && r.Candidate.PatchID != "" {
		if _, e = w.disk.ArtifactSize(r.Candidate.PatchID); e != nil {
			return e
		}
	}
	if _, e = os.Lstat(r.WorkDir); e == nil {
		if e = w.verifyWorkspace(ctx, r); e != nil {
			return e
		}
		if e = w.settleInitialization(ctx, r, false); e != nil {
			return e
		}
		if !validated {
			if retainedPatch, _, e = w.retainedCandidate(ctx, r, false); e != nil {
				return e
			}
		}
		r.CleanupIntent, e = w.cleanupIndex(ctx, r, retainedPatch, validated)
		if e != nil {
			return e
		}
		r.CleanupIntent.AllowIgnored = !preserveCandidate
		if !preserveCandidate {
			if e = w.checkCleanupSource(ctx, r, false); e != nil {
				return e
			}
		}
		if e = w.save(r); e != nil {
			return e
		}
		return w.resumeCleanup(ctx, r)
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Git removed content outside Disk.Write. Reconcile the freed bytes before
	// persisting cleanup, including recovery from an already exceeded cap.
	if e = w.disk.Refresh(); e != nil {
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
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, e := w.load(id)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if r.CandidateCommit != "" {
		ref := candidateRef(id)
		worktrees, err := git(ctx, r.Repository, nil, nil, 1<<20, "worktree", "list", "--porcelain", "-z")
		if err != nil {
			return err
		}
		checkedOut := false
		for _, field := range bytes.Split(worktrees, []byte{0}) {
			checkedOut = checkedOut || string(field) == "branch "+ref
		}
		current, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
		if err == nil && !checkedOut && strings.TrimSpace(string(current)) == r.CandidateCommit {
			if _, e = git(ctx, r.Repository, nil, nil, 1024, "update-ref", "-d", ref, r.CandidateCommit); e != nil {
				return e
			}
		} else if err != nil && !missingCandidateRef(ctx, r.Repository, ref) {
			return err
		}
		// A branch advanced or checked out by a user is no longer disposable.
	}
	for attempt, hash := range r.CandidatePins {
		ref := "refs/runner/candidates/" + id + "/" + attempt
		current, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
		if err == nil && strings.TrimSpace(string(current)) == hash {
			if _, e = git(ctx, r.Repository, nil, nil, 1024, "update-ref", "-d", ref, hash); e != nil {
				return e
			}
		} else if err != nil && !missingCandidateRef(ctx, r.Repository, ref) {
			return err
		}
	}
	if _, e = git(ctx, r.Repository, nil, nil, 4096, "update-ref", "-d", pin(id), r.Base); e != nil {
		return e
	}
	return w.disk.Remove(workspaceName(id))
}

func (w *Workspaces) MarkReady(ctx context.Context, id, generation string) error {
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, e := w.load(id)
	if e != nil {
		return e
	}
	r.ReadyGeneration = generation
	return w.save(r)
}
func (w *Workspaces) ReleaseAttempt(ctx context.Context, a runner.Assignment) error {
	if err := w.lockContext(ctx); err != nil {
		return err
	}
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
	if dir, err := stagedInputDirectory(r.WorkDir, a.AttemptID, false); err != nil {
		return err
	} else if dir != "" {
		files, err := stagedInputData(w.disk, a)
		if err != nil {
			return err
		}
		if err = verifyStagedInputs(dir, files); err != nil {
			return err
		}
		if err = os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if e = os.RemoveAll(filepath.Join(parent, a.AttemptID)); e != nil {
		return e
	}
	return w.disk.Refresh()
}
