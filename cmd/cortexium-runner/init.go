package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
	"github.com/cortexium-io/runner/internal/setup"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func runInit(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlagSet("init", "cortexium-runner init [--config PATH] [--dry-run] [--prune] [options]", stdout)
	configPath := flags.String("config", "", "operator config path; interactive setup defaults to PROJECT/"+defaultRunnerConfigPath)
	owner := flags.String("owner", "", "GitHub Project owner")
	projectNumber := flags.Int("project-number", 0, "GitHub Project number")
	createProject := flags.String("create-project", "", "create and configure a GitHub Project with this title")
	repository := flags.String("repository", "", "public issue intake repository in owner/repository form; when omitted, derive it from the current Git remote")
	intakeLabel := flags.String("intake-label", "needs-assessment", "label that routes public issues into assessment")
	projectVisibility := flags.String("project-visibility", "", "new Project visibility: public or private")
	projectDir := flags.String("project-dir", ".", "local Git checkout used by draft and unmapped items")
	maxParallelism := flags.Int("max-parallelism", 1, "maximum independent cards Runner may execute concurrently (1-16)")
	admissionWindow := flags.Duration("admission-window", 0, "optional rolling window for agent admission ceilings (for example 24h)")
	maxAdmissionAttempts := flags.Int("max-admission-attempts", 0, "maximum agent attempts started in the admission window")
	maxAdmissionHarnessTime := flags.Duration("max-admission-harness-time", 0, "maximum completed harness time in the admission window")
	maxAdmissionTokens := flags.Int64("max-admission-tokens", 0, "maximum harness-reported tokens in the admission window")
	maxAdmissionCostUSD := flags.Float64("max-admission-cost-usd", 0, "maximum harness-reported cost in USD in the admission window")
	qaRejectLimit := 3
	flags.IntVar(&qaRejectLimit, "qa-reject-limit", 3, "consecutive agent QA rejections allowed before human attention is required")
	baseBranch := flags.String("base-branch", "main", "pull request target branch")
	remoteName := flags.String("remote", "origin", "Git remote used for publication")
	autoMerge := flags.Bool("auto-merge", false, "ask GitHub to merge pull requests automatically after its requirements pass")
	mergeMethod := flags.String("merge-method", config.MergeMethodMerge, "automatic pull request merge method: merge, rebase, or squash")
	bootstrapBaseBranch := flags.Bool("bootstrap-base-branch", false, "push the local base branch or initialize it when the remote repository is empty")
	defaultHarness := flags.String("harness", "", "default harness for all roles; overridden by a role-specific harness flag")
	plannerHarness := flags.String("planner-harness", "", "planner harness; inferred only when exactly one supported harness is available")
	implementerHarness := flags.String("implementer-harness", "", "implementer harness; inferred only when exactly one supported harness is available")
	reviewerHarness := flags.String("reviewer-harness", "", "reviewer harness; inferred only when exactly one supported harness is available")
	plannerAccess := flags.String("planner-access", "", "planner access: sandboxed (default) or host")
	implementerAccess := flags.String("implementer-access", "", "implementer access: sandboxed (default) or host")
	reviewerAccess := flags.String("reviewer-access", "", "reviewer access: sandboxed (default) or host")
	defaultHarnessConfig := flags.String("harness-config", "", "default harness configuration: isolated (default) or inherit")
	plannerHarnessConfig := flags.String("planner-harness-config", "", "planner harness configuration: isolated or inherit")
	implementerHarnessConfig := flags.String("implementer-harness-config", "", "implementer harness configuration: isolated or inherit")
	reviewerHarnessConfig := flags.String("reviewer-harness-config", "", "reviewer harness configuration: isolated or inherit")
	defaultPlanningSupport := flags.String("planning-support", "", "default downstream task sizing: standard (regular) or high (small)")
	implementerPlanningSupport := flags.String("implementer-planning-support", "", "implementer task sizing: standard (regular) or high (small)")
	reviewerPlanningSupport := flags.String("reviewer-planning-support", "", "reviewer task sizing: standard (regular) or high (small)")
	defaultModel := flags.String("model", "", "optional default model for all roles; overridden by a role-specific model flag")
	plannerModel := flags.String("planner-model", "", "optional planner model override")
	implementerModel := flags.String("implementer-model", "", "optional implementer model override")
	reviewerModel := flags.String("reviewer-model", "", "optional reviewer model override")
	defaultReasoning := flags.String("reasoning", "", "default reasoning effort for all roles: low, medium, high, or xhigh; Pi also supports off")
	plannerReasoning := flags.String("planner-reasoning", "", "planner reasoning effort: low, medium, high, or xhigh; Pi also supports off")
	implementerReasoning := flags.String("implementer-reasoning", "", "implementer reasoning effort: low, medium, high, or xhigh; Pi also supports off")
	reviewerReasoning := flags.String("reviewer-reasoning", "", "reviewer reasoning effort: low, medium, high, or xhigh; Pi also supports off")
	baseUpdateReview := flags.String("base-update-review", "", "required review policy after an automatic base update: required")
	dryRun := flags.Bool("dry-run", false, "preview local and GitHub changes without applying them")
	interactiveFlag := flags.Bool("interactive", false, "prompt for missing initialization choices even when input is not a terminal")
	nonInteractiveFlag := flags.Bool("non-interactive", false, "never prompt; report every missing required choice")
	prune := flags.Bool("prune", false, "remove extra empty GitHub Project Status options while synchronizing")
	force := flags.Bool("force", false, "replace differing Runner-managed skill files")
	proceed, err := parseFlags(flags, args, "init")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init does not accept positional arguments")
	}
	interactive, err := initInteractiveMode(stdin, stdout, *interactiveFlag, *nonInteractiveFlag)
	if err != nil {
		return err
	}
	var prompter *initPrompter
	if interactive {
		prompter = newInitPrompter(stdin, stdout)
	}
	if strings.TrimSpace(*configPath) == "" {
		if prompter == nil {
			return errors.New("non-interactive init requires an explicit --config path")
		}
		suggested := suggestedInitConfigPath(ctx, *projectDir)
		if suggested == "" {
			*configPath, err = prompter.required("Runner config path")
		} else {
			*configPath, err = prompter.withDefault("Runner config path", suggested)
		}
		if err != nil {
			return err
		}
	}
	// Reject a new worktree-contained destination before interactive harness
	// discovery can inspect any installed CLI. Project-local configs are allowed;
	// init applies the ignored local default without enforcing that user choice.
	if _, statErr := os.Stat(*configPath); errors.Is(statErr, os.ErrNotExist) {
		absoluteProjectDir, resolveErr := filepath.Abs(*projectDir)
		if resolveErr != nil {
			return fmt.Errorf("resolve project directory: %w", resolveErr)
		}
		workspaceRoot := filepath.Join(filepath.Dir(absoluteProjectDir), ".runner-worktrees")
		if err := config.ValidateTrustedConfigDestination(*configPath, workspaceRoot); err != nil {
			return err
		}
	}
	maxParallelismProvided := false
	autoMergeProvided := false
	flags.Visit(func(flagValue *flag.Flag) {
		switch flagValue.Name {
		case "max-parallelism":
			maxParallelismProvided = true
		case "auto-merge":
			autoMergeProvided = true
		}
	})
	if _, statErr := os.Stat(*configPath); statErr == nil {
		forbidden := map[string]bool{
			"owner": true, "project-number": true, "create-project": true, "repository": true,
			"intake-label": true, "project-visibility": true, "project-dir": true,
			"max-parallelism": true, "qa-reject-limit": true, "base-branch": true, "auto-merge": true, "merge-method": true,
			"admission-window": true, "max-admission-attempts": true, "max-admission-harness-time": true,
			"max-admission-tokens": true, "max-admission-cost-usd": true,
			"remote": true, "harness": true, "planner-harness": true, "implementer-harness": true,
			"reviewer-harness": true, "planner-access": true, "implementer-access": true, "reviewer-access": true,
			"harness-config": true, "planner-harness-config": true, "implementer-harness-config": true, "reviewer-harness-config": true,
			"planning-support": true, "implementer-planning-support": true, "reviewer-planning-support": true,
			"planner-model": true, "implementer-model": true,
			"model": true, "reviewer-model": true, "reasoning": true, "planner-reasoning": true, "implementer-reasoning": true,
			"reviewer-reasoning": true, "base-update-review": true,
		}
		var invalid string
		flags.Visit(func(flagValue *flag.Flag) {
			if invalid == "" && forbidden[flagValue.Name] {
				invalid = flagValue.Name
			}
		})
		if invalid != "" {
			return fmt.Errorf("--%s cannot be used when %s already exists; edit the config, then rerun init to synchronize it", invalid, *configPath)
		}
		cfg, _, checkErr := config.CheckTrustedConfig(*configPath)
		if checkErr != nil {
			return fmt.Errorf("validate existing runner config: %w", checkErr)
		}
		return synchronizeInit(ctx, cfg, *configPath, *dryRun, *prune, *force, *bootstrapBaseBranch, prompter, stdout)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect config path: %w", statErr)
	}
	if err := applyInitRoleDefaults(
		*defaultHarness, *defaultModel, *defaultReasoning,
		plannerHarness, implementerHarness, reviewerHarness,
		plannerModel, implementerModel, reviewerModel,
		plannerReasoning, implementerReasoning, reviewerReasoning,
	); err != nil {
		return err
	}
	if err := applyInitPlanningSupportDefaults(*defaultPlanningSupport, implementerPlanningSupport, reviewerPlanningSupport); err != nil {
		return err
	}
	if err := applyInitHarnessConfigDefaults(*defaultHarnessConfig, plannerHarnessConfig, implementerHarnessConfig, reviewerHarnessConfig); err != nil {
		return err
	}
	if prompter != nil {
		if err := promptInitChoices(
			ctx,
			prompter,
			projectNumber, createProject, projectVisibility,
			plannerHarness, implementerHarness, reviewerHarness,
			plannerModel, implementerModel, reviewerModel,
			plannerReasoning, implementerReasoning, reviewerReasoning,
			implementerPlanningSupport, reviewerPlanningSupport,
			maxParallelism, maxParallelismProvided,
			baseUpdateReview, autoMerge, autoMergeProvided, strings.TrimSpace(*baseBranch),
		); err != nil {
			return err
		}
	}
	*implementerPlanningSupport = config.EffectivePlanningSupport(*implementerPlanningSupport)
	*reviewerPlanningSupport = config.EffectivePlanningSupport(*reviewerPlanningSupport)
	if (*projectNumber > 0) == (strings.TrimSpace(*createProject) != "") {
		return errors.New("init requires exactly one of --project-number NUMBER or --create-project TITLE")
	}
	if strings.TrimSpace(*createProject) != "" {
		switch strings.ToLower(strings.TrimSpace(*projectVisibility)) {
		case "public", "private":
		default:
			return errors.New("--project-visibility must be explicitly set to public or private when creating a GitHub Project")
		}
	}
	if *prune && strings.TrimSpace(*createProject) != "" {
		return errors.New("--prune is only meaningful when adopting or synchronizing an existing GitHub Project")
	}
	reviewAfterBaseUpdate, err := parseBaseUpdateReview(*baseUpdateReview)
	if err != nil {
		return err
	}
	absoluteProjectDir, err := filepath.Abs(*projectDir)
	if err != nil {
		return fmt.Errorf("resolve project directory: %w", err)
	}
	workspaceRoot := filepath.Join(filepath.Dir(absoluteProjectDir), ".runner-worktrees")
	if err := config.ValidateTrustedConfigDestination(*configPath, workspaceRoot); err != nil {
		return err
	}
	selectedHarnesses, err := selectInitHarnesses(*plannerHarness, *implementerHarness, *reviewerHarness)
	if err != nil {
		return err
	}
	*plannerHarness = selectedHarnesses[config.WorkRolePlanner]
	*implementerHarness = selectedHarnesses[config.WorkRoleImplementer]
	*reviewerHarness = selectedHarnesses[config.WorkRoleReviewer]
	*plannerAccess, err = resolveInitRoleAccess(prompter, config.WorkRolePlanner, *plannerHarness, *plannerAccess)
	if err != nil {
		return err
	}
	*implementerAccess, err = resolveInitRoleAccess(prompter, config.WorkRoleImplementer, *implementerHarness, *implementerAccess)
	if err != nil {
		return err
	}
	*reviewerAccess, err = resolveInitRoleAccess(prompter, config.WorkRoleReviewer, *reviewerHarness, *reviewerAccess)
	if err != nil {
		return err
	}
	for _, selected := range []struct {
		role   execution.RoleContract
		kind   string
		access string
		mode   string
	}{
		{execution.RolePlanner, *plannerHarness, *plannerAccess, *plannerHarnessConfig},
		{execution.RoleReviewer, *reviewerHarness, *reviewerAccess, *reviewerHarnessConfig},
		{execution.RoleImplementer, *implementerHarness, *implementerAccess, *implementerHarnessConfig},
	} {
		if err := execution.ValidateHarnessProfile(selected.kind, selected.role, selected.access, selected.mode); err != nil {
			return fmt.Errorf("unsupported %s harness profile: %w", selected.role, err)
		}
	}
	workflow := config.WorkflowTemplate(reviewAfterBaseUpdate)
	qaLane := workflow.Lanes["agent_qa"]
	qaLane.RejectLimit = qaRejectLimit
	workflow.Lanes["agent_qa"] = qaLane
	roles := config.RoleTemplate(*plannerHarness)
	roles[config.WorkRolePlanner] = initRole(roles[config.WorkRolePlanner], *plannerHarness, *plannerModel, *plannerReasoning)
	roles[config.WorkRoleImplementer] = initRole(roles[config.WorkRoleImplementer], *implementerHarness, *implementerModel, *implementerReasoning)
	roles[config.WorkRoleReviewer] = initRole(roles[config.WorkRoleReviewer], *reviewerHarness, *reviewerModel, *reviewerReasoning)
	plannerRole := roles[config.WorkRolePlanner]
	plannerRole.Access = *plannerAccess
	plannerRole.HarnessConfig = *plannerHarnessConfig
	roles[config.WorkRolePlanner] = plannerRole
	implementerRole := roles[config.WorkRoleImplementer]
	implementerRole.Access = *implementerAccess
	implementerRole.HarnessConfig = *implementerHarnessConfig
	implementerRole.PlanningSupport = *implementerPlanningSupport
	roles[config.WorkRoleImplementer] = implementerRole
	reviewerRole := roles[config.WorkRoleReviewer]
	reviewerRole.Access = *reviewerAccess
	reviewerRole.HarnessConfig = *reviewerHarnessConfig
	reviewerRole.PlanningSupport = *reviewerPlanningSupport
	roles[config.WorkRoleReviewer] = reviewerRole
	admissionBudget, err := initAdmissionBudget(*admissionWindow, *maxAdmissionAttempts, *maxAdmissionHarnessTime, *maxAdmissionTokens, *maxAdmissionCostUSD)
	if err != nil {
		return err
	}
	harnessKinds := uniqueStrings([]string{*plannerHarness, *implementerHarness, *reviewerHarness})
	harnesses := make([]config.HarnessConfig, 0, len(harnessKinds))
	for _, kind := range harnessKinds {
		command, _ := setup.HarnessCommand(kind)
		enabled := true
		harness := config.HarnessConfig{Kind: kind, Command: command, Enabled: &enabled, WorkspaceWriteRoot: workspaceRoot}
		harnesses = append(harnesses, harness)
	}
	preflightProjectNumber := *projectNumber
	if preflightProjectNumber <= 0 {
		preflightProjectNumber = 1
	}
	preflightConfig := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runr_local_preflight", ProjectDir: absoluteProjectDir, MaxParallelism: *maxParallelism,
		AdmissionBudget: admissionBudget,
		ResourceLimits:  config.DefaultResourceLimitsConfig(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: strings.TrimSpace(*owner), Number: preflightProjectNumber, IntakeRepository: strings.TrimSpace(*repository), IntakeLabel: strings.TrimSpace(*intakeLabel),
			ResultField: "Runner Result", ApprovalField: "Runner Approval",
			PhaseField: "Runner Phase", TransitionField: config.RunnerTransitionFieldName, QAFailuresField: "QA Failures", BranchField: "Runner Branch", PullRequestField: "Pull Request", QACommitField: "QA Commit",
			BaseBranch: strings.TrimSpace(*baseBranch), RemoteName: strings.TrimSpace(*remoteName), AutoMerge: *autoMerge, MergeMethod: strings.TrimSpace(*mergeMethod),
		},
		Harnesses: harnesses,
		Roles:     roles, Workflow: &workflow,
	}
	if preflightConfig.GitHubProject.Owner == "" {
		preflightConfig.GitHubProject.Owner = "preflight"
	}
	if preflightConfig.GitHubProject.IntakeRepository == "" {
		preflightConfig.GitHubProject.IntakeRepository = "preflight/repository"
	}
	if err := config.ValidateConfiguration(preflightConfig); err != nil {
		return fmt.Errorf("validate init settings before GitHub changes: %w", err)
	}
	if err := prepareInitTools(preflightConfig, *dryRun, stdout); err != nil {
		return err
	}
	writeProgress(stdout, "Checking the Git repository and GitHub prerequisites…")
	gitPreflight, err := preflightGitRepository(ctx, absoluteProjectDir, strings.TrimSpace(*remoteName), strings.TrimSpace(*baseBranch), *bootstrapBaseBranch || prompter != nil)
	if err != nil {
		return err
	}
	if gitPreflight.Bootstrap != nil && !*bootstrapBaseBranch {
		confirmed, confirmationErr := confirmBaseBranchBootstrap(prompter, gitPreflight.Bootstrap)
		if confirmationErr != nil {
			return confirmationErr
		}
		if !confirmed {
			return fmt.Errorf("Git remote %q is empty and base branch %q is required; initialize it and rerun init", gitPreflight.Bootstrap.RemoteName, gitPreflight.Bootstrap.BaseBranch)
		}
		*bootstrapBaseBranch = true
	}
	repositoryRoot, remoteRepository := gitPreflight.Root, gitPreflight.Repository
	absoluteProjectDir = repositoryRoot
	workspaceRoot = filepath.Join(filepath.Dir(repositoryRoot), ".runner-worktrees")
	if err := config.ValidateTrustedConfigDestination(*configPath, workspaceRoot); err != nil {
		return err
	}
	if err := preflightConfigDestination(*configPath); err != nil {
		return err
	}
	for index := range harnesses {
		harnesses[index].WorkspaceWriteRoot = workspaceRoot
	}
	resolvedRepository := strings.TrimSpace(*repository)
	if resolvedRepository == "" {
		resolvedRepository = remoteRepository
	}
	if !strings.EqualFold(resolvedRepository, remoteRepository) {
		return fmt.Errorf("--repository %q does not match Git remote %q", resolvedRepository, remoteRepository)
	}
	resolvedOwner := strings.TrimSpace(*owner)
	if resolvedOwner == "" {
		resolvedOwner, _, _ = strings.Cut(resolvedRepository, "/")
	}
	preflightConfig.ProjectDir = absoluteProjectDir
	preflightConfig.GitHubProject.Owner = resolvedOwner
	preflightConfig.GitHubProject.IntakeRepository = resolvedRepository
	provisionRequest := provisionRequestFromConfig(preflightConfig, strings.TrimSpace(*createProject), strings.TrimSpace(*projectVisibility), *prune)
	provisioner := github.NewProjectProvisioner(nil)
	if err := provisioner.Preflight(ctx, provisionRequest, strings.TrimSpace(*createProject) != ""); err != nil {
		return fmt.Errorf("preflight GitHub prerequisites: %w", err)
	}
	if *dryRun {
		fmt.Fprintf(stdout, "Initialization dry run\n  Config: create %s\n", *configPath)
		if err := previewRunnerConfigGitignore(*configPath, repositoryRoot, stdout); err != nil {
			return err
		}
		writeBaseBranchBootstrapPlan(gitPreflight.Bootstrap, stdout)
		if strings.TrimSpace(*createProject) != "" {
			fmt.Fprintf(stdout, "  GitHub Project: create %q for %s and add the configured fields and statuses\n", strings.TrimSpace(*createProject), resolvedOwner)
		} else if err := writeProjectConfigurationPlan(ctx, provisioner, preflightConfig, provisionRequest, stdout); err != nil {
			return err
		}
		return installInitSkills(preflightConfig, *configPath, *force, true, stdout)
	}
	if err := prepareRunnerConfigGitignore(ctx, *configPath, repositoryRoot, stdout); err != nil {
		return err
	}
	if err := applyBaseBranchBootstrap(ctx, gitPreflight.Bootstrap, stdout); err != nil {
		return err
	}
	resolvedProjectNumber := *projectNumber
	if strings.TrimSpace(*createProject) != "" {
		writeProgress(stdout, "Creating and configuring the GitHub Project…")
		created, createErr := provisioner.Create(ctx, provisionRequest)
		if createErr != nil {
			return createErr
		}
		resolvedProjectNumber = created.Number
		fmt.Fprintf(stdout, "Created and configured GitHub Project %s\n", created.URL)
	} else {
		writeProgress(stdout, "Synchronizing the existing GitHub Project…")
		if err := provisioner.Configure(ctx, resolvedProjectNumber, provisionRequest); err != nil {
			return fmt.Errorf("configure existing GitHub Project: %w", err)
		}
		fmt.Fprintf(stdout, "Configured GitHub Project %s/%d\n", resolvedOwner, resolvedProjectNumber)
	}
	runnerID, err := localRunnerID()
	if err != nil {
		return fmt.Errorf("create runner id: %w", err)
	}
	cfg := config.Config{
		ConfigVersion:   config.ConfigVersion,
		RunnerID:        runnerID,
		ProjectDir:      absoluteProjectDir,
		MaxParallelism:  *maxParallelism,
		AdmissionBudget: admissionBudget,
		ResourceLimits:  config.DefaultResourceLimitsConfig(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: resolvedOwner, Number: resolvedProjectNumber, IntakeRepository: resolvedRepository, IntakeLabel: strings.TrimSpace(*intakeLabel),
			ResultField: "Runner Result", ApprovalField: "Runner Approval",
			PhaseField: "Runner Phase", TransitionField: config.RunnerTransitionFieldName, QAFailuresField: "QA Failures", BranchField: "Runner Branch", PullRequestField: "Pull Request", QACommitField: "QA Commit",
			BaseBranch: strings.TrimSpace(*baseBranch), RemoteName: strings.TrimSpace(*remoteName), AutoMerge: *autoMerge, MergeMethod: strings.TrimSpace(*mergeMethod),
		},
		Harnesses: harnesses,
		Roles:     roles, Workflow: &workflow,
	}
	if err := config.ValidateConfiguration(cfg); err != nil {
		return err
	}
	if err := config.SaveConfig(*configPath, cfg); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Created runner config %s\n", *configPath)
	if err := installInitSkills(cfg, *configPath, *force, false, stdout); err != nil {
		return err
	}
	doctorCommand := "cortexium-runner doctor --config " + *configPath
	runCommand := "cortexium-runner run --config " + *configPath
	fmt.Fprintln(stdout, "Initialization complete. Next: run `"+doctorCommand+"`, then `"+runCommand+"`.")
	return nil
}

func initAdmissionBudget(window time.Duration, maxAttempts int, maxHarnessTime time.Duration, maxTokens int64, maxCostUSD float64) (*config.AdmissionBudgetConfig, error) {
	configured := window != 0 || maxAttempts != 0 || maxHarnessTime != 0 || maxTokens != 0 || maxCostUSD != 0
	if !configured {
		return nil, nil
	}
	if window <= 0 {
		return nil, errors.New("--admission-window must be positive when an admission ceiling is configured")
	}
	if window%time.Second != 0 || maxHarnessTime%time.Second != 0 {
		return nil, errors.New("admission window and harness time must use whole-second durations")
	}
	budget := &config.AdmissionBudgetConfig{
		WindowSeconds:     window.Milliseconds() / 1000,
		MaxAttempts:       maxAttempts,
		MaxHarnessSeconds: maxHarnessTime.Milliseconds() / 1000,
		MaxReportedTokens: maxTokens,
	}
	if maxCostUSD != 0 {
		budget.MaxReportedCostUSD = &maxCostUSD
	}
	return budget, nil
}

func synchronizeInit(ctx context.Context, cfg config.Config, configPath string, dryRun, prune, force, bootstrapBaseBranch bool, prompter *initPrompter, stdout io.Writer) error {
	if err := prepareInitTools(cfg, dryRun, stdout); err != nil {
		return err
	}
	writeProgress(stdout, "Checking the Git repository and GitHub prerequisites…")
	project := cfg.GitHubProject
	gitPreflight, err := preflightGitRepository(ctx, cfg.ProjectDir, strings.TrimSpace(project.RemoteName), strings.TrimSpace(project.BaseBranch), bootstrapBaseBranch || prompter != nil)
	if err != nil {
		return err
	}
	if gitPreflight.Bootstrap != nil && !bootstrapBaseBranch {
		confirmed, confirmationErr := confirmBaseBranchBootstrap(prompter, gitPreflight.Bootstrap)
		if confirmationErr != nil {
			return confirmationErr
		}
		if !confirmed {
			return fmt.Errorf("Git remote %q is empty and base branch %q is required; initialize it and rerun init", gitPreflight.Bootstrap.RemoteName, gitPreflight.Bootstrap.BaseBranch)
		}
	}
	repositoryRoot, remoteRepository := gitPreflight.Root, gitPreflight.Repository
	if !strings.EqualFold(strings.TrimSpace(project.IntakeRepository), remoteRepository) {
		return fmt.Errorf("configured intake repository %q does not match Git remote %q", project.IntakeRepository, remoteRepository)
	}
	cfg.ProjectDir = repositoryRoot
	request := provisionRequestFromConfig(cfg, "", "", prune)
	provisioner := github.NewProjectProvisioner(nil)
	if err := provisioner.Preflight(ctx, request, false); err != nil {
		return fmt.Errorf("preflight GitHub prerequisites: %w", err)
	}
	if dryRun {
		fmt.Fprintln(stdout, "Initialization dry run")
		writeBaseBranchBootstrapPlan(gitPreflight.Bootstrap, stdout)
		if err := writeProjectConfigurationPlan(ctx, provisioner, cfg, request, stdout); err != nil {
			return err
		}
		return installInitSkills(cfg, configPath, force, true, stdout)
	}
	if err := applyBaseBranchBootstrap(ctx, gitPreflight.Bootstrap, stdout); err != nil {
		return err
	}
	writeProgress(stdout, "Synchronizing the existing GitHub Project…")
	if err := provisioner.Configure(ctx, project.Number, request); err != nil {
		return fmt.Errorf("synchronize existing GitHub Project: %w", err)
	}
	verb := "Synchronized"
	if prune {
		verb = "Synchronized and pruned"
	}
	fmt.Fprintf(stdout, "%s GitHub Project %s/%d\n", verb, project.Owner, project.Number)
	if err := installInitSkills(cfg, configPath, force, false, stdout); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Initialization complete. Next: run `cortexium-runner doctor --config "+configPath+"` to verify readiness.")
	return nil
}

func provisionRequestFromConfig(cfg config.Config, title, visibility string, prune bool) github.ProvisionRequest {
	project := cfg.GitHubProject
	return github.ProvisionRequest{
		Owner: project.Owner, Title: title, Repository: project.IntakeRepository, Visibility: visibility,
		Statuses: workflowStatusNames(cfg.EffectiveWorkflow()), ResultField: strings.TrimSpace(project.ResultField),
		ApprovalField: strings.TrimSpace(project.ApprovalField), PhaseField: strings.TrimSpace(project.PhaseField), TransitionField: project.TransitionFieldName(),
		QAFailuresField: strings.TrimSpace(project.QAFailuresField), BranchField: strings.TrimSpace(project.BranchField),
		PullRequestField: project.PullRequestField, QACommitField: project.QACommitField,
		IntakeLabel: project.IntakeLabel, Prune: prune,
	}
}

func writeProjectConfigurationPlan(ctx context.Context, provisioner *github.ProjectProvisioner, cfg config.Config, request github.ProvisionRequest, stdout io.Writer) error {
	project := cfg.GitHubProject
	statuses := workflowStatusNames(cfg.EffectiveWorkflow())
	fmt.Fprintf(stdout, "  GitHub Project: %s/%d\n  Statuses: %s\n", project.Owner, project.Number, strings.Join(statuses, ", "))
	writeProgress(stdout, "Inspecting the current GitHub Project configuration…")
	plan, err := provisioner.PlanConfigure(ctx, project.Number, request)
	if err != nil {
		return fmt.Errorf("inspect current GitHub Project without changing it: %w", err)
	}
	if gaps := projectConfigurationGaps(plan.Inspection); len(gaps) == 0 {
		fmt.Fprintln(stdout, "  Current Project: compatible")
	} else {
		fmt.Fprintf(stdout, "  Current Project needs: %s\n", strings.Join(gaps, ", "))
	}
	if request.Prune {
		writeStatusPrunePreview(stdout, plan.ExtraStatuses)
	}
	return nil
}

func writeStatusPrunePreview(stdout io.Writer, options []github.StatusOptionUsage) {
	if len(options) == 0 {
		fmt.Fprintln(stdout, "  Prune: no extra Status options")
		return
	}
	fmt.Fprintln(stdout, "  Prune extra Status options:")
	for _, option := range options {
		if option.Active == 0 && option.Archived == 0 {
			fmt.Fprintf(stdout, "    remove %s (empty)\n", option.Name)
			continue
		}
		fmt.Fprintf(stdout, "    keep %s (%d active, %d archived; occupied options block pruning)\n", option.Name, option.Active, option.Archived)
	}
}

func projectConfigurationGaps(inspection github.ProjectInspection) []string {
	gaps := []string{}
	if !inspection.BoardView {
		gaps = append(gaps, "Kanban board view")
	}
	if !inspection.BoardLifecycleFields {
		gaps = append(gaps, "board-visible Runner Activity and QA Failures fields")
	}
	if !inspection.StatusField || !inspection.WorkflowStatuses {
		gaps = append(gaps, "configured statuses")
	}
	if !inspection.ResultField {
		gaps = append(gaps, "result field")
	}
	if !inspection.ApprovalField || !inspection.PhaseField || !inspection.TransitionField || !inspection.ActivityField || !inspection.QAFailuresField || !inspection.BranchField || !inspection.PullRequestField || !inspection.QACommitField {
		gaps = append(gaps, "Runner lifecycle fields")
	}
	if !inspection.IntakeRepository || !inspection.IntakeLabel {
		gaps = append(gaps, "public issue intake")
	}
	return gaps
}

func localRunnerID() (string, error) {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return "runr_local_" + hex.EncodeToString(value[:]), nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func applyInitRoleDefaults(
	harness, model, reasoning string,
	plannerHarness, implementerHarness, reviewerHarness *string,
	plannerModel, implementerModel, reviewerModel *string,
	plannerReasoning, implementerReasoning, reviewerReasoning *string,
) error {
	harness = strings.TrimSpace(harness)
	model = strings.TrimSpace(model)
	reasoning = strings.TrimSpace(reasoning)
	if harness != "" && !config.ValidHarnessKind(harness) {
		return errors.New("--harness must be codex, claude, or pi")
	}
	if reasoning != "" {
		switch reasoning {
		case "off", "low", "medium", "high", "xhigh":
		default:
			return errors.New("--reasoning must be low, medium, high, or xhigh; Pi also supports off")
		}
	}
	apply := func(value string, targets ...*string) {
		if value == "" {
			return
		}
		for _, target := range targets {
			if strings.TrimSpace(*target) == "" {
				*target = value
			}
		}
	}
	apply(harness, plannerHarness, implementerHarness, reviewerHarness)
	apply(model, plannerModel, implementerModel, reviewerModel)
	apply(reasoning, plannerReasoning, implementerReasoning, reviewerReasoning)
	return nil
}

func applyInitPlanningSupportDefaults(value string, implementer, reviewer *string) error {
	value = strings.TrimSpace(value)
	for name, candidate := range map[string]string{
		"--planning-support":             value,
		"--implementer-planning-support": strings.TrimSpace(*implementer),
		"--reviewer-planning-support":    strings.TrimSpace(*reviewer),
	} {
		if candidate != "" && !config.ValidPlanningSupport(candidate) {
			return fmt.Errorf("%s must be standard or high", name)
		}
	}
	if value != "" {
		if strings.TrimSpace(*implementer) == "" {
			*implementer = value
		}
		if strings.TrimSpace(*reviewer) == "" {
			*reviewer = value
		}
	}
	return nil
}

func applyInitHarnessConfigDefaults(value string, planner, implementer, reviewer *string) error {
	value = strings.TrimSpace(value)
	for name, candidate := range map[string]string{
		"--harness-config":             value,
		"--planner-harness-config":     strings.TrimSpace(*planner),
		"--implementer-harness-config": strings.TrimSpace(*implementer),
		"--reviewer-harness-config":    strings.TrimSpace(*reviewer),
	} {
		if candidate != "" && !config.ValidHarnessConfigMode(candidate) {
			return fmt.Errorf("%s must be isolated or inherit", name)
		}
	}
	for _, target := range []*string{planner, implementer, reviewer} {
		if strings.TrimSpace(*target) == "" {
			*target = config.EffectiveHarnessConfigMode(value)
		}
	}
	return nil
}

func promptInitPlanningSupport(prompter *initPrompter, implementer, reviewer *string) error {
	if strings.TrimSpace(*implementer) != "" || strings.TrimSpace(*reviewer) != "" {
		return nil
	}
	options := []initMenuOption{
		{
			Label:       "Regular coherent tasks (recommended)",
			Description: "Use natural review boundaries without extra decomposition",
			Value:       config.PlanningSupportStandard,
		},
		{
			Label:       "Smaller coherent tasks",
			Description: "Split independently verifiable behavior for less capable downstream models",
			Value:       config.PlanningSupportHigh,
		},
	}
	selected, err := prompter.selectMenu("How should the planner size downstream tasks?", options, 0)
	if err != nil {
		return err
	}
	*implementer, *reviewer = options[selected].Value, options[selected].Value
	return nil
}

func initRole(role config.RoleConfig, harness, model, reasoning string) config.RoleConfig {
	role.Harness = strings.TrimSpace(harness)
	if strings.TrimSpace(model) != "" {
		value := strings.TrimSpace(model)
		role.Model = &value
	}
	if strings.TrimSpace(reasoning) != "" {
		role.Reasoning = strings.TrimSpace(reasoning)
	}
	return role
}

type initPrompter struct {
	input       *bufio.Reader
	output      io.Writer
	terminalIn  *os.File
	terminalOut *os.File
	colors      bool
}

func newInitPrompter(input io.Reader, output io.Writer) *initPrompter {
	prompter := &initPrompter{input: bufio.NewReader(input), output: output}
	inputFile, inputOK := input.(*os.File)
	outputFile, outputOK := output.(*os.File)
	if inputOK && outputOK && isTerminalFile(inputFile) && isTerminalFile(outputFile) {
		prompter.terminalIn = inputFile
		prompter.terminalOut = outputFile
		prompter.colors = terminalColorsEnabled(outputFile)
	}
	return prompter
}

func initInteractiveMode(stdin io.Reader, stdout io.Writer, forceInteractive, nonInteractive bool) (bool, error) {
	if forceInteractive && nonInteractive {
		return false, errors.New("--interactive and --non-interactive cannot be used together")
	}
	if forceInteractive {
		return true, nil
	}
	if nonInteractive {
		return false, nil
	}
	return isTerminalFile(stdin) && isTerminalFile(stdout), nil
}

func promptInitChoices(
	ctx context.Context,
	prompter *initPrompter,
	projectNumber *int,
	createProject, projectVisibility, plannerHarness, implementerHarness, reviewerHarness *string,
	plannerModel, implementerModel, reviewerModel, plannerReasoning, implementerReasoning, reviewerReasoning *string,
	implementerPlanningSupport, reviewerPlanningSupport *string,
	maxParallelism *int,
	maxParallelismProvided bool,
	baseUpdateReview *string,
	autoMerge *bool,
	autoMergeProvided bool,
	baseBranch string,
) error {
	if *projectNumber <= 0 && strings.TrimSpace(*createProject) == "" {
		mode, err := prompter.choice("GitHub Project action", []string{"create", "adopt"}, nil)
		if err != nil {
			return err
		}
		if mode == "create" {
			title, err := prompter.required("New GitHub Project title")
			if err != nil {
				return err
			}
			*createProject = title
		} else {
			value, err := prompter.required("Existing GitHub Project number")
			if err != nil {
				return err
			}
			number, conversionErr := strconv.Atoi(value)
			if conversionErr != nil || number <= 0 {
				return fmt.Errorf("GitHub Project number must be a positive integer, got %q", value)
			}
			*projectNumber = number
		}
	}
	if strings.TrimSpace(*createProject) != "" && strings.TrimSpace(*projectVisibility) == "" {
		visibility, err := prompter.choice("New GitHub Project visibility", []string{"private", "public"}, nil)
		if err != nil {
			return err
		}
		*projectVisibility = visibility
	}
	if err := promptInitHarnessChoices(prompter, plannerHarness, implementerHarness, reviewerHarness); err != nil {
		return err
	}
	if err := promptInitRuntimeChoices(
		ctx,
		prompter,
		plannerHarness, implementerHarness, reviewerHarness,
		plannerModel, implementerModel, reviewerModel,
		plannerReasoning, implementerReasoning, reviewerReasoning,
	); err != nil {
		return err
	}
	if err := promptInitPlanningSupport(prompter, implementerPlanningSupport, reviewerPlanningSupport); err != nil {
		return err
	}
	if !maxParallelismProvided {
		options := []initMenuOption{
			{
				Label:       "1 concurrent card (recommended)",
				Description: "Safest for a first run and strict harness rate limits",
				Value:       "1",
			},
			{
				Label:       "2 concurrent cards",
				Description: "Runs only cards whose declared dependencies are already Done",
				Value:       "2",
			},
			{
				Label:       "4 concurrent cards",
				Description: "Higher throughput, cost, and rate-limit pressure",
				Value:       "4",
			},
		}
		selected, err := prompter.selectMenu(
			"How many independent cards may Runner work on at the same time?",
			options,
			0,
		)
		if err != nil {
			return err
		}
		value, conversionErr := strconv.Atoi(options[selected].Value)
		if conversionErr != nil {
			return fmt.Errorf("invalid built-in parallelism option %q: %w", options[selected].Value, conversionErr)
		}
		*maxParallelism = value
	}
	if strings.TrimSpace(*baseUpdateReview) == "" {
		options := []initMenuOption{
			{
				Label:       "Re-run implementation and Agent QA (recommended)",
				Description: "Safest when merged changes could affect this work",
				Value:       "required",
			},
			{
				Label:       "Continue to human PR review without re-running agents",
				Description: "Faster, but agents do not check the refreshed code",
				Value:       "skip",
			},
		}
		selected, err := prompter.selectMenu(
			fmt.Sprintf("When another PR changes %s, Runner updates this branch. What should happen next?", baseBranch),
			options,
			0,
		)
		if err != nil {
			return err
		}
		*baseUpdateReview = options[selected].Value
	}
	if !autoMergeProvided {
		options := []initMenuOption{
			{
				Label:       "Wait for a human to merge (recommended)",
				Description: "Runner creates the pull request and stops at the review gate",
				Value:       "manual",
			},
			{
				Label:       "Merge automatically after GitHub requirements pass",
				Description: "Useful for autonomous PoCs; checks and branch protections are never bypassed",
				Value:       "automatic",
			},
		}
		selected, err := prompter.selectMenu(
			"After Agent QA passes, what should Runner do with the pull request?",
			options,
			0,
		)
		if err != nil {
			return err
		}
		*autoMerge = options[selected].Value == "automatic"
	}
	return nil
}

func promptInitHarnessChoices(prompter *initPrompter, planner, implementer, reviewer *string) error {
	available := setup.AvailableHarnesses()
	if len(available) == 0 {
		return errors.New("no supported harness is available on PATH; install Codex CLI, Claude Code, or Pi CLI before interactive init")
	}
	allMissing := strings.TrimSpace(*planner) == "" && strings.TrimSpace(*implementer) == "" && strings.TrimSpace(*reviewer) == ""
	if allMissing {
		if len(available) == 1 {
			*planner, *implementer, *reviewer = available[0].Kind, available[0].Kind, available[0].Kind
			return nil
		}
		selected, err := prompter.harness("Default harness for all roles", available)
		if err != nil {
			return err
		}
		*planner, *implementer, *reviewer = selected, selected, selected
		return nil
	}
	for _, role := range []struct {
		name  string
		value *string
	}{
		{name: config.WorkRolePlanner, value: planner},
		{name: config.WorkRoleImplementer, value: implementer},
		{name: config.WorkRoleReviewer, value: reviewer},
	} {
		if strings.TrimSpace(*role.value) != "" {
			continue
		}
		roleLabel := strings.ToUpper(role.name[:1]) + role.name[1:]
		roleContract := execution.RoleContract(role.name)
		roleHarnesses := supportedInitHarnesses(available, roleContract)
		if len(roleHarnesses) == 0 {
			return fmt.Errorf("no installed harness can enforce Runner's %s profile", role.name)
		}
		selected, err := prompter.harness(roleLabel+" harness", roleHarnesses)
		if err != nil {
			return err
		}
		*role.value = selected
	}
	return nil
}

func supportedInitHarnesses(available []setup.HarnessDescriptor, role execution.RoleContract) []setup.HarnessDescriptor {
	supported := make([]setup.HarnessDescriptor, 0, len(available))
	for _, descriptor := range available {
		access := config.RoleAccessSandboxed
		if descriptor.Kind == config.HarnessPiCLI && (role == execution.RoleImplementer || role == execution.RoleReviewer) {
			access = config.RoleAccessHost
		}
		if execution.ValidateHarnessProfile(descriptor.Kind, role, access) == nil {
			supported = append(supported, descriptor)
		}
	}
	return supported
}

func resolveInitRoleAccess(prompter *initPrompter, role, harness, requested string) (string, error) {
	access := config.EffectiveRoleAccess(requested)
	if strings.TrimSpace(requested) != "" && !config.ValidRoleAccess(requested) {
		return "", fmt.Errorf("--%s-access must be sandboxed or host", role)
	}
	if harness != config.HarnessPiCLI || (role != config.WorkRoleImplementer && role != config.WorkRoleReviewer) {
		return access, nil
	}
	if strings.TrimSpace(requested) == "" {
		if prompter == nil {
			return "", fmt.Errorf("Pi CLI has no native OS sandbox for the %s role; rerun with --%s-access host on a trusted machine or choose another harness", role, role)
		}
		answer, err := prompter.choice(
			fmt.Sprintf("Pi CLI requires host access for the %s role. Continue on this trusted machine?", role),
			[]string{"no", "yes"}, map[string]string{"n": "no", "y": "yes"},
		)
		if err != nil {
			return "", err
		}
		if answer != "yes" {
			return "", fmt.Errorf("Pi CLI %s setup stopped without explicit host-access approval", role)
		}
		access = config.RoleAccessHost
	}
	if access != config.RoleAccessHost {
		return "", fmt.Errorf("Pi CLI cannot enforce sandboxed %s access; use host on a trusted machine or choose another harness", role)
	}
	return access, nil
}

func promptInitRuntimeChoices(
	ctx context.Context,
	prompter *initPrompter,
	plannerHarness, implementerHarness, reviewerHarness *string,
	plannerModel, implementerModel, reviewerModel *string,
	plannerReasoning, implementerReasoning, reviewerReasoning *string,
) error {
	harnessesMatch := strings.TrimSpace(*plannerHarness) != "" &&
		strings.TrimSpace(*plannerHarness) == strings.TrimSpace(*implementerHarness) &&
		strings.TrimSpace(*plannerHarness) == strings.TrimSpace(*reviewerHarness)
	modelsMissing := strings.TrimSpace(*plannerModel) == "" && strings.TrimSpace(*implementerModel) == "" && strings.TrimSpace(*reviewerModel) == ""
	if harnessesMatch && modelsMissing {
		model, err := prompter.model(ctx, "Default model for all roles", strings.TrimSpace(*plannerHarness))
		if err != nil {
			return err
		}
		if model != "" {
			*plannerModel, *implementerModel, *reviewerModel = model, model, model
		}
	} else {
		for _, role := range []struct {
			name    string
			model   *string
			harness *string
		}{
			{name: config.WorkRolePlanner, model: plannerModel, harness: plannerHarness},
			{name: config.WorkRoleImplementer, model: implementerModel, harness: implementerHarness},
			{name: config.WorkRoleReviewer, model: reviewerModel, harness: reviewerHarness},
		} {
			if strings.TrimSpace(*role.model) != "" {
				continue
			}
			roleLabel := strings.ToUpper(role.name[:1]) + role.name[1:]
			model, err := prompter.model(ctx, roleLabel+" model", strings.TrimSpace(*role.harness))
			if err != nil {
				return err
			}
			if model != "" {
				*role.model = model
			}
		}
	}

	reasoningMissing := strings.TrimSpace(*plannerReasoning) == "" && strings.TrimSpace(*implementerReasoning) == "" && strings.TrimSpace(*reviewerReasoning) == ""
	if reasoningMissing {
		reasoning, err := prompter.choiceAt("Default reasoning effort for all roles", []string{"low", "medium", "high", "xhigh"}, nil, 2)
		if err != nil {
			return err
		}
		*plannerReasoning, *implementerReasoning, *reviewerReasoning = reasoning, reasoning, reasoning
		return nil
	}
	for _, role := range []struct {
		name      string
		reasoning *string
	}{
		{name: config.WorkRolePlanner, reasoning: plannerReasoning},
		{name: config.WorkRoleImplementer, reasoning: implementerReasoning},
		{name: config.WorkRoleReviewer, reasoning: reviewerReasoning},
	} {
		if strings.TrimSpace(*role.reasoning) != "" {
			continue
		}
		roleLabel := strings.ToUpper(role.name[:1]) + role.name[1:]
		reasoning, err := prompter.choice(roleLabel+" reasoning effort", []string{"low", "medium", "high", "xhigh"}, nil)
		if err != nil {
			return err
		}
		*role.reasoning = reasoning
	}
	return nil
}

func (p *initPrompter) required(label string) (string, error) {
	for {
		value, err := p.read(label)
		if err != nil {
			return "", err
		}
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(p.output, "A value is required.")
	}
}

func (p *initPrompter) withDefault(label, fallback string) (string, error) {
	value, err := p.read(label + " [" + fallback + "]")
	if err != nil {
		return "", err
	}
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func suggestedInitConfigPath(ctx context.Context, projectDir string) string {
	if root, rootErr := runInitGit(ctx, projectDir, "rev-parse", "--show-toplevel"); rootErr == nil {
		return filepath.Join(root, filepath.FromSlash(defaultRunnerConfigPath))
	}
	absolute, err := filepath.Abs(projectDir)
	if err != nil {
		return ""
	}
	return filepath.Join(absolute, filepath.FromSlash(defaultRunnerConfigPath))
}

func (p *initPrompter) choice(label string, allowed []string, aliases map[string]string) (string, error) {
	return p.choiceAt(label, allowed, aliases, 0)
}

func (p *initPrompter) choiceAt(label string, allowed []string, aliases map[string]string, selected int) (string, error) {
	if p.terminalIn != nil && p.terminalOut != nil {
		options := make([]initMenuOption, 0, len(allowed))
		for _, value := range allowed {
			options = append(options, initMenuOption{Label: value, Value: value})
		}
		index, err := p.selectMenu(label, options, selected)
		if err != nil {
			return "", err
		}
		return allowed[index], nil
	}
	accepted := map[string]string{}
	for _, value := range allowed {
		accepted[strings.ToLower(value)] = value
	}
	for alias, value := range aliases {
		accepted[strings.ToLower(alias)] = value
	}
	for {
		value, err := p.read(label + " [" + strings.Join(allowed, "/") + "]")
		if err != nil {
			return "", err
		}
		if resolved, ok := accepted[strings.ToLower(value)]; ok {
			return resolved, nil
		}
		fmt.Fprintf(p.output, "Choose one of: %s.\n", strings.Join(allowed, ", "))
	}
}

func (p *initPrompter) harness(label string, available []setup.HarnessDescriptor) (string, error) {
	options := make([]initMenuOption, 0, len(available))
	for _, descriptor := range available {
		options = append(options, initMenuOption{
			Label: descriptor.DisplayName,
			Value: descriptor.Kind,
		})
	}
	if p.terminalIn != nil && p.terminalOut != nil {
		index, err := p.selectMenu(label, options, 0)
		if err != nil {
			return "", err
		}
		return options[index].Value, nil
	}
	aliases := map[string]string{}
	allowed := make([]string, 0, len(available))
	for _, descriptor := range available {
		allowed = append(allowed, descriptor.Kind)
		aliases[strings.ToLower(descriptor.DisplayName)] = descriptor.Kind
		aliases[strings.TrimSuffix(descriptor.Kind, "_cli")] = descriptor.Kind
		aliases[descriptor.Command] = descriptor.Kind
	}
	return p.choice(label, allowed, aliases)
}

func (p *initPrompter) read(label string) (string, error) {
	fmt.Fprintf(p.output, "%s: ", terminalStyled(p.colors, toneQuestion, label))
	line, err := p.input.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read interactive init response: %w", err)
	}
	value := strings.TrimSpace(line)
	if errors.Is(err, io.EOF) && value == "" {
		return "", errors.New("interactive init input ended before setup was complete; rerun in a terminal or pass --non-interactive with every required option")
	}
	return value, nil
}

func selectInitHarnesses(planner, implementer, reviewer string) (map[string]string, error) {
	selected := map[string]string{
		config.WorkRolePlanner:     strings.TrimSpace(planner),
		config.WorkRoleImplementer: strings.TrimSpace(implementer),
		config.WorkRoleReviewer:    strings.TrimSpace(reviewer),
	}
	for role, kind := range selected {
		if kind != "" && !config.ValidHarnessKind(kind) {
			return nil, fmt.Errorf("--%s-harness must be codex, claude, or pi", role)
		}
	}
	available := setup.AvailableHarnesses()
	if len(available) == 1 {
		for role, kind := range selected {
			if kind == "" {
				selected[role] = available[0].Kind
			}
		}
	}
	missing := []string{}
	for _, role := range config.BuiltinRoleIDs() {
		if selected[role] == "" {
			missing = append(missing, role)
		}
	}
	if len(missing) == 0 {
		return selected, nil
	}
	kinds := make([]string, 0, len(available))
	for _, descriptor := range available {
		kinds = append(kinds, descriptor.Kind)
	}
	if len(kinds) == 0 {
		return nil, fmt.Errorf("no supported harness is available on PATH; install one or explicitly select every role harness for prerequisite installation")
	}
	return nil, fmt.Errorf("multiple harnesses are available (%s); explicitly select --%s-harness", strings.Join(kinds, ", "), strings.Join(missing, "-harness, --"))
}

func parseBaseUpdateReview(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "required":
		return true, nil
	default:
		return false, errors.New("--base-update-review must be explicitly set to required")
	}
}

func preflightConfigDestination(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to replace existing config %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config path: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	directory, err := securefs.OpenDir(dir)
	if err != nil {
		return fmt.Errorf("validate operator-controlled config directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close operator config directory: %w", err)
	}
	probe, err := os.CreateTemp(dir, ".runner-init-preflight-*.tmp")
	if err != nil {
		return fmt.Errorf("verify config destination is writable: %w", err)
	}
	probePath := probe.Name()
	if err := probe.Close(); err != nil {
		os.Remove(probePath)
		return fmt.Errorf("verify config destination: %w", err)
	}
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("clean config destination probe: %w", err)
	}
	return nil
}

type gitRepositoryPreflight struct {
	Root       string
	Repository string
	Bootstrap  *baseBranchBootstrap
}

type baseBranchBootstrap struct {
	Root                string
	RemoteName          string
	BaseBranch          string
	LocalBaseCommit     string
	CurrentBranch       string
	CreateInitialCommit bool
}

func confirmBaseBranchBootstrap(prompter *initPrompter, bootstrap *baseBranchBootstrap) (bool, error) {
	if prompter == nil || bootstrap == nil {
		return false, errors.New("interactive base-branch confirmation is unavailable")
	}
	question := fmt.Sprintf(
		"Git remote %q is empty. Should Runner push the existing local %s branch?",
		bootstrap.RemoteName,
		bootstrap.BaseBranch,
	)
	if bootstrap.CreateInitialCommit {
		question = fmt.Sprintf(
			"Git remote %q is empty. Should Runner create and push an empty initial commit on %s?",
			bootstrap.RemoteName,
			bootstrap.BaseBranch,
		)
	}
	answer, err := prompter.choice(
		question,
		[]string{"yes", "no"},
		map[string]string{"y": "yes", "n": "no"},
	)
	if err != nil {
		return false, err
	}
	return answer == "yes", nil
}

func preflightGitRepository(ctx context.Context, projectDir, remoteName, baseBranch string, allowBootstrap bool) (gitRepositoryPreflight, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return gitRepositoryPreflight{}, errors.New("Git is required before init can inspect the repository")
	}
	if info, err := os.Stat(projectDir); err != nil || !info.IsDir() {
		return gitRepositoryPreflight{}, fmt.Errorf("project directory is unavailable: %s", projectDir)
	}
	command := func(args ...string) (string, error) {
		return runInitGit(ctx, projectDir, args...)
	}
	root, err := command("rev-parse", "--show-toplevel")
	if err != nil {
		return gitRepositoryPreflight{}, fmt.Errorf("project directory must already be a Git repository: %w", err)
	}
	command = func(args ...string) (string, error) {
		return runInitGit(ctx, root, args...)
	}
	remoteURL, err := command("config", "--get", "remote."+remoteName+".url")
	if err != nil {
		return gitRepositoryPreflight{}, fmt.Errorf("configured Git remote %q is unavailable: %w", remoteName, err)
	}
	repository, err := github.RepositoryFromRemote(remoteURL)
	if err != nil {
		return gitRepositoryPreflight{}, fmt.Errorf("configured Git remote %q is not a GitHub repository: %w", remoteName, err)
	}
	remoteHeads, err := command("ls-remote", "--heads", remoteName)
	if err != nil {
		return gitRepositoryPreflight{}, fmt.Errorf("inspect branches on Git remote %q: %w", remoteName, err)
	}
	remoteBranches := parseRemoteBranches(remoteHeads)
	for _, branch := range remoteBranches {
		if branch == baseBranch {
			return gitRepositoryPreflight{Root: root, Repository: repository}, nil
		}
	}
	if len(remoteBranches) > 0 {
		return gitRepositoryPreflight{}, fmt.Errorf("configured base branch %q is unavailable on remote %q, which already contains branches (%s); create and push the intended base branch explicitly", baseBranch, remoteName, strings.Join(remoteBranches, ", "))
	}
	if !allowBootstrap {
		return gitRepositoryPreflight{}, fmt.Errorf("configured base branch %q does not exist because remote %q is empty; rerun init with --bootstrap-base-branch to push an existing local %s branch or initialize and push it, or initialize the branch yourself", baseBranch, remoteName, baseBranch)
	}

	bootstrap := &baseBranchBootstrap{Root: root, RemoteName: remoteName, BaseBranch: baseBranch}
	if localBase, localErr := command("rev-parse", "--verify", "refs/heads/"+baseBranch); localErr == nil && strings.TrimSpace(localBase) != "" {
		bootstrap.LocalBaseCommit = strings.TrimSpace(localBase)
		return gitRepositoryPreflight{Root: root, Repository: repository, Bootstrap: bootstrap}, nil
	}
	if _, headErr := command("rev-parse", "--verify", "HEAD"); headErr == nil {
		return gitRepositoryPreflight{}, fmt.Errorf("remote %q is empty, but local branch %q does not exist and the repository already has commits; create the intended base branch and push it explicitly", remoteName, baseBranch)
	}
	currentBranch, err := command("symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(currentBranch) == "" {
		return gitRepositoryPreflight{}, fmt.Errorf("remote %q and the local repository have no commits, but the current unborn branch cannot be identified; create and push branch %q explicitly", remoteName, baseBranch)
	}
	bootstrap.CurrentBranch = strings.TrimSpace(currentBranch)
	bootstrap.CreateInitialCommit = true
	return gitRepositoryPreflight{Root: root, Repository: repository, Bootstrap: bootstrap}, nil
}

func parseRemoteBranches(output string) []string {
	branches := make([]string, 0)
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(fields[1], "refs/heads/")
		if branch != "" && !seen[branch] {
			branches = append(branches, branch)
			seen[branch] = true
		}
	}
	sort.Strings(branches)
	return branches
}

func writeBaseBranchBootstrapPlan(bootstrap *baseBranchBootstrap, stdout io.Writer) {
	if bootstrap == nil {
		return
	}
	if bootstrap.CreateInitialCommit {
		fmt.Fprintf(stdout, "  Git base branch: create an empty initial commit on %s and push it to %s\n", bootstrap.BaseBranch, bootstrap.RemoteName)
		return
	}
	fmt.Fprintf(stdout, "  Git base branch: push existing local %s (%s) to %s\n", bootstrap.BaseBranch, bootstrap.LocalBaseCommit, bootstrap.RemoteName)
}

func applyBaseBranchBootstrap(ctx context.Context, bootstrap *baseBranchBootstrap, stdout io.Writer) error {
	if bootstrap == nil {
		return nil
	}
	writeProgress(stdout, fmt.Sprintf("Preparing and pushing the %s base branch to %s…", bootstrap.BaseBranch, bootstrap.RemoteName))
	if bootstrap.CreateInitialCommit {
		tree, err := runInitGit(ctx, bootstrap.Root, "mktree")
		if err != nil {
			return fmt.Errorf("create Git tree for initial base commit: %w", err)
		}
		commit, err := runInitGit(ctx, bootstrap.Root, "commit-tree", tree, "-m", "chore: initialize repository for Cortexium Runner")
		if err != nil {
			return fmt.Errorf("create initial commit for base branch %q: %w", bootstrap.BaseBranch, err)
		}
		if bootstrap.CurrentBranch != bootstrap.BaseBranch {
			if _, err := runInitGit(ctx, bootstrap.Root, "symbolic-ref", "HEAD", "refs/heads/"+bootstrap.BaseBranch); err != nil {
				return fmt.Errorf("name unborn base branch %q: %w", bootstrap.BaseBranch, err)
			}
		}
		if _, err := runInitGit(ctx, bootstrap.Root, "update-ref", "refs/heads/"+bootstrap.BaseBranch, commit); err != nil {
			return fmt.Errorf("record initial commit on base branch %q: %w", bootstrap.BaseBranch, err)
		}
		bootstrap.LocalBaseCommit = commit
	}
	refspec := "refs/heads/" + bootstrap.BaseBranch + ":refs/heads/" + bootstrap.BaseBranch
	if _, err := runInitGit(ctx, bootstrap.Root, "push", "--set-upstream", bootstrap.RemoteName, refspec); err != nil {
		return fmt.Errorf("push initialized base branch %q to %q: %w; the local branch and commit were retained so the push can be retried", bootstrap.BaseBranch, bootstrap.RemoteName, err)
	}
	if bootstrap.CreateInitialCommit {
		fmt.Fprintf(stdout, "Created empty initial commit %s on %s and pushed it to %s\n", bootstrap.LocalBaseCommit, bootstrap.BaseBranch, bootstrap.RemoteName)
	} else {
		fmt.Fprintf(stdout, "Pushed existing local base branch %s (%s) to %s\n", bootstrap.BaseBranch, bootstrap.LocalBaseCommit, bootstrap.RemoteName)
	}
	return nil
}

func runInitGit(ctx context.Context, directory string, args ...string) (string, error) {
	result, err := subprocess.RunOSFailClosedInput(ctx, "git", append([]string{"-C", directory}, args...), "", 2*time.Minute, nil, subprocess.GitStdoutLimit, subprocess.DiagnosticStderrLimit)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr))
	}
	return strings.TrimSpace(result.Stdout), nil
}

func workflowStatusNames(workflow config.WorkflowConfig) []string {
	preferred := []string{"needs_assessment", "backlog", "plan", "ready", "in_progress", "agent_qa", "pr_ready", "blocked", "done"}
	seen := map[string]bool{}
	result := make([]string, 0, len(workflow.Lanes))
	for _, id := range preferred {
		if lane, ok := workflow.Lanes[id]; ok {
			result = append(result, lane.Name)
			seen[id] = true
		}
	}
	extra := make([]string, 0)
	for id := range workflow.Lanes {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		result = append(result, workflow.Lanes[id].Name)
	}
	return result
}
