package execution

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

// TestLiveLocalHarnessBenchmark is an opt-in, local-only comparison of the
// Pi and Codex adapters against the same model served by LM Studio. It runs
// sequentially because local inference commonly has a single execution slot.
// Normal tests and CI skip it.
//
// Example:
//
//	CORTEXIUM_RUNNER_LOCAL_BENCHMARK_HARNESSES=pi,codex \
//	CORTEXIUM_RUNNER_LOCAL_BENCHMARK_MODEL=qwen/qwen3.8-27b \
//	CORTEXIUM_RUNNER_LOCAL_BENCHMARK_REASONING=low \
//	go test ./internal/execution -run '^TestLiveLocalHarnessBenchmark$' -count=1 -v -timeout=2h
func TestLiveLocalHarnessBenchmark(t *testing.T) {
	harnesses := compactLocalBenchmarkHarnesses(os.Getenv("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_HARNESSES"))
	if len(harnesses) == 0 {
		t.Skip("set CORTEXIUM_RUNNER_LOCAL_BENCHMARK_HARNESSES to run local model benchmarks")
	}
	model := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_MODEL"))
	if model == "" {
		t.Fatal("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_MODEL is required")
	}
	reasoning := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_REASONING"))
	if reasoning == "" {
		reasoning = "low"
	}
	timeoutSeconds := 1200
	if raw := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_TIMEOUT_SECONDS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			t.Fatal("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_TIMEOUT_SECONDS must be a positive integer")
		}
		timeoutSeconds = value
	}

	codexLauncher := ""
	for _, harness := range harnesses {
		if harness == config.HarnessCodexCLI {
			codexLauncher = newCodexLMStudioLauncher(t)
			break
		}
	}

	cases := []struct {
		name string
		run  func(*testing.T, string, config.ExecutionConfig) (string, metrics.Usage, int64, error)
	}{
		{name: "structured_read", run: runLocalBenchmarkStructuredRead},
		{name: "structured_synthesis", run: runLocalBenchmarkStructuredSynthesis},
		{name: "exact_write", run: runLocalBenchmarkExactWrite},
		{name: "focused_bug_fix", run: runLocalBenchmarkBugFix},
		{name: "seeded_review", run: runLocalBenchmarkReview},
	}
	caseFilter := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LOCAL_BENCHMARK_CASE"))

	for caseIndex, benchmarkCase := range cases {
		if caseFilter != "" && benchmarkCase.name != caseFilter {
			continue
		}
		benchmarkCase := benchmarkCase
		order := append([]string(nil), harnesses...)
		if caseIndex%2 == 1 {
			for left, right := 0, len(order)-1; left < right; left, right = left+1, right-1 {
				order[left], order[right] = order[right], order[left]
			}
		}
		for _, harness := range order {
			harness := harness
			t.Run(benchmarkCase.name+"/"+harness, func(t *testing.T) {
				repo := initGitRepo(t)
				cfg := localBenchmarkConfig(t, harness, model, reasoning, timeoutSeconds, repo, codexLauncher)
				started := time.Now()
				outcome, usage, harnessDuration, err := benchmarkCase.run(t, harness, cfg)
				record := localBenchmarkRecord{
					Harness: harness, Case: benchmarkCase.name, Outcome: outcome,
					DurationMS: time.Since(started).Milliseconds(), HarnessDurationMS: harnessDuration, Usage: usage,
				}
				if err != nil {
					record.Error = err.Error()
				}
				encoded, marshalErr := json.Marshal(record)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				t.Logf("LOCAL_HARNESS_BENCHMARK %s", encoded)
				if err != nil {
					t.Errorf("%s %s failed: %v", harness, benchmarkCase.name, err)
				}
			})
		}
	}
}

func runLocalBenchmarkStructuredSynthesis(t *testing.T, harness string, cfg config.ExecutionConfig) (string, metrics.Usage, int64, error) {
	t.Helper()
	if harness == config.HarnessPiCLI {
		cfg.RoleAccess = config.RoleAccessSandboxed
	}
	schema := []byte(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string","const":"local-synthesis-ok"}},"additionalProperties":false}`)
	var result StructuredHarnessResult
	var err error
	if harness == config.HarnessPiCLI {
		result, err = RunPlannerSynthesisStageWithUsage(t.Context(), harness, cfg,
			"Return the required structured summary without inspecting files or calling tools.", schema, nil)
	} else {
		result, err = RunPlannerWithUsage(t.Context(), harness, cfg, cfg.Harness.WorkingDir,
			"Return the required structured summary without inspecting files or calling tools.", schema, nil)
	}
	if err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	canonical, err := CanonicalizeStructuredResult(result.Message, "summary")
	if err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	var decoded struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(canonical), &decoded); err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	if decoded.Summary != "local-synthesis-ok" {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, fmt.Errorf("unexpected summary %q", decoded.Summary)
	}
	return OutcomeSucceeded, result.Usage, result.DurationMilliseconds, nil
}

type localBenchmarkRecord struct {
	Harness           string        `json:"harness"`
	Case              string        `json:"case"`
	Outcome           string        `json:"outcome"`
	DurationMS        int64         `json:"duration_ms"`
	HarnessDurationMS int64         `json:"harness_duration_ms"`
	Usage             metrics.Usage `json:"usage"`
	Error             string        `json:"error,omitempty"`
}

func compactLocalBenchmarkHarnesses(raw string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range strings.Split(raw, ",") {
		harness := strings.TrimSpace(value)
		if harness == "" || seen[harness] {
			continue
		}
		if harness != config.HarnessPiCLI && harness != config.HarnessCodexCLI {
			continue
		}
		seen[harness] = true
		result = append(result, harness)
	}
	return result
}

func localBenchmarkConfig(t *testing.T, harness, model, reasoning string, timeoutSeconds int, repo, codexLauncher string) config.ExecutionConfig {
	t.Helper()
	command := harness
	modelID := model
	roleAccess := ""
	if harness == config.HarnessPiCLI {
		modelID = "lmstudio/" + strings.TrimPrefix(model, "lmstudio/")
		roleAccess = config.RoleAccessHost
	} else if harness == config.HarnessCodexCLI {
		command = codexLauncher
	}
	return config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD",
		RoleAccess:       roleAccess,
		Harness: config.HarnessConfig{
			Kind: harness, Command: command, Model: &modelID, WorkingDir: repo,
			WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: timeoutSeconds, ReasoningEffort: reasoning,
		},
	}
}

func newCodexLMStudioLauncher(t *testing.T) string {
	t.Helper()
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("find codex: %v", err)
	}
	path := filepath.Join(t.TempDir(), "codex-lmstudio")
	script := fmt.Sprintf("#!/bin/sh\nexec %q --oss --local-provider lmstudio \"$@\"\n", codex)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write Codex LM Studio launcher: %v", err)
	}
	return path
}

func runLocalBenchmarkStructuredRead(t *testing.T, harness string, cfg config.ExecutionConfig) (string, metrics.Usage, int64, error) {
	t.Helper()
	// Pi's read-only planner profile intentionally never permits host access.
	// Implementer and reviewer cases below still use the explicit host profile.
	if harness == config.HarnessPiCLI {
		cfg.RoleAccess = ""
	}
	schema := []byte(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string","const":"local-benchmark-ok"}},"additionalProperties":false}`)
	result, err := RunPlannerWithUsage(t.Context(), harness, cfg, cfg.Harness.WorkingDir,
		"Read README.md, make no changes, and return the required structured summary.", schema, nil)
	if err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	canonical, err := CanonicalizeStructuredResult(result.Message, "summary")
	if err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	var decoded struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(canonical), &decoded); err != nil {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, err
	}
	if decoded.Summary != "local-benchmark-ok" {
		return OutcomeBlocked, result.Usage, result.DurationMilliseconds, fmt.Errorf("unexpected summary %q", decoded.Summary)
	}
	return OutcomeSucceeded, result.Usage, result.DurationMilliseconds, nil
}

func runLocalBenchmarkExactWrite(t *testing.T, harness string, cfg config.ExecutionConfig) (string, metrics.Usage, int64, error) {
	t.Helper()
	assignment := localBenchmarkAssignment(harness, "exact_write", "Create answer.txt containing exactly local-benchmark-ok followed by a newline. Verify its exact content and run git diff --check. Make no other change.", []string{"answer.txt has the exact required content", "git diff --check passes"})
	metadata, output, err := executeLocalBenchmarkWorkspace(t, harness, cfg, assignment)
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	content, err := os.ReadFile(filepath.Join(metadata.WorktreePath, "answer.txt"))
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	if string(content) != "local-benchmark-ok\n" {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, fmt.Errorf("unexpected answer.txt content %q", content)
	}
	return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, nil
}

func runLocalBenchmarkBugFix(t *testing.T, harness string, cfg config.ExecutionConfig) (string, metrics.Usage, int64, error) {
	t.Helper()
	seedLocalBenchmarkBug(t, cfg.Harness.WorkingDir)
	assignment := localBenchmarkAssignment(harness, "focused_bug_fix", "Fix clamp.mjs so clamp(value, min, max) respects both bounds. Do not change the existing test. Run node --test test/clamp.test.mjs and git diff --check. Make no unrelated changes.", []string{"node --test test/clamp.test.mjs passes", "the existing test remains unchanged", "git diff --check passes"})
	metadata, output, err := executeLocalBenchmarkWorkspace(t, harness, cfg, assignment)
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	command := exec.Command("node", "--test", "test/clamp.test.mjs")
	command.Dir = metadata.WorktreePath
	if testOutput, testErr := command.CombinedOutput(); testErr != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, fmt.Errorf("focused test failed: %v: %s", testErr, testOutput)
	}
	wantTest, err := os.ReadFile(filepath.Join(cfg.Harness.WorkingDir, "test", "clamp.test.mjs"))
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	gotTest, err := os.ReadFile(filepath.Join(metadata.WorktreePath, "test", "clamp.test.mjs"))
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	if string(gotTest) != string(wantTest) {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, fmt.Errorf("harness changed the seeded test")
	}
	return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, nil
}

func runLocalBenchmarkReview(t *testing.T, harness string, cfg config.ExecutionConfig) (string, metrics.Usage, int64, error) {
	t.Helper()
	seedLocalBenchmarkBug(t, cfg.Harness.WorkingDir)
	assignment := localBenchmarkAssignment(harness, "seeded_review", "Review the clamp implementation and its focused test. Make no changes. Report a finding when either bound is not enforced.", []string{"clamp(value, min, max) enforces both bounds", "node --test test/clamp.test.mjs passes"})
	assignment.Spec.ReviewRequired = true
	var output Output
	var err error
	if harness == config.HarnessCodexCLI {
		output, err = NewCodexExecutor(cfg, nil).Execute(t.Context(), assignment)
	} else {
		output, err = NewAgentExecutor(harness, cfg, nil).Execute(t.Context(), assignment)
	}
	if err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	if output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "needs_changes" {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, fmt.Errorf("reviewer missed seeded defect: %#v", output.ReviewAssessment)
	}
	return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, nil
}

func localBenchmarkAssignment(harness, id, instructions string, verification []string) Assignment {
	return Assignment{Spec: Spec{
		ID: "local_benchmark_" + id + "_" + harness, ItemID: "PVTI_local_benchmark_" + id + "_" + harness,
		Repository: "owner/local-benchmark", DelegatedContentDigest: "v1:local-benchmark-" + id,
		Task:                 Task{Title: "Local benchmark " + strings.ReplaceAll(id, "_", " "), Instructions: instructions},
		RequiredVerification: verification,
	}}
}

func executeLocalBenchmarkWorkspace(t *testing.T, harness string, cfg config.ExecutionConfig, assignment Assignment) (workspace.Metadata, Output, error) {
	t.Helper()
	var metadata workspace.Metadata
	capture := func(value workspace.Metadata) error {
		metadata = value
		return nil
	}
	if harness == config.HarnessCodexCLI {
		output, err := NewCodexExecutor(cfg, nil).ExecuteWorkspaceWrite(t.Context(), assignment, capture)
		return metadata, output, err
	}
	output, err := NewAgentExecutor(harness, cfg, nil).ExecuteWorkspaceWrite(t.Context(), assignment, capture)
	return metadata, output, err
}

func seedLocalBenchmarkBug(t *testing.T, repo string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(repo, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "clamp.mjs"), []byte("export function clamp(value, min, max) {\n  return Math.min(value, max);\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	test := `import test from "node:test";
import assert from "node:assert/strict";
import { clamp } from "../clamp.mjs";

test("clamp enforces both bounds", () => {
  assert.equal(clamp(-5, 0, 10), 0);
  assert.equal(clamp(15, 0, 10), 10);
});
`
	if err := os.WriteFile(filepath.Join(repo, "test", "clamp.test.mjs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, repo, "add", "clamp.mjs", "test/clamp.test.mjs")
	runGitCommand(t, repo, "commit", "-m", "Seed clamp regression")
}
