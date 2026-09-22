package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

// VerificationEnvelope is the coordinator's protected pair of distinct facts.
// Heavy retains the original check's identity, source and timings on reuse.
// An absent heavy check is nil, never a fabricated failed execution. The whole
// envelope digest belongs in protected state; model-supplied hashes grant no
// provenance. Failed envelopes are diagnostics, not publication acceptance.
type VerificationEnvelope struct {
	Version               int                                       `json:"version"`
	Heavy                 *VerificationReceipt                      `json:"heavy,omitempty"`
	CurrentCandidateCheck *VerificationCurrentCandidateCheckReceipt `json:"current_candidate_check,omitempty"`
}

// VerificationCurrentCandidateCheckReceipt records the maintained broad guard
// on this exact candidate. It has no executable-input reuse policy. A recovered
// invocation must run it again rather than call historical assessment "fresh".
type VerificationCurrentCandidateCheckReceipt struct {
	ExecutionID         string               `json:"execution_id"`
	AttemptID           string               `json:"attempt_id"`
	Repository          string               `json:"repository"`
	PlanID              string               `json:"plan_id,omitempty"`
	PlanRevision        string               `json:"plan_revision,omitempty"`
	Entrypoint          string               `json:"entrypoint"`
	Boundary            VerificationBoundary `json:"boundary"`
	SourceCommitOID     string               `json:"source_commit_oid"`
	SourceTreeOID       string               `json:"source_tree_oid"`
	SourceBaseOID       string               `json:"source_base_oid"`
	CandidateIntegrity  string               `json:"candidate_integrity"`
	SettingsDigest      string               `json:"settings_digest"`
	Command             []string             `json:"command"`
	StartedAt           time.Time            `json:"started_at"`
	FinishedAt          time.Time            `json:"finished_at"`
	RunStartedAt        *time.Time           `json:"run_started_at,omitempty"`
	RunFinishedAt       *time.Time           `json:"run_finished_at,omitempty"`
	RunMilliseconds     *int64               `json:"run_ms,omitempty"`
	CleanupMilliseconds *int64               `json:"cleanup_ms,omitempty"`
	Outcome             string               `json:"outcome"`
	ExitCode            *int                 `json:"exit_code,omitempty"`
	ReportDigest        string               `json:"report_digest"`
	CleanupResolved     bool                 `json:"cleanup_resolved"`
}

// VerificationEnvelopeTarget adds exact current candidate/authority bindings to
// heavy applicability. CurrentCandidateCommand is empty only when no guard is
// configured. The caller obtains all fields independently, never from evidence.
type VerificationEnvelopeTarget struct {
	VerificationTarget
	PlanRevision            string
	TreeOID                 string
	BaseOID                 string
	CandidateIntegrity      string
	CurrentCandidateCommand []string
}

func (e VerificationEnvelope) Digest() (string, error) {
	if e.Version != 1 || e.Heavy == nil && e.CurrentCandidateCheck == nil {
		return "", errors.New("verification evidence has no observed check")
	}
	if e.Heavy != nil {
		if err := e.Heavy.validate(); err != nil {
			return "", err
		}
	}
	if e.CurrentCandidateCheck != nil {
		if err := e.CurrentCandidateCheck.validate(); err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// AuthenticateVerificationEnvelope authenticates even failed observations so
// recovery can preserve their truth. It does not assert applicability or pass.
func AuthenticateVerificationEnvelope(e VerificationEnvelope, expectedDigest string) error {
	digest, err := e.Digest()
	if err != nil {
		return err
	}
	if !validVerificationDigest(expectedDigest) || digest != expectedDigest {
		return errors.New("verification evidence does not match protected provenance")
	}
	return nil
}

// AssessVerificationEnvelope authenticates the complete protected pair and
// assesses applicability. It does not run checks or renew freshness: recovery
// must invoke the launcher with the authenticated Heavy member and its digest.
// A passed guard alone never substitutes for applicable heavy proof.
func AssessVerificationEnvelope(e VerificationEnvelope, expectedDigest string, target VerificationEnvelopeTarget) (VerificationApplicability, error) {
	result := VerificationApplicability{Historical: true}
	if err := AuthenticateVerificationEnvelope(e, expectedDigest); err != nil {
		return result, err
	}
	if e.Heavy == nil {
		result.Reason = "heavy verification has not run"
		return result, nil
	}
	heavyDigest, err := e.Heavy.Digest()
	if err != nil {
		return result, err
	}
	result, err = AssessVerificationReceipt(*e.Heavy, heavyDigest, target.VerificationTarget)
	if err != nil || !result.Applicable {
		return result, err
	}
	refuse := func(reason string) (VerificationApplicability, error) {
		result.Applicable, result.Reason = false, reason
		return result, nil
	}
	guard := e.CurrentCandidateCheck
	if len(target.CurrentCandidateCommand) == 0 {
		if guard != nil {
			return refuse("current-candidate check policy changed")
		}
		return result, nil
	}
	if guard == nil {
		return refuse("current-candidate check is missing")
	}
	if guard.Outcome != "passed" || !guard.CleanupResolved || guard.ExitCode == nil || *guard.ExitCode != 0 {
		return refuse("current-candidate check did not pass with resolved cleanup")
	}
	if target.CandidateIntegrity == "" || !validVerificationOID(target.TreeOID) || !validVerificationOID(target.BaseOID) || (target.PlanID == "") != (target.PlanRevision == "") {
		return refuse("current-candidate bindings are unavailable")
	}
	if guard.Repository != target.Repository || guard.PlanID != target.PlanID || guard.PlanRevision != target.PlanRevision || guard.Entrypoint != target.Entrypoint || guard.Boundary != target.Boundary || guard.SettingsDigest != target.SettingsDigest || !slices.Equal(guard.Command, target.CurrentCandidateCommand) {
		return refuse("current-candidate check authority or settings changed")
	}
	if guard.SourceCommitOID != target.CandidateOID || guard.SourceTreeOID != target.TreeOID || guard.SourceBaseOID != target.BaseOID || guard.CandidateIntegrity != target.CandidateIntegrity {
		return refuse("current-candidate check belongs to another candidate")
	}
	result.Reason = "applicable heavy proof and exact-candidate guard both passed"
	return result, nil
}

func (r VerificationCurrentCandidateCheckReceipt) validate() error {
	if r.ExecutionID == "" || r.AttemptID == "" || r.Repository == "" || r.Entrypoint == "" || (r.PlanID == "") != (r.PlanRevision == "") || r.CandidateIntegrity == "" {
		return errors.New("current-candidate receipt lacks execution or authority bindings")
	}
	if !validVerificationOID(r.SourceCommitOID) || !validVerificationOID(r.SourceTreeOID) || !validVerificationOID(r.SourceBaseOID) || !validVerificationDigest(r.SettingsDigest) || !validVerificationDigest(r.ReportDigest) {
		return errors.New("current-candidate receipt lacks source, settings or report bindings")
	}
	if r.Boundary != VerificationFocused && r.Boundary != VerificationComplete {
		return errors.New("current-candidate receipt boundary is invalid")
	}
	if len(r.Command) == 0 || strings.TrimSpace(r.Command[0]) == "" {
		return errors.New("current-candidate receipt command is missing")
	}
	for _, arg := range r.Command {
		if strings.ContainsRune(arg, 0) {
			return errors.New("current-candidate receipt command is invalid")
		}
	}
	_, startOffset := r.StartedAt.Zone()
	_, finishOffset := r.FinishedAt.Zone()
	if r.StartedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || startOffset != 0 || finishOffset != 0 || r.RunStartedAt == nil || r.RunFinishedAt == nil || r.RunMilliseconds == nil {
		return errors.New("current-candidate receipt requires observed UTC invocation and run intervals")
	}
	_, runStartOffset := r.RunStartedAt.Zone()
	_, runFinishOffset := r.RunFinishedAt.Zone()
	if r.RunStartedAt.Before(r.StartedAt) || r.RunFinishedAt.Before(*r.RunStartedAt) || r.RunFinishedAt.After(r.FinishedAt) || runStartOffset != 0 || runFinishOffset != 0 {
		return errors.New("current-candidate receipt run interval is invalid")
	}
	for _, ms := range []*int64{r.RunMilliseconds, r.CleanupMilliseconds} {
		if ms != nil && *ms < 0 {
			return errors.New("current-candidate receipt duration is invalid")
		}
	}
	if r.ExitCode != nil && *r.ExitCode < 0 {
		return errors.New("current-candidate receipt exit observation is invalid")
	}
	if r.Outcome == "passed" && (!r.CleanupResolved || r.ExitCode == nil || *r.ExitCode != 0) {
		return errors.New("passing current-candidate receipt requires normal exit and resolved cleanup")
	}
	switch r.Outcome {
	case "passed", "failed", "canceled", "timeout", "cleanup_unresolved":
		return nil
	default:
		return errors.New("current-candidate receipt outcome is invalid")
	}
}
