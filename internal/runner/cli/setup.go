package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/securefs"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	apply := f.Bool("apply", false, "apply the displayed setup and run its baseline checks")
	var commands, sources repeated
	f.Var(&commands, "check", "authorized baseline command; repeat for each check")
	f.Var(&sources, "quality-source", "repository-relative quality document; repeat as needed")
	definition := f.String("definition", "Approved outcome satisfied, scoped changes reviewed, and all required checks pass.", "quality definition")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("unexpected init argument")
	}
	if strings.TrimSpace(*definition) == "" {
		return errors.New("quality definition must not be empty")
	}
	id, base, err := workspace.Observe(ctx, cfg)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Repository: %s\nObserved base: %s\nState: %s\nWorkspaces: %s\nUncommitted checkout changes are excluded from the baseline and approved base.\n", cfg.Repository.Path, base, cfg.StateDir, cfg.WorktreeDir)
	for _, role := range []string{"implementer", "reviewer"} {
		name, p := cfg.Role(role)
		fmt.Fprintf(c.Out, "%s: %s = %s / %s, timeout %s\n", role, name, p.Model, p.Reasoning, p.Timeout)
	}
	_, statErr := os.Lstat(filepath.Join(cfg.StateDir, "runner.db"))
	if statErr == nil {
		db, err := store.Open(ctx, cfg.StateDir, !*apply)
		if err != nil {
			return err
		}
		defer db.Close()
		old, drained, err := db.Settings(ctx)
		if err != nil {
			return err
		}
		if id != old.RepositoryID || cfg.Repository != old.Config.Repository || cfg.StateDir != old.Config.StateDir || cfg.WorktreeDir != old.Config.WorktreeDir {
			return errors.New("incompatible setup: use a distinct state directory; automatic migration is not supported")
		}
		if len(commands) != 0 || len(sources) != 0 || *definition != f.Lookup("definition").DefValue {
			return errors.New("quality agreement changes are not supported in slice 1")
		}
		fmt.Fprintln(c.Out, "Retaining quality revision:", old.Quality.Revision())
		if !*apply {
			fmt.Fprintln(c.Out, "Preview only. Repeat with --apply to apply compatible configuration while drained.")
			return nil
		}
		if !drained {
			return errors.New("drain before reconfiguring")
		}
		lock, err := execution.Lock(filepath.Join(cfg.StateDir, "coordinator.lock"))
		if err != nil {
			return err
		}
		defer lock.Close()
		if err = c.Harness.CheckCapabilities(ctx, cfg); err != nil {
			return err
		}
		next := old
		next.Config = cfg
		if cfg.Revision() != old.Config.Revision() {
			if err = db.ApplySettings(ctx, old.Config.Revision(), next); err != nil {
				return err
			}
		}
		fmt.Fprintln(c.Out, "Configuration applied; existing attempts and quality agreement retained.")
		return nil
	}
	if !os.IsNotExist(statErr) {
		return statErr
	}
	if len(commands) == 0 {
		return errors.New("first setup requires at least one explicit --check baseline command")
	}
	fmt.Fprintln(c.Out, "Quality:", *definition)
	for _, command := range commands {
		fmt.Fprintln(c.Out, "Baseline check:", command)
	}
	for _, source := range sources {
		if filepath.IsAbs(source) || source == ".." || strings.HasPrefix(filepath.Clean(source), "../") {
			return errors.New("quality sources must be repository-relative")
		}
		fmt.Fprintln(c.Out, "Quality source:", source)
	}
	if !*apply {
		fmt.Fprintln(c.Out, "Preview only. Repeat with --apply to run these checks in a disposable base copy and initialize local authority.")
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
	if _, err = os.Lstat(filepath.Join(cfg.StateDir, "runner.db")); !os.IsNotExist(err) {
		return errors.New("state appeared during setup; inspect and retry")
	}
	copy, err := workspace.BaselineCopy(ctx, cfg, base)
	if err != nil {
		return err
	}
	before, err := workspace.PrepareCopy(ctx, copy)
	if err != nil {
		return err
	}
	quality := config.Quality{Definition: *definition, Sources: map[string]string{}, Documents: map[string]string{}, Commands: commands, Base: base}
	for _, source := range sources {
		b, _, _, err := securefs.ReadFile(filepath.Join(copy, source), 1<<20)
		if err != nil {
			return err
		}
		quality.Sources[source] = stringDigest(b)
		quality.Documents[source] = string(b)
	}
	for i, command := range commands {
		logPath := filepath.Join(cfg.StateDir, fmt.Sprintf("baseline-%s-%d.log", time.Now().UTC().Format("20060102T150405.000000000"), i))
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		err = c.Harness.Baseline(checkCtx, copy, command, log)
		cancel()
		closeErr := log.Close()
		if err != nil {
			return fmt.Errorf("baseline failed; retained copy %s, evidence %s: %w", copy, logPath, err)
		}
		if closeErr != nil {
			return closeErr
		}
		quality.Evidence = append(quality.Evidence, logPath)
	}
	if err = workspace.VerifyCopy(copy, before); err != nil {
		return err
	}
	afterID, afterBase, err := workspace.Observe(ctx, cfg)
	if err != nil {
		return err
	}
	if afterID != id || afterBase != base {
		return errors.New("repository/base changed during setup; inspect and repeat")
	}
	db, err := store.Create(ctx, cfg.StateDir, config.Settings{Config: cfg, RepositoryID: id, Quality: quality})
	if err != nil {
		return err
	}
	if err = db.Close(); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "Initialized and drained. Baseline passed; quality revision %s. Baseline copy: %s\n", quality.Revision(), copy)
	return nil
}

func stringDigest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
