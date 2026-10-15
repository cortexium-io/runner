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
	WaitingFor           string       `json:"waiting_for,omitempty"`
	RetryAt              *time.Time   `json:"retry_at,omitempty"`
	RepairsRemaining     int          `json:"repairs_remaining"`
	RetriesRemaining     int          `json:"transient_retries_remaining"`
	InvocationsRemaining *int         `json:"invocations_remaining,omitempty"`
}

func workspaceClaim(c config.Config, t core.Task) string {
	return filepath.Join(c.WorktreeDir, fmt.Sprintf("%s-v%d", t.ID, t.Current().Number))
}

func eligible(c config.Config, t core.Task, attempts []core.Attempt, drained bool, now time.Time) Eligibility {
	p := c.Policy
	v := Eligibility{Role: t.NextRole, RepairsRemaining: max(0, p.MaxRepairs-t.Usage.Repairs), RetriesRemaining: p.MaxTransientRetries}
	if p.MaxInvocationsPerTask != nil {
		left := max(0, *p.MaxInvocationsPerTask-t.Usage.Invocations)
		v.InvocationsRemaining = &left
	}
	v.ProfileName, v.Profile = c.Role(t.NextRole)
	if t.NextRole == "implementer" {
		v.ProfileName, v.Profile = c.Implementer(t.Usage.Escalations)
	}
	v.Reason = "initial implementation or independent review"
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
			v.WaitingFor = "owned task/workspace: attempt " + a.ID
		}
	}
	if v.WaitingFor != "" {
		return v
	}
	switch {
	case t.Attention != nil:
		v.WaitingFor = string(t.Attention.Kind) + ": " + t.Attention.Detail
	case t.Approval == nil || t.Stage == core.Stopped:
		v.WaitingFor = "task approval"
	case t.NextRole == "":
		v.WaitingFor = "no pending invocation"
	case t.Usage.Repairs > p.MaxRepairs && (t.Pending == nil || t.Pending.Kind != "resume"):
		v.WaitingFor = "repair limit below consumed allowance; explicitly apply more allowance before automatic work"
	case t.Pending != nil && t.Pending.Kind == "repair" && t.Usage.Repairs >= p.MaxRepairs:
		v.WaitingFor = "repair limit; inspect findings and explicitly apply more allowance or replace the task"
	case t.Pending != nil && t.Pending.Kind == "retry" && t.Pending.Retry > p.MaxTransientRetries:
		v.WaitingFor = "transient retry limit; inspect failure and explicitly apply more allowance"
	case v.InvocationsRemaining != nil && *v.InvocationsRemaining == 0:
		v.WaitingFor = "task invocation ceiling; explicitly apply more allowance"
	case drained:
		v.WaitingFor = "coordinator drained"
	case occupied >= p.MaxParallelism:
		v.WaitingFor = fmt.Sprintf("global capacity: %d/%d occupied", occupied, p.MaxParallelism)
	case v.RetryAt != nil && now.Before(*v.RetryAt):
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
	Preview         string         `json:"apply_preview"`
	AppliedRevision string         `json:"applied_revision"`
	Proposed        config.Config  `json:"proposed"`
	Active          []core.Attempt `json:"active_pinned_attempts"`
	Tasks           []PolicyEffect `json:"task_effects"`
	Warnings        []string       `json:"warnings"`
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
		return PolicyChange{}, fmt.Errorf("only execution policy and profiles can change; repository and quality authority must remain unchanged")
	}
	attempts, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return PolicyChange{}, err
	}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return PolicyChange{}, err
	}
	result := PolicyChange{Preview: core.Digest([]string{current.Config.Revision(), proposed.Revision()}), AppliedRevision: current.Config.Revision(), Proposed: proposed, Active: []core.Attempt{}, Tasks: []PolicyEffect{}, Warnings: []string{}}
	for _, a := range attempts {
		if a.Occupying {
			result.Active = append(result.Active, a)
		}
	}
	now := e.now()
	for _, t := range tasks {
		result.Tasks = append(result.Tasks, PolicyEffect{t.ID, t.Usage, eligible(current.Config, t, attempts, drained, now), eligible(proposed, t, attempts, drained, now)})
	}
	if len(proposed.Policy.ImplementationLadder) > proposed.Policy.MaxRepairs {
		result.Warnings = append(result.Warnings, "Some escalation steps are unreachable within the repair limit.")
	}
	if proposed.Policy.MaxInvocationsPerTask != nil && *proposed.Policy.MaxInvocationsPerTask < 2 {
		result.Warnings = append(result.Warnings, "The invocation ceiling cannot admit both initial implementation and independent audit.")
	}
	return result, nil
}
