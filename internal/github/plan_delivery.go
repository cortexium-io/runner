package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

const (
	PlanDeliveryPhase    = "plan_delivery"
	PlanIntegratedPhase  = "plan_integrated"
	PlanIntegratingPhase = "plan_integrating"
	PlanRepairingPhase   = "plan_repairing"
	PlanCancelledPhase   = "plan_cancelled"
	PlanAmendingPhase    = "plan_amending"
	PlanRetiredPhase     = "plan_retired"
	PlanProposalPhase    = "plan_proposal"
	planManifestPrefix   = "# Runner outcome delivery plan\n\nThe following contract is proposed until the exact batch is approved. Its shared scope applies to every member; mutable execution history is not part of this contract.\n\n```json\n"
)

// BeginPlanRepair spends the whole-plan allowance and pins the private
// rejected-candidate record before any owner is requeued. Recovery resumes
// this exact intent, rather than granting another allowance.
func (s *Project) BeginPlanRepair(ctx context.Context, action AuthorizedAction, digest, candidate string, failures int) error {
	if !validGitObjectID(candidate) || len(digest) != 67 || failures != action.Item.QAFailures+1 {
		return errors.New("invalid whole-plan repair intent")
	}
	// QACommit is the integrated remote plan head, not the rejected local
	// candidate after a destination refresh. The digest pins both in the
	// protected rejection record; spending repair authority must not adopt a
	// local-only commit as remote integration state.
	return s.transitionWithDeliveryCheck(ctx, action, s.backlogStatus(), "Plan repair "+digest, PlanRepairingPhase, false, func(next *WorkItem) { next.QAFailures = failures }, []projectFieldUpdate{numberProjectField(s.qaFailuresFieldName(), failures)}, func(delivery PlanDelivery) error {
		if delivery.Parent.ID == "" || delivery.Parent.ID != action.Item.ID {
			return errors.New("invalid whole-plan repair intent")
		}
		return nil
	})
}

func (s *Project) FinishPlanRepair(ctx context.Context, action AuthorizedAction, digest string) error {
	if action.Item.Phase != PlanRepairingPhase || action.Item.Result != "Plan repair "+digest {
		return errors.New("whole-plan repair intent changed")
	}
	return s.transition(ctx, action, s.backlogStatus(), "Approved owning cards are repairing the combined candidate.", PlanDeliveryPhase, false, nil, nil)
}

// PlanManifest is the immutable, human-visible contract on the planning parent.
// Members are exact Project IDs, never title matches. Execution state is kept
// in the parent's ordinary signed lifecycle fields, not in this manifest.
type PlanManifest struct {
	Version              int          `json:"version"`
	Amendment            int          `json:"amendment,omitempty"`
	Request              string       `json:"request"`
	Outcome              string       `json:"outcome"`
	SuccessCriteria      []string     `json:"success_criteria"`
	Scope                []string     `json:"scope"`
	Decisions            []string     `json:"decisions"`
	Repository           string       `json:"repository"`
	DestinationBranch    string       `json:"destination_branch"`
	CompleteVerification string       `json:"complete_verification"`
	VerificationDigest   string       `json:"verification_digest"`
	Members              []PlanMember `json:"members"`
}

type PlanMember struct {
	ID                    string   `json:"id"`
	Dependencies          []string `json:"dependencies"`
	ImplementationProfile string   `json:"implementation_profile"`
	ProfileDigest         string   `json:"profile_digest"`
	ProfileReason         string   `json:"profile_reason"`
	Retired               bool     `json:"retired,omitempty"`
	RetirementReason      string   `json:"retirement_reason,omitempty"`
}

func (m PlanManifest) ActiveMembers() []PlanMember {
	var members []PlanMember
	for _, member := range m.Members {
		if !member.Retired {
			members = append(members, member)
		}
	}
	return members
}

// PlanDelivery is returned only after release and current lifecycle authority
// have both been verified. Reading a manifest alone never authorizes work.
type PlanDelivery struct {
	Parent          WorkItem
	Manifest        PlanManifest
	Revision        string
	Children        []WorkItem
	RetiredChildren []WorkItem
}

// AllChildren is the exact release-bound union. Retired rows remain authority
// inputs even though execution, dependencies and completion use active members.
func (d PlanDelivery) AllChildren() []WorkItem {
	return append(append([]WorkItem(nil), d.Children...), d.RetiredChildren...)
}

func FormatPlanManifest(manifest PlanManifest) (string, error) {
	if err := validatePlanManifest(manifest); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	body := planManifestPrefix + string(encoded) + "\n```"
	if len(body) > 60_000 {
		return "", errors.New("plan manifest exceeds the 60000-byte Project issue contract")
	}
	return body, nil
}

func ParsePlanManifest(body string) (PlanManifest, bool, error) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, planManifestPrefix) {
		if strings.Contains(body, "# Runner outcome delivery plan") {
			return PlanManifest{}, true, errors.New("plan manifest is malformed")
		}
		return PlanManifest{}, false, nil
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(body, planManifestPrefix), "\n```")
	var manifest PlanManifest
	if err := json.Unmarshal([]byte(encoded), &manifest); err != nil {
		return manifest, true, errors.New("plan manifest JSON is malformed")
	}
	canonical, err := FormatPlanManifest(manifest)
	if err != nil {
		return manifest, true, err
	}
	if canonical != body {
		return manifest, true, errors.New("plan manifest is not canonical")
	}
	return manifest, true, nil
}

func validatePlanManifest(manifest PlanManifest) error {
	if manifest.Version != 1 || manifest.Amendment < 0 || strings.TrimSpace(manifest.Request) == "" || strings.TrimSpace(manifest.Outcome) == "" ||
		len(manifest.SuccessCriteria) == 0 || !config.ValidRepositoryName(manifest.Repository) ||
		!validPlanBranch(manifest.DestinationBranch) || strings.TrimSpace(manifest.CompleteVerification) == "" || !validPlanDigest(manifest.VerificationDigest) ||
		len(manifest.Members) == 0 || len(manifest.Members) > MaxPlanningBatchChildren {
		return errors.New("plan manifest requires a request, outcome, criteria, repository, destination, complete gate and exact members")
	}
	seen := map[string]bool{}
	retired := map[string]bool{}
	for _, member := range manifest.Members {
		if member.ID == "" || member.ID != strings.TrimSpace(member.ID) || seen[member.ID] ||
			strings.TrimSpace(member.ImplementationProfile) == "" || !validPlanDigest(member.ProfileDigest) || strings.TrimSpace(member.ProfileReason) == "" {
			return errors.New("plan manifest member identity, content or resolved profile/reason is invalid")
		}
		seen[member.ID] = true
		if member.Retired != (strings.TrimSpace(member.RetirementReason) != "") || len(member.RetirementReason) > 4000 || !utf8.ValidString(member.RetirementReason) || strings.ContainsRune(member.RetirementReason, 0) {
			return errors.New("retired plan members require a bounded explicit retirement reason")
		}
		retired[member.ID] = member.Retired
		if !reflect.DeepEqual(member.Dependencies, canonicalDelegatedDependencies(member.Dependencies)) {
			return errors.New("plan manifest dependencies must be canonical immutable IDs")
		}
	}
	if len(manifest.ActiveMembers()) == 0 {
		return errors.New("a delivery plan needs an active member; cancel an entirely retired plan instead")
	}
	for _, member := range manifest.ActiveMembers() {
		for _, dependency := range member.Dependencies {
			if retired[dependency] {
				return errors.New("active members cannot depend on retired work; explicitly amend the dependent contract")
			}
		}
	}
	return nil
}

func validPlanDigest(value string) bool {
	if len(value) != 67 || !strings.HasPrefix(value, "v1:") {
		return false
	}
	for _, digit := range value[3:] {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

func validPlanBranch(branch string) bool {
	return branch != "" && branch == strings.TrimSpace(branch) && !strings.HasPrefix(branch, "-") &&
		!strings.ContainsAny(branch, " \t\r\n\x00~^:?*[\\") && !strings.Contains(branch, "..") && !strings.HasSuffix(branch, ".lock")
}

func PlanRevision(body string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(body)))
	return fmt.Sprintf("v1:%x", digest[:])
}

func PlanBranch(parentID string) string {
	// Preserve the full identity through a digest, avoiding case-folding and
	// punctuation collisions in Project node IDs.
	digest := sha256.Sum256([]byte(strings.TrimSpace(parentID)))
	return fmt.Sprintf("runner/plan-%x", digest[:16])
}

func (s *Project) validatePlanMembers(parent WorkItem, children []WorkItem) (PlanManifest, error) {
	manifest, err := s.validatePlanMemberContract(parent, children)
	if err != nil {
		return manifest, err
	}
	if !s.cfg.PlanDelivery || manifest.CompleteVerification != s.cfg.PlanVerificationID || manifest.VerificationDigest != s.cfg.PlanVerificationDigest {
		return manifest, errors.New("plan delivery is disabled or its approved complete-verification settings changed")
	}
	for _, member := range manifest.ActiveMembers() {
		if member.ProfileDigest != s.cfg.PlanProfileDigests[member.ImplementationProfile] {
			return manifest, errors.New("plan member content, dependency or resolved profile changed after approval")
		}
	}
	return manifest, nil
}

// validatePlanMemberContract checks the immutable contract, not permission to
// execute it under today's model and verification policy.
func (s *Project) validatePlanMemberContract(parent WorkItem, children []WorkItem) (PlanManifest, error) {
	manifest, present, err := ParsePlanManifest(parent.Body)
	if err != nil {
		return manifest, err
	}
	if !present {
		return manifest, errors.New("plan parent has no delivery manifest")
	}
	if manifest.Repository != s.cfg.IntakeRepository || manifest.Repository != parent.Repository || manifest.DestinationBranch != s.cfg.BaseBranch {
		return manifest, errors.New("plan repository or destination changed from the configured delivery target")
	}
	if len(children) != len(manifest.Members) {
		return manifest, errors.New("plan membership is incomplete or has unauthorized additions")
	}
	ordered := append([]WorkItem(nil), children...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PlanningItemIndex < ordered[j].PlanningItemIndex })
	for i, child := range ordered {
		member := manifest.Members[i]
		if child.ID != member.ID || child.PlanningItemIndex != i+1 || child.PlanningSourceID != parent.ID || child.Repository != manifest.Repository ||
			child.ImplementationProfile != member.ImplementationProfile ||
			!reflect.DeepEqual(canonicalDelegatedDependencies(child.Dependencies), member.Dependencies) {
			return manifest, errors.New("plan member content, dependency or resolved profile changed after approval")
		}
	}
	return manifest, nil
}

func (s *Project) ValidatePlanDelivery(parent WorkItem, all []WorkItem) (PlanDelivery, error) {
	return s.validatePlanDeliveryState(parent, all, false)
}

// Cancellation inspection may read an intact cancelled contract, but no
// execution/admission caller may treat it as available delivery authority.
func (s *Project) validatePlanDeliveryState(parent WorkItem, all []WorkItem, allowCancelled bool) (PlanDelivery, error) {
	if parent.Phase == PlanAmendingPhase {
		return PlanDelivery{}, errors.New("plan amendment is fenced; finish the protected operator amendment before admission")
	}
	if !s.cfg.PlanDelivery {
		return PlanDelivery{}, errors.New("plan delivery is not enabled")
	}
	index := newWorkItemIndex(all)
	children := index.childrenBySource[parent.ID]
	manifest, err := s.validatePlanMembers(parent, children)
	if err != nil {
		return PlanDelivery{}, err
	}
	if strings.TrimSpace(parent.Transition) != "" {
		return PlanDelivery{}, errors.New("plan parent is transition-locked")
	}
	if _, err := s.validatePlanningBatch(parent.PlanRelease, parent, children, batchReleasedState); err != nil {
		return PlanDelivery{}, err
	}
	if _, err := s.validateAction(parent); err != nil {
		return PlanDelivery{}, fmt.Errorf("plan delivery lifecycle authority: %w", err)
	}
	if parent.Phase == PlanCancelledPhase && !allowCancelled {
		return PlanDelivery{}, errors.New("plan delivery was cancelled")
	}
	if parent.Branch != PlanBranch(parent.ID) {
		return PlanDelivery{}, errors.New("plan branch identity changed")
	}
	delivery := PlanDelivery{Parent: parent, Manifest: manifest, Revision: PlanRevision(parent.Body)}
	byID := newWorkItemIndex(children).byID
	for _, member := range manifest.Members {
		child := byID[member.ID]
		if _, err := s.validateAction(child); err != nil {
			return PlanDelivery{}, fmt.Errorf("plan member lifecycle authority: %w", err)
		}
		if member.Retired {
			if child.Status != s.backlogStatus() || child.Phase != PlanRetiredPhase || child.Transition != "" || child.PullRequest != "" {
				return PlanDelivery{}, errors.New("retired member lifecycle changed; retirement cannot authorize execution or success")
			}
			delivery.RetiredChildren = append(delivery.RetiredChildren, child)
		} else {
			if child.Phase == PlanRetiredPhase {
				return PlanDelivery{}, errors.New("active member has a retired lifecycle")
			}
			delivery.Children = append(delivery.Children, child)
		}
	}
	return delivery, nil
}

// DeliveryForItem revalidates the complete parent/member authority immediately
// before using shared plan context or mutating a member's repository. It does
// not turn ordinary or historical planning-complete cards into delivery plans.
func (s *Project) DeliveryForItem(ctx context.Context, item WorkItem) (result PlanDelivery, present bool, err error) {
	finish := metrics.StartStage(ctx, metrics.StageAuthorityValidation)
	defer func() { finish.FinishError(err) }()
	if item.PlanningSourceID == "" && item.PlanRelease == "" {
		if _, present, err := ParsePlanManifest(item.Body); present {
			return PlanDelivery{}, true, errors.Join(errors.New("plan has not been released"), err)
		}
		return PlanDelivery{}, false, nil
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return PlanDelivery{}, false, err
	}
	parentID := item.PlanningSourceID
	if item.PlanRelease != "" {
		parentID = item.ID
	}
	parent, err := selectProjectItem(items, parentID)
	if err != nil {
		return PlanDelivery{}, true, err
	}
	if parent.PlanRelease == "" {
		if _, present, err := ParsePlanManifest(parent.Body); present {
			return PlanDelivery{}, true, errors.Join(errors.New("plan has no immutable release authority"), err)
		}
		return PlanDelivery{}, false, nil
	}
	delivery, err := s.ValidatePlanDelivery(parent, items)
	if err == nil && item.ID != parent.ID {
		for _, retired := range delivery.RetiredChildren {
			if retired.ID == item.ID {
				return PlanDelivery{}, true, errors.New("retired plan member has no execution authority")
			}
		}
	}
	return delivery, true, err
}

func (s *Project) refreshDeliveryAuthority(ctx context.Context, item WorkItem) error {
	if item.PlanRelease == "" && item.PlanningSourceID == "" {
		return nil
	}
	_, _, err := s.DeliveryForItem(ctx, item)
	return err
}

// EnsureDeliveryPlanningParent gives CLI planning the same durable parent as
// Project-originated planning. The parent is never placed in an agent lane:
// staging a completed CLI proposal must not admit a second planner invocation.
// An ambiguous unsigned creation is reported, not adopted or duplicated.
func (s *Project) EnsureDeliveryPlanningParent(ctx context.Context, title, request, proposal string) (WorkItem, bool, error) {
	if !s.cfg.PlanDelivery || strings.TrimSpace(request) == "" || strings.TrimSpace(proposal) == "" {
		return WorkItem{}, false, errors.New("delivery proposal requires enabled rollout, original request and exact proposal identity")
	}
	if _, err := s.loadSchema(ctx); err != nil {
		return WorkItem{}, false, err
	}
	if field, ok := s.currentSchema().field(config.RunnerPlanReleaseFieldName); !ok || !projectFieldHasDataType(field, "TEXT") {
		return WorkItem{}, false, errors.New("plan delivery requires an explicit Project migration adding Runner Plan Release")
	}
	title = "Delivery plan: " + strings.TrimSpace(title)
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return WorkItem{}, false, err
	}
	var matches []WorkItem
	for _, item := range items {
		if item.Title != title || item.PlanningSourceID != "" {
			continue
		}
		body := item.Body
		if manifest, present, parseErr := ParsePlanManifest(body); present {
			if parseErr != nil {
				return WorkItem{}, false, parseErr
			}
			body = manifest.Request
		}
		if body == request {
			matches = append(matches, item)
		}
	}
	if len(matches) > 1 {
		return WorkItem{}, false, errors.New("multiple matching delivery proposals require explicit operator reconciliation")
	}
	if len(matches) == 1 {
		item := matches[0]
		if item.PlanRelease != "" {
			return WorkItem{}, false, errors.New("this delivery proposal is already released; inspect its existing parent instead of staging another plan")
		}
		if _, present, err := ParsePlanManifest(item.Body); present {
			if err != nil {
				return WorkItem{}, false, err
			}
			children := newWorkItemIndex(items).childrenBySource[item.ID]
			if _, err := s.validatePlanningBatch(item.Approval, item, children, batchStagedState); err != nil {
				return WorkItem{}, false, err
			}
			return item, true, nil
		}
		if item.Phase != PlanProposalPhase || item.Result != "Delivery proposal "+proposal || item.Status != s.backlogStatus() {
			return WorkItem{}, false, errors.New("an existing delivery proposal is incomplete or changed; it was not adopted or duplicated")
		}
		if _, err := s.validateAction(item); err != nil {
			return WorkItem{}, false, err
		}
		return item, false, nil
	}
	item, err := s.createDraftItem(ctx, title, request)
	if err != nil {
		return WorkItem{}, false, err
	}
	item, err = s.InspectRecoveryItem(ctx, item.ID)
	if err != nil {
		return WorkItem{}, false, err
	}
	issueBacked, err := s.ensureIssueBacked(ctx, []WorkItem{item})
	if err != nil {
		return item, false, err
	}
	item = issueBacked[0]
	item.Repository, item.Status, item.Phase = s.cfg.IntakeRepository, s.backlogStatus(), PlanProposalPhase
	item.Result = "Delivery proposal " + proposal
	state, err := s.stateForStatus(item.Status)
	if err != nil {
		return item, false, err
	}
	action, err := s.signAction(item, s.cfg.InitialRole, state)
	if err != nil {
		return item, false, err
	}
	if err := s.applyFieldUpdates(ctx, item.ID,
		textProjectField(s.phaseFieldName(), item.Phase), textProjectField(s.resultFieldName(), item.Result),
		textProjectField(s.approvalFieldName(), action.assertion), statusProjectField(s.statusFieldName(), item.Status)); err != nil {
		return item, false, fmt.Errorf("durable proposal %s was created but not fully authenticated; inspect it before retrying: %w", item.ID, err)
	}
	current, err := s.InspectRecoveryItem(ctx, item.ID)
	if err != nil {
		return item, false, err
	}
	if _, err := s.validateAction(current); err != nil {
		return item, false, err
	}
	return current, false, nil
}

func (s *Project) integratedPlanMember(item WorkItem) bool {
	if item.Phase != PlanIntegratedPhase || item.Status != s.backlogStatus() || !validGitObjectID(item.QACommit) || item.PullRequest != "" || item.Branch == "" || item.Transition != "" {
		return false
	}
	_, err := s.validateAction(item)
	return err == nil
}

func (s *Project) TransitionPlanIntegrated(ctx context.Context, action AuthorizedAction, candidate string) error {
	if !validGitObjectID(candidate) || action.Item.PlanningSourceID == "" {
		return errors.New("plan integration requires an exact candidate and member identity")
	}
	return s.transitionWithDeliveryCheck(ctx, action, s.backlogStatus(), "Accepted and integrated into the plan branch; not delivered to the destination.", PlanIntegratedPhase, false,
		func(next *WorkItem) {
			next.QACommit = candidate
			next.Activity = "Integrated — awaiting plan delivery"
		},
		[]projectFieldUpdate{textProjectField(s.qaCommitFieldName(), candidate)}, func(delivery PlanDelivery) error {
			if delivery.Parent.ID == "" {
				return errors.New("plan integration authority unavailable")
			}
			return nil
		})
}

func (s *Project) RecordPlanHead(ctx context.Context, action AuthorizedAction, head string) error {
	if action.Item.PlanRelease == "" || (action.Item.Phase != PlanDeliveryPhase && action.Item.Phase != PlanIntegratingPhase) || !validGitObjectID(head) {
		return errors.New("plan head requires an active delivery parent and exact commit")
	}
	return s.transition(ctx, action, s.backlogStatus(), "Delivering approved plan; integrated work is not yet delivered.", PlanDeliveryPhase, false,
		func(next *WorkItem) { next.QACommit = head; next.Activity = "Delivering plan" }, []projectFieldUpdate{textProjectField(s.qaCommitFieldName(), head)})
}

const planIntegrationIntent = "Integrating accepted plan member "

func (s *Project) BeginPlanIntegration(ctx context.Context, parent AuthorizedAction, childID, candidate, base string) error {
	if parent.Item.Phase != PlanDeliveryPhase || parent.Item.QACommit != base || !validGitObjectID(candidate) {
		return errors.New("plan head changed before integration")
	}
	return s.transitionWithDeliveryCheck(ctx, parent, s.backlogStatus(), planIntegrationIntent+childID, PlanIntegratingPhase, false,
		func(next *WorkItem) { next.QACommit = candidate; next.Activity = "Integrating accepted member" }, []projectFieldUpdate{textProjectField(s.qaCommitFieldName(), candidate)}, func(delivery PlanDelivery) error {
			if delivery.Parent.ID == "" || delivery.Parent.ID != parent.Item.ID {
				return errors.New("plan integration requires current release authority")
			}
			for _, child := range delivery.Children {
				if child.ID == childID {
					return nil
				}
			}
			return errors.New("integration target is not an approved plan member")
		})
}

func PlanIntegrationMember(parent WorkItem) string {
	if parent.Phase != PlanIntegratingPhase {
		return ""
	}
	return strings.TrimPrefix(parent.Result, planIntegrationIntent)
}

func (s *Project) PlanMembersIntegrated(delivery PlanDelivery) bool {
	for _, child := range delivery.Children {
		if !s.integratedPlanMember(child) {
			return false
		}
	}
	return len(delivery.Children) > 0
}

// StageDeliveryPlanningApproval persists planner output on its existing parent
// before complete-batch approval. It grants no child execution authority.
func (s *Project) StageDeliveryPlanningApproval(ctx context.Context, expected AuthorizedAction, children []WorkItem, manifest PlanManifest, detail string) error {
	if !s.cfg.PlanDelivery {
		return errors.New("plan delivery is not enabled")
	}
	if err := validatePlanningDeliveryReviewBoundary(manifest, children); err != nil {
		return err
	}
	if _, err := s.loadSchema(ctx); err != nil {
		return err
	}
	if field, ok := s.currentSchema().field(config.RunnerPlanReleaseFieldName); !ok || !projectFieldHasDataType(field, "TEXT") {
		return errors.New("plan delivery requires an explicit Project migration adding Runner Plan Release")
	}
	body, err := FormatPlanManifest(manifest)
	if err != nil {
		return err
	}
	source, err := expected.authorizedItem()
	if err != nil {
		return err
	}
	if source.Body != manifest.Request {
		return errors.New("delivery manifest request differs from the authorized planning request")
	}
	current, err := s.InspectRecoveryItem(ctx, source.ID)
	if err != nil {
		return err
	}
	next := source
	next.Body = body
	if _, err := s.validatePlanMembers(next, children); err != nil {
		return err
	}
	assertion, err := s.signPlanningBatch(next, children, batchStagedState, "")
	if err != nil {
		return err
	}
	if current.Body == body {
		if _, err := s.validatePlanningBatch(current.Approval, current, children, batchStagedState); err != nil {
			return err
		}
		return s.StagePlanningApproval(ctx, expected, children, detail)
	}
	if current.Body != source.Body {
		return errors.New("planning request changed before delivery staging")
	}
	if action, err := s.validateAction(current); err != nil || !sameAuthorizedAction(action, expected) {
		// Recover a lost response between recording staging intent and writing
		// its body, but only when the retained exact planner result proves it.
		prospective := current
		prospective.Body = body
		if _, err := s.validatePlanningBatch(current.Approval, prospective, children, batchStagedState); err != nil {
			return errors.New("planning source has neither current action nor exact delivery-staging authority")
		}
		assertion = current.Approval
	}
	actual, err := s.LifecycleItemsByID(ctx, planMemberIDs(manifest))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, children) {
		return errors.New("planning members changed before delivery staging")
	}
	if current.Approval != assertion {
		if err := s.setApproval(ctx, source.ID, assertion); err != nil {
			return err
		}
	}
	if err := s.writePlanBody(ctx, current, body); err != nil {
		return err
	}
	return s.StagePlanningApproval(ctx, expected, children, detail)
}

func (s *Project) writePlanBody(ctx context.Context, item WorkItem, body string) error {
	var args []string
	if item.DraftContentID != "" && item.URL == "" {
		args = []string{"project", "item-edit", "--id", item.DraftContentID, "--body", body}
	} else {
		if !s.isIntakeIssueURL(item.URL) {
			return errors.New("plan parent must be an issue in the configured repository or a Project draft")
		}
		args = []string{"issue", "edit", item.URL, "--body", body}
	}
	result, err := s.gh(ctx, args...)
	if err != nil {
		return fmt.Errorf("persist delivery manifest: %w", commandFailure(err, result))
	}
	return nil
}

func planMemberIDs(manifest PlanManifest) []string {
	ids := make([]string, len(manifest.Members))
	for i, member := range manifest.Members {
		ids[i] = member.ID
	}
	return ids
}

func (s *Project) completeDeliveryRelease(ctx context.Context, source WorkItem, detail, releaseAssertion string) (WorkItem, error) {
	next := source
	next.Status = s.backlogStatus()
	next.Phase = PlanDeliveryPhase
	next.Activity = "Delivering plan"
	next.Result = canonicalProjectResult(detail)
	next.PlanRelease = releaseAssertion
	next.Branch = PlanBranch(source.ID)
	state, err := s.stateForStatus(next.Status)
	if err != nil {
		return WorkItem{}, err
	}
	action, err := s.signAction(next, s.cfg.InitialRole, state)
	if err != nil {
		return WorkItem{}, err
	}
	if err := s.beginTransition(ctx, source.ID); err != nil {
		return WorkItem{}, err
	}
	if err := s.applyFieldUpdates(ctx, source.ID,
		textProjectField(config.RunnerPlanReleaseFieldName, releaseAssertion),
		textProjectField(s.branchFieldName(), next.Branch),
		textProjectField(s.phaseFieldName(), next.Phase),
		textProjectField(s.activityFieldName(), next.Activity),
		textProjectField(s.resultFieldName(), next.Result),
		textProjectField(s.approvalFieldName(), action.assertion),
		statusProjectField(s.statusFieldName(), next.Status)); err != nil {
		return WorkItem{}, err
	}
	if err := s.finishTransition(ctx, source.ID); err != nil {
		return WorkItem{}, err
	}
	next.Approval = action.assertion
	return next, nil
}
