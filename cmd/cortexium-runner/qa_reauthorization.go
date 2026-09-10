package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/execution"
)

func runQAReauthorization(ctx context.Context, service *engine.Engine, selector string, dryRun, jsonOutput bool, stdin io.Reader, stdout io.Writer) error {
	plan, err := service.PlanQAReauthorization(ctx, selector)
	if err != nil {
		return err
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"applied": false, "qa_only": plan})
	}
	writeQAReauthorizationPreview(stdout, plan)
	if dryRun {
		fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run in a terminal to review and confirm this one QA attempt.")
		return nil
	}
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return errors.New("QA-only reauthorization requires an interactive terminal to confirm the exact candidate and current requirements")
	}
	selected, err := newInitPrompter(stdin, stdout).selectMenu("Authorize ONE QA attempt for this exact candidate and current review context?", []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Review once; leave the card paused regardless of verdict. No implementation, publication, merge, or automatic retry."},
		{Label: "No", Value: "no", Description: "Leave all work and approvals unchanged."},
	}, 1)
	if err != nil {
		return err
	}
	if selected != 0 {
		fmt.Fprintln(stdout, "\nNo changes made.")
		return nil
	}
	result, runErr := service.RunQAReauthorization(ctx, plan)
	// Print the complete candidate-bound result locally; never publish raw QA
	// findings or provider diagnostics to GitHub from this operator-only path.
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return errors.Join(runErr, err)
	}
	fmt.Fprintln(stdout, terminalSafeText(string(encoded)))
	fmt.Fprintln(stdout, "\nQA-only authorization is consumed. The card, approval, PR, retained evidence, and rejection count remain unchanged. A new attempt requires a new preview and confirmation.")
	if runErr != nil {
		return runErr
	}
	if result.Outcome != execution.OutcomeSucceeded {
		return errors.New("one-shot QA did not accept the candidate; no further work was authorized")
	}
	return nil
}

func writeQAReauthorizationPreview(output io.Writer, plan engine.QAReauthorizationPlan) {
	item := plan.Item
	fmt.Fprintln(output, "Runner retained-candidate QA-only reauthorization")
	fmt.Fprintf(output, "  Item: %s (%s)\n  Source: %s\n  Repository: %s\n  Reviewer profile: %s\n", terminalSafeText(item.Title), terminalSafeText(item.ID), terminalSafeText(item.URL), terminalSafeText(item.Repository), terminalSafeText(plan.Role))
	fmt.Fprintf(output, "  Retained worktree: %s\n  Candidate: %s\n  Tree: %s\n  Retained comparison base: %s\n  Current PR base: %s\n", terminalSafeText(plan.Workspace.WorktreePath), plan.Candidate.CommitOID, plan.Candidate.TreeOID, plan.Workspace.BaseRevision, terminalSafeText(plan.PullRequest.BaseRefOID))
	writeAuthorizationBoundRuntimePreview(output, item)
	fmt.Fprintf(output, "  Original retained content identity: %s\n  Current review content identity: %s\n", terminalSafeText(plan.Workspace.DelegatedContentDigest), plan.CurrentContentDigest)
	if plan.Workspace.DelegatedContentDigest != plan.CurrentContentDigest {
		fmt.Fprintln(output, "  CONTENT CHANGED: review the complete current body below. Historical feedback is retained; old proof and acceptance are not rebound to the new requirements. The old body is not stored in the workspace identity; inspect your source history if a textual comparison is needed.")
	}
	for _, reference := range plan.References {
		fmt.Fprintf(output, "  Reference: %s at %s (%s)\n", terminalSafeText(reference.Name), reference.Commit, terminalSafeText(reference.Path))
	}
	fmt.Fprintln(output, "  Exact current source body:")
	for _, line := range strings.Split(item.Body, "\n") {
		fmt.Fprintf(output, "    %s\n", terminalSafeText(line))
	}
	for _, comment := range plan.Comments {
		fmt.Fprintf(output, "  Historical human comment:\n%s\n", terminalSafeText(comment))
	}
	for _, feedback := range plan.Feedback {
		fmt.Fprintf(output, "  Historical QA finding: %s\n", terminalSafeText(feedback))
	}
	fmt.Fprintln(output, "  This authorizes only review of the displayed candidate and requirements, including its configured local verification capabilities. The card remains paused. No implementation, base refresh, publication, merge, sibling work, or automatic retry is authorized.")
}
