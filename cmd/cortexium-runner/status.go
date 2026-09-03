package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

type runnerStatus struct {
	RunnerID               string                      `json:"runner_id"`
	Project                *config.GitHubProjectConfig `json:"project"`
	MaxParallelism         int                         `json:"max_parallelism"`
	Running                bool                        `json:"running"`
	Processing             bool                        `json:"processing"`
	Process                *github.RuntimeState        `json:"process,omitempty"`
	Subprocesses           []runnerSubprocess          `json:"subprocesses,omitempty"`
	ProcessInspectionError string                      `json:"process_inspection_error,omitempty"`
	Work                   engine.WorkStatus           `json:"work"`
	Metrics                runnermetrics.Summary       `json:"metrics"`
	Admission              engine.AdmissionDecision    `json:"admission"`
	MetricsHistoryPath     string                      `json:"metrics_history_path,omitempty"`
	MetricsWarning         string                      `json:"metrics_warning,omitempty"`
	Progress               []runnerAttemptProgress     `json:"progress,omitempty"`
}

type runnerAttemptProgress struct {
	ItemTitle       string   `json:"item_title"`
	Role            string   `json:"role"`
	Harness         string   `json:"harness"`
	Stage           string   `json:"stage"`
	ElapsedSeconds  int64    `json:"elapsed_seconds"`
	CompletedStages []string `json:"completed_stages,omitempty"`
}

func runStatus(ctx context.Context, args []string, stdout io.Writer) error {
	flags := newFlagSet("status", "cortexium-runner status [--config PATH] [--json] [--verbose]", stdout)
	configPath := flags.String("config", "", "runner config path; defaults to .cortexium/runner.json")
	jsonOutput := flags.Bool("json", false, "write status as JSON")
	verbose := flags.Bool("verbose", false, "show sanitized Runner stage progress")
	proceed, err := parseFlags(flags, args, "status")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load status config: %w", err)
	}
	service, err := engine.New(cfg, nil)
	if err != nil {
		return fmt.Errorf("configure runner: %w", err)
	}
	if !*jsonOutput {
		writeProgress(stdout, "Loading Runner process and GitHub Project status…")
	}
	work, err := service.WorkStatus(ctx)
	if err != nil {
		return fmt.Errorf("load current GitHub Project work: %w", err)
	}
	process, running, err := github.InspectProcessState(*cfg.GitHubProject)
	if err != nil {
		return err
	}
	status := runnerStatus{RunnerID: cfg.RunnerID, Project: cfg.GitHubProject, MaxParallelism: cfg.MaxParallelism, Running: running, Work: work}
	var metricAttempts []runnermetrics.Attempt
	if store, storeErr := runnermetrics.NewDefaultStore(cfg.RunnerID); storeErr != nil {
		status.MetricsWarning = storeErr.Error()
		status.Admission = admissionHistoryFailure(cfg.AdmissionBudget, "local metrics store is unavailable: "+storeErr.Error())
	} else {
		status.MetricsHistoryPath = store.Path()
		if history, readErr := store.Read(); readErr != nil {
			status.MetricsWarning = readErr.Error()
			status.Admission = admissionHistoryFailure(cfg.AdmissionBudget, "local metrics history cannot be read: "+readErr.Error())
		} else {
			metricAttempts = history.Attempts
			status.Metrics = runnermetrics.Summarize(history.Attempts)
			if history.MalformedRecords > 0 {
				status.MetricsWarning = fmt.Sprintf("ignored %d malformed history record(s)", history.MalformedRecords)
				status.Admission = admissionHistoryFailure(cfg.AdmissionBudget, fmt.Sprintf("local metrics history contains %d malformed record(s)", history.MalformedRecords))
			} else {
				status.Admission = engine.EvaluateAdmission(cfg.AdmissionBudget, history.Attempts, time.Now().UTC())
			}
		}
	}
	if running {
		status.Process = &process
		if *verbose {
			status.Progress = runnerProgress(metricAttempts, time.Now().UTC(), process.StartedAt)
		}
		observed, inspectionErr := inspectRunnerSubprocesses(ctx, process.PID, cfg.Harnesses)
		if inspectionErr != nil {
			status.ProcessInspectionError = inspectionErr.Error()
		} else {
			status.Subprocesses = annotateRunnerSubprocesses(observed, cfg, work.Active)
		}
		status.Processing = len(status.Subprocesses) > 0 || (len(work.Active) > 0 && (process.NextPollAt.IsZero() || !process.NextPollAt.After(time.Now())))
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(status)
	}
	fmt.Fprintf(stdout, "Runner: %s\nGitHub Project: %s/%d\nProcess: %s\n", cfg.RunnerID, cfg.GitHubProject.Owner, cfg.GitHubProject.Number, map[bool]string{true: "running", false: "stopped"}[running])
	fmt.Fprintf(stdout, "Concurrent agent work: up to %d independent card(s)\n", cfg.MaxParallelism)
	if status.Admission.Configured {
		state := "capacity available"
		if !status.Admission.Allowed {
			state = "paused — " + status.Admission.Summary()
		}
		fmt.Fprintf(stdout, "Agent admission: %s\n", state)
	}
	mergeMode := "human merge required"
	if cfg.GitHubProject.AutoMerge {
		mergeMode = fmt.Sprintf("automatic %s after GitHub requirements pass", config.NormalizeMergeMethod(cfg.GitHubProject.MergeMethod))
	}
	fmt.Fprintf(stdout, "Pull request merge: %s\n", mergeMode)
	if status.Metrics.Attempts > 0 {
		fmt.Fprintf(stdout, "Recorded agent usage: %d attempt(s) · %s agent time", status.Metrics.Attempts, formatMetricDuration(status.Metrics.HarnessDurationMilliseconds))
		if status.Metrics.UsageCoveredAttempts > 0 {
			fmt.Fprintf(stdout, " · %d input / %d cache read / %d output tokens", status.Metrics.Usage.InputTokens, status.Metrics.Usage.CacheReadInputTokens, status.Metrics.Usage.OutputTokens)
		}
		if status.Metrics.Usage.ReportedCostUSD != nil {
			fmt.Fprintf(stdout, " · $%.4f reported", *status.Metrics.Usage.ReportedCostUSD)
		}
		fmt.Fprintln(stdout)
	}
	if status.MetricsWarning != "" {
		fmt.Fprintf(stdout, "Metrics warning: %s\n", terminalSafeText(status.MetricsWarning))
	}
	if running {
		fmt.Fprintf(stdout, "PID: %d\n", process.PID)
		fmt.Fprintf(stdout, "Started: %s\n", formatStatusTime(process.StartedAt))
		if !process.StartedAt.IsZero() {
			fmt.Fprintf(stdout, "Uptime: %s\n", formatStatusDuration(time.Since(process.StartedAt)))
		}
		if !process.LastPollAt.IsZero() {
			fmt.Fprintf(stdout, "Last poll: %s\n", formatStatusTime(process.LastPollAt))
		}
		if status.Processing {
			fmt.Fprintln(stdout, "Next poll: currently processing")
		} else if !process.NextPollAt.IsZero() {
			fmt.Fprintf(stdout, "Next poll: %s\n", formatStatusTime(process.NextPollAt))
		} else {
			fmt.Fprintln(stdout, "Next poll: currently processing")
		}
		if strings.TrimSpace(process.LastError) != "" {
			fmt.Fprintf(stdout, "Last poll error: %s\n", terminalSafeText(process.LastError))
		}
		writeRunnerSubprocesses(stdout, status.Subprocesses, status.ProcessInspectionError, status.Processing)
		if *verbose {
			writeRunnerProgress(stdout, status.Progress)
		}
	} else {
		fmt.Fprintln(stdout, "Next poll: not scheduled")
	}
	statuses := make([]string, 0, len(work.ByStatus))
	for name := range work.ByStatus {
		statuses = append(statuses, name)
	}
	sort.Strings(statuses)
	fmt.Fprintf(stdout, "\nCurrent cards: %d\n", len(work.Items))
	for _, name := range statuses {
		label := name
		if strings.TrimSpace(label) == "" {
			label = "(no status)"
		}
		fmt.Fprintf(stdout, "  %s: %d\n", terminalSafeText(label), work.ByStatus[name])
	}
	writeWorkSection(stdout, "Active work", work.Active, false, *configPath)
	writeWorkSection(stdout, "Queued work", work.Queued, false, *configPath)
	writeWorkSection(stdout, "Blocked work", work.Blocked, true, *configPath)
	writeWorkSection(stdout, "PR ready", work.PRReady, false, *configPath)
	return nil
}

func runnerProgress(attempts []runnermetrics.Attempt, now, processStartedAt time.Time) []runnerAttemptProgress {
	progress := make([]runnerAttemptProgress, 0)
	for _, attempt := range attempts {
		if attempt.Completed || (!processStartedAt.IsZero() && attempt.StartedAt.Before(processStartedAt)) {
			continue
		}
		currentStage := "starting"
		startedAt := attempt.StartedAt
		completed := make([]string, 0, len(attempt.Stages))
		for _, stage := range attempt.Stages {
			if stage.Completed {
				completed = append(completed, runnerStageLabel(stage.Name))
				continue
			}
			currentStage = runnerStageLabel(stage.Name)
			if !stage.StartedAt.IsZero() {
				startedAt = stage.StartedAt
			}
		}
		elapsed := int64(0)
		if !startedAt.IsZero() && now.After(startedAt) {
			elapsed = int64(now.Sub(startedAt) / time.Second)
		}
		progress = append(progress, runnerAttemptProgress{
			ItemTitle: strings.TrimSpace(attempt.ItemTitle), Role: strings.TrimSpace(attempt.Role), Harness: strings.TrimSpace(attempt.Harness),
			Stage: currentStage, ElapsedSeconds: elapsed, CompletedStages: completed,
		})
	}
	return progress
}

func runnerStageLabel(stage string) string {
	switch stage {
	case runnermetrics.StageRepositoryPrepare:
		return "preparing repository"
	case runnermetrics.StageWorkspacePrepare:
		return "preparing workspace"
	case runnermetrics.StageHarnessRun:
		return "agent working"
	case runnermetrics.StagePlannerOutline:
		return "planning work outline"
	case runnermetrics.StagePlannerDetails:
		return "detailing planned work"
	case runnermetrics.StageReviewerAudit:
		return "auditing review evidence"
	case runnermetrics.StageReviewerVerify:
		return "running focused review checks"
	case runnermetrics.StageResultValidate:
		return "validating result"
	case runnermetrics.StageWorkspaceVerify:
		return "verifying workspace integrity"
	case runnermetrics.StageProjectTransition:
		return "updating project"
	case runnermetrics.StagePublishPullRequest:
		return "publishing pull request"
	case runnermetrics.StagePlannerApply:
		return "staging plan"
	default:
		return "working"
	}
}

func writeRunnerProgress(output io.Writer, progress []runnerAttemptProgress) {
	fmt.Fprintf(output, "Agent progress: %d\n", len(progress))
	for _, attempt := range progress {
		fmt.Fprintf(output, "  - %s · %s · %s\n", terminalSafeText(attempt.Role), terminalSafeText(attempt.Harness), terminalSafeText(attempt.ItemTitle))
		fmt.Fprintf(output, "    %s · %s elapsed\n", terminalSafeText(attempt.Stage), formatStatusDuration(time.Duration(attempt.ElapsedSeconds)*time.Second))
		if len(attempt.CompletedStages) > 0 {
			fmt.Fprintf(output, "    completed: %s\n", terminalSafeText(strings.Join(attempt.CompletedStages, ", ")))
		}
	}
}

func admissionHistoryFailure(budget *config.AdmissionBudgetConfig, reason string) engine.AdmissionDecision {
	if budget == nil {
		return engine.AdmissionDecision{Allowed: true}
	}
	copy := *budget
	return engine.AdmissionDecision{Configured: true, Allowed: false, Reason: strings.TrimSpace(reason), Budget: &copy}
}

func writeRunnerSubprocesses(output io.Writer, processes []runnerSubprocess, inspectionError string, currentlyProcessing bool) {
	if strings.TrimSpace(inspectionError) != "" {
		fmt.Fprintf(output, "Execution processes: unavailable (%s)\n", terminalSafeText(inspectionError))
		return
	}
	if len(processes) == 0 {
		if currentlyProcessing {
			fmt.Fprintln(output, "Execution processes: none detected; Runner is between external commands")
		}
		return
	}
	fmt.Fprintf(output, "Execution processes: %d\n", len(processes))
	for _, process := range processes {
		name := process.Command
		if process.Harness != "" {
			name = process.Harness
		}
		fmt.Fprintf(output, "  - %s · PID %d · %s · %s elapsed", terminalSafeText(name), process.PID, terminalSafeText(process.Health), formatStatusDuration(time.Duration(process.ElapsedSeconds)*time.Second))
		if process.TimeoutSeconds > 0 {
			fmt.Fprintf(output, " / %s timeout", formatStatusDuration(time.Duration(process.TimeoutSeconds)*time.Second))
		}
		fmt.Fprintln(output)
		if process.ItemTitle != "" {
			fmt.Fprintf(output, "    %s: %s\n", terminalSafeText(process.Role), terminalSafeText(process.ItemTitle))
		}
	}
}

func writeWorkSection(output io.Writer, title string, items []github.WorkItem, showResult bool, configPath string) {
	fmt.Fprintf(output, "\n%s: %d\n", title, len(items))
	for _, item := range items {
		fmt.Fprintf(output, "  - %s [%s]", terminalSafeText(item.Title), terminalSafeText(item.Status))
		if item.Role != "" {
			fmt.Fprintf(output, " · %s", terminalSafeText(item.Role))
		}
		if item.PullRequest != "" {
			fmt.Fprintf(output, " · %s", terminalSafeText(item.PullRequest))
		}
		fmt.Fprintln(output)
		if showResult {
			writeWorkResult(output, item.Result)
			if strings.TrimSpace(item.Phase) != "" && strings.TrimSpace(item.Role) != "" {
				fmt.Fprintf(output, "    Retry: cortexium-runner retry --config %q --item %s\n", configPath, terminalSafeText(item.ID))
			}
		}
	}
}

func writeWorkResult(output io.Writer, result string) {
	linesWritten := 0
	for _, rawLine := range strings.Split(result, "\n") {
		line := strings.Join(strings.Fields(rawLine), " ")
		if line == "" {
			continue
		}
		if len(line) > 240 {
			line = line[:237] + "..."
		}
		fmt.Fprintf(output, "    %s\n", terminalSafeText(line))
		linesWritten++
		if linesWritten == 3 {
			return
		}
	}
}

func formatStatusTime(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.Local().Format(time.RFC3339)
}

func formatStatusDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	return value.Truncate(time.Second).String()
}
