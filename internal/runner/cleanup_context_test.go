package runner

import (
	"testing"
	"time"
)

func TestCleanupCallTimeoutKeepsFrozenBoundAndLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		want time.Duration
	}{
		{"20m", 5 * time.Minute},
		{"1m", time.Minute},
		{"3s", 3 * time.Second},
		{"", 5 * time.Second},
		{"invalid", 5 * time.Second},
		{"0s", 5 * time.Second},
		{"-1s", 5 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanupCallTimeout(Process{AttemptTimeout: tc.name}); got != tc.want {
				t.Fatal("cleanup changed its maintenance/frozen bound", got, tc.want)
			}
		})
	}
}
