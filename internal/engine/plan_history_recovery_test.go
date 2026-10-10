package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

// Publication updates the existing PR head in this transport just as GitHub
// does. Git, approval, review, receipt and coordinator paths remain production.
type planHistoryRunner struct {
	*planEvidenceRunner
	retirementFeedbackPath string
	retirementArchive      string
}

func (r *planHistoryRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *planHistoryRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := r.planEvidenceRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command == "git" && r.creates > 0 && containsArgument(args, "push") && strings.Contains(strings.Join(args, " "), ":refs/heads/"+r.branch) {
		r.head = runnerGitRevision(ctx, dir, timeout, "refs/heads/"+r.branch)
		if r.retirementFeedbackPath != "" {
			// Simulate an interrupted protected archive write after replacement
			// push. The signed transition can succeed, but retirement must fail
			// closed and be recoverable before a later destination refresh.
			data, readErr := os.ReadFile(r.retirementFeedbackPath)
			if readErr != nil {
				return result, readErr
			}
			var feedback reviewFeedbackRecord
			if err := json.Unmarshal(data, &feedback); err != nil {
				return result, err
			}
			r.retirementArchive = r.retirementFeedbackPath + ".verification-" + planProgressDigest(&feedback)
			if err := os.WriteFile(r.retirementArchive, []byte("interrupted fixture archive"), 0600); err != nil {
				return result, err
			}
			r.retirementFeedbackPath = ""
		}
	}
	return result, err
}

func acceptedNonlinearPlan(t *testing.T) (*deliveryRunFixture, *planHistoryRunner) {
	t.Helper()
	f, evidence := newProductionEvidenceDelivery(t)
	r := &planHistoryRunner{planEvidenceRunner: evidence}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	f.integrateMembers(t)
	for i, item := range append(planEvidenceChildren(t, f), f.parent(t)) {
		action, err := f.service.source.Authorize(t.Context(), item)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.service.source.TransitionRejection(t.Context(), action, item.Status, item.Phase, "Preserved prior QA history", i+1); err != nil {
			t.Fatal(err)
		}
	}
	// Merge-mode refresh constructs the historical topology that the previous
	// rebase implementation incorrectly left intact when base was an ancestor.
	parent := f.parent(t)
	if _, err := f.service.workspaceForItem(t.Context(), parent, github.DelegatedContentFor(parent).Digest, f.repo); err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, f.repo, "new-base.txt", "Independent base change\n")
	results := runPlanEvidenceCycle(t, f)
	if len(results) != 1 || results[0].Outcome != "warning" || r.reviews != 2 {
		t.Fatal("fixture did not refresh locally before accepted combined QA")
	}
	runPlanEvidenceCycle(t, f)
	if f.parent(t).Status != "PR Ready" || r.reviews != 3 || retainedPlanProgress(t, f).Publication == nil {
		t.Fatal("fixture did not retain accepted QA, passed complete proof and an open PR")
	}
	parent = f.parent(t)
	action, err := f.service.source.Authorize(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.source.Transition(t.Context(), action, "Blocked", "Native rebase publication requires history recovery", "agent_qa"); err != nil {
		t.Fatal(err)
	}
	f.cfg.GitHubProject.MergeMethod = config.MergeMethodRebase
	f.cfg.GitHubProject.AutoMerge = true
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	p := retainedPlanProgress(t, f)
	metadata, err := workspace.NewGitProvider(f.service.run).InspectRetainedReview(t.Context(), f.service.workspaceRequestForItem(f.parent(t), github.DelegatedContentFor(f.parent(t)).Digest, f.repo, false))
	if err != nil {
		t.Fatal(err)
	}
	needed, err := workspace.NewGitProvider(f.service.run).CandidateNeedsRebaseNormalization(t.Context(), metadata, workspace.Candidate{CommitOID: p.Candidate.Head, TreeOID: p.Candidate.Tree})
	if err != nil || !needed {
		t.Fatalf("fixture is not an accepted nonlinear candidate: needed=%t err=%v", needed, err)
	}
	return f, r
}

func assertArchivedPlanProgress(t *testing.T, f *deliveryRunFixture, expected *reviewFeedbackRecord) {
	t.Helper()
	path := f.service.reviewFeedbackPath(f.parentID) + ".verification-" + planProgressDigest(expected)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(expected)
	if err != nil || string(data) != string(want) {
		t.Fatal("history archive changed the prior accepted assessment, gate or publication")
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("history archive is not private")
	}
}

func TestPlanHistoryRetryPreservesProofAndRunsFreshParentQA(t *testing.T) {
	f, r := acceptedNonlinearPlan(t)
	parent, children := f.parent(t), planEvidenceChildren(t, f)
	before, err := f.service.readReviewFeedbackRecord(parent)
	if err != nil {
		t.Fatal(err)
	}
	prior := *before.PlanVerification.Publication
	gateRuns, err := os.ReadFile(r.gateLog)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil || preview.HistoryRecovery == nil || *preview.HistoryRecovery != prior || preview.TargetStatus != "Agent QA" || preview.FeedbackOverride != "" {
		t.Fatalf("ordinary preview did not derive the exact protected recovery: %+v err=%v", preview, err)
	}
	if !reflect.DeepEqual(parent, f.parent(t)) || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) || !reflect.DeepEqual(before.PlanVerification, retainedPlanProgress(t, f)) {
		t.Fatal("preview changed authority, counters or accepted proof")
	}
	altered := preview
	changed := *preview.HistoryRecovery
	changed.TreeOID = strings.Repeat("f", len(changed.TreeOID))
	altered.HistoryRecovery = &changed
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), altered); err == nil {
		t.Fatal("altered history preview was applied")
	}
	// A PR base advance after preview cannot be silently accepted as the same
	// tree-only recovery. It is refused before intent or Project mutation.
	base := r.base
	r.base = strings.Repeat("f", len(base))
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err == nil {
		t.Fatal("changed current PR base was accepted against an old preview")
	}
	r.base = base
	if !reflect.DeepEqual(parent, f.parent(t)) || retainedPlanProgress(t, f).HistoryRecovery != nil {
		t.Fatal("refused preview changed lifecycle or retained recovery intent")
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	assertArchivedPlanProgress(t, f, before)
	pending := retainedPlanProgress(t, f)
	if pending.HistoryRecovery == nil || *pending.HistoryRecovery != prior || pending.Candidate.Head != prior.CommitOID || !reflect.DeepEqual(pending.Gate, before.PlanVerification.Gate) || f.parent(t).QAFailures != 3 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatal("retry reset QA, changed children or relabelled old passing proof")
	}
	// Reproduce a crash after native local normalization and before saving fresh
	// model QA. The authenticated remote/public head remains the old head.
	metadata, err := f.service.validateWorkspaceForItem(t.Context(), f.parent(t), github.DelegatedContentFor(f.parent(t)).Digest, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	provider := workspace.NewGitProvider(f.service.run)
	normalized, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, parent.Title, config.MergeMethodRebase)
	if err != nil || normalized.CommitOID == prior.CommitOID || normalized.TreeOID != prior.TreeOID {
		t.Fatalf("native construction did not preserve the accepted tree: %+v err=%v", normalized, err)
	}
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.service.workspaceForItem(t.Context(), f.parent(t), github.DelegatedContentFor(f.parent(t)).Digest, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "--no-optional-locks", "rev-parse", "HEAD")); head != normalized.CommitOID {
		t.Fatal("interrupted recovery remerged the old authenticated plan head")
	}
	if retainedPlanProgress(t, f).Candidate.Head != prior.CommitOID {
		t.Fatal("local normalization was mistaken for new QA acceptance")
	}
	// Same-tree is a strict recovery contract. Dirty source cannot be merged,
	// normalized or reviewed under this retained intent.
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "unexpected.txt"), []byte("changed source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.workspaceForItem(t.Context(), f.parent(t), github.DelegatedContentFor(f.parent(t)).Digest, f.repo); err == nil {
		t.Fatal("changed normalized source bypassed the plan synchronization guard")
	}
	if err := os.Remove(filepath.Join(prepared.WorktreePath, "unexpected.txt")); err != nil {
		t.Fatal(err)
	}
	r.retirementFeedbackPath = f.service.reviewFeedbackPath(f.parentID)
	results, err := f.service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || !strings.Contains(results[0].Error, "history recovery retirement is pending") || r.retirementArchive == "" {
		t.Fatalf("interrupted protected retirement did not retain the published candidate: %+v err=%v", results, err)
	}
	renewed := retainedPlanProgress(t, f)
	if renewed.Candidate.Head != normalized.CommitOID || renewed.Candidate.Tree != prior.TreeOID || renewed.AttemptID == pending.AttemptID || renewed.Accepted.ReviewAssessment.Verdict != "accept" || renewed.Publication == nil || renewed.HistoryRecovery == nil || *renewed.HistoryRecovery != prior || f.parent(t).QACommit != normalized.CommitOID || f.parent(t).Status != "PR Ready" || f.parent(t).QAFailures != 3 || r.reviews != 4 || r.implementations != 2 || r.creates != 1 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatal("history recovery did not run fresh parent QA and retain the exact published candidate, counters and children")
	}
	if renewed.Gate == nil || renewed.Gate.Invocation.ExecutionID == pending.Gate.Invocation.ExecutionID || renewed.Gate.Invocation.Outcome != "passed" || !renewed.Gate.Invocation.CleanupResolved {
		t.Fatal("new parent acceptance did not receive a fresh passing verification invocation")
	}
	// This unchanged-tree catalog permits protected historical heavy proof.
	// Its original execution/source binding must remain historical, never be
	// relabelled as an execution against the newly normalized commit.
	if !renewed.Gate.Historical || !reflect.DeepEqual(renewed.Gate.Receipt, pending.Gate.Receipt) || renewed.Gate.Digest != pending.Gate.Digest {
		t.Fatal("history-only recovery discarded or relabelled applicable heavy evidence")
	}
	if renewed.Publication.CommitOID != normalized.CommitOID || renewed.Publication.TreeOID != prior.TreeOID || renewed.Publication.ApprovedBaseOID != prior.ApprovedBaseOID || renewed.Publication.PlanRevision != prior.PlanRevision || renewed.Publication.VerificationDigest != renewed.EnvelopeDigest {
		t.Fatal("fresh verification and publication lost the exact normalized source, base or plan revision")
	}
	if f.cfg.Verification["complete"].CurrentCandidateCheck != nil {
		guard := renewed.Gate.CurrentCandidateCheck
		if guard == nil || guard.ExecutionID != renewed.Gate.Invocation.ExecutionID+"/current" || guard.SourceCommitOID != normalized.CommitOID || guard.SourceTreeOID != prior.TreeOID || guard.SourceBaseOID != prior.ApprovedBaseOID || guard.PlanRevision != prior.PlanRevision || guard.Outcome != "passed" || !guard.CleanupResolved {
			t.Fatal("current-candidate guard was not freshly bound to the normalized candidate")
		}
	}
	if after, err := os.ReadFile(r.gateLog); err != nil || string(after) != string(gateRuns) {
		t.Fatalf("history-only recovery repeated an applicable heavy command: %q err=%v", after, err)
	}
	assertArchivedPlanProgress(t, f, before)
	// Clear only the disposable injected archive fault. Native reconciliation
	// must finish retirement before refreshing an advanced destination base.
	if err := os.Remove(r.retirementArchive); err != nil {
		t.Fatal(err)
	}
	advanceRemoteBase(t, f.repo, "later-base.txt", "Another base update\n")
	advanced := strings.TrimSpace(runGitTest(t, f.repo, "--no-optional-locks", "--git-dir", f.remote, "rev-parse", "refs/heads/main"))
	r.base = advanced
	warnings, changedLifecycle, err := f.service.reconcilePullRequests(t.Context(), []github.WorkItem{f.parent(t)})
	if err != nil || len(warnings) != 0 || !changedLifecycle || retainedPlanProgress(t, f).HistoryRecovery != nil || r.reviews != 4 || f.parent(t).QAFailures != 3 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatalf("post-replacement reconciliation did not retire its stale lease before base refresh: warnings=%d changed=%t retained=%t reviews=%d status=%s qa=%d childrenEqual=%t err=%v", len(warnings), changedLifecycle, retainedPlanProgress(t, f).HistoryRecovery != nil, r.reviews, f.parent(t).Status, f.parent(t).QAFailures, reflect.DeepEqual(children, planEvidenceChildren(t, f)), err)
	}
	metadata, err = f.service.validateWorkspaceForItem(t.Context(), f.parent(t), github.DelegatedContentFor(f.parent(t)).Digest, f.repo)
	if err != nil || metadata.BaseRevision != advanced {
		t.Fatalf("normal base refresh did not retain the exact new base: %+v err=%v", metadata, err)
	}
	if got := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "--no-optional-locks", "rev-parse", "HEAD^")); got != advanced {
		t.Fatal("controlled rebase refresh resynchronized the previously published plan head")
	}
	runPlanEvidenceCycle(t, f)
	advancedProgress := retainedPlanProgress(t, f)
	if advancedProgress.HistoryRecovery != nil || advancedProgress.Metadata.BaseRevision != advanced || advancedProgress.Publication == nil || advancedProgress.Publication.ApprovedBaseOID != advanced || f.parent(t).Status != "PR Ready" || r.reviews != 5 || r.implementations != 2 || f.parent(t).QAFailures != 3 || !reflect.DeepEqual(children, planEvidenceChildren(t, f)) {
		t.Fatal("post-replacement base advance did not run fresh parent QA without changing prior children or counters")
	}
	assertArchivedPlanProgress(t, f, before)
}

func TestPlanHistoryTerminalMergePrecedesFreshPublicationAndWorkspace(t *testing.T) {
	f, r := acceptedNonlinearPlan(t)
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	parent := f.parent(t)
	p := retainedPlanProgress(t, f)
	metadata, err := f.service.validateWorkspaceForItem(t.Context(), parent, github.DelegatedContentFor(parent).Digest, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := workspace.NewGitProvider(f.service.run).ConstructCandidateForMergeMethod(t.Context(), metadata, parent.Title, config.MergeMethodRebase)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	// Model the valid saved fresh acceptance before its new complete gate or
	// publication exists. The old exact PR can independently become terminal.
	p.Metadata, p.Candidate, p.PreparedCandidate = metadata, snapshot, nil
	p.Assignment.Spec.ReviewCandidateOID = normalized.CommitOID
	p.AttemptID = "fresh-accepted-before-gate"
	p.Comment = formatQAComment(*p.Accepted.ReviewAssessment, normalized.CommitOID)
	p.Publication, p.Gate, p.EnvelopeDigest = nil, nil, ""
	if err := f.service.savePlanVerification(parent, github.DelegatedContentFor(parent), p); err != nil {
		t.Fatal(err)
	}
	gateRuns, err := os.ReadFile(r.gateLog)
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, f.repo, "worktree", "remove", "--force", metadata.WorktreePath)
	r.merged = true
	action, err := f.service.source.Authorize(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	_, lane := f.service.laneForItem(parent)
	result, handled := f.service.resumeAcceptedPlanPublication(t.Context(), action, lane, RunResult{Item: parent}, f.repo, "terminal-history-recovery")
	if !handled || result.Outcome != execution.OutcomeSucceeded || result.Error != "" || f.parent(t).Status != "Done" || r.reviews != 3 || r.implementations != 2 {
		t.Fatalf("old exact merge did not precede fresh gate/workspace requirements: handled=%t result=%+v parent=%s", handled, result, f.parent(t).Status)
	}
	if after, err := os.ReadFile(r.gateLog); err != nil || string(after) != string(gateRuns) {
		t.Fatal("confirmed terminal delivery ran another complete gate")
	}
}
