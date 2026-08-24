package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	osexec "os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type canonicalizingPlannerRunner struct {
	calls   int
	allArgs [][]string
	inputs  []string
}

func (r *canonicalizingPlannerRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	case "codex":
		r.calls++
		r.allArgs = append(r.allArgs, append([]string(nil), args...))
		outputPath := plannerArgumentValue(args, "--output-last-message")
		if outputPath == "" {
			return subprocess.Result{}, errors.New("planner did not request a result file")
		}
		result, err := stagedPlannerFixtureResponse(args,
			`{"goal_summary":"Plan the feature","project_success_criteria":["The feature works."],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Implement feature","dependencies":[]}],"type":"object"}`,
			`{"cards":{"C1":{"objective":"Build the requested feature.","done_when":["The feature works."],"proof_obligations":["The feature works through its user entrypoint."],"assumptions":[]}}}`,
		)
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(outputPath, []byte(result), 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return subprocess.Result{}, errors.New("unexpected command: " + command)
	}
}

func (r *canonicalizingPlannerRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.inputs = append(r.inputs, string(data))
	return r.Run(ctx, command, args, dir, timeout)
}

func TestPlannerCanonicalizesKnownRepresentationResidueLocally(t *testing.T) {
	repo := t.TempDir()
	if output, err := osexec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if output, err := osexec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/owner/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("add Git remote: %v: %s", err, output)
	}
	run := &canonicalizingPlannerRunner{}
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	implementer := roles[config.WorkRoleImplementer]
	implementer.PlanningSupport = config.PlanningSupportHigh
	roles[config.WorkRoleImplementer] = implementer
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner",
		ProjectDir:    repo,
		Roles:         roles,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), run)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	plan, err := service.PlanProject(t.Context(), "Build one useful feature")
	if err != nil {
		t.Fatalf("plan project with local canonicalization: %v", err)
	}
	if run.calls != 2 || len(plan.WorkItems) != 1 || plan.WorkItems[0].Title != "Implement feature" || plan.WorkItems[0].Repository != "owner/repo" {
		t.Fatalf("expected one canonical project plan, calls=%d plan=%#v", run.calls, plan)
	}
	initialPrompt := run.inputs[0]
	for _, dynamic := range []string{"Configured implementer timeout: 2h0m0s", "- Implementer: high.", "- Reviewer: standard.", "Build one useful feature"} {
		if strings.Count(initialPrompt, dynamic) != 1 {
			t.Fatalf("planner prompt must contain dynamic context %q exactly once:\n%s", dynamic, initialPrompt)
		}
	}
	if strings.Count(initialPrompt, "Use these skills for this planner assignment: runner-planner.") != 1 {
		t.Fatalf("planner prompt omitted its trusted role selection:\n%s", initialPrompt)
	}
	for _, boundary := range []string{"--- BEGIN PROJECT IDEA ---", "--- END PROJECT IDEA ---"} {
		if !strings.Contains(initialPrompt, boundary) {
			t.Fatalf("planner prompt omitted approved-data boundary %q:\n%s", boundary, initialPrompt)
		}
	}
	for _, pinnedBoundary := range []string{"--- BEGIN RUNNER-PINNED SKILL ---", "--- END RUNNER-PINNED SKILL ---"} {
		if strings.Count(initialPrompt, pinnedBoundary) != 1 {
			t.Fatalf("planner prompt must contain one pinned skill boundary %q:\n%s", pinnedBoundary, initialPrompt)
		}
	}
}

func TestPlannerRejectsAmbiguousCandidate(t *testing.T) {
	base := `{"goal_summary":"Plan the feature","project_success_criteria":["The feature works."],"project_constraints":[],"open_decisions":[],"work_items":[{"title":"Implement feature","summary":"Build the requested feature.","acceptance_criteria":["The feature works."],"verification":["Run the feature through its user entrypoint."],"risks":[],"non_goals":[],"dependencies":[]}]`
	for _, extra := range []string{
		`"review_verdict":"accept"`,
		`"retry_policy":"automatic"`,
		`"failure_reason":"provider capacity"`,
		`"type":"blocked"`,
		`"format_hint":"remove"`,
	} {
		t.Run(extra, func(t *testing.T) {
			_, err := decodeProjectPlan(base + `,` + extra + `}`)
			if err == nil || !strings.Contains(err.Error(), "ambiguous extra field") {
				t.Fatalf("ambiguous planner candidate was projectable: %v", err)
			}
		})
	}
}

func TestProjectPlannerPromptPinsCanonicalRepository(t *testing.T) {
	prompt := projectPlannerPrompt([]string{"runner-planner"}, projectPlannerExecutionContext{ImplementerTimeout: 2 * time.Hour}, "owner/repo", "Build one useful feature")
	for _, required := range []string{
		"Canonical repository: owner/repo",
		"Runner binds it to every work item; do not return repository fields",
		"Inspect repository instructions, manifests, scripts, and existing tests before defining proof obligations",
		"Describe what must be proven, not the commands or test framework",
		"Treat optional technologies in the project idea as permission, not requirements or preferred defaults",
		"include a final project-readiness card when the complete result needs integration or release proof",
		"Do not invent a browser, deployment, or other interface requirement",
	} {
		if strings.Count(prompt, required) != 1 {
			t.Fatalf("planner prompt must contain %q exactly once:\n%s", required, prompt)
		}
	}
}

func TestProjectPlannerPromptIncludesOnlyOperatorSelectedHighSupport(t *testing.T) {
	standard := projectPlannerPrompt([]string{"runner-planner"}, projectPlannerExecutionContext{}, "owner/repo", "Build one useful feature")
	if strings.Contains(standard, "downstream task sizing") {
		t.Fatalf("standard planning support added redundant prompt text:\n%s", standard)
	}
	high := projectPlannerPrompt([]string{"runner-planner"}, projectPlannerExecutionContext{
		ImplementerSupport: config.PlanningSupportHigh,
		ReviewerSupport:    config.PlanningSupportStandard,
	}, "owner/repo", "Build one useful feature")
	for _, required := range []string{
		"Operator-selected downstream task sizing:",
		"- Implementer: high.",
		"- Reviewer: standard.",
		"Support affects decomposition and specificity, never correctness, scope, or verification rigor.",
	} {
		if strings.Count(high, required) != 1 {
			t.Fatalf("high planning support must contain %q exactly once:\n%s", required, high)
		}
	}
	if strings.Contains(strings.ToLower(high), "model") {
		t.Fatalf("planning support prompt inferred model capability:\n%s", high)
	}
}

func TestProjectPlanStagesLeaveRepositoryBindingToRunner(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(projectPlanOutlineSchema, &schema); err != nil {
		t.Fatal(err)
	}
	cards := schema["properties"].(map[string]any)["cards"].(map[string]any)
	item := cards["items"].(map[string]any)
	properties := item["properties"].(map[string]any)
	if _, exists := properties["repository"]; exists {
		t.Fatalf("model-facing outline schema retained Runner-owned repository: %s", projectPlanOutlineSchema)
	}
	for _, required := range item["required"].([]any) {
		if required == "repository" {
			t.Fatalf("model-facing outline schema still requires repository: %s", projectPlanOutlineSchema)
		}
	}
}

func TestNormalizeProjectPlanCanonicalizesDependencyRepresentation(t *testing.T) {
	plan := ProjectPlan{
		GoalSummary: "Ship the feature", ProjectSuccessCriteria: []string{"The feature works."},
		WorkItems: []github.PlannedItem{
			{Title: "Build API", Summary: "Build it", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}},
			{Title: "Add UI", Summary: "Add it", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}},
			{Title: "Integrate", Summary: "Join it", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{" add ui ", "BUILD API"}},
		},
	}
	normalized, err := normalizeProjectPlan(plan)
	if err != nil {
		t.Fatalf("normalize plan: %v", err)
	}
	want := []string{"Build API", "Add UI"}
	if !reflect.DeepEqual(normalized.WorkItems[2].Dependencies, want) {
		t.Fatalf("dependencies = %#v, want %#v", normalized.WorkItems[2].Dependencies, want)
	}
}

func TestNormalizeProjectPlanRejectsSelfAndDuplicateDependencies(t *testing.T) {
	base := func(dependencies []string) ProjectPlan {
		return ProjectPlan{
			GoalSummary: "Ship", ProjectSuccessCriteria: []string{"Works"},
			WorkItems: []github.PlannedItem{
				{Title: "Build", Summary: "Build", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}},
				{Title: "Verify", Summary: "Verify", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: dependencies},
			},
		}
	}
	for name, test := range map[string]struct {
		dependencies []string
		want         string
	}{
		"self":      {dependencies: []string{"Verify"}, want: "depend on itself"},
		"duplicate": {dependencies: []string{"Build", "build"}, want: "repeats dependency"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeProjectPlan(base(test.dependencies))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("dependencies %#v: error=%v, want %q", test.dependencies, err, test.want)
			}
		})
	}
}

func TestPlannerDoesNotRunWhenAdmissionReservationCannotBePersisted(t *testing.T) {
	repo := t.TempDir()
	if output, err := osexec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	if output, err := osexec.Command("git", "-C", repo, "remote", "add", "origin", "https://github.com/owner/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("add Git remote: %v: %s", err, output)
	}
	run := &canonicalizingPlannerRunner{}
	cfg := completeEngineTestConfig(config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner",
		ProjectDir:    repo,
		AdmissionBudget: &config.AdmissionBudgetConfig{
			WindowSeconds: 3600,
			MaxAttempts:   2,
		},
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	})
	service, err := New(cfg, run)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) { return metrics.ReadResult{}, nil })
	service.SetMetricsObserver(func(metrics.Event) error { return errors.New("disk full") })

	if _, err := service.PlanProject(t.Context(), "Build one useful feature"); err == nil || !strings.Contains(err.Error(), "persist admission reservation") {
		t.Fatalf("planner did not fail closed: %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("planner harness ran despite failed admission reservation: %d call(s)", run.calls)
	}
}

func TestDecodeProjectPlanRequiresCompleteTaskContract(t *testing.T) {
	tests := map[string]struct {
		workItem string
		want     string
	}{
		"missing verification": {
			workItem: `{"title":"Build","repository":"owner/repo","summary":"Build it","acceptance_criteria":["Works"],"risks":[],"non_goals":[],"dependencies":[]}`,
			want:     "incomplete",
		},
		"missing risks": {
			workItem: `{"title":"Build","repository":"owner/repo","summary":"Build it","acceptance_criteria":["Works"],"verification":["Run it"],"non_goals":[],"dependencies":[]}`,
			want:     "risks and non_goals arrays",
		},
		"missing non goals": {
			workItem: `{"title":"Build","repository":"owner/repo","summary":"Build it","acceptance_criteria":["Works"],"verification":["Run it"],"risks":[],"dependencies":[]}`,
			want:     "risks and non_goals arrays",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			value := `{"goal_summary":"Goal","project_success_criteria":["Works"],"project_constraints":[],"open_decisions":[],"work_items":[` + test.workItem + `]}`
			_, err := decodeProjectPlan(value)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeProjectPlanAcceptsWholeResponseJSONFence(t *testing.T) {
	value := "```json\n" + `{"goal_summary":"Goal","project_success_criteria":["The project works."],"project_constraints":[],"open_decisions":[],"work_items":[{"title":"Build","repository":"owner/repo","summary":"Build it","acceptance_criteria":["Works"],"verification":["Run the focused test."],"risks":[],"non_goals":[],"dependencies":[]}]}` + "\n```"
	plan, err := decodeProjectPlan(value)
	if err != nil || len(plan.WorkItems) != 1 {
		t.Fatalf("decode fenced project plan: plan=%#v error=%v", plan, err)
	}
}

func plannerArgumentValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}
