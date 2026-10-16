package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestObserveRequiresCompleteExactRemoteIdentity(t *testing.T) {
	p := core.PublicationIntent{ID: "operation", Repository: "owner/repo", Branch: "runner/operation", BaseBranch: "main", Candidate: core.Candidate{Commit: "accepted"}}
	pr := map[string]any{"number": 7, "url": "https://github.com/owner/repo/pull/7", "state": "OPEN", "isDraft": true, "headRefName": p.Branch, "headRefOid": "accepted", "baseRefName": "main", "body": marker(p), "headRepository": map[string]string{"nameWithOwner": "owner/repo"}}
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		valid  bool
	}{
		{"exact", func(map[string]any, map[string]any) {}, true},
		{"other head", func(_ map[string]any, p map[string]any) { p["headRefOid"] = "other" }, false},
		{"other base", func(_ map[string]any, p map[string]any) { p["baseRefName"] = "elsewhere" }, false},
		{"foreign URL", func(_ map[string]any, p map[string]any) { p["url"] = "https://example.invalid/pull/7" }, false},
		{"fork", func(_ map[string]any, p map[string]any) {
			p["headRepository"] = map[string]string{"nameWithOwner": "other/repo"}
		}, false},
		{"missing marker", func(_ map[string]any, p map[string]any) { p["body"] = "user PR" }, false},
		{"duplicate", func(r map[string]any, p map[string]any) { r["pullRequests"] = map[string]any{"nodes": []any{p, p}} }, false},
		{"unavailable repository", func(r map[string]any, _ map[string]any) { r["nameWithOwner"] = "other/repo" }, false},
		{"missing connection", func(r map[string]any, _ map[string]any) { delete(r, "pullRequests") }, false},
		{"missing ref observation", func(r map[string]any, _ map[string]any) { delete(r, "ref") }, false},
		{"confirmed absence", func(r map[string]any, _ map[string]any) {
			r["ref"] = nil
			r["pullRequests"] = map[string]any{"nodes": []any{}}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := map[string]any{}
			for k, v := range pr {
				candidate[k] = v
			}
			repo := map[string]any{"nameWithOwner": "owner/repo", "ref": map[string]any{"target": map[string]string{"oid": "accepted"}}, "pullRequests": map[string]any{"nodes": []any{candidate}}}
			tc.change(repo, candidate)
			h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
				var payload struct {
					Query     string
					Variables map[string]string
				}
				if err := json.NewDecoder(input).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if args[0] != "graphql" || payload.Variables["branch"] != p.Branch || !strings.Contains(payload.Query, "states:[OPEN,CLOSED,MERGED]") {
					t.Fatal("observation scope changed")
				}
				b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repo}})
				return subprocess.Result{Stdout: string(b)}, nil
			}}
			_, err := h.Observe(t.Context(), p)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t: %v", tc.valid, err)
			}
		})
	}
	// Synchronization observes the original marker and known PR at either end
	// of the leased update, never an arbitrary PR reusing the branch.
	p.ID, p.Candidate.Commit = "update", "refreshed"
	p.Sync = &core.DraftSync{PublicationID: "operation", PreviousCommit: "accepted", Number: 7, URL: "https://github.com/owner/repo/pull/7"}
	for _, scenario := range []string{"previous", "target", "external", "inconsistent ref", "other PR", "wrong marker"} {
		t.Run("sync "+scenario, func(t *testing.T) {
			candidate := map[string]any{}
			for k, v := range pr {
				candidate[k] = v
			}
			head := "accepted"
			switch scenario {
			case "target":
				head = "refreshed"
				candidate["headRefOid"] = head
			case "external":
				head = "external"
				candidate["headRefOid"] = head
			case "inconsistent ref":
				head = "refreshed"
			case "other PR":
				candidate["number"] = 8
				candidate["url"] = "https://github.com/owner/repo/pull/8"
			case "wrong marker":
				candidate["body"] = "<!-- runner-publication:update -->"
			}
			repo := map[string]any{"nameWithOwner": "owner/repo", "ref": map[string]any{"target": map[string]string{"oid": head}}, "pullRequests": map[string]any{"nodes": []any{candidate}}}
			h := Host{request: func(context.Context, []string, io.Reader) (subprocess.Result, error) {
				b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repo}})
				return subprocess.Result{Stdout: string(b)}, nil
			}}
			_, err := h.Observe(t.Context(), p)
			valid := scenario == "previous" || scenario == "target"
			if (err == nil) != valid {
				t.Fatalf("valid=%t: %v", valid, err)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"errors":[{"message":"denied"}]}`, `not JSON`} {
		h := Host{request: func(context.Context, []string, io.Reader) (subprocess.Result, error) {
			return subprocess.Result{Stdout: raw}, nil
		}}
		if _, err := h.Observe(t.Context(), p); err == nil {
			t.Fatalf("missing observation accepted: %s", raw)
		}
	}
}

func TestCreateUsesOneDraftRequestAfterAuthorityRefresh(t *testing.T) {
	p := core.PublicationIntent{ID: "operation", Repository: "owner/repo", Branch: "runner/operation", BaseBranch: "main", Title: "literal `title`", Candidate: core.Candidate{Commit: "accepted"}}
	allowed := false
	calls := 0
	h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
		calls++
		if !allowed || args[0] != "repos/owner/repo/pulls" {
			t.Fatal("wrong authority/destination")
		}
		var payload map[string]any
		if err := json.NewDecoder(input).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["draft"] != true || payload["maintainer_can_modify"] != false || payload["head"] != p.Branch || payload["base"] != "main" || payload["title"] != p.Title || !strings.Contains(payload["body"].(string), marker(p)) {
			t.Fatalf("unsafe payload: %v", payload)
		}
		return subprocess.Result{ExitCode: 1}, errors.New("lost response")
	}}
	if h.CreateDraft(t.Context(), p, func() error { return errors.New("revoked") }) == nil || calls != 0 {
		t.Fatal("revoked create dispatched")
	}
	if h.CreateDraft(t.Context(), p, func() error { allowed = true; return nil }) == nil || calls != 1 {
		t.Fatal("failed create retried")
	}
	p.Sync = &core.DraftSync{PublicationID: "original"}
	if h.CreateDraft(t.Context(), p, func() error { return nil }) == nil || calls != 1 {
		t.Fatal("synchronization created a new PR")
	}
}
