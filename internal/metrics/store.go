package metrics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/cortexium-io/runner/internal/securefs"
)

const (
	maxEventBytes   = 2 * 1024 * 1024
	maxHistoryBytes = 64 * 1024 * 1024
)

type Store struct {
	path string
	mu   sync.Mutex
}

type ReadResult struct {
	Attempts         []Attempt `json:"attempts"`
	MalformedRecords int       `json:"malformed_records,omitempty"`
}

func DefaultPath(runnerID string) (string, error) {
	directory := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_STATE_DIR"))
	if directory == "" {
		var err error
		directory, err = os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("locate user config directory: %w", err)
		}
		directory = filepath.Join(directory, "cortexium-runner")
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(runnerID)))
	return filepath.Join(directory, "metrics", hex.EncodeToString(digest[:12])+".jsonl"), nil
}

func NewDefaultStore(runnerID string) (*Store, error) {
	path, err := DefaultPath(runnerID)
	if err != nil {
		return nil, err
	}
	return NewStore(path), nil
}

func NewStore(path string) *Store {
	return &Store{path: filepath.Clean(path)}
}

func (s *Store) Path() string { return s.path }

func (s *Store) Append(event Event) error {
	if strings.TrimSpace(event.AttemptID) == "" {
		return fmt.Errorf("metrics attempt id is required")
	}
	if !validEventKind(event.Kind) {
		return fmt.Errorf("unsupported metrics event kind %q", event.Kind)
	}
	if event.Kind == EventStageStarted || event.Kind == EventStageCompleted {
		if strings.TrimSpace(event.StageID) == "" || !validStageName(event.Stage) {
			return fmt.Errorf("stage metrics event requires a stage_id and fixed stage name")
		}
		if event.Kind == EventStageCompleted && !validStageOutcome(event.Outcome) {
			return fmt.Errorf("stage completion requires a fixed outcome")
		}
	}
	if !validFailureClass(event.FailureClass) || !validFailureOperation(event.FailureOperation) || !validRetryDisposition(event.RetryDisposition) {
		return fmt.Errorf("metrics recovery fields must use fixed enum values")
	}
	if !validReviewVerdict(event) {
		return fmt.Errorf("metrics review verdict requires a completed attempt and a fixed verdict value")
	}
	if event.DurationMilliseconds < 0 || event.HarnessDurationMilliseconds < 0 || event.PublicationAttempts < 0 || event.PublicationAttempts > 3 {
		return fmt.Errorf("metrics durations or publication attempt count are invalid")
	}
	if err := ValidateUsage(event.Usage); err != nil {
		return fmt.Errorf("invalid metrics usage: %w", err)
	}
	if !validPromptContexts(event.PromptContexts) {
		return fmt.Errorf("metrics prompt context requires a layout ID and SHA-256 guidance digest")
	}
	if !validRetainedAttemptEvidence(event) {
		return fmt.Errorf("metrics attempt evidence is invalid or exceeds a fixed limit")
	}
	event.Version = EventVersion
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode metrics event: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxEventBytes {
		return fmt.Errorf("metrics event exceeds %d bytes", maxEventBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := securefs.EnsurePrivateDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("create private metrics directory: %w", err)
	}
	directory, err := securefs.OpenDir(filepath.Dir(s.path))
	if err != nil {
		return fmt.Errorf("open private metrics directory: %w", err)
	}
	defer directory.Close()
	if err := directory.AppendFile(filepath.Base(s.path), encoded, 0o600, maxHistoryBytes); err != nil {
		return fmt.Errorf("append private metrics history: %w", err)
	}
	return nil
}

func (s *Store) Read() (ReadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	encoded, mode, state, err := securefs.ReadFile(s.path, maxHistoryBytes)
	if errors.Is(err, os.ErrNotExist) || err == nil && !state.Exists {
		return ReadResult{}, nil
	}
	if err != nil {
		return ReadResult{}, fmt.Errorf("read private metrics history: %w", err)
	}
	if mode.Perm() != 0o600 {
		return ReadResult{}, fmt.Errorf("private metrics history mode is %04o, want 0600", mode.Perm())
	}
	if err := securefs.ValidateOwnedRegularFile(state, uint32(os.Geteuid())); err != nil {
		return ReadResult{}, fmt.Errorf("validate private metrics history: %w", err)
	}

	byID := map[string]Attempt{}
	order := []string{}
	seen := map[string]bool{}
	stagesByAttempt := map[string]map[string]Stage{}
	stageOrderByAttempt := map[string][]string{}
	result := ReadResult{}
	for _, line := range bytes.Split(encoded, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		if len(line) > maxEventBytes {
			result.MalformedRecords++
			continue
		}
		var event Event
		if err := json.Unmarshal(line, &event); err != nil || event.Version != EventVersion || strings.TrimSpace(event.AttemptID) == "" || !validEventKind(event.Kind) || !validFailureClass(event.FailureClass) || !validFailureOperation(event.FailureOperation) || !validRetryDisposition(event.RetryDisposition) || !validReviewVerdict(event) || event.DurationMilliseconds < 0 || event.HarnessDurationMilliseconds < 0 || event.PublicationAttempts < 0 || event.PublicationAttempts > 3 || ValidateUsage(event.Usage) != nil || !validPromptContexts(event.PromptContexts) || !validRetainedAttemptEvidence(event) {
			result.MalformedRecords++
			continue
		}
		if !seen[event.AttemptID] {
			order = append(order, event.AttemptID)
			seen[event.AttemptID] = true
		}
		if event.Kind == EventStageStarted || event.Kind == EventStageCompleted {
			if strings.TrimSpace(event.StageID) == "" || !validStageName(event.Stage) || (event.Kind == EventStageCompleted && !validStageOutcome(event.Outcome)) {
				result.MalformedRecords++
				continue
			}
			if stagesByAttempt[event.AttemptID] == nil {
				stagesByAttempt[event.AttemptID] = map[string]Stage{}
			}
			previous, exists := stagesByAttempt[event.AttemptID][event.StageID]
			if exists && (previous.Name != event.Stage || event.Kind == EventStageStarted || previous.Completed) {
				result.MalformedRecords++
				continue
			}
			if !exists {
				stageOrderByAttempt[event.AttemptID] = append(stageOrderByAttempt[event.AttemptID], event.StageID)
			}
			stage := previous
			stage.StageID = event.StageID
			stage.Name = event.Stage
			stage.PromptContexts = event.PromptContexts
			if event.Kind == EventStageStarted {
				stage.StartedAt = event.StartedAt
				stage.Completed = false
			} else {
				if stage.StartedAt.IsZero() {
					stage.StartedAt = event.StartedAt
				}
				stage.FinishedAt = event.FinishedAt
				stage.DurationMilliseconds = event.DurationMilliseconds
				stage.Outcome = event.Outcome
				stage.FailureClass = event.FailureClass
				stage.RetryDisposition = event.RetryDisposition
				stage.Usage = event.Usage
				stage.Completed = true
			}
			stagesByAttempt[event.AttemptID][event.StageID] = stage
			continue
		}
		current := byID[event.AttemptID]
		if event.Kind == EventStarted {
			if current.AttemptID != "" {
				result.MalformedRecords++
				continue
			}
			current.Event = event
			current.Completed = false
		} else if event.Kind == EventCompleted {
			if current.Completed {
				result.MalformedRecords++
				continue
			}
			if current.AttemptID != "" && event.StartedAt.IsZero() {
				event.StartedAt = current.StartedAt
			}
			current.Event = event
			current.Completed = true
		}
		byID[event.AttemptID] = current
	}
	for _, id := range order {
		attempt := byID[id]
		if attempt.AttemptID != "" {
			for _, stageID := range stageOrderByAttempt[id] {
				attempt.Stages = append(attempt.Stages, stagesByAttempt[id][stageID])
			}
			SortStages(attempt.Stages)
			result.Attempts = append(result.Attempts, attempt)
		}
	}
	SortNewest(result.Attempts)
	return result, nil
}

func validEventKind(kind string) bool {
	switch kind {
	case EventStarted, EventCompleted, EventStageStarted, EventStageCompleted:
		return true
	default:
		return false
	}
}
