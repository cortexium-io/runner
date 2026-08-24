package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

func TestInteractiveClaudeModelMenuUsesReadableFamilies(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "claude")
	content := "#!/bin/sh\nprintf '%s\\n' \"--model accepts 'fable', 'opus', or 'sonnet'\"\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	var output bytes.Buffer
	prompter := newInitPrompter(strings.NewReader("1\n"), &output)
	model, err := prompter.model(t.Context(), "Model for all roles", config.HarnessClaudeCLI)
	if err != nil {
		t.Fatalf("choose Claude model: %v", err)
	}
	if model != "opus" {
		t.Fatalf("selected model = %q, want opus", model)
	}
	for _, expected := range []string{"1) Opus", "2) Sonnet", "3) Fable", "Use harness-native selection", "Enter a custom model ID"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("Claude model menu missing %q:\n%s", expected, output.String())
		}
	}
}

func TestInteractiveModelMenuRetainsCustomIDEscapeHatch(t *testing.T) {
	bin := t.TempDir()
	path := filepath.Join(bin, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	var output bytes.Buffer
	prompter := newInitPrompter(strings.NewReader("4\nclaude-opus-4-8\n"), &output)
	model, err := prompter.model(t.Context(), "Model", config.HarnessClaudeCLI)
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
