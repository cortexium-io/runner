package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type plannerCommandRunner struct {
	args            []string
	extensionSource string
	input           string
	timeout         time.Duration
}

type failingPlannerRunner struct{}

func (failingPlannerRunner) Run(_ context.Context, _ string, _ []string, _ string, _ time.Duration) (subprocess.Result, error) {
	return subprocess.Result{Stderr: "operation timed out", ExitCode: 1}, context.DeadlineExceeded
}

func (r *plannerCommandRunner) Run(_ context.Context, command string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if command != "pi" {
		return subprocess.Result{}, nil
	}
	r.args = append([]string(nil), args...)
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--extension" {
			continue
		}
		content, err := os.ReadFile(args[index+1])
		if err != nil {
			return subprocess.Result{}, err
		}
		r.extensionSource = string(content)
	}
	output, err := piToolEventStreamForArgs(args, `{"answer":"planned"}`)
	return subprocess.Result{Stdout: output}, err
}

func (r *plannerCommandRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.input = string(data)
	r.timeout = timeout
	return r.Run(ctx, command, args, dir, timeout)
}

func (failingPlannerRunner) RunLineFilteredInput(_ context.Context, _ string, _ []string, _ string, _ time.Duration, _ io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	return subprocess.Result{Stderr: "operation timed out", ExitCode: 1}, context.DeadlineExceeded
}

func TestPiLMStudioPlannerUsesNativeJSONAfterFinalizer(t *testing.T) {
	run := &plannerCommandRunner{}
	schema := []byte(`{
  "type": "object",
  "required": ["answer"],
  "properties": {
    "answer": {"type": "string"}
  },
  "additionalProperties": false
}`)
	cfg := config.ExecutionConfig{PreserveReasoning: true, Harness: config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", Model: ptr("lmstudio/qwen/qwen3.8-27b"), ReasoningEffort: "high", TimeoutSeconds: 30,
	}}

	result, err := RunPlannerWithUsage(t.Context(), config.HarnessPiCLI, cfg, t.TempDir(), "Plan this work.", schema, run)
	if err != nil {
		t.Fatalf("run Pi planner: %v", err)
	}
	if result.Message != `{"answer":"planned"}` {
		t.Fatalf("unexpected planner result %q", result.Message)
	}
	if len(run.args) == 0 {
		t.Fatal("Pi planner was not invoked")
	}
	joined := strings.Join(run.args, " ")
	for _, required := range []string{"--no-extensions", "--mode json", "--extension", "--tools read,grep,find,ls," + piNativeStructuredFinalizeTool, "--append-system-prompt " + piNativeStructuredResultSystemPrompt} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Pi planner invocation omitted %q: %s", required, joined)
		}
	}
	compactSchema := bytes.Buffer{}
	if err := json.Compact(&compactSchema, schema); err != nil {
		t.Fatal(err)
	}
	if strings.Count(run.extensionSource, compactSchema.String()) != 1 || !strings.Contains(run.extensionSource, piNativeStructuredFinalizeTool) || !strings.Contains(run.extensionSource, `const runnerPreserveReasoning = true`) || !strings.Contains(run.extensionSource, `pi.on("before_provider_request"`) || !strings.Contains(run.extensionSource, `runnerProviderPayload(payload, "", false, true)`) || !strings.Contains(run.extensionSource, `preserve_thinking: disableThinking ? false : preserveReasoning`) || !strings.Contains(run.extensionSource, "response_format") {
		t.Fatalf("Pi native structured-result extension omitted its finalizer or exact schema response:\n%s", run.extensionSource)
	}
	prompt := run.input
	if strings.Contains(prompt, strings.TrimSpace(string(schema))) {
		t.Fatalf("Pi planner duplicated the tool schema in its prompt:\n%s", prompt)
	}
	if strings.Contains(strings.Join(run.args, " "), "Plan this work.") {
		t.Fatalf("Pi planner leaked prompt content into argv: %#v", run.args)
	}
}

func TestProbeAlwaysUsesItsToolFreeSandboxedAccess(t *testing.T) {
	run := &plannerCommandRunner{}
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	cfg := config.ExecutionConfig{
		RoleAccess: config.RoleAccessHost,
		Harness: config.HarnessConfig{
			Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
		},
	}
	result, err := RunProbeWithUsage(t.Context(), config.HarnessPiCLI, cfg, "Probe this model.", schema, run)
	if err != nil {
		t.Fatalf("host-configured Pi role widened or blocked the dedicated probe: %v", err)
	}
	if result.Message != `{"answer":"planned"}` || !strings.Contains(strings.Join(run.args, " "), "--no-tools") {
		t.Fatalf("probe did not keep its tool-free profile: result=%q args=%#v", result.Message, run.args)
	}
}

func TestPiPlannerStageRequiresShallowSchemaConstrainedSampling(t *testing.T) {
	run := &plannerCommandRunner{}
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", TimeoutSeconds: 30,
	}}

	result, err := RunPlannerStageWithUsage(t.Context(), config.HarnessPiCLI, cfg, t.TempDir(), "Return one staged answer.", schema, run)
	if err != nil {
		t.Fatalf("run strict Pi planner stage: %v", err)
	}
	if result.Message != `{"answer":"planned"}` {
		t.Fatalf("unexpected staged result %q", result.Message)
	}
	if !strings.Contains(run.extensionSource, `constrainedSampling: { type: "json_schema", strict: "require" }`) {
		t.Fatalf("staged Pi planner did not require its shallow schema:\n%s", run.extensionSource)
	}
	if run.timeout != 30*time.Second {
		t.Fatalf("staged Pi planner timeout = %s, want one configured timeout per stage", run.timeout)
	}
}

func TestPiPlannerSynthesisStageUsesDirectNativeJSONWithoutTools(t *testing.T) {
	run := &plannerCommandRunner{}
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	cfg := config.ExecutionConfig{
		Skills: []string{"runner-planner"},
		Harness: config.HarnessConfig{
			Kind: config.HarnessPiCLI, Command: "pi", Model: ptr("lmstudio/qwen/qwen3.8-27b"), WorkingDir: t.TempDir(), TimeoutSeconds: 30,
		},
	}

	result, err := RunPlannerSynthesisStageWithUsage(t.Context(), config.HarnessPiCLI, cfg, "Synthesize one card.", schema, run)
	if err != nil {
		t.Fatalf("run Pi planner synthesis stage: %v", err)
	}
	if result.Message != `{"answer":"planned"}` {
		t.Fatalf("unexpected synthesis result %q", result.Message)
	}
	joined := strings.Join(run.args, " ")
	if !strings.Contains(joined, "--no-tools") || strings.Contains(joined, "--tools") || strings.Contains(joined, "read,grep,find,ls") || strings.Contains(joined, piNativeStructuredFinalizeTool) {
		t.Fatalf("Pi synthesis exposed repository tools: %s", joined)
	}
	if !strings.Contains(joined, "--append-system-prompt "+piDirectNativeStructuredResultSystemPrompt) || !strings.Contains(run.extensionSource, "runnerDirectNativeStructuredResult = true") || !strings.Contains(run.extensionSource, "response_format") || strings.Contains(run.extensionSource, "registerTool") {
		t.Fatalf("Pi synthesis did not force direct native JSON without a tool:\nargs=%s\nextension=%s", joined, run.extensionSource)
	}
	if strings.Contains(run.input, "BEGIN RUNNER-PINNED SKILL") {
		t.Fatalf("Pi synthesis repeated the planner orientation skill:\n%s", run.input)
	}
}

func TestPlannerPreservesTypedHarnessRecovery(t *testing.T) {
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", TimeoutSeconds: 30,
	}}
	result, err := RunPlannerWithUsage(t.Context(), config.HarnessPiCLI, cfg, t.TempDir(), "Plan this work.", []byte(`{"type":"object"}`), failingPlannerRunner{})
	if err == nil {
		t.Fatal("failing planner unexpectedly succeeded")
	}
	if result.FailureClass != FailureTimeout || result.RetryDisposition != RetryManual {
		t.Fatalf("planner recovery classification was lost: %#v", result)
	}
}
