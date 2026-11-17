package runner

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type TaskModelChange struct {
	RoleID        string `json:"role_id"`
	PreviousModel string `json:"previous_model"`
	Model         string `json:"model"`
}

// RoleDefinitionGrants returns the one owning definition audit chain. General
// tasks keep their existing chain; fixed processes have no task-allowance state.
func RoleDefinitionGrants(r Run) []TaskAllowanceGrant {
	if r.TaskGraph != nil {
		return r.TaskGraph.AllowanceGrants
	}
	return r.RoleAmendments
}

func setRoleDefinitionGrants(r *Run, grants []TaskAllowanceGrant) {
	if r.TaskGraph != nil {
		r.TaskGraph.AllowanceGrants = grants
	} else {
		r.RoleAmendments = grants
	}
}

func validAmendedModel(model string) bool {
	return model != "" && len(model) <= 128 && utf8.ValidString(model) &&
		!strings.ContainsAny(model, " \t\r\n\x00")
}

// A settled child's definition remains historical proof. Its next visit uses
// the root's explicit model amendment, never a global model-name alias.
func (c *Core) amendedVisitModel(r Run, a Attempt, roleID, original string) string {
	role := r.Roles[roleID]
	selected := a.Profile
	if selected == "" {
		selected = role.DefaultProfile
	}
	if a.Started.IsZero() || role.Execution == nil || selected != "" && selected != "default" {
		return original
	}
	root, err := c.taskRoot(r)
	if r.TaskGraph != nil {
		root, err = c.graphRoot(r)
	}
	if err != nil {
		return original
	}
	for _, grant := range RoleDefinitionGrants(root) {
		if a.Started.Before(grant.GrantedAt) {
			continue
		}
		for _, change := range grant.ModelChanges {
			if change.RoleID == roleID {
				original = change.Model
			}
		}
	}
	return original
}

func applyRoleAmendment(r *Run, req TaskRoleAmendmentRequest, grant *TaskAllowanceGrant) ([]byte, error) {
	if len(req.RoleModels) == 0 {
		if r.TaskGraph == nil || r.Status != "Waiting" || !r.TaskGraph.Paused {
			return nil, errors.New("instruction amendments require a paused general root")
		}
		return applyTaskDefinitionAmendment(r, RetryRequest{RoleInstructions: req.RoleInstructions}, grant)
	}
	if len(req.RoleInstructions) != 0 || len(req.RoleModels) > len(r.Roles) {
		return nil, errors.New("model amendments cannot change instructions or introduce roles")
	}
	_, definition, err := MarshalRunRecord(*r)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(req.RoleModels))
	for id := range req.RoleModels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		role, exists := r.Roles[id]
		model := req.RoleModels[id]
		if !exists || role.Execution == nil || role.Execution.Harness != "native" && role.Execution.Harness != "codex" || !validAmendedModel(model) || !validAmendedModel(role.Execution.Model) || model == role.Execution.Model {
			return nil, fmt.Errorf("role %q needs a changed default model on an existing model harness", id)
		}
		grant.ModelChanges = append(grant.ModelChanges, TaskModelChange{
			RoleID:        id,
			PreviousModel: role.Execution.Model,
			Model:         model,
		})
		profile := *role.Execution
		profile.Model = model
		role.Execution = &profile
		r.Roles[id] = role
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
