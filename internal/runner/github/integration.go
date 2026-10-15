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
	var o core.IntegrationObservation
	if p.Observation == nil || p.Observation.Number < 1 || p.CI == nil || p.CI.Observation == nil {
		return o, errors.New("integration requires a known PR and selected CI commit")
	}
	owner, repo, ok := strings.Cut(p.Intent.Repository, "/")
	if !ok {
		return o, errors.New("invalid repository")
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner string
				PullRequest   *struct {
					remotePR
					MergedAt    *time.Time
					MergeCommit *struct{ OID string }
				}
				Selected *checkCommit
			}
		}
	}
	selected := p.CI.Observation
	if err := h.api(ctx, "graphql", map[string]any{"query": integrationQuery, "variables": map[string]any{"owner": owner, "repo": repo, "number": p.Observation.Number, "commit": selected.Commit}}, &response); err != nil {
		return o, err
	}
	r := response.Data.Repository
	if len(response.Errors) > 0 || r == nil || !strings.EqualFold(r.NameWithOwner, p.Intent.Repository) || r.PullRequest == nil {
		return o, errors.New("integration observation is incomplete")
	}
	pr := r.PullRequest
	if pr.Number != p.Observation.Number || pr.URL != p.Observation.URL || pr.URL != "https://github.com/"+r.NameWithOwner+"/pull/"+strconv.Itoa(pr.Number) || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, p.Intent.Repository) || pr.HeadRefName != p.Intent.Branch || pr.HeadRefOid != p.Intent.Candidate.Commit || pr.BaseRefName != p.Intent.BaseBranch || !strings.Contains(pr.Body, marker(p.Intent)) {
		return o, errors.New("integration PR identity, candidate, base or marker changed")
	}
	o.Remote = core.RemotePublication{Number: pr.Number, URL: pr.URL, State: pr.State, Draft: pr.IsDraft, BranchCommit: pr.HeadRefOid}
	o.Base = pr.BaseRefOid
	if pr.State == "OPEN" || pr.State == "CLOSED" {
		if o.Base == "" {
			return o, errors.New("integration base observation missing")
		}
		return o, nil
	}
	if pr.State != "MERGED" || pr.IsDraft || pr.MergedAt == nil || pr.MergedAt.IsZero() || pr.MergeCommit == nil || pr.MergeCommit.OID == "" {
		return o, errors.New("merged PR observation is incomplete or state is unknown")
	}
	o.MergeCommit, o.MergedAt = pr.MergeCommit.OID, *pr.MergedAt
	if r.Selected == nil || r.Selected.OID != selected.Commit {
		return o, errors.New("selected CI commit unavailable")
	}
	switch selected.Source {
	case "head":
		if selected.Commit != p.Intent.Candidate.Commit {
			return o, errors.New("selected CI head differs from candidate")
		}
	case "test-merge":
		parents := r.Selected.Parents
		if parents == nil || parents.TotalCount != 2 || len(parents.Nodes) != 2 || !((parents.Nodes[0].OID == p.CI.Proof.Base && parents.Nodes[1].OID == p.Intent.Candidate.Commit) || (parents.Nodes[1].OID == p.CI.Proof.Base && parents.Nodes[0].OID == p.Intent.Candidate.Commit)) {
			return o, errors.New("selected CI test merge no longer binds the base and candidate")
		}
	default:
		return o, errors.New("unknown selected CI source")
	}
	checks, err := r.Selected.checks()
	if err != nil {
		return o, err
	}
	o.Checks = checks
	return o, nil
}
