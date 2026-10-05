package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/engine"
	"github.com/cortexium-io/runner/internal/github"
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
