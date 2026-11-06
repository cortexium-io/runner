package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

type workspaceGitIdentityRecord struct {
	GitDir        string `json:"git_dir"`
	CommonDir     string `json:"common_dir"`
	GitLink       string `json:"git_link"`
	Backlink      string `json:"backlink"`
	Head          string `json:"head"`
	RootIdentity  string `json:"root_identity"`
	AdminIdentity string `json:"admin_identity"`
}

type workspaceCleanupIntent struct {
	Identity     workspaceGitIdentityRecord `json:"identity"`
	PatchSHA     string                     `json:"patch_sha256"`
	Tree         string                     `json:"tree"`
	IndexSHA     string                     `json:"index_sha256"`
	IndexSize    int64                      `json:"index_size"`
	AllowIgnored bool                       `json:"allow_ignored,omitempty"`
	IgnoredSHA   string                     `json:"ignored_sha256,omitempty"`
	IgnoredSize  int64                      `json:"ignored_size,omitempty"`
}

func cleanupBlocked(reason string) error {
	return &runner.CleanupBlockedError{Reason: reason + "; preserve the workspace"}
}

func regularWorkspaceFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, cleanupBlocked("workspace identity file is not regular or is oversized")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, cleanupBlocked("workspace identity file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, cleanupBlocked("workspace identity file changed or exceeds its bound")
	}
	return data, nil
}

func (w *Workspaces) lockCleanup(r workspaceRecord) (*os.File, error) {
	if err := captureDirectory(w.disk.Root, "work", r.RunID, "cleanup"); err != nil {
		return nil, err
	}
	path := filepath.Join(w.disk.Root, "work", r.RunID, "cleanup", "lease")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, cleanupBlocked("workspace cleanup lease is not regular")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	owned, err := tryCaptureLease(lease)
	if err != nil || !owned {
		lease.Close()
		return nil, errors.New("owned workspace cleanup writer is still settling")
	}
	return lease, nil
}

func workspaceGitIdentity(ctx context.Context, r workspaceRecord, path string) (workspaceGitIdentityRecord, error) {
	var identity workspaceGitIdentityRecord
	root, err := os.Lstat(path)
	if err != nil || !root.IsDir() {
		return identity, cleanupBlocked("workspace root is missing or not a regular directory")
	}
	identity.RootIdentity, err = workspaceFileIdentity(root)
	if err != nil {
		return identity, err
	}
	link, err := regularWorkspaceFile(filepath.Join(path, ".git"), 4096)
	if err != nil || !strings.HasPrefix(string(link), "gitdir: ") {
		return identity, cleanupBlocked("workspace Git link is missing or invalid")
	}
	identity.GitLink = string(link)
	admin := strings.TrimSpace(strings.TrimPrefix(string(link), "gitdir: "))
	if !filepath.IsAbs(admin) {
		admin = filepath.Join(path, admin)
	}
	identity.GitDir, err = filepath.EvalSymlinks(admin)
	if err != nil || identity.GitDir != filepath.Clean(admin) {
		return identity, cleanupBlocked("workspace Git admin path changed or has symlinks")
	}
	common, err := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return identity, err
	}
	identity.CommonDir, err = filepath.EvalSymlinks(strings.TrimSpace(string(common)))
	if err != nil || filepath.Dir(identity.GitDir) != filepath.Join(identity.CommonDir, "worktrees") {
		return identity, cleanupBlocked("workspace admin is outside the recorded repository")
	}
	adminInfo, err := os.Lstat(identity.GitDir)
	if err != nil || !adminInfo.IsDir() {
		return identity, cleanupBlocked("workspace Git admin is not a regular directory")
	}
	identity.AdminIdentity, err = workspaceFileIdentity(adminInfo)
	if err != nil {
		return identity, err
	}
	backlink, err := regularWorkspaceFile(filepath.Join(identity.GitDir, "gitdir"), 4096)
	if err != nil {
		return identity, err
	}
	identity.Backlink = strings.TrimSpace(string(backlink))
	head, err := regularWorkspaceFile(filepath.Join(identity.GitDir, "HEAD"), 256)
	if err != nil || strings.TrimSpace(string(head)) != r.Base {
		return identity, cleanupBlocked("workspace Git admin base differs")
	}
	identity.Head = strings.TrimSpace(string(head))
	commonLink, err := regularWorkspaceFile(filepath.Join(identity.GitDir, "commondir"), 4096)
	if err != nil {
		return identity, err
	}
	actualCommon := strings.TrimSpace(string(commonLink))
	if !filepath.IsAbs(actualCommon) {
		actualCommon = filepath.Join(identity.GitDir, actualCommon)
	}
	actualCommon, err = filepath.EvalSymlinks(actualCommon)
	if err != nil || actualCommon != identity.CommonDir {
		return identity, cleanupBlocked("workspace Git common directory differs")
	}
	return identity, nil
}

func (w *Workspaces) cleanupQuiescent(r workspaceRecord) (func(), error) {
	entries, err := os.ReadDir(filepath.Join(w.disk.Root, "jobs"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".codex.json") && !strings.HasSuffix(name, ".command.json") && !strings.HasSuffix(name, ".candidate.json") && !strings.HasSuffix(name, ".release.json") {
			continue
		}
		data, err := w.disk.Read("jobs/"+name, w.disk.Policy.Database.MaxRecord)
		var job struct {
			Assignment runner.Assignment    `json:"assignment"`
			End        *runner.ExecutionEnd `json:"end"`
		}
		if err != nil || json.Unmarshal(data, &job) != nil {
			return nil, cleanupBlocked("workspace execution settlement cannot be established")
		}
		if job.Assignment.RunID == r.RunID && job.End == nil {
			return nil, cleanupBlocked("workspace still has an unsettled execution")
		}
	}
	if r.Capture == nil {
		return func() {}, nil
	}
	if r.Candidate == nil || r.Capture.AttemptID != r.Candidate.AttemptID || r.Capture.PatchSHA == "" {
		return nil, cleanupBlocked("workspace source capture is uncommitted")
	}
	path := filepath.Join(w.disk.Root, "work", r.Capture.AttemptID, "capture-v1", "lease")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return func() {}, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil, cleanupBlocked("workspace capture lease ownership changed")
	}
	lease, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	owned, err := tryCaptureLease(lease)
	if err != nil || !owned {
		lease.Close()
		return nil, cleanupBlocked("workspace capture writer is not settled")
	}
	return func() { _ = lease.Close() }, nil
}

func (w *Workspaces) cleanupIndex(ctx context.Context, r workspaceRecord, patch []byte, verifiedSource ...bool) (*workspaceCleanupIntent, error) {
	// Existing strict callers already proved full source equivalence. Ordinary
	// completed cleanup defers its tracked-only proof until this index is built.
	verified := len(verifiedSource) == 0 || verifiedSource[0]
	identity, err := workspaceGitIdentity(ctx, r, r.WorkDir)
	if err != nil || identity.Backlink != filepath.Join(r.WorkDir, ".git") {
		return nil, cleanupBlocked("workspace cleanup Git ownership differs")
	}
	// Strict callers already proved source equivalence and quiescence. Only
	// they free excluded private scratch before allocating the deletion intent,
	// preserving cleanup's above-cap recovery without removing source.
	scratch := filepath.Join(r.WorkDir, ".runner-tmp")
	if info, err := os.Lstat(scratch); verified && err == nil {
		if !info.IsDir() {
			return nil, cleanupBlocked("workspace private scratch ownership changed")
		}
		if err := w.disk.Remove("work/" + r.RunID + "/source/.runner-tmp"); err != nil {
			return nil, err
		}
	} else if verified && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = captureDirectory(w.disk.Root, "work", r.RunID, "cleanup", "objects"); err != nil {
		return nil, err
	}
	rel := "work/" + r.RunID + "/cleanup/index"
	index, err := regularWorkspaceFile(filepath.Join(identity.GitDir, "index"), int64(w.disk.Policy.MaxArtifact))
	if err != nil {
		return nil, err
	}
	if err = w.disk.Write(rel, index, w.disk.Policy.MaxArtifact); err != nil {
		return nil, err
	}
	env := w.cleanupEnvironment(r, identity)
	if _, err = git(ctx, r.WorkDir, env, nil, 4096, "read-tree", "-i", "-m", r.Base); err != nil {
		return nil, err
	}
	if len(patch) != 0 {
		if _, err = git(ctx, r.WorkDir, env, patch, 4096, "apply", "--cached", "--binary"); err != nil {
			return nil, err
		}
	}
	tree, err := git(ctx, r.WorkDir, env, nil, 256, "write-tree")
	if err != nil {
		return nil, err
	}
	index, err = regularWorkspaceFile(filepath.Join(w.disk.Root, filepath.FromSlash(rel)), int64(w.disk.Policy.MaxArtifact))
	if err != nil {
		return nil, err
	}
	return &workspaceCleanupIntent{Identity: identity, PatchSHA: captureDigest(patch), Tree: strings.TrimSpace(string(tree)), IndexSHA: captureDigest(index), IndexSize: int64(len(index))}, w.disk.Refresh()
}

func (w *Workspaces) cleanupEnvironment(r workspaceRecord, identity workspaceGitIdentityRecord) []string {
	root := filepath.Join(w.disk.Root, "work", r.RunID, "cleanup")
	return []string{
		"GIT_INDEX_FILE=" + filepath.Join(root, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(root, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(identity.CommonDir, "objects"),
		"GIT_OPTIONAL_LOCKS=0",
	}
}

func (w *Workspaces) resumeCleanup(ctx context.Context, r workspaceRecord) error {
	intent := r.CleanupIntent
	if intent == nil {
		return cleanupBlocked("workspace cleanup intent is missing")
	}
	var retained []byte
	var err error
	if r.Candidate != nil {
		retained, err = w.disk.Read("artifacts/"+r.Candidate.PatchID, w.disk.Policy.MaxArtifact)
	} else if r.SeedPatchID != "" && w.read != nil {
		retained, err = w.read(ctx, r.SeedPatchID)
	}
	if err != nil {
		return err
	}
	if captureDigest(retained) != intent.PatchSHA {
		return cleanupBlocked("workspace cleanup retained patch changed")
	}
	patch, _, err := w.retainedCandidate(ctx, r, false)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(filepath.Join(w.disk.Root, "work", r.RunID)); err == nil {
		for _, entry := range entries {
			if entry.Name() != "source" && entry.Name() != "cleanup" {
				return cleanupBlocked("workspace cleanup has unexpected run-directory content")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if intent == nil || intent.PatchSHA != captureDigest(patch) || intent.Identity.Backlink != filepath.Join(r.WorkDir, ".git") || intent.Identity.Head != r.Base || intent.IndexSize < 1 || intent.IndexSize > int64(w.disk.Policy.MaxArtifact) || intent.Tree == "" || r.Candidate != nil && intent.Tree != r.Candidate.Tree || r.Capture != nil && r.Capture.PatchSHA != intent.PatchSHA {
		return cleanupBlocked("workspace cleanup candidate identity differs")
	}
	common, err := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	canonicalCommon, err := filepath.EvalSymlinks(strings.TrimSpace(string(common)))
	if err != nil || canonicalCommon != intent.Identity.CommonDir || filepath.Dir(intent.Identity.GitDir) != filepath.Join(canonicalCommon, "worktrees") {
		return cleanupBlocked("workspace cleanup repository identity differs")
	}
	base, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", pin(r.RunID))
	if err != nil || strings.TrimSpace(string(base)) != r.Base {
		return cleanupBlocked("workspace cleanup base ownership changed")
	}
	if _, err = os.Lstat(r.WorkDir); err == nil {
		identity, err := workspaceGitIdentity(ctx, r, r.WorkDir)
		if err != nil || !reflect.DeepEqual(identity, intent.Identity) {
			return cleanupBlocked("workspace cleanup filesystem or Git ownership changed")
		}
		if err := w.checkCleanupSource(ctx, r, true); err != nil {
			return err
		}
		if _, err = git(ctx, r.Repository, nil, nil, 4096, "worktree", "remove", "--force", r.WorkDir); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if info, adminErr := os.Lstat(intent.Identity.GitDir); adminErr == nil {
		id, identityErr := workspaceFileIdentity(info)
		backlink, linkErr := regularWorkspaceFile(filepath.Join(intent.Identity.GitDir, "gitdir"), 4096)
		head, headErr := regularWorkspaceFile(filepath.Join(intent.Identity.GitDir, "HEAD"), 256)
		commonLink, commonErr := regularWorkspaceFile(filepath.Join(intent.Identity.GitDir, "commondir"), 4096)
		actualCommon := strings.TrimSpace(string(commonLink))
		if !filepath.IsAbs(actualCommon) {
			actualCommon = filepath.Join(intent.Identity.GitDir, actualCommon)
		}
		actualCommon, resolveErr := filepath.EvalSymlinks(actualCommon)
		if !info.IsDir() || identityErr != nil || id != intent.Identity.AdminIdentity || linkErr != nil || headErr != nil || commonErr != nil || resolveErr != nil || actualCommon != intent.Identity.CommonDir || strings.TrimSpace(string(head)) != intent.Identity.Head || strings.TrimSpace(string(backlink)) != filepath.Join(r.WorkDir, ".git") {
			return cleanupBlocked("absent workspace cleanup admin identity differs")
		}
		if _, err = git(ctx, r.Repository, nil, nil, 4096, "worktree", "remove", "--force", r.WorkDir); err != nil {
			return err
		}
	} else if !errors.Is(adminErr, os.ErrNotExist) {
		return adminErr
	}
	if _, err = os.Lstat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
		return cleanupBlocked("workspace cleanup did not remove the owned checkout")
	}
	if _, err = os.Lstat(intent.Identity.GitDir); !errors.Is(err, os.ErrNotExist) {
		return cleanupBlocked("workspace cleanup did not settle its exact Git admin")
	}
	registrations, err := git(ctx, r.Repository, nil, nil, 1<<20, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return err
	}
	for _, field := range bytes.Split(registrations, []byte{0}) {
		if string(field) == "worktree "+r.WorkDir {
			return cleanupBlocked("workspace path has an unexpected Git registration")
		}
	}
	if err = w.disk.Refresh(); err != nil {
		return err
	}
	// Remove only this transaction's private cache, then require an empty run
	// directory. Never recursively erase unexpected siblings of the checkout.
	if err = w.disk.Remove("work/" + r.RunID + "/cleanup"); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(w.disk.Root, "work", r.RunID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = w.disk.Refresh(); err != nil {
		return err
	}
	r.Released, r.CleanupIntent = true, nil
	if err = w.save(r); err != nil {
		return err
	}
	return nil
}

func (w *Workspaces) checkCleanupSource(ctx context.Context, r workspaceRecord, allowDeleted bool) error {
	intent := r.CleanupIntent
	index, err := w.disk.Read("work/"+r.RunID+"/cleanup/index", runner.Bytes(intent.IndexSize))
	if err != nil || int64(len(index)) != intent.IndexSize || captureDigest(index) != intent.IndexSHA {
		return cleanupBlocked("workspace cleanup expected index changed")
	}
	// Git may refresh cache extensions: validate through a disposable copy.
	root := filepath.Join(w.disk.Root, "work", r.RunID, "cleanup")
	if err = resetCaptureIndex(root, "check-index"); err != nil {
		return err
	}
	if err = w.disk.Write("work/"+r.RunID+"/cleanup/check-index", index, runner.Bytes(intent.IndexSize)); err != nil {
		return err
	}
	env := append(w.cleanupEnvironment(r, intent.Identity), "GIT_INDEX_FILE="+filepath.Join(root, "check-index"))
	if err = cleanupDeletions(ctx, r.WorkDir, env, allowDeleted); err != nil {
		return err
	}
	var ignored map[string]bool
	if intent.AllowIgnored {
		var data []byte
		if !allowDeleted {
			// Ordinary completed cleanup may discard only cache paths that the
			// unchanged candidate actually ignored before deletion started.
			data, err = git(ctx, r.WorkDir, env, nil, int(w.disk.Policy.MaxArtifact), "ls-files", "--others", "--ignored", "--exclude-standard", "-z", "--", ".", ":(exclude).runner-tmp")
			if err != nil {
				if errors.Is(err, runner.ErrCapacity) {
					return cleanupBlocked("workspace ignored-cache path authority exceeds its bound")
				}
				return err
			}
			intent.IgnoredSHA, intent.IgnoredSize = captureDigest(data), int64(len(data))
			if err = w.disk.Write("work/"+r.RunID+"/cleanup/ignored", data, w.disk.Policy.MaxArtifact); err != nil {
				return err
			}
		} else {
			if intent.IgnoredSize < 0 || intent.IgnoredSize > int64(w.disk.Policy.MaxArtifact) {
				return cleanupBlocked("workspace cleanup ignored-path bound changed")
			}
			data, err = w.disk.Read("work/"+r.RunID+"/cleanup/ignored", runner.Bytes(intent.IgnoredSize))
			if err != nil || int64(len(data)) != intent.IgnoredSize || captureDigest(data) != intent.IgnoredSHA {
				return cleanupBlocked("workspace cleanup ignored-path authority changed")
			}
		}
		ignored, err = cleanupPathSet(data)
		if err != nil {
			return err
		}
	}
	extra, err := git(ctx, r.WorkDir, env, nil, max(1<<20, int(intent.IgnoredSize)), "ls-files", "--others", "-z", "--", ".", ":(exclude).runner-tmp")
	if err != nil {
		if errors.Is(err, runner.ErrCapacity) {
			return cleanupBlocked("workspace untracked source exceeds its verified path authority")
		}
		return err
	}
	paths, err := cleanupPathSet(extra)
	if err != nil {
		return err
	}
	for path := range paths {
		if !ignored[path] {
			return cleanupBlocked("workspace cleanup found unexpected source")
		}
	}
	return nil
}

func cleanupPathSet(data []byte) (map[string]bool, error) {
	paths := map[string]bool{}
	if len(data) == 0 {
		return paths, nil
	}
	fields := bytes.Split(data, []byte{0})
	if len(fields[len(fields)-1]) != 0 {
		return nil, cleanupBlocked("workspace cleanup path authority is incomplete")
	}
	for _, field := range fields[:len(fields)-1] {
		name := string(field)
		if !fs.ValidPath(name) || name == "." || paths[name] {
			return nil, cleanupBlocked("workspace cleanup path authority is malformed")
		}
		paths[name] = true
	}
	return paths, nil
}

func cleanupDeletions(ctx context.Context, dir string, env []string, allowDeleted bool) error {
	active, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := gitCommand(active, dir, env, nil, "diff", "--name-status", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", "--", ".", ":(exclude).runner-tmp")
	diagnostic := &diagnosticTail{limit: 1024}
	cmd.Stderr = diagnostic
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	defer pipe.Close()
	if err := cmd.Start(); err != nil {
		return err
	}
	validation := scanCleanupDeletions(pipe, allowDeleted)
	if validation != nil {
		cancel()
		_ = pipe.Close()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if validation != nil {
		return validation
	}
	if err != nil {
		return fmt.Errorf("git cleanup deletion proof: %w: %s", err, runner.Clip(string(diagnostic.bytes())))
	}
	return nil
}

func scanCleanupDeletions(input io.Reader, allowDeleted bool) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), (1<<20)+1)
	scanner.Split(func(data []byte, eof bool) (int, []byte, error) {
		if end := bytes.IndexByte(data, 0); end >= 0 {
			return end + 1, data[:end], nil
		}
		if eof && len(data) != 0 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	})
	status := true
	for scanner.Scan() {
		record := scanner.Text()
		if len(record) > 1<<20 || status && (!allowDeleted || record != "D") || !status && (!fs.ValidPath(record) || record == ".") {
			return cleanupBlocked("workspace cleanup found modified surviving source or an invalid deletion record")
		}
		status = !status
	}
	if err := scanner.Err(); err != nil {
		return cleanupBlocked("workspace cleanup deletion record is incomplete or oversized")
	}
	if !status {
		return cleanupBlocked("workspace cleanup deletion proof has no path")
	}
	return nil
}
