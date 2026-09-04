package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (s *Project) routeAutonomousAssessmentIssues(ctx context.Context, issues []assessmentIssue) (int, error) {
	private, err := s.intakeRepositoryPrivate(ctx)
	if err != nil {
		return 0, err
	}
	items, err := s.ListItems(ctx)
	if err != nil {
		return 0, fmt.Errorf("reload Project items before autonomous issue routing: %w", err)
	}
	itemsByURL := make(map[string]WorkItem, len(items))
	for _, item := range items {
		itemsByURL[strings.TrimSpace(item.URL)] = item
	}
	routed := 0
	for _, issue := range issues {
		if routed >= MaxAutonomousIssueApprovalsPerSync {
			break
		}
		if !private && !s.trustedIssueAuthor(issueAuthor(issue)) {
			continue
		}
		item, exists := itemsByURL[strings.TrimSpace(issue.URL)]
		if !exists || !strings.EqualFold(strings.TrimSpace(item.Status), s.assessmentStatus()) ||
			item.PlanningMetadataInvalid || strings.TrimSpace(item.PlanningSourceID) != "" || strings.TrimSpace(item.PlanningBatchFingerprint) != "" || HasRuntimeActionState(item) {
			continue
		}
		plan, err := s.PlanApproval(ctx, item.ID)
		if err != nil {
			return routed, fmt.Errorf("preview autonomous approval for issue %s: %w", issue.URL, err)
		}
		trusted, err := s.autonomousIssueTrusted(ctx, issue.URL, true)
		if err != nil {
			return routed, err
		}
		if !trusted {
			continue
		}
		approved, err := s.ApplyApproval(ctx, plan)
		if err != nil {
			return routed, fmt.Errorf("approve trusted issue %s: %w", issue.URL, err)
		}
		initialStatus := strings.TrimSpace(s.cfg.LaneStatuses[strings.TrimSpace(s.cfg.InitialLaneID)])
		if initialStatus == "" {
			return routed, errors.New("autonomous issue intake has no configured initial planning lane")
		}
		if err := s.setStatus(ctx, approved.ID, initialStatus); err != nil {
			return routed, fmt.Errorf("move approved trusted issue %s to %s: %w", issue.URL, initialStatus, err)
		}
		routed++
	}
	return routed, nil
}

// ReconcileAutonomousPlanningApprovals releases exact planner batches whose
// original issue remains trusted. It does not consume harness parallelism.
func (s *Project) ReconcileAutonomousPlanningApprovals(ctx context.Context, items []WorkItem) (int, error) {
	if s.cfg.AutonomousIssueIntake == nil {
		return 0, nil
	}
	released := 0
	for _, source := range items {
		if !strings.EqualFold(strings.TrimSpace(source.Status), s.assessmentStatus()) || strings.TrimSpace(source.Phase) != PlanningApprovalPhase {
			continue
		}
		trusted, err := s.autonomousIssueTrusted(ctx, source.URL, false)
		if err != nil {
			return released, err
		}
		if !trusted {
			continue
		}
		plan, err := s.PlanApproval(ctx, source.ID)
		if err != nil {
			return released, fmt.Errorf("preview autonomous planner-batch approval for issue %s: %w", source.URL, err)
		}
		trusted, err = s.autonomousIssueTrusted(ctx, source.URL, false)
		if err != nil {
			return released, err
		}
		if !trusted {
			continue
		}
		if _, err := s.ApplyApproval(ctx, plan); err != nil {
			return released, fmt.Errorf("release trusted planner batch for issue %s: %w", source.URL, err)
		}
		released++
	}
	return released, nil
}

func (s *Project) autonomousIssueTrusted(ctx context.Context, issueURL string, requireOpenIntakeLabel bool) (bool, error) {
	if s.cfg.AutonomousIssueIntake == nil || !s.isIntakeIssueURL(issueURL) {
		return false, nil
	}
	private, err := s.intakeRepositoryPrivate(ctx)
	if err != nil {
		return false, err
	}
	if private && !requireOpenIntakeLabel {
		return true, nil
	}
	result, err := s.gh(ctx, "issue", "view", strings.TrimSpace(issueURL), "--json", "url,author,labels,state")
	if err != nil {
		return false, fmt.Errorf("inspect autonomous issue authority: %w", commandFailure(err, result))
	}
	var issue struct {
		URL    string `json:"url"`
		State  string `json:"state"`
		Author *struct {
			Login string `json:"login"`
		} `json:"author"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &issue); err != nil {
		return false, fmt.Errorf("decode autonomous issue authority: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(issue.URL), strings.TrimSpace(issueURL)) {
		return false, errors.New("autonomous issue authority lookup returned a different issue")
	}
	if requireOpenIntakeLabel {
		if !strings.EqualFold(strings.TrimSpace(issue.State), "OPEN") || !hasNormalizedLabel(issue.Labels, s.intakeLabel()) {
			return false, nil
		}
	}
	if private {
		return true, nil
	}
	author := ""
	if issue.Author != nil {
		author = issue.Author.Login
	}
	return s.trustedIssueAuthor(author), nil
}

func (s *Project) intakeRepositoryPrivate(ctx context.Context) (bool, error) {
	repository := strings.TrimSpace(s.cfg.IntakeRepository)
	result, err := s.gh(ctx, "repo", "view", repository, "--json", "nameWithOwner,isPrivate")
	if err != nil {
		return false, fmt.Errorf("inspect autonomous intake repository visibility: %w", commandFailure(err, result))
	}
	var payload struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsPrivate     bool   `json:"isPrivate"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return false, fmt.Errorf("decode autonomous intake repository visibility: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(payload.NameWithOwner), repository) {
		return false, errors.New("autonomous intake repository lookup returned a different repository")
	}
	return payload.IsPrivate, nil
}

func (s *Project) trustedIssueAuthor(author string) bool {
	if s.cfg.AutonomousIssueIntake == nil {
		return false
	}
	for _, trusted := range s.cfg.AutonomousIssueIntake.TrustedAuthors {
		if strings.EqualFold(strings.TrimSpace(author), strings.TrimSpace(trusted)) {
			return true
		}
	}
	return false
}

func issueAuthor(issue assessmentIssue) string {
	if issue.Author == nil {
		return ""
	}
	return strings.TrimSpace(issue.Author.Login)
}

func hasNormalizedLabel(labels []struct {
	Name string `json:"name"`
}, expected string) bool {
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), strings.TrimSpace(expected)) {
			return true
		}
	}
	return false
}
