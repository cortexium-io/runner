package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	ConfigVersion          int                               `json:"config_version"`
	RunnerID               string                            `json:"runner_id"`
	Harnesses              []HarnessConfig                   `json:"harnesses"`
	Roles                  map[string]RoleConfig             `json:"roles"`
	PlannerImplementers    []string                          `json:"planner_implementers,omitempty"`
	ImplementerLadder      []string                          `json:"implementer_ladder,omitempty"`
	Workflow               *WorkflowConfig                   `json:"workflow"`
	DoctorRequirements     []CapabilityRequirement           `json:"doctor_requirements,omitempty"`
	ProjectDir             string                            `json:"project_dir"`
	RepositoryReferences   []RepositoryReference             `json:"repository_references,omitempty"`
	ReviewEvidencePaths    []string                          `json:"review_evidence_paths,omitempty"`
	MaxParallelism         int                               `json:"max_parallelism"`
	GuidanceMinOccurrences int                               `json:"guidance_min_occurrences,omitempty"`
	AdmissionBudget        *AdmissionBudgetConfig            `json:"admission_budget,omitempty"`
	ResourceLimits         *ResourceLimitsConfig             `json:"resource_limits,omitempty"`
	PlanDelivery           *PlanDeliveryConfig               `json:"plan_delivery,omitempty"`
	TestSpecialist         *TestSpecialistConfig             `json:"test_specialist,omitempty"`
	Verification           map[string]VerificationEntrypoint `json:"verification,omitempty"`
	GitHubProject          *GitHubProjectConfig              `json:"github_project"`
}

// TestSpecialistConfig permits one explicitly requested test-only handoff. It
// grants neither a new model nor broader execution access and is off by default.
// AllowedPaths selects exact files, not directories or filename patterns.
type TestSpecialistConfig struct {
	Enabled      bool     `json:"enabled"`
	AllowedPaths []string `json:"allowed_paths"`
}

// PlanDeliveryConfig is an explicit rollout boundary. Absence preserves the
// individual-card workflow, including historical planning-complete records.
type PlanDeliveryConfig struct {
	Enabled              bool   `json:"enabled"`
	CompleteVerification string `json:"complete_verification"`
	ReviewerRole         string `json:"reviewer_role,omitempty"`
}

// VerificationEntrypoint is operator-owned argv, not an expression or a model
// supplied command. Execution/containment is owned by the verification launcher.
type VerificationEntrypoint struct {
	Preparation             *VerificationPreparation           `json:"preparation,omitempty"`
	CurrentCandidateCheck   *VerificationCurrentCandidateCheck `json:"current_candidate_check,omitempty"`
	RequireCurrentCandidate bool                               `json:"require_current_candidate,omitempty"`
	ToolchainCommands       []string                           `json:"toolchain_commands"`
	RuntimePaths            []string                           `json:"runtime_paths,omitempty"`
	Command                 string                             `json:"command"`
	Args                    []string                           `json:"args,omitempty"`
	TimeoutSeconds          int                                `json:"timeout_seconds"`
	InputPaths              []string                           `json:"input_paths"`
	DependencyPaths         []string                           `json:"dependency_paths"`
	DependencyExcludePaths  []string                           `json:"dependency_exclude_paths,omitempty"`
}

// VerificationPreparation is one operator-owned dependency preparation command.
// It shares the entrypoint's directory, environment, containment, heavy claim
// and total deadline. Only declared dependency roots may change before the
// actual dependency snapshot becomes the executable check's baseline.
type VerificationPreparation struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// VerificationCurrentCandidateCheck is the maintained current-candidate guard,
// including when the heavy proof is historical. It shares the entrypoint's
// directory, containment, resource claim and total deadline; it grants no
// independent execution policy.
type VerificationCurrentCandidateCheck struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

func (e VerificationEntrypoint) Digest() string {
	encoded, _ := json.Marshal(e)
	return fmt.Sprintf("v1:%x", sha256.Sum256(encoded))
}

func (c Config) EffectiveGuidanceMinOccurrences() int {
	if c.GuidanceMinOccurrences == 0 {
		return 2
	}
	return c.GuidanceMinOccurrences
}

type RoleConfig struct {
	Description       string   `json:"description,omitempty"`
	Extends           string   `json:"extends,omitempty"`
	Harness           string   `json:"harness,omitempty"`
	Access            string   `json:"access,omitempty"`
	HarnessConfig     string   `json:"harness_config,omitempty"`
	SafeTools         *bool    `json:"safe_tools,omitempty"`
	Skills            []string `json:"skills,omitempty"`
	MCPServers        []string `json:"mcp_servers,omitempty"`
	Model             *string  `json:"model,omitempty"`
	Reasoning         string   `json:"reasoning,omitempty"`
	PreserveReasoning *bool    `json:"preserve_reasoning,omitempty"`
	TaskGranularity   string   `json:"task_granularity,omitempty"`
	TimeoutSeconds    int      `json:"timeout_seconds,omitempty"`
}

type HarnessConfig struct {
	Kind               string  `json:"kind"`
	Command            string  `json:"command,omitempty"`
	Enabled            *bool   `json:"enabled,omitempty"`
	WorkingDir         string  `json:"-"`
	WorkspaceWriteRoot string  `json:"workspace_write_root,omitempty"`
	TimeoutSeconds     int     `json:"-"`
	Model              *string `json:"-"`
	ReasoningEffort    string  `json:"-"`
}

func LoadConfig(path string) (Config, error) {
	cfg, _, err := CheckConfig(path)
	return cfg, err
}

func loadConfigFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return decodeConfig(data)
}

func decodeConfig(data []byte) (Config, error) {
	var cfg Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, err
	}
	var extra struct{}
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("config must contain exactly one JSON object")
	}
	return cfg, nil
}

// SaveConfig validates and atomically replaces a local Runner configuration.
func SaveConfig(path string, cfg Config) error {
	if err := ValidateConfiguration(cfg); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".runner-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close config: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("activate config: %w", err)
	}
	return nil
}

func (c Config) Harness(kind string) (HarnessConfig, bool) {
	for _, harness := range c.Harnesses {
		if strings.TrimSpace(harness.Kind) != kind || (harness.Enabled != nil && !*harness.Enabled) {
			continue
		}
		return harness, true
	}
	return HarnessConfig{}, false
}

func (h HarnessConfig) ExecutionPolicySummary() string {
	return "explicit per-role access and harness configuration policy"
}

func validReasoningEffort(harness, value string) bool {
	switch strings.TrimSpace(value) {
	case "low", "medium", "high", "xhigh":
		return true
	case "off":
		return strings.TrimSpace(harness) == HarnessPiCLI
	case "max":
		return strings.TrimSpace(harness) == HarnessCodexCLI
	default:
		return false
	}
}
