package execution

import (
	"crypto/rand"
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
				} else if outcome != OutcomeSucceeded {
					t.Errorf("%s %s did not succeed: %s", harness, benchmarkCase.name, outcome)
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
	// Keep this read-only Pi benchmark on the isolated planner profile.
	// Implementer and reviewer cases below use the explicit host profile.
	if harness == config.HarnessPiCLI {
		cfg.RoleAccess = ""
	}
	// Keep the answer out of the prompt and schema, so this grades a real read
	// rather than letting constrained sampling supply the expected constant.
	want := rand.Text()
	if err := os.WriteFile(filepath.Join(cfg.Harness.WorkingDir, "README.md"), []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, cfg.Harness.WorkingDir, "add", "README.md")
	runGitCommand(t, cfg.Harness.WorkingDir, "commit", "-m", "Seed opaque read challenge")
	schema := []byte(`{"type":"object","required":["summary"],"properties":{"summary":{"type":"string"}},"additionalProperties":false}`)
	result, err := RunPlannerWithUsage(t.Context(), harness, cfg, cfg.Harness.WorkingDir,
		"Read README.md, make no changes, and return its entire trimmed contents as the structured summary.", schema, nil)
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
	if decoded.Summary != want {
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
	if err := localBenchmarkChangedPaths(metadata.WorktreePath, metadata.BaseRevision, "answer.txt"); err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
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
	// Grade bounds and interior values independently without changing the
	// model-visible test or accepting a fix tailored to its two examples.
	command = exec.Command("node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { clamp } from "./clamp.mjs";
for (const [value, min, max, expected] of [
  [-5, 0, 10, 0], [5, 0, 10, 5], [15, 0, 10, 10],
  [0, 0, 10, 0], [10, 0, 10, 10], [4, 3, 3, 3],
  [-8, -5, -1, -5], [-3, -5, -1, -3], [2, -5, -1, -1],
  [0.5, 0.25, 0.75, 0.5]
]) assert.equal(clamp(value, min, max), expected);
`)
	command.Dir = metadata.WorktreePath
	if testOutput, testErr := command.CombinedOutput(); testErr != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, fmt.Errorf("independent bounds check failed: %v: %s", testErr, testOutput)
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
	if err := localBenchmarkChangedPaths(metadata.WorktreePath, metadata.BaseRevision, "clamp.mjs"); err != nil {
		return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, err
	}
	return output.Outcome, output.Usage, output.HarnessDurationMilliseconds, nil
}

func TestLocalBenchmarkChangedPaths(t *testing.T) {
	cases := []struct {
		name      string
		expected  string
		changes   map[string]string
		commit    bool
		wantError bool
	}{
		{name: "modified required file", expected: "README.md", changes: map[string]string{"README.md": "updated\n"}},
		{name: "committed required file", expected: "README.md", changes: map[string]string{"README.md": "updated\n"}, commit: true},
		{name: "untracked required file", expected: "answer.txt", changes: map[string]string{"answer.txt": "answer\n"}},
		{name: "missing required change", expected: "README.md", wantError: true},
		{name: "untracked unrelated file", expected: "README.md", changes: map[string]string{"README.md": "updated\n", "other.txt": "unrelated\n"}, wantError: true},
		{name: "committed unrelated file", expected: "README.md", changes: map[string]string{"README.md": "updated\n", "other.txt": "unrelated\n"}, commit: true, wantError: true},
		{name: "committed whitespace error", expected: "README.md", changes: map[string]string{"README.md": "updated \n"}, commit: true, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := initGitRepo(t)
			base := strings.TrimSpace(runGitCommandOutput(t, repo, "rev-parse", "HEAD"))
			for path, content := range tc.changes {
				if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.commit {
				runGitCommand(t, repo, "add", ".")
				runGitCommand(t, repo, "commit", "-m", "Commit benchmark candidate")
			}
			if err := localBenchmarkChangedPaths(repo, base, tc.expected); (err != nil) != tc.wantError {
				t.Fatalf("changed paths from %s: %v, want error %t", base, err, tc.wantError)
			}
		})
	}
}

func localBenchmarkChangedPaths(repo, base, expected string) error {
	check := exec.Command("git", "diff", "--check", base, "--")
	check.Dir = repo
	if output, err := check.CombinedOutput(); err != nil {
		return fmt.Errorf("independent diff check failed: %v: %s", err, output)
	}
	// Include committed changes as well as the index and working tree. A clean
	// status alone cannot prove that the model changed only the required file.
	command := exec.Command("git", "diff", "--name-only", "--no-renames", "-z", base, "--")
	command.Dir = repo
	output, err := command.Output()
	if err != nil {
		return err
	}
	untracked := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z")
	untracked.Dir = repo
	untrackedOutput, err := untracked.Output()
	if err != nil {
		return err
	}
	found := false
	for _, path := range strings.Split(string(output)+string(untrackedOutput), "\x00") {
		if path == "" {
			continue
		}
		if path != expected {
			return fmt.Errorf("unexpected workspace change %q", path)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("required workspace change %q is missing", expected)
	}
	return nil
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
