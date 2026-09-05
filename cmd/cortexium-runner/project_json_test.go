package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
)

func TestPlanJSONHonorsExplicitStaging(t *testing.T) {
	for _, mode := range []string{"preview", "--stage-only", "--create"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			bin := t.TempDir()
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			writeFakeInitGitCommand(t, bin)
			writeFakeGitHubProjectCommand(t, bin)
			if err := os.Rename(filepath.Join(bin, "gh"), filepath.Join(bin, "gh-fixture")); err != nil {
				t.Fatal(err)
			}
			// Stop at the first mutation: explicit staging must reach GitHub and
			// propagate its error, whereas a JSON preview must never get here.
			commands := map[string]string{
				"gh": `#!/bin/sh
if [ "$1 $2" = "project item-create" ]; then
  echo 'test staging failure' >&2
  exit 1
fi
exec gh-fixture "$@"
`,
				"codex": `#!/bin/sh
case "$*" in
  *--help*) printf '%s\n' '--ephemeral --json --cd --output-last-message --output-schema --config --model --sandbox --ask-for-approval --disable --enable --strict-config --ignore-user-config --ignore-rules --skip-git-repo-check'; exit 0 ;;
esac
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output-last-message) result_path="$2"; shift ;;
  esac
  shift
done
prompt=$(cat)
case "$prompt" in
  *'Shared planning contract — card details:'*)
    printf '%s\n' '{"cards":{"C1":{"objective":"Build the slice","done_when":["It works"],"proof_obligations":["The behavior is demonstrated"],"assumptions":[]}}}' > "$result_path" ;;
  *)
    printf '%s\n' '{"goal_summary":"Build a slice","project_success_criteria":["It works"],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Build the slice","dependencies":[]}]}' > "$result_path" ;;
esac
`,
			}
			for name, body := range commands {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := completeCLITestConfig(t.TempDir())
			configPath := filepath.Join(t.TempDir(), "runner.json")
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			args := []string{"--config", configPath, "--idea", "Build a slice", "--json"}
			if mode != "preview" {
				args = append(args, mode)
			}
			var output bytes.Buffer
			err := runPlan(t.Context(), args, strings.NewReader(""), &output)
			if mode != "preview" {
				if err == nil || !strings.Contains(err.Error(), "test staging failure") {
					t.Fatalf("explicit staging did not propagate the GitHub error: %v; output: %s", err, &output)
				}
				if output.Len() != 0 {
					t.Fatalf("failed staging emitted a misleading JSON receipt: %s", &output)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var plan engine.ProjectPlan
			if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
				t.Fatalf("preview is not a single JSON plan: %v; output: %s", err, &output)
			}
			if len(plan.WorkItems) != 1 || plan.WorkItems[0].Title != "Build the slice" {
				t.Fatalf("unexpected preview: %#v", plan)
			}
		})
	}
}
