package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func TestPlanJSONHonorsExplicitStaging(t *testing.T) {
	for _, test := range []struct {
		name         string
		mode         string
		openDecision bool
	}{
		{name: "preview"},
		{name: "stage failure", mode: "--stage-only"},
		{name: "create failure", mode: "--create"},
		{name: "preview with open decision", openDecision: true},
		{name: "stage with open decision", mode: "--stage-only", openDecision: true},
		{name: "create with open decision", mode: "--create", openDecision: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			bin := t.TempDir()
			githubLog := filepath.Join(bin, "github.log")
			plannerLog := filepath.Join(bin, "planner.log")
			t.Setenv("GH_CALL_LOG", githubLog)
			t.Setenv("FAKE_PLANNER_LOG", plannerLog)
			decisions := []string{}
			if test.openDecision {
				decisions = append(decisions, "Which account may the integration test modify?")
			}
			encodedDecisions, err := json.Marshal(decisions)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_PLAN_OUTLINE", fmt.Sprintf(`{"goal_summary":"Build a slice","project_success_criteria":["It works"],"project_constraints":["No customer data"],"open_decisions":%s,"cards":[{"title":"Build the slice","dependencies":[]}]}`, encodedDecisions))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			writeFakeInitGitCommand(t, bin)
			writeFakeGitHubProjectCommand(t, bin)
			if err := os.Rename(filepath.Join(bin, "gh"), filepath.Join(bin, "gh-fixture")); err != nil {
				t.Fatal(err)
			}
			// Stop at the first mutation. Preview and open-decision plans must
			// never get here; executable plans must preserve the GitHub error.
			commands := map[string]string{
				"gh": `#!/bin/sh
if [ "$1 $2" = "project item-create" ]; then
  printf '%s\n' "$*" >> "$GH_CALL_LOG"
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
    printf '%s\n' 'details' >> "$FAKE_PLANNER_LOG"
    printf '%s\n' '{"cards":{"C1":{"objective":"Build the slice","done_when":["It works"],"proof_obligations":["The behavior is demonstrated"],"assumptions":[]}}}' > "$result_path" ;;
  *)
    printf '%s\n' 'outline' >> "$FAKE_PLANNER_LOG"
    printf '%s\n' "$FAKE_PLAN_OUTLINE" > "$result_path" ;;
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
			worker, err := github.AcquireProcessLock(*cfg.GitHubProject)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Release()
			before, active, err := github.InspectProcessState(*cfg.GitHubProject)
			if err != nil || !active {
				t.Fatalf("worker not active: %v", err)
			}
			ideaPath := filepath.Join(t.TempDir(), "REQUEST.md")
			if err := os.WriteFile(ideaPath, []byte("Build a slice"), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"plan", "--config", configPath, "--idea-file", ideaPath, "--small-tasks", "--json"}
			if test.mode != "" {
				args = append(args, test.mode)
			}
			var output, stderr bytes.Buffer
			code := execute(t.Context(), args, strings.NewReader(""), &output, &stderr)
			after, active, inspectErr := github.InspectProcessState(*cfg.GitHubProject)
			if inspectErr != nil || !active || after != before {
				t.Fatalf("CLI planning changed worker runtime state: before=%+v after=%+v active=%t err=%v", before, after, active, inspectErr)
			}
			if duplicate, lockErr := github.AcquireProcessLock(*cfg.GitHubProject); !errors.Is(lockErr, github.ErrProjectLockBusy) {
				_ = duplicate.Release()
				t.Fatalf("CLI planning released worker ownership: %v", lockErr)
			}
			calls, err := os.ReadFile(plannerLog)
			if err != nil || string(calls) != "outline\ndetails\n" {
				t.Fatalf("expected exactly one invocation per planner stage, got %q: %v", calls, err)
			}
			githubCalls, err := os.ReadFile(githubLog)
			if err != nil {
				t.Fatal(err)
			}
			wantCreates := 0
			if test.mode != "" && !test.openDecision {
				wantCreates = 1
			}
			if creates := strings.Count(string(githubCalls), "project item-create"); creates != wantCreates {
				t.Fatalf("unexpected staging attempts: got %d, want %d; calls: %s", creates, wantCreates, githubCalls)
			}
			if strings.Contains(string(githubCalls), "project item-edit") || strings.Contains(string(githubCalls), "mutation(") {
				t.Fatalf("blocked planning modified GitHub: %s", githubCalls)
			}
			var plan engine.ProjectPlan
			if test.mode != "" {
				wantError := "test staging failure"
				if test.openDecision {
					wantError = "cannot stage cards while 1 open decision(s) require human input"
				}
				if code != 1 || !strings.Contains(stderr.String(), wantError) {
					t.Fatalf("staging error was not preserved: code=%d stderr=%s output=%s", code, &stderr, &output)
				}
				var result struct {
					Plan     engine.ProjectPlan `json:"plan"`
					Error    string             `json:"error"`
					Staged   json.RawMessage    `json:"staged"`
					Released json.RawMessage    `json:"released"`
				}
				if err := json.Unmarshal(output.Bytes(), &result); err != nil {
					t.Fatalf("failed staging lost its JSON plan: %v; output: %s", err, &output)
				}
				if stderr.String() != "error: "+result.Error+"\n" || len(result.Staged) != 0 || len(result.Released) != 0 {
					t.Fatalf("JSON changed the error or claimed staging succeeded: %s", &output)
				}
				if test.openDecision && (!strings.Contains(result.Error, "rerun the same planning command") || strings.Contains(result.Error, "--create")) {
					t.Fatalf("open-decision recovery changed the requested mode: %s", result.Error)
				}
				plan = result.Plan
			} else {
				if code != 0 || stderr.Len() != 0 {
					t.Fatalf("preview failed: code=%d stderr=%s", code, &stderr)
				}
				if err := json.Unmarshal(output.Bytes(), &plan); err != nil {
					t.Fatalf("preview is not a single JSON plan: %v; output: %s", err, &output)
				}
			}
			if plan.GoalSummary != "Build a slice" || !reflect.DeepEqual(plan.OpenDecisions, decisions) ||
				!reflect.DeepEqual(plan.ProjectSuccessCriteria, []string{"It works"}) ||
				!reflect.DeepEqual(plan.ProjectConstraints, []string{"No customer data"}) ||
				len(plan.WorkItems) != 1 || plan.WorkItems[0].Title != "Build the slice" || plan.WorkItems[0].Summary != "Build the slice" ||
				!reflect.DeepEqual(plan.WorkItems[0].AcceptanceCriteria, []string{"It works"}) ||
				!reflect.DeepEqual(plan.WorkItems[0].Verification, []string{"The behavior is demonstrated"}) {
				t.Fatalf("generated plan fields were lost: %#v", plan)
			}
		})
	}
}
