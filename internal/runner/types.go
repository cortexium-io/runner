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
	Projects          []string         `json:"projects"`
	Concurrency       int              `json:"concurrency"`
	TaskConcurrency   int              `json:"task_concurrency,omitempty"`
	ReconcileInterval string           `json:"reconcile_interval"`
	Storage           StoragePolicy    `json:"storage"`
	Codex             *CodexSettings   `json:"codex,omitempty"`
	Release           *ReleaseSettings `json:"release,omitempty"`
}

// ReleaseSettings selects a trusted, model-free operator. Product code and task
// data cannot supply commands or credentials to this capability.
type ReleaseSettings struct {
	Command []string `json:"command"`
	GitHub  string   `json:"github,omitempty"`
}

// CodexSettings selects the instance's native service and command runtime,
// not model context.
type CodexSettings struct {
	Binary           string   `json:"binary"`
	AuthFile         string   `json:"auth_file"`
	StorageLimit     Bytes    `json:"storage_limit"`
	GoBinary         string   `json:"go_binary,omitempty"`
	GoModuleCache    string   `json:"go_module_cache,omitempty"`
	GoCheckBinary    string   `json:"go_check_binary,omitempty"`
	ReadOnlyPaths    []string `json:"read_only_paths,omitempty"`
	CommandReadPaths []string `json:"command_read_paths,omitempty"`
}
type SourceSettings struct {
	Ref string `json:"ref"`
}
type RoleBinding struct {
	File    string `json:"file"`
	Profile string `json:"profile,omitempty"`
}
type ProjectFile struct {
	ID             string                 `json:"id"`
	Name           string                 `json:"name"`
	Workspace      string                 `json:"workspace"`
	Source         *SourceSettings        `json:"source,omitempty"`
	Processes      map[string]string      `json:"processes"`
	DefaultProcess string                 `json:"default_process"`
	Roles          map[string]RoleBinding `json:"roles"`
}
type Project struct {
	ID             string
	Name           string
	Workspace      string
	Source         *SourceSettings
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
	Command      *Command `json:"command,omitempty"`
	Harness      string   `json:"harness"`
	Mode         string   `json:"mode"`
	Delay        string   `json:"delay,omitempty"`
	Input        string   `json:"input,omitempty"`
	Output       string   `json:"output,omitempty"`
	Text         string   `json:"text,omitempty"`
	LogBytes     int64    `json:"log_bytes,omitempty"`
	Model        string   `json:"model,omitempty"`
	Reasoning    string   `json:"reasoning,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
	Access       string   `json:"access,omitempty"`
	MaxInput     Bytes    `json:"max_input,omitempty"`
	MaxOutput    Bytes    `json:"max_output,omitempty"`
}

// Command is a configured argv, never a model-selected shell string. Runtime
// paths belong to the instance; only source/scratch substitutions are supported.
type Command struct {
	Args      []string          `json:"args"`
	Directory string            `json:"directory,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Artifact  string            `json:"artifact,omitempty"`
	Network   bool              `json:"network,omitempty"`
	// Result is a scratch JSON file containing only outcome/reason. Expected
	// nonzero exits can instead be mapped to domain outcomes explicitly.
	Result       string         `json:"result,omitempty"`
	ExitOutcomes map[int]string `json:"exit_outcomes,omitempty"`
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
	Default   *Value `json:"default,omitempty"`
	// Fallback selects an earlier configured step output when FromStep has no
	// accepted result yet. It is one level, with no literal artifact grants.
	Fallback *Binding `json:"fallback,omitempty"`
	FromItem string   `json:"from_item,omitempty"`
	Literal  *Value   `json:"literal,omitempty"`
	// Previous explicitly carries feedback across a correction boundary.
	// Ordinary proof bindings exclude superseded results.
	Previous      bool `json:"previous,omitempty"`
	FromCandidate bool `json:"from_candidate,omitempty"`
}

// Route is declared policy, never a next-step instruction supplied by a worker.
type Route struct {
	Next     string `json:"next"`
	MaxTimes int    `json:"max_times,omitempty"`
	Approval bool   `json:"approval,omitempty"`
}
type Escalation struct {
	On        string `json:"on"`
	Profile   string `json:"profile"`
	MaxTimes  int    `json:"max_times"`
	Exhausted string `json:"exhausted"`
}
type Step struct {
	AttemptTimeout string             `json:"attempt_timeout,omitempty"`
	ID             string             `json:"id"`
	Role           string             `json:"role"`
	Inputs         map[string]Binding `json:"inputs"`
	Escalation     *Escalation        `json:"escalation,omitempty"`
	Routes         map[string]Route   `json:"routes,omitempty"`
	ForEach        *TaskGroup         `json:"for_each,omitempty"`
	Reuse          bool               `json:"reuse,omitempty"`
}

// Task data selects work and dependencies, never new roles or permissions.
// Items are executed in stable dependency order in the run's owned workspace.
type TaskGroup struct {
	Mode        string             `json:"mode,omitempty"`
	MaxParallel int                `json:"max_parallel,omitempty"`
	ScopeInput  string             `json:"scope_input,omitempty"`
	Evidence    map[string]Binding `json:"evidence,omitempty"`
	Items       Binding            `json:"items"`
	Inputs      map[string]string  `json:"inputs"`
	Steps       []Step             `json:"steps"`
	Output      Binding            `json:"output"`
}

type TaskItem struct {
	ID        string                     `json:"id"`
	DependsOn []string                   `json:"depends_on,omitempty"`
	Inputs    map[string]json.RawMessage `json:"inputs"`
}
type Process struct {
	ID                    string             `json:"id"`
	Inputs                map[string]string  `json:"inputs"`
	Steps                 []Step             `json:"steps"`
	Outputs               map[string]Binding `json:"outputs"`
	MaxAttempts           int                `json:"max_attempts"`
	MaxAttemptsPerStep    int                `json:"max_attempts_per_step"`
	InfrastructureRetries int                `json:"infrastructure_retries,omitempty"`
	AttemptTimeout        string             `json:"attempt_timeout"`
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
type FrontendCheckPins struct {
	HelperSHA256     string `json:"helper_sha256"`
	PatchSHA256      string `json:"patch_sha256"`
	DiagnoseTimeouts bool   `json:"diagnose_timeouts,omitempty"`
}
type Assignment struct {
	FrontendCheckPins *FrontendCheckPins `json:"frontend_check_pins,omitempty"`
	TaskSource        *TaskAssignment    `json:"task_source,omitempty"`
	ProjectID         string             `json:"project_id,omitempty"`
	RunID             string             `json:"run_id,omitempty"`
	Repository        string             `json:"repository,omitempty"`
	Source            *SourceSettings    `json:"source,omitempty"`
	AttemptID         string             `json:"attempt_id"`
	Setup             ExecutionSetup     `json:"setup"`
	Inputs            map[string]Value   `json:"inputs"`
	WorkDir           string             `json:"work_dir"`
	OutputDir         string             `json:"output_dir"`
	Limits            ExecutionLimits    `json:"limits"`
	Task              string             `json:"task,omitempty"`
	OutputTypes       map[string]string  `json:"output_types,omitempty"`
	AllowHelp         bool               `json:"allow_help,omitempty"`
	Outcomes          []string           `json:"outcomes,omitempty"`
	Deadline          time.Time          `json:"deadline,omitempty"`
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
	// Retryable is trusted adapter evidence of a settled, safe infrastructure
	// failure. Task output and candidate code cannot grant this classification.
	Retryable bool `json:"retryable,omitempty"`
}
type Observation struct {
	Source       *SourceResult  `json:"source,omitempty"`
	State        ExecutionState `json:"state"`
	NativeHandle string         `json:"native_handle,omitempty"`
	End          *ExecutionEnd  `json:"end,omitempty"`
	Usage        Usage          `json:"usage"`
	Reason       string         `json:"reason,omitempty"`
	NextCheck    time.Time      `json:"next_check,omitempty"`
	// Reconciling identifies a bounded rejoin of the same saved operation, not
	// permission to repeat effects whose settlement is unknown.
	Reconciling     bool            `json:"reconciling,omitempty"`
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

// RunResources owns source workspaces independently of attempt sessions.
type RunResources interface {
	ReleaseRun(context.Context, Run) error
	ForgetRun(context.Context, string) error
}

// SourceRecoveryValidator checks retained source ownership after execution and
// cleanup have settled. It grants no execution or cleanup authority.
type SourceRecoveryValidator interface {
	ValidateSourceRecovery(context.Context, Run, SourceResult) error
}

// CleanupBlockedError preserves resources that cannot be proved disposable.
// The executor classifies only integrity/ownership failures this way; ordinary
// I/O errors retain the existing bounded-delay cleanup retry.
type CleanupBlockedError struct{ Reason string }

func (e *CleanupBlockedError) Error() string { return e.Reason }

type SourceResult struct {
	Tree         string `json:"tree,omitempty"`
	InputPatchID string `json:"input_patch_id,omitempty"`
	Repository   string `json:"repository,omitempty"`
	Base         string `json:"base,omitempty"`
	WorkDir      string `json:"work_dir,omitempty"`
	PatchID      string `json:"patch_id,omitempty"`
	AttemptID    string `json:"attempt_id"`
}
type Attempt struct {
	Source          *SourceResult   `json:"source,omitempty"`
	ID              string          `json:"id"`
	Step            int             `json:"step"`
	Visit           int             `json:"visit,omitempty"`
	Superseded      bool            `json:"superseded,omitempty"`
	EvidenceKey     string          `json:"evidence_key,omitempty"`
	ReusedFrom      string          `json:"reused_from,omitempty"`
	Profile         string          `json:"profile,omitempty"`
	State           ExecutionState  `json:"state"`
	Started         time.Time       `json:"started"`
	Deadline        time.Time       `json:"deadline"`
	NextCheck       time.Time       `json:"next_check,omitzero"`
	Reconciling     bool            `json:"reconciling,omitempty"`
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
	RecordFormat            int                        `json:"record_format,omitempty"`
	Task                    *TaskRunLink               `json:"task,omitempty"`
	TaskGroups              map[string]*TaskGroupState `json:"task_groups,omitempty"`
	TaskUsage               []Usage                    `json:"-"`
	TaskStatuses            []TaskStatus               `json:"-"`
	Repository              string                     `json:"repository,omitempty"`
	Source                  *SourceSettings            `json:"source,omitempty"`
	ResourcesReleased       bool                       `json:"resources_released,omitempty"`
	ID                      string                     `json:"id"`
	ProjectID               string                     `json:"project_id"`
	Process                 Process                    `json:"process"`
	Roles                   map[string]Role            `json:"roles"`
	Profiles                map[string]string          `json:"profiles"`
	Inputs                  map[string]Value           `json:"inputs"`
	Outputs                 map[string]Value           `json:"outputs,omitempty"`
	Attempts                []Attempt                  `json:"attempts,omitempty"`
	Step                    int                        `json:"step"`
	Visit                   int                        `json:"visit,omitempty"`
	VisitExtraAttempts      int                        `json:"visit_extra_attempts,omitempty"`
	StepInputs              map[string]Value           `json:"step_inputs,omitempty"`
	RouteUses               map[string]int             `json:"route_uses,omitempty"`
	CorrectionRecovery      *CorrectionRecovery        `json:"correction_recovery,omitempty"`
	CollectionRecovery      *CollectionRecovery        `json:"collection_recovery,omitempty"`
	FrontendCheckRecovery   *FrontendCheckRecovery     `json:"frontend_check_recovery,omitempty"`
	FrontendTimeoutRecovery *FrontendTimeoutRecovery   `json:"frontend_timeout_recovery,omitempty"`
	Decision                *Decision                  `json:"decision,omitempty"`
	NextProfile             string                     `json:"next_profile,omitempty"`
	Escalations             int                        `json:"escalations"`
	Status                  string                     `json:"status"`
	Reason                  string                     `json:"reason,omitempty"`
	CancelRequested         bool                       `json:"cancel_requested,omitempty"`
	CleanupError            string                     `json:"cleanup_error,omitempty"`
	CleanupBlocked          bool                       `json:"cleanup_blocked,omitempty"`
	Created                 time.Time                  `json:"created"`
	Updated                 time.Time                  `json:"updated"`
	Revision                int64                      `json:"revision"`
}

type Decision struct {
	ID        string `json:"id"`
	AttemptID string `json:"attempt_id"`
	Step      string `json:"step"`
	Outcome   string `json:"outcome"`
	Next      string `json:"next"`
	Reason    string `json:"reason,omitempty"`
}

func (r Run) Terminal() bool {
	return r.Status == "Completed" || r.Status == "Failed" || r.Status == "Cancelled"
}
func (r Run) Current() *Attempt {
	if len(r.Attempts) == 0 {
		return nil
	}
	a := &r.Attempts[len(r.Attempts)-1]
	if a.Step != r.Step || a.Visit != r.Visit {
		return nil
	}
	return a
}
func (r Run) Active() bool {
	if activeTaskGroup(r) != nil {
		return true
	}
	a := r.Current()
	return a != nil && a.State != Finished
}

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
