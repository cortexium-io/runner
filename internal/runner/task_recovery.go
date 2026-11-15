package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

// RecoverTasks reopens the same failed generation after an explicitly reported
// maintenance repair. It changes no plan, accepted result or execution policy.
type RecoverTasksRequest struct {
	RequestID string   `json:"request_id"`
	RunID     string   `json:"run_id"`
	Revision  int64    `json:"revision"`
	TaskIDs   []string `json:"task_ids"`
	Reason    string   `json:"reason"`
}

type TaskRecoveryAudit struct {
	RequestID string    `json:"request_id"`
	OwnerID   string    `json:"owner_run_id"`
	TaskIDs   []string  `json:"task_ids"`
	Rearmed   []string  `json:"rearmed_run_ids"`
	Spent     int       `json:"spent_attempts"`
	Reason    string    `json:"reason"`
	GrantedAt time.Time `json:"granted_at"`
}

type TaskRecoveryStore interface {
	SaveTaskRecovery(context.Context, []Run, []Run, *Receipt) error
}

func (c *Core) RecoverTasks(req RecoverTasksRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, err := json.Marshal(req)
	if err != nil {
		return Run{}, err
	}
	if r, replay, err := c.replay(req.RequestID, "recover_tasks", args); replay || err != nil {
		if replay {
			r = c.outputRun(r)
		}
		return r, err
	}
	owner, exists := c.runs[req.RunID]
	if !exists {
		return Run{}, ErrNotFound
	}
	_, family, reserved, err := c.graphFamily(owner)
	if err != nil || reserved != 0 {
		return Run{}, errors.New("task recovery requires an intact fully materialized role-task family")
	}
	for _, member := range family {
		if !c.graphOwnSettled(member) {
			return Run{}, errors.New("task recovery requires all family execution and cleanup to settle")
		}
	}
	family = Clone(family) // Match the store's canonical JSON representation.
	after, err := c.planTaskRecovery(req, time.Now().UTC())
	if err != nil {
		return Run{}, err
	}
	for _, r := range after {
		if err := admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
			return Run{}, fmt.Errorf("task recovery needs retained-state capacity: %w", err)
		}
	}
	store, supported := c.store.(TaskRecoveryStore)
	if !supported {
		return Run{}, errors.New("store cannot atomically recover a failed task generation")
	}
	receipt := Receipt{Key: req.RequestID, Kind: "recover_tasks", Args: args, RunID: owner.ID}
	if err := store.SaveTaskRecovery(c.ctx, family, after, &receipt); err != nil {
		return Run{}, err
	}
	for _, r := range after {
		c.runs[r.ID] = r
	}
	close(c.changed)
	c.changed = make(chan struct{})
	c.Wake()
	return c.outputRun(c.runs[owner.ID]), nil
}

func (c *Core) planTaskRecovery(req RecoverTasksRequest, now time.Time) ([]Run, error) {
	owner, exists := c.runs[req.RunID]
	if !exists {
		return nil, ErrNotFound
	}
	root, family, reserved, err := c.graphFamily(owner)
	if err != nil || reserved != 0 || owner.Status != "Failed" || root.Status != "Failed" || req.Revision < 1 || req.Revision != c.outputRun(owner).Revision || len(req.TaskIDs) == 0 || len(req.TaskIDs) > len(owner.TaskGraph.Children) || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 2048 {
		return nil, errors.New("task recovery needs the exact failed owning goal revision, current child IDs and a bounded maintenance reason")
	}
	current := map[string]bool{}
	var visit func(Run)
	visit = func(r Run) {
		current[r.ID] = true
		for _, link := range r.TaskGraph.Children {
			if !link.Retired {
				visit(c.runs[link.RunID])
			}
		}
	}
	visit(root)
	if !current[owner.ID] {
		return nil, errors.New("task recovery cannot reopen a retired owning generation")
	}
	for _, r := range family {
		if !r.Terminal() || r.Active() || !allAttemptsReleased(r) || !r.ResourcesReleased || r.CleanupError != "" || r.CleanupBlocked || current[r.ID] && r.CancelRequested || r.Decision != nil {
			return nil, errors.New("task recovery requires the whole family terminal, released and cleanup-clear without cancellation or pending approval")
		}
		for _, a := range r.Attempts {
			if a.State != Finished {
				return nil, errors.New("task recovery cannot replace unsettled historical execution")
			}
		}
	}
	selected, requested := map[string]bool{}, map[string]bool{}
	for _, id := range req.TaskIDs {
		if requested[id] {
			return nil, errors.New("task recovery IDs must be unique")
		}
		requested[id] = true
		for _, link := range owner.TaskGraph.Children {
			if !link.Retired && link.Spec.ID == id {
				task := c.runs[link.RunID]
				if task.Status != "Failed" || len(task.Attempts) == 0 || task.Step < 0 || task.Step >= len(task.Process.Steps) || task.TaskGraph.Phase == TaskWaitingInput {
					return nil, errors.New("selected task must have a settled failed execution and a valid unchanged role visit")
				}
				selected[task.ID] = true
			}
		}
	}
	if len(selected) != len(requested) {
		return nil, errors.New("task recovery IDs must name current direct children of the failed owner")
	}
	rearmed := Clone(selected)
	// A zero-attempt dependency failure is coordinator state, not an executed
	// judgment. Rearm only that causal closure; failed executions elsewhere and
	// every completed/accepted result remain untouched.
	for changed := true; changed; {
		changed = false
		for _, r := range family {
			if !current[r.ID] || rearmed[r.ID] || r.Status != "Failed" || len(r.Attempts) != 0 || len(r.Outputs) != 0 || len(r.TaskGraph.Children) != 0 || r.TaskGraph.Phase != "" {
				continue
			}
			caused, available := false, true
			for _, depID := range r.TaskGraph.DependsOn {
				dep, exists := c.runs[depID]
				if !exists || dep.TaskGraph == nil || dep.TaskGraph.ParentRunID != r.TaskGraph.ParentRunID {
					return nil, errors.New("task recovery dependency ownership differs")
				}
				caused = caused || rearmed[depID] && r.Reason == "declared task dependency did not complete: "+dep.TaskGraph.TaskID
				available = available && (dep.Status == "Completed" || rearmed[depID])
			}
			if caused && available {
				rearmed[r.ID], changed = true, true
			}
		}
	}
	parents := map[string]bool{}
	for id := range rearmed {
		for parentID := c.runs[id].TaskGraph.ParentRunID; parentID != ""; parentID = c.runs[parentID].TaskGraph.ParentRunID {
			parent := c.runs[parentID]
			if parent.Status != "Failed" {
				return nil, errors.New("owning task already accepted or remains active; recovery cannot invalidate its judgment")
			}
			parents[parentID] = true
		}
	}
	prospective := &Core{runs: map[string]Run{}}
	for _, r := range family {
		prospective.runs[r.ID] = Clone(r)
	}
	for id := range rearmed {
		r := prospective.runs[id]
		if selected[id] {
			r.Visit++
			r.VisitExtraAttempts = 0
			r.TaskGraph.Results = graphResultRefs(r)
		}
		r.TaskGraph.Phase, r.TaskGraph.Paused = "", false
		r.Outputs = nil
		r.Status, r.Reason, r.ResourcesReleased = "Queued", "owner-authorized maintenance recovery of the same task generation", false
		prospective.runs[id] = r
	}
	for id := range parents {
		r := prospective.runs[id]
		r.TaskGraph.Phase, r.TaskGraph.Paused = TaskWaitingChildren, false
		kept := r.TaskGraph.Results[:0]
		for _, result := range r.TaskGraph.Results {
			if !rearmed[result.RunID] && !parents[result.RunID] {
				kept = append(kept, result)
			}
		}
		r.TaskGraph.Results = kept
		r.Outputs = nil
		r.Status, r.Reason, r.ResourcesReleased = "Waiting", "waiting for maintenance-recovered role tasks", false
		prospective.runs[id] = r
	}
	if err := prospective.graphReserve(prospective.runs[owner.ID], 0, true); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rearmed)+len(parents))
	for id := range rearmed {
		ids = append(ids, id)
	}
	for id := range parents {
		if !rearmed[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	audit := TaskRecoveryAudit{RequestID: req.RequestID, OwnerID: owner.ID, TaskIDs: Clone(req.TaskIDs), Rearmed: ids,
		Spent: c.familyAttempts(owner), Reason: req.Reason, GrantedAt: now}
	r := prospective.runs[root.ID]
	r.TaskGraph.Recoveries = append(r.TaskGraph.Recoveries, audit)
	prospective.runs[root.ID] = r
	after := make([]Run, 0, len(ids))
	for _, id := range ids {
		r := prospective.runs[id]
		r.Revision++
		r.Updated = now
		after = append(after, r)
	}
	return after, nil
}

// ValidateTaskRecovery rederives the exact state transition from transactionally
// loaded family facts. A caller cannot smuggle a policy/result edit into recovery.
func ValidateTaskRecovery(before, after []Run, receipt *Receipt) error {
	if len(before) == 0 || len(after) == 0 || receipt == nil || receipt.Kind != "recover_tasks" {
		return errors.New("invalid atomic task recovery")
	}
	var req RecoverTasksRequest
	if json.Unmarshal(receipt.Args, &req) != nil || req.RequestID != receipt.Key || req.RunID != receipt.RunID {
		return errors.New("task recovery receipt differs from request")
	}
	view := &Core{runs: map[string]Run{}}
	for _, r := range before {
		if view.runs[r.ID].ID != "" {
			return errors.New("duplicate task recovery family member")
		}
		view.runs[r.ID] = r
	}
	root, family, reserved, err := view.graphFamily(view.runs[req.RunID])
	if err != nil || len(family) != len(before) || reserved != 0 {
		return errors.New("task recovery omitted owned family state")
	}
	var latest *TaskRecoveryAudit
	for _, r := range after {
		if r.ID == root.ID && r.TaskGraph != nil && len(r.TaskGraph.Recoveries) != 0 {
			latest = &r.TaskGraph.Recoveries[len(r.TaskGraph.Recoveries)-1]
		}
	}
	if latest == nil || latest.GrantedAt.IsZero() {
		return errors.New("task recovery immutable audit is missing")
	}
	want, err := view.planTaskRecovery(req, latest.GrantedAt)
	if err != nil || !reflect.DeepEqual(want, after) {
		return errors.New("task recovery differs from its exact bounded failed-generation transition")
	}
	return nil
}

func ValidateTaskRecoveryAudit(before, after []TaskRecoveryAudit, allowed bool) error {
	if reflect.DeepEqual(before, after) {
		return nil
	}
	if !allowed || len(after) != len(before)+1 || !slices.EqualFunc(before, after[:len(before)], func(a, b TaskRecoveryAudit) bool { return reflect.DeepEqual(a, b) }) {
		return errors.New("task recovery audit cannot change outside its atomic owner operation")
	}
	return nil
}

func taskRecoveryRecord(r Run) error {
	if r.TaskGraph == nil || len(r.TaskGraph.Recoveries) == 0 {
		return nil
	}
	if r.TaskGraph.RootRunID != r.ID || r.TaskGraph.ParentRunID != "" {
		return errors.New("task maintenance audit belongs to the retained root")
	}
	seen := map[string]bool{}
	for _, audit := range r.TaskGraph.Recoveries {
		if audit.RequestID == "" || seen[audit.RequestID] || audit.OwnerID == "" || len(audit.TaskIDs) == 0 || len(audit.Rearmed) == 0 || audit.Spent < 1 || audit.Spent >= r.Process.MaxAttempts || strings.TrimSpace(audit.Reason) == "" || len(audit.Reason) > 2048 || audit.GrantedAt.IsZero() {
			return errors.New("invalid retained task maintenance audit")
		}
		seen[audit.RequestID] = true
	}
	return nil
}
