package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/skills"
)

const (
	reviewerComparisonOldSource       = "40ee353c38c33f500cb99d909d194d1b5eaf2988"
	reviewerComparisonModel           = "gpt-6-astra"
	reviewerComparisonWall            = 45 * time.Minute
	reviewerComparisonTokens          = 300000
	reviewerComparisonAssessmentBytes = 128 * 1024
)

// Set only by the preparation build's -ldflags. No production prompt override.
var reviewerComparisonBuildSource string

type reviewerComparisonCase struct {
	Name             string `json:"name"`
	ExpectedVerdict  string `json:"expected_verdict"`
	Candidate        string `json:"candidate"`
	Base             string `json:"base"`
	AssignmentDigest string `json:"assignment_digest"`
}

type reviewerComparisonArm struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Binary       string `json:"binary"`
	BinaryDigest string `json:"binary_digest"`
	SkillDigest  string `json:"skill_digest"`
}

type reviewerComparisonManifest struct {
	Version       int                      `json:"version"`
	HarnessDigest string                   `json:"common_harness_digest"`
	Model         string                   `json:"model"`
	Reasoning     string                   `json:"reasoning"`
	Arms          []reviewerComparisonArm  `json:"arms"`
	Cases         []reviewerComparisonCase `json:"cases"`
}

type reviewerComparisonRequest struct {
	Describe    bool                   `json:"describe"`
	Source      string                 `json:"source"`
	SkillDigest string                 `json:"skill_digest"`
	Case        reviewerComparisonCase `json:"case"`
	Deadline    time.Time              `json:"deadline"`
	CLIVersion  string                 `json:"cli_version"`
	Output      string                 `json:"output"`
}

type reviewerComparisonResponse struct {
	Source      string                   `json:"source"`
	SkillDigest string                   `json:"skill_digest"`
	Cases       []reviewerComparisonCase `json:"cases,omitempty"`
	Result      *evalCaseRecord          `json:"result,omitempty"`
	Stages      []metrics.Event          `json:"stages,omitempty"`
	Failed      bool                     `json:"failed,omitempty"`
	// Untrusted reviewer reasoning is retained only in the private worker file,
	// never emitted by the controller's console or aggregate event stream.
	Assessment              json.RawMessage `json:"assessment,omitempty"`
	IndependentAdjudication string          `json:"independent_adjudication,omitempty"`
}

func comparisonAssessment(assessment *execution.ReviewAssessment) json.RawMessage {
	if assessment == nil {
		return nil
	}
	data, err := json.Marshal(assessment)
	if err != nil || len(data) > reviewerComparisonAssessmentBytes {
		return nil // No truncated reasoning can count as a complete assessment.
	}
	return data
}

func comparisonDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func comparisonCase(scenario reviewerEvalScenario, assignment execution.Assignment) reviewerComparisonCase {
	encoded, err := json.Marshal(assignment.Spec)
	if err != nil {
		panic(err)
	}
	return reviewerComparisonCase{Name: scenario.name, ExpectedVerdict: scenario.wantVerdict,
		Candidate: assignment.Spec.ReviewCandidateOID, Base: assignment.Spec.ReviewBaseOID, AssignmentDigest: comparisonDigest(encoded)}
}

func writeComparisonJSON(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(file).Encode(value)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func readComparisonJSON(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 2*1024*1024))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); err != io.EOF {
		return errors.New("comparison record has trailing or oversized data")
	}
	return nil
}

func comparisonCLIVersion(ctx context.Context) (string, error) {
	result, err := (subprocess.OSRunner{}).RunBoundedHeadTailInput(ctx, "codex", []string{"--version"}, "", 10*time.Second, nil, 1024, "[truncated]")
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Stdout) == "" {
		return "", errors.New("cannot observe Codex CLI version")
	}
	return strings.TrimSpace(result.Stdout), nil
}

// TestReviewerComparisonWorker is private to prepared test binaries. Describe
// performs deterministic local checks only; review requires a controller's
// exact fixture binding and shared deadline. Ordinary go test skips both.
func TestReviewerComparisonWorker(t *testing.T) {
	requestPath := os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_REQUEST")
	if requestPath == "" {
		t.Skip("private reviewer comparison worker")
	}
	var request reviewerComparisonRequest
	if err := readComparisonJSON(requestPath, &request); err != nil {
		t.Fatal(err)
	}
	if request.Source == "" || request.Source != reviewerComparisonBuildSource {
		t.Fatal("frozen worker source mismatch")
	}
	skill, ok := (skills.EmbeddedCatalog{}).Get("runner-reviewer")
	if !ok || request.SkillDigest != "" && request.SkillDigest != skill.SHA256 {
		t.Fatal("frozen reviewer guidance mismatch")
	}
	response := reviewerComparisonResponse{Source: reviewerComparisonBuildSource, SkillDigest: skill.SHA256}
	ctx := t.Context()
	if !request.Describe {
		remaining := time.Until(request.Deadline)
		if remaining <= 0 || remaining > reviewerComparisonWall {
			t.Fatal("comparison deadline is absent, expired or expanded")
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, request.Deadline)
		defer cancel()
		version, err := comparisonCLIVersion(ctx)
		if err != nil || version != request.CLIVersion {
			t.Fatal("Codex CLI version changed after comparison admission")
		}
	}
	for _, scenario := range reviewerComparisonScenarios(t) {
		if !request.Describe && scenario.name != request.Case.Name {
			continue
		}
		repo, assignment := prepareReviewerEval(t, scenario)
		_, duration, err := runReviewerFixtureTests(ctx, repo)
		if err != nil {
			t.Fatal("common reviewer fixture failed before model admission")
		}
		addReviewerFixtureEvidence(&assignment)
		metadata := comparisonCase(scenario, assignment)
		response.Cases = append(response.Cases, metadata)
		if request.Describe {
			continue
		}
		if metadata != request.Case {
			t.Fatal("common fixture metadata changed before model admission")
		}
		settings := evalSettings{Candidate: reviewerComparisonBuildSource, CodexModel: reviewerComparisonModel, Reasoning: "medium", CaseTimeout: time.Until(request.Deadline)}
		if settings.CaseTimeout < time.Second {
			t.Fatal("no remaining comparison execution budget")
		}
		result := executeReviewerEval(ctx, t, config.HarnessCodexCLI, settings, scenario, repo, assignment, duration)
		response.Result = &evalCaseRecord{ExpectedVerdict: result.ExpectedVerdict, ObservedVerdict: result.ObservedVerdict, ReviewJudgment: result.ReviewJudgment,
			Outcome: result.Outcome, FailureClass: result.FailureClass, RetryDisposition: result.RetryDisposition, RetryAfter: result.RetryAfter,
			FailureStage: result.FailureStage, Usage: result.Usage, FixtureTestDurationMS: result.FixtureTestDurationMS,
			HarnessDurationMS: result.HarnessDurationMilliseconds, PromptContexts: result.PromptContexts}
		response.Failed = result.Err != nil
		response.Assessment = comparisonAssessment(result.ReviewAssessment)
		response.IndependentAdjudication = "pending"
		for _, stage := range result.Stages {
			if stage.Stage == metrics.StageReviewerAudit || stage.Stage == metrics.StageReviewerVerify {
				response.Stages = append(response.Stages, stage)
			}
		}
	}
	if len(response.Cases) == 0 {
		t.Fatal("unknown comparison case")
	}
	if err := writeComparisonJSON(request.Output, response); err != nil {
		t.Fatal(err)
	}
}

func invokeComparisonWorker(ctx context.Context, binary string, request reviewerComparisonRequest) (reviewerComparisonResponse, error) {
	directory := filepath.Dir(request.Output)
	requestPath := filepath.Join(directory, "request.json")
	if err := writeComparisonJSON(requestPath, request); err != nil {
		return reviewerComparisonResponse{}, err
	}
	workerCtx, err := subprocess.WithEnvironmentVariable(ctx, "CORTEXIUM_RUNNER_REVIEW_COMPARISON_REQUEST", requestPath)
	if err != nil {
		return reviewerComparisonResponse{}, err
	}
	// Existing subprocess ownership supervises the worker and its descendants.
	output, runErr := (subprocess.OSRunner{}).RunBoundedHeadTailInput(workerCtx, binary, []string{"-test.run=^TestReviewerComparisonWorker$", "-test.count=1", "-test.timeout=46m"}, filepath.Dir(binary), 0, nil, 4096, "[truncated]")
	var response reviewerComparisonResponse
	readErr := readComparisonJSON(request.Output, &response)
	if runErr != nil || output.ExitCode != 0 || readErr != nil {
		return response, errors.New("comparison worker failed or returned uncertain evidence; no retry authorized")
	}
	return response, nil
}

func comparisonSettings(path, candidate string) evalSettings {
	return evalSettings{Candidate: candidate, Run: 1, ArtifactPath: path, MaxAttempts: 8,
		AggregateTime: reviewerComparisonWall, CaseTimeout: reviewerComparisonWall, MaxTokens: reviewerComparisonTokens,
		CodexModel: reviewerComparisonModel, Reasoning: "medium"}
}

func comparisonWorkerResult(arm reviewerComparisonArm, metadata reviewerComparisonCase, response reviewerComparisonResponse) evalCaseResult {
	if response.Source != arm.Source || response.SkillDigest != arm.SkillDigest || len(response.Cases) != 1 || response.Cases[0] != metadata || response.Result == nil || !validEvalReviewRecord(*response.Result) || !validEvalFailureStage(response.Result.FailureStage) || response.Result.ExpectedVerdict != metadata.ExpectedVerdict {
		return evalCaseResult{Err: errors.New("worker result no longer matches admitted source/corpus")}
	}
	r := response.Result
	usage := normalizedEvalUsage(r.Usage, config.HarnessCodexCLI)
	result := evalCaseResult{Outcome: r.Outcome, FailureClass: r.FailureClass, RetryDisposition: r.RetryDisposition, RetryAfter: r.RetryAfter,
		FailureStage: r.FailureStage, ExpectedVerdict: r.ExpectedVerdict, ObservedVerdict: r.ObservedVerdict, ReviewJudgment: r.ReviewJudgment,
		Usage: usage, FixtureTestDurationMS: r.FixtureTestDurationMS, HarnessDurationMilliseconds: r.HarnessDurationMS, PromptContexts: r.PromptContexts}
	if response.Failed {
		result.Err = errors.New("reviewer evaluation did not complete correctly")
	}
	var assessment execution.ReviewAssessment
	assessmentAvailable := len(response.Assessment) > 0 && len(response.Assessment) <= reviewerComparisonAssessmentBytes &&
		json.Unmarshal(response.Assessment, &assessment) == nil && assessment.Verdict == r.ObservedVerdict && response.IndependentAdjudication == "pending"
	guidanceAvailable := len(r.PromptContexts) > 0
	for _, observed := range r.PromptContexts {
		digest, err := hex.DecodeString(strings.TrimPrefix(observed.GuidanceDigest, "sha256:"))
		if observed.Layout == "" || !strings.HasPrefix(observed.GuidanceDigest, "sha256:") || err != nil || len(digest) != sha256.Size {
			guidanceAvailable = false
		}
	}
	// All audit/focused usage is aggregated once by the existing executor.
	// Reuse production admission's checked accounting, rather than introduce
	// a second total formula in this common old/new-source test harness.
	now := time.Now()
	accounting := EvaluateAdmission(&config.AdmissionBudgetConfig{WindowSeconds: 60, MaxReportedTokens: 1<<63 - 1},
		[]metrics.Attempt{{Completed: true, Event: metrics.Event{Harness: config.HarnessCodexCLI, StartedAt: now, Usage: usage}}}, now)
	if !accounting.Allowed || metrics.ValidateUsage(usage) != nil || usage.CoverageStatus() != metrics.UsageComplete || !usage.Available || !guidanceAvailable || !assessmentAvailable {
		result.AdmissionStop = "review usage, observed guidance provenance or retained assessment unavailable"
	} else {
		result.ComparisonUsable = r.ObservedVerdict != "" && r.ReviewJudgment != "" &&
			(r.FailureStage == "" && !response.Failed || r.FailureStage == "reviewer_verdict")
	}
	return result
}

func validateComparisonManifest(manifest reviewerComparisonManifest) error {
	if manifest.Version != 1 || manifest.Model != reviewerComparisonModel || manifest.Reasoning != "medium" || len(manifest.Arms) != 2 || len(manifest.Cases) != 4 || len(manifest.HarnessDigest) != 64 {
		return errors.New("comparison requires the reviewed four-case, two-arm contract")
	}
	if manifest.Arms[0].Name != "old" || manifest.Arms[0].Source != reviewerComparisonOldSource || manifest.Arms[1].Name != "revised" || len(manifest.Arms[1].Source) != 40 {
		return errors.New("comparison source pins changed")
	}
	wanted := []string{"record_update", "record_update_access", "record_update_repair", "record_update_missing_proof"}
	verdicts := []string{"accept", "needs_changes", "needs_changes", "blocked"}
	for index, entry := range manifest.Cases {
		if entry.Name != wanted[index] || entry.ExpectedVerdict != verdicts[index] || len(entry.AssignmentDigest) != 64 || len(entry.Candidate) != 40 || len(entry.Base) != 40 {
			return errors.New("comparison corpus metadata changed")
		}
	}
	for _, arm := range manifest.Arms {
		data, err := os.ReadFile(arm.Binary)
		if err != nil || comparisonDigest(data) != arm.BinaryDigest || len(arm.SkillDigest) != 64 {
			return errors.New("prepared comparison binary or guidance identity changed")
		}
	}
	return nil
}

// Completion means collecting the entire fixed corpus, not permission to admit
// a ninth assignment. Keep a final token-budget diagnostic without turning an
// already collected result into a failure or admitting anything else.
func runComparisonPairs(ctx context.Context, coordinator *evalCoordinator, manifest reviewerComparisonManifest, run func(context.Context, reviewerComparisonArm, reviewerComparisonCase) evalCaseResult) bool {
	if len(manifest.Cases) != 4 || len(manifest.Arms) != 2 {
		return false
	}
	collected := 0
	for index, metadata := range manifest.Cases {
		// Counterbalance arm order; never select or repeat cases after outcomes.
		for offset := 0; offset < 2; offset++ {
			arm := manifest.Arms[(index+offset)%2]
			result := coordinator.runCase(ctx, config.HarnessCodexCLI, config.WorkRoleReviewer, arm.Name+":"+metadata.Name, func(ctx context.Context) evalCaseResult {
				return run(ctx, arm, metadata)
			})
			if !result.ComparisonUsable || ctx.Err() != nil || coordinator.recordErr != nil {
				return false
			}
			collected++
			if collected == 8 {
				return true
			}
			if result.AdmissionStop != "" {
				return false
			}
		}
	}
	return false
}

// This entrypoint is deliberately not the planner/implementer smoke matrix.
// It opens a single exclusive spent artifact before admitting either arm.
func TestLiveReviewerComparison(t *testing.T) {
	if os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_MODE") != "run" {
		t.Skip("requires independently reviewed committed fixtures and explicit live admission")
	}
	directory := os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_DIRECTORY")
	var manifest reviewerComparisonManifest
	if err := readComparisonJSON(filepath.Join(directory, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if err := validateComparisonManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CORTEXIUM_RUNNER_REVIEW_COMPARISON_APPROVED_CANDIDATE") != manifest.Arms[1].Source {
		t.Fatal("explicit admission does not name the prepared revised source")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := comparisonCleanSource(t.Context(), root, manifest.Arms[1].Source); err != nil {
		t.Fatal(err)
	}
	_, harnessDigest, err := comparisonCommonFiles(root)
	if err != nil || harnessDigest != manifest.HarnessDigest {
		t.Fatal("reviewed common harness changed after preparation")
	}
	version, err := comparisonCLIVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := newEvalCoordinator(comparisonSettings(filepath.Join(directory, "run.jsonl"), manifest.Arms[1].Source), os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	coordinator.emit("EVAL_COMPARISON", struct {
		Event      string                     `json:"event"`
		Manifest   reviewerComparisonManifest `json:"manifest"`
		CLIVersion string                     `json:"cli_version"`
	}{"comparison", manifest, version})
	passed := false
	defer func() {
		coordinator.finish(passed)
		if coordinator.recordErr != nil {
			t.Error("comparison artifact incomplete; no retry authorized")
		}
	}()
	ctx, cancel := context.WithDeadline(t.Context(), coordinator.started.Add(reviewerComparisonWall))
	defer cancel()
	passed = runComparisonPairs(ctx, coordinator, manifest, func(ctx context.Context, arm reviewerComparisonArm, metadata reviewerComparisonCase) evalCaseResult {
		if err := validateComparisonManifest(manifest); err != nil {
			return evalCaseResult{Err: err}
		}
		caseDir, err := os.MkdirTemp(directory, arm.Name+"-"+metadata.Name+"-")
		if err != nil {
			return evalCaseResult{Err: err}
		}
		response, err := invokeComparisonWorker(ctx, arm.Binary, reviewerComparisonRequest{Source: arm.Source, SkillDigest: arm.SkillDigest, Case: metadata,
			Deadline: coordinator.started.Add(reviewerComparisonWall), CLIVersion: version, Output: filepath.Join(caseDir, "result.json")})
		if err != nil {
			return evalCaseResult{Err: err}
		}
		result := comparisonWorkerResult(arm, metadata, response)
		coordinator.emit("EVAL_STAGES", struct {
			Event  string          `json:"event"`
			Case   string          `json:"case"`
			Stages []metrics.Event `json:"stages"`
		}{"stages", arm.Name + ":" + metadata.Name, response.Stages})
		return result
	})
	if !passed {
		t.Error("comparison stopped before the full corpus was durably collected; no retry authorized")
	}
	// 'passed' here means the bounded experiment completed, not that one arm
	// is better. Per-case judgments remain the independent quality result.
}

func TestReviewerComparisonControllerCompletesEightPairsAndSeparatesStops(t *testing.T) {
	for _, test := range []struct {
		name                        string
		tokens                      int64
		cancelAt, failPersistenceAt int
		qualityMismatch             bool
		changeEighth                func(*reviewerComparisonResponse)
		wantCalls                   int
		wantComplete, wantBudget    bool
	}{
		{name: "all eight", tokens: 1, wantCalls: 8, wantComplete: true},
		{name: "token boundary on eighth", tokens: 37500, wantCalls: 8, wantComplete: true, wantBudget: true},
		{name: "premature token boundary", tokens: 50000, wantCalls: 6, wantBudget: true},
		{name: "eighth canceled", tokens: 1, cancelAt: 8, wantCalls: 8},
		{name: "eighth persistence failure", tokens: 1, failPersistenceAt: 8, wantCalls: 8},
		{name: "eighth missing prompt provenance", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Result.PromptContexts = nil }},
		{name: "eighth placeholder prompt provenance", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Result.PromptContexts = []metrics.PromptContext{{}} }},
		{name: "eighth partial usage", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Result.Usage.Coverage = metrics.UsagePartial }},
		{name: "eighth unknown usage", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Result.Usage.Coverage = metrics.UsageUnknown }},
		{name: "eighth unavailable usage", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Result.Usage = metrics.Usage{} }},
		{name: "eighth token boundary cannot forgive missing provenance", tokens: 37500, wantCalls: 8, wantBudget: true, changeEighth: func(r *reviewerComparisonResponse) { r.Result.PromptContexts = nil }},
		{name: "eighth worker failure with a verdict", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Failed = true }},
		{name: "eighth missing private assessment", tokens: 1, wantCalls: 8, changeEighth: func(r *reviewerComparisonResponse) { r.Assessment = nil }},
		{name: "quality disagreement is a collected result", tokens: 1, qualityMismatch: true, wantCalls: 8, wantComplete: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.jsonl")
			coordinator, err := newEvalCoordinator(comparisonSettings(path, "candidate"), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer coordinator.Close()
			manifest := reviewerComparisonManifest{Arms: []reviewerComparisonArm{{Name: "old"}, {Name: "revised"}}}
			for _, scenario := range reviewerComparisonScenarios(t) {
				manifest.Cases = append(manifest.Cases, reviewerComparisonCase{Name: scenario.name, ExpectedVerdict: scenario.wantVerdict})
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			seen := map[string]bool{}
			complete := runComparisonPairs(ctx, coordinator, manifest, func(_ context.Context, arm reviewerComparisonArm, metadata reviewerComparisonCase) evalCaseResult {
				calls++
				key := arm.Name + ":" + metadata.Name
				if seen[key] {
					t.Fatal("controller repeated a pair")
				}
				seen[key] = true
				if calls == test.cancelAt {
					cancel()
				}
				if calls == test.failPersistenceAt {
					coordinator.artifact.Close()
				}
				response := reviewerComparisonResponse{Source: arm.Source, SkillDigest: arm.SkillDigest, Cases: []reviewerComparisonCase{metadata},
					Result: &evalCaseRecord{ExpectedVerdict: metadata.ExpectedVerdict, ObservedVerdict: metadata.ExpectedVerdict, ReviewJudgment: "expected_checks_match", Outcome: "succeeded",
						Usage:          metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: test.tokens},
						PromptContexts: []metrics.PromptContext{{Layout: "reviewer", GuidanceDigest: "sha256:" + strings.Repeat("a", 64)}}}}
				if test.qualityMismatch && calls == 2 {
					response.Result.ObservedVerdict = "needs_changes"
					response.Result.ReviewJudgment = "unnecessary_rejection"
					response.Result.FailureStage = "reviewer_verdict"
					response.Failed = true
				}
				response.Assessment = comparisonAssessment(&execution.ReviewAssessment{Verdict: response.Result.ObservedVerdict})
				response.IndependentAdjudication = "pending"
				if calls == 8 && test.changeEighth != nil {
					test.changeEighth(&response)
				}
				return comparisonWorkerResult(arm, metadata, response)
			})
			if complete != test.wantComplete || calls != test.wantCalls {
				t.Fatalf("complete=%v calls=%d", complete, calls)
			}
			if test.wantBudget && !strings.Contains(coordinator.stopReason, "reported tokens") {
				t.Fatal("budget diagnostic discarded")
			}
			if test.failPersistenceAt == 0 {
				coordinator.finish(complete)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				lines := strings.Split(strings.TrimSpace(string(data)), "\n")
				var summary evalSummaryRecord
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &summary); err != nil {
					t.Fatal(err)
				}
				if summary.Passed != test.wantComplete {
					t.Fatal("summary confused corpus completion with admission exhaustion")
				}
				if test.qualityMismatch && summary.ReviewerJudgments["unnecessary_rejection"] != 1 {
					t.Fatal("quality disagreement lost")
				}
				if test.changeEighth != nil {
					var last evalCaseRecord
					if err := json.Unmarshal([]byte(lines[len(lines)-2]), &last); err != nil {
						t.Fatal(err)
					}
					if summary.ReviewerJudgments["expected_checks_match"] != 8 || last.Outcome != "succeeded" || last.ReviewJudgment != "expected_checks_match" || last.ObservedVerdict != "blocked" {
						t.Fatal("incomplete comparison discarded the eighth quality result or rewrote its outcome")
					}
				}
			}
		})
	}
}

func TestReviewerComparisonFixturesHaveIdenticalMetadataAndMissingProofOracle(t *testing.T) {
	cases := reviewerComparisonScenarios(t)
	if len(cases) != 4 {
		t.Fatal("comparison must contain exactly four reviewed cases")
	}
	for _, scenario := range cases {
		_, left := prepareReviewerEval(t, scenario)
		_, right := prepareReviewerEval(t, scenario)
		addReviewerFixtureEvidence(&left)
		addReviewerFixtureEvidence(&right)
		if comparisonCase(scenario, left) != comparisonCase(scenario, right) || !reflect.DeepEqual(left, right) {
			t.Fatal("fixture metadata varies across arms")
		}
		for _, proof := range left.Spec.RecordedVerification {
			if proof.Criterion == scenario.missingProof {
				t.Fatal("unavailable attestation received passing test prose")
			}
		}
	}
	missing := cases[3]
	assessment := execution.ReviewAssessment{Verdict: "blocked", Maintainability: execution.ReviewMaintainabilityResult{Status: "passed"}, Rules: []execution.ReviewRuleResult{{Status: "passed"}}, Criteria: []execution.ReviewCriterionResult{
		{Criterion: recordUpdateProofs[0], Status: "passed"}, {Criterion: recordUpdateProofs[1], Status: "passed"}, {Criterion: missing.missingProof, Status: "blocked"},
	}}
	if got := reviewerEvalJudgment(missing, &assessment); got != "expected_checks_match" {
		t.Fatalf("legitimate proof blocker: %s", got)
	}
	assessment.Criteria[0].Status = "failed"
	if got := reviewerEvalJudgment(missing, &assessment); got != "unexpected_findings" {
		t.Fatal("invented implementation defect passed oracle")
	}
	assessment.Verdict = "accept"
	if got := reviewerEvalJudgment(missing, &assessment); got != "false_acceptance" {
		t.Fatal("missing proof was accepted")
	}
}

func TestReviewerComparisonBudgetIsSharedAndNeverRewritesOutcome(t *testing.T) {
	for _, test := range []struct {
		name  string
		usage metrics.Usage
		count int
	}{
		{"eight", metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 1}, 8},
		{"tokens", metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 150000}, 2},
		{"unavailable", metrics.Usage{}, 1},
		{"partial", metrics.Usage{Available: true, Coverage: metrics.UsagePartial, InputTokens: 1}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.jsonl")
			coordinator, err := newEvalCoordinator(comparisonSettings(path, "candidate"), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer coordinator.Close()
			calls := 0
			for index := 0; index < 9; index++ {
				result := coordinator.runCase(t.Context(), "codex", "reviewer", fmt.Sprintf("arm%d:case%d", index%2, index/2), func(context.Context) evalCaseResult {
					calls++
					return evalCaseResult{Outcome: "succeeded", ObservedVerdict: "accept", ReviewJudgment: "expected_checks_match", Usage: test.usage}
				})
				if index < test.count && (result.Outcome != "succeeded" || result.ReviewJudgment != "expected_checks_match" || result.FailureClass != "") {
					t.Fatal("budget rewrote observed outcome")
				}
			}
			if calls != test.count || len(coordinator.attempts) != test.count {
				t.Fatalf("calls=%d attempts=%d", calls, len(coordinator.attempts))
			}
			if reopened, err := newEvalCoordinator(comparisonSettings(path, "candidate"), io.Discard); err == nil {
				reopened.Close()
				t.Fatal("spent artifact reopened with fresh budget")
			}
		})
	}
}

func TestReviewerComparisonPersistenceFailureAndDeadlinePreventAdmission(t *testing.T) {
	for _, reason := range []string{"artifact", "deadline"} {
		t.Run(reason, func(t *testing.T) {
			coordinator, err := newEvalCoordinator(comparisonSettings(filepath.Join(t.TempDir(), "run.jsonl"), "candidate"), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			defer coordinator.Close()
			if reason == "artifact" {
				coordinator.artifact.Close()
			} else {
				coordinator.started = time.Now().Add(-reviewerComparisonWall)
			}
			result := coordinator.runCase(t.Context(), "codex", "reviewer", "old:record_update", func(context.Context) evalCaseResult {
				t.Fatal("admitted without durable budget")
				return evalCaseResult{}
			})
			if result.AdmissionStop == "" {
				t.Fatal("missing admission refusal")
			}
		})
	}
}

func TestReviewerComparisonRejectsMismatchedEvidenceAndUnknownUsage(t *testing.T) {
	arm := reviewerComparisonArm{Source: "source", SkillDigest: "skill"}
	metadata := reviewerComparisonCase{Name: "record_update", Candidate: "candidate", AssignmentDigest: "approved-inputs", ExpectedVerdict: "accept"}
	complete := metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 12, OutputTokens: 8}
	response := func() reviewerComparisonResponse {
		return reviewerComparisonResponse{Source: arm.Source, SkillDigest: arm.SkillDigest, Cases: []reviewerComparisonCase{metadata},
			Assessment: comparisonAssessment(&execution.ReviewAssessment{Verdict: "accept"}), IndependentAdjudication: "pending",
			Result: &evalCaseRecord{ExpectedVerdict: "accept", ObservedVerdict: "accept", Outcome: "succeeded", ReviewJudgment: "expected_checks_match", Usage: complete,
				PromptContexts: []metrics.PromptContext{{Layout: "reviewer", GuidanceDigest: "sha256:" + strings.Repeat("a", 64)}}},
			Stages: []metrics.Event{{Stage: metrics.StageReviewerAudit, Usage: complete}, {Stage: metrics.StageReviewerVerify, Usage: complete}}}
	}
	for _, test := range []struct {
		name   string
		change func(*reviewerComparisonResponse)
	}{
		{"source", func(r *reviewerComparisonResponse) { r.Source = "other" }},
		{"guidance", func(r *reviewerComparisonResponse) { r.SkillDigest = "other" }},
		{"candidate", func(r *reviewerComparisonResponse) { r.Cases[0].Candidate = "other" }},
		{"approved input", func(r *reviewerComparisonResponse) { r.Cases[0].AssignmentDigest = "other" }},
		{"oracle", func(r *reviewerComparisonResponse) { r.Result.ExpectedVerdict = "needs_changes" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := response()
			test.change(&r)
			if result := comparisonWorkerResult(arm, metadata, r); result.Err == nil {
				t.Fatal("changed fixture/evidence accepted")
			}
		})
	}
	for _, coverage := range []string{metrics.UsageUnknown, metrics.UsagePartial, metrics.UsageUnavailable} {
		r := response()
		r.Result.Usage.Coverage = coverage
		if coverage == metrics.UsageUnavailable {
			r.Result.Usage = metrics.Usage{Coverage: coverage}
		}
		result := comparisonWorkerResult(arm, metadata, r)
		if result.AdmissionStop == "" || result.ComparisonUsable || result.Outcome != "succeeded" || result.ReviewJudgment != "expected_checks_match" {
			t.Fatalf("coverage %s lost separation of quality and budget", coverage)
		}
	}
	result := comparisonWorkerResult(arm, metadata, response())
	if result.Err != nil || result.AdmissionStop != "" || !result.ComparisonUsable || !reflect.DeepEqual(result.Usage, normalizedEvalUsage(complete, config.HarnessCodexCLI)) {
		t.Fatal("stage events double-counted aggregate usage")
	}
}

func TestReviewerComparisonAssessmentIsBoundedPrivateAndAwaitingAdjudication(t *testing.T) {
	const reasoning = "Private reviewer rationale requiring independent interpretation"
	assessment := &execution.ReviewAssessment{Verdict: "accept", Summary: reasoning,
		Criteria: []execution.ReviewCriterionResult{{Criterion: recordUpdateProofs[0], Status: "passed", Summary: reasoning, Evidence: []string{"observed source"}}}}
	arm := reviewerComparisonArm{Source: "source", SkillDigest: "skill"}
	metadata := reviewerComparisonCase{Name: "record_update", ExpectedVerdict: "accept"}
	response := reviewerComparisonResponse{Source: arm.Source, SkillDigest: arm.SkillDigest, Cases: []reviewerComparisonCase{metadata},
		Assessment: comparisonAssessment(assessment), IndependentAdjudication: "pending",
		Result: &evalCaseRecord{ExpectedVerdict: "accept", ObservedVerdict: "accept", ReviewJudgment: "expected_checks_match", Outcome: "succeeded",
			Usage:          metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 1},
			PromptContexts: []metrics.PromptContext{{Layout: "reviewer", GuidanceDigest: "sha256:" + strings.Repeat("a", 64)}}}}
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "result.json")
	if err := writeComparisonJSON(privatePath, response); err != nil {
		t.Fatal(err)
	}
	var restored reviewerComparisonResponse
	if err := readComparisonJSON(privatePath, &restored); err != nil {
		t.Fatal(err)
	}
	var retained execution.ReviewAssessment
	if err := json.Unmarshal(restored.Assessment, &retained); err != nil || !reflect.DeepEqual(*assessment, retained) || restored.IndependentAdjudication != "pending" {
		t.Fatal("private assessment was lost, altered or labeled independently adjudicated")
	}
	if info, err := os.Stat(privatePath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("reviewer reasoning artifact is not private")
	}
	var console strings.Builder
	coordinator, err := newEvalCoordinator(comparisonSettings(filepath.Join(dir, "run.jsonl"), "candidate"), &console)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	result := coordinator.runCase(t.Context(), "codex", "reviewer", metadata.Name, func(context.Context) evalCaseResult {
		return comparisonWorkerResult(arm, metadata, restored)
	})
	if !result.ComparisonUsable || strings.Contains(console.String(), reasoning) || strings.Contains(console.String(), `"assessment"`) {
		t.Fatal("assessment evidence unavailable or leaked to console")
	}
	for _, test := range []struct {
		name       string
		assessment json.RawMessage
	}{
		{"missing", nil},
		{"malformed", json.RawMessage(`{"verdict":"accept"}`)},
		{"wrong verdict", comparisonAssessment(&execution.ReviewAssessment{Verdict: "needs_changes"})},
		{"oversized", json.RawMessage(strings.Repeat(" ", reviewerComparisonAssessmentBytes+1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := restored
			r.Assessment = test.assessment
			result := comparisonWorkerResult(arm, metadata, r)
			if result.ComparisonUsable || result.AdmissionStop == "" || result.ReviewJudgment != "expected_checks_match" || result.Outcome != "succeeded" {
				t.Fatal("uncertain assessment erased the observed result or counted as complete")
			}
		})
	}
	assessment.Summary = strings.Repeat("x", reviewerComparisonAssessmentBytes)
	if comparisonAssessment(assessment) != nil {
		t.Fatal("oversized assessment retained or silently truncated")
	}
	if validEvalReviewRecord(evalCaseRecord{ReviewJudgment: "correct"}) {
		t.Fatal("automatic record still claims adjudicated correctness")
	}
}

func TestReviewerComparisonCancellationSpendsCurrentPairWithoutAnotherAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	coordinator, err := newEvalCoordinator(comparisonSettings(path, "candidate"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := coordinator.runCase(ctx, "codex", "reviewer", "old:record_update", func(context.Context) evalCaseResult {
		cancel()
		return evalCaseResult{Outcome: "succeeded", Usage: metrics.Usage{Available: true, Coverage: metrics.UsageComplete, InputTokens: 1}}
	})
	if result.AdmissionStop == "" || result.Outcome != "succeeded" || len(coordinator.attempts) != 1 {
		t.Fatal("cancellation lost spent assignment or rewrote observed result")
	}
	coordinator.runCase(t.Context(), "codex", "reviewer", "revised:record_update", func(context.Context) evalCaseResult { t.Fatal("admitted after cancellation"); return evalCaseResult{} })
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), `"event":"started"`) != 1 {
		t.Fatal("non-admitted pair was recorded as executed")
	}
}

func TestReviewerComparisonManifestRejectsChangedModelSourceCorpusAndBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "reviewer.test")
	if err := os.WriteFile(binary, []byte("synthetic binary identity"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := reviewerComparisonManifest{Version: 1, HarnessDigest: strings.Repeat("b", 64), Model: reviewerComparisonModel, Reasoning: "medium",
		Arms: []reviewerComparisonArm{{Name: "old", Source: reviewerComparisonOldSource, Binary: binary, BinaryDigest: comparisonDigest([]byte("synthetic binary identity")), SkillDigest: strings.Repeat("c", 64)},
			{Name: "revised", Source: strings.Repeat("a", 40), Binary: binary, BinaryDigest: comparisonDigest([]byte("synthetic binary identity")), SkillDigest: strings.Repeat("d", 64)}}}
	for _, scenario := range reviewerComparisonScenarios(t) {
		manifest.Cases = append(manifest.Cases, reviewerComparisonCase{Name: scenario.name, ExpectedVerdict: scenario.wantVerdict, Candidate: strings.Repeat("e", 40), Base: strings.Repeat("f", 40), AssignmentDigest: strings.Repeat("1", 64)})
	}
	if err := validateComparisonManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*reviewerComparisonManifest)
	}{
		{"model", func(m *reviewerComparisonManifest) { m.Model = "other" }},
		{"reasoning", func(m *reviewerComparisonManifest) { m.Reasoning = "high" }},
		{"old source", func(m *reviewerComparisonManifest) { m.Arms[0].Source = strings.Repeat("9", 40) }},
		{"duplicate case", func(m *reviewerComparisonManifest) { m.Cases[1] = m.Cases[0] }},
		{"more cases", func(m *reviewerComparisonManifest) { m.Cases = append(m.Cases, m.Cases[0]) }},
		{"binary identity", func(m *reviewerComparisonManifest) { m.Arms[0].BinaryDigest = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, _ := json.Marshal(manifest)
			var changed reviewerComparisonManifest
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			test.change(&changed)
			if err := validateComparisonManifest(changed); err == nil {
				t.Fatal("changed comparison accepted")
			}
		})
	}
}
