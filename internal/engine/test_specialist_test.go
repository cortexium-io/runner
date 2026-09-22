package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
)

const testContributionRequest = `{"outcome":"test_requested","summary":"Implementation retained; request a focused selection regression.","work_done":["Retained the approved selection change."],"verification":["Inspected current selection checks; no check yet for the new boundary."],"blockers":[],"test_request":{"criterion_indices":[0],"reason":"Protect the changed selection boundary at the smallest faithful level.","existing_checks":["Current selection check inspected."],"paths":["selection_test.go"]}}`
const testContributionResult = `{"outcome":"succeeded","summary":"Added the focused selection regression.","work_done":["Added one test in the authorized file."],"verification":["Source inspection only; original implementer must run the focused check."],"blockers":[]}`

func testSpecialistFixture(t *testing.T) (*Engine, *repairImplementationRunner, github.WorkItem, config.Config) {
	t.Helper()
	_, run, item, cfg := implementationRepairFixture(t)
	cfg.TestSpecialist = &config.TestSpecialistConfig{Enabled: true, AllowedPaths: []string{"selection_test.go"}}
	service, err := New(cfg, run)
	if err != nil {
		t.Fatal(err)
	}
	run.responses = []string{testContributionRequest, testContributionResult}
	run.inspect = func(dir string) error {
		if run.calls == 2 {
			return os.WriteFile(filepath.Join(dir, "selection_test.go"), []byte("package selection\n// focused test fixture\n"), 0o644)
		}
		return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("retained implementation\n"), 0o644)
	}
	return service, run, item, cfg
}

func TestTestSpecialistImplementationRetainsDeadlineCapacityAndIndependentRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "then-repair"}[repair], func(t *testing.T) {
			service, run, item, _ := testSpecialistFixture(t)
			if repair {
				run.responses = append(run.responses, unfinishedImplementation)
			}
			run.stdout = `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}}` + "\n"
			var original, private string
			run.beforeReturn = func(ctx context.Context, dir string, call int) error {
				switch call {
				case 1:
					original = dir
				case 2:
					private = dir
					if private == original {
						t.Fatal("specialist reused writable original checkout")
					}
					record, err := service.readImplementationCheckpoint(item.ID)
					if err != nil || record == nil || record.Specialist == nil || record.Specialist.Phase != specialistStarted || record.CorrectionUsed {
						t.Fatalf("handoff not spent separately before launch: %#v %v", record, err)
					}
					invocation := strings.Join(run.args[:len(run.args)-1], " ")
					if !slices.Contains(run.args, "--ignore-user-config") || !strings.Contains(invocation, `default_permissions="runner_implementation_write"`) || !strings.Contains(invocation, "mcp_servers={}") || strings.Contains(invocation, "danger-full-access") || strings.Contains(invocation, original) {
						t.Fatalf("specialist widened containment: %s", invocation)
					}
				case 3:
					if dir != original {
						t.Fatal("continuation lost retained workspace")
					}
					if _, err := os.Stat(filepath.Join(original, "selection_test.go")); err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(private); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("private copy not cleaned: %v", err)
					}
					record, err := service.readImplementationCheckpoint(item.ID)
					if err != nil || record.Specialist.Phase != specialistResuming || record.CorrectionUsed {
						t.Fatalf("continuation state: %#v %v", record, err)
					}
				case 4:
					record, err := service.readImplementationCheckpoint(item.ID)
					if err != nil || record.Specialist.Phase != specialistFinished || !record.CorrectionUsed {
						t.Fatalf("independent repair state: %#v %v", record, err)
					}
				}
				return nil
			}
			history := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "history.jsonl"))
			service.SetMetricsObserver(history.Append)
			result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			want := 3
			if repair {
				want = 4
			}
			if result.Outcome != execution.OutcomeSucceeded || run.calls != want || run.project.status != "Agent QA" {
				t.Fatalf("handoff failed: calls=%d result=%#v", run.calls, result)
			}
			if result.Usage.InputTokens != int64(want*100) || result.Usage.OutputTokens != int64(want*30) {
				t.Fatalf("usage lost/doubled: %#v", result.Usage)
			}
			for _, deadline := range run.deadlines {
				if deadline.IsZero() || !deadline.Equal(run.deadlines[0]) {
					t.Fatalf("renewed deadline: %v", run.deadlines)
				}
			}
			if !strings.Contains(run.prompts[2], "untrusted historical evidence") || strings.Contains(run.prompts[2], "Runner permits one explicitly requested") {
				t.Fatal("continuation lost provenance or regained capability")
			}
			stored, err := history.Read()
			if err != nil || len(stored.Attempts) != 1 {
				t.Fatalf("history: %#v %v", stored, err)
			}
			count := 0
			for _, stage := range stored.Attempts[0].Stages {
				if stage.Name == metrics.StageTestSpecialist {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("specialist not one accounted stage: %d", count)
			}
		})
	}
}

func TestTestSpecialistImplementationNoChangeAndRefusedDelta(t *testing.T) {
	for _, mutation := range []string{"no-change", "production", "control", "source", "second-request", "cancel"} {
		t.Run(mutation, func(t *testing.T) {
			service, run, item, _ := testSpecialistFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			original := ""
			run.inspect = func(dir string) error {
				if run.calls == 1 {
					original = dir
					return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("retained\n"), 0o644)
				}
				if run.calls != 2 {
					return nil
				}
				switch mutation {
				case "production":
					return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("unauthorized"), 0o644)
				case "control":
					return os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("untrusted instructions"), 0o644)
				case "source":
					return os.WriteFile(filepath.Join(original, "selection.txt"), []byte("operator edit"), 0o644)
				case "cancel":
					cancel()
					return context.Canceled
				}
				return nil
			}
			if mutation == "second-request" {
				run.responses = append(run.responses, testContributionRequest)
			}
			result := service.executeItem(ctx, admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			if mutation == "no-change" {
				if result.Outcome != execution.OutcomeSucceeded || run.calls != 3 {
					t.Fatalf("no-change: %#v calls=%d", result, run.calls)
				}
			} else if result.Outcome != execution.OutcomeBlocked || run.calls > 3 || result.RetryDisposition == "automatic" {
				t.Fatalf("unsafe handoff continued: %#v calls=%d", result, run.calls)
			}
			if mutation != "source" {
				content, err := os.ReadFile(filepath.Join(original, "selection.txt"))
				if err != nil || string(content) != "retained\n" {
					t.Fatalf("specialist changed retained source: %q %v", content, err)
				}
			}
		})
	}
}

func TestTestSpecialistRestartUsesObservedResultWithoutRepeatingSpentCalls(t *testing.T) {
	for _, phase := range []string{specialistStarted, specialistResultReady, specialistApplying, specialistApplied, specialistResuming, "partial-application"} {
		t.Run(phase, func(t *testing.T) {
			service, run, item, cfg := testSpecialistFixture(t)
			run.stdout = `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":30}}` + "\n"
			crashPhase := phase
			if phase == specialistResultReady || phase == "partial-application" {
				crashPhase = specialistApplying
			}
			if phase == specialistApplied {
				crashPhase = specialistResuming
			}
			if phase == specialistStarted {
				run.beforeReturn = func(_ context.Context, _ string, call int) error {
					if call == 2 {
						panic("simulated specialist exit")
					}
					return nil
				}
			} else {
				run.afterCommand = func(_ string, _ []string) error {
					record, err := service.readImplementationCheckpoint(item.ID)
					if err != nil {
						return err
					}
					if record != nil && record.Specialist != nil && record.Specialist.Phase == crashPhase {
						panic("simulated specialist exit")
					}
					return nil
				}
			}
			func() {
				defer func() {
					if recover() != "simulated specialist exit" {
						t.Fatal("fixture did not interrupt the selected checkpoint boundary")
					}
				}()
				service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			}()
			run.afterCommand, run.beforeReturn = nil, nil
			if run.calls != 2 {
				t.Fatalf("interruption did not follow one specialist: %d", run.calls)
			}
			record, err := service.readImplementationCheckpoint(item.ID)
			if err != nil || record == nil || record.Specialist == nil {
				t.Fatalf("missing spent checkpoint: %#v %v", record, err)
			}
			if phase == specialistResultReady || phase == specialistApplied {
				// These durable writes precede the next phase without an intervening
				// subprocess. Restore the exact prior phase to exercise restart at
				// that filesystem boundary, retaining its observed result/delta.
				record.Specialist.Phase = phase
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(service.implementationCheckpointPath(item.ID), encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "partial-application" {
				if err := os.WriteFile(filepath.Join(record.WorktreePath, "selection_test.go"), []byte("partial retained apply\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			restarted, err := New(cfg, run)
			if err != nil {
				t.Fatal(err)
			}
			result := restarted.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, restarted.source, github.WorkItem{ID: item.ID}), event: restarted.newItemAttempt(item)})
			if phase == specialistResultReady || phase == specialistApplied {
				if result.Outcome != execution.OutcomeSucceeded || run.calls != 3 || result.Usage.InputTokens != 100 || !result.ResumedCheckpoint {
					t.Fatalf("protected result not resumed exactly once: calls=%d result=%#v", run.calls, result)
				}
				if !run.deadlines[2].Equal(run.deadlines[0]) {
					t.Fatal("restart renewed original deadline")
				}
			} else if result.Outcome != execution.OutcomeBlocked || run.calls != 2 || result.RetryDisposition != "manual" {
				t.Fatalf("uncertain phase renewed spent call: calls=%d result=%#v", run.calls, result)
			}
		})
	}
}

func TestTestSpecialistFailureRetainsPartialAndUnavailableUsage(t *testing.T) {
	for _, reported := range []bool{true, false} {
		t.Run(map[bool]string{true: "partial", false: "unavailable"}[reported], func(t *testing.T) {
			service, run, item, _ := testSpecialistFixture(t)
			run.stdout = `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":30}}` + "\n"
			run.beforeReturn = func(_ context.Context, _ string, call int) error {
				if call == 1 {
					run.stdout = ""
					if reported {
						run.stdout = `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":12,"output_tokens":3}}}}` + "\n"
					}
					return nil
				}
				return context.DeadlineExceeded
			}
			history := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "history.jsonl"))
			service.SetMetricsObserver(history.Append)
			result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			wantInput := int64(100)
			if reported {
				wantInput += 12
			}
			if result.Outcome != execution.OutcomeBlocked || result.FailureClass != "timeout" || result.RetryDisposition != "manual" || run.calls != 2 || result.Usage.InputTokens != wantInput || result.Usage.Coverage != metrics.UsagePartial {
				t.Fatalf("failure usage/deadline renewed: %#v calls%d", result, run.calls)
			}
			if _, err := os.Stat(filepath.Join(result.WorktreePath, "selection_test.go")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed contribution applied: %v", err)
			}
			stored, err := history.Read()
			if err != nil || len(stored.Attempts) != 1 {
				t.Fatal(err)
			}
			found := false
			for _, stage := range stored.Attempts[0].Stages {
				if stage.Name == metrics.StageTestSpecialist {
					found = true
					want := metrics.UsageUnavailable
					if reported {
						want = metrics.UsagePartial
					}
					if stage.Usage.Coverage != want {
						t.Fatalf("specialist missing usage became zero/complete: %#v", stage.Usage)
					}
				}
			}
			if !found {
				t.Fatal("missing specialist stage")
			}
		})
	}
}

func TestTestSpecialistAfterRepairCannotRenewEitherAllowance(t *testing.T) {
	service, run, item, _ := testSpecialistFixture(t)
	run.responses = []string{unfinishedImplementation, testContributionRequest, testContributionResult, unfinishedImplementation}
	run.inspect = func(dir string) error {
		if run.calls == 3 {
			return os.WriteFile(filepath.Join(dir, "selection_test.go"), []byte("package selection\n"), 0o644)
		}
		return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("retained\n"), 0o644)
	}
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if run.calls != 4 || result.Outcome != execution.OutcomeBlocked || result.FailureClass != "repair_exhausted" || result.RetryDisposition != "manual" {
		t.Fatalf("handoff renewed repair: calls%d %#v", run.calls, result)
	}
	for _, deadline := range run.deadlines {
		if !deadline.Equal(run.deadlines[0]) {
			t.Fatal("handoff after repair renewed deadline")
		}
	}
}

func TestTestSpecialistRestartAfterRepairRestoresExactContinuation(t *testing.T) {
	for _, recovery := range []struct {
		phase     string
		candidate bool
	}{
		{specialistResultReady, false}, {specialistApplied, false},
		{specialistResultReady, true}, {specialistApplied, true},
	} {
		completions := []string{"complete", "repair-still-spent"}
		if recovery.phase == specialistResultReady && !recovery.candidate {
			completions = append(completions, "altered-correction-context", "missing-correction-context")
		}
		for _, completion := range completions {
			finalRepair := completion == "repair-still-spent"
			phase := recovery.phase
			name := map[bool]string{false: "implementation-repair/", true: "candidate-correction/"}[recovery.candidate] + phase + "/" + completion
			t.Run(name, func(t *testing.T) {
				service, run, item, cfg := testSpecialistFixture(t)
				run.responses = []string{unfinishedImplementation, testContributionRequest, testContributionResult}
				if recovery.candidate {
					run.responses[0] = "" // Successful model result; real Git rejects the candidate.
				}
				if finalRepair {
					run.responses = append(run.responses, unfinishedImplementation)
				}
				run.stdout = `{"type":"turn.completed","usage":{"input_tokens":100,"output_tokens":30}}` + "\n"
				run.inspect = func(dir string) error {
					if run.calls == 3 {
						return os.WriteFile(filepath.Join(dir, "selection_test.go"), []byte("package selection\n"), 0o644)
					}
					if recovery.candidate && run.calls == 1 {
						return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("bad formatting  \n"), 0o644)
					}
					return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("retained\n"), 0o644)
				}
				nextPhase := specialistApplying
				if phase == specialistApplied {
					nextPhase = specialistResuming
				}
				run.afterCommand = func(string, []string) error {
					record, err := service.readImplementationCheckpoint(item.ID)
					if err != nil {
						return err
					}
					if record != nil && record.Specialist != nil && record.Specialist.Phase == nextPhase {
						panic("simulated exit after repair and specialist")
					}
					return nil
				}
				func() {
					defer func() {
						if recover() != "simulated exit after repair and specialist" {
							t.Fatal("fixture missed the recovery boundary")
						}
					}()
					service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
				}()
				run.afterCommand = nil
				record, err := service.readImplementationCheckpoint(item.ID)
				if err != nil || record == nil || record.Specialist == nil || !record.CorrectionUsed || run.calls != 3 {
					t.Fatalf("missing spent repair/specialist: %#v %v calls%d", record, err, run.calls)
				}
				// Exercise the immediately prior durable write, retaining the actual
				// observed result, candidate, bindings, usage and original deadline.
				record.Specialist.Phase = phase
				wantRefusal := ""
				switch completion {
				case "altered-correction-context":
					record.Specialist.CorrectionContext += "\nChanged historical correction."
					wantRefusal = "exact assignment changed"
				case "missing-correction-context":
					record.Specialist.CorrectionContext = ""
					wantRefusal = "correction context does not match"
				}
				encoded, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(service.implementationCheckpointPath(item.ID), encoded, 0o600); err != nil {
					t.Fatal(err)
				}
				run.beforeReturn = func(_ context.Context, dir string, call int) error {
					if wantRefusal != "" {
						t.Fatal("changed invocation context admitted a model call")
					}
					if call != 4 {
						t.Fatalf("replayed repair or specialist call: %d", call)
					}
					retained, err := service.readImplementationCheckpoint(item.ID)
					if err != nil || retained == nil || !retained.CorrectionUsed || retained.Specialist.Phase != specialistResuming {
						t.Fatalf("lost spent allowances: %#v %v", retained, err)
					}
					if _, err := os.Stat(filepath.Join(dir, "selection_test.go")); err != nil {
						t.Fatal(err)
					}
					for _, expected := range map[bool][]string{
						false: {"selection.spec.ts:42", "final automatic corrective pass"},
						true:  {"Runner candidate correction", "trailing whitespace", "BEGIN PRE-CORRECTION RESULT"},
					}[recovery.candidate] {
						if !strings.Contains(run.prompts[3], expected) {
							t.Fatalf("continuation lost its exact correction guidance/evidence: %s", expected)
						}
					}
					return nil
				}
				restarted, err := New(cfg, run)
				if err != nil {
					t.Fatal(err)
				}
				result := restarted.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, restarted.source, github.WorkItem{ID: item.ID}), event: restarted.newItemAttempt(item)})
				if wantRefusal != "" {
					if run.calls != 3 || result.Outcome != execution.OutcomeBlocked || result.RetryDisposition != "manual" || !strings.Contains(result.Error, wantRefusal) || result.Usage.InputTokens != 0 || result.Usage.Available {
						t.Fatalf("changed context was not refused before execution: calls%d %#v", run.calls, result)
					}
					return
				}
				if run.calls != 4 || result.Usage.InputTokens != 100 || result.Usage.OutputTokens != 30 || !result.ResumedCheckpoint {
					t.Fatalf("recovery replayed/lost execution or historical usage: calls%d %#v", run.calls, result)
				}
				for _, deadline := range run.deadlines {
					if !deadline.Equal(record.ExecutionDeadline) {
						t.Fatal("recovery renewed original deadline")
					}
				}
				if finalRepair {
					if result.Outcome != execution.OutcomeBlocked || result.FailureClass != "repair_exhausted" || result.RetryDisposition != "manual" {
						t.Fatalf("recovery renewed spent repair: %#v", result)
					}
				} else if result.Outcome != execution.OutcomeSucceeded || run.project.status != "Agent QA" {
					t.Fatalf("recovery failed: %#v", result)
				}
			})
		}
	}
}

func TestTestSpecialistUnresolvedCleanupKeepsOwnedFilesAndStops(t *testing.T) {
	service, run, item, _ := testSpecialistFixture(t)
	var private, artifacts, runtimeDir string
	run.beforeReturn = func(_ context.Context, dir string, call int) error {
		if call != 2 {
			return nil
		}
		private = dir
		artifacts = filepath.Dir(argumentValue(run.args, "--output-schema"))
		match := regexp.MustCompile(`TMPDIR="([^"]+)"`).FindStringSubmatch(strings.Join(run.args[:len(run.args)-1], " "))
		if len(match) != 2 {
			t.Fatal("fixture lacks observed private runtime directory")
		}
		runtimeDir = match[1]
		return &subprocess.CleanupError{Err: errors.New("fixture-owned descendant not confirmed stopped")}
	}
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if result.FailureClass != "cleanup_unresolved" || result.RetryDisposition != "manual" || run.calls != 2 {
		t.Fatalf("unresolved cleanup continued: %#v calls%d", result, run.calls)
	}
	// The fake owns no actual processes. Dispose only these exact fixture-created
	// directories after proving production preserved them for the live owner.
	for _, directory := range []string{private, artifacts, runtimeDir} {
		if directory == "" {
			t.Fatal("missing retained owner path")
		}
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			t.Fatalf("removed owned directory before cleanup resolved: %s %v", directory, err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
	}
}
