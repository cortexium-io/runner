package main

import (
	"strings"
	"testing"
)

func TestFrontendRecoveryCLIRejectsGrantsAndUnboundRequests(t *testing.T) {
	base := []string{
		"recover-frontend-check", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "blocked-owner",
		"--revision", "8", "--patch-sha256", strings.Repeat("a", 64), "--helper-sha256", strings.Repeat("b", 64),
		"--publication-head", strings.Repeat("c", 40),
	}
	for _, invalid := range [][]string{
		{"--extra-attempts", "1"}, {"--profile", "override"}, {"--max-uses", "3"}, {"--preserve-source"}, {"--cleanup-only"},
		{"--revision", "0"}, {"--patch-sha256", ""}, {"--helper-sha256", ""}, {"--publication-head", ""},
	} {
		args := append(append([]string{}, base...), invalid...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "grants no retry or profile override") {
			t.Fatal("unbound request or permission override reached service", invalid, err)
		}
	}
}

func TestFrontendTimeoutRecoveryCLIRequiresPriorCheckAndRejectsAllowanceOverrides(t *testing.T) {
	base := []string{
		"recover-frontend-timeouts", "--data", t.TempDir(), "--run", "saved-run", "--attempt", "blocked-owner",
		"--revision", "8", "--patch-sha256", strings.Repeat("a", 64), "--helper-sha256", strings.Repeat("b", 64),
		"--publication-head", strings.Repeat("c", 40), "--check-attempt", "old-check", "--check-sha256", strings.Repeat("d", 64),
	}
	for _, invalid := range [][]string{
		{"--check-attempt", ""}, {"--check-sha256", ""}, {"--extra-attempts", "1"}, {"--profile", "override"}, {"--max-uses", "3"},
	} {
		args := append(append([]string{}, base...), invalid...)
		if err := run(t.Context(), args); err == nil || !strings.Contains(err.Error(), "grants no retry or profile override") {
			t.Fatal("unsafe timeout recovery reached service", invalid, err)
		}
	}
}
