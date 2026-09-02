package engine

import (
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

type admittedAction struct {
	action    github.AuthorizedAction
	resources []string
}

func (s *Engine) actionResourceKeys(action github.AuthorizedAction) ([]string, error) {
	item := action.Item
	itemResource, err := itemResourceKey(item)
	if err != nil {
		return nil, err
	}
	resources := []string{itemResource}
	contract := s.cfg.RoleContract(action.Role)
	if contract != config.WorkRoleImplementer && contract != config.WorkRoleReviewer {
		return resources, nil
	}
	workspaceResource, err := s.workspaceResourceKey(item)
	if err != nil {
		return nil, err
	}
	return append(resources, workspaceResource), nil
}

func itemResourceKey(item github.WorkItem) (string, error) {
	itemID := strings.TrimSpace(item.ID)
	if itemID == "" {
		return "", errors.New("action resource claim requires a Project item ID")
	}
	return "item:" + itemID, nil
}

func (s *Engine) workspaceResourceKey(item github.WorkItem) (string, error) {
	identity, err := workspace.ResourceIdentity(s.workspaceRequestForItem(item, "", "", false))
	if err != nil {
		return "", err
	}
	return "workspace:" + identity, nil
}

func (s *Engine) integrationResourceKey(item github.WorkItem, baseBranch string) (string, error) {
	repository := strings.TrimSpace(item.Repository)
	if repository == "" {
		repository = strings.TrimSpace(s.cfg.GitHubProject.IntakeRepository)
	}
	baseBranch = defaultString(baseBranch, s.baseBranch())
	if repository == "" || baseBranch == "" {
		return "", errors.New("integration resource claim requires a repository and base branch")
	}
	return "integration:" + strings.ToLower(repository) + "/" + baseBranch, nil
}

func (s *Engine) workspaceRequestForItem(item github.WorkItem, delegatedContentDigest, repoRoot string, quarantineMismatch bool) workspace.Request {
	repository := strings.TrimSpace(item.Repository)
	if repository == "" {
		repository = strings.TrimSpace(s.cfg.GitHubProject.IntakeRepository)
	}
	return workspace.Request{
		WorkingDir: repoRoot, WorktreeRoot: s.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: strings.TrimSpace(item.ID), DelegatedContentDigest: strings.TrimSpace(delegatedContentDigest), Repository: repository,
		BranchPrefix: "runner", BranchName: strings.TrimSpace(item.Branch), BaseRef: s.remoteName() + "/" + s.baseBranch(),
		QuarantineMismatch: quarantineMismatch,
	}
}

func occupiedResourceKeys(inFlight map[string][]string) map[string]struct{} {
	occupied := map[string]struct{}{}
	for _, resources := range inFlight {
		for _, resource := range resources {
			occupied[resource] = struct{}{}
		}
	}
	return occupied
}

func resourcesAvailable(resources []string, occupied map[string]struct{}) bool {
	for _, resource := range resources {
		if _, exists := occupied[resource]; exists {
			return false
		}
	}
	return true
}

func reserveResources(resources []string, occupied map[string]struct{}) {
	for _, resource := range resources {
		occupied[resource] = struct{}{}
	}
}

func (s *Engine) itemsWithoutResourceConflicts(items []github.WorkItem, inFlight map[string][]string) ([]github.WorkItem, error) {
	if len(inFlight) == 0 {
		return items, nil
	}
	occupied := occupiedResourceKeys(inFlight)
	filtered := make([]github.WorkItem, 0, len(items))
	for _, item := range items {
		resources := make([]string, 0, 2)
		itemResource, err := itemResourceKey(item)
		if err != nil {
			return nil, err
		}
		resources = append(resources, itemResource)
		if strings.TrimSpace(item.Branch) != "" || strings.TrimSpace(item.PullRequest) != "" {
			workspaceResource, err := s.workspaceResourceKey(item)
			if err != nil {
				return nil, err
			}
			resources = append(resources, workspaceResource)
		}
		if resourcesAvailable(resources, occupied) {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}
