package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// Persisted evidence binds an explicit contract, not the shape of a runtime
// struct or the Runner release. Keep its field names and order stable. New
// execution policy must be added deliberately and covered by upgrade fixtures.
type executionSettingsBinding struct {
	TestSpecialist          *TestSpecialistConfig
	WorkspaceBaseRef        string
	RoleAccess              string
	HarnessConfigMode       string
	Harness                 harnessSettingsBinding
	Skills                  []string
	MCPServers              []string
	SafeTools               bool
	PreserveReasoning       bool
	Codemode                *bool `json:"Codemode,omitempty"`
	ResourceLimits          ResourceLimits
	RepositoryReferences    []RepositoryReference
	ReferenceProtectedRoots []string
	ReviewEvidenceRoot      string
	ReviewEvidencePaths     []string
}

type harnessSettingsBinding struct {
	Kind               string `json:"kind"`
	Command            string `json:"command,omitempty"`
	Enabled            *bool  `json:"enabled,omitempty"`
	WorkspaceWriteRoot string `json:"workspace_write_root,omitempty"`
}

func bindHarness(h HarnessConfig) harnessSettingsBinding {
	return harnessSettingsBinding{h.Kind, h.Command, h.Enabled, h.WorkspaceWriteRoot}
}

func settingsDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (c ExecutionConfig) evidenceSettings() executionSettingsBinding {
	return executionSettingsBinding{
		TestSpecialist: c.TestSpecialist, WorkspaceBaseRef: c.WorkspaceBaseRef,
		RoleAccess: c.RoleAccess, HarnessConfigMode: c.HarnessConfigMode,
		Harness: bindHarness(c.Harness), Skills: c.Skills, MCPServers: c.MCPServers,
		SafeTools: c.SafeTools, PreserveReasoning: c.PreserveReasoning, Codemode: &c.Codemode,
		ResourceLimits: c.ResourceLimits, RepositoryReferences: c.RepositoryReferences,
		ReferenceProtectedRoots: c.ReferenceProtectedRoots, ReviewEvidenceRoot: c.ReviewEvidenceRoot,
		ReviewEvidencePaths: c.ReviewEvidencePaths,
	}
}

func (c ExecutionConfig) EvidenceSettingsDigest() string {
	return settingsDigest(c.evidenceSettings())
}

// v0.6.1 omitted Codemode entirely. Its only representable behavior was off.
// Compare that exact historical projection of today's policy; never relabel a
// record or accept a missing binding for an enabled feature.
func (c ExecutionConfig) MatchesEvidenceSettings(digest string) bool {
	binding := c.evidenceSettings()
	if digest == settingsDigest(binding) {
		return true
	}
	if c.Codemode {
		return false
	}
	binding.Codemode = nil
	return digest == settingsDigest(binding)
}

type roleSettingsBinding struct {
	Harness           string   `json:"harness,omitempty"`
	Access            string   `json:"access,omitempty"`
	HarnessConfig     string   `json:"harness_config,omitempty"`
	SafeTools         *bool    `json:"safe_tools,omitempty"`
	Skills            []string `json:"skills,omitempty"`
	MCPServers        []string `json:"mcp_servers,omitempty"`
	Model             *string  `json:"model,omitempty"`
	Reasoning         string   `json:"reasoning,omitempty"`
	PreserveReasoning *bool    `json:"preserve_reasoning,omitempty"`
	Codemode          *bool    `json:"codemode,omitempty"`
	TaskGranularity   string   `json:"task_granularity,omitempty"`
	TimeoutSeconds    int      `json:"timeout_seconds,omitempty"`
}

func bindRole(p RoleConfig) roleSettingsBinding {
	return roleSettingsBinding{p.Harness, p.Access, p.HarnessConfig, p.SafeTools, p.Skills, p.MCPServers,
		p.Model, p.Reasoning, p.PreserveReasoning, p.Codemode, p.TaskGranularity, p.TimeoutSeconds}
}
