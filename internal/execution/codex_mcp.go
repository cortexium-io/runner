package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func runnerBrowserCommand() (string, []string) {
	return "npx", []string{
		"-y", "chrome-devtools-mcp@1.7.0", "--headless", "--isolated", "--slim",
		"--allowed-url-pattern=http://localhost:*/*", "--allowed-url-pattern=http://127.0.0.1:*/*",
		"--chrome-arg=--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1",
		"--chrome-arg=--use-mock-keychain", "--no-performance-crux", "--redact-network-headers", "--no-usage-statistics",
	}
}

func runnerBrowserMCP() codexMCPServer {
	command, args := runnerBrowserCommand()
	return codexMCPServer{
		Name: runnerBrowserMCPServer, Enabled: true,
		Transport: codexMCPTransport{
			Type: "stdio", Command: command, Args: args,
		},
	}
}

// codexMCPProfileArgs turns an explicit role allowlist into one self-contained
// Codex config override. User and project configuration remain suppressed, and
// no unlisted MCP server is available to the model.
func codexMCPProfileArgs(ctx context.Context, run subprocess.Runner, command, cwd string, allowed []string, safeTools bool) ([]string, error) {
	if len(allowed) == 0 && !safeTools {
		return nil, nil
	}
	var configured []codexMCPServer
	if len(allowed) > 0 {
		result, err := subprocess.RunFailClosed(
			ctx, run, command, []string{"mcp", "list", "--json"}, cwd, 10*time.Second,
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
	if safeTools {
		selected = append(selected, runnerBrowserMCP())
		seen[runnerBrowserMCPServer] = true
	}
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
	sort.Slice(selected, func(i, j int) bool { return selected[i].Name < selected[j].Name })

	var config strings.Builder
	config.WriteString("mcp_servers={")
	for index, server := range selected {
		if index > 0 {
			config.WriteByte(',')
		}
		config.WriteString(server.Name)
		config.WriteString("={command=")
		config.WriteString(strconv.Quote(strings.TrimSpace(server.Transport.Command)))
		writeTOMLStringList(&config, "args", server.Transport.Args)
		writeTOMLStringList(&config, "env_vars", server.Transport.EnvVars)
		if server.Transport.CWD != nil && strings.TrimSpace(*server.Transport.CWD) != "" {
			config.WriteString(",cwd=")
			config.WriteString(strconv.Quote(strings.TrimSpace(*server.Transport.CWD)))
		}
		writeTOMLFloat(&config, "startup_timeout_sec", server.StartupTimeoutSeconds)
		writeTOMLFloat(&config, "tool_timeout_sec", server.ToolTimeoutSeconds)
		writeTOMLStringList(&config, "enabled_tools", server.EnabledTools)
		writeTOMLStringList(&config, "disabled_tools", server.DisabledTools)
		config.WriteString(",enabled=true,default_tools_approval_mode=\"approve\"}")
	}
	config.WriteByte('}')
	return []string{"--config", config.String()}, nil
}

func codexMCPPrompt(allowed []string, safeTools bool) string {
	if len(allowed) == 0 && !safeTools {
		return ""
	}
	names := append([]string{}, allowed...)
	if safeTools {
		names = append(names, runnerBrowserMCPServer)
	}
	sort.Strings(names)
	return "\n\nRunner-granted Codex MCP servers: " + strings.Join(names, ", ") + ". Call their tools as direct MCP tool calls, not through Code Mode or a tools object. list_mcp_resources reports resources, not the available MCP tools, and an empty resource list does not mean the granted tools are unavailable. Do not use or search for any unlisted MCP server.\n" + runnerBrowserPrompt(safeTools)
}

func runnerBrowserPrompt(safeTools bool) string {
	if !safeTools {
		return ""
	}
	return `
Runner browser verification contract:
- For rendered-page, interaction, or console verification, start the local application or server and use runner_browser before trying any shell-launched browser.
- Call runner_browser's navigate, evaluate, and screenshot tools directly. Do not infer availability from resource discovery.
- Run an already-configured project browser test when the task requires it, but failure of that one integration does not make browser verification unavailable while runner_browser is granted.
- Do not download a browser, install a browser dependency, or inspect ambient browser caches merely to perform verification.
- Report browser capability as blocked only after a direct runner_browser tool call returns a concrete failure.
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
