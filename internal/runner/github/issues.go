package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type issue struct {
	NodeID      string          `json:"node_id"`
	Number      int             `json:"number"`
	URL         string          `json:"html_url"`
	Title       string          `json:"title"`
	Body        *string         `json:"body"`
	State       string          `json:"state"`
	PullRequest json.RawMessage `json:"pull_request"`
	Labels      []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// Issues binds provider configuration at composition time. The engine's intake
// and view contracts do not require another provider to understand GitHub config.
type Issues struct {
	Host   Host
	Config config.GitHubIssues
}

func (i Issues) Requests(ctx context.Context) ([]core.SourceRequest, error) {
	return i.Host.Requests(ctx, i.Config)
}

func (i Issues) Request(ctx context.Context, key string) (core.SourceRequest, error) {
	number, err := strconv.Atoi(key)
	if err != nil {
		return core.SourceRequest{}, errors.New("GitHub request key must be an issue number")
	}
	return i.Host.Request(ctx, i.Config, number)
}

func (i Issues) ObserveOverview(ctx context.Context, marker string) (string, error) {
	return i.Host.ObserveOverview(ctx, i.Config, marker)
}

func (i Issues) PublishOverview(ctx context.Context, marker, body string, beforeWrite func() error) (bool, error) {
	return i.Host.PublishOverview(ctx, i.Config, marker, body, beforeWrite)
}

type issueRequestError struct{ status int }

func (e issueRequestError) Error() string {
	return "GitHub Issues request unavailable; check authentication, permissions and network"
}

func (h Host) rest(ctx context.Context, method, endpoint string, input, output any) error {
	var b bytes.Buffer
	args := []string{endpoint, "--method", method}
	if input != nil {
		if err := json.NewEncoder(&b).Encode(input); err != nil {
			return err
		}
		args = append(args, "--input", "-")
	}
	request := h.request
	if request == nil {
		request = subprocess.RunGitHubAPI
	}
	r, err := request(ctx, args, &b)
	if err != nil || r.ExitCode != 0 {
		var response struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal([]byte(r.Stdout), &response)
		status, _ := strconv.Atoi(response.Status)
		return issueRequestError{status: status}
	}
	if err = json.Unmarshal([]byte(r.Stdout), output); err != nil {
		return errors.New("GitHub Issues returned invalid JSON")
	}
	return nil
}

func requestFromIssue(g config.GitHubIssues, i issue) (core.SourceRequest, error) {
	expected := "https://github.com/" + g.Repository + "/issues/" + strconv.Itoa(i.Number)
	if !config.ValidRepository(g.Repository) || i.Number < 1 || i.NodeID == "" || strings.TrimSpace(i.Title) == "" || !strings.EqualFold(i.URL, expected) || (len(i.PullRequest) != 0 && string(i.PullRequest) != "null") || (i.State != "open" && i.State != "closed") {
		return core.SourceRequest{}, errors.New("GitHub issue identity is incomplete, outside the connection, or a pull request")
	}
	body := ""
	if i.Body != nil {
		body = *i.Body
	}
	return core.SourceRequest{Open: i.State == "open", Source: core.Source{Provider: "github-issue", Connection: strings.ToLower(g.Repository), ID: i.NodeID, URL: i.URL, Title: i.Title, Body: body, Revision: core.Digest([]string{i.Title, body})}}, nil
}

func (h Host) Request(ctx context.Context, g config.GitHubIssues, number int) (core.SourceRequest, error) {
	if !config.ValidRepository(g.Repository) || number < 1 {
		return core.SourceRequest{}, errors.New("GitHub request requires OWNER/REPO and a positive issue number")
	}
	var i issue
	if err := h.rest(ctx, "GET", fmt.Sprintf("repos/%s/issues/%d", g.Repository, number), nil, &i); err != nil {
		return core.SourceRequest{}, err
	}
	if i.Number != number {
		return core.SourceRequest{}, errors.New("GitHub returned a different issue")
	}
	return requestFromIssue(g, i)
}

func (h Host) Requests(ctx context.Context, g config.GitHubIssues) ([]core.SourceRequest, error) {
	if !config.ValidRepository(g.Repository) {
		return nil, errors.New("invalid GitHub Issues repository")
	}
	out := []core.SourceRequest{}
	for page := 1; page <= 10; page++ {
		var issues []issue
		endpoint := fmt.Sprintf("repos/%s/issues?state=open&labels=%s&per_page=100&page=%d", g.Repository, url.QueryEscape(g.Label()), page)
		if err := h.rest(ctx, "GET", endpoint, nil, &issues); err != nil {
			return nil, err
		}
		if issues == nil {
			return nil, errors.New("GitHub returned an incomplete issue inbox")
		}
		for _, i := range issues {
			if len(i.PullRequest) != 0 && string(i.PullRequest) != "null" {
				continue // GitHub's issues endpoint includes pull requests.
			}
			r, err := requestFromIssue(g, i)
			if err != nil {
				return nil, err
			}
			matches := false
			for _, l := range i.Labels {
				matches = matches || l.Name == g.Label()
			}
			if !r.Open || !matches {
				return nil, errors.New("GitHub returned an issue outside the requested inbox")
			}
			if i.Number != g.OverviewIssue {
				out = append(out, r)
			}
		}
		if len(issues) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("issue inbox exceeds 1000 entries; narrow the configured intake label")
}

func (h Host) overview(ctx context.Context, g config.GitHubIssues, marker string) (core.SourceRequest, error) {
	if !config.ValidRepository(g.Repository) || g.OverviewIssue < 1 || !strings.HasPrefix(marker, "<!-- runner-overview:") {
		return core.SourceRequest{}, errors.New("overview requires a valid connection and instance marker")
	}
	var current issue
	if err := h.rest(ctx, "GET", fmt.Sprintf("repos/%s/issues/%d", g.Repository, g.OverviewIssue), nil, &current); err != nil {
		return core.SourceRequest{}, err
	}
	r, err := requestFromIssue(g, current)
	if err != nil {
		return r, err
	}
	if current.Number != g.OverviewIssue || current.Labels == nil {
		return r, errors.New("GitHub returned an incomplete or different overview issue")
	}
	if !r.Open {
		return r, errors.New("overview issue is closed; reopen the configured issue before synchronizing")
	}
	if strings.Contains(r.Source.Body, "<!-- runner-overview:") && !strings.HasPrefix(r.Source.Body, marker+"\n") {
		return r, errors.New("overview belongs to a different Runner instance")
	}
	for _, l := range current.Labels {
		if l.Name == g.Label() {
			return r, errors.New("overview issue has the intake label; use a separate tracking issue")
		}
	}
	return r, nil
}

func (h Host) ObserveOverview(ctx context.Context, g config.GitHubIssues, marker string) (string, error) {
	r, err := h.overview(ctx, g, marker)
	return r.Source.Body, err
}

func (h Host) PublishOverview(ctx context.Context, g config.GitHubIssues, marker, body string, beforeWrite func() error) (bool, error) {
	if len(body) > 60000 || !strings.HasPrefix(body, marker+"\n") {
		return false, errors.New("overview exceeds the bounded presentation size or has no identity")
	}
	r, err := h.overview(ctx, g, marker)
	if err != nil {
		return false, err
	}
	if r.Source.Body == body {
		return false, nil
	}
	if beforeWrite == nil {
		return false, errors.New("overview write requires durable intent")
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = beforeWrite(); err != nil {
		return false, err
	}
	var updated issue
	endpoint := fmt.Sprintf("repos/%s/issues/%d", g.Repository, g.OverviewIssue)
	if err = h.rest(ctx, "PATCH", endpoint, map[string]string{"body": body}, &updated); err != nil {
		var refusal issueRequestError
		if errors.As(err, &refusal) && refusal.status >= 400 && refusal.status < 500 && refusal.status != 408 {
			return false, err // An explicit rejection did not change the body.
		}
		return true, err // No newer write until this sole intent is observed.
	}
	result, err := requestFromIssue(g, updated)
	if err != nil || updated.Number != g.OverviewIssue || result.Source.ID != r.Source.ID || result.Source.Body != body {
		return true, errors.New("GitHub did not confirm the expected overview update")
	}
	return false, nil
}
