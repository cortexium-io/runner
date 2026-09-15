package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	runnermetrics "github.com/cortexium-io/runner/internal/metrics"
	bundledskills "github.com/cortexium-io/runner/skills"
)

// Snapshot once at service/CLI construction, not at export time. Only the digest
// is retained: config paths, commands, environment values and reference contents
// do not become new telemetry payloads. This is not an effective-harness digest.
func metricsRunObserver(cfg config.Config, observe func(runnermetrics.Event) error) func(runnermetrics.Event) error {
	encoded, err := json.Marshal(cfg)
	identity := runnermetrics.RunContext{
		RunnerVersion: buildVersion(), BundledSkillsVersion: bundledskills.BundledVersion,
		ConfigDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)),
	}
	return func(event runnermetrics.Event) error {
		if err != nil {
			return errors.New("metrics run configuration could not be fingerprinted")
		}
		context := identity
		event.RunContext = &context
		return observe(event)
	}
}

type metricsOutput struct {
	RunnerID         string                      `json:"runner_id"`
	Project          *config.GitHubProjectConfig `json:"project"`
	HistoryPath      string                      `json:"history_path"`
	Summary          runnermetrics.Summary       `json:"summary"`
	Attempts         []runnermetrics.Attempt     `json:"-"`
	History          []metricAttemptHistory      `json:"attempts"`
	MalformedRecords int                         `json:"malformed_records,omitempty"`
}

// metricAttemptHistory is an output-only projection of the private metrics
// record. Grouping by provenance prevents model reports from appearing to be
// Runner observations while retaining every bounded fact needed to inspect the
// attempt. It is read-only and is never consumed by workflow code.
type metricAttemptHistory struct {
	AttemptID             string                    `json:"attempt_id"`
	RunnerObserved        metricRunnerObserved      `json:"runner_observed"`
	ModelReported         metricModelReported       `json:"model_reported"`
	ProvenanceUnavailable *metricUnattributedReport `json:"provenance_unavailable,omitempty"`
	Unavailable           []string                  `json:"unavailable,omitempty"`
}

type metricRunnerObserved struct {
	RunnerID                    string                         `json:"runner_id,omitempty"`
	RunContext                  *runnermetrics.RunContext      `json:"run_context,omitempty"`
	ProjectOwner                string                         `json:"project_owner,omitempty"`
	ProjectNumber               int                            `json:"project_number,omitempty"`
	Repository                  string                         `json:"repository,omitempty"`
	ItemID                      string                         `json:"item_id,omitempty"`
	ItemTitle                   string                         `json:"item_title,omitempty"`
	Role                        string                         `json:"role,omitempty"`
	Harness                     string                         `json:"harness,omitempty"`
	Model                       string                         `json:"configured_model,omitempty"`
	Reasoning                   string                         `json:"configured_reasoning,omitempty"`
	Iteration                   int                            `json:"iteration,omitempty"`
	StartedAt                   *time.Time                     `json:"started_at,omitempty"`
	FinishedAt                  *time.Time                     `json:"finished_at,omitempty"`
	Completed                   bool                           `json:"completed"`
	DurationMilliseconds        int64                          `json:"duration_milliseconds,omitempty"`
	HarnessDurationMilliseconds int64                          `json:"harness_duration_milliseconds,omitempty"`
	Outcome                     string                         `json:"outcome,omitempty"`
	Summary                     string                         `json:"summary,omitempty"`
	FailureClass                string                         `json:"failure_class,omitempty"`
	FailureOperation            string                         `json:"failure_operation,omitempty"`
	PublicationAttempts         int                            `json:"publication_attempts,omitempty"`
	RetryDisposition            string                         `json:"retry_disposition,omitempty"`
	RetryAfter                  string                         `json:"retry_after,omitempty"`
	ResumedCheckpoint           bool                           `json:"resumed_checkpoint,omitempty"`
	ApprovedRequest             *runnermetrics.ApprovedRequest `json:"approved_request,omitempty"`
	Lineage                     *runnermetrics.ObservedLineage `json:"lineage,omitempty"`
	LegacyCandidateCommitOID    string                         `json:"legacy_candidate_commit_oid,omitempty"`
	PromptContexts              []runnermetrics.PromptContext  `json:"prompt_contexts,omitempty"`
	Stages                      []metricStageHistory           `json:"stages,omitempty"`
}

type metricModelReported struct {
	Rationale      string                        `json:"rationale,omitempty"`
	Actions        []string                      `json:"actions,omitempty"`
	Verification   []string                      `json:"verification,omitempty"`
	ReviewVerdict  string                        `json:"review_verdict,omitempty"`
	ReviewFindings []runnermetrics.ReviewFinding `json:"review_findings,omitempty"`
	ReviewDetails  []runnermetrics.ReviewDetail  `json:"review_details,omitempty"`
	Complete       *bool                         `json:"complete,omitempty"`
	Usage          *runnermetrics.Usage          `json:"usage,omitempty"`
}

type metricUnattributedReport struct {
	Summary string `json:"legacy_summary"`
}

type metricStageHistory struct {
	StageID              string                        `json:"stage_id"`
	Name                 string                        `json:"name"`
	StartedAt            *time.Time                    `json:"runner_observed_started_at,omitempty"`
	FinishedAt           *time.Time                    `json:"runner_observed_finished_at,omitempty"`
	DurationMilliseconds int64                         `json:"runner_observed_duration_milliseconds,omitempty"`
	Outcome              string                        `json:"runner_observed_outcome,omitempty"`
	FailureClass         string                        `json:"runner_observed_failure_class,omitempty"`
	RetryDisposition     string                        `json:"runner_observed_retry_disposition,omitempty"`
	PromptContexts       []runnermetrics.PromptContext `json:"runner_observed_prompt_contexts,omitempty"`
	Completed            bool                          `json:"runner_observed_completed"`
	Usage                *runnermetrics.Usage          `json:"model_reported_usage,omitempty"`
	Unavailable          []string                      `json:"unavailable,omitempty"`
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
	attempts, err := filterMetricAttempts(history.Attempts, *item)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*item) != "" && len(attempts) == 0 {
		return fmt.Errorf("no recorded attempts match %q", strings.TrimSpace(*item))
	}
	sortMetricAttemptsChronologically(attempts)
	view := metricsOutput{
		RunnerID: cfg.RunnerID, Project: cfg.GitHubProject, HistoryPath: store.Path(),
		Summary: runnermetrics.Summarize(attempts), Attempts: attempts, History: metricAttemptHistories(attempts), MalformedRecords: history.MalformedRecords,
	}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	writeMetrics(stdout, view)
	return nil
}

func sortMetricAttemptsChronologically(attempts []runnermetrics.Attempt) {
	sort.SliceStable(attempts, func(i, j int) bool {
		left, right := attempts[i].StartedAt, attempts[j].StartedAt
		if left.IsZero() {
			return false
		}
		if right.IsZero() {
			return true
		}
		return left.Before(right)
	})
}

func metricAttemptHistories(attempts []runnermetrics.Attempt) []metricAttemptHistory {
	history := make([]metricAttemptHistory, 0, len(attempts))
	for _, attempt := range attempts {
		history = append(history, metricAttemptHistoryFor(attempt))
	}
	return history
}

func metricAttemptHistoryFor(attempt runnermetrics.Attempt) metricAttemptHistory {
	observed := metricRunnerObserved{
		RunnerID: attempt.RunnerID, RunContext: attempt.RunContext, ProjectOwner: attempt.ProjectOwner, ProjectNumber: attempt.ProjectNumber,
		Repository: attempt.Repository, ItemID: attempt.ItemID, ItemTitle: attempt.ItemTitle, Role: attempt.Role, Harness: attempt.Harness,
		Model: attempt.Model, Reasoning: attempt.Reasoning, Iteration: attempt.Iteration,
		Completed: attempt.Completed, DurationMilliseconds: attempt.DurationMilliseconds, HarnessDurationMilliseconds: attempt.HarnessDurationMilliseconds,
		Outcome: attempt.Outcome, Summary: attempt.RunnerObservation, FailureClass: attempt.FailureClass, FailureOperation: attempt.FailureOperation,
		PublicationAttempts: attempt.PublicationAttempts, RetryDisposition: attempt.RetryDisposition, RetryAfter: attempt.RetryAfter,
		ResumedCheckpoint: attempt.ResumedCheckpoint, ApprovedRequest: attempt.ApprovedRequest, Lineage: attempt.Lineage,
		LegacyCandidateCommitOID: attempt.CandidateOID, PromptContexts: append([]runnermetrics.PromptContext(nil), attempt.PromptContexts...),
	}
	if !attempt.StartedAt.IsZero() {
		started := attempt.StartedAt
		observed.StartedAt = &started
	}
	if attempt.Completed && !attempt.FinishedAt.IsZero() {
		finished := attempt.FinishedAt
		observed.FinishedAt = &finished
	}
	for _, stage := range attempt.Stages {
		stageHistory := metricStageHistory{
			StageID: stage.StageID, Name: stage.Name, DurationMilliseconds: stage.DurationMilliseconds,
			Outcome: stage.Outcome, FailureClass: stage.FailureClass, RetryDisposition: stage.RetryDisposition,
			PromptContexts: append([]runnermetrics.PromptContext(nil), stage.PromptContexts...), Completed: stage.Completed,
		}
		if !stage.StartedAt.IsZero() {
			started := stage.StartedAt
			stageHistory.StartedAt = &started
		} else {
			stageHistory.Unavailable = append(stageHistory.Unavailable, "runner_observed_started_at")
		}
		if stage.Completed && !stage.FinishedAt.IsZero() {
			finished := stage.FinishedAt
			stageHistory.FinishedAt = &finished
		} else if stage.Completed {
			stageHistory.Unavailable = append(stageHistory.Unavailable, "runner_observed_finished_at")
		}
		if !stage.Completed || strings.TrimSpace(stage.Outcome) == "" {
			stageHistory.Unavailable = append(stageHistory.Unavailable, "runner_observed_outcome")
		}
		if stage.Usage.Available {
			usage := stage.Usage
			stageHistory.Usage = &usage
		} else {
			stageHistory.Unavailable = append(stageHistory.Unavailable, "model_reported_usage")
		}
		observed.Stages = append(observed.Stages, stageHistory)
	}
	reported := metricModelReported{
		Rationale: attempt.ModelReportedSummary, Actions: append([]string(nil), attempt.WorkDone...),
		Verification: append([]string(nil), attempt.Verification...), ReviewVerdict: attempt.ReviewVerdict,
		ReviewFindings: append([]runnermetrics.ReviewFinding(nil), attempt.ReviewFindings...),
		ReviewDetails:  append([]runnermetrics.ReviewDetail(nil), attempt.ReviewDetails...), Complete: attempt.ModelReportComplete,
	}
	if attempt.Usage.Available {
		usage := attempt.Usage
		reported.Usage = &usage
	}
	result := metricAttemptHistory{AttemptID: attempt.AttemptID, RunnerObserved: observed, ModelReported: reported}
	if attempt.Summary != "" {
		result.ProvenanceUnavailable = &metricUnattributedReport{Summary: attempt.Summary}
		result.Unavailable = append(result.Unavailable, "provenance_unavailable.legacy_summary.source")
	}
	result.Unavailable = append(result.Unavailable, unavailableAttemptFacts(attempt)...)
	return result
}

func unavailableAttemptFacts(attempt runnermetrics.Attempt) []string {
	var unavailable []string
	if attempt.RunContext == nil {
		unavailable = append(unavailable, "runner_observed.run_context")
	}
	for _, value := range []struct {
		name, value string
	}{
		{"item_id", attempt.ItemID}, {"repository", attempt.Repository}, {"configured_model", attempt.Model},
		{"configured_reasoning", attempt.Reasoning}, {"summary", attempt.RunnerObservation},
	} {
		if strings.TrimSpace(value.value) == "" {
			unavailable = append(unavailable, "runner_observed."+value.name)
		}
	}
	if attempt.StartedAt.IsZero() {
		unavailable = append(unavailable, "runner_observed.started_at")
	}
	if attempt.Completed && attempt.FinishedAt.IsZero() {
		unavailable = append(unavailable, "runner_observed.finished_at")
	}
	if !attempt.Completed || strings.TrimSpace(attempt.Outcome) == "" {
		unavailable = append(unavailable, "runner_observed.outcome")
	}
	if attempt.ApprovedRequest == nil {
		unavailable = append(unavailable, "runner_observed.approved_request")
	}
	lineage := attempt.Lineage
	if lineage == nil {
		lineage = &runnermetrics.ObservedLineage{}
	}
	identities := []struct {
		name  string
		value string
	}{
		{"repository", lineage.Repository}, {"branch", lineage.Branch},
		{"base.commit_oid", lineage.Base.CommitOID}, {"base.tree_oid", lineage.Base.TreeOID},
		{"candidate.commit_oid", lineage.Candidate.CommitOID}, {"candidate.tree_oid", lineage.Candidate.TreeOID},
		{"evidence_candidate.commit_oid", lineage.EvidenceCandidate.CommitOID}, {"evidence_candidate.tree_oid", lineage.EvidenceCandidate.TreeOID},
		{"reviewed_candidate.commit_oid", lineage.ReviewedCandidate.CommitOID}, {"reviewed_candidate.tree_oid", lineage.ReviewedCandidate.TreeOID},
		{"rebased_candidate.commit_oid", lineage.RebasedCandidate.CommitOID}, {"rebased_candidate.tree_oid", lineage.RebasedCandidate.TreeOID},
		{"published_candidate.commit_oid", lineage.PublishedCandidate.CommitOID}, {"published_candidate.tree_oid", lineage.PublishedCandidate.TreeOID},
		{"pull_request.url", lineage.PullRequestURL}, {"merge.commit_oid", lineage.Merge.CommitOID}, {"merge.tree_oid", lineage.Merge.TreeOID},
	}
	for _, identity := range identities {
		if strings.TrimSpace(identity.value) == "" {
			unavailable = append(unavailable, "runner_observed.lineage."+identity.name)
		}
	}
	if lineage.PullRequestNumber == 0 {
		unavailable = append(unavailable, "runner_observed.lineage.pull_request.number")
	}
	if attempt.ModelReportComplete == nil {
		unavailable = append(unavailable, "model_reported.complete")
	}
	if attempt.ModelReportComplete == nil || !*attempt.ModelReportComplete {
		for _, value := range []struct {
			name    string
			missing bool
		}{
			{"rationale", strings.TrimSpace(attempt.ModelReportedSummary) == ""}, {"actions", len(attempt.WorkDone) == 0},
			{"verification", len(attempt.Verification) == 0}, {"review_verdict", strings.TrimSpace(attempt.ReviewVerdict) == ""},
			{"review_findings", len(attempt.ReviewFindings) == 0}, {"review_details", len(attempt.ReviewDetails) == 0},
		} {
			if value.missing {
				unavailable = append(unavailable, "model_reported."+value.name)
			}
		}
	}
	if !attempt.Usage.Available {
		unavailable = append(unavailable, "model_reported.usage")
	}
	return unavailable
}

func filterMetricAttempts(attempts []runnermetrics.Attempt, selector string) ([]runnermetrics.Attempt, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return append([]runnermetrics.Attempt(nil), attempts...), nil
	}
	var result []runnermetrics.Attempt
	for _, attempt := range attempts {
		if strings.TrimSpace(attempt.ItemID) == selector {
			result = append(result, attempt)
		}
	}
	if len(result) > 0 {
		return result, nil
	}
	// A shared title does not prove that records with missing IDs belong to the
	// same card. Multiple title matches are safe only when every retained record
	// carries the same non-empty exact item ID.
	matchedItemID := ""
	for _, attempt := range attempts {
		if !strings.EqualFold(strings.TrimSpace(attempt.ItemTitle), selector) {
			continue
		}
		itemID := strings.TrimSpace(attempt.ItemID)
		if len(result) > 0 && (itemID == "" || matchedItemID == "" || itemID != matchedItemID) {
			return nil, fmt.Errorf("item title %q matches records without one unambiguous card ID; use an exact item ID", selector)
		}
		matchedItemID = itemID
		result = append(result, attempt)
	}
	return result, nil
}

func writeMetrics(output io.Writer, view metricsOutput) {
	project := "unknown"
	if view.Project != nil {
		project = fmt.Sprintf("%s/%d", view.Project.Owner, view.Project.Number)
	}
	fmt.Fprintf(output, "Runner metrics: %s\nGitHub Project: %s\n", terminalSafeText(view.RunnerID), terminalSafeText(project))
	if view.MalformedRecords > 0 {
		fmt.Fprintf(output, "History warning: ignored %d malformed record(s)\n", view.MalformedRecords)
	}
	if len(view.Attempts) == 0 {
		fmt.Fprintln(output, "Recorded attempts: 0 (history starts after metrics-enabled Runner executions)")
		fmt.Fprintf(output, "History: %s\n", terminalSafeText(view.HistoryPath))
		return
	}
	fmt.Fprintf(output, "Recorded attempts: %d · %d completed · %d unfinished\n", view.Summary.Attempts, view.Summary.CompletedAttempts, view.Summary.UnfinishedAttempts)
	fmt.Fprintf(output, "Harness invocations: %d · saved-result resumes: %d\n", view.Summary.HarnessInvocations, view.Summary.ResumedCheckpointAttempts)
	if view.Summary.ReviewVerdictCoveredAttempts > 0 {
		fmt.Fprintf(output, "Recorded QA verdicts: %d · %d accepted · %d changes requested · %d blocked\n",
			view.Summary.ReviewVerdictCoveredAttempts, view.Summary.ReviewAcceptedAttempts, view.Summary.ReviewChangesRequestedAttempts, view.Summary.ReviewBlockedAttempts)
	} else {
		fmt.Fprintln(output, "Recorded QA verdicts: unavailable (no validated verdicts recorded)")
	}
	fmt.Fprintln(output, "QA verdicts are separate from publication outcomes; missing verdicts are not inferred, and saved-acceptance resumes do not add a new verdict.")
	fmt.Fprintf(output, "Agent time: %s · Runner/GitHub overhead: %s\n",
		formatMetricDuration(view.Summary.HarnessDurationMilliseconds), formatMetricDuration(view.Summary.RunnerDurationMilliseconds))
	if view.Summary.StageCoveredAttempts > 0 {
		fmt.Fprintf(output, "Stage evidence: %d/%d attempts · successful attempts with failed/blocked stages: %d · recovered publication retries: %d\n",
			view.Summary.StageCoveredAttempts, view.Summary.Attempts, view.Summary.RecoveredStageFailureAttempts, view.Summary.RecoveredPublicationAttempts)
		for _, stage := range view.Summary.Stages {
			fmt.Fprintf(output, "  %s: %d/%d completed · %d failed · %d blocked · %s recorded\n",
				terminalSafeText(stage.Name), stage.Completed, stage.Runs, stage.Failed, stage.Blocked, formatMetricDuration(stage.DurationMilliseconds))
		}
		fmt.Fprintln(output, "Stage durations may overlap; they are not extra wall time.")
	} else {
		fmt.Fprintln(output, "Stage evidence: unavailable from the recorded attempts")
	}
	fmt.Fprintln(output, "Individual test-command duration/repetition and between-attempt wait causes: unavailable; stage time is not test time.")
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
	fmt.Fprintln(output, "\nChronological history (oldest first):")
	for index, attempt := range view.Attempts {
		state := attempt.Outcome
		duration := "unavailable"
		if attempt.Completed {
			duration = formatStatusDuration(time.Duration(attempt.DurationMilliseconds) * time.Millisecond)
		} else {
			state = "unfinished"
			if !attempt.StartedAt.IsZero() {
				duration = formatStatusDuration(time.Since(attempt.StartedAt))
			}
		}
		if strings.TrimSpace(state) == "" {
			state = "unavailable"
		}
		model := strings.TrimSpace(attempt.Model)
		if model == "" {
			model = "unavailable"
		}
		reasoning := strings.TrimSpace(attempt.Reasoning)
		if reasoning == "" {
			reasoning = "unavailable"
		}
		fmt.Fprintf(output, "  %d. %s · attempt %s · %s/%s · %s\n", index+1, metricTime(attempt.StartedAt), terminalSafeText(attempt.AttemptID), metricKnown(attempt.Role), metricKnown(attempt.Harness), metricKnown(attempt.ItemTitle))
		fmt.Fprintln(output, "    Runner-observed:")
		fmt.Fprintf(output, "      state: %s · %s · configured model %s · configured reasoning %s · iteration %d\n", terminalSafeText(state), duration, terminalSafeText(model), terminalSafeText(reasoning), attempt.Iteration)
		fmt.Fprintf(output, "      finished: %s · runner %s · project %s/%s\n", metricTime(attempt.FinishedAt), metricKnown(attempt.RunnerID), metricKnown(attempt.ProjectOwner), metricKnownNumber(attempt.ProjectNumber))
		fmt.Fprintf(output, "      item: %s · repository %s\n", metricKnown(attempt.ItemID), metricKnown(attempt.Repository))
		if identity := attempt.RunContext; identity != nil {
			fmt.Fprintf(output, "      run: %s · bundled skills %s · config %s\n", terminalSafeText(identity.RunnerVersion), terminalSafeText(identity.BundledSkillsVersion), terminalSafeText(identity.ConfigDigest))
		} else {
			fmt.Fprintln(output, "      run: unavailable")
		}
		if attempt.ApprovedRequest != nil {
			fmt.Fprintf(output, "      approval digest: %s\n", terminalSafeText(attempt.ApprovedRequest.DelegatedContentDigest))
			fmt.Fprintf(output, "      protected canonical approved content (terminal-escaped): %s\n", terminalSafeText(attempt.ApprovedRequest.Snapshot))
		} else {
			fmt.Fprintln(output, "      approval digest: unavailable")
			fmt.Fprintln(output, "      protected canonical approved content: unavailable")
		}
		if strings.TrimSpace(attempt.RunnerObservation) != "" {
			fmt.Fprintf(output, "      outcome explanation: %s\n", terminalSafeText(strings.Join(strings.Fields(attempt.RunnerObservation), " ")))
		} else {
			fmt.Fprintln(output, "      outcome explanation: unavailable")
		}
		writeMetricLineage(output, attempt)
		if attempt.ResumedCheckpoint {
			fmt.Fprintln(output, "      resumed: exact saved checkpoint; harness was not invoked again")
		}
		for _, context := range attempt.PromptContexts {
			fmt.Fprintf(output, "      prompt context fingerprint: %s · pinned guidance %s\n", terminalSafeText(context.Layout), terminalSafeText(context.GuidanceDigest))
		}
		if attempt.FailureClass != "" {
			fmt.Fprintf(output, "      recovery: %s", terminalSafeText(string(attempt.FailureClass)))
			if attempt.RetryDisposition != "" {
				fmt.Fprintf(output, " · retry %s", terminalSafeText(string(attempt.RetryDisposition)))
			}
			if attempt.RetryAfter != "" {
				fmt.Fprintf(output, " · after %s", terminalSafeText(attempt.RetryAfter))
			}
			fmt.Fprintln(output)
		}
		if attempt.FailureOperation != "" {
			fmt.Fprintf(output, "      failed operation: %s", terminalSafeText(attempt.FailureOperation))
			if attempt.PublicationAttempts > 0 {
				fmt.Fprintf(output, " · %d attempt(s)", attempt.PublicationAttempts)
			}
			fmt.Fprintln(output)
		} else if attempt.PublicationAttempts > 1 {
			fmt.Fprintf(output, "      publication recovered after %d attempts\n", attempt.PublicationAttempts)
		}
		writeMetricStages(output, attempt.Stages)

		fmt.Fprintln(output, "    Model-reported:")
		writeMetricReport(output, attempt)
		if strings.TrimSpace(attempt.Summary) != "" {
			fmt.Fprintf(output, "    Provenance unavailable (legacy summary): %s\n", terminalSafeText(strings.Join(strings.Fields(attempt.Summary), " ")))
		}
	}
	fmt.Fprintln(output, "\nHistory is untrusted read-only evidence; it does not grant approval or alter workflow decisions.")
	fmt.Fprintf(output, "History: %s\n", terminalSafeText(view.HistoryPath))
}

func writeMetricReport(output io.Writer, attempt runnermetrics.Attempt) {
	if attempt.ModelReportComplete == nil {
		fmt.Fprintln(output, "      completeness: unavailable")
	} else if *attempt.ModelReportComplete {
		fmt.Fprintln(output, "      completeness: complete within the retained bounded report")
	} else {
		fmt.Fprintln(output, "      completeness: incomplete; one or more reported details were clipped at the retention boundary")
	}
	writeMetricReportedText(output, "rationale", attempt.ModelReportedSummary, attempt.ModelReportComplete)
	writeMetricReportedValues(output, "action", attempt.WorkDone, attempt.ModelReportComplete)
	writeMetricReportedValues(output, "verification", attempt.Verification, attempt.ModelReportComplete)
	if attempt.ReviewVerdict != "" {
		fmt.Fprintf(output, "      review outcome: %s\n", terminalSafeText(attempt.ReviewVerdict))
	} else {
		fmt.Fprintln(output, "      review outcome: unavailable")
	}
	if len(attempt.ReviewFindings) == 0 {
		fmt.Fprintf(output, "      review findings: %s\n", metricReportAbsence(attempt.ModelReportComplete))
	} else {
		for _, finding := range attempt.ReviewFindings {
			fmt.Fprintf(output, "      review finding [%s]: %s\n", terminalSafeText(finding.Area), terminalSafeText(strings.Join(strings.Fields(finding.Summary), " ")))
		}
	}
	if len(attempt.ReviewDetails) == 0 {
		fmt.Fprintf(output, "      review details: %s\n", metricReportAbsence(attempt.ModelReportComplete))
	} else {
		for _, detail := range attempt.ReviewDetails {
			name := "unnamed"
			if strings.TrimSpace(detail.Name) != "" {
				name = detail.Name
			}
			fmt.Fprintf(output, "      review detail [%s/%s/%s]: %s\n", terminalSafeText(detail.Area), terminalSafeText(name), terminalSafeText(detail.Status), terminalSafeText(strings.Join(strings.Fields(detail.Summary), " ")))
			writeMetricReportedValues(output, "review evidence", detail.Evidence, attempt.ModelReportComplete)
		}
	}
	if attempt.Usage.ReportedCostUSD != nil {
		fmt.Fprintf(output, "      usage: %d input · %d cache read · %d cache write · %d output · $%.4f reported\n",
			attempt.Usage.InputTokens, attempt.Usage.CacheReadInputTokens, attempt.Usage.CacheWriteInputTokens, attempt.Usage.OutputTokens, *attempt.Usage.ReportedCostUSD)
	} else if attempt.Usage.Available {
		fmt.Fprintf(output, "      usage: %d input · %d cache read · %d cache write · %d output · cost unavailable\n",
			attempt.Usage.InputTokens, attempt.Usage.CacheReadInputTokens, attempt.Usage.CacheWriteInputTokens, attempt.Usage.OutputTokens)
	} else {
		fmt.Fprintln(output, "      usage: unavailable; not zero")
	}
	if len(attempt.Usage.Models) > 0 {
		models := make([]string, 0, len(attempt.Usage.Models))
		for model := range attempt.Usage.Models {
			models = append(models, model)
		}
		sort.Strings(models)
		for _, model := range models {
			usage := attempt.Usage.Models[model]
			fmt.Fprintf(output, "      usage model %s: %d input · %d cache read · %d cache write · %d output",
				terminalSafeText(model), usage.InputTokens, usage.CacheReadInputTokens, usage.CacheWriteInputTokens, usage.OutputTokens)
			if usage.ReportedCostUSD != nil {
				fmt.Fprintf(output, " · $%.4f reported", *usage.ReportedCostUSD)
			} else {
				fmt.Fprint(output, " · cost unavailable")
			}
			fmt.Fprintln(output)
		}
	}
}

func writeMetricReportedText(output io.Writer, label, value string, complete *bool) {
	if strings.TrimSpace(value) == "" {
		fmt.Fprintf(output, "      %s: %s\n", terminalSafeText(label), metricReportAbsence(complete))
		return
	}
	fmt.Fprintf(output, "      %s: %s\n", terminalSafeText(label), terminalSafeText(strings.Join(strings.Fields(value), " ")))
}

func writeMetricReportedValues(output io.Writer, label string, values []string, complete *bool) {
	if len(values) == 0 {
		fmt.Fprintf(output, "      %s: %s\n", terminalSafeText(label), metricReportAbsence(complete))
		return
	}
	for _, value := range values {
		if value = strings.Join(strings.Fields(value), " "); value != "" {
			fmt.Fprintf(output, "      %s: %s\n", terminalSafeText(label), terminalSafeText(value))
		}
	}
}

func metricReportAbsence(complete *bool) string {
	if complete != nil && *complete {
		return "none reported"
	}
	return "unavailable"
}

func metricKnown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unavailable"
	}
	return terminalSafeText(value)
}

func metricTime(value time.Time) string {
	if value.IsZero() {
		return "unavailable"
	}
	return value.Local().Format(time.RFC3339)
}

func writeMetricLineage(output io.Writer, attempt runnermetrics.Attempt) {
	lineage := attempt.Lineage
	if lineage == nil {
		lineage = &runnermetrics.ObservedLineage{}
	}
	fmt.Fprintf(output, "      lineage repository: %s · branch %s\n", metricKnown(lineage.Repository), metricKnown(lineage.Branch))
	writeMetricObjectIdentity(output, "base", lineage.Base)
	writeMetricObjectIdentity(output, "candidate", lineage.Candidate)
	writeMetricObjectIdentity(output, "verification evidence candidate", lineage.EvidenceCandidate)
	writeMetricObjectIdentity(output, "reviewed candidate", lineage.ReviewedCandidate)
	writeMetricObjectIdentity(output, "later rebased candidate", lineage.RebasedCandidate)
	writeMetricObjectIdentity(output, "later published candidate", lineage.PublishedCandidate)
	fmt.Fprintf(output, "      pull request: %s · number %s\n", metricKnown(lineage.PullRequestURL), metricKnownNumber(lineage.PullRequestNumber))
	writeMetricObjectIdentity(output, "merge", lineage.Merge)
	if attempt.CandidateOID != "" && attempt.CandidateOID != lineage.Candidate.CommitOID {
		fmt.Fprintf(output, "      legacy candidate commit observation: %s (tree unavailable)\n", terminalSafeText(attempt.CandidateOID))
	}
}

func writeMetricObjectIdentity(output io.Writer, label string, identity runnermetrics.ObjectIdentity) {
	fmt.Fprintf(output, "      %s: commit %s · tree %s\n", terminalSafeText(label), metricKnown(identity.CommitOID), metricKnown(identity.TreeOID))
}

func metricKnownNumber(value int) string {
	if value == 0 {
		return "unavailable"
	}
	return fmt.Sprintf("%d", value)
}

func writeMetricStages(output io.Writer, stages []runnermetrics.Stage) {
	for _, stage := range stages {
		state := stage.Outcome
		if !stage.Completed {
			state = "unfinished"
		}
		fmt.Fprintf(output, "      stage: %s · %s · %s", terminalSafeText(stage.Name), terminalSafeText(state), formatMetricDuration(stage.DurationMilliseconds))
		if stage.FailureClass != "" {
			fmt.Fprintf(output, " · %s", terminalSafeText(string(stage.FailureClass)))
		}
		if stage.RetryDisposition != "" {
			fmt.Fprintf(output, " · retry %s", terminalSafeText(string(stage.RetryDisposition)))
		}
		fmt.Fprintln(output)
		for _, context := range stage.PromptContexts {
			fmt.Fprintf(output, "        prompt context fingerprint: %s · pinned guidance %s\n", terminalSafeText(context.Layout), terminalSafeText(context.GuidanceDigest))
		}
		if stage.Usage.Available {
			fmt.Fprintf(output, "        model-reported stage usage: %d input · %d cache read · %d cache write · %d output\n",
				stage.Usage.InputTokens, stage.Usage.CacheReadInputTokens, stage.Usage.CacheWriteInputTokens, stage.Usage.OutputTokens)
		} else if stage.Completed {
			fmt.Fprintln(output, "        model-reported stage usage: unavailable; not zero")
		}
	}
}

func formatMetricDuration(milliseconds int64) string {
	return formatStatusDuration(time.Duration(milliseconds) * time.Millisecond)
}
