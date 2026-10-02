package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/presentation"
)

func (s *Engine) cardAgentAttribution(item github.WorkItem) string {
	if s.readMetricsHistory == nil {
		return ""
	}
	history, err := s.readMetricsHistory()
	if err != nil {
		return "Agent attribution unavailable; inspect local metrics."
	}
	type contribution struct {
		at               time.Time
		attemptID        string
		model, reasoning string
	}
	latest := map[string]contribution{}
	for _, attempt := range history.Attempts {
		if attempt.RunnerID != s.cfg.RunnerID || !strings.EqualFold(attempt.ProjectOwner, s.cfg.GitHubProject.Owner) || attempt.ProjectNumber != s.cfg.GitHubProject.Number || attempt.IsRunnerObservation() {
			continue
		}
		own := attempt.ItemID == item.ID && item.ID != ""
		parent := attempt.ItemID == item.PlanningSourceID && item.PlanningSourceID != ""
		if !own && !parent {
			continue
		}
		for _, stage := range attempt.Stages {
			role := attempt.RoleContract
			if role == "" {
				role = attempt.Role // Preserve historical role names; never infer from current config.
			}
			part := agentWorkPart(role, stage.Name)
			if part == "" || parent && part != "Planning" {
				continue
			}
			previous, exists := latest[part]
			if !exists || stage.StartedAt.After(previous.at) || stage.StartedAt.Equal(previous.at) && attempt.AttemptID > previous.attemptID {
				latest[part] = contribution{stage.StartedAt, attempt.AttemptID, attempt.Model, attempt.Reasoning}
			}
		}
	}
	if len(latest) == 0 {
		return ""
	}
	parts := make([]string, 0, len(latest))
	for part := range latest {
		parts = append(parts, part)
	}
	sort.Strings(parts)
	var report strings.Builder
	for _, part := range parts {
		entry := latest[part]
		model, reasoning := strings.TrimSpace(entry.model), strings.TrimSpace(entry.reasoning)
		if model == "" {
			model = "not recorded"
		}
		if reasoning == "" {
			reasoning = "harness default"
		}
		if report.Len() > 0 {
			report.WriteString("; ")
		}
		fmt.Fprintf(&report, "%s: %s (reasoning: %s)", attributionField(part, 64), attributionField(model, 120), attributionField(reasoning, 32))
	}
	return report.String()
}

func agentWorkPart(role, stage string) string {
	switch stage {
	case metrics.StagePlannerOutline, metrics.StagePlannerDetails:
		return "Planning"
	case metrics.StageReviewerAudit, metrics.StageReviewerVerify:
		return "QA"
	case metrics.StageTestSpecialist:
		return "Test specialist"
	case metrics.StageHarnessRun:
		switch role {
		case config.WorkRolePlanner:
			return "Planning"
		case config.WorkRoleImplementer:
			return "Implementation"
		case config.WorkRoleReviewer:
			return "QA"
		default:
			return titleIdentifier(role)
		}
	default:
		return ""
	}
}

func attributionField(value string, limit int) string {
	return presentation.MarkdownInline(boundedReviewText(strings.Join(strings.Fields(value), " "), limit))
}
