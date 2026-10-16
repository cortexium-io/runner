package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/securefs"
)

func (c CLI) configure(ctx context.Context, e *engine.Engine, path string, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: config edit|explain|apply")
	}
	if args[0] == "edit" {
		return c.editConfig(ctx, e, path, args[1:])
	}
	f := c.flags("config " + args[0])
	var asJSON bool
	var preview string
	switch args[0] {
	case "explain":
		f.BoolVar(&asJSON, "json", false, "structured policy and effects")
	case "apply":
		f.StringVar(&preview, "preview", "", "displayed apply preview")
	default:
		return errors.New("usage: config edit|explain|apply")
	}
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected config argument")
	}
	change, err := e.PreviewPolicy(ctx, cfg)
	if err != nil {
		return err
	}
	if args[0] == "explain" {
		if asJSON {
			return c.json(change)
		}
		c.printPolicy(change)
		fmt.Fprintln(c.Out, "Apply preview:", change.Preview)
		fmt.Fprintln(c.Out, "Next: config apply --preview", change.Preview)
		return nil
	}
	if preview == "" || preview != change.Preview {
		return errors.New("policy preview is missing or stale; run config explain")
	}
	if err = c.Harness.CheckCapabilities(ctx, cfg); err != nil {
		return err
	}
	next := e.Settings
	next.Config = cfg
	if err = e.Store.ApplySettings(ctx, change.AppliedRevision, next); err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Policy applied. Active attempts and pending retry profiles stay pinned; consumed allowances are retained. New admissions use this policy; unsent automatic merges require automatic policy to remain enabled.")
	return nil
}

func (c CLI) printPolicy(p engine.PolicyChange) {
	cfg := p.Proposed
	fmt.Fprintln(c.Out, "Resolved execution and merge policy")
	fmt.Fprintf(c.Out, "Merge policy: %s -> %s; applies to existing and future approved work.\n", p.MergePolicyBefore, cfg.MergePolicy())
	fmt.Fprintln(c.Out, "Automatic policy derives one guarded merge from a ready PR observation; blocked results wait for another observation. No polling or ready-state writes.")
	fmt.Fprintf(c.Out, "Delivery: %s; destination: %s; base: %s (fixed at initialization)\n", cfg.Delivery.Mode, cfg.Delivery.Repository, cfg.Repository.BaseRef)
	for _, role := range []string{"planner", "implementer", "reviewer"} {
		name, profile := cfg.Role(role)
		if name == "" {
			fmt.Fprintln(c.Out, role+": not configured")
		} else {
			fmt.Fprintf(c.Out, "%s: %s = %s / %s; timeout %s\n", role, name, profile.Model, profile.Reasoning, profile.Timeout)
		}
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Profiles[name]
		fmt.Fprintf(c.Out, "profile.%s = %s %s %s\n", name, p.Model, p.Reasoning, p.Timeout)
	}
	fmt.Fprintf(c.Out, "Parallelism: %d; repairs: %d; transient retries: %d; base refreshes: %d; invocation ceiling: ", cfg.Policy.MaxParallelism, cfg.Policy.MaxRepairs, cfg.Policy.MaxTransientRetries, cfg.Policy.MaxBaseRefreshes)
	if cfg.Policy.MaxInvocationsPerTask == nil {
		fmt.Fprintln(c.Out, "unlimited")
	} else {
		fmt.Fprintln(c.Out, *cfg.Policy.MaxInvocationsPerTask)
	}
	ladder := append([]string{cfg.Roles["implementer"]}, cfg.Policy.ImplementationLadder...)
	fmt.Fprintln(c.Out, "Implementation escalation on confirmed rejection:", strings.Join(ladder, " -> "))
	fmt.Fprintln(c.Out, "The last profile remains selected within the repair allowance. Transient retries do not escalate.")
	for _, a := range p.Active {
		if a.Role == "refresh" {
			fmt.Fprintf(c.Out, "Active base inspection %s: no model; reserved cycle retained\n", a.ID)
		} else {
			fmt.Fprintf(c.Out, "Pinned attempt %s (%s): %s / %s; %s; unaffected by this edit\n", a.ID, a.Role, a.Profile.Model, a.Profile.Reasoning, a.Profile.Timeout)
		}
	}
	for _, publication := range p.Publications {
		fmt.Fprintf(c.Out, "Active publication %s: shares capacity; destination %s; unsent automatic merge rechecks the applied policy\n", publication.Intent.ID, publication.Intent.Repository)
	}
	for _, effect := range p.Tasks {
		fmt.Fprintf(c.Out, "Task %s: consumed %d invocations / %d repairs; remaining %d repairs / %d retries", effect.TaskID, effect.Usage.Invocations, effect.Usage.Repairs, effect.After.RepairsRemaining, effect.After.RetriesRemaining)
		if effect.After.InvocationsRemaining != nil {
			fmt.Fprintf(c.Out, " / %d invocations", *effect.After.InvocationsRemaining)
		}
		fmt.Fprintf(c.Out, "; base refreshes: %d consumed / %d remaining\n", effect.Usage.BaseRefreshes, effect.After.RefreshesRemaining)
		if effect.After.Role == "refresh" {
			fmt.Fprintln(c.Out, "  Next refresh:", effect.After.Reason)
		} else if effect.After.Role != "" {
			fmt.Fprintf(c.Out, "  Next %s: %s / %s; %s\n", effect.After.Role, effect.After.Profile.Model, effect.After.Profile.Reasoning, effect.After.Reason)
		}
		if effect.After.WaitingFor != "" {
			fmt.Fprintln(c.Out, "  Wait:", effect.After.WaitingFor)
		}
	}
	for _, warning := range p.Warnings {
		fmt.Fprintln(c.Out, "Warning:", warning)
	}
	fmt.Fprintln(c.Out, "Active attempts and pending retries retain their profiles. Counters carry forward; lowering limits never cancels active work.")
}

const editSettings = `Settings: merge-policy (human or automatic), parallelism, repairs, transient-retries, base-refreshes, invocations (positive number or unlimited),
implementer, reviewer, planner (profile name; planner also accepts none),
escalation (comma-separated successor profiles or none),
profile.NAME (MODEL REASONING TIMEOUT, or remove).
Example: profile.careful = gpt-6.1-sol high 20m
Execution and merge policy change here; repository, quality and delivery destination stay fixed.`

func setSetting(cfg *config.Config, key, value string) error {
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	switch key {
	case "merge-policy":
		cfg.Policy.Merge = value
	case "parallelism", "repairs", "transient-retries", "base-refreshes", "invocations":
		if key == "invocations" && value == "unlimited" {
			cfg.Policy.MaxInvocationsPerTask = nil
			return nil
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s needs an integer", key)
		}
		switch key {
		case "parallelism":
			cfg.Policy.MaxParallelism = n
		case "repairs":
			cfg.Policy.MaxRepairs = n
		case "transient-retries":
			cfg.Policy.MaxTransientRetries = n
		case "base-refreshes":
			cfg.Policy.MaxBaseRefreshes = n
		case "invocations":
			cfg.Policy.MaxInvocationsPerTask = &n
		}
	case "implementer", "reviewer", "planner":
		if key == "planner" && value == "none" {
			delete(cfg.Roles, key)
		} else {
			cfg.Roles[key] = value
		}
	case "escalation":
		cfg.Policy.ImplementationLadder = nil
		if value != "none" {
			for _, name := range strings.Split(value, ",") {
				cfg.Policy.ImplementationLadder = append(cfg.Policy.ImplementationLadder, strings.TrimSpace(name))
			}
		}
	default:
		name, ok := strings.CutPrefix(key, "profile.")
		if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") {
			return fmt.Errorf("unknown setting %q", key)
		}
		if value == "remove" {
			delete(cfg.Profiles, name)
			return nil
		}
		parts := strings.Fields(value)
		if len(parts) != 3 {
			return errors.New("profile value needs MODEL REASONING TIMEOUT")
		}
		cfg.Profiles[name] = core.Profile{Harness: "codex", Model: parts[0], Reasoning: parts[1], Timeout: parts[2]}
	}
	return nil
}

func (c CLI) editConfig(ctx context.Context, e *engine.Engine, path string, args []string) error {
	f := c.flags("config edit")
	var edits repeated
	f.Var(&edits, "set", "KEY=VALUE; repeat to change multiple settings")
	save := f.Bool("save", false, "save a noninteractive preview to the file; does not apply")
	preview := f.String("preview", "", "displayed save preview")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || (len(edits) == 0 && (*save || *preview != "")) {
		return errors.New("use --set KEY=VALUE with --save --preview DIGEST, or no flags for interactive editing")
	}
	raw, mode, state, err := securefs.ReadFile(path, 1<<20)
	if err != nil {
		return err
	}
	if err = securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return err
	}
	var proposed config.Config
	if err = config.Decode(raw, &proposed); err != nil {
		return err
	}
	scanner := bufio.NewScanner(c.In)
	read := func(prompt string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fmt.Fprint(c.Out, prompt)
		type response struct {
			line string
			err  error
		}
		ready := make(chan response, 1)
		// One pending stdin read at most. Cancellation can leave it blocked until
		// stdin closes, but must not hold up CLI exit or save partial input.
		go func() {
			if !scanner.Scan() {
				err := scanner.Err()
				if err == nil {
					err = io.EOF
				}
				ready <- response{err: err}
				return
			}
			ready <- response{line: strings.TrimSpace(scanner.Text())}
		}()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case r := <-ready:
			return r.line, r.err
		}
	}
	interactive := len(edits) == 0
	if interactive {
		current, err := e.PreviewPolicy(ctx, proposed)
		if err != nil {
			return err
		}
		c.printPolicy(current)
		fmt.Fprintln(c.Out, editSettings+"\nEnter a setting, done to preview, or cancel to discard.")
		for {
			key, err := read("Setting: ")
			if errors.Is(err, io.EOF) || key == "cancel" {
				fmt.Fprintln(c.Out, "Cancelled; nothing saved or applied.")
				return nil
			}
			if err != nil {
				return err
			}
			if key == "done" {
				break
			}
			value, err := read("Value: ")
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(c.Out, "Cancelled; nothing saved or applied.")
				return nil
			}
			if err != nil {
				return err
			}
			if err = setSetting(&proposed, key, value); err != nil {
				return err
			}
		}
	} else {
		for _, edit := range edits {
			key, value, ok := strings.Cut(edit, "=")
			if !ok {
				return errors.New("--set needs KEY=VALUE")
			}
			if err = setSetting(&proposed, key, value); err != nil {
				return err
			}
		}
	}
	change, err := e.PreviewPolicy(ctx, proposed)
	if err != nil {
		return err
	}
	if err = c.Harness.CheckCapabilities(ctx, proposed); err != nil {
		return err
	}
	// Bind the save preview to both original bytes and applied policy. File save
	// and policy application are deliberately separate commands/transactions.
	digest := core.Digest([]string{string(raw), change.Preview})
	c.printPolicy(change)
	fmt.Fprintln(c.Out, "Save preview:", digest)
	if interactive {
		answer, err := read("Save configuration file only? [y/N]: ")
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		*save = err == nil && (answer == "y" || answer == "yes")
		*preview = digest
	}
	if !*save {
		fmt.Fprintln(c.Out, "Nothing saved or applied. To save noninteractively, repeat --set values with --save --preview", digest)
		return nil
	}
	if *preview != digest {
		return errors.New("save preview is missing or stale; preview config edit again")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	latest, err := e.PreviewPolicy(ctx, proposed)
	if err != nil {
		return err
	}
	if latest.Preview != change.Preview {
		return errors.New("applied policy changed during editing; preview again")
	}
	dir, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	data, err := json.MarshalIndent(proposed, "", "  ")
	if err != nil {
		return err
	}
	if err = e.Store.AllowMutation(ctx); err != nil {
		return err
	}
	if err = dir.ReplaceFile(filepath.Base(path), append(data, '\n'), mode, state); err != nil {
		return fmt.Errorf("configuration not saved (file may have changed): %w", err)
	}
	fmt.Fprintln(c.Out, "Saved configuration file; running policy is unchanged. Next: config explain, then config apply --preview DIGEST.")
	return nil
}
