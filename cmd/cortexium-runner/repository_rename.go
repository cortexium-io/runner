package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
)

func runRepositoryRename(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	flags := newFlagSet("recover-repository-rename", "cortexium-runner recover-repository-rename [--config PATH] --item ID|URL --from OWNER/OLD_NAME [--dry-run|--json]", stdout)
	configPath := flags.String("config", "", "trusted operator configuration using the canonical new repository name")
	selector := flags.String("item", "", "completed planning parent or any member of its complete released batch")
	from := flags.String("from", "", "original owner/repository name authenticated by the released planning batch")
	dryRun := flags.Bool("dry-run", false, "preview exact recovery without changing GitHub or private records")
	jsonOutput := flags.Bool("json", false, "emit a read-only JSON preview")
	proceed, err := parseFlags(flags, args, "recover-repository-rename")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*selector) == "" || strings.TrimSpace(*from) == "" {
		return errors.New("repository rename recovery requires --item and --from and accepts no positional arguments")
	}
	cfg, err := config.LoadTrustedConfig(resolveRunnerConfigPath(*configPath, ""))
	if err != nil {
		return err
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	plan, err := service.PlanRepositoryRename(ctx, strings.TrimSpace(*selector), strings.TrimSpace(*from))
	if err != nil {
		return err
	}
	return reviewRepositoryRename(plan, *dryRun, *jsonOutput, stdin, stdout, func() error {
		return service.ApplyRepositoryRename(ctx, plan)
	})
}

func reviewRepositoryRename(plan engine.RepositoryRenameRecovery, dryRun, jsonOutput bool, stdin io.Reader, stdout io.Writer, apply func() error) error {
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"applied": false, "recovery": plan})
	}
	writeRepositoryRenamePreview(stdout, plan)
	if dryRun {
		fmt.Fprintln(stdout, "\nDry run only. Re-run without --dry-run in a terminal to confirm this exact complete-batch recovery.")
		return nil
	}
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return errors.New("repository rename recovery requires an interactive terminal to confirm the exact complete batch and retained workspaces; use --dry-run or --json for inspection")
	}
	accepted, err := confirmRepositoryRename(newInitPrompter(stdin, stdout))
	if err != nil {
		return err
	}
	if !accepted {
		fmt.Fprintln(stdout, "\nNo changes made.")
		return nil
	}
	if err := apply(); err != nil {
		return fmt.Errorf("repository rename recovery did not finish; preserve the private journal and rerun this command to preview and explicitly resume: %w", err)
	}
	fmt.Fprintf(stdout, "\nRecovered %d original cards to implementation. Retained work and rejection counts were preserved; fresh verification and independent review are required.\n", len(plan.Batch.Children))
	return nil
}

func confirmRepositoryRename(prompter *initPrompter) (bool, error) {
	selected, err := prompter.selectMenu("Renew authority for this exact complete batch after the verified repository rename?", []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Resume implementation with retained bytes and history; require fresh candidate verification and independent review."},
		{Label: "No", Value: "no", Description: "Leave all cards and private records unchanged."},
	}, 1)
	return selected == 0 && err == nil, err
}

func writeRepositoryRenamePreview(output io.Writer, plan engine.RepositoryRenameRecovery) {
	fmt.Fprintln(output, "Runner complete-batch repository rename recovery")
	fmt.Fprintf(output, "  Repository: %s -> %s\n  Immutable identity: %d / %s\n  Planning parent: %s (%s)\n  Retained history journal: %s\n",
		terminalSafeText(plan.Batch.From), terminalSafeText(plan.Batch.To), plan.Batch.Repository.ID, terminalSafeText(plan.Batch.Repository.NodeID),
		terminalSafeText(plan.Batch.Source.Title), terminalSafeText(plan.Batch.Source.ID), terminalSafeText(plan.Journal))
	fmt.Fprintln(output, "  Exact preserved planning request:")
	for _, line := range strings.Split(plan.Batch.Source.Body, "\n") {
		fmt.Fprintf(output, "    %s\n", terminalSafeText(line))
	}
	for _, child := range plan.Batch.Children {
		fmt.Fprintf(output, "\n  Card %d/%d: %s (%s)\n  Issue: %s\n  Dependencies: %s\n  Destination: %s\n  Branch field: %s\n  QA rejections: %d\n  Preserved planning source: %s\n",
			child.Before.PlanningItemIndex, child.Before.PlanningBatchSize, terminalSafeText(child.Before.Title), terminalSafeText(child.Before.ID),
			terminalSafeText(child.Before.URL), terminalSafeText(strings.Join(child.Before.Dependencies, ", ")), terminalSafeText(child.After.Status),
			terminalSafeText(child.Before.Branch), child.Before.QAFailures, terminalSafeText(child.Before.PlanningSourceFingerprint))
		fmt.Fprintln(output, "  Exact preserved body and planning provenance:")
		for _, line := range strings.Split(child.Before.Body, "\n") {
			fmt.Fprintf(output, "    %s\n", terminalSafeText(line))
		}
	}
	for _, retained := range plan.Workspaces {
		fmt.Fprintf(output, "\n  Retained item: %s\n  Worktree: %s\n  Recorded branch: %s\n  HEAD: %s\n  Tree: %s\n  Clean: %t\n  Byte fingerprint: %s\n  Original content: %s\n  Renewed content: %s\n",
			terminalSafeText(retained.ItemID), terminalSafeText(retained.Before.WorktreePath), terminalSafeText(retained.Before.Branch),
			terminalSafeText(retained.Snapshot.Head), terminalSafeText(retained.Snapshot.Tree), retained.Snapshot.Clean, terminalSafeText(retained.Snapshot.Fingerprint),
			terminalSafeText(retained.Before.DelegatedContentDigest), terminalSafeText(retained.After.DelegatedContentDigest))
	}
	fmt.Fprintln(output, "\n  Bodies, planning provenance, dependencies, candidate bytes, branches, history and rejection counts are preserved. A signed batch-local rename binding renews execution authority.")
	fmt.Fprintln(output, "  All children return to implementation. No QA acceptance, publication, merge or delivered outcome is granted.")
}
