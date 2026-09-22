package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"
)

// VerificationInputs names Runner-observed applicability inputs. Each digest
// covers the exact relevant input set, including names as well as content.
// Requirements may be an affected subset; Base covers relevant base inputs,
// not merely the destination tip. Screenshots and receipts are not executable
// inputs, but remain protected by the separate full candidate integrity check.
type VerificationInputs struct {
	Selection     VerificationInputSelection `json:"selection"`
	Requirements  string                     `json:"requirements"`
	Executable    string                     `json:"executable"`
	Dependencies  string                     `json:"dependencies"`
	Configuration string                     `json:"configuration"`
	Environment   string                     `json:"environment"`
	Base          string                     `json:"base"`
}

// VerificationInputSelection comes from the reviewed entrypoint configuration,
// never a model-proposed file list. Policy identifies the selector semantics;
// Paths identifies its explicit roots. Input digests cover the resulting file
// names/content and non-file inputs. The launcher must verify that collection
// follows this policy; validating this data alone cannot prove completeness.
type VerificationInputSelection struct {
	Policy string   `json:"policy"`
	Paths  []string `json:"paths"`
}

// VerificationReceipt is produced only by Runner's supported verification
// launcher after observing the command and cleanup. A model-authored report or
// captured artifact cannot create this attestation. Persist its digest in an
// independently protected existing evidence/acceptance record, not alongside
// untrusted report text. Durations are observed milliseconds, not estimates;
// nil means unavailable, whereas a pointer to zero is an observed zero.
type VerificationReceipt struct {
	Preparation *VerificationPreparationReceipt `json:"preparation,omitempty"`
	Version     int                             `json:"version"`
	ExecutionID string                          `json:"execution_id"`
	AttemptID   string                          `json:"attempt_id"`
	StartedAt   time.Time                       `json:"started_at"`
	FinishedAt  time.Time                       `json:"finished_at"`
	// StartedAt/FinishedAt include observation, waiting and cleanup. These
	// nullable endpoints identify the supervised run interval, not an inferred
	// process spawn timestamp. They are absent when execution was not admitted.
	RunStartedAt        *time.Time           `json:"run_started_at,omitempty"`
	RunFinishedAt       *time.Time           `json:"run_finished_at,omitempty"`
	Repository          string               `json:"repository"`
	PlanID              string               `json:"plan_id,omitempty"`
	PlanRevision        string               `json:"plan_revision,omitempty"`
	SourceCommitOID     string               `json:"source_commit_oid"`
	SourceTreeOID       string               `json:"source_tree_oid"`
	SourceBaseOID       string               `json:"source_base_oid"`
	Entrypoint          string               `json:"entrypoint"`
	Command             []string             `json:"command"`
	SettingsDigest      string               `json:"settings_digest"`
	Inputs              VerificationInputs   `json:"inputs"`
	Boundary            VerificationBoundary `json:"boundary"`
	Outcome             string               `json:"outcome"`
	ExitCode            *int                 `json:"exit_code,omitempty"` // nil preserves unavailable/historical observations
	ReportDigest        string               `json:"report_digest"`
	CleanupResolved     bool                 `json:"cleanup_resolved"`
	WaitMilliseconds    *int64               `json:"wait_ms,omitempty"`
	RunMilliseconds     *int64               `json:"run_ms,omitempty"`
	CleanupMilliseconds *int64               `json:"cleanup_ms,omitempty"`
}

// VerificationPreparationReceipt records dependency preparation, never check
// proof. Its supervised run and cleanup share the enclosing invocation deadline.
type VerificationPreparationReceipt struct {
	Command             []string  `json:"command"`
	StartedAt           time.Time `json:"started_at"`
	RunFinishedAt       time.Time `json:"run_finished_at"`
	FinishedAt          time.Time `json:"finished_at"`
	RunMilliseconds     int64     `json:"run_ms"`
	CleanupMilliseconds *int64    `json:"cleanup_ms,omitempty"`
	Outcome             string    `json:"outcome"`
	ExitCode            *int      `json:"exit_code,omitempty"`
	ReportDigest        string    `json:"report_digest"`
	CleanupResolved     bool      `json:"cleanup_resolved"`
}

// VerificationTarget contains current independently observed bindings. Unknown
// inputs must not be filled from a historical receipt just to permit reuse.
// PlanRevision deliberately remains on the receipt: an amendment can preserve
// unaffected proof only when the coordinator independently resolves the exact
// current obligation subset and computes its Requirements digest. A different
// revision is not permission to copy the old digest or ignore changed criteria.
type VerificationTarget struct {
	Repository              string
	PlanID                  string
	CandidateOID            string
	Entrypoint              string
	SettingsDigest          string
	Inputs                  VerificationInputs
	Boundary                VerificationBoundary
	RequireCurrentCandidate bool
}

type VerificationApplicability struct {
	Applicable bool   `json:"applicable"`
	Reason     string `json:"reason"`
	// Historical is always true: assessing an existing receipt is not running a
	// new check, even when its source commit equals the current candidate.
	Historical      bool   `json:"historical"`
	ExecutionID     string `json:"execution_id"`
	SourceCommitOID string `json:"source_commit_oid"`
}

func (r VerificationReceipt) Digest() (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// AssessVerificationReceipt neither grants authority nor attests test adequacy.
// expectedDigest must come from the caller's authenticated/private record, not
// the receipt's producer-controlled payload. The original receipt is unchanged,
// including failed attempts, command settings and source identity.
func AssessVerificationReceipt(r VerificationReceipt, expectedDigest string, target VerificationTarget) (VerificationApplicability, error) {
	result := VerificationApplicability{Historical: true, ExecutionID: r.ExecutionID, SourceCommitOID: r.SourceCommitOID}
	digest, err := r.Digest()
	if err != nil {
		return result, err
	}
	if !validVerificationDigest(expectedDigest) || digest != expectedDigest {
		return result, errors.New("verification receipt does not match protected provenance")
	}
	refuse := func(reason string) (VerificationApplicability, error) {
		result.Reason = reason
		return result, nil
	}
	if err := target.Inputs.validate(); err != nil || !validVerificationDigest(target.SettingsDigest) || !validVerificationOID(target.CandidateOID) || target.Entrypoint == "" {
		return refuse("current verification bindings are unavailable")
	}
	if r.Repository != target.Repository || r.PlanID != target.PlanID {
		return refuse("repository or plan identity changed")
	}
	if r.Outcome != "passed" || !r.CleanupResolved {
		return refuse("verification did not pass with resolved cleanup")
	}
	if r.Boundary != target.Boundary || r.Entrypoint != target.Entrypoint || r.SettingsDigest != target.SettingsDigest {
		return refuse("verification boundary, entrypoint or settings changed")
	}
	if r.Inputs.Selection.Policy != target.Inputs.Selection.Policy || !slices.Equal(r.Inputs.Selection.Paths, target.Inputs.Selection.Paths) {
		return refuse("verification input selection changed")
	}
	for _, check := range []struct{ name, prior, current string }{
		{"requirements", r.Inputs.Requirements, target.Inputs.Requirements},
		{"executable inputs", r.Inputs.Executable, target.Inputs.Executable},
		{"dependencies", r.Inputs.Dependencies, target.Inputs.Dependencies},
		{"configuration", r.Inputs.Configuration, target.Inputs.Configuration},
		{"environment", r.Inputs.Environment, target.Inputs.Environment},
		{"relevant base inputs", r.Inputs.Base, target.Inputs.Base},
	} {
		if check.prior != check.current {
			return refuse(check.name + " changed")
		}
	}
	if target.RequireCurrentCandidate && r.SourceCommitOID != target.CandidateOID {
		return refuse("policy requires a current-candidate check")
	}
	result.Applicable = true
	result.Reason = "recorded passing check has unchanged observed applicability inputs"
	return result, nil
}

func (r VerificationReceipt) validate() error {
	if r.Version != 1 || r.ExecutionID == "" || r.AttemptID == "" || r.Repository == "" || r.Entrypoint == "" || len(r.Command) == 0 {
		return errors.New("verification receipt lacks execution provenance")
	}
	if (r.PlanID == "") != (r.PlanRevision == "") {
		return errors.New("verification receipt plan identity is incomplete")
	}
	_, startOffset := r.StartedAt.Zone()
	_, finishOffset := r.FinishedAt.Zone()
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || startOffset != 0 || finishOffset != 0 {
		return errors.New("verification receipt requires its observed UTC invocation interval")
	}
	if (r.RunStartedAt == nil) != (r.RunFinishedAt == nil) || (r.RunStartedAt == nil) != (r.RunMilliseconds == nil) {
		return errors.New("verification receipt run interval and duration must be observed together")
	}
	if r.RunStartedAt != nil {
		_, startOffset := r.RunStartedAt.Zone()
		_, finishOffset := r.RunFinishedAt.Zone()
		if r.RunStartedAt.Before(r.StartedAt) || r.RunFinishedAt.Before(*r.RunStartedAt) || r.RunFinishedAt.After(r.FinishedAt) || startOffset != 0 || finishOffset != 0 {
			return errors.New("verification receipt run interval is outside its observed UTC invocation")
		}
	}
	if r.Outcome == "passed" && (r.RunStartedAt == nil || !r.CleanupResolved) {
		return errors.New("passing verification requires an observed check with resolved cleanup")
	}
	if r.ExitCode != nil && (r.RunStartedAt == nil || *r.ExitCode < 0 || r.Outcome == "passed" && *r.ExitCode != 0) {
		return errors.New("verification exit code contradicts its observed check")
	}
	if p := r.Preparation; p != nil {
		_, startOffset := p.StartedAt.Zone()
		_, finishOffset := p.FinishedAt.Zone()
		_, runOffset := p.RunFinishedAt.Zone()
		if len(p.Command) == 0 || strings.TrimSpace(p.Command[0]) == "" || p.StartedAt.Before(r.StartedAt) || p.RunFinishedAt.Before(p.StartedAt) || p.FinishedAt.Before(p.RunFinishedAt) || p.FinishedAt.After(r.FinishedAt) || startOffset != 0 || finishOffset != 0 || runOffset != 0 || p.RunMilliseconds < 0 || !validVerificationDigest(p.ReportDigest) {
			return errors.New("verification preparation lacks observed command, interval or report")
		}
		for _, arg := range p.Command {
			if strings.ContainsRune(arg, 0) {
				return errors.New("verification preparation command is invalid")
			}
		}
		if p.CleanupMilliseconds != nil && *p.CleanupMilliseconds < 0 {
			return errors.New("verification preparation cleanup timing is invalid")
		}
		if p.ExitCode != nil && (*p.ExitCode < 0 || p.Outcome == "passed" && *p.ExitCode != 0) {
			return errors.New("preparation exit code contradicts its outcome")
		}
		switch p.Outcome {
		case "passed", "failed", "timeout", "canceled", "cleanup_unresolved":
		default:
			return errors.New("verification preparation outcome is invalid")
		}
		if r.RunStartedAt != nil && (r.RunStartedAt.Before(p.FinishedAt) || p.Outcome != "passed" || !p.CleanupResolved) {
			return errors.New("verification check cannot precede successful resolved preparation")
		}
	}
	for _, arg := range r.Command {
		if strings.ContainsRune(arg, 0) {
			return errors.New("verification receipt command is invalid")
		}
	}
	if strings.TrimSpace(r.Command[0]) == "" || !validVerificationOID(r.SourceCommitOID) || !validVerificationOID(r.SourceTreeOID) || !validVerificationOID(r.SourceBaseOID) || !validVerificationDigest(r.SettingsDigest) || !validVerificationDigest(r.ReportDigest) {
		return errors.New("verification receipt lacks source, settings or report bindings")
	}
	if r.Boundary != VerificationFocused && r.Boundary != VerificationComplete {
		return errors.New("verification receipt boundary is invalid")
	}
	switch r.Outcome {
	case "passed", "failed", "canceled", "timeout", "cleanup_unresolved":
	default:
		return errors.New("verification receipt outcome is invalid")
	}
	for _, duration := range []*int64{r.WaitMilliseconds, r.RunMilliseconds, r.CleanupMilliseconds} {
		if duration != nil && *duration < 0 {
			return errors.New("verification receipt durations must be observed nonnegative values")
		}
	}
	return r.Inputs.validate()
}

func (inputs VerificationInputs) validate() error {
	if strings.TrimSpace(inputs.Selection.Policy) == "" || len(inputs.Selection.Paths) == 0 {
		return errors.New("verification input selection is unavailable")
	}
	seen := map[string]bool{}
	for _, name := range inputs.Selection.Paths {
		if name == "" || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || path.Clean(name) != name || strings.ContainsAny(name, "\\\x00") || seen[name] {
			return errors.New("verification input selection contains an invalid or repeated root")
		}
		seen[name] = true
	}
	for name, digest := range map[string]string{
		"requirements": inputs.Requirements, "executable": inputs.Executable, "dependencies": inputs.Dependencies,
		"configuration": inputs.Configuration, "environment": inputs.Environment, "base": inputs.Base,
	} {
		if !validVerificationDigest(digest) {
			return fmt.Errorf("verification %s digest is unavailable or invalid", name)
		}
	}
	return nil
}

func validVerificationDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validVerificationOID(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && (len(decoded) == 20 || len(decoded) == sha256.Size) && value == strings.ToLower(value)
}
