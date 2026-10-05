package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
)

func TestParentRenewalCarriesAllMembersAndRequiresFreshGate(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "prepared")
	f := newGuardedPlanFixture(t, func(e *config.VerificationEntrypoint) {
		e.DependencyPaths = []string{"deps"}
		e.Preparation = &config.VerificationPreparation{Command: "/bin/sh", Args: []string{"-c", "mkdir -p deps/pkg; printf '*.js text\\n' > deps/pkg/.gitattributes; printf prepared > \"$1\"", "prepare", marker}}
	})
	if err := os.WriteFile(filepath.Join(f.repo, ".git", "info", "exclude"), []byte("deps/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.integrateMembers(t)
	r := &preparationAuthorityOutageRunner{deliveryMilestoneRunner: f.runner, marker: marker}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	if result := f.service.executeQA(t.Context(), action, "original-parent-review"); result.Outcome != execution.OutcomeBlocked {
		t.Fatal("fixture did not interrupt gate")
	}
	parent := f.parent(t)
	history, err := os.ReadFile(f.service.reviewFeedbackPath(f.parentID))
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := github.ParsePlanManifest(parent.Body)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Amendment++
	manifest.VerificationTimeoutSeconds = 60
	request := github.PlanAmendmentRequest{ExpectedRevision: github.PlanRevision(parent.Body), Reason: "Explicitly renew parent review and complete-gate deadline", Manifest: manifest, RenewParentReview: true}
	preview, err := f.service.PlanDeliveryAmendment(t.Context(), f.parentID, request)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Affected) != 0 || len(preview.CarriedAcceptance) != 2 {
		t.Fatal("parent renewal invalidated accepted child work")
	}
	if err := f.service.ApplyDeliveryAmendment(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	if after := f.parent(t); after.QAFailures != parent.QAFailures || after.QACommit != parent.QACommit {
		t.Fatal("renewal reset history or adopted a different integrated head")
	}
	for cycle := 0; cycle < 3 && f.parent(t).Status != "PR Ready"; cycle++ {
		results, err := f.service.RunCycle(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("renewed delivery failed: %s %s", result.Summary, result.Error)
			}
		}
	}
	current := retainedPlanProgress(t, f)
	if f.parent(t).Status != "PR Ready" || r.reviews != 4 || r.implementations != 2 || r.creates != 1 || current.AttemptID == "original-parent-review" || current.Gate.Historical || current.Gate.Invocation.Outcome != "passed" || current.Publication.PlanRevision != preview.Revision {
		t.Fatal("renewal did not require fresh parent QA/gate or repeated child work")
	}
	base := f.cfg.Verification["complete"]
	effective := base
	effective.TimeoutSeconds = 60
	if base.TimeoutSeconds != 30 || current.Gate.Receipt.SettingsDigest != strings.TrimPrefix(effective.Digest(), "v1:") || current.Gate.Receipt.SettingsDigest == strings.TrimPrefix(base.Digest(), "v1:") {
		t.Fatal("gate budget was not bound independently of unchanged catalog settings")
	}
	archives, err := filepath.Glob(f.service.reviewFeedbackPath(f.parentID) + ".amended-*")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range archives {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(data, history) {
			found = true
		}
	}
	if !found || len(history) == 0 {
		t.Fatal("renewal lost original interrupted-review history")
	}
}
