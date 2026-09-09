package execution

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
)

func TestPromptStablePrefixSurvivesDifferentCardsAndProofCounts(t *testing.T) {
	one := reviewerAssignment()
	two := reviewerAssignment()
	two.Spec.Task = Task{Title: "Completely different card", Instructions: "Different task context"}
	two.Spec.RequiredVerification = []string{"New proof"}
	two.Spec.ApprovedBodySnapshot = "Different approved body"
	two.Spec.DelegatedContentDigest = "different-digest"
	two.Spec.ReviewBaseOID = strings.Repeat("c", 40)
	two.Spec.ReviewCandidateOID = strings.Repeat("d", 40)
	two.Spec.ReviewBaseline = &ReviewBaseline{CommitOID: strings.Repeat("e", 40)}
	for _, name := range []string{"Codex CLI", "Claude Code", "Pi CLI"} {
		for stage, build := range map[string]func(Assignment) string{
			"implementer": func(a Assignment) string { return buildHarnessPrompt(a, true, name) },
			"audit":       func(a Assignment) string { return reviewerAuditPrompt(a, name) },
			"focused": func(a Assignment) string {
				return reviewerResolutionPrompt(a, name, []reviewerUnresolvedCheck{{Key: "P1", Question: a.Spec.RequiredVerification[0]}})
			},
		} {
			t.Run(name+"/"+stage, func(t *testing.T) {
				first, second := build(one), build(two)
				prefix, _, found := strings.Cut(first, "\n\nTitle: ")
				other, _, otherFound := strings.Cut(second, "\n\nTitle: ")
				if !found || !otherFound || prefix != other {
					t.Fatal("card-specific data split the stable instruction prefix")
				}
				if len(prefix) < 1000 || !strings.Contains(prefix, "structured-output mechanism") {
					t.Fatal("stable stage/output instructions remain behind dynamic context")
				}
				if strings.Count(second, two.Spec.Task.Title) != 1 {
					t.Fatal("task title duplicated or lost")
				}
			})
		}
	}
}

func TestPromptContextUsesActualPinnedGuidanceNotTaskData(t *testing.T) {
	cfg := config.ExecutionConfig{Skills: []string{"runner-implementer"}, SafeTools: true}
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		guidance := harnessGuidance(kind, cfg, true)
		if strings.Count(guidance, "--- BEGIN RUNNER-PINNED SKILL ---") != 1 {
			t.Fatal("pinned skill content missing or duplicated")
		}
		trace := metrics.NewAttemptTrace(func(metrics.Event) error { return nil }, metrics.Event{})
		ctx := metrics.WithAttemptTrace(t.Context(), trace)
		recordPromptContext(ctx, guidance)
		recordPromptContext(ctx, guidance)
		contexts := trace.PromptContexts()
		if len(contexts) != 1 || contexts[0].Layout != promptLayoutVersion || len(contexts[0].GuidanceDigest) != 71 {
			t.Fatalf("missing stable guidance fingerprint: %#v", contexts)
		}
		changed := cfg
		changed.Skills = []string{"runner-reviewer"}
		recordPromptContext(ctx, harnessGuidance(kind, changed, true))
		if len(trace.PromptContexts()) != 2 {
			t.Fatal("changed pinned guidance reused the old fingerprint")
		}
	}
	executor := CodexExecutor{config: cfg}
	prompt := executor.workspaceWritePrompt(reviewerAssignment())
	if strings.Index(prompt, "--- END RUNNER-PINNED SKILL ---") > strings.Index(prompt, "Title:") {
		t.Fatal("Codex launch placed shared guidance after variable task data")
	}
}
