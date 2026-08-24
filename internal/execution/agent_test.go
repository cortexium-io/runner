package execution

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type agentCLICommandRunner struct {
	args  []string
	input string
}

func (r *agentCLICommandRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	r.args = append([]string{}, args...)
	result := `{"outcome":"succeeded","summary":"Reviewed","work_done":["Inspected repository"],"verification":["Inspected the configured repository files"],"blockers":[]}`
	return subprocess.Result{Stdout: `{"result":` + quoteJSON(result) + `}`}, nil
}

func (r *agentCLICommandRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.input = string(data)
	return r.Run(ctx, command, args, dir, timeout)
}

func (r *agentCLICommandRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.input = string(data)
	return r.Run(ctx, command, args, dir, timeout)
}

type claudeWorkspaceCommandRunner struct {
	args  []string
	dir   string
	input string
}

func (r *claudeWorkspaceCommandRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command != "claude" {
		return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	}
	r.args = append([]string{}, args...)
	r.dir = dir
	worktree := argumentValue(args, "--add-dir")
	if worktree == "" {
		worktree = dir
	}
	if err := os.WriteFile(filepath.Join(worktree, "claude-change.txt"), []byte("implemented\n"), 0o644); err != nil {
		return subprocess.Result{}, err
	}
	return subprocess.Result{Stdout: `{"structured_output":{"outcome":"succeeded","summary":"Implemented","work_done":["Created claude-change.txt"],"verification":["Inspected the written file"],"blockers":[]}}`}, nil
}

func (r *claudeWorkspaceCommandRunner) RunBoundedInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	data, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	r.input = string(data)
	return r.Run(ctx, command, args, dir, timeout)
}

type piWorkspaceCommandRunner struct {
	args    []string
	allArgs [][]string
	outputs []string
	calls   int
}

func (r *piWorkspaceCommandRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command != "pi" {
		return (subprocess.OSRunner{}).Run(ctx, command, args, dir, timeout)
	}
	callIndex := r.calls
	r.calls++
	r.args = append([]string{}, args...)
	r.allArgs = append(r.allArgs, append([]string{}, args...))
	if err := os.WriteFile(filepath.Join(dir, "pi-change.txt"), []byte("implemented\n"), 0o644); err != nil {
		return subprocess.Result{}, err
	}
	output := `{"outcome":"succeeded","summary":"Implemented","work_done":["Created pi-change.txt"],"verification":["Inspected the written file"],"blockers":[]}`
	if callIndex < len(r.outputs) {
		output = r.outputs[callIndex]
	}
	stream, err := piToolEventStreamForArgs(args, output)
	return subprocess.Result{Stdout: stream}, err
}

func (r *piWorkspaceCommandRunner) RunLineFilteredInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string, _ subprocess.LineFilter) (subprocess.Result, error) {
	if _, err := io.Copy(io.Discard, input); err != nil {
		return subprocess.Result{}, err
	}
	return r.Run(ctx, command, args, dir, timeout)
}

func TestClaudeAnalysisExecutorUsesReadOnlyNonInteractiveToolEnvelope(t *testing.T) {
	run := &agentCLICommandRunner{}
	cfg := config.ExecutionConfig{WorkspaceBaseRef: "HEAD", Harness: config.HarnessConfig{Kind: config.HarnessClaudeCLI, Command: "claude", WorkingDir: t.TempDir(), TimeoutSeconds: 30}}
	executor := NewAgentExecutor(config.HarnessClaudeCLI, cfg, run)
	packet := testCodexCLIAssignmentSpec()
	assignment := testPollResponse(packet).Assignments[0]
	output, err := executor.Execute(t.Context(), assignment)
	if err != nil || output.Outcome != OutcomeSucceeded {
		t.Fatalf("execute: output=%#v error=%v", output, err)
	}
	joined := strings.Join(run.args, " ")
	for _, forbidden := range []string{"--dangerously-skip-permissions", "bypassPermissions", "--disallowedTools"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Claude invocation used unsafe or competing permission argument %q: %s", forbidden, joined)
		}
	}
	for _, expected := range []string{"--print", "--output-format json", "--json-schema", "--permission-mode dontAsk", "--tools Read,Grep,Glob", "--allowedTools Read,Grep,Glob"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Claude invocation is missing result-contract argument %q: %s", expected, joined)
		}
	}
}

func TestClaudeWorkspaceInvocationUsesNativeSandboxByDefault(t *testing.T) {
	if err := ValidateHarnessProfile(config.HarnessClaudeCLI, RoleImplementer); err != nil {
		t.Fatalf("Claude implementation profile was rejected: %v", err)
	}
	profile, _ := ProfileForRole(RoleImplementer)
	args := claudeProfileArgs(profile, profileWorkspace{Dir: "/worktree", ReadRoot: "/worktree"}, true)
	joined := strings.Join(args, " ")
	for _, required := range []string{"--permission-mode dontAsk", "--settings", "--tools Read,Grep,Glob,Bash,Edit,Write", "--allowedTools Read,Grep,Glob,Bash,Edit,Write,mcp__runner_browser__*"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Claude implementer omitted %q: %s", required, joined)
		}
	}
	if contains(args, "--safe-mode") {
		t.Fatalf("Claude safe implementer disabled Runner's explicit MCP server: %#v", args)
	}
	if strings.Contains(joined, "--add-dir /worktree") {
		t.Fatalf("Claude implementer registered its current worktree twice: %s", joined)
	}
	for _, overridden := range []string{"--dangerously-skip-permissions", `"allowUnsandboxedCommands":true`} {
		if strings.Contains(joined, overridden) {
			t.Fatalf("Claude implementer escaped its native sandbox with %q: %s", overridden, joined)
		}
	}
}

func TestClaudeReviewerUsesNativeReadOnlySandboxByDefault(t *testing.T) {
	run := &sharedReviewerHarnessRunner{response: failingReviewerContent()}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessClaudeCLI, Command: "claude", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
	}}
	output, err := NewAgentExecutor(config.HarnessClaudeCLI, cfg, run).Execute(t.Context(), reviewerAssignment())
	if err != nil || output.Outcome != OutcomeSucceeded {
		t.Fatalf("execute Claude reviewer: output=%#v error=%v", output, err)
	}
	for _, required := range []string{"--allowedTools", "--tools", "--safe-mode", "--setting-sources", "--permission-mode", "--settings", "--no-chrome", "--add-dir"} {
		if !contains(run.args, required) {
			t.Fatalf("Claude reviewer omitted %q: %#v", required, run.args)
		}
	}
	if contains(run.args, "--dangerously-skip-permissions") || !containsArgPair(run.args, "--tools", "Read,Grep,Glob,Bash") {
		t.Fatalf("Claude reviewer escaped isolation or expanded tools: %#v", run.args)
	}
}

func TestClaudePlannerUsesReadOnlyNonInteractiveToolEnvelope(t *testing.T) {
	run := &agentCLICommandRunner{}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessClaudeCLI, Command: "claude", WorkingDir: t.TempDir(), TimeoutSeconds: 30,
	}}
	if _, err := RunPlannerWithUsage(t.Context(), config.HarnessClaudeCLI, cfg, cfg.Harness.WorkingDir, "Plan this.", executionContentSchema, run); err != nil {
		t.Fatalf("run Claude planner: %v", err)
	}
	if !containsArgPair(run.args, "--permission-mode", "dontAsk") || !containsArgPair(run.args, "--tools", "Read,Grep,Glob,Bash") || !containsArgPair(run.args, "--allowedTools", "Read,Grep,Glob,Bash") || !contains(run.args, "--settings") {
		t.Fatalf("unexpected Claude planner tool envelope: %#v", run.args)
	}
}

func TestClaudeWorkspaceWriteUsesIsolatedWorktree(t *testing.T) {
	repo := initGitRepo(t)
	run := &claudeWorkspaceCommandRunner{}
	cfg := config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD", SafeTools: true,
		Harness: config.HarnessConfig{
			Kind: config.HarnessClaudeCLI, Command: "claude", WorkingDir: repo, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: 30,
		},
	}
	executor := NewAgentExecutor(config.HarnessClaudeCLI, cfg, run)
	packet := testCodexCLIAssignmentSpec()
	assignment := testPollResponse(packet).Assignments[0]
	var prepared workspace.Metadata
	output, err := executor.ExecuteWorkspaceWrite(t.Context(), assignment, func(metadata workspace.Metadata) error {
		prepared = metadata
		return nil
	})
	if err != nil || output.Outcome != OutcomeSucceeded {
		t.Fatalf("Claude implementation failed: output=%#v error=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(prepared.WorktreePath, "claude-change.txt")); err != nil {
		t.Fatalf("Claude change was not written in the task worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "claude-change.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Claude changed the active checkout: %v", err)
	}
	if filepath.Clean(run.dir) == filepath.Clean(prepared.WorktreePath) {
		t.Fatalf("Claude was launched from a linked Git worktree, which overflows its macOS sandbox profile: %s", run.dir)
	}
	if !containsArgPair(run.args, "--add-dir", prepared.WorktreePath) {
		t.Fatalf("Claude was not granted its assigned worktree from the neutral launch directory: %#v", run.args)
	}
	if !strings.Contains(run.input, "Assigned worktree: "+prepared.WorktreePath) {
		t.Fatalf("Claude prompt omitted the exact assigned worktree path: %s", run.input)
	}
	if !strings.Contains(run.input, "use runner_browser before trying any shell-launched browser") {
		t.Fatalf("Claude prompt omitted the Runner browser priority: %s", run.input)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(argumentValue(run.args, "--json-schema")), &schema); err != nil {
		t.Fatalf("decode Claude execution schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	verification := properties["verification"].(map[string]any)
	if got := verification["maxItems"]; got != float64(len(packet.RequiredVerification)) {
		t.Fatalf("Claude verification maxItems = %#v, want %d", got, len(packet.RequiredVerification))
	}
}

func argumentValue(args []string, name string) string {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == name {
			return args[index+1]
		}
	}
	return ""
}

func TestPiExecutorSuppressesNativePermissionConfiguration(t *testing.T) {
	run := &piWorkspaceCommandRunner{}
	cfg := config.ExecutionConfig{Harness: config.HarnessConfig{
		Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: t.TempDir(), TimeoutSeconds: 30, ReasoningEffort: "high",
	}}
	packet := testCodexCLIAssignmentSpec()
	assignment := testPollResponse(packet).Assignments[0]
	output, err := NewAgentExecutor(config.HarnessPiCLI, cfg, run).Execute(t.Context(), assignment)
	if err != nil || output.Outcome != OutcomeSucceeded {
		t.Fatalf("execute Pi planner: output=%#v error=%v", output, err)
	}
	joined := strings.Join(run.args, " ")
	for _, expected := range []string{"--print", "--no-session", "--no-extensions", "--mode json", "--append-system-prompt " + piStructuredResultSystemPrompt, "--thinking high"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Pi invocation is missing %q: %s", expected, joined)
		}
	}
	for _, expected := range []string{"--no-approve", "--no-skills", "--no-context-files", "--tools read,grep,find,ls," + piStructuredResultTool} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Pi invocation omitted fixed control %q: %s", expected, joined)
		}
	}
	prompt := buildHarnessPrompt(assignment, false, "Pi CLI")
	if strings.Contains(prompt, strings.TrimSpace(string(executionContentSchema))) {
		t.Fatalf("Pi prompt duplicated the structured-result tool schema:\n%s", prompt)
	}
	for _, required := range []string{"dedicated Runner finalization tool", "follow that tool's own completion instructions exactly"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("Pi prompt omitted %q:\n%s", required, prompt)
		}
	}
}

func TestPiWorkspaceExecutionUsesIsolatedWorktree(t *testing.T) {
	repo := initGitRepo(t)
	run := &piWorkspaceCommandRunner{}
	cfg := config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD",
		RoleAccess:       config.RoleAccessHost,
		SafeTools:        true,
		Harness: config.HarnessConfig{
			Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: repo, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: 30,
		},
	}
	executor := NewAgentExecutor(config.HarnessPiCLI, cfg, run)
	packet := testCodexCLIAssignmentSpec()
	assignment := testPollResponse(packet).Assignments[0]
	var prepared workspace.Metadata
	output, err := executor.ExecuteWorkspaceWrite(t.Context(), assignment, func(metadata workspace.Metadata) error {
		prepared = metadata
		return nil
	})
	if err != nil || output.Outcome != OutcomeSucceeded {
		t.Fatalf("Pi implementation failed: output=%#v error=%v", output, err)
	}
	if _, err := os.Stat(filepath.Join(prepared.WorktreePath, "pi-change.txt")); err != nil {
		t.Fatalf("Pi change was not written in the task worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "pi-change.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Pi changed the active checkout: %v", err)
	}
	wantTools := "read,grep,find,ls,bash,edit,write," + piStructuredResultTool + "," + strings.Join(piBrowserToolNames, ",")
	if !contains(run.args, "--no-approve") || !containsArgPair(run.args, "--tools", wantTools) || contains(run.args, "--approve") {
		t.Fatalf("Pi implementer did not use the explicit fixed host tool envelope: %#v", run.args)
	}
	for _, required := range []string{"--no-extensions", "--extension"} {
		if !contains(run.args, required) {
			t.Fatalf("Pi implementer omitted Runner's structured-result boundary %q: %#v", required, run.args)
		}
	}
	extensions := 0
	for _, argument := range run.args {
		if argument == "--extension" {
			extensions++
		}
	}
	if extensions != 2 {
		t.Fatalf("Pi implementer received %d Runner extensions, want structured result plus browser: %#v", extensions, run.args)
	}
}

func TestPiWorkspaceWriteUsesOnlyCorrectedResultAsEvidence(t *testing.T) {
	repo := initGitRepo(t)
	corrected := `{"outcome":"succeeded","summary":"Corrected implementation result","work_done":["Created pi-change.txt"],"verification":["Inspected the written file"],"blockers":[]}`
	run := &piWorkspaceCommandRunner{outputs: []string{
		strings.TrimSuffix(corrected, "}") + `,"type":"object"}`,
		corrected,
	}}
	cfg := config.ExecutionConfig{
		WorkspaceBaseRef: "HEAD",
		RoleAccess:       config.RoleAccessHost,
		Harness: config.HarnessConfig{
			Kind: config.HarnessPiCLI, Command: "pi", WorkingDir: repo, WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: 30,
		},
	}
	executor := NewAgentExecutor(config.HarnessPiCLI, cfg, run)
	packet := testCodexCLIAssignmentSpec()

	output, err := executor.ExecuteWorkspaceWrite(t.Context(), testPollResponse(packet).Assignments[0], nil)
	if err != nil || output.Outcome != OutcomeSucceeded || output.Summary != "Corrected implementation result" || run.calls != 1 {
		t.Fatalf("Pi implementation did not use the locally canonicalized result: calls=%d output=%#v err=%v", run.calls, output, err)
	}
}

func quoteJSON(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}
