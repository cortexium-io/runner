package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/engine"
)

func (c CLI) status(ctx context.Context, e *engine.Engine, path string, args []string) error {
	f := c.flags("status")
	asJSON := f.Bool("json", false, "structured status; one JSON object per line when watching")
	watch := f.Duration("watch", 0, "read-only refresh interval, at least 1s; interrupt to stop")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (*watch != 0 && *watch < time.Second) {
		return errors.New("usage: status [--json] [--watch DURATION]; minimum watch interval is 1s")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cfg, err := config.Load(path)
		if err != nil {
			return err
		}
		s, err := e.Status(ctx, cfg)
		if err != nil {
			return err
		}
		if *asJSON && *watch != 0 {
			err = json.NewEncoder(c.Out).Encode(s)
		} else if *asJSON {
			err = c.json(s)
		} else {
			c.printStatus(s)
		}
		if err != nil || *watch == 0 {
			return err
		}
		timer := time.NewTimer(*watch)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (c CLI) printStatus(s engine.Status) {
	owner := "absent"
	if s.Running {
		owner = "running"
	}
	fmt.Fprintf(c.Out, "%s — coordinator %s; drained=%t; reservations=%d/%d\n", s.ObservedAt.Format(time.RFC3339), owner, s.Drained, s.Occupied, s.Parallelism)
	fmt.Fprintln(c.Out, "Merge policy:", s.MergePolicy)
	held := s.Restoration != nil && s.Restoration.InspectionOnly
	if held {
		fmt.Fprintln(c.Out, "Restored snapshot is inspection-only; later history is unknown. Reconcile retained and remote evidence, then initialize fresh authority at distinct paths and authorize new work. This snapshot cannot resume.")
	}
	if s.Unapplied && !held {
		fmt.Fprintln(c.Out, "Configuration has unapplied edits: config explain, then config apply --preview DIGEST.")
	}
	if !s.QualityConfigured && !held {
		fmt.Fprintln(c.Out, "Quality is not configured: quality preview/check/apply before submitting work.")
	}
	if len(s.Tasks) == 0 && !held {
		fmt.Fprintln(c.Out, "No tasks. Use task add --file TASK.md (or add --plan for a planning request).")
	}
	for _, group := range engine.StatusGroups {
		printed := false
		for _, v := range s.Tasks {
			if v.Group != group {
				continue
			}
			if !printed {
				fmt.Fprintln(c.Out, "\n"+group)
				printed = true
			}
			fmt.Fprintf(c.Out, "  %s v%d — %s — %s\n    %s\n", v.Task.ID, v.Task.Current().Number, v.Task.Stage, engine.TaskTitle(v.Task), v.Summary)
			if a := v.FocusAttempt; a != nil {
				fmt.Fprintf(c.Out, "    Attempt %s: %s / %s", a.ID, a.Role, a.State)
				if a.Role == "refresh" {
					fmt.Fprint(c.Out, "; deterministic, no model")
				} else {
					fmt.Fprintf(c.Out, "; %s / %s", a.Profile.Model, a.Profile.Reasoning)
				}
				if v.ElapsedSeconds != nil {
					fmt.Fprintf(c.Out, "; elapsed %s", time.Duration(*v.ElapsedSeconds)*time.Second)
				}
				fmt.Fprintf(c.Out, "\n    Evidence: %s\n", a.EvidenceDir)
			}
			fmt.Fprintf(c.Out, "    Remaining: %d repairs, %d transient retries, %d base refreshes", v.Eligibility.RepairsRemaining, v.Eligibility.RetriesRemaining, v.Eligibility.RefreshesRemaining)
			if v.Eligibility.InvocationsRemaining != nil {
				fmt.Fprintf(c.Out, ", %d invocations", *v.Eligibility.InvocationsRemaining)
			}
			fmt.Fprintln(c.Out)
			fmt.Fprintln(c.Out, "    Next:", v.NextAction)
		}
	}
}

func (c CLI) help() {
	fmt.Fprintln(c.Out, `Runner local — commands use runner-next --config PATH COMMAND.
init [--check COMMAND] [--apply]       Preview or initialize private local state.
task add [--plan] --file TASK.md       Capture Ready work or a Plan request.
task show ID [--json]                 Inspect content, proof, history and exact next action.
task approve|release ID --version N --preview DIGEST
task replace ID --version N --file TASK.md
task adopt-quality ID --version N --preview DIGEST
task resume ID --attempt ID [--allow-new-invocation]
task delivery ID --reconcile          Inspect and safely continue draft publication/synchronization.
task verify-base ID                  Inspect current base; refresh or queue bounded conflict repair.
task assess-ci ID                    Observe pinned hosted checks for the confirmed draft.
task integrate ID                    Observe the known PR; automatic policy may act on a ready result.
task merge ID                        Authorize one guarded ordinary merge of a ready PR.
task stop ID                         Revoke authority; retain work and evidence.
status [--json] [--watch 2s]          Read-only attention, work, waits and retained results.
config edit [--set KEY=VALUE]         Interactive editor or preview named changes.
config edit --set KEY=VALUE --save --preview DIGEST
config explain [--json]              Review resolved policy and effects.
config apply --preview DIGEST         Apply saved edits; active work stays pinned.
quality preview|check|apply|show       Inspect, check and apply repository agreements.
run                                  Reconcile and admit authorized work.
drain                                Stop admission and wait for active work to settle.
backup --to DIRECTORY                Snapshot a drained, idle installation to a new private directory.
restore --from DIRECTORY [--apply]    Preview or restore to absent original paths; always drained.

Use COMMAND --help for flags. Status and show never start work. Local delivery
retains an accepted candidate at Awaiting delivery. task integrate ID inspects
an ordinary merge of the exact verified candidate. task merge ID explicitly
authorizes one guarded merge of a ready PR; recovery never repeats that write.
Applied merge-policy=automatic may derive a merge from a ready PR observation.
Status/show never start work. Confirmed integration releases dependent work.`)
}
