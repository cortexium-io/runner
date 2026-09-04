package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type codexMCPServer struct {
	Name                  string            `json:"name"`
	Enabled               bool              `json:"enabled"`
	Required              bool              `json:"required"`
	Transport             codexMCPTransport `json:"transport"`
	EnabledTools          []string          `json:"enabled_tools"`
	DisabledTools         []string          `json:"disabled_tools"`
	StartupTimeoutSeconds *float64          `json:"startup_timeout_sec"`
	ToolTimeoutSeconds    *float64          `json:"tool_timeout_sec"`
}

type codexMCPTransport struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	EnvVars []string          `json:"env_vars"`
	CWD     *string           `json:"cwd"`
}

const maxCodexMCPConfigurationBytes = 1024 * 1024

const runnerBrowserMCPServer = "runner_browser"

const runnerBrowserStartupTimeoutSeconds = 30

func runnerBrowserCommand() (string, []string) {
	return "npx", []string{
		"-y", "chrome-devtools-mcp@1.7.0", "--headless", "--isolated", "--slim",
		"--allowed-url-pattern=http://localhost:*/*", "--allowed-url-pattern=http://127.0.0.1:*/*",
		"--chrome-arg=--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1",
		"--chrome-arg=--use-mock-keychain", "--no-performance-crux", "--redact-network-headers", "--no-usage-statistics",
	}
}

func runnerBrowserEnvironment(trustedToolDir string) map[string]string {
	return map[string]string{
		"NPM_CONFIG_CACHE":        filepath.Join(trustedToolDir, "npm-cache"),
		"NPM_CONFIG_USERCONFIG":   filepath.Join(trustedToolDir, "npmrc"),
		"NPM_CONFIG_GLOBALCONFIG": filepath.Join(trustedToolDir, "global-npmrc"),
	}
}

func runnerBrowserMCP(trustedToolDir string) codexMCPServer {
	command, args := runnerBrowserCommand()
	cwd := filepath.Clean(trustedToolDir)
	startupTimeout := float64(runnerBrowserStartupTimeoutSeconds)
	return codexMCPServer{
		Name:                  runnerBrowserMCPServer,
		Enabled:               true,
		Required:              true,
		EnabledTools:          []string{"navigate_page", "evaluate_script", "take_screenshot"},
		StartupTimeoutSeconds: &startupTimeout,
		Transport: codexMCPTransport{
			Type: "stdio", Command: command, Args: args, Env: runnerBrowserEnvironment(cwd), CWD: &cwd,
		},
	}
}

// codexMCPProfileArgsForConfig turns a role allowlist into one self-contained
// Codex config override. Isolated roles inspect only the operator catalog from
// Runner's trusted cwd; inherited roles deliberately retain ambient discovery.
func codexMCPProfileArgsForConfig(ctx context.Context, run subprocess.Runner, command string, workspace profileWorkspace, allowed []string, safeTools bool, harnessConfigMode string) ([]string, error) {
	if len(allowed) == 0 && !safeTools {
		return nil, nil
	}
	if safeTools && (strings.TrimSpace(workspace.TrustedToolDir) == "" || !filepath.IsAbs(workspace.TrustedToolDir)) {
		return nil, errors.New("Runner browser requires an absolute private trusted tool directory")
	}
	var configured []codexMCPServer
	if len(allowed) > 0 {
		catalogCWD := workspace.Dir
		if !inheritsHarnessConfiguration(harnessConfigMode) {
			catalogCWD = workspace.TrustedToolDir
			if strings.TrimSpace(catalogCWD) == "" || !filepath.IsAbs(catalogCWD) {
				return nil, errors.New("isolated Codex MCP inspection requires an absolute private trusted tool directory")
			}
		}
		result, err := subprocess.RunFailClosed(
			ctx, run, command, []string{"mcp", "list", "--json"}, catalogCWD, 10*time.Second,
			maxCodexMCPConfigurationBytes, subprocess.DiagnosticStderrLimit,
		)
		if err != nil {
			return nil, fmt.Errorf("inspect configured Codex MCP servers: %w", err)
		}
		if err := json.Unmarshal([]byte(result.Stdout), &configured); err != nil {
			return nil, errors.New("Codex returned malformed MCP configuration")
		}
	}
	byName := make(map[string]codexMCPServer, len(configured))
	for _, server := range configured {
		byName[strings.TrimSpace(server.Name)] = server
	}
	selected := make([]codexMCPServer, 0, len(allowed)+1)
	seen := map[string]bool{}
	for _, name := range allowed {
		name = strings.TrimSpace(name)
		if !config.ValidMCPServerName(name) {
			return nil, fmt.Errorf("invalid Codex MCP server name %q", name)
		}
		if safeTools && name == runnerBrowserMCPServer {
			return nil, fmt.Errorf("Codex MCP server name %q is reserved by Runner safe tools", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		server, ok := byName[name]
		if !ok || !server.Enabled {
			return nil, fmt.Errorf("Codex MCP server %q is not configured and enabled", name)
		}
		if server.Transport.Type != "stdio" || strings.TrimSpace(server.Transport.Command) == "" {
			return nil, fmt.Errorf("Codex MCP server %q must use a local stdio command", name)
		}
		if len(server.Transport.Env) > 0 {
			return nil, fmt.Errorf("Codex MCP server %q contains inline environment values; use env_vars so Runner never places secrets in process arguments", name)
		}
		selected = append(selected, server)
	}
	if inheritsHarnessConfiguration(harnessConfigMode) {
		if !safeTools {
			return nil, nil
		}
		var encoded strings.Builder
		encoded.WriteString("mcp_servers.")
		encoded.WriteString(runnerBrowserMCPServer)
		encoded.WriteByte('=')
		writeCodexMCPServer(&encoded, runnerBrowserMCP(workspace.TrustedToolDir))
		return []string{"--config", encoded.String()}, nil
	}
	if safeTools {
		selected = append(selected, runnerBrowserMCP(workspace.TrustedToolDir))
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })

	var config strings.Builder
	config.WriteString("mcp_servers={")
	for index, server := range selected {
		if index > 0 {
			config.WriteByte(',')
		}
		config.WriteString(server.Name)
		config.WriteByte('=')
		writeCodexMCPServer(&config, server)
	}
	config.WriteByte('}')
	return []string{"--config", config.String()}, nil
}

func writeCodexMCPServer(builder *strings.Builder, server codexMCPServer) {
	builder.WriteString("{command=")
	builder.WriteString(strconv.Quote(strings.TrimSpace(server.Transport.Command)))
	writeTOMLStringList(builder, "args", server.Transport.Args)
	writeTOMLStringMap(builder, "env", server.Transport.Env)
	writeTOMLStringList(builder, "env_vars", server.Transport.EnvVars)
	if server.Transport.CWD != nil && strings.TrimSpace(*server.Transport.CWD) != "" {
		builder.WriteString(",cwd=")
		builder.WriteString(strconv.Quote(strings.TrimSpace(*server.Transport.CWD)))
	}
	writeTOMLFloat(builder, "startup_timeout_sec", server.StartupTimeoutSeconds)
	writeTOMLFloat(builder, "tool_timeout_sec", server.ToolTimeoutSeconds)
	writeTOMLStringList(builder, "enabled_tools", server.EnabledTools)
	writeTOMLStringList(builder, "disabled_tools", server.DisabledTools)
	builder.WriteString(",enabled=true")
	if server.Required {
		builder.WriteString(",required=true")
	}
	builder.WriteString(",default_tools_approval_mode=\"approve\"}")
}

func writeTOMLStringMap(builder *strings.Builder, name string, values map[string]string) {
	if len(values) == 0 {
		return
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	builder.WriteByte(',')
	builder.WriteString(name)
	builder.WriteString("={")
	for index, key := range keys {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.Quote(key))
		builder.WriteByte('=')
		builder.WriteString(strconv.Quote(values[key]))
	}
	builder.WriteByte('}')
}

func codexMCPPrompt(allowed []string, safeTools bool) string {
	return codexMCPPromptForConfig(allowed, safeTools, config.HarnessConfigModeIsolated)
}

func codexMCPPromptForConfig(allowed []string, safeTools bool, harnessConfigMode string) string {
	if len(allowed) == 0 && !safeTools {
		if inheritsHarnessConfiguration(harnessConfigMode) {
			return "\n\nRunner is inheriting the operator's Codex configuration. Use ambient MCP servers only when their configured purpose is relevant to this assignment.\n"
		}
		return ""
	}
	names := append([]string{}, allowed...)
	if safeTools {
		names = append(names, runnerBrowserMCPServer)
	}
	sort.Strings(names)
	message := "\n\nRunner-granted Codex MCP servers: " + strings.Join(names, ", ") + ". Use whichever callable surface this Codex session provides. If a granted MCP tool is not exposed as a direct call and Code Mode is active, inspect ALL_TOOLS for entries whose names contain the exact server name, then call the matching function through the tools object. list_mcp_resources reports resources, not the available MCP tools, and an empty resource list does not mean the granted tools are unavailable."
	if inheritsHarnessConfiguration(harnessConfigMode) {
		message += " Runner is also inheriting the operator's ambient Codex MCP configuration; use those servers only when relevant to this assignment.\n"
	} else {
		message += " Do not use or search for any unlisted MCP server.\n"
	}
	return message + runnerBrowserPrompt(safeTools)
}

func runnerBrowserPrompt(safeTools bool) string {
	if !safeTools {
		return ""
	}
	return `
Runner browser verification contract:
- For rendered-page, interaction, or console verification, start the local application or server and use runner_browser before trying any shell-launched browser.
- runner_browser exposes navigate_page, evaluate_script, and take_screenshot. Use their direct MCP calls when available. In Code Mode, inspect ALL_TOOLS for runner_browser and invoke the matching functions through the tools object. Do not infer availability from resource discovery.
- Run an already-configured project browser test when the task requires it, but failure of that one integration does not make browser verification unavailable while runner_browser is granted.
- Do not download a browser, install a browser dependency, or inspect ambient browser caches merely to perform verification.
- Report browser capability as blocked only after an exposed runner_browser call returns a concrete failure, or after both the direct and Code Mode tool catalogs have been checked and contain no runner_browser entry.
`
}

func writeTOMLStringList(builder *strings.Builder, name string, values []string) {
	if len(values) == 0 {
		return
	}
	builder.WriteByte(',')
	builder.WriteString(name)
	builder.WriteString("=[")
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.Quote(value))
	}
	builder.WriteByte(']')
}

func writeTOMLFloat(builder *strings.Builder, name string, value *float64) {
	if value == nil {
		return
	}
	builder.WriteByte(',')
	builder.WriteString(name)
	builder.WriteByte('=')
	builder.WriteString(strconv.FormatFloat(*value, 'f', -1, 64))
}
