package execution

import (
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/skills"
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
				if !strings.Contains(prefix, "structured-output mechanism") {
					t.Fatal("stable stage/output instructions remain behind dynamic context")
				}
				if strings.Count(second, two.Spec.Task.Title) != 1 {
					t.Fatal("task title duplicated or lost")
				}
			})
		}
	}
}

func TestReviewerStageGuidanceIsScopedAcrossHarnesses(t *testing.T) {
	assignment := reviewerAssignment()
	checks := []reviewerUnresolvedCheck{{Key: "P2", Question: "Does the required interaction complete?"}}
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		t.Run(kind, func(t *testing.T) {
			cfg := config.ExecutionConfig{Skills: []string{"runner-reviewer"}}
			guidance := harnessGuidance(kind, cfg, true)
			audit := guidance + reviewerAuditPrompt(assignment, reviewerHarnessDisplayName(kind))
			focused := guidance + reviewerResolutionPrompt(assignment, reviewerHarnessDisplayName(kind), checks)
			t.Logf("assembled audit: %d bytes / %d words; focused: %d bytes / %d words", len(audit), len(strings.Fields(audit)), len(focused), len(strings.Fields(focused)))
			for _, prompt := range []string{audit, focused} {
				assertPinnedReviewerGuidance(t, prompt)
			}
			for _, procedure := range []string{"Verification scheduling", "Unexplained timing failures", "unchanged automatic retry with adequate diagnostics", "deliberately warm up the app"} {
				if strings.Contains(audit, procedure) || strings.Count(focused, procedure) != 1 {
					t.Fatalf("dynamic-only procedure %q is missing, duplicated, or loaded during static audit", procedure)
				}
			}
			if !strings.Contains(audit, "Do not run tests") || !strings.Contains(focused, "Never loop until green") ||
				!strings.Contains(focused, "Choose the smallest existing check") {
				t.Fatal("stage execution boundaries changed")
			}
			if strings.Contains(focused, `"key":"P1"`) || !strings.Contains(focused, `"key":"P2"`) {
				t.Fatal("focused verification must receive only unresolved checks")
			}
		})
	}
}

func assertPinnedReviewerGuidance(t *testing.T, prompt string) {
	t.Helper()
	skill, ok := (skills.EmbeddedCatalog{}).Get("runner-reviewer")
	if !ok || len(skill.Content) == 0 {
		t.Fatal("bundled reviewer guidance is missing")
	}
	// The catalog pins the reviewed policy bytes; check that every shared
	// safeguard is injected exactly once without coupling assembly to its prose.
	if strings.Count(prompt, strings.TrimSpace(string(skill.Content))) != 1 {
		t.Fatal("complete pinned reviewer guidance must have one authoritative home")
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
	prompt := executor.workspaceWritePrompt(t.Context(), reviewerAssignment())
	if strings.Index(prompt, "--- END RUNNER-PINNED SKILL ---") > strings.Index(prompt, "Title:") {
		t.Fatal("Codex launch placed shared guidance after variable task data")
	}
}
