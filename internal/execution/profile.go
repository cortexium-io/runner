package execution

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	bundledskills "github.com/cortexium-io/runner/skills"
)

// RoleContract identifies a Runner-owned execution boundary. The role fixes
// the workspace and tool ceiling; configuration may explicitly choose whether
// that ceiling runs inside the harness sandbox or on the trusted host.
type RoleContract string

const (
	RolePlanner     RoleContract = "planner"
	RoleSynthesis   RoleContract = "synthesis"
	RoleReviewer    RoleContract = "reviewer"
	RoleProbe       RoleContract = "probe"
	RoleImplementer RoleContract = "implementer"
)

type WorkspaceClass string

const (
	WorkspaceNeutral  WorkspaceClass = "private_neutral"
	WorkspaceWorktree WorkspaceClass = "issue_worktree"
)

type RepositoryAccess string

const (
	RepositoryNone      RepositoryAccess = "none"
	RepositoryReadOnly  RepositoryAccess = "read_only"
	RepositoryReadWrite RepositoryAccess = "read_write"
)

type ToolClass string

const (
	ToolRepositoryRead ToolClass = "repository_read"
	ToolReadShell      ToolClass = "read_shell"
	ToolStructuredOut  ToolClass = "structured_output"
	ToolShell          ToolClass = "shell"
	ToolEdit           ToolClass = "edit"
)

type LocalResourcePolicy string

const (
	LocalResourcesHarnessSandbox LocalResourcePolicy = "harness_sandbox"
	LocalResourcesFullAccess     LocalResourcePolicy = "full_access"
)

type ApprovalBehavior string

const (
	ApprovalNever  ApprovalBehavior = "never"
	ApprovalBypass ApprovalBehavior = "bypass"
)

type SandboxRequirement string

const (
	SandboxReadOnly       SandboxRequirement = "read_only"
	SandboxWorkspaceWrite SandboxRequirement = "workspace_write"
	SandboxFullAccess     SandboxRequirement = "full_access"
)

// ExecutionProfile is the single capability representation consumed by every
// harness launch. Adapters may expose less than this ceiling, but never more.
type ExecutionProfile struct {
	Role            RoleContract
	Workspace       WorkspaceClass
	Repository      RepositoryAccess
	MutationAllowed bool
	Tools           []ToolClass
	LocalResources  LocalResourcePolicy
	Approval        ApprovalBehavior
	Sandbox         SandboxRequirement
}

var fixedProfiles = map[RoleContract]ExecutionProfile{
	RolePlanner: {
		Role: RolePlanner, Workspace: WorkspaceNeutral, Repository: RepositoryReadOnly,
		Tools:          []ToolClass{ToolRepositoryRead, ToolReadShell, ToolStructuredOut},
		LocalResources: LocalResourcesHarnessSandbox, Approval: ApprovalNever, Sandbox: SandboxReadOnly,
	},
	RoleSynthesis: {
		Role: RoleSynthesis, Workspace: WorkspaceNeutral, Repository: RepositoryNone,
		Tools: []ToolClass{ToolStructuredOut}, LocalResources: LocalResourcesHarnessSandbox,
		Approval: ApprovalNever, Sandbox: SandboxReadOnly,
	},
	RoleReviewer: {
		Role: RoleReviewer, Workspace: WorkspaceNeutral, Repository: RepositoryReadOnly,
		Tools: []ToolClass{ToolRepositoryRead, ToolReadShell, ToolStructuredOut},
		// Reviewers may write only to Runner's private neutral workspace. The
		// reviewed repository is outside that writable root and remains read-only.
		LocalResources: LocalResourcesHarnessSandbox, Approval: ApprovalNever, Sandbox: SandboxWorkspaceWrite,
	},
	RoleProbe: {
		Role: RoleProbe, Workspace: WorkspaceNeutral, Repository: RepositoryNone,
		Tools: []ToolClass{ToolStructuredOut}, LocalResources: LocalResourcesHarnessSandbox,
		Approval: ApprovalNever, Sandbox: SandboxReadOnly,
	},
	RoleImplementer: {
		Role: RoleImplementer, Workspace: WorkspaceWorktree, Repository: RepositoryReadWrite,
		MutationAllowed: true, Tools: []ToolClass{ToolRepositoryRead, ToolShell, ToolEdit, ToolStructuredOut},
		LocalResources: LocalResourcesHarnessSandbox, Approval: ApprovalNever, Sandbox: SandboxWorkspaceWrite,
	},
}

func ProfileForRole(role RoleContract, configuredAccess ...string) (ExecutionProfile, error) {
	profile, ok := fixedProfiles[role]
	if !ok {
		return ExecutionProfile{}, fmt.Errorf("unknown Runner execution role %q", role)
	}
	access := config.RoleAccessSandboxed
	if len(configuredAccess) > 0 {
		access = config.EffectiveRoleAccess(configuredAccess[0])
	}
	if !config.ValidRoleAccess(access) {
		return ExecutionProfile{}, fmt.Errorf("unknown Runner access mode %q", access)
	}
	if access == config.RoleAccessHost {
		if role == RolePlanner || role == RoleSynthesis || role == RoleProbe {
			return ExecutionProfile{}, fmt.Errorf("Runner %s never permits host access", role)
		}
		profile.LocalResources = LocalResourcesFullAccess
		profile.Approval = ApprovalBypass
		profile.Sandbox = SandboxFullAccess
	}
	if err := profile.validate(); err != nil {
		return ExecutionProfile{}, fmt.Errorf("invalid Runner execution profile %q: %w", role, err)
	}
	profile.Tools = append([]ToolClass(nil), profile.Tools...)
	return profile, nil
}

func (profile ExecutionProfile) validate() error {
	if !profile.allowsTool(ToolStructuredOut) {
		return errors.New("structured output channel is required")
	}
	if profile.MutationAllowed {
		if profile.Workspace != WorkspaceWorktree || profile.Repository != RepositoryReadWrite || !profile.allowsTool(ToolShell) || !profile.allowsTool(ToolEdit) {
			return errors.New("mutating roles require the issue worktree and shell/edit capabilities")
		}
		if !validAccessBoundary(profile, SandboxWorkspaceWrite) {
			return errors.New("mutating roles require either workspace-write isolation or explicit host access")
		}
		return nil
	}
	if profile.Role == RoleReviewer {
		if profile.Workspace != WorkspaceNeutral || profile.Repository != RepositoryReadOnly || !profile.allowsTool(ToolReadShell) || profile.allowsTool(ToolEdit) {
			return errors.New("reviewer roles require a neutral workspace, read-only repository access, and no declared edit capability")
		}
		if !validAccessBoundary(profile, SandboxWorkspaceWrite) {
			return errors.New("reviewer roles require either private scratch-workspace isolation or explicit host access")
		}
		return nil
	}
	if profile.Workspace != WorkspaceNeutral || profile.Repository == RepositoryReadWrite || profile.LocalResources != LocalResourcesHarnessSandbox || profile.Approval != ApprovalNever || profile.Sandbox != SandboxReadOnly || profile.allowsTool(ToolShell) || profile.allowsTool(ToolEdit) {
		return errors.New("read-only roles require a neutral workspace without mutating tools")
	}
	return nil
}

func validAccessBoundary(profile ExecutionProfile, isolatedSandbox SandboxRequirement) bool {
	if profile.Sandbox == isolatedSandbox {
		return profile.LocalResources == LocalResourcesHarnessSandbox && profile.Approval == ApprovalNever
	}
	return profile.Sandbox == SandboxFullAccess && profile.LocalResources == LocalResourcesFullAccess && profile.Approval == ApprovalBypass
}

// ValidateHarnessProfile verifies that the harness has an adapter for the
// Runner role and access boundary. Pi has no native OS sandbox for shell or
// edit tools, so its mutating/reviewer roles require an explicit host opt-in.
func ValidateHarnessProfile(kind string, role RoleContract, configuredAccess ...string) error {
	access := config.RoleAccessSandboxed
	if len(configuredAccess) > 0 {
		access = config.EffectiveRoleAccess(configuredAccess[0])
	}
	if _, err := ProfileForRole(role, access); err != nil {
		return err
	}
	switch kind {
	case config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI:
	default:
		return fmt.Errorf("unsupported harness %q", kind)
	}
	if kind == config.HarnessPiCLI && access != config.RoleAccessHost && (role == RoleImplementer || role == RoleReviewer) {
		return fmt.Errorf("Pi CLI cannot enforce %s in sandboxed mode; set this role's access to host only on a trusted machine or inside an external sandbox", role)
	}
	return nil
}

func (profile ExecutionProfile) allowsTool(class ToolClass) bool {
	for _, allowed := range profile.Tools {
		if allowed == class {
			return true
		}
	}
	return false
}

// profileWorkspace owns the process cwd and the only repository root conveyed
// to a non-implementation launch.
type profileWorkspace struct {
	Dir           string
	ReadRoot      string
	GitReadRoots  []string
	ToolReadPaths []string
	TempDir       string
	ToolPath      string
	cleanup       func() error
}

func prepareProfileWorkspace(profile ExecutionProfile, requestedRoot string, forbiddenRoots ...string) (profileWorkspace, error) {
	if profile.Workspace == WorkspaceWorktree {
		root, err := cleanExistingDirectory(requestedRoot)
		if err != nil {
			return profileWorkspace{}, err
		}
		tempDir, err := newProfileTempDir()
		if err != nil {
			return profileWorkspace{}, err
		}
		workspace := profileWorkspace{Dir: root, ReadRoot: root, TempDir: tempDir, cleanup: func() error { return os.RemoveAll(tempDir) }}
		if err := populateProfileWorkspacePaths(&workspace, root); err != nil {
			_ = workspace.cleanup()
			return profileWorkspace{}, err
		}
		return workspace, nil
	}
	dir, err := os.MkdirTemp("", "cortexium-runner-neutral-")
	if err != nil {
		return profileWorkspace{}, fmt.Errorf("create private neutral workspace: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return profileWorkspace{}, fmt.Errorf("protect private neutral workspace: %w", err)
	}
	for _, root := range append([]string{requestedRoot}, forbiddenRoots...) {
		if strings.TrimSpace(root) != "" && pathInsideOrEqual(resolvedExistingPath(dir), resolvedExistingPath(root)) {
			_ = os.RemoveAll(dir)
			return profileWorkspace{}, fmt.Errorf("private neutral workspace %s resolved inside protected repository or worktree root %s", dir, root)
		}
	}
	runtimeDir := filepath.Join(dir, "runtime")
	if err := os.Mkdir(runtimeDir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return profileWorkspace{}, fmt.Errorf("create private execution runtime directory: %w", err)
	}
	workspace := profileWorkspace{Dir: dir, TempDir: runtimeDir, cleanup: func() error { return os.RemoveAll(dir) }}
	if profile.Repository != RepositoryNone {
		workspace.ReadRoot, err = cleanExistingDirectory(requestedRoot)
		if err != nil {
			_ = workspace.cleanup()
			return profileWorkspace{}, fmt.Errorf("resolve profile repository read root: %w", err)
		}
		if err := populateProfileWorkspacePaths(&workspace, workspace.ReadRoot); err != nil {
			_ = workspace.cleanup()
			return profileWorkspace{}, err
		}
	}
	return workspace, nil
}

func populateProfileWorkspacePaths(workspace *profileWorkspace, repositoryRoot string) error {
	gitRoots, err := repositoryGitReadRoots(repositoryRoot)
	if err != nil {
		return fmt.Errorf("resolve repository Git metadata for sandbox: %w", err)
	}
	workspace.GitReadRoots = gitRoots
	workspace.ToolReadPaths = developmentToolReadPaths()
	if gitToolDir := macOSGitToolDirectory(); gitToolDir != "" {
		workspace.ToolReadPaths = minimalPathRoots(append(workspace.ToolReadPaths, gitToolDir))
	}
	workspace.ToolPath = developmentToolPath()
	return nil
}

func newProfileTempDir() (string, error) {
	directory, err := os.MkdirTemp("", "cortexium-runner-runtime-")
	if err != nil {
		return "", fmt.Errorf("create private execution runtime directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		_ = os.RemoveAll(directory)
		return "", fmt.Errorf("protect private execution runtime directory: %w", err)
	}
	return directory, nil
}

const maxGitPathFileBytes = 4 * 1024

// repositoryGitReadRoots returns only Git administration that lives outside
// the assigned checkout. Linked worktrees keep their index and common object
// store there, so read-only shell commands need this exact repository-owned
// path without gaining ambient access to the source checkout or operator home.
func repositoryGitReadRoots(repositoryRoot string) ([]string, error) {
	repositoryRoot = resolvedExistingPath(repositoryRoot)
	dotGit := filepath.Join(repositoryRoot, ".git")
	info, err := os.Lstat(dotGit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("repository .git path is a symbolic link")
	}

	gitDir := dotGit
	if !info.IsDir() {
		if !info.Mode().IsRegular() {
			return nil, errors.New("repository .git path is neither a directory nor a regular file")
		}
		value, readErr := readBoundedPathFile(dotGit, info)
		if readErr != nil {
			return nil, readErr
		}
		const prefix = "gitdir:"
		if !strings.HasPrefix(strings.ToLower(value), prefix) {
			return nil, errors.New("repository .git file does not contain a gitdir reference")
		}
		gitDir = strings.TrimSpace(value[len(prefix):])
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(repositoryRoot, gitDir)
		}
	}
	gitDir, err = cleanExistingDirectory(resolvedExistingPath(gitDir))
	if err != nil {
		return nil, fmt.Errorf("resolve Git directory: %w", err)
	}
	if pathInsideOrEqual(gitDir, repositoryRoot) {
		return nil, nil
	}

	commonDir := gitDir
	commonPath := filepath.Join(gitDir, "commondir")
	commonInfo, commonErr := os.Lstat(commonPath)
	if commonErr == nil {
		if commonInfo.Mode()&os.ModeSymlink != 0 || !commonInfo.Mode().IsRegular() {
			return nil, errors.New("linked-worktree commondir is not a regular file")
		}
		value, readErr := readBoundedPathFile(commonPath, commonInfo)
		if readErr != nil {
			return nil, readErr
		}
		commonDir = strings.TrimSpace(value)
		if !filepath.IsAbs(commonDir) {
			commonDir = filepath.Join(gitDir, commonDir)
		}
		commonDir, err = cleanExistingDirectory(resolvedExistingPath(commonDir))
		if err != nil {
			return nil, fmt.Errorf("resolve linked-worktree common Git directory: %w", err)
		}
	} else if !errors.Is(commonErr, os.ErrNotExist) {
		return nil, commonErr
	}

	expectedWorktreesRoot := filepath.Join(commonDir, "worktrees")
	if filepath.Base(commonDir) != ".git" || !pathInsideOrEqual(gitDir, expectedWorktreesRoot) {
		return nil, errors.New("external Git metadata is not a standard linked-worktree directory")
	}
	return minimalPathRoots([]string{gitDir, commonDir}), nil
}

func readBoundedPathFile(path string, info os.FileInfo) (string, error) {
	if info.Size() <= 0 || info.Size() > maxGitPathFileBytes {
		return "", fmt.Errorf("%s exceeds the Git path-file size limit", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func developmentToolReadPaths() []string {
	paths := developmentToolReadPathsWith(exec.LookPath, filepath.EvalSymlinks)
	return minimalPathRoots(append(paths, homebrewRuntimeReadPaths(paths)...))
}

// codexHelperReadPaths grants only the installed Codex launch directory and
// its standalone package tree. Codex's built-in patch helper re-executes the
// current CLI binary; without these read-only paths, sandboxed implementers can
// run shell tools but cannot apply ordinary workspace patches.
func codexHelperReadPaths(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	path, err := exec.LookPath(command)
	if err != nil || strings.TrimSpace(path) == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return nil
		}
	}
	path = filepath.Clean(path)
	paths := []string{filepath.Dir(path)}
	if target, readErr := os.Readlink(path); readErr == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		if root := codexStandaloneRoot(filepath.Clean(target)); root != "" {
			paths = append(paths, root)
		}
	}
	if resolved, resolveErr := filepath.EvalSymlinks(path); resolveErr == nil && filepath.IsAbs(resolved) {
		paths = append(paths, filepath.Dir(filepath.Clean(resolved)))
		if root := codexStandaloneRoot(filepath.Clean(resolved)); root != "" {
			paths = append(paths, root)
		}
	}
	return minimalPathRoots(paths)
}

func codexStandaloneRoot(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for index := 0; index+2 < len(parts); index++ {
		if parts[index] == ".codex" && parts[index+1] == "packages" && parts[index+2] == "standalone" {
			prefix := strings.Join(parts[:index+3], string(filepath.Separator))
			if filepath.IsAbs(path) {
				prefix = string(filepath.Separator) + strings.TrimPrefix(prefix, string(filepath.Separator))
			}
			return filepath.Clean(prefix)
		}
	}
	return ""
}

func developmentToolReadPathsWith(lookPath func(string) (string, error), evalSymlinks func(string) (string, error)) []string {
	paths := make([]string, 0, 9)
	for _, tool := range []string{"node", "npm", "npx"} {
		found, err := lookPath(tool)
		if err != nil || strings.TrimSpace(found) == "" {
			continue
		}
		if !filepath.IsAbs(found) {
			found, err = filepath.Abs(found)
			if err != nil {
				continue
			}
		}
		found = filepath.Clean(found)
		paths = append(paths, filepath.Dir(found))
		resolved := found
		if candidate, resolveErr := evalSymlinks(found); resolveErr == nil && filepath.IsAbs(candidate) {
			resolved = filepath.Clean(candidate)
			paths = append(paths, resolved)
		}
		if root := developmentToolRuntimeRoot(resolved); root != "" {
			paths = append(paths, root)
		}
	}
	return minimalPathRoots(paths)
}

func homebrewRuntimeReadPaths(paths []string) []string {
	result := []string{}
	seenPrefixes := map[string]bool{}
	for _, path := range paths {
		separator := string(filepath.Separator) + "Cellar" + string(filepath.Separator)
		index := strings.Index(path, separator)
		if index <= 0 {
			continue
		}
		prefix := filepath.Clean(path[:index])
		if seenPrefixes[prefix] {
			continue
		}
		seenPrefixes[prefix] = true
		for _, directory := range []string{"bin", "Cellar", "opt"} {
			candidate := filepath.Join(prefix, directory)
			if info, err := os.Stat(candidate); err == nil && info.IsDir() {
				result = append(result, candidate)
			}
		}
		matches, _ := filepath.Glob(filepath.Join(prefix, "etc", "openssl*", "openssl.cnf"))
		for _, candidate := range matches {
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				result = append(result, candidate)
			}
		}
	}
	return result
}

func developmentToolPath() string {
	directories := []string{macOSGitToolDirectory()}
	for _, tool := range []string{"node", "npm", "npx"} {
		if path, err := exec.LookPath(tool); err == nil && filepath.IsAbs(path) {
			directories = append(directories, filepath.Dir(filepath.Clean(path)))
		}
	}
	directories = append(directories, "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	seen := map[string]bool{}
	result := make([]string, 0, len(directories))
	for _, directory := range directories {
		if directory == "" || seen[directory] {
			continue
		}
		seen[directory] = true
		result = append(result, directory)
	}
	return strings.Join(result, string(os.PathListSeparator))
}

func macOSGitToolDirectory() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	result, err := exec.Command("/usr/bin/xcrun", "--find", "git").Output()
	if err != nil {
		return ""
	}
	gitPath := strings.TrimSpace(string(result))
	if !filepath.IsAbs(gitPath) {
		return ""
	}
	return filepath.Dir(filepath.Clean(gitPath))
}

func developmentToolRuntimeRoot(path string) string {
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		if filepath.Base(directory) == "bin" {
			root := filepath.Dir(directory)
			switch root {
			case string(filepath.Separator), "/usr", "/System", "/bin", "/sbin":
				return ""
			default:
				return filepath.Clean(root)
			}
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return ""
		}
	}
}

func minimalPathRoots(paths []string) []string {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			continue
		}
		unique[filepath.Clean(path)] = struct{}{}
	}
	ordered := make([]string, 0, len(unique))
	for path := range unique {
		ordered = append(ordered, path)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if len(ordered[i]) == len(ordered[j]) {
			return ordered[i] < ordered[j]
		}
		return len(ordered[i]) < len(ordered[j])
	})
	result := make([]string, 0, len(ordered))
	for _, candidate := range ordered {
		covered := false
		for _, root := range result {
			if pathInsideOrEqual(candidate, root) {
				covered = true
				break
			}
		}
		if !covered {
			result = append(result, candidate)
		}
	}
	sort.Strings(result)
	return result
}

func resolvedExistingPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(resolved)
}

func cleanExistingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("execution profile requires an explicit workspace root")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", absolute)
	}
	return filepath.Clean(absolute), nil
}

func profileRepositoryInstruction(workspace profileWorkspace) string {
	if workspace.ReadRoot == "" || workspace.ReadRoot == workspace.Dir {
		return ""
	}
	return "\n\nRunner-approved read-only repository root: " + workspace.ReadRoot +
		"\nInspect only that root. The process current directory is a private neutral Runner workspace."
}

func trustedSkillInstructions(cfg config.ExecutionConfig) string {
	if len(cfg.Skills) == 0 {
		return ""
	}
	catalog := bundledskills.EmbeddedCatalog{}
	var builder strings.Builder
	for _, id := range cfg.Skills {
		skill, ok := catalog.Get(strings.TrimSpace(id))
		if !ok {
			// Validated runtime config cannot reach this branch. Keep direct
			// package callers fail-safe by never loading an unpinned local file.
			continue
		}
		builder.WriteString("\n\nRunner-pinned skill ")
		builder.WriteString(skill.ID)
		builder.WriteString(":\n--- BEGIN RUNNER-PINNED SKILL ---\n")
		builder.Write(skill.Content)
		builder.WriteString("\n--- END RUNNER-PINNED SKILL ---")
	}
	return builder.String()
}
