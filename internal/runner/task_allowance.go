package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// TaskAllowanceGrant amends only the root's aggregate ceiling. Historical
// executions, continuation debits and individual role-visit limits are retained.
type TaskAllowanceGrant struct {
	PreviousDefinition *CardRecoveryArtifact   `json:"previous_definition,omitempty"`
	InstructionChanges []TaskInstructionChange `json:"instruction_changes,omitempty"`
	PreviousMaxTasks   int                     `json:"previous_max_tasks,omitempty"`
	MaxTasks           int                     `json:"max_tasks,omitempty"`
	RequestID          string                  `json:"request_id"`
	PreviousLimit      int                     `json:"previous_limit"`
	Limit              int                     `json:"limit"`
	Spent              int                     `json:"spent_attempts"`
	Reason             string                  `json:"reason"`
	GrantedAt          time.Time               `json:"granted_at"`
}

type TaskInstructionChange struct {
	RoleID         string `json:"role_id"`
	PreviousSHA256 string `json:"previous_sha256"`
	SHA256         string `json:"sha256"`
}

func taskDefinitionAmendment(req RetryRequest) bool {
	return req.RoleInstructions != nil || req.MaxTasks != 0
}

// Only default native instructions and the aggregate task count may change.
// The prior complete definition remains one canonical immutable artifact.
func applyTaskDefinitionAmendment(r *Run, req RetryRequest, grant *TaskAllowanceGrant) ([]byte, error) {
	if !taskDefinitionAmendment(req) {
		return nil, nil
	}
	if r.Process.Tasks == nil || req.RoleInstructions != nil && len(req.RoleInstructions) == 0 || len(req.RoleInstructions) > len(r.Roles) {
		return nil, errors.New("definition amendment needs existing native roles or an increased task allowance")
	}
	_, definition, err := MarshalRunRecord(*r)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(req.RoleInstructions))
	for id := range req.RoleInstructions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		role, exists := r.Roles[id]
		instructions := req.RoleInstructions[id]
		if !exists || role.Execution == nil || role.Execution.Harness != "native" || role.Execution.Native == nil || !utf8.ValidString(instructions) || strings.TrimSpace(instructions) == "" || len(instructions) > 32<<10 || len(instructions) > int(role.Execution.MaxInput) || instructions == role.Execution.Instructions {
			return nil, fmt.Errorf("role %q amendment needs changed nonblank default native instructions within existing input limits", id)
		}
		grant.InstructionChanges = append(grant.InstructionChanges, TaskInstructionChange{
			RoleID:         id,
			PreviousSHA256: recoveryDigestRawText(role.Execution.Instructions),
			SHA256:         recoveryDigestRawText(instructions),
		})
		profile := *role.Execution
		profile.Instructions = instructions
		role.Execution = &profile
		r.Roles[id] = role
	}
	if req.MaxTasks != 0 {
		if req.MaxTasks <= r.Process.Tasks.MaxTasks || req.MaxTasks > 64 {
			return nil, errors.New("task allowance amendment must strictly increase max_tasks up to 64")
		}
		grant.PreviousMaxTasks, grant.MaxTasks = r.Process.Tasks.MaxTasks, req.MaxTasks
		r.Process.Tasks.MaxTasks = req.MaxTasks
	}
	digest := recoveryDigestRawText(string(definition))
	grant.PreviousDefinition = &CardRecoveryArtifact{
		ID:     r.ID + "/definition-" + digest + ".json",
		RunID:  r.ID,
		Bytes:  int64(len(definition)),
		SHA256: digest,
	}
	return definition, nil
}

const taskAllowanceReason = "explicit owner goal allowance grant"

func taskAllowanceGrantReason(req RetryRequest) string {
	if req.Reason != "" {
		return req.Reason
	}
	return taskAllowanceReason
}

func taskAllowanceRecord(r Run) error {
	g := r.TaskGraph
	if g == nil {
		return nil
	}
	if len(g.AllowanceGrants) != 0 && (g.RootRunID != r.ID || g.ParentRunID != "") {
		return errors.New("goal allowance grants belong only to the root")
	}
	limit := r.Process.MaxAttempts
	if len(g.AllowanceGrants) != 0 {
		limit = g.AllowanceGrants[0].PreviousLimit
	}
	if g.Lineage != nil && limit != g.Lineage.MaxAttempts {
		return errors.New("goal allowance grants differ from their original continuation ceiling")
	}
	seen := map[string]bool{}
	roleHashes := map[string]string{}
	maxTasks := 0
	spent := 0
	var grantedAt time.Time
	for _, grant := range g.AllowanceGrants {
		if grant.RequestID == "" || len(grant.RequestID) > 128 || seen[grant.RequestID] || grant.PreviousLimit != limit || limit < 1 || grant.Limit <= limit || grant.Limit > MaxGeneralTaskAttempts || grant.Spent < spent || grant.Spent > limit || strings.TrimSpace(grant.Reason) == "" || len(grant.Reason) > 2048 || grant.GrantedAt.IsZero() || grant.GrantedAt.Before(grantedAt) {
			return errors.New("invalid root goal allowance grant chain")
		}
		amended := len(grant.InstructionChanges) != 0 || grant.MaxTasks != 0
		if amended != (grant.PreviousDefinition != nil) || grant.MaxTasks == 0 && grant.PreviousMaxTasks != 0 {
			return errors.New("definition amendment requires its previous definition proof")
		}
		if proof := grant.PreviousDefinition; proof != nil {
			if proof.RunID != r.ID || proof.Bytes <= 0 || !frontendRecheckHex(proof.SHA256, 64) || proof.ID != r.ID+"/definition-"+proof.SHA256+".json" {
				return errors.New("invalid previous task definition artifact")
			}
		}
		previousRole := ""
		for _, change := range grant.InstructionChanges {
			if change.RoleID <= previousRole || !frontendRecheckHex(change.PreviousSHA256, 64) || !frontendRecheckHex(change.SHA256, 64) || change.PreviousSHA256 == change.SHA256 || roleHashes[change.RoleID] != "" && roleHashes[change.RoleID] != change.PreviousSHA256 {
				return errors.New("invalid task instruction amendment chain")
			}
			previousRole, roleHashes[change.RoleID] = change.RoleID, change.SHA256
		}
		if grant.MaxTasks != 0 {
			if grant.PreviousMaxTasks < 1 || grant.MaxTasks <= grant.PreviousMaxTasks || grant.MaxTasks > 64 || maxTasks != 0 && maxTasks != grant.PreviousMaxTasks {
				return errors.New("invalid task allowance amendment chain")
			}
			maxTasks = grant.MaxTasks
		}
		seen[grant.RequestID] = true
		limit, spent, grantedAt = grant.Limit, grant.Spent, grant.GrantedAt
	}
	if r.Process.MaxAttempts != limit {
		return errors.New("goal attempt ceiling differs from its audited owner grants")
	}
	if maxTasks != 0 && (r.Process.Tasks == nil || r.Process.Tasks.MaxTasks != maxTasks) {
		return errors.New("task allowance differs from its owner amendment")
	}
	for id, digest := range roleHashes {
		role := r.Roles[id]
		if role.Execution == nil || role.Execution.Harness != "native" || recoveryDigestRawText(role.Execution.Instructions) != digest {
			return errors.New("current role instructions differ from their owner amendment")
		}
	}
	return nil
}

// ValidateTaskAllowanceChange authenticates an append against the ordinary
// retry receipt in the same transaction. A save without that owner operation
// cannot mint capacity, rewrite a grant or alter work while granting capacity.
func ValidateTaskAllowanceChange(before, after Run, receipt *Receipt, family []Run) error {
	var oldGrants, newGrants []TaskAllowanceGrant
	if before.TaskGraph != nil {
		oldGrants = before.TaskGraph.AllowanceGrants
	}
	if after.TaskGraph != nil {
		newGrants = after.TaskGraph.AllowanceGrants
	}
	if reflect.DeepEqual(oldGrants, newGrants) {
		if before.TaskGraph != nil && (!reflect.DeepEqual(before.Process, after.Process) || !reflect.DeepEqual(before.Roles, after.Roles) || !reflect.DeepEqual(before.Profiles, after.Profiles)) {
			return errors.New("frozen task definition can change only with an audited owner grant")
		}
		return nil
	}
	bad := errors.New("goal allowance change needs one append-only root owner retry grant and unchanged work")
	if before.TaskGraph == nil || after.TaskGraph == nil || before.ID != after.ID || before.ID != before.TaskGraph.RootRunID || before.TaskGraph.ParentRunID != "" || len(newGrants) != len(oldGrants)+1 || len(oldGrants) != 0 && !reflect.DeepEqual(oldGrants, newGrants[:len(oldGrants)]) || receipt == nil || receipt.Kind != "retry" || receipt.RunID != before.ID {
		return bad
	}
	var req RetryRequest
	if err := json.Unmarshal(receipt.Args, &req); err != nil || req.RequestID != receipt.Key || req.RunID != before.ID || req.WithinBudget || req.CleanupOnly || req.ExtraAttempts < 1 || req.ExtraAttempts > MaxGeneralTaskAttempts-before.Process.MaxAttempts {
		return bad
	}
	view := &Core{runs: map[string]Run{}}
	var revision int64
	for _, member := range family {
		if _, duplicate := view.runs[member.ID]; duplicate {
			return bad
		}
		view.runs[member.ID] = member
		revision += member.Revision
	}
	root, members, reserved, err := view.graphFamily(before)
	if err != nil || root.ID != before.ID || len(members) != len(family) || !reflect.DeepEqual(view.runs[before.ID], before) || req.Revision != 0 && req.Revision != revision {
		return bad
	}
	if taskDefinitionAmendment(req) {
		if req.Revision < 1 || reserved != 0 {
			return bad
		}
		for _, member := range members {
			if !view.graphOwnSettled(member) || !member.ResourcesReleased || member.CleanupBlocked || member.CleanupError != "" {
				return bad
			}
		}
	}
	spent := view.familyAttempts(before)
	grant := newGrants[len(oldGrants)]
	if grant.RequestID != receipt.Key || grant.PreviousLimit != before.Process.MaxAttempts || grant.Limit != before.Process.MaxAttempts+req.ExtraAttempts || grant.Spent != spent || grant.Reason != taskAllowanceGrantReason(req) || after.Revision != before.Revision+1 || after.Updated.Before(grant.GrantedAt) || before.Status != "Failed" && before.Status != "Waiting" || before.Decision != nil || before.TaskGraph.Phase == TaskWaitingInput {
		return bad
	}
	expected := Clone(before)
	expectedGrant := grant
	expectedGrant.PreviousDefinition, expectedGrant.InstructionChanges = nil, nil
	expectedGrant.PreviousMaxTasks, expectedGrant.MaxTasks = 0, 0
	if _, err := applyTaskDefinitionAmendment(&expected, req, &expectedGrant); err != nil || !reflect.DeepEqual(grant, expectedGrant) {
		return bad
	}
	expected.Process.MaxAttempts = grant.Limit
	expected.TaskGraph.AllowanceGrants = Clone(newGrants)
	if req.Directive != "" {
		if strings.TrimSpace(req.Directive) == "" || len(req.Directive) > 2048 {
			return bad
		}
		expected.TaskGraph.Directive = req.Directive
	}
	expected.Status, expected.Reason = "Queued", "authorized task retry"
	if before.TaskGraph.Phase == TaskWaitingChildren {
		expected.Status, expected.Reason = "Waiting", "authorized root goal allowance grant"
	}
	if req.Profile != "" {
		expected.NextProfile = req.Profile
	}
	expected.ResourcesReleased, expected.CleanupBlocked, expected.CleanupError = false, false, ""
	expected.Revision, expected.Updated = after.Revision, after.Updated
	if !reflect.DeepEqual(expected, after) {
		return bad
	}
	return taskAllowanceRecord(after)
}

func (c *Core) graphAttemptBudget(r Run) *TaskAttemptBudget {
	root, err := c.graphRoot(r)
	if err != nil {
		return nil
	}
	spent := c.familyAttempts(r)
	budget := &TaskAttemptBudget{
		Limit:     root.Process.MaxAttempts,
		Spent:     spent,
		Remaining: max(0, root.Process.MaxAttempts-spent),
	}
	if root.TaskGraph.Lineage != nil {
		budget.PriorAttempts = root.TaskGraph.Lineage.PriorAttempts
	}
	return budget
}

func (c *Core) writeTaskDefinitionProof(r Run, ref *CardRecoveryArtifact, proof []byte) error {
	registered, err := c.store.Artifact(c.ctx, ref.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	registeredProof := err == nil
	if registeredProof && (registered.RunID != r.ID || registered.ProjectID != r.ProjectID || registered.Bytes != ref.Bytes) {
		return errors.New("previous task definition artifact ownership differs")
	}
	name := "artifacts/" + ref.ID
	old, err := c.disk.Read(name, c.disk.Policy.MaxArtifact)
	if err == nil {
		if !bytes.Equal(old, proof) {
			return errors.New("previous task definition artifact bytes differ; preserve it")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if registeredProof {
		return errors.New("registered previous task definition proof is missing")
	}
	if err := c.disk.Write(name, proof, c.disk.Policy.MaxArtifact); err != nil {
		return err
	}
	return nil
}

func (c *Core) readTaskDefinitionProofs(r Run) error {
	if r.TaskGraph == nil {
		return nil
	}
	var expected *runDefinition
	for _, grant := range r.TaskGraph.AllowanceGrants {
		ref := grant.PreviousDefinition
		if ref == nil {
			continue
		}
		registered, err := c.store.Artifact(c.ctx, ref.ID)
		if err != nil || registered.RunID != r.ID || registered.ProjectID != r.ProjectID || registered.Bytes != ref.Bytes {
			return errors.New("previous task definition artifact registration differs or is missing")
		}
		raw, err := c.disk.Read("artifacts/"+ref.ID, c.disk.Policy.MaxArtifact)
		if err != nil || int64(len(raw)) != ref.Bytes || recoveryDigestRawText(string(raw)) != ref.SHA256 {
			return errors.New("previous task definition proof bytes differ or are missing")
		}
		var definition runDefinition
		if json.Unmarshal(raw, &definition) != nil || definition.Process.MaxAttempts != grant.PreviousLimit {
			return errors.New("previous task definition proof differs from its grant")
		}
		if expected != nil {
			expected.Process.MaxAttempts = definition.Process.MaxAttempts
			if !reflect.DeepEqual(*expected, definition) {
				return errors.New("previous task definition chain differs")
			}
		}
		receipt, err := c.store.FindReceipt(c.ctx, grant.RequestID)
		var req RetryRequest
		if err != nil || receipt.Kind != "retry" || receipt.RunID != r.ID || json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != grant.RequestID || req.RunID != r.ID || req.Revision < 1 || req.ExtraAttempts != grant.Limit-grant.PreviousLimit || req.WithinBudget || req.CleanupOnly {
			return errors.New("task definition amendment receipt differs or is missing")
		}
		prior := Run{
			ID: r.ID, RecordFormat: 9, Process: definition.Process, Roles: definition.Roles, Profiles: definition.Profiles,
			TaskGraph: &TaskGraphState{RootRunID: r.ID, TaskID: "root"},
		}
		amended := grant
		amended.PreviousDefinition, amended.InstructionChanges = nil, nil
		amended.PreviousMaxTasks, amended.MaxTasks = 0, 0
		if _, err := applyTaskDefinitionAmendment(&prior, req, &amended); err != nil || !reflect.DeepEqual(amended, grant) {
			return errors.New("task definition amendment differs from its previous proof and owner receipt")
		}
		prior.Process.MaxAttempts = grant.Limit
		expected = &runDefinition{Process: prior.Process, Roles: prior.Roles, Profiles: prior.Profiles}
	}
	if expected != nil {
		expected.Process.MaxAttempts = r.Process.MaxAttempts
		if !reflect.DeepEqual(*expected, runDefinition{Process: r.Process, Roles: r.Roles, Profiles: r.Profiles}) {
			return errors.New("current task definition differs from its retained amendment chain")
		}
	}
	return nil
}
