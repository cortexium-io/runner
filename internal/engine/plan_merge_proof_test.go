package engine

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/subprocess"
)

type planMergeProofRunner struct {
	*deliveryMilestoneRunner
	mergeRequests, cancellations int
	enabled                      bool
}

func planProofWarnings(results []RunResult) string {
	var selected []string
	for _, result := range results {
		selected = append(selected, result.Summary+": "+result.Error)
	}
	return strings.Join(selected, "; ")
}

func (r *planMergeProofRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *planMergeProofRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "merge" {
		if containsArgument(args, "--disable-auto") {
			r.cancellations++
			r.enabled = false
		} else {
			r.mergeRequests++
			r.enabled = true
		}
		return subprocess.Result{}, nil
	}
	result, err := r.deliveryMilestoneRunner.Run(ctx, command, args, dir, timeout)
	if err == nil && command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view" && r.enabled {
		var payload map[string]any
		if err := json.Unmarshal([]byte(result.Stdout), &payload); err != nil {
			return result, err
		}
		payload["autoMergeRequest"] = map[string]any{"enabledAt": "2026-01-01T00:00:00Z"}
		body, err := json.Marshal(payload)
		result.Stdout = string(body)
		return result, err
	}
	return result, err
}

func publishedGuardedPlan(t *testing.T) (*deliveryRunFixture, *planMergeProofRunner, *planVerificationProgress) {
	t.Helper()
	f := newGuardedPlanFixture(t)
	f.integrateMembers(t)
	action, err := f.service.source.Authorize(t.Context(), f.parent(t))
	if err != nil {
		t.Fatal(err)
	}
	result := f.service.executeQA(t.Context(), action, "accepted-before-merge")
	if result.Outcome != execution.OutcomeSucceeded || result.Error != "" || f.parent(t).Status != "PR Ready" {
		t.Fatalf("fixture publication: %s %s %s", result.Outcome, result.Summary, result.Error)
	}
	r := &planMergeProofRunner{deliveryMilestoneRunner: f.runner}
	f.cfg.GitHubProject.AutoMerge = true
	progress := retainedPlanProgress(t, f)
	if _, err := os.Stat(progress.Metadata.WorktreePath); err != nil || result.WorktreeCleaned {
		t.Fatal("pending final PR lost its accepted workspace before merge")
	}
	return f, r, progress
}

func TestPlanMergeRequiresApplicableProtectedProof(t *testing.T) {
	for _, change := range []string{"unchanged", "missing-progress", "tampered-receipt", "changed-runtime", "changed-reviewer", "changed-guard", "enabled-stale"} {
		t.Run(change, func(t *testing.T) {
			f, r, before := publishedGuardedPlan(t)
			switch change {
			case "missing-progress", "tampered-receipt":
				record, err := f.service.readReviewFeedbackRecord(f.parent(t))
				if err != nil {
					t.Fatal(err)
				}
				if change == "missing-progress" {
					record.PlanVerification = nil
				} else {
					record.PlanVerification.Gate.Receipt.ReportDigest = strings.Repeat("a", 64)
				}
				if err := f.service.writeReviewFeedback(*record); err != nil {
					t.Fatal(err)
				}
			case "changed-runtime", "enabled-stale":
				if err := os.WriteFile(f.cfg.Verification["complete"].RuntimePaths[0], []byte("different executable runtime"), 0o600); err != nil {
					t.Fatal(err)
				}
				r.enabled = change == "enabled-stale"
			case "changed-reviewer":
				profile := f.cfg.Roles["reviewer"]
				profile.Access = config.RoleAccessSandboxed
				f.cfg.Roles["reviewer"] = profile
			case "changed-guard":
				entry := f.cfg.Verification["complete"]
				entry.CurrentCandidateCheck.Args = []string{"-c", "exit 0"}
				f.cfg.Verification["complete"] = entry
			}
			var err error
			f.service, err = New(f.cfg, r)
			if err != nil {
				t.Fatal(err)
			}
			items, err := f.service.source.LifecycleItems(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			warnings, _, reconcileErr := f.service.reconcilePullRequests(t.Context(), items)
			if change == "unchanged" {
				if reconcileErr != nil || r.mergeRequests != 1 || f.parent(t).Status != "PR Ready" {
					t.Fatalf("applicable proof refused: %v requests=%d status=%s warnings=%s", reconcileErr, r.mergeRequests, f.parent(t).Status, planProofWarnings(warnings))
				}
				after := retainedPlanProgress(t, f)
				old, _ := json.Marshal(before.Gate)
				current, _ := json.Marshal(after.Gate)
				if string(old) != string(current) {
					t.Fatal("merge observation reran or relabeled existing proof")
				}
			} else {
				if r.mergeRequests != 0 {
					t.Fatal("inapplicable proof enabled final merge")
				}
				// Changed catalog invalidates signed authority before the proof
				// hook; every other stale observation uses admitted QA recovery.
				if change != "changed-guard" && (reconcileErr != nil || f.parent(t).Status != "Agent QA") {
					t.Fatalf("proof did not return to QA: %v status=%s warnings=%s", reconcileErr, f.parent(t).Status, planProofWarnings(warnings))
				}
				if change == "enabled-stale" && (r.cancellations != 1 || r.enabled) {
					t.Fatal("stale proof left automatic merge armed")
				}
			}
			if r.reviews != 3 || r.implementations != 2 || r.creates != 1 {
				t.Fatal("deterministic reconciliation launched model/publication work")
			}
		})
	}
}

func TestPlanMergeChangedBaseReturnsToQAWithoutGate(t *testing.T) {
	f, r, before := publishedGuardedPlan(t)
	updater := filepath.Join(t.TempDir(), "base-update")
	runGitTest(t, "", "clone", "--branch", "main", f.remote, updater)
	runGitTest(t, updater, "config", "user.name", "Fixture")
	runGitTest(t, updater, "config", "user.email", "fixture@example.com")
	if err := os.WriteFile(filepath.Join(updater, "base-addition.txt"), []byte("independent destination change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, updater, "add", "base-addition.txt")
	runGitTest(t, updater, "commit", "-m", "Advance destination")
	runGitTest(t, updater, "push", "origin", "main")
	r.base = strings.TrimSpace(runGitTest(t, updater, "rev-parse", "HEAD"))
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	warnings, _, err := f.service.reconcilePullRequests(t.Context(), items)
	if err != nil || f.parent(t).Status != "Agent QA" || r.mergeRequests != 0 {
		t.Fatalf("changed base did not renew QA: %v status=%s warnings=%s", err, f.parent(t).Status, planProofWarnings(warnings))
	}
	if r.reviews != 3 || r.implementations != 2 || retainedPlanProgress(t, f).Gate.Invocation.ExecutionID != before.Gate.Invocation.ExecutionID {
		t.Fatal("base refresh reran a model or gate inside reconciliation")
	}
}

func TestPlanTerminalReconciliationDoesNotRequireVerificationRuntime(t *testing.T) {
	f, r, before := publishedGuardedPlan(t)
	r.merged = true
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", "refs/heads/main", before.Candidate.Head)
	runGitTest(t, "", "--git-dir", f.remote, "update-ref", "-d", "refs/heads/"+f.parent(t).Branch)
	if err := os.Remove(f.cfg.Verification["complete"].RuntimePaths[0]); err != nil {
		t.Fatal(err)
	}
	var err error
	f.service, err = New(f.cfg, r)
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	warnings, _, err := f.service.reconcilePullRequests(t.Context(), items)
	if err != nil || f.parent(t).Status != "Done" || r.mergeRequests != 0 || r.reviews != 3 {
		t.Fatalf("terminal delivery depended on old tools: %v status=%s warnings=%s", err, f.parent(t).Status, planProofWarnings(warnings))
	}
	if _, err := os.Stat(before.Metadata.WorktreePath); !os.IsNotExist(err) {
		t.Fatal("confirmed merge did not perform ordinary terminal cleanup")
	}
}
