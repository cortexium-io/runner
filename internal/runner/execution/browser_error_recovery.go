package execution

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/cortexium-io/runner/internal/release"
	"github.com/cortexium-io/runner/internal/runner"
)

// Validation is read-only. Normal owner preparation still validates/restores the
// candidate, and every changed candidate still traverses full release gates.
func (s *Service) ValidateBrowserErrorRecovery(ctx context.Context, run runner.Run, source runner.SourceResult, report []byte, head string) (json.RawMessage, error) {
	feedback, err := release.RecoverBrowserErrorFeedback(report)
	if err != nil {
		return nil, err
	}
	if err := s.ValidateSourceRecovery(ctx, run, source); err != nil {
		return nil, err
	}
	if err := verifyFrontendCheckCardPins(ctx, run); err != nil {
		return nil, err
	}
	r := s.releases
	if r.cfg == nil || !filepath.IsAbs(r.cfg.GitHub) || !publicationCommit.MatchString(head) {
		return nil, errors.New("browser recovery requires configured publication verification")
	}
	binding, err := r.loadPublication(runner.Assignment{
		RunID:      run.ID,
		ProjectID:  run.ProjectID,
		Repository: run.Repository,
		Source:     run.Source,
	})
	if err != nil {
		return nil, err
	}
	if err := verifyBrowserRecoveryPublication(run, binding, head); err != nil {
		return nil, err
	}
	if err := r.validateFrontendDraft(ctx, run, binding.Latest); err != nil {
		return nil, err
	}
	return json.Marshal(feedback)
}

func verifyBrowserRecoveryPublication(run runner.Run, binding publicationBinding, head string) error {
	if binding.AuthorityOnly || binding.Latest.Action != "publish" || binding.Latest.Head != head || binding.Latest.RunID != run.ID {
		return errors.New("browser recovery does not own the current published candidate")
	}
	for i := len(run.Attempts) - 1; i >= 0; i-- {
		a := run.Attempts[i]
		if a.Step < 0 || a.Step >= len(run.Process.Steps) || run.Process.Steps[a.Step].ID != "publish" {
			continue
		}
		var receipt release.Receipt
		if a.State != runner.Finished || !a.Released || a.End == nil || a.End.Kind != "returned" || a.End.Result == nil || a.End.Result.Outcome != "ready" ||
			json.Unmarshal(a.End.Result.Outputs["receipt"].Inline, &receipt) != nil || receipt != binding.Latest || a.ID != binding.LastAttempt {
			return errors.New("browser recovery latest publication differs from saved execution")
		}
		return nil
	}
	return errors.New("browser recovery lacks its saved same-run publication")
}
