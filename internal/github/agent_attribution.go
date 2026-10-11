package github

import (
	"strings"

	"github.com/cortexium-io/runner/internal/config"
)

// SetAgentAttributionReader attaches a projection of recorded execution settings.
// The returned Runner-owned metadata is informational and grants no authority.
func (s *Project) SetAgentAttributionReader(reader func(WorkItem) string) {
	s.agentAttribution = reader
}

func (s *Project) agentAttributionUpdates(item WorkItem) []projectFieldUpdate {
	field, ok := s.currentSchema().field(config.RunnerAgentsFieldName)
	if s.agentAttribution == nil || !ok || !projectFieldHasDataType(field, "TEXT") {
		return nil // Existing Projects keep working until init/doctor --fix provisions the field.
	}
	attribution := canonicalProjectResult(strings.TrimSpace(s.agentAttribution(item)))
	if attribution == "" {
		return nil
	}
	return []projectFieldUpdate{textProjectField(config.RunnerAgentsFieldName, attribution)}
}
