package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

// A final destination refresh can produce accepted R from integrated P. If
// publication pushes R and stops before its Project transition, normal plan
// synchronization must not reject our own R or overwrite the retained proof.
// This runs inside the admitted QA resource claim and plan lock, before any fetch
// or preparation; absent acceptance leaves the normal exact-P path unchanged.
func (s *Engine) resumeAcceptedPlanPublication(ctx context.Context, action github.AuthorizedAction, lane config.ResolvedWorkflowLane, result RunResult, repoRoot string) (RunResult, bool) {
	if action.Item.PlanRelease == "" {
		return RunResult{}, false
	}
	fail := func(err error) (RunResult, bool) {
		return s.failExecution(ctx, action, lane, result, retainedAcceptanceResumeFailure, err, integrityViolationOutput(retainedAcceptanceResumeFailure, err)), true
	}
	delivery, _, err := s.planGate(ctx, action)
	if err != nil {
		return fail(err)
	}
	content, err := action.DelegatedContent()
	if err != nil {
		return fail(err)
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	metadata, err := provider.InspectRetainedReview(ctx, s.workspaceRequestForItem(action.Item, content.Digest, repoRoot, false))
	if errors.Is(err, os.ErrNotExist) {
		return RunResult{}, false
	}
	if err != nil {
		return fail(err)
	}
	if metadata.Identity.DelegatedContentDigest != content.Digest {
		return fail(errors.New("retained plan workspace belongs to different approved content"))
	}
	snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
	if err != nil {
		return fail(err)
	}
	record, accepted, err := provider.LoadPublicationAcceptance(ctx, metadata, snapshot, workspace.PublicationEvidence{PlanRevision: delivery.Revision})
	if err != nil {
		return fail(err)
	}
	if !accepted {
		return RunResult{}, false
	}
	if err := s.validateCompletePlanEvidence(ctx, action, metadata, record); err != nil {
		return fail(err)
	}
	result.ResumedCheckpoint = true
	result.WorktreePath, result.Branch = metadata.WorktreePath, metadata.BranchName
	observeWorkspaceLineage(&result, metadata)
	observeCandidateLineage(&result, workspace.Candidate{CommitOID: record.CommitOID, TreeOID: record.TreeOID})
	lineage := observedLineage(&result)
	lineage.ReviewedCandidate = metrics.ObjectIdentity{CommitOID: record.CommitOID, TreeOID: record.TreeOID}
	lineage.EvidenceCandidate = lineage.ReviewedCandidate
	manager := github.NewPullRequestManager(s.run, s.source)
	published, found, err := manager.RecoverPlanPublication(ctx, action, metadata, record, s.baseBranch(), s.remoteName(), func(ctx context.Context, fresh github.AuthorizedAction) error {
		return s.validateCompletePlanEvidence(ctx, fresh, metadata, record)
	})
	if err != nil {
		return fail(err)
	}
	if found {
		// Terminal reconciliation, not this recovered publication, decides
		// delivery. An exact closed-but-unmerged PR will therefore block.
		return s.finishAcceptedQAPublication(ctx, action, lane, result, record, published), true
	}
	// No PR exists yet. The existing guarded publisher accepts only P or
	// exact accepted R, skipping the push when R is already present.
	return s.publishAcceptedQA(ctx, action, lane, result, repoRoot, metadata, record), true
}

func (s *Engine) deliveryManifest(source github.WorkItem, plan ProjectPlan, children []github.WorkItem) (github.PlanManifest, error) {
	if len(children) != len(plan.WorkItems) {
		return github.PlanManifest{}, errors.New("delivery manifest requires the complete generated member set")
	}
	manifest := github.PlanManifest{
		Version: 1, Request: source.Body, Outcome: plan.GoalSummary,
		SuccessCriteria: plan.ProjectSuccessCriteria, Scope: plan.ProjectConstraints, Decisions: plan.OpenDecisions,
		Repository: s.cfg.GitHubProject.IntakeRepository, DestinationBranch: s.baseBranch(),
		CompleteVerification: s.cfg.GitHubProject.PlanVerificationID, VerificationDigest: s.cfg.GitHubProject.PlanVerificationDigest,
	}
	for i, child := range children {
		deps := append([]string{}, child.Dependencies...)
		sort.Strings(deps)
		manifest.Members = append(manifest.Members, github.PlanMember{ID: child.ID, Dependencies: deps, ImplementationProfile: child.ImplementationProfile,
			ProfileDigest: s.cfg.GitHubProject.PlanProfileDigests[child.ImplementationProfile], ProfileReason: plan.WorkItems[i].ProfileReason})
	}
	return manifest, nil
}

func (s *Engine) stagePlannerResult(ctx context.Context, action github.AuthorizedAction, plan ProjectPlan, children []github.WorkItem, detail string) error {
	if !s.cfg.GitHubProject.PlanDelivery {
		return s.source.StagePlanningApproval(ctx, action, children, detail)
	}
	manifest, err := s.deliveryManifest(action.Item, plan, children)
	if err != nil {
		return err
	}
	return s.source.StageDeliveryPlanningApproval(ctx, action, children, manifest, detail)
}

func (s *Engine) applyDeliveryProjectPlan(ctx context.Context, plan ProjectPlan, target string) ([]github.WorkItem, error) {
	lane := s.cfg.EffectiveWorkflow().PlanLane
	fingerprint, err := planningBatchFingerprint("", lane, target, plan)
	if err != nil {
		return nil, err
	}
	parent, staged, err := s.source.EnsureDeliveryPlanningParent(ctx, plan.GoalSummary, plan.SourceContext, fingerprint)
	if err != nil {
		return nil, err
	}
	if staged {
		approval, err := s.source.PlanApproval(ctx, parent.ID)
		if err != nil {
			return nil, err
		}
		if approval.Batch == nil {
			return nil, errors.New("saved delivery proposal is not a complete staged batch")
		}
		children := make([]github.WorkItem, len(approval.Batch.Children))
		for i, child := range approval.Batch.Children {
			children[i] = child.Item
		}
		original := parent
		original.Body = plan.SourceContext
		manifest, err := s.deliveryManifest(original, plan, children)
		if err != nil {
			return nil, err
		}
		body, err := github.FormatPlanManifest(manifest)
		if err != nil || body != parent.Body {
			return nil, errors.New("saved delivery contract differs from the supplied plan; amendment requires a fresh preview")
		}
		expectedFingerprint, err := planningBatchFingerprint(parent.ID, lane, target, plan)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.PlanningBatchFingerprint != expectedFingerprint {
				return nil, errors.New("saved delivery members differ from the supplied plan")
			}
		}
		return children, nil
	}
	action, err := s.source.Authorize(ctx, parent)
	if err != nil {
		return nil, err
	}
	children, err := s.applyPlannerBatch(ctx, action, plan, lane)
	if err != nil {
		return children, err
	}
	if err := s.stagePlannerResult(ctx, action, plan, children, "Delivery plan staged for exact whole-plan approval."); err != nil {
		return children, err
	}
	ids := make([]string, len(children))
	for i, child := range children {
		ids[i] = child.ID
	}
	return s.source.LifecycleItemsByID(ctx, ids)
}

// bindDeliveryAssignment adds shared context only after validating both the
// immutable release and current parent/member lifecycle state. It is also
// included in checkpoint and review-baseline bindings, never a prose history.
func (s *Engine) bindDeliveryAssignment(ctx context.Context, item github.WorkItem, assignment *execution.Assignment) error {
	delivery, present, err := s.source.DeliveryForItem(ctx, item)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	memberIDs := make([]string, len(delivery.Manifest.Members))
	for i, member := range delivery.Manifest.Members {
		memberIDs[i] = member.ID
	}
	assignment.Spec.PlanContext = &execution.PlanContext{ID: delivery.Parent.ID, Revision: delivery.Revision, ApprovedBody: delivery.Parent.Body,
		Repository: delivery.Manifest.Repository, DestinationBranch: delivery.Manifest.DestinationBranch, Branch: delivery.Parent.Branch, MemberIDs: memberIDs}
	assignment.Spec.ReviewScope, assignment.Spec.VerificationBoundary = execution.ReviewScopeCard, execution.VerificationFocused
	if item.ID == delivery.Parent.ID {
		if !assignment.Spec.ReviewRequired || s.cfg.RoleContract(item.Role) != config.WorkRoleReviewer {
			return errors.New("complete verification handoff requires the authorized parent reviewer")
		}
		entry, configured := s.cfg.Verification[delivery.Manifest.CompleteVerification]
		if !configured || entry.Digest() != delivery.Manifest.VerificationDigest {
			return errors.New("approved complete verification entrypoint changed before parent review")
		}
		assignment.Spec.PlanContext.CompleteVerification = delivery.Manifest.CompleteVerification
		assignment.Spec.ReviewScope, assignment.Spec.VerificationBoundary = execution.ReviewScopePlan, execution.VerificationComplete
		assignment.Spec.RequiredVerification = append([]string(nil), delivery.Manifest.SuccessCriteria...)
		assignment.Spec.PlanMemberBriefs = nil
		for _, child := range delivery.Children {
			assignment.Spec.PlanMemberBriefs = append(assignment.Spec.PlanMemberBriefs, execution.PlanMemberBrief{ID: child.ID, ApprovedBody: child.Body})
		}
	}
	base := delivery.Parent.Branch
	if item.ID == delivery.Parent.ID {
		base = delivery.Manifest.DestinationBranch
	}
	assignment.Spec.Task.Instructions = strings.ReplaceAll(assignment.Spec.Task.Instructions, "Target base branch: "+s.baseBranch(), "Target base branch: "+base)
	assignment.Spec.Task.Instructions = strings.ReplaceAll(assignment.Spec.Task.Instructions, "Review comparison base: "+s.remoteName()+"/"+s.baseBranch(), "Review comparison base: "+s.remoteName()+"/"+base)
	return execution.ValidateAssignmentContext(assignment.Spec)
}

func (s *Engine) revalidateDeliveryAssignment(ctx context.Context, item github.WorkItem, assignment execution.Assignment) error {
	refreshed := assignment
	refreshed.Spec.PlanContext = nil
	if err := s.bindDeliveryAssignment(ctx, item, &refreshed); err != nil {
		return err
	}
	if !reflect.DeepEqual(assignment.Spec.PlanContext, refreshed.Spec.PlanContext) || !reflect.DeepEqual(assignment.Spec.PlanMemberBriefs, refreshed.Spec.PlanMemberBriefs) {
		return fmt.Errorf("plan revision changed during assignment; prior evidence cannot authorize a new scope")
	}
	return nil
}

func (s *Engine) lockPlan(id string) func() {
	lock, _ := s.planIntegrations.LoadOrStore(id, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Engine) baseBranchForItem(ctx context.Context, item github.WorkItem) (string, error) {
	delivery, present, err := s.source.DeliveryForItem(ctx, item)
	if err != nil {
		return "", err
	}
	if present && item.ID != delivery.Parent.ID {
		return delivery.Parent.Branch, nil
	}
	return s.baseBranch(), nil
}

func (s *Engine) fetchItemBase(ctx context.Context, item github.WorkItem, root string) (string, error) {
	if item.PlanRelease != "" {
		if err := s.verifyPlanHead(ctx, item, root); err != nil {
			return "", err
		}
	}
	base, err := s.baseBranchForItem(ctx, item)
	if err != nil {
		return "", err
	}
	result, err := s.git(ctx, []string{"fetch", s.remoteName(), remoteRefspec(s.remoteName(), base)}, root, 2*time.Minute)
	if err != nil {
		return "", fmt.Errorf("fetch assignment base: %w", commandFailure(err, result))
	}
	return base, nil
}

func (s *Engine) verifyPlanHead(ctx context.Context, item github.WorkItem, root string) error {
	delivery, present, err := s.source.DeliveryForItem(ctx, item)
	if err != nil || !present || delivery.Parent.ID != item.ID || delivery.Parent.QACommit != item.QACommit {
		return errors.Join(errors.New("plan head requires current parent authority"), err)
	}
	return workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).VerifyPlanBranch(ctx, root, delivery.Manifest.Repository, s.remoteName(), delivery.Parent.Branch, delivery.Parent.QACommit, func() error {
		current, present, err := s.source.DeliveryForItem(ctx, item)
		if err != nil || !present || current.Revision != delivery.Revision || current.Parent.QACommit != delivery.Parent.QACommit {
			return errors.Join(errors.New("authenticated plan head changed"), err)
		}
		return nil
	})
}

func (s *Engine) integratePlanAcceptance(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord) error {
	delivery, present, err := s.source.DeliveryForItem(ctx, action.Item)
	if err != nil || !present {
		return errors.Join(errors.New("accepted plan member has no current authority"), err)
	}
	unlock := s.lockPlan(delivery.Parent.ID)
	defer unlock()
	return s.integratePlanAcceptanceLocked(ctx, action, metadata, record)
}

func (s *Engine) integratePlanAcceptanceLocked(ctx context.Context, action github.AuthorizedAction, metadata workspace.Metadata, record workspace.PublicationRecord) error {
	delivery, present, err := s.source.DeliveryForItem(ctx, action.Item)
	if err != nil || !present {
		return errors.Join(errors.New("plan integration lost authority"), err)
	}
	if record.PlanRevision != delivery.Revision {
		return errors.New("accepted member belongs to another plan revision")
	}
	parent, err := s.source.Authorize(ctx, delivery.Parent)
	if err != nil {
		return err
	}
	if parent.Item.Phase == github.PlanDeliveryPhase {
		if parent.Item.QACommit != record.ApprovedBaseOID {
			return workspace.ErrPublicationBaseChanged
		}
		if err := s.source.BeginPlanIntegration(ctx, parent, action.Item.ID, record.CommitOID, record.ApprovedBaseOID); err != nil {
			return err
		}
	} else if github.PlanIntegrationMember(parent.Item) != action.Item.ID || parent.Item.QACommit != record.CommitOID {
		return errors.New("another plan integration owns the current head")
	}
	provider := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits())
	err = provider.IntegrateAccepted(ctx, metadata, record, s.remoteName(), delivery.Parent.Branch, func() error {
		current, present, err := s.source.DeliveryForItem(ctx, action.Item)
		if err != nil || !present {
			return errors.Join(errors.New("integration authority changed"), err)
		}
		if current.Revision != delivery.Revision || github.PlanIntegrationMember(current.Parent) != action.Item.ID || current.Parent.QACommit != record.CommitOID {
			return errors.New("integration intent changed before Git mutation")
		}
		refreshed, err := s.source.Authorize(ctx, action.Item)
		if err != nil {
			return err
		}
		content, err := refreshed.DelegatedContent()
		if err != nil || content.Digest != record.DelegatedContentDigest || refreshed.Item.Branch != metadata.BranchName {
			return errors.New("accepted member changed before integration")
		}
		action = refreshed
		return nil
	})
	if err != nil {
		return err
	}
	if action.Item.Phase != github.PlanIntegratedPhase {
		if err := s.source.TransitionPlanIntegrated(ctx, action, record.CommitOID); err != nil {
			return err
		}
	}
	parent, err = s.source.Authorize(ctx, github.WorkItem{ID: delivery.Parent.ID})
	if err != nil {
		return err
	}
	return s.source.RecordPlanHead(ctx, parent, record.CommitOID)
}

// Reconciliation advances only deterministic delivery state. Existing agent
// admission, workspace claims and review lanes still own all model work.
func (s *Engine) reconcilePlans(ctx context.Context, items []github.WorkItem) (bool, error) {
	if !s.cfg.GitHubProject.PlanDelivery {
		return false, nil
	}
	changed := false
	for _, parent := range items {
		if parent.PlanRelease == "" || (parent.Phase != github.PlanDeliveryPhase && parent.Phase != github.PlanIntegratingPhase && parent.Phase != github.PlanRepairingPhase) {
			continue
		}
		unlock := s.lockPlan(parent.ID)
		progress, err := s.reconcilePlan(ctx, parent)
		unlock()
		if err != nil {
			return changed, err
		}
		changed = changed || progress
	}
	return changed, nil
}

func (s *Engine) reconcilePlan(ctx context.Context, parent github.WorkItem) (bool, error) {
	delivery, present, err := s.source.DeliveryForItem(ctx, parent)
	if err != nil || !present {
		return false, errors.Join(errors.New("delivery plan authority is not current"), err)
	}
	parent = delivery.Parent
	if parent.Phase == github.PlanRepairingPhase {
		return true, s.resumePlanRepair(ctx, delivery)
	}
	root, err := s.repositoryDir(ctx, parent.Repository)
	if err != nil {
		return false, err
	}
	if parent.Phase == github.PlanIntegratingPhase {
		id := github.PlanIntegrationMember(parent)
		var child github.WorkItem
		for _, member := range delivery.Children {
			if member.ID == id {
				child = member
			}
		}
		if child.ID == "" {
			return false, errors.New("pending integration names an unauthorized member")
		}
		action, err := s.source.Authorize(ctx, child)
		if err != nil {
			return false, err
		}
		content, err := action.DelegatedContent()
		if err != nil {
			return false, err
		}
		metadata, err := s.workspaceForItem(ctx, child, content.Digest, root)
		if err != nil {
			return false, err
		}
		snapshot, err := s.checkoutSnapshotState(ctx, metadata.WorktreePath)
		if err != nil {
			return false, err
		}
		record, found, err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).LoadPublicationAcceptance(ctx, metadata, snapshot, workspace.PublicationEvidence{PlanRevision: delivery.Revision})
		if err != nil || !found || record.CommitOID != parent.QACommit {
			return false, errors.Join(errors.New("pending integration has no exact retained QA acceptance"), err)
		}
		return true, s.integratePlanAcceptanceLocked(ctx, action, metadata, record)
	}
	action, err := s.source.Authorize(ctx, parent)
	if err != nil {
		return false, err
	}
	changed := false
	if parent.QACommit == "" {
		if err := s.fetchBase(ctx, root); err != nil {
			return false, err
		}
		result, err := s.git(ctx, []string{"rev-parse", "--verify", s.remoteName() + "/" + s.baseBranch()}, root, 30*time.Second)
		if err != nil {
			return false, err
		}
		head := strings.TrimSpace(result.Stdout)
		if err := s.source.RecordPlanHead(ctx, action, head); err != nil {
			return false, err
		}
		action, err = s.source.Authorize(ctx, github.WorkItem{ID: parent.ID})
		if err != nil {
			return false, err
		}
		parent = action.Item
		changed = true
	}
	if err := workspace.NewGitProviderWithLimits(s.run, s.snapshotLimits()).EnsurePlanBranch(ctx, root, parent.Repository, s.remoteName(), parent.Branch, parent.QACommit, func() error {
		_, err := s.source.RefreshAction(ctx, action)
		return err
	}); err != nil {
		return changed, err
	}
	if s.source.PlanMembersIntegrated(delivery) {
		return true, s.source.Transition(ctx, action, s.cfg.GitHubProject.QAStatus, "Every approved member is integrated. Review the combined outcome against the original plan.", "")
	}
	return changed, nil
}
