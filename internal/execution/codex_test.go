package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestParseStructuredExecutionResultAcceptsEveryProcessOutcome(t *testing.T) {
	for _, outcome := range []string{
		OutcomeSucceeded,
		OutcomeNeedsInput,
		OutcomeBlocked,
	} {
		t.Run(outcome, func(t *testing.T) {
			blocker := "null"
			if outcome == OutcomeNeedsInput || outcome == OutcomeBlocked {
				blocker = `"operator input is required"`
			}
			result, err := parseStructuredExecutionResult(`{"outcome":"` + outcome + `","summary":"done","work_done":["checked"],"verification":["test check completed"],"blocker":` + blocker + `,"review_assessment":null}`)
			if err != nil {
				t.Fatalf("parse result: %v", err)
			}
			if result.Outcome != outcome || result.Summary != "done" || len(result.WorkDone) != 1 {
				t.Fatalf("unexpected parsed result %#v", result)
			}
		})
	}
}

func TestCodexInvocationMCPOverridesFollowUserConfigIsolation(t *testing.T) {
	profile, err := ProfileForRole(RoleReviewer, config.RoleAccessHost)
	if err != nil {
		t.Fatal(err)
	}
	executor := NewCodexExecutor(config.ExecutionConfig{
		HarnessConfigMode: config.HarnessConfigModeIsolated,
		Harness:           config.HarnessConfig{Kind: config.HarnessCodexCLI, Command: "codex", ReasoningEffort: "high"},
	}, nil)
	workspace := profileWorkspace{Dir: "/neutral"}
	mcpArgs := []string{"--config", `mcp_servers={runner_browser={command="npx"}}`}
	for name, args := range map[string][]string{
		"read only":       executor.args(profile, workspace, mcpArgs, "/result", "/schema", Assignment{}),
		"workspace write": executor.profileWorkspaceWriteArgs(profile, workspace, mcpArgs, "/result", "/schema", Assignment{}),
	} {
		isolationIndex := slices.Index(args, "--ignore-user-config")
		mcpIndex := slices.Index(args, mcpArgs[1])
		if isolationIndex < 0 || mcpIndex <= isolationIndex {
			t.Fatalf("%s MCP override must follow --ignore-user-config: %#v", name, args)
		}
	}
}

func TestCodexInvocationPreservesMaxReasoningWithoutRemapping(t *testing.T) {
	model := "gpt-5.6-luna"
	executor := NewCodexExecutor(config.ExecutionConfig{
		HarnessConfigMode: config.HarnessConfigModeIsolated,
		Harness: config.HarnessConfig{
			Kind: config.HarnessCodexCLI, Command: "codex", Model: &model, ReasoningEffort: "max",
		},
	}, nil)
	for _, role := range []RoleContract{RoleReviewer, RoleImplementer} {
		profile, err := ProfileForRole(role, config.RoleAccessSandboxed)
		if err != nil {
			t.Fatal(err)
		}
		workspace := profileWorkspace{Dir: "/workspace"}
		args := executor.args(profile, workspace, nil, "/result", "/schema", Assignment{})
		if role == RoleImplementer {
			args = executor.profileWorkspaceWriteArgs(profile, workspace, nil, "/result", "/schema", Assignment{})
		}
		if !containsArgPair(args, "--model", model) || !containsArgPair(args, "-c", `model_reasoning_effort="max"`) {
			t.Fatalf("%s lost model/effort selection: %v", role, args)
		}
	}
}

func TestExecutionContentSchemaIsSmallStrictAndShared(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(executionContentSchema, &schema); err != nil {
		t.Fatalf("decode execution content schema: %v", err)
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 5 {
		t.Fatalf("expected exactly five schema properties, got %#v", schema["properties"])
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != len(properties) {
		t.Fatalf("expected every property to be required, got %#v", schema["required"])
	}
	requiredSet := make(map[string]bool, len(required))
	for _, value := range required {
		name, ok := value.(string)
		if !ok {
			t.Fatalf("required property name must be a string, got %#v", value)
		}
		requiredSet[name] = true
	}
	for name := range properties {
		if !requiredSet[name] {
			t.Fatalf("schema property %q is not required", name)
		}
	}

	blockers, ok := properties["blockers"].(map[string]any)
	if !ok || blockers["type"] != "array" || blockers["maxItems"] != float64(1) {
		t.Fatalf("expected one bounded blockers array, got %#v", properties["blockers"])
	}
	for _, forbidden := range []string{"blocker", "review_assessment", "verdict", "criteria", "rules"} {
		if _, exists := properties[forbidden]; exists {
			t.Fatalf("execution content schema retained Runner-owned field %q", forbidden)
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("expected additionalProperties false, got %#v", schema["additionalProperties"])
	}

	unsupported := map[string]bool{
		"allOf":             true,
		"dependentRequired": true,
		"dependentSchemas":  true,
		"else":              true,
		"if":                true,
		"not":               true,
		"then":              true,
	}
	var rejectUnsupportedKeywords func(any)
	rejectUnsupportedKeywords = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, nested := range value {
				if unsupported[key] {
					t.Fatalf("unsupported schema keyword %q is present", key)
				}
				rejectUnsupportedKeywords(nested)
			}
		case []any:
			for _, nested := range value {
				rejectUnsupportedKeywords(nested)
			}
		}
	}
	rejectUnsupportedKeywords(schema)
}

func TestExecutionContentSchemaCapsVerificationAtApprovedCheckCount(t *testing.T) {
	for _, approvedChecks := range []int{1, 3} {
		var schema map[string]any
		if err := json.Unmarshal(executionContentSchemaForVerification(approvedChecks), &schema); err != nil {
			t.Fatalf("decode execution content schema for %d checks: %v", approvedChecks, err)
		}
		properties := schema["properties"].(map[string]any)
		verification := properties["verification"].(map[string]any)
		if got := verification["maxItems"]; got != float64(approvedChecks) {
			t.Fatalf("verification maxItems = %#v, want %d", got, approvedChecks)
		}
		if _, constrained := verification["minItems"]; constrained {
			t.Fatal("verification minItems would prevent a blocked outcome from returning no evidence")
		}
	}
}

func TestStructuredExecutionResultMarshalsAbsentBlockerAsNull(t *testing.T) {
	value, err := json.Marshal(StructuredExecutionResult{
		Outcome:      OutcomeSucceeded,
		Summary:      "done",
		WorkDone:     []string{"checked"},
		Verification: []string{"test check completed"},
	})
	if err != nil {
		t.Fatalf("marshal structured result: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(value, &fields); err != nil {
		t.Fatalf("decode marshaled structured result: %v", err)
	}
	if blocker, ok := fields["blocker"]; !ok || blocker != nil {
		t.Fatalf("expected explicit null blocker, got %s", value)
	}
}

func TestParseStructuredExecutionResultRejectsInvalidResults(t *testing.T) {
	tests := map[string]string{
		"plain text":                    "completed",
		"missing review assessment":     `{"outcome":"succeeded","summary":"done","work_done":[],"blocker":null}`,
		"missing verification":          `{"outcome":"succeeded","summary":"done","work_done":["changed"],"blocker":null,"review_assessment":null}`,
		"successful without checks":     `{"outcome":"succeeded","summary":"done","work_done":["changed"],"verification":[],"blocker":null,"review_assessment":null}`,
		"empty verification entry":      `{"outcome":"succeeded","summary":"done","work_done":["changed"],"verification":[" "],"blocker":null,"review_assessment":null}`,
		"unknown field":                 `{"outcome":"succeeded","summary":"done","work_done":[],"retry":true}`,
		"unknown outcome":               `{"outcome":"maybe","summary":"done","work_done":[]}`,
		"missing work done":             `{"outcome":"succeeded","summary":"done"}`,
		"empty summary":                 `{"outcome":"succeeded","summary":" ","work_done":[]}`,
		"empty work item":               `{"outcome":"succeeded","summary":"done","work_done":[" "]}`,
		"multiple values":               `{"outcome":"succeeded","summary":"done","work_done":[]} {}`,
		"needs input without blocker":   `{"outcome":"needs_input","summary":"waiting","work_done":[]}`,
		"needs input with null blocker": `{"outcome":"needs_input","summary":"waiting","work_done":[],"blocker":null}`,
		"blocked without blocker":       `{"outcome":"blocked","summary":"waiting","work_done":[]}`,
		"blocked with empty blocker":    `{"outcome":"blocked","summary":"waiting","work_done":[],"blocker":" "}`,
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseStructuredExecutionResult(value); err == nil {
				t.Fatalf("expected %q to be rejected", value)
			}
		})
	}
}

func TestHarnessPromptsUseDynamicTaskContextExactlyOnce(t *testing.T) {
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]
	resolved := "AGENT_ONLY TEAM_ONLY MEMBER_ONLY BINDING_ONLY TASK_ONLY"
	title := "UNIQUE_RUNTIME_TITLE"
	contextRef := "https://example.test/unique-context-ref"
	bodySnapshot := "UNIQUE_APPROVED_BODY_SNAPSHOT"
	contentDigest := "v1:unique-delegated-content-digest"
	assignment.Spec.Task.Instructions = resolved
	assignment.Spec.Task.Title = title
	assignment.Spec.ContextRefs = []string{contextRef}
	assignment.Spec.ApprovedBodySnapshot = bodySnapshot
	assignment.Spec.DelegatedContentDigest = contentDigest

	for name, prompt := range map[string]string{
		"codex-read-only":        buildCodexPrompt(assignment),
		"codex-workspace-write":  buildWorkspaceWriteCodexPrompt(assignment),
		"claude-read-only":       buildHarnessPrompt(assignment, false, "Claude Code"),
		"claude-workspace-write": buildHarnessPrompt(assignment, true, "Claude Code"),
		"pi-read-only":           buildHarnessPrompt(assignment, false, "Pi CLI"),
		"pi-workspace-write":     buildHarnessPrompt(assignment, true, "Pi CLI"),
	} {
		t.Run(name, func(t *testing.T) {
			for _, dynamic := range append(strings.Fields(resolved), title, contextRef, bodySnapshot, contentDigest) {
				if strings.Count(prompt, dynamic) != 1 {
					t.Fatalf("expected dynamic context %q exactly once in prompt:\n%s", dynamic, prompt)
				}
			}
			if !strings.Contains(prompt, "Set blockers to [] for succeeded. Set blockers to exactly one non-empty reason for needs_input or blocked.") {
				t.Fatalf("prompt must explain the required blockers array:\n%s", prompt)
			}
		})
	}
}

func TestWorkspaceWritePromptsDelegateProofMethodWithoutAgentExpansion(t *testing.T) {
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]
	assignment.Spec.RequiredVerification = []string{"Run the existing focused test; it passes."}
	for name, prompt := range map[string]string{
		"codex":  buildWorkspaceWriteCodexPrompt(assignment),
		"claude": buildHarnessPrompt(assignment, true, "Claude Code"),
		"pi":     buildHarnessPrompt(assignment, true, "Pi CLI"),
	} {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"Runner-owned proof obligations:",
				"These obligations define what must be proved, not how.",
				"choose the smallest reliable proof method",
				"add or update durable tests when that is the simplest reliable regression protection",
				"Do not create a second test framework",
				"Do not substitute broader checks",
				"return exactly 1 verification evidence entry",
				"Combine related observations for the same obligation",
			} {
				if strings.Count(prompt, required) != 1 {
					t.Fatalf("prompt must contain %q exactly once:\n%s", required, prompt)
				}
			}
		})
	}
}

func TestCodexCLIReviewerAcceptsCompletedNeedsChangesReview(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	packet.ReviewRequired = true
	packet.RequiredVerification = []string{"behavior.txt contains ready", "git diff --check passes"}
	assignment := Assignment{Spec: packet}
	runner := &structuredResultCommandRunner{rawResult: []byte(failingReviewerContent())}
	cfg := testCodexConfig(t)
	executor := NewCodexExecutor(cfg, runner)

	output, err := executor.Execute(t.Context(), assignment)
	if err != nil {
		t.Fatalf("assemble reviewer observations: %v", err)
	}
	if output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil ||
		output.ReviewAssessment.Verdict != "needs_changes" {
		t.Fatalf("expected completed non-passing assessment, got %#v", output)
	}
	if runner.calls != 1 {
		t.Fatalf("completed review should not need repair, calls=%d", runner.calls)
	}
}

func TestCodexCLIReviewerRejectsAmbiguousAssessmentWithoutRepair(t *testing.T) {
	packet := testCodexCLIAssignmentSpec()
	packet.ReviewRequired = true
	assignment := Assignment{Spec: packet}
	assessment := passingMockReviewAssessment(packet)
	assessment.Rules[0].RuleSourceID = "rules_wrong"

	runner := &structuredResultCommandRunner{result: StructuredExecutionResult{
		Outcome:          OutcomeSucceeded,
		Summary:          "Review attempted.",
		WorkDone:         []string{"Reviewed the bounded change."},
		Verification:     []string{"Inspected the complete change against the review contract."},
		ReviewAssessment: assessment,
	}}
	executor := NewCodexExecutor(testCodexConfig(t), runner)

	output, err := executor.Execute(t.Context(), assignment)
	if err == nil {
		t.Fatal("expected structurally invalid reviewer evidence to fail")
	}
	if runner.calls != 1 || output.Outcome != OutcomeBlocked || output.ReviewAssessment != nil {
		t.Fatalf("expected ambiguous reviewer authority to block without repair, calls=%d output=%#v", runner.calls, output)
	}
}

func TestCodexCLIExecutorReturnsEveryStructuredOutcome(t *testing.T) {
	for _, outcome := range []string{
		OutcomeSucceeded,
		OutcomeNeedsInput,
		OutcomeBlocked,
	} {
		t.Run(outcome, func(t *testing.T) {
			var blocker *string
			if outcome == OutcomeNeedsInput || outcome == OutcomeBlocked {
				blocker = ptr("operator input is required")
			}
			runner := &structuredResultCommandRunner{result: StructuredExecutionResult{
				Outcome:      outcome,
				Summary:      "structured " + outcome,
				WorkDone:     []string{"captured evidence"},
				Verification: []string{"test check completed"},
				Blocker:      blocker,
			}}
			cfg := testCodexConfig(t)
			executor := NewCodexExecutor(cfg, runner)
			assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]

			output, err := executor.Execute(t.Context(), assignment)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if output.Outcome != outcome || output.Summary != "structured "+outcome || len(output.WorkDone) != 1 {
				t.Fatalf("unexpected output %#v", output)
			}
			if runner.calls != 1 {
				t.Fatalf("expected one Codex invocation, got %d", runner.calls)
			}
			assertApprovedCodexArgs(t, runner.args)
		})
	}
}

func TestModelAuthoredBlockedTextHasNoRecoveryAuthority(t *testing.T) {
	blocker := "You've hit your session limit; retry after 10:40 with token=secret"
	runner := &structuredResultCommandRunner{result: StructuredExecutionResult{
		Outcome: OutcomeBlocked, Summary: blocker, WorkDone: []string{}, Verification: []string{}, Blocker: &blocker,
	}}
	output, err := NewCodexExecutor(testCodexConfig(t), runner).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	if err != nil {
		t.Fatalf("schema-valid blocked result should remain local task output: %v", err)
	}
	if output.Outcome != OutcomeBlocked || output.FailureClass != FailureNone || output.RetryDisposition != "" || output.RetryAfter != "" || output.RemoteDetailSafe {
		t.Fatalf("model-authored text gained trusted recovery authority: %#v", output)
	}
}

func TestCodexCLIUsesNativeDefaultModelWhenNoneIsConfigured(t *testing.T) {
	run := &structuredResultCommandRunner{result: StructuredExecutionResult{
		Outcome: OutcomeSucceeded, Summary: "done", WorkDone: []string{"checked"}, Verification: []string{"test check completed"},
	}}
	cfg := testCodexConfig(t)
	cfg.Harness.Model = nil
	executor := NewCodexExecutor(cfg, run)
	if _, err := executor.Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]); err != nil {
		t.Fatalf("execute with native model default: %v", err)
	}
	if contains(run.args, "--model") {
		t.Fatalf("native Codex model should not be overridden: %#v", run.args)
	}
}

func TestCodexPromptMakesTheJSONOnlyBoundaryExplicit(t *testing.T) {
	prompt := buildCodexPrompt(testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	for _, required := range []string{"exactly one JSON object", "Do not use Markdown", "Set blockers to [] for succeeded", "Do not return blocker or review_assessment fields"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt omitted %q: %s", required, prompt)
		}
	}
}

func TestCodexCLIExecutorFailsClosedWithoutModelFallback(t *testing.T) {
	runner := &structuredResultCommandRunner{
		commandResult: subprocess.Result{Stderr: "model configured-model is unavailable", ExitCode: 1},
		err:           errors.New("exit status 1"),
	}
	cfg := testCodexConfig(t)
	executor := NewCodexExecutor(cfg, runner)
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]

	output, err := executor.Execute(t.Context(), assignment)
	if err == nil {
		t.Fatal("expected model entitlement failure")
	}
	if runner.calls != 1 {
		t.Fatalf("expected one Codex invocation and no fallback, got %d", runner.calls)
	}
	if output.Outcome != OutcomeBlocked || output.Blocker == nil || !strings.Contains(output.Summary, "configured-model") {
		t.Fatalf("expected safe blocked evidence, got %#v", output)
	}
	assertApprovedCodexArgs(t, runner.args)
}

func TestCodexCLIExecutorClassifiesTerminalProviderFailureForAutomaticRetry(t *testing.T) {
	runner := &structuredResultCommandRunner{
		commandResult: subprocess.Result{
			Stdout:   `{"type":"turn.failed","error":{"message":"unexpected status 404 Not Found: Unknown error, url: https://chatgpt.com/backend-api/codex/responses, token=private"}}`,
			ExitCode: 1,
		},
		err: errors.New("exit status 1"),
	}
	output, err := NewCodexExecutor(testCodexConfig(t), runner).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	if err == nil {
		t.Fatal("expected terminal provider failure")
	}
	if output.FailureClass != FailureTransientExternal || output.RetryDisposition != RetryAutomatic || !output.RemoteDetailSafe || !output.DiscardDiagnostics {
		t.Fatalf("provider failure classification = %#v", output)
	}
	if strings.Contains(output.Summary, "token") || output.Blocker == nil || strings.Contains(*output.Blocker, "token") {
		t.Fatalf("provider diagnostic escaped fixed recovery output: %#v", output)
	}
}

func TestCodexCLIExecutorFailsClosedOnMalformedStructuredResult(t *testing.T) {
	runner := &structuredResultCommandRunner{rawResult: []byte(`{"outcome":"needs_input","summary":"waiting","work_done":[]}`)}
	cfg := testCodexConfig(t)
	executor := NewCodexExecutor(cfg, runner)
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]

	output, err := executor.Execute(t.Context(), assignment)
	if err == nil {
		t.Fatal("expected malformed structured result error")
	}
	if runner.calls != 1 || output.Outcome != OutcomeBlocked || output.Blocker == nil {
		t.Fatalf("expected missing substantive fields to block without correction, got calls=%d output=%#v", runner.calls, output)
	}
}

func assertApprovedCodexArgs(t *testing.T, args []string) {
	t.Helper()
	if !containsArgPair(args, "--model", "configured-model") {
		t.Fatalf("expected configured model in args %#v", args)
	}
	if !containsArgPair(args, "-c", `model_reasoning_effort="medium"`) {
		t.Fatalf("expected explicit reasoning effort in args %#v", args)
	}
	if execIndex, effortIndex, modelIndex := slices.Index(args, "exec"), slices.Index(args, "-c"), slices.Index(args, "--model"); execIndex < 0 || effortIndex < 0 || modelIndex < 0 || effortIndex > execIndex || modelIndex > execIndex {
		t.Fatalf("model config must be applied before exec so it cannot replace permission and MCP overrides: %#v", args)
	}
	for _, expected := range [][2]string{{"--ask-for-approval", config.CodexApprovalNever}, {"--config", `default_permissions="runner_repository_read"`}} {
		if !containsArgPair(args, expected[0], expected[1]) {
			t.Fatalf("Codex invocation omitted fixed execution profile %q %q in args %#v", expected[0], expected[1], args)
		}
	}
	if contains(args, "--sandbox") || !contains(args, "--strict-config") {
		t.Fatalf("Codex repository-read invocation did not require its scoped permission profile: %#v", args)
	}
	for _, forbidden := range []string{"shell_environment_policy.inherit="} {
		if strings.Contains(strings.Join(args, " "), forbidden) {
			t.Fatalf("Codex invocation added unrelated configuration %q in args %#v", forbidden, args)
		}
	}
	if !contains(args, "--output-schema") {
		t.Fatalf("expected output schema in args %#v", args)
	}
}

type structuredResultCommandRunner struct {
	result        StructuredExecutionResult
	results       []StructuredExecutionResult
	rawResult     []byte
	rawResults    [][]byte
	commandResult subprocess.Result
	err           error
	calls         int
	args          []string
	allArgs       [][]string
	dirs          []string
	inputs        []string
}

type substitutingCodexResultRunner struct {
	target string
	moved  string
}

func (r *substitutingCodexResultRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	flag := "--output-last-message"
	if r.target == "schema" {
		flag = "--output-schema"
	}
	for index := 0; index+1 < len(args); index++ {
		if args[index] != flag {
			continue
		}
		path := args[index+1]
		r.moved = path + ".moved"
		if err := os.Rename(path, r.moved); err != nil {
			return subprocess.Result{}, err
		}
		content := testStructuredResult("substituted result")
		if r.target == "schema" {
			content = executionContentSchema
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return subprocess.Result{}, err
		}
	}
	return subprocess.Result{}, nil
}

func TestCodexExecutorRejectsResultAndSchemaPathSubstitution(t *testing.T) {
	for _, target := range []string{"result", "schema"} {
		t.Run(target, func(t *testing.T) {
			runner := &substitutingCodexResultRunner{target: target}
			executor := NewCodexExecutor(testCodexConfig(t), runner)
			output, err := executor.Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
			if err == nil || output.Outcome == OutcomeSucceeded {
				t.Fatalf("substituted %s was accepted: output=%#v error=%v", target, output, err)
			}
			if runner.moved != "" {
				_ = os.Remove(runner.moved)
				_ = os.Remove(filepath.Dir(runner.moved))
			}
		})
	}
}

func (r *structuredResultCommandRunner) Run(_ context.Context, _ string, args []string, dir string, _ time.Duration) (subprocess.Result, error) {
	callIndex := r.calls
	r.calls++
	r.args = append([]string{}, args...)
	r.allArgs = append(r.allArgs, append([]string{}, args...))
	r.dirs = append(r.dirs, dir)
	for i, arg := range args {
		if arg != "--output-last-message" || i+1 >= len(args) {
			continue
		}
		value := r.rawResult
		if callIndex < len(r.rawResults) {
			value = r.rawResults[callIndex]
		}
		if callIndex < len(r.results) {
			value = executionContentJSON(r.results[callIndex])
		}
		if value == nil && r.err == nil {
			value = executionContentJSON(r.result)
		}
		if value != nil {
			if err := os.WriteFile(args[i+1], value, 0o600); err != nil {
				return subprocess.Result{}, err
			}
		}
	}
	return r.commandResult, r.err
}

func executionContentJSON(result StructuredExecutionResult) []byte {
	blockers := []string{}
	if result.Blocker != nil {
		blockers = append(blockers, *result.Blocker)
	}
	value, _ := json.Marshal(executionContent{
		Outcome: result.Outcome, Summary: result.Summary, WorkDone: result.WorkDone,
		Verification: result.Verification, Blockers: blockers,
	})
	return value
}

func (r *structuredResultCommandRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.inputs = append(r.inputs, string(data))
	return r.Run(ctx, command, args, dir, timeout)
}
