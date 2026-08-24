package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/securefs"
)

const identityVersion = 2

var ErrIdentityMismatch = errors.New("workspace identity mismatch")

// Identity is the canonical private binding for one reusable task workspace.
// It lives beside, never inside, the mutable Git worktree.
type Identity struct {
	Version                int    `json:"version"`
	ItemID                 string `json:"item_id"`
	DelegatedContentDigest string `json:"delegated_content_digest"`
	BaseRef                string `json:"base_ref"`
	BaseRevision           string `json:"base_revision"`
	Branch                 string `json:"branch"`
	Repository             string `json:"repository"`
	WorktreePath           string `json:"worktree_path"`
}

func newIdentity(itemID, digest, baseRef, baseRevision, branch, repository, worktreePath string) (Identity, error) {
	identity := Identity{
		Version: identityVersion, ItemID: strings.TrimSpace(itemID),
		DelegatedContentDigest: strings.TrimSpace(digest), BaseRef: strings.TrimSpace(baseRef), BaseRevision: strings.TrimSpace(baseRevision),
		Branch: strings.TrimSpace(branch), Repository: strings.TrimSpace(repository), WorktreePath: normalizedPath(worktreePath),
	}
	if identity.ItemID == "" || identity.DelegatedContentDigest == "" || identity.BaseRef == "" || identity.BaseRevision == "" || identity.Branch == "" || identity.Repository == "" || identity.WorktreePath == "" {
		return Identity{}, errors.New("workspace identity requires item, delegated content, base ref, base revision, branch, repository, and worktree path")
	}
	return identity, nil
}

func activeIdentityPath(worktreeRoot, workID string) string {
	return filepath.Join(worktreeRoot, ".runner-state", workID+".json")
}

func readIdentity(path string) (Identity, bool, error) {
	content, _, state, err := securefs.ReadFile(path, 64*1024)
	if err != nil {
		return Identity{}, false, err
	}
	if !state.Exists {
		return Identity{}, false, nil
	}
	identity, err := decodeIdentity(content)
	return identity, err == nil, err
}

func decodeIdentity(content []byte) (Identity, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var identity Identity
	if err := decoder.Decode(&identity); err != nil {
		return Identity{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Identity{}, errors.New("workspace identity contains trailing data")
	}
	if identity.Version != identityVersion {
		return Identity{}, fmt.Errorf("unsupported workspace identity version %d", identity.Version)
	}
	return identity, nil
}

func writeIdentity(path string, identity Identity) error {
	if err := securefs.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	content, err := encodeIdentity(identity)
	if err != nil {
		return err
	}
	return securefs.WriteFileExclusive(path, content, 0o600)
}

func replaceIdentity(path string, expected, replacement Identity) error {
	content, _, state, err := securefs.ReadFile(path, 64*1024)
	if err != nil {
		return err
	}
	if !state.Exists {
		return workspaceIdentityMismatch(expected, Identity{}, false, "")
	}
	recorded, err := decodeIdentity(content)
	if err != nil {
		return err
	}
	if recorded != expected {
		return workspaceIdentityMismatch(expected, recorded, true, "")
	}
	replacementContent, err := encodeIdentity(replacement)
	if err != nil {
		return err
	}
	directory, err := securefs.OpenDir(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.ReplaceFile(filepath.Base(path), replacementContent, 0o600, state)
}

func encodeIdentity(identity Identity) ([]byte, error) {
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(identity); err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}

func metadataFor(repoRoot, sourceSnapshot string, identity Identity) Metadata {
	return Metadata{
		RepoRoot: repoRoot, SourceSnapshot: sourceSnapshot, WorktreePath: identity.WorktreePath,
		BranchName: identity.Branch, BaseRef: identity.BaseRef, BaseRevision: identity.BaseRevision, Identity: identity,
	}
}

func workspaceIdentityMismatch(expected, actual Identity, hasActual bool, registeredBranch string) error {
	if !hasActual {
		if strings.TrimSpace(registeredBranch) != "" {
			return fmt.Errorf("%w: branch %q has no private identity record", ErrIdentityMismatch, registeredBranch)
		}
		return fmt.Errorf("%w: private identity record is missing", ErrIdentityMismatch)
	}
	if actual == expected {
		if registeredBranch = strings.TrimSpace(registeredBranch); registeredBranch != "" && registeredBranch != expected.Branch {
			return fmt.Errorf("%w: registered branch %q does not match recorded branch %q", ErrIdentityMismatch, registeredBranch, expected.Branch)
		}
		return fmt.Errorf("%w: retained branch %q is missing", ErrIdentityMismatch, expected.Branch)
	}
	return fmt.Errorf("%w: expected item %q, content %q, base ref %q at %q, branch %q, repository %q, and path %q; recorded item %q, content %q, base ref %q at %q, branch %q, repository %q, and path %q", ErrIdentityMismatch,
		expected.ItemID, expected.DelegatedContentDigest, expected.BaseRef, expected.BaseRevision, expected.Branch, expected.Repository, expected.WorktreePath,
		actual.ItemID, actual.DelegatedContentDigest, actual.BaseRef, actual.BaseRevision, actual.Branch, actual.Repository, actual.WorktreePath)
}

func (p GitProvider) quarantine(ctx context.Context, repoRoot, worktreeRoot, workID, worktreePath, branch, identityPath string, hasIdentity bool) error {
	if err := securefs.ValidatePrivateDir(worktreeRoot); err != nil {
		return fmt.Errorf("validate private workspace root before quarantine: %w", err)
	}
	now := p.now
	if now == nil {
		now = time.Now
	}
	stem := workID + "-" + now().UTC().Format("20060102T150405.000000000Z")
	quarantineRoot := filepath.Join(worktreeRoot, ".runner-quarantine")
	stateRoot := filepath.Join(worktreeRoot, ".runner-state", "quarantine")
	if err := securefs.EnsurePrivateDir(quarantineRoot); err != nil {
		return fmt.Errorf("create quarantine directory: %w", err)
	}
	if err := securefs.EnsurePrivateDir(stateRoot); err != nil {
		return fmt.Errorf("create quarantine state directory: %w", err)
	}

	var quarantinePath, quarantineBranch, quarantineIdentityPath string
	found := false
	for suffix := 1; suffix <= 1000; suffix++ {
		name := stem
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d", stem, suffix)
		}
		quarantinePath = filepath.Join(quarantineRoot, name)
		quarantineBranch = "runner-quarantine/" + name
		quarantineIdentityPath = filepath.Join(stateRoot, name+".json")
		if _, pathErr := os.Lstat(quarantinePath); !errors.Is(pathErr, os.ErrNotExist) {
			continue
		}
		if _, stateErr := os.Lstat(quarantineIdentityPath); !errors.Is(stateErr, os.ErrNotExist) {
			continue
		}
		if p.branchExists(ctx, repoRoot, quarantineBranch) {
			continue
		}
		found = true
		break
	}
	if !found {
		return errors.New("no collision-free quarantine target is available")
	}

	registeredBranch, registered, err := p.registeredWorktree(ctx, repoRoot, worktreePath)
	if err != nil {
		return err
	}
	if registered {
		if strings.TrimSpace(branch) != "" && registeredBranch != strings.TrimSpace(branch) {
			branch = registeredBranch
		}
		if _, err := p.git(ctx, repoRoot, "worktree", "move", worktreePath, quarantinePath); err != nil {
			return fmt.Errorf("move stale worktree to %s: %w", quarantinePath, err)
		}
		if _, err := p.git(ctx, quarantinePath, "branch", "-m", quarantineBranch); err != nil {
			return fmt.Errorf("retain stale worktree at %s but rename its branch: %w", quarantinePath, err)
		}
	} else if _, statErr := os.Lstat(worktreePath); statErr == nil {
		return errors.New("workspace path is not owned by the configured repository")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else if p.branchExists(ctx, repoRoot, branch) {
		if _, err := p.git(ctx, repoRoot, "branch", "-m", branch, quarantineBranch); err != nil {
			return fmt.Errorf("retain stale branch as %q: %w", quarantineBranch, err)
		}
	}
	if hasIdentity {
		if err := securefs.LinkFile(identityPath, quarantineIdentityPath); err != nil {
			return fmt.Errorf("retain stale workspace identity: %w", err)
		}
		if err := securefs.RemoveFile(identityPath); err != nil {
			return fmt.Errorf("retire active stale workspace identity: %w", err)
		}
	}
	return nil
}
