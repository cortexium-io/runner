package github

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
)

const integrationQuery = `query($owner:String!,$repo:String!,$number:Int!,$commit:GitObjectID!){repository(owner:$owner,name:$repo){nameWithOwner pullRequest(number:$number){` + publicationFields + ` baseRefOid mergedAt mergeCommit{oid}} selected:object(oid:$commit){...CheckCommit}}} ` + checkCommitFields

func (h Host) ObserveIntegration(ctx context.Context, p core.Publication) (core.IntegrationObservation, error) {
	o, err := h.observeIntegration(ctx, p, false)
	return o.IntegrationObservation, err
}

func (h Host) ObserveMerge(ctx context.Context, p core.Publication) (core.MergeObservation, error) {
	return h.observeIntegration(ctx, p, true)
}

func (h Host) observeIntegration(ctx context.Context, p core.Publication, merge bool) (core.MergeObservation, error) {
	var result core.MergeObservation
	o := &result.IntegrationObservation
	if p.Observation == nil || p.Observation.Number < 1 || p.CI == nil || p.CI.Observation == nil {
		return result, errors.New("integration requires a known PR and selected CI commit")
	}
	owner, repo, ok := strings.Cut(p.Intent.Repository, "/")
	if !ok {
		return result, errors.New("invalid repository")
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner      string
				MergeCommitAllowed *bool
				PullRequest        *struct {
					remotePR
					mergePolicy
					Draft       *bool `json:"isDraft"`
					MergedAt    *time.Time
					MergeCommit *struct{ OID string }
				}
				Selected *checkCommit
			}
		}
	}
	selected := p.CI.Observation
	query := integrationQuery
	if merge {
		query = mergeQuery
	}
	if err := h.api(ctx, "graphql", map[string]any{"query": query, "variables": map[string]any{"owner": owner, "repo": repo, "number": p.Observation.Number, "commit": selected.Commit}}, &response); err != nil {
		return result, err
	}
	r := response.Data.Repository
	if len(response.Errors) > 0 || r == nil || !strings.EqualFold(r.NameWithOwner, p.Intent.Repository) || r.PullRequest == nil {
		return result, errors.New("integration observation is incomplete")
	}
	pr := r.PullRequest
	if pr.Draft == nil {
		return result, errors.New("PR draft state missing")
	}
	pr.IsDraft = *pr.Draft
	if pr.Number != p.Observation.Number || pr.URL != p.Observation.URL || pr.URL != "https://github.com/"+r.NameWithOwner+"/pull/"+strconv.Itoa(pr.Number) || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, p.Intent.Repository) || pr.HeadRefName != p.Intent.Branch || pr.HeadRefOid != p.Intent.Candidate.Commit || pr.BaseRefName != p.Intent.BaseBranch || !strings.Contains(pr.Body, marker(p.Intent)) {
		return result, errors.New("integration PR identity, candidate, base or marker changed")
	}
	o.Remote = core.RemotePublication{Number: pr.Number, URL: pr.URL, State: pr.State, Draft: pr.IsDraft, BranchCommit: pr.HeadRefOid}
	o.Base = pr.BaseRefOid
	if pr.State == "OPEN" || pr.State == "CLOSED" {
		if o.Base == "" {
			return result, errors.New("integration base observation missing")
		}
		if !merge || pr.State == "CLOSED" {
			return result, nil
		}
		var err error
		result.Admission, err = pr.mergePolicy.assess(r.MergeCommitAllowed, pr.IsDraft)
		if err != nil {
			return result, err
		}
	}
	if pr.State != "OPEN" && (pr.State != "MERGED" || pr.IsDraft || pr.MergedAt == nil || pr.MergedAt.IsZero() || pr.MergeCommit == nil || pr.MergeCommit.OID == "") {
		return result, errors.New("merged PR observation is incomplete or state is unknown")
	}
	if pr.State == "MERGED" {
		o.MergeCommit, o.MergedAt = pr.MergeCommit.OID, *pr.MergedAt
	}
	if r.Selected == nil || r.Selected.OID != selected.Commit {
		return result, errors.New("selected CI commit unavailable")
	}
	switch selected.Source {
	case "head":
		if selected.Commit != p.Intent.Candidate.Commit {
			return result, errors.New("selected CI head differs from candidate")
		}
	case "test-merge":
		parents := r.Selected.Parents
		if parents == nil || parents.TotalCount != 2 || len(parents.Nodes) != 2 || !((parents.Nodes[0].OID == p.CI.Proof.Base && parents.Nodes[1].OID == p.Intent.Candidate.Commit) || (parents.Nodes[1].OID == p.CI.Proof.Base && parents.Nodes[0].OID == p.Intent.Candidate.Commit)) {
			return result, errors.New("selected CI test merge no longer binds the base and candidate")
		}
	default:
		return result, errors.New("unknown selected CI source")
	}
	checks, err := r.Selected.checks()
	if err != nil {
		return result, err
	}
	o.Checks = checks
	return result, nil
}
