package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
// Selected paired cases qualify replacement reviewer behavior.
func TestLiveReadyReviewBehavior(t *testing.T) {
	root, p, harness := liveEvaluation(t, "ready")
	const correct = "def greet(name):\n    return 'Hello, ' + (name.strip() or 'World') + '!'\n"
	const consolidated = "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value, expected in [('Ada', 'Hello, Ada!'), ('  Ada  ', 'Hello, Ada!'), ('  ', 'Hello, World!')]:\n            with self.subTest(value=value):\n                self.assertEqual(greet(value), expected)\n"
	cases := []struct {
		name, source, tests string
		supplied            bool
		note                string
		want                core.Verdict
		exception           bool
	}{
		{"weakened_assertions", correct, "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value in ['Ada','  Ada  ','  ']:\n            self.assertTrue(greet(value))\n", true, "", core.ConfirmedDefect, false},
		{"consolidated_tests", correct, consolidated, true, "Three previous exact-output tests have been consolidated into one subtest table retaining all three expectations.", core.Accepted, false},
		{"fabricated_evidence", correct, consolidated, false, "Implementer claims python3 -m unittest -q passed, but supplied event log contains only that claim and no command execution.", core.EvidenceGap, false},
		{"supplied_evidence", correct, consolidated, true, "", core.Accepted, false},
		{"confirmed_defect", "def greet(name):\n    return 'Hello, ' + (name or 'World') + '!'\n", consolidated, true, "", core.ConfirmedDefect, false},
		{"speculative_suggestion", correct, consolidated, true, "A prior suggestion asks for a plugin registry for hypothetical future localization. It identifies no current requirement or reachable failure.", core.Accepted, false},
		{"known_baseline_exception", correct, consolidated, true, "Known legacy check failure is explicitly excepted; task assertions still must pass.", core.Accepted, true},
		{"exception_cannot_waive_task_defect", "def greet(name):\n    return 'Hello, ' + (name or 'World') + '!'\n", consolidated, true, "Only the legacy check is excepted, never this task's required greeting behavior.", core.ConfirmedDefect, true},
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
			if tc.exception {
				files["legacy.txt"] = "known unrelated failure\n"
			}
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
			request := Request{Attempt: attempt, Task: task, Dir: neutral, ReadRoots: []string{source, events}, Proofs: version.Proofs, Quality: config.Quality{Definition: "Approved behavior, sensitive exact assertions, and actual required check evidence. Reject concrete defects; do not require speculative features.", Checks: []config.QualityCheck{{ID: "unit", Command: "python3 -B -m unittest -q", Directory: ".", Prerequisites: []string{"python3"}, Timeout: "1m"}}}}
			if tc.exception {
				log := filepath.Join(dir, "legacy-check.log")
				output := []byte("known unrelated failure\n")
				if err := os.WriteFile(log, output, 0600); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(output)
				observation := core.CheckObservation{CheckID: "legacy", Status: "failed", ExitCode: 1, OutputDigest: fmt.Sprintf("%x", digest), Evidence: log, Stopped: true}
				request.Quality.Checks = append(request.Quality.Checks, config.QualityCheck{ID: "legacy", Command: "cat legacy.txt; exit 1", Directory: ".", Prerequisites: []string{"sh", "cat"}, Timeout: "1m"})
				request.Quality.Baseline = []core.CheckObservation{observation}
				request.Quality.Exceptions = []config.BaselineException{{CheckID: "legacy", Rationale: "Unrelated pre-existing failure; scoped file unchanged", Files: map[string]string{"legacy.txt": fmt.Sprintf("600:%x", digest)}, ExitCode: 1, OutputDigest: observation.OutputDigest}}
				request.QualityEvidence = []core.CheckObservation{observation}
				request.ReadRoots = append(request.ReadRoots, log)
			}
			result := harness.Execute(context.Background(), request, func(process core.Process) error {
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
			result := harness.Execute(t.Context(), Request{Attempt: attempt, Task: task, Dir: neutral, ReadRoots: []string{source}, Quality: config.Quality{Definition: "Approved behavior and exact expected outputs. Reuse or extend the owning unittest suite for a concrete defect; no duplicate framework or unnecessary tests. Required checks must be recorded honestly.", Checks: []config.QualityCheck{{ID: "unit", Command: "python3 -B -m unittest -q", Directory: ".", Prerequisites: []string{"python3"}, Timeout: "1m"}}}}, func(process core.Process) error {
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

// Qualify the CI context at the existing native execution boundary: repair a
// reproducible defect, but do not invent one from a failed job name alone.
func TestLiveCIRepairBehavior(t *testing.T) {
	root, profile, harness := liveEvaluation(t, "ci-repair")
	const tests = "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value, expected in [('Ada', 'Hello, Ada!'), ('  Ada  ', 'Hello, Ada!'), ('  ', 'Hello, World!')]:\n            with self.subTest(value=value):\n                self.assertEqual(greet(value), expected)\n"
	for _, name := range []string{"reproducible", "insufficient", "review"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			source := "def greet(name):\n    return 'Hello, ' + (name.strip() or 'World') + '!'\n"
			if name == "reproducible" {
				source = "def greet(name):\n    return 'Hello, ' + (name or 'World') + '!'\n"
			}
			for path, body := range map[string]string{"greet.py": source, "test_greet.py": tests} {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body := "## Outcome\nNormalize names in greet.\n## Scope\ngreet.py and existing test_greet.py only. No new files, dependencies or framework.\n## Acceptance\ngreet('Ada') and greet('  Ada  ') return 'Hello, Ada!'; greet('  ') returns 'Hello, World!'.\n## Verification\nUse existing exact-output unittest cases in test_greet.py. Run python3 -B -m unittest -q. Preserve all expectations; no duplicate test suite.\n"
			version, err := core.NewVersion(1, body, "fixture", "recorded", "quality")
			if err != nil {
				t.Fatal(err)
			}
			job := "python-unit"
			if name == "insufficient" {
				job = "windows-only-integration"
			}
			failure := &core.CIRepair{PublicationID: "fixture", Assessment: core.CIAssessment{State: "failed", Detail: "Required hosted job failed; no remote logs available. Diagnose locally; missing evidence must remain explicit.", Results: []core.CIResult{{RemoteCheck: core.RemoteCheck{CIRequirement: core.CIRequirement{Kind: "check", Name: job, AppID: 15368}, Status: "COMPLETED", Conclusion: "FAILURE"}, State: "failed"}}}}
			a := core.Attempt{ID: name, Role: "implementer", Kind: "ci_repair", Repair: 1, ProfileName: "build", Profile: profile, CIRepair: failure, EvidenceDir: filepath.Join(root, name+"-evidence")}
			r := Request{Attempt: a, Task: core.Task{ID: name, Versions: []core.Version{version}, CIRepair: failure}, Dir: dir, Proofs: version.Proofs, Quality: config.Quality{Definition: "Approved behavior, exact assertions and honest actual test evidence; do not infer a code defect from unavailable hosted logs.", Checks: []config.QualityCheck{{ID: "unit", Command: "python3 -B -m unittest -q", Directory: ".", Prerequisites: []string{"python3"}, Timeout: "1m"}}}}
			if name == "review" {
				command := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-q")
				command.Dir = dir
				output, err := command.CombinedOutput()
				if err != nil {
					t.Fatal("review fixture check failed", err)
				}
				events := filepath.Join(root, "review-check-events.jsonl")
				data, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "command_execution", "command": "python3 -B -m unittest -q", "status": "completed", "exit_code": 0, "aggregated_output": string(output)}})
				if err = os.WriteFile(events, data, 0600); err != nil {
					t.Fatal(err)
				}
				r.Attempt.Role = "audit"
				r.ReadRoots = []string{dir, events}
				r.Dir = filepath.Join(root, "review-neutral")
				if err = os.MkdirAll(r.Dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result := harness.Execute(t.Context(), r, func(p core.Process) error {
				data, _ := json.Marshal(p)
				return os.WriteFile(filepath.Join(root, name+"-process.json"), data, 0600)
			})
			data, _ := json.MarshalIndent(result, "", "  ")
			if err = os.WriteFile(filepath.Join(root, name+"-result.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if !result.Stopped || result.Error != "" || result.Result == nil {
				t.Fatalf("native repair failed: %+v", result)
			}
			verdict := core.ReviewOutcome(result.Result.Checks)
			if name != "insufficient" {
				if result.Result.Outcome != "succeeded" || verdict != core.Accepted {
					t.Fatal("reproducible repair not completed", result.Result)
				}
			} else {
				if result.Result.Outcome == "succeeded" && verdict == core.Accepted {
					t.Fatal("unexplained CI treated as fixed")
				}
				actual, err := os.ReadFile(filepath.Join(dir, "greet.py"))
				if err != nil || string(actual) != source {
					t.Fatal("speculative edit", err)
				}
			}
			if name == "review" {
				actual, err := os.ReadFile(filepath.Join(dir, "greet.py"))
				if err != nil || string(actual) != source {
					t.Fatal("review changed source", err)
				}
			}
			actual, err := os.ReadFile(filepath.Join(dir, "test_greet.py"))
			if err != nil || string(actual) != tests {
				t.Fatal("sufficient owning tests were changed", err)
			}
			command := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-q")
			command.Dir = dir
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("owning tests failed: %s %v", output, err)
			}
			t.Logf("%s: %s; evidence=%s", name, result.Result.Summary, root)
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

func TestLiveConflictRepairBehavior(t *testing.T) {
	root, profile, harness := liveEvaluation(t, "conflict-repair")
	const tests = "import unittest\nfrom greet import greet\nclass GreetingTests(unittest.TestCase):\n    def test_required_inputs(self):\n        for value, expected in [('Ada', 'Welcome, Ada!'), ('  Ada  ', 'Welcome, Ada!'), ('  ', 'Welcome, World!')]:\n            with self.subTest(value=value):\n                self.assertEqual(greet(value), expected)\n"
	const source = "def greet(name):\n<<<<<<< target-base\n    return 'Welcome, ' + (name or 'World') + '!'\n=======\n    return 'Hello, ' + (name.strip() or 'World') + '!'\n>>>>>>> prior-candidate\n"
	for _, name := range []string{"resolvable", "decision"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"greet.py": source}
			acceptance := "Preserve the base's Welcome greeting and the task's whitespace normalization. greet('Ada') and greet('  Ada  ') return 'Welcome, Ada!'; blank returns 'Welcome, World!'."
			verification := "Reuse existing exact-output test_greet.py; run python3 -B -m unittest -q. The suite already covers every required input; no new tests needed."
			if name == "resolvable" {
				files["test_greet.py"] = tests
			} else {
				acceptance = "Normalize whitespace and preserve the chosen greeting. The base switched Hello to Welcome, but which product greeting is intended has not been decided. Neither side has authority to settle that choice."
				verification = "Inspect the conflicting source. No test specifies the intended greeting. Report the missing product choice before editing or inventing tests."
			}
			for path, body := range files {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			version, err := core.NewVersion(1, "## Outcome\nNormalize greet names while integrating the current base.\n## Scope\ngreet.py and existing tests only. No new files or dependencies.\n## Acceptance\n"+acceptance+"\n## Verification\n"+verification+"\n", "fixture", "recorded", "quality")
			if err != nil {
				t.Fatal(err)
			}
			conflict := &core.ConflictRepair{Candidate: core.Candidate{Commit: "prior-candidate"}, Target: core.BaseBinding{Commit: "target-base", Cycle: 1}, Paths: []string{"greet.py"}}
			request := Request{Attempt: core.Attempt{ID: name, Role: "implementer", Kind: "conflict_repair", Repair: 1, Base: "target-base", ProfileName: "build", Profile: profile, ConflictRepair: conflict, EvidenceDir: filepath.Join(root, name+"-evidence")}, Task: core.Task{ID: name, Versions: []core.Version{version}, ConflictRepair: conflict}, Dir: dir, Proofs: version.Proofs, Quality: config.Quality{Definition: "Preserve both-side required behavior, sufficient owning tests and honest evidence. Missing product choices require needs_input."}}
			result := harness.Execute(t.Context(), request, func(p core.Process) error {
				data, _ := json.Marshal(p)
				return os.WriteFile(filepath.Join(root, name+"-process.json"), data, 0600)
			})
			data, _ := json.MarshalIndent(result, "", "  ")
			if err = os.WriteFile(filepath.Join(root, name+"-result.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			if !result.Stopped || result.Error != "" || result.Result == nil {
				t.Fatalf("native call failed: %+v", result)
			}
			if name == "decision" {
				if result.Result.Outcome != "needs_input" {
					t.Fatal("missing choice invented", result.Result)
				}
				actual, err := os.ReadFile(filepath.Join(dir, "greet.py"))
				if err != nil || string(actual) != source {
					t.Fatal("speculative resolution", err)
				}
			} else {
				if result.Result.Outcome != "succeeded" || core.ReviewOutcome(result.Result.Checks) != core.Accepted {
					t.Fatal("conflict not resolved", result.Result)
				}
				actual, err := os.ReadFile(filepath.Join(dir, "test_greet.py"))
				if err != nil || string(actual) != tests {
					t.Fatal("sufficient owning tests changed", err)
				}
				cmd := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-q")
				cmd.Dir = dir
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatal("both-side behavior failed", string(output), err)
				}
				// Independently audit the resolution using the real implementation log.
				request.ReadRoots = []string{dir, filepath.Join(request.Attempt.EvidenceDir, "events.jsonl")}
				request.Task.Review = result.Result.Checks
				request.Attempt.Role, request.Attempt.ID = "audit", "resolution-audit"
				request.Attempt.EvidenceDir = filepath.Join(root, "review-evidence")
				request.Dir = filepath.Join(root, "review-neutral")
				if err = os.MkdirAll(request.Dir, 0700); err != nil {
					t.Fatal(err)
				}
				before, err := os.ReadFile(filepath.Join(dir, "greet.py"))
				if err != nil {
					t.Fatal(err)
				}
				review := harness.Execute(t.Context(), request, func(p core.Process) error {
					data, _ := json.Marshal(p)
					return os.WriteFile(filepath.Join(root, "review-process.json"), data, 0600)
				})
				data, _ = json.MarshalIndent(review, "", "  ")
				if err = os.WriteFile(filepath.Join(root, "review-result.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				if !review.Stopped || review.Error != "" || review.Result == nil || review.Result.Outcome != "succeeded" || core.ReviewOutcome(review.Result.Checks) != core.Accepted {
					t.Fatalf("resolution audit: %+v", review)
				}
				after, err := os.ReadFile(filepath.Join(dir, "greet.py"))
				if err != nil || string(before) != string(after) {
					t.Fatal("audit edited resolution", err)
				}
			}
			t.Logf("%s: %s; evidence=%s", name, result.Result.Summary, root)
		})
	}
}

// Individual child checks pass in both cases. The parent must assess whether
// the application actually consumes their combined behavior.
func TestLiveCombinedOutcomeBehavior(t *testing.T) {
	root, p, harness := liveEvaluation(t, "combined-outcome")
	for _, broken := range []bool{true, false} {
		name := "correct"
		if broken {
			name = "missing-consumer"
		}
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			source := filepath.Join(dir, "source")
			neutral := filepath.Join(dir, "audit")
			for _, path := range []string{source, neutral} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			app := "from greet import greet\nprint(greet('  Ada  '))\n"
			if broken {
				app = "from greet import greet\nprint('Hello,   Ada  !')\n"
			}
			files := map[string]string{"greet.py": "def greet(name):\n    return 'Hello, ' + (name.strip() or 'World') + '!'\n", "example.py": app, "test_greet.py": "import unittest\nfrom greet import greet\nclass Tests(unittest.TestCase):\n    def test_names(self):\n        for name, expected in [('Ada', 'Hello, Ada!'), ('  Ada  ', 'Hello, Ada!'), (' ', 'Hello, World!')]:\n            self.assertEqual(greet(name), expected)\n"}
			for name, body := range files {
				if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "python3", "-B", "-m", "unittest", "-q")
			cmd.Dir = source
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(dir, "combined-check.log")
			if err = os.WriteFile(log, out, 0600); err != nil {
				t.Fatal(err)
			}
			body := "## Outcome\nNormalize names and demonstrate the normalized result in example.py.\n## Scope\ngreet.py, example.py and existing test_greet.py only. No new framework.\n## Acceptance\ngreet strips names and substitutes World for whitespace. Executing example.py must print exactly Hello, Ada! and a newline, using greet on padded Ada. Preserve child greeting assertions.\n## Verification\nRun existing unittest checks. Inspect the combined application consumption at the example.py boundary: padded Ada must use greet and produce the exact stated output. Existing evidence is sufficient when it establishes both guarantees; don't require a new suite merely because two cards were combined.\n"
			v, err := core.NewVersion(1, body, "fixture", "local", "quality")
			if err != nil {
				t.Fatal(err)
			}
			task := core.Task{ID: name, PlanRequest: true, Versions: []core.Version{v}, Proposal: &core.PlanningResult{Body: body}, Release: &core.PlanRelease{Preview: "released", Children: []string{"greeting", "example"}}}
			history := []core.Attempt{{ID: "greeting-audit", TaskID: "greeting", Role: "audit", Candidate: &core.Candidate{Commit: "historical-child-head"}, Result: &core.Result{Outcome: "succeeded", Summary: "Individual greeting assertions passed"}}, {ID: "example-audit", TaskID: "example", Role: "audit", Candidate: &core.Candidate{Commit: "historical-example-head"}, Result: &core.Result{Outcome: "succeeded", Summary: "Example file exists; child reviewer reported success"}}}
			r := Request{Attempt: core.Attempt{ID: name, Role: "audit", Profile: p, EvidenceDir: filepath.Join(dir, "attempt")}, Task: task, Proofs: v.Proofs, Dir: neutral, ReadRoots: []string{source, log}, Quality: config.Quality{Definition: "Entire released outcome established, with preserved child guarantees", Checks: []config.QualityCheck{{ID: "unit", At: "completion", Command: "python3 -B -m unittest -q", Directory: ".", Prerequisites: []string{"python3"}, Timeout: "1m"}}}, QualityEvidence: []core.CheckObservation{{CheckID: "unit", Status: "passed", Stopped: true, Evidence: log}}, Plan: &PlanContext{OriginalRequest: body, SharedBody: body, Evidence: history}}
			got := harness.Execute(t.Context(), r, func(core.Process) error { return nil })
			if got.Error != "" || !got.Stopped || got.Result == nil {
				t.Fatal(got)
			}
			want := core.Accepted
			if broken {
				want = core.ConfirmedDefect
			}
			actual := core.ReviewOutcome(got.Result.Checks)
			data, _ := json.MarshalIndent(map[string]any{"expected": want, "actual": actual, "result": got.Result, "model": p}, "", "  ")
			if err = os.WriteFile(filepath.Join(dir, "evaluation.json"), data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("expected=%s actual=%s; %s", want, actual, got.Result.Summary)
			if actual != want {
				t.Fatalf("want %s got %s; evidence %s", want, actual, dir)
			}
			for name, body := range files {
				data, err := os.ReadFile(filepath.Join(source, name))
				if err != nil || string(data) != body {
					t.Fatal("audit altered source", name, err)
				}
			}
		})
	}
}
