package execution

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
)

// TestLivePlannerAndReviewerHarness is intentionally opt-in because it invokes
// a real configured model and may incur usage. Together with
// TestLiveWorkspaceWriteHarness, it proves all three Runner role adapters.
//
// Run one or more harnesses with:
//
//	CORTEXIUM_RUNNER_LIVE_HARNESSES=codex,claude,pi go test ./internal/execution -run '^TestLive.*Harness$' -v
func TestLivePlannerAndReviewerHarness(t *testing.T) {
	requested := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LIVE_HARNESSES"))
	if requested == "" {
		t.Skip("set CORTEXIUM_RUNNER_LIVE_HARNESSES to run paid live harness checks")
	}
	for _, kind := range strings.Split(requested, ",") {
		kind := strings.TrimSpace(kind)
		t.Run(kind, func(t *testing.T) {
			if !config.ValidHarnessKind(kind) {
				t.Fatalf("unsupported live harness %q", kind)
			}
			repo := initGitRepo(t)
			cfg := config.ExecutionConfig{
				WorkspaceBaseRef: "HEAD",
				Harness: config.HarnessConfig{
					Kind: kind, Command: liveHarnessCommand(kind), WorkingDir: repo,
					WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: 300, ReasoningEffort: "high",
				},
			}

			t.Run("planner", func(t *testing.T) {
				schema := []byte(`{
  "type": "object",
  "required": ["summary"],
  "properties": {"summary": {"type": "string", "const": "runner-live-planner-probe"}},
  "additionalProperties": false
}`)
				prompt := "Use the runner-planner skill. Inspect README.md, make no changes, and return the required structured summary."
				result, err := RunPlannerWithUsage(
					t.Context(), kind, cfg, repo, prompt, schema, nil,
				)
				if err != nil {
					t.Fatalf("live planner: %v", err)
				}
				var output struct {
					Summary string `json:"summary"`
				}
				canonical, err := CanonicalizeStructuredResult(result.Message, "summary")
				if err != nil {
					t.Fatalf("canonicalize live planner result: %v", err)
				}
				if err := json.Unmarshal([]byte(canonical), &output); err != nil {
					t.Fatalf("decode live planner result: %v\nresult: %s", err, result.Message)
				}
				if output.Summary != "runner-live-planner-probe" {
					t.Fatalf("unexpected live planner result: %s", result.Message)
				}
			})

			t.Run("reviewer", func(t *testing.T) {
				assignment := Assignment{Spec: Spec{
					ID: "live_reviewer_probe_" + kind,
					Task: Task{
						Title:        "Prove live reviewer execution",
						Instructions: "Use the runner-reviewer skill. Inspect README.md and run git diff --check. Make no changes. Accept only if the required criterion, repository instructions, and maintainability checks pass.",
					},
					RequiredVerification: []string{"README.md contains exactly hello followed by a newline and git diff --check passes"},
					ReviewRequired:       true,
				}}
				var output Output
				var err error
				if kind == config.HarnessCodexCLI {
					output, err = NewCodexExecutor(cfg, nil).Execute(t.Context(), assignment)
				} else {
					output, err = NewAgentExecutor(kind, cfg, nil).Execute(t.Context(), assignment)
				}
				if err != nil {
					t.Fatalf("live reviewer: %v\noutput: %#v", err, output)
				}
				if output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "accept" {
					t.Fatalf("unexpected live reviewer outcome: %#v", output)
				}
			})
		})
	}
}

// TestLiveReviewerBrowserHarness is opt-in because it invokes a real model.
// It exercises browser verification through the same neutral reviewer
// workspace and native sandbox used by Agent QA.
func TestLiveReviewerBrowserHarness(t *testing.T) {
	requested := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LIVE_BROWSER_HARNESSES"))
	if requested == "" {
		t.Skip("set CORTEXIUM_RUNNER_LIVE_BROWSER_HARNESSES to run paid reviewer browser checks")
	}
	for _, kind := range strings.Split(requested, ",") {
		kind := strings.TrimSpace(kind)
		t.Run(kind, func(t *testing.T) {
			if !config.ValidHarnessKind(kind) {
				t.Fatalf("unsupported live reviewer harness %q", kind)
			}
			repo := initGitRepo(t)
			page := `<!doctype html><html><body><main id="probe">runner-reviewer-browser-probe</main><script>document.body.dataset.runnerReady = "yes"; console.log("runner-reviewer-console-ok")</script></body></html>`
			if err := os.WriteFile(filepath.Join(repo, "browser-probe.html"), []byte(page), 0o644); err != nil {
				t.Fatalf("write browser probe: %v", err)
			}
			runGitCommand(t, repo, "add", "browser-probe.html")
			runGitCommand(t, repo, "commit", "-m", "Add browser probe")

			browserInstruction := "Use only Runner's runner_browser navigate_page and evaluate_script MCP tools. Call them directly when they are exposed as direct tools. In Code Mode, inspect ALL_TOOLS for runner_browser and invoke the matching functions through the tools object. Do not inspect list_mcp_resources because it does not list tools. Do not launch a browser through shell commands."
			if kind == config.HarnessPiCLI {
				browserInstruction = "Use only the runner_browser_navigate and runner_browser_evaluate tools granted by Runner. Do not launch a browser through shell commands."
			}
			cfg := config.ExecutionConfig{
				Harness: config.HarnessConfig{
					Kind: kind, Command: liveHarnessCommand(kind), WorkingDir: repo,
					WorkspaceWriteRoot: filepath.Join(t.TempDir(), "worktrees"), TimeoutSeconds: 180, ReasoningEffort: "medium",
				},
				SafeTools: true,
			}
			if access := strings.TrimSpace(os.Getenv("CORTEXIUM_RUNNER_LIVE_ACCESS")); access != "" {
				cfg.RoleAccess = access
			} else if kind == config.HarnessPiCLI {
				cfg.RoleAccess = config.RoleAccessHost
			}
			assignment := Assignment{Spec: Spec{
				ID: "live_reviewer_browser_probe_" + kind,
				Task: Task{
					Title:        "Prove sandboxed reviewer browser execution",
					Instructions: "Use the runner-reviewer skill. " + browserInstruction + " Start a temporary localhost static server for the assigned repository, load browser-probe.html with a fresh temporary browser profile, and for Chromium on macOS include --use-mock-keychain. Inspect the rendered DOM and console, then stop the server and remove every browser/server artifact outside the repository. Do not change the repository. Stop after the allowed browser attempts and report the exact capability failure if neither works.",
				},
				RequiredVerification: []string{"The localhost page renders runner-reviewer-browser-probe, JavaScript sets data-runner-ready to yes, and the console contains runner-reviewer-console-ok"},
				ReviewRequired:       true,
			}}
			var output Output
			var err error
			if kind == config.HarnessCodexCLI {
				output, err = NewCodexExecutor(cfg, nil).Execute(t.Context(), assignment)
			} else {
				output, err = NewAgentExecutor(kind, cfg, nil).Execute(t.Context(), assignment)
			}
			if err != nil {
				t.Fatalf("live reviewer browser: %v\noutput: %#v", err, output)
			}
			if output.Outcome != OutcomeSucceeded || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "accept" {
				t.Fatalf("unexpected live reviewer browser outcome: %#v", output)
			}
			if status := strings.TrimSpace(runGitCommandOutput(t, repo, "status", "--porcelain", "--untracked-files=all")); status != "" {
				t.Fatalf("reviewer changed the repository: %s", status)
			}
		})
	}
}
