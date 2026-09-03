package metrics

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const maxEventBytes = 2 * 1024 * 1024

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
	if event.DurationMilliseconds < 0 || event.HarnessDurationMilliseconds < 0 || event.PublicationAttempts < 0 || event.PublicationAttempts > 3 {
		return fmt.Errorf("metrics durations or publication attempt count are invalid")
	}
	if err := ValidateUsage(event.Usage); err != nil {
		return fmt.Errorf("invalid metrics usage: %w", err)
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
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create metrics directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open metrics history: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(encoded); err != nil {
		return fmt.Errorf("append metrics history: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync metrics history: %w", err)
	}
	return nil
}

func (s *Store) Read() (ReadResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return ReadResult{}, nil
	}
	if err != nil {
		return ReadResult{}, fmt.Errorf("open metrics history: %w", err)
	}
	defer file.Close()

	byID := map[string]Attempt{}
	order := []string{}
	seen := map[string]bool{}
	stagesByAttempt := map[string]map[string]Stage{}
	stageOrderByAttempt := map[string][]string{}
	result := ReadResult{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxEventBytes)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil || event.Version != EventVersion || strings.TrimSpace(event.AttemptID) == "" || !validEventKind(event.Kind) || !validFailureClass(event.FailureClass) || !validFailureOperation(event.FailureOperation) || !validRetryDisposition(event.RetryDisposition) || event.DurationMilliseconds < 0 || event.HarnessDurationMilliseconds < 0 || event.PublicationAttempts < 0 || event.PublicationAttempts > 3 || ValidateUsage(event.Usage) != nil {
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
			if _, exists := stagesByAttempt[event.AttemptID][event.StageID]; !exists {
				stageOrderByAttempt[event.AttemptID] = append(stageOrderByAttempt[event.AttemptID], event.StageID)
			}
			stage := stagesByAttempt[event.AttemptID][event.StageID]
			stage.StageID = event.StageID
			stage.Name = event.Stage
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
			current.Event = event
			current.Completed = false
		} else if event.Kind == EventCompleted {
			if current.AttemptID != "" && event.StartedAt.IsZero() {
				event.StartedAt = current.StartedAt
			}
			current.Event = event
			current.Completed = true
		}
		byID[event.AttemptID] = current
	}
	if err := scanner.Err(); err != nil {
		return ReadResult{}, fmt.Errorf("read metrics history: %w", err)
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
