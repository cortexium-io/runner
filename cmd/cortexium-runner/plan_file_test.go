package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
)

func savedPlanFixture() engine.ProjectPlan {
	return engine.ProjectPlan{
		GoalSummary: "Build a slice", ProjectSuccessCriteria: []string{"It works"},
		ProjectConstraints: []string{"No customer data"}, OpenDecisions: []string{},
		SourceContext: "Build the approved slice; no customer data.",
		WorkItems: []github.PlannedItem{{
			Title: "Build the slice", Repository: "example/repo", Summary: "Implement the behavior",
			AcceptanceCriteria: []string{"It works"}, Verification: []string{"The behavior is demonstrated"},
			Risks: []string{}, NonGoals: []string{}, Dependencies: []string{},
		}},
	}
}

func writeSavedPlan(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSavedPlanRoundTripRetainsExactProposalWithoutReceiptAuthority(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	plan := savedPlanFixture()
	document := newProjectPlanDocument(cfg, plan)
	for _, wrapper := range []string{"", "error", "staged", "released"} {
		t.Run(wrapper, func(t *testing.T) {
			var value any = document
			if wrapper != "" {
				value = map[string]any{"plan": document, wrapper: map[string]string{"approval": "not-authority"}}
			}
			loaded, err := readProjectPlanFile(writeSavedPlan(t, value), cfg)
			if err != nil || !reflect.DeepEqual(loaded, plan) {
				t.Fatalf("saved plan changed its fingerprint inputs: got=%#v want=%#v error=%v", loaded, plan, err)
			}
		})
	}
}

func TestSavedPlanRejectsMissingOrChangedTargetAndUnexpectedAuthority(t *testing.T) {
	cfg := completeCLITestConfig(t.TempDir())
	for _, field := range []string{"owner", "number", "repository", "base_branch", "destination", "target", "source_context", "approval", "child approval", "result field"} {
		t.Run(field, func(t *testing.T) {
			data, _ := json.Marshal(newProjectPlanDocument(cfg, savedPlanFixture()))
			var value map[string]any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "target", "source_context":
				delete(value, field)
			case "approval":
				value[field] = "forged"
			case "child approval":
				value["work_items"].([]any)[0].(map[string]any)["approval"] = "forged"
			case "number":
				value["target"].(map[string]any)[field] = 999
			case "result field":
				value = map[string]any{"plan": value, "approval": "forged"}
			default:
				value["target"].(map[string]any)[field] = "other"
			}
			if _, err := readProjectPlanFile(writeSavedPlan(t, value), cfg); err == nil {
				t.Fatal("invalid saved plan was accepted")
			}
		})
	}
	for _, data := range []string{"{} {}", "null", "[]", strings.Repeat(" ", maxSavedPlanBytes+1)} {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readProjectPlanFile(path, cfg); err == nil {
			t.Fatal("malformed or oversized saved plan accepted")
		}
	}
}

func TestPlanFileCLIUsesNoPlannerAndPreservesStagingFailure(t *testing.T) {
	for _, mode := range []string{"preview", "open decision", "staging failure", "invalid dependency", "invalid profile"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CORTEXIUM_RUNNER_STATE_DIR", t.TempDir())
			t.Setenv("XDG_CACHE_HOME", t.TempDir())
			bin := t.TempDir()
			githubLog, plannerLog := filepath.Join(bin, "github.log"), filepath.Join(bin, "planner.log")
			t.Setenv("GH_CALL_LOG", githubLog)
			t.Setenv("FAKE_PLANNER_LOG", plannerLog)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			writeFakeGitHubProjectCommand(t, bin)
			if err := os.Rename(filepath.Join(bin, "gh"), filepath.Join(bin, "gh-fixture")); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{
				"codex": "#!/bin/sh\nprintf '%s\\n' invoked >> \"$FAKE_PLANNER_LOG\"\nexit 98\n",
				"gh": `#!/bin/sh
if [ "$1 $2" = "project item-create" ]; then
  printf '%s\n' "$*" >> "$GH_CALL_LOG"
  echo 'test staging failure' >&2
  exit 1
fi
exec gh-fixture "$@"
`,
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cfg := completeCLITestConfig(t.TempDir())
			configPath := filepath.Join(t.TempDir(), "runner.json")
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}
			plan := savedPlanFixture()
			switch mode {
			case "open decision":
				plan.OpenDecisions = []string{"Which account is disposable?"}
			case "invalid dependency":
				plan.WorkItems[0].Dependencies = []string{"unknown card"}
			case "invalid profile":
				plan.WorkItems[0].ImplementationProfile = "unconfigured"
				plan.WorkItems[0].ProfileReason = "Claimed to be cheaper"
			}
			path := writeSavedPlan(t, newProjectPlanDocument(cfg, plan))
			args := []string{"plan", "--config", configPath, "--plan-file", path, "--json"}
			if mode != "preview" {
				args = append(args, "--stage-only")
			}
			worker, err := github.AcquireProcessLock(*cfg.GitHubProject)
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Release()
			var output, stderr bytes.Buffer
			code := execute(t.Context(), args, strings.NewReader(""), &output, &stderr)
			if calls, err := os.ReadFile(plannerLog); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("import invoked the planner: %s (%v)", calls, err)
			}
			if duplicate, err := github.AcquireProcessLock(*cfg.GitHubProject); !errors.Is(err, github.ErrProjectLockBusy) {
				_ = duplicate.Release()
				t.Fatalf("import changed worker ownership: %v", err)
			}
			calls, _ := os.ReadFile(githubLog)
			wantCreates := 0
			if mode == "staging failure" {
				wantCreates = 1
			}
			if strings.Count(string(calls), "project item-create") != wantCreates || strings.Contains(string(calls), "project item-edit") {
				t.Fatalf("unexpected GitHub mutations: %s", calls)
			}
			if mode == "preview" {
				var got projectPlanDocument
				if code != 0 || json.Unmarshal(output.Bytes(), &got) != nil || got.SourceContext != plan.SourceContext {
					t.Fatalf("saved preview failed: code=%d stderr=%s output=%s", code, &stderr, &output)
				}
			} else if mode == "open decision" || mode == "staging failure" {
				var result struct {
					Plan  projectPlanDocument `json:"plan"`
					Error string              `json:"error"`
				}
				if code != 1 || json.Unmarshal(output.Bytes(), &result) != nil || result.Plan.SourceContext != plan.SourceContext || result.Error == "" || stderr.String() != "error: "+result.Error+"\n" {
					t.Fatalf("saved plan/error lost: code=%d stderr=%s output=%s", code, &stderr, &output)
				}
				if !reflect.DeepEqual(result.Plan.OpenDecisions, plan.OpenDecisions) {
					t.Fatalf("open decisions were lost: %s", &output)
				}
			} else if code != 1 || output.Len() != 0 {
				t.Fatalf("invalid proposal was accepted: code=%d stderr=%s output=%s", code, &stderr, &output)
			}
		})
	}
}

func TestPlanFileRejectsPlanningAndApprovalFlags(t *testing.T) {
	for _, flags := range [][]string{{"--idea", "changed"}, {"--idea-file", "idea.txt"}, {"--small-tasks"}, {"--create"}, {"--approve-staged", "v1:old"}} {
		args := append([]string{"plan", "--plan-file", "plan.json"}, flags...)
		var output, stderr bytes.Buffer
		if code := execute(t.Context(), args, strings.NewReader(""), &output, &stderr); code != 1 || !strings.Contains(stderr.String(), "--plan-file can only preview") {
			t.Fatalf("conflicting flags accepted: args=%v code=%d stderr=%s", flags, code, &stderr)
		}
	}
}
