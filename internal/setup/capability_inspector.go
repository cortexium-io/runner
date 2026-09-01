package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	bundledskills "github.com/cortexium-io/runner/skills"
)

// chrome-devtools-mcp's allowed URL patterns are the browser-level boundary
// that keeps Runner's built-in browser on loopback. Chrome added the required
// support in major version 149.
const minimumRunnerBrowserChromeMajor = 149

type InspectionRequest struct {
	CheckedAt    time.Time
	ProjectDir   string
	Requirements []config.CapabilityRequirement
}

type InspectionReport struct {
	Ready            bool                        `json:"ready"`
	Snapshot         CapabilitySnapshot          `json:"capability_snapshot"`
	GitHubAuth       *GitHubAuthInspection       `json:"github_auth,omitempty"`
	Harnesses        []HarnessInspection         `json:"harnesses"`
	Project          *ProjectInspection          `json:"project,omitempty"`
	GitHubRepository *GitHubRepositoryInspection `json:"github_repository,omitempty"`
	GitHubProject    *github.ProjectInspection   `json:"github_project,omitempty"`
	RequiredMCPs     int                         `json:"required_mcps"`
	Warnings         []string                    `json:"warnings,omitempty"`
	Recommendations  []string                    `json:"recommendations,omitempty"`
}

type GitHubAuthInspection struct {
	Status string `json:"status"`
	Login  string `json:"login,omitempty"`
	Detail string `json:"detail"`
}

type HarnessInspection struct {
	Kind            string `json:"kind"`
	DisplayName     string `json:"display_name"`
	Command         string `json:"command"`
	ExecutionPolicy string `json:"execution_policy,omitempty"`
	Path            string `json:"path,omitempty"`
	Version         string `json:"version,omitempty"`
	Status          string `json:"status"`
	SkillsReady     bool   `json:"skills_ready"`
	Ready           bool   `json:"ready"`
	Authentication  string `json:"authentication"`
	Detail          string `json:"detail"`
}

type ProjectInspection struct {
	Path           string `json:"path"`
	RepositoryRoot string `json:"repository_root,omitempty"`
	Status         string `json:"status"`
	Detail         string `json:"detail"`
}

type Inspector struct {
	cfg      config.Config
	run      subprocess.Runner
	lookPath func(string) (string, error)
	homeDir  func() (string, error)
	catalog  bundledskills.Catalog
}

func NewInspector(cfg config.Config, run subprocess.Runner) *Inspector {
	if run == nil {
		run = subprocess.OSRunner{}
	}
	return &Inspector{
		cfg: cfg, run: run, lookPath: exec.LookPath, homeDir: os.UserHomeDir, catalog: bundledskills.EmbeddedCatalog{},
	}
}

func (i *Inspector) Inspect(ctx context.Context, request InspectionRequest) InspectionReport {
	checkedAt := request.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now()
	}
	checkedAt = checkedAt.UTC()
	capabilities := []CapabilityState{}
	warnings := []string{}

	for _, command := range []string{"git", "gh"} {
		capabilities = upsertCapability(capabilities, i.inspectTool(ctx, command))
	}
	githubAuth := i.inspectGitHubAuth(ctx, capabilities)
	capabilities = upsertCapability(capabilities, CapabilityState{
		ID: "github_api", Type: config.CapabilityTypeProfile, Status: githubAuth.Status, Detail: stringPtr(githubAuth.Detail),
	})
	for _, requirement := range request.Requirements {
		if requirement.Type == config.CapabilityTypeLocalTool && !isHarnessKind(requirement.ID) {
			if hasCapabilityState(capabilities, requirement.Type, requirement.ID) {
				continue
			}
			capabilities = upsertCapability(capabilities, i.inspectTool(ctx, requirement.ID))
		}
	}

	home, homeErr := i.homeDir()
	if homeErr != nil {
		warnings = append(warnings, "cannot resolve the user home directory: "+homeErr.Error())
	}
	descriptors := defaultHarnessDescriptors(home, i.cfg.Harnesses)
	harnessReports := make([]HarnessInspection, 0, len(descriptors))
	readyHarnesses := 0
	installedHarnesses := 0
	standaloneHarnesses := map[string]bool{}
	if i.cfg.HasProject() {
		for _, kind := range i.cfg.ConfiguredRoleHarnesses() {
			standaloneHarnesses[kind] = true
		}
	}
	for _, descriptor := range descriptors {
		if i.cfg.HasProject() && !standaloneHarnesses[descriptor.Kind] {
			continue
		}
		if !harnessEnabled(descriptor.Kind, i.cfg.Harnesses) {
			continue
		}
		harness, harnessCapabilities := i.inspectHarness(ctx, descriptor, homeErr, i.requiredBundledSkills(descriptor.Kind))
		for _, capability := range harnessCapabilities {
			capabilities = upsertCapability(capabilities, capability)
		}
		if harness.Ready {
			readyHarnesses++
		}
		if harness.Status == CapabilityAvailable {
			installedHarnesses++
		}
		harnessReports = append(harnessReports, harness)
	}
	requiredMCPs := 0
	for _, requirement := range request.Requirements {
		if requirement.Type != config.CapabilityTypeMCPServer {
			continue
		}
		requiredMCPs++
		capabilities = upsertCapability(capabilities, i.inspectMCP(ctx, descriptors, requirement.ID))
	}

	var project *ProjectInspection
	projectReady := true
	projectDir := strings.TrimSpace(request.ProjectDir)
	if projectDir != "" {
		project = i.inspectProject(ctx, projectDir)
		projectReady = project.Status == CapabilityAvailable
		capabilities = upsertCapability(capabilities, CapabilityState{
			ID: "git_repository", Type: config.CapabilityTypeProfile, Status: project.Status,
			Detail: stringPtr(project.Detail),
		})
	}

	missing := missingRequiredCapabilities(capabilities, request.Requirements)
	coreReady := capabilityAvailable(capabilities, config.CapabilityTypeLocalTool, "git") &&
		capabilityAvailable(capabilities, config.CapabilityTypeLocalTool, "gh") && githubAuth.Status == CapabilityAvailable
	roleHarnessesReady, missingRoleHarnesses := roleHarnessReadiness(i.cfg, harnessReports, capabilities)
	sourceReady := true
	repositoryReady := true
	var githubRepository *GitHubRepositoryInspection
	if i.cfg.HasProject() && githubAuth.Status == CapabilityAvailable {
		githubRepository = i.inspectGitHubRepository(ctx)
		repositoryReady = githubRepository.Status == CapabilityAvailable
		capabilities = upsertCapability(capabilities, CapabilityState{
			ID: "github_repository", Type: config.CapabilityTypeProfile, Status: githubRepository.Status, Detail: stringPtr(githubRepository.Detail),
		})
		if githubRepository.AutoMergeRequested && githubRepository.ClassicProtection && !githubRepository.ProtectionDetailsKnown {
			warnings = append(warnings, fmt.Sprintf("base branch %s/%s is protected, but the GitHub account cannot inspect classic protection details; verify that its merge rules allow %s", githubRepository.Repository, githubRepository.BaseBranch, githubRepository.MergeMethod))
		}
	}
	var githubProject *github.ProjectInspection
	if i.cfg.HasProject() && i.cfg.GitHubProject != nil {
		inspection, err := github.NewProject(i.cfg.ResolveProject(), i.run).Inspect(ctx)
		if err != nil {
			sourceReady = false
			warnings = append(warnings, "GitHub Project is unavailable: "+err.Error())
			capabilities = upsertCapability(capabilities, CapabilityState{ID: config.GitHubProjectCapabilityID, Type: config.CapabilityTypeProfile, Status: CapabilityBlocked, Detail: stringPtr(err.Error())})
		} else {
			githubProject = &inspection
			sourceReady = inspection.BoardView && inspection.StatusField && inspection.WorkflowStatuses && inspection.ApprovalField && inspection.PhaseField && inspection.TransitionField && inspection.ActivityField && inspection.QAFailuresField && inspection.BranchField && inspection.PullRequestField && inspection.QACommitField && inspection.IntakeRepository && inspection.IntakeLabel
			status := CapabilityAvailable
			detail := "GitHub Project is readable and has a Kanban board with the configured Status options"
			if !sourceReady {
				status = CapabilityBlocked
				detail = "GitHub Project or public issue intake is missing a Kanban board, configured field, status, repository, or label"
			}
			capabilities = upsertCapability(capabilities, CapabilityState{ID: config.GitHubProjectCapabilityID, Type: config.CapabilityTypeProfile, Status: status, Detail: stringPtr(detail)})
			if !inspection.ResultField {
				warnings = append(warnings, "Runner Result field is absent; result summaries will remain in runner output only")
			}
			if !inspection.ApprovalField {
				warnings = append(warnings, fmt.Sprintf("%s field is absent; no Project item can receive execution authority", i.cfg.GitHubProject.ApprovalFieldName()))
			}
		}
	}
	harnessesReady := readyHarnesses > 0
	if i.cfg.HasProject() {
		harnessesReady = installedHarnesses > 0 && roleHarnessesReady
	}
	ready := coreReady && harnessesReady && projectReady && repositoryReady && sourceReady && len(missing) == 0
	sort.Slice(capabilities, func(a, b int) bool {
		if capabilities[a].Type == capabilities[b].Type {
			return capabilities[a].ID < capabilities[b].ID
		}
		return capabilities[a].Type < capabilities[b].Type
	})
	recommendations := doctorRecommendations(capabilities, harnessReports, missing, sourceReady, projectReady, false)
	for _, missingHarness := range missingRoleHarnesses {
		recommendations = append(recommendations, fmt.Sprintf("Install and set up %s with skill %q for the %s role, or select another supported harness for that role.", missingHarness.DisplayName, missingHarness.Skill, missingHarness.Role))
	}
	if githubRepository != nil && githubRepository.Recommendation != "" {
		recommendations = append(recommendations, githubRepository.Recommendation)
	}
	return InspectionReport{
		Ready:      ready,
		Snapshot:   CapabilitySnapshot{RunnerID: i.cfg.RunnerID, CheckedAt: checkedAt, Capabilities: capabilities, MissingCapabilities: missing},
		GitHubAuth: githubAuth, Harnesses: harnessReports, Project: project, GitHubRepository: githubRepository, GitHubProject: githubProject, RequiredMCPs: requiredMCPs, Warnings: warnings, Recommendations: recommendations,
	}
}

func (i *Inspector) inspectGitHubAuth(ctx context.Context, capabilities []CapabilityState) *GitHubAuthInspection {
	report := &GitHubAuthInspection{Status: CapabilityMissing, Detail: "GitHub CLI is unavailable"}
	if !capabilityAvailable(capabilities, config.CapabilityTypeLocalTool, "gh") {
		return report
	}
	path, err := i.lookPath("gh")
	if err != nil {
		return report
	}
	if _, err := subprocess.RunFailClosed(ctx, i.run, path, []string{"auth", "status", "--hostname", "github.com"}, "", 10*time.Second, subprocess.GitHubStdoutLimit, subprocess.DiagnosticStderrLimit); err != nil {
		report.Status = CapabilityBlocked
		report.Detail = "GitHub CLI is not authenticated for github.com; run `gh auth login`"
		return report
	}
	result, err := subprocess.RunFailClosed(ctx, i.run, path, []string{"api", "user", "--jq", ".login"}, "", 10*time.Second, subprocess.GitHubStdoutLimit, subprocess.DiagnosticStderrLimit)
	if err != nil || strings.TrimSpace(result.Stdout) == "" {
		report.Status = CapabilityBlocked
		report.Detail = "GitHub CLI authentication cannot access the GitHub API"
		return report
	}
	report.Status = CapabilityAvailable
	report.Login = firstNonEmptyLine(result.Stdout)
	report.Detail = "GitHub API access is ready as " + report.Login
	return report
}

func (i *Inspector) inspectTool(ctx context.Context, command string) CapabilityState {
	command = strings.TrimSpace(command)
	capability := CapabilityState{ID: command, Type: config.CapabilityTypeLocalTool, Status: CapabilityMissing}
	if command == "" || strings.ContainsAny(command, `/\\`) {
		capability.Status = CapabilityBlocked
		capability.Detail = stringPtr("tool id must be a command name without path separators")
		return capability
	}
	if command == "chrome" {
		return i.inspectChrome(ctx)
	}
	path, err := i.lookPath(command)
	if err != nil {
		capability.Detail = stringPtr(command + " executable not found in PATH")
		return capability
	}
	capability.Status = CapabilityAvailable
	capability.Detail = stringPtr("executable found at " + path)
	if args := knownVersionArgs(command); len(args) > 0 {
		var result subprocess.Result
		var runErr error
		switch command {
		case "git":
			result, runErr = subprocess.RunFailClosed(ctx, i.run, path, args, "", 5*time.Second, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
		case "gh":
			result, runErr = subprocess.RunFailClosed(ctx, i.run, path, args, "", 5*time.Second, subprocess.GitHubStdoutLimit, subprocess.DiagnosticStderrLimit)
		default:
			result, runErr = i.run.Run(ctx, path, args, "", 5*time.Second)
		}
		if runErr == nil {
			version := firstNonEmptyLine(result.Stdout, result.Stderr)
			if version != "" {
				capability.Version = stringPtr(version)
			}
		}
	}
	return capability
}

func (i *Inspector) inspectChrome(ctx context.Context) CapabilityState {
	capability := CapabilityState{ID: "chrome", Type: config.CapabilityTypeLocalTool, Status: CapabilityMissing}
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, command := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if path, err := i.lookPath(command); err == nil {
			candidates = append(candidates, path)
		}
	}
	for _, path := range candidates {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		capability.Status = CapabilityAvailable
		capability.Detail = stringPtr("isolated headless browser executable found at " + path)
		result, err := i.run.Run(ctx, path, []string{"--version"}, "", 5*time.Second)
		if err != nil {
			capability.Status = CapabilityBlocked
			capability.Detail = stringPtr("browser executable was found, but Runner could not determine its version")
			return capability
		}
		version := firstNonEmptyLine(result.Stdout, result.Stderr)
		major, ok := browserMajorVersion(version)
		if !ok {
			capability.Status = CapabilityBlocked
			capability.Detail = stringPtr("browser executable was found, but its major version could not be determined")
			return capability
		}
		capability.Version = stringPtr(version)
		if major < minimumRunnerBrowserChromeMajor {
			capability.Status = CapabilityBlocked
			capability.Detail = stringPtr(fmt.Sprintf("Chrome or Chromium %d+ is required for Runner's loopback-only browser; found major version %d", minimumRunnerBrowserChromeMajor, major))
		}
		return capability
	}
	capability.Detail = stringPtr("compatible Chrome or Chromium executable not found")
	return capability
}

func browserMajorVersion(version string) (int, bool) {
	for _, field := range strings.Fields(version) {
		candidate := strings.TrimLeft(field, "vV")
		majorText, _, _ := strings.Cut(candidate, ".")
		major, err := strconv.Atoi(majorText)
		if err == nil && major > 0 {
			return major, true
		}
	}
	return 0, false
}

func (i *Inspector) inspectProject(ctx context.Context, projectDir string) *ProjectInspection {
	absolute, err := filepath.Abs(projectDir)
	if err != nil {
		return &ProjectInspection{Path: projectDir, Status: CapabilityBlocked, Detail: "cannot resolve project directory: " + err.Error()}
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return &ProjectInspection{Path: absolute, Status: CapabilityMissing, Detail: "project directory is unavailable"}
	}
	gitPath, err := i.lookPath("git")
	if err != nil {
		return &ProjectInspection{Path: absolute, Status: CapabilityMissing, Detail: "git is unavailable"}
	}
	result, err := subprocess.RunFailClosed(ctx, i.run, gitPath, []string{"-C", absolute, "rev-parse", "--show-toplevel"}, "", 5*time.Second, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
	if err != nil {
		return &ProjectInspection{Path: absolute, Status: CapabilityBlocked, Detail: "project directory is not inside a Git repository"}
	}
	root := strings.TrimSpace(result.Stdout)
	if i.cfg.HasProject() && i.cfg.GitHubProject != nil {
		remote := strings.TrimSpace(i.cfg.GitHubProject.RemoteName)
		base := strings.TrimSpace(i.cfg.GitHubProject.BaseBranch)
		if remote == "" || base == "" {
			return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: "github_project.remote_name and github_project.base_branch are required to verify the Git workspace"}
		}
		expectedRepository := strings.TrimSpace(i.cfg.GitHubProject.IntakeRepository)
		if expectedRepository == "" {
			return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: "github_project.intake_repository is required to verify Git remote identity"}
		}
		remoteURL, err := subprocess.RunFailClosed(ctx, i.run, gitPath, []string{"-C", root, "config", "--get", "remote." + remote + ".url"}, "", 10*time.Second, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
		if err != nil {
			return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: fmt.Sprintf("configured Git remote %q is unavailable", remote)}
		}
		remoteRepository, err := github.RepositoryFromRemote(remoteURL.Stdout)
		if err != nil {
			return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: fmt.Sprintf("configured Git remote %q is not a supported GitHub repository", remote)}
		}
		if !strings.EqualFold(remoteRepository, expectedRepository) {
			return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: fmt.Sprintf("configured repository %q does not match Git remote %q", expectedRepository, remoteRepository)}
		}
		remoteBase, remoteBaseErr := subprocess.RunFailClosed(ctx, i.run, gitPath, []string{"-C", root, "rev-parse", "--verify", "refs/remotes/" + remote + "/" + base}, "", 10*time.Second, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
		if remoteBaseErr != nil || strings.TrimSpace(remoteBase.Stdout) == "" {
			localBase, localBaseErr := subprocess.RunFailClosed(ctx, i.run, gitPath, []string{"-C", root, "rev-parse", "--verify", "refs/heads/" + base}, "", 10*time.Second, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
			if localBaseErr != nil || strings.TrimSpace(localBase.Stdout) == "" {
				return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityBlocked, Detail: fmt.Sprintf("configured base branch %q is unavailable locally; fetch %s/%s before running", base, remote, base)}
			}
		}
	}
	return &ProjectInspection{Path: absolute, RepositoryRoot: root, Status: CapabilityAvailable, Detail: "Git repository is ready to create task-scoped worktrees; the project checkout is left untouched"}
}

func knownVersionArgs(command string) []string {
	switch command {
	case "git", "gh", "go", "node", "npm", "npx", "rg":
		return []string{"--version"}
	default:
		return nil
	}
}

func firstNonEmptyLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return truncate(trimmed, 240)
			}
		}
	}
	return ""
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func capabilityAvailable(capabilities []CapabilityState, capabilityType, id string) bool {
	for _, capability := range capabilities {
		if capability.Type == capabilityType && capability.ID == id {
			return capability.Status == CapabilityAvailable
		}
	}
	return false
}

func hasCapabilityState(capabilities []CapabilityState, capabilityType, id string) bool {
	for _, capability := range capabilities {
		if capability.Type == capabilityType && capability.ID == id {
			return true
		}
	}
	return false
}

func isHarnessKind(id string) bool { return config.ValidHarnessKind(strings.TrimSpace(id)) }
