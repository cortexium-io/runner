package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func runStop(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("stop", "cortexium-runner stop [--config PATH] [--wait] [--timeout DURATION]", stdout)
	configPath := flags.String("config", "", "optional filter: stop only this config's project")
	wait := flags.Bool("wait", false, "wait for the selected workers and their launchd jobs to stop")
	timeout := flags.Duration("timeout", 0, "maximum acknowledgment/shutdown wait; zero waits until interrupted (never kills work)")
	proceed, err := parseFlags(flags, args, "stop")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 || *timeout < 0 {
		return errors.New("stop accepts no positional arguments and requires a nonnegative timeout")
	}
	var project *config.GitHubProjectConfig
	if strings.TrimSpace(*configPath) != "" {
		cfg, err := config.LoadConfig(*configPath)
		if err != nil {
			return fmt.Errorf("load stop filter: %w", err)
		}
		project = cfg.GitHubProject
		if project == nil {
			return errors.New("stop filter has no GitHub Project")
		}
	}
	processes, discoveryErr := github.RunningProcesses()
	var selected []github.RuntimeState
	for _, process := range processes {
		if project == nil || strings.EqualFold(process.Owner, project.Owner) && process.Project == project.Number {
			selected = append(selected, process)
		}
	}
	if len(selected) == 0 {
		if discoveryErr != nil {
			return discoveryErr
		}
		fmt.Fprintln(stdout, "No matching local Runner workers are running.")
		return nil
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	return stopProcesses(ctx, selected, *wait, stdout, discoveryErr)
}

func stopProcesses(ctx context.Context, processes []github.RuntimeState, wait bool, stdout io.Writer, initialErr error) error {
	failures := []error{initialErr}
	pending := make([]github.RuntimeState, 0, len(processes))
	for _, process := range processes {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, fmt.Errorf("stop request interrupted: %w; previously accepted requests remain active", err))...)
		}
		if err := github.RequestProcessStop(process); err != nil {
			failures = append(failures, fmt.Errorf("%s/%d: %w", process.Owner, process.Project, err))
			continue
		}
		pending = append(pending, process)
		fmt.Fprintf(stdout, "Stop requested: %s/%d (PID %d); active assignments will finish.\n", terminalSafeText(process.Owner), process.Project, process.PID)
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for len(pending) > 0 {
		next := pending[:0]
		for _, process := range pending {
			active, current, err := github.ProcessStillRunning(process)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if !active {
				if current.PID != 0 && (current.PID != process.PID || !current.StartedAt.Equal(process.StartedAt)) {
					failures = append(failures, fmt.Errorf("%s/%d was replaced by another worker; replacement left untouched", process.Owner, process.Project))
					continue
				}
				stopped, err := launchdStopped(ctx, process)
				if err != nil {
					failures = append(failures, err)
					continue
				}
				if stopped {
					fmt.Fprintf(stdout, "Stopped: %s/%d.\n", terminalSafeText(process.Owner), process.Project)
					continue
				}
			} else if current.Stopping && !wait {
				fmt.Fprintf(stdout, "Stopping: %s/%d — finishing %d active assignment(s).\n", terminalSafeText(process.Owner), process.Project, current.Active)
				continue
			}
			next = append(next, process)
		}
		pending = next
		if len(pending) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return errors.Join(append(failures, fmt.Errorf("stop wait ended: %w; requests remain active and no work was killed", ctx.Err()))...)
		case <-ticker.C:
		}
	}
	return errors.Join(failures...)
}
