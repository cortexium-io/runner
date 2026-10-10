package github

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type planHistoryRecoveryRunner struct {
	*pullRequestTestRunner
	t                 *testing.T
	current           workspace.PublicationRecord
	state             string
	readbackFailure   bool
	pushResponseError error
	closeBeforePush   bool
	views             int
}

func (r *planHistoryRecoveryRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) > 1 && args[0] == "pr" && args[1] == "view" {
		r.views++
		head := strings.TrimSpace(runGitTest(r.t, "", "--git-dir", r.publicationRemote, "rev-parse", r.current.DestinationRef))
		if head == r.current.CommitOID && r.readbackFailure {
			r.readbackFailure = false
			return subprocess.Result{Stderr: "HTTP 503: Service Unavailable", ExitCode: 1}, errors.New("exit status 1")
		}
		state := r.state
		if state == "" {
			state = "OPEN"
		}
		if r.closeBeforePush && r.views == 2 {
			state = "CLOSED"
		}
		r.viewOutput = terminalPlanPR(r.t, r.current, state, head, "owner/repo", "main")
	}
	result, err := r.pullRequestTestRunner.Run(ctx, command, args, dir, timeout)
	if command == "git" && slices.Contains(args, "push") && err == nil && r.pushResponseError != nil {
		err = r.pushResponseError
		r.pushResponseError = nil
		result.Stderr = err.Error()
		result.ExitCode = 1
	}
	return result, err
}

func (r *planHistoryRecoveryRunner) RunFailClosed(ctx context.Context, command string, args []string, dir string, timeout time.Duration, _, _ int) (subprocess.Result, error) {
	return r.Run(ctx, command, args, dir, timeout)
}

func acceptedPlanHistoryReplacement(t *testing.T) (workspace.Metadata, workspace.PublicationRecord, workspace.PublicationRecord, AuthorizedAction, *planHistoryRecoveryRunner) {
	t.Helper()
	metadata, initial, action := acceptedTerminalPlan(t)
	// A real merge graph with identical source bytes makes the regression
	// specifically about history, rather than an implementation correction.
	side := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "commit-tree", initial.TreeOID, "-p", initial.CommitOID, "-m", "accepted member"))
	merged := strings.TrimSpace(runGitTest(t, metadata.WorktreePath, "commit-tree", initial.TreeOID, "-p", initial.CommitOID, "-p", side, "-m", "combined accepted members"))
	runGitTest(t, metadata.WorktreePath, "update-ref", initial.DestinationRef, merged, initial.CommitOID)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	snapshot, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	prior, err := provider.RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Accepted combined plan.", "Combined parent accepted.", workspace.PublicationEvidence{
		PlanRevision: initial.PlanRevision, VerificationReceipt: "protected prior complete proof", VerificationDigest: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	runGitTest(t, metadata.WorktreePath, "push", "origin", prior.CommitOID+":"+prior.DestinationRef)
	if _, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Normalize accepted plan history", config.MergeMethodRebase); err != nil {
		t.Fatal(err)
	}
	snapshot, err = workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	current, err := provider.RecordPublicationAcceptance(t.Context(), metadata, snapshot, "Fresh parent QA accepted.", "Normalized parent accepted.", workspace.PublicationEvidence{
		PlanRevision: initial.PlanRevision, VerificationReceipt: "protected fresh complete proof", VerificationDigest: strings.Repeat("c", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if current.CommitOID == prior.CommitOID || current.TreeOID != prior.TreeOID {
		t.Fatal("fixture did not normalize exact-tree merge history")
	}
	action.Item.PullRequest, action.Item.QACommit = "https://github.com/owner/repo/pull/12", prior.CommitOID
	action = authorizedPullRequestTestAction(action.Item)
	runner := &planHistoryRecoveryRunner{pullRequestTestRunner: &pullRequestTestRunner{
		existingOpen: true, viewHead: action.Item.Branch, publicationRemote: metadata.RepoRoot + "/../origin.git",
	}, t: t, current: current}
	return metadata, prior, current, action, runner
}

func TestPlanHistoryReplacementUsesExactLeaseAndFreshProof(t *testing.T) {
	for _, tc := range []struct {
		name            string
		readbackFailure bool
		pushError       error
		cancelled       bool
	}{
		{name: "normal"},
		{name: "interrupted readback", readbackFailure: true},
		{name: "lost push response", pushError: errors.New("HTTP 503: Service Unavailable")},
		{name: "cancelled after push", pushError: context.Canceled, cancelled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata, prior, current, action, runner := acceptedPlanHistoryReplacement(t)
			runner.readbackFailure, runner.pushResponseError = tc.readbackFailure, tc.pushError
			guardCalls := 0
			manager := NewPullRequestManager(runner, staticActionRefresher{}).WithPlanHistoryReplacement(prior, config.MergeMethodRebase)
			published, err := manager.PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", config.MergeMethodRebase, func(context.Context, AuthorizedAction) error { guardCalls++; return nil })
			if tc.cancelled {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("lost cancellation=%v, want cancellation", err)
				}
				published, err = manager.PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", config.MergeMethodRebase, func(context.Context, AuthorizedAction) error { guardCalls++; return nil })
			}
			if err != nil || published.CommitSHA != current.CommitOID || guardCalls == 0 {
				t.Fatalf("replacement=%#v guard=%d err=%v", published, guardCalls, err)
			}
			want := "--force-with-lease=" + current.DestinationRef + ":" + prior.CommitOID
			pushes := 0
			for _, call := range runner.gitCalls {
				if strings.Contains(call, " push ") || strings.HasPrefix(call, "push ") {
					pushes++
					if !strings.Contains(call, want) || !strings.Contains(call, current.CommitOID+":"+current.DestinationRef) {
						t.Fatalf("incorrect replacement push: %s", call)
					}
				}
			}
			if pushes != 1 {
				t.Fatalf("pushes=%d, want one", pushes)
			}
			if head := strings.TrimSpace(runGitTest(t, "", "--git-dir", runner.publicationRemote, "rev-parse", current.DestinationRef)); head != current.CommitOID {
				t.Fatalf("remote head=%s, want accepted %s", head, current.CommitOID)
			}
			if err := workspace.NewGitProvider(subprocess.OSRunner{}).VerifyTerminalPlanAcceptance(metadata, prior); err != nil {
				t.Fatalf("old proof changed: %v", err)
			}
			callsBeforeRepeat := len(runner.gitCalls)
			if _, err := manager.PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", config.MergeMethodRebase, func(context.Context, AuthorizedAction) error { return nil }); err != nil {
				t.Fatalf("repeat recovery: %v", err)
			}
			for _, call := range runner.gitCalls[callsBeforeRepeat:] {
				if strings.Contains(call, " push ") || strings.HasPrefix(call, "push ") {
					t.Fatalf("repeat recovery pushed again: %s", call)
				}
			}
		})
	}
}

type planHistoryRevokingRefresher struct{ calls int }

func (r *planHistoryRevokingRefresher) RefreshAction(_ context.Context, action AuthorizedAction) (AuthorizedAction, error) {
	r.calls++
	if r.calls == 2 {
		return AuthorizedAction{}, errors.New("parent approval changed at final boundary")
	}
	return action, nil
}

func TestPlanHistoryReplacementRechecksFinalAuthorityAndPRState(t *testing.T) {
	for _, changed := range []string{"approval", "PR closed"} {
		t.Run(changed, func(t *testing.T) {
			metadata, prior, current, action, runner := acceptedPlanHistoryReplacement(t)
			var refresher ActionRefresher = staticActionRefresher{}
			if changed == "approval" {
				refresher = &planHistoryRevokingRefresher{}
			} else {
				runner.closeBeforePush = true
			}
			_, err := NewPullRequestManager(runner, refresher).WithPlanHistoryReplacement(prior, config.MergeMethodRebase).PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", config.MergeMethodRebase, func(context.Context, AuthorizedAction) error { return nil })
			if err == nil {
				t.Fatal("changed final boundary permitted replacement")
			}
			for _, call := range runner.gitCalls {
				if strings.Contains(call, " push ") || strings.HasPrefix(call, "push ") {
					t.Fatalf("changed final boundary reached push: %s", call)
				}
			}
		})
	}
}

func TestPlanPublicationReplacesOnlyProtectedTrackedPredecessor(t *testing.T) {
	for _, tc := range []struct {
		name       string
		closePR    bool
		refuseGate bool
	}{
		{name: "accepted tracked predecessor"},
		{name: "closed before push", closePR: true},
		{name: "fresh complete proof refused", refuseGate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata, prior, current, action, runner := acceptedPlanHistoryReplacement(t)
			runner.closeBeforePush = tc.closePR
			guardCalls := 0
			_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", config.MergeMethodRebase, func(context.Context, AuthorizedAction) error {
				guardCalls++
				if tc.refuseGate {
					return errors.New("fresh complete proof refused")
				}
				return nil
			})
			refused := tc.closePR || tc.refuseGate
			if (err != nil) != refused {
				t.Fatalf("replacement error=%v, want refusal=%t", err, refused)
			}
			pushes := 0
			for _, call := range runner.gitCalls {
				if strings.Contains(call, " push ") || strings.HasPrefix(call, "push ") {
					pushes++
					if !strings.Contains(call, "--force-with-lease="+prior.DestinationRef+":"+prior.CommitOID) || !strings.Contains(call, current.CommitOID+":"+current.DestinationRef) {
						t.Fatalf("replacement lost exact signed prior-head lease: %s", call)
					}
				}
			}
			wantPushes, wantHead := 1, current.CommitOID
			if refused {
				wantPushes, wantHead = 0, prior.CommitOID
			}
			if pushes != wantPushes || !tc.closePR && guardCalls == 0 {
				t.Fatalf("pushes=%d guardCalls=%d, want pushes=%d", pushes, guardCalls, wantPushes)
			}
			if head := strings.TrimSpace(runGitTest(t, "", "--git-dir", runner.publicationRemote, "rev-parse", prior.DestinationRef)); head != wantHead {
				t.Fatalf("remote head=%s, want %s", head, wantHead)
			}
		})
	}
}

func TestPlanHistoryReplacementRefusesChangedBindings(t *testing.T) {
	for _, tc := range []struct {
		name              string
		change            func(*workspace.PublicationRecord, *workspace.PublicationRecord, *AuthorizedAction, *planHistoryRecoveryRunner)
		method            string
		publicationMethod string
		guardError        bool
	}{
		{name: "tampered predecessor", change: func(p, _ *workspace.PublicationRecord, _ *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			p.VerificationReceipt = "tampered"
		}},
		{name: "changed source tree", change: func(_, c *workspace.PublicationRecord, _ *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			c.TreeOID = strings.Repeat("d", 40)
		}},
		{name: "changed approved base", change: func(_, c *workspace.PublicationRecord, _ *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			c.ApprovedBaseOID = strings.Repeat("d", 40)
		}},
		{name: "changed plan revision", change: func(_, c *workspace.PublicationRecord, _ *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			c.PlanRevision = "v1:" + strings.Repeat("d", 64)
		}},
		{name: "different tracked PR", change: func(_, _ *workspace.PublicationRecord, a *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			a.Item.PullRequest = "https://github.com/owner/repo/pull/13"
			*a = authorizedPullRequestTestAction(a.Item)
		}},
		{name: "different accepted head", change: func(_, _ *workspace.PublicationRecord, a *AuthorizedAction, _ *planHistoryRecoveryRunner) {
			a.Item.QACommit = strings.Repeat("d", 40)
			*a = authorizedPullRequestTestAction(a.Item)
		}},
		{name: "closed PR", change: func(_, _ *workspace.PublicationRecord, _ *AuthorizedAction, r *planHistoryRecoveryRunner) {
			r.state = "CLOSED"
		}},
		{name: "merged old head", change: func(_, _ *workspace.PublicationRecord, _ *AuthorizedAction, r *planHistoryRecoveryRunner) {
			r.state = "MERGED"
		}},
		{name: "different remote head", change: func(p, _ *workspace.PublicationRecord, _ *AuthorizedAction, r *planHistoryRecoveryRunner) {
			runGitTest(r.t, "", "--git-dir", r.publicationRemote, "update-ref", p.DestinationRef, p.ApprovedBaseOID, p.CommitOID)
		}},
		{name: "merge policy", method: config.MergeMethodMerge},
		{name: "mismatched publication policy", method: config.MergeMethodRebase, publicationMethod: config.MergeMethodMerge},
		{name: "fresh guard refused", guardError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata, prior, current, action, runner := acceptedPlanHistoryReplacement(t)
			if tc.change != nil {
				tc.change(&prior, &current, &action, runner)
			}
			method := tc.method
			if method == "" {
				method = config.MergeMethodRebase
			}
			publicationMethod := tc.publicationMethod
			if publicationMethod == "" {
				publicationMethod = method
			}
			_, err := NewPullRequestManager(runner, staticActionRefresher{}).WithPlanHistoryReplacement(prior, method).PublishAuthorized(t.Context(), action, metadata, current, "main", "origin", publicationMethod, func(context.Context, AuthorizedAction) error {
				if tc.guardError {
					return errors.New("fresh guard refused")
				}
				return nil
			})
			if err == nil {
				t.Fatal("changed binding authorized history replacement")
			}
			for _, call := range runner.gitCalls {
				if strings.Contains(call, " push ") || strings.HasPrefix(call, "push ") {
					t.Fatalf("refusal reached push: %s", call)
				}
			}
		})
	}
}
