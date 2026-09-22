package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
)

func TestReadyPreviewResolvesDefaultAndAllowedProfileWithoutMutation(t *testing.T) {
	bin := t.TempDir()
	writeFakeGitHubProjectCommand(t, bin)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	log := filepath.Join(t.TempDir(), "gh-calls")
	t.Setenv("GH_CALL_LOG", log)
	cfg := completeCLITestConfig(t.TempDir())
	model := "test-mechanical-model"
	cfg.Roles["mechanical"] = config.RoleConfig{Extends: "implementer", Description: "Existing pattern", Model: &model, Reasoning: "low"}
	cfg.PlannerImplementers = []string{"mechanical"}
	path := filepath.Join(t.TempDir(), "runner.json")
	if err := config.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []string{"", "mechanical"} {
		args := []string{"ready", "--config", path, "--title", "Fix header", "--body", "Only the approved header change.", "--dry-run"}
		if selected != "" {
			args = append(args, "--profile", selected)
		}
		var out bytes.Buffer
		if err := runAdd(t.Context(), args, &out); err != nil {
			t.Fatal(err)
		}
		want := "Implementation profile: implementer (configured default)"
		if selected != "" {
			want = "Implementation profile: mechanical (explicit selection); harness: codex; model: test-mechanical-model; reasoning: low"
		}
		if !strings.Contains(out.String(), want) || !strings.Contains(out.String(), "model:") || !strings.Contains(out.String(), "reasoning:") {
			t.Fatalf("preview omitted resolved policy: %s", out.String())
		}
	}
	for _, args := range [][]string{
		{"ready", "--profile", "unknown"},
		{"plan", "--profile", "mechanical"},
		{"ready", "--profile", "reviewer"},
	} {
		args = append(args, "--config", path, "--title", "Fix header", "--body", "Only header.")
		if err := runAdd(t.Context(), args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid selection accepted: %v", args)
		}
	}
	if calls, err := os.ReadFile(log); !os.IsNotExist(err) || len(calls) != 0 {
		t.Fatalf("preview/rejected profile contacted GitHub: %s %v", calls, err)
	}
	var out bytes.Buffer
	if err := runAdd(t.Context(), []string{"ready", "--config", path, "--title", "Fix header", "--body", "Only header.", "--profile", "mechanical"}, &out); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || strings.Count(string(calls), "project item-create") != 1 || !strings.Contains(string(calls), "## Runner implementation profile\n\nmechanical") {
		t.Fatalf("explicit choice not persisted exactly once: %s %v", calls, err)
	}
}

func TestReadyBodySelectionCannotBeSilentlyOverridden(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	cfg.Roles["mechanical"] = config.RoleConfig{Extends: "implementer", Description: "Existing pattern"}
	defaultProfile := cfg.Roles["implementer"]
	defaultProfile.Description = "Default implementation"
	cfg.Roles["implementer"] = defaultProfile
	cfg.PlannerImplementers = []string{"mechanical", "implementer"}
	resolved, err := cfg.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	body, err := github.WithManualImplementationProfile("Only this fix.", "mechanical")
	if err != nil {
		t.Fatal(err)
	}
	actual, summary, err := prepareReadyWork(resolved, body, "")
	if err != nil || actual != body || !strings.Contains(summary, "mechanical (explicit selection)") {
		t.Fatalf("existing choice not respected: %s %v", summary, err)
	}
	if _, _, err := prepareReadyWork(resolved, body, "implementer"); err == nil {
		t.Fatal("flag silently overrode body selection")
	}
}
