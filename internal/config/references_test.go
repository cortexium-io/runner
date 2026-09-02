package config

import (
	"strings"
	"testing"
)

const testReferenceCommit = "714128eaeb8e3805431f8fdeaa49a570e2830cea"

func TestRepositoryReferencesResolveOnlyForPlannerAndReviewerContracts(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.RepositoryReferences = []RepositoryReference{{
		Name: " legacy-frontend ", Path: "/references/legacy-frontend", Commit: strings.ToUpper(testReferenceCommit),
	}}
	runtime, err := cfg.Resolve()
	if err != nil {
		t.Fatalf("resolve repository references: %v", err)
	}
	for _, role := range []string{WorkRolePlanner, WorkRoleReviewer} {
		execution := runtime.Execution(role, HarnessCodexCLI, cfg.ProjectDir)
		if len(execution.RepositoryReferences) != 1 {
			t.Fatalf("%s references = %#v, want one", role, execution.RepositoryReferences)
		}
		if reference := execution.RepositoryReferences[0]; reference.Name != "legacy-frontend" || reference.Commit != testReferenceCommit {
			t.Fatalf("%s reference was not normalized: %#v", role, reference)
		}
		if len(execution.ReferenceProtectedRoots) != 2 {
			t.Fatalf("%s protected roots = %#v, want project and worktree roots", role, execution.ReferenceProtectedRoots)
		}
	}
	if execution := runtime.Execution(WorkRoleImplementer, HarnessCodexCLI, "/worktrees/item"); len(execution.RepositoryReferences) != 0 || len(execution.ReferenceProtectedRoots) != 0 {
		t.Fatalf("implementer received repository references: %#v", execution)
	}
}

func TestRepositoryReferenceStaticValidation(t *testing.T) {
	tests := []struct {
		name       string
		references []RepositoryReference
		want       string
	}{
		{name: "missing name", references: []RepositoryReference{{Path: "/references/a", Commit: testReferenceCommit}}, want: "name is required"},
		{name: "duplicate name", references: []RepositoryReference{{Name: "legacy", Path: "/references/a", Commit: testReferenceCommit}, {Name: "LEGACY", Path: "/references/b", Commit: testReferenceCommit}}, want: "duplicate name"},
		{name: "relative path", references: []RepositoryReference{{Name: "legacy", Path: "references/a", Commit: testReferenceCommit}}, want: "path must be absolute"},
		{name: "short commit", references: []RepositoryReference{{Name: "legacy", Path: "/references/a", Commit: "714128e"}}, want: "full 40- or 64-character"},
		{name: "project overlap", references: []RepositoryReference{{Name: "legacy", Path: "/project/legacy", Commit: testReferenceCommit}}, want: "protected project or workspace"},
		{name: "worktree overlap", references: []RepositoryReference{{Name: "legacy", Path: "/worktrees/legacy", Commit: testReferenceCommit}}, want: "protected project or workspace"},
		{name: "reference overlap", references: []RepositoryReference{{Name: "legacy", Path: "/references/legacy", Commit: testReferenceCommit}, {Name: "nested", Path: "/references/legacy/nested", Commit: testReferenceCommit}}, want: "overlaps repository_references"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := explicitTestConfig()
			cfg.RepositoryReferences = test.references
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid references were accepted: %v", err)
			}
		})
	}
}

func TestRepositoryReferencesRequireAbsolutePrimaryRepository(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.ProjectDir = "."
	cfg.RepositoryReferences = []RepositoryReference{{Name: "legacy", Path: "/references/legacy", Commit: testReferenceCommit}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "absolute project_dir") {
		t.Fatalf("relative primary repository was accepted with references: %v", err)
	}
}

func TestRepositoryReferencesRejectSandboxedPiReadContracts(t *testing.T) {
	cfg := explicitTestConfig()
	cfg.Harnesses[0].Kind = HarnessPiCLI
	cfg.Harnesses[0].Command = "pi"
	cfg.Roles = RoleTemplate(HarnessPiCLI)
	for _, roleID := range []string{WorkRoleImplementer, WorkRoleReviewer} {
		role := cfg.Roles[roleID]
		role.Access = RoleAccessHost
		cfg.Roles[roleID] = role
	}
	cfg.RepositoryReferences = []RepositoryReference{{Name: "legacy", Path: "/references/legacy", Commit: testReferenceCommit}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `host access for Pi role "planner"`) {
		t.Fatalf("sandboxed Pi reference reader was accepted: %v", err)
	}
}
