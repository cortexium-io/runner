package config

import (
	"strings"
	"testing"
)

func TestResourceLimitsResolveAndPropagateToExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits *ResourceLimitsConfig
		want   ResourceLimits
	}{
		{name: "omitted defaults", want: DefaultResourceLimits()},
		{
			name:   "explicit overrides",
			limits: &ResourceLimitsConfig{SnapshotMaxEntries: intPointer(20), SnapshotMaxFileBytes: int64Pointer(30), SnapshotMaxTotalBytes: int64Pointer(40)},
			want:   ResourceLimits{SnapshotMaxEntries: 20, SnapshotMaxFileBytes: 30, SnapshotMaxTotalBytes: 40},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.ResourceLimits = tc.limits
			runtime, err := cfg.Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if runtime.ResourceLimits != tc.want {
				t.Fatalf("resolved resource limits = %#v, want %#v", runtime.ResourceLimits, tc.want)
			}
			if got := runtime.Execution(WorkRoleImplementer, HarnessCodexCLI, "/tmp/worktree").ResourceLimits; got != tc.want {
				t.Fatalf("execution resource limits = %#v, want %#v", got, tc.want)
			}
		})
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
