package execution

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func specialistAssignment() Assignment {
	spec := testCodexCLIWorkspaceWriteAssignmentSpec()
	spec.RequiredVerification = []string{"Selection preserves the intended target."}
	spec.TestSpecialist = &TestSpecialistCapability{AllowedPaths: []string{"selection_test.go"}}
	return Assignment{Spec: spec}
}

func specialistRequest() *TestSpecialistRequest {
	return &TestSpecialistRequest{CriterionIndices: []int{0}, Reason: "Cover the changed boundary.", ExistingChecks: []string{}, Paths: []string{"selection_test.go"}}
}

func TestTestSpecialistRequestContractAndNativeSchemas(t *testing.T) {
	assignment := specialistAssignment()
	content := map[string]any{"outcome": OutcomeTestRequested, "summary": "Retained implementation needs a focused check.", "work_done": []string{"Kept implementation."}, "verification": []string{}, "blockers": []string{}, "test_request": specialistRequest()}
	encode := func() string {
		t.Helper()
		encoded, err := json.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	result, err := assembleImplementationContent(assignment, encode())
	if err != nil || result.TestRequest == nil || result.Outcome != OutcomeTestRequested {
		t.Fatalf("valid handoff: %#v %v", result, err)
	}
	if _, err := assembleExecutionContent(assignment, encode()); err == nil {
		t.Fatal("ordinary execution accepted specialist request")
	}
	without := assignment
	without.Spec.TestSpecialist = nil
	if _, err := assembleImplementationContent(without, encode()); err == nil {
		t.Fatal("request created its own authority")
	}
	for _, scenario := range []string{"unknown-criterion", "duplicate", "wrong-path", "control", "missing-inspection", "oversized-reason"} {
		t.Run(scenario, func(t *testing.T) {
			request := specialistRequest()
			switch scenario {
			case "unknown-criterion":
				request.CriterionIndices = []int{1}
			case "duplicate":
				request.Paths = append(request.Paths, request.Paths[0])
			case "wrong-path":
				request.Paths = []string{"app.go"}
			case "control":
				request.Paths = []string{"AGENTS.md"}
			case "missing-inspection":
				request.ExistingChecks = nil
			case "oversized-reason":
				request.Reason = strings.Repeat("x", 4097)
			}
			if err := ValidateTestSpecialistRequest(assignment.Spec, request); err == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	content["outcome"] = OutcomeSucceeded
	if _, err := assembleImplementationContent(assignment, encode()); err == nil {
		t.Fatal("success smuggled a handoff")
	}
	for _, enabled := range []bool{false, true} {
		var capability *TestSpecialistCapability
		if enabled {
			capability = assignment.Spec.TestSpecialist
		}
		var schema struct {
			Properties map[string]struct {
				Enum []string `json:"enum"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(implementationContentSchema(1, capability), &schema); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, outcome := range schema.Properties["outcome"].Enum {
			if outcome == OutcomeTestRequested {
				found = true
			}
		}
		if found != enabled {
			t.Fatalf("schema capability not explicit: %t %s", enabled, implementationContentSchema(1, capability))
		}
	}
}

func TestTestSpecialistUnavailableCapabilityRefusedBeforeAnyOperation(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			cfg.Harness.Kind, cfg.Harness.Command = kind, kind
			assignment := specialistAssignment()
			calls := 0
			run := &sharedReviewerHarnessRunner{onRun: func(string) error { calls++; return errors.New("unexpected operation") }}
			capture := &workspaceRequestCapture{err: errors.New("unexpected workspace preparation")}
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				e := NewCodexExecutor(cfg, run)
				e.workspaceProvider = capture
				output, err = e.ExecuteWorkspaceWrite(t.Context(), assignment, nil)
			} else {
				e := NewAgentExecutor(kind, cfg, run)
				e.workspaceProvider = capture
				output, err = e.ExecuteWorkspaceWrite(t.Context(), assignment, nil)
			}
			if err == nil || output.FailureClass != FailureInvalidContract || calls != 0 || len(capture.requests) != 0 {
				t.Fatalf("unapproved capability reached implementation: %#v %v calls%d", output, err, calls)
			}
			cfg.TestSpecialist = &config.TestSpecialistConfig{Enabled: true, AllowedPaths: []string{"different_test.go"}}
			if _, err := ExecuteTestSpecialist(t.Context(), kind, cfg, assignment, specialistRequest(), cfg.Harness.WorkingDir, run); err == nil || calls != 0 {
				t.Fatalf("mismatched policy reached specialist: %v calls%d", err, calls)
			}
		})
	}
}

func TestTestSpecialistNativeInvocationNarrowsProfileAndCannotRecurse(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI} {
		t.Run(kind, func(t *testing.T) {
			cfg := testWorkspaceWriteConfig(t)
			cfg.Harness.Kind, cfg.Harness.Command = kind, kind
			cfg.TestSpecialist = &config.TestSpecialistConfig{Enabled: true, AllowedPaths: []string{"selection_test.go"}}
			cfg.RoleAccess = config.RoleAccessHost
			cfg.HarnessConfigMode = config.HarnessConfigModeIsolated
			cfg.SafeTools = true
			model := "retained-model"
			cfg.Harness.Model = &model
			run := &implementationReferenceRunner{representationResidueRunner: representationResidueRunner{kind: kind, directWorkspace: true, result: validExecutionContentJSON("Justified no additional check.")}}
			output, err := ExecuteTestSpecialist(t.Context(), kind, cfg, specialistAssignment(), specialistRequest(), cfg.Harness.WorkingDir, run)
			if err != nil || output.Outcome != OutcomeSucceeded || len(run.harnessDirs) != 1 {
				t.Fatalf("native specialist: %#v %v calls%d", output, err, len(run.harnessDirs))
			}
			args := strings.Join(run.args, " ")
			if argumentValue(run.args, "--model") != model || strings.Contains(args, "danger-full-access") || strings.Contains(args, "bypassPermissions") || strings.Contains(args, "runner-host") {
				t.Fatalf("profile/model drift: %s", args)
			}
			if kind == config.HarnessCodexCLI && (!strings.Contains(args, "mcp_servers={}") || !strings.Contains(args, "--ignore-user-config")) {
				t.Fatalf("ambient Codex authority: %s", args)
			}
			if kind == config.HarnessClaudeCLI && (!strings.Contains(args, "--strict-mcp-config") || !strings.Contains(args, "--settings")) {
				t.Fatalf("ambient Claude authority: %s", args)
			}
			if strings.Contains(run.prompt, "Runner permits one explicitly requested") || !strings.Contains(run.prompt, "Original approved proof obligations") {
				t.Fatal("recursive capability or missing criteria context")
			}
		})
	}
}
