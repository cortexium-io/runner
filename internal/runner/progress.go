package runner

import "time"

// Progress is a bounded read-only projection, not a scheduler or heartbeat.
// LastProgressAt is the latest saved lifecycle milestone. Updated may also
// change on reconciliation polls and must not be relabeled as useful progress.
type Progress struct {
	Execution      string          `json:"execution"`
	Active         int             `json:"active_attempts"`
	Reconciling    int             `json:"reconciling_attempts"`
	Unknown        int             `json:"unknown_attempts"`
	Pending        int             `json:"pending_attempts"`
	LastProgressAt time.Time       `json:"last_progress_at,omitzero"`
	ProgressBasis  string          `json:"progress_basis"`
	Attempts       []AttemptTiming `json:"attempt_timing,omitempty"`
	Attention      []Attention     `json:"attention,omitempty"`
	NextAction     string          `json:"next_action"`
	Truncated      bool            `json:"truncated,omitempty"`
}

type AttemptTiming struct {
	RunID           string         `json:"run_id"`
	AttemptID       string         `json:"attempt_id"`
	State           ExecutionState `json:"state"`
	Started         time.Time      `json:"started,omitzero"`
	Deadline        time.Time      `json:"deadline,omitzero"`
	DeadlineKnown   bool           `json:"deadline_known"`
	DeadlineElapsed bool           `json:"deadline_elapsed,omitempty"`
	NextCheck       time.Time      `json:"next_check,omitzero"`
	StopRequested   bool           `json:"stop_requested,omitempty"`
}

type Attention struct {
	RunID      string `json:"run_id"`
	Kind       string `json:"kind"`
	DecisionID string `json:"decision_id,omitempty"`
}

const maxProgressFacts = 9 // One root and at most eight simultaneously owned cards.

func (p *Progress) milestone(at time.Time) {
	if at.After(p.LastProgressAt) {
		p.LastProgressAt = at
	}
}

func (p *Progress) attention(a Attention) {
	for _, old := range p.Attention {
		if old == a {
			return
		}
	}
	if a.Kind == "human_decision" {
		p.Attention = append([]Attention{a}, p.Attention...)
		if len(p.Attention) > maxProgressFacts {
			p.Attention, p.Truncated = p.Attention[:maxProgressFacts], true
		}
		return
	}
	if len(p.Attention) < maxProgressFacts {
		p.Attention = append(p.Attention, a)
	} else {
		p.Truncated = true
	}
}

func (p *Progress) timing(a AttemptTiming) {
	if len(p.Attempts) < maxProgressFacts {
		p.Attempts = append(p.Attempts, a)
	} else {
		p.Truncated = true
	}
}

func ownProgress(r Run, now time.Time) Progress {
	p := Progress{}
	p.milestone(r.Created)
	for _, group := range r.TaskGroups {
		if group != nil {
			p.milestone(group.Started)
		}
	}
	for _, a := range r.Attempts {
		p.milestone(a.Started)
		p.milestone(a.FinishedAt)
		if a.State == Finished {
			continue
		}
		known := !a.Deadline.IsZero()
		elapsed := known && !a.Deadline.After(now)
		state := a.State
		if state != Running && state != NotStarted && state != Unknown {
			state = Unknown
		}
		p.timing(AttemptTiming{
			RunID:           r.ID,
			AttemptID:       a.ID,
			State:           state,
			Started:         a.Started,
			Deadline:        a.Deadline,
			DeadlineKnown:   known,
			DeadlineElapsed: elapsed,
			NextCheck:       a.NextCheck,
			StopRequested:   a.StopRequested,
		})
		switch a.State {
		case Running:
			p.Active++
		case NotStarted:
			p.Pending++
		case Unknown:
			if a.Reconciling && known && !elapsed {
				p.Reconciling++
			} else {
				p.Unknown++
			}
		default:
			p.Unknown++
		}
		if !known {
			p.attention(Attention{RunID: r.ID, Kind: "timing_unknown"})
		} else if elapsed {
			p.attention(Attention{RunID: r.ID, Kind: "deadline_elapsed"})
		}
	}
	if r.Decision != nil {
		// Independent of execution: another child can still be running.
		p.attention(Attention{RunID: r.ID, Kind: "human_decision", DecisionID: r.Decision.ID})
	}
	return p
}

func (p *Progress) finish(r Run) {
	p.ProgressBasis = "unknown"
	if !p.LastProgressAt.IsZero() {
		p.ProgressBasis = "saved_lifecycle_milestone"
	}
	p.Execution = "idle"
	switch {
	case p.Unknown > 0:
		p.Execution = "unknown"
	case p.Active > 0:
		p.Execution = "active"
	case p.Reconciling > 0:
		p.Execution = "reconciling"
	case p.Pending > 0:
		p.Execution = "pending"
	}
	prerequisite := len(r.Attempts) == 0 && r.Reason == "waiting for declared task prerequisites"
	if r.CleanupError != "" || r.CleanupBlocked {
		p.attention(Attention{RunID: r.ID, Kind: "cleanup_blocked"})
	} else if r.Status == "Failed" || r.Status == "Waiting" && r.Decision == nil && !prerequisite && p.Reconciling == 0 && p.Unknown == 0 {
		p.attention(Attention{RunID: r.ID, Kind: "blocked"})
	}
	p.NextAction = "none"
	if p.Active+p.Reconciling > 0 {
		p.NextAction = "wait_for_execution"
	} else if p.Pending > 0 || r.Status == "Queued" {
		p.NextAction = "wait_for_dispatch"
	}
	if p.Unknown > 0 {
		p.NextAction = "reconcile_execution"
	}
	for _, fact := range p.Attention {
		switch fact.Kind {
		case "blocked", "cleanup_blocked", "timing_unknown":
			p.NextAction = "inspect_blocker"
		case "deadline_elapsed":
			p.NextAction = "wait_for_settlement"
		}
	}
	for _, fact := range p.Attention {
		if fact.Kind == "human_decision" {
			p.NextAction = "decide_saved_approval"
			break
		}
	}
}

func runProgress(r Run, now time.Time) Progress {
	p := ownProgress(r, now)
	p.finish(r)
	return p
}

// Summarize shares one accounting/progress contract across status, wait and
// compact result. It reads no diagnostics or external observer/sink state.
func Summarize(r Run, now time.Time) Summary {
	out := Summary{
		ID:          r.ID,
		Project:     r.ProjectID,
		Process:     r.Process.ID,
		Status:      r.Status,
		Reason:      r.Reason,
		Step:        r.Step,
		Attempts:    len(r.Attempts),
		Revision:    r.Revision,
		Updated:     r.Updated,
		ParentRunID: taskParentID(r),
		Progress:    ownProgress(r, now),
	}
	p := &out.Progress
	for _, task := range r.TaskStatuses {
		out.Attempts += task.Attempts
		child := task.Progress
		if child == nil {
			if task.Active || task.Unsettled || task.Reconciling {
				p.Unknown++ // Legacy/missing detail cannot prove running or a deadline.
				p.attention(Attention{RunID: task.RunID, Kind: "timing_unknown"})
			}
			continue
		}
		p.Active += child.Active
		p.Reconciling += child.Reconciling
		p.Unknown += child.Unknown
		p.Pending += child.Pending
		p.milestone(child.LastProgressAt)
		p.Truncated = p.Truncated || child.Truncated
		for _, a := range child.Attempts {
			p.timing(a)
		}
		for _, a := range child.Attention {
			p.attention(a)
		}
	}
	p.finish(r)
	return out
}
