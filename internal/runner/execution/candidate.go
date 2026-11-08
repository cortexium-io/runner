package execution

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

// Candidates retains an accepted patch as a local commit/ref. It never checks
// out a branch, changes the user's index, merges, pushes or runs project hooks.
// The normal core owns dispatch, retry, persistence and cleanup policy.
type Candidates struct {
	disk       *runner.Disk
	workspaces *Workspaces
	mu         sync.Mutex
	workers    map[string]*candidateWork
	wake       func(string)
	closed     bool
}

type candidateJob struct {
	Assignment runner.Assignment    `json:"assignment"`
	End        *runner.ExecutionEnd `json:"end,omitempty"`
	Stopped    bool                 `json:"stopped,omitempty"`
	Source     *runner.SourceResult `json:"source,omitempty"`
	Capture    *captureReference    `json:"capture,omitempty"`
}

func candidateName(id string) string { return "jobs/" + id + ".candidate.json" }
func candidateRef(id string) string  { return "refs/heads/codex/runner/" + id }

func missingCandidateRef(ctx context.Context, repo, ref string) bool {
	_, err := git(ctx, repo, nil, nil, 256, "show-ref", "--verify", "--quiet", ref)
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

func (c *Candidates) Owns(id string) bool {
	_, err := os.Lstat(filepath.Join(c.disk.Root, candidateName(id)))
	return !errors.Is(err, os.ErrNotExist)
}

func (c *Candidates) load(id string) (candidateJob, error) {
	var job candidateJob
	data, err := c.disk.Read(candidateName(id), c.disk.Policy.Database.MaxRecord)
	if err == nil {
		err = json.Unmarshal(data, &job)
	}
	if err == nil && job.Assignment.AttemptID != id {
		err = errors.New("candidate identity differs from saved attempt")
	}
	return job, err
}

func (c *Candidates) save(job candidateJob) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked(job)
}
func (c *Candidates) saveLocked(job candidateJob) error {
	if old, err := c.load(job.Assignment.AttemptID); err == nil {
		job.Stopped = job.Stopped || old.Stopped
		if job.Source == nil {
			job.Source, job.Capture = old.Source, old.Capture
		}
	}
	if job.Stopped && job.End != nil && job.End.Kind == "returned" {
		var text string
		_ = json.Unmarshal(job.End.Result.Outputs["candidate"].Inline, &text)
		job.End = &runner.ExecutionEnd{Kind: "stopped", Reason: runner.Clip("candidate integration settled after cancellation; retained " + text)}
	}
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return c.disk.Write(candidateName(job.Assignment.AttemptID), data, c.disk.Policy.Database.MaxRecord)
}

func (c *Candidates) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if old, err := c.load(a.AttemptID); errors.Is(err, os.ErrNotExist) {
		if err = c.save(candidateJob{Assignment: runner.Clone(a)}); err != nil {
			return runner.Observation{}, err
		}
	} else if err != nil {
		return runner.Observation{}, err
	} else {
		before, _ := json.Marshal(old.Assignment)
		after, _ := json.Marshal(a)
		if !bytes.Equal(before, after) {
			return runner.Observation{}, errors.New("candidate attempt arguments changed")
		}
	}
	return c.Observe(ctx, a.AttemptID)
}

func (c *Candidates) Observe(ctx context.Context, id string) (runner.Observation, error) {
	job, err := c.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if job.End == nil {
		return c.joinCandidate(ctx, job)
	}
	return candidateObservation(job), nil
}
func candidateObservation(job candidateJob) runner.Observation {
	zero := int64(0)
	return runner.Observation{
		State:  runner.Finished,
		End:    job.End,
		Source: runner.Clone(job.Source),
		Usage:  runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
	}
}

func (c *Candidates) Stop(ctx context.Context, id string) error {
	c.mu.Lock()
	job, err := c.load(id)
	if err != nil || job.End != nil {
		c.mu.Unlock()
		return err
	}
	job.Stopped = true
	err = c.saveLocked(job)
	work := c.workers[id]
	if work != nil {
		work.cancel()
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if work != nil {
		select {
		case <-work.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_, err = c.Observe(ctx, id)
	return err
}

func (c *Candidates) Release(ctx context.Context, id string) error {
	c.mu.Lock()
	if work := c.workers[id]; work != nil {
		select {
		case <-work.done:
			delete(c.workers, id)
		default:
			c.mu.Unlock()
			return errors.New("cannot release active candidate integration")
		}
	}
	c.mu.Unlock()
	job, err := c.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.End == nil {
		return errors.New("cannot release unsettled candidate integration")
	}
	lease, err := c.candidateLease(job)
	if err != nil {
		return err
	}
	defer lease.Close()
	if err = c.releaseCandidateScratch(ctx, job); err != nil {
		return err
	}
	if err = c.disk.Remove("jobs/" + id + ".candidate.lock"); err != nil {
		return err
	}
	return c.disk.Remove(candidateName(id))
}

func (w *Workspaces) retainCandidate(ctx context.Context, a runner.Assignment) (string, error) {
	if err := w.lockContext(ctx); err != nil {
		return "", err
	}
	defer w.mu.Unlock()
	if err := candidateApproval(a); err != nil {
		return "", err
	}
	r, err := w.load(a.RunID)
	if err != nil {
		return "", err
	}
	if r.Candidate == nil {
		if err = w.captureApprovedSeed(ctx, r, a); err != nil {
			return "", err
		}
		r, err = w.load(a.RunID)
		if err != nil {
			return "", err
		}
	}
	patch, err := w.candidatePatch(ctx, r, a)
	if err != nil {
		return "", err
	}
	if r.CandidatePins == nil {
		r.CandidatePins = map[string]string{}
	}
	hash := r.CandidatePins[a.AttemptID]
	if hash == "" {
		hash, err = w.buildCandidate(ctx, r, a, patch)
		if err != nil {
			return "", err
		}
		r.CandidatePins[a.AttemptID] = hash
		if err = w.save(r); err != nil {
			return "", err
		}
	} else if err = validateCandidateObject(ctx, r, hash); err != nil {
		return "", err
	}
	ref := candidateRef(a.RunID)
	pinRef := "refs/runner/candidates/" + a.RunID + "/" + a.AttemptID
	pinned, pinErr := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", pinRef)
	if pinErr == nil {
		if strings.TrimSpace(string(pinned)) != hash {
			return "", errors.New("candidate history pin changed; preserve it")
		}
	} else if !missingCandidateRef(ctx, r.Repository, pinRef) {
		return "", pinErr
	} else if _, err = git(ctx, r.Repository, nil, nil, 1024, "update-ref", pinRef, hash, strings.Repeat("0", len(hash))); err != nil {
		return "", err
	}
	old, readErr := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
	if readErr == nil {
		if strings.TrimSpace(string(old)) != hash {
			if strings.TrimSpace(string(old)) != r.CandidateCommit {
				return "", errors.New("candidate branch already contains different work; preserve it and ask the user")
			}
			worktrees, err := git(ctx, r.Repository, nil, nil, 16<<10, "worktree", "list", "--porcelain")
			if err != nil {
				return "", err
			}
			if strings.Contains(string(worktrees), "\nbranch "+ref+"\n") {
				return "", errors.New("candidate branch is checked out by a user; preserve it")
			}
			if _, err = git(ctx, r.Repository, nil, nil, 1024, "update-ref", ref, hash, r.CandidateCommit); err != nil {
				return "", err
			}
		}
	} else {
		if !missingCandidateRef(ctx, r.Repository, ref) {
			return "", readErr
		}
		if r.CandidateCommit != "" {
			return "", errors.New("owned candidate branch disappeared; preserve history and ask the user")
		}
		if _, err = git(ctx, r.Repository, nil, nil, 1024, "update-ref", ref, hash, strings.Repeat("0", len(hash))); err != nil {
			return "", err
		}
	}
	r.CandidateCommit = hash
	if err = w.save(r); err != nil {
		return "", err
	}
	return ref + "\n" + hash + "\nLocal reviewed candidate; not merged, pushed or deployed.", nil
}

// An independently reviewed, already prepared seed needs no coding turn. Capture
// it only after approval, without releasing the source lock between validation
// and capture. A baseline or arbitrary late workspace edit is never a seed.
func (w *Workspaces) captureApprovedSeed(ctx context.Context, r workspaceRecord, a runner.Assignment) error {
	var review string
	repository, err := filepath.EvalSymlinks(a.Repository)
	if err != nil {
		return err
	}
	if a.Deadline.IsZero() || !a.Deadline.After(time.Now()) || a.Source == nil || a.Source.Ref != r.Base || repository != r.Repository || a.TaskSource != nil ||
		r.Released || !r.SeedReady || r.SeedPatchID == "" || r.SeedPatchID != a.Inputs["patch"].ArtifactID || r.ReadyGeneration == "" ||
		r.CleanupIntent != nil || r.SourcePreservation != nil && !r.SourcePreservation.Complete ||
		r.Capture != nil && r.Capture.AttemptID != a.AttemptID ||
		json.Unmarshal(a.Inputs["review"].Inline, &review) != nil || strings.TrimSpace(review) == "" {
		return errors.New("integration requires the exact owned ready seed and independent review")
	}
	if err = w.verifyWorkspace(ctx, r); err != nil {
		return err
	}
	patch, _, err := w.retainedCandidate(ctx, r, true)
	if err != nil {
		return err
	}
	if len(patch) == 0 {
		return errors.New("integration seed cannot be an unchanged baseline")
	}
	if _, err = w.captureLocked(ctx, a); err != nil {
		return err
	}
	current, err := w.load(a.RunID)
	if err != nil {
		return err
	}
	captured, err := w.candidatePatch(ctx, current, a)
	if err != nil || !bytes.Equal(captured, patch) {
		if err != nil {
			return err
		}
		return errors.New("captured source differs from the independently reviewed seed")
	}
	// Reconstruct the supplied seed independently against its original base and
	// compare the resulting tree and literal checkout with the committed capture.
	_, err = w.validateSeed(ctx, current, patch, a.Limits.MaxArtifact, true)
	return err
}

func (w *Workspaces) candidatePatch(ctx context.Context, r workspaceRecord, a runner.Assignment) ([]byte, error) {
	if r.Candidate == nil {
		return nil, errors.New("integration has no retained run candidate")
	}
	patch, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		return nil, err
	}
	if r.Capture == nil || r.Capture.AttemptID != r.Candidate.AttemptID || r.Capture.PatchSHA == "" || captureDigest(patch) != r.Capture.PatchSHA {
		return nil, errors.New("reviewed candidate patch differs from its committed capture")
	}
	if r.Candidate.PatchID == a.Inputs["patch"].ArtifactID {
		return patch, nil // Ordinary captured candidates keep their strict ID rule.
	}
	if !r.SeedReady || r.SeedPatchID != a.Inputs["patch"].ArtifactID || r.Candidate.InputPatchID != r.SeedPatchID ||
		r.Candidate.AttemptID != a.AttemptID || r.Candidate.PatchID != a.AttemptID+"/changes.patch" || r.Candidate.Tree != r.Capture.Tree ||
		a.Source == nil || a.Source.Ref != r.Base || r.Candidate.Repository != r.Repository || r.Candidate.Base != r.Base || r.Candidate.WorkDir != r.WorkDir {
		return nil, errors.New("integration patch differs from the retained run candidate")
	}
	repository, err := filepath.EvalSymlinks(a.Repository)
	if err != nil || repository != r.Repository {
		return nil, errors.New("integration seed repository differs")
	}
	seed, err := w.readSeed(ctx, r, a.Limits.MaxArtifact)
	if err != nil {
		return nil, err
	}
	if len(seed) == 0 || !bytes.Equal(seed, patch) {
		return nil, errors.New("captured integration patch differs from the reviewed seed")
	}
	return patch, nil
}

// Return a real capture made by this integration attempt, including on a failed
// or stopped result. Core registers its artifact before releasing any scratch;
// this is source provenance, not an additional approval.
func (w *Workspaces) candidateSource(ctx context.Context, a runner.Assignment) (*runner.SourceResult, *captureReference, error) {
	if _, err := os.Lstat(filepath.Join(w.disk.Root, "work", a.AttemptID, "capture-v1")); errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	if err := w.lockContext(ctx); err != nil {
		return nil, nil, err
	}
	defer w.mu.Unlock()
	r, err := w.load(a.RunID)
	if err != nil {
		return nil, nil, err
	}
	s := r.Candidate
	if s == nil || s.AttemptID != a.AttemptID || s.PatchID == a.Inputs["patch"].ArtifactID {
		return nil, nil, nil
	}
	if s.PatchID != a.AttemptID+"/changes.patch" || s.InputPatchID != r.SeedPatchID || s.InputPatchID != a.Inputs["patch"].ArtifactID ||
		s.Repository != r.Repository || s.Base != r.Base || s.WorkDir != r.WorkDir || r.Capture == nil || r.Capture.AttemptID != a.AttemptID || r.Capture.Tree != s.Tree || r.Capture.PatchSHA == "" {
		return nil, nil, errors.New("integration capture provenance differs")
	}
	patch, err := w.disk.Read("artifacts/"+s.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		return nil, nil, err
	}
	if captureDigest(patch) != r.Capture.PatchSHA {
		return nil, nil, errors.New("integration capture patch changed before result collection")
	}
	if _, err = w.readCaptureManifest(r, a, "work/"+a.AttemptID+"/capture-v1"); err != nil {
		return nil, nil, err
	}
	return runner.Clone(s), runner.Clone(r.Capture), nil
}

func (c *Candidates) saveCandidateResult(ctx context.Context, j candidateJob) error {
	source, capture, err := c.workspaces.candidateSource(ctx, j.Assignment)
	if err != nil {
		return err
	}
	j.Source, j.Capture = source, capture
	return c.save(j)
}

func candidateApproval(a runner.Assignment) error {
	var verdict string
	if a.Setup.Profile.Harness != "git-candidate" || a.Setup.Profile.Access != "workspace-read" ||
		json.Unmarshal(a.Inputs["verdict"].Inline, &verdict) != nil || verdict != "APPROVE" {
		var review string
		_ = json.Unmarshal(a.Inputs["review"].Inline, &review)
		return errors.New("candidate integration requires an explicit APPROVE verdict; user decision needed: " + runner.Clip(review))
	}
	return nil
}
func validateCandidateObject(ctx context.Context, r workspaceRecord, hash string) error {
	tree, err := taskTree(ctx, r.Repository, hash)
	if err != nil {
		return err
	}
	if r.Candidate == nil || tree != r.Candidate.Tree {
		return errors.New("saved candidate commit tree differs")
	}
	parent, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", hash+"^")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(parent)) != r.Base {
		return errors.New("saved candidate commit base differs")
	}
	return nil
}
func (w *Workspaces) buildCandidate(ctx context.Context, r workspaceRecord, a runner.Assignment, patch []byte) (string, error) {
	scratch := filepath.Join(w.disk.Root, "work", a.AttemptID, "candidate")
	if err := captureDirectory(w.disk.Root, "work", a.AttemptID, "candidate"); err != nil {
		return "", err
	}
	if err := candidateScratchFiles(scratch); err != nil {
		return "", err
	}
	if err := removeTaskIndex(filepath.Join(scratch, "index")); err != nil {
		return "", err
	}
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(scratch, "index"),
		"GIT_AUTHOR_NAME=Cortexium Runner",
		"GIT_AUTHOR_EMAIL=runner@localhost",
		"GIT_COMMITTER_NAME=Cortexium Runner",
		"GIT_COMMITTER_EMAIL=runner@localhost",
		"GIT_AUTHOR_DATE=" + a.Deadline.UTC().Format("2006-01-02T15:04:05Z"),
		"GIT_COMMITTER_DATE=" + a.Deadline.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if _, err := git(ctx, r.Repository, env, nil, 1024, "read-tree", r.Base); err != nil {
		return "", err
	}
	if len(patch) != 0 {
		if _, err := git(ctx, r.Repository, env, patch, 1024, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
			return "", err
		}
	}
	tree, err := git(ctx, r.Repository, env, nil, 256, "write-tree")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(tree)) != r.Candidate.Tree {
		return "", errors.New("retained patch does not reconstruct the reviewed candidate")
	}
	commit, err := git(ctx, r.Repository, env, []byte("Runner reviewed candidate "+a.RunID+"\n"), 256, "-c", "commit.gpgSign=false", "commit-tree", r.Candidate.Tree, "-p", r.Base)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(commit)), nil
}

// Other source collectors may hold the workspace mutex. Acquiring it must not
// turn a cancelled observer/attempt into an uncancellable blocked goroutine.
func (w *Workspaces) lockContext(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if w.mu.TryLock() {
			if err := ctx.Err(); err != nil {
				w.mu.Unlock()
				return err
			}
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func candidateScratchFiles(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if (entry.Name() != "index" && entry.Name() != "index.lock") || !entry.Type().IsRegular() {
			return errors.New("candidate scratch ownership changed; preserve it")
		}
	}
	return nil
}
func (c *Candidates) releaseScratch(job candidateJob) error {
	root := filepath.Join(c.disk.Root, "work", job.Assignment.AttemptID)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("candidate attempt scratch identity changed; preserve it")
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "candidate" || !entry.IsDir() {
			return errors.New("candidate attempt scratch has unknown content; preserve it")
		}
	}
	dir := filepath.Join(root, "candidate")
	if len(entries) > 0 {
		if err = candidateScratchFiles(dir); err != nil {
			return err
		}
		if err = removeTaskIndex(filepath.Join(dir, "index")); err != nil {
			return err
		}
		if err = os.Remove(dir); err != nil {
			return err
		}
	}
	if err = os.Remove(root); err != nil {
		return err
	}
	return c.disk.Refresh()
}

func (c *Candidates) releaseCandidateScratch(ctx context.Context, job candidateJob) error {
	if job.Source == nil || job.Capture == nil {
		return c.releaseScratch(job)
	}
	a := job.Assignment
	if job.Source.AttemptID != a.AttemptID || job.Source.PatchID != a.AttemptID+"/changes.patch" || job.Capture.AttemptID != a.AttemptID || job.Source.Tree != job.Capture.Tree {
		return errors.New("candidate capture cleanup provenance differs; preserve it")
	}
	root := filepath.Join(c.disk.Root, "work", a.AttemptID)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New("candidate capture scratch ownership changed; preserve it")
	}
	if err := c.workspaces.lockContext(ctx); err != nil {
		return err
	}
	defer c.workspaces.mu.Unlock()
	r, err := c.workspaces.load(a.RunID)
	if err != nil {
		return err
	}
	r.Candidate, r.Capture = job.Source, job.Capture
	manifest, err := c.workspaces.readCaptureManifest(r, a, "work/"+a.AttemptID+"/capture-v1")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() != "candidate" && entry.Name() != "capture-v1" {
			return errors.New("candidate capture scratch has unknown content; preserve it")
		}
		if entry.Name() == "candidate" {
			if err = candidateScratchFiles(filepath.Join(root, "candidate")); err != nil {
				return err
			}
		}
	}
	path := filepath.Join(root, "capture-v1", "lease")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("candidate capture lease ownership changed; preserve it")
	}
	lease, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer lease.Close()
	opened, err := lease.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return errors.New("candidate capture lease identity changed; preserve it")
	}
	owned, err := tryCaptureLease(lease)
	if err != nil || !owned {
		return errors.New("candidate capture writer remains active; preserve it")
	}
	if err = c.candidateCaptureFiles(ctx, r, manifest, filepath.Join(root, "capture-v1")); err != nil {
		return err
	}
	return c.disk.Remove("work/" + a.AttemptID)
}

func (c *Candidates) candidateCaptureFiles(ctx context.Context, r workspaceRecord, manifest captureManifest, root string) error {
	// Git may read private objects while deriving their allowed identities. Refuse
	// links throughout that input tree before letting it open any object.
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("candidate capture scratch contains links or special files; preserve it")
		}
		return ctx.Err()
	}); err != nil {
		return err
	}
	files := map[string]bool{"lease": true, "manifest.json": true, "index": true, "tree-index": true}
	dirs := map[string]bool{".": true, "files": true, "objects": true}
	object := func(hash string) {
		dirs["objects/"+hash[:2]] = true
		files["objects/"+hash[:2]+"/"+hash[2:]] = true
	}
	for i, file := range manifest.Files {
		if file.Mode != "0" {
			files[fmt.Sprintf("files/%06d", i)] = true
			object(file.Blob)
		}
	}
	common, err := git(ctx, r.Repository, nil, nil, 4096, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return err
	}
	env := []string{"GIT_OBJECT_DIRECTORY=" + filepath.Join(root, "objects"), "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + strings.TrimSpace(string(common))}
	trees, err := git(ctx, r.Repository, env, nil, int(manifest.Limit), "ls-tree", "-r", "-d", "-z", r.Capture.Tree)
	if err != nil {
		return err
	}
	object(r.Capture.Tree)
	for _, line := range bytes.Split(trees, []byte{0}) {
		if len(line) == 0 {
			continue
		}
		header, _, found := bytes.Cut(line, []byte{'\t'})
		fields := strings.Fields(string(header))
		if !found || len(fields) != 3 || fields[1] != "tree" || len(fields[2]) != len(r.Base) {
			return errors.New("candidate capture tree inventory is invalid; preserve it")
		}
		if _, err := hex.DecodeString(fields[2]); err != nil {
			return errors.New("candidate capture tree object is invalid; preserve it")
		}
		object(fields[2])
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if entry.IsDir() && !dirs[name] || !entry.IsDir() && (!entry.Type().IsRegular() || !files[name]) {
			return errors.New("candidate capture scratch has unknown content; preserve it")
		}
		return ctx.Err()
	})
}
