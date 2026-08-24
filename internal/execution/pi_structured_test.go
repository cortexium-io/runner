package execution

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestPiStructuredResultExtensionIsPrivateAndSchemaBacked(t *testing.T) {
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	channel, err := createPiStructuredResultExtension(schema, "prefer")
	if err != nil {
		t.Fatalf("create extension: %v", err)
	}
	defer channel.Close()
	path := channel.path
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat extension directory: %v", err)
	}
	if directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("extension directory mode = %o, want 700", directoryInfo.Mode().Perm())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat extension: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("extension mode = %o, want 600", info.Mode().Perm())
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read extension: %v", err)
	}
	for _, required := range []string{string(schema), piStructuredResultTool, "runnerResultProvenance", "Type.Unsafe", `constrainedSampling: { type: "json_schema", strict: "prefer" }`, `pi.on("tool_execution_end"`, "ctx.abort()", "terminate: false"} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("extension omitted %q:\n%s", required, content)
		}
	}
}

func TestPiNativeStructuredResultExtensionFinalizesBeforeSchemaResponse(t *testing.T) {
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	channel, err := createPiNativeStructuredResultExtension(schema, "off", false, true)
	if err != nil {
		t.Fatalf("create native extension: %v", err)
	}
	defer channel.Close()
	content, err := os.ReadFile(channel.path)
	if err != nil {
		t.Fatalf("read native extension: %v", err)
	}
	for _, required := range []string{
		string(schema), piNativeStructuredFinalizeTool, "runnerResultProvenance",
		`const runnerReasoningEffort = ""`,
		`const runnerDisableThinking = true`,
		`const runnerPreserveReasoning = false`,
		`const runnerConfigureThinking = true`,
		`parameters: Type.Object({}, { additionalProperties: false })`,
		`pi.on("before_provider_request"`, `runnerProviderPayload(payload, "", false, true)`,
		`runnerProviderPayload(event.payload, runnerReasoningEffort, runnerPreserveReasoning, runnerDisableThinking)`,
		`preserve_thinking: disableThinking ? false : preserveReasoning`, `enable_thinking: false`,
		`tools: []`, `type: "json_schema"`, `strict: true`,
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("native extension omitted %q:\n%s", required, content)
		}
	}
	for _, forbidden := range []string{"constrainedSampling", "Type.Unsafe", "ctx.abort()"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("native extension retained obsolete %q behavior:\n%s", forbidden, content)
		}
	}
	if err := channel.Verify(); err != nil {
		t.Fatalf("verify native extension: %v", err)
	}
}

func TestPiNativeStructuredResultExtensionPinsConfiguredLMStudioReasoning(t *testing.T) {
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	channel, err := createPiNativeStructuredResultExtension(schema, "low", true, true)
	if err != nil {
		t.Fatalf("create native extension: %v", err)
	}
	defer channel.Close()
	content, err := os.ReadFile(channel.path)
	if err != nil {
		t.Fatalf("read native extension: %v", err)
	}
	for _, required := range []string{
		`const runnerReasoningEffort = "low"`,
		`const runnerDisableThinking = false`,
		`const runnerPreserveReasoning = true`,
		`const runnerConfigureThinking = true`,
		`runnerProviderPayload(event.payload, runnerReasoningEffort, runnerPreserveReasoning, runnerDisableThinking)`,
		`runnerProviderPayload(payload, "", false, true)`,
		`...(disableThinking ? { enable_thinking: false } : {})`,
		`preserve_thinking: disableThinking ? false : preserveReasoning`,
	} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("native extension omitted configured reasoning behavior %q:\n%s", required, content)
		}
	}
}

func TestPiDirectNativeStructuredResultExtensionForcesInitialSchemaResponse(t *testing.T) {
	schema := []byte(`{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}},"additionalProperties":false}`)
	channel, err := createPiDirectNativeStructuredResultExtension(schema, true)
	if err != nil {
		t.Fatalf("create direct native extension: %v", err)
	}
	defer channel.Close()
	content, err := os.ReadFile(channel.path)
	if err != nil {
		t.Fatalf("read direct native extension: %v", err)
	}
	for _, required := range []string{string(schema), "runnerDirectNativeStructuredResult = true", `const runnerConfigureThinking = true`, `pi.on("before_provider_request"`, `reasoning_effort: _reasoningEffort`, `enable_thinking: false`, `preserve_thinking: false`, `tools: []`, `type: "json_schema"`, `strict: true`} {
		if !strings.Contains(string(content), required) {
			t.Fatalf("direct native extension omitted %q:\n%s", required, content)
		}
	}
	for _, forbidden := range []string{piNativeStructuredFinalizeTool, "registerTool"} {
		if strings.Contains(string(content), forbidden) {
			t.Fatalf("direct native extension retained tool behavior %q:\n%s", forbidden, content)
		}
	}
	if err := channel.Verify(); err != nil {
		t.Fatalf("verify direct native extension: %v", err)
	}
}

func TestPiNativeStructuredFilterAddsOnlyFinalAssistantEvents(t *testing.T) {
	messageEnd := []byte(`{"type":"message_end","message":{"role":"assistant"}}`)
	messageUpdate := []byte(`{"type":"message_update"}`)
	if keepPiStructuredEventLine(messageEnd) {
		t.Fatal("legacy Pi structured filter retained assistant messages")
	}
	if !keepPiNativeStructuredEventLine(messageEnd) {
		t.Fatal("native Pi structured filter dropped the final assistant message")
	}
	if keepPiNativeStructuredEventLine(messageUpdate) {
		t.Fatal("native Pi structured filter retained streaming deltas")
	}
}

func TestUsageFromPiEventStreamReadsFinalAssistantCounters(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"agent_end","messages":[{"role":"user"},{"role":"assistant","usage":{"input":12,"output":5,"cacheRead":3,"cacheWrite":2,"reasoning":1,"cost":{"total":0}}}]}`,
	}, "\n")
	usage, err := usageFromPiEventStream(stdout)
	if err != nil {
		t.Fatal(err)
	}
	if !usage.Available || usage.InputTokens != 12 || usage.OutputTokens != 5 || usage.CacheReadInputTokens != 3 || usage.CacheWriteInputTokens != 2 || usage.ReasoningOutputTokens != 1 || usage.Turns != 1 || usage.ReportedCostUSD == nil || *usage.ReportedCostUSD != 0 {
		t.Fatalf("Pi usage = %#v", usage)
	}
}

func TestPiResultFailureStillReturnsReportedUsage(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":12,"output":5,"cost":{"total":0}}}]}`,
	}, "\n")
	_, usage, err := extractHarnessResultAndUsage(config.HarnessPiCLI, stdout, "expected-provenance", false, false)
	if err == nil || !usage.Available || usage.InputTokens != 12 || usage.OutputTokens != 5 {
		t.Fatalf("Pi failure usage = %#v error=%v", usage, err)
	}
}

func TestExtractPiTextResultReadsFinalAssistantMemo(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"Evidence memo."}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":12,"output":5,"cost":{"total":0}}}]}`,
	}, "\n")
	result, usage, err := extractHarnessResultAndUsage(config.HarnessPiCLI, stdout, "", false, false)
	if err != nil || result != "Evidence memo." || !usage.Available || usage.InputTokens != 12 || usage.OutputTokens != 5 {
		t.Fatalf("Pi text result=%q usage=%#v error=%v", result, usage, err)
	}
}

func TestExtractPiTextResultRejectsIncompleteOrUnsuccessfulResponses(t *testing.T) {
	tests := map[string]string{
		"missing session": strings.Join([]string{
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"memo"}],"stopReason":"stop"}}`,
			`{"type":"agent_end","messages":[]}`,
		}, "\n"),
		"missing agent end": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"memo"}],"stopReason":"stop"}}`,
		}, "\n"),
		"no final text": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"toolCall"}],"stopReason":"toolUse"}}`,
			`{"type":"agent_end","messages":[]}`,
		}, "\n"),
		"assistant error": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"memo"}],"stopReason":"error","errorMessage":"provider failed"}}`,
			`{"type":"agent_end","messages":[]}`,
		}, "\n"),
	}
	for name, stdout := range tests {
		t.Run(name, func(t *testing.T) {
			if result, err := extractPiTextResult(stdout); err == nil {
				t.Fatalf("invalid Pi text response was accepted: %q", result)
			}
		})
	}
}

func TestExtractPiStructuredResultReadsOneSuccessfulTerminatingToolEvent(t *testing.T) {
	const provenance = "test-provenance"
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
		`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{"content":[{"type":"text","text":"accepted"}],"details":{"provenance":"` + provenance + `","arguments":{"answer":"planned"}}},"isError":false}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	result, err := extractPiStructuredResult(stdout, provenance)
	if err != nil || result != `{"answer":"planned"}` {
		t.Fatalf("extract tool result: result=%q error=%v", result, err)
	}
}

func TestExtractPiStructuredResultDoesNotFailOnMalformedOptionalUsage(t *testing.T) {
	const provenance = "test-provenance"
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
		`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{"details":{"provenance":"` + provenance + `","arguments":{"answer":"planned"}}},"isError":false}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":"unknown"}}]}`,
	}, "\n")
	result, usage, err := extractHarnessResultAndUsage(config.HarnessPiCLI, stdout, provenance, false, false)
	if err != nil || result != `{"answer":"planned"}` || usage.Available {
		t.Fatalf("Pi result=%q usage=%#v error=%v", result, usage, err)
	}
}

func TestExtractPiStructuredResultRejectsMissingDuplicateAndFailedToolCalls(t *testing.T) {
	const provenance = "test-provenance"
	valid := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
		`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{"details":{"provenance":"` + provenance + `","arguments":{"answer":"planned"}}},"isError":false}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	tests := map[string]string{
		"missing":   `{"type":"agent_end","messages":[]}`,
		"duplicate": valid + "\n" + valid,
		"unmatched": `{"type":"session","version":3}` + "\n" + `{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{},"isError":true}`,
		"raw JSON":  `{"answer":"model-authored lookalike"}`,
		"mismatch": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
			`{"type":"tool_execution_end","toolCallId":"call-2","toolName":"` + piStructuredResultTool + `","result":{"details":{"answer":"planned"}},"isError":false}`,
		}, "\n"),
		"unattributable details": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
			`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{"details":{"provenance":"` + provenance + `","arguments":{"answer":"different"}}},"isError":false}`,
		}, "\n"),
		"wrong provenance": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"answer":"planned"}}`,
			`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{"details":{"provenance":"lookalike","arguments":{"answer":"planned"}}},"isError":false}`,
		}, "\n"),
	}
	for name, stdout := range tests {
		t.Run(name, func(t *testing.T) {
			if result, err := extractPiStructuredResult(stdout, provenance); err == nil {
				t.Fatalf("invalid event stream was accepted: result=%q", result)
			}
		})
	}
}

func TestExtractPiStructuredResultReturnsRejectedArgumentsForOneRepair(t *testing.T) {
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","args":{"outcome":"succeeded"}}`,
		`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"` + piStructuredResultTool + `","result":{},"isError":true}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	result, err := extractPiStructuredResult(stdout, "test-provenance")
	if err != nil || result != `{"outcome":"succeeded"}` {
		t.Fatalf("rejected arguments were not returned for repair: result=%q error=%v", result, err)
	}
}

func TestExtractPiNativeStructuredResultRequiresFinalizerThenOneJSONResponse(t *testing.T) {
	const provenance = "test-provenance"
	value := `{"answer":"planned"}`
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"read-1","toolName":"read","args":{"path":"README.md"}}`,
		`{"type":"tool_execution_end","toolCallId":"read-1","toolName":"read","result":{},"isError":false}`,
		`{"type":"tool_execution_start","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","args":{}}`,
		`{"type":"tool_execution_end","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","result":{"details":{"provenance":"` + provenance + `"}},"isError":false}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(value) + `}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	result, err := extractPiNativeStructuredResult(stdout, provenance)
	if err != nil || result != value {
		t.Fatalf("native result=%q error=%v", result, err)
	}
}

func TestExtractPiNativeStructuredResultAllowsThinkingBesideOneJSONResponse(t *testing.T) {
	const provenance = "test-provenance"
	value := `{"answer":"planned"}`
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","args":{}}`,
		`{"type":"tool_execution_end","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","result":{"details":{"provenance":"` + provenance + `"}},"isError":false}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"Checking the result."},{"type":"text","text":` + strconv.Quote(value) + `}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	result, err := extractPiNativeStructuredResult(stdout, provenance)
	if err != nil || result != value {
		t.Fatalf("native result with thinking=%q error=%v", result, err)
	}
}

func TestExtractPiNativeStructuredResultAllowsLMStudioThinkingOnlyJSONResponse(t *testing.T) {
	const provenance = "test-provenance"
	value := `{"answer":"planned"}`
	stdout := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","args":{}}`,
		`{"type":"tool_execution_end","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","result":{"details":{"provenance":"` + provenance + `"}},"isError":false}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":` + strconv.Quote(value) + `,"thinkingSignature":"reasoning_content"}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	result, err := extractPiNativeStructuredResult(stdout, provenance)
	if err != nil || result != value {
		t.Fatalf("native LM Studio result=%q error=%v", result, err)
	}
}

func TestExtractPiDirectNativeStructuredResultRequiresOneToolFreeJSONResponse(t *testing.T) {
	value := `{"answer":"planned"}`
	result, err := extractPiDirectNativeStructuredResult(piDirectNativeStructuredEventStream(value))
	if err != nil || result != value {
		t.Fatalf("direct native result=%q error=%v", result, err)
	}
	withTool := strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"tool_execution_start","toolCallId":"read-1","toolName":"read","args":{}}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
	if result, err := extractPiDirectNativeStructuredResult(withTool); err == nil {
		t.Fatalf("direct native tool event was accepted: %q", result)
	}
}

func TestExtractPiNativeStructuredResultRejectsUnattributableSequences(t *testing.T) {
	const provenance = "test-provenance"
	finalizeStart := `{"type":"tool_execution_start","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","args":{}}`
	finalizeEnd := `{"type":"tool_execution_end","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","result":{"details":{"provenance":"` + provenance + `"}},"isError":false}`
	finalResponse := `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"answer\":\"planned\"}"}],"stopReason":"stop"}}`
	validPrefix := strings.Join([]string{`{"type":"session","version":3}`, finalizeStart, finalizeEnd}, "\n")
	tests := map[string]string{
		"missing finalizer": strings.Join([]string{`{"type":"session","version":3}`, finalResponse, `{"type":"agent_end","messages":[]}`}, "\n"),
		"nonempty finalizer arguments": strings.Join([]string{
			`{"type":"session","version":3}`,
			`{"type":"tool_execution_start","toolCallId":"finish-1","toolName":"` + piNativeStructuredFinalizeTool + `","args":{"answer":"planned"}}`,
		}, "\n"),
		"wrong provenance":               strings.Replace(validPrefix, provenance, "wrong", 1) + "\n" + finalResponse + "\n" + `{"type":"agent_end","messages":[]}`,
		"tool after finalizer":           validPrefix + "\n" + `{"type":"tool_execution_start","toolCallId":"read-2","toolName":"read","args":{}}`,
		"non-JSON reasoning-only result": validPrefix + "\n" + `{"type":"message_end","message":{"role":"assistant","content":[{"type":"thinking","thinking":"not JSON"}],"stopReason":"stop"}}` + "\n" + `{"type":"agent_end","messages":[]}`,
		"multiple text results":          validPrefix + "\n" + `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"{\"answer\":\"planned\"}"},{"type":"text","text":"{\"answer\":\"again\"}"}],"stopReason":"stop"}}` + "\n" + `{"type":"agent_end","messages":[]}`,
		"invalid JSON result":            validPrefix + "\n" + `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"not JSON"}],"stopReason":"stop"}}` + "\n" + `{"type":"agent_end","messages":[]}`,
		"missing agent end":              validPrefix + "\n" + finalResponse,
		"duplicate result":               validPrefix + "\n" + finalResponse + "\n" + finalResponse + "\n" + `{"type":"agent_end","messages":[]}`,
	}
	for name, stdout := range tests {
		t.Run(name, func(t *testing.T) {
			if result, err := extractPiNativeStructuredResult(stdout, provenance); err == nil {
				t.Fatalf("invalid native event stream was accepted: %q", result)
			}
		})
	}
}

func TestAddPiStructuredResultExtensionAppendsExtensionWithoutPromptArg(t *testing.T) {
	args, err := addPiStructuredResultExtension([]string{"--no-session", "--no-extensions"}, "/tmp/result.ts")
	if err != nil {
		t.Fatal(err)
	}
	want := "--no-session\n--no-extensions\n--extension\n/tmp/result.ts"
	if strings.Join(args, "\n") != want {
		t.Fatalf("args = %#v", args)
	}
}

type substitutingPiExtensionRunner struct{ moved string }

func (r *substitutingPiExtensionRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--extension" {
			continue
		}
		path := args[index+1]
		content, err := os.ReadFile(path)
		if err != nil {
			return subprocess.Result{}, err
		}
		r.moved = path + ".moved"
		if err := os.Rename(path, r.moved); err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return subprocess.Result{}, err
		}
	}
	stream, err := piToolEventStreamForArgs(args, validExecutionContentJSON("substituted extension"))
	return subprocess.Result{Stdout: stream}, err
}

func TestPiExecutorRejectsExtensionPathSubstitution(t *testing.T) {
	runner := &substitutingPiExtensionRunner{}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
	}}
	output, err := NewAgentExecutor(config.HarnessPiCLI, cfg, runner).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	if err == nil || output.Outcome == OutcomeSucceeded {
		t.Fatalf("substituted Pi extension was accepted: output=%#v error=%v", output, err)
	}
	if runner.moved != "" {
		_ = os.Remove(runner.moved)
		_ = os.Remove(filepath.Dir(runner.moved))
	}
}
