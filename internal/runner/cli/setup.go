package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/engine"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/securefs"
)

type repeated []string

func (v *repeated) String() string { return strings.Join(*v, ", ") }
func (v *repeated) Set(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("value cannot be empty")
	}
	*v = append(*v, s)
	return nil
}
func (c CLI) init(ctx context.Context, cfg config.Config, args []string) error {
	f := c.flags("init")
	apply := f.Bool("apply", false, "initialize local authority; run any displayed baseline checks")
	var commands, sources repeated
	f.Var(&commands, "check", "authorized root check; repeat as needed")
	f.Var(&sources, "quality-source", "repository-relative policy source")
	definition := f.String("definition", "Approved outcome satisfied, scoped changes reviewed, and all required checks pass.", "quality definition")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected init argument")
	}
	id, base, err := workspace.Observe(ctx, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Repository: %s\nObserved base: %s\nState: %s\nWorkspaces: %s\nUncommitted checkout changes are excluded.\n", cfg.Repository.Path, base, cfg.StateDir, cfg.WorktreeDir)
	fmt.Fprintf(c.Out, "Delivery: %s; destination: %s; base: %s\n", cfg.Delivery.Mode, cfg.Delivery.Repository, cfg.Repository.BaseRef)
	if cfg.Delivery.Mode == "github-draft" {
		fmt.Fprintln(c.Out, "Accepted tasks authorize pushing the exact candidate and opening one draft PR. Completion requires current-base proof, passing pinned CI and confirmation of an ordinary human merge; Runner does not request merges. Credentials: operator gh auth login for github.com.")
	}
	for _, role := range []string{"planner", "implementer", "reviewer"} {
		name, p := cfg.Role(role)
		if name != "" {
			fmt.Fprintf(c.Out, "%s: %s = %s / %s, timeout %s\n", role, name, p.Model, p.Reasoning, p.Timeout)
		}
	}
	if _, err = os.Lstat(filepath.Join(cfg.StateDir, "runner.db")); err == nil {
		db, err := store.Open(ctx, cfg.StateDir, true)
		if err != nil {
			return err
		}
		defer db.Close()
		old, _, err := db.Settings(ctx)
		if err != nil {
			return err
		}
		if !config.SameRepository(old, config.Settings{Config: cfg, RepositoryID: id}) {
			return errors.New("incompatible setup: use a distinct state directory")
		}
		if len(commands) > 0 || len(sources) > 0 || *definition != f.Lookup("definition").DefValue {
			return errors.New("setup exists; use quality preview/check/apply for agreement changes")
		}
		if cfg.Revision() != old.Config.Revision() {
			return errors.New("setup exists; use config explain/apply for execution policy changes")
		}
		fmt.Fprintln(c.Out, "Setup already applied; quality revision:", old.Quality.Revision())
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	spec := config.QualitySpec{Definition: *definition, Sources: sources}
	for i, command := range commands {
		spec.Checks = append(spec.Checks, config.QualityCheck{ID: fmt.Sprintf("check-%d", i+1), Command: command, Directory: ".", Prerequisites: []string{"sh"}, Timeout: "20m"})
	}
	if len(commands) > 0 {
		if err = spec.Validate(); err != nil {
			return err
		}
	} else if len(sources) > 0 || *definition != f.Lookup("definition").DefValue {
		return errors.New("quality sources/definition require a check")
	}
	fmt.Fprintln(c.Out, "Check environment:", config.CheckEnvironment)
	for _, check := range spec.Checks {
		fmt.Fprintf(c.Out, "%s: %s; cwd=%s; timeout=%s\n", check.ID, check.Command, check.Directory, check.Timeout)
	}
	if !*apply {
		fmt.Fprintln(c.Out, "Preview only. Repeat with --apply. Without checks, setup leaves quality unconfigured.")
		return nil
	}
	if err = c.Harness.CheckCapabilities(ctx, cfg); err != nil {
		return err
	}
	if err = securefs.EnsurePrivateDir(cfg.StateDir); err != nil {
		return err
	}
	if err = securefs.EnsurePrivateDir(cfg.WorktreeDir); err != nil {
		return err
	}
	lock, err := execution.Lock(filepath.Join(cfg.StateDir, "coordinator.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	db, err := store.Create(ctx, cfg.StateDir, config.Settings{Config: cfg, RepositoryID: id})
	if err != nil {
		return err
	}
	defer db.Close()
	if len(commands) == 0 {
		fmt.Fprintln(c.Out, "Initialized and drained. Use quality preview/check/apply before submitting work.")
		return nil
	}
	e := engine.Engine{Store: db, Settings: config.Settings{Config: cfg, RepositoryID: id}, Executor: c.Harness, Checker: c.Harness}
	p, err := e.PreviewQuality(ctx, spec)
	if err != nil {
		return err
	}
	report, err := e.RunBaseline(ctx, spec, p.Preview)
	if err != nil {
		return err
	}
	fmt.Fprintln(c.Out, "Baseline report:", report.ID)
	p, err = e.PreviewAgreement(ctx, report.ID, nil)
	if err != nil {
		return err
	}
	if err = e.ApplyAgreement(ctx, report.ID, nil, p.Preview); err != nil {
		return fmt.Errorf("setup retained without an applied agreement; inspect quality report %s: %w", report.ID, err)
	}
	fmt.Fprintln(c.Out, "Initialized and drained. Baseline passed; quality revision:", p.Quality.Revision())
	return nil
}
