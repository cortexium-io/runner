package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// RepositoryReference is an operator-selected, immutable Git checkout exposed
// as evidence to planner, implementer, and reviewer contracts. Role configuration
// cannot widen or narrow this fixed Runner-owned boundary.
type RepositoryReference struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

func validateRepositoryReferences(cfg Config) error {
	if len(cfg.RepositoryReferences) > 0 && !filepath.IsAbs(strings.TrimSpace(cfg.ProjectDir)) {
		return errors.New("repository_references require an absolute project_dir")
	}
	seenNames := map[string]struct{}{}
	paths := make([]string, 0, len(cfg.RepositoryReferences))
	protected := cfg.RepositoryReferenceProtectedRoots()
	for index, reference := range cfg.RepositoryReferences {
		name := strings.TrimSpace(reference.Name)
		if name == "" {
			return fmt.Errorf("repository_references[%d].name is required", index)
		}
		key := strings.ToLower(name)
		if _, exists := seenNames[key]; exists {
			return fmt.Errorf("repository_references contains duplicate name %q", name)
		}
		seenNames[key] = struct{}{}

		path := strings.TrimSpace(reference.Path)
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("repository_references[%d].path must be absolute", index)
		}
		path = filepath.Clean(path)
		for priorIndex, prior := range paths {
			if pathsOverlap(path, prior) {
				return fmt.Errorf("repository_references[%d].path overlaps repository_references[%d].path", index, priorIndex)
			}
		}
		for _, root := range protected {
			if pathsOverlap(path, root) {
				return fmt.Errorf("repository_references[%d].path overlaps protected project or workspace root %q", index, root)
			}
		}
		paths = append(paths, path)

		commit := strings.TrimSpace(reference.Commit)
		if !validFullGitObjectID(commit) {
			return fmt.Errorf("repository_references[%d].commit must be a full 40- or 64-character hexadecimal Git object ID", index)
		}
	}

	if len(cfg.RepositoryReferences) == 0 {
		return nil
	}
	for _, roleID := range cfg.ExecutionRoleIDs() {
		role, ok := cfg.RoleProfile(roleID)
		if !ok {
			continue
		}
		contract := cfg.RoleContract(roleID)
		if contract != WorkRolePlanner && contract != WorkRoleImplementer && contract != WorkRoleReviewer {
			continue
		}
		if strings.TrimSpace(role.Harness) == HarnessPiCLI && EffectiveRoleAccess(role.Access) != RoleAccessHost {
			return fmt.Errorf("repository_references require host access for Pi role %q because Pi cannot enforce read-only reference roots", roleID)
		}
	}
	return nil
}

func validFullGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			lower := character | 0x20
			if lower < 'a' || lower > 'f' {
				return false
			}
		}
	}
	return true
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(strings.TrimSpace(left))
	right = filepath.Clean(strings.TrimSpace(right))
	if left == "." || right == "." || !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	return pathInsideOrEqual(left, right) || pathInsideOrEqual(right, left)
}

func pathInsideOrEqual(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}

// RepositoryReferenceProtectedRoots returns the primary checkout and every
// harness worktree root using the same relative-root convention as workspace
// preparation.
func (c Config) RepositoryReferenceProtectedRoots() []string {
	return repositoryReferenceProtectedRoots(c.ProjectDir, c.Harnesses)
}

func repositoryReferenceProtectedRoots(projectDir string, harnesses []HarnessConfig) []string {
	project := filepath.Clean(strings.TrimSpace(projectDir))
	result := []string{}
	if project != "." && filepath.IsAbs(project) {
		result = append(result, project)
	}
	for _, harness := range harnesses {
		root := strings.TrimSpace(harness.WorkspaceWriteRoot)
		if root == "" {
			continue
		}
		if !filepath.IsAbs(root) && project != "." && filepath.IsAbs(project) {
			root = filepath.Join(filepath.Dir(project), root)
		}
		result = append(result, filepath.Clean(root))
	}
	return compactAbsolutePaths(result)
}

func compactAbsolutePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = filepath.Clean(strings.TrimSpace(path))
		if path == "." || !filepath.IsAbs(path) {
			continue
		}
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		result = append(result, path)
	}
	return result
}

func cloneRepositoryReferences(values []RepositoryReference) []RepositoryReference {
	if len(values) == 0 {
		return nil
	}
	result := make([]RepositoryReference, len(values))
	for index, value := range values {
		result[index] = RepositoryReference{
			Name: strings.TrimSpace(value.Name), Path: filepath.Clean(strings.TrimSpace(value.Path)),
			Commit: strings.ToLower(strings.TrimSpace(value.Commit)),
		}
	}
	return result
}
