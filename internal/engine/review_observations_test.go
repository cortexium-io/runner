package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/workspace"
)

func TestReviewObservationsExcludeEvidenceGapsAndPreferences(t *testing.T) {
	review := execution.ReviewAssessment{
		Criteria: []execution.ReviewCriterionResult{
			{Status: "failed", Summary: "Writes begin before hydration."},
			{Status: "blocked", Summary: "The report was unavailable."},
			{Status: "passed", Summary: "Navigation works."},
		},
		Rules: []execution.ReviewRuleResult{{Status: "failed", Findings: []execution.ReviewRuleFinding{
			{Severity: "blocking", Summary: "Tenant ownership check omitted."},
			{Severity: "warning", Summary: "Prefer another variable name."},
		}}},
		Maintainability: execution.ReviewMaintainabilityResult{Status: "blocked", Summary: "Unknown"},
	}
	want := []metrics.ReviewFinding{{Area: "acceptance", Summary: "Writes begin before hydration."},
		{Area: "repository_rules", Summary: "Tenant ownership check omitted."}}
	if got := reviewFindingObservations(review); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestAttemptLineageHelpersRetainOnlyExplicitRunnerObservations(t *testing.T) {
	result := RunResult{}
	content := github.DelegatedContent{Digest: "v1:" + strings.Repeat("a", 64), BodySnapshot: "Exact approved request"}
	observeApprovedRequest(&result, content)
	metadata := workspace.Metadata{BranchName: "runner/history", BaseRevision: strings.Repeat("b", 40), Identity: workspace.Identity{Repository: "owner/repo"}}
	observeWorkspaceLineage(&result, metadata)
	candidate := workspace.Candidate{CommitOID: strings.Repeat("c", 40), TreeOID: strings.Repeat("d", 40)}
	observeCandidateLineage(&result, candidate)
	if result.ApprovedRequest == nil || result.ApprovedRequest.DelegatedContentDigest != content.Digest || result.ApprovedRequest.BodySnapshot != content.BodySnapshot {
		t.Fatalf("approved request observation changed: %#v", result.ApprovedRequest)
	}
	if result.Lineage == nil || result.Lineage.Repository != "owner/repo" || result.Lineage.Branch != metadata.BranchName || result.Lineage.Base.CommitOID != metadata.BaseRevision || result.Lineage.Candidate.CommitOID != candidate.CommitOID || result.Lineage.Candidate.TreeOID != candidate.TreeOID {
		t.Fatalf("workspace/candidate observation changed: %#v", result.Lineage)
	}
	if result.Lineage.PublishedCandidate.CommitOID != "" || result.Lineage.Merge.CommitOID != "" {
		t.Fatalf("unobserved publication identity was fabricated: %#v", result.Lineage)
	}
}

func TestReviewDetailObservationsRetainBoundedStructuredReview(t *testing.T) {
	review := execution.ReviewAssessment{
		Criteria:        []execution.ReviewCriterionResult{{Criterion: "Exact candidate", Status: "passed", Summary: "Prior check remains applicable because the repair did not touch that path; the focused delta check passed.", Evidence: []string{"reused source audit for unchanged path", "focused delta test"}}},
		Rules:           []execution.ReviewRuleResult{{RuleSourceID: "AGENTS.md", RuleSourceVersion: "current", Status: "failed", Summary: "One rule failed.", Findings: []execution.ReviewRuleFinding{{Severity: "blocking", Summary: "Unsafe mode.", Evidence: []string{"mode was 0644"}}}}},
		Maintainability: execution.ReviewMaintainabilityResult{Status: "passed", Summary: "Readable.", Evidence: []string{"small explicit helper"}},
	}
	details := reviewDetailObservations(review)
	if len(details) != 4 || details[0].Area != "acceptance" || details[0].Name != "Exact candidate" || details[0].Evidence[1] != "focused delta test" ||
		details[1].Name != "AGENTS.md@current" || details[2].Summary != "Unsafe mode." || details[3].Area != "maintainability" {
		t.Fatalf("structured review detail was lost: %#v", details)
	}
}

func TestTerminalPullRequestObservationRetainsFinalObservedLineage(t *testing.T) {
	qaCommit := strings.Repeat("a", 40)
	item := github.WorkItem{
		ID: "PVTI_history", Title: "Retain final history", Body: "Exact approved request", Repository: "owner/repo", Status: "PR Ready",
		Branch: "runner/history", PullRequest: "https://github.com/owner/repo/pull/9", QACommit: qaCommit,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}}), project)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("load authorized item: items=%#v err=%v", items, err)
	}
	action := mustAuthorizeTest(t, service.source, items[0])
	var retained metrics.Event
	service.SetMetricsObserver(func(event metrics.Event) error {
		retained = event
		return nil
	})
	details := github.PullRequestDetails{
		URL: "https://github.com/owner/repo/pull/9", Number: 9, State: "MERGED", HeadRefName: "runner/history",
		HeadRefOID: strings.Repeat("b", 40), BaseRefOID: strings.Repeat("c", 40), MergeCommitOID: strings.Repeat("d", 40),
	}
	if err := service.recordTerminalPullRequestObservation(action, details, "merged"); err != nil {
		t.Fatal(err)
	}
	if retained.ApprovedRequest == nil || retained.ApprovedRequest.BodySnapshot != item.Body || retained.ApprovedRequest.DelegatedContentDigest != github.DelegatedContentFor(item).Digest {
		t.Fatalf("final observation lost exact approval: %#v", retained.ApprovedRequest)
	}
	if retained.Lineage == nil || retained.Lineage.Candidate.CommitOID != qaCommit || retained.Lineage.ReviewedCandidate.CommitOID != qaCommit ||
		retained.Lineage.PublishedCandidate.CommitOID != details.HeadRefOID || retained.Lineage.Base.CommitOID != details.BaseRefOID || retained.Lineage.Merge.CommitOID != details.MergeCommitOID ||
		retained.Lineage.PullRequestURL != details.URL || retained.Lineage.PullRequestNumber != details.Number {
		t.Fatalf("final Runner observation lost lineage: %#v", retained.Lineage)
	}
	if retained.Model != "" || retained.Reasoning != "" || retained.RunnerObservation == "" {
		t.Fatalf("Runner observation was presented as a model report: %#v", retained)
	}
}
