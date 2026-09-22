package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
)

type evalSettings struct {
	MaxAttempts   int
	Candidate     string
	Run           int
	ArtifactPath  string
	CaseTimeout   time.Duration
	AggregateTime time.Duration
	MaxTokens     int64
	MaxCostUSD    *float64
	CodexModel    string
	ClaudeModel   string
	PiModel       string
	Reasoning     string
	PiHostAccess  bool
}

type evalCaseResult struct {
	// Only the comparison worker adapter can establish usable observed evidence;
	// an admission-stop diagnostic is never evidence of completeness.
	ComparisonUsable            bool
	ReviewAssessment            *execution.ReviewAssessment
	AdmissionStop               string
	Stages                      []metrics.Event
	PromptContexts              []metrics.PromptContext
	FixtureTestDurationMS       int64
	ExpectedVerdict             string
	ObservedVerdict             string
	ReviewJudgment              string
	Outcome                     string
	FailureClass                string
	RetryDisposition            string
	RetryAfter                  string
	FailureStage                string
	HarnessDurationMilliseconds int64
	Usage                       metrics.Usage
	Err                         error
}

type evalCaseRecord struct {
	AdmissionStop         string                  `json:"admission_stop,omitempty"`
	PromptContexts        []metrics.PromptContext `json:"prompt_contexts,omitempty"`
	FixtureTestDurationMS int64                   `json:"fixture_test_duration_ms,omitempty"`
	HarnessDurationMS     int64                   `json:"harness_duration_ms,omitempty"`
	ExpectedVerdict       string                  `json:"expected_verdict,omitempty"`
	ObservedVerdict       string                  `json:"observed_verdict,omitempty"`
	ReviewJudgment        string                  `json:"review_judgment,omitempty"`
	Event                 string                  `json:"event"`
	Candidate             string                  `json:"candidate"`
	Run                   int                     `json:"run"`
	Harness               string                  `json:"harness"`
	Role                  string                  `json:"role"`
	Case                  string                  `json:"case"`
	Outcome               string                  `json:"outcome,omitempty"`
	FailureClass          string                  `json:"failure_class,omitempty"`
	RetryDisposition      string                  `json:"retry_disposition,omitempty"`
	RetryAfter            string                  `json:"retry_after,omitempty"`
	FailureStage          string                  `json:"failure_stage,omitempty"`
	DurationMS            int64                   `json:"duration_ms,omitempty"`
	Usage                 metrics.Usage           `json:"usage,omitempty"`
}

type evalSummaryRecord struct {
	AdmissionStop     string          `json:"admission_stop,omitempty"`
	ReviewerJudgments map[string]int  `json:"reviewer_judgments,omitempty"`
	Event             string          `json:"event"`
	Candidate         string          `json:"candidate"`
	Run               int             `json:"run"`
	Passed            bool            `json:"passed"`
	Summary           metrics.Summary `json:"summary"`
}

type evalCoordinator struct {
	stopReason        string
	recordErr         error
	reviewerJudgments map[string]int
	settings          evalSettings
	started           time.Time
	attempts          []metrics.Attempt
	output            io.Writer
	artifact          *os.File
}

func evalSettingsFromEnvironment() (evalSettings, error) {
	settings := evalSettings{
		Candidate:    strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_CANDIDATE")),
		ArtifactPath: strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_ARTIFACT")),
		CodexModel:   strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_CODEX_MODEL")),
		ClaudeModel:  strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_CLAUDE_MODEL")),
		PiModel:      strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_PI_MODEL")),
		Reasoning:    strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_REASONING")),
	}
	if settings.Reasoning == "" {
		settings.Reasoning = "high"
	}
	var err error
	if settings.Run, err = positiveEnvInt("CORTEXIUM_RUNNER_EVAL_RUN"); err != nil || settings.Run > 2 {
		return evalSettings{}, errors.New("CORTEXIUM_RUNNER_EVAL_RUN must be 1 or 2")
	}
	caseSeconds, err := positiveEnvInt("CORTEXIUM_RUNNER_EVAL_CASE_TIMEOUT_SECONDS")
	if err != nil {
		return evalSettings{}, err
	}
	aggregateSeconds, err := positiveEnvInt("CORTEXIUM_RUNNER_EVAL_MAX_SECONDS")
	if err != nil {
		return evalSettings{}, err
	}
	settings.CaseTimeout = time.Duration(caseSeconds) * time.Second
	settings.AggregateTime = time.Duration(aggregateSeconds) * time.Second
	if settings.Candidate == "" || settings.ArtifactPath == "" {
		return evalSettings{}, errors.New("live evaluation requires candidate and artifact path")
	}
	for _, harness := range compactEvalHarnesses(os.Getenv("CORTEXIUM_RUNNER_EVAL_HARNESSES")) {
		if harness != config.HarnessPiCLI {
			continue
		}
		if settings.PiModel == "" {
			return evalSettings{}, errors.New("live evaluation requires an explicit Pi model when Pi is selected")
		}
		if strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_ALLOW_PI_HOST")) != "1" {
			return evalSettings{}, errors.New("Pi implementer and reviewer evaluation requires explicit host-access approval")
		}
		settings.PiHostAccess = true
	}
	if raw := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_MAX_TOKENS")); raw != "" {
		settings.MaxTokens, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || settings.MaxTokens <= 0 {
			return evalSettings{}, errors.New("CORTEXIUM_RUNNER_EVAL_MAX_TOKENS must be a positive integer")
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_MAX_COST_USD")); raw != "" {
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil || value <= 0 {
			return evalSettings{}, errors.New("CORTEXIUM_RUNNER_EVAL_MAX_COST_USD must be a positive number")
		}
		settings.MaxCostUSD = &value
	}
	return settings, nil
}

func (s evalSettings) modelForHarness(kind string) string {
	switch kind {
	case config.HarnessCodexCLI:
		return s.CodexModel
	case config.HarnessClaudeCLI:
		return s.ClaudeModel
	case config.HarnessPiCLI:
		return s.PiModel
	default:
		return ""
	}
}

func positiveEnvInt(name string) (int, error) {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}

func newEvalCoordinator(settings evalSettings, output io.Writer) (*evalCoordinator, error) {
	// A started evaluation is never silently restarted with a reset budget.
	artifact, err := os.OpenFile(settings.ArtifactPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open private evaluation artifact: %w", err)
	}
	if err := artifact.Chmod(0o600); err != nil {
		_ = artifact.Close()
		return nil, fmt.Errorf("protect evaluation artifact: %w", err)
	}
	return &evalCoordinator{settings: settings, started: time.Now(), output: output, artifact: artifact}, nil
}

func (c *evalCoordinator) Close() error { return c.artifact.Close() }

func (c *evalCoordinator) beforeCase() error {
	if c.recordErr != nil {
		return errors.New("evaluation artifact persistence failed")
	}
	if c.stopReason != "" {
		return errors.New(c.stopReason)
	}
	if time.Since(c.started) >= c.settings.AggregateTime {
		return errors.New("aggregate live-evaluation wall-time ceiling reached")
	}
	// Full matrix: seven cases for each of three harnesses.
	maxAttempts := c.settings.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 21
	}
	decision := EvaluateAdmission(c.admissionBudget(maxAttempts), c.attempts, time.Now())
	if !decision.Allowed {
		return errors.New(decision.Reason)
	}
	return nil
}

func (c *evalCoordinator) admissionBudget(maxAttempts int) *config.AdmissionBudgetConfig {
	return &config.AdmissionBudgetConfig{
		WindowSeconds: c.settings.AggregateTime.Milliseconds()/1000 + 1,
		MaxAttempts:   maxAttempts, MaxHarnessSeconds: int64(c.settings.AggregateTime / time.Second),
		MaxReportedTokens: c.settings.MaxTokens, MaxReportedCostUSD: c.settings.MaxCostUSD,
	}
}

func (c *evalCoordinator) runCase(ctx context.Context, harness, role, caseID string, run func(context.Context) evalCaseResult) evalCaseResult {
	if err := ctx.Err(); err != nil {
		c.stopReason = "evaluation canceled before admission"
		return evalCaseResult{AdmissionStop: c.stopReason, Err: err}
	}
	if err := c.beforeCase(); err != nil {
		result := evalCaseResult{AdmissionStop: err.Error(), Err: err}
		c.stopReason = err.Error()
		// An admission refusal is not an executed assignment or reported usage.
		return result
	}
	c.emit("EVAL_CASE", evalCaseRecord{Event: "started", Candidate: c.settings.Candidate, Run: c.settings.Run, Harness: harness, Role: role, Case: caseID})
	if c.recordErr != nil {
		return evalCaseResult{AdmissionStop: "evaluation artifact persistence failed", Err: c.recordErr}
	}
	started := time.Now()
	aggregateContext, aggregateCancel := context.WithDeadline(ctx, c.started.Add(c.settings.AggregateTime))
	defer aggregateCancel()
	caseContext, cancel := context.WithTimeout(aggregateContext, c.settings.CaseTimeout)
	result := run(caseContext)
	// The frozen old comparison worker returns its native historical units.
	// Summarize normalizes one leaf using its recorded harness on current
	// Runner; the unchanged old metrics API keeps this shared worker buildable.
	result.Usage = normalizedEvalUsage(result.Usage, harness)
	if result.AdmissionStop != "" {
		c.stopReason = result.AdmissionStop
	}
	contextErr := caseContext.Err()
	cancel()
	duration := time.Since(started)
	if result.Outcome == "" {
		result.Outcome = "blocked"
	}
	c.attempts = append(c.attempts, metrics.Attempt{Event: metrics.Event{
		AttemptID: metrics.NewAttemptID(), Kind: metrics.EventCompleted, ItemTitle: caseID,
		Harness: harness, Role: role, StartedAt: started, FinishedAt: time.Now(),
		DurationMilliseconds: duration.Milliseconds(), HarnessDurationMilliseconds: result.HarnessDurationMilliseconds,
		Outcome: result.Outcome, FailureClass: result.FailureClass, RetryDisposition: result.RetryDisposition, RetryAfter: result.RetryAfter, Usage: result.Usage,
	}, Completed: true})
	if contextErr != nil {
		result.ComparisonUsable = false
		c.stopReason, result.AdmissionStop = "evaluation execution deadline or cancellation reached", "evaluation execution deadline or cancellation reached"
		result.Err = errors.Join(result.Err, contextErr)
	}
	if decision := EvaluateAdmission(c.admissionBudget(0), c.attempts, time.Now()); !decision.Allowed {
		c.stopReason, result.AdmissionStop = decision.Reason, decision.Reason
		result.Err = errors.Join(result.Err, errors.New(decision.Reason))
	}
	c.emit("EVAL_CASE", evalCaseRecord{
		AdmissionStop: result.AdmissionStop, PromptContexts: result.PromptContexts,
		FixtureTestDurationMS: result.FixtureTestDurationMS, HarnessDurationMS: result.HarnessDurationMilliseconds,
		ExpectedVerdict: result.ExpectedVerdict, ObservedVerdict: result.ObservedVerdict, ReviewJudgment: result.ReviewJudgment,
		Event: "completed", Candidate: c.settings.Candidate, Run: c.settings.Run, Harness: harness, Role: role, Case: caseID,
		Outcome: result.Outcome, FailureClass: result.FailureClass, RetryDisposition: result.RetryDisposition, RetryAfter: result.RetryAfter, FailureStage: result.FailureStage,
		DurationMS: duration.Milliseconds(), Usage: result.Usage,
	})
	if c.recordErr != nil {
		result.AdmissionStop = "evaluation artifact persistence failed"
		result.Err = errors.Join(result.Err, c.recordErr)
	}
	if result.ReviewJudgment != "" {
		if c.reviewerJudgments == nil {
			c.reviewerJudgments = map[string]int{}
		}
		c.reviewerJudgments[result.ReviewJudgment]++
	}
	return result
}

func normalizedEvalUsage(usage metrics.Usage, harness string) metrics.Usage {
	return metrics.Summarize([]metrics.Attempt{{Completed: true, Event: metrics.Event{Harness: harness, Usage: usage}}}).Usage
}

func (c *evalCoordinator) finish(passed bool) {
	c.emit("EVAL_SUMMARY", evalSummaryRecord{
		AdmissionStop: c.stopReason,
		Event:         "summary", Candidate: c.settings.Candidate, Run: c.settings.Run,
		Passed: passed, Summary: metrics.Summarize(c.attempts), ReviewerJudgments: c.reviewerJudgments,
	})
}

func (c *evalCoordinator) emit(prefix string, record any) {
	if event, ok := record.(evalCaseRecord); ok && !validEvalFailureStage(event.FailureStage) {
		panic("invalid evaluation failure stage")
	}
	if event, ok := record.(evalCaseRecord); ok && !validEvalReviewRecord(event) {
		panic("invalid evaluation review judgment")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(c.output, "%s %s\n", prefix, encoded)
	if c.recordErr == nil {
		_, c.recordErr = fmt.Fprintln(c.artifact, string(encoded))
		if c.recordErr == nil {
			c.recordErr = c.artifact.Sync()
		}
	}
}

func validEvalReviewRecord(record evalCaseRecord) bool {
	switch record.ExpectedVerdict {
	case "", "accept", "needs_changes", "blocked":
	default:
		return false
	}
	switch record.ObservedVerdict {
	case "", "accept", "needs_changes", "blocked":
	default:
		return false
	}
	switch record.ReviewJudgment {
	case "", "expected_checks_match", "unexpected_findings", "false_acceptance", "unnecessary_rejection", "missed_defect", "incomplete_review":
		return true
	default:
		return false
	}
}

func validEvalFailureStage(stage string) bool {
	switch stage {
	case "", "admission", "planner_execution", "open_decisions", "work_item_count", "required_concept", "implementation_execution", "fixture_content", "reviewer_execution", "reviewer_verdict":
		return true
	default:
		return false
	}
}

func TestEvalSettingsRequireExplicitPiModelAndHostAccess(t *testing.T) {
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CANDIDATE", "abc123")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_ARTIFACT", t.TempDir()+"/summary.jsonl")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_RUN", "1")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CASE_TIMEOUT_SECONDS", "1")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_MAX_SECONDS", "2")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_MAX_TOKENS", "100")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_HARNESSES", "codex,claude")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_PI_MODEL", "")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_ALLOW_PI_HOST", "")
	if _, err := evalSettingsFromEnvironment(); err != nil {
		t.Fatalf("non-Pi evaluation required Pi settings: %v", err)
	}

	t.Setenv("CORTEXIUM_RUNNER_EVAL_HARNESSES", "pi")
	if _, err := evalSettingsFromEnvironment(); err == nil || !strings.Contains(err.Error(), "explicit Pi model") {
		t.Fatalf("Pi evaluation accepted no Pi model: %v", err)
	}
	t.Setenv("CORTEXIUM_RUNNER_EVAL_PI_MODEL", "lmstudio/qwen")
	if _, err := evalSettingsFromEnvironment(); err == nil || !strings.Contains(err.Error(), "explicit host-access approval") {
		t.Fatalf("Pi evaluation accepted no host-access approval: %v", err)
	}
	t.Setenv("CORTEXIUM_RUNNER_EVAL_ALLOW_PI_HOST", "1")
	if settings, err := evalSettingsFromEnvironment(); err != nil || settings.PiModel != "lmstudio/qwen" || !settings.PiHostAccess {
		t.Fatalf("Pi evaluation rejected explicit model and host access: settings=%#v error=%v", settings, err)
	}
}

func TestEvalSettingsAcceptHarnessModelsAndReasoning(t *testing.T) {
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CANDIDATE", "abc123")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_ARTIFACT", t.TempDir()+"/summary.jsonl")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_RUN", "1")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CASE_TIMEOUT_SECONDS", "1")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_MAX_SECONDS", "2")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_MAX_TOKENS", "100")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_HARNESSES", "codex,claude")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CODEX_MODEL", "gpt-small")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_CLAUDE_MODEL", "sonnet")
	t.Setenv("CORTEXIUM_RUNNER_EVAL_REASONING", "medium")

	settings, err := evalSettingsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if settings.modelForHarness(config.HarnessCodexCLI) != "gpt-small" || settings.modelForHarness(config.HarnessClaudeCLI) != "sonnet" || settings.Reasoning != "medium" {
		t.Fatalf("evaluation overrides were not preserved: %#v", settings)
	}
}

func TestEvalFailureStageAllowlistRejectsDiagnostics(t *testing.T) {
	if !validEvalFailureStage("required_concept") || validEvalFailureStage("prompt=secret") {
		t.Fatal("evaluation failure-stage allowlist is unsafe")
	}
}

func TestEvalCoordinatorFailsClosedWhenConfiguredUsageIsUnavailable(t *testing.T) {
	artifact := t.TempDir() + "/summary.jsonl"
	cost := 1.0
	coordinator, err := newEvalCoordinator(evalSettings{
		Candidate: "abc123", Run: 1, ArtifactPath: artifact,
		CaseTimeout: time.Second, AggregateTime: time.Minute, MaxTokens: 100, MaxCostUSD: &cost,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	result := coordinator.runCase(t.Context(), "pi", "planner", "missing_usage", func(context.Context) evalCaseResult {
		return evalCaseResult{Outcome: "succeeded", HarnessDurationMilliseconds: 1}
	})
	if result.Err == nil || !strings.Contains(result.AdmissionStop, "lack token usage") || result.Outcome != "succeeded" || result.FailureClass != "" {
		t.Fatalf("missing reported usage did not fail closed: %#v", result)
	}
}

func TestEvalRecordsExcludePromptsResultsAndDiagnostics(t *testing.T) {
	artifact := t.TempDir() + "/summary.jsonl"
	var output strings.Builder
	coordinator, err := newEvalCoordinator(evalSettings{
		Candidate: "abc123", Run: 1, ArtifactPath: artifact,
		CaseTimeout: time.Second, AggregateTime: time.Minute,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	secret := "SECRET_PROMPT_AND_RESULT"
	result := coordinator.runCase(t.Context(), "pi", "reviewer", "seeded_regression", func(context.Context) evalCaseResult {
		return evalCaseResult{Outcome: "blocked", FailureClass: "timeout", RetryDisposition: "none", Err: errors.New(secret)}
	})
	coordinator.runCase(t.Context(), "codex", "planner", "after_timeout", func(context.Context) evalCaseResult {
		return evalCaseResult{Outcome: "succeeded"}
	})
	coordinator.finish(result.Err == nil)
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	artifactData, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	combined := output.String() + string(artifactData)
	if strings.Contains(combined, secret) || !strings.Contains(combined, `"failure_class":"timeout"`) || !strings.Contains(combined, `"case":"after_timeout"`) || !strings.Contains(combined, `"attempts":2`) {
		t.Fatalf("unsafe or untruthful evaluation record: %s", combined)
	}
	info, err := os.Stat(artifact)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("evaluation artifact permissions = %v error=%v", info.Mode().Perm(), err)
	}
}

func TestEvalRecordsRetainReviewerJudgmentsAndSeparateTestTime(t *testing.T) {
	artifact := filepath.Join(t.TempDir(), "summary.jsonl")
	coordinator, err := newEvalCoordinator(evalSettings{
		Candidate: "abc123", Run: 1, ArtifactPath: artifact,
		CaseTimeout: time.Second, AggregateTime: time.Minute,
	}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	for _, verdict := range []string{"accept", "needs_changes"} {
		coordinator.runCase(t.Context(), "codex", "reviewer", verdict, func(context.Context) evalCaseResult {
			scenario := reviewerEvalScenario{wantVerdict: "accept", failedCriterion: -1}
			return evalCaseResult{Outcome: "succeeded", ExpectedVerdict: "accept", ObservedVerdict: verdict,
				ReviewJudgment: reviewerEvalJudgment(scenario, &execution.ReviewAssessment{Verdict: verdict,
					Criteria: []execution.ReviewCriterionResult{{Criterion: recordUpdateProofs[0], Status: "passed"}, {Criterion: recordUpdateProofs[1], Status: "passed"}},
					Rules:    []execution.ReviewRuleResult{{Status: "passed"}}, Maintainability: execution.ReviewMaintainabilityResult{Status: "passed"}}),
				FixtureTestDurationMS: 12, HarnessDurationMilliseconds: 34}
		})
	}
	coordinator.finish(false)
	content, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	var summary evalSummaryRecord
	var completed []evalCaseRecord
	for _, line := range strings.Split(strings.TrimSpace(string(content)), "\n") {
		var record evalCaseRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record.Event == "summary" {
			if err := json.Unmarshal([]byte(line), &summary); err != nil {
				t.Fatal(err)
			}
		} else if record.Event == "completed" {
			completed = append(completed, record)
		}
	}
	if len(completed) != 2 || completed[0].ReviewJudgment != "expected_checks_match" || completed[1].ReviewJudgment != "unnecessary_rejection" {
		t.Fatalf("review outcomes = %#v", completed)
	}
	if completed[0].FixtureTestDurationMS != 12 || completed[0].HarnessDurationMS != 34 || completed[1].ObservedVerdict != "needs_changes" {
		t.Fatalf("lost timing or verdict evidence: %#v", completed)
	}
	if summary.ReviewerJudgments["expected_checks_match"] != 1 || summary.ReviewerJudgments["unnecessary_rejection"] != 1 {
		t.Fatalf("review summary = %#v", summary.ReviewerJudgments)
	}
	if validEvalReviewRecord(evalCaseRecord{ObservedVerdict: "private model response"}) || validEvalReviewRecord(evalCaseRecord{ReviewJudgment: "private diagnostic"}) {
		t.Fatal("review telemetry accepted unrestricted content")
	}
}

func TestEvalRecordsPreserveStructuredProviderLimit(t *testing.T) {
	artifact := t.TempDir() + "/summary.jsonl"
	var output strings.Builder
	coordinator, err := newEvalCoordinator(evalSettings{
		Candidate: "abc123", Run: 1, ArtifactPath: artifact,
		CaseTimeout: time.Second, AggregateTime: time.Minute,
	}, &output)
	if err != nil {
		t.Fatal(err)
	}
	result := coordinator.runCase(t.Context(), "claude", "planner", "provider_limit", func(context.Context) evalCaseResult {
		return evalCaseResult{
			Outcome: "blocked", FailureClass: "capacity_exhausted", RetryDisposition: "manual",
			RetryAfter: "2026-08-13T10:40:00+02:00", Err: errors.New("unretained provider diagnostic"),
		}
	})
	coordinator.finish(result.Err == nil)
	if err := coordinator.Close(); err != nil {
		t.Fatal(err)
	}
	artifactData, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	combined := output.String() + string(artifactData)
	if !strings.Contains(combined, `"failure_class":"capacity_exhausted"`) || !strings.Contains(combined, `"retry_disposition":"manual"`) || !strings.Contains(combined, `"retry_after":"2026-08-13T10:40:00+02:00"`) {
		t.Fatalf("provider limit classification was not preserved: %s", combined)
	}
	if strings.Contains(combined, "unretained provider diagnostic") {
		t.Fatalf("provider diagnostic escaped sanitized records: %s", combined)
	}
}
