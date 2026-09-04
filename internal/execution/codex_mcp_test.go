package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type codexMCPListRunner struct {
	stdout  string
	err     error
	calls   int
	wantDir string
}

func (runner *codexMCPListRunner) Run(_ context.Context, command string, args []string, dir string, _ time.Duration) (subprocess.Result, error) {
	runner.calls++
	wantDir := runner.wantDir
	if wantDir == "" {
		wantDir = "/neutral"
	}
	if command != "codex" || strings.Join(args, " ") != "mcp list --json" || dir != wantDir {
		return subprocess.Result{}, errors.New("unexpected MCP inspection command")
	}
	return subprocess.Result{Stdout: runner.stdout}, runner.err
}

func TestCodexMCPProfileArgsExposeOnlyExplicitServer(t *testing.T) {
	runner := &codexMCPListRunner{stdout: `[
  {"name":"other","enabled":true,"transport":{"type":"stdio","command":"other"}},
  {"name":"browser","enabled":true,"transport":{"type":"stdio","command":"npx","args":["browser-mcp","--isolated"],"env_vars":["BROWSER_PATH"]},"enabled_tools":["navigate"],"startup_timeout_sec":20,"tool_timeout_sec":60}
]`}
	workspace := profileWorkspace{Dir: "/worktree", TrustedToolDir: "/neutral"}
	args, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, []string{"browser"}, false, config.HarnessConfigModeIsolated)
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
	workspace := profileWorkspace{Dir: "/worktree", TrustedToolDir: "/neutral"}
	if _, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, []string{"browser"}, false, config.HarnessConfigModeIsolated); err == nil || !strings.Contains(err.Error(), "use env_vars") {
		t.Fatalf("inline MCP environment was not rejected without exposure: %v", err)
	}
}

func TestCodexMCPProfileArgsDoNotInspectWhenNoServerIsGranted(t *testing.T) {
	runner := &codexMCPListRunner{}
	workspace := profileWorkspace{Dir: "/worktree", TrustedToolDir: "/neutral"}
	args, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, nil, false, config.HarnessConfigModeIsolated)
	if err != nil || len(args) != 0 || runner.calls != 0 {
		t.Fatalf("empty MCP grant performed work: args=%#v err=%v calls=%d", args, err, runner.calls)
	}
}

func TestCodexSafeToolsInjectPinnedLoopbackOnlyBrowserWithoutUserConfig(t *testing.T) {
	runner := &codexMCPListRunner{}
	workspace := profileWorkspace{Dir: "/worktree", TrustedToolDir: "/neutral"}
	args, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, nil, true, config.HarnessConfigModeIsolated)
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
		`cwd="/neutral"`, `"NPM_CONFIG_CACHE"="/neutral/npm-cache"`, `"NPM_CONFIG_USERCONFIG"="/neutral/npmrc"`,
		`startup_timeout_sec=30`, `enabled_tools=["navigate","evaluate","screenshot"]`,
		`enabled=true`, `required=true`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Runner browser profile omitted %q: %s", expected, joined)
		}
	}
}

func TestInheritedCodexMCPAddsRunnerBrowserWithoutReplacingAmbientCatalog(t *testing.T) {
	runner := &codexMCPListRunner{stdout: `[{"name":"operator_browser","enabled":true,"transport":{"type":"stdio","command":"operator-browser"}}]`}
	workspace := profileWorkspace{Dir: "/neutral", TrustedToolDir: "/trusted"}
	args, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, []string{"operator_browser"}, true, config.HarnessConfigModeInherit)
	if err != nil {
		t.Fatalf("build inherited Runner browser profile: %v", err)
	}
	joined := strings.Join(args, " ")
	if runner.calls != 1 || !strings.Contains(joined, "mcp_servers.runner_browser={") || strings.Contains(joined, "mcp_servers={") || strings.Contains(joined, "operator-browser") {
		t.Fatalf("inherited MCP profile replaced or inspected ambient catalog: args=%#v calls=%d", args, runner.calls)
	}
	if !strings.Contains(codexMCPPromptForConfig([]string{"operator_browser"}, true, config.HarnessConfigModeInherit), "ambient Codex MCP configuration") {
		t.Fatal("inherited MCP prompt did not disclose ambient configuration")
	}
}

func TestIsolatedCodexMCPInspectsCatalogOutsideImplementationWorktree(t *testing.T) {
	runner := &codexMCPListRunner{
		stdout:  `[{"name":"operator_browser","enabled":true,"transport":{"type":"stdio","command":"operator-browser"}}]`,
		wantDir: "/trusted",
	}
	workspace := profileWorkspace{Dir: "/worktree", TrustedToolDir: "/trusted"}
	args, err := codexMCPProfileArgsForConfig(t.Context(), runner, "codex", workspace, []string{"operator_browser"}, false, config.HarnessConfigModeIsolated)
	if err != nil {
		t.Fatalf("build isolated MCP profile: %v", err)
	}
	if runner.calls != 1 || !strings.Contains(strings.Join(args, " "), `command="operator-browser"`) {
		t.Fatalf("isolated MCP catalog was not reconstructed from trusted cwd: args=%#v calls=%d", args, runner.calls)
	}
}

func TestCodexMCPPromptNamesOnlyGrantedServersAndExplainsToolDiscovery(t *testing.T) {
	prompt := codexMCPPrompt([]string{"browser", "audit"}, false)
	for _, expected := range []string{"audit, browser", "whichever callable surface", "ALL_TOOLS", "tools object", "list_mcp_resources reports resources"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("MCP prompt omitted %q: %s", expected, prompt)
		}
	}
	if prompt := codexMCPPrompt(nil, false); prompt != "" {
		t.Fatalf("empty grant added an MCP prompt: %q", prompt)
	}
}

func TestRunnerBrowserPromptSupportsDirectAndCodeModeTools(t *testing.T) {
	prompt := codexMCPPrompt(nil, true)
	for _, expected := range []string{
		"use runner_browser before trying any shell-launched browser",
		"navigate, evaluate, and screenshot",
		"In Code Mode, inspect ALL_TOOLS for runner_browser",
		"Do not download a browser",
		"both the direct and Code Mode tool catalogs have been checked",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("Runner browser prompt omitted %q: %s", expected, prompt)
		}
	}
	if prompt := runnerBrowserPrompt(false); prompt != "" {
		t.Fatalf("disabled safe tools added browser instructions: %q", prompt)
	}
}
