package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
)

var recordUpdateProofs = []string{
	"Updating an owned record changes only its title and preserves its tenant.",
	"Updates for another tenant, an unknown record, or a blank title return an error without changing stored records.",
}

type reviewerEvalScenario struct {
	name, source, tests, priorSource string
	wantVerdict                      string
	failedCriterion                  int
	wantOracleFailure                string
}

func reviewerEvalScenarios(t *testing.T, smoke bool) []reviewerEvalScenario {
	t.Helper()
	read := func(name string) string {
		t.Helper()
		content, err := os.ReadFile(filepath.Join("testdata", "reviewer", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	source, tests, shallow := read("records.go"), read("records_test.go"), read("shallow_test.go.txt")
	unguarded := strings.Replace(source, " || record.Tenant != tenant", "", 1)
	lostOwner := strings.Replace(source, "records[id] = record", "records[id] = Record{Title: title}", 1)
	cases := []reviewerEvalScenario{
		{name: "record_update", source: source, tests: tests, wantVerdict: "accept", failedCriterion: -1},
		{name: "record_update_access", source: unguarded, tests: shallow, wantVerdict: "needs_changes", failedCriterion: 1, wantOracleFailure: "TestUpdateRejectsInvalidRequests/foreign_tenant"},
		{name: "record_update_repair", source: lostOwner, tests: shallow, priorSource: unguarded, wantVerdict: "needs_changes", failedCriterion: 0, wantOracleFailure: "TestUpdatePreservesOwnership"},
	}
	if smoke {
		return cases[:2] // Both acceptance and rejection are needed to judge a reviewer.
	}
	return cases
}

func writeReviewerEvalFile(t *testing.T, repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func prepareReviewerEval(t *testing.T, scenario reviewerEvalScenario) (string, execution.Assignment) {
	t.Helper()
	// No remote, live service, browser, or generated test framework is needed.
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-b", "main")
	runGitTest(t, repo, "config", "user.name", "Test User")
	runGitTest(t, repo, "config", "user.email", "test@example.com")
	writeReviewerEvalFile(t, repo, "go.mod", "module example.com/records\n\ngo 1.25.0\n")
	instructions := "Implement record title editing in this Go backend. " + strings.Join(recordUpdateProofs, " ") +
		" The caller supplies the authenticated tenant; titles are stored as supplied. Concurrent map access is outside this component's contract."
	writeReviewerEvalFile(t, repo, "README.md", instructions+"\n")
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-m", "Describe record updates")
	base := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "record_update_review", ItemID: "record_update", Repository: "owner/repo",
		Task:                 execution.Task{Title: "Review record title editing", Instructions: instructions},
		ApprovedBodySnapshot: instructions, RequiredVerification: append([]string(nil), recordUpdateProofs...),
		ReviewRequired: true, ReviewBaseOID: base,
	}}
	if scenario.priorSource != "" {
		writeReviewerEvalFile(t, repo, "records.go", scenario.priorSource)
		writeReviewerEvalFile(t, repo, "records_test.go", scenario.tests)
		runGitTest(t, repo, "add", ".")
		runGitTest(t, repo, "commit", "-m", "Implement record updates")
		assignment.Spec.ReviewBaseline = &execution.ReviewBaseline{
			CommitOID: strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD")), BaseOID: base,
			CommentContext: []string{},
			Assessment: execution.ReviewAssessment{
				Verdict: "needs_changes", Summary: "Record updates do not enforce ownership.",
				Criteria: []execution.ReviewCriterionResult{
					{Criterion: recordUpdateProofs[0], Status: "passed", Summary: "Owned edits preserve the tenant.", Evidence: []string{"The current record is copied, its title is changed, and that record is stored."}},
					{Criterion: recordUpdateProofs[1], Status: "failed", Summary: "Another tenant can update an existing record.", Evidence: []string{"Update checks record existence and title but never compares the record tenant to the authenticated tenant."}},
				},
				Rules:           []execution.ReviewRuleResult{{RuleSourceID: "repository_instructions", RuleSourceVersion: "current", Status: "passed", Summary: "No additional repository rule violation.", Findings: []execution.ReviewRuleFinding{}}},
				Maintainability: execution.ReviewMaintainabilityResult{Status: "passed", Summary: "The function is direct.", Evidence: []string{"One update function with explicit validation and assignment."}},
			},
		}
		if err := execution.ValidateReviewBaseline(assignment.Spec, assignment.Spec.ReviewBaseline); err != nil {
			t.Fatal(err)
		}
	}
	writeReviewerEvalFile(t, repo, "records.go", scenario.source)
	writeReviewerEvalFile(t, repo, "records_test.go", scenario.tests)
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-m", "Complete record updates")
	assignment.Spec.ReviewCandidateOID = strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	return repo, assignment
}

func runReviewerFixtureTests(ctx context.Context, repo string) (string, time.Duration, error) {
	started := time.Now()
	command := exec.CommandContext(ctx, "go", "test", "-count=1", ".")
	command.Dir = repo
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
	output, err := command.CombinedOutput()
	return string(output), time.Since(started), err
}

// The reference tests use requirement-derived literal expectations, never the
// candidate to compute its own answer. Faults and oracle failures stay outside
// the repository visible to the reviewer.
func TestReviewerEvalCandidatesPassVisibleTestsAndExposeKnownFaults(t *testing.T) {
	oracle, err := os.ReadFile(filepath.Join("testdata", "reviewer", "records_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range reviewerEvalScenarios(t, false) {
		t.Run(scenario.name, func(t *testing.T) {
			repo, _ := prepareReviewerEval(t, scenario)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if output, _, err := runReviewerFixtureTests(ctx, repo); err != nil {
				t.Fatalf("visible tests must pass: %v\n%s", err, output)
			}
			if scenario.wantOracleFailure == "" {
				return // This candidate already includes the complete reference tests.
			}
			oracleRepo := t.TempDir()
			writeReviewerEvalFile(t, oracleRepo, "go.mod", "module example.com/records\n\ngo 1.25.0\n")
			writeReviewerEvalFile(t, oracleRepo, "records.go", scenario.source)
			writeReviewerEvalFile(t, oracleRepo, "records_test.go", string(oracle))
			output, _, err := runReviewerFixtureTests(ctx, oracleRepo)
			if err == nil || !strings.Contains(output, "--- FAIL: "+scenario.wantOracleFailure+" (") {
				t.Fatalf("oracle must detect %s, error=%v\n%s", scenario.wantOracleFailure, err, output)
			}
		})
	}
}

func reviewerEvalJudgment(scenario reviewerEvalScenario, assessment *execution.ReviewAssessment) string {
	if assessment == nil || assessment.Verdict == "blocked" {
		return "incomplete_review"
	}
	if assessment.Verdict != scenario.wantVerdict {
		if assessment.Verdict == "accept" {
			return "false_acceptance"
		}
		return "unnecessary_rejection"
	}
	if scenario.failedCriterion >= 0 {
		for _, criterion := range assessment.Criteria {
			if criterion.Criterion == recordUpdateProofs[scenario.failedCriterion] && criterion.Status == "failed" {
				return "correct"
			}
		}
		return "missed_defect"
	}
	return "correct"
}

func TestReviewerEvalJudgmentRequiresCorrectDecisionAndAffectedProof(t *testing.T) {
	for _, tc := range []struct {
		name, wantVerdict, verdict, wantJudgment string
		failedCriterion                          int
		criteria                                 []execution.ReviewCriterionResult
	}{
		{"accept correct", "accept", "accept", "correct", -1, nil},
		{"blanket rejection", "accept", "needs_changes", "unnecessary_rejection", -1, nil},
		{"accept defect", "needs_changes", "accept", "false_acceptance", 1, nil},
		{"unrelated rejection", "needs_changes", "needs_changes", "missed_defect", 1, nil},
		{"blocked is inconclusive", "needs_changes", "blocked", "incomplete_review", 1, nil},
		{"detect ownership fault", "needs_changes", "needs_changes", "correct", 1, []execution.ReviewCriterionResult{{Criterion: recordUpdateProofs[1], Status: "failed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scenario := reviewerEvalScenario{wantVerdict: tc.wantVerdict, failedCriterion: tc.failedCriterion}
			got := reviewerEvalJudgment(scenario, &execution.ReviewAssessment{Verdict: tc.verdict, Criteria: tc.criteria})
			if got != tc.wantJudgment {
				t.Fatalf("judgment = %q, want %q", got, tc.wantJudgment)
			}
		})
	}
}

func runLiveReviewerEval(ctx context.Context, t *testing.T, kind string, settings evalSettings, scenario reviewerEvalScenario) evalCaseResult {
	t.Helper()
	repo, assignment := prepareReviewerEval(t, scenario)
	result := evalCaseResult{ExpectedVerdict: scenario.wantVerdict}
	_, duration, err := runReviewerFixtureTests(ctx, repo)
	result.FixtureTestDurationMS = duration.Milliseconds()
	if err != nil {
		result.Err = errors.New("reviewer fixture visible tests failed")
		result.FailureClass, result.FailureStage = string(execution.FailureInvalidConfiguration), "fixture_content"
		return result
	}
	for _, criterion := range recordUpdateProofs {
		assignment.Spec.RecordedVerification = append(assignment.Spec.RecordedVerification, execution.VerificationEvidence{
			Criterion: criterion,
			Evidence:  fmt.Sprintf("go test -count=1 . passed at candidate %s in %d ms, with GOWORK=off, GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off. Inspect records_test.go to assess what these assertions establish.", assignment.Spec.ReviewCandidateOID, duration.Milliseconds()),
		})
	}
	access, err := evalHarnessRoleAccess(kind, settings)
	if err != nil {
		result.Err, result.FailureClass, result.FailureStage = err, string(execution.FailureInvalidConfiguration), "reviewer_execution"
		return result
	}
	cfg := config.ExecutionConfig{
		RoleAccess: access, Skills: []string{"runner-reviewer"},
		Harness: config.HarnessConfig{Kind: kind, Command: kind, WorkingDir: repo, TimeoutSeconds: int(settings.CaseTimeout.Seconds()), ReasoningEffort: settings.Reasoning},
	}
	if model := settings.modelForHarness(kind); model != "" {
		cfg.Harness.Model = &model
	}
	var output execution.Output
	if kind == config.HarnessCodexCLI {
		output, err = execution.NewCodexExecutor(cfg, nil).Execute(ctx, assignment)
	} else {
		output, err = execution.NewAgentExecutor(kind, cfg, nil).Execute(ctx, assignment)
	}
	result.Outcome, result.Err = output.Outcome, err
	result.FailureClass, result.RetryDisposition, result.RetryAfter = string(output.FailureClass), string(output.RetryDisposition), output.RetryAfter
	result.Usage, result.HarnessDurationMilliseconds = output.Usage, output.HarnessDurationMilliseconds
	if err != nil {
		result.FailureStage = "reviewer_execution"
		return result
	}
	if output.ReviewAssessment != nil {
		result.ObservedVerdict = output.ReviewAssessment.Verdict
	}
	result.ReviewJudgment = reviewerEvalJudgment(scenario, output.ReviewAssessment)
	if result.ReviewJudgment != "correct" {
		result.Err = errors.New("reviewer did not establish the expected judgment")
		result.Outcome, result.FailureClass, result.FailureStage = execution.OutcomeBlocked, string(execution.FailureInvalidContract), "reviewer_verdict"
	}
	head := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	if status := strings.TrimSpace(runGitTest(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" || head != assignment.Spec.ReviewCandidateOID {
		result.Err = errors.New("reviewer changed the candidate repository")
		result.Outcome, result.FailureClass, result.FailureStage = execution.OutcomeBlocked, string(execution.FailureIntegrityViolation), "reviewer_execution"
		result.ReviewJudgment = ""
	}
	return result
}
