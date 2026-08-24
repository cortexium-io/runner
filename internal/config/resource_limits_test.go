package config

import (
	"strings"
	"testing"
)

func TestResourceLimitsOmittedResolveToSnapshotDefaults(t *testing.T) {
	cfg := explicitTestConfig()
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	want := DefaultResourceLimits()
	if runtime.ResourceLimits != want || runtime.Execution(WorkRoleImplementer, HarnessCodexCLI, "/tmp/worktree").ResourceLimits != want {
		t.Fatalf("omitted resource limits resolved to %#v, want %#v", runtime.ResourceLimits, want)
	}
}

func TestResourceLimitsValidOverridesResolve(t *testing.T) {
	cfg := explicitTestConfig()
	entries, fileBytes, totalBytes := 20, int64(30), int64(40)
	cfg.ResourceLimits = &ResourceLimitsConfig{SnapshotMaxEntries: &entries, SnapshotMaxFileBytes: &fileBytes, SnapshotMaxTotalBytes: &totalBytes}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ResourceLimits != (ResourceLimits{SnapshotMaxEntries: 20, SnapshotMaxFileBytes: 30, SnapshotMaxTotalBytes: 40}) {
		t.Fatalf("override did not resolve: %#v", runtime.ResourceLimits)
	}
}

func TestResourceLimitsRejectInvalidValues(t *testing.T) {
	for name, limits := range map[string]*ResourceLimitsConfig{
		"entries":      {SnapshotMaxEntries: intPointer(0)},
		"individual":   {SnapshotMaxFileBytes: int64Pointer(-1)},
		"aggregate":    {SnapshotMaxTotalBytes: int64Pointer(0)},
		"inconsistent": {SnapshotMaxFileBytes: int64Pointer(10), SnapshotMaxTotalBytes: int64Pointer(9)},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.ResourceLimits = limits
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "resource_limits") {
				t.Fatalf("invalid resource limits were accepted: %v", err)
			}
		})
	}
}

func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
