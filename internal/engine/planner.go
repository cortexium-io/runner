package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
)

type ProjectPlan struct {
	GoalSummary            string               `json:"goal_summary"`
	ProjectSuccessCriteria []string             `json:"project_success_criteria"`
	ProjectConstraints     []string             `json:"project_constraints"`
	OpenDecisions          []string             `json:"open_decisions"`
	WorkItems              []github.PlannedItem `json:"work_items"`
	SourceContext          string               `json:"-"`
}

type ProjectPlanApproval struct {
	BatchFingerprint string            `json:"batch_fingerprint"`
	Destination      string            `json:"destination"`
	Children         []github.WorkItem `json:"children"`
}

const directProjectPlanSourceLane = "local_plan"

// CheckProjectPlanningAvailability prevents a new paid planner call while a
// prior local planning batch still owns unapproved Project cards. Combining
// two generated plans would change the batch the operator reviewed.
func (s *Engine) CheckProjectPlanningAvailability(ctx context.Context) error {
	items, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return fmt.Errorf("inspect prior local project planning: %w", err)
	}
	assessmentStatus := strings.TrimSpace(s.cfg.LaneStatus(s.cfg.EffectiveWorkflow().IntakeLane))
	type batch struct {
		fingerprint string
		expected    int
		items       []github.WorkItem
	}
	batches := map[string]*batch{}
	for _, item := range items {
		fingerprint := strings.TrimSpace(item.PlanningBatchFingerprint)
		if strings.TrimSpace(item.PlanningSourceID) != "" || item.PlanningSourceLane != directProjectPlanSourceLane || fingerprint == "" ||
			!strings.EqualFold(strings.TrimSpace(item.Status), assessmentStatus) {
			continue
		}
		current := batches[fingerprint]
		if current == nil {
			current = &batch{fingerprint: fingerprint, expected: item.PlanningBatchSize}
			batches[fingerprint] = current
		}
		if current.expected != item.PlanningBatchSize {
			return errors.New("prior local project planning contains inconsistent batch sizes; review its unapproved cards before planning again")
		}
		current.items = append(current.items, item)
	}
	if len(batches) == 0 {
		return nil
	}
	fingerprints := make([]string, 0, len(batches))
	for fingerprint := range batches {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	if len(fingerprints) > 1 {
		return fmt.Errorf("GitHub Project contains %d prior local planning batches; review their unapproved cards before starting another planner call", len(fingerprints))
	}
	current := batches[fingerprints[0]]
	sort.Slice(current.items, func(left, right int) bool {
		return current.items[left].PlanningItemIndex < current.items[right].PlanningItemIndex
	})
	identities := make([]string, 0, len(current.items))
	for _, item := range current.items {
		identities = append(identities, fmt.Sprintf("%s (%s)", strings.TrimSpace(item.ID), strings.TrimSpace(item.Title)))
	}
	if current.expected > 0 && len(current.items) == current.expected {
		return fmt.Errorf("a complete unapproved project plan is already staged as batch %s; review it with `--approve-staged %s` using the same config instead of running the planner again", current.fingerprint, current.fingerprint)
	}
	return fmt.Errorf(
		"an interrupted local project plan already has %d of %d unapproved card(s): %s; remove that partial batch from the Project before running the planner again",
		len(current.items), current.expected, strings.Join(identities, ", "),
	)
}

func (s *Engine) PlanProject(ctx context.Context, idea string) (ProjectPlan, error) {
	admission, err := s.AdmissionStatus(time.Now().UTC())
	if err != nil {
		return ProjectPlan{}, err
	}
	s.recordAdmissionDecision(admission)
	if admission.Configured && !admission.Allowed {
		return ProjectPlan{}, fmt.Errorf("agent admission paused: %s", admission.Summary())
	}
	role := s.cfg.RoleIDForContract(config.WorkRolePlanner)
	startedAt := time.Now().UTC()
	attemptID := metrics.NewAttemptID()
	harness := s.roleHarness(role)
	profile, _ := s.cfg.RoleProfile(role)
	model := ""
	if profile.Model != nil {
		model = strings.TrimSpace(*profile.Model)
	}
	event := metrics.Event{
		AttemptID: attemptID, RunnerID: s.cfg.RunnerID,
		ProjectOwner: s.cfg.GitHubProject.Owner, ProjectNumber: s.cfg.GitHubProject.Number,
		ItemTitle: "Interactive project planning", Role: role, Harness: harness,
		Model: model, Reasoning: profile.Reasoning, Iteration: 1, StartedAt: startedAt,
	}
	trace := metrics.NewAttemptTrace(s.observeMetrics, event)
	ctx = metrics.WithAttemptTrace(ctx, trace)
	if s.observeMetrics != nil {
		started := event
		started.Kind = metrics.EventStarted
		if observeErr := s.observeMetrics(started); observeErr != nil && s.cfg.AdmissionBudget != nil {
			return ProjectPlan{}, fmt.Errorf("persist admission reservation: %w", observeErr)
		}
	}
	plan, harnessResult, err := s.planProjectWithRole(ctx, role, idea)
	if s.observeMetrics != nil {
		finished := time.Now().UTC()
		event.Kind = metrics.EventCompleted
		event.FinishedAt = finished
		event.DurationMilliseconds = finished.Sub(startedAt).Milliseconds()
		event.HarnessDurationMilliseconds = harnessResult.DurationMilliseconds
		event.Usage = harnessResult.Usage
		event.Outcome = execution.OutcomeSucceeded
		event.Summary = "Project plan generated."
		if err != nil {
			event.Outcome = execution.OutcomeBlocked
			event.Summary = "Project planning failed."
			event.FailureClass = string(harnessResult.FailureClass)
			event.RetryDisposition = string(harnessResult.RetryDisposition)
			event.RetryAfter = harnessResult.RetryAfter
		}
		_ = s.observeMetrics(event)
	}
	return plan, err
}

func (s *Engine) planProjectWithRole(ctx context.Context, role, idea string) (ProjectPlan, execution.StructuredHarnessResult, error) {
	idea = strings.TrimSpace(idea)
	if idea == "" {
		return ProjectPlan{}, execution.StructuredHarnessResult{FailureClass: execution.FailureNeedsInput, RetryDisposition: execution.RetryNone}, errors.New("project idea is required")
	}
	harness := s.roleHarness(role)
	skills := s.roleSkills(role)
	finishRepository := metrics.StartStage(ctx, metrics.StageRepositoryPrepare)
	workingDir, err := s.repositoryDir(ctx, "")
	if err != nil {
		finishRepository(metrics.StageOutcomeFailed, string(execution.FailureInvalidConfiguration), string(execution.RetryNone), metrics.Usage{})
		return ProjectPlan{}, execution.StructuredHarnessResult{FailureClass: execution.FailureInvalidConfiguration, RetryDisposition: execution.RetryNone}, err
	}
	finishRepository(metrics.StageOutcomeSucceeded, "", "", metrics.Usage{})
	planningContext := projectPlannerExecutionContext{}
	implementerRole := s.cfg.RoleIDForContract(config.WorkRoleImplementer)
	if profile, ok := s.cfg.RoleProfile(implementerRole); ok {
		if profile.TimeoutSeconds > 0 {
			planningContext.ImplementerTimeout = time.Duration(profile.TimeoutSeconds) * time.Second
		}
		planningContext.ImplementerSupport = config.EffectivePlanningSupport(profile.PlanningSupport)
	}
	reviewerRole := s.cfg.RoleIDForContract(config.WorkRoleReviewer)
	if profile, ok := s.cfg.RoleProfile(reviewerRole); ok {
		planningContext.ReviewerSupport = config.EffectivePlanningSupport(profile.PlanningSupport)
	}
	prompt := projectPlannerPrompt(skills, planningContext, s.cfg.GitHubProject.IntakeRepository, idea)
	harnessResult, err := s.runPlannerHarness(ctx, role, harness, workingDir, prompt)
	if err != nil {
		return ProjectPlan{}, harnessResult, err
	}
	result := harnessResult.Message
	finishValidation := metrics.StartStage(ctx, metrics.StageResultValidate)
	plan, err := decodeProjectPlan(result)
	if err == nil {
		err = s.normalizeProjectPlan(&plan)
	}
	if err == nil {
		finishValidation(metrics.StageOutcomeSucceeded, "", "", metrics.Usage{})
	} else {
		finishValidation(metrics.StageOutcomeFailed, string(execution.FailureInvalidContract), string(execution.RetryNone), metrics.Usage{})
	}
	if err != nil {
		harnessResult.FailureClass = execution.FailureInvalidContract
		harnessResult.RetryDisposition = execution.RetryNone
		return ProjectPlan{}, harnessResult, fmt.Errorf("project plan is invalid: %w", err)
	}
	plan.SourceContext = idea
	harnessResult.FailureClass = execution.FailureNone
	harnessResult.RetryDisposition = ""
	harnessResult.RetryAfter = ""
	return plan, harnessResult, nil
}

type projectPlannerExecutionContext struct {
	ImplementerTimeout time.Duration
	ImplementerSupport string
	ReviewerSupport    string
}

func projectPlannerPrompt(skills []string, executionContext projectPlannerExecutionContext, repository, idea string) string {
	// Planning workflow lives in the installed skill. This prompt contains only
	// selected skill names, runtime budget, operator-selected downstream support,
	// and approved project context.
	var b strings.Builder
	b.WriteString("Use these skills for this planner assignment: ")
	b.WriteString(strings.Join(skills, ", "))
	b.WriteString(".")
	if executionContext.ImplementerTimeout > 0 {
		fmt.Fprintf(&b, "\nConfigured implementer timeout: %s.", executionContext.ImplementerTimeout)
	}
	implementerSupport := config.EffectivePlanningSupport(executionContext.ImplementerSupport)
	reviewerSupport := config.EffectivePlanningSupport(executionContext.ReviewerSupport)
	if implementerSupport == config.PlanningSupportHigh || reviewerSupport == config.PlanningSupportHigh {
		b.WriteString("\nOperator-selected downstream task sizing:")
		fmt.Fprintf(&b, "\n- Implementer: %s.", implementerSupport)
		fmt.Fprintf(&b, "\n- Reviewer: %s.", reviewerSupport)
		b.WriteString("\nApply the planning-support behavior defined by the runner-planner skill. Support affects decomposition and specificity, never correctness, scope, or verification rigor.")
	}
	if repository = strings.TrimSpace(repository); repository != "" {
		fmt.Fprintf(&b, "\nCanonical repository: %s. Runner binds it to every work item; do not return repository fields.", repository)
	}
	b.WriteString("\nInspect repository instructions, manifests, scripts, and existing tests before defining proof obligations. Describe what must be proven, not the commands or test framework; the implementer chooses the smallest reliable method after inspecting the affected code. Never require temporary scripts or overlapping checks.")
	b.WriteString("\nTreat optional technologies in the project idea as permission, not requirements or preferred defaults. Add an optional dependency or external service only when it materially simplifies the requested result without weakening local development or verification.")
	b.WriteString("\nFor a multi-item plan, include a final project-readiness card when the complete result needs integration or release proof that no earlier card can establish. Its proof obligations cover the established complete local suite and the smallest required real-entrypoint smoke. Do not invent a browser, deployment, or other interface requirement.")
	b.WriteString("\n\nApproved project idea:\n--- BEGIN PROJECT IDEA ---\n")
	b.WriteString(strings.TrimSpace(idea))
	b.WriteString("\n--- END PROJECT IDEA ---")
	return b.String()
}

func (s *Engine) ApplyProjectPlan(ctx context.Context, plan ProjectPlan) ([]github.WorkItem, error) {
	target, err := s.prepareDirectProjectPlan(&plan)
	if err != nil {
		return nil, err
	}
	created, err := s.applyProjectPlanAtStatus(ctx, plan, target)
	return created, err
}

func (s *Engine) PlanStagedProjectPlanApproval(ctx context.Context, batchFingerprint string) (ProjectPlanApproval, error) {
	batchFingerprint = strings.TrimSpace(batchFingerprint)
	if batchFingerprint == "" {
		return ProjectPlanApproval{}, errors.New("staged batch fingerprint is required")
	}
	items, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return ProjectPlanApproval{}, err
	}
	children := make([]github.WorkItem, 0)
	for _, item := range items {
		if item.PlanningSourceID == "" && item.PlanningSourceLane == directProjectPlanSourceLane && item.PlanningBatchFingerprint == batchFingerprint {
			children = append(children, item)
		}
	}
	return s.validateStagedProjectPlanApproval(batchFingerprint, children)
}

func (s *Engine) validateStagedProjectPlanApproval(batchFingerprint string, children []github.WorkItem) (ProjectPlanApproval, error) {
	target, err := s.directProjectPlanDestination()
	if err != nil {
		return ProjectPlanApproval{}, err
	}
	if len(children) == 0 || len(children) > github.MaxPlanningBatchChildren {
		return ProjectPlanApproval{}, fmt.Errorf("staged project plan has %d children; expected between 1 and %d", len(children), github.MaxPlanningBatchChildren)
	}
	sort.Slice(children, func(left, right int) bool {
		return children[left].PlanningItemIndex < children[right].PlanningItemIndex
	})
	assessmentStatus := s.cfg.LaneStatus(s.cfg.EffectiveWorkflow().IntakeLane)
	sourceFingerprint := strings.TrimSpace(children[0].PlanningSourceFingerprint)
	if sourceFingerprint == "" {
		return ProjectPlanApproval{}, errors.New("staged project plan source fingerprint is missing")
	}
	for index, child := range children {
		if child.PlanningSourceFingerprint != sourceFingerprint || child.PlanningBatchSize != len(children) || child.PlanningItemIndex != index+1 || child.PlanningDestination != target {
			return ProjectPlanApproval{}, errors.New("staged project plan is incomplete, duplicated, reordered, or has a changed destination")
		}
		if strings.TrimSpace(child.ID) == "" || strings.TrimSpace(child.Title) == "" || strings.TrimSpace(child.Body) == "" {
			return ProjectPlanApproval{}, fmt.Errorf("staged project plan child %d is incomplete", index+1)
		}
		if !strings.EqualFold(strings.TrimSpace(child.Status), strings.TrimSpace(assessmentStatus)) {
			return ProjectPlanApproval{}, fmt.Errorf("staged project plan child %d has partial approval, release, or changed status", index+1)
		}
		if github.HasRuntimeActionState(child) {
			return ProjectPlanApproval{}, fmt.Errorf("staged project plan child %d contains prior Runner action state", index+1)
		}
	}
	if err := github.ValidatePlanningDependencies(children); err != nil {
		return ProjectPlanApproval{}, err
	}
	if err := s.source.ValidateDirectPlanningBatchStaging(children); err != nil {
		return ProjectPlanApproval{}, err
	}
	return ProjectPlanApproval{BatchFingerprint: batchFingerprint, Destination: target, Children: children}, nil
}

func (s *Engine) ApplyProjectPlanApproval(ctx context.Context, approval ProjectPlanApproval) ([]github.WorkItem, error) {
	if strings.TrimSpace(approval.BatchFingerprint) == "" || len(approval.Children) == 0 {
		return nil, errors.New("staged plan approval preview is incomplete; preview the complete batch again")
	}
	itemIDs := make([]string, len(approval.Children))
	for index := range approval.Children {
		itemIDs[index] = approval.Children[index].ID
	}
	children, err := s.source.LifecycleItemsByID(ctx, itemIDs)
	if err != nil {
		return nil, fmt.Errorf("staged plan changed after the approval preview: %w", err)
	}
	refreshed, err := s.validateStagedProjectPlanApproval(approval.BatchFingerprint, children)
	if err != nil {
		return nil, fmt.Errorf("staged plan changed after the approval preview: %w", err)
	}
	if !reflect.DeepEqual(approval, refreshed) {
		return nil, errors.New("staged plan changed after the approval preview; review the complete batch again")
	}
	return s.source.ReleaseStaged(ctx, refreshed.Children, refreshed.Destination)
}

func (s *Engine) prepareDirectProjectPlan(plan *ProjectPlan) (string, error) {
	if err := s.normalizeProjectPlan(plan); err != nil {
		return "", err
	}
	if len(plan.OpenDecisions) > 0 {
		return "", fmt.Errorf(
			"cannot stage cards while %d open decision(s) require human input; add the answers to the project idea and rerun with --create",
			len(plan.OpenDecisions),
		)
	}
	if strings.TrimSpace(plan.SourceContext) == "" {
		return "", errors.New("project plan is missing the original project request")
	}
	return s.directProjectPlanDestination()
}

func (s *Engine) directProjectPlanDestination() (string, error) {
	workflow := s.cfg.EffectiveWorkflow()
	plannerRole := s.cfg.RoleIDForContract(config.WorkRolePlanner)
	for _, laneID := range s.cfg.AgentLaneIDs() {
		lane := workflow.Lanes[laneID]
		if lane.Role == plannerRole {
			return workflow.Lanes[lane.CreatesIn].Name, nil
		}
	}
	return "", errors.New("workflow has no planner lane with a creates_in destination")
}

func (s *Engine) normalizeProjectPlan(plan *ProjectPlan) error {
	if plan == nil {
		return errors.New("project plan is required")
	}
	normalized, err := normalizeProjectPlan(*plan)
	if err != nil {
		return err
	}
	if err := s.normalizePlanRepositories(&normalized); err != nil {
		return err
	}
	*plan = normalized
	return nil
}

func (s *Engine) normalizePlanRepositories(plan *ProjectPlan) error {
	if plan == nil {
		return errors.New("project plan is required")
	}
	repository := strings.TrimSpace(s.cfg.GitHubProject.IntakeRepository)
	if repository == "" {
		return errors.New("github_project.intake_repository is required for planning")
	}
	_, repositoryName, hasOwner := strings.Cut(repository, "/")
	for index := range plan.WorkItems {
		plannedRepository := strings.TrimSpace(plan.WorkItems[index].Repository)
		if plannedRepository == "" || hasOwner && strings.EqualFold(plannedRepository, repositoryName) {
			plan.WorkItems[index].Repository = repository
			continue
		}
		if !strings.EqualFold(plannedRepository, repository) {
			return fmt.Errorf("project plan work_items[%d] repository %q does not match configured repository %q", index, plannedRepository, repository)
		}
		plan.WorkItems[index].Repository = repository
	}
	return nil
}

func (s *Engine) applyProjectPlanAtStatus(ctx context.Context, plan ProjectPlan, status string) ([]github.WorkItem, error) {
	if _, err := s.source.Inspect(ctx); err != nil {
		return nil, err
	}
	plannedItems, fingerprint, err := directProjectPlanItems(plan, status)
	if err != nil {
		return nil, err
	}
	existing, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return nil, err
	}
	matched, err := matchDirectProjectPlanChildren(existing, plannedItems, fingerprint, s.cfg.LaneStatus(s.cfg.EffectiveWorkflow().IntakeLane))
	if err != nil {
		return nil, err
	}
	if len(matched) > 0 {
		if err := s.source.ValidateRecoverableDirectPlanningChildren(matched); err != nil {
			byIndex := make(map[int]github.WorkItem, len(matched))
			for _, child := range matched {
				byIndex[child.PlanningItemIndex] = child
			}
			if provenanceErr := validatePlanningChildProvenance(s.source, plannedItems, byIndex); provenanceErr != nil {
				return nil, fmt.Errorf("reject unauthenticated staged planning metadata: %w", provenanceErr)
			}
		}
	}
	byIndex := make(map[int]github.WorkItem, len(matched))
	for _, item := range matched {
		byIndex[item.PlanningItemIndex] = item
	}
	created := make([]github.WorkItem, 0, len(plannedItems))
	for index, planned := range plannedItems {
		if item, ok := byIndex[index+1]; ok {
			if strings.TrimSpace(item.Status) == "" {
				item, err = s.source.EnsureStaged(ctx, item)
				if err != nil {
					return created, fmt.Errorf("resume staged child %d of %d: %w", index+1, len(plannedItems), err)
				}
			}
			created = append(created, item)
			continue
		}
		item, err := s.source.CreateStaged(ctx, planned)
		if err != nil {
			return created, fmt.Errorf("staged %d of %d items before failure: %w", len(created), len(plannedItems), err)
		}
		created = append(created, item)
	}
	return s.finalizePlanningChildren(ctx, plannedItems, created)
}

func directProjectPlanItems(plan ProjectPlan, destination string) ([]github.PlannedItem, string, error) {
	sourceDigest := sha256.Sum256([]byte(strings.TrimSpace(plan.SourceContext)))
	sourceFingerprint := fmt.Sprintf("v1:%x", sourceDigest[:])
	fingerprint, err := planningBatchFingerprint(sourceFingerprint, directProjectPlanSourceLane, destination, plan)
	if err != nil {
		return nil, "", err
	}
	plannedItems := projectWorkItems(plan)
	for index := range plannedItems {
		plannedItems[index].PlanningSourceLane = directProjectPlanSourceLane
		plannedItems[index].PlanningSourceFingerprint = sourceFingerprint
		plannedItems[index].PlanningDestination = strings.TrimSpace(destination)
		plannedItems[index].PlanningBatchFingerprint = fingerprint
		plannedItems[index].PlanningBatchSize = len(plannedItems)
		plannedItems[index].PlanningItemIndex = index + 1
	}
	return plannedItems, fingerprint, nil
}

func matchDirectProjectPlanChildren(items []github.WorkItem, plannedItems []github.PlannedItem, fingerprint, assessmentStatus string) ([]github.WorkItem, error) {
	if len(plannedItems) == 0 {
		return nil, errors.New("direct project plan has no children")
	}
	sourceFingerprint := plannedItems[0].PlanningSourceFingerprint
	matches := make(map[int]github.WorkItem, len(plannedItems))
	for _, candidate := range items {
		if candidate.PlanningSourceID != "" || candidate.PlanningSourceLane != directProjectPlanSourceLane || candidate.PlanningSourceFingerprint != sourceFingerprint {
			continue
		}
		if candidate.PlanningBatchFingerprint != fingerprint {
			return nil, errors.New("this planning request already has children from a different interrupted batch; review the staged cards before retrying")
		}
		index := candidate.PlanningItemIndex
		if index < 1 || index > len(plannedItems) {
			return nil, errors.New("staged planning batch contains an unexpected child index")
		}
		if _, exists := matches[index]; exists {
			return nil, fmt.Errorf("staged planning batch contains duplicate child index %d", index)
		}
		planned := plannedItems[index-1]
		if candidate.PlanningBatchSize != len(plannedItems) || candidate.PlanningDestination != planned.PlanningDestination ||
			strings.TrimSpace(candidate.Title) != strings.TrimSpace(planned.Title) {
			return nil, fmt.Errorf("staged planning child %d changed after creation; human review is required", index)
		}
		status := strings.TrimSpace(candidate.Status)
		if status != "" && !strings.EqualFold(status, strings.TrimSpace(assessmentStatus)) {
			return nil, fmt.Errorf("staged planning child %d has partial approval, release, or changed status; it was not accepted as staged", index)
		}
		matches[index] = candidate
	}
	if err := validateRecoverablePlanningBodies(plannedItems, matches); err != nil {
		return nil, err
	}
	children := make([]github.WorkItem, 0, len(matches))
	for index := 1; index <= len(plannedItems); index++ {
		if item, ok := matches[index]; ok {
			children = append(children, item)
		}
	}
	return children, nil
}

func (s *Engine) applyPlannerBatch(ctx context.Context, source github.AuthorizedAction, plan ProjectPlan, sourceLane string) ([]github.WorkItem, error) {
	if err := s.normalizeProjectPlan(&plan); err != nil {
		return nil, err
	}
	destination := s.cfg.LaneStatus(s.cfg.EffectiveWorkflow().Lanes[sourceLane].CreatesIn)
	if strings.TrimSpace(sourceLane) == "" || destination == "" {
		return nil, errors.New("planning batch requires its originating lane and destination")
	}
	currentSource, err := s.source.RefreshAction(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("validate planning source before applying its batch: %w", err)
	}
	sourceItem := currentSource.Item
	fingerprint, err := planningBatchFingerprint(sourceItem.ID, sourceLane, destination, plan)
	if err != nil {
		return nil, err
	}
	plannedItems := projectWorkItems(plan)
	for index := range plannedItems {
		plannedItems[index].PlanningSourceID = strings.TrimSpace(sourceItem.ID)
		plannedItems[index].PlanningSourceLane = strings.TrimSpace(sourceLane)
		plannedItems[index].PlanningSourceFingerprint = github.PlanningSourceFingerprint(sourceItem)
		plannedItems[index].PlanningDestination = strings.TrimSpace(destination)
		plannedItems[index].PlanningBatchFingerprint = fingerprint
		plannedItems[index].PlanningBatchSize = len(plannedItems)
		plannedItems[index].PlanningItemIndex = index + 1
	}
	existing, err := s.source.LifecycleItems(ctx)
	if err != nil {
		return nil, err
	}
	matched := make(map[int]github.WorkItem, len(plannedItems))
	assessmentStatus := s.cfg.LaneStatus(s.cfg.EffectiveWorkflow().IntakeLane)
	for _, candidate := range existing {
		if candidate.PlanningSourceID != sourceItem.ID {
			continue
		}
		if candidate.PlanningBatchFingerprint != fingerprint {
			return nil, fmt.Errorf("planning source %s already has children from a different interrupted batch", sourceItem.ID)
		}
		index := candidate.PlanningItemIndex
		if index < 1 || index > len(plannedItems) {
			return nil, fmt.Errorf("planning source %s has an unexpected child index %d", sourceItem.ID, index)
		}
		if _, exists := matched[index]; exists {
			return nil, fmt.Errorf("planning source %s has duplicate child index %d", sourceItem.ID, index)
		}
		planned := plannedItems[index-1]
		if candidate.PlanningBatchSize != len(plannedItems) || strings.TrimSpace(candidate.Title) != strings.TrimSpace(planned.Title) {
			return nil, fmt.Errorf("planning source %s child %d changed after creation; human review is required", sourceItem.ID, index)
		}
		status := strings.TrimSpace(candidate.Status)
		if status != "" && !strings.EqualFold(status, assessmentStatus) {
			return nil, fmt.Errorf("planning source %s child %d has partial approval or release state; it was not accepted as staged", sourceItem.ID, index)
		}
		matched[index] = candidate
	}
	if err := validateRecoverablePlanningBodies(plannedItems, matched); err != nil {
		return nil, fmt.Errorf("planning source %s: %w", sourceItem.ID, err)
	}
	if err := validatePlanningChildProvenance(s.source, plannedItems, matched); err != nil {
		return nil, fmt.Errorf("planning source %s has invalid child creation provenance: %w", sourceItem.ID, err)
	}
	created := make([]github.WorkItem, 0, len(plannedItems))
	for index, planned := range plannedItems {
		if existingChild, ok := matched[index+1]; ok {
			if strings.TrimSpace(existingChild.Status) == "" {
				matchedItem, ensureErr := s.source.EnsureStaged(ctx, existingChild)
				if ensureErr != nil {
					return created, fmt.Errorf("resume planning source %s child %d in staging: %w", sourceItem.ID, index+1, ensureErr)
				}
				existingChild = matchedItem
			}
			created = append(created, existingChild)
			continue
		}
		item, createErr := s.source.CreateStagedFrom(ctx, currentSource, planned)
		if strings.TrimSpace(item.ID) != "" {
			created = append(created, item)
		}
		if createErr != nil {
			return created, fmt.Errorf("created %d of %d staged items before failure: %w", len(created), len(plannedItems), createErr)
		}
	}
	return s.finalizePlanningChildren(ctx, plannedItems, created)
}

func validateRecoverablePlanningBodies(planned []github.PlannedItem, matched map[int]github.WorkItem) error {
	if len(matched) < len(planned) {
		for index, child := range matched {
			if child.Body != github.FormatPlannedItemBody(planned[index-1]) {
				return fmt.Errorf("staged planning child %d was finalized before the complete batch existed or changed after creation", index)
			}
		}
		return nil
	}
	children := make([]github.WorkItem, len(planned))
	for index := range children {
		children[index] = matched[index+1]
	}
	bound, err := bindPlanningDependencyIDs(planned, children)
	if err != nil {
		return err
	}
	for index, child := range children {
		provisional := github.FormatPlannedItemBody(planned[index])
		canonical := github.FormatPlannedItemBody(bound[index])
		if child.Body != provisional && child.Body != canonical {
			return fmt.Errorf("staged planning child %d changed during dependency finalization", index+1)
		}
	}
	return nil
}

func validatePlanningChildProvenance(source *github.Project, planned []github.PlannedItem, matched map[int]github.WorkItem) error {
	if len(matched) < len(planned) {
		for index, child := range matched {
			if err := source.ValidateStagedPlanningChildBodies(child, github.FormatPlannedItemBody(planned[index-1])); err != nil {
				return fmt.Errorf("staged planning child %d: %w", index, err)
			}
		}
		return nil
	}
	children := make([]github.WorkItem, len(planned))
	for index := range children {
		children[index] = matched[index+1]
	}
	bound, err := bindPlanningDependencyIDs(planned, children)
	if err != nil {
		return err
	}
	for index, child := range children {
		if err := source.ValidateStagedPlanningChildBodies(child, github.FormatPlannedItemBody(planned[index]), github.FormatPlannedItemBody(bound[index])); err != nil {
			return fmt.Errorf("staged planning child %d: %w", index+1, err)
		}
	}
	return nil
}

func bindPlanningDependencyIDs(planned []github.PlannedItem, children []github.WorkItem) ([]github.PlannedItem, error) {
	if len(planned) == 0 || len(planned) != len(children) {
		return nil, errors.New("cannot bind dependency IDs until every staged child exists")
	}
	titles := make(map[string]int, len(planned))
	ids := make(map[string]bool, len(children))
	for index := range planned {
		key := strings.ToLower(strings.TrimSpace(planned[index].Title))
		id := strings.TrimSpace(children[index].ID)
		if key == "" || id == "" || ids[id] || strings.TrimSpace(children[index].Title) != strings.TrimSpace(planned[index].Title) {
			return nil, errors.New("staged planning children are missing, duplicated, reordered, or changed before dependency binding")
		}
		titles[key] = index
		ids[id] = true
	}
	bound := append([]github.PlannedItem(nil), planned...)
	for index := range bound {
		bound[index].ResolvedDependencies = nil
		bound[index].DependencyIDsResolved = true
		seen := map[string]bool{}
		for _, dependencyTitle := range bound[index].Dependencies {
			dependencyIndex, ok := titles[strings.ToLower(strings.TrimSpace(dependencyTitle))]
			if !ok {
				return nil, fmt.Errorf("planned child %d references unknown dependency title %q", index+1, dependencyTitle)
			}
			dependencyID := strings.TrimSpace(children[dependencyIndex].ID)
			if dependencyIndex == index || seen[dependencyID] {
				return nil, fmt.Errorf("planned child %d has a self or duplicate dependency", index+1)
			}
			seen[dependencyID] = true
			bound[index].ResolvedDependencies = append(bound[index].ResolvedDependencies, github.PlannedDependency{
				ItemID: dependencyID, Title: strings.TrimSpace(children[dependencyIndex].Title),
			})
		}
	}
	return bound, nil
}

func (s *Engine) finalizePlanningChildren(ctx context.Context, planned []github.PlannedItem, children []github.WorkItem) ([]github.WorkItem, error) {
	bound, err := bindPlanningDependencyIDs(planned, children)
	if err != nil {
		return children, err
	}
	finalized := append([]github.WorkItem(nil), children...)
	for index := range finalized {
		if finalized[index].Body == github.FormatPlannedItemBody(bound[index]) {
			continue
		}
		finalized[index], err = s.source.FinalizeStaged(ctx, finalized[index], bound[index])
		if err != nil {
			return finalized[:index], fmt.Errorf("finalize dependency metadata for staged child %d of %d: %w", index+1, len(finalized), err)
		}
	}
	if len(finalized) > 0 && strings.TrimSpace(finalized[0].PlanningSourceID) == "" {
		finalized, err = s.source.AuthenticateDirectPlanningBatch(ctx, finalized)
		if err != nil {
			return finalized, err
		}
	}
	return finalized, nil
}

func projectWorkItems(plan ProjectPlan) []github.PlannedItem {
	items := append([]github.PlannedItem(nil), plan.WorkItems...)
	for index := range items {
		items[index].ProjectGoal = strings.TrimSpace(plan.GoalSummary)
		items[index].ProjectSuccessCriteria = compactNonEmpty(plan.ProjectSuccessCriteria)
		items[index].ProjectConstraints = compactNonEmpty(plan.ProjectConstraints)
		items[index].ProjectSource = strings.TrimSpace(plan.SourceContext)
	}
	return items
}

func planningBatchFingerprint(sourceID, sourceLane, destination string, plan ProjectPlan) (string, error) {
	payload := struct {
		SourceID      string      `json:"source_id"`
		SourceLane    string      `json:"source_lane"`
		Destination   string      `json:"destination"`
		SourceContext string      `json:"source_context"`
		Plan          ProjectPlan `json:"plan"`
	}{
		SourceID: strings.TrimSpace(sourceID), SourceLane: strings.TrimSpace(sourceLane), Destination: strings.TrimSpace(destination),
		SourceContext: strings.TrimSpace(plan.SourceContext), Plan: plan,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode planning batch identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("v1:%x", digest[:]), nil
}

func (s *Engine) runPlannerHarness(ctx context.Context, role, harness, workingDir, prompt string) (execution.StructuredHarnessResult, error) {
	cfg := s.executionConfig(role, harness, workingDir)
	// Each subprocess receives the configured timeout. Parent cancellation still
	// stops both calls, while one slow outline does not steal the details budget.
	return runStagedProjectPlanner(ctx, prompt, s.cfg.GitHubProject.IntakeRepository, func(callCtx context.Context, stagePrompt string, schema []byte) (execution.StructuredHarnessResult, error) {
		return execution.RunPlannerStageWithUsage(callCtx, harness, cfg, workingDir, stagePrompt, schema, s.run)
	}, func(callCtx context.Context, stagePrompt string, schema []byte) (execution.StructuredHarnessResult, error) {
		return execution.RunPlannerSynthesisStageWithUsage(callCtx, harness, cfg, stagePrompt, schema, s.run)
	})
}

func decodeProjectPlan(value string) (ProjectPlan, error) {
	value = execution.NormalizeStructuredResult(value)
	if err := validateProjectPlanWorkItemLimit(value); err != nil {
		return ProjectPlan{}, err
	}
	canonical, err := execution.CanonicalizeStructuredResult(value, "goal_summary", "project_success_criteria", "project_constraints", "open_decisions", "work_items")
	if err != nil {
		return ProjectPlan{}, fmt.Errorf("canonicalize project plan: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var plan ProjectPlan
	if err := decoder.Decode(&plan); err != nil {
		return ProjectPlan{}, fmt.Errorf("decode project plan: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return ProjectPlan{}, errors.New("project plan must contain exactly one JSON object")
	}
	return normalizeProjectPlan(plan)
}

// validateProjectPlanWorkItemLimit walks the JSON token stream before decoding
// ProjectPlan. This prevents an untrusted model response from allocating an
// arbitrarily large []PlannedItem before the production limit can be checked.
func validateProjectPlanWorkItemLimit(value string) error {
	decoder := json.NewDecoder(strings.NewReader(value))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode project plan: %w", err)
	}
	if opening != json.Delim('{') {
		return errors.New("project plan must be a JSON object")
	}
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode project plan: %w", err)
		}
		field, ok := fieldToken.(string)
		if !ok {
			return errors.New("project plan contains a non-string field name")
		}
		if field != "work_items" {
			if err := skipJSONValue(decoder); err != nil {
				return fmt.Errorf("decode project plan: %w", err)
			}
			continue
		}
		arrayToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode project plan work_items: %w", err)
		}
		if arrayToken != json.Delim('[') {
			return errors.New("project plan work_items must be an array")
		}
		count := 0
		for decoder.More() {
			count++
			if count > github.MaxPlanningBatchChildren {
				return fmt.Errorf("project plan has more than %d work items; the emergency safety maximum is %d", github.MaxPlanningBatchChildren, github.MaxPlanningBatchChildren)
			}
			if err := skipJSONValue(decoder); err != nil {
				return fmt.Errorf("decode project plan work_items[%d]: %w", count-1, err)
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim(']') {
			if err != nil {
				return fmt.Errorf("decode project plan work_items: %w", err)
			}
			return errors.New("project plan work_items is not a complete array")
		}
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		if err != nil {
			return fmt.Errorf("decode project plan: %w", err)
		}
		return errors.New("project plan is not a complete JSON object")
	}
	return nil
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != json.Delim('{') && delim != json.Delim('[') {
		return nil
	}
	depth := 1
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		delim, ok = token.(json.Delim)
		if !ok {
			continue
		}
		switch delim {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}

func normalizeProjectPlan(plan ProjectPlan) (ProjectPlan, error) {
	if len(plan.WorkItems) > github.MaxPlanningBatchChildren {
		return ProjectPlan{}, fmt.Errorf("project plan has %d work items; the emergency safety maximum is %d", len(plan.WorkItems), github.MaxPlanningBatchChildren)
	}
	plan.GoalSummary = strings.TrimSpace(plan.GoalSummary)
	plan.ProjectSuccessCriteria = compactNonEmpty(plan.ProjectSuccessCriteria)
	plan.ProjectConstraints = compactNonEmpty(plan.ProjectConstraints)
	plan.OpenDecisions = compactNonEmpty(plan.OpenDecisions)
	plan.SourceContext = strings.TrimSpace(plan.SourceContext)
	if plan.GoalSummary == "" || len(plan.ProjectSuccessCriteria) == 0 || len(plan.WorkItems) == 0 {
		return ProjectPlan{}, errors.New("project plan requires a goal summary, project success criteria, and at least one work item")
	}
	titles := map[string]int{}
	for index := range plan.WorkItems {
		item := &plan.WorkItems[index]
		item.Title = strings.TrimSpace(item.Title)
		item.Repository = strings.TrimSpace(item.Repository)
		item.Summary = strings.TrimSpace(item.Summary)
		if item.Risks == nil || item.NonGoals == nil {
			return ProjectPlan{}, fmt.Errorf("project plan work_items[%d] must explicitly include risks and non_goals arrays", index)
		}
		item.AcceptanceCriteria = compactNonEmpty(item.AcceptanceCriteria)
		item.Verification = compactNonEmpty(item.Verification)
		item.Risks = compactNonEmpty(item.Risks)
		item.NonGoals = compactNonEmpty(item.NonGoals)
		item.Dependencies = compactNonEmpty(item.Dependencies)
		if item.Title == "" || item.Summary == "" || len(item.AcceptanceCriteria) == 0 || len(item.Verification) == 0 {
			return ProjectPlan{}, fmt.Errorf("project plan work_items[%d] is incomplete", index)
		}
		key := strings.ToLower(item.Title)
		if _, exists := titles[key]; exists {
			return ProjectPlan{}, fmt.Errorf("project plan contains duplicate title %q", item.Title)
		}
		titles[key] = index
	}
	dependencies := make([][]int, len(plan.WorkItems))
	for index := range plan.WorkItems {
		seenDependencies := map[int]bool{}
		for _, dependency := range plan.WorkItems[index].Dependencies {
			dependencyIndex, exists := titles[strings.ToLower(strings.TrimSpace(dependency))]
			if !exists {
				return ProjectPlan{}, fmt.Errorf("project plan work_items[%d] references unknown dependency %q", index, dependency)
			}
			if dependencyIndex == index {
				return ProjectPlan{}, fmt.Errorf("project plan work_items[%d] cannot depend on itself", index)
			}
			if seenDependencies[dependencyIndex] {
				return ProjectPlan{}, fmt.Errorf("project plan work_items[%d] repeats dependency %q", index, plan.WorkItems[dependencyIndex].Title)
			}
			seenDependencies[dependencyIndex] = true
			dependencies[index] = append(dependencies[index], dependencyIndex)
		}
		sort.Ints(dependencies[index])
		plan.WorkItems[index].Dependencies = make([]string, len(dependencies[index]))
		for dependencyPosition, dependencyIndex := range dependencies[index] {
			plan.WorkItems[index].Dependencies[dependencyPosition] = plan.WorkItems[dependencyIndex].Title
		}
	}
	states := make([]uint8, len(plan.WorkItems))
	var visit func(int) error
	visit = func(index int) error {
		switch states[index] {
		case 1:
			return fmt.Errorf("project plan contains a cyclic dependency involving %q", plan.WorkItems[index].Title)
		case 2:
			return nil
		}
		states[index] = 1
		for _, dependencyIndex := range dependencies[index] {
			if err := visit(dependencyIndex); err != nil {
				return err
			}
		}
		states[index] = 2
		return nil
	}
	for index := range plan.WorkItems {
		if err := visit(index); err != nil {
			return ProjectPlan{}, err
		}
	}
	return plan, nil
}
