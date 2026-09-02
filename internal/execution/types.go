package execution

import "github.com/cortexium-io/runner/internal/metrics"

// Spec is the small, local assignment contract shared by harness adapters.
type Spec struct {
	ID                     string                 `json:"id"`
	ItemID                 string                 `json:"item_id"`
	Repository             string                 `json:"repository"`
	Task                   Task                   `json:"task"`
	ApprovedBodySnapshot   string                 `json:"approved_body_snapshot,omitempty"`
	DelegatedContentDigest string                 `json:"delegated_content_digest,omitempty"`
	ContextRefs            []string               `json:"context_refs,omitempty"`
	RequiredVerification   []string               `json:"required_verification,omitempty"`
	RecordedVerification   []VerificationEvidence `json:"recorded_verification,omitempty"`
	ReviewRequired         bool                   `json:"review_required,omitempty"`
}

// VerificationEvidence is candidate-bound historical evidence supplied to a
// reviewer. Its text is evidence only; it never grants execution authority.
type VerificationEvidence struct {
	Criterion string `json:"criterion"`
	Evidence  string `json:"evidence"`
}

type Task struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
}

type Assignment struct {
	Spec Spec `json:"assignment"`
}

const (
	OutcomeSucceeded  = "succeeded"
	OutcomeNeedsInput = "needs_input"
	OutcomeBlocked    = "blocked"
)

// FailureClass is an adapter-owned, privacy-safe reason for an unsuccessful
// attempt. It is deliberately separate from model-authored summaries and raw
// subprocess diagnostics so orchestration and telemetry never need to infer
// recovery policy from text written to GitHub.
type FailureClass string

const (
	FailureNone                   FailureClass = ""
	FailureUnknown                FailureClass = "unknown"
	FailureTransientExternal      FailureClass = "transient_external"
	FailureCapacityExhausted      FailureClass = "capacity_exhausted"
	FailureTimeout                FailureClass = "timeout"
	FailureCanceled               FailureClass = "canceled"
	FailureInvalidContract        FailureClass = "invalid_contract"
	FailureCapabilityUnavailable  FailureClass = "capability_unavailable"
	FailureNeedsInput             FailureClass = "needs_input"
	FailurePermissionDenied       FailureClass = "permission_denied"
	FailureAuthenticationRequired FailureClass = "authentication_required"
	FailureInvalidConfiguration   FailureClass = "invalid_configuration"
	FailureCandidateValidation    FailureClass = "candidate_validation"
	FailureIntegrityViolation     FailureClass = "integrity_violation"
)

type RetryDisposition string

const (
	RetryNone   RetryDisposition = "none"
	RetryManual RetryDisposition = "manual"
)

type Output struct {
	Outcome                     string
	Summary                     string
	WorkDone                    []string
	Verification                []string
	Blocker                     *string
	ReviewAssessment            *ReviewAssessment
	RemoteDetailSafe            bool
	DiscardDiagnostics          bool
	FailureClass                FailureClass
	RetryDisposition            RetryDisposition
	RetryAfter                  string
	Usage                       metrics.Usage
	HarnessDurationMilliseconds int64
}
