package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MaxCompletedIssueClosureAttemptsPerPoll = 16

// IssueCompletionFailure identifies a non-critical issue-closing failure. The
// coordinator reports it without preventing unrelated executable work.
type IssueCompletionFailure struct {
	Item WorkItem
	Err  error
}

// ReconcileCompletedIssues closes issue-backed implementation cards after
// their authenticated PR merge, then closes a planning source only after every
// exact child in its authenticated released batch has the same outcome.
func (s *Project) ReconcileCompletedIssues(ctx context.Context, items []WorkItem) (int, []IssueCompletionFailure) {
	closed := 0
	attempted := 0
	failures := []IssueCompletionFailure{}
	index := newWorkItemIndex(items)
	closeEligible := func(item WorkItem) {
		if attempted >= MaxCompletedIssueClosureAttemptsPerPoll || !strings.EqualFold(strings.TrimSpace(item.IssueState), "OPEN") || !s.isIntakeIssueURL(item.URL) {
			return
		}
		attempted++
		changed, err := s.closeCompletedIssue(ctx, item.URL)
		if err != nil {
			failures = append(failures, IssueCompletionFailure{Item: item, Err: err})
			return
		}
		if changed {
			closed++
		}
	}

	for _, item := range items {
		if strings.TrimSpace(item.PullRequest) != "" && s.hasSuccessfulOutcomeIn(item, index) {
			closeEligible(item)
		}
	}
	for _, source := range items {
		if s.planningSourceWorkCompletedIn(source, index) {
			closeEligible(source)
		}
	}
	return closed, failures
}

func (s *Project) planningSourceWorkCompleted(source WorkItem, items []WorkItem) bool {
	return s.planningSourceWorkCompletedIn(source, newWorkItemIndex(items))
}

func (s *Project) planningSourceWorkCompletedIn(source WorkItem, index *workItemIndex) bool {
	if !strings.EqualFold(strings.TrimSpace(source.Status), s.doneStatus()) || strings.TrimSpace(source.Transition) != "" || strings.TrimSpace(source.PullRequest) != "" {
		return false
	}
	children := index.childrenBySource[strings.TrimSpace(source.ID)]
	if len(children) == 0 {
		return false
	}
	if _, err := s.validatePlanningBatch(source.Approval, source, children, batchReleasedState); err != nil {
		return false
	}
	for _, child := range children {
		if strings.TrimSpace(child.PullRequest) == "" || !s.hasSuccessfulOutcomeIn(child, index) {
			return false
		}
	}
	return true
}

func (s *Project) closeCompletedIssue(ctx context.Context, issueURL string) (bool, error) {
	result, err := s.gh(ctx, "issue", "view", strings.TrimSpace(issueURL), "--json", "url,state")
	if err != nil {
		return false, fmt.Errorf("inspect completed issue before closing: %w", commandFailure(err, result))
	}
	var issue struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &issue); err != nil {
		return false, fmt.Errorf("decode completed issue state: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(issue.URL), strings.TrimSpace(issueURL)) {
		return false, errors.New("completed issue lookup returned a different issue")
	}
	switch strings.ToUpper(strings.TrimSpace(issue.State)) {
	case "CLOSED":
		return false, nil
	case "OPEN":
	default:
		return false, fmt.Errorf("completed issue has unexpected state %q", issue.State)
	}
	result, err = s.gh(ctx, "issue", "close", strings.TrimSpace(issueURL), "--reason", "completed")
	if err != nil {
		return false, fmt.Errorf("close completed issue: %w", commandFailure(err, result))
	}
	return true, nil
}
