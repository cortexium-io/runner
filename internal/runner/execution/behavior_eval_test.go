package execution

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Like the existing agent behavior evaluation, this is opt-in, uses native
// containment, retains evidence, and checks outcomes rather than prompt wording.
// Six small paired cases qualify only the replacement Ready reviewer contract.
func TestLiveReadyReviewBehavior(t *testing.T) {
	root, p, harness := liveEvaluation(t, "ready")
	const correct = "def greet(name):\n    return 'Hello, ' + (name.strip() or 'World') + '!'\n"
	const consolidated = "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value, expected in [('Ada', 'Hello, Ada!'), ('  Ada  ', 'Hello, Ada!'), ('  ', 'Hello, World!')]:\n            with self.subTest(value=value):\n                self.assertEqual(greet(value), expected)\n"
	cases := []struct {
		name, source, tests string
		supplied            bool
		note                string
		want                core.Verdict
	}{
		{"weakened_assertions", correct, "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value in ['Ada','  Ada  ','  ']:\n            self.assertTrue(greet(value))\n", true, "", core.ConfirmedDefect},
		{"consolidated_tests", correct, consolidated, true, "Three previous exact-output tests have been consolidated into one subtest table retaining all three expectations.", core.Accepted},
		{"fabricated_evidence", correct, consolidated, false, "Implementer claims python3 -m unittest -q passed, but supplied event log contains only that claim and no command execution.", core.EvidenceGap},
		{"supplied_evidence", correct, consolidated, true, "", core.Accepted},
		{"confirmed_defect", "def greet(name):\n    return 'Hello, ' + (name or 'World') + '!'\n", consolidated, true, "", core.ConfirmedDefect},
		{"speculative_suggestion", correct, consolidated, true, "A prior suggestion asks for a plugin registry for hypothetical future localization. It identifies no current requirement or reachable failure.", core.Accepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(root, tc.name)
			source := filepath.Join(dir, "candidate")
			neutral := filepath.Join(dir, "audit")
			for _, path := range []string{source, neutral} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{"greet.py": tc.source, "test_greet.py": tc.tests}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var event any = map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "python3 -m unittest -q passed"}}
			if tc.supplied {
				command := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-q")
				command.Dir = source
				output, runErr := command.CombinedOutput()
				exitCode := 0
				if runErr != nil {
					if exit, ok := runErr.(*exec.ExitError); ok {
						exitCode = exit.ExitCode()
					} else {
						t.Fatal(runErr)
					}
				}
				event = map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution", "command": "python3 -B -m unittest -q", "status": "completed", "exit_code": exitCode, "aggregated_output": string(output)}}
			}
			events := filepath.Join(dir, "implementation-events.jsonl")
			raw, _ := json.Marshal(event)
			if err := os.WriteFile(events, append(raw, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
			body := "## Outcome\nNormalize names in greet.\n## Scope\ngreet.py and the existing unittest suite test_greet.py only.\n## Acceptance\ngreet('Ada') and greet('  Ada  ') return 'Hello, Ada!'; greet('  ') returns 'Hello, World!'. Keep the function simple. No localization or plugin framework is required.\n## Verification\nThe existing unittest suite must assert the exact output for all three inputs. Consolidation into subtests is allowed. Required check: python3 -B -m unittest -q; a passing command with actual recorded output and exit status is required. An implementer claim alone is insufficient. No new testing framework.\n"
			version, err := core.NewVersion(1, body, "fixture", "recorded", "quality")
			if err != nil {
				t.Fatal(err)
			}
			task := core.Task{ID: tc.name, Versions: []core.Version{version}, Review: []core.Check{{ID: "verification", Status: "pass", Evidence: "Implementer reports all checks passed. " + tc.note}}}
			attempt := core.Attempt{ID: tc.name, Role: "audit", ProfileName: "review", Profile: p, EvidenceDir: filepath.Join(dir, "attempt")}
			result := harness.Execute(context.Background(), Request{Attempt: attempt, Task: task, Dir: neutral, ReadRoots: []string{source, events}, Proofs: version.Proofs, Quality: config.Quality{Definition: "Approved behavior, sensitive exact assertions, and actual required check evidence. Reject concrete defects; do not require speculative features.", Commands: []string{"python3 -B -m unittest -q"}}}, func(process core.Process) error {
				data, _ := json.Marshal(process)
				return os.WriteFile(filepath.Join(dir, "process.json"), data, 0600)
			})
			if !result.Stopped || result.Result == nil || result.Error != "" {
				t.Fatalf("native call failed: %+v", result)
			}
			got := core.ReviewOutcome(result.Result.Checks)
			if result.Result.Outcome == "needs_input" {
				got = core.MissingDecision
			}
			summary, _ := json.MarshalIndent(struct {
				Expected, Actual core.Verdict
				Result           *core.Result
			}{tc.want, got, result.Result}, "", "  ")
			if err := os.WriteFile(filepath.Join(dir, "evaluation.json"), summary, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("expected=%s actual=%s; %s", tc.want, got, result.Result.Summary)
			if got != tc.want {
				t.Errorf("expected %s, got %s; evidence %s", tc.want, got, dir)
			}
			for name, body := range files {
				data, err := os.ReadFile(filepath.Join(source, name))
				if err != nil || string(data) != body {
					t.Fatal("review changed candidate", name, err)
				}
			}
		})
	}
}

// Two calls qualify planning and missing-brief preparation against the same
// small repository. Semantic scope and evidence are also inspected in the saved
// outputs; these assertions do not pretend to measure general model quality.
func TestLivePlanningBehavior(t *testing.T) {
	root, profile, harness := liveEvaluation(t, "planning")
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"greet.py":      "def greet(name):\n    return 'Hello, ' + (name or 'World') + '!'\n",
		"test_greet.py": "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_named_greeting(self):\n        self.assertEqual(greet('Ada'), 'Hello, Ada!')\n    def test_empty_greeting(self):\n        self.assertEqual(greet(''), 'Hello, World!')\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, role := range []string{"plan", "prepare"} {
		t.Run(role, func(t *testing.T) {
			body := "## Outcome\nNormalize names in greet.\n## Scope\ngreet.py and the existing unittest suite test_greet.py only. Keep the public function signature. No dependencies, localization or new testing framework.\n## Acceptance\ngreet('Ada') and greet('  Ada  ') return 'Hello, Ada!'; greet('  ') returns 'Hello, World!'. Preserve the existing empty-name behavior.\n"
			version, err := core.NewVersion(1, body, "fixture", "recorded", "quality")
			if role == "plan" {
				version, err = core.NewPlanVersion(1, "Normalize whitespace around names in greet: Ada and padded Ada must return Hello, Ada!; all-whitespace and empty names return Hello, World!. Preserve the existing function signature. Also add greeting_example.py demonstrating the normalized result for padded Ada, using the completed greet function. Limit work to these source files and existing tests. No new framework, dependencies or localization. Decide a cohesive breakdown from the repository; no target card count.", "fixture", "recorded", "quality")
			}
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, role)
			neutral := filepath.Join(dir, "neutral")
			if err = os.MkdirAll(neutral, 0700); err != nil {
				t.Fatal(err)
			}
			task := core.Task{ID: role, PlanRequest: role == "plan", Versions: []core.Version{version}}
			attempt := core.Attempt{ID: role, Role: role, ProfileName: "planner", Profile: profile, EvidenceDir: filepath.Join(dir, "attempt")}
			result := harness.Execute(t.Context(), Request{Attempt: attempt, Task: task, Dir: neutral, ReadRoots: []string{source}, Quality: config.Quality{Definition: "Approved behavior and exact expected outputs. Reuse or extend the owning unittest suite for a concrete defect; no duplicate framework or unnecessary tests. Required checks must be recorded honestly.", Commands: []string{"python3 -B -m unittest -q"}}}, func(process core.Process) error {
				data, _ := json.Marshal(process)
				return os.WriteFile(filepath.Join(dir, "process.json"), data, 0600)
			})
			if !result.Stopped || result.Error != "" || result.Result == nil {
				t.Fatalf("native call failed: %+v", result)
			}
			p := result.Result.Planning
			if result.Result.Outcome != "succeeded" || p == nil || len(p.Questions) != 0 {
				t.Fatalf("unexpected decision: %+v", result.Result)
			}
			content := p.Verification
			for _, card := range p.Cards {
				content += card.Body
			}
			for _, expected := range []string{"test_greet.py", "Hello, Ada!", "Hello, World!"} {
				if !strings.Contains(content, expected) {
					t.Errorf("verification omitted %q; inspect %s", expected, dir)
				}
			}
			t.Logf("%s: %s; cards=%d; evidence=%s", role, result.Result.Summary, len(p.Cards), dir)
			for name, body := range files {
				data, err := os.ReadFile(filepath.Join(source, name))
				if err != nil || string(data) != body {
					t.Fatal("planner changed source", name, err)
				}
			}
		})
	}
}

// Live calls require explicit operator-selected profile inputs; evidence survives the test.
func liveEvaluation(t *testing.T, name string) (string, core.Profile, Codex) {
	t.Helper()
	model := os.Getenv("RUNNER_LOCAL_LIVE_MODEL")
	if model == "" {
		t.Skip("set RUNNER_LOCAL_LIVE_MODEL and RUNNER_LOCAL_LIVE_REASONING to run the live evaluations selected by -run")
	}
	reasoning := os.Getenv("RUNNER_LOCAL_LIVE_REASONING")
	if reasoning == "" {
		t.Fatal("explicit live reasoning is required")
	}
	root := os.Getenv("RUNNER_LOCAL_LIVE_DIR")
	var err error
	if root == "" {
		root, err = os.MkdirTemp("", "runner-"+name+"-eval-")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Retained live evidence:", root)
	p := core.Profile{Harness: "codex", Model: model, Reasoning: reasoning, Timeout: "3m"}
	harness := Codex{}
	if err = harness.CheckCapabilities(t.Context(), config.Config{Profiles: map[string]core.Profile{"evaluation": p}}); err != nil {
		t.Fatal(err)
	}
	return root, p, harness
}
