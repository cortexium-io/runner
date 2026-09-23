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
)

// Only paid I/O is substituted. The failing gate is the real declared shell
// command against the real combined candidate, not an injected error string.
type failedPlanGateRunner struct {
	*deliveryMilestoneRunner
	classifications       int
	classification        string
	classificationModel   string
	crash                 bool
	crashTransition       bool
	cleanupUnresolved     bool
	artifacts, runtimeDir string
}

func (r *failedPlanGateRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *failedPlanGateRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	prompt := strings.Join(args, " ")
	if command == "gh" && r.crashTransition && r.classifications > 0 && (strings.Contains(prompt, "mutation") || len(args) > 1 && args[0] == "project" && args[1] == "item-edit") {
		panic("fixture power loss after result before transition")
	}
	if command == "codex" && strings.Contains(prompt, `"review_scope":"plan"`) {
		if strings.Contains(prompt, "BEGIN OBSERVED FAILED-GATE DIAGNOSTICS") {
			r.classifications++
			r.classificationModel = argumentValue(args, "--model")
			if r.cleanupUnresolved {
				r.artifacts = filepath.Dir(argumentValue(args, "--output-schema"))
				// The native reviewer runs in its private neutral directory;
				// host access has no synthetic TMPDIR CLI override to parse.
				r.runtimeDir = filepath.Join(dir, "runtime")
				return subprocess.Result{Stdout: `{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30}}`}, &subprocess.CleanupError{Err: errors.New("fixture descendant remains unresolved")}
			}
			if r.crash {
				panic("fixture power loss after durable classification intent")
			}
			r.reviews++
			body, err := reviewerContentForSchema(args, r.classification != "accept", "member-1 must be ready for the combined outcome")
			if err != nil {
				return subprocess.Result{}, err
			}
			var value map[string]any
			if err := json.Unmarshal(body, &value); err != nil {
				return subprocess.Result{}, err
			}
			if r.classification != "accept" {
				owner := "PVTI_created_2"
				if r.classification == "unknown-owner" {
					owner = "PVTI_unknown"
				}
				value["repair_targets"] = []execution.PlanRepairTarget{{CheckKey: "P1", ItemID: owner, Finding: "Restore the member-1 ready behavior", InScope: true}}
				if r.classification == "mixed-unknown" {
					value["repository_rules"] = map[string]any{"status": "check_required", "summary": "Unknown repository proof", "evidence": []string{"a separate unresolved check is needed"}}
				}
			}
			body, err = json.Marshal(value)
			if err != nil {
				return subprocess.Result{}, err
			}
			return subprocess.Result{}, os.WriteFile(argumentValue(args, "--output-last-message"), body, 0o600)
		}
		// Deliberate false acceptance in the fixture: independent complete
		// validation, not this model answer, must catch the combined defect.
		prior := r.rejectCombined
		r.rejectCombined = false
		result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
		r.rejectCombined = prior
		return result, err
	}
	return r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
}

func prepareFailedPlanGate(t *testing.T, disposition string) (*deliveryRunFixture, *failedPlanGateRunner) {
	t.Helper()
	f := newProductionDelivery(t)
	f.runner.rejectCombined = true
	f.integrateMembers(t)
	r := &failedPlanGateRunner{deliveryMilestoneRunner: f.runner, classification: disposition}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	return f, r
}

func retainedPlanProgress(t *testing.T, f *deliveryRunFixture) *planVerificationProgress {
	t.Helper()
	record, err := f.service.readReviewFeedbackRecord(f.parent(t))
	if err != nil || record == nil || record.PlanVerification == nil {
		t.Fatalf("protected progress missing: %v", err)
	}
	return record.PlanVerification
}

func TestPlanFailedGateClassifiesOneOwnedRepair(t *testing.T) {
	f, r := prepareFailedPlanGate(t, "reject")
	astra := "gpt-6-astra"
	f.cfg.Roles["plan_reviewer"] = config.RoleConfig{Extends: config.WorkRoleReviewer, Model: &astra, Reasoning: "medium"}
	f.cfg.PlanDelivery.ReviewerRole = "plan_reviewer"
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "failed-gate-attempt")
	if result.Outcome != config.WorkflowOutcomeRejected || r.classifications != 1 || r.creates != 0 {
		t.Fatalf("result=%s %s %s classifications=%d PRs=%d", result.Outcome, result.Summary, result.Error, r.classifications, r.creates)
	}
	p := retainedPlanProgress(t, f)
	if p.ReviewerRole != "plan_reviewer" || r.classificationModel != astra {
		t.Fatal("failed-gate classification did not retain the original whole-plan profile")
	}
	if p.Accepted.ReviewAssessment.Verdict != "accept" || p.Gate == nil || p.Gate.Receipt.Outcome != "failed" || p.Classification == nil || p.Classification.Result == nil || p.Publication != nil {
		t.Fatal("accepted QA or actual failed gate/classification history was lost or relabeled")
	}
	if _, err := p.failureDiagnostics(); err != nil {
		t.Fatal(err)
	}
	if f.parent(t).QAFailures != 1 {
		t.Fatal("classification did not use the parent QA allowance")
	}
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "PVTI_created_2" && item.Status != "Ready" {
			t.Fatal("exact approved owner not returned for repair")
		}
		if item.ID == "PVTI_created_3" && item.Phase != github.PlanIntegratedPhase {
			t.Fatal("unaffected member changed")
		}
	}
	// Follow the real existing owning-card repair path through integration and
	// a new parent acceptance. Failed historical proof must not authorize it.
	for cycle := 0; cycle < 4 && f.parent(t).Status != "PR Ready"; cycle++ {
		results, err := f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("classified repair cycle: %s %s", result.Summary, result.Error)
			}
		}
	}
	if f.parent(t).Status != "PR Ready" || f.parent(t).QAFailures != 1 || r.classifications != 1 || r.creates != 1 || r.implementations != 3 || r.reviews != 6 {
		t.Fatalf("repair did not complete once: status=%s QAcount=%d classifier=%d PR=%d impl=%d review=%d", f.parent(t).Status, f.parent(t).QAFailures, r.classifications, r.creates, r.implementations, r.reviews)
	}
	repaired := retainedPlanProgress(t, f)
	if repaired.Candidate.Head == p.Candidate.Head || repaired.Gate.Receipt.Outcome != "passed" || repaired.Gate.Receipt.ExecutionID == p.Gate.Receipt.ExecutionID || repaired.Classification != nil {
		t.Fatal("new acceptance reused a failed gate or prior classifier allowance")
	}
	archives, err := filepath.Glob(f.service.reviewFeedbackPath(f.parentID) + ".verification-*")
	if err != nil || len(archives) == 0 {
		t.Fatal("failed verification history was not archived")
	}
	retainedFailure := false
	for _, path := range archives {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), p.Gate.Receipt.ExecutionID) {
			retainedFailure = true
		}
	}
	if !retainedFailure {
		t.Fatal("repair erased original failed execution history")
	}
}

func TestPlanFailedGateCannotBecomeAcceptance(t *testing.T) {
	for _, disposition := range []string{"accept", "unknown-owner", "mixed-unknown"} {
		t.Run(disposition, func(t *testing.T) {
			f, r := prepareFailedPlanGate(t, disposition)
			action, err := f.service.source.Authorize(t.Context(), f.parent(t))
			if err != nil {
				t.Fatal(err)
			}
			result := f.service.executeQA(t.Context(), action, "failed-gate-attempt")
			if result.Outcome != execution.OutcomeBlocked || r.classifications != 1 || r.creates != 0 {
				t.Fatalf("%s %s %s classifications=%d", result.Outcome, result.Summary, result.Error, r.classifications)
			}
			p := retainedPlanProgress(t, f)
			if p.Publication != nil || p.Classification.Result == nil {
				t.Fatal("classification lost or authorized failed proof")
			}
		})
	}
}

func TestPlanClassifierSpentBeforeInvocationRefusesRestart(t *testing.T) {
	f, r := prepareFailedPlanGate(t, "reject")
	r.crash = true
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "fixture power loss after durable classification intent" {
				t.Fatalf("unexpected panic: %v", recovered)
			}
		}()
		_ = f.service.executeQA(t.Context(), action, "interrupted-gate-attempt")
		t.Fatal("fixture never reached classification")
	}()
	p := retainedPlanProgress(t, f)
	if !p.classificationPending() || p.Gate.Receipt.Outcome != "failed" {
		t.Fatal("spent intent did not precede the model boundary")
	}
	r.crash = false
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "restart-gate-attempt")
	if result.Outcome != execution.OutcomeBlocked || r.classifications != 1 || r.reviews != 3 || r.creates != 0 || !strings.Contains(result.Summary, "already spent") {
		t.Fatalf("restart duplicated uncertain call: %s %s %s calls=%d reviews=%d", result.Outcome, result.Summary, result.Error, r.classifications, r.reviews)
	}
	if !retainedPlanProgress(t, f).Classification.Deadline.Equal(p.Classification.Deadline) {
		t.Fatal("restart renewed classifier deadline")
	}
}

func TestPlanGateDiagnosticTamperingRefused(t *testing.T) {
	f, _ := prepareFailedPlanGate(t, "accept")
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.service.executeQA(t.Context(), action, "failed-gate-attempt")
	p := retainedPlanProgress(t, f)
	p.Gate.Output.Stderr += "forged diagnostic"
	if _, err := p.failureDiagnostics(); err == nil {
		t.Fatal("modified diagnostics accepted")
	}
	p.Failure = &planVerificationFailure{Phase: "preparation", ExecutionID: "invented", ExitCode: 1}
	if _, err := p.failureDiagnostics(); err == nil {
		t.Fatal("preparation error became a product classification")
	}
}

func newGuardedPlanFixture(t *testing.T, alter ...func(*config.VerificationEntrypoint)) *deliveryRunFixture {
	t.Helper()
	repo, remote := createPublicationRepository(t)
	runtime := filepath.Join(t.TempDir(), "test-runtime")
	if err := os.WriteFile(runtime, []byte("observed fixture runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &deliveryMilestoneRunner{project: &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Args: []string{"-c", `test "$(cat member-1.txt)" = ready && test "$(cat member-2.txt)" = ready`}, InputPaths: []string{"member-1.txt", "member-2.txt"}, TimeoutSeconds: 30, CurrentCandidateCheck: &config.VerificationCurrentCandidateCheck{Command: "/bin/sh", Args: []string{"-c", "test -f member-1.txt && test -f member-2.txt"}}},
	}})
	profile := cfg.Roles["reviewer"]
	entry := cfg.Verification["complete"]
	entry.RuntimePaths = []string{runtime}
	cfg.Verification["complete"] = entry
	for _, change := range alter {
		entry := cfg.Verification["complete"]
		change(&entry)
		cfg.Verification["complete"] = entry
	}
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	children, err := service.ApplyProjectPlan(t.Context(), sourcedDirectProjectPlanFixture(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	return &deliveryRunFixture{service: service, cfg: cfg, runner: runner, repo: repo, remote: remote, parentID: children[0].PlanningSourceID}
}

func interruptGuardedPublication(t *testing.T) (*deliveryRunFixture, *publicationCrashRunner, *planVerificationProgress) {
	t.Helper()
	f := newGuardedPlanFixture(t)
	f.integrateMembers(t)
	r := &publicationCrashRunner{deliveryMilestoneRunner: f.runner, afterCreate: true}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.service.executeQA(t.Context(), action, "initial-gate-attempt")
	if !r.crashed || f.runner.creates != 1 || f.runner.reviews != 3 {
		t.Fatal("fixture did not reach lost publication boundary")
	}
	r.offline = false
	p := retainedPlanProgress(t, f)
	if p.Publication == nil || p.Gate.CurrentCandidateCheck == nil || p.Gate.Receipt == nil {
		t.Fatal("passing publication envelope missing")
	}
	return f, r, p
}

func TestPlanPendingPublicationReusesOriginalHeavyAndRenewsGuard(t *testing.T) {
	f, r, before := interruptGuardedPublication(t)
	heavy, _ := json.Marshal(before.Gate.Receipt)
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "recovery-gate-attempt")
	if result.Error != "" || result.Outcome != execution.OutcomeSucceeded || !result.ResumedCheckpoint {
		t.Fatalf("recovery: %s %s %s", result.Outcome, result.Summary, result.Error)
	}
	after := retainedPlanProgress(t, f)
	retained, _ := json.Marshal(after.Gate.Receipt)
	if string(heavy) != string(retained) || !after.Gate.Historical || after.Gate.CurrentCandidateCheck.ExecutionID == before.Gate.CurrentCandidateCheck.ExecutionID || after.Gate.CurrentCandidateCheck.AttemptID != "recovery-gate-attempt" {
		t.Fatal("recovery reran/relabelled heavy proof or failed to renew exact-candidate guard")
	}
	if *after.Publication != *before.Publication {
		t.Fatal("recovery rewrote immutable publication authorization")
	}
	if f.runner.reviews != 3 || f.runner.implementations != 2 || f.runner.creates != 1 || result.HarnessDurationMilliseconds != 0 {
		t.Fatal("successful gate recovery repeated model work or publication")
	}
}

func TestPlanPreparationPreservesQAAndRecoversExactPreparedPublication(t *testing.T) {
	f := newGuardedPlanFixture(t, func(e *config.VerificationEntrypoint) {
		e.DependencyPaths = []string{"deps"}
		e.Preparation = &config.VerificationPreparation{Command: "/bin/sh", Args: []string{"-c", "mkdir -p deps/pkg; printf '*.js text\\n' > deps/pkg/.gitattributes; printf 'dist/\\n' > deps/pkg/.gitignore; printf metadata > deps/pkg/.gitmodules"}}
	})
	// This common control is present before every implementation/QA snapshot.
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte("deps/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.integrateMembers(t)
	r := &publicationCrashRunner{deliveryMilestoneRunner: f.runner, afterCreate: true}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "prepared-plan")
	if !r.crashed || r.creates != 1 || r.reviews != 3 {
		t.Fatalf("prepared plan did not reach publication: %s %s %s", result.Outcome, result.Summary, result.Error)
	}
	r.offline = false
	before := retainedPlanProgress(t, f)
	if before.PreparedCandidate == nil || before.Candidate.Fingerprint == before.PreparedCandidate.Fingerprint || before.Publication.AcceptanceSnapshot != before.PreparedCandidate.Fingerprint || before.Gate.Invocation.Outcome != "passed" {
		t.Fatal("preparation did not retain distinct QA and verified publication snapshots")
	}
	qa, _ := json.Marshal(before.Candidate)
	heavy, _ := json.Marshal(before.Gate.Receipt)
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result = f.service.executeQA(t.Context(), action, "prepared-recovery")
	if result.Outcome != execution.OutcomeSucceeded || r.creates != 1 || r.reviews != 3 {
		t.Fatalf("prepared recovery repeated QA/publication or failed: %s %s %s", result.Outcome, result.Summary, result.Error)
	}
	after := retainedPlanProgress(t, f)
	retainedQA, _ := json.Marshal(after.Candidate)
	retainedHeavy, _ := json.Marshal(after.Gate.Receipt)
	if string(qa) != string(retainedQA) || string(heavy) != string(retainedHeavy) || !after.Gate.Historical || after.Gate.CurrentPreparation != nil || after.Gate.CurrentCandidateCheck.ExecutionID == before.Gate.CurrentCandidateCheck.ExecutionID {
		t.Fatal("recovery rewrote original QA/heavy proof or failed to renew only the guard")
	}
}

func TestPlanInterruptedPreparationRequiresExactSnapshotRestoration(t *testing.T) {
	stop := filepath.Join(t.TempDir(), "stop-preparation")
	if err := os.WriteFile(stop, []byte("fixture setup failure"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newGuardedPlanFixture(t, func(e *config.VerificationEntrypoint) {
		e.DependencyPaths = []string{"deps"}
		e.Preparation = &config.VerificationPreparation{Command: "/bin/sh", Args: []string{"-c", "mkdir -p deps/pkg; printf '*.js text\\n' > deps/pkg/.gitattributes; test ! -f \"$1\"", "prepare", stop}}
	})
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte("deps/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.integrateMembers(t)
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	failed := f.service.executeQA(t.Context(), action, "partial-preparation")
	p := retainedPlanProgress(t, f)
	if failed.Outcome != execution.OutcomeBlocked || p.PreparedCandidate != nil || p.Gate.Receipt != nil || p.Publication != nil || p.Failure != nil || p.Gate.CurrentPreparation.Outcome != "failed" {
		t.Fatalf("failed preparation created acceptance/proof: %s %s", failed.Summary, failed.Error)
	}
	if err := os.Remove(stop); err != nil {
		t.Fatal(err)
	}
	f.service, err = New(f.cfg, f.runner)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	refused := f.service.executeQA(t.Context(), action, "unrestored-restart")
	if refused.Outcome != execution.OutcomeBlocked || !strings.Contains(refused.Error, "workspace binding changed") || f.runner.reviews != 3 || f.runner.creates != 0 {
		t.Fatalf("unexplained mismatch adopted: %s %s", refused.Summary, refused.Error)
	}
	// Restore only the declared root, preserving its diagnostic contents.
	if err := os.Rename(filepath.Join(p.Metadata.WorktreePath, "deps"), filepath.Join(t.TempDir(), "retained-deps")); err != nil {
		t.Fatal(err)
	}
	restored, err := f.service.checkoutSnapshotState(t.Context(), p.Metadata.WorktreePath)
	if err != nil || restored.Fingerprint != p.Candidate.Fingerprint {
		t.Fatalf("ordinary full snapshot was not restored: %v", err)
	}
	preview, err = f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "restored-restart")
	if result.Outcome != execution.OutcomeSucceeded || f.runner.reviews != 3 || f.runner.creates != 1 {
		t.Fatalf("restored acceptance failed or repeated QA: %s %s", result.Summary, result.Error)
	}
	after := retainedPlanProgress(t, f)
	if after.Candidate.Fingerprint != p.Candidate.Fingerprint || after.AttemptID != p.AttemptID || after.PreparedCandidate == nil {
		t.Fatal("restored recovery rewrote the original acceptance")
	}
}

func TestPlanGateRecoveryRefusesChangedCatalogAndProfile(t *testing.T) {
	for _, change := range []string{"catalog", "profile", "tampered-receipt"} {
		t.Run(change, func(t *testing.T) {
			f, r, before := interruptGuardedPublication(t)
			switch change {
			case "catalog":
				e := f.cfg.Verification["complete"]
				e.Args = []string{"-c", "exit 0"}
				f.cfg.Verification["complete"] = e
			case "profile":
				p := f.cfg.Roles["reviewer"]
				p.Access = config.RoleAccessSandboxed
				f.cfg.Roles["reviewer"] = p
			case "tampered-receipt":
				record, err := f.service.readReviewFeedbackRecord(f.parent(t))
				if err != nil {
					t.Fatal(err)
				}
				record.PlanVerification.Gate.Receipt.ReportDigest = strings.Repeat("a", 64)
				if err := f.service.writeReviewFeedback(*record); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			f.service, err = New(f.cfg, r)
			if err != nil {
				t.Fatal(err)
			}
			action, err := f.service.source.Authorize(t.Context(), f.parent(t))
			if err != nil {
				if change == "catalog" && f.runner.reviews == 3 && f.runner.creates == 1 {
					return
				}
				t.Fatal(err)
			}
			result := f.service.executeQA(t.Context(), action, "refused-recovery")
			if result.Outcome != execution.OutcomeBlocked || f.runner.reviews != 3 || f.runner.creates != 1 {
				t.Fatalf("changed proof reused: %s %s %s", result.Outcome, result.Summary, result.Error)
			}
			if change != "tampered-receipt" && retainedPlanProgress(t, f).Gate.Invocation.ExecutionID != before.Gate.Invocation.ExecutionID {
				t.Fatal("refused recovery executed the launcher")
			}
		})
	}
}

func TestPlanMergedRecoveryNeedsNeitherCheckoutNorNewGate(t *testing.T) {
	f, r, before := interruptGuardedPublication(t)
	parent := f.parent(t)
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", "refs/heads/main", before.Candidate.Head)
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", "-d", "refs/heads/"+parent.Branch)
	runGitTest(t, f.repo, "worktree", "remove", "--force", before.Metadata.WorktreePath)
	runGitTest(t, f.repo, "branch", "-D", parent.Branch)
	r.merged = true
	// Terminal delivery is not conditional on still-available validation tools.
	if err := os.Remove(f.cfg.Verification["complete"].RuntimePaths[0]); err != nil {
		t.Fatal(err)
	}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "terminal-recovery")
	if f.parent(t).Status != "Done" {
		t.Fatalf("confirmed merge not delivered: %s %s %s", result.Outcome, result.Summary, result.Error)
	}
	if r.reviews != 3 || r.implementations != 2 || r.creates != 1 {
		t.Fatal("terminal recovery repeated model or publication work")
	}
	after := retainedPlanProgress(t, f)
	if after.Gate.Invocation.ExecutionID != before.Gate.Invocation.ExecutionID {
		t.Fatal("terminal recovery renewed verification")
	}
}

func TestPlanNonCommandGateFailuresNeverInvokeClassifier(t *testing.T) {
	for _, failure := range []string{"missing-runtime", "timeout", "candidate-changed"} {
		t.Run(failure, func(t *testing.T) {
			f := newGuardedPlanFixture(t, func(e *config.VerificationEntrypoint) {
				switch failure {
				case "timeout":
					e.TimeoutSeconds = 1
					e.Args = []string{"-c", "sleep 5"}
				case "candidate-changed":
					e.Args = []string{"-c", "printf changed > member-1.txt; exit 1"}
				}
			})
			f.integrateMembers(t)
			r := &failedPlanGateRunner{deliveryMilestoneRunner: f.runner}
			var err error
			f.service, err = New(f.cfg, r)
			if err != nil {
				t.Fatal(err)
			}
			if failure == "missing-runtime" {
				if err := os.Remove(f.cfg.Verification["complete"].RuntimePaths[0]); err != nil {
					t.Fatal(err)
				}
			}
			action, err := f.service.source.Authorize(t.Context(), f.parent(t))
			if err != nil {
				t.Fatal(err)
			}
			result := f.service.executeQA(t.Context(), action, "noncommand-failure")
			if result.Outcome != execution.OutcomeBlocked || r.classifications != 0 || r.creates != 0 || r.reviews != 3 {
				t.Fatalf("unsafe classification for %s: %s %s %s calls=%d", failure, result.Outcome, result.Summary, result.Error, r.classifications)
			}
			p := retainedPlanProgress(t, f)
			if p.Failure != nil || p.Classification != nil || p.Publication != nil {
				t.Fatal("setup/timeout/provenance refusal created classification authority")
			}
		})
	}
}

func TestPlanCompletedClassifierRecoveryAppliesOnceWithoutModelOrGate(t *testing.T) {
	f, r := prepareFailedPlanGate(t, "reject")
	r.crashTransition = true
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "fixture power loss after result before transition" {
				t.Fatalf("unexpected recovery %v", recovered)
			}
		}()
		_ = f.service.executeQA(t.Context(), action, "classification-completed")
		t.Fatal("fixture did not crash before Project transition")
	}()
	before := retainedPlanProgress(t, f)
	if before.Classification == nil || before.Classification.Result == nil || f.parent(t).QAFailures != 0 {
		t.Fatal("actual result was not retained before mutation")
	}
	r.crashTransition = false
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	second := f.service.executeQA(t.Context(), action, "classification-recovered")
	if second.Outcome != config.WorkflowOutcomeRejected || second.Error != "" || r.classifications != 1 || r.reviews != 4 || r.creates != 0 || f.parent(t).QAFailures != 1 {
		t.Fatalf("invalid replay: %s %s %s classifier=%d reviews=%d failures=%d", second.Outcome, second.Summary, second.Error, r.classifications, r.reviews, f.parent(t).QAFailures)
	}
	after := retainedPlanProgress(t, f)
	if after.Gate.Invocation.ExecutionID != before.Gate.Invocation.ExecutionID || !after.Classification.Deadline.Equal(before.Classification.Deadline) || second.HarnessDurationMilliseconds != 0 {
		t.Fatal("completed replay renewed work/deadline/accounting")
	}
}

func TestPlanClassifierUnresolvedCleanupRetainsOwnerAndAdmission(t *testing.T) {
	f, r := prepareFailedPlanGate(t, "reject")
	r.cleanupUnresolved = true
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "unresolved-classifier")
	if result.FailureClass != string(execution.FailureCleanupUnresolved) || result.RetryDisposition != string(execution.RetryManual) || r.classifications != 1 || r.creates != 0 {
		t.Fatalf("lost owned failure: %s %s %s calls=%d", result.FailureClass, result.RetryDisposition, result.Error, r.classifications)
	}
	if result.Usage.InputTokens != 100 || result.Usage.OutputTokens != 30 || result.Usage.Coverage != metrics.UsagePartial {
		t.Fatalf("provider failure usage lost or duplicated: %+v", result.Usage)
	}
	if f.service.processOwnership.CheckAdmission() == nil {
		t.Fatal("unresolved classifier released local admission")
	}
	p := retainedPlanProgress(t, f)
	if p.Classification == nil || p.Classification.Result == nil || !p.Classification.Failed || p.Classification.Result.FailureClass != execution.FailureCleanupUnresolved {
		t.Fatal("actual unresolved provider result was not retained before transition")
	}
	for _, path := range []string{p.Classification.WorkspacePath, r.artifacts, r.runtimeDir} {
		if path == "" {
			t.Fatal("owned classifier path was not retained")
		}
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("owned path removed with unresolved process: %s %v", path, err)
		}
		// This fake has no live descendants. Remove only exact fixture paths
		// after proving production did not clean underneath the owner.
		t.Cleanup(func() { _ = os.RemoveAll(path) })
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(r.runtimeDir)) })
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	recovered := f.service.executeQA(t.Context(), action, "unresolved-restart")
	if recovered.FailureClass != string(execution.FailureCleanupUnresolved) || r.classifications != 1 || recovered.Usage.Reported() || recovered.HarnessDurationMilliseconds != 0 || f.service.processOwnership.CheckAdmission() == nil {
		t.Fatalf("restart repeated or hid unresolved work: %s calls=%d usage=%+v", recovered.FailureClass, r.classifications, recovered.Usage)
	}
}
