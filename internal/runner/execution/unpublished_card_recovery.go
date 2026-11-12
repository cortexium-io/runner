package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Service) ValidateUnpublishedCardRecovery(ctx context.Context, child runner.Run, proof runner.UnpublishedCardAssessment) error {
	if err := s.ValidateSourceRecovery(ctx, child, proof.Source); err != nil {
		return err
	}
	w := s.workspaces
	if err := w.lockContext(ctx); err != nil {
		return err
	}
	defer w.mu.Unlock()
	r, err := w.sourceRecoveryRecord(ctx, child, proof.Source)
	if err != nil {
		return err
	}
	quiescent, err := w.cleanupQuiescent(r)
	if err != nil {
		return err
	}
	defer quiescent()
	if !r.Released {
		return errors.New("recovery source must have completed owned checkout cleanup")
	}
	if _, err = os.Lstat(r.WorkDir); !errors.Is(err, os.ErrNotExist) {
		return errors.New("recovery requires its settled absent checkout; preserve existing source")
	}
	task := r.TaskSources[proof.Authority.Identity.GroupStep]
	if task == nil || !task.Installed || !sameTaskAuthority(task.Authority, proof.Authority) || task.PinCommit != "" || len(task.Pins) != 0 {
		return errors.New("unpublished card source authority/seed differs or was already pinned")
	}
	raw, _ := json.Marshal(proof.Authority.Dependencies)
	if captureDigest(raw) != task.InputSHA || task.SeedPatchID != proof.Source.InputPatchID || r.SeedPatchID != task.SeedPatchID {
		return errors.New("original dependency manifest/seed differs")
	}
	for _, artifact := range proof.Artifacts {
		raw, e := w.disk.Read("artifacts/"+artifact.ID, w.disk.Policy.MaxArtifact)
		if e != nil || int64(len(raw)) != artifact.Bytes || captureDigest(raw) != artifact.SHA256 {
			return errors.New("bound recovery artifact is missing or changed")
		}
	}
	seed, err := w.disk.Read("artifacts/"+task.SeedPatchID, w.disk.Policy.MaxArtifact)
	if err != nil || captureDigest(seed) != task.SeedSHA {
		return errors.New("original dependency seed changed")
	}
	seedRef, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", taskSeedRef(r.RunID, proof.Authority.Identity.GroupStep))
	if err != nil || strings.TrimSpace(string(seedRef)) != task.SeedCommit {
		return errors.New("original dependency seed ref changed")
	}
	tree, err := taskTree(ctx, r.Repository, task.SeedCommit)
	if err != nil || tree != task.SeedTree {
		return errors.New("original dependency seed tree differs")
	}
	seedRecord := r
	seedRecord.Candidate = &runner.SourceResult{Tree: task.SeedTree}
	if _, err = w.validateSeed(ctx, seedRecord, seed, w.disk.Policy.MaxArtifact, false); err != nil {
		return err
	}
	patch, err := w.disk.Read("artifacts/"+proof.Source.PatchID, w.disk.Policy.MaxArtifact)
	if err != nil {
		return err
	}
	if _, err = w.validateSeedWithinTask(ctx, r, patch, w.disk.Policy.MaxArtifact, false, task); err != nil {
		return err
	}
	a := runner.Assignment{
		RunID:      child.ID,
		Source:     child.Source,
		Repository: child.Repository,
		TaskSource: &proof.Authority,
		Limits:     runner.ExecutionLimits{MaxArtifact: w.disk.Policy.MaxArtifact},
	}
	seen := map[string]bool{}
	for _, pin := range proof.Pins {
		if seen[pin.RunID] {
			return errors.New("duplicate completed recovery pin")
		}
		seen[pin.RunID] = true
		record, e := w.load(pin.RunID)
		if e != nil {
			return e
		}
		if !record.Released || record.Candidate == nil || record.Capture == nil {
			return errors.New("completed pin checkout cleanup or source capture is unsettled")
		}
		if e = s.captureReleaseReady(record.Candidate.AttemptID); e != nil {
			return e
		}
		release, e := w.cleanupQuiescent(record)
		if e != nil {
			return e
		}
		e = w.verifyTaskCandidate(ctx, a, r.Repository, r.Base, pin)
		release()
		if e != nil {
			return e
		}
	}
	for _, dependency := range proof.Authority.Dependencies {
		if !seen[dependency.RunID] {
			return errors.New("dependency has no preserved completed pin")
		}
	}
	if !reflect.DeepEqual(task.Authority, proof.Authority) {
		return errors.New("retained task authority changed")
	}
	return nil
}

func (s *Service) validateRecoveredCardStart(ctx context.Context, a runner.Assignment) error {
	if a.UnpublishedCardRecovery == nil {
		return nil
	}
	if a.Setup.Profile.Harness != "codex" || a.Setup.Profile.Access != "workspace-write" || a.TaskSource == nil {
		return errors.New("card recovery cannot change execution profile")
	}
	proof := *a.UnpublishedCardRecovery
	if !sameTaskAuthority(proof.Authority, *a.TaskSource) {
		return errors.New("recovered owner task authority changed before dispatch")
	}
	return s.ValidateUnpublishedCardRecovery(ctx, runner.Run{ID: a.RunID, Repository: a.Repository, Source: a.Source, ResourcesReleased: true}, proof)
}
