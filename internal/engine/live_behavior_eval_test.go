package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

type plannerEvalScenario struct {
	name              string
	idea              string
	repositoryContext string
	requiredConcepts  []plannerEvalConcept
}

type plannerEvalConcept struct {
	name         string
	alternatives []string
}

// Run the most demanding contract case first so a bad candidate fails before
// spending local or paid model budget on the simpler scenarios.
var plannerEvalCorpus = []plannerEvalScenario{
	{
		name: "idempotent_api",
		idea: `Add an authenticated HTTP endpoint that accepts a client idempotency key and creates one background export job. Repeated requests with the same authenticated tenant and key must return the original job without creating duplicates. Different tenants must remain isolated. Include focused concurrency and authorization verification.`,
		repositoryContext: `# Export service

This repository is a Go 1.25 HTTP service using net/http and PostgreSQL 16
through database/sql. Authentication middleware places an immutable tenant UUID
in the request context. SQL migrations live in migrations/, and export jobs are
persisted in the export_jobs table. Deployments use rolling replacement, so two
adjacent application versions can briefly serve requests together.
`,
		requiredConcepts: []plannerEvalConcept{
			{name: "idempotency", alternatives: []string{"idempot"}},
			{name: "authentication", alternatives: []string{"auth"}},
			{name: "tenant isolation", alternatives: []string{"tenant"}},
			{name: "concurrency", alternatives: []string{"concurr", "simultaneous", "parallel request"}},
			{name: "duplicate prevention", alternatives: []string{"duplicate", "exactly once", "single job"}},
		},
	},
	{
		name: "durable_cli",
		idea: `Create a small local command-line notes application. Users must be able to run "notes add TEXT", "notes list", and "notes remove ID" against durable local storage. Empty notes and invalid IDs need understandable errors. Keep it idiomatic and avoid accounts, networking, plugins, or a browser UI.`,
		repositoryContext: `# Notes CLI

This repository is a Go 1.25 command-line application with its entrypoint in
cmd/notes. It is a single-user local tool with no network access or external
services. The project currently has no persistence dependency, so a simple
standard-library storage format may be selected and documented.
`,
		requiredConcepts: []plannerEvalConcept{
			{name: "add command", alternatives: []string{"add", "create note"}},
			{name: "list command", alternatives: []string{"list", "show notes", "display notes"}},
			{name: "remove command", alternatives: []string{"remove", "delete note"}},
			{name: "durable storage", alternatives: []string{"durable", "persist", "storage", "on-disk", "disk", "file"}},
			{name: "invalid ID handling", alternatives: []string{"invalid id", "invalid ids", "unknown id", "unknown ids", "missing id", "not found", "nonexistent", "does not exist", "unknown note"}},
		},
	},
	{
		name: "safe_migration",
		idea: `Migrate an existing nullable customer timezone column to a required IANA timezone without downtime. Backfill existing null rows to UTC, preserve rollback safety, reject invalid new values, and verify both migrated data and the live write path. Retain legacy data needed for rollback and defer destructive cleanup. Do not introduce a new datastore or service.`,
		repositoryContext: `# Customer service

This repository is a Go 1.25 service using PostgreSQL 16 through database/sql.
Versioned up/down SQL migrations live in migrations/. The customers.timezone
column is currently nullable text, and the live application reads and writes
it. Deployments are rolling, with the previous and new binaries overlapping.
The service already uses time.LoadLocation for IANA timezone validation.
`,
		requiredConcepts: []plannerEvalConcept{
			{name: "backfill", alternatives: []string{"backfill"}},
			{name: "rollback", alternatives: []string{"rollback", "roll back"}},
			{name: "IANA timezone", alternatives: []string{"iana", "timezone"}},
			{name: "invalid value rejection", alternatives: []string{"invalid", "reject"}},
			{name: "migration", alternatives: []string{"migrat"}},
		},
	},
}

func TestPlannerBehaviorEvalCorpusIsSpecific(t *testing.T) {
	if len(plannerEvalCorpus) != 3 {
		t.Fatalf("planner eval corpus must contain exactly three planner scenarios: %d", len(plannerEvalCorpus))
	}
	if plannerEvalCorpus[0].name != "idempotent_api" {
		t.Fatalf("most demanding planner case must run first: %s", plannerEvalCorpus[0].name)
	}
	seen := map[string]bool{}
	for _, scenario := range plannerEvalCorpus {
		if strings.TrimSpace(scenario.name) == "" || strings.TrimSpace(scenario.idea) == "" || strings.TrimSpace(scenario.repositoryContext) == "" || len(scenario.requiredConcepts) < 4 || seen[scenario.name] {
			t.Fatalf("invalid planner eval scenario: %#v", scenario)
		}
		concepts := map[string]bool{}
		for _, concept := range scenario.requiredConcepts {
			if strings.TrimSpace(concept.name) == "" || len(concept.alternatives) == 0 || concepts[concept.name] {
				t.Fatalf("invalid planner eval concept in %s: %#v", scenario.name, concept)
			}
			concepts[concept.name] = true
		}
		seen[scenario.name] = true
	}
}

func TestPlannerBehaviorEvalSmokeUsesDemandingCase(t *testing.T) {
	full := plannerEvalScenarios(false)
	smoke := plannerEvalScenarios(true)
	if len(full) != 3 || len(smoke) != 1 {
		t.Fatalf("unexpected full/smoke corpus sizes: %d/%d", len(full), len(smoke))
	}
	if smoke[0].name != "idempotent_api" {
		t.Fatalf("smoke corpus must use the most demanding planner case: %s", smoke[0].name)
	}
}

func plannerEvalScenarios(smoke bool) []plannerEvalScenario {
	if smoke {
		return plannerEvalCorpus[:1]
	}
	return plannerEvalCorpus
}

func evalHarnessRoleAccess(kind string, settings evalSettings) (string, error) {
	if kind != config.HarnessPiCLI {
		return "", nil
	}
	if !settings.PiHostAccess {
		return "", errors.New("Pi implementer and reviewer evaluation lacks explicit host-access approval")
	}
	return config.RoleAccessHost, nil
}

func TestEvalHarnessRoleAccessRequiresExplicitPiHostApproval(t *testing.T) {
	if access, err := evalHarnessRoleAccess(config.HarnessClaudeCLI, evalSettings{}); err != nil || access != "" {
		t.Fatalf("Claude evaluation unexpectedly changed access: access=%q error=%v", access, err)
	}
	if _, err := evalHarnessRoleAccess(config.HarnessPiCLI, evalSettings{}); err == nil {
		t.Fatal("Pi evaluation accepted no host-access approval")
	}
	if access, err := evalHarnessRoleAccess(config.HarnessPiCLI, evalSettings{PiHostAccess: true}); err != nil || access != config.RoleAccessHost {
		t.Fatalf("Pi evaluation did not select host access: access=%q error=%v", access, err)
	}
}

// TestLiveRunnerBehaviorEval is the opt-in paid matrix. Full runs cover three
// planner cases, one implementer, and three independently specified reviewer
// candidates. Smoke keeps one planner and both a correct and faulty reviewer
// candidate. Normal go test runs skip all model calls.
func TestLiveRunnerBehaviorEval(t *testing.T) {
	requested := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_EVAL_HARNESSES"))
	if requested == "" {
		t.Skip("set CORTEXIUM_RUNNER_EVAL_HARNESSES through scripts/test-agent-behavior.sh")
	}
	settings, err := evalSettingsFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := newEvalCoordinator(settings, os.Stdout)
	if err != nil {
		t.Fatal(err)
	}
	passed := false
	defer func() {
		coordinator.finish(passed)
		if closeErr := coordinator.Close(); closeErr != nil {
			t.Errorf("close evaluation artifact: %v", closeErr)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), settings.AggregateTime)
	defer cancel()

	harnesses := compactEvalHarnesses(requested)
	smoke := os.Getenv("CORTEXIUM_RUNNER_EVAL_SMOKE") == "1"
	scenarios := plannerEvalScenarios(smoke)
	reviews := reviewerEvalScenarios(t, smoke)
	for _, kind := range harnesses {
		if !config.ValidHarnessKind(kind) {
			t.Fatalf("unsupported eval harness %q", kind)
		}
		for _, scenario := range scenarios {
			result := coordinator.runCase(ctx, kind, config.WorkRolePlanner, scenario.name, func(ctx context.Context) evalCaseResult {
				return runLivePlannerEval(ctx, t, kind, settings, scenario)
			})
			if result.Err != nil {
				if result.FailureStage == "required_concept" {
					t.Errorf("%s planner case %s failed (class=%s retry=%s): %v", kind, scenario.name, result.FailureClass, result.RetryDisposition, result.Err)
				} else {
					t.Errorf("%s planner case %s failed (class=%s retry=%s)", kind, scenario.name, result.FailureClass, result.RetryDisposition)
				}
				return
			}
		}
		result := coordinator.runCase(ctx, kind, config.WorkRoleImplementer, "exact_file_write", func(ctx context.Context) evalCaseResult {
			return runLiveImplementationEval(ctx, t, kind, settings)
		})
		if result.Err != nil {
			t.Errorf("%s implementer failed (class=%s retry=%s)", kind, result.FailureClass, result.RetryDisposition)
			return
		}
		for _, scenario := range reviews {
			result := coordinator.runCase(ctx, kind, config.WorkRoleReviewer, scenario.name, func(ctx context.Context) evalCaseResult {
				return runLiveReviewerEval(ctx, t, kind, settings, scenario)
			})
			if result.Err != nil {
				t.Errorf("%s reviewer case %s failed (judgment=%s class=%s)", kind, scenario.name, result.ReviewJudgment, result.FailureClass)
				if result.FailureStage != "reviewer_verdict" {
					return
				}
			}
		}
	}
	wantAttempts := len(harnesses) * (len(scenarios) + 1 + len(reviews))
	if len(coordinator.attempts) != wantAttempts {
		t.Fatalf("live matrix executed %d scenarios, want %d", len(coordinator.attempts), wantAttempts)
	}
	passed = !t.Failed()
}

func compactEvalHarnesses(value string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, raw := range strings.Split(value, ",") {
		kind := strings.TrimSpace(raw)
		if kind != "" && !seen[kind] {
			seen[kind] = true
			result = append(result, kind)
		}
	}
	return result
}

func runLivePlannerEval(ctx context.Context, t *testing.T, kind string, settings evalSettings, scenario plannerEvalScenario) evalCaseResult {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte(scenario.repositoryContext), 0o644); err != nil {
		return evalCaseResult{Err: err, FailureClass: string(execution.FailureInvalidConfiguration), RetryDisposition: string(execution.RetryNone)}
	}
	runGitTest(t, repo, "add", "README.md")
	runGitTest(t, repo, "commit", "-m", "seed representative repository context")
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	planner := roles[config.WorkRolePlanner]
	planner.Harness, planner.TimeoutSeconds, planner.Reasoning = kind, int(settings.CaseTimeout.Seconds()), settings.Reasoning
	if selected := settings.modelForHarness(kind); selected != "" {
		model := selected
		planner.Model = &model
	}
	roles[config.WorkRolePlanner] = planner
	reviewer := roles[config.WorkRoleReviewer]
	reviewer.Harness, reviewer.TimeoutSeconds, reviewer.Reasoning = kind, int(settings.CaseTimeout.Seconds()), settings.Reasoning
	if selected := settings.modelForHarness(kind); selected != "" {
		model := selected
		reviewer.Model = &model
	}
	roles[config.WorkRoleReviewer] = reviewer
	enabled := true
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo, Roles: roles,
		Harnesses:     []config.HarnessConfig{{Kind: kind, Command: kind, Enabled: &enabled, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees")}},
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	})
	service, err := New(cfg, nil)
	if err != nil {
		return evalCaseResult{Err: err, FailureClass: string(execution.FailureInvalidConfiguration), RetryDisposition: string(execution.RetryNone)}
	}
	var completed metrics.Event
	service.SetMetricsObserver(func(event metrics.Event) error {
		if event.Kind == metrics.EventCompleted {
			completed = event
		}
		return nil
	})
	plan, err := service.PlanProject(ctx, scenario.idea)
	result := evalCaseResult{
		Outcome: completed.Outcome, FailureClass: completed.FailureClass, RetryDisposition: completed.RetryDisposition, RetryAfter: completed.RetryAfter,
		HarnessDurationMilliseconds: completed.HarnessDurationMilliseconds, Usage: completed.Usage, Err: err,
	}
	if err != nil {
		result.FailureStage = "planner_execution"
		return result
	}
	if len(plan.OpenDecisions) != 0 {
		result.Outcome = execution.OutcomeBlocked
		result.FailureClass = string(execution.FailureInvalidContract)
		result.RetryDisposition = string(execution.RetryNone)
		result.FailureStage = "open_decisions"
		result.Err = fmt.Errorf("plan contains %d open decisions", len(plan.OpenDecisions))
		return result
	}
	if len(plan.WorkItems) == 0 || len(plan.WorkItems) > 8 {
		result.Outcome = execution.OutcomeBlocked
		result.FailureClass = string(execution.FailureInvalidContract)
		result.RetryDisposition = string(execution.RetryNone)
		result.FailureStage = "work_item_count"
		result.Err = fmt.Errorf("plan contains %d work items", len(plan.WorkItems))
		return result
	}
	encodedPlan, _ := json.Marshal(plan)
	searchable := strings.ToLower(string(encodedPlan))
	for _, concept := range scenario.requiredConcepts {
		found := false
		for _, alternative := range concept.alternatives {
			found = found || strings.Contains(searchable, strings.ToLower(alternative))
		}
		if !found {
			result.Outcome = execution.OutcomeBlocked
			result.FailureClass = string(execution.FailureInvalidContract)
			result.RetryDisposition = string(execution.RetryNone)
			result.FailureStage = "required_concept"
			result.Err = fmt.Errorf("plan lost required concept %q", concept.name)
			return result
		}
	}
	result.Outcome = execution.OutcomeSucceeded
	return result
}

func runLiveImplementationEval(ctx context.Context, t *testing.T, kind string, settings evalSettings) evalCaseResult {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	access, err := evalHarnessRoleAccess(kind, settings)
	if err != nil {
		return evalCaseResult{Outcome: execution.OutcomeBlocked, FailureClass: string(execution.FailureInvalidConfiguration), FailureStage: "implementation_execution", Err: err}
	}
	cfg := config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD", RoleAccess: access, Skills: []string{"runner-implementer"},
		Harness: config.HarnessConfig{Kind: kind, Command: kind, WorkingDir: repo, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: int(settings.CaseTimeout.Seconds()), ReasoningEffort: settings.Reasoning},
	}
	if model := settings.modelForHarness(kind); model != "" {
		cfg.Harness.Model = &model
	}
	assignment := execution.Assignment{Spec: execution.Spec{
		ID: "behavior_eval_implementation_" + kind, ItemID: "PVTI_behavior_eval_implementation_" + kind,
		Repository: "owner/repo", DelegatedContentDigest: "v1:behavior-eval-implementation",
		Task:                 execution.Task{Title: "Implement exact behavior fixture", Instructions: "Use the runner-implementer skill. Create behavior.txt containing exactly ready followed by a newline. Run a byte-exact content check and git diff --check. Make no other change."},
		RequiredVerification: []string{"behavior.txt contains exactly ready followed by a newline", "git diff --check passes"},
	}}
	var prepared workspace.Metadata
	capture := func(metadata workspace.Metadata) error { prepared = metadata; return nil }
	var output execution.Output
	if kind == config.HarnessCodexCLI {
		output, err = execution.NewCodexExecutor(cfg, nil).ExecuteWorkspaceWrite(ctx, assignment, capture)
	} else {
		output, err = execution.NewAgentExecutor(kind, cfg, nil).ExecuteWorkspaceWrite(ctx, assignment, capture)
	}
	result := evalCaseResult{Outcome: output.Outcome, FailureClass: string(output.FailureClass), RetryDisposition: string(output.RetryDisposition), RetryAfter: output.RetryAfter, HarnessDurationMilliseconds: output.HarnessDurationMilliseconds, Usage: output.Usage, Err: err}
	if err != nil || output.Outcome != execution.OutcomeSucceeded {
		result.Err = errors.Join(err, errors.New("implementation fixture failed"))
		result.FailureStage = "implementation_execution"
		return result
	}
	content, err := os.ReadFile(filepath.Join(prepared.WorktreePath, "behavior.txt"))
	if err != nil || string(content) != "ready\n" {
		result.Err = errors.New("implementation fixture content was invalid")
		result.Outcome, result.FailureClass, result.FailureStage = execution.OutcomeBlocked, string(execution.FailureInvalidContract), "fixture_content"
	}
	return result
}
