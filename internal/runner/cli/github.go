package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/securefs"
)

func (c CLI) github(ctx context.Context, e *engine.Engine, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: github inbox [--json] | import --issue NUMBER --plan|--ready [--file BRIEF.md] | sync")
	}
	f := c.flags("github " + args[0])
	var asJSON, plan, ready bool
	var number int
	var file string
	switch args[0] {
	case "inbox":
		f.BoolVar(&asJSON, "json", false, "observed requests and existing local task mappings")
	case "import":
		f.IntVar(&number, "issue", 0, "issue number in the configured Issues repository")
		f.BoolVar(&plan, "plan", false, "capture a planning request; no invocation before owner approval")
		f.BoolVar(&ready, "ready", false, "capture a specified Ready brief; no invocation before owner approval")
		f.StringVar(&file, "file", "", "optional owner-clarified local brief; original source snapshot is retained")
	case "sync":
	default:
		return errors.New("unknown GitHub command; use inbox, import or sync")
	}
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected GitHub command argument")
	}
	switch args[0] {
	case "inbox":
		inbox, err := e.Inbox(ctx)
		if err != nil {
			return err
		}
		if asJSON {
			return c.json(inbox)
		}
		for _, r := range inbox {
			state := "awaiting import and owner approval"
			if r.TaskID != "" {
				state = "captured as " + r.TaskID + "; inspect local status"
			}
			fmt.Fprintf(c.Out, "%s — %q — %s\n", r.Request.Source.URL, r.Request.Source.Title, state)
		}
		if len(inbox) == 0 {
			fmt.Fprintln(c.Out, "No open requests with the configured intake label.")
		}
		return nil
	case "import":
		if number < 1 || plan == ready {
			return errors.New("import requires --issue NUMBER and exactly one of --plan or --ready")
		}
		var brief string
		if file != "" {
			b, _, _, err := securefs.ReadFile(file, 1<<20)
			if err != nil {
				return err
			}
			brief = string(b)
			if brief == "" {
				return errors.New("clarified brief must not be empty")
			}
		}
		t, err := e.ImportIssue(ctx, number, plan, brief)
		if err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "Captured request:", t.ID, "—", t.Stage)
		fmt.Fprintln(c.Out, "Repeated import retains the existing task. Issue edits do not change the captured brief.")
		fmt.Fprintln(c.Out, "Next: task show", t.ID, "; inspect the brief and exact approval preview before task approve. Plan release remains separate.")
		return nil
	default:
		if err := e.SyncOverview(ctx); err != nil {
			return err
		}
		fmt.Fprintln(c.Out, "GitHub overview synchronized. No task approval or execution changed.")
		return nil
	}
}
