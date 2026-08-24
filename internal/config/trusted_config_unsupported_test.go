//go:build !darwin && !linux

package config

import (
	"strings"
	"testing"
)

func TestTrustedConfigProvenanceFailsClosedOnUnsupportedPlatform(t *testing.T) {
	if _, err := LoadTrustedConfig("operator.json"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported platform did not fail closed: %v", err)
	}
}
