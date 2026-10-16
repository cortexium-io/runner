package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/cortexium-io/runner/internal/runner/core"
)

const checkCommitFields = `fragment CheckCommit on Commit {oid parents(first:2){totalCount nodes{oid}} statusCheckRollup{commit{oid} contexts(first:100){totalCount pageInfo{hasNextPage} nodes{__typename ... on CheckRun {name status conclusion detailsUrl checkSuite{app{databaseId} commit{oid}}} ... on StatusContext {context state targetUrl commit{oid}}}}}}`
const ciQuery = `query($owner:String!,$repo:String!,$branch:String!,$ref:String!,$commit:GitObjectID!){repository(owner:$owner,name:$repo){nameWithOwner ref(qualifiedName:$ref){target{oid}} candidate:object(oid:$commit){...CheckCommit} pullRequests(headRefName:$branch,first:2,states:[OPEN,CLOSED,MERGED]){nodes{` + publicationFields + currentBaseFields + ` potentialMergeCommit{...CheckCommit}} pageInfo{hasNextPage}}}} ` + checkCommitFields

type checkCommit struct {
	OID     string
	Parents *struct {
		TotalCount int
		Nodes      []struct{ OID string }
	}
	StatusCheckRollup json.RawMessage
}

func (h Host) ObserveCI(ctx context.Context, p core.PublicationIntent) (core.CIObservation, error) {
	var observation core.CIObservation
	r, err := h.readRemote(ctx, p, true)
	if err != nil {
		return observation, err
	}
	remote, err := validateRemote(p, r)
	if err != nil {
		return observation, err
	}
	if remote.Number <= 0 || remote.State != "OPEN" || !remote.Draft || remote.BranchCommit != p.Candidate.Commit {
		return observation, errors.New("CI requires the exact open draft and candidate head")
	}
	pr := r.PullRequests.Nodes[0]
	if pr.HeadRefOid != p.Candidate.Commit || pr.currentBase() == "" || r.Candidate == nil || r.Candidate.OID != p.Candidate.Commit || len(pr.PotentialMergeCommit) == 0 {
		return observation, errors.New("CI commit/base observation is incomplete")
	}
	head, err := r.Candidate.checks()
	if err != nil {
		return observation, err
	}
	observation = core.CIObservation{Remote: remote, Base: pr.currentBase(), Commit: p.Candidate.Commit, Source: "head", Checks: head}
	if string(pr.PotentialMergeCommit) != "null" {
		var merge checkCommit
		if err = json.Unmarshal(pr.PotentialMergeCommit, &merge); err != nil {
			return observation, errors.New("invalid test merge commit observation")
		}
		if merge.OID == "" || merge.Parents == nil || merge.Parents.TotalCount != 2 || len(merge.Parents.Nodes) != 2 ||
			!((merge.Parents.Nodes[0].OID == pr.currentBase() && merge.Parents.Nodes[1].OID == p.Candidate.Commit) || (merge.Parents.Nodes[1].OID == pr.currentBase() && merge.Parents.Nodes[0].OID == p.Candidate.Commit)) {
			return observation, errors.New("test merge commit does not bind the observed base and candidate")
		}
		checks, err := merge.checks()
		if err != nil {
			return observation, err
		}
		if len(checks) > 0 {
			observation.Commit, observation.Source, observation.Checks = merge.OID, "test-merge", checks
		}
	}
	return observation, nil
}

func (c checkCommit) checks() ([]core.RemoteCheck, error) {
	if len(c.StatusCheckRollup) == 0 {
		return nil, errors.New("CI rollup was omitted")
	}
	if string(c.StatusCheckRollup) == "null" {
		return []core.RemoteCheck{}, nil
	}
	var rollup struct {
		Commit   *struct{ OID string }
		Contexts *struct {
			TotalCount *int
			PageInfo   *struct{ HasNextPage *bool }
			Nodes      []struct {
				Type                                                            string `json:"__typename"`
				Name, Context, Status, Conclusion, State, DetailsURL, TargetURL string
				Commit                                                          *struct{ OID string }
				CheckSuite                                                      *struct {
					App    *struct{ DatabaseID int64 }
					Commit *struct{ OID string }
				}
			}
		}
	}
	if err := json.Unmarshal(c.StatusCheckRollup, &rollup); err != nil {
		return nil, errors.New("invalid CI rollup")
	}
	r := rollup.Contexts
	if rollup.Commit == nil || rollup.Commit.OID != c.OID || r == nil || r.TotalCount == nil || *r.TotalCount > 100 || r.Nodes == nil || len(r.Nodes) != *r.TotalCount || r.PageInfo == nil || r.PageInfo.HasNextPage == nil || *r.PageInfo.HasNextPage {
		return nil, errors.New("CI rollup is incomplete or exceeds 100 contexts; partial results cannot pass")
	}
	checks := make([]core.RemoteCheck, 0, len(r.Nodes))
	for _, n := range r.Nodes {
		check := core.RemoteCheck{}
		switch n.Type {
		case "CheckRun":
			if n.CheckSuite == nil || n.CheckSuite.App == nil || n.CheckSuite.App.DatabaseID <= 0 || n.CheckSuite.Commit == nil || n.CheckSuite.Commit.OID != c.OID {
				return nil, errors.New("CI check producer or commit identity is missing")
			}
			check.CIRequirement = core.CIRequirement{Kind: "check", Name: n.Name, AppID: n.CheckSuite.App.DatabaseID}
			check.Status, check.Conclusion, check.URL = n.Status, n.Conclusion, checkURL(n.DetailsURL)
		case "StatusContext":
			if n.Commit == nil || n.Commit.OID != c.OID {
				return nil, errors.New("CI status commit identity is missing")
			}
			check.CIRequirement = core.CIRequirement{Kind: "status", Name: n.Context}
			check.Status, check.URL = n.State, checkURL(n.TargetURL)
		default:
			return nil, errors.New("CI rollup contains an unknown context type")
		}
		if check.Name == "" {
			return nil, errors.New("CI check name is missing")
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func checkURL(raw string) string {
	if strings.ContainsFunc(raw, unicode.IsControl) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return ""
	}
	return raw
}
