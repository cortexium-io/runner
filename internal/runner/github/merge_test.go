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

func mergeFixture() (core.Publication, map[string]any, map[string]any, map[string]any) {
	intent := core.PublicationIntent{ID: "publication", Repository: "owner/repo", Branch: "runner-next/publication", BaseBranch: "main", Candidate: core.Candidate{Commit: "head"}}
	p := core.Publication{Intent: intent, Observation: &core.RemotePublication{Number: 7, URL: "https://github.com/owner/repo/pull/7"}, CI: &core.CIAssessment{Proof: core.BaseProof{Base: "base"}, Observation: &core.CIObservation{Commit: "head", Source: "head"}}}
	rule := map[string]any{"id": "rule", "isAdminEnforced": true, "requiresStatusChecks": true, "requiresStrictStatusChecks": true, "requiredStatusCheckContexts": []string{"test"}, "requiresApprovingReviews": true, "requiresLinearHistory": false, "allowsForcePushes": false, "allowsDeletions": false, "bypassForcePushAllowances": map[string]int{"totalCount": 0}, "bypassPullRequestAllowances": map[string]int{"totalCount": 0}}
	pr := map[string]any{"id": "PR7", "number": 7, "url": p.Observation.URL, "state": "OPEN", "isDraft": false, "headRefName": intent.Branch, "headRefOid": "head", "baseRefName": "main", "baseRefOid": "old-base", "currentBase": map[string]any{"target": map[string]string{"oid": "base"}}, "body": marker(intent), "headRepository": map[string]string{"nameWithOwner": "owner/repo"}, "mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN", "reviewDecision": "APPROVED", "viewerCanMergeAsAdmin": false, "isMergeQueueEnabled": false, "isInMergeQueue": false, "autoMergeRequest": nil, "baseRef": map[string]any{"branchProtectionRule": rule}}
	repo := map[string]any{"nameWithOwner": "owner/repo", "mergeCommitAllowed": true, "pullRequest": pr, "selected": ciCommitFixture("head", true)}
	return p, repo, pr, rule
}

func TestMergeAdmissionRequiresCompleteProtectedHostState(t *testing.T) {
	for _, scenario := range []string{"admitted", "no reviews required", "draft", "missing draft", "queue", "in queue", "auto merge", "bypass", "merge disabled", "review required", "changes requested", "unknown review", "blocked", "unknown mergeability", "missing queue", "missing auto merge", "missing review", "missing base", "missing live base", "missing live target", "no protection", "loose checks", "no checks", "empty checks", "admin bypass", "linear history", "force push", "deletions", "force allowance", "PR allowance", "missing allowance", "missing enforcement", "identity drift", "CI omitted", "GraphQL error"} {
		t.Run(scenario, func(t *testing.T) {
			p, repo, pr, rule := mergeFixture()
			switch scenario {
			case "no reviews required":
				rule["requiresApprovingReviews"], pr["reviewDecision"] = false, nil
			case "draft":
				pr["isDraft"] = true
			case "missing draft":
				delete(pr, "isDraft")
			case "queue":
				pr["isMergeQueueEnabled"] = true
			case "in queue":
				pr["isInMergeQueue"] = true
			case "auto merge":
				pr["autoMergeRequest"] = map[string]string{"enabledAt": "2026-10-15T10:00:00Z"}
			case "bypass":
				pr["viewerCanMergeAsAdmin"] = true
			case "merge disabled":
				repo["mergeCommitAllowed"] = false
			case "review required":
				pr["reviewDecision"] = nil
			case "changes requested":
				pr["reviewDecision"] = "CHANGES_REQUESTED"
			case "unknown review":
				pr["reviewDecision"] = "FUTURE_STATE"
			case "blocked":
				pr["mergeStateStatus"] = "BLOCKED"
			case "unknown mergeability":
				pr["mergeable"] = "UNKNOWN"
			case "missing queue":
				delete(pr, "isMergeQueueEnabled")
			case "missing auto merge":
				delete(pr, "autoMergeRequest")
			case "missing review":
				delete(pr, "reviewDecision")
			case "missing live base":
				pr["currentBase"] = nil
			case "missing live target":
				pr["currentBase"] = map[string]any{"target": nil}
			case "missing base":
				pr["baseRef"] = nil
			case "no protection":
				pr["baseRef"] = map[string]any{"branchProtectionRule": nil}
			case "loose checks":
				rule["requiresStrictStatusChecks"] = false
			case "no checks":
				rule["requiresStatusChecks"] = false
			case "empty checks":
				rule["requiredStatusCheckContexts"] = []string{}
			case "admin bypass":
				rule["isAdminEnforced"] = false
			case "linear history":
				rule["requiresLinearHistory"] = true
			case "force push":
				rule["allowsForcePushes"] = true
			case "deletions":
				rule["allowsDeletions"] = true
			case "force allowance":
				rule["bypassForcePushAllowances"] = map[string]int{"totalCount": 1}
			case "PR allowance":
				rule["bypassPullRequestAllowances"] = map[string]int{"totalCount": 1}
			case "missing allowance":
				rule["bypassPullRequestAllowances"] = map[string]int{}
			case "missing enforcement":
				delete(rule, "isAdminEnforced")
			case "identity drift":
				pr["headRefOid"] = "other"
			case "CI omitted":
				repo["selected"] = nil
			}
			h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
				var payload struct {
					Query     string
					Variables map[string]any
				}
				if err := json.NewDecoder(input).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if args[0] != "graphql" || payload.Query != mergeQuery || !strings.Contains(payload.Query, "currentBase:baseRef{target{oid}}") || payload.Variables["number"] != float64(7) || payload.Variables["commit"] != "head" || strings.Contains(payload.Query, "mutation") {
					t.Fatal("wrong admission query", payload)
				}
				response := map[string]any{"data": map[string]any{"repository": repo}}
				if scenario == "GraphQL error" {
					response["errors"] = []any{map[string]string{"message": "partial"}}
				}
				b, _ := json.Marshal(response)
				return subprocess.Result{Stdout: string(b)}, nil
			}}
			o, err := h.ObserveMerge(t.Context(), p)
			want := scenario == "admitted" || scenario == "no reviews required"
			if (err == nil && o.Admission.Eligible) != want {
				t.Fatal(scenario, o, err)
			}
			if want && (o.Admission.PullRequestID != "PR7" || o.Admission.ProtectionRuleID != "rule" || o.Base != "base" || len(o.Checks) != 2) {
				t.Fatal("incomplete bound admission", o)
			}
		})
	}
}

func TestMergeSendsOneConditionalMutationAfterAuthorization(t *testing.T) {
	p, _, _, _ := mergeFixture()
	p.IntegrationRequest = &core.IntegrationRequest{Merge: true}
	admission := core.MergeAdmission{Eligible: true, PullRequestID: "PR7", ProtectionRuleID: "rule"}
	p.MergeDispatch = &core.MergeDispatch{Request: *p.IntegrationRequest, Admission: admission}
	calls, authorized := 0, false
	response := `{"data":{"mergePullRequest":{"pullRequest":{"number":7,"state":"MERGED","headRefOid":"head","mergeCommit":{"oid":"merge"}}}}}`
	h := Host{request: func(_ context.Context, args []string, input io.Reader) (subprocess.Result, error) {
		calls++
		var payload struct {
			Query     string
			Variables struct{ Input map[string]string }
		}
		if err := json.NewDecoder(input).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		v := payload.Variables.Input
		if !authorized || args[0] != "graphql" || payload.Query != mergeMutation || len(v) != 3 || v["pullRequestId"] != "PR7" || v["expectedHeadOid"] != "head" || v["mergeMethod"] != "MERGE" {
			t.Fatal("unsafe mutation", payload)
		}
		return subprocess.Result{Stdout: response}, nil
	}}
	if err := h.Merge(t.Context(), p, admission, func() error { return errors.New("stopped") }); err == nil || calls != 0 {
		t.Fatal("revoked authority reached host", err, calls)
	}
	authorize := func() error { authorized = true; return nil }
	if err := h.Merge(t.Context(), p, admission, authorize); err != nil || calls != 1 {
		t.Fatal(err, calls)
	}
	for _, raw := range []string{`{}`, `{"errors":[{"message":"head changed"}]}`, `{"data":{"mergePullRequest":{"pullRequest":{"number":8,"state":"MERGED"}}}}`, `invalid`} {
		before := calls
		response = raw
		if err := h.Merge(t.Context(), p, admission, authorize); err == nil || calls != before+1 {
			t.Fatal("failure repeated or accepted", err, calls)
		}
	}
	wrong := admission
	wrong.PullRequestID = "PR8"
	beforeMismatch := calls
	if err := h.Merge(t.Context(), p, wrong, authorize); err == nil || calls != beforeMismatch {
		t.Fatal("mutation differs from recorded admission", err)
	}
	p.MergeDispatch = nil
	before := calls
	if err := h.Merge(t.Context(), p, admission, authorize); err == nil || calls != before {
		t.Fatal("unrecorded dispatch sent", err)
	}
}
