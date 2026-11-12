package runner

import "time"

func taskParentID(r Run) string {
	if r.Task != nil {
		return r.Task.Identity.RootRunID
	}
	return ""
}

func (c *Core) outputRun(r Run) Run {
	r = Clone(r)
	if r.Task != nil {
		return r
	}
	for _, step := range r.Process.Steps {
		group := r.TaskGroups[step.ID]
		if group == nil {
			continue
		}
		for i, reservation := range group.Children {
			status := TaskStatus{
				ID: reservation.ItemID, RunID: reservation.RunID,
				Status: "Waiting",
				Reason: "waiting for retained child creation",
			}
			if i < len(group.Items) {
				status.DependsOn = group.Items[i].DependsOn
			}
			if child, exists := c.runs[reservation.RunID]; exists {
				r.Revision += child.Revision
				if child.Updated.After(r.Updated) {
					r.Updated = child.Updated
				}
				status.Status, status.Reason = child.Status, child.Reason
				status.Attempts = len(child.Attempts)
				progress := runProgress(child, time.Now())
				status.Progress = &progress
				status.Active = progress.Active > 0
				status.Unsettled = progress.Active+progress.Reconciling+progress.Unknown+progress.Pending > 0
				_, managesResources := c.executor.(RunResources)
				status.CleanupPending = managesResources && !child.ResourcesReleased
				status.CleanupError = child.CleanupError
				if a := child.Current(); a != nil {
					status.Reconciling = a.State == Unknown && a.Reconciling && a.Deadline.After(time.Now())
				}
				for _, attempt := range child.Attempts {
					usage := Clone(attempt.Usage)
					profile, err := child.Roles[child.Process.Steps[attempt.Step].Role].Profile(attempt.Profile)
					if err == nil && profile.Harness != "codex" {
						zero := int64(0)
						if usage.InputTokens == nil {
							usage.InputTokens = &zero
						}
						if usage.OutputTokens == nil {
							usage.OutputTokens = &zero
						}
						if usage.CachedInputTokens == nil {
							usage.CachedInputTokens = &zero
						}
					}
					r.TaskUsage = append(r.TaskUsage, usage)
				}
			}
			r.TaskStatuses = append(r.TaskStatuses, status)
		}
	}
	return r
}
