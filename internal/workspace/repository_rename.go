package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// InspectRepositoryRename distinguishes an unstarted task from retained work,
// including work whose branch was never written back to the Project.
func (p GitProvider) InspectRepositoryRename(ctx context.Context, request Request) (*Identity, error) {
	repo, err := p.repositoryRoot(ctx, request.WorkingDir)
	if err != nil {
		return nil, err
	}
	root, err := resolveWorktreeRoot(repo, request.WorktreeRoot)
	if err != nil {
		return nil, err
	}
	workID, _, err := resolveWorktreeNames(request.WorkID, request.BranchPrefix, request.BranchName)
	if err != nil {
		return nil, err
	}
	_, exists, err := readIdentity(activeIdentityPath(root, workID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !exists {
		if request.BranchName != "" {
			return nil, errors.New("retained task work requires its original private identity; recovery will not replace unbound work")
		}
		if err := p.VerifyWorkspaceAbsent(ctx, request); err != nil {
			return nil, err
		}
		return nil, nil
	}
	identity, err := p.ValidateRetainedIdentity(ctx, request)
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

// RebindRepositoryRename changes only repository and delegated-content bindings.
// The operator journal preserves the original record; Git and worktree bytes
// are never modified. Repeating the exact confirmed replacement is idempotent.
func (p GitProvider) RebindRepositoryRename(ctx context.Context, request Request, before, after Identity) error {
	expected := before
	expected.Repository, expected.DelegatedContentDigest = after.Repository, after.DelegatedContentDigest
	if expected != after || before.Repository == after.Repository || before.DelegatedContentDigest == after.DelegatedContentDigest {
		return errors.New("repository rename may only renew the repository and content bindings")
	}
	current, err := p.InspectRepositoryRename(ctx, request)
	if errors.Is(err, ErrIdentityMismatch) {
		request.Repository, request.DelegatedContentDigest = after.Repository, after.DelegatedContentDigest
		current, err = p.InspectRepositoryRename(ctx, request)
	}
	if err != nil {
		return err
	}
	if current == nil || *current != before && *current != after {
		return errors.New("retained workspace identity changed after repository rename preview")
	}
	if *current == after {
		return nil
	}
	root := filepath.Dir(before.WorktreePath)
	path := activeIdentityPath(root, filepath.Base(before.WorktreePath))
	if err := replaceIdentity(path, before, after); err != nil {
		return fmt.Errorf("renew retained workspace binding: %w", err)
	}
	return nil
}
