package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/verification"
	"github.com/cortexium-io/runner/internal/workspace"
)

const maxCardVerificationBytes = 2 * 1024 * 1024

// The private candidate digest binds these observations to committed source,
// approved card content and destination. Model-written receipts are never loaded.
type cardVerificationRecord struct {
	Version         int                 `json:"version"`
	CandidateDigest string              `json:"candidate_digest"`
	Entrypoint      string              `json:"entrypoint"`
	SettingsDigest  string              `json:"settings_digest"`
	Result          verification.Result `json:"result"`
}

func (s *Engine) cardVerificationPath(itemID string) string {
	return filepath.Join(s.implementationWorkspaceRoot(), ".runner-state", "card-verification", safeRefComponent(itemID)+".json")
}

func (s *Engine) cardVerificationInstructions() string {
	gate := s.cfg.CardVerification
	if gate == nil {
		return ""
	}
	entry := s.cfg.Verification[gate.Entrypoint]
	command, _ := json.Marshal(append([]string{entry.Command}, entry.Args...))
	return fmt.Sprintf(`

Runner-configured pre-QA card verification: %q, argv %s.
The operator has scheduled this exact command to run automatically against a private copy of the committed implementation, before independent Agent QA. Runner owns dependency preparation, execution, cleanup and candidate-bound receipts for this gate. Agent sandbox permissions do not change. Do not run this scheduled command, install its browser dependencies, or request a manual operator handoff merely because its receipt is not available during implementation.
Finish the source change and the other applicable proof, and accurately identify this command as pending Runner verification in the implementation result. Never claim the scheduled execution has passed. Runner supplies actual passing evidence to the reviewer and prevents publication unless it remains applicable to the reviewed candidate.
`, gate.Entrypoint, command)
}

// prepareCardVerification checks a committed implementation before QA, and
// reuses applicable proof at publication. Neither role gains host shell access.
// The caller retains the private copy through the publication proof guard.
func (s *Engine) prepareCardVerification(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, candidate workspace.Candidate, attemptID string) (guard func(context.Context, github.AuthorizedAction) error, cleanup func(), result verification.Result, err error) {
	gate := s.cfg.CardVerification
	if gate == nil {
		return nil, func() {}, verification.Result{}, nil
	}
	entry, ok := s.cfg.Verification[gate.Entrypoint]
	if !ok || gate.Access != config.RoleAccessHost || action.Item.PlanRelease != "" {
		return nil, func() {}, verification.Result{}, errors.New("card verification has no valid operator host grant for this individual card")
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return nil, func() {}, verification.Result{}, err
	}
	if !snapshot.Clean || snapshot.Head != candidate.CommitOID || snapshot.Tree != candidate.TreeOID {
		return nil, func() {}, verification.Result{}, errors.New("card verification requires the exact clean committed candidate")
	}
	binding, err := json.Marshal(struct {
		Identity workspace.Identity
		Snapshot string
	}{metadata.Identity, snapshot.Fingerprint})
	if err != nil {
		return nil, func() {}, verification.Result{}, err
	}
	candidateDigest := fmt.Sprintf("%x", sha256.Sum256(binding))
	validateCandidate := func(ctx context.Context, current github.AuthorizedAction) error {
		content, err := current.DelegatedContent()
		if err != nil {
			return err
		}
		if current.Item.ID != metadata.Identity.ItemID || content.Digest != metadata.Identity.DelegatedContentDigest || current.Item.Branch != metadata.BranchName {
			return errors.New("card authority or destination changed during verification")
		}
		currentSnapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
		if err != nil {
			return err
		}
		// Publication revalidates the protected acceptance under its Git lock.
		// Its callback must only observe source, never reacquire that lock.
		if !currentSnapshot.Clean || currentSnapshot.Head != candidate.CommitOID || currentSnapshot.Tree != candidate.TreeOID || currentSnapshot.Fingerprint != snapshot.Fingerprint {
			return errors.New("card verification source no longer matches its committed candidate")
		}
		source, err := s.checkoutSnapshotState(ctx, metadata.RepoRoot)
		if err != nil || source.Fingerprint != metadata.SourceSnapshot {
			return errors.Join(errors.New("active checkout changed during card verification"), err)
		}
		return nil
	}
	if err := validateCandidate(ctx, action); err != nil {
		return nil, func() {}, verification.Result{}, err
	}
	copy, err := provider.PrepareReviewWorkspace(ctx, metadata, candidate)
	if err != nil {
		return nil, func() {}, verification.Result{}, err
	}
	cleanup = func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = copy.Cleanup(cleanupCtx)
	}
	observe := func(ctx context.Context) (verification.Observation, error) {
		fresh, err := s.source.Authorize(ctx, github.WorkItem{ID: metadata.Identity.ItemID})
		if err != nil {
			return verification.Observation{}, err
		}
		if err := validateCandidate(ctx, fresh); err != nil {
			return verification.Observation{}, err
		}
		content, err := fresh.DelegatedContent()
		if err != nil {
			return verification.Observation{}, err
		}
		observed, err := verification.ObserveCandidate(ctx, copy.Path, metadata.BaseRevision, content.BodySnapshot, entry)
		if err == nil && (observed.CommitOID != candidate.CommitOID || observed.TreeOID != candidate.TreeOID) {
			err = errors.New("card verification copy differs from the reviewed candidate")
		}
		return observed, err
	}
	request := verification.Request{
		Entrypoint: gate.Entrypoint, Entry: entry, Directory: copy.Path,
		Repository: metadata.Identity.Repository, AttemptID: attemptID, Boundary: execution.VerificationFocused, Observe: observe,
	}
	prior, err := s.loadCardVerification(metadata.Identity.ItemID)
	if err != nil {
		return nil, cleanup, verification.Result{}, err
	}
	if prior != nil && prior.CandidateDigest == candidateDigest && prior.Entrypoint == gate.Entrypoint && prior.SettingsDigest == entry.Digest() {
		request.PreviousReceipt, request.PreviousDigest = prior.Result.Receipt, prior.Result.Digest
	}
	observed, runErr := verification.Run(ctx, request)
	record := cardVerificationRecord{Version: 1, CandidateDigest: candidateDigest, Entrypoint: gate.Entrypoint, SettingsDigest: entry.Digest(), Result: observed}
	var unresolved *subprocess.CleanupError
	if errors.As(runErr, &unresolved) {
		s.processOwnership.RecordCleanupFailure()
		cleanup = func() {} // Keep the checkout while an owned descendant may use it.
	}
	if err := s.saveCardVerification(metadata.Identity.ItemID, record); err != nil {
		return nil, cleanup, verification.Result{}, errors.Join(runErr, err)
	}
	if runErr != nil {
		return nil, cleanup, verification.Result{}, fmt.Errorf("configured card verification %q did not pass; diagnostics retained in %s: %w", gate.Entrypoint, s.cardVerificationPath(metadata.Identity.ItemID), runErr)
	}
	if observed.Receipt == nil || observed.Receipt.Outcome != "passed" || observed.Invocation.Outcome != "passed" || !observed.Invocation.CleanupResolved {
		return nil, cleanup, verification.Result{}, errors.New("configured card verification has no passing complete observation")
	}
	envelopeDigest, digestErr := observed.Evidence().Digest()
	if digestErr != nil {
		return nil, cleanup, verification.Result{}, digestErr
	}
	guard = func(ctx context.Context, current github.AuthorizedAction) error {
		if err := validateCandidate(ctx, current); err != nil {
			return err
		}
		fresh, err := observe(ctx)
		if err != nil {
			return err
		}
		applicable, err := execution.AssessVerificationEnvelope(observed.Evidence(), envelopeDigest, execution.VerificationEnvelopeTarget{
			VerificationTarget: execution.VerificationTarget{Repository: metadata.Identity.Repository, CandidateOID: candidate.CommitOID,
				Entrypoint: gate.Entrypoint, SettingsDigest: strings.TrimPrefix(entry.Digest(), "v1:"), Inputs: fresh.Inputs,
				Boundary: execution.VerificationFocused, RequireCurrentCandidate: true},
			TreeOID: fresh.TreeOID, BaseOID: fresh.BaseOID, CandidateIntegrity: fresh.Integrity,
			CurrentCandidateCommand: planCurrentCandidateCommand(entry),
		})
		if err != nil {
			return err
		}
		if !applicable.Applicable {
			return errors.New("card verification is no longer applicable: " + applicable.Reason)
		}
		return nil
	}
	if err := guard(ctx, action); err != nil {
		return nil, cleanup, verification.Result{}, err
	}
	return guard, cleanup, observed, nil
}

func (s *Engine) loadCardVerification(itemID string) (*cardVerificationRecord, error) {
	data, mode, state, err := securefs.ReadFile(s.cardVerificationPath(itemID), maxCardVerificationBytes)
	if errors.Is(err, os.ErrNotExist) || err == nil && !state.Exists {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mode.Perm() != 0o600 {
		return nil, errors.New("card verification record must have mode 0600")
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record cardVerificationRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("card verification record has trailing data")
	}
	if record.Version != 1 || record.CandidateDigest == "" || record.Entrypoint == "" || record.SettingsDigest == "" {
		return nil, errors.New("card verification record has invalid provenance")
	}
	return &record, nil
}

func (s *Engine) saveCardVerification(itemID string, record cardVerificationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(data) > maxCardVerificationBytes {
		return errors.New("card verification result exceeds the private storage limit")
	}
	path := s.cardVerificationPath(itemID)
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	dir, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	_, _, state, err := dir.ReadFile(filepath.Base(path), maxCardVerificationBytes)
	if err != nil {
		return err
	}
	return dir.ReplaceFile(filepath.Base(path), append(data, '\n'), 0o600, state)
}
