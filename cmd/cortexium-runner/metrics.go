package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
)

type metricsOutput struct {
	RunnerID         string                      `json:"runner_id"`
	Project          *config.GitHubProjectConfig `json:"project"`
	HistoryPath      string                      `json:"history_path"`
	Summary          runnermetrics.Summary       `json:"summary"`
	Attempts         []runnermetrics.Attempt     `json:"attempts"`
	MalformedRecords int                         `json:"malformed_records,omitempty"`
}

func runMetrics(args []string, stdout io.Writer) error {
	flags := newFlagSet("metrics", "cortexium-runner metrics [--config PATH] [--item ID|TITLE] [--json]", stdout)
	configPath := flags.String("config", "", "runner config path; defaults to .cortexium/runner.json")
	item := flags.String("item", "", "show attempts for one card ID or title")
	jsonOutput := flags.Bool("json", false, "write metrics as JSON")
	proceed, err := parseFlags(flags, args, "metrics")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("metrics does not accept positional arguments")
	}
	*configPath = resolveRunnerConfigPath(*configPath, "")
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load metrics config: %w", err)
	}
	store, err := runnermetrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		return err
	}
	history, err := store.Read()
	if err != nil {
		return err
	}
	attempts := filterMetricAttempts(history.Attempts, *item)
	if strings.TrimSpace(*item) != "" && len(attempts) == 0 {
		return fmt.Errorf("no recorded attempts match %q", strings.TrimSpace(*item))
	}
	view := metricsOutput{
		RunnerID: cfg.RunnerID, Project: cfg.GitHubProject, HistoryPath: store.Path(),
		Summary: runnermetrics.Summarize(attempts), Attempts: attempts, MalformedRecords: history.MalformedRecords,
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	writeMetrics(stdout, view)
	return nil
}

func filterMetricAttempts(attempts []runnermetrics.Attempt, selector string) []runnermetrics.Attempt {
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" {
		return append([]runnermetrics.Attempt(nil), attempts...)
	}
	result := []runnermetrics.Attempt{}
	for _, attempt := range attempts {
		if strings.EqualFold(strings.TrimSpace(attempt.ItemID), selector) ||
			strings.EqualFold(strings.TrimSpace(attempt.ItemTitle), selector) ||
			strings.Contains(strings.ToLower(attempt.ItemTitle), selector) {
			result = append(result, attempt)
		}
	}
	return result
}

func writeMetrics(output io.Writer, view metricsOutput) {
	project := "unknown"
	if view.Project != nil {
		project = fmt.Sprintf("%s/%d", view.Project.Owner, view.Project.Number)
	}
	fmt.Fprintf(output, "Runner metrics: %s\nGitHub Project: %s\n", view.RunnerID, project)
	if view.Summary.Attempts == 0 {
		fmt.Fprintln(output, "Recorded attempts: 0 (history starts after metrics-enabled Runner executions)")
		fmt.Fprintf(output, "History: %s\n", view.HistoryPath)
		return
	}
	fmt.Fprintf(output, "Recorded attempts: %d · %d completed · %d unfinished\n", view.Summary.Attempts, view.Summary.CompletedAttempts, view.Summary.UnfinishedAttempts)
	fmt.Fprintf(output, "Harness invocations: %d · saved-result resumes: %d\n", view.Summary.HarnessInvocations, view.Summary.ResumedCheckpointAttempts)
	fmt.Fprintf(output, "Agent time: %s · Runner/GitHub overhead: %s\n",
		formatMetricDuration(view.Summary.HarnessDurationMilliseconds), formatMetricDuration(view.Summary.RunnerDurationMilliseconds))
	usage := view.Summary.Usage
	if view.Summary.UsageCoveredAttempts > 0 {
		fmt.Fprintf(output, "Reported tokens: %d input · %d cache read · %d cache write · %d output",
			usage.InputTokens, usage.CacheReadInputTokens, usage.CacheWriteInputTokens, usage.OutputTokens)
		if usage.ReasoningOutputTokens > 0 {
			fmt.Fprintf(output, " · %d reasoning", usage.ReasoningOutputTokens)
		}
		fmt.Fprintf(output, " (%d/%d completed attempts reported usage)\n", view.Summary.UsageCoveredAttempts, view.Summary.CompletedAttempts)
	} else {
		fmt.Fprintln(output, "Reported tokens: unavailable from the recorded harness responses")
	}
	if usage.ReportedCostUSD != nil {
		fmt.Fprintf(output, "Reported cost: $%.4f (%d/%d completed attempts reported cost)\n", *usage.ReportedCostUSD, view.Summary.CostCoveredAttempts, view.Summary.CompletedAttempts)
	} else {
		fmt.Fprintln(output, "Reported cost: unavailable; Runner does not estimate it")
	}
	if view.MalformedRecords > 0 {
		fmt.Fprintf(output, "History warning: ignored %d malformed record(s)\n", view.MalformedRecords)
	}
	fmt.Fprintln(output, "\nAttempts:")
	for _, attempt := range view.Attempts {
		state := attempt.Outcome
		elapsed := time.Since(attempt.StartedAt)
		if attempt.Completed {
			elapsed = time.Duration(attempt.DurationMilliseconds) * time.Millisecond
		} else {
			state = "unfinished"
		}
		model := strings.TrimSpace(attempt.Model)
		if model == "" {
			model = "harness-native"
		}
		if len(attempt.Usage.Models) > 0 {
			reportedModels := make([]string, 0, len(attempt.Usage.Models))
			for reportedModel := range attempt.Usage.Models {
				reportedModels = append(reportedModels, reportedModel)
			}
			sort.Strings(reportedModels)
			model += " (reported: " + strings.Join(reportedModels, ", ") + ")"
		}
		reasoning := strings.TrimSpace(attempt.Reasoning)
		if reasoning == "" {
			reasoning = "harness-native"
		}
		fmt.Fprintf(output, "  - %s · %s · %s/%s · %s · reasoning %s · iteration %d · %s\n", attempt.StartedAt.Local().Format(time.RFC3339), terminalSafeText(state), terminalSafeText(attempt.Role), terminalSafeText(attempt.Harness), terminalSafeText(model), terminalSafeText(reasoning), attempt.Iteration, formatStatusDuration(elapsed))
		fmt.Fprintf(output, "    %s\n", terminalSafeText(attempt.ItemTitle))
		if attempt.Completed && strings.TrimSpace(attempt.Summary) != "" {
			fmt.Fprintf(output, "    %s\n", terminalSafeText(strings.Join(strings.Fields(attempt.Summary), " ")))
		}
		if attempt.ResumedCheckpoint {
			fmt.Fprintln(output, "    resumed: exact saved implementation result; harness was not invoked again")
		}
		if attempt.FailureClass != "" {
			fmt.Fprintf(output, "    recovery: %s", terminalSafeText(string(attempt.FailureClass)))
			if attempt.RetryDisposition != "" {
				fmt.Fprintf(output, " · retry %s", terminalSafeText(string(attempt.RetryDisposition)))
			}
			if attempt.RetryAfter != "" {
				fmt.Fprintf(output, " · after %s", terminalSafeText(attempt.RetryAfter))
			}
			fmt.Fprintln(output)
		}
		if attempt.FailureOperation != "" {
			fmt.Fprintf(output, "    failed operation: %s", terminalSafeText(attempt.FailureOperation))
			if attempt.PublicationAttempts > 0 {
				fmt.Fprintf(output, " · %d attempt(s)", attempt.PublicationAttempts)
			}
			fmt.Fprintln(output)
		} else if attempt.PublicationAttempts > 1 {
			fmt.Fprintf(output, "    publication recovered after %d attempts\n", attempt.PublicationAttempts)
		}
		if attempt.Usage.ReportedCostUSD != nil {
			fmt.Fprintf(output, "    usage: %d input · %d cache read · %d output · $%.4f reported\n",
				attempt.Usage.InputTokens, attempt.Usage.CacheReadInputTokens, attempt.Usage.OutputTokens, *attempt.Usage.ReportedCostUSD)
		} else if attempt.Usage.Available {
			fmt.Fprintf(output, "    usage: %d input · %d cache read · %d output · cost not reported\n",
				attempt.Usage.InputTokens, attempt.Usage.CacheReadInputTokens, attempt.Usage.OutputTokens)
		} else if attempt.Completed {
			fmt.Fprintln(output, "    usage: not reported by harness")
		}
		writeMetricEvidence(output, "work", attempt.WorkDone)
		writeMetricEvidence(output, "verification", attempt.Verification)
		writeMetricStages(output, attempt.Stages)
	}
	fmt.Fprintf(output, "\nHistory: %s\n", terminalSafeText(view.HistoryPath))
}

func writeMetricStages(output io.Writer, stages []runnermetrics.Stage) {
	for _, stage := range stages {
		state := stage.Outcome
		if !stage.Completed {
			state = "unfinished"
		}
		fmt.Fprintf(output, "    stage: %s · %s · %s", terminalSafeText(stage.Name), terminalSafeText(state), formatMetricDuration(stage.DurationMilliseconds))
		if stage.FailureClass != "" {
			fmt.Fprintf(output, " · %s", terminalSafeText(string(stage.FailureClass)))
		}
		if stage.RetryDisposition != "" {
			fmt.Fprintf(output, " · retry %s", terminalSafeText(string(stage.RetryDisposition)))
		}
		fmt.Fprintln(output)
	}
}

func writeMetricEvidence(output io.Writer, label string, values []string) {
	if len(values) == 0 {
		return
	}
	const maximum = 3
	shown := values
	if len(shown) > maximum {
		shown = shown[:maximum]
	}
	for _, value := range shown {
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			fmt.Fprintf(output, "    %s: %s\n", terminalSafeText(label), terminalSafeText(value))
		}
	}
	if len(values) > maximum {
		fmt.Fprintf(output, "    %s: … and %d more\n", terminalSafeText(label), len(values)-maximum)
	}
}

func formatMetricDuration(milliseconds int64) string {
	return formatStatusDuration(time.Duration(milliseconds) * time.Millisecond)
}
