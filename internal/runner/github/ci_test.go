package github

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/subprocess"
)

func TestCIObservesExactCompleteHeadOrTestMerge(t *testing.T) {
	p := core.PublicationIntent{ID: "publication", Repository: "owner/repo", Branch: "runner-next/publication", BaseBranch: "main", Candidate: core.Candidate{Commit: "head"}}

	for _, scenario := range []string{"head", "merge", "no merge checks", "no checks", "wrong head", "wrong base branch", "missing marker", "closed", "ready", "wrong merge parents", "partial", "omitted rollup", "missing merge observation", "missing live base", "missing live target", "unknown type", "wrong check commit", "API errors"} {
		t.Run(scenario, func(t *testing.T) {
			head := ciCommitFixture("head", scenario != "no checks")
			merge := ciCommitFixture("merge", scenario == "merge")
			merge["parents"] = map[string]any{"totalCount": 2, "nodes": []any{map[string]string{"oid": "base"}, map[string]string{"oid": "head"}}}
			pr := map[string]any{"number": 7, "url": "https://github.com/owner/repo/pull/7", "state": "OPEN", "isDraft": true, "headRefName": p.Branch, "headRefOid": "head", "baseRefName": "main", "baseRefOid": "old-base", "currentBase": map[string]any{"target": map[string]string{"oid": "base"}}, "body": marker(p), "headRepository": map[string]string{"nameWithOwner": "owner/repo"}, "potentialMergeCommit": nil}
			repo := map[string]any{"nameWithOwner": "owner/repo", "ref": map[string]any{"target": map[string]string{"oid": "head"}}, "candidate": head, "pullRequests": map[string]any{"nodes": []any{pr}}}
			switch scenario {
			case "merge", "no merge checks":
				pr["potentialMergeCommit"] = merge
			case "missing live base":
				pr["currentBase"] = nil
			case "missing live target":
				pr["currentBase"] = map[string]any{"target": nil}
			case "wrong head":
				pr["headRefOid"] = "other"
			case "wrong base branch":
				pr["baseRefName"] = "other"
			case "missing marker":
				pr["body"] = "changed"
			case "closed":
				pr["state"] = "CLOSED"
			case "ready":
				pr["isDraft"] = false
			case "wrong merge parents":
				merge["parents"] = map[string]any{"totalCount": 1, "nodes": []any{map[string]string{"oid": "head"}}}
				pr["potentialMergeCommit"] = merge
			case "partial":
				head["statusCheckRollup"].(map[string]any)["contexts"].(map[string]any)["pageInfo"] = map[string]bool{"hasNextPage": true}
			case "omitted rollup":
				delete(head, "statusCheckRollup")
			case "missing merge observation":
				delete(pr, "potentialMergeCommit")
			case "unknown type", "wrong check commit":
				node := head["statusCheckRollup"].(map[string]any)["contexts"].(map[string]any)["nodes"].([]any)[1].(map[string]any)
				if scenario == "unknown type" {
					node["__typename"] = "Other"
				} else {
					node["commit"] = map[string]string{"oid": "old"}
				}
			}
			h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
				var payload struct {
					Query     string
					Variables map[string]string
				}
				if err := json.NewDecoder(input).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if args[0] != "graphql" || payload.Variables["commit"] != "head" || !strings.Contains(payload.Query, "contexts(first:100)") || !strings.Contains(payload.Query, "currentBase:baseRef{target{oid}}") || strings.Contains(payload.Query, "mutation") {
					t.Fatal("CI read scope changed")
				}
				response := map[string]any{"data": map[string]any{"repository": repo}}
				if scenario == "API errors" {
					response["errors"] = []any{map[string]string{"message": "partial data"}}
				}
				b, _ := json.Marshal(response)
				return subprocess.Result{Stdout: string(b)}, nil
			}}
			observation, err := h.ObserveCI(t.Context(), p)
			valid := scenario == "head" || scenario == "merge" || scenario == "no merge checks" || scenario == "no checks"
			if (err == nil) != valid {
				t.Fatal(scenario, observation, err)
			}
			if !valid {
				return
			}
			want := "head"
			if scenario == "merge" {
				want = "test-merge"
			}
			if observation.Source != want || observation.Base != "base" {
				t.Fatal(observation)
			}
			if scenario == "no checks" {
				if len(observation.Checks) != 0 {
					t.Fatal("fabricated checks")
				}
				return
			}
			if observation.Checks[0].AppID != 15368 || observation.Checks[1].URL != "" {
				t.Fatal("producer lost or unsafe URL retained")
			}
		})
	}
}

func ciCommitFixture(oid string, hasChecks bool) map[string]any {
	c := map[string]any{"oid": oid, "statusCheckRollup": nil}
	if hasChecks {
		c["statusCheckRollup"] = map[string]any{"commit": map[string]string{"oid": oid}, "contexts": map[string]any{"totalCount": 2, "pageInfo": map[string]bool{"hasNextPage": false}, "nodes": []any{
			map[string]any{"__typename": "CheckRun", "name": "test", "status": "COMPLETED", "conclusion": "SUCCESS", "detailsUrl": "https://github.com/owner/repo/actions/runs/1", "checkSuite": map[string]any{"app": map[string]int{"databaseId": 15368}, "commit": map[string]string{"oid": oid}}},
			map[string]any{"__typename": "StatusContext", "context": "legacy", "state": "PENDING", "targetUrl": "javascript:bad", "commit": map[string]string{"oid": oid}},
		}}}
	}
	return c
}
