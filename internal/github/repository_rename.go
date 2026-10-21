package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// RepositoryRenameTransition belongs exclusively to confirmed operator recovery.
// Ordinary polling must leave this durable stop marker alone.
const RepositoryRenameTransition = "repository-rename-v1"

type RepositoryIdentity struct {
	ID       int64  `json:"id"`
	NodeID   string `json:"node_id"`
	FullName string `json:"full_name"`
}

type RepositoryRenameChild struct {
	Before WorkItem `json:"before"`
	After  WorkItem `json:"after"`
}

type RepositoryRenamePlan struct {
	From       string                  `json:"from"`
	To         string                  `json:"to"`
	Repository RepositoryIdentity      `json:"repository"`
	Source     WorkItem                `json:"source"`
	Renewed    WorkItem                `json:"renewed_source"`
	Children   []RepositoryRenameChild `json:"children"`
}

func (s *Project) VerifyRepositoryRename(ctx context.Context, from string) (RepositoryIdentity, error) {
	to := s.cfg.IntakeRepository
	oldOwner, _, oldOK := strings.Cut(from, "/")
	newOwner, _, newOK := strings.Cut(to, "/")
	if !oldOK || !newOK || !strings.EqualFold(oldOwner, newOwner) || strings.EqualFold(from, to) {
		return RepositoryIdentity{}, errors.New("recovery requires distinct old and canonical new names under the same GitHub owner")
	}
	for _, name := range []string{from, to} {
		if normalized, err := normalizeGitHubRepository(name); err != nil || normalized != name || strings.ContainsAny(name, "?#%") {
			return RepositoryIdentity{}, errors.New("repository names must be plain owner/name values")
		}
	}
	var identities [2]RepositoryIdentity
	for i, name := range []string{from, to} {
		result, err := s.gh(ctx, "api", "repos/"+name)
		if err != nil {
			return RepositoryIdentity{}, fmt.Errorf("resolve immutable repository identity for %s: %w", name, commandFailure(err, result))
		}
		if err := json.Unmarshal([]byte(result.Stdout), &identities[i]); err != nil {
			return RepositoryIdentity{}, fmt.Errorf("decode repository identity: %w", err)
		}
		if identities[i].ID <= 0 || identities[i].NodeID == "" || identities[i].FullName != to {
			return RepositoryIdentity{}, errors.New("both repository names must resolve unambiguously to the configured canonical repository with numeric and node IDs")
		}
	}
	if identities[0] != identities[1] {
		return RepositoryIdentity{}, errors.New("old and new names identify different GitHub repositories")
	}
	return identities[0], nil
}

func (s *Project) PlanRepositoryRename(ctx context.Context, selector, from string) (RepositoryRenamePlan, error) {
	identity, err := s.VerifyRepositoryRename(ctx, from)
	if err != nil {
		return RepositoryRenamePlan{}, err
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return RepositoryRenamePlan{}, err
	}
	selected, err := selectProjectItem(items, selector)
	if err != nil {
		return RepositoryRenamePlan{}, err
	}
	source, err := selectProjectItem(items, stagedBatchSourceID(selected, items))
	if err != nil {
		return RepositoryRenamePlan{}, err
	}
	children := []WorkItem{}
	for _, item := range items {
		if item.PlanningSourceID == source.ID {
			children = append(children, item)
		}
	}
	return s.buildRepositoryRename(from, identity, source, children)
}

func (s *Project) buildRepositoryRename(from string, identity RepositoryIdentity, source WorkItem, children []WorkItem) (RepositoryRenamePlan, error) {
	fail := func(message string) (RepositoryRenamePlan, error) { return RepositoryRenamePlan{}, errors.New(message) }
	if source.Status != s.doneStatus() || source.Phase != "" || source.Transition != "" || source.Activity != "" ||
		source.Branch != "" || source.PullRequest != "" || source.QACommit != "" || source.PlanningMetadataInvalid ||
		source.PlanningBatchFingerprint != "" || source.PlanRelease != "" || source.Repository != s.cfg.IntakeRepository || !issueReferenceMatchesRepository(source.URL, source.Repository) {
		return fail("rename recovery requires an intact, completed issue-backed planning source without delivery or active action state")
	}
	if _, present, _ := ParsePlanManifest(source.Body); present {
		return fail("rename recovery supports legacy planning-only batches; whole-plan delivery requires its own recovery")
	}
	ordered := append([]WorkItem(nil), children...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PlanningItemIndex < ordered[j].PlanningItemIndex })
	oldSource := source
	oldSource.Repository = from
	oldChildren := make([]WorkItem, len(ordered))
	seen := map[string]bool{source.ID: true}
	for i, child := range ordered {
		metadata, present, err := decodePlannedItemMetadata(child.Body)
		if err != nil || !present || metadata.Repository != from || child.Repository != source.Repository ||
			child.PlanningMetadataInvalid || child.Status != s.assessmentStatus() || child.Approval != "" ||
			child.Transition != "" || child.Activity != "" || child.PullRequest != "" || child.QACommit != "" || child.PlanRelease != "" ||
			(child.Phase != "" && child.Phase != s.laneIDForStatus(s.readyStatus())) || child.QAFailures < 0 ||
			!issueReferenceMatchesRepository(child.URL, source.Repository) || child.IssueState == "CLOSED" || seen[child.ID] {
			return fail("rename recovery requires every exact child in assessment with missing approval, no active assignment, and no published or accepted candidate")
		}
		seen[child.ID] = true
		oldChildren[i] = child
		oldChildren[i].Repository = from
	}
	provenance, err := s.validatePlanningBatch(source.Approval, oldSource, oldChildren, batchReleasedState)
	if err != nil {
		return RepositoryRenamePlan{}, fmt.Errorf("authenticate original released batch under its old repository name: %w", err)
	}
	if provenance.Destination != s.readyStatus() || s.cfg.PlanningDestinations[provenance.SourceLane] != provenance.Destination {
		return fail("original batch destination is no longer the authorized implementation lane")
	}
	plan := RepositoryRenamePlan{From: from, To: source.Repository, Repository: identity, Source: source, Renewed: source}
	newChildren := make([]WorkItem, len(ordered))
	for i, child := range ordered {
		next := child
		next.Body, err = renamedPlanningBody(child.Body, plan.To, PlanningSourceFingerprint(source))
		if err != nil {
			return RepositoryRenamePlan{}, err
		}
		next.PlanningSourceFingerprint = PlanningSourceFingerprint(source)
		next.Status, next.Phase = s.readyStatus(), s.laneIDForStatus(s.readyStatus())
		next.Result = "Operator recovered the complete planning batch after repository rename; fresh implementation and verification required."
		action, err := s.signAction(next, s.cfg.LaneRoles[next.Phase], next.Phase)
		if err != nil {
			return RepositoryRenamePlan{}, err
		}
		newChildren[i] = action.Item
		// Role is derived locally when authorizing; it is not a Project field.
		newChildren[i].Role = child.Role
		plan.Children = append(plan.Children, RepositoryRenameChild{Before: child, After: newChildren[i]})
	}
	plan.Renewed.Approval, err = s.signPlanningBatch(source, newChildren, batchReleasedState, provenance.Generation)
	return plan, err
}

// renamedPlanningBody changes only the two administrative bindings in the
// canonical final metadata block. Requirements and dependency text stay byte-exact.
func renamedPlanningBody(body, repository, fingerprint string) (string, error) {
	if _, present, err := decodePlannedItemMetadata(body); err != nil || !present {
		return "", errors.New("cannot renew malformed planning metadata")
	}
	start := strings.LastIndex(body, "```json\n") + len("```json\n")
	end := strings.LastIndex(body, "\n```")
	var wire plannedItemMetadataWire
	if err := json.Unmarshal([]byte(body[start:end]), &wire); err != nil {
		return "", err
	}
	wire.Repository, wire.PlanningSourceFingerprint = repository, fingerprint
	encoded, err := json.Marshal(wire)
	return body[:start] + string(encoded) + body[end:], err
}

// ValidateRepositoryRename also accepts only the exact intermediate states of
// this operation, allowing an explicitly confirmed retry after a lost response.
func (s *Project) ValidateRepositoryRename(ctx context.Context, plan RepositoryRenamePlan) error {
	identity, err := s.VerifyRepositoryRename(ctx, plan.From)
	if err != nil {
		return err
	}
	before := make([]WorkItem, len(plan.Children))
	for i := range before {
		before[i] = plan.Children[i].Before
	}
	rebuilt, err := s.buildRepositoryRename(plan.From, identity, plan.Source, before)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(rebuilt, plan) {
		return errors.New("repository rename preview or original provenance changed")
	}
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return err
	}
	members := map[string]bool{plan.Source.ID: true}
	for _, child := range plan.Children {
		members[child.Before.ID] = true
	}
	for _, item := range items {
		if (item.PlanningSourceID == plan.Source.ID || item.PlanningBatchFingerprint == before[0].PlanningBatchFingerprint) && !members[item.ID] {
			return errors.New("planning batch gained an unexpected or mixed member")
		}
	}
	source, err := selectProjectItem(items, plan.Source.ID)
	if err != nil || !renameIntermediate(source, plan.Source, plan.Renewed) {
		return errors.New("planning source changed after rename preview")
	}
	for _, child := range plan.Children {
		current, err := selectProjectItem(items, child.Before.ID)
		if err != nil || !renameIntermediate(current, child.Before, child.After) {
			return fmt.Errorf("planning child %s changed after rename preview", child.Before.ID)
		}
	}
	return nil
}

func renameIntermediate(current, before, after WorkItem) bool {
	if current.Body != before.Body && current.Body != after.Body ||
		current.Approval != before.Approval && current.Approval != after.Approval ||
		current.Status != before.Status && current.Status != after.Status ||
		current.Phase != before.Phase && current.Phase != after.Phase ||
		current.Result != before.Result && current.Result != after.Result ||
		current.Transition != "" && current.Transition != RepositoryRenameTransition {
		return false
	}
	current.Body, current.Approval, current.Status = before.Body, before.Approval, before.Status
	current.Phase, current.Result, current.Transition = before.Phase, before.Result, before.Transition
	current.PlanningSourceFingerprint = before.PlanningSourceFingerprint
	left, _ := json.Marshal(current)
	right, _ := json.Marshal(before)
	return current.PlanningMetadataInvalid == before.PlanningMetadataInvalid && bytes.Equal(left, right)
}

// ApplyRepositoryRename runs under the engine's maintenance locks and durable
// private intent. The callback renews private workspace bindings before release.
func (s *Project) ApplyRepositoryRename(ctx context.Context, plan RepositoryRenamePlan, renewWorkspaces func() error) error {
	if err := s.ValidateRepositoryRename(ctx, plan); err != nil {
		return err
	}
	// Lock every member before changing content. These markers survive ordinary
	// polling, including a crash before the final release.
	for _, id := range append([]string{plan.Source.ID}, renameChildIDs(plan)...) {
		if err := s.setTextField(ctx, id, s.transitionFieldName(), RepositoryRenameTransition); err != nil {
			return err
		}
	}
	for _, child := range plan.Children {
		result, err := s.gh(ctx, "issue", "edit", child.Before.URL, "--body", child.After.Body)
		if err != nil {
			return fmt.Errorf("renew planning metadata; rerun confirmed rename recovery: %w", commandFailure(err, result))
		}
		if err := s.applyFieldUpdates(ctx, child.After.ID,
			textProjectField(s.phaseFieldName(), child.After.Phase),
			textProjectField(s.resultFieldName(), child.After.Result),
			textProjectField(s.approvalFieldName(), child.After.Approval),
			statusProjectField(s.statusFieldName(), child.After.Status)); err != nil {
			return err
		}
	}
	if err := renewWorkspaces(); err != nil {
		return err
	}
	if err := s.ValidateRepositoryRename(ctx, plan); err != nil {
		return err
	}
	if err := s.setApproval(ctx, plan.Renewed.ID, plan.Renewed.Approval); err != nil {
		return err
	}
	// While any sibling remains locked, the complete-batch eligibility gate
	// prevents every member from executing. Nothing grants QA or merge authority.
	for _, id := range append(renameChildIDs(plan), plan.Source.ID) {
		if err := s.finishTransition(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func renameChildIDs(plan RepositoryRenamePlan) []string {
	ids := make([]string, len(plan.Children))
	for i := range ids {
		ids[i] = plan.Children[i].Before.ID
	}
	return ids
}
