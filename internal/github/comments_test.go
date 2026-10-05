package github

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type commentBodyRunner struct{ body string }

func (r commentBodyRunner) Run(_ context.Context, _ string, args []string, _ string, _ time.Duration) (subprocess.Result, error) {
	if strings.Join(args, " ") == "api user --jq .login" {
		return subprocess.Result{Stdout: "dan"}, nil
	}
	nodes := []any{
		map[string]any{"author": map[string]string{"login": "dan"}, "body": r.body},
		map[string]any{"author": map[string]string{"login": "someone-else"}, "body": r.body},
	}
	encoded, err := json.Marshal(map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]any{"comments": map[string]any{"nodes": nodes}}}}})
	return subprocess.Result{Stdout: string(encoded)}, err
}

func TestBoundedCommentRetainsExactFullBodyIdentity(t *testing.T) {
	body := "<!-- cortexium-runner:qa:accepted -->\n\n" + strings.Repeat("Evidence æøå. ", 800)
	item := WorkItem{URL: "https://github.com/owner/repo/issues/1"}
	for _, tc := range []struct {
		name, observed string
		matches        bool
	}{
		{"exact", body, true},
		{"changed suffix", body + " Additional proof required.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := NewProject(config.ProjectConfig{}, commentBodyRunner{tc.observed})
			comments, err := p.ItemComments(t.Context(), item)
			if err != nil || len(comments) != 1 {
				t.Fatalf("actor-filtered comments: %+v %v", comments, err)
			}
			comment := comments[0]
			if len(comment.Body) > maxAssignmentCommentBody || len(comment.Body) >= len(body) {
				t.Fatal("prompt body was not bounded")
			}
			if comment.MatchesBody(body) != tc.matches {
				t.Fatal("bounded prefix was substituted for exact full-body identity")
			}
		})
	}
}
