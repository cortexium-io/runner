package engine

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/verification"
	"github.com/cortexium-io/runner/internal/workspace"
)

func (s *Engine) continuePlanVerification(ctx context.Context, action github.AuthorizedAction, lane config.ResolvedWorkflowLane, result RunResult, p *planVerificationProgress, attemptID string) RunResult {
	fail := func(summary string, err error) RunResult {
		return s.failExecution(ctx, action, lane, result, summary, err, blockedExecutorOutput(summary, err))
	}
	fresh, err := s.revalidatePlanProgress(ctx, action, p)
	if err != nil {
		return fail("Retained whole-plan acceptance requires renewed assessment", err)
	}
	action = fresh
	content, err := action.DelegatedContent()
	if err != nil {
		return fail("Whole-plan authority is unavailable", err)
	}
	if p.Classification != nil || p.Failure != nil {
		return s.classifyPlanVerificationFailure(ctx, action, lane, result, p, attemptID)
	}
	observed, gateErr := s.runPlanVerification(ctx, action, p, attemptID)
	p.Gate, p.Failure, p.EnvelopeDigest = &observed, nil, ""
	if observed.Receipt != nil || observed.CurrentCandidateCheck != nil {
		p.EnvelopeDigest, err = observed.Evidence().Digest()
		gateErr = errors.Join(gateErr, err)
	}
	var checkFailure *verification.CheckFailure
	if err == nil && errors.As(gateErr, &checkFailure) {
		p.Failure = &planVerificationFailure{Phase: checkFailure.Phase, ExecutionID: checkFailure.ExecutionID, ExitCode: checkFailure.ExitCode}
	}
	if err := s.savePlanVerification(action.Item, content, p); err != nil {
		return fail("Whole-plan gate observations could not be retained safely", err)
	}
	if gateErr != nil {
		if p.Failure != nil {
			return s.classifyPlanVerificationFailure(ctx, action, lane, result, p, attemptID)
		}
		return fail("Whole-plan verification stopped without a classifiable command failure", gateErr)
	}
	if observed.Receipt == nil || observed.Receipt.Outcome != "passed" || observed.Invocation.Outcome != "passed" || !observed.Invocation.CleanupResolved {
		return fail("Whole-plan verification has no passing complete evidence", errors.New("complete gate did not pass"))
	}
	encoded, err := json.Marshal(observed.Evidence())
	if err != nil {
		return fail("Complete verification could not be encoded", err)
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	if p.Publication == nil {
		// Recovery may find the immutable record after its write succeeded but
		// before saving its reference in parent progress.
		existing, found, loadErr := provider.LoadPublicationAcceptance(ctx, p.Metadata, p.Candidate, workspace.PublicationEvidence{PlanRevision: p.Assignment.Spec.PlanContext.Revision})
		if loadErr != nil {
			return fail("Final publication acceptance could not be recovered", loadErr)
		}
		if found {
			p.Publication = &existing
		}
	}
	if p.Publication == nil {
		record, err := provider.RecordPublicationAcceptance(ctx, p.Metadata, p.Candidate, p.Report, p.Comment, workspace.PublicationEvidence{PlanRevision: p.Assignment.Spec.PlanContext.Revision, VerificationReceipt: string(encoded), VerificationDigest: p.EnvelopeDigest})
		if err != nil {
			return fail("QA and complete proof could not be bound for publication", err)
		}
		p.Publication = &record
	}
	if err := s.savePlanVerification(action.Item, content, p); err != nil {
		return fail("Final publication reference could not be retained", err)
	}
	if err := s.validateCompletePlanEvidence(ctx, action, p.Metadata, *p.Publication); err != nil {
		return fail("Final publication proof is not applicable", err)
	}
	lineage := observedLineage(&result)
	lineage.ReviewedCandidate = metrics.ObjectIdentity{CommitOID: p.Candidate.Head, TreeOID: p.Candidate.Tree}
	lineage.EvidenceCandidate = lineage.ReviewedCandidate
	return s.publishAcceptedQA(ctx, action, lane, result, p.Metadata.RepoRoot, p.Metadata, *p.Publication)
}

func (s *Engine) classifyPlanVerificationFailure(ctx context.Context, action github.AuthorizedAction, lane config.ResolvedWorkflowLane, result RunResult, p *planVerificationProgress, attemptID string) RunResult {
	fail := func(summary string, err error) RunResult {
		return s.failExecution(ctx, action, lane, result, summary, err, blockedExecutorOutput(summary, err))
	}
	diagnostic, err := p.failureDiagnostics()
	if err != nil {
		return fail("Whole-plan failure diagnostics are not safe to classify", err)
	}
	if p.classificationPending() {
		return fail("Whole-plan failure classification was already spent; its outcome is uncertain", errors.New("refusing to repeat an unconfirmed reviewer invocation"))
	}
	if p.Classification == nil {
		if action.Item.QAFailures != p.QAFailures || p.QAFailures >= lane.MaxQARejections {
			return fail("Whole-plan failure classification allowance is unavailable", errors.New("QA allowance changed or was exhausted"))
		}
		provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
		candidate := workspace.Candidate{CommitOID: p.Candidate.Head, TreeOID: p.Candidate.Tree}
		review, err := provider.PrepareReviewWorkspace(ctx, p.Metadata, candidate)
		if err != nil {
			return fail("Failure-classification workspace could not be prepared", err)
		}
		cleanup := true
		defer func() {
			if !cleanup {
				return
			}
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = review.Cleanup(cleanup)
		}()
		before, err := s.checkoutSnapshotState(ctx, review.Path)
		if err != nil {
			return fail("Failure-classification workspace could not be protected", err)
		}
		if err := s.verifyPlanProgressCandidate(ctx, action, p); err != nil {
			return fail("Whole-plan failure authority changed", err)
		}
		cfg := s.executionConfig(p.ReviewerRole, s.roleHarness(p.ReviewerRole), review.Path)
		deadline := time.Now().UTC().Add(time.Duration(cfg.Harness.TimeoutSeconds) * time.Second)
		if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
			deadline = limit.UTC()
		}
		p.Classification = &planVerificationClassification{AttemptID: attemptID, WorkspacePath: review.Path, Deadline: deadline, FailureDigest: p.failureDigest()}
		content, err := action.DelegatedContent()
		if err != nil {
			return fail("Whole-plan classification lost authority", err)
		}
		if err := s.savePlanVerification(action.Item, content, p); err != nil {
			return fail("Whole-plan classification allowance could not be durably spent", err)
		}
		callCtx, cancel := context.WithDeadline(ctx, deadline)
		output, callErr := execution.ReviewPlanVerificationFailure(callCtx, s.roleHarness(p.ReviewerRole), cfg, p.Assignment, string(diagnostic), s.run)
		cancel()
		// Record the actual provider outcome before any Project mutation, even
		// for cancellation or unavailable/partial usage. On replay it is history,
		// not newly consumed tokens or a second invocation.
		p.Classification.Result, p.Classification.Failed = &output, callErr != nil
		var checkErr error
		if output.FailureClass == execution.FailureCleanupUnresolved {
			cleanup = false // A surviving owned process may still use this checkout.
			s.processOwnership.RecordCleanupFailure()
			p.Classification.Failed = true
			callErr = errors.Join(callErr, errors.New("classifier cleanup unresolved; retained owned workspace: "+review.Path))
		} else {
			verifyCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			after, err := s.checkoutSnapshotState(verifyCtx, review.Path)
			checkErr = err
			if checkErr == nil && (after.Fingerprint != before.Fingerprint || after.Head != before.Head) {
				checkErr = errors.New("classification changed its read-only review candidate")
			}
			checkErr = errors.Join(checkErr, s.verifyPlanProgressCandidate(verifyCtx, action, p))
			done()
		}
		p.Classification.Failed = p.Classification.Failed || checkErr != nil
		result.Usage = result.Usage.Add(output.Usage)
		result.HarnessDurationMilliseconds += output.HarnessDurationMilliseconds
		if err := s.savePlanVerification(action.Item, content, p); err != nil {
			if callErr != nil {
				return s.failExecution(ctx, action, lane, result, "Whole-plan classification failure could not be retained", errors.Join(callErr, err), output)
			}
			return fail("Whole-plan classification result could not be retained", err)
		}
		if callErr != nil {
			return s.failExecution(ctx, action, lane, result, "Whole-plan classification did not complete safely", errors.Join(callErr, checkErr), output)
		}
		if checkErr != nil {
			return fail("Whole-plan classification did not preserve its candidate", checkErr)
		}
	}
	output := *p.Classification.Result
	if output.FailureClass == execution.FailureCleanupUnresolved {
		s.processOwnership.RecordCleanupFailure()
		return s.failExecution(ctx, action, lane, result, "Retained classifier cleanup remains unresolved", errors.New("inspect retained classifier workspace before recovery: "+p.Classification.WorkspacePath), output)
	}
	if err := s.verifyPlanProgressCandidate(ctx, action, p); err != nil {
		return fail("Whole-plan classification no longer applies to the current candidate", err)
	}
	if p.Classification.Failed || execution.ValidateReviewOutput(p.Assignment, output) != nil || output.ReviewAssessment == nil || output.ReviewAssessment.Verdict != "needs_changes" {
		return fail("Failed complete verification requires input or an amendment", errors.New("classification supplied no valid concrete in-scope rejection; failed proof cannot be accepted"))
	}
	for _, criterion := range output.ReviewAssessment.Criteria {
		if criterion.Status == "blocked" {
			return fail("Failed complete verification still has unanswered proof questions", errors.New("one-shot classification cannot launch focused verification"))
		}
	}
	for _, rule := range output.ReviewAssessment.Rules {
		if rule.Status == "blocked" {
			return fail("Failed complete verification still has unanswered repository questions", errors.New("one-shot classification cannot launch focused verification"))
		}
	}
	if output.ReviewAssessment.Maintainability.Status == "blocked" {
		return fail("Failed complete verification still has unanswered engineering questions", errors.New("one-shot classification cannot launch focused verification"))
	}
	delivery, _, err := s.planGate(ctx, action)
	if err != nil {
		return fail("Whole-plan repair authority changed", err)
	}
	if err := validatePlanRepairCoverage(delivery, *output.ReviewAssessment); err != nil {
		return fail("Whole-plan failure needs approved ownership or an amendment", err)
	}
	if action.Item.QAFailures != p.QAFailures {
		// A recorded rejection may already have charged the allowance. Its
		// authenticated repair intent is reconciled by the existing repair path.
		if action.Item.QAFailures == p.QAFailures+1 {
			result.Outcome = execution.OutcomeBlocked
			result.Summary = "The retained whole-plan rejection was already charged; no classification or rejection was repeated."
			return result
		}
		return fail("Whole-plan rejection allowance changed", errors.New("refusing to reset or charge an unrelated QA allowance"))
	}
	baseline := &execution.ReviewBaseline{Assessment: *output.ReviewAssessment, CommitOID: p.Candidate.Head, BaseOID: p.Metadata.BaseRevision, BindingDigest: reviewBaselineBindingDigest(p.Assignment.Spec), CommentContext: append([]string{}, p.Assignment.Spec.ReviewCommentContext...)}
	result.ReviewVerdict = output.ReviewAssessment.Verdict
	result.ReviewFindings = reviewFindingObservations(*output.ReviewAssessment)
	return s.rejectPlan(ctx, action, p.Assignment, *output.ReviewAssessment, baseline, lane, result)
}

func (s *Engine) verifyPlanProgressCandidate(ctx context.Context, action github.AuthorizedAction, p *planVerificationProgress) error {
	if _, err := s.revalidatePlanProgress(ctx, action, p); err != nil {
		return err
	}
	if err := s.verifyProgressPlanHead(ctx, action, p); err != nil {
		return err
	}
	snapshot, err := s.checkoutSnapshotState(ctx, p.Metadata.WorktreePath)
	if err != nil {
		return err
	}
	if !snapshot.Clean || snapshot.Head != p.Candidate.Head || snapshot.Tree != p.Candidate.Tree || snapshot.Fingerprint != p.Candidate.Fingerprint {
		return errors.New("accepted plan candidate changed")
	}
	source, err := s.checkoutSnapshotState(ctx, p.Metadata.RepoRoot)
	if err != nil {
		return err
	}
	if source.Fingerprint != p.Metadata.SourceSnapshot {
		return errors.New("active source checkout changed")
	}
	return nil
}
