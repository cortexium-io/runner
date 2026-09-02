package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func runAdd(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: cortexium-runner add plan|ready [--config PATH] --title TEXT (--body TEXT|--body-file PATH) [--dry-run]")
		fmt.Fprintln(stdout, "Use plan for a planner proposal, or ready for one sufficiently specified implementation card.")
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(args[0]))
	if mode != "plan" && mode != "ready" {
		return fmt.Errorf("unknown add destination %q; use plan or ready", mode)
	}
	flags := newFlagSet("add "+mode, "cortexium-runner add plan|ready [--config PATH] --title TEXT (--body TEXT|--body-file PATH) [--dry-run]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	title := flags.String("title", "", "short card title")
	body := flags.String("body", "", "goal and constraints for Plan, or sufficient implementation detail for Ready")
	bodyFile := flags.String("body-file", "", "file containing the card body")
	dryRun := flags.Bool("dry-run", false, "show the destination without changing GitHub")
	proceed, err := parseFlags(flags, args[1:], "add "+mode)
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("add does not accept positional arguments after the destination")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		return fmt.Errorf("resolve runner config: %w", err)
	}
	targetStatus, action, err := humanWorkDestination(resolved, mode)
	if err != nil {
		return err
	}
	workTitle := strings.TrimSpace(*title)
	if workTitle == "" {
		return errors.New("--title is required")
	}
	workBody, err := resolveWorkItemBody(*body, *bodyFile)
	if err != nil {
		return err
	}
	if *dryRun {
		fmt.Fprintf(stdout, "Would add %q to %s in GitHub Project %s/%d. %s\n", terminalSafeText(workTitle), terminalSafeText(targetStatus), terminalSafeText(resolved.GitHubProject.Owner), resolved.GitHubProject.Number, action)
		return nil
	}
	project := github.NewProject(resolved.GitHubProject, nil)
	item, err := project.CreateHumanWorkItem(ctx, workTitle, workBody, targetStatus)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Added %s to %s. %s\n", terminalSafeText(item.ID), terminalSafeText(targetStatus), action)
	return nil
}

func humanWorkDestination(cfg config.RuntimeConfig, mode string) (status, action string, err error) {
	switch mode {
	case "plan":
		laneID := strings.TrimSpace(cfg.GitHubProject.InitialLaneID)
		status = strings.TrimSpace(cfg.GitHubProject.LaneStatuses[laneID])
		if status == "" || cfg.RoleContracts[cfg.GitHubProject.InitialRole] != config.WorkRolePlanner {
			return "", "", errors.New("workflow has no planner intake lane")
		}
		return status, "Runner will ask the planner to stage a dependency-aware proposal for review.", nil
	case "ready":
		status = strings.TrimSpace(cfg.GitHubProject.ReadyStatus)
		if status == "" {
			return "", "", errors.New("workflow has no implementer intake lane")
		}
		return status, "Runner will implement it when its declared dependencies have succeeded and resources are available.", nil
	default:
		return "", "", fmt.Errorf("unknown add destination %q; use plan or ready", mode)
	}
}

func resolveWorkItemBody(direct, filePath string) (string, error) {
	direct = strings.TrimSpace(direct)
	filePath = strings.TrimSpace(filePath)
	if direct != "" && filePath != "" {
		return "", errors.New("use either --body or --body-file, not both")
	}
	if direct != "" {
		return direct, nil
	}
	if filePath == "" {
		return "", errors.New("--body or --body-file is required")
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read work item body file: %w", err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("work item body file is empty")
	}
	return value, nil
}

func runPlan(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) (returnErr error) {
	flags := newFlagSet("plan", "cortexium-runner plan [--config PATH] [--idea TEXT|--idea-file PATH] [--small-tasks] [--create|--stage-only|--approve-staged FINGERPRINT]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	idea := flags.String("idea", "", "project idea and constraints; omit both idea flags for interactive multiline input")
	ideaFile := flags.String("idea-file", "", "file containing the project idea and constraints; omit both idea flags for interactive multiline input")
	smallTasks := flags.Bool("small-tasks", false, "plan smaller independently verifiable tasks for this run")
	create := flags.Bool("create", false, "create and approve the proposed cards after the preview")
	stageOnly := flags.Bool("stage-only", false, "stage the proposed cards unapproved for a separate review")
	approveStaged := flags.String("approve-staged", "", "approve and release the exact previously staged batch fingerprint")
	jsonOutput := flags.Bool("json", false, "write the plan as JSON")
	proceed, err := parseFlags(flags, args, "plan")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("plan does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	if *create && *stageOnly {
		return errors.New("use either --create to create and approve a plan or --stage-only to leave it unapproved, not both")
	}
	if (*create || *stageOnly) && strings.TrimSpace(*approveStaged) != "" {
		return errors.New("use --create or --stage-only for a new plan, or --approve-staged to release an already reviewed batch, not both")
	}
	approvingStaged := strings.TrimSpace(*approveStaged) != ""
	if approvingStaged && (strings.TrimSpace(*idea) != "" || strings.TrimSpace(*ideaFile) != "" || *smallTasks) {
		return errors.New("--approve-staged identifies an existing exact batch and cannot be combined with --idea, --idea-file, or --small-tasks")
	}
	if approvingStaged && (*jsonOutput || !isTerminalFile(stdin) || !isTerminalFile(stdout)) {
		return errors.New("--approve-staged requires an interactive terminal so the exact complete batch can be reviewed and explicitly accepted")
	}
	promptForIdea := !approvingStaged && strings.TrimSpace(*idea) == "" && strings.TrimSpace(*ideaFile) == "" && isTerminalFile(stdin) && isTerminalFile(stdout)
	if promptForIdea && *jsonOutput {
		return errors.New("interactive plan input cannot be combined with --json; use --idea, --idea-file, or pipe the idea on standard input")
	}
	var prompter *initPrompter
	ideaInput := stdin
	if promptForIdea {
		prompter = newInitPrompter(stdin, stdout)
		ideaInput = prompter.input
	}
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	planningSupport := ""
	if *smallTasks {
		planningSupport = config.PlanningSupportHigh
	}
	if err := applyPlanPlanningSupport(&cfg, planningSupport); err != nil {
		return err
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	if promptForIdea {
		if err := service.CheckProjectPlanningAvailability(ctx); err != nil {
			return err
		}
	}
	projectIdea := ""
	if !approvingStaged {
		projectIdea, err = resolvePlanIdea(*idea, *ideaFile, ideaInput, stdout, promptForIdea)
		if err != nil {
			return err
		}
	}
	projectLock, err := github.AcquireProcessLock(*cfg.GitHubProject)
	if err != nil {
		return err
	}
	defer func() {
		if err := projectLock.Release(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	if !approvingStaged {
		if err := service.CheckProjectPlanningAvailability(ctx); err != nil {
			return err
		}
	}
	if approvingStaged {
		approval, err := service.PlanStagedProjectPlanApproval(ctx, strings.TrimSpace(*approveStaged))
		if err != nil {
			return err
		}
		writeStagedProjectPlanSummary(stdout, approval)
		accepted, err := confirmStagedPlanApproval(newInitPrompter(stdin, stdout), approval)
		if err != nil {
			return err
		}
		if !accepted {
			fmt.Fprintln(stdout, "\nNo cards approved. The complete batch remains unapproved in staging.")
			return nil
		}
		writeProgress(stdout, "Revalidating and releasing the displayed complete staged batch…")
		released, err := service.ApplyProjectPlanApproval(ctx, approval)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\nApproved and released %d GitHub Project cards to %s.\n", len(released), terminalSafeApprovalText(approval.Destination))
		return nil
	}
	attachMetricsStore(service, cfg, stdout)
	if !*jsonOutput {
		writeProgress(stdout, "Planning your project. This may take a moment…")
	}
	plan, err := service.PlanProject(ctx, projectIdea)
	if err != nil {
		return err
	}
	if *jsonOutput && !*create {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(plan)
	}
	if !*jsonOutput {
		fmt.Fprintf(stdout, "%s\n\n", terminalSafeText(plan.GoalSummary))
		fmt.Fprintln(stdout, "Project success criteria:")
		for _, criterion := range plan.ProjectSuccessCriteria {
			fmt.Fprintf(stdout, "- %s\n", terminalSafeText(criterion))
		}
		if len(plan.ProjectConstraints) > 0 {
			fmt.Fprintln(stdout, "\nConstraints and non-goals:")
			for _, constraint := range plan.ProjectConstraints {
				fmt.Fprintf(stdout, "- %s\n", terminalSafeText(constraint))
			}
		}
		fmt.Fprintln(stdout, "\nProposed work:")
		writeProjectPlanItems(stdout, plan, terminalColorsEnabled(stdout))
		if len(plan.OpenDecisions) > 0 {
			fmt.Fprintf(stdout, "\nOpen decisions: %s\n", terminalSafeText(strings.Join(plan.OpenDecisions, "; ")))
		}
	}
	stageCards, releaseStaged := planApplyMode(*create, *stageOnly, false)
	if !stageCards && promptForIdea {
		if len(plan.OpenDecisions) > 0 {
			fmt.Fprintln(stdout, "\nPreview only. Answer the open decisions above in the project idea, then run plan again before staging cards.")
			return nil
		}
		stageCards, err = confirmPlanCreation(prompter, cfg, len(plan.WorkItems))
		if err != nil {
			return err
		}
		if !stageCards {
			fmt.Fprintln(stdout, "\nNo cards created. GitHub was left unchanged.")
			return nil
		}
		stageCards, releaseStaged = planApplyMode(false, false, true)
	}
	if !stageCards {
		fmt.Fprintln(stdout, "\nPreview only. Re-run with --create to create and approve these GitHub Project cards, or --stage-only to leave them staged for separate review.")
		return nil
	}
	cardLabel := "cards"
	if len(plan.WorkItems) == 1 {
		cardLabel = "card"
	}
	if !*jsonOutput {
		writeProgress(stdout, fmt.Sprintf("Staging %d unapproved GitHub Project %s…", len(plan.WorkItems), cardLabel))
	}
	staged, err := service.ApplyProjectPlan(ctx, plan)
	if err != nil {
		return err
	}
	if len(staged) == 0 || strings.TrimSpace(staged[0].PlanningBatchFingerprint) == "" {
		return errors.New("staged project plan did not return a batch fingerprint")
	}
	approval := engine.ProjectPlanApproval{
		BatchFingerprint: staged[0].PlanningBatchFingerprint,
		Destination:      staged[0].PlanningDestination,
		Children:         staged,
	}
	if releaseStaged {
		if !*jsonOutput {
			writeProgress(stdout, "Revalidating and releasing the complete staged batch…")
		}
		released, err := service.ApplyProjectPlanApproval(ctx, approval)
		if err != nil {
			return err
		}
		if *jsonOutput {
			encoder := json.NewEncoder(stdout)
			encoder.SetIndent("", "  ")
			return encoder.Encode(map[string]any{"plan": plan, "released": engine.ProjectPlanApproval{
				BatchFingerprint: approval.BatchFingerprint,
				Destination:      approval.Destination,
				Children:         released,
			}})
		}
		fmt.Fprintf(stdout, "\nCreated and approved %d GitHub Project %s in %s.\n", len(released), cardLabel, terminalSafeApprovalText(approval.Destination))
		return nil
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"plan": plan, "staged": approval})
	}
	writeStagedProjectPlanReceipt(stdout, approval, *configPath)
	return nil
}

func planApplyMode(create, stageOnly, interactiveAccepted bool) (stage, release bool) {
	if interactiveAccepted || create {
		return true, true
	}
	return stageOnly, false
}

func applyPlanPlanningSupport(cfg *config.Config, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !config.ValidPlanningSupport(value) {
		return errors.New("--planning-support must be standard or high")
	}
	for _, contract := range []string{config.WorkRoleImplementer, config.WorkRoleReviewer} {
		roleID := cfg.RoleIDForContract(contract)
		role, ok := cfg.Roles[roleID]
		if !ok {
			return fmt.Errorf("configured %s role %q is missing", contract, roleID)
		}
		role.PlanningSupport = value
		cfg.Roles[roleID] = role
	}
	return nil
}

func writeStagedProjectPlanReceipt(output io.Writer, approval engine.ProjectPlanApproval, configPath string) {
	writeStagedProjectPlanSummary(output, approval)
	fmt.Fprintf(
		output,
		"\nReview the staged batch and authorize its release with:\n  cortexium-runner plan --config %q --approve-staged %s\n",
		configPath,
		terminalSafeApprovalText(approval.BatchFingerprint),
	)
}

func writeStagedProjectPlanSummary(output io.Writer, approval engine.ProjectPlanApproval) {
	fmt.Fprintln(output, "\nRunner staged project plan")
	fmt.Fprintf(output, "  Batch fingerprint: %s\n", terminalSafeApprovalText(approval.BatchFingerprint))
	fmt.Fprintf(output, "  Destination after approval: %s\n", terminalSafeApprovalText(approval.Destination))
	fmt.Fprintf(output, "  Exact staged children: %d\n", len(approval.Children))
	for index, child := range approval.Children {
		fmt.Fprintf(output, "\n  %d. %s (%s)\n", index+1, terminalSafeApprovalText(child.Title), terminalSafeApprovalText(child.ID))
		if child.Repository != "" {
			fmt.Fprintf(output, "     Repository: %s\n", terminalSafeApprovalText(child.Repository))
		}
		fmt.Fprintf(output, "     Current status: %s\n", terminalSafeApprovalText(child.Status))
		if len(child.Dependencies) > 0 {
			fmt.Fprintf(output, "     Dependencies: %s\n", terminalSafeApprovalText(strings.Join(child.Dependencies, ", ")))
		}
	}
}

func confirmStagedPlanApproval(prompter *initPrompter, approval engine.ProjectPlanApproval) (bool, error) {
	return confirmStagedBatchApproval(prompter, len(approval.Children), approval.Destination, "complete staged plan approval confirmation is unavailable")
}

func writeProjectPlanItems(output io.Writer, plan engine.ProjectPlan, colors bool) {
	for index, item := range plan.WorkItems {
		if index > 0 {
			fmt.Fprintln(output)
		}
		number := terminalStyled(colors, toneQuestion, fmt.Sprintf("%d.", index+1))
		fmt.Fprintf(output, "%s %s\n   %s\n", number, terminalSafeText(item.Title), terminalSafeText(item.Summary))
		fmt.Fprintf(output, "   acceptance: %s\n", terminalSafeText(strings.Join(item.AcceptanceCriteria, "; ")))
		fmt.Fprintf(output, "   verification: %s\n", terminalSafeText(strings.Join(item.Verification, "; ")))
		if len(item.Risks) > 0 {
			fmt.Fprintf(output, "   risks: %s\n", terminalSafeText(strings.Join(item.Risks, "; ")))
		}
		if len(item.NonGoals) > 0 {
			fmt.Fprintf(output, "   not included: %s\n", terminalSafeText(strings.Join(item.NonGoals, "; ")))
		}
		if len(item.Dependencies) > 0 {
			fmt.Fprintf(output, "   depends on: %s\n", terminalSafeText(strings.Join(item.Dependencies, ", ")))
		}
	}
}

func confirmPlanCreation(prompter *initPrompter, cfg config.Config, cardCount int) (bool, error) {
	if prompter == nil || cfg.GitHubProject == nil {
		return false, errors.New("interactive plan creation confirmation is unavailable")
	}
	cardLabel := fmt.Sprintf("these %d proposed cards", cardCount)
	if cardCount == 1 {
		cardLabel = "this proposed card"
	}
	question := fmt.Sprintf(
		"Create and approve %s in GitHub Project #%d (%s)?",
		cardLabel,
		cfg.GitHubProject.Number,
		cfg.GitHubProject.Owner,
	)
	options := []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Stage the complete batch, revalidate it, then release it to its configured work lane."},
		{Label: "No", Value: "no", Description: "Keep the preview and leave GitHub unchanged."},
	}
	selected, err := prompter.selectMenu(question, options, 0)
	if err != nil {
		return false, err
	}
	return options[selected].Value == "yes", nil
}

func resolvePlanIdea(direct, filePath string, stdin io.Reader, stdout io.Writer, prompt bool) (string, error) {
	direct = strings.TrimSpace(direct)
	filePath = strings.TrimSpace(filePath)
	if direct != "" && filePath != "" {
		return "", errors.New("use either --idea or --idea-file, not both")
	}
	if direct != "" {
		return direct, nil
	}
	if filePath != "" {
		data, err := os.ReadFile(filePath)
		if err != nil {
			return "", fmt.Errorf("read project idea file: %w", err)
		}
		idea := strings.TrimSpace(string(data))
		if idea == "" {
			return "", errors.New("project idea file is empty")
		}
		return idea, nil
	}
	return readMultilinePlanIdea(stdin, stdout, prompt)
}

func readMultilinePlanIdea(stdin io.Reader, stdout io.Writer, prompt bool) (string, error) {
	if prompt {
		fmt.Fprintln(stdout, styleForOutput(stdout, toneQuestion, "Project idea"))
		fmt.Fprintln(stdout, "Describe what you want to build, including useful constraints and acceptance criteria.")
		fmt.Fprintln(stdout, multilinePlanIdeaInstruction(terminalColorsEnabled(stdout)))
	}

	reader, ok := stdin.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReader(stdin)
	}
	lines := make([]string, 0, 8)
	for {
		if prompt {
			fmt.Fprint(stdout, "> ")
		}
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read project idea: %w", err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line != "" || !errors.Is(err, io.EOF) {
			lines = append(lines, line)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if prompt {
		fmt.Fprintln(stdout)
	}
	idea := strings.TrimSpace(strings.Join(lines, "\n"))
	if idea == "" {
		return "", errors.New("project idea is empty; enter it interactively, pipe it on standard input, or use --idea/--idea-file")
	}
	return idea, nil
}

func multilinePlanIdeaInstruction(colors bool) string {
	return "Enter as many lines as needed. When finished, press " + terminalStyled(colors, toneQuestion, "Ctrl-D") + " at the empty prompt."
}

func runApprove(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlagSet("approve", "cortexium-runner approve [--config PATH] --item ID|URL [--dry-run]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	selector := flags.String("item", "", "GitHub Project item id or issue URL")
	dryRun := flags.Bool("dry-run", false, "preview the exact approval without changing GitHub")
	jsonOutput := flags.Bool("json", false, "write the approval preview as JSON without changing GitHub")
	proceed, err := parseFlags(flags, args, "approve")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("approve does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	if strings.TrimSpace(*selector) == "" {
		return errors.New("approve requires --item ID|URL")
	}
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	if !*jsonOutput {
		writeProgress(stdout, "Loading and verifying the selected Project item…")
	}
	plan, err := service.PlanProjectItemApproval(ctx, *selector)
	if err != nil {
		return err
	}
	if *jsonOutput {
		payload := map[string]any{"applied": false, "approval": plan}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	if plan.Batch != nil {
		writeBatchApprovalPreview(stdout, *plan.Batch)
		if *dryRun {
			fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run to approve and release this complete batch.")
			return nil
		}
		if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
			return errors.New("complete planning batch approval requires an interactive terminal so the exact displayed batch can be explicitly accepted")
		}
		accepted, err := confirmBatchApproval(newInitPrompter(stdin, stdout), *plan.Batch)
		if err != nil {
			return err
		}
		if !accepted {
			fmt.Fprintln(stdout, "\nNo cards approved. The complete batch remains unapproved in staging.")
			return nil
		}
		writeProgress(stdout, "Revalidating and releasing the displayed complete planning batch…")
		approved, err := service.ApplyProjectItemApproval(ctx, plan)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "\nApproved %d staged children and moved the planning source %s to %s.\n", len(plan.Batch.Children), terminalSafeApprovalText(approved.ID), terminalSafeApprovalText(approved.Status))
		return nil
	}
	label := strings.TrimSpace(cfg.GitHubProject.IntakeLabel)
	if label == "" {
		label = "needs-assessment"
	}
	writeItemApprovalPreview(stdout, plan, label)
	if *dryRun {
		fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run to approve this item.")
		return nil
	}
	writeProgress(stdout, "Recording the displayed approval and updating the Project item…")
	approved, err := service.ApplyProjectItemApproval(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nApproved %s and moved it to %s.\n", terminalSafeApprovalText(approved.ID), terminalSafeApprovalText(approved.Status))
	return nil
}

func writeItemApprovalPreview(output io.Writer, plan github.ApprovalPlan, intakeLabel string) {
	fmt.Fprintln(output, "Runner approval")
	fmt.Fprintf(output, "  Item: %s (%s)\n", terminalSafeApprovalText(plan.Item.Title), terminalSafeApprovalText(plan.Item.ID))
	fmt.Fprintf(output, "  Current status: %s\n", terminalSafeApprovalText(plan.Item.Status))
	if plan.Item.Repository != "" {
		fmt.Fprintf(output, "  Repository: %s\n", terminalSafeApprovalText(plan.Item.Repository))
	}
	if plan.Item.URL != "" {
		fmt.Fprintf(output, "  Source URL: %s\n", terminalSafeApprovalText(plan.Item.URL))
	}
	if len(plan.Item.Dependencies) > 0 {
		fmt.Fprintf(output, "  Dependencies: %s\n", terminalSafeApprovalText(strings.Join(plan.Item.Dependencies, ", ")))
	}
	if strings.TrimSpace(plan.Item.Body) != "" {
		fmt.Fprintln(output, "  Exact source body:")
		for _, line := range strings.Split(strings.TrimSpace(plan.Item.Body), "\n") {
			fmt.Fprintf(output, "    %s\n", terminalSafeApprovalText(line))
		}
	}
	fmt.Fprintf(output, "  Role: %s\n", terminalSafeApprovalText(plan.Role))
	fmt.Fprintf(output, "  Authenticated assertion: %s\n", terminalSafeApprovalText(plan.Assertion))
	if plan.RemoveIntakeLabel {
		fmt.Fprintf(output, "  Public intake label: remove %s\n", terminalSafeApprovalText(intakeLabel))
	}
}

func writeBatchApprovalPreview(output io.Writer, batch github.BatchApprovalPlan) {
	fmt.Fprintln(output, "Runner complete planning batch approval")
	fmt.Fprintf(output, "  Planning source: %s (%s)\n", terminalSafeApprovalText(batch.Source.Title), terminalSafeApprovalText(batch.Source.ID))
	fmt.Fprintf(output, "  Authenticated staging provenance: %s\n", terminalSafeApprovalText(batch.Source.Approval))
	fmt.Fprintf(output, "  Destination: %s\n", terminalSafeApprovalText(batch.Destination))
	fmt.Fprintf(output, "  Exact staged children: %d\n", len(batch.Children))
	for index, child := range batch.Children {
		fmt.Fprintf(output, "\n  %d. %s (%s)\n", index+1, terminalSafeApprovalText(child.Item.Title), terminalSafeApprovalText(child.Item.ID))
		if child.Item.Repository != "" {
			fmt.Fprintf(output, "     Repository: %s\n", terminalSafeApprovalText(child.Item.Repository))
		}
		writeAuthorizationBoundRuntimePreview(output, child.Item)
		fmt.Fprintf(output, "     Destination: %s\n", terminalSafeApprovalText(batch.Destination))
		fmt.Fprintf(output, "     Role: %s\n", terminalSafeApprovalText(child.Role))
		fmt.Fprintf(output, "     Authenticated assertion: %s\n", terminalSafeApprovalText(child.Assertion))
		fmt.Fprintln(output, "     Exact source body:")
		for _, line := range strings.Split(strings.TrimSpace(child.Item.Body), "\n") {
			fmt.Fprintf(output, "       %s\n", terminalSafeApprovalText(line))
		}
	}
}

func confirmBatchApproval(prompter *initPrompter, batch github.BatchApprovalPlan) (bool, error) {
	return confirmStagedBatchApproval(prompter, len(batch.Children), batch.Destination, "complete planning batch approval confirmation is unavailable")
}

func confirmStagedBatchApproval(prompter *initPrompter, childCount int, destination, unavailableMessage string) (bool, error) {
	destination = strings.TrimSpace(destination)
	if prompter == nil || childCount == 0 || destination == "" {
		return false, errors.New(unavailableMessage)
	}
	question := fmt.Sprintf("Approve all %d exact staged cards and release them together to %s?", childCount, destination)
	options := []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Authorize and release the complete displayed batch."},
		{Label: "No", Value: "no", Description: "Leave every card unapproved in staging."},
	}
	selected, err := prompter.selectMenu(question, options, 1)
	if err != nil {
		return false, err
	}
	return options[selected].Value == "yes", nil
}

// terminalSafeApprovalText preserves reviewable content while making terminal
// control and Unicode formatting characters visible instead of executable.
func terminalSafeApprovalText(value string) string {
	return terminalSafeText(value)
}

func writeAuthorizationBoundRuntimePreview(output io.Writer, item github.WorkItem) {
	empty := func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "(empty)"
		}
		return terminalSafeApprovalText(value)
	}
	fmt.Fprintf(output, "     Current status: %s\n", empty(item.Status))
	fmt.Fprintf(output, "     Result: %s\n", empty(item.Result))
	fmt.Fprintf(output, "     Phase: %s\n", empty(item.Phase))
	fmt.Fprintf(output, "     Activity: %s\n", empty(item.Activity))
	fmt.Fprintf(output, "     QA failures: %d\n", item.QAFailures)
	fmt.Fprintf(output, "     Branch: %s\n", empty(item.Branch))
	fmt.Fprintf(output, "     Pull request: %s\n", empty(item.PullRequest))
	fmt.Fprintf(output, "     QA commit: %s\n", empty(item.QACommit))
}

func runRetry(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlagSet("retry", "cortexium-runner retry [--config PATH] [--item ID|URL|TITLE] [--feedback TEXT] [--dry-run]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	selector := flags.String("item", "", "blocked GitHub Project item id, URL, or exact title; omit in a terminal to choose")
	feedback := flags.String("feedback", "", "replace stale retry feedback and reset the QA failure count")
	dryRun := flags.Bool("dry-run", false, "preview the retry destination without changing GitHub")
	jsonOutput := flags.Bool("json", false, "write the retry plan as JSON")
	proceed, err := parseFlags(flags, args, "retry")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("retry does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadTrustedConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	selected := strings.TrimSpace(*selector)
	if selected == "" {
		if !*jsonOutput {
			writeProgress(stdout, "Loading retryable blocked work…")
		}
		selected, err = chooseRetryItem(ctx, stdin, stdout, cfg, service)
		if err != nil {
			return err
		}
	}
	if !*jsonOutput {
		writeProgress(stdout, "Checking the selected blocked item and its retry destination…")
	}
	var plan github.RetryPlan
	if strings.TrimSpace(*feedback) == "" {
		plan, err = service.PlanProjectItemRetry(ctx, selected)
	} else {
		plan, err = service.PlanProjectItemRetryWithFeedback(ctx, selected, *feedback)
	}
	if err != nil {
		return err
	}
	var retried *github.WorkItem
	if !*dryRun {
		if !*jsonOutput {
			writeProgress(stdout, "Returning the item to its recorded Runner lane…")
		}
		item, applyErr := service.ApplyProjectItemRetry(ctx, plan)
		if applyErr != nil {
			return applyErr
		}
		retried = &item
	}
	if *jsonOutput {
		payload := map[string]any{"applied": !*dryRun, "retry": plan}
		if retried != nil {
			payload["item"] = retried
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(payload)
	}
	fmt.Fprintln(stdout, "Runner retry")
	fmt.Fprintf(stdout, "  Item: %s (%s)\n", terminalSafeText(plan.Item.Title), terminalSafeText(plan.Item.ID))
	fmt.Fprintf(stdout, "  Current status: %s\n", terminalSafeText(plan.Item.Status))
	fmt.Fprintf(stdout, "  Retry destination: %s\n", terminalSafeText(plan.TargetStatus))
	if plan.FeedbackOverride != "" {
		fmt.Fprintf(stdout, "  Replacement feedback: %s\n", terminalSafeText(plan.FeedbackOverride))
		fmt.Fprintln(stdout, "  QA failures: reset to 0")
	}
	if *dryRun {
		fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run to retry this item.")
		return nil
	}
	fmt.Fprintf(stdout, "\nMoved the item to %s. A running Runner will check it on its next poll.\n", terminalSafeText(retried.Status))
	return nil
}

func chooseRetryItem(ctx context.Context, stdin io.Reader, stdout io.Writer, cfg config.Config, service *engine.Engine) (string, error) {
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return "", errors.New("retry requires --item ID|URL|TITLE when input or output is not a terminal")
	}
	status, err := service.WorkStatus(ctx)
	if err != nil {
		return "", fmt.Errorf("load blocked work: %w", err)
	}
	resolved, err := cfg.Resolve()
	if err != nil {
		return "", err
	}
	items := make([]github.WorkItem, 0, len(status.Blocked))
	options := make([]initMenuOption, 0, len(status.Blocked))
	for _, item := range status.Blocked {
		target := resolved.LaneStatus(item.Phase)
		if strings.TrimSpace(target) == "" {
			continue
		}
		items = append(items, item)
		options = append(options, initMenuOption{Label: item.Title, Description: "Return to " + target})
	}
	if len(items) == 0 {
		return "", errors.New("no blocked item has a recorded Runner retry destination")
	}
	prompter := newInitPrompter(stdin, stdout)
	index, err := prompter.selectMenu("Blocked item to retry", options, 0)
	if err != nil {
		return "", err
	}
	return items[index].ID, nil
}
