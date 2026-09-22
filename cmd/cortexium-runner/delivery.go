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
	"github.com/cortexium-io/runner/internal/github"
)

func runDelivery(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, "Usage: cortexium-runner delivery migrate --config PATH --entrypoint ID [--dry-run|--json]")
		fmt.Fprintln(stdout, "       cortexium-runner delivery cancel --config PATH --item ID|URL [--dry-run|--json]")
		fmt.Fprintln(stdout, "Preview exact operator changes; applying requires interactive confirmation and graceful quiescence. Neither command stops or starts Runner.")
		return nil
	}
	mode := args[0]
	if mode != "migrate" && mode != "cancel" {
		return errors.New("delivery supports migrate or cancel")
	}
	flags := newFlagSet("delivery "+mode, "cortexium-runner delivery "+mode+" --config PATH [--entrypoint ID|--item ID] [--dry-run|--json]", stdout)
	path := flags.String("config", "", "existing trusted operator configuration")
	entry := flags.String("entrypoint", "", "migrate: existing reviewed complete-verification catalog ID")
	item := flags.String("item", "", "cancel: exact approved unpublished plan parent")
	dry := flags.Bool("dry-run", false, "show exact changes without mutation")
	jsonOutput := flags.Bool("json", false, "preview only as JSON; never applies changes")
	proceed, err := parseFlags(flags, args[1:], "delivery "+mode)
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 || mode == "migrate" && (strings.TrimSpace(*entry) == "" || *item != "") || mode == "cancel" && (strings.TrimSpace(*item) == "" || *entry != "") {
		return errors.New("delivery migrate requires only --entrypoint; delivery cancel requires only --item; no positional arguments")
	}
	*path = resolveRunnerConfigPath(*path, "")
	cfg, err := config.LoadTrustedConfig(*path)
	if err != nil {
		return err
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return err
	}
	var preview any
	var apply func() error
	if mode == "migrate" {
		plan, err := service.PlanDeliveryMigration(ctx, *path, *entry)
		if err != nil {
			return err
		}
		preview = plan
		apply = func() error { return service.ApplyDeliveryMigration(ctx, plan) }
		if !*jsonOutput {
			writeDeliveryMigrationPreview(stdout, plan)
		}
	} else {
		plan, err := service.PlanDeliveryCancellation(ctx, *item)
		if err != nil {
			return err
		}
		preview = plan
		apply = func() error { _, err := service.ApplyDeliveryCancellation(ctx, plan); return err }
		if !*jsonOutput {
			writeDeliveryCancellationPreview(stdout, plan)
		}
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(map[string]any{"applied": false, "operation": mode, "preview": preview})
	}
	if *dry {
		return nil
	}
	if !isTerminalFile(stdin) || !isTerminalFile(stdout) {
		return errors.New("delivery changes require an interactive terminal to confirm this exact preview")
	}
	choice, err := newInitPrompter(stdin, stdout).selectMenu("Apply this exact delivery change?", []initMenuOption{
		{Label: "Yes", Value: "yes", Description: "Revalidate this exact preview and apply only while Runner and standalone operations are quiescent."},
		{Label: "No", Value: "no", Description: "Leave configuration and Project unchanged."},
	}, 1)
	if err != nil {
		return err
	}
	if choice != 0 {
		fmt.Fprintln(stdout, "No changes made.")
		return nil
	}
	if err := apply(); err != nil {
		return err
	}
	if mode == "migrate" {
		fmt.Fprintln(stdout, "Plan delivery enabled for new plans. Existing history unchanged. No service started; run normal Doctor, then restore the existing service when ready.")
	} else {
		fmt.Fprintln(stdout, "Plan cancelled: new admission fenced; members, branches, proof and rejection counts retained. No PR closed and no service started.")
	}
	return nil
}

func writeDeliveryMigrationPreview(out io.Writer, plan engine.DeliveryMigration) {
	fmt.Fprintf(out, "Delivery migration\nProject: %s/%d (%s)\nConfig: %s\nConfig snapshot: %s\nProject snapshot: %s\nCreate Runner Plan Release TEXT: %t\n",
		terminalSafeText(plan.Project.Owner), plan.Project.Number, terminalSafeText(plan.Project.ProjectID), terminalSafeText(plan.Configuration.Path),
		terminalSafeText(plan.Configuration.Digest), terminalSafeText(plan.Project.Snapshot), plan.Project.CreateField)
	before, _ := json.Marshal(plan.Configuration.Before)
	after, _ := json.Marshal(plan.Configuration.After)
	entry, _ := json.MarshalIndent(plan.Configuration.Entrypoint, "", "  ")
	fmt.Fprintf(out, "Only configuration delta: plan_delivery %s -> %s\nExisting reviewed catalog entry (unchanged):\n%s\n", terminalSafeText(string(before)), terminalSafeText(string(after)), terminalSafeText(string(entry)))
	fmt.Fprintln(out, "Gracefully stop Runner and wait for assignments/descendants and standalone operations to finish before applying. Existing batches must be fully delivered. No models, limits, card history or other configuration changes; no service start. An exact config backup is retained. A partially created field is retained inert if activation cannot finish.")
}

func writeDeliveryCancellationPreview(out io.Writer, plan github.PlanCancellation) {
	fmt.Fprintf(out, "Cancel approved delivery plan\nParent: %s\nRevision: %s\nRepository: %s\nPlan branch: %s\nRetained whole-plan QA failures: %d\nAlready cancelled: %t\n",
		terminalSafeText(plan.ID), terminalSafeText(plan.Revision), terminalSafeText(plan.Repository), terminalSafeText(plan.Branch), plan.QAFailures, plan.AlreadyCancelled)
	for _, member := range plan.Members {
		fmt.Fprintf(out, "Retain member %s: status=%s phase=%s branch=%s accepted=%s QA failures=%d\n", terminalSafeText(member.ID), terminalSafeText(member.Status), terminalSafeText(member.Phase), terminalSafeText(member.Branch), terminalSafeText(member.QACommit), member.QAFailures)
	}
	fmt.Fprintln(out, "Gracefully stop Runner and finish standalone operations first. Apply fences only the exact parent; retained work is not deleted, reverted, retried or delivered. Published/merged PRs require separate coordination. No automatic drain, kill, counter reset or restart.")
}
