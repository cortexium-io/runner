package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestUnstartedReauthorizationPreviewDisclosesExactReleaseAndAbsentEvidence(t *testing.T) {
	var output bytes.Buffer
	writeReauthorizationPreview(&output, engine.ProjectItemReauthorization{Approval: github.ReauthorizationPlan{
		Item:      github.WorkItem{ID: "member", Body: "Exact bounded repair", ImplementationProfile: "implementer"},
		Unstarted: true, PlanRevision: "v1:exact-revision", RemoveIntakeLabel: true, TargetStatus: "Ready",
	}})
	for _, expected := range []string{"approved, unstarted", "Plan revision: v1:exact-revision", "Private workspace and execution evidence: absent", "Remove reassessment label: true", "Exact bounded repair", "no QA acceptance or batch approval"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("preview omitted %q", expected)
		}
	}
	if strings.Contains(output.String(), "Retained worktree:") {
		t.Fatal("unstarted recovery claimed a retained workspace")
	}
}

func TestBranchlessReauthorizationPreviewDisclosesRetainedIdentityAndOriginalMembers(t *testing.T) {
	var output bytes.Buffer
	writeReauthorizationPreview(&output, engine.ProjectItemReauthorization{
		Approval: github.ReauthorizationPlan{
			Item:            github.WorkItem{ID: "third", Title: "Retained third child", Phase: "ready", Body: "Exact original scope"},
			PlanningSource:  &github.WorkItem{ID: "parent", Title: "Original source"},
			PlanningMembers: []github.WorkItem{{ID: "first", Status: "Done"}, {ID: "second", Status: "Done", QAFailures: 1}, {ID: "third", Status: "Needs assessment"}, {ID: "fourth", Status: "Ready"}, {ID: "fifth", Status: "Ready"}},
		},
		Workspace:        workspace.Identity{Branch: "runner/original", BaseRef: "refs/remotes/origin/main", BaseRevision: "accepted-base", WorktreePath: "/private/retained", DelegatedContentDigest: "v1:exact-content"},
		RetainedSnapshot: &workspace.Snapshot{Head: "retained-head", Tree: "retained-tree", Fingerprint: "v1:dirty-snapshot", Clean: false},
		EvidenceDigests:  map[string]string{"qa_feedback": "feedback-hash"},
	})
	for _, expected := range []string{"retained-implementation", "runner/original", "accepted-base", "/private/retained", "v1:exact-content", "retained-head", "retained-tree", "clean: false", "v1:dirty-snapshot", "Project Branch remains blank", "Original source (parent)", "first:", "second:", "third:", "fourth:", "fifth:", "status=Done; QA failures=1", "QA failures: 0", "Exact original scope", "implementation evidence: absent", "qa_feedback evidence: feedback-hash", "no QA acceptance or batch approval"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("preview omitted %q: %s", expected, output.String())
		}
	}
}
