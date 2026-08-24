package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

type testAssignmentResponse struct {
	Assignments []Assignment
}

func testCodexConfig(t *testing.T) config.ExecutionConfig {
	t.Helper()
	return config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD",
		Harness: config.HarnessConfig{
			Kind:            config.HarnessCodexCLI,
			Command:         "codex",
			WorkingDir:      t.TempDir(),
			TimeoutSeconds:  30,
			Model:           ptr("configured-model"),
			ReasoningEffort: "medium",
		},
	}
}

func testCodexCLIAssignmentSpec() Spec {
	return Spec{
		ID: "assignment_codex_1", ItemID: "PVTI_codex_1", Repository: "owner/repo",
		DelegatedContentDigest: "v1:test-delegated-content",
		Task: Task{
			Title:        "Read runner docs",
			Instructions: "Inspect the runner README and summarize the analysis adapter contract. Do not edit files.",
		},
		ContextRefs:          []string{"https://github.com/cortexium-io/runner/issues/4"},
		RequiredVerification: []string{"codex_completed", "codex_project_contract"},
	}
}

func testPollResponse(packets ...Spec) testAssignmentResponse {
	assignments := make([]Assignment, 0, len(packets))
	for _, packet := range packets {
		assignments = append(assignments, Assignment{Spec: packet})
	}
	return testAssignmentResponse{Assignments: assignments}
}

func passingMockReviewAssessment(packet Spec) *ReviewAssessment {
	criteria := make([]ReviewCriterionResult, 0, len(packet.RequiredVerification))
	for _, criterion := range packet.RequiredVerification {
		criteria = append(criteria, ReviewCriterionResult{
			Criterion: criterion,
			Status:    "passed",
			Summary:   "The test reviewer satisfied this required verification signal.",
			Evidence:  []string{"test review completion"},
		})
	}
	rules := []ReviewRuleResult{{
		RuleSourceID: "repository_instructions", RuleSourceVersion: "current", Status: "passed",
		Summary: "The test reviewer evaluated the mandatory repository rules.", Findings: []ReviewRuleFinding{},
	}}
	return &ReviewAssessment{
		Criteria: criteria,
		Rules:    rules,
		Maintainability: ReviewMaintainabilityResult{
			Status:   "passed",
			Summary:  "The test reviewer found no maintainability blocker.",
			Evidence: []string{"deterministic test review"},
		},
		Verdict: "accept",
		Summary: "The test reviewer passed acceptance criteria, mandatory rules, and maintainability.",
	}
}

func testStructuredResult(summary string) []byte {
	value, err := json.Marshal(executionContent{
		Outcome:      OutcomeSucceeded,
		Summary:      summary,
		WorkDone:     []string{"Completed the approved local assignment."},
		Verification: []string{"Completed the deterministic test verification."},
		Blockers:     []string{},
	})
	if err != nil {
		panic(err)
	}
	return value
}

func validExecutionContentJSON(summary string) string {
	return `{"outcome":"succeeded","summary":"` + summary + `","work_done":["completed the assigned work"],"verification":["checked the completed work"],"blockers":[]}`
}

func piToolEventStream(value, provenance string) string {
	return strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"test-call","toolName":"` + piStructuredResultTool + `","args":` + value + `}`,
		`{"type":"tool_execution_end","toolCallId":"test-call","toolName":"` + piStructuredResultTool + `","result":{"details":{"provenance":` + strconv.Quote(provenance) + `,"arguments":` + value + `}},"isError":false}`,
		`{"type":"agent_end","messages":[]}`,
	}, "\n")
}

func piNativeStructuredEventStream(value, provenance string) string {
	return strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"agent_start"}`,
		`{"type":"tool_execution_start","toolCallId":"test-finalize","toolName":"` + piNativeStructuredFinalizeTool + `","args":{}}`,
		`{"type":"tool_execution_end","toolCallId":"test-finalize","toolName":"` + piNativeStructuredFinalizeTool + `","result":{"details":{"provenance":` + strconv.Quote(provenance) + `}},"isError":false}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(value) + `}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":12,"output":5,"reasoning":0,"cost":{"total":0}}}]}`,
	}, "\n")
}

func piDirectNativeStructuredEventStream(value string) string {
	return strings.Join([]string{
		`{"type":"session","version":3}`,
		`{"type":"agent_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":` + strconv.Quote(value) + `}],"stopReason":"stop"}}`,
		`{"type":"agent_end","messages":[{"role":"assistant","usage":{"input":12,"output":5,"reasoning":0,"cost":{"total":0}}}]}`,
	}, "\n")
}

func piToolEventStreamForArgs(args []string, value string) (string, error) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] != "--extension" {
			continue
		}
		content, err := os.ReadFile(args[index+1])
		if err != nil {
			return "", err
		}
		const marker = `const runnerResultProvenance = `
		start := strings.Index(string(content), marker)
		if start < 0 {
			return "", fmt.Errorf("Pi extension lacks result provenance")
		}
		quoted := strings.TrimSpace(strings.SplitN(string(content)[start+len(marker):], ";", 2)[0])
		provenance, err := strconv.Unquote(quoted)
		if err != nil {
			return "", err
		}
		if strings.Contains(string(content), piNativeStructuredFinalizeTool) {
			return piNativeStructuredEventStream(value, provenance), nil
		}
		if strings.Contains(string(content), "runnerDirectNativeStructuredResult = true") {
			return piDirectNativeStructuredEventStream(value), nil
		}
		return piToolEventStream(value, provenance), nil
	}
	return "", fmt.Errorf("Pi invocation lacks extension")
}

func ptr(value string) *string { return &value }

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsArgPair(args []string, first, second string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == first && args[index+1] == second {
			return true
		}
	}
	return false
}
