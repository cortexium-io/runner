package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestGuidanceCLIProjectsPrivateHistoryWithoutChangingIt(t *testing.T) {
	t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
	cfg := completeCLITestConfig(t.TempDir())
	configPath := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	store, err := metrics.NewDefaultStore(cfg.RunnerID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{"one", "two"} {
		if err := store.Append(guidanceCLIEvent(cfg, item)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := execute(t.Context(), []string{"guidance", "--config", configPath, "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("CLI exit %d: %s", code, stderr.String())
	}
	var view guidanceOutput
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil {
		t.Fatalf("unclean JSON: %s: %v", stdout.String(), err)
	}
	if len(view.Drafts) != 1 || view.MinimumOccurrences != 2 || view.Drafts[0].Status != "draft" {
		t.Fatalf("unexpected draft view: %#v", view)
	}
	stdout.Reset()
	if err := runGuidance([]string{"--config", configPath}, &stdout); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"nothing is active", "attempt-one", "attempt-two", "Evidence:", "History:"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("human view omitted %q: %s", expected, stdout.String())
		}
	}
	stdout.Reset()
	if err := runGuidance([]string{"--config", configPath, "--min-occurrences", "3", "--json"}, &stdout); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stdout.Bytes(), &view); err != nil || len(view.Drafts) != 0 {
		t.Fatalf("threshold override failed: %s (%v)", stdout.String(), err)
	}
	if err := runGuidance([]string{"--config", configPath, "--min-occurrences", "1"}, io.Discard); err == nil {
		t.Fatal("one card cannot establish repetition")
	}
	after, err := os.ReadFile(store.Path())
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("inspection changed history: %v", err)
	}
	if info, err := os.Stat(store.Path()); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("history is not private: %v %v", info, err)
	}
}

func TestGuidanceObserverNotifiesOnceAndRebuildsOnRestart(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	store := metrics.NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
	var notifications bytes.Buffer
	observe := guidanceMetricsObserver(store, cfg, &notifications)
	for _, item := range []string{"one", "one", "two", "two"} {
		if err := observe(guidanceCLIEvent(cfg, item)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Count(notifications.String(), "guidance draft available:") != 1 {
		t.Fatalf("incorrect notifications: %s", notifications.String())
	}
	notifications.Reset()
	observe = guidanceMetricsObserver(store, cfg, &notifications)
	if err := observe(guidanceCLIEvent(cfg, "three")); err != nil {
		t.Fatal(err)
	}
	if notifications.Len() != 0 {
		t.Fatalf("restart re-notified an existing draft: %s", notifications.String())
	}
	other := guidanceCLIEvent(cfg, "foreign")
	other.ProjectNumber++
	detector := metrics.NewGuidanceDetector(2)
	history := metrics.ReadResult{Attempts: []metrics.Attempt{
		{Event: guidanceCLIEvent(cfg, "one"), Completed: true}, {Event: other, Completed: true},
	}}
	replayGuidance(detector, cfg, history)
	if len(detector.Drafts()) != 0 {
		t.Fatal("foreign project history included")
	}
}

type unavailableGuidanceOutput struct{}

func (unavailableGuidanceOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGuidanceNotificationFailureDoesNotFailMetrics(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	store := metrics.NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
	observe := guidanceMetricsObserver(store, cfg, unavailableGuidanceOutput{})
	for _, item := range []string{"one", "two"} {
		if err := observe(guidanceCLIEvent(cfg, item)); err != nil {
			t.Fatalf("notification error escaped as an admission error: %v", err)
		}
	}
	history, err := store.Read()
	if err != nil || len(history.Attempts) != 2 {
		t.Fatalf("lost metrics: %#v %v", history, err)
	}
	blocked := metrics.NewStore(t.TempDir()) // A directory cannot be appended as history.
	if err := guidanceMetricsObserver(blocked, cfg, io.Discard)(guidanceCLIEvent(cfg, "three")); err == nil || errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("real metrics failure was swallowed: %v", err)
	}
}

func TestGuidanceConfigurationIsOptionalValidatedAndPreserved(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	if cfg.EffectiveGuidanceMinOccurrences() != 2 {
		t.Fatal("default threshold must be two independent cards")
	}
	for _, invalid := range []int{-1, 1} {
		cfg.GuidanceMinOccurrences = invalid
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted threshold %d", invalid)
		}
	}
	cfg.GuidanceMinOccurrences = 3
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.EffectiveGuidanceMinOccurrences() != 3 {
		t.Fatalf("threshold not preserved: %v", err)
	}
}

func guidanceCLIEvent(cfg config.Config, item string) metrics.Event {
	return metrics.Event{Kind: metrics.EventCompleted, AttemptID: "attempt-" + item, RunnerID: cfg.RunnerID,
		ProjectOwner: cfg.GitHubProject.Owner, ProjectNumber: cfg.GitHubProject.Number, Repository: cfg.GitHubProject.IntakeRepository,
		ItemID: item, ItemTitle: "Local card", Role: "reviewer", Harness: "codex", Outcome: "blocked",
		FailureClass: "review_incomplete", StartedAt: time.Unix(100, 0).UTC()}
}

func TestGuidanceRecoveredStageLiveNotificationsMatchHistoryReplay(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	store := metrics.NewStore(filepath.Join(t.TempDir(), "history.jsonl"))
	var notifications bytes.Buffer
	observe := guidanceMetricsObserver(store, cfg, &notifications)
	live := metrics.NewGuidanceDetector(2)
	for _, item := range []string{"one", "one", "two"} {
		event := guidanceCLIEvent(cfg, item)
		event.Kind = metrics.EventStageCompleted
		event.StageID = "prepare-" + item
		event.Stage = metrics.StageWorkspacePrepare
		event.Outcome = metrics.StageOutcomeFailed
		event.FailureClass = "capability_unavailable"
		event.StartedAt = event.StartedAt.Add(time.Second)
		event.CandidateOID = strings.Repeat("a", 40)
		if err := observe(event); err != nil {
			t.Fatal(err)
		}
		live.Observe(event)
		event.Kind = metrics.EventCompleted
		event.Stage, event.StageID = "", ""
		event.Outcome, event.FailureClass = "succeeded", ""
		event.CandidateOID = strings.Repeat("b", 40)
		if err := observe(event); err != nil {
			t.Fatal(err)
		}
		live.Observe(event)
	}
	if strings.Count(notifications.String(), "guidance draft available:") != 1 {
		t.Fatalf("recovered stage notification was lost or duplicated: %s", notifications.String())
	}
	history, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	replayed := metrics.NewGuidanceDetector(2)
	replayGuidance(replayed, cfg, history)
	if !reflect.DeepEqual(live.Drafts(), replayed.Drafts()) {
		t.Fatalf("live/replay incident evidence differs:\nlive: %#v\nreplay: %#v", live.Drafts(), replayed.Drafts())
	}
	notifications.Reset()
	observe = guidanceMetricsObserver(store, cfg, &notifications)
	if err := observe(history.Attempts[0].Event); err != nil || notifications.Len() != 0 {
		t.Fatalf("restart notified an existing incident: %v, %s", err, notifications.String())
	}
}
