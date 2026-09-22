package github

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/cortexium-io/runner/internal/config"
)

// PlanAmendmentRequest carries the complete desired membership, not a request
// to invent cards. This initial operator boundary supports the same exact
// members; additions/removals are refused explicitly, never silently ignored.
type PlanAmendmentRequest struct {
	ExpectedRevision      string            `json:"expected_revision"`
	Reason                string            `json:"reason"`
	Manifest              PlanManifest      `json:"manifest"`
	MemberBodies          map[string]string `json:"member_bodies,omitempty"`
	AdditionalAffectedIDs []string          `json:"additional_affected_ids,omitempty"`
}

// PlanAmendmentState is private protected recovery data. Do not print it:
// before/after snapshots include signed authority, not just preview prose.
type PlanAmendmentState struct {
	Request  PlanAmendmentRequest
	Before   []WorkItem // parent first, then exact manifest order
	After    []WorkItem
	Affected []string
}

func (p PlanAmendmentState) Digest() string {
	b, _ := json.Marshal(p)
	return fmt.Sprintf("v1:%x", sha256.Sum256(b))
}

func (s *Project) PlanDeliveryAmendment(ctx context.Context, selector string, request PlanAmendmentRequest) (PlanAmendmentState, error) {
	cancellation, err := s.PlanCancellation(ctx, selector)
	if err != nil {
		return PlanAmendmentState{}, err
	}
	if cancellation.AlreadyCancelled {
		return PlanAmendmentState{}, errors.New("cancelled plans cannot be reopened by amendment; use a separate corrective plan")
	}
	d := cancellation.delivery
	if d.Parent.Phase == PlanIntegratingPhase || d.Parent.Phase == PlanRepairingPhase {
		return PlanAmendmentState{}, errors.New("finish the existing integration/repair intent before amending; an amendment cannot discard an unfinished allowance")
	}
	if d.Parent.Phase != PlanDeliveryPhase && d.Parent.Status != s.qaStatus() && d.Parent.Status != s.blockedStatus() {
		return PlanAmendmentState{}, errors.New("finish integration/repair recovery before amending this unpublished plan")
	}
	return s.buildPlanAmendment(d, request)
}

func (s *Project) buildPlanAmendment(d PlanDelivery, request PlanAmendmentRequest) (PlanAmendmentState, error) {
	if request.ExpectedRevision != d.Revision || strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 4000 || !utf8.ValidString(request.Reason) || strings.ContainsRune(request.Reason, 0) {
		return PlanAmendmentState{}, errors.New("amendment needs the exact current revision and a bounded reason")
	}
	old, next := d.Manifest, request.Manifest
	if next.Amendment != old.Amendment+1 || next.Request != old.Request || next.Repository != old.Repository || next.DestinationBranch != old.DestinationBranch {
		return PlanAmendmentState{}, errors.New("amendment must advance its ordinal by one and preserve the original request, repository and destination; retargeting requires a separate corrective plan")
	}
	if len(next.Members) != len(old.Members) {
		return PlanAmendmentState{}, errors.New("membership additions/removals are not supported by this amendment slice; no cards or integrated code were changed")
	}
	for i := range old.Members {
		if old.Members[i].ID != next.Members[i].ID {
			return PlanAmendmentState{}, errors.New("membership additions, removals or reordering require a separate explicit membership amendment; this command preserves the exact member set")
		}
	}
	body, err := FormatPlanManifest(next)
	if err != nil {
		return PlanAmendmentState{}, err
	}
	children := map[string]WorkItem{}
	for _, child := range d.Children {
		children[child.ID] = child
	}
	state := PlanAmendmentState{Request: request, Before: []WorkItem{d.Parent}, After: []WorkItem{d.Parent}}
	state.After[0].Body = body
	seeds := map[string]bool{}
	globalOld, globalNext := old, next
	globalOld.Members, globalNext.Members = nil, nil
	globalOld.Amendment, globalNext.Amendment = 0, 0
	sharedChanged := !reflect.DeepEqual(globalOld, globalNext)
	changed := sharedChanged
	for i, member := range next.Members {
		before := children[member.ID]
		after := before
		if replacement, ok := request.MemberBodies[member.ID]; ok {
			if len(replacement) > 60000 || strings.TrimSpace(replacement) == "" || !utf8.ValidString(replacement) || strings.ContainsRune(replacement, 0) {
				return PlanAmendmentState{}, errors.New("member body must be valid bounded UTF-8")
			}
			previousMetadata, _, err := decodePlannedItemMetadata(before.Body)
			if err != nil {
				return PlanAmendmentState{}, err
			}
			metadata, present, err := decodePlannedItemMetadata(replacement)
			if err != nil || !present {
				return PlanAmendmentState{}, errors.New("amended member must retain canonical authenticated planning metadata")
			}
			comparable := metadata
			comparable.Dependencies, comparable.ImplementationProfile = previousMetadata.Dependencies, previousMetadata.ImplementationProfile
			if !reflect.DeepEqual(comparable, previousMetadata) {
				return PlanAmendmentState{}, errors.New("amendment cannot change member provenance or repository")
			}
			after.Body, after.Dependencies, after.ImplementationProfile = replacement, metadata.Dependencies, metadata.ImplementationProfile
		}
		localChanged := before.Body != after.Body || !reflect.DeepEqual(old.Members[i], member)
		changed = changed || localChanged
		seeds[member.ID] = sharedChanged || localChanged
		state.Before, state.After = append(state.Before, before), append(state.After, after)
	}
	for id := range request.MemberBodies {
		if _, ok := children[id]; !ok {
			return PlanAmendmentState{}, fmt.Errorf("body update for nonmember %s is not authorized", id)
		}
	}
	for _, id := range request.AdditionalAffectedIDs {
		if _, ok := children[id]; !ok {
			return PlanAmendmentState{}, fmt.Errorf("additional affected item %s is not a member", id)
		}
		seeds[id], changed = true, true
	}
	if !changed {
		return PlanAmendmentState{}, errors.New("amendment has no contract change or explicit additional invalidation")
	}
	state.Affected = amendmentDependencyClosure(seeds, old.Members, next.Members)
	if _, err := s.validatePlanMembers(state.After[0], state.After[1:]); err != nil {
		return PlanAmendmentState{}, err
	}
	if err := ValidatePlanningDependencies(state.After[1:]); err != nil {
		return PlanAmendmentState{}, err
	}
	for i := range state.After {
		after := &state.After[i]
		if i == 0 {
			after.Status, after.Phase, after.Activity = s.backlogStatus(), PlanDeliveryPhase, "Amended — delivery review required"
		} else if slices.Contains(state.Affected, after.ID) {
			after.Status, after.Phase, after.QACommit, after.Activity = s.readyStatus(), s.laneIDForStatus(s.readyStatus()), "", "Amended — affected work requires acceptance"
		}
		// Results, rejection counters and candidate branches are history, not
		// allowances that an amendment may reset. Parent QACommit remains the
		// authenticated integrated remote head even though its acceptance expires.
	}
	batch, err := s.validatePlanningBatch(d.Parent.PlanRelease, d.Parent, d.Children, batchReleasedState)
	if err != nil {
		return PlanAmendmentState{}, err
	}
	state.After[0].PlanRelease, err = s.signPlanningBatch(state.After[0], state.After[1:], batchReleasedState, batch.Generation)
	if err != nil {
		return PlanAmendmentState{}, err
	}
	for i := range state.After {
		current, err := s.validateAction(state.Before[i])
		if err != nil {
			return PlanAmendmentState{}, err
		}
		after := state.After[i]
		lane, err := s.stateForStatus(after.Status)
		if err != nil {
			return PlanAmendmentState{}, err
		}
		role, err := s.roleForNextState(current, after, lane)
		if err != nil {
			return PlanAmendmentState{}, err
		}
		signed, err := s.signAction(after, role, lane)
		if err != nil {
			return PlanAmendmentState{}, err
		}
		state.After[i] = signed.Item
	}
	return state, nil
}

func amendmentDependencyClosure(seeds map[string]bool, graphs ...[]PlanMember) []string {
	for changed := true; changed; {
		changed = false
		for _, members := range graphs {
			for _, member := range members {
				if seeds[member.ID] {
					continue
				}
				for _, dep := range member.Dependencies {
					if seeds[dep] {
						seeds[member.ID], changed = true, true
						break
					}
				}
			}
		}
	}
	var result []string
	for id, affected := range seeds {
		if affected {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

// ValidatePlanAmendmentState authenticates the saved source and recomputes every
// permitted destination. A private intent is not permission to widen a patch.
func (s *Project) ValidatePlanAmendmentState(state PlanAmendmentState) error {
	if len(state.Before) < 2 || len(state.After) != len(state.Before) {
		return errors.New("invalid protected amendment member snapshots")
	}
	d, err := s.ValidatePlanDelivery(state.Before[0], state.Before)
	if err != nil {
		return err
	}
	rebuilt, err := s.buildPlanAmendment(d, state.Request)
	if err != nil || !reflect.DeepEqual(rebuilt, state) {
		return errors.Join(errors.New("protected amendment no longer matches exact authority/settings"), err)
	}
	return nil
}

// FencePlanAmendment runs only under offline operator locks after the complete
// intent has been durably saved. No child/contract mutation precedes this fence.
func (s *Project) FencePlanAmendment(ctx context.Context, state PlanAmendmentState) error {
	if err := s.ValidatePlanAmendmentState(state); err != nil {
		return err
	}
	if err := s.CheckPlanAmendment(ctx, state); err != nil {
		return err
	}
	current, err := s.inspectAmendmentItem(ctx, state, 0)
	if err != nil {
		return err
	}
	if current.Phase == PlanAmendingPhase || amendmentItemMatches(current, state.After[0]) {
		return nil
	}
	fence := state.Before[0]
	fence.Phase = PlanAmendingPhase
	return s.writeAmendmentItem(ctx, current, fence, false)
}

// CheckPlanAmendment refuses additions, disappearance, unexpected field values
// or publication, including an ambiguous PR creation not yet recorded on Project.
func (s *Project) CheckPlanAmendment(ctx context.Context, state PlanAmendmentState) error {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return err
	}
	index := newWorkItemIndex(items)
	parent := state.Before[0]
	if len(index.childrenBySource[parent.ID]) != len(state.Before)-1 {
		return errors.New("amendment membership changed; preserve operator changes and inspect")
	}
	for i, before := range state.Before {
		actual, ok := index.byID[before.ID]
		if !ok || !amendmentIntermediate(actual, before, state.After[i], i == 0) {
			return fmt.Errorf("plan amendment item %s changed outside its recorded before/after states; no overwrite", before.ID)
		}
	}
	manifest, _, _ := ParsePlanManifest(parent.Body)
	_, found, err := NewPullRequestManager(s.run, s).findPlanPublication(ctx, parent.Repository, parent.Branch, manifest.DestinationBranch)
	if err != nil {
		return err
	}
	if found {
		return errors.New("plan publication appeared during amendment; leave fenced and coordinate the existing PR")
	}
	return nil
}

func amendmentIntermediate(actual, before, after WorkItem, parent bool) bool {
	if actual.Transition != "" && actual.Transition != transitionLockValue {
		return false
	}
	if actual.Body == after.Body {
		actual.Body, actual.Dependencies, actual.ImplementationProfile = before.Body, before.Dependencies, before.ImplementationProfile
	} else if actual.Body != before.Body {
		return false
	}
	pairs := [][3]*string{{&actual.Approval, &before.Approval, &after.Approval}, {&actual.PlanRelease, &before.PlanRelease, &after.PlanRelease}, {&actual.Status, &before.Status, &after.Status}, {&actual.QACommit, &before.QACommit, &after.QACommit}, {&actual.Activity, &before.Activity, &after.Activity}}
	for _, pair := range pairs {
		if *pair[0] != *pair[1] && *pair[0] != *pair[2] {
			return false
		}
		*pair[0] = *pair[1]
	}
	if actual.Phase != before.Phase && actual.Phase != after.Phase && !(parent && actual.Phase == PlanAmendingPhase) {
		return false
	}
	actual.Phase = before.Phase
	return amendmentItemMatches(actual, before)
}

// The whole-plan check cannot validate a later item read. Refuse an intervening
// edit before acquiring its transition or writing any contract fields.
func (s *Project) inspectAmendmentItem(ctx context.Context, state PlanAmendmentState, index int) (WorkItem, error) {
	actual, err := s.InspectRecoveryItem(ctx, state.Before[index].ID)
	if err != nil {
		return WorkItem{}, err
	}
	if !amendmentIntermediate(actual, state.Before[index], state.After[index], index == 0) {
		return WorkItem{}, fmt.Errorf("plan amendment item %s changed outside its recorded before/after states; no overwrite", actual.ID)
	}
	return actual, nil
}

func (s *Project) WritePlanAmendmentContracts(ctx context.Context, state PlanAmendmentState) error {
	for i := 1; i < len(state.After); i++ {
		if err := s.CheckPlanAmendment(ctx, state); err != nil {
			return err
		}
		current, err := s.inspectAmendmentItem(ctx, state, i)
		if err != nil {
			return err
		}
		if err := s.writeAmendmentItem(ctx, current, state.After[i], true); err != nil {
			return err
		}
	}
	if err := s.CheckPlanAmendment(ctx, state); err != nil {
		return err
	}
	current, err := s.inspectAmendmentItem(ctx, state, 0)
	if err != nil {
		return err
	}
	after := state.After[0]
	after.Phase = PlanAmendingPhase // do not reopen admission before local proof/rebinding completes
	return s.writeAmendmentItem(ctx, current, after, false)
}

func (s *Project) FinishPlanAmendment(ctx context.Context, state PlanAmendmentState) error {
	if err := s.CheckPlanAmendment(ctx, state); err != nil {
		return err
	}
	for _, expected := range state.After[1:] {
		actual, err := s.InspectRecoveryItem(ctx, expected.ID)
		if err != nil || actual.Transition != "" || !amendmentItemMatches(actual, expected) {
			return errors.Join(errors.New("amended child did not commit exactly"), err)
		}
	}
	actual, err := s.inspectAmendmentItem(ctx, state, 0)
	if err != nil {
		return err
	}
	return s.writeAmendmentItem(ctx, actual, state.After[0], true)
}

func (s *Project) writeAmendmentItem(ctx context.Context, actual, next WorkItem, unlock bool) error {
	if amendmentItemMatches(actual, next) && (actual.Transition == "" || !unlock) {
		return nil
	}
	if actual.Transition == "" {
		if err := s.beginTransition(ctx, actual.ID); err != nil {
			return err
		}
		locked, err := s.InspectRecoveryItem(ctx, actual.ID)
		if err != nil || locked.Transition != transitionLockValue || !amendmentItemMatches(locked, actual) {
			return errors.Join(errors.New("amendment item changed while acquiring transition; retain fence and inspect"), err)
		}
	}
	if actual.Body != next.Body {
		if err := s.writePlanBody(ctx, actual, next.Body); err != nil {
			return err
		}
	}
	var updates []projectFieldUpdate
	for _, field := range []struct{ name, old, value string }{
		{config.RunnerPlanReleaseFieldName, actual.PlanRelease, next.PlanRelease}, {s.phaseFieldName(), actual.Phase, next.Phase},
		{s.qaCommitFieldName(), actual.QACommit, next.QACommit}, {s.activityFieldName(), actual.Activity, next.Activity}, {s.approvalFieldName(), actual.Approval, next.Approval},
	} {
		if field.old != field.value {
			updates = append(updates, textProjectField(field.name, field.value))
		}
	}
	if actual.Status != next.Status {
		updates = append(updates, statusProjectField(s.statusFieldName(), next.Status))
	}
	if err := s.applyFieldUpdates(ctx, next.ID, updates...); err != nil {
		return err
	}
	readback, err := s.InspectRecoveryItem(ctx, next.ID)
	if err != nil || !amendmentItemMatches(readback, next) {
		return errors.Join(errors.New("amendment readback differs; retain fence and inspect"), err)
	}
	if unlock {
		return s.finishTransition(ctx, next.ID)
	}
	return nil
}
