package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

// Candidates retains an accepted patch as a local commit/ref. It never checks
// out a branch, changes the user's index, merges, pushes or runs project hooks.
// The normal core owns dispatch, retry, persistence and cleanup policy.
type Candidates struct {
	disk       *runner.Disk
	workspaces *Workspaces
}

type candidateJob struct {
	Assignment runner.Assignment    `json:"assignment"`
	End        *runner.ExecutionEnd `json:"end,omitempty"`
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
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return c.disk.Write(candidateName(job.Assignment.AttemptID), data, c.disk.Policy.Database.MaxRecord)
}

func (c *Candidates) Start(ctx context.Context, a runner.Assignment) (runner.Observation, error) {
	if _, err := c.load(a.AttemptID); errors.Is(err, os.ErrNotExist) {
		if err = c.save(candidateJob{Assignment: runner.Clone(a)}); err != nil {
			return runner.Observation{}, err
		}
	} else if err != nil {
		return runner.Observation{}, err
	}
	return c.Observe(ctx, a.AttemptID)
}

func (c *Candidates) Observe(ctx context.Context, id string) (runner.Observation, error) {
	job, err := c.load(id)
	if err != nil {
		return runner.Observation{}, err
	}
	if job.End == nil {
		candidate, err := c.workspaces.retainCandidate(ctx, job.Assignment)
		if err != nil {
			if ctx.Err() != nil {
				return runner.Observation{}, ctx.Err() // Reconcile the same saved intent.
			}
			job.End = &runner.ExecutionEnd{Kind: "failed", Reason: runner.Clip(err.Error())}
		} else {
			job.End = &runner.ExecutionEnd{
				Kind: "returned",
				Result: &runner.StepResult{
					Outcome: "ready",
					Outputs: map[string]runner.Value{"candidate": runner.Text(candidate)},
				},
			}
		}
		if err = c.save(job); err != nil {
			return runner.Observation{}, err
		}
	}
	zero := int64(0)
	return runner.Observation{
		State: runner.Finished,
		End:   job.End,
		Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
	}, nil
}

func (c *Candidates) Stop(ctx context.Context, id string) error {
	job, err := c.load(id)
	if err != nil || job.End != nil {
		return err
	}
	w, err := c.workspaces.load(job.Assignment.RunID)
	if err != nil {
		return err
	}
	if missingCandidateRef(ctx, w.Repository, candidateRef(w.RunID)) {
		job.End = &runner.ExecutionEnd{Kind: "stopped", Reason: "candidate integration stopped before branch creation"}
		return c.save(job)
	}
	// An existing ref may be the result whose acknowledgement was lost.
	_, err = c.Observe(ctx, id)
	return err
}

func (c *Candidates) Release(_ context.Context, id string) error {
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
	return c.disk.Remove(candidateName(id))
}

func (w *Workspaces) retainCandidate(ctx context.Context, a runner.Assignment) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var verdict string
	if a.Setup.Profile.Harness != "git-candidate" || a.Setup.Profile.Access != "workspace-read" ||
		json.Unmarshal(a.Inputs["verdict"].Inline, &verdict) != nil || verdict != "APPROVE" {
		var review string
		_ = json.Unmarshal(a.Inputs["review"].Inline, &review)
		return "", errors.New("candidate integration requires an explicit APPROVE verdict; user decision needed: " + runner.Clip(review))
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
	scratch := filepath.Join(w.disk.Root, "work", a.AttemptID, "candidate")
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return "", err
	}
	defer os.RemoveAll(scratch)
	env := []string{
		"GIT_INDEX_FILE=" + filepath.Join(scratch, "index"),
		"GIT_AUTHOR_NAME=Cortexium Runner",
		"GIT_AUTHOR_EMAIL=runner@localhost",
		"GIT_COMMITTER_NAME=Cortexium Runner",
		"GIT_COMMITTER_EMAIL=runner@localhost",
		"GIT_AUTHOR_DATE=" + a.Deadline.UTC().Format("2006-01-02T15:04:05Z"),
		"GIT_COMMITTER_DATE=" + a.Deadline.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if _, err = git(ctx, r.Repository, env, nil, 1024, "read-tree", r.Base); err != nil {
		return "", err
	}
	if len(patch) != 0 {
		if _, err = git(ctx, r.Repository, env, patch, 1024, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
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
	ref := candidateRef(a.RunID)
	hash := strings.TrimSpace(string(commit))
	if r.CandidatePins == nil {
		r.CandidatePins = map[string]string{}
	}
	if previous, ok := r.CandidatePins[a.AttemptID]; ok && previous != hash {
		return "", errors.New("saved candidate intent differs")
	}
	r.CandidatePins[a.AttemptID] = hash
	if err = w.save(r); err != nil {
		return "", err
	}
	pinRef := "refs/runner/candidates/" + a.RunID + "/" + a.AttemptID
	pinned, pinErr := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", pinRef)
	if pinErr == nil {
		if strings.TrimSpace(string(pinned)) != hash {
			return "", errors.New("candidate history pin changed; preserve it")
		}
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
