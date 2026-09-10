package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

// QAReauthorizationPlan is an operator preview, not a transferable approval or
// AuthorizedAction. Its private, single-use binding lives only in this CLI run.
// No normal workflow state or retained workspace identity is rewritten.
type QAReauthorizationPlan struct {
	Item                 github.WorkItem                  `json:"item"`
	Role                 string                           `json:"role"`
	Workspace            workspace.Identity               `json:"workspace"`
	Candidate            workspace.Candidate              `json:"candidate"`
	Snapshot             workspace.Snapshot               `json:"snapshot"`
	SourceSnapshot       string                           `json:"source_snapshot"`
	PullRequest          github.PullRequestDetails        `json:"pull_request"`
	CurrentContentDigest string                           `json:"current_content_digest"`
	References           []config.RepositoryReference     `json:"references"`
	Comments             []string                         `json:"comments"`
	Feedback             []string                         `json:"historical_feedback"`
	Verification         []execution.VerificationEvidence `json:"recorded_verification,omitempty"`
	seal                 *qaReviewSeal
}

type qaReviewSeal struct {
	owner  *Engine
	digest [32]byte
	config [32]byte
	used   atomic.Bool
}

type QAReauthorizationResult struct {
	RunResult
	Assessment *execution.ReviewAssessment `json:"assessment,omitempty"`
}

func (s *Engine) PlanQAReauthorization(ctx context.Context, selector string) (QAReauthorizationPlan, error) {
	plan, _, err := s.inspectQAReauthorization(ctx, selector)
	if err != nil {
		return QAReauthorizationPlan{}, err
	}
	plan.seal = &qaReviewSeal{owner: s, digest: qaPreviewDigest(plan), config: qaPreviewDigest(s.cfg)}
	return plan, nil
}

func qaPreviewDigest(value any) [32]byte {
	encoded, _ := json.Marshal(value)
	return sha256.Sum256(encoded)
}

func (s *Engine) inspectQAReauthorization(ctx context.Context, selector string) (QAReauthorizationPlan, workspace.Metadata, error) {
	fail := func(err error) (QAReauthorizationPlan, workspace.Metadata, error) {
		return QAReauthorizationPlan{}, workspace.Metadata{}, err
	}
	item, err := s.source.InspectRecoveryItem(ctx, selector)
	if err != nil {
		return fail(err)
	}
	lane, exists := s.cfg.Workflow.Lanes[item.Phase]
	if !exists || s.cfg.RoleContract(lane.Role) != config.WorkRoleReviewer ||
		!strings.EqualFold(item.Status, s.cfg.GitHubProject.BlockedStatus) || item.Approval != "" ||
		item.Branch == "" || item.PullRequest == "" || item.Transition != "" || item.Activity != "" ||
		item.Body == "" || item.PlanningMetadataInvalid || item.DraftContentID != "" || item.QAFailures < 0 {
		return fail(errors.New("QA-only reauthorization requires a paused Blocked reviewer-phase card with missing approval, retained branch and PR, and no active transition; fresh staged cards must use batch approval"))
	}
	repo, err := s.repositoryDir(ctx, item.Repository)
	if err != nil {
		return fail(err)
	}
	content := github.DelegatedContentFor(item)
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	metadata, err := provider.InspectRetainedReview(ctx, s.workspaceRequestForItem(item, content.Digest, repo, false))
	if err != nil {
		return fail(fmt.Errorf("inspect retained QA workspace: %w", err))
	}
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return fail(err)
	}
	if !snapshot.Clean || snapshot.Branch != metadata.BranchName || !reviewObjectID(snapshot.Head) || !reviewObjectID(snapshot.Tree) {
		return fail(errors.New("QA-only reauthorization requires the exact clean committed candidate on its retained branch"))
	}
	if _, err := s.git(ctx, []string{"merge-base", "--is-ancestor", metadata.BaseRevision, snapshot.Head}, metadata.WorktreePath, 30*time.Second); err != nil {
		return fail(fmt.Errorf("retained QA base is not an ancestor of the candidate: %w", err))
	}
	pr, err := github.NewPullRequestManager(s.run, s.source).InspectForRecovery(ctx, item.Repository, item.PullRequest)
	if err != nil {
		return fail(err)
	}
	if pr.State != "OPEN" || pr.AutoMergeEnabled {
		return fail(errors.New("QA-only reauthorization requires an open PR with auto-merge disabled"))
	}
	if err := github.ValidateTrackedPullRequest(pr, item.Repository, metadata.BranchName, snapshot.Head, s.baseBranch(), ""); err != nil {
		return fail(err)
	}
	if item.QACommit != "" && item.QACommit != snapshot.Head {
		return fail(errors.New("retained QA commit differs from the candidate; review the inconsistent state before reauthorizing"))
	}
	refs, err := workspace.ValidateRepositoryReferences(ctx, s.run, s.cfg.RepositoryReferences, []string{repo, s.implementationWorkspaceRoot()})
	if err != nil {
		return fail(err)
	}
	comments, err := s.source.ItemComments(ctx, item)
	if err != nil {
		return fail(err)
	}
	feedback, err := s.readReviewFeedbackRecord(item)
	if err != nil {
		return fail(err)
	}
	plan := QAReauthorizationPlan{
		Item: item, Role: lane.Role, Workspace: metadata.Identity, Candidate: workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree},
		Snapshot: snapshot, SourceSnapshot: metadata.SourceSnapshot, PullRequest: pr, CurrentContentDigest: content.Digest,
		References: refs, Comments: humanCommentContext(comments),
	}
	if feedback != nil {
		if feedback.DelegatedContentDigest != metadata.Identity.DelegatedContentDigest {
			return fail(errors.New("historical QA feedback does not match the retained workspace content identity"))
		}
		plan.Feedback = append([]string(nil), feedback.Items...)
	}
	// Changed requirements need renewed proof. Keep the old evidence file intact
	// rather than rebinding it or claiming it establishes the new contract.
	if content.Digest == metadata.Identity.DelegatedContentDigest {
		plan.Verification, err = s.loadVerificationEvidence(item, content, metadata, plan.Candidate, approvedVerificationContract(content.BodySnapshot))
		if err != nil {
			return fail(err)
		}
	}
	return plan, metadata, nil
}

// RunQAReauthorization consumes the confirmed preview exactly once. A failure
// needs a new preview and confirmation; it can never schedule a retry or mint
// workflow/publication authority, including when the review accepts.
func (s *Engine) RunQAReauthorization(ctx context.Context, plan QAReauthorizationPlan) (result QAReauthorizationResult, err error) {
	seal := plan.seal
	if seal == nil || seal.owner != s || qaPreviewDigest(plan) != seal.digest || qaPreviewDigest(s.cfg) != seal.config || !seal.used.CompareAndSwap(false, true) {
		return result, errors.New("QA-only preview is missing, modified, or already consumed; preview and confirm again")
	}
	lock, err := github.AcquireQAReviewLock(s.cfg.GitHubProject.GitHubProjectConfig, plan.Item.ID)
	if err != nil {
		return result, err
	}
	defer lock.Release()
	fresh, metadata, err := s.inspectQAReauthorization(ctx, plan.Item.ID)
	if err != nil {
		return result, err
	}
	if qaPreviewDigest(fresh) != seal.digest {
		return result, errors.New("card, PR, candidate, workspace, references, or review context changed after preview; preview and confirm again")
	}
	guard, err := s.acquireLocalAdmission(ctx, true)
	if err != nil {
		return result, err
	}
	defer guard.Release()
	admission, err := s.AdmissionStatus(time.Now().UTC())
	if err != nil {
		return result, err
	}
	if !admission.Allowed {
		return result, fmt.Errorf("agent admission paused: %s", admission.Summary())
	}
	slot, err := s.acquireLocalExecutionSlot()
	if err != nil {
		return result, err
	}
	defer slot.Release()
	item := plan.Item
	item.Role = plan.Role
	event := s.newItemAttempt(item)
	if err := s.recordAttemptStart(event); err != nil && s.cfg.AdmissionBudget != nil {
		return result, err
	}
	if err := guard.Release(); err != nil {
		return result, err
	}
	trace := metrics.NewAttemptTrace(s.observeMetrics, event)
	ctx = metrics.WithAttemptTrace(ctx, trace)
	result.RunResult = RunResult{Item: plan.Item, Harness: event.Harness, CandidateOID: plan.Candidate.CommitOID, WorktreePath: metadata.WorktreePath, Branch: metadata.BranchName, Outcome: execution.OutcomeBlocked, RetryDisposition: string(execution.RetryNone)}
	defer func() {
		result.StartedAt, result.FinishedAt = event.StartedAt, time.Now().UTC()
		result.DurationMilliseconds = result.FinishedAt.Sub(result.StartedAt).Milliseconds()
		if err != nil {
			result.Error = err.Error()
			result.Outcome = execution.OutcomeBlocked
		}
		if s.observeMetrics != nil {
			event.Kind, event.FinishedAt = metrics.EventCompleted, result.FinishedAt
			event.DurationMilliseconds, event.HarnessDurationMilliseconds = result.DurationMilliseconds, result.HarnessDurationMilliseconds
			event.Outcome, event.Summary, event.FailureClass = result.Outcome, "One-shot operator QA finished; card remains paused.", result.FailureClass
			event.RetryDisposition, event.Usage, event.CandidateOID = string(execution.RetryNone), result.Usage, result.CandidateOID
			event.PromptContexts = trace.PromptContexts()
			event.WorkDone, event.Verification = result.WorkDone, result.Verification
			event.ReviewFindings = result.ReviewFindings
			if observeErr := s.observeMetrics(event); observeErr != nil {
				result.MetricsError = observeErr.Error()
			}
		}
	}()
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	review, err := provider.PrepareReviewWorkspace(ctx, metadata, plan.Candidate)
	if err != nil {
		return result, err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if cleanupErr := review.Cleanup(cleanupCtx); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	reviewSnapshot, err := s.checkoutSnapshotState(ctx, review.Path)
	if err != nil {
		return result, err
	}
	if !reviewSnapshot.Clean || reviewSnapshot.Head != plan.Candidate.CommitOID || reviewSnapshot.Tree != plan.Candidate.TreeOID {
		return result, errors.New("private review workspace is not the exact previewed clean candidate")
	}
	assignment := s.assignment(item, github.DelegatedContentFor(item), plan.Feedback, plan.Comments)
	assignment.Spec.ReviewOnly = true
	assignment.Spec.ReviewBaseOID, assignment.Spec.ReviewCandidateOID = metadata.BaseRevision, plan.Candidate.CommitOID
	assignment.Spec.RecordedVerification = plan.Verification
	output, runErr := s.runReviewer(ctx, item.Role, review.Path, assignment)
	result.Usage, result.HarnessDurationMilliseconds = output.Usage, output.HarnessDurationMilliseconds
	result.WorkDone, result.Verification = output.WorkDone, output.Verification
	result.FailureClass = string(output.FailureClass)
	verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	currentReview, err := s.checkoutSnapshotState(verifyCtx, review.Path)
	if err != nil {
		result.FailureClass = string(execution.FailureIntegrityUnverified)
		return result, errors.Join(runErr, fmt.Errorf("QA integrity could not be verified: %w", err))
	}
	if currentReview.Fingerprint != reviewSnapshot.Fingerprint {
		result.FailureClass = string(execution.FailureIntegrityViolation)
		return result, errors.Join(runErr, snapshotChangeError("review workspace changed", reviewSnapshot, currentReview))
	}
	// Re-read GitHub and private bindings after the review. A concurrent human
	// change invalidates this result rather than silently widening its approval.
	after, _, inspectErr := s.inspectQAReauthorization(verifyCtx, plan.Item.ID)
	if inspectErr != nil {
		result.FailureClass = string(execution.FailureIntegrityUnverified)
		return result, errors.Join(runErr, fmt.Errorf("QA-only context could not be revalidated; no acceptance recorded: %w", inspectErr))
	}
	if qaPreviewDigest(after) != seal.digest || qaPreviewDigest(s.cfg) != seal.config {
		result.FailureClass = string(execution.FailureIntegrityViolation)
		return result, errors.Join(runErr, errors.New("QA-only context changed during review; no acceptance recorded"))
	}
	if ctx.Err() != nil {
		result.FailureClass = string(execution.FailureCanceled)
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.FailureClass = string(execution.FailureTimeout)
		}
		return result, errors.Join(runErr, ctx.Err())
	}
	if runErr != nil {
		return result, runErr
	}
	result.Outcome, result.Summary, result.Assessment = output.Outcome, output.Summary, output.ReviewAssessment
	if output.ReviewAssessment != nil {
		result.ReviewFindings = reviewFindingObservations(*output.ReviewAssessment)
	}
	if output.ReviewAssessment != nil && output.ReviewAssessment.Verdict == "needs_changes" {
		result.Outcome = config.WorkflowOutcomeRejected
	}
	return result, nil
}

func (s *Engine) runReviewer(ctx context.Context, role, path string, assignment execution.Assignment) (execution.Output, error) {
	harness := s.roleHarness(role)
	cfg := s.executionConfig(role, harness, path)
	switch harness {
	case config.HarnessCodexCLI:
		return execution.NewCodexExecutor(cfg, s.run).Execute(ctx, assignment)
	case config.HarnessClaudeCLI, config.HarnessPiCLI:
		return execution.NewAgentExecutor(harness, cfg, s.run).Execute(ctx, assignment)
	default:
		return execution.Output{}, fmt.Errorf("unsupported reviewer harness %q", harness)
	}
}
