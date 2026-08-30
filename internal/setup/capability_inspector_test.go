package setup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	bundledskills "github.com/cortexium-io/runner/skills"
)

func initGitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is required for capability tests: %v", err)
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "runner-test@example.invalid"},
		{"config", "user.name", "Runner Test"},
	} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "Initial commit"}} {
		command := exec.Command("git", args...)
		command.Dir = dir
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return dir
}

type inspectorCommandRunner struct{}

func (inspectorCommandRunner) Run(_ context.Context, command string, args []string, dir string, _ time.Duration) (subprocess.Result, error) {
	name := filepath.Base(command)
	if len(args) == 1 && args[0] == "--version" {
		return subprocess.Result{Stdout: name + " version test\n"}, nil
	}
	if name == "codex" && reflect.DeepEqual(args, []string{"--help"}) {
		return subprocess.Result{Stdout: codexRootHelp()}, nil
	}
	if name == "codex" && reflect.DeepEqual(args, []string{"exec", "--help"}) {
		return subprocess.Result{Stdout: codexExecHelp()}, nil
	}
	if len(args) == 1 && args[0] == "--help" {
		return subprocess.Result{Stdout: harnessHelp(map[string]string{"claude": config.HarnessClaudeCLI, "pi": config.HarnessPiCLI}[name])}, nil
	}
	if name == "git" && len(args) >= 4 && args[0] == "-C" && args[2] == "rev-parse" {
		return subprocess.Result{Stdout: dirOrArg(dir, args[1]) + "\n"}, nil
	}
	if name == "codex" && strings.Join(args, " ") == "mcp list --json" {
		return subprocess.Result{Stdout: `[{"name":"semantic","enabled":true}]`}, nil
	}
	if name == "gh" && strings.Join(args, " ") == "api user --jq .login" {
		return subprocess.Result{Stdout: "octocat\n"}, nil
	}
	if name == "gh" && len(args) == 2 && args[0] == "api" {
		switch {
		case strings.Contains(args[1], "/rules/branches/"):
			return subprocess.Result{Stdout: `[]`}, nil
		case strings.Contains(args[1], "/branches/"):
			return subprocess.Result{Stdout: `{"name":"main","protected":false}`}, nil
		case strings.HasPrefix(args[1], "repos/"):
			return subprocess.Result{Stdout: `{"allow_auto_merge":true,"allow_merge_commit":true,"allow_rebase_merge":true,"allow_squash_merge":true,"permissions":{"push":true}}`}, nil
		}
	}
	return subprocess.Result{}, nil
}

func TestCapabilityInspectorRejectsHarnessWithoutConfiguredPolicyFlags(t *testing.T) {
	home := t.TempDir()
	enabled := true
	cfg := config.Config{Harnesses: []config.HarnessConfig{{
		Kind: config.HarnessCodexCLI, Command: "codex", Enabled: &enabled,
	}}}
	inspector := NewInspector(cfg, inspectorCommandRunner{})
	inspector.homeDir = func() (string, error) { return home, nil }
	inspector.lookPath = func(command string) (string, error) { return filepath.Join("/tools", command), nil }
	inspector.run = policyHelpRunner{inspectorCommandRunner: inspectorCommandRunner{}}

	descriptor := defaultHarnessDescriptors(home, cfg.Harnesses)[0]
	report, capabilities := inspector.inspectHarness(t.Context(), descriptor, nil, inspector.requiredBundledSkills(descriptor.Kind))
	if report.Status != CapabilityBlocked || report.Ready || !strings.Contains(report.Detail, "--ask-for-approval") {
		t.Fatalf("unsupported configured policy was reported ready: %#v", report)
	}
	if len(capabilities) == 0 || capabilities[0].Status != CapabilityBlocked {
		t.Fatalf("unsupported policy capability was not blocked: %#v", capabilities)
	}
}

type policyHelpRunner struct{ inspectorCommandRunner }

func (policyHelpRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if filepath.Base(command) == "codex" && reflect.DeepEqual(args, []string{"--help"}) {
		return subprocess.Result{Stdout: strings.ReplaceAll(codexRootHelp(), "--ask-for-approval\n", "")}, nil
	}
	return (inspectorCommandRunner{}).Run(ctx, command, args, dir, timeout)
}

func TestCapabilityInspectorAcceptsCodexFlagsAtTheirInvocationScopes(t *testing.T) {
	cfg := config.Config{Harnesses: []config.HarnessConfig{{
		Kind: config.HarnessCodexCLI,
	}}}
	inspector := NewInspector(cfg, inspectorCommandRunner{})
	harness, ok := cfg.Harness(config.HarnessCodexCLI)
	if !ok {
		t.Fatal("configured Codex harness was not found")
	}
	if err := inspector.inspectHarnessInvocationSupport(t.Context(), "/tools/codex", harness); err != nil {
		t.Fatalf("root- and exec-scoped Codex flags were rejected: %v", err)
	}
}

func codexRootHelp() string {
	return strings.Join([]string{"--sandbox", "--ask-for-approval", "--disable", "--enable", "--config", "--strict-config"}, "\n") + "\n"
}

func codexExecHelp() string {
	return strings.Join([]string{"--ephemeral", "--json", "--cd", "--output-last-message", "--output-schema", "--model", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check"}, "\n") + "\n"
}

func harnessHelp(kind string) string {
	flags := map[string][]string{
		config.HarnessClaudeCLI: {
			"--print", "--output-format", "--json-schema", "--no-session-persistence", "--model", "--effort", "--permission-mode", "--settings", "--tools", "--allowedTools", "--safe-mode", "--setting-sources", "--strict-mcp-config", "--mcp-config", "--disable-slash-commands", "--no-chrome", "--add-dir", "--dangerously-skip-permissions",
		},
		config.HarnessPiCLI: {
			"--print", "--no-session", "--no-extensions", "--mode", "--append-system-prompt", "--extension", "--model", "--thinking", "--no-approve", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--no-tools", "--tools",
		},
	}
	return strings.Join(flags[kind], "\n") + "\n"
}

func TestCapabilityInspectorRequiresExplicitPiHostForImplementationAndReview(t *testing.T) {
	enabled := true
	workflow := config.WorkflowTemplate(true)
	roles := config.RoleTemplate(config.HarnessPiCLI)
	cfg := config.Config{
		GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1}, Workflow: &workflow, Roles: roles,
		Harnesses: []config.HarnessConfig{{Kind: config.HarnessPiCLI, Command: "pi", Enabled: &enabled, WorkspaceWriteRoot: t.TempDir()}},
	}
	inspector := NewInspector(cfg, inspectorCommandRunner{})
	harness, _ := cfg.Harness(config.HarnessPiCLI)
	if err := inspector.inspectHarnessInvocationSupport(t.Context(), "/tools/pi", harness); err == nil || !strings.Contains(err.Error(), "access to host") {
		t.Fatalf("Pi sandboxed implementation/review was reported ready: %v", err)
	}
	for _, id := range []string{config.WorkRoleImplementer, config.WorkRoleReviewer} {
		role := roles[id]
		role.Access = config.RoleAccessHost
		roles[id] = role
	}
	cfg.Roles = roles
	inspector = NewInspector(cfg, inspectorCommandRunner{})
	if err := inspector.inspectHarnessInvocationSupport(t.Context(), "/tools/pi", harness); err != nil {
		t.Fatalf("explicit Pi host profiles were rejected: %v", err)
	}
}

type missingInvocationFlagRunner struct {
	kind        string
	missingFlag string
}

func (r missingInvocationFlagRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	var help string
	if r.kind == config.HarnessCodexCLI {
		help = codexExecHelp()
		if reflect.DeepEqual(args, []string{"--help"}) {
			help = codexRootHelp()
		}
	} else {
		help = harnessHelp(r.kind)
	}
	return subprocess.Result{Stdout: strings.ReplaceAll(help, r.missingFlag+"\n", "")}, nil
}

func TestCapabilityInspectorRejectsMissingNonInteractiveInvocationFlag(t *testing.T) {
	tests := []struct {
		kind        string
		missingFlag string
	}{
		{kind: config.HarnessCodexCLI, missingFlag: "--json"},
		{kind: config.HarnessClaudeCLI, missingFlag: "--json-schema"},
		{kind: config.HarnessPiCLI, missingFlag: "--print"},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			cfg := config.Config{Harnesses: []config.HarnessConfig{{
				Kind: test.kind,
			}}}
			inspector := NewInspector(cfg, missingInvocationFlagRunner{kind: test.kind, missingFlag: test.missingFlag})
			harness, ok := cfg.Harness(test.kind)
			if !ok {
				t.Fatal("configured harness was not found")
			}
			err := inspector.inspectHarnessInvocationSupport(t.Context(), "/tools/harness", harness)
			if err == nil || !strings.Contains(err.Error(), test.missingFlag) || !strings.Contains(err.Error(), "non-interactive invocation") {
				t.Fatalf("missing %s was not rejected clearly: %v", test.missingFlag, err)
			}
		})
	}
}

type unauthenticatedGitHubRunner struct{ inspectorCommandRunner }

func (unauthenticatedGitHubRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if filepath.Base(command) == "gh" && len(args) > 0 && args[0] == "auth" {
		return subprocess.Result{Stderr: "not logged in", ExitCode: 1}, errors.New("exit status 1")
	}
	return (inspectorCommandRunner{}).Run(ctx, command, args, dir, timeout)
}

func dirOrArg(dir, arg string) string {
	if strings.TrimSpace(arg) != "" {
		return arg
	}
	return dir
}

func TestCapabilityInspectorRequiresOneHarnessWithBundledSkills(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	inspector := NewInspector(config.Config{}, inspectorCommandRunner{})
	inspector.homeDir = func() (string, error) { return home, nil }
	inspector.lookPath = func(command string) (string, error) {
		switch command {
		case "git", "gh", "codex":
			return filepath.Join("/tools", command), nil
		default:
			return "", errors.New("not found")
		}
	}

	missing := inspector.Inspect(t.Context(), InspectionRequest{ProjectDir: project})
	if missing.Ready {
		t.Fatal("expected missing role skills to block readiness")
	}

	descriptor := defaultHarnessDescriptors(home, nil)[0]
	for _, skill := range (bundledskills.EmbeddedCatalog{}).List() {
		path := filepath.Join(descriptor.SkillRoot, skill.ID, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create skill directory: %v", err)
		}
		if err := os.WriteFile(path, skill.Content, 0o644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}

	ready := inspector.Inspect(t.Context(), InspectionRequest{ProjectDir: project})
	if !ready.Ready {
		t.Fatalf("expected installed Codex with trusted skills to be ready: %#v", ready)
	}
	if ready.Harnesses[0].Authentication != "not_inspected" {
		t.Fatalf("doctor must not inspect harness authentication: %#v", ready.Harnesses[0])
	}
}

func TestCapabilityInspectorChecksOnlyExplicitMCPRequirements(t *testing.T) {
	home := t.TempDir()
	inspector := NewInspector(config.Config{}, inspectorCommandRunner{})
	inspector.homeDir = func() (string, error) { return home, nil }
	inspector.lookPath = func(command string) (string, error) {
		if command == "codex" || command == "git" || command == "gh" {
			return filepath.Join("/tools", command), nil
		}
		return "", errors.New("not found")
	}
	requirement := config.CapabilityRequirement{ID: "codex/semantic", Type: config.CapabilityTypeMCPServer, Required: true}
	report := inspector.Inspect(t.Context(), InspectionRequest{Requirements: []config.CapabilityRequirement{requirement}})
	if report.RequiredMCPs != 1 || !capabilityAvailable(report.Snapshot.Capabilities, config.CapabilityTypeMCPServer, requirement.ID) {
		t.Fatalf("expected explicit Codex MCP requirement to be detected: %#v", report)
	}
}

func TestClaudeMCPRequiresExactNamedSuccessfulStatus(t *testing.T) {
	tests := map[string]struct {
		output string
		want   bool
	}{
		"connected":        {"semantic: command - ✓ Connected", true},
		"successful":       {"semantic: successful", true},
		"failed":           {"semantic: ✗ Failed to connect", false},
		"errored":          {"semantic: Error: connection refused", false},
		"disconnected":     {"semantic: Disconnected", false},
		"not connected":    {"semantic: not connected", false},
		"never connected":  {"semantic: never connected", false},
		"needs auth":       {"semantic: ! Needs authentication", false},
		"name only":        {"semantic: configured", false},
		"different server": {"semantic-extra: ✓ Connected", false},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := claudeMCPAvailable(test.output, "semantic"); got != test.want {
				t.Fatalf("claudeMCPAvailable(%q) = %t, want %t", test.output, got, test.want)
			}
		})
	}
	if claudeMCPAvailable("other: connected", "connected") {
		t.Fatal("status text was mistaken for the configured server name")
	}
}

func TestCapabilityInspectorFailsClosedWithoutGitHubAPIAuthentication(t *testing.T) {
	inspector := NewInspector(config.Config{}, unauthenticatedGitHubRunner{})
	inspector.lookPath = func(command string) (string, error) {
		return filepath.Join("/tools", command), nil
	}
	report := inspector.Inspect(t.Context(), InspectionRequest{})
	if report.GitHubAuth == nil || report.GitHubAuth.Status != CapabilityBlocked || report.Ready {
		t.Fatalf("unauthenticated GitHub API was not blocked: %#v", report)
	}
	found := false
	for _, recommendation := range report.Recommendations {
		found = found || strings.Contains(recommendation, "gh auth login")
	}
	if !found {
		t.Fatalf("missing GitHub authentication recommendation: %#v", report.Recommendations)
	}
}

func TestCapabilityInspectorAllowsDirtyProjectCheckout(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "uncommitted.txt"), []byte("local work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	inspection := NewInspector(config.Config{}, subprocess.OSRunner{}).inspectProject(t.Context(), repo)
	if inspection.Status != CapabilityAvailable || !strings.Contains(inspection.Detail, "left untouched") {
		t.Fatalf("dirty project checkout was not accepted as a repository source: %#v", inspection)
	}
}

func TestStandaloneCapabilityInspectorRequiresConfiguredGitRemote(t *testing.T) {
	repo := initGitRepo(t)
	cfg := config.Config{GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 1}}
	inspection := NewInspector(cfg, subprocess.OSRunner{}).inspectProject(t.Context(), repo)
	if inspection.Status != CapabilityBlocked || !strings.Contains(inspection.Detail, "remote") {
		t.Fatalf("repository without origin was reported as ready: %#v", inspection)
	}
}

func TestStandaloneCapabilityInspectorRejectsDifferentGitHubRemote(t *testing.T) {
	repo := initGitRepo(t)
	command := exec.Command("git", "remote", "add", "origin", "https://github.com/other/repo.git")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v\n%s", err, output)
	}
	base := strings.TrimSpace(runGitOutput(t, repo, "branch", "--show-current"))
	cfg := config.Config{GitHubProject: &config.GitHubProjectConfig{
		Owner: "owner", Number: 1, IntakeRepository: "owner/repo", BaseBranch: base, RemoteName: "origin",
	}}
	inspection := NewInspector(cfg, subprocess.OSRunner{}).inspectProject(t.Context(), repo)
	if inspection.Status != CapabilityBlocked || !strings.Contains(inspection.Detail, "does not match") {
		t.Fatalf("different GitHub remote was reported as ready: %#v", inspection)
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestStandaloneDoctorRequiresHarnessAssignedToEachRole(t *testing.T) {
	workflow := config.WorkflowTemplate(true)
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	planner := roles[config.WorkRolePlanner]
	planner.Harness = config.HarnessClaudeCLI
	roles[config.WorkRolePlanner] = planner
	reviewer := roles[config.WorkRoleReviewer]
	reviewer.Harness = config.HarnessClaudeCLI
	roles[config.WorkRoleReviewer] = reviewer
	cfg := config.Config{
		GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1}, Roles: roles, Workflow: &workflow,
	}
	ready, missing := roleHarnessReadiness(cfg, []HarnessInspection{
		{Kind: config.HarnessClaudeCLI, DisplayName: "Claude Code", Status: CapabilityAvailable, Ready: true},
		{Kind: config.HarnessCodexCLI, DisplayName: "Codex CLI", Ready: false},
	}, []CapabilityState{
		{ID: harnessSkillCapabilityID(config.HarnessClaudeCLI, "runner-planner"), Type: config.CapabilityTypeSkill, Status: CapabilityAvailable},
		{ID: harnessSkillCapabilityID(config.HarnessClaudeCLI, "runner-reviewer"), Type: config.CapabilityTypeSkill, Status: CapabilityAvailable},
	})
	if ready || len(missing) != 1 || missing[0].Role != config.WorkRoleImplementer || missing[0].Kind != config.HarnessCodexCLI {
		t.Fatalf("unexpected role harness readiness: ready=%v missing=%#v", ready, missing)
	}
}

func TestSkillInstallerInstallsTrustedSkillsWithoutOverwritingDifferences(t *testing.T) {
	home := t.TempDir()
	installer := NewSkillInstaller()
	installer.homeDir = func() (string, error) { return home, nil }
	workflow := config.WorkflowTemplate(true)
	cfg := config.Config{Roles: config.RoleTemplate(config.HarnessCodexCLI), Workflow: &workflow}
	results, err := installer.InstallConfigured(cfg, false)
	if err != nil || len(results) != len((bundledskills.EmbeddedCatalog{}).List()) {
		t.Fatalf("install skills: results=%#v error=%v", results, err)
	}
	path := filepath.Join(home, ".codex", "skills", "runner-implementer", "SKILL.md")
	if err := os.WriteFile(path, []byte("user-owned change\n"), 0o644); err != nil {
		t.Fatalf("change installed skill: %v", err)
	}
	if _, err := installer.InstallConfigured(cfg, false); err == nil {
		t.Fatal("expected setup to refuse a differing existing skill")
	} else if !errors.Is(err, ErrDifferingSkill) {
		t.Fatalf("expected differing-skill error, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "user-owned change\n" {
		t.Fatalf("refused setup changed existing skill: %q error=%v", content, err)
	}
	repaired, err := installer.InstallConfigured(cfg, true)
	if err != nil {
		t.Fatalf("repair differing skill: %v", err)
	}
	var repairedImplementer *SkillInstallResult
	for index := range repaired {
		if repaired[index].Skill == "runner-implementer" {
			repairedImplementer = &repaired[index]
			break
		}
	}
	if repairedImplementer == nil || repairedImplementer.Status != "replaced" {
		t.Fatalf("expected replaced implementer result, got %#v", repairedImplementer)
	}
	content, err = os.ReadFile(path)
	want, ok := (bundledskills.EmbeddedCatalog{}).Get("runner-implementer")
	if !ok {
		t.Fatal("bundled implementer skill is missing")
	}
	if err != nil || string(content) != string(want.Content) {
		t.Fatalf("repair did not restore bundled skill: error=%v", err)
	}
}

func TestSkillInstallerRepairsOnlyBundledSkillsAssignedToEachHarness(t *testing.T) {
	home := t.TempDir()
	installer := NewSkillInstaller()
	installer.homeDir = func() (string, error) { return home, nil }
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	implementer := roles[config.WorkRoleImplementer]
	implementer.Harness = config.HarnessClaudeCLI
	roles[config.WorkRoleImplementer] = implementer
	reviewer := roles[config.WorkRoleReviewer]
	reviewer.Harness = config.HarnessClaudeCLI
	roles[config.WorkRoleReviewer] = reviewer
	workflow := config.WorkflowTemplate(true)
	cfg := config.Config{Roles: roles, Workflow: &workflow}

	unselectedPath := filepath.Join(home, ".codex", "skills", "runner-reviewer", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(unselectedPath), 0o755); err != nil {
		t.Fatalf("create unselected skill directory: %v", err)
	}
	if err := os.WriteFile(unselectedPath, []byte("unselected local copy\n"), 0o644); err != nil {
		t.Fatalf("write unselected skill: %v", err)
	}

	results, err := installer.InstallConfigured(cfg, true)
	if err != nil {
		t.Fatalf("install configured skills: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("configured install results = %#v", results)
	}
	for _, result := range results {
		if result.Harness == config.HarnessCodexCLI && result.Skill != "runner-planner" {
			t.Fatalf("installed unassigned Codex skill: %#v", result)
		}
		if result.Harness == config.HarnessClaudeCLI && result.Skill == "runner-planner" {
			t.Fatalf("installed unassigned Claude skill: %#v", result)
		}
	}
	content, err := os.ReadFile(unselectedPath)
	if err != nil || string(content) != "unselected local copy\n" {
		t.Fatalf("changed unselected bundled skill copy: %q error=%v", content, err)
	}
}

func TestInspectorRequiresOnlyBundledSkillsUsedByRolesOnEachHarness(t *testing.T) {
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	implementer := roles[config.WorkRoleImplementer]
	implementer.Harness = config.HarnessClaudeCLI
	roles[config.WorkRoleImplementer] = implementer
	workflow := config.WorkflowTemplate(true)
	inspector := NewInspector(config.Config{Roles: roles, Workflow: &workflow, GitHubProject: &config.GitHubProjectConfig{}}, inspectorCommandRunner{})

	codexSkills := inspector.requiredBundledSkills(config.HarnessCodexCLI)
	claudeSkills := inspector.requiredBundledSkills(config.HarnessClaudeCLI)
	if got := skillIDs(codexSkills); !reflect.DeepEqual(got, []string{"runner-planner", "runner-reviewer"}) {
		t.Fatalf("Codex required skills = %v", got)
	}
	if got := skillIDs(claudeSkills); !reflect.DeepEqual(got, []string{"runner-implementer"}) {
		t.Fatalf("Claude required skills = %v", got)
	}
}

func TestSkillInstallerIncludesBundledSkillsInheritedByCustomWorkflowRoles(t *testing.T) {
	home := t.TempDir()
	installer := NewSkillInstaller()
	installer.homeDir = func() (string, error) { return home, nil }
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	roles["delivery"] = config.RoleConfig{Extends: config.WorkRoleImplementer, Harness: config.HarnessClaudeCLI}
	workflow := config.WorkflowTemplate(true)
	ready := workflow.Lanes["ready"]
	ready.Role = "delivery"
	workflow.Lanes["ready"] = ready
	cfg := config.Config{Roles: roles, Workflow: &workflow}

	results, err := installer.InstallConfigured(cfg, false)
	if err != nil {
		t.Fatalf("install custom workflow role skill: %v", err)
	}
	wantPath := filepath.Join(home, ".claude", "skills", "runner-implementer", "SKILL.md")
	found := false
	for _, result := range results {
		found = found || result.Path == wantPath
	}
	if !found {
		t.Fatalf("custom workflow role skill was not installed: %#v", results)
	}
}

func skillIDs(skills []bundledskills.Skill) []string {
	result := make([]string, 0, len(skills))
	for _, skill := range skills {
		result = append(result, skill.ID)
	}
	return result
}
