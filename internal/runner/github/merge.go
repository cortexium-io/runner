package github

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/cortexium-io/runner/internal/runner/core"
)

// Use the protection attached to the actual base ref, never a guessed pattern.
const mergePolicyFields = ` id mergeable mergeStateStatus reviewDecision viewerCanMergeAsAdmin isMergeQueueEnabled isInMergeQueue autoMergeRequest{enabledAt} baseRef{branchProtectionRule{id isAdminEnforced requiresStatusChecks requiresStrictStatusChecks requiredStatusCheckContexts requiresApprovingReviews requiresLinearHistory allowsForcePushes allowsDeletions bypassForcePushAllowances(first:1){totalCount} bypassPullRequestAllowances(first:1){totalCount}}}`

const mergeQuery = `query($owner:String!,$repo:String!,$number:Int!,$commit:GitObjectID!){repository(owner:$owner,name:$repo){nameWithOwner mergeCommitAllowed pullRequest(number:$number){` + publicationFields + mergePolicyFields + ` baseRefOid mergedAt mergeCommit{oid}} selected:object(oid:$commit){...CheckCommit}}} ` + checkCommitFields

type allowanceCount struct{ TotalCount *int }
type mergePolicy struct {
	ID, Mergeable, MergeStateStatus                            string
	ReviewDecision                                             json.RawMessage
	ViewerCanMergeAsAdmin, IsMergeQueueEnabled, IsInMergeQueue *bool
	AutoMergeRequest                                           json.RawMessage
	BaseRef                                                    *struct {
		BranchProtectionRule *struct {
			ID                                                                                          string
			IsAdminEnforced, RequiresStatusChecks, RequiresStrictStatusChecks, RequiresApprovingReviews *bool
			RequiresLinearHistory, AllowsForcePushes, AllowsDeletions                                   *bool
			RequiredStatusCheckContexts                                                                 []string
			BypassForcePushAllowances, BypassPullRequestAllowances                                      *allowanceCount
		}
	}
}

func (p mergePolicy) assess(mergeAllowed *bool, draft bool) (core.MergeAdmission, error) {
	a := core.MergeAdmission{PullRequestID: p.ID}
	if p.ID == "" || mergeAllowed == nil || p.ViewerCanMergeAsAdmin == nil || p.IsMergeQueueEnabled == nil || p.IsInMergeQueue == nil || len(p.AutoMergeRequest) == 0 || len(p.ReviewDecision) == 0 || p.Mergeable == "" || p.MergeStateStatus == "" {
		return a, errors.New("merge policy observation incomplete")
	}
	switch {
	case draft:
		a.Detail = "Mark the PR ready through GitHub's review workflow before requesting merge."
	case !*mergeAllowed:
		a.Detail = "Repository does not allow ordinary merge commits."
	case *p.IsMergeQueueEnabled || *p.IsInMergeQueue:
		a.Detail = "Merge queues are outside this bundle."
	case string(p.AutoMergeRequest) != "null":
		a.Detail = "Disable external auto-merge before requesting Runner-owned merge."
	case *p.ViewerCanMergeAsAdmin:
		a.Detail = "Current host identity can bypass protections; merge is refused."
	case p.Mergeable != "MERGEABLE" || p.MergeStateStatus != "CLEAN":
		a.Detail = "Host merge requirements are not clean and mergeable (" + p.MergeStateStatus + "/" + p.Mergeable + ")."
	case string(p.ReviewDecision) != "null" && string(p.ReviewDecision) != `"APPROVED"`:
		a.Detail = "Host review requirements are not satisfied."
	}
	if a.Detail != "" {
		return a, nil
	}
	if p.BaseRef == nil || p.BaseRef.BranchProtectionRule == nil {
		a.Detail = "A strict, non-bypassable classic branch protection rule is required; ruleset-only guards are unsupported."
		return a, nil
	}
	r := p.BaseRef.BranchProtectionRule
	a.ProtectionRuleID = r.ID
	if r.ID == "" || r.IsAdminEnforced == nil || r.RequiresStatusChecks == nil || r.RequiresStrictStatusChecks == nil || r.RequiresApprovingReviews == nil || r.RequiresLinearHistory == nil || r.AllowsForcePushes == nil || r.AllowsDeletions == nil || r.RequiredStatusCheckContexts == nil || r.BypassForcePushAllowances == nil || r.BypassForcePushAllowances.TotalCount == nil || r.BypassPullRequestAllowances == nil || r.BypassPullRequestAllowances.TotalCount == nil {
		return a, errors.New("base protection observation incomplete")
	}
	if *r.RequiresLinearHistory {
		a.Detail = "Base requires linear history; ordinary merge commits are not permitted."
		return a, nil
	}
	if !*r.IsAdminEnforced || !*r.RequiresStatusChecks || !*r.RequiresStrictStatusChecks || len(r.RequiredStatusCheckContexts) == 0 || *r.AllowsForcePushes || *r.AllowsDeletions || *r.BypassForcePushAllowances.TotalCount != 0 || *r.BypassPullRequestAllowances.TotalCount != 0 {
		a.Detail = "Base requires enforced strict status checks, at least one required check, and no force-push, deletion or bypass allowances."
		return a, nil
	}
	if *r.RequiresApprovingReviews && string(p.ReviewDecision) != `"APPROVED"` {
		a.Detail = "Required host reviews are not approved."
		return a, nil
	}
	a.Eligible, a.Detail = true, "Host admits an ordinary merge under enforced strict base protection; pinned CI and local authority must also pass."
	return a, nil
}

const mergeMutation = `mutation($input:MergePullRequestInput!){mergePullRequest(input:$input){pullRequest{number state headRefOid mergeCommit{oid}}}}`

func (h Host) Merge(ctx context.Context, p core.Publication, a core.MergeAdmission, authorize func() error) error {
	if p.MergeDispatch == nil || p.IntegrationRequest == nil || !p.IntegrationRequest.Merge || !a.Eligible || a.PullRequestID == "" || a.ProtectionRuleID == "" || p.Observation == nil || core.Digest(a) != core.Digest(p.MergeDispatch.Admission) || core.Digest(p.IntegrationRequest) != core.Digest(p.MergeDispatch.Request) {
		return errors.New("merge lacks a durable authorized dispatch and host admission")
	}
	if err := authorize(); err != nil {
		return err
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			MergePullRequest *struct {
				PullRequest *struct {
					Number            int
					State, HeadRefOid string
					MergeCommit       *struct{ OID string }
				}
			}
		}
	}
	input := map[string]any{"pullRequestId": a.PullRequestID, "expectedHeadOid": p.Intent.Candidate.Commit, "mergeMethod": "MERGE"}
	if err := h.api(ctx, "graphql", map[string]any{"query": mergeMutation, "variables": map[string]any{"input": input}}, &response); err != nil {
		return err
	}
	r := response.Data.MergePullRequest
	if len(response.Errors) != 0 || r == nil || r.PullRequest == nil || r.PullRequest.Number != p.Observation.Number || r.PullRequest.State != "MERGED" || r.PullRequest.HeadRefOid != p.Intent.Candidate.Commit || r.PullRequest.MergeCommit == nil || r.PullRequest.MergeCommit.OID == "" {
		return errors.New("merge response did not confirm the expected PR; inspect the outcome without repeating the write")
	}
	return nil // The engine still requires a fresh read and real Git receipt.
}
