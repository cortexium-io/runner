package config

import (
	"strings"
	"testing"
)

func TestVerificationRuntimePathsAreExplicitBoundedArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		paths []string
		valid bool
	}{
		{"none", nil, true},
		{"engine bundle", []string{"/Applications/Chromium.app", "/opt/browser/lib/engine.so"}, true},
		{"relative", []string{"browser/engine"}, false},
		{"filesystem root", []string{"/"}, false},
		{"application root", []string{"/Applications"}, false},
		{"user home", []string{"/Users/operator"}, false},
		{"installation root", []string{"/opt/homebrew"}, false},
		{"project root", []string{"/work/runner"}, false},
		{"project ancestor", []string{"/work"}, false},
		{"unclean", []string{"/opt/browser/../engine"}, false},
		{"control", []string{"/opt/browser/engine\x01"}, false},
		{"glob", []string{"/opt/browser/*"}, false},
		{"git administration", []string{"/opt/browser/.git/objects"}, false},
		{"duplicate", []string{"/opt/browser/engine", "/opt/browser/engine"}, false},
		{"overlapping", []string{"/opt/browser/engine", "/opt/browser"}, false},
		{"oversized path", []string{"/opt/" + strings.Repeat("x", 4096)}, false},
		{"oversized selection", make([]string, 65), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateVerificationRuntimePaths(tc.paths, "/work/runner"); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestVerificationRuntimePathsRefusedUntilCollectorAvailable(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Verification = map[string]VerificationEntrypoint{"complete": {
		Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, TimeoutSeconds: 60,
		InputPaths: []string{"src"}, RuntimePaths: []string{"/Applications/Chromium.app"},
	}}
	if err := ValidateConfiguration(cfg); err == nil || !strings.Contains(err.Error(), "not supported until bounded runtime artifact observation") {
		t.Fatalf("unobserved runtime artifact selection was accepted: %v", err)
	}
}

func TestVerificationDigestBindsRuntimeSelection(t *testing.T) {
	entry := VerificationEntrypoint{Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, InputPaths: []string{"src"}, TimeoutSeconds: 60}
	original := entry.Digest()
	entry.RuntimePaths = []string{"/Applications/Chromium.app"}
	first := entry.Digest()
	entry.RuntimePaths[0] = "/Applications/OtherChromium.app"
	if original == first || first == entry.Digest() {
		t.Fatal("changing selected runtime artifacts did not invalidate approved catalog identity")
	}
}
