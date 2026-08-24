package config

import "fmt"

const (
	DefaultSnapshotMaxEntries    = 100_000
	DefaultSnapshotMaxFileBytes  = int64(64 * 1024 * 1024)
	DefaultSnapshotMaxTotalBytes = int64(1024 * 1024 * 1024)
)

// ResourceLimitsConfig exposes only repository snapshot scale. Operational
// GitHub and subprocess safety caps remain fixed in code.
type ResourceLimitsConfig struct {
	SnapshotMaxEntries    *int   `json:"snapshot_max_entries,omitempty"`
	SnapshotMaxFileBytes  *int64 `json:"snapshot_max_file_bytes,omitempty"`
	SnapshotMaxTotalBytes *int64 `json:"snapshot_max_total_bytes,omitempty"`
}

type ResourceLimits struct {
	SnapshotMaxEntries    int
	SnapshotMaxFileBytes  int64
	SnapshotMaxTotalBytes int64
}

func DefaultResourceLimits() ResourceLimits {
	return ResourceLimits{
		SnapshotMaxEntries:    DefaultSnapshotMaxEntries,
		SnapshotMaxFileBytes:  DefaultSnapshotMaxFileBytes,
		SnapshotMaxTotalBytes: DefaultSnapshotMaxTotalBytes,
	}
}

func DefaultResourceLimitsConfig() *ResourceLimitsConfig {
	entries := DefaultSnapshotMaxEntries
	fileBytes := DefaultSnapshotMaxFileBytes
	totalBytes := DefaultSnapshotMaxTotalBytes
	return &ResourceLimitsConfig{SnapshotMaxEntries: &entries, SnapshotMaxFileBytes: &fileBytes, SnapshotMaxTotalBytes: &totalBytes}
}

func (c Config) ResolveResourceLimits() ResourceLimits {
	resolved := DefaultResourceLimits()
	if c.ResourceLimits == nil {
		return resolved
	}
	if c.ResourceLimits.SnapshotMaxEntries != nil {
		resolved.SnapshotMaxEntries = *c.ResourceLimits.SnapshotMaxEntries
	}
	if c.ResourceLimits.SnapshotMaxFileBytes != nil {
		resolved.SnapshotMaxFileBytes = *c.ResourceLimits.SnapshotMaxFileBytes
	}
	if c.ResourceLimits.SnapshotMaxTotalBytes != nil {
		resolved.SnapshotMaxTotalBytes = *c.ResourceLimits.SnapshotMaxTotalBytes
	}
	return resolved
}

func validateResourceLimits(cfg *ResourceLimitsConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.SnapshotMaxEntries != nil && *cfg.SnapshotMaxEntries <= 0 {
		return fmt.Errorf("resource_limits.snapshot_max_entries must be positive; got %d", *cfg.SnapshotMaxEntries)
	}
	if cfg.SnapshotMaxFileBytes != nil && *cfg.SnapshotMaxFileBytes <= 0 {
		return fmt.Errorf("resource_limits.snapshot_max_file_bytes must be positive; got %d", *cfg.SnapshotMaxFileBytes)
	}
	if cfg.SnapshotMaxTotalBytes != nil && *cfg.SnapshotMaxTotalBytes <= 0 {
		return fmt.Errorf("resource_limits.snapshot_max_total_bytes must be positive; got %d", *cfg.SnapshotMaxTotalBytes)
	}
	resolved := DefaultResourceLimits()
	if cfg.SnapshotMaxEntries != nil {
		resolved.SnapshotMaxEntries = *cfg.SnapshotMaxEntries
	}
	if cfg.SnapshotMaxFileBytes != nil {
		resolved.SnapshotMaxFileBytes = *cfg.SnapshotMaxFileBytes
	}
	if cfg.SnapshotMaxTotalBytes != nil {
		resolved.SnapshotMaxTotalBytes = *cfg.SnapshotMaxTotalBytes
	}
	if resolved.SnapshotMaxTotalBytes < resolved.SnapshotMaxFileBytes {
		return fmt.Errorf("resource_limits.snapshot_max_total_bytes (%d) must be at least snapshot_max_file_bytes (%d)", resolved.SnapshotMaxTotalBytes, resolved.SnapshotMaxFileBytes)
	}
	return nil
}
