package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
)

type planRepairRecord struct {
	Revision       string                     `json:"revision"`
	Candidate      string                     `json:"candidate"`
	IntegratedHead string                     `json:"integrated_head"`
	Failures       int                        `json:"failures"`
	Assessment     execution.ReviewAssessment `json:"assessment"`
}

func (r planRepairRecord) digest() string {
	b, _ := json.Marshal(r)
	return fmt.Sprintf("v1:%x", sha256.Sum256(b))
}

func validatePlanRepairCoverage(delivery github.PlanDelivery, assessment execution.ReviewAssessment) error {
	if assessment.Verdict != "needs_changes" {
		return errors.New("whole-plan repair requires a concrete rejection")
	}
	failed := map[string]bool{}
	for index, criterion := range delivery.Manifest.SuccessCriteria {
		for _, check := range assessment.Criteria {
			if check.Criterion == criterion && check.Status == "failed" {
				failed[fmt.Sprintf("P%d", index+1)] = true
			}
		}
	}
	for _, rule := range assessment.Rules {
		if rule.Status == "failed" {
			failed["R"] = true
		}
	}
	if assessment.Maintainability.Status == "failed" {
		failed["M"] = true
	}
	covered := map[string]bool{}
	members := map[string]bool{}
	for _, member := range delivery.Children {
		members[member.ID] = true
	}
	for _, target := range assessment.RepairTargets {
		if !target.InScope || !members[target.ItemID] || !failed[target.CheckKey] || strings.TrimSpace(target.Finding) == "" {
			return errors.New("repair needs an approved in-scope owning member for every failed check; amendment or input is required")
		}
		covered[target.CheckKey] = true
	}
	if len(failed) == 0 || len(covered) != len(failed) {
		return errors.New("whole-plan defect ownership is incomplete; no repair was authorized")
	}
	return nil
}

func (s *Engine) rejectPlan(ctx context.Context, action github.AuthorizedAction, assignment execution.Assignment, assessment execution.ReviewAssessment, baseline *execution.ReviewBaseline, lane config.ResolvedWorkflowLane, result RunResult) RunResult {
	delivery, present, err := s.source.DeliveryForItem(ctx, action.Item)
	if err != nil || !present || delivery.Parent.ID != action.Item.ID {
		return s.failExecution(ctx, action, lane, result, "Whole-plan rejection lost current authority", err, integrityViolationOutput("Whole-plan rejection lost current authority", err))
	}
	content, err := action.DelegatedContent()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	repair := planRepairRecord{Revision: delivery.Revision, Candidate: baseline.CommitOID, IntegratedHead: delivery.Parent.QACommit, Failures: action.Item.QAFailures + 1, Assessment: assessment}
	if err := s.saveReviewFeedback(action.Item, content, assessment, baseline, repair); err != nil {
		return s.failExecution(ctx, action, lane, result, "Whole-plan rejection evidence could not be retained", err, blockedExecutorOutput("Whole-plan rejection evidence could not be retained", err))
	}
	if err = execution.ValidateReviewBaseline(assignment.Spec, baseline); err == nil {
		err = validatePlanRepairCoverage(delivery, assessment)
	}
	if err != nil || repair.Failures >= lane.MaxQARejections {
		detail := "Whole-plan QA requires operator input: retained findings need exact in-scope ownership, or the configured rejection allowance is exhausted."
		qaLane, _ := s.laneForItem(action.Item)
		if updateErr := s.transitionRejection(ctx, action, s.cfg.LaneStatus(lane.Transitions[config.WorkflowOutcomeExhausted]), qaLane, detail, repair.Failures); updateErr != nil {
			err = errors.Join(err, updateErr)
		}
		result.Outcome = execution.OutcomeBlocked
		result.Summary = detail
		if err != nil {
			result.Error = err.Error()
		}
		return result
	}
	if err = s.source.BeginPlanRepair(ctx, action, repair.digest(), repair.Candidate, repair.Failures); err == nil {
		delivery, _, err = s.source.DeliveryForItem(ctx, action.Item)
		if err == nil {
			err = s.resumePlanRepair(ctx, delivery)
		}
	}
	result.Outcome = config.WorkflowOutcomeRejected
	result.Summary = "Whole-plan QA requested bounded repairs from the existing approved owning cards."
	if err != nil {
		result.Outcome = execution.OutcomeBlocked
		result.Error = err.Error()
		result.Summary = "Whole-plan repair transition is retained for safe reconciliation."
	}
	return result
}

func (s *Engine) resumePlanRepair(ctx context.Context, delivery github.PlanDelivery) error {
	parent := delivery.Parent
	action, err := s.source.Authorize(ctx, parent)
	if err != nil {
		return err
	}
	content, err := action.DelegatedContent()
	if err != nil {
		return err
	}
	feedback, err := s.loadReviewFeedbackRecord(parent, content)
	if err != nil || feedback == nil || feedback.PlanRepair == nil {
		return errors.Join(errors.New("pending plan repair lost its protected rejection record"), err)
	}
	r := *feedback.PlanRepair
	if r.Revision != delivery.Revision || r.IntegratedHead != parent.QACommit || !reviewObjectID(r.Candidate) || r.Failures != parent.QAFailures || parent.Result != "Plan repair "+r.digest() {
		return errors.New("pending plan repair identity changed")
	}
	if err := validatePlanRepairCoverage(delivery, r.Assessment); err != nil {
		return err
	}
	ownerFindings := map[string][]string{}
	for _, target := range r.Assessment.RepairTargets {
		ownerFindings[target.ItemID] = append(ownerFindings[target.ItemID], target.CheckKey+": "+target.Finding)
	}
	marker := "Repair approved plan candidate " + r.digest()
	// Validate every affected member before the first transition. Ready items
	// with this exact marker are previously completed transitions, not a retry.
	for _, child := range delivery.Children {
		if ownerFindings[child.ID] != nil && child.Phase != github.PlanIntegratedPhase && !(child.Status == s.cfg.LaneStatus(s.cfg.Workflow.ReadyLane) && child.Result == marker) {
			return errors.New("plan repair owner changed state; refusing partial admission")
		}
	}
	for _, child := range delivery.Children {
		findings := ownerFindings[child.ID]
		if findings == nil || child.Result == marker {
			continue
		}
		current, err := s.source.Authorize(ctx, child)
		if err != nil {
			return err
		}
		bound, err := current.DelegatedContent()
		if err != nil {
			return err
		}
		existing, err := s.loadReviewFeedbackRecord(current.Item, bound)
		if err != nil {
			return err
		}
		if existing == nil {
			existing = &reviewFeedbackRecord{Version: reviewFeedbackVersion, ItemID: child.ID, DelegatedContentDigest: bound.Digest, Items: []string{}}
		}
		detail := marker + "\n" + strings.Join(findings, "\n")
		found := false
		for _, item := range existing.Items {
			if item == detail {
				found = true
			}
		}
		if !found {
			existing.Items = append(existing.Items, detail)
		}
		if err := s.writeReviewFeedback(*existing); err != nil {
			return err
		}
		if err := s.transitionAfterBranchUpdate(ctx, current, s.cfg.LaneStatus(s.cfg.Workflow.ReadyLane), s.phaseForTargetLane(s.cfg.Workflow.ReadyLane), marker); err != nil {
			return err
		}
	}
	action, err = s.source.Authorize(ctx, parent)
	if err != nil {
		return err
	}
	return s.source.FinishPlanRepair(ctx, action, r.digest())
}
