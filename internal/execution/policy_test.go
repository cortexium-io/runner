package execution

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestFixedProfilesCoverEveryRunnerLaunchRole(t *testing.T) {
	for _, role := range []RoleContract{RolePlanner, RoleSynthesis, RoleReviewer, RoleProbe, RoleImplementer} {
		profile, err := ProfileForRole(role)
		if err != nil {
			t.Fatalf("profile %s: %v", role, err)
		}
		if profile.Role != role {
			t.Fatalf("unexpected %s profile: %#v", role, profile)
		}
		if role == RoleImplementer {
			if !profile.MutationAllowed || profile.Workspace != WorkspaceWorktree || profile.LocalResources != LocalResourcesHarnessSandbox || profile.Approval != ApprovalNever || profile.Sandbox != SandboxWorkspaceWrite {
				t.Fatalf("implementer ceiling is incomplete: %#v", profile)
			}
		} else if role == RoleReviewer {
			if profile.MutationAllowed || profile.Workspace != WorkspaceNeutral || profile.LocalResources != LocalResourcesHarnessSandbox || profile.Approval != ApprovalNever || profile.Sandbox != SandboxWorkspaceWrite {
				t.Fatalf("reviewer native policy is incomplete: %#v", profile)
			}
		} else if profile.MutationAllowed || profile.Workspace != WorkspaceNeutral || profile.LocalResources != LocalResourcesHarnessSandbox || profile.Approval != ApprovalNever || profile.Sandbox != SandboxReadOnly {
			t.Fatalf("read-only role %s can mutate or use a repository cwd: %#v", role, profile)
		}
	}
}

func TestHostAccessIsExplicitAndPreservesRoleToolCeilings(t *testing.T) {
	for _, role := range []RoleContract{RoleImplementer, RoleReviewer} {
		profile, err := ProfileForRole(role, config.RoleAccessHost)
		if err != nil {
			t.Fatalf("host profile %s: %v", role, err)
		}
		if profile.Sandbox != SandboxFullAccess || profile.LocalResources != LocalResourcesFullAccess || profile.Approval != ApprovalBypass {
			t.Fatalf("host boundary %s = %#v", role, profile)
		}
		if role == RoleReviewer && (profile.MutationAllowed || profile.allowsTool(ToolEdit)) {
			t.Fatalf("reviewer host access expanded its tool ceiling: %#v", profile)
		}
	}
	if _, err := ProfileForRole(RolePlanner, config.RoleAccessHost); err == nil {
		t.Fatal("planner host access was accepted")
	}
}

func TestCodexProfileForcesReadOnlyPolicy(t *testing.T) {
	profile, _ := ProfileForRole(RolePlanner)
	workspace := profileWorkspace{Dir: "/neutral", ReadRoot: "/repo"}
	args := append(codexProfileArgs(profile, workspace, false), codexExecIsolationArgs(profile, workspace)...)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--ask-for-approval never", "--strict-config",
		`permissions.runner_repository_read={description="Runner-scoped repository read"`,
		`filesystem={":minimal"="read",":workspace_roots"={"."="read"}}`,
		`workspace_roots={"/repo"=true}`,
		`network={enabled=false}`,
		`default_permissions="runner_repository_read"`,
		"--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--cd /neutral",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Codex profile omitted %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "danger-full-access") || strings.Contains(joined, "--sandbox") {
		t.Fatalf("Codex repository-read profile used an ambient legacy sandbox: %s", joined)
	}
}

func TestSandboxedCodexRolesCanReadInstalledCLI(t *testing.T) {
	root := t.TempDir()
	standalone := filepath.Join(root, ".codex", "packages", "standalone")
	releaseBin := filepath.Join(standalone, "releases", "v1", "bin")
	if err := os.MkdirAll(releaseBin, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(releaseBin, "codex")
	if err := os.WriteFile(target, []byte("codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	launcherDir := filepath.Join(root, ".local", "bin")
	if err := os.MkdirAll(launcherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(launcherDir, "codex")
	if err := os.Symlink(target, launcher); err != nil {
		t.Fatal(err)
	}

	for _, role := range []RoleContract{RolePlanner, RoleReviewer, RoleImplementer} {
		profile, err := ProfileForRole(role)
		if err != nil {
			t.Fatalf("profile %s: %v", role, err)
		}
		workspace := profileWorkspace{Dir: "/workspace", ReadRoot: "/repo"}
		joined := strings.Join(codexProfileArgs(profile, workspace, false, launcher), " ")
		if !strings.Contains(joined, strconv.Quote(standalone)+`="read"`) {
			t.Fatalf("Codex %s profile cannot read its installed CLI: %s", role, joined)
		}
	}
}

func TestCodexImplementerUsesScopedWritePermissionProfile(t *testing.T) {
	profile, _ := ProfileForRole(RoleImplementer)
	workspace := profileWorkspace{Dir: "/worktree", ReadRoot: "/worktree"}
	args := append(codexProfileArgs(profile, workspace, false), codexExecIsolationArgs(profile, workspace)...)
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		"--ask-for-approval never", "--strict-config",
		`permissions.runner_implementation_write={description="Runner-scoped implementation write"`,
		`filesystem={":minimal"="read",":workspace_roots"={"."="write"}}`,
		`workspace_roots={"/worktree"=true}`,
		`network={enabled=false}`,
		`default_permissions="runner_implementation_write"`,
		"--ignore-user-config", "--ignore-rules", "--cd /worktree",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Codex implementer profile omitted %q: %s", expected, joined)
		}
	}
	if strings.Contains(joined, "danger-full-access") || strings.Contains(joined, "--sandbox workspace-write") {
		t.Fatalf("Codex implementer retained an ambient legacy sandbox: %s", joined)
	}
}

func TestCodexImplementerSafeToolsUseBoundedDevelopmentNetwork(t *testing.T) {
	profile, _ := ProfileForRole(RoleImplementer)
	joined := strings.Join(codexProfileArgs(profile, profileWorkspace{Dir: "/worktree", ReadRoot: "/worktree"}, true), " ")
	for _, expected := range []string{
		"--strict-config", "--enable network_proxy", "runner_implementer_development",
		`filesystem={":minimal"="read",":workspace_roots"={"."="write"},"~/.npm"="write"}`,
		`workspace_roots={"/worktree"=true}`,
		`"localhost"="allow"`, `"127.0.0.1"="allow"`, `"registry.npmjs.org"="allow"`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("safe implementer profile omitted %q: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{"danger-full-access", "github.com", "0.0.0.0"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("safe implementer profile widened access with %q: %s", forbidden, joined)
		}
	}
}

func TestSandboxProfilesGrantOnlyResolvedGitAndDevelopmentToolReads(t *testing.T) {
	profile, _ := ProfileForRole(RoleImplementer)
	workspace := profileWorkspace{
		Dir: "/worktrees/assignment", ReadRoot: "/worktrees/assignment",
		GitReadRoots:  []string{"/repos/project/.git"},
		ToolReadPaths: []string{"/opt/tools/node/24", "/opt/tools/npm", "/opt/tools/bin/node"},
		TempDir:       "/private/runtime",
		ToolPath:      "/opt/tools/bin:/usr/bin:/bin",
	}
	codex := strings.Join(codexProfileArgs(profile, workspace, true), " ")
	claude := strings.Join(claudeProfileArgs(profile, workspace, true), " ")
	for _, allowed := range []string{"/repos/project/.git", "/opt/tools/node/24", "/opt/tools/npm", "/opt/tools/bin/node"} {
		if !strings.Contains(codex, strconv.Quote(allowed)+`="read"`) {
			t.Fatalf("Codex profile omitted exact read grant %q: %s", allowed, codex)
		}
		if !strings.Contains(claude, allowed) {
			t.Fatalf("Claude profile omitted exact read grant %q: %s", allowed, claude)
		}
	}
	for _, expected := range []string{
		`"/private/runtime"="write"`,
		`shell_environment_policy.set={GIT_ATTR_NOSYSTEM="1",GIT_CONFIG_GLOBAL="/dev/null",GIT_CONFIG_NOSYSTEM="1",GIT_CONFIG_SYSTEM="/dev/null",GIT_TERMINAL_PROMPT="0",NODE_COMPILE_CACHE="/private/runtime/node-compile-cache",PATH="/opt/tools/bin:/usr/bin:/bin",TMPDIR="/private/runtime",XDG_CONFIG_HOME="/private/runtime",XDG_STATE_HOME="/private/runtime",ZDOTDIR="/private/runtime"}`,
	} {
		if !strings.Contains(codex, expected) {
			t.Fatalf("Codex sandbox omitted private runtime policy %q: %s", expected, codex)
		}
	}
	for _, expected := range []string{
		`"allowWrite":["/private/runtime","~/.npm"]`,
		`"GIT_CONFIG_GLOBAL":"/dev/null"`,
		`"NODE_COMPILE_CACHE":"/private/runtime/node-compile-cache"`,
		`"PATH":"/opt/tools/bin:/usr/bin:/bin"`,
		`"TMPDIR":"/private/runtime"`,
		`"XDG_STATE_HOME":"/private/runtime"`,
	} {
		if !strings.Contains(claude, expected) {
			t.Fatalf("Claude sandbox omitted private runtime policy %q: %s", expected, claude)
		}
	}
	for _, forbidden := range []string{"/repos/project\"=\"read", "/opt/tools\"=\"read", "/home/operator"} {
		if strings.Contains(codex, forbidden) || strings.Contains(claude, forbidden) {
			t.Fatalf("sandbox profile widened access with %q: codex=%s claude=%s", forbidden, codex, claude)
		}
	}
}

func TestReviewerProfilesDefaultToNativeIsolation(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	profile, _ := ProfileForRole(RoleReviewer)
	workspace := profileWorkspace{Dir: "/neutral", ReadRoot: "/repo"}
	codex := append(codexProfileArgs(profile, workspace, true), codexExecIsolationArgs(profile, workspace)...)
	joinedCodex := strings.Join(codex, " ")
	for _, required := range []string{
		"--strict-config", "--enable", "network_proxy",
		`permissions.runner_reviewer_browser={description="Runner reviewer with local browser QA",filesystem={":minimal"="read",":workspace_roots"={"."="read"},"/neutral"="write"}`,
		`workspace_roots={"/repo"=true}`,
		`network={enabled=true,mode="limited",allow_local_binding=true,domains={"localhost"="allow","127.0.0.1"="allow"}}`,
		`default_permissions="runner_reviewer_browser"`,
		"--ignore-user-config", "--ignore-rules", "--disable", "mcp_servers={}", "exec", "--ephemeral", "--json", "--cd", "/neutral", "--skip-git-repo-check",
	} {
		if !contains(codex, required) {
			if !strings.Contains(joinedCodex, required) {
				t.Fatalf("Codex reviewer omitted %q: %#v", required, codex)
			}
		}
	}
	if containsArgPair(codex, "--add-dir", "/repo") {
		t.Fatalf("Codex reviewer made the reviewed repository writable: %#v", codex)
	}
	if !containsArgPair(codex, "--ask-for-approval", "never") || strings.Contains(joinedCodex, "danger-full-access") || containsArgPair(codex, "--sandbox", "workspace-write") {
		t.Fatalf("Codex reviewer did not remain isolated: %#v", codex)
	}

	claude := claudeProfileArgs(profile, workspace, true)
	for _, required := range []string{"--permission-mode", "--tools", "--allowedTools", "--safe-mode", "--setting-sources", "--no-chrome", "--settings", "--add-dir"} {
		if required != "--safe-mode" && !contains(claude, required) {
			t.Fatalf("Claude reviewer omitted %q: %#v", required, claude)
		}
	}
	if contains(claude, "--safe-mode") || contains(claude, "--dangerously-skip-permissions") || !containsArgPair(claude, "--tools", "Read,Grep,Glob,Bash") || !containsArgPair(claude, "--allowedTools", "Read,Grep,Glob,Bash,mcp__runner_browser__*") {
		t.Fatalf("Claude reviewer did not remain sandboxed with fixed read tools: %#v", claude)
	}
	joinedClaude := strings.Join(claude, " ")
	for _, expected := range []string{
		`"autoMemoryEnabled":false`, `"disableAllHooks":true`,
		`"allowLocalBinding":true`, `"allowedDomains":["localhost","127.0.0.1"]`,
		`"denyWrite":["/repo"]`, `"denyRead":["/home/operator"]`, `"allowRead":["/neutral","/repo"]`,
		`"mcpServers":{"runner_browser"`, `chrome-devtools-mcp@1.7.0`,
	} {
		if !strings.Contains(joinedClaude, expected) {
			t.Fatalf("Claude reviewer sandbox omitted %s: %#v", expected, claude)
		}
	}
	for _, forbidden := range []string{"registry.npmjs.org", "https://", "dangerously-skip-permissions"} {
		if strings.Contains(joinedClaude, forbidden) {
			t.Fatalf("Claude reviewer profile widened access with %q: %#v", forbidden, claude)
		}
	}

	if err := ValidateHarnessProfile(config.HarnessPiCLI, RoleReviewer); err == nil {
		t.Fatal("Pi reviewer was accepted without host access")
	}
	piHost, err := ProfileForRole(RoleReviewer, config.RoleAccessHost)
	if err != nil {
		t.Fatal(err)
	}
	pi := piProfileArgs(piHost)
	if !contains(pi, "--no-approve") || !containsArgPair(pi, "--tools", "read,grep,find,ls,bash,"+piStructuredResultTool) || contains(pi, "--approve") {
		t.Fatalf("Pi host reviewer did not suppress ambient resources or fix tools: %#v", pi)
	}
}

func TestToolFreeProfilesSuppressNativeResources(t *testing.T) {
	profile, _ := ProfileForRole(RoleProbe)
	claude := strings.Join(claudeProfileArgs(profile, profileWorkspace{Dir: "/neutral"}, false), " ")
	for _, expected := range []string{"--safe-mode", "--setting-sources", "--strict-mcp-config", "--disable-slash-commands", "--permission-mode dontAsk", "--tools  --allowedTools"} {
		if !strings.Contains(claude, expected) {
			t.Fatalf("Claude probe profile omitted %q: %s", expected, claude)
		}
	}
	pi := strings.Join(piProfileArgs(profile), " ")
	for _, expected := range []string{"--no-extensions", "--no-skills", "--no-context-files", "--no-approve", "--no-tools", "--tools " + piStructuredResultTool} {
		if !strings.Contains(pi, expected) {
			t.Fatalf("Pi probe profile omitted %q: %s", expected, pi)
		}
	}
}

func TestPiLMStudioUsesNativeFinalizerWithoutChangingOtherProviders(t *testing.T) {
	profile, err := ProfileForRole(RolePlanner)
	if err != nil {
		t.Fatal(err)
	}
	lmStudio := piProfileArgsForModel(profile, ptr("lmstudio/qwen/qwen3.8-27b"))
	if !containsArgPair(lmStudio, "--tools", "read,grep,find,ls,"+piNativeStructuredFinalizeTool) ||
		!containsArgPair(lmStudio, "--append-system-prompt", piNativeStructuredResultSystemPrompt) ||
		strings.Contains(strings.Join(lmStudio, " "), piStructuredResultSystemPrompt) {
		t.Fatalf("LM Studio Pi profile did not select native JSON finalization: %#v", lmStudio)
	}
	if !piUsesQwenThinkingControls(ptr("lmstudio/qwen/qwen3.8-27b")) || piUsesQwenThinkingControls(ptr("lmstudio/mistral-small")) || piUsesQwenThinkingControls(ptr("ollama/qwen3.8:27b")) {
		t.Fatal("Qwen thinking controls were not limited to LM Studio Qwen models")
	}
	legacy := piProfileArgsForModel(profile, ptr("ollama/qwen3.8:27b"))
	if !containsArgPair(legacy, "--tools", "read,grep,find,ls,"+piStructuredResultTool) ||
		!containsArgPair(legacy, "--append-system-prompt", piStructuredResultSystemPrompt) ||
		strings.Contains(strings.Join(legacy, " "), piNativeStructuredResultSystemPrompt) {
		t.Fatalf("non-LM-Studio Pi profile lost its compatible result transport: %#v", legacy)
	}
}

func TestClaudeImplementerSafeToolsUseBoundedDevelopmentProfile(t *testing.T) {
	t.Setenv("HOME", "/home/operator")
	profile, _ := ProfileForRole(RoleImplementer)
	args := claudeProfileArgs(profile, profileWorkspace{Dir: "/worktree", ReadRoot: "/worktree"}, true)
	if !containsArgPair(args, "--tools", "Read,Grep,Glob,Bash,Edit,Write") {
		t.Fatalf("Claude implementer exposed an unexpected built-in tool set: %#v", args)
	}
	if !containsArgPair(args, "--allowedTools", "Read,Grep,Glob,Bash,Edit,Write,mcp__runner_browser__*") {
		t.Fatalf("Claude implementer did not authorize Runner's browser tools: %#v", args)
	}
	joined := strings.Join(args, " ")
	for _, expected := range []string{
		`"mcpServers":{"runner_browser"`, `chrome-devtools-mcp@1.7.0`,
		`--allowed-url-pattern=http://localhost:*/*`, `--allowed-url-pattern=http://127.0.0.1:*/*`,
		`--host-resolver-rules=MAP * ~NOTFOUND`,
		`"allowedDomains":["localhost","127.0.0.1","registry.npmjs.org"]`,
		`"denyRead":["/home/operator"]`, `"allowRead":["/worktree","~/.npm"]`, `"allowWrite":["~/.npm"]`,
		`mcp__runner_browser__*`,
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Claude safe implementer profile omitted %q: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{"github.com", "dangerously-skip-permissions", `"allowUnsandboxedCommands":true`} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Claude safe implementer profile widened access with %q: %s", forbidden, joined)
		}
	}
	if contains(args, "--safe-mode") {
		t.Fatalf("Claude safe implementer disabled Runner's explicit MCP server: %#v", args)
	}
}

func TestEveryHarnessSupportsTheImplementerRole(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI} {
		if err := ValidateHarnessProfile(kind, RoleImplementer); err != nil {
			t.Fatalf("%s implementer profile rejected: %v", kind, err)
		}
	}
	if err := ValidateHarnessProfile(config.HarnessPiCLI, RoleImplementer); err == nil {
		t.Fatal("Pi implementer was accepted without explicit host access")
	}
	if err := ValidateHarnessProfile(config.HarnessPiCLI, RoleImplementer, config.RoleAccessHost); err != nil {
		t.Fatalf("Pi host implementer profile rejected: %v", err)
	}
}

func TestUnknownExecutionRoleFailsClosed(t *testing.T) {
	if err := ValidateHarnessProfile(config.HarnessCodexCLI, RoleContract("unknown")); err == nil {
		t.Fatal("unknown execution role was accepted")
	}
}
