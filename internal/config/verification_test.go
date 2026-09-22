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

func TestVerificationRuntimePathsAcceptsBoundedExplicitClosure(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Verification = map[string]VerificationEntrypoint{"complete": {
		Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, TimeoutSeconds: 60,
		InputPaths: []string{"src"}, RuntimePaths: []string{"/Applications/Chromium.app"},
	}}
	if err := ValidateConfiguration(cfg); err != nil {
		t.Fatalf("bounded runtime artifact selection was refused: %v", err)
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

func TestVerificationPreparationConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*VerificationEntrypoint)
		valid  bool
	}{
		{"one command", func(*VerificationEntrypoint) {}, true},
		{"absolute command", func(e *VerificationEntrypoint) { e.Preparation.Command = "/opt/node/bin/npm" }, true},
		{"missing command", func(e *VerificationEntrypoint) { e.Preparation.Command = "" }, false},
		{"relative executable", func(e *VerificationEntrypoint) { e.Preparation.Command = "bin/npm" }, false},
		{"NUL argument", func(e *VerificationEntrypoint) { e.Preparation.Args = []string{"ci\x00"} }, false},
		{"no mutable roots", func(e *VerificationEntrypoint) { e.DependencyPaths = nil }, false},
		{"source contains dependencies", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{"src/deps"} }, false},
		{"dependencies contain source", func(e *VerificationEntrypoint) { e.InputPaths = []string{"node_modules/source"} }, false},
		{"dependencies contain dependencies", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{"node_modules/nested", "node_modules"} }, false},
		{"Git administration", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{".git/objects"} }, false},
		{"repository workflow", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{".github/workflows"} }, false},
		{"agent control", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{".codex"} }, false},
		{"instructions", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{"AGENTS.md"} }, false},
		{"escape", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{"../dependencies"} }, false},
		{"path prefix is not ancestor", func(e *VerificationEntrypoint) { e.DependencyPaths = []string{"src-dependencies"} }, true},
		{"cache exclusion inside dependency", func(e *VerificationEntrypoint) { e.DependencyExcludePaths = []string{"node_modules/.vite"} }, true},
		{"exclusion cannot widen dependency", func(e *VerificationEntrypoint) { e.DependencyExcludePaths = []string{"src/cache"} }, false},
		{"explicit preparation cache root", func(e *VerificationEntrypoint) {
			e.DependencyPaths = []string{"node_modules", ".runner-npm-cache"}
			e.DependencyExcludePaths = []string{".runner-npm-cache"}
		}, true},
		{"complete root exclusion without preparation", func(e *VerificationEntrypoint) {
			e.Preparation = nil
			e.DependencyPaths = []string{"node_modules", ".runner-npm-cache"}
			e.DependencyExcludePaths = []string{".runner-npm-cache"}
		}, false},
		{"all dependencies excluded", func(e *VerificationEntrypoint) {
			e.DependencyExcludePaths = []string{"node_modules"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			entry := VerificationEntrypoint{
				Command: "npm", Args: []string{"test"}, ToolchainCommands: []string{"node", "npm"}, TimeoutSeconds: 60,
				InputPaths: []string{"src", "package.json", "package-lock.json"}, DependencyPaths: []string{"node_modules"},
				Preparation: &VerificationPreparation{Command: "npm", Args: []string{"ci", "--cache", "node_modules/.npm-cache"}},
			}
			tc.change(&entry)
			cfg.Verification = map[string]VerificationEntrypoint{"complete": entry}
			if err := ValidateConfiguration(cfg); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
		})
	}
}

func TestPreparationCannotOverlapSelectedRuntime(t *testing.T) {
	for _, runtimePath := range []string{"/work/project/node_modules", "/work/project/node_modules/engine", "/work/project"} {
		entry := VerificationEntrypoint{DependencyPaths: []string{"node_modules"}, RuntimePaths: []string{runtimePath}}
		if err := validateVerificationPreparationPaths(entry, "/work/project"); err == nil {
			t.Fatalf("runtime %q overlapped mutable dependencies", runtimePath)
		}
	}
}

func TestVerificationDigestBindsPreparation(t *testing.T) {
	entry := VerificationEntrypoint{Command: "npm", ToolchainCommands: []string{"npm", "node"}, InputPaths: []string{"src"}, DependencyPaths: []string{"node_modules"}, TimeoutSeconds: 60}
	without := entry.Digest()
	entry.Preparation = &VerificationPreparation{Command: "npm", Args: []string{"ci"}}
	with := entry.Digest()
	entry.Preparation.Args = []string{"install"}
	changed := entry.Digest()
	entry.Preparation.Command = "/opt/node/bin/npm"
	if without == with || with == changed || changed == entry.Digest() {
		t.Fatal("preparation command/arguments did not bind the approved catalog digest")
	}
}

func TestVerificationDigestBindsCurrentCandidatePolicy(t *testing.T) {
	entry := VerificationEntrypoint{Command: "npm", ToolchainCommands: []string{"npm"}, TimeoutSeconds: 60, InputPaths: []string{"src"}}
	applicabilityPolicy := entry.Digest()
	entry.RequireCurrentCandidate = true
	if applicabilityPolicy == entry.Digest() {
		t.Fatal("requiring current-candidate execution did not change approved settings")
	}
}

func TestVerificationCurrentCandidateCheckConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		args          []string
		valid         bool
	}{
		{"PATH executable", "npm", []string{"run", "check:current"}, true},
		{"absolute executable", "/bin/sh", []string{"scripts/current-check.sh"}, true},
		{"missing executable", "", nil, false},
		{"relative executable", "bin/check", nil, false},
		{"command newline", "npm\n", nil, false},
		{"NUL argument", "npm", []string{"check\x00"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			entry := VerificationEntrypoint{Command: "npm", ToolchainCommands: []string{"node", "npm"}, TimeoutSeconds: 60, InputPaths: []string{"src"}}
			without := entry.Digest()
			entry.CurrentCandidateCheck = &VerificationCurrentCandidateCheck{Command: tc.command, Args: tc.args}
			cfg.Verification = map[string]VerificationEntrypoint{"complete": entry}
			if err := ValidateConfiguration(cfg); (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if !tc.valid {
				return
			}
			with := entry.Digest()
			entry.CurrentCandidateCheck.Args = append(entry.CurrentCandidateCheck.Args, "--different")
			changedArgs := entry.Digest()
			entry.CurrentCandidateCheck.Command = "/opt/other/check"
			if without == with || with == changedArgs || changedArgs == entry.Digest() {
				t.Fatal("current-candidate command/arguments not bound to catalog approval")
			}
		})
	}
}
