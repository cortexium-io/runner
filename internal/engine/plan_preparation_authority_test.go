package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type preparationAuthorityOutageRunner struct {
	*deliveryMilestoneRunner
	marker      string
	interrupted bool
}

func (r *preparationAuthorityOutageRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && !r.interrupted {
		if _, err := os.Stat(r.marker); err == nil {
			r.interrupted = true
			return subprocess.Result{}, errors.New("fixture GitHub HTTP 502 after successful preparation")
		}
	}
	return r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
}

func TestPlanPreparationAuthorityOutageResumesWithoutRepeatingQA(t *testing.T) {
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
	failed := f.service.executeQA(t.Context(), action, "prepared-authority-outage")
	before := retainedPlanProgress(t, f)
	if !r.interrupted || failed.Outcome != execution.OutcomeBlocked || r.reviews != 3 || r.creates != 0 || before.PreparedCandidate == nil || before.PreparedCandidate.Fingerprint == before.Candidate.Fingerprint || before.PreparedCandidate.Head != before.Candidate.Head || before.Gate.Receipt != nil || before.Gate.CurrentCandidateCheck != nil || before.Gate.CurrentPreparation.Outcome != "passed" || before.Gate.Invocation.Outcome != "failed" || !before.Gate.Invocation.CleanupResolved || before.Failure != nil || before.Publication != nil {
		t.Fatalf("outage lost safe preparation or created passing proof: %s %s", failed.Summary, failed.Error)
	}
	qa, _ := json.Marshal(before.Candidate)
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := f.service.PlanProjectItemRetry(t.Context(), f.parentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ApplyProjectItemRetry(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	action, err = f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "authority-restored")
	if result.Outcome != execution.OutcomeSucceeded || !result.ResumedCheckpoint || r.reviews != 3 || r.creates != 1 {
		t.Fatalf("retry repeated QA or failed: %s %s", result.Summary, result.Error)
	}
	after := retainedPlanProgress(t, f)
	retainedQA, _ := json.Marshal(after.Candidate)
	if string(qa) != string(retainedQA) || after.Gate.Invocation.ExecutionID == before.Gate.Invocation.ExecutionID || after.Gate.Invocation.Outcome != "passed" || after.Gate.Historical || after.Gate.Receipt == nil || after.Gate.Receipt.Outcome != "passed" || after.Publication == nil || after.QAFailures != before.QAFailures {
		t.Fatal("retry rewrote original QA or relabelled interrupted evidence")
	}
	archives, err := filepath.Glob(f.service.reviewFeedbackPath(f.parentID) + ".verification-*")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, path := range archives {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var record reviewFeedbackRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if p := record.PlanVerification; p != nil && p.Gate != nil && p.Gate.Invocation.ExecutionID == before.Gate.Invocation.ExecutionID && p.Gate.Invocation.Outcome == "failed" && p.PreparedCandidate != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("interrupted gate history was not preserved")
	}
}
