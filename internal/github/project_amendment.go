package github

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/config"
)

// AmendmentPlan changes the approved requirements, never scheduling metadata or
// sibling authority. Execution remains paused until a separate ordinary retry.
type AmendmentPlan struct {
	Item      WorkItem `json:"item"`
	Body      string   `json:"replacement_body"`
	RetryLane string   `json:"retry_lane"`
	Unstarted bool     `json:"unstarted"`

	after          AuthorizedAction
	source         WorkItem
	sourceApproval string
}

const unstartedAmendmentResult = "Operator amended the approved requirements before execution. QA failure count retained; explicit retry required."

func (s *Project) PlanAmendment(ctx context.Context, selector, body string) (AmendmentPlan, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 60_000 || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return AmendmentPlan{}, errors.New("replacement body must be nonempty UTF-8 text of at most 60000 bytes without NUL")
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return AmendmentPlan{}, err
	}
	item, err := selectProjectItem(items, selector)
	if err != nil {
		return AmendmentPlan{}, err
	}
	action, err := s.validateAction(item)
	if err != nil {
		return AmendmentPlan{}, fmt.Errorf("amendment requires intact existing approval: %w", err)
	}
	unstarted := item.PlanningSourceLane == "local_plan" && item.PlanningSourceID == "" &&
		item.Branch == "" && item.Phase == "" && item.QAFailures == 0 &&
		(item.Result == "" || item.Result == unstartedAmendmentResult)
	retryLane := item.Phase
	if unstarted {
		retryLane, _ = s.uniqueAgentLaneForRole(action.Role)
	}
	inactive := item.Activity == "" || (unstarted && item.Activity == config.RunnerActivityWaitingForDependencies)
	if item.PlanningMetadataInvalid || item.PlanRelease != "" || item.PullRequest != "" || item.QACommit != "" || !inactive ||
		(!strings.EqualFold(item.Status, s.blockedStatus()) && !s.agentStatus(item.Status)) || retryLane == "" ||
		!s.agentStatus(s.cfg.LaneStatuses[retryLane]) || strings.EqualFold(item.IssueState, "CLOSED") {
		return AmendmentPlan{}, errors.New("amendment requires paused unpublished work or a released unstarted local-plan member, with intact approval and no active assignment or QA acceptance")
	}
	if !s.isIntakeIssueURL(item.URL) {
		return AmendmentPlan{}, errors.New("amendment requires an issue-backed card in the configured repository")
	}
	if body == item.Body {
		return AmendmentPlan{}, errors.New("replacement body is unchanged")
	}
	oldMetadata, oldPresent, oldErr := decodePlannedItemMetadata(item.Body)
	newMetadata, newPresent, newErr := decodePlannedItemMetadata(body)
	var oldDeps, newDeps []string
	var oldDepsErr, newDepsErr error
	var oldProfile, newProfile string
	var oldProfileErr, newProfileErr error
	if !oldPresent {
		oldDeps, _, oldDepsErr = decodeManualDependencies(item.Body)
		oldProfile, oldProfileErr = ManualImplementationProfile(item.Body)
	}
	if !newPresent {
		newDeps, _, newDepsErr = decodeManualDependencies(body)
		newProfile, newProfileErr = ManualImplementationProfile(body)
	}
	if oldErr != nil || newErr != nil || oldPresent != newPresent || !reflect.DeepEqual(oldMetadata, newMetadata) ||
		oldProfileErr != nil || newProfileErr != nil || oldProfile != newProfile ||
		oldDepsErr != nil || newDepsErr != nil || !reflect.DeepEqual(oldDeps, newDeps) {
		return AmendmentPlan{}, errors.New("amendment cannot change repository, dependencies, execution profile, or Runner planning metadata")
	}
	index := newWorkItemIndex(items)
	if reason, detail := s.planningBatchEligibilityIn(item, index); reason != "" {
		return AmendmentPlan{}, errors.New(detail)
	}
	next := item
	next.Body, next.Status = body, s.blockedStatus()
	next.Result = "Operator amended the approved requirements. Candidate and QA failure count retained; explicit retry required."
	if unstarted {
		next.Result = unstartedAmendmentResult
	}
	state, err := s.stateForStatus(next.Status)
	if err != nil {
		return AmendmentPlan{}, err
	}
	after, err := s.signAction(next, action.Role, state)
	if err != nil {
		return AmendmentPlan{}, err
	}
	plan := AmendmentPlan{Item: item, Body: body, RetryLane: retryLane, Unstarted: unstarted, after: after}
	if item.PlanningSourceID != "" {
		plan.source = index.byID[item.PlanningSourceID]
		children := append([]WorkItem(nil), index.childrenBySource[item.PlanningSourceID]...)
		previous, err := s.validatePlanningBatch(plan.source.Approval, plan.source, children, batchReleasedState)
		if err != nil {
			return AmendmentPlan{}, err
		}
		for i := range children {
			if children[i].ID == item.ID {
				children[i] = after.Item
			}
		}
		plan.sourceApproval, err = s.signPlanningBatch(plan.source, children, batchReleasedState, previous.Generation)
		if err != nil {
			return AmendmentPlan{}, err
		}
	}
	return plan, nil
}

// ApplyAmendment runs only after the engine has revalidated and rebound the
// retained workspace under its operator mutation guard. Ambiguous writes are
// read back; rollback never overwrites an intervening operator edit.
func (s *Project) ApplyAmendment(ctx context.Context, plan AmendmentPlan) (result WorkItem, err error) {
	fresh, err := s.PlanAmendment(ctx, plan.Item.ID, plan.Body)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(plan, fresh) {
		return result, errors.New("card, batch, or requirements changed after amendment preview")
	}
	if err := s.beginTransition(ctx, plan.Item.ID); err != nil {
		return result, err
	}
	defer func() {
		if err == nil {
			return
		}
		verifyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
		defer cancel()
		current, inspectErr := s.InspectRecoveryItem(verifyCtx, plan.Item.ID)
		if inspectErr == nil && amendmentItemMatches(current, plan.after.Item) {
			// A lost mutation response must not cause the engine to undo a
			// successfully committed workspace binding.
			result = plan.after.Item
			if plan.source.ID != "" {
				source, sourceErr := s.InspectRecoveryItem(verifyCtx, plan.source.ID)
				expected := plan.source
				expected.Approval = plan.sourceApproval
				if sourceErr != nil || !reflect.DeepEqual(source, expected) {
					return
				}
			}
			if unlockErr := s.finishTransition(verifyCtx, result.ID); unlockErr == nil {
				err = nil
			}
			return
		}
		rollbackErr := s.rollbackAmendment(verifyCtx, plan, current, inspectErr)
		err = errors.Join(err, rollbackErr)
	}()
	locked, err := s.InspectRecoveryItem(ctx, plan.Item.ID)
	if err != nil {
		return result, err
	}
	if locked.Transition != transitionLockValue || !amendmentItemMatches(locked, plan.Item) {
		return result, errors.New("card changed while locking amendment; no requirements were written")
	}
	if plan.source.ID != "" {
		source, err := s.InspectRecoveryItem(ctx, plan.source.ID)
		if err != nil {
			return result, err
		}
		if !reflect.DeepEqual(source, plan.source) {
			return result, errors.New("planning source changed during amendment; no requirements were written")
		}
	}
	response, err := s.gh(ctx, "issue", "edit", plan.Item.URL, "--body", plan.Body)
	if err != nil {
		return result, fmt.Errorf("write amended requirements: %w", commandFailure(err, response))
	}
	if plan.source.ID != "" {
		if err := s.setApproval(ctx, plan.source.ID, plan.sourceApproval); err != nil {
			return result, err
		}
	}
	next := plan.after.Item
	if err := s.applyFieldUpdates(ctx, next.ID,
		textProjectField(s.resultFieldName(), next.Result), textProjectField(s.approvalFieldName(), next.Approval),
		statusProjectField(s.statusFieldName(), next.Status)); err != nil {
		return result, err
	}
	current, err := s.InspectRecoveryItem(ctx, next.ID)
	if err != nil {
		return result, err
	}
	if !amendmentItemMatches(current, next) {
		return result, errors.New("amendment readback changed; leave Runner stopped and inspect the card")
	}
	if plan.source.ID != "" {
		source, inspectErr := s.InspectRecoveryItem(ctx, plan.source.ID)
		expected := plan.source
		expected.Approval = plan.sourceApproval
		if inspectErr != nil || !reflect.DeepEqual(source, expected) {
			return result, errors.Join(errors.New("planning source changed during amendment; leave Runner stopped"), inspectErr)
		}
	}
	result = next
	if err := s.finishTransition(ctx, next.ID); err != nil {
		return result, err
	}
	return result, nil
}

func amendmentItemMatches(actual, expected WorkItem) bool {
	actual.Transition, expected.Transition = "", ""
	actual.Role, expected.Role = "", ""
	actual.Dependencies = append([]string(nil), actual.Dependencies...)
	expected.Dependencies = append([]string(nil), expected.Dependencies...)
	actual.Labels = append([]string(nil), actual.Labels...)
	expected.Labels = append([]string(nil), expected.Labels...)
	return reflect.DeepEqual(actual, expected)
}

func (s *Project) rollbackAmendment(ctx context.Context, plan AmendmentPlan, current WorkItem, inspectErr error) error {
	unsafe := errors.New("amendment could not be safely restored; leave Runner stopped and inspect the exact preview, card, and workspace before recovery")
	if inspectErr != nil {
		return errors.Join(unsafe, inspectErr)
	}
	if current.Transition != transitionLockValue {
		return unsafe
	}
	// Only these fields could have been written by this operation.
	if (current.Body != plan.Item.Body && current.Body != plan.Body) ||
		(current.Approval != plan.Item.Approval && current.Approval != plan.after.Item.Approval) ||
		(current.Status != plan.Item.Status && current.Status != plan.after.Item.Status) ||
		(current.Result != plan.Item.Result && current.Result != plan.after.Item.Result) {
		return unsafe
	}
	comparable := current
	comparable.Body, comparable.Approval, comparable.Status, comparable.Result = plan.Item.Body, plan.Item.Approval, plan.Item.Status, plan.Item.Result
	if !amendmentItemMatches(comparable, plan.Item) {
		return unsafe
	}
	if plan.source.ID != "" {
		source, err := s.InspectRecoveryItem(ctx, plan.source.ID)
		if err != nil {
			return errors.Join(unsafe, err)
		}
		if source.Approval != plan.source.Approval && source.Approval != plan.sourceApproval {
			return unsafe
		}
		comparable := source
		comparable.Approval = plan.source.Approval
		if !reflect.DeepEqual(comparable, plan.source) {
			return unsafe
		}
		if err := s.setApproval(ctx, source.ID, plan.source.Approval); err != nil {
			return errors.Join(unsafe, err)
		}
	}
	if current.Body != plan.Item.Body {
		if _, err := s.gh(ctx, "issue", "edit", plan.Item.URL, "--body", plan.Item.Body); err != nil {
			return errors.Join(unsafe, err)
		}
	}
	if err := s.applyFieldUpdates(ctx, plan.Item.ID, textProjectField(s.resultFieldName(), plan.Item.Result),
		textProjectField(s.approvalFieldName(), plan.Item.Approval), statusProjectField(s.statusFieldName(), plan.Item.Status)); err != nil {
		return errors.Join(unsafe, err)
	}
	restored, err := s.InspectRecoveryItem(ctx, plan.Item.ID)
	if err != nil || !amendmentItemMatches(restored, plan.Item) {
		return errors.Join(unsafe, err)
	}
	if err := s.finishTransition(ctx, plan.Item.ID); err != nil {
		return errors.Join(unsafe, err)
	}
	return nil
}
