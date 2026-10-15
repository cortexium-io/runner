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

const publicationFields = `number url state isDraft headRefName headRefOid baseRefName body headRepository{nameWithOwner}`
const observeQuery = `query($owner:String!,$repo:String!,$branch:String!,$ref:String!){repository(owner:$owner,name:$repo){nameWithOwner ref(qualifiedName:$ref){target{oid}} pullRequests(headRefName:$branch,first:2,states:[OPEN,CLOSED,MERGED]){nodes{` + publicationFields + `} pageInfo{hasNextPage}}}}`

type remotePR struct {
	Number                                                 int
	URL, State, HeadRefName, HeadRefOid, BaseRefName, Body string
	IsDraft                                                bool
	HeadRepository                                         *struct{ NameWithOwner string }
	BaseRefOid                                             string
	PotentialMergeCommit                                   json.RawMessage
}
type remoteRepository struct {
	NameWithOwner string
	Ref           json.RawMessage
	PullRequests  *struct {
		Nodes    []remotePR
		PageInfo struct{ HasNextPage bool }
	}
	Candidate *checkCommit
}

func marker(p core.PublicationIntent) string {
	return "<!-- runner-publication:" + p.PublicationID() + " -->"
}

func (h Host) readRemote(ctx context.Context, p core.PublicationIntent, checks bool) (*remoteRepository, error) {
	owner, repo, ok := strings.Cut(p.Repository, "/")
	if !ok {
		return nil, errors.New("invalid repository")
	}
	var response struct {
		Errors []json.RawMessage
		Data   struct{ Repository *remoteRepository }
	}
	query := observeQuery
	variables := map[string]string{"owner": owner, "repo": repo, "branch": p.Branch, "ref": "refs/heads/" + p.Branch}
	if checks {
		query = ciQuery
		variables["commit"] = p.Candidate.Commit
	}
	if err := h.api(ctx, "graphql", map[string]any{"query": query, "variables": variables}, &response); err != nil {
		return nil, err
	}
	if len(response.Errors) != 0 {
		return nil, errors.New("GitHub returned incomplete observation with GraphQL errors")
	}
	return response.Data.Repository, nil
}
func (h Host) Observe(ctx context.Context, p core.PublicationIntent) (core.RemotePublication, error) {
	r, err := h.readRemote(ctx, p, false)
	if err != nil {
		return core.RemotePublication{}, err
	}
	return validateRemote(p, r)
}
func validateRemote(p core.PublicationIntent, r *remoteRepository) (core.RemotePublication, error) {
	if r == nil || !strings.EqualFold(r.NameWithOwner, p.Repository) {
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
	if pr.Number <= 0 || pr.URL != expectedURL || pr.HeadRepository == nil || !strings.EqualFold(pr.HeadRepository.NameWithOwner, p.Repository) || pr.HeadRefName != p.Branch || (pr.HeadRefOid != p.Candidate.Commit && (p.Sync == nil || pr.HeadRefOid != p.Sync.PreviousCommit)) || pr.BaseRefName != p.BaseBranch || !strings.Contains(pr.Body, marker(p)) {
		return o, errors.New("remote PR identity, candidate, base or Runner marker differs from the stored intent")
	}
	if p.Sync != nil && (pr.Number != p.Sync.Number || pr.URL != p.Sync.URL || pr.HeadRefOid != o.BranchCommit) {
		return o, errors.New("synchronization PR or branch identity differs from the known draft")
	}
	if pr.State != "OPEN" && pr.State != "CLOSED" && pr.State != "MERGED" {
		return o, errors.New("GitHub returned an unknown PR state")
	}
	o.Number, o.URL, o.State, o.Draft = pr.Number, pr.URL, pr.State, pr.IsDraft
	return o, nil
}

func (h Host) Push(ctx context.Context, p core.PublicationIntent, authorize func() error) error {
	expected := ""
	if p.Sync != nil {
		expected = p.Sync.PreviousCommit
		if expected == "" {
			return errors.New("draft synchronization requires an expected remote commit")
		}
	}
	return workspace.PushDraft(ctx, p.Candidate, p.Branch, "https://github.com/"+p.Repository+".git", expected, authorize)
}

func (h Host) CreateDraft(ctx context.Context, p core.PublicationIntent, authorize func() error) error {
	if p.Sync != nil {
		return errors.New("draft synchronization cannot create a PR")
	}
	body := fmt.Sprintf("%s\n\nRunner task `%s`, version %d.\n\nInitial locally reviewed candidate: `%s`\nInitial recorded base: `%s`\nQuality revision: `%s`\n\nThese details describe the initial snapshot. Runner may later advance this branch after current-base checks and independent review; exact synchronization evidence stays in the local task history.\n\n**Draft handoff only. CI assessment and integration are pending. Do not merge from this bundle's local acceptance.**\n", marker(p), p.TaskID, p.Version, p.Candidate.Commit, p.ApprovedBase, p.Quality)
	if authorize == nil {
		return errors.New("draft creation requires current authority")
	}
	if err := authorize(); err != nil {
		return err
	}
	return h.api(ctx, "repos/"+p.Repository+"/pulls", map[string]any{"title": p.Title, "body": body, "head": p.Branch, "base": p.BaseBranch, "draft": true, "maintainer_can_modify": false}, nil)
}

// CurrentBase fetches objects using the same protected destination as publication.
func (Host) CurrentBase(ctx context.Context, path, repository, branch string) (string, error) {
	return workspace.FetchBase(ctx, path, repository, branch)
}
