// Package github implements draft publication transport, independently of boards.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/workspace"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type Host struct {
	request func(context.Context, []string, io.Reader) (subprocess.Result, error)
}

func (h Host) api(ctx context.Context, endpoint string, input any, output any) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request := h.request
	if request == nil {
		request = subprocess.RunGitHubAPI
	}
	r, err := request(ctx, []string{endpoint, "--method", "POST", "--input", "-"}, bytes.NewReader(b))
	if err != nil || r.ExitCode != 0 {
		return fmt.Errorf("GitHub %s request failed (exit %d); check gh authentication/network, then reconcile", endpoint, r.ExitCode)
	}
	if output == nil {
		return nil
	}
	if err = json.Unmarshal([]byte(r.Stdout), output); err != nil {
		return fmt.Errorf("GitHub returned invalid JSON: %w", err)
	}
	return nil
}

const observeQuery = `query($owner:String!,$repo:String!,$branch:String!,$ref:String!){repository(owner:$owner,name:$repo){nameWithOwner ref(qualifiedName:$ref){target{oid}} pullRequests(headRefName:$branch,first:2,states:[OPEN,CLOSED,MERGED]){nodes{number url state isDraft headRefName headRefOid baseRefName body headRepository{nameWithOwner}} pageInfo{hasNextPage}}}}`

func marker(p core.PublicationIntent) string { return "<!-- runner-publication:" + p.ID + " -->" }

func (h Host) Observe(ctx context.Context, p core.PublicationIntent) (core.RemotePublication, error) {
	owner, repo, ok := strings.Cut(p.Repository, "/")
	if !ok {
		return core.RemotePublication{}, errors.New("invalid repository")
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct {
			Repository *struct {
				NameWithOwner string
				Ref           json.RawMessage
				PullRequests  *struct {
					Nodes []struct {
						Number                                                 int
						URL, State, HeadRefName, HeadRefOid, BaseRefName, Body string
						IsDraft                                                bool
						HeadRepository                                         *struct{ NameWithOwner string }
					}
					PageInfo struct{ HasNextPage bool }
				}
			}
		}
	}
	input := map[string]any{"query": observeQuery, "variables": map[string]string{"owner": owner, "repo": repo, "branch": p.Branch, "ref": "refs/heads/" + p.Branch}}
	if err := h.api(ctx, "graphql", input, &response); err != nil {
		return core.RemotePublication{}, err
	}
	r := response.Data.Repository
	if len(response.Errors) != 0 || r == nil || !strings.EqualFold(r.NameWithOwner, p.Repository) {
		return core.RemotePublication{}, errors.New("GitHub repository observation is unavailable or differs from the authorized destination")
	}
	o := core.RemotePublication{}
	if len(r.Ref) == 0 || r.PullRequests == nil || r.PullRequests.Nodes == nil {
		return o, errors.New("GitHub returned an incomplete repository observation")
	}
	if string(r.Ref) != "null" {
		var ref struct{ Target struct{ OID string } }
		if err := json.Unmarshal(r.Ref, &ref); err != nil {
			return o, errors.New("GitHub returned an invalid branch observation")
		}
		o.BranchCommit = ref.Target.OID
		if o.BranchCommit == "" {
			return o, errors.New("GitHub returned an incomplete branch identity")
		}
	}
	if r.PullRequests.PageInfo.HasNextPage || len(r.PullRequests.Nodes) > 1 {
		return o, errors.New("multiple PRs use the publication branch; manual reconciliation required")
	}
	if len(r.PullRequests.Nodes) == 0 {
		return o, nil
	}
	pr := r.PullRequests.Nodes[0]
	expectedURL := "https://github.com/" + r.NameWithOwner + "/pull/" + strconv.Itoa(pr.Number)
	if pr.Number <= 0 || pr.URL != expectedURL || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, p.Repository) || pr.HeadRefName != p.Branch || pr.HeadRefOid != p.Candidate.Commit || pr.BaseRefName != p.BaseBranch || !strings.Contains(pr.Body, marker(p)) {
		return o, errors.New("remote PR identity, candidate, base or Runner marker differs from the stored intent")
	}
	if pr.State != "OPEN" && pr.State != "CLOSED" && pr.State != "MERGED" {
		return o, errors.New("GitHub returned an unknown PR state")
	}
	o.Number, o.URL, o.State, o.Draft = pr.Number, pr.URL, pr.State, pr.IsDraft
	return o, nil
}

func (h Host) Push(ctx context.Context, p core.PublicationIntent, authorize func() error) error {
	return workspace.PushDraft(ctx, p.Candidate, p.Branch, "https://github.com/"+p.Repository+".git", authorize)
}

func (h Host) CreateDraft(ctx context.Context, p core.PublicationIntent, authorize func() error) error {
	body := fmt.Sprintf("%s\n\nRunner task `%s`, version %d.\n\nLocally reviewed candidate: `%s`\nRecorded base: `%s`\nQuality revision: `%s`\n\n**Draft handoff only. Latest-base verification, CI assessment and integration are pending. Do not merge from this bundle's local acceptance.**\n", marker(p), p.TaskID, p.Version, p.Candidate.Commit, p.ApprovedBase, p.Quality)
	if authorize == nil {
		return errors.New("draft creation requires current authority")
	}
	if err := authorize(); err != nil {
		return err
	}
	return h.api(ctx, "repos/"+p.Repository+"/pulls", map[string]any{"title": p.Title, "body": body, "head": p.Branch, "base": p.BaseBranch, "draft": true, "maintainer_can_modify": false}, nil)
}
