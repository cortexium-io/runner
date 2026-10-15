package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
)

// Eligibility is shared by inspection and dispatch. The store rechecks capacity,
// ownership, revision and allowance reservation in the committing transaction.
type Eligibility struct {
	Role                 string       `json:"role,omitempty"`
	ProfileName          string       `json:"profile_name,omitempty"`
	Profile              core.Profile `json:"profile"`
	Reason               string       `json:"reason,omitempty"`
	WaitKind             string       `json:"wait_kind,omitempty"`
	WaitingFor           string       `json:"waiting_for,omitempty"`
	RetryAt              *time.Time   `json:"retry_at,omitempty"`
	RefreshesRemaining   int          `json:"base_refreshes_remaining"`
	RepairsRemaining     int          `json:"repairs_remaining"`
	RetriesRemaining     int          `json:"transient_retries_remaining"`
	InvocationsRemaining *int         `json:"invocations_remaining,omitempty"`
}

func workspaceClaim(c config.Config, t core.Task) string {
	return filepath.Join(c.WorktreeDir, t.WorkspaceKey())
}

func eligible(c config.Config, t core.Task, attempts []core.Attempt, drained bool, now time.Time, tasks ...core.Task) Eligibility {
	p := c.Policy
	v := Eligibility{RefreshesRemaining: max(0, p.MaxBaseRefreshes-t.Usage.BaseRefreshes), Role: t.NextRole, RepairsRemaining: max(0, p.MaxRepairs-t.Usage.Repairs), RetriesRemaining: p.MaxTransientRetries}
	if p.MaxInvocationsPerTask != nil {
		left := max(0, *p.MaxInvocationsPerTask-t.Usage.Invocations)
		v.InvocationsRemaining = &left
	}
	v.ProfileName, v.Profile = c.Role(t.NextRole)
	if t.NextRole == "implementer" {
		v.ProfileName, v.Profile = c.Implementer(t.Usage.Escalations)
	}
	v.Reason = "initial implementation or independent review"
	if t.NextRole == "refresh" {
		v.Reason = "deterministic base inspection and pinned checks; independent review follows a changed candidate"
	}
	if core.PlanningRole(t.NextRole) {
		v.Reason = "authorized planning or missing verification preparation"
	}
	if q := t.Pending; q != nil {
		v.Reason = q.Reason
		switch q.Kind {
		case "repair":
			v.ProfileName, v.Profile = c.Implementer(t.Usage.Escalations + 1)
		case "retry", "resume":
			v.ProfileName, v.Profile = q.ProfileName, q.Profile
			v.RetriesRemaining = max(0, p.MaxTransientRetries-q.Retry)
			if q.Kind == "retry" {
				v.RetriesRemaining = max(0, p.MaxTransientRetries-q.Retry+1)
				deadline := q.NotBefore
				v.RetryAt = &deadline
			}
		}
	}
	occupied := 0
	for _, a := range attempts {
		if a.TaskID == t.ID && a.Version == t.Current().Number && (a.Occupying || t.Stage == core.Stopped || (t.Attention != nil && t.Attention.Attempt == a.ID)) {
			v.RetriesRemaining = max(0, p.MaxTransientRetries-a.Retry)
		}
		if !a.Occupying {
			continue
		}
		occupied++
		if a.TaskID == t.ID || a.WorkspaceClaim == workspaceClaim(c, t) {
			v.WaitKind = "ownership"
			v.WaitingFor = "owned task/workspace: attempt " + a.ID
		}
	}
	if v.WaitingFor != "" {
		return v
	}
	switch {
	case t.Attention != nil:
		v.WaitKind = "attention"
		v.WaitingFor = string(t.Attention.Kind) + ": " + t.Attention.Detail
	case t.Approval == nil || t.Stage == core.Stopped:
		v.WaitKind = "approval"
		v.WaitingFor = "task approval"
	case t.NextRole == "":
		v.WaitKind = "idle"
		v.WaitingFor = "no pending invocation"
	case core.PrerequisiteWait(t, tasks) != "":
		v.WaitKind = "dependency"
		v.WaitingFor = core.PrerequisiteWait(t, tasks)
	case core.PlanningRole(t.NextRole) && v.ProfileName == "":
		v.WaitKind = "configuration"
		v.WaitingFor = "configure and apply a planner profile"
	case t.Usage.Repairs > p.MaxRepairs && (t.Pending == nil || t.Pending.Kind != "resume"):
		v.WaitKind = "repair_limit"
		v.WaitingFor = "repair limit below consumed allowance; explicitly apply more allowance before automatic work"
	case t.Pending != nil && (t.Pending.Kind == "repair" || t.Pending.Kind == "ci_repair" || t.Pending.Kind == "conflict_repair") && t.Usage.Repairs >= p.MaxRepairs:
		v.WaitKind = "repair_limit"
		v.WaitingFor = "repair limit; inspect findings and explicitly apply more allowance or replace the task"
	case t.Pending != nil && t.Pending.Kind == "retry" && t.Pending.Retry > p.MaxTransientRetries:
		v.WaitKind = "retry_limit"
		v.WaitingFor = "transient retry limit; inspect failure and explicitly apply more allowance"
	case t.NextRole != "refresh" && v.InvocationsRemaining != nil && *v.InvocationsRemaining == 0:
		v.WaitKind = "invocation_limit"
		v.WaitingFor = "task invocation ceiling; explicitly apply more allowance"
	case drained:
		v.WaitKind = "drained"
		v.WaitingFor = "coordinator drained"
	case occupied >= p.MaxParallelism:
		v.WaitKind = "capacity"
		v.WaitingFor = fmt.Sprintf("global capacity: %d/%d occupied", occupied, p.MaxParallelism)
	case v.RetryAt != nil && now.Before(*v.RetryAt):
		v.WaitKind = "backoff"
		v.WaitingFor = "retry backoff until " + v.RetryAt.UTC().Format(time.RFC3339)
	}
	return v
}

func retryDelay(number int) time.Duration {
	if number == 1 {
		return 30 * time.Second
	}
	if number == 2 {
		return 2 * time.Minute
	}
	return 5 * time.Minute
}

type PolicyChange struct {
	MergePolicyBefore string             `json:"merge_policy_before"`
	Publications      []core.Publication `json:"active_publications"`
	Preview           string             `json:"apply_preview"`
	AppliedRevision   string             `json:"applied_revision"`
	Proposed          config.Config      `json:"proposed"`
	Active            []core.Attempt     `json:"active_pinned_attempts"`
	Tasks             []PolicyEffect     `json:"task_effects"`
	Warnings          []string           `json:"warnings"`
}
type PolicyEffect struct {
	TaskID string      `json:"task_id"`
	Usage  core.Usage  `json:"consumed"`
	Before Eligibility `json:"before"`
	After  Eligibility `json:"after"`
}

func (e *Engine) PreviewPolicy(ctx context.Context, proposed config.Config) (PolicyChange, error) {
	current, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return PolicyChange{}, err
	}
	next := current
	next.Config = proposed
	if err = proposed.Validate(); err != nil {
		return PolicyChange{}, err
	}
	if !config.SameAuthority(current, next) {
		return PolicyChange{}, fmt.Errorf("only execution/merge policy and profiles can change; repository and quality authority must remain unchanged")
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return PolicyChange{}, err
	}
	publications, err := e.Store.Publications(ctx, "")
	if err != nil {
		return PolicyChange{}, err
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return PolicyChange{}, err
	}
	result := PolicyChange{MergePolicyBefore: current.Config.MergePolicy(), Preview: core.Digest([]string{current.Config.Revision(), proposed.Revision()}), AppliedRevision: current.Config.Revision(), Proposed: proposed, Active: []core.Attempt{}, Tasks: []PolicyEffect{}, Warnings: []string{}}
	if current.Config.MergePolicy() != proposed.MergePolicy() {
		if proposed.MergePolicy() == "automatic" {
			result.Warnings = append(result.Warnings, "Applying automatic merge authorizes existing approved work and future approved work in this repository/base. A ready PR observation may trigger one guarded merge; task approval and every proof remain required.")
		} else {
			result.Warnings = append(result.Warnings, "Applying human merge prevents unsent automatic writes. Explicit task merge requests remain authorized; already-dispatched merges can still complete through inspection.")
		}
	}
	for _, p := range publications {
		if p.Occupying {
			result.Publications = append(result.Publications, p)
		}
	}
	for _, a := range attempts {
		if a.Occupying {
			result.Active = append(result.Active, a)
		}
	}
	now := e.now()
	for _, t := range tasks {
		t = ciRepairTask(t, publications)
		after := publicationWait(eligible(proposed, t, attempts, drained, now, tasks...), proposed, t, attempts, publications)
		before := publicationWait(eligible(current.Config, t, attempts, drained, now, tasks...), current.Config, t, attempts, publications)
		result.Tasks = append(result.Tasks, PolicyEffect{t.ID, t.Usage, before, after})
		minimum := 0
		switch t.NextRole {
		case "plan", "audit", "verify":
			minimum = 1
		case "implementer":
			minimum = 2
		case "prepare":
			minimum = 3
		}
		for _, a := range attempts {
			if a.TaskID == t.ID && (a.Occupying || a.State == "completed") && a.Authorized(t) {
				minimum = max(0, minimum-1)
				break
			}
		}
		if after.InvocationsRemaining != nil && *after.InvocationsRemaining < minimum {
			result.Warnings = append(result.Warnings, fmt.Sprintf("Task %s needs at least %d further invocations for its pending journey, but only %d remain; use config edit to inspect the invocation ceiling.", t.ID, minimum, *after.InvocationsRemaining))
		}
	}
	if len(proposed.Policy.ImplementationLadder) > proposed.Policy.MaxRepairs {
		result.Warnings = append(result.Warnings, "Some escalation steps are unreachable within the repair limit.")
	}
	if proposed.Policy.MaxInvocationsPerTask != nil && *proposed.Policy.MaxInvocationsPerTask < 2 {
		result.Warnings = append(result.Warnings, "The invocation ceiling cannot admit both initial implementation and independent audit.")
	}
	return result, nil
}
