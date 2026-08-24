package execution

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/securefs"
)

const piStructuredResultTool = "cortexium_runner_result"
const piNativeStructuredFinalizeTool = "cortexium_runner_finalize"

const piStructuredResultSystemPrompt = "When the cortexium_runner_result tool is available, call it exactly once as your final action after completing the assigned work. Put the final structured result in that tool's arguments. Do not print the result as assistant text, do not copy or describe the tool schema, and do not emit anything after the tool call."
const piNativeStructuredResultSystemPrompt = "When the cortexium_runner_finalize tool is available, complete the assignment and all required tool work first, then call cortexium_runner_finalize exactly once with no arguments. After it succeeds, return the requested structured result as your next and only response. Do not call another tool, add prose, use Markdown, or describe the schema."
const piDirectNativeStructuredResultSystemPrompt = "Return only the requested structured result. Do not call tools, add prose, use Markdown, or describe the schema."

type piStructuredResultChannel struct {
	artifacts  *securefs.ArtifactSet
	path       string
	provenance string
}

func (c *piStructuredResultChannel) Close() error { return c.artifacts.Close() }

func (c *piStructuredResultChannel) Verify() error {
	return c.artifacts.VerifyImmutable(piExtensionName)
}

func keepPiStructuredEventLine(line []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil || !isPiJSONEventType(event.Type) {
		return true
	}
	return event.Type == "session" || event.Type == "agent_start" || event.Type == "agent_end" ||
		event.Type == "tool_execution_start" || event.Type == "tool_execution_end"
}

func keepPiNativeStructuredEventLine(line []byte) bool {
	if keepPiStructuredEventLine(line) {
		return true
	}
	var event struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(bytes.TrimSpace(line), &event) == nil && event.Type == "message_end"
}

func keepPiTextEventLine(line []byte) bool {
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(line), &event); err != nil || !isPiJSONEventType(event.Type) {
		return true
	}
	return event.Type == "session" || event.Type == "agent_start" || event.Type == "agent_end" ||
		event.Type == "message_end" || event.Type == "tool_execution_start" || event.Type == "tool_execution_end"
}

func extractPiTextResult(stdout string) (string, error) {
	type contentItem struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type message struct {
		Role         string        `json:"role"`
		Content      []contentItem `json:"content"`
		StopReason   string        `json:"stopReason"`
		ErrorMessage string        `json:"errorMessage"`
	}
	var sawSession, sawAgentEnd, sawAssistant bool
	var finalText string
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(stdout)))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessResultBytes)
	for scanner.Scan() {
		var event struct {
			Type    string  `json:"type"`
			Message message `json:"message"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &event); err != nil {
			return "", fmt.Errorf("decode Pi JSON event stream: %w", err)
		}
		switch event.Type {
		case "session":
			sawSession = true
		case "agent_end":
			sawAgentEnd = true
		case "message_end":
			if event.Message.Role != "assistant" {
				continue
			}
			if !sawSession {
				return "", errors.New("Pi final text lacks session provenance")
			}
			if event.Message.StopReason == "error" || event.Message.StopReason == "aborted" || strings.TrimSpace(event.Message.ErrorMessage) != "" {
				return "", errors.New("Pi reported an unsuccessful final assistant message")
			}
			sawAssistant = true
			var parts []string
			for _, item := range event.Message.Content {
				if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
					parts = append(parts, strings.TrimSpace(item.Text))
				}
			}
			finalText = strings.Join(parts, "\n\n")
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Pi JSON event stream: %w", err)
	}
	if !sawSession || !sawAgentEnd {
		return "", errors.New("Pi text response did not complete a correlated session")
	}
	if !sawAssistant || strings.TrimSpace(finalText) == "" {
		return "", errors.New("Pi returned no final assistant text")
	}
	return strings.TrimSpace(finalText), nil
}

func createPiStructuredResultExtension(schema []byte, constrainedSamplingStrict string) (*piStructuredResultChannel, error) {
	if constrainedSamplingStrict != "prefer" && constrainedSamplingStrict != "require" {
		return nil, fmt.Errorf("unsupported Pi constrained-sampling strictness %q", constrainedSamplingStrict)
	}
	compactSchema := bytes.Buffer{}
	if err := json.Compact(&compactSchema, schema); err != nil {
		return nil, fmt.Errorf("prepare Pi structured-result schema: %w", err)
	}
	provenanceBytes := make([]byte, 32)
	if _, err := rand.Read(provenanceBytes); err != nil {
		return nil, fmt.Errorf("create Pi result provenance: %w", err)
	}
	provenance := fmt.Sprintf("%x", provenanceBytes)
	// Pi's tool-level terminate hint can exit print mode before its JSON event
	// writer flushes the attributable tool pair. Aborting from the trusted
	// tool_execution_end hook preserves start/end provenance and prevents a
	// second model turn.
	source := `import { Type } from "typebox";

const runnerResultProvenance = ` + strconv.Quote(provenance) + `;

export default function (pi) {
  pi.on("tool_execution_end", (event, ctx) => {
    if (event.toolName === "` + piStructuredResultTool + `") {
      ctx.abort();
    }
  });
  pi.registerTool({
    name: "` + piStructuredResultTool + `",
    label: "Runner structured result",
    description: "Submit the final structured result for the current Runner assignment. Call this exactly once as the final action.",
    promptSnippet: "Submit the final Runner result through ` + piStructuredResultTool + `",
    promptGuidelines: [
      "Use ` + piStructuredResultTool + ` exactly once as your final action for this Runner assignment.",
      "Populate every required field from completed work and evidence. Do not echo or describe the schema.",
      "After calling ` + piStructuredResultTool + `, do not emit another assistant response in the same turn."
    ],
    parameters: Type.Unsafe(` + compactSchema.String() + `),
    constrainedSampling: { type: "json_schema", strict: ` + strconv.Quote(constrainedSamplingStrict) + ` },
    async execute(_toolCallId, params) {
      return {
        content: [{ type: "text", text: "Runner structured result accepted." }],
        details: { provenance: runnerResultProvenance, arguments: params },
        terminate: false
      };
    }
  });
}
`
	artifacts, err := securefs.NewArtifactSet("cortexium-runner-pi-result", []securefs.ArtifactFile{{
		Name: piExtensionName, Content: []byte(source),
	}})
	if err != nil {
		return nil, fmt.Errorf("create Pi structured-result extension: %w", err)
	}
	return &piStructuredResultChannel{artifacts: artifacts, path: artifacts.Path(piExtensionName), provenance: provenance}, nil
}

func createPiNativeStructuredResultExtension(schema []byte, reasoningEffort string, preserveReasoning, configureThinking bool) (*piStructuredResultChannel, error) {
	compactSchema := bytes.Buffer{}
	if err := json.Compact(&compactSchema, schema); err != nil {
		return nil, fmt.Errorf("prepare Pi native structured-result schema: %w", err)
	}
	reasoningEffort = strings.ToLower(strings.TrimSpace(reasoningEffort))
	disableThinking := reasoningEffort == "off"
	if disableThinking {
		reasoningEffort = ""
	}
	provenanceBytes := make([]byte, 32)
	if _, err := rand.Read(provenanceBytes); err != nil {
		return nil, fmt.Errorf("create Pi native result provenance: %w", err)
	}
	provenance := fmt.Sprintf("%x", provenanceBytes)
	source := `import { Type } from "typebox";

const runnerResultProvenance = ` + strconv.Quote(provenance) + `;
const runnerReasoningEffort = ` + strconv.Quote(reasoningEffort) + `;
const runnerDisableThinking = ` + strconv.FormatBool(disableThinking) + `;
const runnerPreserveReasoning = ` + strconv.FormatBool(preserveReasoning) + `;
const runnerConfigureThinking = ` + strconv.FormatBool(configureThinking) + `;
let nativeResultPending = false;

function runnerProviderPayload(payload, reasoningEffort, preserveReasoning, disableThinking) {
  const chatTemplateKwargs = payload.chat_template_kwargs;
  const hasChatTemplateKwargs = typeof chatTemplateKwargs === "object" && chatTemplateKwargs !== null && !Array.isArray(chatTemplateKwargs);
  const { reasoning_effort: _reasoningEffort, ...payloadWithoutReasoning } = payload;
  const requestPayload = disableThinking ? payloadWithoutReasoning : payload;
  const request = reasoningEffort === "" ? requestPayload : { ...requestPayload, reasoning_effort: reasoningEffort };
  if (!runnerConfigureThinking && !hasChatTemplateKwargs) {
    return request;
  }
  return {
    ...request,
    chat_template_kwargs: {
      ...(hasChatTemplateKwargs ? chatTemplateKwargs : {}),
      ...(disableThinking ? { enable_thinking: false } : {}),
      preserve_thinking: disableThinking ? false : preserveReasoning
    }
  };
}

export default function (pi) {
  pi.registerTool({
    name: "` + piNativeStructuredFinalizeTool + `",
    label: "Finalize Runner result",
    description: "After completing the assignment and all required tool work, call this empty tool once to produce the final structured result.",
    promptSnippet: "Finalize the completed assignment through ` + piNativeStructuredFinalizeTool + `",
    promptGuidelines: [
      "Complete all assigned work and evidence gathering before calling ` + piNativeStructuredFinalizeTool + `.",
      "Call ` + piNativeStructuredFinalizeTool + ` exactly once with no arguments.",
      "After it succeeds, return only the requested structured result and do not call another tool."
    ],
    parameters: Type.Object({}, { additionalProperties: false }),
    async execute() {
      nativeResultPending = true;
      return {
        content: [{ type: "text", text: "Return the final structured result now." }],
        details: { provenance: runnerResultProvenance },
        terminate: false
      };
    }
  });

  pi.on("before_provider_request", (event) => {
    if (typeof event.payload !== "object" || event.payload === null || Array.isArray(event.payload)) {
      throw new Error("Runner cannot apply native structured output to this provider payload");
    }
    if (!nativeResultPending) {
      return runnerProviderPayload(event.payload, runnerReasoningEffort, runnerPreserveReasoning, runnerDisableThinking);
    }
    const { tools: _tools, tool_choice: _toolChoice, toolChoice: _camelToolChoice, ...payload } = event.payload;
    return {
      ...runnerProviderPayload(payload, "", false, true),
      tools: [],
      response_format: {
        type: "json_schema",
        json_schema: {
          name: "cortexium_runner_result",
          strict: true,
          schema: ` + compactSchema.String() + `
        }
      }
    };
  });
}
`
	artifacts, err := securefs.NewArtifactSet("cortexium-runner-pi-native-result", []securefs.ArtifactFile{{
		Name: piExtensionName, Content: []byte(source),
	}})
	if err != nil {
		return nil, fmt.Errorf("create Pi native structured-result extension: %w", err)
	}
	return &piStructuredResultChannel{artifacts: artifacts, path: artifacts.Path(piExtensionName), provenance: provenance}, nil
}

func createPiDirectNativeStructuredResultExtension(schema []byte, configureThinking bool) (*piStructuredResultChannel, error) {
	compactSchema := bytes.Buffer{}
	if err := json.Compact(&compactSchema, schema); err != nil {
		return nil, fmt.Errorf("prepare Pi direct native structured-result schema: %w", err)
	}
	provenanceBytes := make([]byte, 32)
	if _, err := rand.Read(provenanceBytes); err != nil {
		return nil, fmt.Errorf("create Pi direct native result provenance: %w", err)
	}
	provenance := fmt.Sprintf("%x", provenanceBytes)
	source := `const runnerResultProvenance = ` + strconv.Quote(provenance) + `;
const runnerDirectNativeStructuredResult = true;
const runnerConfigureThinking = ` + strconv.FormatBool(configureThinking) + `;

function runnerProviderPayload(payload) {
  const chatTemplateKwargs = payload.chat_template_kwargs;
  const hasChatTemplateKwargs = typeof chatTemplateKwargs === "object" && chatTemplateKwargs !== null && !Array.isArray(chatTemplateKwargs);
  const { reasoning_effort: _reasoningEffort, ...request } = payload;
  if (!runnerConfigureThinking && !hasChatTemplateKwargs) {
    return request;
  }
  return {
    ...request,
    chat_template_kwargs: {
      ...(hasChatTemplateKwargs ? chatTemplateKwargs : {}),
      enable_thinking: false,
      preserve_thinking: false
    }
  };
}

export default function (pi) {
  pi.on("before_provider_request", (event) => {
    if (typeof event.payload !== "object" || event.payload === null || Array.isArray(event.payload)) {
      throw new Error("Runner cannot apply direct native structured output to this provider payload");
    }
    const { tools: _tools, tool_choice: _toolChoice, toolChoice: _camelToolChoice, ...payload } = event.payload;
    return {
      ...runnerProviderPayload(payload),
      tools: [],
      response_format: {
        type: "json_schema",
        json_schema: {
          name: "cortexium_runner_result",
          strict: true,
          schema: ` + compactSchema.String() + `
        }
      }
    };
  });
}
`
	artifacts, err := securefs.NewArtifactSet("cortexium-runner-pi-direct-native-result", []securefs.ArtifactFile{{
		Name: piExtensionName, Content: []byte(source),
	}})
	if err != nil {
		return nil, fmt.Errorf("create Pi direct native structured-result extension: %w", err)
	}
	return &piStructuredResultChannel{artifacts: artifacts, path: artifacts.Path(piExtensionName), provenance: provenance}, nil
}

func usageFromPiEventStream(stdout string) (metrics.Usage, error) {
	type piUsage struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
		Reasoning  int64 `json:"reasoning"`
		Cost       *struct {
			Total float64 `json:"total"`
		} `json:"cost"`
	}
	type piMessage struct {
		Role  string   `json:"role"`
		Usage *piUsage `json:"usage"`
	}
	var usage metrics.Usage
	scanner := bufio.NewScanner(strings.NewReader(stdout))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessResultBytes)
	for scanner.Scan() {
		var event struct {
			Type     string      `json:"type"`
			Messages []piMessage `json:"messages"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(scanner.Bytes()), &event); err != nil {
			return metrics.Usage{}, fmt.Errorf("decode Pi usage event: %w", err)
		}
		if event.Type != "agent_end" {
			continue
		}
		for _, message := range event.Messages {
			if message.Role != "assistant" || message.Usage == nil {
				continue
			}
			addition := metrics.Usage{
				Available: true, InputTokens: message.Usage.Input, OutputTokens: message.Usage.Output,
				CacheReadInputTokens: message.Usage.CacheRead, CacheWriteInputTokens: message.Usage.CacheWrite,
				ReasoningOutputTokens: message.Usage.Reasoning, Turns: 1,
			}
			if message.Usage.Cost != nil {
				cost := message.Usage.Cost.Total
				addition.ReportedCostUSD = &cost
			}
			usage = usage.Add(addition)
		}
	}
	if err := scanner.Err(); err != nil {
		return metrics.Usage{}, fmt.Errorf("read Pi usage event stream: %w", err)
	}
	if err := metrics.ValidateUsage(usage); err != nil {
		return metrics.Usage{}, fmt.Errorf("validate Pi usage: %w", err)
	}
	return usage, nil
}

func addPiStructuredResultExtension(args []string, path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("Pi structured invocation requires an extension path")
	}
	result := make([]string, 0, len(args)+2)
	result = append(result, args...)
	result = append(result, "--extension", path)
	return result, nil
}

func extractPiStructuredResult(stdout, expectedProvenance string) (string, error) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return "", errors.New("Pi returned no output")
	}

	type eventResult struct {
		Details struct {
			Provenance string          `json:"provenance"`
			Arguments  json.RawMessage `json:"arguments"`
		} `json:"details"`
	}
	type event struct {
		Type       string          `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
		Result     eventResult     `json:"result"`
		IsError    bool            `json:"isError"`
	}

	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessResultBytes)
	sawSession := false
	sawAgentEnd := false
	var structured []byte
	var candidateArgs []byte
	var callID string
	var sawStart, sawEnd, failed bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var current event
		if err := json.Unmarshal([]byte(line), &current); err != nil {
			return "", fmt.Errorf("decode Pi JSON event stream: %w", err)
		}
		if current.Type == "session" {
			sawSession = true
			continue
		}
		if current.Type == "agent_end" {
			if sawEnd {
				sawAgentEnd = true
			}
			continue
		}
		if current.ToolName != piStructuredResultTool {
			if sawEnd && current.Type == "tool_execution_start" {
				return "", errors.New("Pi executed another tool after the terminating structured-result tool")
			}
			continue
		}
		if current.Type == "tool_execution_start" {
			if !sawSession {
				return "", errors.New("Pi structured-result tool call lacks session provenance")
			}
			if sawStart {
				return "", errors.New("Pi called the structured-result tool more than once")
			}
			if strings.TrimSpace(current.ToolCallID) == "" || !validJSONObject(current.Args) {
				return "", errors.New("Pi structured-result tool start lacks a call identity or attributable arguments")
			}
			sawStart = true
			callID = current.ToolCallID
			candidateArgs = append([]byte(nil), current.Args...)
			continue
		}
		if current.Type != "tool_execution_end" {
			continue
		}
		if !sawStart || sawEnd {
			return "", errors.New("Pi structured-result tool end is unmatched or duplicated")
		}
		if current.ToolCallID != callID {
			return "", errors.New("Pi structured-result tool call identity changed")
		}
		sawEnd = true
		failed = current.IsError
		if failed {
			continue
		}
		if current.Result.Details.Provenance == "" || len(bytes.TrimSpace(current.Result.Details.Arguments)) == 0 {
			return "", errors.New("Pi structured-result tool returned no details")
		}
		if current.Result.Details.Provenance != expectedProvenance {
			return "", errors.New("Pi structured-result tool provenance does not match the configured extension")
		}
		if !sameJSON(candidateArgs, current.Result.Details.Arguments) {
			return "", errors.New("Pi structured-result tool details are not attributable to its arguments")
		}
		structured = append([]byte(nil), current.Result.Details.Arguments...)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Pi JSON event stream: %w", err)
	}
	if !sawStart || !sawEnd {
		return "", errors.New("Pi did not produce one correlated structured-result tool call")
	}
	if !sawAgentEnd {
		return "", errors.New("Pi structured-result tool did not terminate the agent successfully")
	}
	if structured != nil && !failed {
		return string(structured), nil
	}
	if failed && validJSONObject(candidateArgs) {
		return string(candidateArgs), nil
	}
	return "", errors.New("Pi structured-result tool failed without a proven candidate")
}

type piNativeContentItem struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Thinking string `json:"thinking"`
}

type piNativeMessage struct {
	Role         string                `json:"role"`
	Content      []piNativeContentItem `json:"content"`
	StopReason   string                `json:"stopReason"`
	ErrorMessage string                `json:"errorMessage"`
}

type piNativeEvent struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     struct {
		Details struct {
			Provenance string `json:"provenance"`
		} `json:"details"`
	} `json:"result"`
	IsError bool            `json:"isError"`
	Message piNativeMessage `json:"message"`
}

func extractPiNativeStructuredResult(stdout, expectedProvenance string) (string, error) {

	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(stdout)))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessResultBytes)
	var callID, result string
	var sawSession, sawStart, sawEnd, sawFinal, sawAgentEnd bool
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var current piNativeEvent
		if err := json.Unmarshal(line, &current); err != nil {
			return "", fmt.Errorf("decode Pi native JSON event stream: %w", err)
		}
		switch current.Type {
		case "session":
			sawSession = true
		case "tool_execution_start":
			if sawEnd {
				return "", errors.New("Pi executed another tool after native-result finalization")
			}
			if current.ToolName != piNativeStructuredFinalizeTool {
				continue
			}
			if !sawSession {
				return "", errors.New("Pi native-result finalizer lacks session provenance")
			}
			if sawStart || strings.TrimSpace(current.ToolCallID) == "" || !emptyJSONObject(current.Args) {
				return "", errors.New("Pi native-result finalizer must be called once with no arguments")
			}
			sawStart = true
			callID = current.ToolCallID
		case "tool_execution_end":
			if current.ToolName != piNativeStructuredFinalizeTool {
				continue
			}
			if !sawStart || sawEnd || current.ToolCallID != callID {
				return "", errors.New("Pi native-result finalizer completion is unmatched or duplicated")
			}
			if current.IsError || current.Result.Details.Provenance != expectedProvenance {
				return "", errors.New("Pi native-result finalizer failed or lacks configured provenance")
			}
			sawEnd = true
		case "message_end":
			if !sawEnd || current.Message.Role != "assistant" {
				continue
			}
			if sawFinal {
				return "", errors.New("Pi emitted more than one assistant response after native-result finalization")
			}
			if current.Message.StopReason != "stop" || strings.TrimSpace(current.Message.ErrorMessage) != "" {
				return "", errors.New("Pi native structured response did not finish successfully")
			}
			var err error
			result, err = piNativeStructuredText(current.Message.Content)
			if err != nil {
				return "", fmt.Errorf("Pi native structured response: %w", err)
			}
			if !validJSONObject([]byte(result)) {
				return "", errors.New("Pi native structured response is not one JSON object")
			}
			sawFinal = true
		case "agent_end":
			if sawFinal {
				sawAgentEnd = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Pi native JSON event stream: %w", err)
	}
	if !sawStart || !sawEnd || !sawFinal || !sawAgentEnd {
		return "", errors.New("Pi did not complete one correlated native structured result")
	}
	return result, nil
}

func extractPiDirectNativeStructuredResult(stdout string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(stdout)))
	scanner.Buffer(make([]byte, 64*1024), maxHarnessResultBytes)
	var result string
	var sawSession, sawFinal, sawAgentEnd bool
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var current piNativeEvent
		if err := json.Unmarshal(line, &current); err != nil {
			return "", fmt.Errorf("decode Pi direct native JSON event stream: %w", err)
		}
		switch current.Type {
		case "session":
			sawSession = true
		case "tool_execution_start", "tool_execution_end":
			return "", errors.New("Pi direct native structured response executed a tool")
		case "message_end":
			if current.Message.Role != "assistant" {
				continue
			}
			if sawFinal {
				return "", errors.New("Pi emitted more than one direct native structured response")
			}
			if current.Message.StopReason != "stop" || strings.TrimSpace(current.Message.ErrorMessage) != "" {
				return "", errors.New("Pi direct native structured response did not finish successfully")
			}
			var err error
			result, err = piNativeStructuredText(current.Message.Content)
			if err != nil {
				return "", fmt.Errorf("Pi direct native structured response: %w", err)
			}
			if !validJSONObject([]byte(result)) {
				return "", errors.New("Pi direct native structured response is not one JSON object")
			}
			sawFinal = true
		case "agent_end":
			if sawFinal {
				sawAgentEnd = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read Pi direct native JSON event stream: %w", err)
	}
	if !sawSession || !sawFinal || !sawAgentEnd {
		return "", errors.New("Pi did not complete one direct native structured result")
	}
	return result, nil
}

func validJSONObject(value []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

func piNativeStructuredText(content []piNativeContentItem) (string, error) {
	var text, thinking string
	var sawText, sawThinking bool
	for _, item := range content {
		switch item.Type {
		case "thinking":
			if sawThinking {
				return "", errors.New("contained more than one thinking value")
			}
			sawThinking = true
			thinking = strings.TrimSpace(item.Thinking)
		case "text":
			if sawText {
				return "", errors.New("contained more than one text value")
			}
			sawText = true
			text = strings.TrimSpace(item.Text)
		default:
			return "", fmt.Errorf("contained unsupported %q content", item.Type)
		}
	}
	if sawText {
		return text, nil
	}
	if sawThinking {
		// LM Studio returns strict JSON-schema output from Qwen in the OpenAI
		// reasoning_content field even when thinking is disabled. Pi faithfully
		// exposes that provider field as thinking. The caller still validates the
		// value as one JSON object and against the Runner-owned result schema.
		return thinking, nil
	}
	return "", errors.New("did not contain a structured value")
}

func emptyJSONObject(value []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil && len(object) == 0
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func isPiJSONEventType(value string) bool {
	switch value {
	case "session", "agent_start", "agent_end", "turn_start", "turn_end", "message_start", "message_update", "message_end",
		"tool_execution_start", "tool_execution_update", "tool_execution_end", "queue_update", "compaction_start", "compaction_end",
		"auto_retry_start", "auto_retry_end", "summarization_retry_scheduled", "summarization_retry_attempt_start", "summarization_retry_finished":
		return true
	default:
		return false
	}
}
