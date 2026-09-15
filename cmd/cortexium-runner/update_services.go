package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/securefs"
)

type stoppedRunnerService struct {
	process github.RuntimeState
	plist   string
	digest  [sha256.Size]byte
}

func prepareRunnerUpdate(ctx context.Context, executable string, stdout io.Writer) (func() error, error) {
	workers, err := github.RunningProcesses()
	if err != nil {
		return nil, err
	}
	var selected []github.RuntimeState
	var services []stoppedRunnerService
	for _, process := range workers {
		if process.Executable == "" {
			return nil, errors.New("a legacy worker is running; update it at an idle boundary before using automatic drain-and-update")
		}
		if process.Executable != executable {
			continue
		}
		if !process.StopSupported {
			return nil, errors.New("a selected worker does not support graceful stop")
		}
		if process.Stopping {
			return nil, errors.New("a selected worker is already stopping; finish that stop before updating (it will not be restarted implicitly)")
		}
		if process.LaunchdService == "" {
			return nil, errors.New("a foreground worker uses this executable; run stop --wait first, then update and explicitly start it with run")
		}
		if err := validateLaunchdTarget(process.LaunchdService); err != nil {
			return nil, err
		}
		output, err := launchctl(ctx, "print", process.LaunchdService)
		if err != nil {
			return nil, err
		}
		if launchdValue(output, "pid") != fmt.Sprint(process.PID) {
			return nil, errors.New("launchd worker changed before update")
		}
		plist := launchdValue(output, "path")
		digest, err := runnerServiceDigest(plist)
		if err != nil {
			return nil, err
		}
		services = append(services, stoppedRunnerService{process, plist, digest})
		selected = append(selected, process)
	}
	if len(selected) == 0 {
		return nil, nil
	}
	if err := stopProcesses(ctx, selected, true, stdout, nil); err != nil {
		return nil, fmt.Errorf("update aborted before replacement: %w; inspect stopped services before restarting", err)
	}
	return func() error {
		// Restoration must still run if the caller cancels after draining.
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var failures []error
		for _, service := range services {
			if err := restartUpdatedRunner(ctx, service); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", service.process.LaunchdService, err))
			} else {
				fmt.Fprintf(stdout, "Restarted and observed a successful poll: %s/%d.\n", terminalSafeText(service.process.Owner), service.process.Project)
			}
		}
		return errors.Join(failures...)
	}, nil
}

func runnerServiceDigest(path string) ([sha256.Size]byte, error) {
	data, _, state, err := securefs.ReadFile(path, 1024*1024)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func restartUpdatedRunner(ctx context.Context, service stoppedRunnerService) error {
	digest, err := runnerServiceDigest(service.plist)
	if err != nil {
		return err
	}
	if digest != service.digest {
		return errors.New("launchd configuration changed during the update; left stopped")
	}
	stopped, err := launchdStopped(ctx, service.process)
	if err != nil {
		return err
	}
	if !stopped {
		return errors.New("launchd job was reloaded by another operator; left untouched")
	}
	domain := service.process.LaunchdService[:strings.LastIndex(service.process.LaunchdService, "/")]
	if _, err := launchctl(ctx, "bootstrap", domain, service.plist); err != nil {
		return err
	}
	if _, err := launchctl(ctx, "kickstart", service.process.LaunchdService); err != nil {
		return err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		worker, active, err := github.InspectProcessState(config.GitHubProjectConfig{Owner: service.process.Owner, Number: service.process.Project})
		if err != nil {
			return err
		}
		if active && worker.StartedAt.After(service.process.StartedAt) && !worker.Stopping && !worker.LastPollAt.IsZero() && worker.LastError == "" {
			output, err := launchctl(ctx, "print", service.process.LaunchdService)
			if err != nil {
				return err
			}
			program, err := filepath.EvalSymlinks(launchdValue(output, "program"))
			if err == nil && program == service.process.Executable && launchdValue(output, "pid") == fmt.Sprint(worker.PID) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("service reloaded but fresh successful poll was not verified: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
