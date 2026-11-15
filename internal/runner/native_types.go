package runner

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

// CapabilitySelection describes capabilities exposed by the harness, not an
// instruction asking an agent to ignore tools it can still call.
type CapabilitySelection struct {
	Mode  string   `json:"mode"`
	Names []string `json:"names,omitempty"`
}

// NativeProfile explicitly opts into the user's installed harness configuration.
// MCP permissions belong to that harness and are not a filesystem sandbox.
type NativeProfile struct {
	Harness           string              `json:"harness"`
	Binary            string              `json:"binary,omitempty"`
	Context           string              `json:"context,omitempty"`
	Tools             CapabilitySelection `json:"tools"`
	Skills            CapabilitySelection `json:"skills"`
	Network           bool                `json:"network,omitempty"`
	RuntimeWritePaths []string            `json:"runtime_write_paths,omitempty"`
}

func (p NativeProfile) Validate() error {
	if p.Context != "" && p.Context != "native" && p.Context != "lean" {
		return errors.New("native context must be native or lean")
	}
	if p.Context == "lean" && (p.Harness != "codex" || p.Skills.Mode != "none") {
		return errors.New("lean context requires Codex with skills:none")
	}
	if err := ValidateRuntimeWritePaths(p.RuntimeWritePaths); err != nil {
		return err
	}
	if p.Binary != "" && !filepath.IsAbs(p.Binary) {
		return errors.New("native binary must be an absolute configured path")
	}
	for _, selection := range []CapabilitySelection{p.Tools, p.Skills} {
		if selection.Mode != "none" && selection.Mode != "selected" && selection.Mode != "native" {
			return errors.New("native capabilities require an explicit none, selected or native mode")
		}
		if (selection.Mode == "selected") != (len(selection.Names) > 0) {
			return errors.New("only selected capabilities take a nonempty names list")
		}
	}
	if p.Skills.Mode == "selected" {
		return errors.New("selected native skills are unsupported: this harness cannot enforce a skill allowlist")
	}
	switch p.Harness {
	case "codex":
		if p.Tools.Mode != "native" {
			return errors.New("Codex native tools require native mode; use the restricted codex adapter or Claude's enforced selected tools")
		}
	case "claude":
		if len(p.RuntimeWritePaths) > 0 {
			return errors.New("Claude read-only tools cannot use runtime_write_paths")
		}
		// Claude's --restricted confines these file tools to working roots. Its
		// print interface has no equivalent of Codex's OS command sandbox.
		if p.Tools.Mode == "native" {
			return errors.New("Claude native tool inheritance is unsupported without an enforced command sandbox; select read-only tools")
		}
		for _, name := range p.Tools.Names {
			if !slices.Contains([]string{"Read", "Glob", "Grep", "WebFetch", "WebSearch"}, name) {
				return fmt.Errorf("unsupported restricted Claude tool %q", name)
			}
			if !p.Network && slices.Contains([]string{"WebFetch", "WebSearch"}, name) {
				return errors.New("Claude web tools require explicit native.network")
			}
		}
	default:
		return errors.New("native harness must be codex or claude")
	}
	return nil
}

// ValidateRuntimeWritePaths bounds explicitly configured cache roots. Executors
// also verify canonical existing directories outside source/coordinator state.
func ValidateRuntimeWritePaths(paths []string) error {
	if len(paths) > 16 {
		return errors.New("runtime_write_paths is limited to 16 configured directories")
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return errors.New("runtime_write_paths must be absolute directories other than the filesystem root")
		}
	}
	return nil
}

// NativeProvenance keeps bounded operational facts. Available is an observed
// inventory; Permitted is policy; Used contains observed calls. None is inferred
// from another. Missing harness telemetry is stated in Gaps.
type NativeProvenance struct {
	Machine          string              `json:"machine"`
	Platform         string              `json:"platform"`
	Harness          string              `json:"harness"`
	Version          string              `json:"version,omitempty"`
	Binary           string              `json:"binary"`
	Model            string              `json:"requested_model"`
	ReportedModel    string              `json:"reported_model,omitempty"`
	RequestDigest    string              `json:"request_sha256"`
	PromptBytes      int                 `json:"runner_prompt_bytes"`
	SchemaBytes      int                 `json:"output_schema_bytes"`
	UsageScope       string              `json:"usage_scope"`
	Context          *NativeContextFacts `json:"context,omitempty"`
	ResultsInput     *NativeInputFile    `json:"results_input,omitempty"`
	Reasoning        string              `json:"requested_reasoning,omitempty"`
	Access           string              `json:"access"`
	Permitted        NativeProfile       `json:"permitted"`
	Configuration    []NativeFingerprint `json:"configuration,omitempty"`
	Available        []string            `json:"available,omitempty"`
	Used             []NativeUse         `json:"used,omitempty"`
	Decision         string              `json:"decision,omitempty"`
	Gaps             []string            `json:"gaps,omitempty"`
	Dropped          int                 `json:"dropped_events,omitempty"`
	DiagnosticEvents int                 `json:"diagnostic_events,omitempty"`
	StartedAt        time.Time           `json:"worker_started_at"`
	LaunchedAt       time.Time           `json:"model_launched_at"`
	FirstEventAt     time.Time           `json:"first_event_at,omitempty"`
	CompletedAt      time.Time           `json:"completed_at,omitempty"`
	VersionMillis    int64               `json:"version_probe_ms"`
	ContextMillis    int64               `json:"context_probe_ms"`
	StartupMillis    int64               `json:"startup_ms"`
	ExecutionMillis  int64               `json:"execution_ms"`
	CleanupAssurance string              `json:"cleanup_assurance"`
}

// NativeContextFacts records generated policy and its configuration-only
// verification, without server commands, environments or authentication values.
type NativeContextFacts struct {
	Mode            string   `json:"mode"`
	RunnerOverrides []string `json:"runner_overrides,omitempty"`
	Overrides       []string `json:"overrides,omitempty"`
	ConfiguredMCP   []string `json:"configured_mcp,omitempty"`
	EnabledMCP      []string `json:"enabled_mcp"`
	Verified        bool     `json:"verified"`
}

// NativeInputFile identifies a complete immutable execution input, not a new
// artifact authority. Canonical task results remain owned by their child runs.
type NativeInputFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
	Items  int    `json:"items"`
}

type NativeFingerprint struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type NativeUse struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Status   string `json:"status,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}
