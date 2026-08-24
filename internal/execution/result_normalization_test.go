package execution

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestNormalizeStructuredResultOnlyUnwrapsOneWholeResponseObjectFence(t *testing.T) {
	object := `{"answer":"planned"}`
	tests := map[string]struct {
		input string
		want  string
	}{
		"raw object":        {input: " \n" + object + "\n", want: object},
		"json fence":        {input: "```json\n" + object + "\n```", want: object},
		"uppercase fence":   {input: "```JSON\n" + object + "\n```", want: object},
		"plain fence":       {input: "```\n" + object + "\n```", want: object},
		"surrounding prose": {input: "Result:\n```json\n" + object + "\n```", want: "Result:\n```json\n" + object + "\n```"},
		"two fences":        {input: "```json\n" + object + "\n```\n```json\n" + object + "\n```", want: "```json\n" + object + "\n```\n```json\n" + object + "\n```"},
		"array fence":       {input: "```json\n[]\n```", want: "```json\n[]\n```"},
		"other language":    {input: "```javascript\n" + object + "\n```", want: "```javascript\n" + object + "\n```"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := NormalizeStructuredResult(test.input); got != test.want {
				t.Fatalf("normalized result = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCanonicalizeStructuredResultRemovesOnlyKnownRepresentationResidue(t *testing.T) {
	canonical, err := CanonicalizeStructuredResult("```json\n"+`{"answer":"planned","type":"object"}`+"\n```", "answer")
	if err != nil || canonical != `{"answer":"planned"}` {
		t.Fatalf("canonical result = %q, error=%v", canonical, err)
	}
	for name, value := range map[string]string{
		"unknown extra":   `{"answer":"planned","retry":true}`,
		"wrong type":      `{"answer":"planned","type":"blocked"}`,
		"missing field":   `{"type":"object"}`,
		"multiple values": `{"answer":"planned"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalizeStructuredResult(value, "answer"); err == nil {
				t.Fatalf("accepted %s", value)
			}
		})
	}
}

func TestAssembleExecutionContentDerivesInternalBlockerWithoutNulls(t *testing.T) {
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]
	for _, outcome := range []string{OutcomeSucceeded, OutcomeNeedsInput, OutcomeBlocked} {
		t.Run(outcome, func(t *testing.T) {
			blockers := `[]`
			if outcome != OutcomeSucceeded {
				blockers = `["operator input is required"]`
			}
			value := `{"outcome":"` + outcome + `","summary":"done","work_done":["checked"],"verification":["test passed"],"blockers":` + blockers + `}`
			result, err := assembleExecutionContent(assignment, value)
			if err != nil || result.Outcome != outcome || (outcome == OutcomeSucceeded) != (result.Blocker == nil) {
				t.Fatalf("result=%#v error=%v", result, err)
			}
		})
	}
}

func TestAssembleExecutionContentRejectsNullLegacyAndContradictoryBlockers(t *testing.T) {
	assignment := testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0]
	for name, value := range map[string]string{
		"null blockers":   `{"outcome":"succeeded","summary":"done","work_done":["checked"],"verification":["test passed"],"blockers":null}`,
		"legacy fields":   `{"outcome":"succeeded","summary":"done","work_done":["checked"],"verification":["test passed"],"blocker":null,"review_assessment":null}`,
		"success blocker": `{"outcome":"succeeded","summary":"done","work_done":["checked"],"verification":["test passed"],"blockers":["contradiction"]}`,
		"missing blocker": `{"outcome":"blocked","summary":"waiting","work_done":[],"verification":[],"blockers":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := assembleExecutionContent(assignment, value); err == nil {
				t.Fatalf("accepted invalid execution content: %s", value)
			}
		})
	}
}

func TestAgentExecutorsCanonicalizeTypeResidueLocallyInOneCall(t *testing.T) {
	for _, kind := range []string{config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			run := &oneResultAgentRunner{kind: kind, output: strings.TrimSuffix(validExecutionContentJSON("done"), "}") + `,"type":"object"}`}
			cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
				Kind: kind, Command: kind, WorkingDir: t.TempDir(), TimeoutSeconds: 30,
			}}
			output, err := NewAgentExecutor(kind, cfg, run).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
			if err != nil || output.Outcome != OutcomeSucceeded || run.calls != 1 {
				t.Fatalf("calls=%d output=%#v error=%v", run.calls, output, err)
			}
		})
	}
}

func TestAgentExecutorRejectsUnknownContentWithoutSecondCall(t *testing.T) {
	run := &oneResultAgentRunner{kind: config.HarnessClaudeCLI, output: strings.TrimSuffix(validExecutionContentJSON("done"), "}") + `,"retry":true}`}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessClaudeCLI, Command: "claude", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
	}}
	output, err := NewAgentExecutor(config.HarnessClaudeCLI, cfg, run).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	if err == nil || output.FailureClass != FailureInvalidContract || output.RetryDisposition != RetryNone || run.calls != 1 {
		t.Fatalf("calls=%d output=%#v error=%v", run.calls, output, err)
	}
}

type oneResultAgentRunner struct {
	kind   string
	output string
	err    error
	calls  int
}

func (r *oneResultAgentRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.calls++
	output := r.output
	if r.kind == config.HarnessClaudeCLI && output != "" {
		output = `{"result":` + quoteJSON(output) + `}`
	} else if r.kind == config.HarnessPiCLI && output != "" {
		var err error
		output, err = piToolEventStreamForArgs(args, output)
		if r.err == nil {
			r.err = err
		}
	}
	return subprocess.Result{Stdout: output}, r.err
}

func (r *oneResultAgentRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	if _, err := io.Copy(io.Discard, input); err != nil {
		return subprocess.Result{}, err
	}
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *oneResultAgentRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	return r.RunBoundedInput(ctx, command, args, dir, timeout, input, 0, "")
}

func TestHarnessFailureDoesNotTriggerAnotherCall(t *testing.T) {
	run := &oneResultAgentRunner{kind: config.HarnessPiCLI, err: errors.New("model unavailable")}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: t.TempDir(), TimeoutSeconds: 30}}
	output, err := NewAgentExecutor(config.HarnessPiCLI, cfg, run).Execute(t.Context(), testPollResponse(testCodexCLIAssignmentSpec()).Assignments[0])
	if err == nil || output.Outcome != OutcomeBlocked || run.calls != 1 {
		t.Fatalf("calls=%d output=%#v error=%v", run.calls, output, err)
	}
}
