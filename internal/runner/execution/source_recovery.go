package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func (s *Service) ValidateSourceRecovery(ctx context.Context, run runner.Run, source runner.SourceResult) error {
	if err := s.captureReleaseReady(source.AttemptID); err != nil {
		return err
	}
	return s.workspaces.validateSourceRecovery(ctx, run, source)
}

// Validation never restores or removes a checkout. An existing checkout uses
// the ordinary disposable-index verification; absent source needs both durable
// cleanup confirmations. Normal Prepare still validates/restores before start.
func (w *Workspaces) validateSourceRecovery(ctx context.Context, run runner.Run, source runner.SourceResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, err := w.sourceRecoveryRecord(ctx, run, source)
	if err != nil {
		return err
	}
	quiescent, err := w.cleanupQuiescent(r)
	if err != nil {
		return err
	}
	defer quiescent()
	if _, err := os.Lstat(r.WorkDir); errors.Is(err, os.ErrNotExist) {
		if !r.Released || !run.ResourcesReleased {
			return errors.New("absent source lacks confirmed owned cleanup; preserve it")
		}
		return nil
	} else if err != nil {
		return err
	}
	if err := w.verifyWorkspace(ctx, r); err != nil {
		return err
	}
	if info, err := os.Lstat(filepath.Join(r.WorkDir, ".runner-tmp")); err == nil && !info.IsDir() {
		return errors.New("workspace scratch root is not an owned directory; preserve it")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, _, err = w.retainedCandidate(ctx, r, true)
	return err
}

func (w *Workspaces) sourceRecoveryRecord(ctx context.Context, run runner.Run, source runner.SourceResult) (workspaceRecord, error) {
	r, err := w.load(run.ID)
	if err != nil {
		return r, err
	}
	repo, err := filepath.EvalSymlinks(run.Repository)
	if err != nil || repo != r.Repository || !reflect.DeepEqual(r.Candidate, &source) {
		return r, errors.New("retained source/workspace identity differs; preserve it")
	}
	if run.Source == nil || (len(run.Source.Ref) == 40 || len(run.Source.Ref) == 64) && run.Source.Ref != source.Base {
		return r, errors.New("retained source differs from the run baseline; preserve it")
	}
	if r.Capture == nil || r.Capture.AttemptID != source.AttemptID || r.Capture.Tree != source.Tree || r.Capture.PatchSHA == "" {
		return r, errors.New("retained source/workspace capture identity differs; preserve it")
	}
	patch, err := w.disk.Read("artifacts/"+source.PatchID, w.disk.Policy.MaxArtifact)
	if err != nil {
		return r, err
	}
	if captureDigest(patch) != r.Capture.PatchSHA {
		return r, errors.New("retained source patch differs from its committed capture; preserve it")
	}
	base, err := git(ctx, r.Repository, nil, nil, 256, "rev-parse", "--verify", pin(run.ID))
	if err != nil || strings.TrimSpace(string(base)) != source.Base {
		return r, errors.New("retained source base ownership changed; preserve it")
	}
	return r, nil
}
