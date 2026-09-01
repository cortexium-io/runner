package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type pullRequestTestRunner struct {
	subprocess.OSRunner
	calls             []string
	createdBody       string
	existingOpen      bool
	ambiguousOpen     bool
	viewHead          string
	viewHeadOID       string
	viewHeadRepo      string
	viewBase          string
	viewBaseOID       string
	viewOutput        string
	configuredActor   string
	autoMergeErr      error
	publicationRemote string
	gitCalls          []string
}

type staticActionRefresher struct {
	err error
}

func (r staticActionRefresher) RefreshAction(_ context.Context, action AuthorizedAction) (AuthorizedAction, error) {
	if r.err != nil {
		return AuthorizedAction{}, r.err
	}
	return action, nil
}

func authorizedPullRequestTestAction(item WorkItem) AuthorizedAction {
	if item.ID == "" {
		item.ID = "PVTI_test"
	}
	if item.Branch == "" {
		item.Branch = "cortexium/task"
	}
	return newAuthorizedAction(item, "reviewer", "test", "authenticated-by-project-source")
}

func (r *pullRequestTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		r.gitCalls = append(r.gitCalls, strings.Join(args, " "))
		if r.publicationRemote != "" {
			args = append([]string(nil), args...)
			for index := range args {
				if args[index] == "https://github.com/owner/repo.git" {
					args[index] = r.publicationRemote
				}
				if args[index] == "protocol.https.allow=always" {
					args[index] = "protocol.file.allow=always"
				}
			}
		}
		return r.OSRunner.Run(ctx, command, args, dir, timeout)
	}
	if command != "gh" {
		return subprocess.Result{}, errors.New("unexpected command: " + command)
	}
	joined := strings.Join(args, " ")
	r.calls = append(r.calls, joined)
	switch {
	case joined == "api user --jq .login":
		actor := r.configuredActor
		if actor == "" {
			actor = "maintainer"
		}
		return subprocess.Result{Stdout: actor + "\n"}, nil
	case strings.HasPrefix(joined, "pr list "):
		if r.ambiguousOpen {
			return subprocess.Result{Stdout: `[{"url":"https://github.com/owner/repo/pull/12","number":12,"headRefName":"cortexium/task","baseRefName":"main"},{"url":"https://github.com/owner/repo/pull/13","number":13,"headRefName":"cortexium/task","baseRefName":"main"}]`}, nil
		}
		if r.existingOpen {
			return subprocess.Result{Stdout: `[{"url":"https://github.com/owner/repo/pull/12","number":12,"headRefName":"cortexium/task","baseRefName":"main"}]`}, nil
		}
		return subprocess.Result{Stdout: `[]`}, nil
	case strings.HasPrefix(joined, "pr create "):
		for index, arg := range args {
			if arg == "--body" && index+1 < len(args) {
				r.createdBody = args[index+1]
				break
			}
		}
		return subprocess.Result{Stdout: "https://github.com/owner/repo/pull/12\n"}, nil
	case strings.HasPrefix(joined, "pr view "):
		if r.viewOutput != "" {
			return subprocess.Result{Stdout: r.viewOutput}, nil
		}
		head := r.viewHead
		if head == "" {
			head = "cortexium/task"
		}
		base := r.viewBase
		if base == "" {
			base = "main"
		}
		headOID := r.viewHeadOID
		if headOID == "" {
			headOID = strings.Repeat("a", 40)
		}
		headRepo := r.viewHeadRepo
		if headRepo == "" {
			headRepo = "owner/repo"
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"` + headRepo + `"},"headRefName":"` + head + `","headRefOid":"` + headOID + `","baseRefName":"` + base + `","baseRefOid":"` + r.viewBaseOID + `","mergeStateStatus":"CLEAN","comments":[],"reviews":[]}`}, nil
	case strings.HasPrefix(joined, "pr merge "):
		if r.autoMergeErr != nil {
			return subprocess.Result{Stderr: "repository auto-merge is disabled", ExitCode: 1}, r.autoMergeErr
		}
		return subprocess.Result{}, nil
	default:
		return subprocess.Result{}, errors.New("unexpected gh command: " + joined)
	}
}

func (r *pullRequestTestRunner) RunFailClosed(ctx context.Context, command string, args []string, dir string, timeout time.Duration, _, _ int) (subprocess.Result, error) {
	return r.Run(ctx, command, args, dir, timeout)
}

func TestPullRequestFeedbackEnforcesCombinedEntryLimitBeforeAggregation(t *testing.T) {
	for count := MaxPullRequestFeedbackEntries; count <= MaxPullRequestFeedbackEntries+1; count++ {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			comments := make([]map[string]any, count)
			for index := range comments {
				comments[index] = map[string]any{
					"body": "feedback", "authorAssociation": "MEMBER", "author": map[string]string{"login": "maintainer"},
				}
			}
			payload, err := json.Marshal(map[string]any{
				"url": "https://github.com/owner/repo/pull/12", "number": 12, "state": "OPEN", "headRefName": "cortexium/task", "headRefOid": "head", "baseRefName": "main", "mergeStateStatus": "CLEAN",
				"comments": comments, "reviews": []any{},
			})
			if err != nil {
				t.Fatal(err)
			}
			runner := &pullRequestTestRunner{viewOutput: string(payload)}
			details, inspectErr := NewPullRequestManager(runner, staticActionRefresher{}).inspect(t.Context(), "owner/repo", "12")
			if count == MaxPullRequestFeedbackEntries {
				if inspectErr != nil || len(details.Feedback) > maxPullRequestFeedbackBytes {
					t.Fatalf("exact feedback limit failed: bytes=%d error=%v", len(details.Feedback), inspectErr)
				}
			} else if inspectErr == nil || !strings.Contains(inspectErr.Error(), "fixed limit of 100") || details.Feedback != "" {
				t.Fatalf("feedback overflow was accepted: details=%#v error=%v", details, inspectErr)
			}
		})
	}
}

func TestPullRequestFeedbackPublishesTrustedDiscussionReferenceOnly(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"url": "https://github.com/owner/repo/pull/12", "number": 12, "state": "OPEN", "headRefName": "cortexium/task", "headRefOid": "head", "baseRefName": "main", "mergeStateStatus": "CLEAN",
		"comments": []any{
			map[string]any{
				"body":              "Please add the missing edge-case test.",
				"authorAssociation": "MEMBER",
				"author":            map[string]string{"login": "maintainer"},
			},
		},
		"reviews": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	details, err := NewPullRequestManager(&pullRequestTestRunner{viewOutput: string(payload)}, staticActionRefresher{}).inspect(t.Context(), "owner/repo", "12")
	if err != nil {
		t.Fatalf("inspect trusted feedback: %v", err)
	}
	if details.Feedback != "Inspect the pull request discussion at https://github.com/owner/repo/pull/12 locally before continuing." {
		t.Fatalf("trusted feedback = %q", details.Feedback)
	}
	if strings.Contains(details.Feedback, "missing edge-case test") {
		t.Fatalf("trusted feedback exposed raw body: %q", details.Feedback)
	}
}

func TestPullRequestFeedbackIgnoresUntrustedCommentBody(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"url": "https://github.com/owner/repo/pull/12", "number": 12, "state": "OPEN", "headRefName": "cortexium/task", "headRefOid": "head", "baseRefName": "main", "mergeStateStatus": "CLEAN",
		"comments": []any{
			map[string]any{
				"body":              "attacker prompt",
				"authorAssociation": "MEMBER",
				"author":            map[string]string{"login": "attacker"},
			},
		},
		"reviews": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	details, err := NewPullRequestManager(&pullRequestTestRunner{viewOutput: string(payload)}, staticActionRefresher{}).inspect(t.Context(), "owner/repo", "12")
	if err != nil {
		t.Fatalf("inspect untrusted feedback: %v", err)
	}
	if details.Feedback != "" {
		t.Fatalf("untrusted feedback should be dropped, got %q", details.Feedback)
	}
}

func TestGitHubPullRequestManagerRequestsAutoMergeWithoutBypassingProtections(t *testing.T) {
	runner := &pullRequestTestRunner{}
	head := strings.Repeat("a", 40)
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "https://github.com/owner/repo/pull/12", QACommit: head})
	err := NewPullRequestManager(runner, staticActionRefresher{}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", "")
	if err != nil {
		t.Fatalf("request auto merge: %v", err)
	}
	if got := strings.Join(runner.calls, "\n"); got != "pr view https://github.com/owner/repo/pull/12 --repo owner/repo --json url,number,state,headRepository,headRefName,headRefOid,baseRefName,baseRefOid,mergeStateStatus,autoMergeRequest,comments,reviews\npr merge https://github.com/owner/repo/pull/12 --repo owner/repo --auto --merge --match-head-commit "+head {
		t.Fatalf("auto-merge command = %q", got)
	}
	if strings.Contains(strings.Join(runner.calls, " "), "--admin") {
		t.Fatal("auto-merge command bypassed repository protections")
	}
}

func TestGitHubPullRequestManagerUsesConfiguredAutoMergeMethod(t *testing.T) {
	runner := &pullRequestTestRunner{}
	head := strings.Repeat("a", 40)
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "12", QACommit: head})
	if err := NewPullRequestManager(runner, staticActionRefresher{}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", config.MergeMethodRebase); err != nil {
		t.Fatalf("request rebase auto merge: %v", err)
	}
	if got := strings.Join(runner.calls, "\n"); !strings.Contains(got, "pr merge 12 --repo owner/repo --auto --rebase --match-head-commit "+head) {
		t.Fatalf("auto-merge command did not use rebase: %q", got)
	}
}

func TestGitHubPullRequestManagerReportsAutoMergeFailure(t *testing.T) {
	runner := &pullRequestTestRunner{autoMergeErr: errors.New("exit status 1")}
	head := strings.Repeat("a", 40)
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "12", QACommit: head})
	err := NewPullRequestManager(runner, staticActionRefresher{}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", "")
	if err == nil || !strings.Contains(err.Error(), "automatic pull request merge") || !strings.Contains(err.Error(), "auto-merge is disabled") {
		t.Fatalf("auto-merge failure = %v", err)
	}
}

func TestGitHubPullRequestManagerCancelsAutoMergeBeforeRework(t *testing.T) {
	runner := &pullRequestTestRunner{}
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "12"})
	err := NewPullRequestManager(runner, staticActionRefresher{}).CancelAutoMergeAuthorized(t.Context(), action)
	if err != nil {
		t.Fatalf("cancel auto merge: %v", err)
	}
	if got := strings.Join(runner.calls, "\n"); got != "pr merge 12 --repo owner/repo --disable-auto" {
		t.Fatalf("cancel auto-merge command = %q", got)
	}
}

func TestGitHubPullRequestManagerRejectsUnsafeAutoMergeSelector(t *testing.T) {
	runner := &pullRequestTestRunner{}
	head := strings.Repeat("a", 40)
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "--admin", QACommit: head})
	err := NewPullRequestManager(runner, staticActionRefresher{}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", "")
	if err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("unsafe auto-merge selector was accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("GitHub CLI was called before selector validation: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRequiresAuthorizedBoundAction(t *testing.T) {
	runner := &pullRequestTestRunner{}
	manager := NewPullRequestManager(runner, staticActionRefresher{})
	head := strings.Repeat("a", 40)
	if err := manager.RequestAutoMergeAuthorized(t.Context(), AuthorizedAction{}, head, "main", "", ""); err == nil || !strings.Contains(err.Error(), "validated Runner authority") {
		t.Fatalf("unauthorized auto-merge was accepted: %v", err)
	}
	action := newAuthorizedAction(
		WorkItem{ID: "PVTI_1", Repository: "owner/repo", PullRequest: "12", QACommit: head},
		"reviewer", "pr_ready", "authenticated-by-project-source",
	)
	if err := manager.RequestAutoMergeAuthorized(t.Context(), action, strings.Repeat("b", 40), "main", "", ""); err == nil || !strings.Contains(err.Error(), "not part of the authorized Project action") {
		t.Fatalf("unbound auto-merge head was accepted: %v", err)
	}
	modified := action
	modified.Item.Repository = "attacker/repo"
	if err := manager.CancelAutoMergeAuthorized(t.Context(), modified); err == nil || !strings.Contains(err.Error(), "modified after validation") {
		t.Fatalf("modified authorized action was accepted: %v", err)
	}
	if err := manager.RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", ""); err != nil {
		t.Fatalf("authorized auto-merge was rejected: %v", err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("unauthorized operations reached GitHub: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRefreshesAuthorityBeforeMergeControl(t *testing.T) {
	runner := &pullRequestTestRunner{}
	head := strings.Repeat("a", 40)
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "12", QACommit: head})
	err := NewPullRequestManager(runner, staticActionRefresher{err: errors.New("Project action changed")}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", "", "")
	if err == nil || !strings.Contains(err.Error(), "refresh Project authority") {
		t.Fatalf("stale merge authority was accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("stale merge authority reached GitHub: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRejectsPullRequestIdentityDriftBeforeAutoMerge(t *testing.T) {
	head := strings.Repeat("a", 40)
	runner := &pullRequestTestRunner{
		viewHeadOID: head,
		viewBaseOID: strings.Repeat("b", 40),
	}
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "12", QACommit: head})
	err := NewPullRequestManager(runner, staticActionRefresher{}).RequestAutoMergeAuthorized(t.Context(), action, head, "main", strings.Repeat("c", 40), "")
	if err == nil || !strings.Contains(err.Error(), "automatic merge pull request identity changed") || !strings.Contains(err.Error(), "base commit") {
		t.Fatalf("pull request identity drift was accepted for auto-merge: %v", err)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "pr merge ") {
		t.Fatalf("identity-drifted pull request reached merge control: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerPublishesExactAcceptedTupleUnderSanitizedGit(t *testing.T) {
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hook, "pre-push"), []byte("#!/bin/sh\ntouch "+hookMarker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, remote, metadata, record, action := acceptedPublicationTupleWithSetup(t, func(repo, _ string) {
		runGitTest(t, repo, "config", "core.hooksPath", hook)
		runGitTest(t, repo, "config", "commit.gpgSign", "true")
		runGitTest(t, repo, "config", "remote.origin.pushurl", "https://github.com/attacker/repo.git")
		runGitTest(t, repo, "config", "remote.origin.push", "HEAD:refs/heads/attacker")
		runGitTest(t, repo, "config", "url.https://github.com/attacker/repo.git.insteadOf", "https://github.com/owner/repo.git")
		runGitTest(t, repo, "config", "filter.attacker.smudge", "sh -c 'touch "+hookMarker+"'")
	})
	runner := &pullRequestTestRunner{
		publicationRemote: remote,
		viewHeadOID:       record.CommitOID,
		viewBaseOID:       record.ApprovedBaseOID,
	}
	manager := NewPullRequestManager(runner, staticActionRefresher{})
	qaReport := "**Verdict:** Accepted\n\n### Checks performed\n\n- All QA checks passed."
	published, err := manager.PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, qaReport)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if published.URL != "https://github.com/owner/repo/pull/12" || published.Number != 12 || published.CommitSHA == "" {
		t.Fatalf("unexpected publication %#v", published)
	}
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", record.DestinationRef))
	remoteTree := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", record.DestinationRef+"^{tree}"))
	if remoteHead != record.CommitOID || remoteTree != record.TreeOID || published.CommitSHA != record.CommitOID {
		t.Fatalf("published tuple differs: head=%q tree=%q published=%#v record=%#v", remoteHead, remoteTree, published, record)
	}
	gitCalls := strings.Join(runner.gitCalls, "\n")
	if !strings.Contains(gitCalls, "push --porcelain --no-verify https://github.com/owner/repo.git "+record.CommitOID+":"+record.DestinationRef) ||
		strings.Contains(gitCalls, "HEAD:"+record.DestinationRef) || strings.Contains(gitCalls, " push origin ") {
		t.Fatalf("publication did not use the exact literal URL and OID refspec:\n%s", gitCalls)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository pre-push hook ran: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	if !strings.Contains(joined, "pr create --repo owner/repo --base main --head cortexium/task") {
		t.Fatalf("PR creation did not use the approved repository and refs:\n%s", joined)
	}
	wantBody := "Created by the local Project Runner after agent QA passed.\n\n" +
		"Source: https://github.com/owner/repo/issues/1\n\n" +
		"## Agent QA\n\nRunner recorded an accepted QA classification for the exact published commit. Detailed model-authored evidence remains local."
	if runner.createdBody != wantBody {
		t.Fatalf("pull request body = %q, want %q", runner.createdBody, wantBody)
	}
	if strings.Contains(runner.createdBody, qaReport) {
		t.Fatalf("pull request body exposed caller-provided QA text: %q", runner.createdBody)
	}
}

func TestGitHubPullRequestManagerRejectsBaseMovementBeforePush(t *testing.T) {
	repo, remote, metadata, record, action := acceptedPublicationTuple(t)
	advanceRemoteBase(t, repo, "base.txt", "new base\n")

	_, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{}).PublishAuthorized(
		t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if !errors.Is(err, ErrPublicationBaseChanged) {
		t.Fatalf("publication accepted a changed base revision: %v", err)
	}
	command := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", "refs/heads/cortexium/task")
	if err := command.Run(); err == nil {
		t.Fatal("publication pushed the implementation after the base identity changed")
	}
}

func TestGitHubPullRequestManagerRefreshesAuthorityAfterFinalFetchBeforePush(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	runner := &pullRequestTestRunner{publicationRemote: remote}
	_, err := NewPullRequestManager(runner, staticActionRefresher{err: errors.New("Project action changed")}).PublishAuthorized(
		t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if err == nil || !strings.Contains(err.Error(), "refresh Project authority") {
		t.Fatalf("stale publication authority was accepted: %v", err)
	}
	gitCalls := strings.Join(runner.gitCalls, "\n")
	if !strings.Contains(gitCalls, " fetch ") || strings.Contains(gitCalls, " push ") || len(runner.calls) != 0 {
		t.Fatalf("authority refresh did not occur between fetch and push: git=%#v gh=%#v", runner.gitCalls, runner.calls)
	}
}

func TestGitHubPullRequestManagerRejectsDifferentRepositoryRemote(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTupleWithSetup(t, func(repo, _ string) {
		runGitTest(t, repo, "config", "remote.origin.url", "https://github.com/other/repo.git")
	})
	_, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "accepted")
	if err == nil || !strings.Contains(err.Error(), "does not match Git remote") {
		t.Fatalf("publication accepted a different repository remote: %v", err)
	}
	command := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", "refs/heads/cortexium/wrong-remote")
	if err := command.Run(); err == nil {
		t.Fatal("publication pushed before rejecting the mismatched remote")
	}
}

func TestGitHubPullRequestManagerRejectsUnsafePersistedSelector(t *testing.T) {
	runner := &pullRequestTestRunner{}
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "--repo=attacker/repository"})
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).InspectAuthorized(t.Context(), action)
	if err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("unsafe pull request selector was accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("GitHub CLI was called before selector validation: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRejectsSelectorFromAnotherRepository(t *testing.T) {
	runner := &pullRequestTestRunner{}
	action := authorizedPullRequestTestAction(WorkItem{Repository: "owner/repo", PullRequest: "https://github.com/attacker/repo/pull/12"})
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).InspectAuthorized(t.Context(), action)
	if err == nil || !strings.Contains(err.Error(), "approved repository") {
		t.Fatalf("foreign pull request selector was accepted: %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("GitHub CLI was called before repository validation: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerReusesOpenPRAfterPartialPublication(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	runner := &pullRequestTestRunner{
		existingOpen:      true,
		publicationRemote: remote,
		viewHeadOID:       record.CommitOID,
		viewBaseOID:       record.ApprovedBaseOID,
	}
	if _, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted"); err != nil {
		t.Fatalf("initial push: %v", err)
	}
	runner.gitCalls = nil
	published, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if err != nil {
		t.Fatalf("reuse open pull request: %v", err)
	}
	if published.URL != "https://github.com/owner/repo/pull/12" || published.CommitSHA == "" {
		t.Fatalf("unexpected reused pull request %#v", published)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "pr create ") {
		t.Fatalf("Runner created a duplicate PR: %#v", runner.calls)
	}
	for _, call := range runner.gitCalls {
		for _, forbidden := range []string{" add ", " commit ", " commit-tree ", " merge ", " write-tree "} {
			if strings.Contains(" "+call+" ", forbidden) {
				t.Fatalf("partial publication recovery reconstructed a commit: %s", call)
			}
		}
	}
}

func TestGitHubPullRequestManagerRejectsAmbiguousOpenPullRequests(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	runner := &pullRequestTestRunner{ambiguousOpen: true, publicationRemote: remote}
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if err == nil || !strings.Contains(err.Error(), "2 open pull requests") {
		t.Fatalf("ambiguous pull requests were accepted: %v", err)
	}
	joined := strings.Join(runner.calls, "\n")
	if !strings.Contains(joined, "--limit 100") || strings.Contains(joined, "pr create ") {
		t.Fatalf("ambiguous lookup did not fail before creation: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRejectsReusedOpenPRWithUnexpectedAcceptedTuple(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	runner := &pullRequestTestRunner{
		existingOpen:      true,
		publicationRemote: remote,
		viewHeadOID:       strings.Repeat("d", 40),
		viewBaseOID:       record.ApprovedBaseOID,
	}
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if err == nil || !strings.Contains(err.Error(), "accepted publication tuple") || !strings.Contains(err.Error(), "head commit") {
		t.Fatalf("reused pull request with mismatched accepted tuple was accepted: %v", err)
	}
	if strings.Contains(strings.Join(runner.calls, "\n"), "pr create ") {
		t.Fatalf("mismatched reused PR reached creation path: %#v", runner.calls)
	}
}

func TestGitHubPullRequestManagerRejectsMismatchedPersistedPR(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	action.Item.PullRequest = "https://github.com/owner/repo/pull/12"
	action = authorizedPullRequestTestAction(action.Item)
	runner := &pullRequestTestRunner{
		viewHead:          "someone/else",
		publicationRemote: remote,
		viewHeadOID:       record.CommitOID,
		viewBaseOID:       record.ApprovedBaseOID,
	}
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), action, metadata, record, "main", "origin", config.MergeMethodMerge, "Accepted")
	if err == nil || !strings.Contains(err.Error(), "accepted publication tuple") || !strings.Contains(err.Error(), "head branch") {
		t.Fatalf("mismatched persisted pull request was accepted: %v", err)
	}
}

func TestGitHubPullRequestManagerRejectsChangedTupleBeforePushOrGitHubMutation(t *testing.T) {
	_, remote, metadata, record, action := acceptedPublicationTuple(t)
	for _, test := range []struct {
		name   string
		mutate func(*workspace.PublicationRecord, *AuthorizedAction)
	}{
		{name: "tree", mutate: func(record *workspace.PublicationRecord, _ *AuthorizedAction) {
			record.TreeOID = strings.Repeat("a", 40)
		}},
		{name: "repository", mutate: func(record *workspace.PublicationRecord, _ *AuthorizedAction) { record.Repository = "attacker/repo" }},
		{name: "destination", mutate: func(record *workspace.PublicationRecord, _ *AuthorizedAction) {
			record.DestinationRef = "refs/heads/attacker"
		}},
		{name: "authorized branch", mutate: func(_ *workspace.PublicationRecord, action *AuthorizedAction) { action.Item.Branch = "attacker" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changedRecord, changedAction := record, action
			test.mutate(&changedRecord, &changedAction)
			runner := &pullRequestTestRunner{publicationRemote: remote}
			_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(t.Context(), changedAction, metadata, changedRecord, "main", "origin", config.MergeMethodMerge, "Accepted")
			if err == nil {
				t.Fatal("changed publication tuple was accepted")
			}
			if len(runner.calls) != 0 || strings.Contains(strings.Join(runner.gitCalls, "\n"), " push ") {
				t.Fatalf("changed tuple reached push or GitHub: git=%#v gh=%#v", runner.gitCalls, runner.calls)
			}
		})
	}
}

func TestGitHubPullRequestManagerRefreshesBranchAndReportsConflicts(t *testing.T) {
	t.Run("clean update", func(t *testing.T) {
		repo, remote, metadata, _, action := acceptedPublicationTuple(t)
		runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
		advanceRemoteBase(t, repo, "base.txt", "base update\n")

		manager := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{})
		refresh, err := manager.RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", config.MergeMethodMerge)
		if err != nil || !refresh.Updated || refresh.Conflicted {
			t.Fatalf("refresh = %#v, error = %v", refresh, err)
		}
		assertRefreshedWorkspaceReusable(t, repo, metadata, action)
	})

	t.Run("conflict", func(t *testing.T) {
		repo, remote, metadata, _, action := acceptedPublicationTuple(t)
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("branch change\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		runGitTest(t, metadata.WorktreePath, "add", "--all")
		runGitTest(t, metadata.WorktreePath, "commit", "-m", "branch change")
		runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
		advanceRemoteBase(t, repo, "README.md", "base change\n")

		manager := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{})
		refresh, err := manager.RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", config.MergeMethodMerge)
		if err != nil || !refresh.Conflicted || refresh.Updated || len(refresh.ConflictFiles) != 1 || refresh.ConflictFiles[0] != "README.md" {
			t.Fatalf("refresh = %#v, error = %v", refresh, err)
		}
		assertRefreshedWorkspaceReusable(t, repo, metadata, action)
	})
}

func TestGitHubPullRequestManagerMergeAndSquashRefreshRetainMergeTopology(t *testing.T) {
	for _, mergeMethod := range []string{config.MergeMethodMerge, config.MergeMethodSquash} {
		t.Run(mergeMethod, func(t *testing.T) {
			repo, remote, metadata, _, action := acceptedPublicationTuple(t)
			runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
			advanceRemoteBase(t, repo, "base.txt", "base update\n")
			refresh, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{}).
				RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", mergeMethod)
			if err != nil || !refresh.Updated || refresh.Conflicted {
				t.Fatalf("refresh = %#v, error = %v", refresh, err)
			}
			parents := strings.Fields(runGitTest(t, metadata.WorktreePath, "rev-list", "--parents", "-n", "1", "HEAD"))
			if len(parents) != 3 {
				t.Fatalf("%s refresh parents = %v, want merge topology", mergeMethod, parents)
			}
		})
	}
}

func TestGitHubPullRequestManagerPublishesLinearRebaseRefreshWithExactLease(t *testing.T) {
	for _, conflicted := range []bool{false, true} {
		name := "clean"
		if conflicted {
			name = "conflict"
		}
		t.Run(name, func(t *testing.T) {
			remote, metadata, record, action, previousRemoteOID := acceptedRebaseRefreshTuple(t, conflicted)
			runner := &pullRequestTestRunner{
				publicationRemote: remote,
				viewHeadOID:       record.CommitOID,
				viewBaseOID:       record.ApprovedBaseOID,
			}
			published, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(
				t.Context(), action, metadata, record, "main", "origin", config.MergeMethodRebase, "Accepted")
			if err != nil {
				t.Fatalf("publish rebase refresh: %v", err)
			}
			if published.CommitSHA != record.CommitOID {
				t.Fatalf("published commit = %q, want %q", published.CommitSHA, record.CommitOID)
			}
			remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", record.DestinationRef))
			if remoteHead != record.CommitOID {
				t.Fatalf("remote head = %q, want accepted %q", remoteHead, record.CommitOID)
			}
			wantLease := "push --porcelain --no-verify --force-with-lease=" + record.DestinationRef + ":" + previousRemoteOID +
				" https://github.com/owner/repo.git " + record.CommitOID + ":" + record.DestinationRef
			if !strings.Contains(strings.Join(runner.gitCalls, "\n"), wantLease) {
				t.Fatalf("rebase publication did not use the exact expected-old-OID lease:\n%s", strings.Join(runner.gitCalls, "\n"))
			}
		})
	}
}

func TestGitHubPullRequestManagerRejectsExternallyChangedRebaseDestination(t *testing.T) {
	remote, metadata, record, action, previousRemoteOID := acceptedRebaseRefreshTuple(t, false)
	externalOID := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"))
	runGitTest(t, "", "--git-dir", remote, "update-ref", record.DestinationRef, externalOID, previousRemoteOID)
	runner := &pullRequestTestRunner{publicationRemote: remote}
	_, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(
		t.Context(), action, metadata, record, "main", "origin", config.MergeMethodRebase, "Accepted")
	if err == nil || !strings.Contains(err.Error(), "publication destination changed externally") {
		t.Fatalf("externally changed rebase destination was accepted: %v", err)
	}
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", record.DestinationRef))
	if remoteHead != externalOID {
		t.Fatalf("external destination was overwritten: got %s, want %s", remoteHead, externalOID)
	}
	if strings.Contains(strings.Join(runner.gitCalls, "\n"), " push ") {
		t.Fatalf("externally changed destination reached push: %#v", runner.gitCalls)
	}
}

func TestGitHubPullRequestManagerReusesAlreadyPublishedRebaseCandidate(t *testing.T) {
	remote, metadata, record, action, previousRemoteOID := acceptedRebaseRefreshTuple(t, false)
	runGitTest(t, metadata.WorktreePath, "push", "--force-with-lease="+record.DestinationRef+":"+previousRemoteOID,
		"origin", record.CommitOID+":"+record.DestinationRef)
	runner := &pullRequestTestRunner{
		publicationRemote: remote,
		viewHeadOID:       record.CommitOID,
		viewBaseOID:       record.ApprovedBaseOID,
	}
	published, err := NewPullRequestManager(runner, staticActionRefresher{}).PublishAuthorized(
		t.Context(), action, metadata, record, "main", "origin", config.MergeMethodRebase, "Accepted")
	if err != nil || published.CommitSHA != record.CommitOID {
		t.Fatalf("reuse already-published rebase candidate: published=%#v error=%v", published, err)
	}
	if strings.Contains(strings.Join(runner.gitCalls, "\n"), " push ") {
		t.Fatalf("already-published rebase candidate was pushed again: %#v", runner.gitCalls)
	}
}

func acceptedRebaseRefreshTuple(t *testing.T, conflicted bool) (string, workspace.Metadata, workspace.PublicationRecord, AuthorizedAction, string) {
	t.Helper()
	repo, remote, metadata, _, action := acceptedPublicationTuple(t)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	if conflicted {
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("task change\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.ConstructCandidate(t.Context(), metadata, "Change task file"); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
	previousRemoteOID := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	action.Item.PullRequest = "https://github.com/owner/repo/pull/12"
	action.Item.QACommit = previousRemoteOID
	action = authorizedPullRequestTestAction(action.Item)
	if conflicted {
		advanceRemoteBase(t, repo, "README.md", "base change\n")
	} else {
		advanceRemoteBase(t, repo, "base.txt", "base change\n")
	}
	refresh, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{}).
		RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", config.MergeMethodRebase)
	if err != nil {
		t.Fatalf("refresh rebase candidate: %v", err)
	}
	if refresh.Conflicted != conflicted || refresh.Updated == conflicted {
		t.Fatalf("rebase refresh = %#v, want conflicted=%t", refresh, conflicted)
	}
	content, err := action.DelegatedContent()
	if err != nil {
		t.Fatal(err)
	}
	metadata, err = provider.Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Dir(metadata.WorktreePath), WorkID: filepath.Base(metadata.WorktreePath),
		ItemID: action.Item.ID, DelegatedContentDigest: content.Digest, Repository: action.Item.Repository,
		BranchName: metadata.BranchName, BaseRef: metadata.BaseRef,
	})
	if err != nil {
		t.Fatalf("reopen refreshed workspace: %v", err)
	}
	if conflicted {
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "README.md"), []byte("resolved task and base\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	candidate, err := provider.ConstructCandidateForMergeMethod(t.Context(), metadata, "Accepted rebase refresh", config.MergeMethodRebase)
	if err != nil {
		t.Fatalf("construct rebase-compatible candidate: %v", err)
	}
	parents := strings.Fields(runGitTest(t, metadata.WorktreePath, "rev-list", "--parents", "-n", "1", candidate.CommitOID))
	if len(parents) != 2 || parents[1] != metadata.BaseRevision {
		t.Fatalf("rebase candidate parents = %v, want only base %s", parents, metadata.BaseRevision)
	}
	if got := runGitTest(t, metadata.WorktreePath, "show", candidate.CommitOID+":feature.txt"); got != "accepted implementation\n" {
		t.Fatalf("rebase candidate lost task content: %q", got)
	}
	if conflicted {
		if got := runGitTest(t, metadata.WorktreePath, "show", candidate.CommitOID+":README.md"); got != "resolved task and base\n" {
			t.Fatalf("rebase candidate lost conflict resolution: %q", got)
		}
	} else if got := runGitTest(t, metadata.WorktreePath, "show", candidate.CommitOID+":base.txt"); got != "base change\n" {
		t.Fatalf("rebase candidate lost base content: %q", got)
	}
	accepted, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	record, err := provider.RecordPublicationAcceptance(t.Context(), metadata, accepted)
	if err != nil {
		t.Fatal(err)
	}
	return remote, metadata, record, action, previousRemoteOID
}

func assertRefreshedWorkspaceReusable(t *testing.T, repo string, metadata workspace.Metadata, action AuthorizedAction) {
	t.Helper()
	content, err := action.DelegatedContent()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Dir(metadata.WorktreePath), WorkID: filepath.Base(metadata.WorktreePath),
		ItemID: action.Item.ID, DelegatedContentDigest: content.Digest, Repository: action.Item.Repository,
		BranchName: metadata.BranchName, BaseRef: metadata.BaseRef,
	})
	if err != nil {
		t.Fatalf("refreshed workspace is not reusable: %v", err)
	}
	if reopened.WorktreePath != metadata.WorktreePath {
		t.Fatalf("refreshed workspace moved from %q to %q", metadata.WorktreePath, reopened.WorktreePath)
	}
}

func TestGitHubPullRequestManagerKeepsBaseRefreshLocalUntilReplacementQA(t *testing.T) {
	repo, remote, metadata, _, action := acceptedPublicationTuple(t)
	runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	advanceRemoteBase(t, repo, "base.txt", "base update\n")
	hookMarker := filepath.Join(t.TempDir(), "merge-hook-ran")
	hooks := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pre-merge-commit", "post-merge"} {
		if err := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\ntouch "+hookMarker+"\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runGitTest(t, metadata.WorktreePath, "config", "core.hooksPath", hooks)
	runGitTest(t, metadata.WorktreePath, "config", "commit.gpgSign", "true")

	refresh, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{err: errors.New("must not be called")}).RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", config.MergeMethodMerge)
	if err != nil || !refresh.Updated {
		t.Fatalf("local branch refresh failed: refresh=%#v error=%v", refresh, err)
	}
	updatedRemoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	if updatedRemoteHead != remoteHead {
		t.Fatalf("unreviewed base refresh was pushed: before=%s after=%s", remoteHead, updatedRemoteHead)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository merge hook ran during sanitized local refresh: %v", err)
	}
}

func TestGitHubPullRequestManagerRejectsConfiguredBaseRefreshFilter(t *testing.T) {
	repo, remote, metadata, _, action := acceptedPublicationTuple(t)
	runGitTest(t, metadata.WorktreePath, "push", "-u", "origin", "cortexium/task")
	advanceRemoteBase(t, repo, "base.txt", "base update\n")
	runGitTest(t, metadata.WorktreePath, "config", "filter.attacker.smudge", "sh -c 'exit 0'")
	refresh, err := NewPullRequestManager(&pullRequestTestRunner{publicationRemote: remote}, staticActionRefresher{}).RefreshBranchAuthorized(t.Context(), action, metadata, "main", "origin", config.MergeMethodMerge)
	if err == nil || !strings.Contains(err.Error(), "refuses configured filters") || refresh.Updated || refresh.Conflicted {
		t.Fatalf("configured base-refresh filter was accepted: refresh=%#v error=%v", refresh, err)
	}
}

func acceptedPublicationTuple(t *testing.T) (string, string, workspace.Metadata, workspace.PublicationRecord, AuthorizedAction) {
	t.Helper()
	return acceptedPublicationTupleWithSetup(t, nil)
}

func acceptedPublicationTupleWithSetup(t *testing.T, setup func(repo, worktree string)) (string, string, workspace.Metadata, workspace.PublicationRecord, AuthorizedAction) {
	t.Helper()
	repo, remote := createPublicationRepository(t)
	action := authorizedPullRequestTestAction(WorkItem{
		ID: "item", Title: "Implement feature", Body: "Acceptance criteria", Repository: "owner/repo",
		URL: "https://github.com/owner/repo/issues/1", Branch: "cortexium/task",
	})
	content, err := action.DelegatedContent()
	if err != nil {
		t.Fatal(err)
	}
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "publication_test",
		ItemID: action.Item.ID, DelegatedContentDigest: content.Digest, Repository: action.Item.Repository,
		BranchName: action.Item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "feature.txt"), []byte("accepted implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), metadata, "Accepted QA candidate"); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(repo, metadata.WorktreePath)
	}
	accepted, err := workspace.CaptureSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	record, err := provider.RecordPublicationAcceptance(t.Context(), metadata, accepted)
	if err != nil {
		t.Fatal(err)
	}
	return repo, remote, metadata, record, action
}

func createPublicationRepository(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "origin.git")
	repo := filepath.Join(root, "repo")
	runGitTest(t, "", "init", "--bare", remote)
	runGitTest(t, "", "init", "-b", "main", repo)
	runGitTest(t, repo, "config", "user.name", "Test User")
	runGitTest(t, repo, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "initial")
	runGitTest(t, repo, "config", "url."+remote+".insteadOf", "https://github.com/owner/repo.git")
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	runGitTest(t, repo, "push", "-u", "origin", "main")
	runGitTest(t, "", "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/main")
	return repo, remote
}

func advanceRemoteBase(t *testing.T, repo, path, content string) {
	t.Helper()
	updater := filepath.Join(t.TempDir(), "updater")
	runGitTest(t, "", "clone", filepath.Join(filepath.Dir(repo), "origin.git"), updater)
	runGitTest(t, updater, "config", "user.name", "Test User")
	runGitTest(t, updater, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(updater, path), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, updater, "add", "--all")
	runGitTest(t, updater, "commit", "-m", "advance base")
	runGitTest(t, updater, "push", "origin", "main")
}

func runGitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
