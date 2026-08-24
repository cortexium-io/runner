package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const MaxIntakeMutationRequests = 100

type intakeMutationBudgetKey struct{}

type intakeMutationBudget struct{ used int }

func withIntakeMutationBudget(ctx context.Context) context.Context {
	return context.WithValue(ctx, intakeMutationBudgetKey{}, &intakeMutationBudget{})
}

func intakeMutationBudgetFromContext(ctx context.Context) *intakeMutationBudget {
	budget, _ := ctx.Value(intakeMutationBudgetKey{}).(*intakeMutationBudget)
	return budget
}

func (b *intakeMutationBudget) consume() error {
	if b == nil {
		return nil
	}
	if b.used >= MaxIntakeMutationRequests {
		return fmt.Errorf("public intake reached fixed mutation request limit of %d; remaining eligible issues will resume on a later synchronization cycle", MaxIntakeMutationRequests)
	}
	b.used++
	return nil
}

func (s *Project) SyncAssessmentIssues(ctx context.Context) (AssessmentSyncResult, error) {
	items, err := s.LifecycleItems(ctx)
	if err != nil {
		return AssessmentSyncResult{}, err
	}
	return s.SyncAssessmentIssuesFrom(ctx, items)
}

func (s *Project) SyncAssessmentIssuesFrom(ctx context.Context, items []WorkItem) (AssessmentSyncResult, error) {
	repository := strings.TrimSpace(s.cfg.IntakeRepository)
	if repository == "" {
		return AssessmentSyncResult{}, nil
	}
	if s.assessmentStatus() == "" {
		return AssessmentSyncResult{}, errors.New("assessment_status is required when issue intake is enabled")
	}
	ctx = withIntakeMutationBudget(ctx)
	existingByURL := make(map[string]WorkItem, len(items))
	for _, item := range items {
		if item.URL != "" {
			existingByURL[item.URL] = item
		}
	}
	result, err := s.gh(ctx, "issue", "list", "--repo", repository, "--label", s.intakeLabel(), "--state", "open", "--limit", strconv.Itoa(MaxAssessmentIssues+1), "--json", "url")
	if err != nil {
		return AssessmentSyncResult{}, fmt.Errorf("list public assessment issues: %w", commandFailure(err, result))
	}
	issues, err := decodeAssessmentIssues(result.Stdout)
	if err != nil {
		return AssessmentSyncResult{}, fmt.Errorf("decode public assessment issues: %w", err)
	}
	syncResult := AssessmentSyncResult{Discovered: len(issues)}
	for _, issue := range issues {
		url := strings.TrimSpace(issue.URL)
		if url == "" {
			continue
		}
		if existing, exists := existingByURL[url]; exists {
			if strings.TrimSpace(existing.Approval) != "" {
				if err := s.clearApproval(ctx, existing.ID); err != nil {
					return syncResult, err
				}
			}
			if !strings.EqualFold(strings.TrimSpace(existing.Status), s.assessmentStatus()) {
				if err := s.setStatus(ctx, existing.ID, s.assessmentStatus()); err != nil {
					return syncResult, err
				}
				syncResult.Reclassified++
			}
			continue
		}
		addedResult, addErr := s.gh(ctx, "project", "item-add", strconv.Itoa(s.cfg.Number), "--owner", strings.TrimSpace(s.cfg.Owner), "--url", url, "--format", "json")
		if addErr != nil {
			return syncResult, fmt.Errorf("add assessment issue to GitHub Project: %w", commandFailure(addErr, addedResult))
		}
		var added struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(addedResult.Stdout), &added); err != nil || strings.TrimSpace(added.ID) == "" {
			return syncResult, errors.New("GitHub Project item-add did not return an item id")
		}
		if err := s.setStatus(ctx, added.ID, s.assessmentStatus()); err != nil {
			return syncResult, err
		}
		existingByURL[url] = WorkItem{ID: added.ID, URL: url, Status: s.assessmentStatus()}
		syncResult.Added++
	}
	return syncResult, nil
}

func decodeAssessmentIssues(output string) ([]struct {
	URL string `json:"url"`
}, error) {
	decoder := json.NewDecoder(bytes.NewReader([]byte(output)))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '[' {
		return nil, errors.New("public assessment issue response must be a JSON array")
	}
	issues := make([]struct {
		URL string `json:"url"`
	}, 0)
	for decoder.More() {
		if len(issues) >= MaxAssessmentIssues {
			return nil, fmt.Errorf("public assessment intake exceeds the supported limit of %d open labeled issues; reduce or partition the intake before running", MaxAssessmentIssues)
		}
		var issue struct {
			URL string `json:"url"`
		}
		if err := decoder.Decode(&issue); err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("public assessment issue response contains trailing JSON")
	}
	return issues, nil
}

func (s *Project) inspectIntake(ctx context.Context) (bool, bool, error) {
	repository := strings.TrimSpace(s.cfg.IntakeRepository)
	if repository == "" {
		return true, true, nil
	}
	result, err := s.gh(ctx, "repo", "view", repository, "--json", "nameWithOwner,hasIssuesEnabled")
	if err != nil {
		return false, false, fmt.Errorf("inspect intake repository: %w", commandFailure(err, result))
	}
	var repo struct {
		NameWithOwner    string `json:"nameWithOwner"`
		HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &repo); err != nil {
		return false, false, fmt.Errorf("decode intake repository: %w", err)
	}
	repositoryOK := strings.EqualFold(strings.TrimSpace(repo.NameWithOwner), repository) && repo.HasIssuesEnabled
	labelsResult, err := s.gh(ctx, "label", "list", "--repo", repository, "--search", s.intakeLabel(), "--limit", "100", "--json", "name")
	if err != nil {
		return repositoryOK, false, fmt.Errorf("inspect intake label: %w", commandFailure(err, labelsResult))
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if output := strings.TrimSpace(labelsResult.Stdout); output != "" {
		if err := json.Unmarshal([]byte(output), &labels); err != nil {
			return repositoryOK, false, fmt.Errorf("decode intake labels: %w", err)
		}
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), s.intakeLabel()) {
			return repositoryOK, true, nil
		}
	}
	return repositoryOK, false, nil
}
