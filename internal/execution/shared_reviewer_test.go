package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func reviewerAssignment() Assignment {
	return Assignment{Spec: Spec{
		Task:                   Task{Title: "Detect regression", Instructions: "Inspect behavior.txt and reject the seeded regression."},
		Repository:             "owner/repo",
		DelegatedContentDigest: "v1:approved",
		ApprovedBodySnapshot:   "## Proof obligations\n- behavior.txt contains ready\n- git diff --check passes",
		RequiredVerification:   []string{"behavior.txt contains ready", "git diff --check passes"},
		RecordedVerification:   []VerificationEvidence{{Criterion: "behavior.txt contains ready", Evidence: "focused check passed at the candidate commit"}},
		ReviewRequired:         true,
	}}
}

func failingReviewerContent() string {
	return `{"criteria":{"P1":{"status":"failed","summary":"The seeded behavior is wrong.","evidence":["behavior.txt contains broken."]},"P2":{"status":"passed","summary":"The diff is syntactically clean.","evidence":["git diff --check returned no findings."]}},"repository_rules":{"status":"failed","summary":"The exact file-content contract is broken.","evidence":["behavior.txt:1 contains broken."]},"maintainability":{"status":"passed","summary":"The change remains localized.","evidence":["Only the fixture content changed."]},"summary":"The seeded regression requires changes."}`
}

func TestReviewerAuditSchemaUsesFixedProofKeys(t *testing.T) {
	schema, err := reviewerAuditSchema(2)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	properties := decoded["properties"].(map[string]any)
	for _, required := range []string{"criteria", "repository_rules", "maintainability", "summary"} {
		if _, exists := properties[required]; !exists {
			t.Fatalf("reviewer schema omitted %q: %s", required, schema)
		}
	}
	criteria := properties["criteria"].(map[string]any)
	criterionProperties := criteria["properties"].(map[string]any)
	if len(criterionProperties) != 2 || criterionProperties["P1"] == nil || criterionProperties["P2"] == nil || criteria["additionalProperties"] != false {
		t.Fatalf("criteria are not fixed by Runner-owned keys: %#v", criteria)
	}
	for _, forbidden := range []string{"outcome", "blocker", "verdict", "review_assessment", "work_done", "verification"} {
		if bytes.Contains(schema, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("reviewer schema retained Runner-owned field %q: %s", forbidden, schema)
		}
	}
	if !bytes.Contains(schema, []byte(`"check_required"`)) || bytes.Contains(schema, []byte(`"blocked"`)) {
		t.Fatalf("evidence-audit schema does not defer only unresolved dynamic checks: %s", schema)
	}
}

func TestReviewerAuditPromptBindsProofsAndDefersDynamicChecks(t *testing.T) {
	assignment := reviewerAssignment()
	prompt := reviewerAuditPrompt(assignment, "Pi CLI")
	for _, required := range []string{
		`"key":"P1"`, `"key":"P2"`, assignment.Spec.RequiredVerification[0], assignment.Spec.RequiredVerification[1],
		"focused check passed at the candidate commit", "The implementer owns how proof is produced",
		"source and evidence triage, not test execution", "Do not run tests", "establishes failure for that exact behavior",
		"It does not complete the audit of the whole proof key or path", "A failed key records status; it is not a stop signal",
		"inspect directly adjacent card-owned paths, operations, and state transitions", "Group all independent blockers",
		"one or more blocking violations",
		"fresh focused-verification stage containing only the unresolved checks",
	} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("reviewer prompt omitted %q:\n%s", required, prompt)
		}
	}
	for _, forbidden := range []string{
		"At most one", "criterion stage", "static stage", "Runner may invoke",
		"Record it without further diagnostics on that path", "stop investigating that path",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("reviewer prompt retained staged or numeric orchestration %q:\n%s", forbidden, prompt)
		}
	}
}

func TestReviewerChecksUseEvidenceWhenRedundantSummaryIsOmitted(t *testing.T) {
	audit := `{"criteria":{"P1":{"status":"passed","evidence":["behavior.txt contains ready."]},"P2":{"status":"passed","summary":"The diff is clean.","evidence":["git diff --check passed."]}},"repository_rules":{"status":"passed","summary":"No rule violation was found.","evidence":["The cumulative diff follows the repository instructions."]},"maintainability":{"status":"passed","summary":"The change is focused.","evidence":["Only the intended fixture changed."]},"summary":"The candidate satisfies the audited obligations."}`
	content, err := decodeReviewerAuditContent(reviewerAssignment(), audit)
	if err != nil {
		t.Fatalf("decode reviewer audit with omitted redundant summary: %v", err)
	}
	if content.Criteria["P1"].Summary != "behavior.txt contains ready." {
		t.Fatalf("reviewer audit did not reuse concrete evidence as its summary: %#v", content.Criteria["P1"])
	}

	resolution := `{"checks":{"P2":{"status":"passed","evidence":["git diff --check exited successfully."]}},"summary":"The unresolved check passed."}`
	resolved, err := decodeReviewerResolutionContent([]reviewerUnresolvedCheck{{Key: "P2"}}, resolution)
	if err != nil {
		t.Fatalf("decode reviewer resolution with omitted redundant summary: %v", err)
	}
	if resolved.Checks["P2"].Summary != "git diff --check exited successfully." {
		t.Fatalf("reviewer resolution did not reuse concrete evidence as its summary: %#v", resolved.Checks["P2"])
	}
}

func TestAssembleReviewerContentDerivesBlockedOutcome(t *testing.T) {
	value := `{"criteria":{"P1":{"status":"blocked","summary":"The file could not be read.","evidence":["The approved read capability returned permission denied."]},"P2":{"status":"passed","summary":"The diff is clean.","evidence":["git diff --check passed."]}},"repository_rules":{"status":"passed","summary":"No repository-rule defect was identified.","evidence":["The available diff showed no rule violation."]},"maintainability":{"status":"blocked","summary":"Maintainability could not be inspected.","evidence":["Source reads remained unavailable."]},"summary":"Required review evidence is unavailable."}`
	structured, err := assembleReviewerContent(reviewerAssignment(), value)
	if err != nil {
		t.Fatalf("assemble blocked reviewer content: %v", err)
	}
	if structured.Outcome != OutcomeNeedsInput || structured.Blocker == nil || *structured.Blocker != incompleteReviewerBlocker || structured.ReviewAssessment == nil || structured.ReviewAssessment.Verdict != "blocked" {
		t.Fatalf("Runner-derived blocked authority is wrong: %#v", structured)
	}
	output := reviewerExecutorOutput(structured)
	if output.FailureClass != FailureCapabilityUnavailable || output.RetryDisposition != RetryManual || !output.RemoteDetailSafe {
		t.Fatalf("blocked reviewer output is not manually retryable: %#v", output)
	}
}

func TestAssembleReviewerContentRetainsBlockedRepositoryRuleEvidence(t *testing.T) {
	value := `{"criteria":{"P1":{"status":"passed","summary":"The behavior is correct.","evidence":["behavior.txt contains ready."]},"P2":{"status":"passed","summary":"The diff is clean.","evidence":["git diff --check passed."]}},"repository_rules":{"status":"blocked","summary":"A required policy command is unavailable.","evidence":["The repository-required policy checker is not installed."]},"maintainability":{"status":"passed","summary":"The change remains localized.","evidence":["Only the intended fixture changed."]},"summary":"Repository-rule evidence is unavailable."}`
	structured, err := assembleReviewerContent(reviewerAssignment(), value)
	if err != nil {
		t.Fatalf("assemble repository-rule blocker: %v", err)
	}
	if structured.ReviewAssessment == nil || structured.ReviewAssessment.Verdict != "blocked" ||
		len(structured.ReviewAssessment.Rules[0].Findings) != 1 || structured.ReviewAssessment.Rules[0].Findings[0].Severity != "warning" ||
		!strings.Contains(strings.Join(structured.Verification, "\n"), "policy checker is not installed") {
		t.Fatalf("blocked repository-rule evidence was lost: %#v", structured)
	}
}

func TestAssembleReviewerContentPrefersKnownFailureOverBlockedCheck(t *testing.T) {
	value := strings.Replace(failingReviewerContent(), `"maintainability":{"status":"passed"`, `"maintainability":{"status":"blocked"`, 1)
	structured, err := assembleReviewerContent(reviewerAssignment(), value)
	if err != nil {
		t.Fatalf("assemble mixed reviewer content: %v", err)
	}
	if structured.Outcome != OutcomeSucceeded || structured.Blocker != nil || structured.ReviewAssessment == nil || structured.ReviewAssessment.Verdict != "needs_changes" {
		t.Fatalf("known failure did not take precedence: %#v", structured)
	}
}

func TestAssembleReviewerContentDerivesRunnerAuthority(t *testing.T) {
	assignment := reviewerAssignment()
	structured, err := assembleReviewerContent(assignment, failingReviewerContent())
	if err != nil {
		t.Fatalf("assemble reviewer content: %v", err)
	}
	assessment := structured.ReviewAssessment
	if structured.Outcome != OutcomeSucceeded || structured.Blocker != nil || assessment == nil || assessment.Verdict != "needs_changes" {
		t.Fatalf("Runner-derived result authority is wrong: %#v", structured)
	}
	if assessment.Criteria[0].Criterion != assignment.Spec.RequiredVerification[0] || assessment.Criteria[1].Criterion != assignment.Spec.RequiredVerification[1] {
		t.Fatalf("Runner did not bind immutable criteria by position: %#v", assessment.Criteria)
	}
	rule := assessment.Rules[0]
	if rule.RuleSourceID != "repository_instructions" || rule.RuleSourceVersion != "current" || rule.Status != "failed" || len(rule.Findings) != 1 {
		t.Fatalf("Runner did not derive rule authority and status: %#v", rule)
	}
	if len(structured.WorkDone) != 1 || len(structured.Verification) != 4 {
		t.Fatalf("Runner did not assemble deterministic completion evidence: %#v", structured)
	}
}

func TestAssembleReviewerContentCanonicalizesKnownRepresentationResidue(t *testing.T) {
	value := strings.TrimSuffix(failingReviewerContent(), "}") + `,"type":"object"}`
	structured, err := assembleReviewerContent(reviewerAssignment(), value)
	if err != nil || structured.ReviewAssessment == nil || structured.ReviewAssessment.Verdict != "needs_changes" {
		t.Fatalf("reviewer canonicalization failed: result=%#v error=%v", structured, err)
	}
}

func TestAssembleReviewerContentRejectsInvalidOrAmbiguousContent(t *testing.T) {
	assignment := reviewerAssignment()
	for name, value := range map[string]string{
		"unknown field":     strings.TrimSuffix(failingReviewerContent(), "}") + `,"verdict":"accept"}`,
		"missing criterion": strings.Replace(failingReviewerContent(), `,"P2":{"status":"passed","summary":"The diff is syntactically clean.","evidence":["git diff --check returned no findings."]}`, "", 1),
		"unknown criterion": strings.Replace(failingReviewerContent(), `"P2":`, `"P3":`, 1),
		"empty rule proof":  strings.Replace(failingReviewerContent(), `"behavior.txt:1 contains broken."`, `" "`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := assembleReviewerContent(assignment, value); err == nil {
				t.Fatalf("invalid reviewer content was accepted: %s", value)
			}
		})
	}
}

type sharedReviewerHarnessRunner struct {
	response         string
	responses        []string
	responseIndex    int
	args             []string
	allArgs          [][]string
	inputs           []string
	directSchemas    [][]byte
	extensionSources []string
	timeouts         []time.Duration
}

func (r *sharedReviewerHarnessRunner) Run(_ context.Context, command string, args []string, _ string, timeout time.Duration) (subprocess.Result, error) {
	r.args = append([]string(nil), args...)
	r.allArgs = append(r.allArgs, append([]string(nil), args...))
	r.timeouts = append(r.timeouts, timeout)
	response := r.response
	if len(r.responses) > 0 {
		if r.responseIndex >= len(r.responses) {
			return subprocess.Result{}, errors.New("reviewer launched more model calls than the test supplied")
		}
		response = r.responses[r.responseIndex]
		r.responseIndex++
	}
	switch command {
	case config.HarnessCodexCLI:
		for index := 0; index+1 < len(args); index++ {
			switch args[index] {
			case "--output-schema":
				schema, err := os.ReadFile(args[index+1])
				if err != nil {
					return subprocess.Result{}, err
				}
				r.directSchemas = append(r.directSchemas, schema)
			case "--output-last-message":
				if err := os.WriteFile(args[index+1], []byte(response), 0o600); err != nil {
					return subprocess.Result{}, err
				}
			}
		}
		return subprocess.Result{}, nil
	case config.HarnessClaudeCLI:
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--json-schema" {
				r.directSchemas = append(r.directSchemas, []byte(args[index+1]))
			}
		}
		return subprocess.Result{Stdout: `{"structured_output":` + response + `}`}, nil
	case config.HarnessPiCLI:
		hasExtension := false
		for index := 0; index+1 < len(args); index++ {
			if args[index] == "--extension" {
				hasExtension = true
				source, err := os.ReadFile(args[index+1])
				if err != nil {
					return subprocess.Result{}, err
				}
				r.extensionSources = append(r.extensionSources, string(source))
			}
		}
		if !hasExtension {
			message, err := json.Marshal(map[string]any{
				"role":       "assistant",
				"content":    []map[string]string{{"type": "text", "text": response}},
				"stopReason": "stop",
			})
			if err != nil {
				return subprocess.Result{}, err
			}
			return subprocess.Result{Stdout: strings.Join([]string{
				`{"type":"session","version":3}`,
				`{"type":"message_end","message":` + string(message) + `}`,
				`{"type":"agent_end","messages":[]}`,
			}, "\n")}, nil
		}
		output, err := piToolEventStreamForArgs(args, response)
		return subprocess.Result{Stdout: output}, err
	default:
		return subprocess.Result{}, errors.New("unexpected command: " + command)
	}
}

func (r *sharedReviewerHarnessRunner) recordInput(input io.Reader) error {
	data, err := io.ReadAll(input)
	if err == nil {
		r.inputs = append(r.inputs, string(data))
	}
	return err
}

func (r *sharedReviewerHarnessRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	if err := r.recordInput(input); err != nil {
		return subprocess.Result{}, err
	}
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *sharedReviewerHarnessRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	if err := r.recordInput(input); err != nil {
		return subprocess.Result{}, err
	}
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *sharedReviewerHarnessRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	if err := r.recordInput(input); err != nil {
		return subprocess.Result{}, err
	}
	return r.Run(ctx, command, args, dir, timeout)
}

func TestAllReviewersUseOneSharedAuditSchemaWhenEvidenceIsConclusive(t *testing.T) {
	assignment := reviewerAssignment()
	wantSchema, err := reviewerAuditSchema(len(assignment.Spec.RequiredVerification))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			run := &sharedReviewerHarnessRunner{response: failingReviewerContent()}
			enabled := true
			cfg := config.ExecutionConfig{
				Skills: []string{"runner-reviewer"}, SafeTools: true,
				Harness: config.HarnessConfig{
					Kind: kind, Command: kind, Enabled: &enabled, WorkingDir: t.TempDir(), WorkspaceWriteRoot: t.TempDir(), TimeoutSeconds: 30,
				},
			}
			if kind == config.HarnessPiCLI {
				cfg.RoleAccess = config.RoleAccessHost
			}
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				output, err = NewCodexExecutor(cfg, run).Execute(t.Context(), assignment)
			} else {
				output, err = NewAgentExecutor(kind, cfg, run).Execute(t.Context(), assignment)
			}
			if err != nil || output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "needs_changes" {
				t.Fatalf("shared reviewer failed: output=%#v err=%v", output, err)
			}
			if len(run.inputs) != 1 || len(run.allArgs) != 1 || len(run.timeouts) != 1 {
				t.Fatalf("reviewer calls = inputs %d args %d timeouts %d, want one", len(run.inputs), len(run.allArgs), len(run.timeouts))
			}
			if kind == config.HarnessPiCLI {
				structuredExtensions := 0
				for _, source := range run.extensionSources {
					if strings.Contains(source, `constrainedSampling: { type: "json_schema", strict: "require" }`) {
						structuredExtensions++
					}
				}
				if structuredExtensions != 1 {
					t.Fatalf("Pi did not use the one shared structured result: %#v", run.extensionSources)
				}
			} else {
				if len(run.directSchemas) != 1 || !bytes.Equal(bytes.TrimSpace(run.directSchemas[0]), bytes.TrimSpace(wantSchema)) {
					t.Fatalf("%s did not use the shared schema:\n%s", kind, strings.Join(byteSlicesToStrings(run.directSchemas), "\n"))
				}
			}
		})
	}
}

func TestAllReviewersUseFreshFocusedStageOnlyForUnresolvedProofs(t *testing.T) {
	assignment := reviewerAssignment()
	wantAuditSchema, err := reviewerAuditSchema(len(assignment.Spec.RequiredVerification))
	if err != nil {
		t.Fatal(err)
	}
	wantResolutionSchema, err := reviewerResolutionSchema([]reviewerUnresolvedCheck{{Key: "P2"}})
	if err != nil {
		t.Fatal(err)
	}
	audit := `{"criteria":{"P1":{"status":"passed","summary":"Recorded evidence matches the source.","evidence":["behavior.txt and its durable test agree."]},"P2":{"status":"check_required","summary":"Does git diff --check pass for the exact candidate?","evidence":["No candidate-bound result was recorded for this obligation."]}},"repository_rules":{"status":"passed","summary":"The source audit found no blocking rule violation.","evidence":["The cumulative diff follows the repository instructions."]},"maintainability":{"status":"passed","summary":"The change is focused and readable.","evidence":["The diff changes only the intended fixture."]},"summary":"One proof obligation requires a focused command."}`
	resolution := `{"checks":{"P2":{"status":"passed","summary":"The exact candidate is clean.","evidence":["git diff --check exited successfully."]}},"summary":"The one unresolved command passed."}`
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			run := &sharedReviewerHarnessRunner{responses: []string{audit, resolution}}
			enabled := true
			cfg := config.ExecutionConfig{
				Skills: []string{"runner-reviewer"}, SafeTools: true,
				Harness: config.HarnessConfig{
					Kind: kind, Command: kind, Enabled: &enabled, WorkingDir: t.TempDir(), WorkspaceWriteRoot: t.TempDir(), TimeoutSeconds: 30,
				},
			}
			if kind == config.HarnessPiCLI {
				cfg.RoleAccess = config.RoleAccessHost
			}
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				output, err = NewCodexExecutor(cfg, run).Execute(t.Context(), assignment)
			} else {
				output, err = NewAgentExecutor(kind, cfg, run).Execute(t.Context(), assignment)
			}
			if err != nil || output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "accept" {
				t.Fatalf("focused shared reviewer failed: output=%#v err=%v", output, err)
			}
			if len(run.inputs) != 2 || len(run.allArgs) != 2 {
				t.Fatalf("reviewer calls = inputs %d args %d, want one audit and one focused verification", len(run.inputs), len(run.allArgs))
			}
			focusedPrompt := run.inputs[1]
			if !strings.Contains(focusedPrompt, `"key":"P2"`) || !strings.Contains(focusedPrompt, assignment.Spec.RequiredVerification[1]) || strings.Contains(focusedPrompt, `"key":"P1"`) || strings.Contains(focusedPrompt, assignment.Spec.RequiredVerification[0]) {
				t.Fatalf("focused stage did not contain only unresolved proof P2:\n%s", focusedPrompt)
			}
			if kind == config.HarnessPiCLI {
				structuredExtensions := 0
				for _, source := range run.extensionSources {
					if strings.Contains(source, `constrainedSampling: { type: "json_schema", strict: "require" }`) {
						structuredExtensions++
					}
				}
				if structuredExtensions != 2 {
					t.Fatalf("Pi focused review used %d structured stages, want two", structuredExtensions)
				}
			} else if len(run.directSchemas) != 2 ||
				!bytes.Equal(bytes.TrimSpace(run.directSchemas[0]), bytes.TrimSpace(wantAuditSchema)) ||
				!bytes.Equal(bytes.TrimSpace(run.directSchemas[1]), bytes.TrimSpace(wantResolutionSchema)) {
				t.Fatalf("%s did not use one audit and one focused schema:\n%s", kind, strings.Join(byteSlicesToStrings(run.directSchemas), "\n"))
			}
		})
	}
}

func TestReviewerContinuesIndependentFocusedVerificationAfterAuditFailure(t *testing.T) {
	assignment := reviewerAssignment()
	audit := `{"criteria":{"P1":{"status":"failed","summary":"The source contains the wrong value.","evidence":["behavior.txt contains broken."]},"P2":{"status":"check_required","summary":"Does git diff --check pass?","evidence":["No candidate-bound command result is recorded."]}},"repository_rules":{"status":"passed","summary":"No separate rule violation was found.","evidence":["The one static pass found no additional violation."]},"maintainability":{"status":"passed","summary":"The change is localized.","evidence":["Only the intended fixture changed."]},"summary":"A concrete source defect requires changes."}`
	resolution := `{"checks":{"P2":{"status":"passed","summary":"The exact candidate is clean.","evidence":["git diff --check exited successfully."]}},"summary":"The independent unresolved command passed."}`
	run := &sharedReviewerHarnessRunner{responses: []string{audit, resolution}}
	enabled := true
	cfg := config.ExecutionConfig{Skills: []string{"runner-reviewer"}, SafeTools: true, Harness: config.HarnessConfig{
		Kind: config.HarnessCodexCLI, Command: config.HarnessCodexCLI, Enabled: &enabled,
		WorkingDir: t.TempDir(), WorkspaceWriteRoot: t.TempDir(), TimeoutSeconds: 30,
	}}
	output, err := NewCodexExecutor(cfg, run).Execute(t.Context(), assignment)
	if err != nil || output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "needs_changes" {
		t.Fatalf("audit failure was not returned directly: output=%#v err=%v", output, err)
	}
	if len(run.inputs) != 2 || output.ReviewAssessment.Criteria[0].Status != "failed" || output.ReviewAssessment.Criteria[1].Status != "passed" {
		t.Fatalf("reviewer did not complete the independent check: calls=%d assessment=%#v", len(run.inputs), output.ReviewAssessment)
	}
	if !strings.Contains(run.inputs[1], `"key":"P2"`) || strings.Contains(run.inputs[1], `"key":"P1"`) ||
		!strings.Contains(run.inputs[1], "Complete the rest of that bounded check and every other supplied check independently") ||
		!strings.Contains(run.inputs[1], "report every concrete failing case encountered") ||
		strings.Contains(run.inputs[1], "stop investigating that path") {
		t.Fatalf("focused verification did not isolate the unresolved check:\n%s", run.inputs[1])
	}
}

func byteSlicesToStrings(values [][]byte) []string {
	result := make([]string, len(values))
	for index := range values {
		result[index] = string(values[index])
	}
	return result
}
