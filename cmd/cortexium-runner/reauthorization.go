package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cortexium-io/runner/internal/engine"
)

func runRetryReauthorization(ctx context.Context, service *engine.Engine, selector string, dryRun, jsonOutput bool, stdin io.Reader, stdout io.Writer) error {
	plan, err := service.PlanProjectItemReauthorization(ctx, selector)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"applied": false, "reauthorization": plan})
	}
	writeReauthorizationPreview(stdout, plan)
	if dryRun {
		fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run in a terminal to review and confirm reauthorization.")
		return nil
	}
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return errors.New("reauthorization requires an interactive terminal to confirm the exact card and retained action state")
	}
	selected, err := newInitPrompter(stdin, stdout).selectMenu(
		"Reauthorize this exact retained implementation and retry it?",
		[]initMenuOption{
			{Label: "Yes", Value: "yes", Description: "Authorize only this card; preserve its worktree, private feedback, and QA failure count."},
			{Label: "No", Value: "no", Description: "Leave the card unchanged in assessment."},
		}, 1,
	)
	if err != nil {
		return err
	}
	if selected != 0 {
		fmt.Fprintln(stdout, "\nNo changes made. The card remains in assessment.")
		return nil
	}
	item, err := service.ApplyProjectItemReauthorization(ctx, plan)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nReauthorized %s and moved it to %s. Existing work and QA failures were preserved.\n", terminalSafeText(item.ID), terminalSafeText(item.Status))
	return nil
}

func writeReauthorizationPreview(output io.Writer, plan engine.ProjectItemReauthorization) {
	item := plan.Approval.Item
	fmt.Fprintln(output, "Runner retained-implementation reauthorization")
	fmt.Fprintf(output, "  Item: %s (%s)\n  Repository: %s\n  Source URL: %s\n", terminalSafeText(item.Title), terminalSafeText(item.ID), terminalSafeText(item.Repository), terminalSafeText(item.URL))
	fmt.Fprintf(output, "  Destination: %s\n  Role: %s\n  Implementation profile: %s\n", terminalSafeText(plan.Approval.TargetStatus), terminalSafeText(plan.Approval.Role), terminalSafeText(item.ImplementationProfile))
	fmt.Fprintf(output, "  Dependencies: %s\n  Retained worktree: %s\n", terminalSafeText(strings.Join(item.Dependencies, ", ")), terminalSafeText(plan.Workspace.WorktreePath))
	writeAuthorizationBoundRuntimePreview(output, item)
	fmt.Fprintln(output, "  Exact source body:")
	for _, line := range strings.Split(item.Body, "\n") {
		fmt.Fprintf(output, "    %s\n", terminalSafeText(line))
	}
	fmt.Fprintln(output, "  Only this card returns to implementation; no QA acceptance or batch approval is granted.")
	fmt.Fprintln(output, "  Result becomes: Operator reauthorized the retained implementation and requested a retry.")
}
