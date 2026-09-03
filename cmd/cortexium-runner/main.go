package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := execute(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := run(ctx, args, stdin, stdout); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", terminalSafeText(err.Error()))
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		writeUsage(stdout)
		return nil
	}
	if args[0] == "-h" || args[0] == "--help" {
		writeUsage(stdout)
		return nil
	}
	if args[0] == "help" {
		if len(args) == 1 {
			writeUsage(stdout)
			return nil
		}
		return run(ctx, append(append([]string{}, args[1:]...), "--help"), stdin, stdout)
	}
	switch args[0] {
	case "--version":
		if len(args) != 1 {
			return errors.New("--version does not accept arguments")
		}
		fmt.Fprintf(stdout, "cortexium-runner %s\n", buildVersion())
		return nil
	case "init":
		return runInit(ctx, args[1:], stdin, stdout)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout)
	case "update":
		return runUpdate(ctx, args[1:], stdout)
	case "add":
		return runAdd(ctx, args[1:], stdout)
	case "plan":
		return runPlan(ctx, args[1:], stdin, stdout)
	case "approve":
		return runApprove(ctx, args[1:], stdin, stdout)
	case "retry":
		return runRetry(ctx, args[1:], stdin, stdout)
	case "status":
		return runStatus(ctx, args[1:], stdout)
	case "metrics":
		return runMetrics(args[1:], stdout)
	case "harness":
		return runHarness(ctx, args[1:], stdout)
	case "role":
		return runRole(args[1:], stdout)
	case "run":
		return runWorker(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q; run cortexium-runner help for usage", args[0])
	}
}

func writeUsage(output io.Writer) {
	fmt.Fprintln(output, `Local GitHub Project Runner

Usage:
  cortexium-runner --version
  cortexium-runner COMMAND [options]

Getting started:
  cortexium-runner init [--config PATH] [--dry-run] [--prune]
  cortexium-runner doctor [--config PATH] [--fix] [--offline] [--probe-harnesses] [--json]
  cortexium-runner harness check [--config PATH] [--browser] [--timeout DURATION]
  cortexium-runner update [--check] [--version vMAJOR.MINOR.PATCH]

Project work:
  cortexium-runner add plan|ready [--config PATH] --title TEXT (--body TEXT|--body-file PATH) [--dry-run]
  cortexium-runner plan [--config PATH] [--idea TEXT|--idea-file PATH] [--create|--stage-only|--approve-staged FINGERPRINT]
  cortexium-runner approve [--config PATH] --item ID|URL [--dry-run]
  cortexium-runner retry [--config PATH] [--item ID|URL|TITLE] [--feedback TEXT] [--dry-run]

Execution:
  cortexium-runner run [--config PATH] [--once] [--poll-interval DURATION] [--max-idle-interval DURATION]
  cortexium-runner status [--config PATH]
  cortexium-runner metrics [--config PATH] [--item ID|TITLE] [--json]

Customization:
  cortexium-runner role list|show|add|edit|remove [options]

Generated workflow:
  1. init creates or adopts the GitHub Project, synchronizes its fields and statuses,
     writes the local config, and installs the bundled role skills.
  2. doctor validates the config and verifies GitHub, repository, harness, skill,
     tool, and MCP readiness. Use --offline for static validation only.
  3. add plan asks the running planner to shape a goal into reviewable cards;
     add ready authorizes one sufficiently specified card for implementation.
  4. run syncs public intake and drives configured planning, implementation, QA,
     PR, and human-gate transitions.

No hosted control plane, webhook, or inbound server is required.

See README.md for command flags and configuration examples.`)
}

func runWorker(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("run", "cortexium-runner run [--config PATH] [--once] [options]", stdout)
	configPath := flags.String("config", "", "trusted operator config path; defaults to .cortexium/runner.json")
	once := flags.Bool("once", false, "run one polling cycle and exit")
	pollInterval := flags.Duration("poll-interval", engine.DefaultPollInterval, "base delay between continuous runner polls")
	maxIdleInterval := flags.Duration("max-idle-interval", engine.DefaultMaxIdleInterval, "maximum delay between continuous runner polls while idle")
	proceed, err := parseFlags(flags, args, "run")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("runner does not accept positional arguments")
	}
	if *pollInterval <= 0 {
		return errors.New("--poll-interval must be positive")
	}
	if *maxIdleInterval <= 0 {
		return errors.New("--max-idle-interval must be positive")
	}
	if *maxIdleInterval < *pollInterval {
		return errors.New("--max-idle-interval must be greater than or equal to --poll-interval")
	}

	*configPath = resolveRunnerConfigPath(*configPath, "")
	return runConfiguredWorker(ctx, *configPath, *once, *pollInterval, *maxIdleInterval, stdout)
}

func runConfiguredWorker(ctx context.Context, configPath string, once bool, pollInterval, maxIdleInterval time.Duration, stdout io.Writer) (returnErr error) {
	cfg, err := config.LoadTrustedConfig(configPath)
	if err != nil {
		return fmt.Errorf("load runner config: %w", err)
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return fmt.Errorf("configure runner: %w", err)
	}
	attachMetricsStore(service, cfg, stdout)
	projectLock, err := github.AcquireProcessLock(*cfg.GitHubProject)
	if err != nil {
		return err
	}
	defer func() {
		if err := projectLock.Release(); err != nil && returnErr == nil {
			returnErr = err
		}
	}()
	startedAt := projectLock.StartedAt
	writeResult := func(result engine.RunResult) {
		fmt.Fprintf(stdout, "%s [%s/%s]: %s\n", terminalSafeText(result.Item.Title), terminalSafeText(result.Item.Role), terminalSafeText(result.Harness), terminalSafeText(result.Summary))
		if result.WorktreePath != "" {
			label := "worktree"
			if result.WorktreeCleaned {
				label = "worktree cleaned"
			}
			fmt.Fprintf(stdout, "  %s: %s\n  branch: %s\n", label, terminalSafeText(result.WorktreePath), terminalSafeText(result.Branch))
		}
		if result.Error != "" {
			fmt.Fprintf(stdout, "  error: %s\n", terminalSafeText(result.Error))
		}
		if result.MetricsError != "" {
			fmt.Fprintf(stdout, "  metrics warning: %s\n", terminalSafeText(result.MetricsError))
		}
	}
	if !once {
		writeProgress(stdout, "Runner started. Checking the GitHub Project for work…")
		lastAdmissionSummary := ""
		return service.RunLoop(ctx, pollInterval, maxIdleInterval, writeResult, func(err error) {
			fmt.Fprintf(stdout, "runner error: %s\n", terminalSafeText(err.Error()))
		}, func(poll engine.PollState) {
			admissionSummary := ""
			if poll.Admission.Configured && !poll.Admission.Allowed {
				admissionSummary = poll.Admission.Summary()
				if admissionSummary != lastAdmissionSummary {
					fmt.Fprintf(stdout, "Runner admission paused: %s\n", terminalSafeText(admissionSummary))
				}
			} else if lastAdmissionSummary != "" {
				fmt.Fprintln(stdout, "Runner admission resumed: the rolling budget has capacity.")
			}
			lastAdmissionSummary = admissionSummary
			if err := projectLock.UpdateRuntime(github.RuntimeState{PID: os.Getpid(), Owner: cfg.GitHubProject.Owner, Project: cfg.GitHubProject.Number, StartedAt: startedAt, LastPollAt: poll.LastPollAt, NextPollAt: poll.NextPollAt, LastError: poll.LastError}); err != nil {
				fmt.Fprintf(stdout, "runner status error: %s\n", terminalSafeText(err.Error()))
			}
		})
	}
	writeProgress(stdout, "Running one GitHub Project cycle…")
	results, err := service.RunCycle(ctx)
	if err != nil {
		return fmt.Errorf("run cycle: %w", err)
	}
	if len(results) == 0 {
		if admission := service.LastAdmissionDecision(); admission.Configured && !admission.Allowed {
			fmt.Fprintf(stdout, "paused: %s\n", terminalSafeText(admission.Summary()))
			return nil
		}
		fmt.Fprintln(stdout, "idle: no ready GitHub Project items")
		return nil
	}
	for _, result := range results {
		writeResult(result)
	}
	return nil
}

func attachMetricsStore(service *engine.Engine, cfg config.Config, output io.Writer) *runnermetrics.Store {
	store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		if output != nil {
			fmt.Fprintf(output, "metrics warning: %v\n", err)
		}
		return nil
	}
	service.SetMetricsObserver(store.Append)
	service.SetMetricsHistoryReader(store.Read)
	return store
}
