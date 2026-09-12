package metrics

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

const EventVersion = 1

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
	StagePlannerOutline     = "planner_outline"
	StagePlannerDetails     = "planner_details"
	StageReviewerAudit      = "reviewer_audit"
	StageReviewerVerify     = "reviewer_verification"
	StageResultValidate     = "result_validate"
	StageWorkspaceVerify    = "workspace_verify"
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
		StageWorkspaceVerify, StageProjectTransition, StagePublishPullRequest,
		StagePlannerApply:
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
	case "", "unknown", "transient_external", "capacity_exhausted", "timeout", "canceled",
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

// Usage contains only counters reported by a harness. Runner never estimates
// token usage or monetary cost when a harness does not expose those values.
type Usage struct {
	Available               bool                  `json:"available"`
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

type ModelUsage struct {
	InputTokens           int64    `json:"input_tokens,omitempty"`
	CacheReadInputTokens  int64    `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens int64    `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          int64    `json:"output_tokens,omitempty"`
	ReportedCostUSD       *float64 `json:"reported_cost_usd,omitempty"`
}

func ValidateUsage(usage Usage) error {
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

type Event struct {
	Version                     int             `json:"version"`
	Kind                        string          `json:"kind"`
	AttemptID                   string          `json:"attempt_id"`
	RunnerID                    string          `json:"runner_id"`
	ProjectOwner                string          `json:"project_owner"`
	ProjectNumber               int             `json:"project_number"`
	Repository                  string          `json:"repository,omitempty"`
	ItemID                      string          `json:"item_id,omitempty"`
	ItemTitle                   string          `json:"item_title"`
	Role                        string          `json:"role"`
	Harness                     string          `json:"harness"`
	Model                       string          `json:"model,omitempty"`
	Reasoning                   string          `json:"reasoning,omitempty"`
	Iteration                   int             `json:"iteration,omitempty"`
	StartedAt                   time.Time       `json:"started_at"`
	FinishedAt                  time.Time       `json:"finished_at,omitempty"`
	DurationMilliseconds        int64           `json:"duration_milliseconds,omitempty"`
	HarnessDurationMilliseconds int64           `json:"harness_duration_milliseconds,omitempty"`
	Outcome                     string          `json:"outcome,omitempty"`
	FailureClass                string          `json:"failure_class,omitempty"`
	FailureOperation            string          `json:"failure_operation,omitempty"`
	PublicationAttempts         int             `json:"publication_attempts,omitempty"`
	RetryDisposition            string          `json:"retry_disposition,omitempty"`
	RetryAfter                  string          `json:"retry_after,omitempty"`
	StageID                     string          `json:"stage_id,omitempty"`
	Stage                       string          `json:"stage,omitempty"`
	Summary                     string          `json:"summary,omitempty"`
	WorkDone                    []string        `json:"work_done,omitempty"`
	Verification                []string        `json:"verification,omitempty"`
	ReviewFindings              []ReviewFinding `json:"review_findings,omitempty"`
	CandidateOID                string          `json:"candidate_oid,omitempty"`
	PromptContexts              []PromptContext `json:"prompt_contexts,omitempty"`
	ResumedCheckpoint           bool            `json:"resumed_checkpoint,omitempty"`
	Usage                       Usage           `json:"usage"`
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
	for _, value := range values {
		if !validPromptContext(value) {
			return false
		}
	}
	return true
}

// ReviewFinding retains validated failed checks as local, untrusted evidence.
// These observations never become assignment instructions automatically.
type ReviewFinding struct {
	Area    string `json:"area"`
	Summary string `json:"summary"`
}

type Attempt struct {
	Event
	Completed bool    `json:"completed"`
	Stages    []Stage `json:"stages,omitempty"`
}

type Stage struct {
	StageID              string          `json:"stage_id"`
	Name                 string          `json:"name"`
	StartedAt            time.Time       `json:"started_at"`
	FinishedAt           time.Time       `json:"finished_at,omitempty"`
	DurationMilliseconds int64           `json:"duration_milliseconds,omitempty"`
	Outcome              string          `json:"outcome,omitempty"`
	FailureClass         string          `json:"failure_class,omitempty"`
	RetryDisposition     string          `json:"retry_disposition,omitempty"`
	Usage                Usage           `json:"usage"`
	PromptContexts       []PromptContext `json:"prompt_contexts,omitempty"`
	Completed            bool            `json:"completed"`
}

type Summary struct {
	Attempts                      int            `json:"attempts"`
	CompletedAttempts             int            `json:"completed_attempts"`
	UnfinishedAttempts            int            `json:"unfinished_attempts"`
	SucceededAttempts             int            `json:"succeeded_attempts"`
	BlockedAttempts               int            `json:"blocked_attempts"`
	HarnessInvocations            int            `json:"harness_invocations"`
	ResumedCheckpointAttempts     int            `json:"resumed_checkpoint_attempts"`
	HarnessDurationMilliseconds   int64          `json:"harness_duration_milliseconds"`
	RunnerDurationMilliseconds    int64          `json:"runner_duration_milliseconds"`
	Usage                         Usage          `json:"usage"`
	UsageCoveredAttempts          int            `json:"usage_covered_attempts"`
	CostCoveredAttempts           int            `json:"cost_covered_attempts"`
	StageCoveredAttempts          int            `json:"stage_covered_attempts"`
	RecoveredStageFailureAttempts int            `json:"recovered_stage_failure_attempts"`
	RecoveredPublicationAttempts  int            `json:"recovered_publication_attempts"`
	Stages                        []StageSummary `json:"stages,omitempty"`
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
	result.Attempts = len(attempts)
	for _, attempt := range attempts {
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
