package metrics

import (
	"context"
	"time"
)

// HarnessActivity summarizes observed provider events, not CPU time or proof
// that commands/tests succeeded. No names, IDs, arguments, or output survive.
type HarnessActivity struct {
	Coverage         string    `json:"coverage"`
	Events           int       `json:"events"`
	ToolsStarted     int       `json:"tools_started"`
	ToolsCompleted   int       `json:"tools_completed"`
	ToolMilliseconds int64     `json:"tool_milliseconds"`
	LastEventAt      time.Time `json:"last_event_at,omitempty"`
	LastEventKind    string    `json:"last_event_kind,omitempty"`
	ActiveTools      int       `json:"active_tools"`
	OldestActiveAt   time.Time `json:"oldest_active_at,omitempty"`
}

func validHarnessActivity(a *HarnessActivity) bool {
	if a == nil {
		return true
	}
	if a.Coverage != "observed" && a.Coverage != "partial" && a.Coverage != "unavailable" {
		return false
	}
	if a.Events < 0 || a.ToolsStarted < 0 || a.ToolsCompleted < 0 || a.ActiveTools < 0 || a.ActiveTools > 256 || a.ToolMilliseconds < 0 {
		return false
	}
	switch a.LastEventKind {
	case "", "turn", "message", "shell_started", "shell_completed", "tool_started", "tool_completed", "other":
		return true
	default:
		return false
	}
}

func harnessStage(name string) bool {
	switch name {
	case StageHarnessRun, StagePlannerOutline, StagePlannerDetails, StageReviewerAudit, StageReviewerVerify:
		return true
	default:
		return false
	}
}

func validActivityEvent(event Event) bool {
	return event.HarnessActivity == nil || (event.Kind == EventStageCompleted && harnessStage(event.Stage) && validHarnessActivity(event.HarnessActivity))
}

func RecordHarnessActivity(ctx context.Context, activity HarnessActivity) {
	trace, _ := ctx.Value(traceContextKey{}).(*AttemptTrace)
	if trace == nil || !validHarnessActivity(&activity) {
		return
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.activity = &activity
}
