package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

type guidanceOutput struct {
	RunnerID           string                  `json:"runner_id"`
	HistoryPath        string                  `json:"history_path"`
	MinimumOccurrences int                     `json:"minimum_occurrences"`
	Drafts             []metrics.GuidanceDraft `json:"drafts"`
	MalformedRecords   int                     `json:"malformed_records,omitempty"`
}

func runGuidance(args []string, stdout io.Writer) error {
	flags := newFlagSet("guidance", "cortexium-runner guidance [--config PATH] [--min-occurrences N] [--json]", stdout)
	configPath := flags.String("config", "", "runner config path; defaults to .cortexium/runner.json")
	minimum := flags.Int("min-occurrences", 0, "distinct cards required; defaults to guidance_min_occurrences (2 when omitted)")
	jsonOutput := flags.Bool("json", false, "write private, unapproved drafts as JSON")
	proceed, err := parseFlags(flags, args, "guidance")
	if err != nil || !proceed {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("guidance does not accept positional arguments")
	}
	if *minimum != 0 && *minimum < 2 {
		return errors.New("min-occurrences must be at least 2")
	}
	cfg, err := config.LoadConfig(resolveRunnerConfigPath(*configPath, ""))
	if err != nil {
		return fmt.Errorf("load guidance config: %w", err)
	}
	if *minimum == 0 {
		*minimum = cfg.EffectiveGuidanceMinOccurrences()
	}
	store, err := metrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		return err
	}
	history, err := store.Read()
	if err != nil {
		return err
	}
	detector := metrics.NewGuidanceDetector(*minimum)
	replayGuidance(detector, cfg, history)
	view := guidanceOutput{RunnerID: cfg.RunnerID, HistoryPath: store.Path(), MinimumOccurrences: *minimum,
		Drafts: detector.Drafts(), MalformedRecords: history.MalformedRecords}
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(view)
	}
	fmt.Fprintf(stdout, "Private guidance drafts: %d (threshold: %d distinct cards)\n", len(view.Drafts), *minimum)
	fmt.Fprintln(stdout, "Untrusted observations for human review; nothing is active or injected into prompts.")
	for _, draft := range view.Drafts {
		fmt.Fprintf(stdout, "\n%s · %s · investigate in %s\n", draft.ID, terminalSafeText(draft.Repository), draft.Destination)
		fmt.Fprintf(stdout, "  Pattern (evidence only): %s\n  %s\n  Review: %s\n",
			terminalSafeText(draft.Pattern), draft.Observation, draft.ReviewInstruction)
		for _, incident := range draft.Incidents {
			fmt.Fprintf(stdout, "  - card %s · attempt %s · candidate %s\n", terminalSafeText(incident.ItemID),
				terminalSafeText(incident.AttemptID), terminalSafeText(incident.CandidateOID))
		}
	}
	if view.MalformedRecords > 0 {
		fmt.Fprintf(stdout, "History warning: ignored %d malformed record(s)\n", view.MalformedRecords)
	}
	fmt.Fprintf(stdout, "\nEvidence: cortexium-runner metrics --config PATH --item CARD_ID --json\nHistory: %s\n", terminalSafeText(view.HistoryPath))
	return nil
}

func guidanceEventInScope(event metrics.Event, cfg config.Config) bool {
	return event.RunnerID == cfg.RunnerID && cfg.GitHubProject != nil &&
		strings.EqualFold(event.ProjectOwner, cfg.GitHubProject.Owner) && event.ProjectNumber == cfg.GitHubProject.Number
}

func replayGuidance(detector *metrics.GuidanceDetector, cfg config.Config, history metrics.ReadResult) {
	for _, attempt := range history.Attempts {
		if attempt.Completed && guidanceEventInScope(attempt.Event, cfg) {
			detector.Observe(attempt.Event)
		}
	}
}

// The service uses this observer; other CLI commands keep clean JSON output.
// Projection/read/notification errors never become admission-budget errors.
// Only a genuine metrics append failure is returned to the engine.
func guidanceMetricsObserver(store *metrics.Store, cfg config.Config, output io.Writer) func(metrics.Event) error {
	if output == nil {
		output = io.Discard
	}
	var mu sync.Mutex
	detector := metrics.NewGuidanceDetector(cfg.EffectiveGuidanceMinOccurrences())
	history, err := store.Read()
	if err != nil {
		fmt.Fprintln(output, "guidance warning: prior history could not be read; draft notifications cover new attempts only")
	} else {
		replayGuidance(detector, cfg, history)
	}
	return func(event metrics.Event) error {
		mu.Lock()
		defer mu.Unlock()
		if err := store.Append(event); err != nil {
			return err
		}
		if guidanceEventInScope(event, cfg) {
			for _, id := range detector.Observe(event) {
				fmt.Fprintf(output, "guidance draft available: %s (private, unapproved; inspect with cortexium-runner guidance)\n", id)
			}
		}
		return nil
	}
}
