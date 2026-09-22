package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

const implementationRepairResult = `{"outcome":"repair_needed","summary":"Retained work needs a bounded repair.","work_done":["Implemented selection."],"verification":["selection.spec.ts:42 fails: edit target missing."],"blockers":["Correct the selection transition and rerun its focused test."]}`

func TestRepairResultIsImplementationOnlyAndRequiresEvidence(t *testing.T) {
	assignment := Assignment{Spec: testCodexCLIWorkspaceWriteAssignmentSpec()}
	result, err := assembleImplementationContent(assignment, implementationRepairResult)
	if err != nil || result.Outcome != OutcomeRepairNeeded || result.Blocker == nil {
		t.Fatalf("valid repair request rejected: %#v %v", result, err)
	}
	if _, err := assembleExecutionContent(assignment, implementationRepairResult); err == nil {
		t.Fatal("non-implementation result accepted repair_needed")
	}
	assignment.Spec.ReviewRequired = true
	if _, err := assembleImplementationContent(assignment, implementationRepairResult); err == nil {
		t.Fatal("review assignment accepted implementation repair")
	}
	assignment.Spec.ReviewRequired = false
	for _, field := range []string{"work_done", "verification", "blockers"} {
		t.Run(field, func(t *testing.T) {
			var content map[string]any
			if err := json.Unmarshal([]byte(implementationRepairResult), &content); err != nil {
				t.Fatal(err)
			}
			content[field] = []string{}
			encoded, _ := json.Marshal(content)
			if _, err := assembleImplementationContent(assignment, string(encoded)); err == nil {
				t.Fatalf("repair request without %s accepted", field)
			}
		})
	}
	if strings.Contains(string(executionContentSchema), "repair_needed") || !strings.Contains(string(implementationContentSchema(1)), "repair_needed") {
		t.Fatal("repair outcome crossed the schema role boundary")
	}
}

func TestSupportedImplementersReturnRepairRequestWithoutGrantingRetryAuthority(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			cfg.Harness.Kind, cfg.Harness.Command = kind, kind
			if kind == config.HarnessPiCLI {
				cfg.RoleAccess = config.RoleAccessHost
			}
			run := &representationResidueRunner{kind: kind, result: implementationRepairResult}
			assignment := Assignment{Spec: testCodexCLIWorkspaceWriteAssignmentSpec()}
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				output, err = NewCodexExecutor(cfg, run).ExecuteWorkspaceWrite(t.Context(), assignment, nil)
			} else {
				output, err = NewAgentExecutor(kind, cfg, run).ExecuteWorkspaceWrite(t.Context(), assignment, nil)
			}
			if err != nil || output.Outcome != OutcomeRepairNeeded || output.FailureClass != FailureImplementationRepair || output.RetryDisposition != RetryManual || output.RemoteDetailSafe || len(run.harnessDirs) != 1 {
				t.Fatalf("adapter lost repair request or granted retries itself: %#v err=%v calls=%d", output, err, len(run.harnessDirs))
			}
		})
	}
}
