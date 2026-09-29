package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
)

type reviewerCompletenessScenario struct {
	name, request, proof, baseSource, candidateSource, checks, oracle string
	verdict, requirements                                             string
	evidenceTerms                                                     []string
}

const updateBaseSource = `package fixture
import "strings"
type Store struct { Value string }
func (s *Store) Update(value string, persist func(string) error) error {
 next := strings.Clone(value)
 if err := persist(next); err != nil { return err }
 s.Value = next
 return nil
}
`
const updateChecks = `package fixture
import "testing"
func TestUpdateTrims(t *testing.T) {
 s := &Store{Value:"previous"}
 var written string
 if err := s.Update("  next  ", func(v string) error { written = v; return nil }); err != nil { t.Fatal(err) }
 if s.Value != "next" || written != "next" { t.Fatal("trimmed value not saved") }
}
`
const recoveryOracle = `package fixture
import ("errors"; "testing")
func TestOraclePreservesState(t *testing.T) {
 s := &Store{Value:"previous"}
 failure := errors.New("disk full")
 if err := s.Update("  next  ", func(string) error { return failure }); err != failure { t.Fatal("persistence error lost") }
 if s.Value != "previous" { t.Fatal("failed persistence changed visible state") }
}
`

// The proof lists deliberately omit a required boundary in the first two cases.
// Expected verdicts and oracle checks are evaluator-only; they never enter prompts.
var reviewerCompletenessCorpus = []reviewerCompletenessScenario{
	{
		name:       "review_missing_recovery",
		request:    "Trim leading and trailing whitespace before saving a value. Failed persistence must leave the previously visible value intact. Keep the current standard-library implementation.",
		proof:      "Successful updates store and expose the trimmed value.",
		baseSource: updateBaseSource,
		candidateSource: `package fixture
import "strings"
type Store struct { Value string }
func (s *Store) Update(value string, persist func(string) error) error {
 s.Value = strings.TrimSpace(value)
 return persist(s.Value)
}
`,
		checks: updateChecks, oracle: recoveryOracle,
		verdict: "needs_changes", requirements: "failed", evidenceTerms: []string{"persist", "previous", "rollback"},
	},
	{
		name:    "review_unchanged_consumer",
		request: "Add display_name to the encoded person payload for a new client. Preserve the existing Label consumer's behavior; it must still display the person's name.",
		proof:   "The encoded payload contains display_name with the person's name.",
		baseSource: `package fixture
func Encode(name string) map[string]string { return map[string]string{"name": name} }
`,
		candidateSource: `package fixture
func Encode(name string) map[string]string { return map[string]string{"display_name": name} }
`,
		checks: `package fixture
import "testing"
func TestNewField(t *testing.T) { if Encode("Ada")["display_name"] != "Ada" { t.Fatal("display_name missing") } }
`,
		oracle: `package fixture
import "testing"
func TestOracleConsumer(t *testing.T) { if Label(Encode("Ada")) != "Ada" { t.Fatal("existing consumer lost name") } }
`,
		verdict: "needs_changes", requirements: "failed", evidenceTerms: []string{"label", "[\"name\"]", "consumer"},
	},
	{
		name:            "review_conflicting_requirements",
		request:         "Trim outer whitespace when storing names. Store each name exactly as entered, preserving its letter case. Also store all names in lowercase. Neither constraint has priority; there is only one stored Value field. Do not add another representation.",
		proof:           "Lowercase names with outer whitespace are stored trimmed.",
		baseSource:      updateBaseSource,
		candidateSource: strings.Replace(updateBaseSource, "strings.Clone(value)", "strings.ToLower(strings.TrimSpace(value))", 1),
		checks:          updateChecks,
		verdict:         "needs_input", requirements: "needs_input", evidenceTerms: []string{"case", "lowercase", "conflict"},
	},
	{
		name:            "review_correct_simple_change",
		request:         "Trim leading and trailing whitespace before saving a value. Failed persistence must leave the previously visible value intact. Keep the current standard-library implementation. No concurrency, service, or new storage system is required.",
		proof:           "Successful updates store and expose the trimmed value.",
		baseSource:      updateBaseSource,
		candidateSource: strings.Replace(updateBaseSource, "strings.Clone(value)", "strings.TrimSpace(value)", 1),
		checks:          updateChecks, oracle: recoveryOracle,
		verdict: "accept", requirements: "passed",
	},
}

func prepareReviewerCompletenessEval(t *testing.T, scenario reviewerCompletenessScenario) (string, execution.Assignment) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module reviewfixture\n\ngo 1.25\n")
	write("fixture.go", scenario.baseSource)
	write("AGENTS.md", "Use the standard library and preserve behavior required by the approved request. Do not add unrelated features.\n")
	if scenario.name == "review_unchanged_consumer" {
		write("consumer.go", "package fixture\nfunc Label(payload map[string]string) string { return payload[\"name\"] }\n")
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Seed existing behavior")
	base := runGitTest(t, repo, "rev-parse", "HEAD")
	write("fixture.go", scenario.candidateSource)
	write("fixture_test.go", scenario.checks)
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "Implement requested behavior")
	head := runGitTest(t, repo, "rev-parse", "HEAD")
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "eval_" + scenario.name, Repository: "owner/repo", DelegatedContentDigest: "v1:review-eval-" + scenario.name,
		Task:                 execution.Task{Title: "Review requested behavior", Instructions: scenario.request},
		ApprovedBodySnapshot: scenario.request + "\n\n## Proof obligations\n- " + scenario.proof,
		RequiredVerification: []string{scenario.proof}, ReviewRequired: true,
		ReviewBaseOID: base, ReviewCandidateOID: head,
	}}
	return repo, assignment
}

func runLiveReviewerJudgmentEval(ctx context.Context, t *testing.T, kind string, settings evalSettings, scenario reviewerCompletenessScenario) evalCaseResult {
	t.Helper()
	repo, assignment := prepareReviewerCompletenessEval(t, scenario)
	access, err := evalHarnessRoleAccess(kind, settings)
	if err != nil {
		return evalCaseResult{Err: err, FailureClass: string(execution.FailureInvalidConfiguration)}
	}
	cfg := config.ExecutionConfig{Skills: []string{"runner-reviewer"}, RoleAccess: access,
		Harness: config.HarnessConfig{Kind: kind, Command: kind, WorkingDir: repo, TimeoutSeconds: int(settings.CaseTimeout.Seconds()), ReasoningEffort: settings.Reasoning}}
	if model := settings.modelForHarness(kind); model != "" {
		cfg.Harness.Model = &model
	}
	var output execution.Output
	if kind == config.HarnessCodexCLI {
		output, err = execution.NewCodexExecutor(cfg, nil).Execute(ctx, assignment)
	} else {
		output, err = execution.NewAgentExecutor(kind, cfg, nil).Execute(ctx, assignment)
	}
	result := evalCaseResult{Outcome: output.Outcome, FailureClass: string(output.FailureClass), RetryDisposition: string(output.RetryDisposition), RetryAfter: output.RetryAfter, HarnessDurationMilliseconds: output.HarnessDurationMilliseconds, Usage: output.Usage, Err: err, ExpectedVerdict: scenario.verdict}
	if output.ReviewAssessment != nil {
		result.ObservedVerdict = output.ReviewAssessment.Verdict
	}
	if err != nil {
		result.FailureStage = "reviewer_execution"
		return result
	}
	result.ReviewJudgment = "expected_checks_match"
	if err := judgeReviewerEval(scenario, output); err != nil {
		result.ReviewJudgment = "unexpected_findings"
		result.Err, result.FailureStage = err, "reviewer_verdict"
		result.FailureClass, result.RetryDisposition = string(execution.FailureInvalidContract), string(execution.RetryNone)
	}
	return result
}

func judgeReviewerEval(scenario reviewerCompletenessScenario, output execution.Output) error {
	assessment := output.ReviewAssessment
	wantOutcome := execution.OutcomeSucceeded
	if scenario.verdict == "needs_input" {
		wantOutcome = execution.OutcomeNeedsInput
	}
	if output.Outcome != wantOutcome || assessment == nil || assessment.Verdict != scenario.verdict || assessment.Requirements.Status != scenario.requirements {
		return errors.New("reviewer did not make the expected requirements decision")
	}
	if len(assessment.Criteria) != 1 || assessment.Criteria[0].Criterion != scenario.proof || assessment.Criteria[0].Status != "passed" || len(assessment.Rules) != 1 || assessment.Rules[0].Status != "passed" || assessment.Maintainability.Status != "passed" {
		return errors.New("reviewer invented an unrelated failure or omitted a review area")
	}
	if len(scenario.evidenceTerms) > 0 {
		finding := strings.ToLower(assessment.Requirements.Summary + " " + strings.Join(assessment.Requirements.Evidence, " "))
		matched := false
		for _, term := range scenario.evidenceTerms {
			matched = matched || strings.Contains(finding, term)
		}
		if !matched {
			return errors.New("requirements finding did not identify the relevant boundary")
		}
	}
	return nil
}

func TestReviewerEvalFixturesExposeOmissionsWithoutFailingListedChecks(t *testing.T) {
	for _, scenario := range reviewerCompletenessCorpus {
		t.Run(scenario.name, func(t *testing.T) {
			repo, assignment := prepareReviewerCompletenessEval(t, scenario)
			run := func() (string, error) {
				command := exec.CommandContext(t.Context(), "go", "test", "-count=1", "./...")
				command.Dir = repo
				data, err := command.CombinedOutput()
				return string(data), err
			}
			if data, err := run(); err != nil {
				t.Fatalf("listed proof is not green: %v\n%s", err, data)
			}
			if assignment.Spec.ReviewBaseOID == assignment.Spec.ReviewCandidateOID {
				t.Fatal("fixture has no candidate delta")
			}
			if scenario.oracle == "" {
				return
			}
			if err := os.WriteFile(filepath.Join(repo, "oracle_test.go"), []byte(scenario.oracle), 0o600); err != nil {
				t.Fatal(err)
			}
			data, err := run()
			if scenario.verdict == "accept" {
				if err != nil {
					t.Fatalf("correct control does not preserve behavior: %v\n%s", err, data)
				}
			} else if err == nil || !strings.Contains(data, "--- FAIL: TestOracle") {
				t.Fatalf("omission fixture did not expose its specific defect: %v\n%s", err, data)
			}
		})
	}
}

func TestReviewerEvalRejectsMissesAndFalseAlarms(t *testing.T) {
	for _, scenario := range reviewerCompletenessCorpus {
		outcome := execution.OutcomeSucceeded
		if scenario.verdict == "needs_input" {
			outcome = execution.OutcomeNeedsInput
		}
		evidence := "Source proves required behavior."
		if len(scenario.evidenceTerms) > 0 {
			evidence = scenario.evidenceTerms[0]
		}
		output := execution.Output{Outcome: outcome, ReviewAssessment: &execution.ReviewAssessment{Verdict: scenario.verdict, Requirements: execution.ReviewCheckResult{Status: scenario.requirements, Evidence: []string{evidence}}, Criteria: []execution.ReviewCriterionResult{{Criterion: scenario.proof, Status: "passed"}}, Rules: []execution.ReviewRuleResult{{Status: "passed"}}, Maintainability: execution.ReviewCheckResult{Status: "passed"}}}
		if err := judgeReviewerEval(scenario, output); err != nil {
			t.Fatal(err)
		}
		output.ReviewAssessment.Maintainability.Status = "failed"
		if judgeReviewerEval(scenario, output) == nil {
			t.Fatalf("%s accepted an invented maintainability defect", scenario.name)
		}
		output.ReviewAssessment.Maintainability.Status = "passed"
		if scenario.verdict == "accept" {
			output.ReviewAssessment.Verdict = "needs_changes"
		} else {
			output.ReviewAssessment.Verdict = "accept"
		}
		if err := judgeReviewerEval(scenario, output); err == nil {
			t.Fatalf("%s accepted wrong judgment", scenario.name)
		}
	}
}
