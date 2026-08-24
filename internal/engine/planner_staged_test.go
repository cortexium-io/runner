package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestStagedProjectPlannerAssemblesFixedKeyPlan(t *testing.T) {
	responses := []string{
		`{"goal_summary":"Ship authenticated exports","project_success_criteria":["Retries create one job"],"project_constraints":["Keep tenants isolated"],"open_decisions":[],"cards":[{"title":"Build export endpoint","dependencies":[]},{"title":"Verify concurrency","dependencies":[1]}]}`,
		`{"cards":{"C1":{"objective":"Create the authenticated endpoint.","done_when":["Repeated tenant keys return the original job."],"proof_obligations":["Tenant-scoped idempotency is demonstrated."],"assumptions":["Use the existing export store."]},"C2":{"objective":"Exercise concurrent duplicate requests.","done_when":["Only one job is created under contention."],"proof_obligations":["Concurrent duplicate requests are shown to converge on one job."],"assumptions":[]}}}`,
	}
	var prompts []string
	var schemas [][]byte
	call := func(_ context.Context, prompt string, schema []byte) (execution.StructuredHarnessResult, error) {
		index := len(prompts)
		prompts = append(prompts, prompt)
		schemas = append(schemas, append([]byte(nil), schema...))
		return execution.StructuredHarnessResult{
			Message: responses[index], DurationMilliseconds: 100,
			Usage: metrics.Usage{Available: true, InputTokens: 10, OutputTokens: 5, Turns: 1},
		}, nil
	}

	result, err := runStagedProjectPlanner(t.Context(), "Plan the export feature.", "owner/repo", call, call)
	if err != nil {
		t.Fatalf("run shared staged planner: %v", err)
	}
	if len(prompts) != 2 || result.DurationMilliseconds != 200 || result.Usage.InputTokens != 20 || result.Usage.Turns != 2 {
		t.Fatalf("shared stages were not aggregated: calls=%d result=%#v", len(prompts), result)
	}
	plan, err := decodeProjectPlan(result.Message)
	if err != nil {
		t.Fatalf("assembled plan does not satisfy the canonical contract: %v\n%s", err, result.Message)
	}
	if len(plan.WorkItems) != 2 || plan.WorkItems[0].Title != "Build export endpoint" || plan.WorkItems[1].Repository != "owner/repo" {
		t.Fatalf("Runner did not bind fixed titles and repository: %#v", plan.WorkItems)
	}
	if got := plan.WorkItems[1].Dependencies; len(got) != 1 || got[0] != "Build export endpoint" {
		t.Fatalf("dependency was not preserved: %#v", got)
	}
	if got := plan.WorkItems[0].Verification; len(got) != 1 || got[0] != "Tenant-scoped idempotency is demonstrated." {
		t.Fatalf("proof obligation was not preserved: %#v", got)
	}
	if !strings.Contains(prompts[0], "schema ceiling is emergency loop protection") || !strings.Contains(prompts[1], "The implementer will inspect the repository and choose the smallest reliable proof method") {
		t.Fatalf("shared planner prompts omitted the agreed sizing or proof ownership:\n%s\n%s", prompts[0], prompts[1])
	}
	if strings.Contains(prompts[0], "Pi") || strings.Contains(prompts[1], "Pi") || strings.Contains(prompts[1], "exact command") {
		t.Fatalf("shared planner retained harness-specific or prescribed-test language:\n%s\n%s", prompts[0], prompts[1])
	}
	var detailSchema map[string]any
	if err := json.Unmarshal(schemas[1], &detailSchema); err != nil {
		t.Fatal(err)
	}
	cards := detailSchema["properties"].(map[string]any)["cards"].(map[string]any)
	properties := cards["properties"].(map[string]any)
	if len(properties) != 2 || properties["C1"] == nil || properties["C2"] == nil || cards["additionalProperties"] != false {
		t.Fatalf("details schema is not fixed to Runner-owned keys: %#v", cards)
	}
}

func TestStagedProjectPlannerRejectsInvalidOutlineBeforeDetails(t *testing.T) {
	calls := 0
	call := func(_ context.Context, _ string, _ []byte) (execution.StructuredHarnessResult, error) {
		calls++
		return execution.StructuredHarnessResult{Message: `{"goal_summary":"Goal","project_success_criteria":["Works"],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Build","dependencies":[1]}]}`}, nil
	}
	result, err := runStagedProjectPlanner(t.Context(), "Plan it.", "owner/repo", call, call)
	if err == nil || !strings.Contains(err.Error(), "not an earlier card") || calls != 1 {
		t.Fatalf("invalid outline was not rejected before details: calls=%d result=%#v error=%v", calls, result, err)
	}
	if result.FailureClass != execution.FailureInvalidContract || result.RetryDisposition != execution.RetryNone {
		t.Fatalf("invalid staged contract classification was lost: %#v", result)
	}
}

func TestProjectPlanOutlineTreatsExplicitNoDecisionAsEmpty(t *testing.T) {
	outline := projectPlanOutline{
		GoalSummary: "Ship", ProjectSuccessCriteria: []string{"Works"}, ProjectConstraints: []string{},
		OpenDecisions: []string{"No open decisions."}, Cards: []projectPlanOutlineCard{{Title: "Build", Dependencies: []int{}}},
	}
	if err := normalizeProjectPlanOutline(&outline); err != nil {
		t.Fatalf("normalize outline: %v", err)
	}
	if outline.OpenDecisions == nil || len(outline.OpenDecisions) != 0 {
		t.Fatalf("explicit no-decision text was not normalized to []: %#v", outline.OpenDecisions)
	}
}

func TestStagedProjectPlannerRejectsMissingOrUnknownCardKeys(t *testing.T) {
	outline := `{"goal_summary":"Goal","project_success_criteria":["Works"],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Build","dependencies":[]}]}`
	for name, details := range map[string]string{
		"missing": `{"cards":{}}`,
		"unknown": `{"cards":{"C2":{"objective":"Build it","done_when":["Works"],"proof_obligations":["Behavior is demonstrated"],"assumptions":[]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			call := func(_ context.Context, _ string, _ []byte) (execution.StructuredHarnessResult, error) {
				calls++
				if calls == 1 {
					return execution.StructuredHarnessResult{Message: outline}, nil
				}
				return execution.StructuredHarnessResult{Message: details}, nil
			}
			if _, err := runStagedProjectPlanner(t.Context(), "Plan it.", "owner/repo", call, call); err == nil {
				t.Fatal("invalid fixed-key details were accepted")
			}
		})
	}
}

func TestStagedProjectPlannerPreservesPriorUsageOnStageFailure(t *testing.T) {
	calls := 0
	call := func(_ context.Context, _ string, _ []byte) (execution.StructuredHarnessResult, error) {
		calls++
		result := execution.StructuredHarnessResult{
			DurationMilliseconds: 25,
			Usage:                metrics.Usage{Available: true, InputTokens: 7, OutputTokens: 3, Turns: 1},
		}
		if calls == 1 {
			result.Message = `{"goal_summary":"Goal","project_success_criteria":["Works"],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Build","dependencies":[]}]}`
			return result, nil
		}
		result.FailureClass = execution.FailureTimeout
		result.RetryDisposition = execution.RetryManual
		return result, context.DeadlineExceeded
	}

	result, err := runStagedProjectPlanner(t.Context(), "Plan it.", "owner/repo", call, call)
	if !errors.Is(err, context.DeadlineExceeded) || calls != 2 || result.DurationMilliseconds != 50 || result.Usage.InputTokens != 14 || result.Usage.Turns != 2 {
		t.Fatalf("partial staged evidence was lost: calls=%d result=%#v error=%v", calls, result, err)
	}
	if result.FailureClass != execution.FailureTimeout || result.RetryDisposition != execution.RetryManual {
		t.Fatalf("typed stage failure was lost: %#v", result)
	}
}
