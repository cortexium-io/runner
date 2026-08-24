package setup

import (
	"time"

	"github.com/cortexium-io/runner/internal/config"
)

const (
	CapabilityAvailable = "available"
	CapabilityMissing   = "missing"
	CapabilityBlocked   = "blocked"
)

type CapabilitySnapshot struct {
	RunnerID            string                         `json:"runner_id"`
	CheckedAt           time.Time                      `json:"checked_at"`
	Capabilities        []CapabilityState              `json:"capabilities"`
	MissingCapabilities []config.CapabilityRequirement `json:"missing_capabilities"`
}

type CapabilityState struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Version *string `json:"version,omitempty"`
	Detail  *string `json:"detail,omitempty"`
}
