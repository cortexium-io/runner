package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
)

type ApprovalPlan struct {
	Item              WorkItem           `json:"item"`
	Role              string             `json:"role"`
	Assertion         string             `json:"assertion"`
	RemoveIntakeLabel bool               `json:"remove_intake_label"`
	Batch             *BatchApprovalPlan `json:"batch,omitempty"`

	action AuthorizedAction
}

type BatchApprovalPlan struct {
	Source      WorkItem            `json:"source"`
	Destination string              `json:"destination"`
	Children    []BatchApprovalItem `json:"children"`
}

type BatchApprovalItem struct {
	Item      WorkItem `json:"item"`
	Role      string   `json:"role"`
	Assertion string   `json:"assertion"`

	action AuthorizedAction
}

func (s *Project) PlanApproval(ctx context.Context, selector string) (ApprovalPlan, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return ApprovalPlan{}, errors.New("project item id or URL is required")
	}
	schema, err := s.loadSchema(ctx)
	if err != nil {
		return ApprovalPlan{}, err
	}
	field, ok := schema.field(s.approvalFieldName())
	if !ok || field.Type != "ProjectV2Field" {
		return ApprovalPlan{}, fmt.Errorf("GitHub Project requires text field %q before work can be approved", s.approvalFieldName())
	}
	items, err := s.ListItems(ctx)
	if err != nil {
		return ApprovalPlan{}, err
	}
	item, err := selectProjectItem(items, selector)
	if err != nil {
		return ApprovalPlan{}, err
	}
	if item.PlanningMetadataInvalid {
		return ApprovalPlan{}, errors.New("project item contains hidden, malformed, or lookalike Runner planning metadata and cannot be approved")
	}
	if strings.TrimSpace(item.PlanningSourceID) == "" && strings.TrimSpace(item.PlanningBatchFingerprint) != "" {
		return ApprovalPlan{}, errors.New("direct staged planning children cannot be approved individually; use the fingerprint-bound plan --approve-staged command to review and release the complete batch")
	}
	if stagedBatchSourceID(item, items) != "" {
		return s.planBatchApproval(items, item)
	}
	if !s.approvableStatus(item.Status) {
		return ApprovalPlan{}, fmt.Errorf("project item %s in status %q cannot be approved; move it to assessment and preview approval again", item.ID, item.Status)
	}
	if strings.TrimSpace(item.Transition) != "" {
		return ApprovalPlan{}, errors.New("project item has an interrupted Runner transition; run Runner once to recover it before previewing approval again")
	}
	if HasRuntimeActionState(item) {
		return ApprovalPlan{}, errors.New("project item contains prior Runner action state; inspect it, clear Runner Phase, Runner Activity, Result, QA failures, branch, pull request, and commit snapshot fields, then preview approval again")
	}
	if strings.TrimSpace(s.cfg.InitialRole) == "" {
		return ApprovalPlan{}, errors.New("workflow has no planning role for approved intake; configure a planner lane before approving work")
	}
	action, err := s.signAction(item, s.cfg.InitialRole, approvalReadyState)
	if err != nil {
		return ApprovalPlan{}, err
	}
	removeLabel, err := s.hasIntakeLabel(ctx, item)
	if err != nil {
		return ApprovalPlan{}, err
	}
	return ApprovalPlan{
		Item: item, Role: action.Role, Assertion: action.assertion,
		RemoveIntakeLabel: removeLabel, action: action,
	}, nil
}

// HasRuntimeActionState reports whether an item carries state from a prior
// Runner attempt. Fresh intake and staged planning children must not authorize
// that state as part of a new action.
func HasRuntimeActionState(item WorkItem) bool {
	return strings.TrimSpace(item.Result) != "" || strings.TrimSpace(item.Phase) != "" || strings.TrimSpace(item.Transition) != "" || strings.TrimSpace(item.Activity) != "" || item.QAFailures != 0 || strings.TrimSpace(item.Branch) != "" ||
		strings.TrimSpace(item.PullRequest) != "" || strings.TrimSpace(item.QACommit) != ""
}

func (s *Project) ApplyApproval(ctx context.Context, plan ApprovalPlan) (WorkItem, error) {
	if plan.Batch != nil {
		return s.applyBatchApproval(ctx, plan)
	}
	if strings.TrimSpace(plan.Item.ID) == "" || plan.action.assertion == "" || plan.action.Item.ID != plan.Item.ID ||
		plan.Assertion != plan.action.assertion {
		return WorkItem{}, errors.New("approval preview is incomplete; preview the item again and retry approval")
	}
	if _, err := s.loadSchema(ctx); err != nil {
		return WorkItem{}, err
	}
	items, err := s.ListItems(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	current, err := selectProjectItem(items, plan.Item.ID)
	if err != nil {
		return WorkItem{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(current.Status), strings.TrimSpace(plan.Item.Status)) || current.Approval != plan.Item.Approval {
		return WorkItem{}, errors.New("project item approval or status changed after the approval preview; review it and preview approval again")
	}
	if !s.approvableStatus(current.Status) {
		return WorkItem{}, fmt.Errorf("project item %s moved to status %q; move it to assessment, review it, and preview approval again", current.ID, current.Status)
	}
	previewedAction, err := s.signAction(current, plan.Role, approvalReadyState)
	if err != nil {
		return WorkItem{}, err
	}
	if !sameAuthorizedAction(plan.action, previewedAction) {
		return WorkItem{}, errors.New("project item content or action state changed after the approval preview; review it and preview approval again")
	}
	removeLabel, err := s.hasIntakeLabel(ctx, current)
	if err != nil {
		return WorkItem{}, err
	}
	if removeLabel != plan.RemoveIntakeLabel {
		return WorkItem{}, errors.New("project item intake label changed after the approval preview; review it and preview approval again")
	}
	if err := s.beginTransition(ctx, current.ID); err != nil {
		return WorkItem{}, fmt.Errorf("lock item before approval; no approval was written, so fix Project access and retry: %w", err)
	}
	current.Transition = transitionLockValue
	if plan.RemoveIntakeLabel {
		result, removeErr := s.gh(ctx, "issue", "edit", current.URL, "--remove-label", s.intakeLabel())
		if removeErr != nil {
			return WorkItem{}, fmt.Errorf("remove public assessment label; the item remains safely transition-locked and approve can be retried: %w", commandFailure(removeErr, result))
		}
	}
	if plan.RemoveIntakeLabel {
		current.Labels = withoutNormalizedValue(current.Labels, s.intakeLabel())
	}
	issueBacked, err := s.ensureIssueBacked(ctx, []WorkItem{current})
	if err != nil {
		return WorkItem{}, fmt.Errorf("prepare approved item for discussion; the item remains safely transition-locked: %w", err)
	}
	current = issueBacked[0]
	next := current
	next.Status = s.backlogStatus()
	next.Transition = ""
	refreshed, err := s.signAction(next, plan.Role, approvalReadyState)
	if err != nil {
		return WorkItem{}, err
	}
	if err := s.setStatus(ctx, current.ID, s.backlogStatus()); err != nil {
		return WorkItem{}, fmt.Errorf("move approved item to backlog; the item remains safely transition-locked and approve can be retried: %w", err)
	}
	if err := s.setApproval(ctx, current.ID, refreshed.assertion); err != nil {
		return WorkItem{}, fmt.Errorf("record authenticated approval; the item remains safely transition-locked and approve can be retried: %w", err)
	}
	if err := s.finishTransition(ctx, current.ID); err != nil {
		return WorkItem{}, fmt.Errorf("approval committed but its transition lock could not be cleared; a later poll will recover it: %w", err)
	}
	current.Approval = refreshed.assertion
	current.Status = s.backlogStatus()
	current.Role = refreshed.Role
	current.Transition = ""
	return current, nil
}

func stagedBatchSourceID(selected WorkItem, items []WorkItem) string {
	if sourceID := strings.TrimSpace(selected.PlanningSourceID); sourceID != "" {
		return sourceID
	}
	for _, item := range items {
		if strings.TrimSpace(item.PlanningSourceID) == strings.TrimSpace(selected.ID) {
			return strings.TrimSpace(selected.ID)
		}
	}
	return ""
}

func (s *Project) planBatchApproval(items []WorkItem, selected WorkItem) (ApprovalPlan, error) {
	sourceID := stagedBatchSourceID(selected, items)
	if sourceID == "" {
		return ApprovalPlan{}, errors.New("staged planning batch was not found")
	}
	source, err := selectProjectItem(items, sourceID)
	if err != nil {
		return ApprovalPlan{}, fmt.Errorf("find staged planning source: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(source.Status), s.assessmentStatus()) {
		return ApprovalPlan{}, fmt.Errorf("planning source %s is not awaiting complete-batch approval", source.ID)
	}
	children := make([]WorkItem, 0)
	for _, item := range items {
		if strings.TrimSpace(item.PlanningSourceID) == sourceID {
			children = append(children, item)
		}
	}
	if len(children) == 0 || len(children) > MaxPlanningBatchChildren {
		return ApprovalPlan{}, fmt.Errorf("staged planning batch has %d children; expected between 1 and %d", len(children), MaxPlanningBatchChildren)
	}
	sort.Slice(children, func(left, right int) bool {
		return children[left].PlanningItemIndex < children[right].PlanningItemIndex
	})
	fingerprint := strings.TrimSpace(children[0].PlanningBatchFingerprint)
	if fingerprint == "" {
		return ApprovalPlan{}, errors.New("staged planning batch fingerprint is missing")
	}
	sourceLane := strings.TrimSpace(children[0].PlanningSourceLane)
	targetStatus := strings.TrimSpace(children[0].PlanningDestination)
	if sourceLane == "" || targetStatus == "" || s.cfg.PlanningDestinations[sourceLane] != targetStatus {
		return ApprovalPlan{}, errors.New("staged planning batch destination is missing or no longer authorized by its originating planner lane")
	}
	if phase := strings.TrimSpace(source.Phase); phase != PlanningApprovalPhase {
		return ApprovalPlan{}, fmt.Errorf("planning source %s has no authenticated staged-batch phase", source.ID)
	}
	state, err := s.stateForStatus(targetStatus)
	if err != nil {
		return ApprovalPlan{}, err
	}
	role := strings.TrimSpace(s.cfg.LaneRoles[state])
	if role == "" || !s.agentStatus(targetStatus) {
		return ApprovalPlan{}, fmt.Errorf("planning batch destination %q is not an executable role lane", targetStatus)
	}
	batch := &BatchApprovalPlan{Source: source, Destination: targetStatus}
	if err := ValidatePlanningDependencies(children); err != nil {
		return ApprovalPlan{}, err
	}
	for index, child := range children {
		if child.PlanningBatchFingerprint != fingerprint || child.PlanningBatchSize != len(children) || child.PlanningItemIndex != index+1 ||
			child.PlanningSourceLane != sourceLane || child.PlanningDestination != targetStatus ||
			child.PlanningSourceFingerprint != PlanningSourceFingerprint(source) {
			return ApprovalPlan{}, errors.New("staged planning batch is incomplete, duplicated, reordered, or mixed with changed content")
		}
		if !strings.EqualFold(strings.TrimSpace(child.Status), s.assessmentStatus()) || strings.TrimSpace(child.Approval) != "" {
			return ApprovalPlan{}, fmt.Errorf("staged planning child %s moved to unexpected status %q", child.ID, child.Status)
		}
		if HasRuntimeActionState(child) {
			return ApprovalPlan{}, fmt.Errorf("staged planning child %s contains prior Runner action state", child.ID)
		}
		if strings.TrimSpace(child.ID) == "" || strings.TrimSpace(child.Title) == "" || strings.TrimSpace(child.Body) == "" {
			return ApprovalPlan{}, fmt.Errorf("staged planning child %d is incomplete", index+1)
		}
		next := child
		next.Status = strings.TrimSpace(targetStatus)
		next.Approval = ""
		action, signErr := s.signAction(next, role, state)
		if signErr != nil {
			return ApprovalPlan{}, signErr
		}
		batch.Children = append(batch.Children, BatchApprovalItem{Item: child, Role: role, Assertion: action.assertion, action: action})
	}
	childItems := make([]WorkItem, len(batch.Children))
	for index := range batch.Children {
		childItems[index] = batch.Children[index].Item
	}
	if _, err := s.validatePlanningBatch(source.Approval, source, childItems, batchStagedState); err != nil {
		return ApprovalPlan{}, err
	}
	return ApprovalPlan{Item: source, Batch: batch}, nil
}

func (s *Project) applyBatchApproval(ctx context.Context, plan ApprovalPlan) (WorkItem, error) {
	if plan.Batch == nil || strings.TrimSpace(plan.Batch.Source.ID) == "" || len(plan.Batch.Children) == 0 {
		return WorkItem{}, errors.New("batch approval preview is incomplete; preview the complete batch again")
	}
	if _, err := s.loadSchema(ctx); err != nil {
		return WorkItem{}, err
	}
	items, err := s.ListItems(ctx)
	if err != nil {
		return WorkItem{}, err
	}
	current, err := selectProjectItem(items, plan.Batch.Source.ID)
	if err != nil {
		return WorkItem{}, err
	}
	refreshedPlan, err := s.planBatchApproval(items, current)
	if err != nil {
		return WorkItem{}, fmt.Errorf("planning source or staged child content changed after the approval preview: %w", err)
	}
	if refreshedPlan.Batch == nil || !sameBatchApprovalPreview(*plan.Batch, *refreshedPlan.Batch) {
		return WorkItem{}, errors.New("planning source or staged child content changed after the approval preview; review the complete batch and preview approval again")
	}
	batch := refreshedPlan.Batch
	childItems := make([]WorkItem, len(batch.Children))
	for index := range batch.Children {
		childItems[index] = batch.Children[index].Item
	}
	provenance, err := s.validatePlanningBatch(batch.Source.Approval, batch.Source, childItems, batchStagedState)
	if err != nil {
		return WorkItem{}, fmt.Errorf("revalidate authenticated staged batch before approval: %w", err)
	}
	for _, child := range batch.Children {
		if err := s.setStatus(ctx, child.Item.ID, s.assessmentStatus()); err != nil {
			return WorkItem{}, fmt.Errorf("park complete planning batch before approval writes: %w", err)
		}
		if strings.TrimSpace(child.Item.Approval) != "" {
			if err := s.clearApproval(ctx, child.Item.ID); err != nil {
				return WorkItem{}, fmt.Errorf("clear partial child approval before authorizing the complete batch: %w", err)
			}
		}
	}
	for index := range childItems {
		childItems[index].Status = s.assessmentStatus()
		childItems[index].Approval = ""
	}
	childItems, err = s.ensureIssueBacked(ctx, childItems)
	if err != nil {
		return WorkItem{}, fmt.Errorf("prepare planning batch for discussion; every child remains safely in assessment: %w", err)
	}
	if _, err := s.validatePlanningBatch(batch.Source.Approval, batch.Source, childItems, batchStagedState); err != nil {
		return WorkItem{}, fmt.Errorf("revalidate authenticated staged batch after issue conversion: %w", err)
	}
	state, err := s.stateForStatus(batch.Destination)
	if err != nil {
		return WorkItem{}, err
	}
	convertedChildren := make([]BatchApprovalItem, len(childItems))
	for index, child := range childItems {
		next := child
		next.Status = batch.Destination
		action, signErr := s.signAction(next, batch.Children[index].Role, state)
		if signErr != nil {
			return WorkItem{}, fmt.Errorf("authenticate converted planning child %d: %w", index+1, signErr)
		}
		convertedChildren[index] = BatchApprovalItem{Item: child, Role: action.Role, Assertion: action.assertion, action: action}
	}
	batch.Children = convertedChildren
	releaseAssertion, err := s.signPlanningBatch(batch.Source, childItems, batchReleasedState, provenance.Generation)
	if err != nil {
		return WorkItem{}, fmt.Errorf("authenticate complete planning batch release: %w", err)
	}
	for index, child := range batch.Children {
		if err := s.setApproval(ctx, child.Item.ID, child.Assertion); err != nil {
			cleanupErr := s.parkBatchInAssessment(ctx, batch.Children)
			return WorkItem{}, errors.Join(fmt.Errorf("approve planning child %d of %d: %w", index+1, len(batch.Children), err), cleanupErr)
		}
	}
	items, err = s.ListItems(ctx)
	if err != nil {
		cleanupErr := s.parkBatchInAssessment(ctx, batch.Children)
		return WorkItem{}, errors.Join(fmt.Errorf("revalidate planning source before releasing complete batch: %w", err), cleanupErr)
	}
	current, err = selectProjectItem(items, batch.Source.ID)
	if err != nil || !reflect.DeepEqual(current, batch.Source) || PlanningSourceFingerprint(current) != batch.Children[0].Item.PlanningSourceFingerprint {
		cleanupErr := s.parkBatchInAssessment(ctx, batch.Children)
		return WorkItem{}, errors.Join(errors.New("planning source changed before releasing complete batch"), cleanupErr)
	}
	for index, child := range batch.Children {
		if err := s.setStatus(ctx, child.Item.ID, batch.Destination); err != nil {
			cleanupErr := s.parkBatchInAssessment(ctx, batch.Children)
			return WorkItem{}, errors.Join(fmt.Errorf("release planning child %d of %d: %w", index+1, len(batch.Children), err), cleanupErr)
		}
	}
	detail := fmt.Sprintf("Operator approved and released the complete normalized planning batch of %d work items.", len(batch.Children))
	if err := s.completeStagedPlanningSource(ctx, batch.Source, detail, releaseAssertion); err != nil {
		cleanupErr := s.parkBatchInAssessment(ctx, batch.Children)
		return WorkItem{}, errors.Join(fmt.Errorf("complete planning batch release: %w", err), cleanupErr)
	}
	publishedDetail, err := runnerProjectResult(detail)
	if err != nil {
		return WorkItem{}, err
	}
	batch.Source.Status = s.doneStatus()
	batch.Source.Phase = ""
	batch.Source.Result = canonicalProjectResult(publishedDetail)
	batch.Source.Approval = releaseAssertion
	return batch.Source, nil
}

func sameBatchApprovalPreview(left, right BatchApprovalPlan) bool {
	if left.Destination != right.Destination || !reflect.DeepEqual(left.Source, right.Source) || len(left.Children) != len(right.Children) {
		return false
	}
	for index := range left.Children {
		if !reflect.DeepEqual(left.Children[index].Item, right.Children[index].Item) || left.Children[index].Role != right.Children[index].Role || left.Children[index].Assertion != right.Children[index].Assertion {
			return false
		}
	}
	return true
}

func (s *Project) completeStagedPlanningSource(ctx context.Context, source WorkItem, detail, releaseAssertion string) error {
	publishedDetail, err := runnerProjectResult(detail)
	if err != nil {
		return err
	}
	if err := s.setResult(ctx, source.ID, publishedDetail); err != nil {
		return err
	}
	if err := s.clearField(ctx, source.ID, s.phaseFieldName()); err != nil {
		return err
	}
	if strings.TrimSpace(source.Activity) != "" {
		if err := s.clearField(ctx, source.ID, s.activityFieldName()); err != nil {
			return err
		}
	}
	if err := s.setStatus(ctx, source.ID, s.doneStatus()); err != nil {
		return err
	}
	if err := s.setApproval(ctx, source.ID, releaseAssertion); err != nil {
		return fmt.Errorf("commit authenticated complete-batch release: %w", err)
	}
	return nil
}

func (s *Project) parkBatchInAssessment(ctx context.Context, children []BatchApprovalItem) error {
	var failures []error
	for _, child := range children {
		if err := s.setStatus(ctx, child.Item.ID, s.assessmentStatus()); err != nil {
			failures = append(failures, fmt.Errorf("park child %s: %w", child.Item.ID, err))
		}
		if err := s.clearApproval(ctx, child.Item.ID); err != nil {
			failures = append(failures, fmt.Errorf("clear child %s approval: %w", child.Item.ID, err))
		}
	}
	return errors.Join(failures...)
}

// ReleaseStaged authorizes an operator-reviewed local plan only after every
// exact child exists in assessment. Approval writes complete before any child
// is moved to the destination lane.
func (s *Project) ReleaseStaged(ctx context.Context, expected []WorkItem, targetStatus string) ([]WorkItem, error) {
	if len(expected) == 0 || len(expected) > MaxPlanningBatchChildren {
		return nil, fmt.Errorf("staged plan has %d children; expected between 1 and %d", len(expected), MaxPlanningBatchChildren)
	}
	if err := ValidatePlanningDependencies(expected); err != nil {
		return nil, err
	}
	if err := s.ValidateDirectPlanningBatchStaging(expected); err != nil {
		return nil, err
	}
	if _, err := s.loadSchema(ctx); err != nil {
		return nil, err
	}
	itemIDs := make([]string, len(expected))
	for index := range expected {
		itemIDs[index] = expected[index].ID
	}
	items, err := s.LifecycleItemsByID(ctx, itemIDs)
	if err != nil {
		return nil, err
	}
	state, err := s.stateForStatus(targetStatus)
	if err != nil {
		return nil, err
	}
	role := strings.TrimSpace(s.cfg.LaneRoles[state])
	if role == "" || !s.agentStatus(targetStatus) {
		return nil, fmt.Errorf("staged plan destination %q is not an executable role lane", targetStatus)
	}
	for index, previewed := range expected {
		current := items[index]
		if !reflect.DeepEqual(current, previewed) {
			return nil, fmt.Errorf("staged child %d changed after creation; no child was approved", index+1)
		}
		if !strings.EqualFold(strings.TrimSpace(current.Status), s.assessmentStatus()) {
			return nil, fmt.Errorf("staged child %d is not safely in assessment", index+1)
		}
		if HasRuntimeActionState(current) {
			return nil, fmt.Errorf("staged child %d contains prior Runner action state", index+1)
		}
	}
	items, err = s.ensureIssueBacked(ctx, items)
	if err != nil {
		return nil, fmt.Errorf("prepare staged plan for discussion; every child remains safely in assessment: %w", err)
	}
	if err := s.ValidateDirectPlanningBatchStaging(items); err != nil {
		return nil, fmt.Errorf("revalidate staged plan after issue conversion: %w", err)
	}
	children := make([]BatchApprovalItem, 0, len(items))
	for _, current := range items {
		next := current
		next.Status = strings.TrimSpace(targetStatus)
		action, signErr := s.signAction(next, role, state)
		if signErr != nil {
			return nil, signErr
		}
		children = append(children, BatchApprovalItem{Item: current, Role: role, Assertion: action.assertion, action: action})
	}
	for index, child := range children {
		if err := s.setApproval(ctx, child.Item.ID, child.Assertion); err != nil {
			cleanupErr := s.parkBatchInAssessment(ctx, children)
			return nil, errors.Join(fmt.Errorf("approve staged child %d of %d: %w", index+1, len(children), err), cleanupErr)
		}
	}
	released := append([]WorkItem(nil), items...)
	for index, child := range children {
		if err := s.setStatus(ctx, child.Item.ID, targetStatus); err != nil {
			cleanupErr := s.parkBatchInAssessment(ctx, children)
			return nil, errors.Join(fmt.Errorf("release staged child %d of %d: %w", index+1, len(children), err), cleanupErr)
		}
		released[index] = child.action.Item
	}
	return released, nil
}

func (s *Project) reclassifyForApproval(ctx context.Context, item WorkItem, detail string) error {
	if err := s.setStatus(ctx, item.ID, s.assessmentStatus()); err != nil {
		return err
	}
	if strings.TrimSpace(item.Approval) != "" {
		if err := s.clearApproval(ctx, item.ID); err != nil {
			return fmt.Errorf("item is safely in assessment but its invalid approval could not be cleared; retry reassessment: %w", err)
		}
	}
	if strings.TrimSpace(item.Activity) != "" {
		if err := s.clearField(ctx, item.ID, s.activityFieldName()); err != nil {
			return fmt.Errorf("item is safely in assessment but its stale Runner activity could not be cleared: %w", err)
		}
	}
	publishedDetail, err := runnerProjectResult(detail)
	if err != nil {
		return err
	}
	if err := s.setResult(ctx, item.ID, publishedDetail); err != nil {
		return fmt.Errorf("item is safely in assessment but the recovery explanation could not be recorded: %w", err)
	}
	return nil
}

func (s *Project) setApproval(ctx context.Context, itemID, assertion string) error {
	if _, _, _, _, _, actionErr := parseActionAssertion(assertion); actionErr != nil {
		if _, _, _, batchErr := parsePlanningBatchAssertion(assertion); batchErr != nil {
			return errors.New("authenticated approval assertion is invalid")
		}
	}
	schema := s.currentSchema()
	field, ok := schema.field(s.approvalFieldName())
	if !ok || field.Type != "ProjectV2Field" {
		return fmt.Errorf("GitHub Project has no text field %q", s.approvalFieldName())
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--text", assertion)
	if err != nil {
		return fmt.Errorf("update GitHub Project approval: %w", commandFailure(err, result))
	}
	return nil
}

func (s *Project) clearApproval(ctx context.Context, itemID string) error {
	schema := s.currentSchema()
	field, ok := schema.field(s.approvalFieldName())
	if !ok || field.Type != "ProjectV2Field" {
		return fmt.Errorf("GitHub Project has no text field %q", s.approvalFieldName())
	}
	result, err := s.gh(ctx, "project", "item-edit", "--id", itemID, "--project-id", schema.ProjectID, "--field-id", field.ID, "--clear")
	if err != nil {
		return fmt.Errorf("clear GitHub Project approval: %w", commandFailure(err, result))
	}
	return nil
}

func selectProjectItem(items []WorkItem, selector string) (WorkItem, error) {
	selector = strings.TrimSpace(selector)
	for _, item := range items {
		if item.ID == selector || item.URL == selector {
			return item, nil
		}
	}
	var titleMatch *WorkItem
	for index := range items {
		if !strings.EqualFold(strings.TrimSpace(items[index].Title), selector) {
			continue
		}
		if titleMatch != nil {
			return WorkItem{}, fmt.Errorf("GitHub Project title %q is ambiguous; use the item id or URL", selector)
		}
		titleMatch = &items[index]
	}
	if titleMatch != nil {
		return *titleMatch, nil
	}
	return WorkItem{}, fmt.Errorf("GitHub Project item %q was not found", selector)
}

func (s *Project) approvableStatus(status string) bool {
	for _, allowed := range []string{s.assessmentStatus(), s.backlogStatus()} {
		if strings.EqualFold(strings.TrimSpace(status), allowed) {
			return true
		}
	}
	return false
}

func (s *Project) hasIntakeLabel(ctx context.Context, item WorkItem) (bool, error) {
	if !s.isIntakeIssueURL(item.URL) {
		return false, nil
	}
	result, err := s.gh(ctx, "issue", "view", item.URL, "--json", "labels")
	if err != nil {
		return false, fmt.Errorf("inspect public assessment label: %w", commandFailure(err, result))
	}
	var payload struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return false, fmt.Errorf("decode public assessment labels: %w", err)
	}
	for _, label := range payload.Labels {
		if normalizeProjectKey(label.Name) == normalizeProjectKey(s.intakeLabel()) {
			return true, nil
		}
	}
	return false, nil
}

func (s *Project) isIntakeIssueURL(value string) bool {
	repository := strings.Trim(strings.TrimSpace(s.cfg.IntakeRepository), "/")
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") || repository == "" {
		return false
	}
	prefix := "/" + strings.ToLower(repository) + "/issues/"
	return strings.HasPrefix(strings.ToLower(parsed.EscapedPath()), prefix)
}

func withoutNormalizedValue(values []string, remove string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if normalizeProjectKey(value) != normalizeProjectKey(remove) {
			result = append(result, value)
		}
	}
	return result
}
