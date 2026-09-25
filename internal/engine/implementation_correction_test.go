package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

const unfinishedImplementation = `{"outcome":"repair_needed","summary":"Retained toolbar repair needs a selection fix.","work_done":["Kept compact toolbar controls."],"verification":["Focused selection.spec.ts:42 failed: edit target missing."],"blockers":["Repair the selection-to-edit transition and rerun its focused check."]}`

type repairImplementationRunner struct {
	*successfulImplementationRunner
	responses    []string
	deadlines    []time.Time
	prompts      []string
	beforeReturn func(context.Context, string, int) error
	afterCommand func(string, []string) error
}

func (r *repairImplementationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := r.successfulImplementationRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command != "codex" && r.afterCommand != nil {
		err = r.afterCommand(command, args)
	}
	if err != nil || command != "codex" {
		return result, err
	}
	deadline, _ := ctx.Deadline()
	r.deadlines = append(r.deadlines, deadline)
	r.prompts = append(r.prompts, args[len(args)-1])
	if r.beforeReturn != nil {
		if err := r.beforeReturn(ctx, dir, r.calls); err != nil {
			return result, err
		}
	}
	if r.calls <= len(r.responses) && r.responses[r.calls-1] != "" {
		err = os.WriteFile(argumentValue(args, "--output-last-message"), []byte(r.responses[r.calls-1]), 0o600)
	}
	return result, err
}

func (r *repairImplementationRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func implementationRepairFixture(t *testing.T) (*Engine, *repairImplementationRunner, github.WorkItem, config.Config) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_repair", Title: "Repair selection", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/430", Repository: "owner/repo", Status: "Ready", Role: config.WorkRoleImplementer, QAFailures: 1}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: item.QAFailures}
	run := &repairImplementationRunner{successfulImplementationRunner: &successfulImplementationRunner{project: project}, responses: []string{unfinishedImplementation}}
	run.inspect = func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("retained implementation\n"), 0o644)
	}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, run)
	if err != nil {
		t.Fatal(err)
	}
	return service, run, item, cfg
}

func TestImplementationRepairRetainsWorkEvidenceDeadlineAndUsage(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	run.stdout = `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}}` + "\n"
	run.beforeReturn = func(ctx context.Context, dir string, call int) error {
		if call == 2 {
			data, err := os.ReadFile(filepath.Join(dir, "selection.txt"))
			if err != nil || string(data) != "retained implementation\n" {
				t.Fatalf("repair lost retained work: %q %v", data, err)
			}
			record, err := service.readImplementationCheckpoint(item.ID)
			if err != nil || record == nil || !record.Incomplete || !record.CorrectionUsed || !record.ExecutionDeadline.Equal(deadline) {
				t.Fatalf("allowance was not spent before second call: %#v %v", record, err)
			}
		}
		return nil
	}
	history := metrics.NewStore(filepath.Join(t.TempDir(), "metrics", "metrics.jsonl"))
	service.SetMetricsObserver(history.Append)
	result := service.executeItem(ctx, admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if result.Outcome != execution.OutcomeSucceeded || run.calls != 2 || run.project.status != "Agent QA" || run.project.qaFailures != 1 {
		t.Fatalf("repair did not reach QA without a rejection: calls=%d result=%#v state=%s failures=%d", run.calls, result, run.project.status, run.project.qaFailures)
	}
	if result.Usage.InputTokens != 200 || result.Usage.OutputTokens != 60 || result.Usage.CacheReadInputTokens != 40 {
		t.Fatalf("repair usage lost or double counted: %#v", result.Usage)
	}
	if run.deadlines[0].IsZero() || !run.deadlines[0].Equal(run.deadlines[1]) {
		t.Fatalf("repair renewed the original deadline: %v", run.deadlines)
	}
	for _, prompt := range run.prompts {
		if !strings.Contains(prompt, "finish by "+deadline.UTC().Format(time.RFC3339)) || strings.Contains(prompt, "from launch") {
			t.Fatal("implementation or repair prompt contradicted the enforced original deadline")
		}
	}
	if !strings.Contains(run.prompts[1], "selection.spec.ts:42") || !strings.Contains(run.prompts[1], "selection-to-edit") || !strings.Contains(run.prompts[1], "Kept compact toolbar") {
		t.Fatal("repair prompt lost prior work, failure evidence or remaining correction")
	}
	if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful QA handoff retained the checkpoint: %v", err)
	}
	retained, err := history.Read()
	if err != nil || retained.MalformedRecords != 0 || len(retained.Attempts) != 1 {
		t.Fatalf("invalid repair metrics: %#v %v", retained, err)
	}
	harnessCalls, admittedRepairs := 0, 0
	for _, stage := range retained.Attempts[0].Stages {
		if stage.Name == metrics.StageHarnessRun {
			harnessCalls++
		}
		if stage.Name == metrics.StageImplementationRepair && stage.RetryDisposition == "automatic" {
			admittedRepairs++
		}
	}
	if harnessCalls != 2 || admittedRepairs != 1 || !strings.Contains(strings.Join(retained.Attempts[0].WorkDone, "\n"), "selection.spec.ts:42") {
		t.Fatalf("repair history lost calls/admission/earlier failure: %#v", retained.Attempts[0])
	}
}

func TestImplementationRepairStopsAfterOnePassAndAllowsExplicitRetry(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	run.responses = []string{unfinishedImplementation, unfinishedImplementation}
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if result.Outcome != execution.OutcomeBlocked || result.FailureClass != "repair_exhausted" || result.RetryDisposition != "manual" || run.calls != 2 || run.project.status != "Blocked" || run.project.qaFailures != 1 {
		t.Fatalf("repair did not stop at bound: calls=%d result=%#v", run.calls, result)
	}
	if !strings.Contains(result.Error, "Repair the selection-to-edit transition") {
		t.Fatalf("final remaining repair was lost: %s", result.Error)
	}
	if strings.Contains(run.project.result, "selection.spec") || !strings.Contains(run.project.result, "repair_exhausted") {
		t.Fatalf("remote result lost safe recovery explanation: %s", run.project.result)
	}
	plan, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := service.ApplyProjectItemRetry(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	result = service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, retried), event: service.newItemAttempt(retried)})
	if result.Outcome != execution.OutcomeSucceeded || run.calls != 3 || run.project.qaFailures != 1 {
		t.Fatalf("explicit retry could not resume retained work: %#v calls=%d", result, run.calls)
	}
}

func TestImplementationRepairNeverRetriesHumanOrEnvironmentBlockers(t *testing.T) {
	for _, outcome := range []string{execution.OutcomeNeedsInput, execution.OutcomeBlocked} {
		t.Run(outcome, func(t *testing.T) {
			service, run, item, _ := implementationRepairFixture(t)
			run.responses = []string{strings.Replace(unfinishedImplementation, "repair_needed", outcome, 1)}
			result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			if run.calls != 1 || result.RetryDisposition != "manual" || run.project.status != "Blocked" || run.project.qaFailures != 1 {
				t.Fatalf("blocked/needs_input became automatic: %#v calls=%d", result, run.calls)
			}
		})
	}
}

func TestImplementationRepairRejectsChangedAuthorityAndIntegrity(t *testing.T) {
	for _, change := range []string{"approval", "source checkout"} {
		t.Run(change, func(t *testing.T) {
			service, run, item, cfg := implementationRepairFixture(t)
			run.beforeReturn = func(_ context.Context, _ string, _ int) error {
				if change == "approval" {
					item.Body = "Different unapproved work"
					run.project.itemsJSON = `{"items":[` + projectItemJSON(item) + `]}`
					return nil
				}
				return os.WriteFile(filepath.Join(cfg.ProjectDir, "README.md"), []byte("unexpected source mutation\n"), 0o644)
			}
			result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			if run.calls != 1 || result.FailureClass != "integrity_violation" {
				t.Fatalf("unsafe continuation: %#v calls=%d", result, run.calls)
			}
		})
	}
}

func TestImplementationRepairDoesNotContinueAfterCancellation(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	run.beforeReturn = func(context.Context, string, int) error { cancel(); return nil }
	result := service.executeItem(ctx, admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if run.calls != 1 || result.Outcome == execution.OutcomeSucceeded || result.RetryDisposition == "automatic" {
		t.Fatalf("canceled work continued: %#v calls=%d", result, run.calls)
	}
}

func TestImplementationRepairPreparationBoundary(t *testing.T) {
	for _, change := range []string{"workspace changed", "deadline", "cancellation"} {
		t.Run(change, func(t *testing.T) {
			service, run, item, _ := implementationRepairFixture(t)
			changed := false
			run.afterCommand = func(command string, args []string) error {
				if changed || command != "gh" || !strings.Contains(strings.Join(args, " "), "Repairing implementation (1/1)") {
					return nil
				}
				changed = true
				switch change {
				case "deadline":
					return context.DeadlineExceeded
				case "cancellation":
					return context.Canceled
				default:
					return os.WriteFile(filepath.Join(run.dir, "selection.txt"), []byte("unexpected late mutation\n"), 0o644)
				}
			}
			result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
			want := "repair_exhausted"
			if change == "workspace changed" {
				want = "integrity_violation"
			}
			if !changed || run.calls != 1 || result.FailureClass != want {
				t.Fatalf("unsafe or misclassified continuation: changed=%v calls=%d class=%s status=%s error=%s", changed, run.calls, result.FailureClass, run.project.status, result.Error)
			}
			if change != "workspace changed" {
				// An interrupted activity write retains the authenticated
				// transition lock and spent guard until normal reconciliation.
				record, err := service.readImplementationCheckpoint(item.ID)
				if err != nil || record == nil || !record.Incomplete || !record.CorrectionUsed {
					t.Fatalf("interrupted admission lost its guard: %#v %v", record, err)
				}
				if _, err := service.source.RecoverInterrupted(t.Context()); err != nil {
					t.Fatal(err)
				}
				result = service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, github.WorkItem{ID: item.ID}), event: service.newItemAttempt(item)})
			}
			if run.calls != 1 || run.project.status != "Blocked" || result.FailureClass != want {
				t.Fatalf("reconciliation renewed correction: calls=%d status=%s class=%s", run.calls, run.project.status, result.FailureClass)
			}
		})
	}
}

func TestImplementationRepairProviderFailureCannotRenewBudget(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	run.beforeReturn = func(_ context.Context, _ string, call int) error {
		if call == 1 {
			run.stdout = `{"type":"turn.failed","error":{"message":"Selected model is at capacity. Please try a different model."}}` + "\n"
			return nil
		}
		return errors.New("exit status 1")
	}
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if run.calls != 2 || result.FailureClass != "capacity_exhausted" || result.RetryDisposition != "manual" || run.project.status != "Blocked" || result.RetryAfter != "" {
		t.Fatalf("provider failure renewed repair budget: calls=%d result=%#v", run.calls, result)
	}
}

func TestImplementationRepairRestartCannotRenewSpentAllowance(t *testing.T) {
	service, run, item, cfg := implementationRepairFixture(t)
	run.beforeReturn = func(context.Context, string, int) error {
		if run.calls == 2 {
			panic("simulated supervisor exit")
		}
		return nil
	}
	func() {
		defer func() {
			if recover() != "simulated supervisor exit" {
				t.Fatal("did not simulate exit during correction")
			}
		}()
		service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	}()
	run.beforeReturn = nil
	restarted, err := New(cfg, run)
	if err != nil {
		t.Fatal(err)
	}
	result := restarted.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, restarted.source, github.WorkItem{ID: item.ID}), event: restarted.newItemAttempt(item)})
	if run.calls != 2 || result.FailureClass != "repair_exhausted" || result.RetryDisposition != "manual" || !strings.Contains(result.Summary, "interrupted") || run.project.status != "Blocked" {
		t.Fatalf("restart granted another repair: %#v calls=%d", result, run.calls)
	}
}

func TestImplementationRepairSharesAllowanceWithCandidateCorrection(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	run.beforeReturn = func(_ context.Context, dir string, call int) error {
		if call == 2 {
			return os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("trailing whitespace  \n"), 0o644)
		}
		return nil
	}
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if run.calls != 2 || result.FailureClass != "candidate_validation" || result.RetryDisposition != "manual" || run.project.status != "Blocked" {
		t.Fatalf("formatting granted a third call: %#v calls=%d", result, run.calls)
	}
}

func TestImplementationRepairCheckpointRejectsMalformedSpentState(t *testing.T) {
	service, run, item, _ := implementationRepairFixture(t)
	run.beforeReturn = func(context.Context, string, int) error {
		if run.calls == 2 {
			panic("stop")
		}
		return nil
	}
	func() {
		defer func() { _ = recover() }()
		service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	}()
	path := service.implementationCheckpointPath(item.ID)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record implementationCheckpointRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	record.ExecutionDeadline = time.Time{}
	data, _ = json.Marshal(record)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.readImplementationCheckpoint(item.ID); err == nil {
		t.Fatal("accepted incomplete record with no spent deadline")
	}
}

func TestImplementationRepairResumedCheckpointKeepsDeadlineAndAllowance(t *testing.T) {
	for _, test := range []struct {
		name     string
		deadline time.Time
		used     bool
	}{
		{"original deadline expired", time.Now().Add(-time.Hour), false},
		{"correction already used", time.Now().Add(time.Hour), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, run, item, cfg := implementationRepairFixture(t)
			action := mustAuthorizeTest(t, service.source, item)
			content, err := action.DelegatedContent()
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := service.implementationWorkspaceForItem(t.Context(), item, content.Digest, cfg.ProjectDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "selection.txt"), []byte("bad formatting  \n"), 0o644); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.workspaceSnapshotState(t.Context(), metadata.WorktreePath)
			if err != nil {
				t.Fatal(err)
			}
			assignment := service.assignment(item, content, nil, nil)
			digest := implementationContextDigest(content, item, nil, nil, assignment.Spec.RequiredVerification)
			output := execution.Output{Outcome: execution.OutcomeSucceeded, Summary: "Completed before interrupted candidate checks.", WorkDone: []string{"Retained implementation."}, Verification: []string{"Focused test passed."}}
			if err := service.saveImplementationCheckpoint(item, content, digest, metadata, snapshot, workspace.Candidate{}, output, test.deadline, test.used); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(cfg, run)
			if err != nil {
				t.Fatal(err)
			}
			result := restarted.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, restarted.source, item), event: restarted.newItemAttempt(item)})
			if run.calls != 0 || result.FailureClass != "candidate_validation" || run.project.status != "Blocked" {
				t.Fatalf("restart renewed expired/used budget: outcome=%s class=%s error=%s calls=%d", result.Outcome, result.FailureClass, result.Error, run.calls)
			}
			if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stopped invalid candidate cannot be explicitly retried: %v", err)
			}
		})
	}
}

func TestImplementationRepairFailedRecoveryTransitionKeepsGuard(t *testing.T) {
	service, run, item, cfg := implementationRepairFixture(t)
	run.responses = []string{unfinishedImplementation, unfinishedImplementation}
	run.project.failStatusAt = 1
	result := service.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, service.source, item), event: service.newItemAttempt(item)})
	if result.Error == "" || run.calls != 2 {
		t.Fatalf("expected failed recovery transition: error=%s calls=%d", result.Error, run.calls)
	}
	record, err := service.readImplementationCheckpoint(item.ID)
	if err != nil || record == nil || !record.Incomplete || !record.CorrectionUsed {
		t.Fatalf("failed transition lost guard: %#v %v", record, err)
	}
	run.project.failStatusAt = 0
	restarted, err := New(cfg, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.source.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	result = restarted.executeItem(t.Context(), admittedAction{action: mustAuthorizeTest(t, restarted.source, github.WorkItem{ID: item.ID}), event: restarted.newItemAttempt(item)})
	if run.calls != 2 || result.FailureClass != "repair_exhausted" {
		t.Fatalf("failed transition/restart repeated correction: %s calls=%d", result.FailureClass, run.calls)
	}
}
