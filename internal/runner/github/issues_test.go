package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func issueFixture(number int, body string) map[string]any {
	return map[string]any{"node_id": fmt.Sprintf("I_%d", number), "number": number, "html_url": fmt.Sprintf("https://github.com/owner/repo/issues/%d", number), "title": "A request", "body": body, "state": "open", "labels": []any{}}
}

func issueResponse(v any) (subprocess.Result, error) {
	b, err := json.Marshal(v)
	return subprocess.Result{Stdout: string(b)}, err
}

func TestIssueInboxPaginatesAndExcludesPullRequests(t *testing.T) {
	calls := 0
	g := config.GitHubIssues{Repository: "owner/repo", OverviewIssue: 7}
	h := Host{request: func(_ context.Context, args []string, _ io.Reader) (subprocess.Result, error) {
		calls++
		if !strings.Contains(args[0], "state=open&labels=runner%3Arequest&per_page=100&page=") || args[2] != "GET" {
			t.Fatal("inbox read escaped scope", args)
		}
		if calls == 1 {
			page := []any{}
			for n := 1; n <= 100; n++ {
				i := issueFixture(n, "request body")
				i["labels"] = []any{map[string]string{"name": g.Label()}}
				if n == 2 {
					i["pull_request"] = map[string]string{"url": "pull"}
				}
				page = append(page, i)
			}
			return issueResponse(page)
		}
		if calls != 2 || !strings.HasSuffix(args[0], "page=2") {
			t.Fatal("pagination was not bounded and sequential", args)
		}
		return issueResponse([]any{})
	}}
	requests, err := h.Requests(t.Context(), g)
	if err != nil || len(requests) != 98 || calls != 2 { // PR and configured overview excluded.
		t.Fatalf("inbox: %d requests, %d calls, %v", len(requests), calls, err)
	}
	for _, raw := range []string{"null", "{}", `[{"number":3}]`, "not json"} {
		h.request = func(context.Context, []string, io.Reader) (subprocess.Result, error) {
			return subprocess.Result{Stdout: raw}, nil
		}
		if _, err := h.Requests(t.Context(), g); err == nil {
			t.Fatal("incomplete inbox accepted", raw)
		}
	}
}

func TestRequestRejectsWrongIdentityAndCapturesContent(t *testing.T) {
	g := config.GitHubIssues{Repository: "owner/repo", OverviewIssue: 7}
	for _, scenario := range []string{"valid", "other number", "other repository", "PR", "missing identity", "unknown state"} {
		t.Run(scenario, func(t *testing.T) {
			i := issueFixture(3, "exact body\n")
			switch scenario {
			case "other number":
				i = issueFixture(4, "other")
			case "other repository":
				i["html_url"] = "https://github.com/other/repo/issues/3"
			case "PR":
				i["pull_request"] = map[string]string{"url": "pull"}
			case "missing identity":
				delete(i, "node_id")
			case "unknown state":
				i["state"] = "unknown"
			}
			h := Host{request: func(_ context.Context, args []string, _ io.Reader) (subprocess.Result, error) {
				if args[0] != "repos/owner/repo/issues/3" || args[2] != "GET" {
					t.Fatal("request scope changed", args)
				}
				return issueResponse(i)
			}}
			r, err := h.Request(t.Context(), g, 3)
			if (err == nil) != (scenario == "valid") {
				t.Fatal("invalid issue identity accepted", err)
			}
			if scenario == "valid" && (r.Source.Body != "exact body\n" || r.Source.Revision == "" || r.Source.ID != "I_3" || !r.Open) {
				t.Fatal("source snapshot changed", r)
			}
		})
	}
}

func TestOverviewWritesOnlyConfiguredBodyAndReconcilesIdempotently(t *testing.T) {
	g := config.GitHubIssues{Repository: "owner/repo", OverviewIssue: 7}
	marker := "<!-- runner-overview:instance -->"
	body := marker + "\n\npublic progress"
	for _, scenario := range []string{"valid", "other owner", "request label", "missing labels", "closed", "PR", "no intent", "intent refused", "rejected", "lost response"} {
		t.Run(scenario, func(t *testing.T) {
			i := issueFixture(7, "Dedicated overview body; explicit configuration grants Runner ownership")
			switch scenario {
			case "other owner":
				i["body"] = "<!-- runner-overview:other -->\n"
			case "request label":
				i["labels"] = []any{map[string]string{"name": g.Label()}}
			case "missing labels":
				delete(i, "labels")
			case "closed":
				i["state"] = "closed"
			case "PR":
				i["pull_request"] = map[string]string{"url": "pull"}
			}
			patches := 0
			intents := 0
			h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
				if args[0] != "repos/owner/repo/issues/7" {
					t.Fatal("write/read escaped the configured issue", args)
				}
				if args[2] == "PATCH" {
					patches++
					if intents != 1 {
						t.Fatal("dispatched without exactly one retained intent")
					}
					var payload map[string]string
					if err := json.NewDecoder(input).Decode(&payload); err != nil || len(payload) != 1 || payload["body"] != body {
						t.Fatal("overview changed more than its body", payload, err)
					}
					if scenario == "rejected" {
						return subprocess.Result{ExitCode: 1, Stdout: `{"status":"403"}`}, nil
					}
					i["body"] = payload["body"]
					if scenario == "lost response" {
						return subprocess.Result{ExitCode: 1}, nil
					}
				} else if args[2] != "GET" {
					t.Fatal("unexpected operation", args)
				}
				return issueResponse(i)
			}}
			beforeWrite := func() error {
				intents++
				if scenario == "intent refused" {
					return errors.New("cannot retain intent")
				}
				return nil
			}
			if scenario == "no intent" {
				beforeWrite = nil
			}
			uncertain, err := h.PublishOverview(t.Context(), g, marker, body, beforeWrite)
			if uncertain != (scenario == "lost response") {
				t.Fatal("definite refusal confused with unknown write", uncertain, err)
			}
			if scenario == "valid" || scenario == "lost response" {
				if (err != nil) != (scenario == "lost response") || patches != 1 {
					t.Fatal("update did not retain uncertain outcome", err, patches)
				}
				if got, err := h.ObserveOverview(t.Context(), g, marker); err != nil || got != body {
					t.Fatal("write receipt not observed", err)
				}
				if _, err = h.PublishOverview(t.Context(), g, marker, body, beforeWrite); err != nil || patches != 1 || intents != 1 {
					t.Fatal("idempotent observation repeated the write", err, patches)
				}
			} else if scenario == "rejected" {
				if err == nil || patches != 1 || i["body"] == body {
					t.Fatal("explicit rejection was not retained as unsent", err)
				}
			} else if err == nil || patches != 0 {
				t.Fatal("unsafe overview write", err, patches)
			}
		})
	}
}
