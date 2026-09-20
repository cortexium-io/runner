package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
)

func runAmend(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlagSet("amend", "cortexium-runner amend --config PATH --item ID|URL --body-file PATH [--dry-run|--json]", stdout)
	configPath := flags.String("config", "", "trusted operator config path")
	selector := flags.String("item", "", "previously approved unpublished card")
	bodyFile := flags.String("body-file", "", "complete replacement requirements, retaining Runner planning metadata and dependencies")
	dryRun := flags.Bool("dry-run", false, "preview without changing requirements or workspace")
	jsonOutput := flags.Bool("json", false, "preview only as JSON; never grants approval")
	proceed, err := parseFlags(flags, args, "amend")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*selector) == "" || strings.TrimSpace(*bodyFile) == "" {
		return errors.New("amend requires --item and --body-file and accepts no positional arguments")
	}
	file, err := os.Open(*bodyFile)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(file, 60_001))
	file.Close()
	if err != nil {
		return err
	}
	cfg, err := config.LoadTrustedConfig(resolveRunnerConfigPath(*configPath, ""))
	if err != nil {
		return err
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	plan, err := service.PlanRequirementAmendment(ctx, *selector, string(body))
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"applied": false, "amendment": plan})
	}
	writeAmendmentPreview(stdout, plan)
	if *dryRun {
		return nil
	}
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return errors.New("amendment requires an interactive terminal to confirm the exact old and new requirements and candidate")
	}
	choice, err := newInitPrompter(stdin, stdout).selectMenu("Approve these exact amended requirements?", []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Replace this card's requirements; retain candidate and QA count; leave it Blocked for explicit retry."},
		{Label: "No", Value: "no", Description: "Leave requirements and candidate unchanged."},
	}, 1)
	if err != nil {
		return err
	}
	if choice != 0 {
		fmt.Fprintln(stdout, "No changes made.")
		return nil
	}
	item, err := service.ApplyRequirementAmendment(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Amended %s. Candidate preserved; QA failures remain %d. Card is Blocked; review then use ordinary retry.\n", terminalSafeText(item.ID), item.QAFailures)
	return nil
}

func writeAmendmentPreview(output io.Writer, plan engine.RequirementAmendment) {
	fmt.Fprintf(output, "Runner requirement amendment\nItem: %s\nRepository: %s\nCandidate: %s\nWorktree: %s\nRetained QA failures: %d\nRetry lane: %s\n",
		terminalSafeText(plan.Approval.Item.ID), terminalSafeText(plan.Approval.Item.Repository), terminalSafeText(plan.Candidate.Head), terminalSafeText(plan.Workspace.WorktreePath), plan.Approval.Item.QAFailures, terminalSafeText(plan.Approval.Item.Phase))
	fmt.Fprintln(output, "\nPrevious approved body:")
	for _, line := range strings.Split(plan.Approval.Item.Body, "\n") {
		fmt.Fprintln(output, terminalSafeText(line))
	}
	fmt.Fprintln(output, "\nReplacement approved body:")
	for _, line := range strings.Split(plan.Approval.Body, "\n") {
		fmt.Fprintln(output, terminalSafeText(line))
	}
	fmt.Fprintln(output, "\nRunner must be stopped. No work starts, no QA rejection count resets, and no existing QA acceptance or proof is transferred to the new requirements.")
}
