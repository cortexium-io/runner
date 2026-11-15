package runner

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
)

// TaskAllowanceGrant amends only the root's aggregate ceiling. Historical
// executions, continuation debits and individual role-visit limits are retained.
type TaskAllowanceGrant struct {
	RequestID     string    `json:"request_id"`
	PreviousLimit int       `json:"previous_limit"`
	Limit         int       `json:"limit"`
	Spent         int       `json:"spent_attempts"`
	Reason        string    `json:"reason"`
	GrantedAt     time.Time `json:"granted_at"`
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
	spent := 0
	var grantedAt time.Time
	for _, grant := range g.AllowanceGrants {
		if grant.RequestID == "" || len(grant.RequestID) > 128 || seen[grant.RequestID] || grant.PreviousLimit != limit || limit < 1 || grant.Limit <= limit || grant.Limit > MaxGeneralTaskAttempts || grant.Spent < spent || grant.Spent > limit || strings.TrimSpace(grant.Reason) == "" || len(grant.Reason) > 2048 || grant.GrantedAt.IsZero() || grant.GrantedAt.Before(grantedAt) {
			return errors.New("invalid root goal allowance grant chain")
		}
		seen[grant.RequestID] = true
		limit, spent, grantedAt = grant.Limit, grant.Spent, grant.GrantedAt
	}
	if r.Process.MaxAttempts != limit {
		return errors.New("goal attempt ceiling differs from its audited owner grants")
	}
	return nil
}

// ValidateTaskAllowanceChange authenticates an append against the ordinary
// retry receipt in the same transaction. A save without that owner operation
// cannot mint capacity, rewrite a grant or alter work while granting capacity.
func ValidateTaskAllowanceChange(before, after Run, receipt *Receipt, spent int) error {
	var oldGrants, newGrants []TaskAllowanceGrant
	if before.TaskGraph != nil {
		oldGrants = before.TaskGraph.AllowanceGrants
	}
	if after.TaskGraph != nil {
		newGrants = after.TaskGraph.AllowanceGrants
	}
	if reflect.DeepEqual(oldGrants, newGrants) {
		if before.TaskGraph != nil && before.Process.MaxAttempts != after.Process.MaxAttempts {
			return errors.New("goal attempt ceiling can change only with an audited owner retry grant")
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
	grant := newGrants[len(oldGrants)]
	if grant.RequestID != receipt.Key || grant.PreviousLimit != before.Process.MaxAttempts || grant.Limit != before.Process.MaxAttempts+req.ExtraAttempts || grant.Spent != spent || grant.Reason != taskAllowanceGrantReason(req) || after.Revision != before.Revision+1 || after.Updated.Before(grant.GrantedAt) || before.Status != "Failed" && before.Status != "Waiting" || before.Decision != nil || before.TaskGraph.Phase == TaskWaitingInput {
		return bad
	}
	expected := Clone(before)
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
