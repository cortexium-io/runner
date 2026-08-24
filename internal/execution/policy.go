package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const (
	codexRepositoryReadPermissionProfile         = "runner_repository_read"
	codexReviewerPermissionProfile               = "runner_reviewer_read"
	codexImplementationWritePermissionProfile    = "runner_implementation_write"
	codexReviewerBrowserPermissionProfile        = "runner_reviewer_browser"
	codexImplementerDevelopmentPermissionProfile = "runner_implementer_development"
)

func requiresFullHarnessAccess(profile ExecutionProfile) bool {
	return profile.Sandbox == SandboxFullAccess
}

// codexProfileArgs supplies Runner-owned policy for every role. Implementers
// and reviewers default to native isolation. Host access is an explicit role
// choice, never an implicit browser workaround.
func codexProfileArgs(profile ExecutionProfile, workspace profileWorkspace, safeTools bool, command ...string) []string {
	args := []string{"--ask-for-approval", config.CodexApprovalNever}
	if requiresFullHarnessAccess(profile) {
		args = append(args, "--sandbox", config.CodexSandboxDangerFullAccess)
	} else if profile.Repository != RepositoryNone && (profile.allowsTool(ToolReadShell) || profile.allowsTool(ToolShell)) {
		name := codexRepositoryReadPermissionProfile
		description := "Runner-scoped repository read"
		filesystem := `{":minimal"="read",":workspace_roots"={"."="read"}}`
		workspaceRoots := "{}"
		if workspace.ReadRoot != "" {
			workspaceRoots = fmt.Sprintf("{%s=true}", strconv.Quote(workspace.ReadRoot))
		}
		network := `{enabled=false}`

		switch profile.Role {
		case RoleReviewer:
			name = codexReviewerPermissionProfile
			description = "Runner reviewer read with private temporary output"
			filesystem = fmt.Sprintf(`{":minimal"="read",":workspace_roots"={"."="read"},%s="write"}`, strconv.Quote(workspace.Dir))
			if safeTools {
				name = codexReviewerBrowserPermissionProfile
				description = "Runner reviewer with local browser QA"
				network = `{enabled=true,mode="limited",allow_local_binding=true,domains={"localhost"="allow","127.0.0.1"="allow"}}`
			}
		case RoleImplementer:
			name = codexImplementationWritePermissionProfile
			description = "Runner-scoped implementation write"
			filesystem = `{":minimal"="read",":workspace_roots"={"."="write"}}`
			if safeTools {
				name = codexImplementerDevelopmentPermissionProfile
				description = "Runner implementer with package and local-app access"
				filesystem = `{":minimal"="read",":workspace_roots"={"."="write"},"~/.npm"="write"}`
				network = `{enabled=true,mode="limited",allow_local_binding=true,domains={"localhost"="allow","127.0.0.1"="allow","registry.npmjs.org"="allow"}}`
			}
		}
		readPaths := sandboxAdditionalReadPaths(workspace, safeTools)
		if profile.Role == RoleImplementer && len(command) > 0 {
			readPaths = append(readPaths, codexHelperReadPaths(command[0])...)
		}
		filesystem = addCodexReadPaths(filesystem, minimalPathRoots(readPaths))
		filesystem = addCodexWritePaths(filesystem, sandboxAdditionalWritePaths(workspace))
		permissionProfile := fmt.Sprintf(
			"permissions.%s={description=%s,filesystem=%s,workspace_roots=%s,network=%s}",
			name, strconv.Quote(description), filesystem, workspaceRoots, network,
		)
		args = append(args,
			"--strict-config",
			"--config", permissionProfile,
			"--config", fmt.Sprintf("default_permissions=%s", strconv.Quote(name)),
		)
		if environment := codexSandboxEnvironmentConfig(workspace); environment != "" {
			args = append(args, "--config", environment)
		}
		if safeTools && (profile.Role == RoleReviewer || profile.Role == RoleImplementer) {
			args = append(args, "--enable", "network_proxy")
		}
	} else if !profile.MutationAllowed {
		args = append(args, "--sandbox", config.CodexSandboxReadOnly)
	}
	// These features are unrelated to every fixed Runner role. Disabling them
	// prevents project/user configuration from reintroducing privileged local
	// resources while authentication remains available to the CLI.
	for _, feature := range []string{
		"apps", "plugins", "plugin_sharing", "remote_plugin", "hooks", "skill_search",
		"skill_mcp_dependency_install", "shell_snapshot",
		"workspace_dependencies", "browser_use", "browser_use_external",
		"browser_use_full_cdp_access", "computer_use", "image_generation", "in_app_browser",
		"standalone_web_search", "multi_agent", "multi_agent_v2", "auth_elicitation",
		"tool_call_mcp_elicitation", "request_permissions_tool", "code_mode",
		"code_mode_only", "code_mode_buffered_exec", "code_mode_host",
	} {
		args = append(args, "--disable", feature)
	}
	args = append(args, "--config", "mcp_servers={}")
	if !profile.allowsTool(ToolReadShell) && !profile.allowsTool(ToolShell) {
		for _, feature := range []string{
			"shell_tool", "shell_zsh_fork", "unified_exec", "unified_exec_zsh_fork",
		} {
			args = append(args, "--disable", feature)
		}
	}
	return args
}

func codexExecIsolationArgs(profile ExecutionProfile, workspace profileWorkspace) []string {
	args := []string{"exec"}
	args = append(args, "--ignore-user-config", "--ignore-rules")
	args = append(args, "--ephemeral", "--json", "--cd", workspace.Dir)
	if profile.Workspace == WorkspaceNeutral {
		args = append(args, "--skip-git-repo-check")
	}
	return args
}

func claudeProfileArgs(profile ExecutionProfile, workspace profileWorkspace, safeTools bool) []string {
	args := []string{
		"--print", "--output-format", "json", "--no-session-persistence",
	}
	// Claude Code safe mode disables MCP servers supplied explicitly through
	// --mcp-config. Roles without Runner safe tools keep that stronger blanket
	// suppression; safe-tool roles use the explicit controls below instead.
	if !safeTools {
		args = append(args, "--safe-mode")
	}
	args = append(args,
		"--setting-sources", "", "--strict-mcp-config",
		"--mcp-config", claudeMCPConfig(safeTools), "--disable-slash-commands", "--no-chrome",
	)
	tools := claudeTools(profile)
	allowedTools := tools
	if safeTools && (profile.Role == RoleImplementer || profile.Role == RoleReviewer) {
		if allowedTools != "" {
			allowedTools += ","
		}
		allowedTools += "mcp__" + runnerBrowserMCPServer + "__*"
	}
	if requiresFullHarnessAccess(profile) {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		args = append(args,
			"--permission-mode", config.ClaudePermissionDontAsk,
			"--settings", claudeSandboxSettings(profile, workspace, safeTools),
		)
	}
	args = append(args,
		"--tools", tools, "--allowedTools", allowedTools,
	)
	if workspace.ReadRoot != "" && filepath.Clean(workspace.ReadRoot) != filepath.Clean(workspace.Dir) {
		args = append(args, "--add-dir", workspace.ReadRoot)
	}
	return args
}

func claudeMCPConfig(safeTools bool) string {
	servers := map[string]any{}
	if safeTools {
		command, args := runnerBrowserCommand()
		servers[runnerBrowserMCPServer] = map[string]any{"command": command, "args": args}
	}
	encoded, _ := json.Marshal(map[string]any{"mcpServers": servers})
	return string(encoded)
}

func claudeTools(profile ExecutionProfile) string {
	tools := []string{}
	if profile.allowsTool(ToolRepositoryRead) {
		tools = append(tools, "Read", "Grep", "Glob")
	}
	if profile.allowsTool(ToolReadShell) || profile.allowsTool(ToolShell) {
		tools = append(tools, "Bash")
	}
	if profile.allowsTool(ToolEdit) {
		tools = append(tools, "Edit", "Write")
	}
	return strings.Join(tools, ",")
}

func claudeSandboxSettings(profile ExecutionProfile, workspace profileWorkspace, safeTools bool) string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		// A missing absolute home must fail closed. The explicit workspace
		// allowlist remains, but system commands may be unavailable.
		home = string(filepath.Separator)
	}
	allowRead := []string{workspace.Dir}
	if workspace.ReadRoot != "" && filepath.Clean(workspace.ReadRoot) != filepath.Clean(workspace.Dir) {
		allowRead = append(allowRead, workspace.ReadRoot)
	}
	allowRead = append(allowRead, sandboxAdditionalReadPaths(workspace, safeTools)...)
	allowWrite := sandboxAdditionalWritePaths(workspace)
	filesystem := map[string]any{
		"denyRead":  []string{filepath.Clean(home)},
		"allowRead": allowRead,
	}
	if len(allowWrite) > 0 {
		filesystem["allowWrite"] = allowWrite
	}
	if workspace.ReadRoot != "" {
		if profile.Role == RoleReviewer {
			filesystem["denyWrite"] = []string{workspace.ReadRoot}
		}
	}
	if safeTools && profile.Role == RoleImplementer {
		// npm's content-addressed cache is the only home-directory exception.
		// Project files and package execution remain inside the worktree.
		filesystem["allowRead"] = append(allowRead, "~/.npm")
		filesystem["allowWrite"] = append(allowWrite, "~/.npm")
	}
	sandbox := map[string]any{
		"enabled": true, "failIfUnavailable": true, "autoAllowBashIfSandboxed": true,
		"allowUnsandboxedCommands": false, "filesystem": filesystem,
	}
	if profile.allowsTool(ToolReadShell) || profile.allowsTool(ToolShell) {
		domains := []string{"localhost", "127.0.0.1"}
		if safeTools && profile.Role == RoleImplementer {
			domains = append(domains, "registry.npmjs.org")
		}
		sandbox["network"] = map[string]any{"allowLocalBinding": true, "allowedDomains": domains}
	}
	settings := map[string]any{
		"autoMemoryEnabled": false,
		"disableAllHooks":   true,
		"sandbox":           sandbox,
	}
	if environment := sandboxEnvironment(workspace); len(environment) > 0 {
		settings["env"] = environment
	}
	encoded, _ := json.Marshal(settings)
	return string(encoded)
}

func sandboxAdditionalReadPaths(workspace profileWorkspace, safeTools bool) []string {
	paths := append([]string(nil), workspace.GitReadRoots...)
	if safeTools {
		paths = append(paths, workspace.ToolReadPaths...)
	}
	filtered := make([]string, 0, len(paths))
	for _, path := range minimalPathRoots(paths) {
		if workspace.Dir != "" && pathInsideOrEqual(path, workspace.Dir) {
			continue
		}
		if workspace.ReadRoot != "" && pathInsideOrEqual(path, workspace.ReadRoot) {
			continue
		}
		filtered = append(filtered, path)
	}
	return filtered
}

func addCodexReadPaths(filesystem string, paths []string) string {
	return addCodexPaths(filesystem, paths, "read")
}

func addCodexWritePaths(filesystem string, paths []string) string {
	return addCodexPaths(filesystem, paths, "write")
}

func addCodexPaths(filesystem string, paths []string, access string) string {
	if len(paths) == 0 {
		return filesystem
	}
	filesystem = strings.TrimSuffix(filesystem, "}")
	for _, path := range paths {
		filesystem += "," + strconv.Quote(path) + "=" + strconv.Quote(access)
	}
	return filesystem + "}"
}

func sandboxAdditionalWritePaths(workspace profileWorkspace) []string {
	if workspace.TempDir == "" || (workspace.Dir != "" && pathInsideOrEqual(workspace.TempDir, workspace.Dir)) {
		return nil
	}
	return []string{filepath.Clean(workspace.TempDir)}
}

func sandboxEnvironment(workspace profileWorkspace) map[string]string {
	if workspace.TempDir == "" {
		return nil
	}
	tempDir := filepath.Clean(workspace.TempDir)
	environment := map[string]string{
		"GIT_ATTR_NOSYSTEM":   "1",
		"GIT_CONFIG_GLOBAL":   "/dev/null",
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_SYSTEM":   "/dev/null",
		"GIT_TERMINAL_PROMPT": "0",
		"NODE_COMPILE_CACHE":  filepath.Join(tempDir, "node-compile-cache"),
		"TMPDIR":              tempDir,
		"XDG_CONFIG_HOME":     tempDir,
		"XDG_STATE_HOME":      tempDir,
		"ZDOTDIR":             tempDir,
	}
	if workspace.ToolPath != "" {
		environment["PATH"] = workspace.ToolPath
	}
	return environment
}

func codexSandboxEnvironmentConfig(workspace profileWorkspace) string {
	environment := sandboxEnvironment(workspace)
	if len(environment) == 0 {
		return ""
	}
	keys := []string{
		"GIT_ATTR_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM",
		"GIT_CONFIG_SYSTEM", "GIT_TERMINAL_PROMPT", "NODE_COMPILE_CACHE",
		"PATH", "TMPDIR", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "ZDOTDIR",
	}
	entries := make([]string, 0, len(environment))
	for _, key := range keys {
		if value, ok := environment[key]; ok {
			entries = append(entries, key+"="+strconv.Quote(value))
		}
	}
	return "shell_environment_policy.set={" + strings.Join(entries, ",") + "}"
}

func piProfileArgs(profile ExecutionProfile) []string {
	return piProfileArgsForResultMode(profile, true, false)
}

func piProfileArgsForModel(profile ExecutionProfile, model *string) []string {
	return piProfileArgsForResultMode(profile, true, piUsesNativeStructuredOutput(model))
}

func piDirectNativeProfileArgs(profile ExecutionProfile) []string {
	args := piProfileArgsForResultMode(profile, false, false)
	return append(args, "--append-system-prompt", piDirectNativeStructuredResultSystemPrompt)
}

func piProfileArgsForResultMode(profile ExecutionProfile, structured, nativeStructured bool) []string {
	args := []string{
		"--print", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates",
		"--no-themes", "--no-context-files", "--mode", "json",
	}
	resultTool := piStructuredResultTool
	resultPrompt := piStructuredResultSystemPrompt
	if nativeStructured {
		resultTool = piNativeStructuredFinalizeTool
		resultPrompt = piNativeStructuredResultSystemPrompt
	}
	if structured {
		args = append(args, "--append-system-prompt", resultPrompt)
	}
	args = append(args, "--no-approve")
	resultToolSuffix := ""
	if structured {
		resultToolSuffix = "," + resultTool
	}
	if !profile.allowsTool(ToolRepositoryRead) {
		if structured {
			return append(args, "--no-tools", "--tools", resultTool)
		}
		return append(args, "--no-tools")
	}
	if profile.MutationAllowed {
		return append(args, "--tools", "read,grep,find,ls,bash,edit,write"+resultToolSuffix)
	}
	if requiresFullHarnessAccess(profile) && profile.allowsTool(ToolReadShell) {
		return append(args, "--tools", "read,grep,find,ls,bash"+resultToolSuffix)
	}
	return append(args, "--tools", "read,grep,find,ls"+resultToolSuffix)
}

func piUsesNativeStructuredOutput(model *string) bool {
	if model == nil {
		return false
	}
	provider, _, found := strings.Cut(strings.TrimSpace(*model), "/")
	return found && strings.EqualFold(provider, "lmstudio")
}

func piUsesQwenThinkingControls(model *string) bool {
	if !piUsesNativeStructuredOutput(model) {
		return false
	}
	_, modelID, _ := strings.Cut(strings.TrimSpace(*model), "/")
	return strings.Contains(strings.ToLower(modelID), "qwen")
}

// RequiredHarnessFlags is shared by setup inspection and process launch. It is
// the only harness flag policy table in Runner.
func RequiredHarnessFlags(kind string, role RoleContract, configuredAccess ...string) (root, command []string, err error) {
	access := config.RoleAccessSandboxed
	if len(configuredAccess) > 0 {
		access = config.EffectiveRoleAccess(configuredAccess[0])
	}
	if err := ValidateHarnessProfile(kind, role, access); err != nil {
		return nil, nil, err
	}
	switch kind {
	case config.HarnessCodexCLI:
		if access == config.RoleAccessHost {
			root = []string{"--ask-for-approval", "--sandbox", "--disable", "--config"}
		} else {
			root = []string{"--ask-for-approval", "--disable", "--config"}
			if role == RolePlanner || role == RoleReviewer || role == RoleImplementer {
				root = append(root, "--strict-config")
			}
		}
		command = []string{"--ephemeral", "--json", "--cd", "--output-last-message", "--output-schema"}
		if role != RoleImplementer {
			command = append(command, "--skip-git-repo-check")
		}
		command = append(command, "--ignore-user-config", "--ignore-rules")
	case config.HarnessClaudeCLI:
		command = []string{
			"--print", "--output-format", "--no-session-persistence", "--json-schema",
			"--safe-mode", "--setting-sources", "--strict-mcp-config", "--mcp-config",
			"--disable-slash-commands", "--no-chrome", "--tools", "--allowedTools",
		}
		if access == config.RoleAccessHost {
			command = append(command, "--dangerously-skip-permissions")
		} else {
			command = append(command, "--permission-mode", "--settings")
		}
		if role != RoleProbe {
			command = append(command, "--add-dir")
		}
	case config.HarnessPiCLI:
		command = []string{
			"--print", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates",
			"--no-themes", "--no-context-files", "--mode", "--append-system-prompt", "--extension",
		}
		command = append(command, "--no-approve")
		if role == RoleProbe || role == RoleSynthesis {
			command = append(command, "--no-tools")
		} else {
			command = append(command, "--tools")
		}
	}
	return root, command, nil
}

func validateAdvertisedFlags(help string, flags []string) error {
	for _, flag := range flags {
		if !strings.Contains(help, flag) {
			return fmt.Errorf("installed harness does not advertise required isolation flag %s", flag)
		}
	}
	return nil
}

func ensureHarnessAdvertisesProfile(ctx context.Context, run subprocess.Runner, command, kind string, role RoleContract, configuredAccess ...string) error {
	// Synthetic runners are capability declarations in unit tests. Production
	// OS launches prove the installed binary's controls before starting a model.
	if _, ok := run.(subprocess.OSRunner); !ok {
		return nil
	}
	root, invocation, err := RequiredHarnessFlags(kind, role, configuredAccess...)
	if err != nil {
		return err
	}
	if len(root) > 0 {
		result, runErr := run.Run(ctx, command, []string{"--help"}, "", 5*time.Second)
		if runErr != nil {
			return fmt.Errorf("inspect installed %s root flags: %w", kind, runErr)
		}
		if err := validateAdvertisedFlags(result.Stdout+"\n"+result.Stderr, root); err != nil {
			return fmt.Errorf("installed %s cannot enforce %s profile: %w", kind, role, err)
		}
	}
	helpArgs := []string{"--help"}
	if kind == config.HarnessCodexCLI {
		helpArgs = []string{"exec", "--help"}
	}
	result, runErr := run.Run(ctx, command, helpArgs, "", 5*time.Second)
	if runErr != nil {
		return fmt.Errorf("inspect installed %s invocation flags: %w", kind, runErr)
	}
	if err := validateAdvertisedFlags(result.Stdout+"\n"+result.Stderr, invocation); err != nil {
		return fmt.Errorf("installed %s cannot enforce %s profile: %w", kind, role, err)
	}
	return nil
}
