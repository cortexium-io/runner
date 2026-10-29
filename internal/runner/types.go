// Package runner owns process semantics. Adapters depend on this package, not the reverse.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrCapacity = errors.New("storage allowance exhausted")
)

type Bytes int64

func (b *Bytes) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*b = Bytes(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	for _, u := range []struct {
		s string
		n int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, u.s) {
			v, err := strconv.ParseInt(strings.TrimSuffix(s, u.s), 10, 64)
			if err != nil || v < 0 || v > (1<<62)/u.n {
				return fmt.Errorf("invalid byte allowance %q", s)
			}
			*b = Bytes(v * u.n)
			return nil
		}
	}
	return fmt.Errorf("byte allowance %q needs B, KiB, MiB or GiB", s)
}

type StoragePolicy struct {
	MaxManaged Bytes `json:"max_managed"`
	MinFree    Bytes `json:"min_free"`
	Database   struct {
		MaxSize   Bytes `json:"max_size"`
		MaxRecord Bytes `json:"max_record"`
		MaxRuns   int   `json:"max_runs"`
	} `json:"database"`
	Logs struct {
		MaxPerAttempt Bytes  `json:"max_per_attempt"`
		KeepFor       string `json:"keep_for"`
	} `json:"logs"`
	MaxArtifact Bytes  `json:"max_artifact"`
	KeepRuns    string `json:"keep_runs"`
}

func DefaultStorage() StoragePolicy {
	var p StoragePolicy
	p.MaxManaged = 10 << 30
	p.MinFree = 5 << 30
	p.Database.MaxSize = 256 << 20
	p.Database.MaxRecord = 16 << 10
	p.Database.MaxRuns = 1000
	p.Logs.MaxPerAttempt = 2 << 20
	p.Logs.KeepFor = "48h"
	p.MaxArtifact = 16 << 20
	p.KeepRuns = "168h"
	return p
}
func (p StoragePolicy) Validate() error {
	if p.Database.MaxRecord < 4096 || p.Database.MaxRecord > 1<<20 || p.Database.MaxSize < 1<<20 || p.Database.MaxRuns < 1 || p.Database.MaxRuns > 10000 {
		return errors.New("database needs a 4KiB–1MiB record limit, at least 1MiB capacity and 1–10000 retained runs")
	}
	if p.MaxManaged <= p.Database.MaxSize+2*p.Database.MaxRecord || p.MinFree < 0 || p.MaxArtifact < 1 || p.Logs.MaxPerAttempt < 1 || p.Logs.MaxPerAttempt > 4<<20 {
		return errors.New("invalid managed-storage, artifact or log allowance")
	}
	for _, s := range []string{p.KeepRuns, p.Logs.KeepFor} {
		d, e := time.ParseDuration(s)
		if e != nil || d <= 0 {
			return fmt.Errorf("invalid retention %q", s)
		}
	}
	return nil
}

type Instance struct {
	Projects          []string       `json:"projects"`
	Concurrency       int            `json:"concurrency"`
	ReconcileInterval string         `json:"reconcile_interval"`
	Storage           StoragePolicy  `json:"storage"`
	Codex             *CodexSettings `json:"codex,omitempty"`
}

// CodexSettings describes one instance-owned native service, not model context.
type CodexSettings struct {
	Binary       string `json:"binary"`
	AuthFile     string `json:"auth_file"`
	StorageLimit Bytes  `json:"storage_limit"`
}
type RoleBinding struct {
	File    string `json:"file"`
	Profile string `json:"profile,omitempty"`
}
type ProjectFile struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Workspace      string                 `json:"workspace"`
	Processes      map[string]string      `json:"processes"`
	DefaultProcess string                 `json:"default_process"`
	Roles          map[string]RoleBinding `json:"roles"`
}
type Project struct {
	ID             string
	Name           string
	Workspace      string
	DefaultProcess string
	Processes      map[string]Process
	Roles          map[string]Role
	Profiles       map[string]string
}
type Snapshot struct {
	Instance Instance
	Projects map[string]Project
	Files    []string
}

type Profile struct {
	Harness      string `json:"harness"`
	Mode         string `json:"mode"`
	Delay        string `json:"delay,omitempty"`
	Input        string `json:"input,omitempty"`
	Output       string `json:"output,omitempty"`
	Text         string `json:"text,omitempty"`
	LogBytes     int64  `json:"log_bytes,omitempty"`
	Model        string `json:"model,omitempty"`
	Reasoning    string `json:"reasoning,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	Access       string `json:"access,omitempty"`
	MaxInput     Bytes  `json:"max_input,omitempty"`
	MaxOutput    Bytes  `json:"max_output,omitempty"`
}
type Role struct {
	ID             string             `json:"id"`
	Purpose        string             `json:"purpose,omitempty"`
	Inputs         map[string]string  `json:"inputs"`
	Outputs        map[string]string  `json:"outputs"`
	Execution      *Profile           `json:"execution,omitempty"`
	DefaultProfile string             `json:"default_profile,omitempty"`
	Profiles       map[string]Profile `json:"profiles,omitempty"`
}

func (r Role) Profile(name string) (Profile, error) {
	if name == "" {
		name = r.DefaultProfile
	}
	if r.Execution != nil && (name == "" || name == "default") {
		return *r.Execution, nil
	}
	p, ok := r.Profiles[name]
	if !ok {
		return p, fmt.Errorf("role %s has no profile %q", r.ID, name)
	}
	return p, nil
}

type Binding struct {
	FromInput string `json:"from_input,omitempty"`
	FromStep  string `json:"from_step,omitempty"`
	Output    string `json:"output,omitempty"`
}
type Escalation struct {
	On        string `json:"on"`
	Profile   string `json:"profile"`
	MaxTimes  int    `json:"max_times"`
	Exhausted string `json:"exhausted"`
}
type Step struct {
	ID         string             `json:"id"`
	Role       string             `json:"role"`
	Inputs     map[string]Binding `json:"inputs"`
	Escalation *Escalation        `json:"escalation,omitempty"`
}
type Process struct {
	ID                 string             `json:"id"`
	Inputs             map[string]string  `json:"inputs"`
	Steps              []Step             `json:"steps"`
	Outputs            map[string]Binding `json:"outputs"`
	MaxAttempts        int                `json:"max_attempts"`
	MaxAttemptsPerStep int                `json:"max_attempts_per_step"`
	AttemptTimeout     string             `json:"attempt_timeout"`
}

type Value struct {
	Inline     json.RawMessage `json:"inline,omitempty"`
	ArtifactID string          `json:"artifact_id,omitempty"`
}

func Text(s string) Value { b, _ := json.Marshal(s); return Value{Inline: b} }
func (v Value) Validate() error {
	if (len(v.Inline) > 0) == (v.ArtifactID != "") {
		return errors.New("a value needs exactly one inline JSON value or artifact ID")
	}
	if len(v.Inline) > 0 && !json.Valid(v.Inline) {
		return errors.New("invalid inline JSON")
	}
	return nil
}
func ValidateValues(values map[string]Value, contract map[string]string) error {
	if len(values) != len(contract) {
		return errors.New("input/output names do not match their contract")
	}
	for k, t := range contract {
		v, ok := values[k]
		if !ok {
			return fmt.Errorf("missing %s", k)
		}
		if e := v.Validate(); e != nil {
			return fmt.Errorf("%s: %w", k, e)
		}
		switch t {
		case "artifact":
			if v.ArtifactID == "" {
				return fmt.Errorf("%s needs an artifact", k)
			}
		case "text":
			var s string
			if v.ArtifactID != "" || json.Unmarshal(v.Inline, &s) != nil {
				return fmt.Errorf("%s needs inline text", k)
			}
		case "json":
			if v.ArtifactID != "" {
				return fmt.Errorf("%s needs inline JSON", k)
			}
		default:
			return fmt.Errorf("unsupported value type %q", t)
		}
	}
	return nil
}

type ExecutionSetup struct {
	Profile Profile `json:"profile"`
}
type ExecutionLimits struct {
	MaxRecord   Bytes `json:"max_record"`
	MaxArtifact Bytes `json:"max_artifact"`
	MaxLog      Bytes `json:"max_log"`
}
type Assignment struct {
	AttemptID   string            `json:"attempt_id"`
	Setup       ExecutionSetup    `json:"setup"`
	Inputs      map[string]Value  `json:"inputs"`
	WorkDir     string            `json:"work_dir"`
	OutputDir   string            `json:"output_dir"`
	Limits      ExecutionLimits   `json:"limits"`
	Task        string            `json:"task,omitempty"`
	OutputTypes map[string]string `json:"output_types,omitempty"`
	AllowHelp   bool              `json:"allow_help,omitempty"`
	Deadline    time.Time         `json:"deadline,omitempty"`
}
type StepResult struct {
	Outcome string           `json:"outcome"`
	Outputs map[string]Value `json:"outputs,omitempty"`
	Reason  string           `json:"reason,omitempty"`
}
type ExecutionState string

const (
	NotStarted ExecutionState = "not_started"
	Running    ExecutionState = "running"
	Finished   ExecutionState = "finished"
	Unknown    ExecutionState = "unknown"
)

type Usage struct {
	InputTokens       *int64 `json:"input_tokens,omitempty"`
	OutputTokens      *int64 `json:"output_tokens,omitempty"`
	CachedInputTokens *int64 `json:"cached_input_tokens,omitempty"`
}
type ExecutionEnd struct {
	Kind   string      `json:"kind"`
	Result *StepResult `json:"result,omitempty"`
	Reason string      `json:"reason,omitempty"`
}
type Observation struct {
	State           ExecutionState  `json:"state"`
	NativeHandle    string          `json:"native_handle,omitempty"`
	End             *ExecutionEnd   `json:"end,omitempty"`
	Usage           Usage           `json:"usage"`
	Reason          string          `json:"reason,omitempty"`
	NextCheck       time.Time       `json:"next_check,omitempty"`
	DroppedLogBytes int64           `json:"dropped_log_bytes,omitempty"`
	NativePeakBytes int64           `json:"native_peak_bytes,omitempty"`
	Effective       *ExecutionFacts `json:"effective,omitempty"`
}
type ExecutionFacts struct {
	Model       string `json:"model"`
	Reasoning   string `json:"reasoning"`
	Access      string `json:"access"`
	HistoryMode string `json:"history_mode"`
}
type Executor interface {
	Start(context.Context, Assignment) (Observation, error)
	Observe(context.Context, string) (Observation, error)
	Stop(context.Context, string) error
	Release(context.Context, string) error
}

type Attempt struct {
	ID              string          `json:"id"`
	Step            int             `json:"step"`
	Profile         string          `json:"profile"`
	State           ExecutionState  `json:"state"`
	Started         time.Time       `json:"started"`
	Deadline        time.Time       `json:"deadline"`
	NextCheck       time.Time       `json:"next_check,omitempty"`
	End             *ExecutionEnd   `json:"end,omitempty"`
	Usage           Usage           `json:"usage"`
	Reason          string          `json:"reason,omitempty"`
	StopRequested   bool            `json:"stop_requested,omitempty"`
	TimedOut        bool            `json:"timed_out,omitempty"`
	Released        bool            `json:"released,omitempty"`
	FinishedAt      time.Time       `json:"finished_at,omitempty"`
	DroppedLogBytes int64           `json:"dropped_log_bytes,omitempty"`
	StopAttempts    int             `json:"stop_attempts,omitempty"`
	StopAllowance   int             `json:"stop_allowance,omitempty"`
	NativeHandle    string          `json:"native_handle,omitempty"`
	NativePeakBytes int64           `json:"native_peak_bytes,omitempty"`
	Effective       *ExecutionFacts `json:"effective,omitempty"`
}
type Run struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	Process         Process           `json:"process"`
	Roles           map[string]Role   `json:"roles"`
	Profiles        map[string]string `json:"profiles"`
	Inputs          map[string]Value  `json:"inputs"`
	Outputs         map[string]Value  `json:"outputs,omitempty"`
	Attempts        []Attempt         `json:"attempts,omitempty"`
	Step            int               `json:"step"`
	NextProfile     string            `json:"next_profile,omitempty"`
	Escalations     int               `json:"escalations"`
	Status          string            `json:"status"`
	Reason          string            `json:"reason,omitempty"`
	CancelRequested bool              `json:"cancel_requested,omitempty"`
	CleanupError    string            `json:"cleanup_error,omitempty"`
	Created         time.Time         `json:"created"`
	Updated         time.Time         `json:"updated"`
	Revision        int64             `json:"revision"`
}

func (r Run) Terminal() bool {
	return r.Status == "Completed" || r.Status == "Failed" || r.Status == "Cancelled"
}
func (r Run) Current() *Attempt {
	if len(r.Attempts) == 0 {
		return nil
	}
	a := &r.Attempts[len(r.Attempts)-1]
	if a.Step != r.Step {
		return nil
	}
	return a
}
func (r Run) Active() bool { a := r.Current(); return a != nil && a.State != Finished }

type Artifact struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	ProjectID string `json:"project_id"`
	Bytes     int64  `json:"bytes"`
}
type Receipt struct {
	Key   string
	Kind  string
	Args  []byte
	RunID string
}
type Store interface {
	Load(context.Context) ([]Run, error)
	Save(context.Context, Run, []Artifact, *Receipt) error
	FindReceipt(context.Context, string) (Receipt, error)
	Artifact(context.Context, string) (Artifact, error)
	HasConsumers(context.Context, string) (bool, error)
	Delete(context.Context, string) error
}

// Clone copies validated domain records. Untrusted JSON must be validated at
// ingress; a serialization failure here is an internal programming error.
func Clone[T any](v T) T {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("copy validated record: %w", err))
	}
	var out T
	if err = json.Unmarshal(b, &out); err != nil {
		panic(fmt.Errorf("copy validated record: %w", err))
	}
	return out
}
func Clip(s string) string {
	if len(s) > 512 {
		return s[:512] + "…"
	}
	return s
}
