package metrics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

const EventVersion = 1

const (
	maxApprovedSnapshotBytes = 256 * 1024
	maxEvidenceEntries       = 1000
	maxEvidenceTextBytes     = 8 * 1024
	maxIdentityTextBytes     = 1024
)

const (
	EventStarted        = "started"
	EventCompleted      = "completed"
	EventStageStarted   = "stage_started"
	EventStageCompleted = "stage_completed"
)

const (
	StageWorkspacePrepare   = "workspace_prepare"
	StageRepositoryPrepare  = "repository_prepare"
	StageHarnessRun         = "harness_run"
	StageHarnessCleanup     = "harness_cleanup"
	StagePlannerOutline     = "planner_outline"
	StagePlannerDetails     = "planner_details"
	StageReviewerAudit      = "reviewer_audit"
	StageReviewerVerify     = "reviewer_verification"
	StageResultValidate     = "result_validate"
	StageWorkspaceVerify    = "workspace_verify"
	StageCandidateConstruct = "candidate_construct"
	StageProjectTransition  = "project_transition"
	StagePublishPullRequest = "publish_pull_request"
	StagePlannerApply       = "planner_apply"
)

const (
	StageOutcomeSucceeded = "succeeded"
	StageOutcomeFailed    = "failed"
	StageOutcomeBlocked   = "blocked"
)

func validStageName(name string) bool {
	switch name {
	case StageWorkspacePrepare, StageRepositoryPrepare, StageHarnessRun, StagePlannerOutline,
		StagePlannerDetails, StageReviewerAudit, StageReviewerVerify, StageResultValidate,
		StageWorkspaceVerify, StageCandidateConstruct, StageProjectTransition, StagePublishPullRequest,
		StagePlannerApply, StageHarnessCleanup:
		return true
	default:
		return false
	}
}

func validStageOutcome(outcome string) bool {
	switch outcome {
	case StageOutcomeSucceeded, StageOutcomeFailed, StageOutcomeBlocked:
		return true
	default:
		return false
	}
}

// These allowlists mirror execution.FailureClass and RetryDisposition without
// importing execution back into metrics. They keep arbitrary diagnostic text
// out of the durable enum fields even if a future caller bypasses AttemptTrace.
func validFailureClass(class string) bool {
	switch class {
	case "", "unknown", "transient_external", "capacity_exhausted", "timeout", "canceled", "cleanup_unresolved",
		"invalid_contract", "capability_unavailable", "review_incomplete", "browser_startup", "needs_input", "agent_blocked", "permission_denied",
		"authentication_required", "invalid_configuration", "candidate_validation", "integrity_violation", "integrity_unverified":
		return true
	default:
		return false
	}
}

func validRetryDisposition(disposition string) bool {
	switch disposition {
	case "", "none", "manual", "automatic":
		return true
	default:
		return false
	}
}

func validFailureOperation(operation string) bool {
	switch operation {
	case "", "publication_push_candidate", "publication_refresh_authority",
		"publication_find_pull_request", "publication_inspect_pull_request",
		"publication_create_pull_request", "publication_validate_pull_request":
		return true
	default:
		return false
	}
}

func validReviewVerdict(event Event) bool {
	if event.ReviewVerdict == "" {
		return true
	}
	if event.Kind != EventCompleted {
		return false
	}
	switch event.ReviewVerdict {
	case "accept", "needs_changes", "blocked":
		return true
	default:
		return false
	}
}

// Usage contains only counters reported by a harness. Runner never estimates
// token usage or monetary cost when a harness does not expose those values.
type Usage struct {
	Available               bool                  `json:"available"`
	Coverage                string                `json:"coverage,omitempty"`
	InputTokens             int64                 `json:"input_tokens,omitempty"`
	CacheReadInputTokens    int64                 `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens   int64                 `json:"cache_write_input_tokens,omitempty"`
	OutputTokens            int64                 `json:"output_tokens,omitempty"`
	ReasoningOutputTokens   int64                 `json:"reasoning_output_tokens,omitempty"`
	APIDurationMilliseconds int64                 `json:"api_duration_milliseconds,omitempty"`
	Turns                   int64                 `json:"turns,omitempty"`
	ReportedCostUSD         *float64              `json:"reported_cost_usd,omitempty"`
	Models                  map[string]ModelUsage `json:"models,omitempty"`
}

const (
	UsageComplete    = "complete"
	UsagePartial     = "partial"
	UsageUnavailable = "unavailable"
	UsageUnknown     = "unknown"
)

// CoverageStatus does not invent completeness for historical counters.
func (u Usage) CoverageStatus() string {
	if u.Coverage != "" {
		return u.Coverage
	}
	if u.Reported() {
		return UsageUnknown
	}
	return UsageUnavailable
}

// Reported includes independently reported cost even if token counters are
// unavailable. Available continues to mean that token counters were supplied.
func (u Usage) Reported() bool { return u.Available || u.ReportedCostUSD != nil || len(u.Models) > 0 }

type ModelUsage struct {
	InputTokens           int64    `json:"input_tokens,omitempty"`
	CacheReadInputTokens  int64    `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens int64    `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          int64    `json:"output_tokens,omitempty"`
	ReportedCostUSD       *float64 `json:"reported_cost_usd,omitempty"`
}

func ValidateUsage(usage Usage) error {
	switch usage.Coverage {
	case "", UsageUnknown:
	case UsageComplete, UsagePartial:
		if !usage.Reported() {
			return errors.New("reported usage coverage requires counters")
		}
	case UsageUnavailable:
		if usage.Reported() {
			return errors.New("unavailable usage cannot have available counters")
		}
	default:
		return errors.New("invalid usage coverage")
	}
	if usage.InputTokens < 0 || usage.CacheReadInputTokens < 0 || usage.CacheWriteInputTokens < 0 ||
		usage.OutputTokens < 0 || usage.ReasoningOutputTokens < 0 || usage.APIDurationMilliseconds < 0 || usage.Turns < 0 {
		return errors.New("usage counters cannot be negative")
	}
	if invalidCost(usage.ReportedCostUSD) {
		return errors.New("reported usage cost must be finite and non-negative")
	}
	for _, model := range usage.Models {
		if model.InputTokens < 0 || model.CacheReadInputTokens < 0 || model.CacheWriteInputTokens < 0 || model.OutputTokens < 0 {
			return errors.New("model usage counters cannot be negative")
		}
		if invalidCost(model.ReportedCostUSD) {
			return errors.New("reported model cost must be finite and non-negative")
		}
	}
	return nil
}

func invalidCost(cost *float64) bool {
	return cost != nil && (*cost < 0 || math.IsNaN(*cost) || math.IsInf(*cost, 0))
}

func (u Usage) Add(other Usage) Usage {
	// A zero value is an accumulator identity. An explicit unavailable invocation
	// is not: adding its missing counters makes a reported total partial.
	left, right := u.CoverageStatus(), other.CoverageStatus()
	if !u.Reported() && u.Coverage == "" {
		u.Coverage = other.Coverage
	} else if !other.Reported() && other.Coverage == "" {
	} else if (u.Reported() || other.Reported()) && (left == UsagePartial || right == UsagePartial || left == UsageUnavailable || right == UsageUnavailable) {
		u.Coverage = UsagePartial
	} else if u.Available != other.Available || (u.ReportedCostUSD == nil) != (other.ReportedCostUSD == nil) {
		// A cost-only invocation must not make missing tokens look like zero
		// when combined with an invocation that does report tokens (or vice versa).
		u.Coverage = UsagePartial
	} else if left == UsageUnknown || right == UsageUnknown {
		u.Coverage = UsageUnknown
	} else if left == UsageComplete && right == UsageComplete {
		u.Coverage = UsageComplete
	} else {
		u.Coverage = UsageUnavailable
	}
	u.Available = u.Available || other.Available
	u.InputTokens += other.InputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.CacheWriteInputTokens += other.CacheWriteInputTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningOutputTokens += other.ReasoningOutputTokens
	u.APIDurationMilliseconds += other.APIDurationMilliseconds
	u.Turns += other.Turns
	if other.ReportedCostUSD != nil {
		value := *other.ReportedCostUSD
		if u.ReportedCostUSD != nil {
			value += *u.ReportedCostUSD
		}
		u.ReportedCostUSD = &value
	}
	if len(other.Models) > 0 {
		if u.Models == nil {
			u.Models = map[string]ModelUsage{}
		}
		for model, addition := range other.Models {
			current := u.Models[model]
			current.InputTokens += addition.InputTokens
			current.CacheReadInputTokens += addition.CacheReadInputTokens
			current.CacheWriteInputTokens += addition.CacheWriteInputTokens
			current.OutputTokens += addition.OutputTokens
			if addition.ReportedCostUSD != nil {
				value := *addition.ReportedCostUSD
				if current.ReportedCostUSD != nil {
					value += *current.ReportedCostUSD
				}
				current.ReportedCostUSD = &value
			}
			u.Models[model] = current
		}
	}
	return u
}

// Event records an attempt or stage. ReviewVerdict is a validated review
// decision, not the attempt's publication outcome or an authorization. Empty
// means no validated verdict was recorded.
type Event struct {
	Version                     int              `json:"version"`
	Kind                        string           `json:"kind"`
	AttemptID                   string           `json:"attempt_id"`
	RunnerID                    string           `json:"runner_id"`
	RunContext                  *RunContext      `json:"run_context,omitempty"`
	ProjectOwner                string           `json:"project_owner"`
	ProjectNumber               int              `json:"project_number"`
	Repository                  string           `json:"repository,omitempty"`
	ItemID                      string           `json:"item_id,omitempty"`
	ItemTitle                   string           `json:"item_title"`
	Role                        string           `json:"role"`
	Harness                     string           `json:"harness"`
	Model                       string           `json:"model,omitempty"`
	Reasoning                   string           `json:"reasoning,omitempty"`
	Iteration                   int              `json:"iteration,omitempty"`
	StartedAt                   time.Time        `json:"started_at"`
	FinishedAt                  time.Time        `json:"finished_at,omitempty"`
	DurationMilliseconds        int64            `json:"duration_milliseconds,omitempty"`
	HarnessDurationMilliseconds int64            `json:"harness_duration_milliseconds,omitempty"`
	Outcome                     string           `json:"outcome,omitempty"`
	FailureClass                string           `json:"failure_class,omitempty"`
	FailureOperation            string           `json:"failure_operation,omitempty"`
	PublicationAttempts         int              `json:"publication_attempts,omitempty"`
	RetryDisposition            string           `json:"retry_disposition,omitempty"`
	RetryAfter                  string           `json:"retry_after,omitempty"`
	StageID                     string           `json:"stage_id,omitempty"`
	Stage                       string           `json:"stage,omitempty"`
	Summary                     string           `json:"summary,omitempty"`
	ModelReportedSummary        string           `json:"model_reported_summary,omitempty"`
	WorkDone                    []string         `json:"work_done,omitempty"`
	Verification                []string         `json:"verification,omitempty"`
	ReviewVerdict               string           `json:"review_verdict,omitempty"`
	ReviewFindings              []ReviewFinding  `json:"review_findings,omitempty"`
	ReviewDetails               []ReviewDetail   `json:"model_reported_review_details,omitempty"`
	ModelReportComplete         *bool            `json:"model_report_complete,omitempty"`
	CandidateOID                string           `json:"candidate_oid,omitempty"`
	RunnerObservation           string           `json:"runner_observed_outcome,omitempty"`
	ApprovedRequest             *ApprovedRequest `json:"runner_observed_approved_request,omitempty"`
	Lineage                     *ObservedLineage `json:"runner_observed_lineage,omitempty"`
	PromptContexts              []PromptContext  `json:"prompt_contexts,omitempty"`
	ResumedCheckpoint           bool             `json:"resumed_checkpoint,omitempty"`
	Usage                       Usage            `json:"usage"`
	HarnessActivity             *HarnessActivity `json:"harness_activity,omitempty"`
}

// ApprovedRequest retains the exact canonical content covered by the observed
// delegated digest. The snapshot includes the approved body and execution-defining
// metadata, but no action assertion. It is private evidence, never authority.
type ApprovedRequest struct {
	DelegatedContentDigest string `json:"delegated_content_digest"`
	Snapshot               string `json:"snapshot"`
}

// NewApprovedRequest preserves the supplied identity; validation refuses a
// snapshot whose bytes do not match it rather than manufacturing a new identity.
func NewApprovedRequest(delegatedContentDigest, snapshot string) *ApprovedRequest {
	return &ApprovedRequest{
		DelegatedContentDigest: strings.TrimSpace(delegatedContentDigest),
		Snapshot:               snapshot,
	}
}

// ObjectIdentity is a commit/tree pair observed by Runner. TreeOID is optional
// because not every provider boundary exposes it; absent values stay absent.
type ObjectIdentity struct {
	CommitOID string `json:"commit_oid,omitempty"`
	TreeOID   string `json:"tree_oid,omitempty"`
}

// ObservedLineage contains only identities obtained at Runner-controlled
// boundaries. Model-authored summaries and verification claims live in the
// existing report fields and must not be copied into this structure.
type ObservedLineage struct {
	Repository         string         `json:"repository,omitempty"`
	Branch             string         `json:"branch,omitempty"`
	Base               ObjectIdentity `json:"base,omitempty"`
	Candidate          ObjectIdentity `json:"candidate,omitempty"`
	EvidenceCandidate  ObjectIdentity `json:"evidence_candidate,omitempty"`
	ReviewedCandidate  ObjectIdentity `json:"reviewed_candidate,omitempty"`
	RebasedCandidate   ObjectIdentity `json:"rebased_candidate,omitempty"`
	PublishedCandidate ObjectIdentity `json:"published_candidate,omitempty"`
	PullRequestURL     string         `json:"pull_request_url,omitempty"`
	PullRequestNumber  int            `json:"pull_request_number,omitempty"`
	Merge              ObjectIdentity `json:"merge,omitempty"`
}

// RunContext identifies the CLI build and loaded operator configuration when
// the event was recorded, not when history is exported. It grants no authority.
// BundledSkillsVersion identifies the bundle, not necessarily the installed
// role guidance; PromptContext records the guidance actually supplied.
type RunContext struct {
	RunnerVersion        string `json:"runner_version"`
	BundledSkillsVersion string `json:"bundled_skills_version"`
	ConfigDigest         string `json:"config_digest"`
}

func validRunContext(value *RunContext) bool {
	if value == nil {
		return true // Unrecorded identity is unknown, never inferred from today's config.
	}
	if len(value.RunnerVersion) == 0 || len(value.RunnerVersion) > 128 ||
		len(value.BundledSkillsVersion) == 0 || len(value.BundledSkillsVersion) > 128 ||
		!strings.HasPrefix(value.ConfigDigest, "sha256:") {
		return false
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(value.ConfigDigest, "sha256:"))
	return err == nil && len(digest) == sha256.Size
}

// PromptContext fingerprints Runner-owned static guidance only. It is not the
// full provider request, repository instructions, or a provider cache key.
type PromptContext struct {
	Layout         string `json:"layout"`
	GuidanceDigest string `json:"guidance_digest"`
}

func validPromptContext(value PromptContext) bool {
	if len(value.Layout) == 0 || len(value.Layout) > 64 || !strings.HasPrefix(value.GuidanceDigest, "sha256:") {
		return false
	}
	for _, r := range value.Layout {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	digest, err := hex.DecodeString(strings.TrimPrefix(value.GuidanceDigest, "sha256:"))
	return err == nil && len(digest) == 32
}

func validPromptContexts(values []PromptContext) bool {
	if len(values) > maxEvidenceEntries {
		return false
	}
	for _, value := range values {
		if !validPromptContext(value) {
			return false
		}
	}
	return true
}

func validRetainedAttemptEvidence(event Event) bool {
	if event.Kind != EventCompleted && (event.Lineage != nil || event.RunnerObservation != "" ||
		event.Summary != "" || event.ModelReportedSummary != "" || len(event.WorkDone) != 0 || len(event.Verification) != 0 || event.ReviewVerdict != "" ||
		len(event.ReviewFindings) != 0 || len(event.ReviewDetails) != 0 || event.ModelReportComplete != nil) {
		return false
	}
	if event.Kind != EventStarted && event.Kind != EventCompleted && event.ApprovedRequest != nil {
		return false
	}
	if !validBoundedEvidence(event.WorkDone) || !validBoundedEvidence(event.Verification) ||
		len(event.ReviewFindings) > maxEvidenceEntries || len(event.ReviewDetails) > maxEvidenceEntries ||
		len(event.Summary) > maxEvidenceTextBytes || len(event.ModelReportedSummary) > maxEvidenceTextBytes || len(event.RunnerObservation) > maxEvidenceTextBytes {
		return false
	}
	reviewEvidenceEntries := 0
	for _, detail := range event.ReviewDetails {
		reviewEvidenceEntries += len(detail.Evidence)
		if !validReviewArea(detail.Area) || !validReviewStatus(detail.Status) || len(detail.Name) > maxIdentityTextBytes ||
			strings.TrimSpace(detail.Summary) == "" || len(detail.Summary) > maxEvidenceTextBytes || !validBoundedEvidence(detail.Evidence) || reviewEvidenceEntries > maxEvidenceEntries {
			return false
		}
	}
	for _, finding := range event.ReviewFindings {
		if strings.TrimSpace(finding.Area) == "" || strings.TrimSpace(finding.Summary) == "" ||
			len(finding.Area) > maxIdentityTextBytes || len(finding.Summary) > maxEvidenceTextBytes {
			return false
		}
	}
	if event.CandidateOID != "" && !validObjectID(event.CandidateOID) {
		return false
	}
	if event.ApprovedRequest != nil {
		approval := event.ApprovedRequest
		digest := sha256.Sum256([]byte(approval.Snapshot))
		if !validDelegatedContentDigest(approval.DelegatedContentDigest) ||
			approval.DelegatedContentDigest != "v1:"+hex.EncodeToString(digest[:]) ||
			!json.Valid([]byte(approval.Snapshot)) || len(approval.Snapshot) > maxApprovedSnapshotBytes {
			return false
		}
	}
	if event.Lineage == nil {
		return true
	}
	if event.ApprovedRequest == nil {
		return false
	}
	lineage := event.Lineage
	if len(lineage.Repository) > maxIdentityTextBytes || len(lineage.Branch) > maxIdentityTextBytes ||
		len(lineage.PullRequestURL) > maxIdentityTextBytes || lineage.PullRequestNumber < 0 ||
		!validObjectIdentity(lineage.Base) || !validObjectIdentity(lineage.Candidate) ||
		!validObjectIdentity(lineage.EvidenceCandidate) || !validObjectIdentity(lineage.ReviewedCandidate) ||
		!validObjectIdentity(lineage.RebasedCandidate) || !validObjectIdentity(lineage.PublishedCandidate) || !validObjectIdentity(lineage.Merge) {
		return false
	}
	if (lineage.Repository != "" && strings.TrimSpace(lineage.Repository) == "") || (lineage.Branch != "" && strings.TrimSpace(lineage.Branch) == "") ||
		(lineage.PullRequestURL == "") != (lineage.PullRequestNumber == 0) {
		return false
	}
	if lineage.PullRequestURL != "" && !strings.HasPrefix(lineage.PullRequestURL, "https://github.com/") {
		return false
	}
	if event.CandidateOID != "" && lineage.Candidate.CommitOID != "" && event.CandidateOID != lineage.Candidate.CommitOID {
		return false
	}
	if lineage.EvidenceCandidate.CommitOID != "" && lineage.EvidenceCandidate != lineage.Candidate {
		return false
	}
	if lineage.ReviewedCandidate.CommitOID != "" && lineage.Candidate.CommitOID == "" {
		return false
	}
	return lineage.Repository != "" || lineage.Branch != "" || lineage.PullRequestURL != "" || lineage.PullRequestNumber != 0 ||
		lineage.Base.CommitOID != "" || lineage.Candidate.CommitOID != "" || lineage.EvidenceCandidate.CommitOID != "" ||
		lineage.ReviewedCandidate.CommitOID != "" || lineage.RebasedCandidate.CommitOID != "" || lineage.PublishedCandidate.CommitOID != "" || lineage.Merge.CommitOID != ""
}

func validReviewArea(value string) bool {
	switch value {
	case "acceptance", "repository_rules", "maintainability":
		return true
	default:
		return false
	}
}

func validReviewStatus(value string) bool {
	switch value {
	case "passed", "failed", "blocked":
		return true
	default:
		return false
	}
}

func validBoundedEvidence(values []string) bool {
	if len(values) > maxEvidenceEntries {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxEvidenceTextBytes {
			return false
		}
	}
	return true
}

func validDelegatedContentDigest(value string) bool {
	if !strings.HasPrefix(value, "v1:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "v1:"))
	return err == nil && len(decoded) == 32
}

func validObjectIdentity(value ObjectIdentity) bool {
	if value.TreeOID != "" && value.CommitOID == "" {
		return false
	}
	return (value.CommitOID == "" || validObjectID(value.CommitOID)) && (value.TreeOID == "" || validObjectID(value.TreeOID))
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && (len(decoded) == 20 || len(decoded) == 32)
}

// ReviewFinding retains validated failed checks as local, untrusted evidence.
// These observations never become assignment instructions automatically.
type ReviewFinding struct {
	Area    string `json:"area"`
	Summary string `json:"summary"`
}

// ReviewDetail retains one bounded structured model-reported QA result.
// Runner validates its shape and status, but its text is not an observation of
// command execution and never grants workflow authority.
type ReviewDetail struct {
	Area     string   `json:"area"`
	Name     string   `json:"name,omitempty"`
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence,omitempty"`
}

type Attempt struct {
	Event
	Completed bool    `json:"completed"`
	Stages    []Stage `json:"stages,omitempty"`
}

// IsRunnerObservation identifies deterministic reconciliation records, which
// belong in item history but are not harness attempts or admission spending.
func (e Event) IsRunnerObservation() bool {
	return e.Kind == EventCompleted && e.Role == "runner" && e.Harness == "runner" && e.Model == "" && e.Reasoning == ""
}

type Stage struct {
	HarnessActivity      *HarnessActivity `json:"harness_activity,omitempty"`
	StageID              string           `json:"stage_id"`
	Name                 string           `json:"name"`
	StartedAt            time.Time        `json:"started_at"`
	FinishedAt           time.Time        `json:"finished_at,omitempty"`
	DurationMilliseconds int64            `json:"duration_milliseconds,omitempty"`
	Outcome              string           `json:"outcome,omitempty"`
	FailureClass         string           `json:"failure_class,omitempty"`
	RetryDisposition     string           `json:"retry_disposition,omitempty"`
	Usage                Usage            `json:"usage"`
	PromptContexts       []PromptContext  `json:"prompt_contexts,omitempty"`
	Completed            bool             `json:"completed"`
}

type Summary struct {
	Attempts                       int            `json:"attempts"`
	CompletedAttempts              int            `json:"completed_attempts"`
	UnfinishedAttempts             int            `json:"unfinished_attempts"`
	SucceededAttempts              int            `json:"succeeded_attempts"`
	BlockedAttempts                int            `json:"blocked_attempts"`
	HarnessInvocations             int            `json:"harness_invocations"`
	ResumedCheckpointAttempts      int            `json:"resumed_checkpoint_attempts"`
	HarnessDurationMilliseconds    int64          `json:"harness_duration_milliseconds"`
	RunnerDurationMilliseconds     int64          `json:"runner_duration_milliseconds"`
	Usage                          Usage          `json:"usage"`
	UsageCoveredAttempts           int            `json:"usage_covered_attempts"`
	CompleteUsageAttempts          int            `json:"complete_usage_attempts"`
	PartialUsageAttempts           int            `json:"partial_usage_attempts"`
	CostCoveredAttempts            int            `json:"cost_covered_attempts"`
	StageCoveredAttempts           int            `json:"stage_covered_attempts"`
	RecoveredStageFailureAttempts  int            `json:"recovered_stage_failure_attempts"`
	RecoveredPublicationAttempts   int            `json:"recovered_publication_attempts"`
	ReviewVerdictCoveredAttempts   int            `json:"review_verdict_covered_attempts"`
	ReviewAcceptedAttempts         int            `json:"review_accepted_attempts"`
	ReviewChangesRequestedAttempts int            `json:"review_changes_requested_attempts"`
	ReviewBlockedAttempts          int            `json:"review_blocked_attempts"`
	Stages                         []StageSummary `json:"stages,omitempty"`
}

// StageSummary aggregates recorded stage intervals, not individual agent tool
// calls. Stages may nest or overlap, so their durations must not be added to
// attempt duration or interpreted as wall-clock time through integration.
type StageSummary struct {
	Name                 string `json:"name"`
	Runs                 int    `json:"runs"`
	Completed            int    `json:"completed"`
	Failed               int    `json:"failed"`
	Blocked              int    `json:"blocked"`
	DurationMilliseconds int64  `json:"duration_milliseconds"`
	Usage                Usage  `json:"usage"`
	UsageCoveredStages   int    `json:"usage_covered_stages"`
	CostCoveredStages    int    `json:"cost_covered_stages"`
}

func Summarize(attempts []Attempt) Summary {
	var result Summary
	stages := map[string]StageSummary{}
	for _, attempt := range attempts {
		if attempt.IsRunnerObservation() {
			continue
		}
		result.Attempts++
		if len(attempt.Stages) > 0 {
			result.StageCoveredAttempts++
		}
		failedStage := false
		for _, stage := range attempt.Stages {
			group := stages[stage.Name]
			group.Name = stage.Name
			group.Runs++
			if stage.Completed {
				group.Completed++
				group.DurationMilliseconds += stage.DurationMilliseconds
				switch stage.Outcome {
				case StageOutcomeFailed:
					group.Failed++
					failedStage = true
				case StageOutcomeBlocked:
					group.Blocked++
					failedStage = true
				}
				group.Usage = group.Usage.Add(stage.Usage)
				if stage.Usage.Available {
					group.UsageCoveredStages++
				}
				if stage.Usage.ReportedCostUSD != nil {
					group.CostCoveredStages++
				}
			}
			stages[stage.Name] = group
			if stage.Completed && isHarnessStage(stage.Name) {
				result.HarnessInvocations++
			}
		}
		if !attempt.Completed {
			result.UnfinishedAttempts++
			continue
		}
		result.CompletedAttempts++
		switch attempt.ReviewVerdict {
		case "accept":
			result.ReviewAcceptedAttempts++
			result.ReviewVerdictCoveredAttempts++
		case "needs_changes":
			result.ReviewChangesRequestedAttempts++
			result.ReviewVerdictCoveredAttempts++
		case "blocked":
			result.ReviewBlockedAttempts++
			result.ReviewVerdictCoveredAttempts++
		}
		if attempt.ResumedCheckpoint {
			result.ResumedCheckpointAttempts++
		}
		switch attempt.Outcome {
		case "succeeded":
			result.SucceededAttempts++
			if failedStage {
				result.RecoveredStageFailureAttempts++
			}
			if attempt.PublicationAttempts > 1 {
				result.RecoveredPublicationAttempts++
			}
		case "blocked", "needs_input":
			result.BlockedAttempts++
		}
		result.HarnessDurationMilliseconds += attempt.HarnessDurationMilliseconds
		overhead := attempt.DurationMilliseconds - attempt.HarnessDurationMilliseconds
		if overhead > 0 {
			result.RunnerDurationMilliseconds += overhead
		}
		if attempt.Usage.Available {
			result.UsageCoveredAttempts++
			switch attempt.Usage.CoverageStatus() {
			case UsageComplete:
				result.CompleteUsageAttempts++
			case UsagePartial:
				result.PartialUsageAttempts++
			}
		}
		if attempt.Usage.ReportedCostUSD != nil {
			result.CostCoveredAttempts++
		}
		result.Usage = result.Usage.Add(attempt.Usage)
	}
	for _, stage := range stages {
		result.Stages = append(result.Stages, stage)
	}
	sort.Slice(result.Stages, func(i, j int) bool { return result.Stages[i].Name < result.Stages[j].Name })
	return result
}

func isHarnessStage(name string) bool {
	switch name {
	case StageHarnessRun, StagePlannerOutline, StagePlannerDetails, StageReviewerAudit, StageReviewerVerify:
		return true
	default:
		return false
	}
}

func NewAttemptID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err == nil {
		return "att_" + hex.EncodeToString(buffer)
	}
	return "att_" + time.Now().UTC().Format("20060102T150405.000000000")
}

func NewStageID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err == nil {
		return "stg_" + hex.EncodeToString(buffer)
	}
	return "stg_" + time.Now().UTC().Format("20060102T150405.000000000")
}

func SortNewest(attempts []Attempt) {
	sort.SliceStable(attempts, func(i, j int) bool {
		return attempts[i].StartedAt.After(attempts[j].StartedAt)
	})
}

func SortStages(stages []Stage) {
	sort.SliceStable(stages, func(i, j int) bool {
		return stages[i].StartedAt.Before(stages[j].StartedAt)
	})
}
