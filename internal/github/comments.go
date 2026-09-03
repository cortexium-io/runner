package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const (
	maxAssignmentComments    = 20
	maxAssignmentCommentBody = 4_000
	maxIssueCommentBody      = 32_000
)

// ItemComment is bounded issue discussion supplied as historical task context.
// GitHub Project draft items do not support comments and return an empty slice.
type ItemComment struct {
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	URL       string `json:"url"`
}

// ItemComments loads only the newest bounded discussion for one claimed card.
// It deliberately does not expand the Project-wide polling query.
func (s *Project) ItemComments(ctx context.Context, item WorkItem) ([]ItemComment, error) {
	owner, repository, number, supported := issueReference(item.URL)
	if !supported {
		return []ItemComment{}, nil
	}
	actorResult, err := s.gh(ctx, "api", "user", "--jq", ".login")
	if err != nil {
		return nil, fmt.Errorf("resolve authenticated GitHub comment actor: %w", commandFailure(err, actorResult))
	}
	trustedActor := strings.TrimSpace(actorResult.Stdout)
	if trustedActor == "" || strings.ContainsAny(trustedActor, "\x00\r\n") {
		return nil, errors.New("authenticated GitHub comment actor is empty or ambiguous")
	}
	query := `query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){issue(number:$number){comments(last:` + strconv.Itoa(maxAssignmentComments) + `){nodes{author{login}body createdAt url}}}}}`
	result, err := s.gh(ctx, "api", "graphql", "-f", "query="+query, "-F", "owner="+owner, "-F", "name="+repository, "-F", "number="+strconv.Itoa(number))
	if err != nil {
		return nil, fmt.Errorf("load issue comments: %w", commandFailure(err, result))
	}
	var payload struct {
		Data struct {
			Repository *struct {
				Issue *struct {
					Comments struct {
						Nodes []struct {
							Author *struct {
								Login string `json:"login"`
							} `json:"author"`
							Body      string `json:"body"`
							CreatedAt string `json:"createdAt"`
							URL       string `json:"url"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
		return nil, fmt.Errorf("decode issue comments: %w", err)
	}
	if payload.Data.Repository == nil || payload.Data.Repository.Issue == nil {
		return nil, errors.New("issue-backed Project card no longer resolves to an issue")
	}
	nodes := payload.Data.Repository.Issue.Comments.Nodes
	comments := make([]ItemComment, 0, len(nodes))
	for _, node := range nodes {
		body := truncate(strings.TrimSpace(node.Body), maxAssignmentCommentBody)
		if body == "" {
			continue
		}
		if node.Author == nil || !strings.EqualFold(strings.TrimSpace(node.Author.Login), trustedActor) {
			continue
		}
		author := strings.TrimSpace(node.Author.Login)
		comments = append(comments, ItemComment{Author: author, Body: body, CreatedAt: strings.TrimSpace(node.CreatedAt), URL: strings.TrimSpace(node.URL)})
	}
	return comments, nil
}

// PostIssueComment writes an idempotent Runner comment only while the exact
// Project action remains authorized. Unsupported draft cards are a no-op.
func (s *Project) PostIssueComment(ctx context.Context, expected AuthorizedAction, marker, body string) (bool, error) {
	current, err := s.refreshAuthorizedAction(ctx, expected)
	if err != nil {
		return false, err
	}
	item, err := current.authorizedItem()
	if err != nil {
		return false, err
	}
	if _, _, _, supported := issueReference(item.URL); !supported {
		return false, nil
	}
	marker, body = strings.TrimSpace(marker), strings.TrimSpace(body)
	if marker == "" || body == "" {
		return false, errors.New("issue comment marker and body are required")
	}
	if len(body) > maxIssueCommentBody {
		return false, fmt.Errorf("issue comment exceeds the %d-byte safety limit", maxIssueCommentBody)
	}
	comments, err := s.ItemComments(ctx, item)
	if err != nil {
		return false, err
	}
	for _, comment := range comments {
		if strings.Contains(comment.Body, marker) {
			return false, nil
		}
	}
	result, err := s.gh(ctx, "issue", "comment", item.URL, "--body", marker+"\n\n"+body)
	if err != nil {
		return false, fmt.Errorf("post issue comment: %w", commandFailure(err, result))
	}
	return true, nil
}

func issueReference(value string) (owner, repository string, number int, supported bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "issues" {
		return "", "", 0, false
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number < 1 {
		return "", "", 0, false
	}
	return parts[0], parts[1], number, true
}
