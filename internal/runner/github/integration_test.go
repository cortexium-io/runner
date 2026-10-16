package github

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestIntegrationReadsKnownPREvenAfterBranchDeletion(t *testing.T) {
	intent := core.PublicationIntent{ID: "publication", Repository: "owner/repo", Branch: "runner/publication", BaseBranch: "main", Candidate: core.Candidate{Commit: "head"}}
	p := core.Publication{Intent: intent, Observation: &core.RemotePublication{Number: 7, URL: "https://github.com/owner/repo/pull/7"}, CI: &core.CIAssessment{Proof: core.BaseProof{Base: "base"}, Observation: &core.CIObservation{Commit: "head", Source: "head"}}}
	for _, scenario := range []string{"merged head", "merged test-merge", "open draft", "open ready", "closed", "missing live base", "missing live target", "merged deleted base", "wrong PR", "changed head", "changed base", "changed repository", "missing marker", "missing merge", "missing time", "unknown state", "selected disappeared", "wrong parents", "partial checks"} {
		t.Run(scenario, func(t *testing.T) {
			ci := *p.CI.Observation
			p.CI.Observation = &ci
			ci.Commit, ci.Source = "head", "head"
			if scenario == "merged test-merge" || scenario == "wrong parents" {
				ci.Commit, ci.Source = "test-merge", "test-merge"
			}
			selected := ciCommitFixture(ci.Commit, true)
			selected["parents"] = map[string]any{"totalCount": 2, "nodes": []any{map[string]string{"oid": "base"}, map[string]string{"oid": "head"}}}
			pr := map[string]any{"number": 7, "url": p.Observation.URL, "state": "MERGED", "isDraft": false, "headRefName": intent.Branch, "headRefOid": "head", "baseRefName": "main", "baseRefOid": "old-base", "currentBase": map[string]any{"target": map[string]string{"oid": "later-base"}}, "headRepository": map[string]string{"nameWithOwner": "owner/repo"}, "body": marker(intent), "mergedAt": "2026-10-15T10:00:00Z", "mergeCommit": map[string]string{"oid": "merge"}}
			repo := map[string]any{"nameWithOwner": "owner/repo", "pullRequest": pr, "selected": selected}
			switch scenario {
			case "open draft", "open ready":
				pr["state"], pr["isDraft"] = "OPEN", scenario == "open draft"
			case "closed":
				pr["state"] = "CLOSED"
			case "missing live base", "missing live target":
				pr["state"], pr["currentBase"] = "OPEN", nil
				if scenario == "missing live target" {
					pr["currentBase"] = map[string]any{"target": nil}
				}
			case "merged deleted base":
				pr["currentBase"] = nil
			case "wrong PR":
				pr["number"] = 8
			case "changed head":
				pr["headRefOid"] = "other"
			case "changed base":
				pr["baseRefName"] = "other"
			case "changed repository":
				repo["nameWithOwner"] = "other/repo"
			case "missing marker":
				pr["body"] = "edited"
			case "missing merge":
				pr["mergeCommit"] = nil
			case "missing time":
				pr["mergedAt"] = nil
			case "unknown state":
				pr["state"] = "UNKNOWN"
			case "selected disappeared":
				repo["selected"] = nil
			case "wrong parents":
				selected["parents"] = map[string]any{"totalCount": 1, "nodes": []any{map[string]string{"oid": "head"}}}
			case "partial checks":
				selected["statusCheckRollup"].(map[string]any)["contexts"].(map[string]any)["pageInfo"] = map[string]bool{"hasNextPage": true}
			}
			h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
				var payload struct {
					Query     string
					Variables map[string]any
				}
				if err := json.NewDecoder(input).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if args[0] != "graphql" || payload.Variables["number"] != float64(7) || payload.Variables["commit"] != ci.Commit || strings.Contains(payload.Query, "mutation") || strings.Contains(payload.Query, "ref(qualifiedName") || !strings.Contains(payload.Query, "pullRequest(number:") || !strings.Contains(payload.Query, "currentBase:baseRef{target{oid}}") {
					t.Fatal("inspection lost exact identity or requires deleted branch", payload)
				}
				b, _ := json.Marshal(map[string]any{"data": map[string]any{"repository": repo}})
				return subprocess.Result{Stdout: string(b)}, nil
			}}
			got, err := h.ObserveIntegration(t.Context(), p)
			valid := strings.HasPrefix(scenario, "merged ") || strings.HasPrefix(scenario, "open ") || scenario == "closed"
			if (err == nil) != valid {
				t.Fatal(scenario, got, err)
			}
			if !valid {
				return
			}
			if scenario != "merged deleted base" && got.Base != "later-base" {
				t.Fatal("live base target lost", got)
			}
			if strings.HasPrefix(scenario, "merged ") && (got.MergeCommit != "merge" || !got.MergedAt.Equal(time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)) || len(got.Checks) != 2 || got.Checks[0].AppID != 15368) {
				t.Fatal("merge identity or pinned CI lost", got)
			}
		})
	}
}
