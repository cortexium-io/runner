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
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/github"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/securefs"
)

type Harness interface {
	engine.Executor
	CheckCapabilities(context.Context, config.Config) error
	execution.CheckRunner
}
type CLI struct {
	In      io.Reader
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
	if c.In == nil {
		c.In = os.Stdin
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		c.help()
		return nil
	}
	global := c.flags("runner-next")
	path := global.String("config", "", "explicit Runner local configuration path")
	if err := global.Parse(args); err != nil {
		return err
	}
	args = global.Args()
	if *path == "" || len(args) == 0 {
		return errors.New("usage: runner-next --config PATH init|quality|config explain|config apply|task|run|drain|status")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		return c.init(ctx, cfg, args[1:])
	}
	readonly := (args[0] == "quality" && len(args) > 1 && (args[1] == "preview" || args[1] == "show")) || args[0] == "status" || (args[0] == "config" && len(args) > 1 && args[1] != "apply") || (args[0] == "task" && len(args) > 1 && args[1] == "show")
	db, err := store.Open(ctx, cfg.StateDir, readonly)
	if err != nil {
		return fmt.Errorf("cannot open local authority (inspection never creates it): %w", err)
	}
	defer db.Close()
	settings, _, err := db.Settings(ctx)
	if err != nil {
		return err
	}
	if !readonly && args[0] != "config" && args[0] != "drain" && !(args[0] == "task" && len(args) > 1 && args[1] == "stop") && settings.Config.Revision() != cfg.Revision() {
		return errors.New("configuration has unapplied changes; use config explain, then config apply --preview DIGEST")
	}
	app := engine.Engine{Store: db, Settings: settings, Executor: c.Harness, Checker: c.Harness}
	if cfg.Delivery.Mode == "github-draft" {
		app.Delivery = github.Host{}
		app.Bases = github.Host{}
		app.Checks = github.Host{}
		app.Integrations = github.Host{}
		app.Merges = github.Host{}
	}
	switch args[0] {
	case "quality":
		return c.quality(ctx, &app, args[1:])
	case "config":
		return c.configure(ctx, &app, *path, cfg, args[1:])
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
		return c.status(ctx, &app, *path, args[1:])
	case "task":
		return c.task(ctx, &app, args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
func (c CLI) task(ctx context.Context, e *engine.Engine, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: task add|show|approve|release|replace|adopt-quality|stop|resume|delivery|verify-base|assess-ci|integrate|merge")
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
	var reconcile *bool
	switch command {
	case "add":
		plan = f.Bool("plan", false, "capture a planning request; approval authorizes planning only")
		file = f.String("file", "", "task Markdown")
	case "replace":
		file = f.String("file", "", "task Markdown")
		version = f.Int("version", 0, "expected current version")
	case "approve", "release", "adopt-quality":
		version = f.Int("version", 0, "displayed version")
		preview = f.String("preview", "", "displayed approval-preview digest")
	case "show":
		asJSON = f.Bool("json", false, "structured task")
	case "resume":
		attempt = f.String("attempt", "", "interrupted attempt ID")
		allow = f.Bool("allow-new-invocation", false, "explicitly authorize one repeated invocation")
	case "delivery":
		reconcile = f.Bool("reconcile", false, "inspect remote identity and continue safely; uncertain PR creation never repeats")
	case "stop", "verify-base", "assess-ci", "integrate", "merge":
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
	case "merge":
		if err := e.RequestMerge(ctx, id); err != nil {
			return err
		}
	case "integrate":
		if err := e.InspectIntegration(ctx, id); err != nil {
			return err
		}
	case "assess-ci":
		if err := e.AssessCI(ctx, id); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "CI assessment requested; run if the coordinator is absent or drained.")
		return nil
	case "verify-base":
		if err := e.VerifyBase(ctx, id); err != nil {
			return err
		}
	case "delivery":
		if !*reconcile {
			return errors.New("use task show for read-only inspection, or task delivery ID --reconcile for remote reconciliation")
		}
		if err := e.ReconcileDelivery(ctx, id); err != nil {
			return err
		}
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
	case "adopt-quality":
		if *version < 1 || *preview == "" {
			return errors.New("--version and --preview are required")
		}
		if _, err := e.AdoptQuality(ctx, id, *version, *preview); err != nil {
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
	fmt.Fprintf(c.Out, "%s v%d — %s\n%s\n%s\nNext: %s\n", t.ID, t.Current().Number, t.Stage, engine.TaskTitle(t), v.Summary, v.NextAction)
	if v.DeliveryDestination != "" {
		fmt.Fprintln(c.Out, "Configured delivery destination:", v.DeliveryDestination, "; merge policy:", v.MergePolicy)
	}
	if v.Recovery != nil {
		fmt.Fprintf(c.Out, "Recovery role: %s\nPreserved: %s\nRepeats: %s\n", v.Recovery.Role, v.Recovery.Preserved, v.Recovery.Repeats)
	}
	if !full {
		return
	}
	if t.PlanRequest {
		fmt.Fprintln(c.Out, "Plan request — initial approval permits planning only; task release authorizes its proposed children.")
	} else if t.Current().NeedsVerification() && t.Prepared == nil {
		fmt.Fprintln(c.Out, "Ready task — approval permits planner verification preparation before implementation.")
	}
	fmt.Fprintf(c.Out, "%s\nContent digest: %s\nQuality revision: %s\nObserved base: %s\nApproval preview: %s\n", t.Current().Body, t.Current().Digest, t.Current().Quality, v.ObservedBase, v.Preview)
	if v.QualityAdoptionPreview != "" {
		fmt.Fprintf(c.Out, "Current default quality: %s\nQuality adoption preview: %s\nAdoption creates an unapproved version and revokes old actions.\n", v.CurrentQuality, v.QualityAdoptionPreview)
	}
	if t.Proposal != nil {
		fmt.Fprintf(c.Out, "Proposal: %d cards; %d open questions\n", len(t.Proposal.Cards), len(t.Proposal.Questions))
		if v.ReleasePreview != "" {
			fmt.Fprintf(c.Out, "Release preview: %s\n", v.ReleasePreview)
		}
		for _, card := range t.Proposal.Cards {
			fmt.Fprintf(c.Out, "\nCard %s; prerequisites: %v\n%s\n", card.Key, card.Dependencies, card.Body)
		}
		for _, question := range t.Proposal.Questions {
			fmt.Fprintln(c.Out, "Question:", question)
		}
	}
	if t.Prepared != nil {
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
	if t.WorkingBase != nil {
		fmt.Fprintf(c.Out, "Working base: %s; refresh cycle %d\n", t.WorkingBase.Commit, t.WorkingBase.Cycle)
	}
	if t.RefreshTarget != nil {
		fmt.Fprintf(c.Out, "Reserved refresh: %d -> %s\n", t.RefreshTarget.Cycle, t.RefreshTarget.Commit)
	}
	if t.BaseProof != nil {
		if t.Integration != nil {
			fmt.Fprintf(c.Out, "Retained base proof: %s; observed %s; confirmed in integration receipt\n", t.BaseProof.Base, t.BaseProof.ObservedAt.Format(time.RFC3339))
		} else {
			fmt.Fprintf(c.Out, "Base proof: %s; observed %s; matches current acceptance=%t\n", t.BaseProof.Base, t.BaseProof.ObservedAt.Format(time.RFC3339), t.BaseProof.Matches(t))
		}
	}
	if t.Integration != nil {
		r := t.Integration.Assessment
		fmt.Fprintf(c.Out, "Confirmed integration: %s; merge %s; base observed %s at %s\n", r.Observation.Remote.URL, r.Observation.MergeCommit, r.BaseTip, r.ObservedAt.Format(time.RFC3339))
	}
	if t.Candidate != nil {
		fmt.Fprintf(c.Out, "Candidate: %s\nWorkspace: %s\n", t.Candidate.Commit, t.Candidate.Workspace)
	}
	if t.ConflictRepair != nil {
		r := t.ConflictRepair
		fmt.Fprintf(c.Out, "Conflict repair source: %s; target %s (refresh %d); paths %s\n", r.Candidate.Commit, r.Target.Commit, r.Target.Cycle, strings.Join(r.Paths, ", "))
	}
	if t.CIRepair != nil {
		fmt.Fprintf(c.Out, "CI repair source: publication %s; failed candidate %s; observed %s\n", t.CIRepair.PublicationID, t.CIRepair.Assessment.Proof.Candidate.Commit, t.CIRepair.Assessment.ObservedAt.Format(time.RFC3339))
	}
	for _, p := range v.Publications {
		if t.Candidate != nil && *t.Candidate != p.Intent.Candidate {
			fmt.Fprintln(c.Out, "Historical candidate retained in this operation:", p.Intent.Candidate.Commit)
		}
		fmt.Fprintf(c.Out, "Publication history %s (v%d): %s; %s\nDestination: %s -> %s; branch %s\n", p.Intent.ID, p.Intent.Version, p.State, p.Detail, p.Intent.Repository, p.Intent.BaseBranch, p.Intent.Branch)
		if u := p.Intent.Sync; u != nil {
			fmt.Fprintf(c.Out, "Synchronization: %s -> %s; predecessor %s; existing PR %d\nVerified base: %s; observed %s\n", u.PreviousCommit, p.Intent.Candidate.Commit, u.PreviousID, u.Number, u.Base, u.ObservedAt.Format(time.RFC3339))
		}

		if p.IntegrationRequest != nil {
			if p.IntegrationRequest.Merge && p.MergeDispatch == nil {
				if p.IntegrationRequest.AutomaticPolicy != "" {
					fmt.Fprintln(c.Out, "Automatic merge queued or running under policy revision", p.IntegrationRequest.AutomaticPolicy, "; live policy and host admission required.")
				} else {
					fmt.Fprintln(c.Out, "Explicit merge queued or running; live admission required before one conditional write.")
				}
			} else {
				fmt.Fprintln(c.Out, "Integration observation queued or running; enabled automatic policy may act on a ready result. Consumed dispatches cannot repeat.")
			}
		}
		if p.MergeDispatch != nil {
			if p.MergeDispatch.Request.AutomaticPolicy != "" {
				fmt.Fprintln(c.Out, "Automatic merge policy origin:", p.MergeDispatch.Request.AutomaticPolicy)
			}
			fmt.Fprintf(c.Out, "Merge dispatch consumed at %s under protection %s; recovery inspects only and never repeats the write.\n", p.MergeDispatch.RequestedAt.Format(time.RFC3339), p.MergeDispatch.Admission.ProtectionRuleID)
		}
		if r := p.Integration; r != nil {
			if r.Admission != nil {
				fmt.Fprintf(c.Out, "Merge admission: %s (protection %s)\n", r.Admission.Detail, r.Admission.ProtectionRuleID)
			}
			fmt.Fprintf(c.Out, "Integration %s at %s: %s\n", r.State, r.ObservedAt.Format(time.RFC3339), r.Detail)
		}
		if p.CIRequest != nil {
			fmt.Fprintln(c.Out, "CI assessment queued or running; previous results are historical.")
		}
		if a := p.CI; a != nil {
			if !p.CurrentCI(t) {
				fmt.Fprintln(c.Out, "Historical CI assessment; not current evidence.")
			}
			fmt.Fprintf(c.Out, "CI %s at %s; verified base %s\n%s\n", a.State, a.ObservedAt.Format(time.RFC3339), a.Proof.Base, a.Detail)
			if a.Observation != nil {
				fmt.Fprintf(c.Out, "CI commit: %s (%s)\n", a.Observation.Commit, a.Observation.Source)
			}
			for _, r := range a.Results {
				fmt.Fprintf(c.Out, "Required CI %s %q app=%d: %s (status=%q conclusion=%q) %s\n", r.Kind, r.Name, r.AppID, r.State, r.Status, r.Conclusion, r.URL)
			}
		}
		if p.ObservedAt != nil {
			fmt.Fprintf(c.Out, "Last remote observation: %s\n", p.ObservedAt.Format(time.RFC3339))
		}
		if p.Observation != nil && p.Observation.URL != "" {
			fmt.Fprintln(c.Out, "Pull request:", p.Observation.URL)
		}
	}
	if t.Attention != nil {
		fmt.Fprintf(c.Out, "Attention (%s): %s; attempt %s\n", t.Attention.Kind, t.Attention.Detail, t.Attention.Attempt)
	}
	for _, a := range v.Attempts {
		fmt.Fprintf(c.Out, "Attempt %s: %s / %s", a.ID, a.Role, a.State)
		if a.Role == "refresh" {
			fmt.Fprint(c.Out, "; deterministic, no model")
		} else {
			fmt.Fprintf(c.Out, "; %s / %s", a.Profile.Model, a.Profile.Reasoning)
		}
		fmt.Fprintf(c.Out, "; occupying=%t\nEvidence: %s\n", a.Occupying, a.EvidenceDir)
		for _, check := range a.Checks {
			fmt.Fprintf(c.Out, "Quality check %s: %s; evidence %s; %s\n", check.CheckID, check.Status, check.Evidence, check.Detail)
		}
		fmt.Fprintf(c.Out, "Invocation %d; repair %d; transient retry %d; policy %s; elapsed %s\n", a.Invocation, a.Repair, a.Retry, a.PolicyRevision, elapsed(a))
	}
	if v.CancellationPending {
		fmt.Fprintln(c.Out, "Cancellation pending: revoked execution still owns its reservation.")
	}
	fmt.Fprintf(c.Out, "Allowances remaining: repairs=%d; transient retries=%d; base refreshes=%d (consumed %d)", v.Eligibility.RepairsRemaining, v.Eligibility.RetriesRemaining, v.Eligibility.RefreshesRemaining, t.Usage.BaseRefreshes)
	if v.Eligibility.InvocationsRemaining != nil {
		fmt.Fprintf(c.Out, "; invocations=%d", *v.Eligibility.InvocationsRemaining)
	}
	fmt.Fprintln(c.Out)
	if v.Eligibility.Role != "" && (v.FocusAttempt == nil || !v.FocusAttempt.Occupying) {
		if v.Eligibility.Role == "refresh" {
			fmt.Fprintln(c.Out, "Next action: refresh;", v.Eligibility.Reason)
		} else {
			fmt.Fprintf(c.Out, "Next role: %s; profile %s = %s / %s; reason: %s\n", v.Eligibility.Role, v.Eligibility.ProfileName, v.Eligibility.Profile.Model, v.Eligibility.Profile.Reasoning, v.Eligibility.Reason)
		}
	}
}

func elapsed(a core.Attempt) time.Duration {
	end := time.Now()
	if a.FinishedAt != nil {
		end = *a.FinishedAt
	}
	return max(time.Duration(0), end.Sub(a.StartedAt).Round(time.Second))
}
