// Package cli translates explicit local commands into application operations.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/securefs"
)

type Harness interface {
	engine.Executor
	CheckCapabilities(context.Context, config.Config) error
	Baseline(context.Context, string, string, *os.File) error
}
type CLI struct {
	Out     io.Writer
	Harness Harness
}

func (c CLI) flags(name string) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(c.Out)
	return f
}
func (c CLI) json(v any) error {
	enc := json.NewEncoder(c.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
func (c CLI) Run(ctx context.Context, args []string) error {
	if c.Out == nil {
		c.Out = os.Stdout
	}
	if c.Harness == nil {
		c.Harness = execution.Codex{}
	}
	global := c.flags("runner-next")
	path := global.String("config", "", "explicit Runner local configuration path")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if *path == "" || len(args) == 0 {
		return errors.New("usage: runner-next --config PATH init|config explain|config apply|task|run|drain|status")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		return c.init(ctx, cfg, args[1:])
	}
	readonly := args[0] == "status" || (args[0] == "config" && len(args) > 1 && args[1] == "explain") || (args[0] == "task" && len(args) > 1 && args[1] == "show")
	db, err := store.Open(ctx, cfg.StateDir, readonly)
	if err != nil {
		return fmt.Errorf("cannot open local authority (inspection never creates it): %w", err)
	}
	defer db.Close()
	settings, drained, err := db.Settings(ctx)
	if err != nil {
		return err
	}
	if !readonly && args[0] != "config" && args[0] != "drain" && !(args[0] == "task" && len(args) > 1 && args[1] == "stop") && settings.Config.Revision() != cfg.Revision() {
		return errors.New("configuration has unapplied changes; use config explain, then config apply --preview DIGEST")
	}
	app := engine.Engine{Store: db, Settings: settings, Executor: c.Harness}
	switch args[0] {
	case "config":
		if len(args) < 2 {
			return errors.New("usage: config explain|apply --preview DIGEST")
		}
		change, err := app.PreviewPolicy(ctx, cfg)
		if err != nil {
			return err
		}
		switch args[1] {
		case "explain":
			if len(args) != 2 {
				return errors.New("config explain takes no arguments")
			}
			return c.json(change)
		case "apply":
			f := c.flags("config apply")
			preview := f.String("preview", "", "displayed policy preview digest")
			if err = f.Parse(args[2:]); err != nil {
				return err
			}
			if f.NArg() != 0 || *preview == "" || *preview != change.Preview {
				return errors.New("policy preview is missing or stale; run config explain")
			}
			if err = c.Harness.CheckCapabilities(ctx, cfg); err != nil {
				return err
			}
			next := settings
			next.Config = cfg
			if err = db.ApplySettings(ctx, change.AppliedRevision, next); err != nil {
				return err
			}
			fmt.Fprintln(c.Out, "Policy applied. Active attempts and pending retry profiles stay pinned; consumed allowances are retained. New admissions use this policy.")
			return nil
		default:
			return errors.New("usage: config explain|apply --preview DIGEST")
		}
	case "run":
		if len(args) != 1 {
			return errors.New("run takes no arguments")
		}
		if err = c.Harness.CheckCapabilities(ctx, cfg); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Coordinator running. Use status to inspect work and drain to stop admission.")
		return app.Run(ctx)
	case "drain":
		if len(args) != 1 {
			return errors.New("drain takes no arguments")
		}
		if err = db.SetDrained(ctx, true); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Drain recorded. Active work may settle; no new invocation will start.")
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for app.Running() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
			}
		}
		return nil
	case "status":
		f := c.flags("status")
		asJSON := f.Bool("json", false, "structured status")
		if err = f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return errors.New("unexpected status argument")
		}
		tasks, err := db.Tasks(ctx)
		if err != nil {
			return err
		}
		views := []engine.View{}
		for _, t := range tasks {
			v, err := app.Show(ctx, t.ID)
			if err != nil {
				return err
			}
			views = append(views, v)
		}
		if *asJSON {
			return c.json(struct {
				Running   bool          `json:"coordinator_running"`
				Drained   bool          `json:"drained"`
				Unapplied bool          `json:"unapplied_config"`
				Tasks     []engine.View `json:"tasks"`
			}{app.Running(), drained, cfg.Revision() != settings.Config.Revision(), views})
		}
		fmt.Fprintf(c.Out, "Coordinator running: %t; drained: %t; unapplied config: %t\n", app.Running(), drained, cfg.Revision() != settings.Config.Revision())
		for _, v := range views {
			c.printView(v, false)
		}
		return nil
	case "task":
		return c.task(ctx, &app, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func (c CLI) task(ctx context.Context, e *engine.Engine, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: task add|show|approve|release|replace|stop|resume")
	}
	command := args[0]
	args = args[1:]
	id := ""
	if command != "add" {
		if len(args) == 0 {
			return errors.New("task ID is required")
		}
		id, args = args[0], args[1:]
	}
	f := c.flags("task " + command)
	var file *string
	var version *int
	var preview *string
	var asJSON *bool
	var attempt *string
	var allow *bool
	var plan *bool
	switch command {
	case "add":
		plan = f.Bool("plan", false, "capture a planning request; approval authorizes planning only")
		file = f.String("file", "", "task Markdown")
	case "replace":
		file = f.String("file", "", "task Markdown")
		version = f.Int("version", 0, "expected current version")
	case "approve", "release":
		version = f.Int("version", 0, "displayed version")
		preview = f.String("preview", "", "displayed approval-preview digest")
	case "show":
		asJSON = f.Bool("json", false, "structured task")
	case "resume":
		attempt = f.String("attempt", "", "interrupted attempt ID")
		allow = f.Bool("allow-new-invocation", false, "explicitly authorize one repeated invocation")
	case "stop":
	default:
		return fmt.Errorf("unknown task command %q", command)
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected task arguments")
	}
	if file != nil {
		if *file == "" {
			return errors.New("--file is required")
		}
		b, _, _, err := securefs.ReadFile(*file, 1<<20)
		if err != nil {
			return err
		}
		if command == "add" {
			var t core.Task
			if *plan {
				t, err = e.AddPlan(ctx, string(b))
			} else {
				t, err = e.Add(ctx, string(b))
			}
			if err != nil {
				return err
			}
			id = t.ID
		} else {
			if *version < 1 {
				return errors.New("--version is required")
			}
			if _, err = e.Replace(ctx, id, *version, string(b)); err != nil {
				return err
			}
		}
	}
	switch command {
	case "approve":
		if *version < 1 || *preview == "" {
			return errors.New("--version and --preview are required")
		}
		if _, err := e.Approve(ctx, id, *version, *preview); err != nil {
			return err
		}
	case "release":
		if *version < 1 || *preview == "" {
			return errors.New("--version and --preview are required")
		}
		if _, err := e.Release(ctx, id, *version, *preview); err != nil {
			return err
		}
	case "stop":
		if err := e.Stop(ctx, id); err != nil {
			return err
		}
	case "resume":
		if *attempt == "" {
			return errors.New("--attempt is required")
		}
		message, err := e.Resume(ctx, id, *attempt, *allow)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.Out, message)
	}
	view, err := e.Show(ctx, id)
	if err != nil {
		return err
	}
	if asJSON != nil && *asJSON {
		return c.json(view)
	}
	c.printView(view, command == "show")
	if command != "show" && !e.Running() {
		fmt.Fprintln(c.Out, "Coordinator is absent. Run `runner-next --config PATH run` to reconcile pending work.")
	}
	return nil
}
func (c CLI) printView(v engine.View, full bool) {
	t := v.Task
	fmt.Fprintf(c.Out, "%s v%d — %s\n", t.ID, t.Current().Number, t.Stage)
	if t.PlanRequest {
		fmt.Fprintln(c.Out, "Plan request — initial approval permits planning only; task release authorizes its proposed children.")
	} else if t.Current().NeedsVerification() && t.Prepared == nil {
		fmt.Fprintln(c.Out, "Ready task — approval permits planner verification preparation before implementation.")
	}
	if full {
		fmt.Fprintf(c.Out, "%s\nContent digest: %s\nQuality revision: %s\nObserved base: %s\nApproval preview: %s\n", t.Current().Body, t.Current().Digest, t.Current().Quality, v.ObservedBase, v.Preview)
	}
	if t.Proposal != nil {
		fmt.Fprintf(c.Out, "Proposal: %d cards; %d open questions\n", len(t.Proposal.Cards), len(t.Proposal.Questions))
		if v.ReleasePreview != "" {
			fmt.Fprintf(c.Out, "Release preview: %s\n", v.ReleasePreview)
		}
		if full {
			for _, card := range t.Proposal.Cards {
				fmt.Fprintf(c.Out, "\nCard %s; prerequisites: %v\n%s\n", card.Key, card.Dependencies, card.Body)
			}
			for _, question := range t.Proposal.Questions {
				fmt.Fprintln(c.Out, "Question:", question)
			}
		}
	}
	if t.Prepared != nil && full {
		fmt.Fprintf(c.Out, "Prepared verification (attempt %s):\n%s\n", t.Prepared.Attempt, t.Prepared.Requirement)
	}
	if t.Release != nil {
		fmt.Fprintf(c.Out, "Released children: %v\n", t.Release.Children)
	}
	if t.ParentID != "" {
		fmt.Fprintf(c.Out, "Parent plan: %s; card: %s; dependencies: %v\n", t.ParentID, t.PlanKey, t.Current().Dependencies)
	}
	if t.Approval != nil {
		fmt.Fprintf(c.Out, "Approved base: %s\n", t.Approval.Base)
	}
	if t.Candidate != nil {
		fmt.Fprintf(c.Out, "Candidate: %s\nWorkspace: %s\n", t.Candidate.Commit, t.Candidate.Workspace)
	}
	if t.Attention != nil {
		fmt.Fprintf(c.Out, "Attention (%s): %s; attempt %s\n", t.Attention.Kind, t.Attention.Detail, t.Attention.Attempt)
	}
	for _, a := range v.Attempts {
		fmt.Fprintf(c.Out, "Attempt %s: %s / %s; %s / %s; occupying=%t\nEvidence: %s\n", a.ID, a.Role, a.State, a.Profile.Model, a.Profile.Reasoning, a.Occupying, a.EvidenceDir)
		fmt.Fprintf(c.Out, "Invocation %d; repair %d; transient retry %d; policy %s; elapsed %s\n", a.Invocation, a.Repair, a.Retry, a.PolicyRevision, elapsed(a))
	}
	if v.CancellationPending {
		fmt.Fprintln(c.Out, "Cancellation pending: revoked execution still owns its reservation.")
	}
	fmt.Fprintf(c.Out, "Allowances remaining: repairs=%d; transient retries=%d", v.Eligibility.RepairsRemaining, v.Eligibility.RetriesRemaining)
	if v.Eligibility.InvocationsRemaining != nil {
		fmt.Fprintf(c.Out, "; invocations=%d", *v.Eligibility.InvocationsRemaining)
	}
	fmt.Fprintln(c.Out)
	if v.Task.NextRole != "" {
		fmt.Fprintf(c.Out, "Next role: %s; profile %s = %s / %s; reason: %s\n", v.Eligibility.Role, v.Eligibility.ProfileName, v.Eligibility.Profile.Model, v.Eligibility.Profile.Reasoning, v.Eligibility.Reason)
	}
	fmt.Fprintln(c.Out, "Next:", v.NextAction)
}

func elapsed(a core.Attempt) time.Duration {
	end := time.Now()
	if a.FinishedAt != nil {
		end = *a.FinishedAt
	}
	return max(time.Duration(0), end.Sub(a.StartedAt).Round(time.Second))
}
