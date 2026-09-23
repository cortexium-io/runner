package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestInteractiveClaudeModelMenuPinsRecommendedVersion(t *testing.T) {
	var output bytes.Buffer
	prompter := newInitPrompter(strings.NewReader("1\n"), &output)
	model, err := prompter.model(t.Context(), "Model for all roles", config.HarnessClaudeCLI, "")
	if err != nil {
		t.Fatalf("choose Claude model: %v", err)
	}
	if model != "claude-opus-5-5" {
		t.Fatalf("selected model = %q, want claude-opus-5-5", model)
	}
	for _, expected := range []string{"1) Opus 5.5 (recommended)", "2) Sonnet", "3) Use harness-native selection", "4) Enter a custom model ID"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("Claude model menu missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "Fable") {
		t.Fatalf("Claude model menu advertised an unsupported alias:\n%s", output.String())
	}
}

func TestInteractiveModelMenuRetainsCustomIDEscapeHatch(t *testing.T) {
	var output bytes.Buffer
	prompter := newInitPrompter(strings.NewReader("4\nclaude-opus-4-8\n"), &output)
	model, err := prompter.model(t.Context(), "Model", config.HarnessClaudeCLI, "")
	if err != nil {
		t.Fatalf("choose custom Claude model: %v", err)
	}
	if model != "claude-opus-4-8" {
		t.Fatalf("custom model = %q", model)
	}
}

func TestCodexModelMenuUsesLocalModelCatalog(t *testing.T) {
	root := t.TempDir()
	content := `{
  "models": [
    {"slug":"gpt-test-sol","display_name":"GPT Test Sol","description":"Frontier coding model.","visibility":"list"},
    {"slug":"hidden-model","display_name":"Hidden","description":"Hidden.","visibility":"hide"}
  ]
}`
	if err := os.WriteFile(filepath.Join(root, "models_cache.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", root)

	options := codexModelOptions()
	if len(options) != 1 || options[0].Label != "GPT Test Sol" || options[0].Value != "gpt-test-sol" {
		t.Fatalf("Codex model options = %#v", options)
	}
}

func TestPiModelMenuUsesModelsReportedByCLI(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "pi")
	content := `#!/bin/sh
printf '%s\n' 'provider  model             context  max-out  thinking  images'
printf '%s\n' 'lmstudio  qwen/qwen3.8-27b  128K     16.4K    yes       no'
printf '%s\n' 'ollama    llama3.1:8b       128K     32K      no        no'
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	options := piModelOptions(t.Context(), "")
	if len(options) != 2 || options[0].Value != "lmstudio/qwen/qwen3.8-27b" || options[1].Value != "ollama/llama3.1:8b" {
		t.Fatalf("Pi model options = %#v", options)
	}
}

func TestRecommendedModelNeverDependsOnCatalogOrderOrInventsAvailability(t *testing.T) {
	for _, tc := range []struct {
		role string
		want string
	}{
		{config.WorkRolePlanner, "gpt-6-astra"},
		{config.WorkRoleImplementer, "gpt-6-sol"},
		{config.WorkRoleReviewer, "gpt-6-astra"},
	} {
		t.Run(tc.role, func(t *testing.T) {
			options := []initModelOption{{Value: "gpt-6-luna"}, {Value: "gpt-6-sol"}, {Value: "gpt-6-astra"}, {Native: true}, {Custom: true}}
			got := recommendedModelIndex(config.HarnessCodexCLI, tc.role, options)
			if options[got].Value != tc.want {
				t.Fatalf("recommended model = %q, want %q", options[got].Value, tc.want)
			}
			if got := recommendedModelIndex(config.HarnessPiCLI, tc.role, options); got != 3 {
				t.Fatalf("Pi silently selected a provider: %d", got)
			}
			options[got].Value = "other-model"
			if got := recommendedModelIndex(config.HarnessCodexCLI, tc.role, options); got != 3 {
				t.Fatalf("missing recommendation did not leave native selection: %d", got)
			}
		})
	}
}

func TestInteractiveCodexEnterAcceptsVisibleRoleRecommendations(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "models_cache.json"), []byte(`{"models":[{"slug":"gpt-6-astra","visibility":"list"},{"slug":"gpt-6-sol","visibility":"list"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", root)
	var output bytes.Buffer
	p := newInitPrompter(strings.NewReader("\n\n\n\n\n\n"), &output)
	ph, ih, rh := config.HarnessCodexCLI, config.HarnessCodexCLI, config.HarnessCodexCLI
	pm, im, rm, pr, ir, rr := "", "", "", "", "", ""
	if err := promptInitRuntimeChoices(t.Context(), p, &ph, &ih, &rh, &pm, &im, &rm, &pr, &ir, &rr); err != nil {
		t.Fatalf("role selections: %v; prompts: %s", err, output.String())
	}
	if pm != "gpt-6-astra" || pr != "medium" || im != "gpt-6-sol" || ir != "high" || rm != "gpt-6-astra" || rr != "medium" {
		t.Fatalf("unexpected recommendations: %s/%s %s/%s %s/%s", pm, pr, im, ir, rm, rr)
	}
	for _, label := range []string{"Planner model", "Implementer model", "Reviewer model", "gpt-6-astra (recommended)", "gpt-6-sol (recommended)"} {
		if !strings.Contains(output.String(), label) {
			t.Fatalf("missing visible recommendation %q: %s", label, output.String())
		}
	}
}

func TestRuntimeChoicesUseModelSpecificReasoningAndPreserveOverrides(t *testing.T) {
	var output bytes.Buffer
	// Models are explicit: only the two missing efforts should be prompted.
	p := newInitPrompter(strings.NewReader("\n\n"), &output)
	ph, ih, rh := config.HarnessCodexCLI, config.HarnessCodexCLI, config.HarnessCodexCLI
	pm, im, rm := "gpt-6-sol", "gpt-6-luna", "gpt-6-astra"
	pr, ir, rr := "", "xhigh", ""
	if err := promptInitRuntimeChoices(t.Context(), p, &ph, &ih, &rh, &pm, &im, &rm, &pr, &ir, &rr); err != nil {
		t.Fatal(err)
	}
	if pr != "high" || ir != "xhigh" || rr != "medium" || pm != "gpt-6-sol" || im != "gpt-6-luna" || rm != "gpt-6-astra" {
		t.Fatalf("unexpected selections: %s/%s %s/%s %s/%s", pm, pr, im, ir, rm, rr)
	}
	base := config.RoleTemplate(config.HarnessCodexCLI)[config.WorkRoleReviewer]
	role := initRole(base, config.HarnessClaudeCLI, "claude-opus-5-5", "")
	if role.Reasoning != "medium" || role.Access != base.Access || role.TimeoutSeconds != base.TimeoutSeconds {
		t.Fatal("mixed-harness setup inherited wrong effort or changed containment/timeout")
	}
	role = initRole(base, config.HarnessCodexCLI, "", "low")
	if role.Model != nil || role.Reasoning != "low" {
		t.Fatal("native selection or explicit effort was overwritten")
	}
}

func TestPiSetupPermitsExplicitNonReasoningModelsWithoutWideningOtherHarnesses(t *testing.T) {
	for _, harnesses := range [][]string{{config.HarnessCodexCLI}, {config.HarnessClaudeCLI}, {config.HarnessPiCLI, config.HarnessCodexCLI}} {
		if slices.Contains(initReasoningOptions(harnesses...), "off") {
			t.Fatalf("off advertised to incompatible harnesses: %v", harnesses)
		}
	}
	var output bytes.Buffer
	p := newInitPrompter(strings.NewReader("off\n"), &output)
	got, err := p.reasoningChoice("Pi effort", initReasoningOptions(config.HarnessPiCLI), "medium")
	if err != nil || got != "off" {
		t.Fatalf("non-reasoning selection = %q, %v", got, err)
	}
}
