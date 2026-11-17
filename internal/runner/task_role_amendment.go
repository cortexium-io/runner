package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// TaskRoleAmendmentRequest changes either future native instructions or default
// model selections. It grants no execution, access, allowance or approval.
type TaskRoleAmendmentRequest struct {
	RequestID        string            `json:"request_id"`
	RunID            string            `json:"run_id"`
	Revision         int64             `json:"revision"`
	Reason           string            `json:"reason"`
	RoleInstructions map[string]string `json:"role_instructions,omitempty"`
	RoleModels       map[string]string `json:"role_models,omitempty"`
}

const taskRoleAmendmentReceipt = "task_role_amendment"

func roleOnlyAmendment(grant TaskAllowanceGrant) bool {
	return grant.Limit == grant.PreviousLimit && (len(grant.InstructionChanges) != 0 || len(grant.ModelChanges) != 0) && grant.PreviousDefinition != nil && grant.PreviousMaxTasks == 0 && grant.MaxTasks == 0
}

func taskRoleAmendmentRequest(receipt Receipt) (TaskRoleAmendmentRequest, error) {
	var req TaskRoleAmendmentRequest
	decoder := json.NewDecoder(bytes.NewReader(receipt.Args))
	decoder.DisallowUnknownFields()
	if receipt.Kind != taskRoleAmendmentReceipt || decoder.Decode(&req) != nil {
		return req, errors.New("invalid owner role amendment receipt")
	}
	canonical, err := json.Marshal(req)
	if err != nil || !bytes.Equal(canonical, receipt.Args) || req.RequestID != receipt.Key || req.RunID != receipt.RunID || len(req.RequestID) < 1 || len(req.RequestID) > 128 || req.Revision < 1 || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 2048 || (len(req.RoleInstructions) == 0) == (len(req.RoleModels) == 0) {
		return req, errors.New("owner role amendment needs its exact request, revision, exactly one instruction or model map and bounded reason")
	}
	return req, nil
}

func (c *Core) roleAmendmentFamily(r Run, revision int64) ([]Run, error) {
	if r.TaskGraph == nil {
		if r.Task != nil || r.Status != "Failed" || !r.ResourcesReleased || r.CancelRequested || r.Decision != nil || r.CleanupBlocked || r.CleanupError != "" || !c.graphOwnSettled(r) || revision < 1 || revision != c.outputRun(r).Revision || c.familyAttempts(r) == invalidFamilyAttempts {
			return nil, errors.New("model amendment needs the exact settled failed fixed-process root revision")
		}
		family := []Run{r}
		for _, group := range r.TaskGroups {
			for _, link := range group.Children {
				child, ok := c.runs[link.RunID]
				if !ok || !child.Terminal() || !child.ResourcesReleased || child.CleanupBlocked || child.CleanupError != "" || !c.graphOwnSettled(child) {
					return nil, errors.New("model amendment needs settled retained children")
				}
				family = append(family, child)
			}
		}
		return family, nil
	}
	if r.ID != r.TaskGraph.RootRunID || r.TaskGraph.ParentRunID != "" || (r.Status != "Waiting" || !r.TaskGraph.Paused) && r.Status != "Failed" || r.CancelRequested || r.Decision != nil || r.Step < 0 || r.Step >= len(r.Process.Steps) || revision < 1 {
		return nil, errors.New("role amendment needs the exact paused or failed settled general root revision")
	}
	_, family, reserved, err := c.graphFamily(r)
	if err != nil || reserved != 0 {
		return nil, errors.New("role amendment needs an intact fully materialized family")
	}
	if revision != c.outputRun(r).Revision {
		return nil, errors.New("role amendment revision changed; inspect the current family before amending")
	}
	for _, member := range family {
		if !c.graphOwnSettled(member) || member.CleanupBlocked || member.CleanupError != "" || member.ID != r.ID && (!member.Terminal() || !member.ResourcesReleased) {
			return nil, errors.New("role amendment needs all affected attempts and descendant cleanup settled; root source resources remain retained")
		}
	}
	return family, nil
}

// AmendTaskRoles records an owner amendment without resuming the paused root.
// Existing child definitions and every attempt remain immutable.
func (c *Core) AmendTaskRoles(req TaskRoleAmendmentRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, err := json.Marshal(req)
	if err != nil || len(args) > int(c.disk.Policy.Database.MaxRecord) {
		return Run{}, fmt.Errorf("%w: role amendment receipt exceeds max_record", ErrCapacity)
	}
	receipt := Receipt{Key: req.RequestID, Kind: taskRoleAmendmentReceipt, Args: args, RunID: req.RunID}
	if r, replayed, err := c.replay(req.RequestID, taskRoleAmendmentReceipt, args); replayed || err != nil {
		if err != nil {
			return Run{}, err
		}
		for _, grant := range RoleDefinitionGrants(r) {
			if grant.RequestID == req.RequestID && roleOnlyAmendment(grant) {
				if err := c.readTaskDefinitionProofs(r); err != nil {
					return Run{}, err
				}
				return c.outputRun(r), nil
			}
		}
		family, err := c.roleAmendmentFamily(r, req.Revision)
		if err != nil {
			return Run{}, err
		}
		return c.reconcileTaskRoleAmendment(r, family, receipt, errors.New("committed role amendment is not yet reconciled; reopen the same state if retained reads remain unavailable"))
	}
	if _, err := taskRoleAmendmentRequest(receipt); err != nil {
		return Run{}, err
	}
	before, exists := c.runs[req.RunID]
	if !exists {
		return Run{}, ErrNotFound
	}
	if err := c.taskContinuationFrozen(before); err != nil {
		return Run{}, err
	}
	family, err := c.roleAmendmentFamily(before, req.Revision)
	if err != nil {
		return Run{}, err
	}
	if err := c.readTaskDefinitionProofs(before); err != nil {
		return Run{}, err
	}
	after := Clone(before)
	grant := TaskAllowanceGrant{
		RequestID:     req.RequestID,
		PreviousLimit: before.Process.MaxAttempts,
		Limit:         before.Process.MaxAttempts,
		Spent:         c.familyAttempts(before),
		Reason:        req.Reason,
		GrantedAt:     time.Now().UTC(),
	}
	definition, err := applyRoleAmendment(&after, req, &grant)
	if err != nil {
		return Run{}, err
	}
	setRoleDefinitionGrants(&after, append(RoleDefinitionGrants(after), grant))
	if err := admitRunRecord(after, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	if err := c.writeTaskDefinitionProof(after, grant.PreviousDefinition, definition); err != nil {
		return Run{}, err
	}
	proof := grant.PreviousDefinition
	artifacts := []Artifact{{
		ID:        proof.ID,
		RunID:     after.ID,
		ProjectID: after.ProjectID,
		Bytes:     proof.Bytes,
	}}
	if err := c.persist(after, artifacts, &receipt); err != nil {
		return c.reconcileTaskRoleAmendment(before, family, receipt, err)
	}
	return c.outputRun(c.runs[after.ID]), nil
}

func validateTaskRoleAmendment(before, after Run, receipt Receipt, family []Run) error {
	req, err := taskRoleAmendmentRequest(receipt)
	if err != nil {
		return err
	}
	bad := errors.New("role amendment must preserve paused work, authority and allowance with one authenticated definition proof")
	oldGrants, newGrants := RoleDefinitionGrants(before), RoleDefinitionGrants(after)
	if req.RunID != before.ID || len(newGrants) != len(oldGrants)+1 || len(oldGrants) != 0 && !reflect.DeepEqual(oldGrants, newGrants[:len(oldGrants)]) {
		return bad
	}
	view := &Core{runs: map[string]Run{}}
	for _, member := range family {
		if _, exists := view.runs[member.ID]; exists {
			return bad
		}
		view.runs[member.ID] = member
	}
	if !reflect.DeepEqual(view.runs[before.ID], before) {
		return bad
	}
	members, err := view.roleAmendmentFamily(before, req.Revision)
	if err != nil || len(members) != len(family) {
		return bad
	}
	grant := newGrants[len(oldGrants)]
	if !roleOnlyAmendment(grant) || grant.RequestID != req.RequestID || grant.PreviousLimit != before.Process.MaxAttempts || grant.Spent != view.familyAttempts(before) || grant.Reason != req.Reason || grant.GrantedAt.IsZero() || after.Revision != before.Revision+1 || after.Updated.Before(grant.GrantedAt) {
		return bad
	}
	expected, expectedGrant := Clone(before), grant
	expectedGrant.PreviousDefinition, expectedGrant.InstructionChanges, expectedGrant.ModelChanges = nil, nil, nil
	if _, err := applyRoleAmendment(&expected, req, &expectedGrant); err != nil || !reflect.DeepEqual(expectedGrant, grant) {
		return bad
	}
	setRoleDefinitionGrants(&expected, Clone(newGrants))
	expected.Revision, expected.Updated = after.Revision, after.Updated
	if !reflect.DeepEqual(expected, after) {
		return bad
	}
	return taskAllowanceRecord(after)
}

// Reconcile only the exact committed paused amendment after a lost save reply.
// No execution is started and no other retained in-memory family is replaced.
func (c *Core) reconcileTaskRoleAmendment(before Run, family []Run, receipt Receipt, original error) (Run, error) {
	savedReceipt, err := c.store.FindReceipt(c.ctx, receipt.Key)
	if err != nil || !reflect.DeepEqual(savedReceipt, receipt) {
		return Run{}, original
	}
	rows, err := c.store.Load(c.ctx)
	if err != nil {
		return Run{}, original
	}
	savedRows := map[string]Run{}
	for _, row := range rows {
		savedRows[row.ID] = row
	}
	for _, member := range family {
		if member.ID != before.ID && !reflect.DeepEqual(savedRows[member.ID], member) {
			return Run{}, original
		}
	}
	saved := savedRows[before.ID]
	if validateTaskRoleAmendment(before, saved, receipt, family) != nil || c.readTaskDefinitionProofs(saved) != nil {
		return Run{}, original
	}
	c.runs[saved.ID], c.maintenanceError = saved, ""
	close(c.changed)
	c.changed = make(chan struct{})
	return c.outputRun(saved), nil
}
