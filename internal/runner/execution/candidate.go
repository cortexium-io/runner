package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		State: runner.Finished,
		End:   job.End,
		Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
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

func (c *Candidates) Release(_ context.Context, id string) error {
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
	if err = c.releaseScratch(job); err != nil {
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
	if r.Candidate == nil || r.Candidate.PatchID != a.Inputs["patch"].ArtifactID {
		return "", errors.New("integration patch differs from the retained run candidate")
	}
	patch, err := w.disk.Read("artifacts/"+r.Candidate.PatchID, a.Limits.MaxArtifact)
	if err != nil {
		return "", err
	}
	if r.Capture == nil || r.Capture.AttemptID != r.Candidate.AttemptID || r.Capture.PatchSHA == "" || captureDigest(patch) != r.Capture.PatchSHA {
		return "", errors.New("reviewed candidate patch differs from its committed capture")
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
