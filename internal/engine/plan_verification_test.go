package engine

import (
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
)

func TestParentCompleteVerificationIsBoundFromApprovedCatalog(t *testing.T) {
	f := newProductionDelivery(t)
	parent, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the signed parent/reviewer boundary without executing work;
	// production admission separately requires integrated members.
	if err := f.service.source.Transition(t.Context(), parent, "Agent QA", "Review combined outcome", ""); err != nil {
		t.Fatal(err)
	}
	parent, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	content, err := parent.DelegatedContent()
	if err != nil {
		t.Fatal(err)
	}
	entry := f.service.cfg.Verification["complete"]
	for _, tc := range []struct {
		name   string
		change func(*config.VerificationEntrypoint, *execution.Assignment) bool
		valid  bool
	}{
		{"current approved entry", func(*config.VerificationEntrypoint, *execution.Assignment) bool { return true }, true},
		{"missing entry", func(*config.VerificationEntrypoint, *execution.Assignment) bool { return false }, false},
		{"same ID changed command", func(e *config.VerificationEntrypoint, _ *execution.Assignment) bool {
			e.Command = "/bin/false"
			return true
		}, false},
		{"changed preparation", func(e *config.VerificationEntrypoint, _ *execution.Assignment) bool {
			e.Preparation = &config.VerificationPreparation{Command: "npm", Args: []string{"ci"}}
			return true
		}, false},
		{"not a review", func(_ *config.VerificationEntrypoint, a *execution.Assignment) bool {
			a.Spec.ReviewRequired = false
			return true
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := entry
			assignment := f.service.assignment(parent.Item, content, nil, nil)
			f.service.cfg.Verification = map[string]config.VerificationEntrypoint{}
			if tc.change(&candidate, &assignment) {
				f.service.cfg.Verification["complete"] = candidate
			}
			err := f.service.bindDeliveryAssignment(t.Context(), parent.Item, &assignment)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && (assignment.Spec.PlanContext.CompleteVerification != "complete" || assignment.Spec.ReviewScope != execution.ReviewScopePlan || assignment.Spec.VerificationBoundary != execution.VerificationComplete) {
				t.Fatal("parent did not receive the exact approved scheduled complete entrypoint")
			}
			if !tc.valid && assignment.Spec.PlanContext != nil && assignment.Spec.PlanContext.CompleteVerification != "" {
				t.Fatal("invalid binding promised a complete verification gate")
			}
		})
	}
	if f.runner.implementations != 0 || f.runner.reviews != 0 || f.runner.planPushes != 0 || f.runner.creates != 0 {
		t.Fatal("binding check performed model work or Git/PR publication")
	}
}
