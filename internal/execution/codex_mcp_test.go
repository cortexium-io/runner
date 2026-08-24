package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/subprocess"
)

type codexMCPListRunner struct {
	stdout string
	err    error
	calls  int
}

func (runner *codexMCPListRunner) Run(_ context.Context, command string, args []string, dir string, _ time.Duration) (subprocess.Result, error) {
	runner.calls++
	if command != "codex" || strings.Join(args, " ") != "mcp list --json" || dir != "/neutral" {
		return subprocess.Result{}, errors.New("unexpected MCP inspection command")
	}
	return subprocess.Result{Stdout: runner.stdout}, runner.err
}

func TestCodexMCPProfileArgsExposeOnlyExplicitServer(t *testing.T) {
	runner := &codexMCPListRunner{stdout: `[
  {"name":"other","enabled":true,"transport":{"type":"stdio","command":"other"}},
  {"name":"browser","enabled":true,"transport":{"type":"stdio","command":"npx","args":["browser-mcp","--isolated"],"env_vars":["BROWSER_PATH"]},"enabled_tools":["navigate"],"startup_timeout_sec":20,"tool_timeout_sec":60}
]`}
	args, err := codexMCPProfileArgs(t.Context(), runner, "codex", "/neutral", []string{"browser"}, false)
	if err != nil {
		t.Fatalf("build MCP profile: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		`mcp_servers={browser={command="npx"`,
		`args=["browser-mcp","--isolated"]`,
		`env_vars=["BROWSER_PATH"]`,
		`enabled_tools=["navigate"]`,
		`startup_timeout_sec=20`, `tool_timeout_sec=60`,
		`enabled=true`, `default_tools_approval_mode="approve"`,
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("MCP profile omitted %q: %#v", required, args)
		}
	}
	if strings.Contains(joined, "other") || runner.calls != 1 {
		t.Fatalf("MCP profile exposed an unlisted server or repeated discovery: args=%#v calls=%d", args, runner.calls)
	}
}

func TestCodexMCPProfileArgsRejectInlineEnvironmentValues(t *testing.T) {
	runner := &codexMCPListRunner{stdout: `[{"name":"browser","enabled":true,"transport":{"type":"stdio","command":"browser","env":{"TOKEN":"secret"}}}]`}
	if _, err := codexMCPProfileArgs(t.Context(), runner, "codex", "/neutral", []string{"browser"}, false); err == nil || !strings.Contains(err.Error(), "use env_vars") {
		t.Fatalf("inline MCP environment was not rejected without exposure: %v", err)
	}
}

func TestCodexMCPProfileArgsDoNotInspectWhenNoServerIsGranted(t *testing.T) {
	runner := &codexMCPListRunner{}
	args, err := codexMCPProfileArgs(t.Context(), runner, "codex", "/neutral", nil, false)
	if err != nil || len(args) != 0 || runner.calls != 0 {
		t.Fatalf("empty MCP grant performed work: args=%#v err=%v calls=%d", args, err, runner.calls)
	}
}

func TestCodexSafeToolsInjectPinnedLoopbackOnlyBrowserWithoutUserConfig(t *testing.T) {
	runner := &codexMCPListRunner{}
	args, err := codexMCPProfileArgs(t.Context(), runner, "codex", "/neutral", nil, true)
	if err != nil {
		t.Fatalf("build Runner browser profile: %v", err)
	}
	if runner.calls != 0 {
		t.Fatalf("Runner browser unexpectedly trusted ambient MCP config: %d inspection call(s)", runner.calls)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		`runner_browser={command="npx"`, "chrome-devtools-mcp@1.7.0", "--headless", "--isolated", "--slim",
		"--allowed-url-pattern=http://localhost:*/*", "--allowed-url-pattern=http://127.0.0.1:*/*",
		"--chrome-arg=--use-mock-keychain", "--no-performance-crux", "--redact-network-headers", "--no-usage-statistics",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Runner browser profile omitted %q: %s", expected, joined)
		}
	}
}

func TestCodexMCPPromptNamesOnlyGrantedServersAndExplainsToolDiscovery(t *testing.T) {
	prompt := codexMCPPrompt([]string{"browser", "audit"}, false)
	for _, expected := range []string{"audit, browser", "direct MCP tool calls", "list_mcp_resources reports resources"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("MCP prompt omitted %q: %s", expected, prompt)
		}
	}
	if prompt := codexMCPPrompt(nil, false); prompt != "" {
		t.Fatalf("empty grant added an MCP prompt: %q", prompt)
	}
}

func TestRunnerBrowserPromptRequiresDirectBrowserBeforeFallback(t *testing.T) {
	prompt := codexMCPPrompt(nil, true)
	for _, expected := range []string{
		"use runner_browser before trying any shell-launched browser",
		"navigate, evaluate, and screenshot",
		"Do not download a browser",
		"only after a direct runner_browser tool call returns a concrete failure",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Runner browser prompt omitted %q: %s", expected, prompt)
		}
	}
	if prompt := runnerBrowserPrompt(false); prompt != "" {
		t.Fatalf("disabled safe tools added browser instructions: %q", prompt)
	}
}
