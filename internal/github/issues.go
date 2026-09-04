package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ensureIssueBacked converts Project drafts at the execution authorization
// boundary. Planning can therefore remain a reversible draft-only operation,
// while every executable card has a durable GitHub discussion surface.
func (s *Project) ensureIssueBacked(ctx context.Context, items []WorkItem) ([]WorkItem, error) {
	result := append([]WorkItem(nil), items...)
	repository, err := s.configuredIssueRepository()
	if err != nil {
		return nil, err
	}
	needsConversion := false
	for _, item := range result {
		if strings.TrimSpace(item.URL) == "" {
			needsConversion = true
			continue
		}
		if !issueReferenceMatchesRepository(item.URL, repository) {
			return nil, fmt.Errorf("Project item %s is not an issue in configured intake repository %q", item.ID, repository)
		}
	}
	if !needsConversion {
		return result, nil
	}
	repositoryID, repository, err := s.issueRepositoryID(ctx)
	if err != nil {
		return nil, err
	}
	for index, item := range result {
		if strings.TrimSpace(item.URL) != "" {
			continue
		}
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Body) == "" {
			return nil, fmt.Errorf("Project draft %d is incomplete and cannot be converted to an issue", index+1)
		}
		if target := strings.TrimSpace(item.Repository); target != "" && !strings.EqualFold(target, repository) {
			return nil, fmt.Errorf("Project draft %s targets repository %q instead of configured intake repository %q", item.ID, target, repository)
		}
		converted, convertErr := s.convertDraftToIssue(ctx, item, repositoryID, repository)
		if convertErr != nil {
			return nil, fmt.Errorf("convert Project draft %s to an issue: %w", item.ID, convertErr)
		}
		result[index] = converted
	}

	ids := make([]string, len(result))
	for index := range result {
		ids[index] = result[index].ID
	}
	reloaded, err := s.LifecycleItemsByID(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reload issue-backed Project cards: %w", err)
	}
	for index := range result {
		if !sameItemAfterIssueConversion(items[index], reloaded[index], repository) {
			return nil, fmt.Errorf("Project item %s changed while it was converted to an issue", result[index].ID)
		}
	}
	return reloaded, nil
}

func (s *Project) issueRepositoryID(ctx context.Context) (string, string, error) {
	repository, err := s.configuredIssueRepository()
	if err != nil {
		return "", "", err
	}
	owner, name, _ := strings.Cut(repository, "/")
	query := `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id nameWithOwner hasIssuesEnabled}}`
	response, err := s.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "owner="+owner, "-F", "name="+name)
	if err != nil {
		return "", "", fmt.Errorf("resolve issue repository: %w", commandFailure(err, response))
	}
	var payload struct {
		Data struct {
			Repository *struct {
				ID               string `json:"id"`
				NameWithOwner    string `json:"nameWithOwner"`
				HasIssuesEnabled bool   `json:"hasIssuesEnabled"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Stdout), &payload); err != nil {
		return "", "", fmt.Errorf("decode issue repository: %w", err)
	}
	if payload.Data.Repository == nil || strings.TrimSpace(payload.Data.Repository.ID) == "" ||
		!strings.EqualFold(strings.TrimSpace(payload.Data.Repository.NameWithOwner), repository) {
		return "", "", fmt.Errorf("configured intake repository %q was not found", repository)
	}
	if !payload.Data.Repository.HasIssuesEnabled {
		return "", "", fmt.Errorf("configured intake repository %q has issues disabled", repository)
	}
	return strings.TrimSpace(payload.Data.Repository.ID), repository, nil
}

func (s *Project) configuredIssueRepository() (string, error) {
	repository := strings.Trim(strings.TrimSpace(s.cfg.IntakeRepository), "/")
	owner, name, found := strings.Cut(repository, "/")
	if !found || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" || strings.Contains(name, "/") {
		return "", errors.New("configured intake repository must use owner/repository format")
	}
	return strings.TrimSpace(owner) + "/" + strings.TrimSpace(name), nil
}

func (s *Project) convertDraftToIssue(ctx context.Context, item WorkItem, repositoryID, repository string) (WorkItem, error) {
	mutation := `mutation($item_id:ID!,$repository_id:ID!){convertProjectV2DraftIssueItemToIssue(input:{itemId:$item_id,repositoryId:$repository_id}){item{id content{... on Issue{id title body url state repository{nameWithOwner}}}}}}`
	response, err := s.gh(ctx, "api", "graphql", "-f", "query="+mutation, "-F", "item_id="+strings.TrimSpace(item.ID), "-F", "repository_id="+strings.TrimSpace(repositoryID))
	if err != nil {
		return WorkItem{}, commandFailure(err, response)
	}
	var payload struct {
		Data struct {
			Conversion *struct {
				Item *struct {
					ID      string `json:"id"`
					Content *struct {
						ID         string `json:"id"`
						Title      string `json:"title"`
						Body       string `json:"body"`
						URL        string `json:"url"`
						State      string `json:"state"`
						Repository *struct {
							NameWithOwner string `json:"nameWithOwner"`
						} `json:"repository"`
					} `json:"content"`
				} `json:"item"`
			} `json:"convertProjectV2DraftIssueItemToIssue"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(response.Stdout), &payload); err != nil {
		return WorkItem{}, fmt.Errorf("decode converted issue: %w", err)
	}
	if payload.Data.Conversion == nil || payload.Data.Conversion.Item == nil || payload.Data.Conversion.Item.Content == nil {
		return WorkItem{}, errors.New("GitHub did not return the converted issue")
	}
	converted := payload.Data.Conversion.Item
	content := converted.Content
	if strings.TrimSpace(converted.ID) != strings.TrimSpace(item.ID) || strings.TrimSpace(content.ID) == "" ||
		strings.TrimSpace(content.URL) == "" || strings.TrimSpace(content.Title) != strings.TrimSpace(item.Title) ||
		strings.TrimSpace(content.Body) != strings.TrimSpace(item.Body) || content.Repository == nil ||
		!strings.EqualFold(strings.TrimSpace(content.Repository.NameWithOwner), repository) {
		return WorkItem{}, errors.New("GitHub returned a converted issue with unexpected identity or content")
	}
	next := item
	next.DraftContentID = strings.TrimSpace(content.ID)
	next.Title = strings.TrimSpace(content.Title)
	next.Body = strings.TrimSpace(content.Body)
	next.URL = strings.TrimSpace(content.URL)
	next.Repository = strings.TrimSpace(content.Repository.NameWithOwner)
	next.IssueState = strings.TrimSpace(content.State)
	return next, nil
}

func sameItemAfterIssueConversion(before, after WorkItem, repository string) bool {
	if strings.TrimSpace(before.URL) != "" {
		return reflect.DeepEqual(before, after)
	}
	expected := before
	expected.DraftContentID = after.DraftContentID
	expected.URL = after.URL
	expected.Repository = repository
	expected.IssueState = after.IssueState
	return issueReferenceMatchesRepository(after.URL, repository) && reflect.DeepEqual(expected, after)
}

func issueReferenceMatchesRepository(value, repository string) bool {
	owner, name, _, supported := issueReference(value)
	return supported && strings.EqualFold(owner+"/"+name, strings.TrimSpace(repository))
}
