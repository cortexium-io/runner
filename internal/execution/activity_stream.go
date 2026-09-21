package execution

import (
	"encoding/json"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

// Times are receipt times at Runner, not invented provider/CPU durations.
// Only bounded in-flight IDs are held in memory; none enter the metrics store.
type activityStream struct {
	summary metrics.HarnessActivity
	active  map[string]time.Time
}

func (s *activityStream) consume(kind string, line []byte, now time.Time) {
	if kind != config.HarnessCodexCLI && kind != config.HarnessPiCLI {
		return
	}
	var event struct {
		Type       string `json:"type"`
		ToolCallID string `json:"toolCallId"`
		Item       struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(line, &event) != nil {
		s.summary.Coverage = "partial"
		return
	}
	s.summary.Events++
	s.summary.LastEventAt = now
	s.summary.LastEventKind = "other"
	if s.summary.Coverage == "" {
		s.summary.Coverage = "observed"
	}
	var id string
	var started, completed, shell bool
	if kind == config.HarnessCodexCLI {
		switch event.Type {
		case "turn.started", "turn.completed", "turn.failed":
			s.summary.LastEventKind = "turn"
		case "item.started", "item.completed":
			switch event.Item.Type {
			case "command_execution", "mcp_tool_call":
				id, started, completed, shell = event.Item.ID, event.Type == "item.started", event.Type == "item.completed", event.Item.Type == "command_execution"
			case "agent_message":
				s.summary.LastEventKind = "message"
			}
		}
	} else {
		switch event.Type {
		case "tool_execution_start", "tool_execution_end":
			id, started, completed = event.ToolCallID, event.Type == "tool_execution_start", event.Type == "tool_execution_end"
		case "message_end":
			s.summary.LastEventKind = "message"
		case "agent_start", "agent_end":
			s.summary.LastEventKind = "turn"
		}
	}
	if !started && !completed {
		return
	}
	category := "tool"
	if shell {
		category = "shell"
	}
	if started {
		s.summary.LastEventKind = category + "_started"
	} else {
		s.summary.LastEventKind = category + "_completed"
	}
	if id == "" || len(id) > 256 {
		s.summary.Coverage = "partial"
		return
	}
	if s.active == nil {
		s.active = map[string]time.Time{}
	}
	if started {
		if _, exists := s.active[id]; exists {
			return
		}
		s.summary.ToolsStarted++
		if len(s.active) >= 256 {
			s.summary.Coverage = "partial"
			return
		}
		s.active[id] = now
	} else if start, exists := s.active[id]; exists {
		s.summary.ToolsCompleted++
		s.summary.ToolMilliseconds += max(0, now.Sub(start).Milliseconds())
		delete(s.active, id)
	} else {
		s.summary.Coverage = "partial"
	}
}

func (s *activityStream) finish() metrics.HarnessActivity {
	if s.summary.Coverage == "" {
		s.summary.Coverage = "unavailable"
	}
	s.summary.ActiveTools = len(s.active)
	for _, started := range s.active {
		if s.summary.OldestActiveAt.IsZero() || started.Before(s.summary.OldestActiveAt) {
			s.summary.OldestActiveAt = started
		}
	}
	return s.summary
}
