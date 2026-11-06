package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

type adoptedSourceRecord struct {
	AttemptID string `json:"attempt_id"`
	Head      string `json:"head"`
	Tree      string `json:"tree"`
	PatchSHA  string `json:"verified_patch_sha256"`
	SeedSHA   string `json:"seed_sha256"`
}

// Helper-owned seeds are not registered with Core until their source result
// returns. Only an exact persisted source intent may read that bounded file
// directly; ordinary artifact IDs still go through the configured reader.
func (w *Workspaces) readSeed(ctx context.Context, r workspaceRecord, limit runner.Bytes) ([]byte, error) {
	want := ""
	if p := r.AdoptedSource; p != nil && r.SeedPatchID == p.AttemptID+"/changes.patch" {
		want = p.SeedSHA
	}
	for _, t := range r.TaskSources {
		if t != nil && r.SeedPatchID == t.SeedPatchID {
			if want != "" && want != t.SeedSHA {
				return nil, errors.New("owned source seed intents disagree")
			}
			want = t.SeedSHA
		}
	}
	if want == "" {
		if w.read == nil {
			return nil, errors.New("workspace seed artifact reader is unavailable")
		}
		return w.read(ctx, r.SeedPatchID)
	}
	patch, err := w.disk.Read("artifacts/"+r.SeedPatchID, limit)
	if err == nil && captureDigest(patch) != want {
		err = errors.New("owned source seed differs from authenticated intent; preserve it")
	}
	return patch, err
}

// The release adapter authenticates the accepted publication before this call.
// This narrow handoff can replace only a pristine pre-model checkout. It cannot
// turn an arbitrary late patch into a new seed or change the original base.
func (w *Workspaces) prepareAdoptedSource(ctx context.Context, a runner.Assignment, acceptedHead, acceptedTree, patchSHA string) (*runner.SourceResult, error) {
	if a.Setup.Profile.Harness != "release" || a.Setup.Profile.Mode != "source-adopt" || a.Setup.Profile.Access != "input-only" || a.TaskSource != nil || w.read == nil || !taskComponent(a.RunID) || !taskComponent(a.AttemptID) {
		return nil, errors.New("source adoption needs its authenticated fixed release role")
	}
	repo, base, err := w.taskRepository(ctx, a)
	if err != nil {
		return nil, err
	}
	tree, err := taskTree(ctx, repo, acceptedHead)
	if err != nil {
		return nil, err
	}
	if tree != acceptedTree {
		return nil, errors.New("accepted source head/tree differs")
	}
	parent, err := git(ctx, repo, nil, nil, 256, "rev-parse", "--verify", acceptedHead+"^")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(parent)) != base {
		return nil, errors.New("accepted source parent differs from original base")
	}
	patch, err := w.read(ctx, a.Inputs["patch"].ArtifactID)
	if err != nil {
		return nil, err
	}
	if len(patch) > int(a.Limits.MaxArtifact) || captureDigest(patch) != patchSHA {
		return nil, errors.New("accepted source patch changed after authentication")
	}
	canonical, err := git(ctx, repo, nil, nil, int(a.Limits.MaxArtifact), "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, acceptedTree, "--")
	if err != nil {
		return nil, err
	}
	if err = w.lockContext(ctx); err != nil {
		return nil, err
	}
	r, err := w.load(a.RunID)
	if errors.Is(err, os.ErrNotExist) {
		r = workspaceRecord{RunID: a.RunID, Repository: repo, Base: base, WorkDir: filepath.Join(w.disk.Root, "work", a.RunID, "source")}
		err = nil
	}
	proof := adoptedSourceRecord{AttemptID: a.AttemptID, Head: acceptedHead, Tree: acceptedTree, PatchSHA: patchSHA, SeedSHA: captureDigest(canonical)}
	if err == nil && r.AdoptedSource != nil && *r.AdoptedSource != proof {
		err = errors.New("source adoption intent changed; preserve it")
	}
	if err == nil && r.AdoptedSource == nil {
		if r.Repository != repo || r.Base != base || r.Candidate != nil || r.SeedPatchID != "" {
			err = errors.New("source adoption would replace unrelated source evidence")
		}
		if err == nil {
			if _, e := os.Lstat(r.WorkDir); e == nil {
				_, _, err = w.retainedCandidate(ctx, r, true)
			} else if !errors.Is(e, os.ErrNotExist) {
				err = e
			}
		}
		if err == nil {
			validation := r
			validation.Candidate = &runner.SourceResult{Tree: acceptedTree}
			_, err = w.validateSeed(ctx, validation, patch, a.Limits.MaxArtifact, false)
		}
		if err == nil {
			id := a.AttemptID + "/changes.patch"
			err = w.disk.Write("artifacts/"+id, canonical, a.Limits.MaxArtifact)
			if err == nil {
				r.AdoptedSource = &proof
				r.SeedPatchID, r.SeedReady, r.ReadyGeneration = id, false, ""
				err = w.save(r)
			}
		}
	}
	if err == nil && r.AdoptedSource != nil {
		_, err = w.readSeed(ctx, r, a.Limits.MaxArtifact)
	}
	w.mu.Unlock()
	if err != nil {
		return nil, err
	}
	prepared := runner.Clone(a)
	prepared.Inputs = runner.Clone(a.Inputs)
	prepared.Inputs["patch"] = runner.Value{ArtifactID: a.AttemptID + "/changes.patch"}
	if _, err = w.Prepare(ctx, prepared); err != nil {
		return nil, err
	}
	if err = taskUpdateRef(ctx, repo, sourceAdoptionRef(a.RunID, a.AttemptID), acceptedHead, ""); err != nil {
		return nil, err
	}
	source, err := w.Capture(ctx, a)
	if err != nil {
		return nil, err
	}
	if source.Tree != acceptedTree {
		return nil, errors.New("adopted capture differs from accepted source tree")
	}
	return source, nil
}

func sourceAdoptionRef(run, attempt string) string {
	return "refs/runner/source-adoptions/" + run + "/" + attempt
}

func (w *Workspaces) forgetAdoptedSource(ctx context.Context, id string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, err := w.load(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || r.AdoptedSource == nil {
		return err
	}
	if !r.Released {
		return errors.New("source adoption cleanup is not settled")
	}
	ref := sourceAdoptionRef(id, r.AdoptedSource.AttemptID)
	current, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", ref)
	if err != nil {
		if missingCandidateRef(ctx, r.Repository, ref) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(string(current)) != r.AdoptedSource.Head {
		return errors.New("accepted source pin changed; preserve it")
	}
	_, err = git(ctx, r.Repository, nil, nil, 1024, "update-ref", "-d", ref, r.AdoptedSource.Head)
	return err
}
