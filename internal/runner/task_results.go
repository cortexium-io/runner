package runner

import "time"

func taskParentID(r Run) string {
	if r.TaskGraph != nil {
		return r.TaskGraph.ParentRunID
	}
	if r.Task != nil {
		return r.Task.Identity.RootRunID
	}
	return ""
}

func (c *Core) outputRun(r Run) Run {
	r = Clone(r)
	if r.TaskGraph != nil {
		c.outputGraph(&r, r, false)
		return r
	}
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
					status.Reconciling = a.Recovering(time.Now())
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

func (c *Core) outputGraph(out *Run, parent Run, retired bool) {
	for _, link := range parent.TaskGraph.Children {
		status := TaskStatus{
			ID:          link.Spec.ID,
			RunID:       link.RunID,
			ParentRunID: parent.ID,
			Role:        link.Spec.Role,
			Retired:     retired || link.Retired,
			DependsOn:   Clone(link.Spec.DependsOn),
			Status:      "Waiting",
			Reason:      "waiting for retained child creation",
		}
		child, exists := c.runs[link.RunID]
		if exists {
			out.Revision += child.Revision
			if child.Updated.After(out.Updated) {
				out.Updated = child.Updated
			}
			status.Status, status.Reason, status.Attempts = child.Status, child.Reason, len(child.Attempts)
			progress := runProgress(child, time.Now())
			if status.Retired {
				progress.Attention = nil
			}
			status.Progress = &progress
			status.Active = progress.Active > 0
			status.Unsettled = progress.Active+progress.Pending+progress.Reconciling+progress.Unknown > 0
			status.CleanupPending = implementsRunResources(c.executor) && !child.ResourcesReleased
			status.CleanupError = child.CleanupError
			for _, attempt := range child.Attempts {
				usage := Clone(attempt.Usage)
				profile, err := child.Roles[child.Process.Steps[attempt.Step].Role].Profile(attempt.Profile)
				if err == nil && profile.Harness != "codex" && profile.Harness != "native" {
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
				out.TaskUsage = append(out.TaskUsage, usage)
			}
		}
		out.TaskStatuses = append(out.TaskStatuses, status)
		if exists && child.TaskGraph != nil {
			c.outputGraph(out, child, status.Retired)
		}
	}
}
