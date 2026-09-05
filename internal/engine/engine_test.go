package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/metrics"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func runEngineTestWithInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, run func(context.Context, string, []string, string, time.Duration) (subprocess.Result, error)) (subprocess.Result, error) {
	if input == nil {
		return subprocess.Result{}, errors.New("test harness input is required")
	}
	prompt, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	// Legacy engine test doubles inspect the prompt as their final synthetic
	// argument. This adapter keeps production transport on stdin while letting
	// those behavior-focused doubles consume the same prompt without invoking a
	// real harness process.
	return run(ctx, command, append(append([]string(nil), args...), string(prompt)), dir, timeout)
}

func (r *parallelImplementationRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *trustedCycleOrderingRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r plannerNeedsInputRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r plannerStagesBatchRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r plannerProviderFailureRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *successfulImplementationRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r permissionDeniedImplementationRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *baseMovingReviewer) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r reviewerRejectRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	prompt, err := io.ReadAll(input)
	if err != nil {
		return subprocess.Result{}, err
	}
	if r.prompts != nil {
		*r.prompts = append(*r.prompts, string(prompt))
	}
	return r.Run(ctx, command, append(append([]string(nil), args...), string(prompt)), dir, timeout)
}

func (r *candidateInspectingReviewer) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *integrityMutatingReviewer) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *reviewerAutoMergeRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r mutatingReviewerAcceptRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r activeCheckoutMutatingReviewer) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func TestMain(m *testing.M) {
	stateDir, err := os.MkdirTemp("", "runner-engine-test-state-")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("CORTEXIUM_RUNNER_STATE_DIR", stateDir); err != nil {
		panic(err)
	}
	for _, runnerID := range []string{"runner", "runner_test"} {
		digest := sha256.Sum256([]byte(runnerID))
		path := filepath.Join(stateDir, "approval-authority", hex.EncodeToString(digest[:12])+".key")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, engineTestApprovalKey, 0o600); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(stateDir)
	os.Exit(code)
}

type closedPullRequestRunner struct{ project *fakeGitHubProjectRunner }

func (r closedPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"CLOSED","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"UNKNOWN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type mergedPullRequestRunner struct{ project *fakeGitHubProjectRunner }

func (r mergedPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"MERGED","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"UNKNOWN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type terminalMismatchedPullRequestRunner struct{ project *fakeGitHubProjectRunner }

func (r terminalMismatchedPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"MERGED","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"different-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"UNKNOWN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type terminalTreeEquivalentPullRequestRunner struct {
	project *fakeGitHubProjectRunner
	head    string
}

func (r terminalTreeEquivalentPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"MERGED","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"` + r.head + `","baseRefName":"main","baseRefOid":"","mergeStateStatus":"UNKNOWN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type openThenMergedPullRequestRunner struct {
	project *fakeGitHubProjectRunner
	views   int
}

func (r *openThenMergedPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		r.views++
		state := "OPEN"
		if r.views > 1 {
			state = "MERGED"
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"` + state + `","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"CLEAN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type terminalNoInspectRunner struct{ project *fakeGitHubProjectRunner }

func (r terminalNoInspectRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{}, errors.New("terminal pull request must not be inspected")
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type failingPullRequestInspectionRunner struct{ project *fakeGitHubProjectRunner }

func (r failingPullRequestInspectionRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stderr: "raw-token=secret", ExitCode: 1}, errors.New("exit status 1")
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type changedHeadPullRequestRunner struct{ project *fakeGitHubProjectRunner }

func (r changedHeadPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"new-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"CLEAN","comments":[],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type autoMergeReconciliationRunner struct {
	project        *fakeGitHubProjectRunner
	head           string
	baseRevision   string
	enabled        bool
	mergeRequests  int
	mergeCancels   int
	mergeCancelErr error
	itemListCalls  int
	onItemList     func(int)
	prViews        int
	onPRView       func(int)
}

type integrationPullRequest struct {
	url          string
	number       int
	branch       string
	head         string
	baseRevision string
	state        string
	mergeState   string
	checks       string
	enabled      bool
}

type serializedIntegrationRunner struct {
	project       *fakeGitHubProjectRunner
	pullRequests  map[string]*integrationPullRequest
	mergeRequests []string
	mergeCancels  []string
	checkViews    []string
}

func (r *serializedIntegrationRunner) pullRequest(selector string) *integrationPullRequest {
	for _, pullRequest := range r.pullRequests {
		if selector == pullRequest.url || selector == fmt.Sprint(pullRequest.number) {
			return pullRequest
		}
	}
	return nil
}

func (r *serializedIntegrationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "view" {
		pullRequest := r.pullRequest(args[2])
		if pullRequest == nil {
			return subprocess.Result{}, fmt.Errorf("unknown pull request %q", args[2])
		}
		if strings.Contains(strings.Join(args, " "), "statusCheckRollup") {
			r.checkViews = append(r.checkViews, pullRequest.url)
		}
		autoMerge := "null"
		if pullRequest.enabled {
			autoMerge = `{"enabledAt":"2026-09-02T00:00:00Z"}`
		}
		state := defaultString(pullRequest.state, "OPEN")
		mergeState := defaultString(pullRequest.mergeState, "CLEAN")
		checks := defaultString(pullRequest.checks, "[]")
		payload := fmt.Sprintf(`{"url":%q,"number":%d,"state":%q,"headRepository":{"nameWithOwner":"owner/repo"},"headRefName":%q,"headRefOid":%q,"baseRefName":"main","baseRefOid":%q,"mergeStateStatus":%q,"autoMergeRequest":%s,"comments":[],"reviews":[],"statusCheckRollup":%s}`,
			pullRequest.url, pullRequest.number, state, pullRequest.branch, pullRequest.head, pullRequest.baseRevision, mergeState, autoMerge, checks)
		return subprocess.Result{Stdout: payload}, nil
	}
	if command == "gh" && len(args) >= 3 && args[0] == "pr" && args[1] == "merge" {
		pullRequest := r.pullRequest(args[2])
		if pullRequest == nil {
			return subprocess.Result{}, fmt.Errorf("unknown pull request %q", args[2])
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--disable-auto") {
			pullRequest.enabled = false
			r.mergeCancels = append(r.mergeCancels, pullRequest.url)
			return subprocess.Result{}, nil
		}
		if strings.Contains(joined, "--auto") {
			pullRequest.enabled = true
			r.mergeRequests = append(r.mergeRequests, pullRequest.url)
			return subprocess.Result{}, nil
		}
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func (r *autoMergeReconciliationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		r.prViews++
		if r.onPRView != nil {
			r.onPRView(r.prViews)
		}
		autoMerge := "null"
		if r.enabled {
			autoMerge = `{"enabledAt":"2026-08-07T00:00:00Z"}`
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"` + r.head + `","baseRefName":"main","baseRefOid":"` + r.baseRevision + `","mergeStateStatus":"CLEAN","autoMergeRequest":` + autoMerge + `,"comments":[],"reviews":[]}`}, nil
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "merge" {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--disable-auto") {
			r.mergeCancels++
			if r.mergeCancelErr != nil {
				return subprocess.Result{Stderr: r.mergeCancelErr.Error(), ExitCode: 1}, r.mergeCancelErr
			}
			r.enabled = false
			return subprocess.Result{}, nil
		}
		if strings.Contains(joined, "--auto") {
			r.enabled = true
			r.mergeRequests++
			return subprocess.Result{}, nil
		}
	}
	if command == "gh" && (isLifecycleItemsCall(strings.Join(args, " ")) || isDirectProjectItemCall(strings.Join(args, " "))) {
		r.itemListCalls++
		if r.onItemList != nil {
			r.onItemList(r.itemListCalls)
		}
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

type openPullRequestRunner struct {
	project         *fakeGitHubProjectRunner
	feedback        string
	feedbackActor   string
	configuredActor string
}

type plannerNeedsInputRunner struct {
	project        *fakeGitHubProjectRunner
	capturedPrompt *string
}

type plannerStagesBatchRunner struct {
	project        *fakeGitHubProjectRunner
	forbiddenTitle string
	plannerCalls   *int
	outline        string
	details        string
}

type plannerProviderFailureRunner struct{ project *fakeGitHubProjectRunner }

type permissionDeniedImplementationRunner struct{ project *fakeGitHubProjectRunner }

type baseFetchFailureRunner struct{ project *fakeGitHubProjectRunner }

type successfulImplementationRunner struct {
	project *fakeGitHubProjectRunner
	inspect func(string) error
	dir     string
	args    []string
	calls   int
}

type trustedCycleOrderingRunner struct {
	implementation *successfulImplementationRunner
	events         []string
	prViews        int
	codexCalls     map[string]int
}

func (r *trustedCycleOrderingRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && projectUpdateSelects(args, "PVTI_interrupted", "O_ready") {
		r.events = append(r.events, "recover")
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		r.prViews++
		r.events = append(r.events, "reconcile")
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"MERGED","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"UNKNOWN","comments":[],"reviews":[]}`}, nil
	}
	if command == "codex" {
		r.codexCalls[dir]++
		r.events = append(r.events, "execute")
	}
	if command == "gh" && len(args) >= 2 && args[0] == "issue" && args[1] == "list" {
		r.events = append(r.events, "intake")
	}
	return r.implementation.Run(ctx, command, args, dir, timeout)
}

func (r *successfulImplementationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		r.calls++
		r.dir = dir
		r.args = append([]string(nil), args...)
		if r.inspect != nil {
			if err := r.inspect(dir); err != nil {
				return subprocess.Result{}, err
			}
		}
		encoded, err := json.Marshal(map[string]any{
			"outcome": execution.OutcomeSucceeded, "summary": "Implemented in a clean bound workspace.",
			"work_done": []string{"Completed the approved work."}, "verification": []string{"Verified the clean workspace."}, "blockers": []string{},
		})
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

func (r baseFetchFailureRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && len(args) > 0 && args[0] == "fetch" {
		return subprocess.Result{Stderr: "temporary network failure", ExitCode: 1}, errors.New("fetch failed")
	}
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func (r permissionDeniedImplementationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		return subprocess.Result{Stderr: "configured Codex sandbox denied the required command", ExitCode: 1}, errors.New("permission denied")
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

func (r plannerNeedsInputRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		if r.capturedPrompt != nil && len(args) > 0 {
			*r.capturedPrompt = args[len(args)-1]
		}
		outputPath := argumentValue(args, "--output-last-message")
		if outputPath == "" {
			return subprocess.Result{}, errors.New("planner did not request a result file")
		}
		plan, err := stagedPlannerFixtureResponse(args,
			`{"goal_summary":"Clarify scope","project_success_criteria":["The supported API contract is explicit."],"project_constraints":[],"open_decisions":["Which API version must remain compatible?"],"cards":[{"title":"Implement after clarification","dependencies":[]}]}`,
			`{"cards":{"C1":{"objective":"Implement the selected compatibility contract.","done_when":["Compatibility is defined."],"proof_obligations":["The selected compatibility behavior is demonstrated."],"assumptions":[]}}}`,
		)
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(outputPath, []byte(plan), 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

func (r plannerStagesBatchRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		if r.plannerCalls != nil {
			*r.plannerCalls = *r.plannerCalls + 1
		}
		if strings.TrimSpace(r.forbiddenTitle) != "" && strings.Contains(strings.Join(args, "\n"), r.forbiddenTitle) {
			return subprocess.Result{}, errors.New("mutable Project title reached planner harness input")
		}
		outputPath := argumentValue(args, "--output-last-message")
		if outputPath == "" {
			return subprocess.Result{}, errors.New("planner did not request a result file")
		}
		outline := r.outline
		if strings.TrimSpace(outline) == "" {
			outline = `{"goal_summary":"Deliver the slice","project_success_criteria":["The slice works."],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Implement the slice","dependencies":[]}]}`
		}
		details := r.details
		if strings.TrimSpace(details) == "" {
			details = `{"cards":{"C1":{"objective":"Build the requested slice.","done_when":["It works."],"proof_obligations":["The requested behavior is demonstrated."],"assumptions":[]}}}`
		}
		plan, err := stagedPlannerFixtureResponse(args, outline, details)
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(outputPath, []byte(plan), 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

func (r plannerProviderFailureRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		return subprocess.Result{
			Stdout:   `{"type":"turn.failed","error":{"message":"unexpected status 404 Not Found: Unknown error, url: https://chatgpt.com/backend-api/codex/responses, token=private"}}`,
			ExitCode: 1,
		}, errors.New("exit status 1")
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

type reviewerRejectRunner struct {
	project *fakeGitHubProjectRunner
	prompts *[]string
}

type reviewerAcceptRunner struct{ project *fakeGitHubProjectRunner }

type resumedAcceptanceRunner struct {
	project    *fakeGitHubProjectRunner
	reviewRuns int
}

func (r *resumedAcceptanceRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		r.reviewRuns++
		return subprocess.Result{}, errors.New("reviewer must not run for an unchanged accepted candidate")
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

type candidateInspectingReviewer struct {
	project    *fakeGitHubProjectRunner
	head, tree string
	status     string
}

func (r *candidateInspectingReviewer) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		target := profileReadRoot(args, dir)
		git := subprocess.OSRunner{}
		head, err := git.Run(ctx, "git", []string{"rev-parse", "HEAD"}, target, timeout)
		if err != nil {
			return head, err
		}
		tree, err := git.Run(ctx, "git", []string{"rev-parse", "HEAD^{tree}"}, target, timeout)
		if err != nil {
			return tree, err
		}
		status, err := git.Run(ctx, "git", []string{"status", "--porcelain", "--untracked-files=all"}, target, timeout)
		if err != nil {
			return status, err
		}
		r.head = strings.TrimSpace(head.Stdout)
		r.tree = strings.TrimSpace(tree.Stdout)
		r.status = status.Stdout
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

type integrityMutatingReviewer struct {
	project    *fakeGitHubProjectRunner
	mutate     func(string) error
	privileged []string
}

func (r *integrityMutatingReviewer) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" && r.mutate != nil {
		if err := r.mutate(profileReadRoot(args, dir)); err != nil {
			return subprocess.Result{}, err
		}
	}
	if command == "git" {
		joined := strings.Join(args, " ")
		removesRetainedWorktree := strings.Contains(joined, "worktree remove") && !strings.Contains(joined, filepath.Join("review-workspaces", "review-"))
		if containsArgument(args, "commit") || containsArgument(args, "push") || removesRetainedWorktree {
			r.privileged = append(r.privileged, "git "+joined)
		}
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
		r.privileged = append(r.privileged, "gh "+strings.Join(args, " "))
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func containsArgument(args []string, expected string) bool {
	for _, argument := range args {
		if argument == expected {
			return true
		}
	}
	return false
}

func runEngineTestGit(ctx context.Context, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	args = append([]string(nil), args...)
	remote := ""
	for candidate := filepath.Clean(dir); candidate != filepath.Dir(candidate); candidate = filepath.Dir(candidate) {
		path := filepath.Join(filepath.Dir(candidate), "origin.git")
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			remote = path
			break
		}
	}
	if remote != "" {
		for index := range args {
			if args[index] == "https://github.com/owner/repo.git" {
				args[index] = remote
			}
			if args[index] == "protocol.https.allow=always" {
				args[index] = "protocol.file.allow=always"
			}
		}
	}
	return (subprocess.OSRunner{}).Run(ctx, "git", args, dir, timeout)
}

type reviewForbiddenRunner struct {
	project     *fakeGitHubProjectRunner
	reviewCalls int
}

type baseMovingReviewer struct {
	project  *fakeGitHubProjectRunner
	moveBase func()
	reviews  int
}

type parallelImplementationRunner struct {
	project *fakeGitHubProjectRunner
	gitMu   sync.Mutex
	mu      sync.Mutex
	gate    chan struct{}
	once    sync.Once
	active  int
	maximum int
	dirs    map[string]int
}

func (r *parallelImplementationRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		r.gitMu.Lock()
		defer r.gitMu.Unlock()
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		r.mu.Lock()
		r.active++
		if r.active > r.maximum {
			r.maximum = r.active
		}
		r.dirs[dir]++
		if r.active == 2 {
			r.once.Do(func() { close(r.gate) })
		}
		r.mu.Unlock()
		defer func() {
			r.mu.Lock()
			r.active--
			r.mu.Unlock()
		}()

		select {
		case <-r.gate:
		case <-ctx.Done():
			return subprocess.Result{}, ctx.Err()
		}
		encoded, err := json.Marshal(map[string]any{
			"outcome": execution.OutcomeSucceeded, "summary": "Implemented the independent card.", "work_done": []string{"Completed the card."},
			"verification": []string{"Verified the card through its configured entrypoint."}, "blockers": []string{},
		})
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

type reviewerAutoMergeRunner struct {
	project                    *fakeGitHubProjectRunner
	baseRevision               string
	mergeRequests              int
	mergeErr                   error
	beforePostPublicationFetch func()
	postPublicationFetchSeen   bool
}

func runnerGitRevision(ctx context.Context, dir string, timeout time.Duration, revision string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	result, err := runEngineTestGit(ctx, []string{"rev-parse", revision}, dir, timeout)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

func (r *reviewerAutoMergeRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" && len(args) > 0 && args[0] == "fetch" && r.project.status == "PR Ready" && !r.postPublicationFetchSeen {
		r.postPublicationFetchSeen = true
		if r.beforePostPublicationFetch != nil {
			r.beforePostPublicationFetch()
		}
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "merge" {
		r.mergeRequests++
		if r.mergeErr != nil {
			return subprocess.Result{Stderr: "repository auto-merge is disabled", ExitCode: 1}, r.mergeErr
		}
		return subprocess.Result{}, nil
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		head := runnerGitRevision(ctx, dir, timeout, "HEAD")
		if head == "" && strings.TrimSpace(r.project.qaCommit) != "" {
			head = strings.TrimSpace(r.project.qaCommit)
		}
		if head == "" {
			head = "qa-head"
		}
		baseRevision := runnerGitRevision(ctx, dir, timeout, "origin/main")
		if baseRevision == "" {
			baseRevision = strings.TrimSpace(r.project.baseRevision)
		}
		if baseRevision == "" {
			baseRevision = r.baseRevision
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"` + head + `","baseRefName":"main","baseRefOid":"` + baseRevision + `","mergeStateStatus":"CLEAN","comments":[],"reviews":[]}`}, nil
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

type mutatingReviewerAcceptRunner struct{ project *fakeGitHubProjectRunner }

type activeCheckoutMutatingReviewer struct {
	project *fakeGitHubProjectRunner
	active  string
}

func (r activeCheckoutMutatingReviewer) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		if err := os.WriteFile(filepath.Join(r.active, "qa-side-effect.txt"), []byte("unreviewed\n"), 0o644); err != nil {
			return subprocess.Result{}, err
		}
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func (r mutatingReviewerAcceptRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		target := profileReadRoot(args, dir)
		if err := os.WriteFile(filepath.Join(target, "changed-during-qa.txt"), []byte("not reviewed\n"), 0o644); err != nil {
			return subprocess.Result{}, err
		}
		git := subprocess.OSRunner{}
		if result, err := git.Run(ctx, "git", []string{"add", "--all"}, target, timeout); err != nil {
			return result, err
		}
		if result, err := git.Run(ctx, "git", []string{"commit", "-m", "unreviewed QA mutation"}, target, timeout); err != nil {
			return result, err
		}
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func profileReadRoot(args []string, fallback string) string {
	if len(args) == 0 {
		return fallback
	}
	const marker = "Runner-approved read-only repository root: "
	for _, line := range strings.Split(args[len(args)-1], "\n") {
		if strings.HasPrefix(line, marker) {
			return strings.TrimSpace(strings.TrimPrefix(line, marker))
		}
	}
	return fallback
}

func (r *reviewForbiddenRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		r.reviewCalls++
		return subprocess.Result{}, errors.New("review must not run for an identity-mismatched workspace")
	}
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func (r *baseMovingReviewer) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	result, err := (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
	if command == "codex" && err == nil {
		r.reviews++
		r.moveBase()
	}
	return result, err
}

func (r reviewerAcceptRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		target := profileReadRoot(args, dir)
		r.project.qaCommit = runnerGitRevision(ctx, target, timeout, "HEAD")
		r.project.baseRevision = runnerGitRevision(ctx, target, timeout, "origin/main")
		encoded, encodeErr := reviewerContentForSchema(args, false)
		if encodeErr != nil {
			return subprocess.Result{}, encodeErr
		}
		if err := os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	case "gh":
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "pr list "):
			return subprocess.Result{Stdout: `[]`}, nil
		case strings.HasPrefix(joined, "pr create "):
			return subprocess.Result{Stdout: "https://github.com/owner/repo/pull/12\n"}, nil
		case strings.HasPrefix(joined, "pr view "):
			head := runnerGitRevision(ctx, dir, timeout, "HEAD")
			if head == "" {
				head = strings.TrimSpace(r.project.qaCommit)
			}
			if head == "" {
				head = "qa-head"
			}
			baseRevision := runnerGitRevision(ctx, dir, timeout, "origin/main")
			if baseRevision == "" {
				baseRevision = strings.TrimSpace(r.project.baseRevision)
			}
			return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"` + head + `","baseRefName":"main","baseRefOid":"` + baseRevision + `","mergeStateStatus":"CLEAN","comments":[],"reviews":[]}`}, nil
		}
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func (r reviewerRejectRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	switch command {
	case "git":
		return runEngineTestGit(ctx, args, dir, timeout)
	case "codex":
		outputPath := argumentValue(args, "--output-last-message")
		encoded, encodeErr := reviewerContentForSchema(args, true)
		if encodeErr != nil {
			return subprocess.Result{}, encodeErr
		}
		if err := os.WriteFile(outputPath, encoded, 0o600); err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, nil
	default:
		return r.project.Run(ctx, command, args, dir, timeout)
	}
}

func reviewerContentForSchema(args []string, reject bool) ([]byte, error) {
	schema, err := os.ReadFile(argumentValue(args, "--output-schema"))
	if err != nil {
		return nil, fmt.Errorf("read reviewer schema: %w", err)
	}
	var decoded struct {
		Properties struct {
			Criteria struct {
				Required []string `json:"required"`
			} `json:"criteria"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return nil, fmt.Errorf("decode reviewer schema: %w", err)
	}
	criteria := make(map[string]any, len(decoded.Properties.Criteria.Required))
	for _, key := range decoded.Properties.Criteria.Required {
		criteria[key] = map[string]any{
			"status": "passed", "summary": "The approved check passed.", "evidence": []string{"focused verification"},
		}
	}
	summary := "Agent QA accepted the implementation."
	if reject && len(criteria) > 0 {
		criteria[decoded.Properties.Criteria.Required[0]] = map[string]any{
			"status": "failed", "summary": "A required edge case is missing.", "evidence": []string{"feature_test.go lacks the edge case"},
		}
		summary = "Add the missing edge-case test."
	}
	encoded, err := json.Marshal(map[string]any{
		"criteria":         criteria,
		"repository_rules": map[string]any{"status": "passed", "summary": "Repository instructions passed.", "evidence": []string{"focused diff"}},
		"maintainability":  map[string]any{"status": "passed", "summary": "Maintainability is acceptable.", "evidence": []string{"focused diff"}},
		"summary":          summary,
	})
	if err != nil {
		return nil, fmt.Errorf("encode reviewer content: %w", err)
	}
	return encoded, nil
}

func stagedPlannerFixtureResponse(args []string, outline, details string) (string, error) {
	schema, err := os.ReadFile(argumentValue(args, "--output-schema"))
	if err != nil {
		return "", fmt.Errorf("read planner schema: %w", err)
	}
	var decoded struct {
		Properties struct {
			Cards struct {
				Type string `json:"type"`
			} `json:"cards"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		return "", fmt.Errorf("decode planner schema: %w", err)
	}
	switch decoded.Properties.Cards.Type {
	case "array":
		return outline, nil
	case "object":
		return details, nil
	default:
		return "", fmt.Errorf("unknown planner stage schema: cards type %q", decoded.Properties.Cards.Type)
	}
}

func (r openPullRequestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "gh" && strings.Join(args, " ") == "api user --jq .login" {
		actor := r.configuredActor
		if actor == "" {
			actor = "maintainer"
		}
		return subprocess.Result{Stdout: actor + "\n"}, nil
	}
	if command == "git" {
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if command == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
		body := strings.ReplaceAll(r.feedback, `"`, `\"`)
		actor := r.feedbackActor
		if strings.TrimSpace(actor) == "" {
			actor = "maintainer"
		}
		return subprocess.Result{Stdout: `{"url":"https://github.com/owner/repo/pull/12","number":12,"state":"OPEN","headRepository":{"nameWithOwner":"owner/repo"},"headRefName":"cortexium/task","headRefOid":"qa-head","baseRefName":"main","baseRefOid":"","mergeStateStatus":"CLEAN","comments":[{"author":{"login":"` + actor + `"},"body":"` + body + `"}],"reviews":[]}`}, nil
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func TestExecutionConfigCombinesHarnessBoundaryWithExplicitRoleSettings(t *testing.T) {
	configuredModel := "configured-model"
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	implementer := roles[config.WorkRoleImplementer]
	implementer.Model = &configuredModel
	implementer.Reasoning = "xhigh"
	implementer.TimeoutSeconds = 90
	roles[config.WorkRoleImplementer] = implementer
	service := Engine{cfg: config.RuntimeConfig{
		Harnesses: []config.HarnessConfig{{
			Kind: config.HarnessCodexCLI, Command: "configured-codex", WorkspaceWriteRoot: "/worktrees",
		}},
		Roles: roles,
	}}

	cfg := service.executionConfig(config.WorkRoleImplementer, config.HarnessCodexCLI, "/project")
	codex := cfg.Harness
	if codex.Model == nil || *codex.Model != configuredModel {
		t.Fatalf("execution did not preserve the configured model: %#v", codex.Model)
	}
	if codex.Command != "configured-codex" || codex.WorkingDir != "/project" || codex.WorkspaceWriteRoot != "/worktrees" || codex.TimeoutSeconds != 90 || codex.ReasoningEffort != "xhigh" {
		t.Fatalf("execution lost safe harness settings: %#v", codex)
	}
}

func TestMaxParallelismUsesExplicitConfiguration(t *testing.T) {
	if got := (&Engine{cfg: config.RuntimeConfig{MaxParallelism: 4}}).maxParallelism(); got != 4 {
		t.Fatalf("configured max parallelism = %d, want 4", got)
	}
}

func TestRunCycleExecutesTwoIndependentCardsConcurrentlyExactlyOnce(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	first := github.WorkItem{ID: "PVTI_parallel_one", Title: "Build the first independent slice", Body: "Criteria one", Repository: "owner/repo", Status: "Ready"}
	second := github.WorkItem{ID: "PVTI_parallel_two", Title: "Build the second independent slice", Body: "Criteria two", Repository: "owner/repo", Status: "Ready"}
	first.Approval = testApproval(first)
	second.Approval = testApproval(second)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(first) + `,` + projectItemJSON(second) + `]}`}
	runner := &parallelImplementationRunner{project: project, gate: make(chan struct{}), dirs: map[string]int{}}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo, MaxParallelism: 2,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	})
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	results, err := service.RunCycle(ctx)
	if err != nil {
		t.Fatalf("run parallel cycle: %v", err)
	}
	if len(results) != 2 || runner.maximum != 2 || len(runner.dirs) != 2 {
		t.Fatalf("independent cards did not execute concurrently in separate worktrees: results=%#v maximum=%d dirs=%#v", results, runner.maximum, runner.dirs)
	}
	for dir, calls := range runner.dirs {
		if calls != 1 {
			t.Fatalf("worktree %q executed %d times, want exactly once", dir, calls)
		}
	}
	for _, result := range results {
		if result.Outcome != execution.OutcomeSucceeded || result.WorktreePath == "" || result.Branch == "" {
			t.Fatalf("parallel result lacked successful workspace evidence: %#v", result)
		}
	}
}

func TestPreparePollSelectsSafeWorkPastWorkspaceConflict(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	first := github.WorkItem{
		ID: "PVTI_resource_first", Title: "Use the shared branch first", Body: "First criteria", Repository: "owner/repo", Status: "Ready", Branch: "runner/shared",
	}
	conflicting := github.WorkItem{
		ID: "PVTI_resource_conflict", Title: "Wait for the shared branch", Body: "Conflicting criteria", Repository: "owner/repo", Status: "Ready", Branch: "runner/shared",
	}
	independent := github.WorkItem{
		ID: "PVTI_resource_independent", Title: "Use an independent branch", Body: "Independent criteria", Repository: "owner/repo", Status: "Ready", Branch: "runner/independent",
	}
	first.Approval = testApproval(first)
	conflicting.Approval = testApproval(conflicting)
	independent.Approval = testApproval(independent)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(first) + `,` + projectItemJSON(conflicting) + `,` + projectItemJSON(independent) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, MaxParallelism: 2,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	prepared, err := service.preparePoll(t.Context(), 2, false, nil)
	if err != nil {
		t.Fatalf("prepare resource-aware poll: %v", err)
	}
	if len(prepared.claimed) != 2 {
		t.Fatalf("claimed actions = %#v, want two safe actions", prepared.claimed)
	}
	claimedIDs := []string{prepared.claimed[0].action.Item.ID, prepared.claimed[1].action.Item.ID}
	if !reflect.DeepEqual(claimedIDs, []string{first.ID, independent.ID}) {
		t.Fatalf("resource-aware selection = %#v, want first and independent", claimedIDs)
	}
	if !resourcesAvailable(prepared.claimed[1].resources, occupiedResourceKeys(map[string][]string{first.ID: prepared.claimed[0].resources})) {
		t.Fatalf("selected independent action conflicts with first: %#v", prepared.claimed)
	}
}

func TestRunCycleCompletesRecoveryReconciliationAndApprovedWorkBeforeFailedPublicIntake(t *testing.T) {
	oversized := make([]map[string]string, github.MaxAssessmentIssues+1)
	for index := range oversized {
		oversized[index] = map[string]string{"url": ""}
	}
	oversizedJSON, err := json.Marshal(oversized)
	if err != nil {
		t.Fatal(err)
	}
	for name, issuesJSON := range map[string]string{"malformed": `{malformed`, "oversized": string(oversizedJSON)} {
		t.Run(name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			approved := github.WorkItem{ID: "PVTI_approved", Title: "Complete trusted work", Body: "Criteria", Repository: "owner/repo", Status: "Ready"}
			approved.Approval = testApproval(approved)
			interrupted := github.WorkItem{ID: "PVTI_interrupted", Title: "Resume trusted work", Body: "Criteria", Repository: "owner/repo", Status: "In Progress", Phase: "ready"}
			interrupted.Approval = testApproval(interrupted)
			pullRequest := github.WorkItem{ID: "PVTI_pr", Title: "Reconcile trusted pull request", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready", PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head"}
			pullRequest.Approval = testApproval(pullRequest)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(approved) + `,` + projectItemJSON(interrupted) + `,` + projectItemJSON(pullRequest) + `]}`, issuesJSON: issuesJSON}
			implementation := &successfulImplementationRunner{project: project}
			runner := &trustedCycleOrderingRunner{implementation: implementation, codexCalls: map[string]int{}}
			cfg := completeEngineTestConfig(config.Config{
				ProjectDir: repo, MaxParallelism: 1,
				GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			})
			service, newErr := New(cfg, runner)
			if newErr != nil {
				t.Fatal(newErr)
			}
			results, cycleErr := service.RunCycle(t.Context())
			if cycleErr == nil || !strings.Contains(cycleErr.Error(), "after admitted work") || len(cycleErr.Error()) > 512 {
				t.Fatalf("failed intake was not reported as a bounded local error after trusted work: %v", cycleErr)
			}
			succeeded := map[string]int{}
			for _, result := range results {
				if result.Outcome == execution.OutcomeSucceeded {
					succeeded[result.Item.ID]++
				}
			}
			statuses := map[string]string{}
			for _, remote := range project.remoteItems {
				statuses[remote.ID] = remote.Status
			}
			if succeeded[approved.ID] != 1 || len(runner.codexCalls) != 1 || runner.prViews != 1 || statuses[interrupted.ID] != "Ready" || statuses[pullRequest.ID] != "Done" {
				t.Fatalf("trusted paths did not complete exactly once: successes=%#v codex=%#v pr_views=%d statuses=%#v results=%#v", succeeded, runner.codexCalls, runner.prViews, statuses, results)
			}
			intakeIndex := -1
			for index, event := range runner.events {
				if event == "intake" {
					intakeIndex = index
				}
				if event == "execute" && intakeIndex >= 0 {
					t.Fatalf("approved execution ran after intake: events=%#v", runner.events)
				}
			}
			if intakeIndex < 0 || len(runner.events) < 4 || runner.events[0] != "recover" || runner.events[1] != "reconcile" {
				t.Fatalf("trusted reconciliation/intake ordering was not deterministic: events=%#v", runner.events)
			}
		})
	}
}

func TestRunCycleDoesNotClaimCardsWhenAdmissionBudgetIsExhausted(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_budget", Title: "Wait for budget", Body: "Criteria", Repository: "owner/repo", Status: "Ready"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &parallelImplementationRunner{project: project, gate: make(chan struct{}), dirs: map[string]int{}}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo, MaxParallelism: 2, AdmissionBudget: &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 1},
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	})
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
		return metrics.ReadResult{Attempts: []metrics.Attempt{{Event: metrics.Event{AttemptID: "used", StartedAt: time.Now().UTC().Add(-time.Second)}}}}, nil
	})
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run budget-paused cycle: %v", err)
	}
	decision := service.LastAdmissionDecision()
	if len(results) != 0 || decision.Allowed || runner.maximum != 0 || len(runner.dirs) != 0 {
		t.Fatalf("budget-paused cycle claimed work: results=%#v decision=%#v runner=%#v", results, decision, runner)
	}
}

func TestRoleSkillsUseConfiguredSelection(t *testing.T) {
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	planner := roles[config.WorkRolePlanner]
	planner.Skills = []string{"custom-planner"}
	roles[config.WorkRolePlanner] = planner
	service := Engine{cfg: config.RuntimeConfig{
		Roles: roles,
		RoleContracts: map[string]string{
			config.WorkRolePlanner: config.WorkRolePlanner, config.WorkRoleImplementer: config.WorkRoleImplementer, config.WorkRoleReviewer: config.WorkRoleReviewer,
		},
	}}
	if got := strings.Join(service.roleSkills(config.WorkRolePlanner), ","); got != "custom-planner" {
		t.Fatalf("configured planner skills = %q, want custom-planner", got)
	}
	if got := strings.Join(service.roleSkills(config.WorkRoleReviewer), ","); got != "runner-reviewer" {
		t.Fatalf("default reviewer skills = %q, want runner-reviewer", got)
	}
}

func TestAssignmentSeparatesDynamicContextFromHarnessAndRoleWorkflow(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_1", Title: "UNIQUE_TASK_TITLE", Body: "UNIQUE_APPROVED_BODY", URL: "https://github.com/owner/repo/issues/unique-context",
		Repository: "owner/repo", Role: config.WorkRoleImplementer, Approval: "authenticated-test-assertion",
		Result: "UNIQUE_PREVIOUS_CONTEXT",
	}
	service := &Engine{cfg: config.RuntimeConfig{
		Roles: config.RoleTemplate(config.HarnessCodexCLI),
		RoleContracts: map[string]string{
			config.WorkRoleImplementer: config.WorkRoleImplementer,
		},
	}}
	content := github.DelegatedContentFor(item)
	assignment := service.assignment(item, content, nil, []string{"@dan: KEEP_THIS_HUMAN_COMMENT"})
	instructions := assignment.Spec.Task.Instructions
	for _, dynamic := range []string{"runner-implementer", "UNIQUE_PREVIOUS_CONTEXT", "KEEP_THIS_HUMAN_COMMENT"} {
		if strings.Count(instructions, dynamic) != 1 {
			t.Fatalf("assignment must include dynamic context %q exactly once:\n%s", dynamic, instructions)
		}
	}
	for _, boundary := range []string{"--- BEGIN HUMAN COMMENTS ---", "--- END HUMAN COMMENTS ---"} {
		if strings.Count(instructions, boundary) != 1 {
			t.Fatalf("assignment omitted human-comment boundary %q:\n%s", boundary, instructions)
		}
	}
	if assignment.Spec.ApprovedBodySnapshot != "UNIQUE_APPROVED_BODY" || strings.Contains(instructions, "UNIQUE_APPROVED_BODY") {
		t.Fatalf("approved body snapshot was not separated from mutable instructions: %#v", assignment.Spec)
	}
	for _, wrapperOwned := range []string{item.Title, item.URL, "native harness configuration", "return a blocked outcome"} {
		if strings.Contains(instructions, wrapperOwned) {
			t.Fatalf("assignment repeated wrapper-owned context %q:\n%s", wrapperOwned, instructions)
		}
	}
	if assignment.Spec.Task.Title != "GitHub Project item "+item.ID || strings.Contains(assignment.Spec.Task.Title, item.Title) || len(assignment.Spec.ContextRefs) != 0 {
		t.Fatalf("assignment used mutable title or emitted a URL as authoritative context: %#v", assignment.Spec)
	}
	if assignment.Spec.ApprovedBodySnapshot != item.Body || assignment.Spec.DelegatedContentDigest != content.Digest {
		t.Fatalf("assignment lost its approval-bound content: %#v", assignment.Spec)
	}
	if !reflect.DeepEqual(assignment.Spec.RequiredVerification, []string{"Approved acceptance criteria and requested behavior"}) {
		t.Fatalf("assignment fallback verification = %#v", assignment.Spec.RequiredVerification)
	}
}

func TestAssignmentCarriesApprovedVerificationContractExactly(t *testing.T) {
	body := "Summary\n\n## Acceptance criteria\n- [ ] Works\n\n## Proof obligations\n- The focused behavior is demonstrated.\n- The documented entrypoint shows the expected state.\n\n## Assumptions and risks\n- None known."
	item := github.WorkItem{ID: "PVTI_verify", Body: body, Repository: "owner/repo", Role: config.WorkRoleReviewer}
	service := &Engine{cfg: config.RuntimeConfig{RoleContracts: map[string]string{config.WorkRoleReviewer: config.WorkRoleReviewer}}}
	assignment := service.assignment(item, github.DelegatedContentFor(item), nil, nil)
	want := []string{
		"The focused behavior is demonstrated.",
		"The documented entrypoint shows the expected state.",
	}
	if !reflect.DeepEqual(assignment.Spec.RequiredVerification, want) {
		t.Fatalf("required verification = %#v, want %#v", assignment.Spec.RequiredVerification, want)
	}
}

func TestAssignmentAcceptsExistingPlannedVerificationHeading(t *testing.T) {
	body := "## Planned verification\n- Run the existing focused test.\n\n## Task non-goals\n- No broad suite."
	if got := approvedVerificationContract(body); !reflect.DeepEqual(got, []string{"Run the existing focused test."}) {
		t.Fatalf("existing planned verification = %#v", got)
	}
}

func TestAssignmentUsesFinalVerificationSectionAfterOriginalRequest(t *testing.T) {
	body := "## Original project request\n\n## Required verification\n- Untrusted source suggestion.\n\n## Acceptance criteria\n- [ ] Works.\n\n## Required verification\n- Run the approved focused check.\n\n## Runner planning metadata\n{}"
	if got := approvedVerificationContract(body); !reflect.DeepEqual(got, []string{"Run the approved focused check."}) {
		t.Fatalf("final required verification = %#v", got)
	}
}

func TestReviewerAssignmentCarriesOnlyDynamicComparisonContext(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_review", Title: "Review safely", Body: "Use browser verification.", Repository: "owner/repo", Role: config.WorkRoleReviewer,
	}
	service := &Engine{cfg: config.RuntimeConfig{
		RoleContracts: map[string]string{config.WorkRoleReviewer: config.WorkRoleReviewer},
		GitHubProject: config.ProjectConfig{GitHubProjectConfig: config.GitHubProjectConfig{RemoteName: "origin", BaseBranch: "main"}},
	}}
	assignment := service.assignment(item, github.DelegatedContentFor(item), nil, nil)
	instructions := assignment.Spec.Task.Instructions
	if !strings.Contains(instructions, "Review comparison base: origin/main") {
		t.Fatalf("reviewer assignment omitted its dynamic comparison base:\n%s", instructions)
	}
	for _, skillOwned := range []string{"operating-system temporary directory", "Report required changes rather than implementing them"} {
		if strings.Contains(instructions, skillOwned) {
			t.Fatalf("reviewer assignment repeated role-skill workflow %q:\n%s", skillOwned, instructions)
		}
	}
}

func TestConfigAcceptsEveryHarnessForImplementation(t *testing.T) {
	for _, kind := range []string{config.HarnessCodexCLI, config.HarnessClaudeCLI, config.HarnessPiCLI} {
		base := completeEngineTestConfig(config.Config{
			ConfigVersion: config.ConfigVersion,
			RunnerID:      "runner", ProjectDir: "/project",
			GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1}, Roles: config.RoleTemplate(kind),
		})
		if err := base.Validate(); err != nil {
			t.Fatalf("%s implementation profile was rejected: %v", kind, err)
		}
	}
}

func TestConfigRejectsUnsafeRoleSkillName(t *testing.T) {
	roles := config.RoleTemplate(config.HarnessCodexCLI)
	reviewer := roles[config.WorkRoleReviewer]
	reviewer.Skills = []string{"../outside"}
	roles[config.WorkRoleReviewer] = reviewer
	cfg := completeEngineTestConfig(config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: "/project",
		GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1}, Roles: roles,
	})
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected unsafe role skill name to be rejected")
	}
}

func TestConfigRequiresDistinctProjectStatuses(t *testing.T) {
	workflow := config.WorkflowTemplate(true)
	lane := workflow.Lanes["needs_assessment"]
	lane.Name = "Ready"
	workflow.Lanes["needs_assessment"] = lane
	cfg := completeEngineTestConfig(config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: "/project",
		GitHubProject: &config.GitHubProjectConfig{Owner: "example", Number: 1},
		Workflow:      &workflow,
	})
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected assessment and default ready statuses to be distinct")
	}
}

func TestRetryPhaseRoutesPublicationBlockersBackThroughAgentQA(t *testing.T) {
	service, err := New(completeEngineTestConfig(config.Config{}), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	if phase := service.retryPhase("pr_ready", "blocked"); phase != "agent_qa" {
		t.Fatalf("publication retry phase = %q, want agent_qa", phase)
	}
	if phase := service.retryPhase("ready", "blocked"); phase != "ready" {
		t.Fatalf("implementation retry phase = %q, want ready", phase)
	}
}

func TestPlannerOpenDecisionsMoveWorkToNeedsInputWithoutCreatingCards(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	item := github.WorkItem{
		ID: "PVTI_plan", Title: "Plan API compatibility", Body: "Decide and split the work.",
		URL: "https://github.com/owner/repo/issues/1", Repository: "owner/repo", Status: "Plan",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{
		itemsJSON:     `{"items":[` + projectItemJSON(item) + `]}`,
		issueComments: []github.ItemComment{{Author: "dan", Body: "The intended contract is API v2."}},
	}
	var plannerPrompt string
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), plannerNeedsInputRunner{project: project, capturedPrompt: &plannerPrompt})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run planner cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeNeedsInput || project.status != "Blocked" {
		t.Fatalf("open planning decision did not use needs_input: results=%#v status=%q", results, project.status)
	}
	if project.createdBody != "" || !strings.Contains(results[0].Summary, "Which API version") || strings.Contains(project.result, "Which API version") || !strings.Contains(project.result, "posted its planning questions") || len(project.postedComments) != 1 || !strings.Contains(project.postedComments[0], "Which API version") || !strings.Contains(plannerPrompt, "The intended contract is API v2") {
		t.Fatalf("planner created work despite open decisions or lost context: created=%q result=%q", project.createdBody, project.result)
	}
}

func TestPlannerCycleStagesBatchAndWaitsForExplicitOperatorApproval(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	approved := github.WorkItem{ID: "PVTI_plan", Title: "Approved presentation title", Body: "Split this request.", Repository: "owner/repo", Status: "Plan"}
	item := approved
	item.Title = "UNAPPROVED_MUTABLE_PLANNER_TITLE"
	item.Approval = testApproval(approved)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), plannerStagesBatchRunner{project: project, forbiddenTitle: item.Title})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded {
		t.Fatalf("run planner cycle: results=%#v error=%v", results, err)
	}
	project.loadRemoteItems()
	var source, child github.WorkItem
	for _, current := range project.remoteItems {
		if current.ID == item.ID {
			source = current
		} else if github.DecodePlannedItemMetadata(current.Body).PlanningSourceID == item.ID {
			child = current
		}
	}
	if source.Status != "Needs assessment" || source.Phase != github.PlanningApprovalPhase || !strings.HasPrefix(source.Approval, "batch-v1:") || child.Status != "Needs assessment" || child.Approval != "" {
		t.Fatalf("planner did not stop at the operator boundary: source=%#v child=%#v", source, child)
	}
	if len(source.Approval) > 1000 {
		t.Fatalf("authenticated staging marker exceeded the supported Project text field size: %d", len(source.Approval))
	}
	if !strings.Contains(results[0].Summary, "approve --item PVTI_plan --dry-run") {
		t.Fatalf("planner result omitted operator approval guidance: %#v", results[0])
	}
}

func TestPlannerRetryResumesExactCheckpointAfterPartialChildCreation(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	item := github.WorkItem{ID: "PVTI_plan_resume", Title: "Plan two slices", Body: "Split this request safely.", Repository: "owner/repo", Status: "Plan"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, failCreateAt: 2}
	plannerCalls := 0
	runner := plannerStagesBatchRunner{
		project: project, plannerCalls: &plannerCalls,
		outline: `{"goal_summary":"Deliver both slices","project_success_criteria":["Both slices work."],"project_constraints":[],"open_decisions":[],"cards":[{"title":"Implement first slice","dependencies":[]},{"title":"Implement second slice","dependencies":[1]}]}`,
		details: `{"cards":{"C1":{"objective":"Build the first slice.","done_when":["The first slice works."],"proof_obligations":["The first behavior is demonstrated."],"assumptions":[]},"C2":{"objective":"Build the second slice.","done_when":["The second slice works."],"proof_obligations":["The second behavior is demonstrated."],"assumptions":[]}}}`,
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}

	first, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Outcome == execution.OutcomeSucceeded || plannerCalls != 2 || project.createCount != 2 {
		t.Fatalf("first interrupted planner attempt was not reproduced: results=%#v planner_calls=%d creates=%d", first, plannerCalls, project.createCount)
	}
	checkpointPath := service.plannerCheckpointPath(item.ID)
	if info, err := os.Stat(checkpointPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("completed plan was not retained privately: info=%v error=%v", info, err)
	}

	retry, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("plan interrupted planner retry: %v", err)
	}
	if _, err := service.ApplyProjectItemRetry(t.Context(), retry); err != nil {
		t.Fatalf("apply interrupted planner retry: %v", err)
	}
	project.failCreateAt = 0
	second, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || second[0].Outcome != execution.OutcomeSucceeded || !second[0].ResumedCheckpoint || plannerCalls != 2 || project.createCount != 3 {
		t.Fatalf("retry did not resume the saved plan exactly: results=%#v planner_calls=%d creates=%d", second, plannerCalls, project.createCount)
	}
	project.loadRemoteItems()
	children := make([]github.WorkItem, 0, 2)
	for _, current := range project.remoteItems {
		if github.DecodePlannedItemMetadata(current.Body).PlanningSourceID == item.ID {
			children = append(children, current)
		}
	}
	if len(children) != 2 {
		t.Fatalf("resumed batch contains %d children, want 2: %#v", len(children), children)
	}
	firstMetadata := github.DecodePlannedItemMetadata(children[0].Body)
	secondMetadata := github.DecodePlannedItemMetadata(children[1].Body)
	if firstMetadata.PlanningBatchFingerprint == "" || firstMetadata.PlanningBatchFingerprint != secondMetadata.PlanningBatchFingerprint {
		t.Fatalf("resumed children do not share the exact saved batch: %#v", children)
	}
	if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
		t.Fatalf("completed planner checkpoint was not cleared: %v", err)
	}
}

func TestPlannerProviderFailureSchedulesAutomaticRetryInPlanLane(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_plan_provider_retry", Title: "Plan a feature", Body: "Split the feature into safe work.",
		Repository: "owner/repo", Status: "Plan", Role: config.WorkRolePlanner,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), plannerProviderFailureRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	project.loadRemoteItems()
	var current github.WorkItem
	for _, candidate := range project.remoteItems {
		if candidate.ID == item.ID {
			current = candidate
			break
		}
	}
	if len(results) != 1 || results[0].Outcome != "retry_scheduled" || results[0].RetryDisposition != string(execution.RetryAutomatic) ||
		current.Status != "Plan" || current.Activity != config.RunnerActivityWaitingForHarness || strings.Contains(current.Result, "token=private") {
		t.Fatalf("planner provider failure was not safely retained for retry: results=%#v item=%#v", results, current)
	}
}

func TestPlannerSourceStagingFailureIsReportedAndRemainsOperatorRecoverable(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	item := github.WorkItem{ID: "PVTI_plan", Title: "Plan the slice", Body: "Split this request.", Repository: "owner/repo", Status: "Plan"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, failFieldID: "F_result"}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), plannerStagesBatchRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome == execution.OutcomeSucceeded || !strings.Contains(results[0].Error, "record staged planning result") {
		t.Fatalf("failed source staging was reported as success: %#v", results)
	}
	project.failFieldID = ""
	results, err = service.RunCycle(t.Context())
	if err != nil || len(results) != 0 {
		t.Fatalf("retry interrupted source staging: results=%#v error=%v", results, err)
	}
	project.loadRemoteItems()
	var source github.WorkItem
	childCount := 0
	for _, current := range project.remoteItems {
		if current.ID == item.ID {
			source = current
		}
		if github.DecodePlannedItemMetadata(current.Body).PlanningSourceID == item.ID {
			childCount++
			if current.Status != "Needs assessment" || current.Approval != "" {
				t.Fatalf("recovery changed staged child authority: %#v", current)
			}
		}
	}
	if childCount != 1 || project.createCount != 1 || source.Status != "Needs assessment" || source.Phase != github.PlanningApprovalPhase || !strings.HasPrefix(source.Approval, "batch-v1:") {
		t.Fatalf("retry did not idempotently complete exact source staging: source=%#v children=%d creates=%d", source, childCount, project.createCount)
	}
	preview, err := service.PlanProjectItemApproval(t.Context(), item.ID)
	if err != nil || preview.Batch == nil || len(preview.Batch.Children) != 1 {
		t.Fatalf("recovered source did not expose the exact approval preview: preview=%#v error=%v", preview, err)
	}
}

func TestInterruptedPlannerSourceRecoveryRejectsChangedChild(t *testing.T) {
	repo := t.TempDir()
	if output, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	runGitTest(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	item := github.WorkItem{ID: "PVTI_plan", Title: "Plan the slice", Body: "Split this request.", Repository: "owner/repo", Status: "Plan"}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, failFieldID: "F_result"}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), plannerStagesBatchRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunCycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	project.failFieldID = ""
	project.loadRemoteItems()
	for index := range project.remoteItems {
		if github.DecodePlannedItemMetadata(project.remoteItems[index].Body).PlanningSourceID == item.ID {
			project.remoteItems[index].Body += "\nchanged after interruption"
		}
	}

	if _, err := service.RunCycle(t.Context()); err == nil || (!strings.Contains(err.Error(), "changed") && !strings.Contains(err.Error(), "mixed-batch")) {
		t.Fatalf("changed interrupted child was not rejected: %v", err)
	}
	project.loadRemoteItems()
	for _, current := range project.remoteItems {
		if github.DecodePlannedItemMetadata(current.Body).PlanningSourceID == item.ID && (current.Status != "Needs assessment" || current.Approval != "") {
			t.Fatalf("changed interrupted child gained executable authority: %#v", current)
		}
	}
}

func TestPartialPlannerBatchIsQuarantinedFromExecution(t *testing.T) {
	project := &fakeGitHubProjectRunner{failCreateAt: 2}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	plan := ProjectPlan{GoalSummary: "Two-card plan", ProjectSuccessCriteria: []string{"The complete behavior works."}, SourceContext: "Build the complete behavior.", WorkItems: []github.PlannedItem{
		{Title: "First", Summary: "First slice", AcceptanceCriteria: []string{"First works"}, Verification: []string{"Test first"}, Risks: []string{}, NonGoals: []string{}},
		{Title: "Second", Summary: "Second slice", AcceptanceCriteria: []string{"Second works"}, Verification: []string{"Test second"}, Risks: []string{}, NonGoals: []string{}},
	}}
	created, err := service.ApplyProjectPlan(t.Context(), plan)
	if err == nil || len(created) != 1 {
		t.Fatalf("partial plan did not report its created subset: created=%#v err=%v", created, err)
	}
	if project.status != "Needs assessment" || !strings.HasPrefix(project.approval, "v2:") {
		t.Fatalf("partial plan card was not left non-executable with creation provenance: status=%q approval=%q", project.status, project.approval)
	}
}

func TestDirectProjectPlanStagesOnlyUntilExplicitCompleteBatchApproval(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()

	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("stage direct plan: %v", err)
	}
	if len(staged) != 2 || staged[0].PlanningBatchFingerprint == "" {
		t.Fatalf("staging omitted complete batch identity: %#v", staged)
	}
	if len(staged[1].Dependencies) != 1 || staged[1].Dependencies[0] != staged[0].ID ||
		!strings.Contains(staged[1].Body, "## Runner planning metadata") ||
		!strings.Contains(staged[1].Body, `"item_id":"`+staged[0].ID+`"`) {
		t.Fatalf("staging did not finalize visible ID-bound dependencies: %#v", staged[1])
	}
	for _, child := range staged {
		if child.Status != "Needs assessment" || !strings.HasPrefix(child.Approval, "batch-v1:") {
			t.Fatalf("direct planner output became executable: %#v", child)
		}
	}
	for _, call := range project.calls {
		if strings.Contains(call, "--single-select-option-id O_ready") {
			t.Fatalf("direct staging wrote an executable status: %s", call)
		}
	}
	if _, err := service.PlanProjectItemApproval(t.Context(), staged[0].ID); err == nil || !strings.Contains(err.Error(), "cannot be approved individually") {
		t.Fatalf("generic item approval could bypass complete direct batch review: %v", err)
	}

	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatalf("preview exact direct batch: %v", err)
	}
	if preview.Destination != "Ready" || len(preview.Children) != len(staged) {
		t.Fatalf("direct approval preview was incomplete: %#v", preview)
	}
	released, err := service.ApplyProjectPlanApproval(t.Context(), preview)
	if err != nil {
		t.Fatalf("approve exact direct batch: %v", err)
	}
	for _, child := range released {
		if child.Status != "Ready" || !strings.HasPrefix(child.Approval, "v2:") {
			t.Fatalf("explicit approval did not release exact child: %#v", child)
		}
	}
}

func TestDirectProjectPlanFinalizesWhileProjectItemConnectionLags(t *testing.T) {
	project := &fakeGitHubProjectRunner{hideCreatedFromList: true}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}

	staged, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
	if err != nil {
		t.Fatalf("stage direct plan while Project item connection lags: %v", err)
	}
	if len(staged) != 2 || project.createCount != 2 {
		t.Fatalf("staging did not retain the exact created batch: staged=%#v creates=%d", staged, project.createCount)
	}
	if len(staged[1].Dependencies) != 1 || staged[1].Dependencies[0] != staged[0].ID {
		t.Fatalf("staging did not finalize exact-ID dependencies: %#v", staged[1])
	}
}

func TestDirectProjectPlanApprovalRejectsCopiedCanonicalMetadataWithoutRunnerProvenance(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	plan.WorkItems = plan.WorkItems[:1]
	planned, fingerprint, err := directProjectPlanItems(plan, "Ready")
	if err != nil {
		t.Fatal(err)
	}
	children := []github.WorkItem{{ID: "PVTI_forged_1", Title: planned[0].Title}}
	bound, err := bindPlanningDependencyIDs(planned, children)
	if err != nil {
		t.Fatal(err)
	}
	for index := range children {
		children[index].Body = github.FormatPlannedItemBody(bound[index])
		children[index].Status = "Needs assessment"
	}
	project.itemsJSON = `{"items":[` + projectItemJSON(children[0]) + `]}`
	if _, err := service.PlanStagedProjectPlanApproval(t.Context(), fingerprint); err == nil || !strings.Contains(err.Error(), "authenticated complete-batch staging provenance") {
		t.Fatalf("copied canonical metadata minted direct scheduling authority: %v", err)
	}
}

func TestDirectProjectPlanStagingSurvivesNormalRecoveryCycle(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
	if err != nil {
		t.Fatalf("stage direct plan: %v", err)
	}
	if results, err := service.RunCycle(t.Context()); err != nil || len(results) != 0 {
		t.Fatalf("normal engine cycle rejected or executed an intact direct staged batch: results=%#v error=%v", results, err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatalf("preview direct batch after recovery: %v", err)
	}
	if len(preview.Children) != len(staged) {
		t.Fatalf("recovery changed direct staged batch: preview=%#v staged=%#v", preview, staged)
	}
}

func TestDependencyTitleRenamesDoNotChangeIDBasedClaimability(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	project.remoteItems[0].Title = "Renamed before release"
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatalf("dependency rename changed batch authorization: %v", err)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	project.loadRemoteItems()
	for index := range project.remoteItems {
		if project.remoteItems[index].ID == staged[0].ID {
			project.remoteItems[index].Title = "Renamed after release"
			project.remoteItems[index].Status = "Done"
			project.remoteItems[index].Approval = testApproval(project.remoteItems[index])
		}
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil || len(ready) != 1 || ready[0].Item.ID != staged[1].ID {
		t.Fatalf("dependency title rename changed ID-based claimability: ready=%#v error=%v", ready, err)
	}
}

func TestInterruptedDependencyMetadataFinalizationResumesExactBatch(t *testing.T) {
	project := &fakeGitHubProjectRunner{failBodyEditAt: 2}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	plan.WorkItems = append(plan.WorkItems, github.PlannedItem{
		Title: "Third direct child", Summary: "Implement the third child.", AcceptanceCriteria: []string{"Third works."},
		Verification: []string{"Test third."}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{"Second direct child"},
	})
	if _, err := service.ApplyProjectPlan(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "finalize dependency metadata") {
		t.Fatalf("interrupted metadata finalization was not surfaced: %v", err)
	}
	if project.createCount != 3 {
		t.Fatalf("interrupted finalization did not first create the complete batch: %d", project.createCount)
	}
	fingerprint := github.DecodePlannedItemMetadata(project.remoteItems[0].Body).PlanningBatchFingerprint
	if _, err := service.PlanStagedProjectPlanApproval(t.Context(), fingerprint); err == nil || !strings.Contains(err.Error(), "dependency graph") {
		t.Fatalf("incompletely finalized batch produced an approval preview: %v", err)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), ProjectPlanApproval{
		BatchFingerprint: fingerprint, Children: []github.WorkItem{{ID: "untrusted-preview"}},
	}); err == nil || !strings.Contains(err.Error(), "staged plan changed") {
		t.Fatalf("incompletely finalized batch was releasable: %v", err)
	}
	project.failBodyEditAt = 0
	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("resume exact metadata finalization: %v", err)
	}
	if project.createCount != 3 || len(staged) != 3 || len(staged[2].Dependencies) != 1 || staged[2].Dependencies[0] != staged[1].ID {
		t.Fatalf("resume did not reuse and ID-bind the exact children: creates=%d staged=%#v", project.createCount, staged)
	}
	bodyEdits := 0
	for _, call := range project.calls {
		args := strings.Fields(call)
		if !strings.HasPrefix(call, "project item-edit ") {
			continue
		}
		id := argumentValue(args, "--id")
		if argumentValue(args, "--body") != "" {
			bodyEdits++
			if !strings.HasPrefix(id, "DI_") {
				t.Fatalf("draft body edit used Project item identity: %s", call)
			}
		} else if argumentValue(args, "--field-id") != "" && !strings.HasPrefix(id, "PVTI_") {
			t.Fatalf("Project field edit used draft content identity: %s", call)
		}
	}
	if bodyEdits == 0 {
		t.Fatal("dependency finalization performed no draft body edits")
	}
}

func TestDirectProjectPlanPartialReleaseRemainsUnclaimableWhenCleanupFails(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
	if err != nil {
		t.Fatalf("stage direct plan: %v", err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatalf("preview direct plan: %v", err)
	}
	project.statusWrites = 0
	project.clearApprovalWrites = 0
	project.failStatusWrites = map[int]bool{
		2: true, // Fail the second child release after the first reaches Ready.
		3: true, // Fail to return the first child to assessment during cleanup.
	}
	project.failClearApprovalAt = 1 // Also leave the first child's signed Ready assertion behind.

	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err == nil || !strings.Contains(err.Error(), "release staged child 2 of 2") ||
		!strings.Contains(err.Error(), "park child") || !strings.Contains(err.Error(), "clear child") {
		t.Fatalf("release and cleanup failures were not reported together: %v", err)
	}
	project.loadRemoteItems()
	var partiallyReleased github.WorkItem
	for _, item := range project.remoteItems {
		if item.ID == staged[0].ID {
			partiallyReleased = item
		}
	}
	if partiallyReleased.Status != "Ready" || !strings.HasPrefix(partiallyReleased.Approval, "v2:") {
		t.Fatalf("test did not reproduce a signed child stranded in Ready: %#v", partiallyReleased)
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil {
		t.Fatalf("poll after partial direct release: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("partial direct batch became executable despite incomplete release: %#v", ready)
	}
	action, err := service.source.Authorize(t.Context(), partiallyReleased)
	if err != nil {
		t.Fatalf("load stranded child's otherwise valid authority: %v", err)
	}
	if _, err := service.source.Claim(t.Context(), action); err == nil || !strings.Contains(err.Error(), "planning batch") {
		t.Fatalf("partial direct batch was claimable after cleanup failure: %v", err)
	}
}

func TestReleasedDirectProjectPlanRemainsCompleteAsChildrenProgress(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	plan.WorkItems[1].Dependencies = []string{}
	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("stage direct plan: %v", err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatalf("preview direct plan: %v", err)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatalf("release direct plan: %v", err)
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("poll released direct batch: ready=%#v error=%v", ready, err)
	}
	claimed, err := service.source.Claim(t.Context(), ready[0], "ready")
	if err != nil {
		t.Fatalf("claim first released direct child: %v", err)
	}
	ready, err = service.source.Poll(t.Context(), 10)
	if err != nil || len(ready) != 1 || ready[0].Item.ID == claimed.Item.ID {
		t.Fatalf("progressing one child relocked the accepted direct batch: ready=%#v error=%v", ready, err)
	}
}

func TestInterruptedDirectProjectPlanRetryReusesExactChildren(t *testing.T) {
	project := &fakeGitHubProjectRunner{failCreateAt: 2}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	partial, err := service.ApplyProjectPlan(t.Context(), plan)
	if err == nil || len(partial) != 1 {
		t.Fatalf("expected one safely staged child before interruption: staged=%#v error=%v", partial, err)
	}
	project.failCreateAt = 0

	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("resume exact interrupted plan: %v", err)
	}
	if len(staged) != 2 || project.createCount != 3 {
		t.Fatalf("retry duplicated or omitted children: staged=%#v create attempts=%d", staged, project.createCount)
	}
	seen := map[string]bool{}
	for _, child := range staged {
		if seen[child.ID] {
			t.Fatalf("retry returned duplicate child %s", child.ID)
		}
		seen[child.ID] = true
	}
}

func TestInterruptedDirectProjectPlanRetryRepairsMissingStagingStatus(t *testing.T) {
	project := &fakeGitHubProjectRunner{failStatusAt: 1}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	partial, err := service.ApplyProjectPlan(t.Context(), plan)
	if err == nil || len(partial) != 0 || project.createCount != 1 {
		t.Fatalf("expected creation interrupted before the staging status completed: staged=%#v creates=%d error=%v", partial, project.createCount, err)
	}
	project.failStatusAt = 0

	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatalf("resume missing staging status: %v", err)
	}
	if len(staged) != 2 || project.createCount != 2 {
		t.Fatalf("status recovery duplicated or omitted a child: staged=%#v creates=%d", staged, project.createCount)
	}
	for _, child := range staged {
		if child.Status != "Needs assessment" || !strings.HasPrefix(child.Approval, "batch-v1:") {
			t.Fatalf("status recovery did not remain safely staged: %#v", child)
		}
	}
}

func TestInterruptedDirectProjectPlanRejectsChangedPlanWithoutWrites(t *testing.T) {
	project := &fakeGitHubProjectRunner{failCreateAt: 2}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	if _, err := service.ApplyProjectPlan(t.Context(), plan); err == nil {
		t.Fatal("expected interrupted first staging attempt")
	}
	project.failCreateAt = 0
	plan.WorkItems[0].Summary = "Changed after partial staging"

	if _, err := service.ApplyProjectPlan(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "different interrupted batch") {
		t.Fatalf("changed interrupted plan was silently accepted: %v", err)
	}
	if project.createCount != 2 {
		t.Fatalf("changed interrupted retry performed a new create: %d", project.createCount)
	}
}

func TestDirectProjectPlanApprovalRejectsChangedChildAfterPreview(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	staged, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	project.remoteItems[0].Body += "\nchanged"

	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err == nil || !strings.Contains(err.Error(), "changed after the approval preview") {
		t.Fatalf("changed staged child was released: %v", err)
	}
	for _, item := range project.remoteItems {
		if item.Status != "Needs assessment" || !strings.HasPrefix(item.Approval, "batch-v1:") {
			t.Fatalf("changed-child rejection altered authority: %#v", item)
		}
	}
}

func TestDirectProjectPlanApprovalRejectsPriorRunnerActionState(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	project.remoteItems[0].Result = "prior attempt"
	if _, err := service.PlanStagedProjectPlanApproval(t.Context(), staged[0].PlanningBatchFingerprint); err == nil || !strings.Contains(err.Error(), "prior Runner action state") {
		t.Fatalf("direct staged preview accepted prior Runner action state: %v", err)
	}
}

func directProjectPlanFixture() ProjectPlan {
	return ProjectPlan{
		GoalSummary: "Deliver the direct plan", ProjectSuccessCriteria: []string{"The complete batch works."}, SourceContext: "Build this exact direct plan.",
		ProjectConstraints: []string{}, OpenDecisions: []string{}, WorkItems: []github.PlannedItem{
			{Title: "First direct child", Summary: "Implement the first child.", AcceptanceCriteria: []string{"First works."}, Verification: []string{"Test first."}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}},
			{Title: "Second direct child", Summary: "Implement the second child.", AcceptanceCriteria: []string{"Second works."}, Verification: []string{"Test second."}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{"First direct child"}},
		},
	}
}

func TestProjectPlanCarriesTheProjectContractIntoEveryCreatedCard(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
		},
	})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	plan := ProjectPlan{
		GoalSummary:            "Deliver a complete, understandable product.",
		ProjectSuccessCriteria: []string{"The primary user journey works end to end.", "The result is clear and polished."},
		ProjectConstraints:     []string{"Keep the implementation in one repository."},
		SourceContext:          "Build the original product without losing its qualitative requirements.",
		WorkItems: []github.PlannedItem{{
			Title: "Build the slice", Summary: "Implement the cohesive slice.", AcceptanceCriteria: []string{"The slice works."},
			Verification: []string{"Exercise the complete user flow."}, Risks: []string{"The visible flow may regress."}, NonGoals: []string{"Do not redesign unrelated screens."},
		}},
	}

	if _, err := service.ApplyProjectPlan(t.Context(), plan); err != nil {
		t.Fatalf("apply project plan: %v", err)
	}
	for _, expected := range []string{
		"## Project outcome", plan.GoalSummary,
		"## Project success criteria", plan.ProjectSuccessCriteria[1],
		"## Project constraints and non-goals", plan.ProjectConstraints[0],
		"## Original project request", plan.SourceContext,
		"## Acceptance criteria", plan.WorkItems[0].AcceptanceCriteria[0],
		"## Proof obligations", plan.WorkItems[0].Verification[0],
		"## Assumptions and risks", plan.WorkItems[0].Risks[0],
		"## Task non-goals", plan.WorkItems[0].NonGoals[0],
	} {
		if !strings.Contains(project.createdBody, expected) {
			t.Fatalf("created card omitted %q:\n%s", expected, project.createdBody)
		}
	}
}

func TestApplyProjectPlanExplainsHowToResolveOpenDecisions(t *testing.T) {
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	_, err = service.ApplyProjectPlan(t.Context(), ProjectPlan{
		GoalSummary: "Build the game", ProjectSuccessCriteria: []string{"The complete product works."}, SourceContext: "Build the requested product.",
		OpenDecisions: []string{"Choose a rendering engine", "Choose a map layout"},
		WorkItems: []github.PlannedItem{{
			Title: "Build", Summary: "Build the game", AcceptanceCriteria: []string{"The game works"}, Verification: []string{"Test build"}, Risks: []string{}, NonGoals: []string{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "2 open decision(s)") || !strings.Contains(err.Error(), "add the answers to the project idea") {
		t.Fatalf("unexpected open-decision error: %v", err)
	}
}

func TestInterruptedPlannerBatchReusesExistingChildrenWithoutDuplicates(t *testing.T) {
	plan := ProjectPlan{GoalSummary: "Resumable plan", ProjectSuccessCriteria: []string{"The planned behavior works."}, SourceContext: "Original request", WorkItems: []github.PlannedItem{{
		Title: "Only child", Repository: "owner/repo", Summary: "Implement once", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test once"}, Risks: []string{}, NonGoals: []string{},
	}}}
	sourceItem := github.WorkItem{ID: "PVTI_plan", Title: "Plan", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner}
	sourceItem.Approval = testApproval(sourceItem)
	plan, err := normalizeProjectPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := planningBatchFingerprint(sourceItem.ID, "plan", "Ready", plan)
	if err != nil {
		t.Fatal(err)
	}
	planned := projectWorkItems(plan)[0]
	planned.PlanningSourceID = sourceItem.ID
	planned.PlanningSourceLane = "plan"
	planned.PlanningSourceFingerprint = github.PlanningSourceFingerprint(sourceItem)
	planned.PlanningDestination = "Ready"
	planned.PlanningBatchFingerprint = fingerprint
	planned.PlanningBatchSize = 1
	planned.PlanningItemIndex = 1
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(sourceItem) + `]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	parent := mustAuthorizeTest(t, service.source, sourceItem)
	if _, err := service.source.CreateStagedFrom(t.Context(), parent, planned); err != nil {
		t.Fatalf("create interrupted child fixture: %v", err)
	}
	created, err := service.applyPlannerBatch(t.Context(), parent, plan, "plan")
	if err != nil || len(created) != 1 {
		t.Fatalf("resume planner batch: created=%#v error=%v", created, err)
	}
	if project.createCount != 1 {
		t.Fatalf("resume created duplicate children: %d", project.createCount)
	}
}

func TestPlannerBatchStagesEveryChildWithoutApprovalOrRelease(t *testing.T) {
	plan := ProjectPlan{
		GoalSummary: "Staged plan", ProjectSuccessCriteria: []string{"The operator reviews every child."}, SourceContext: "Approved request",
		WorkItems: []github.PlannedItem{
			{Title: "First child", Repository: "owner/repo", Summary: "First", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test first"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}},
			{Title: "Second child", Repository: "owner/repo", Summary: "Second", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test second"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{"First child"}},
		},
	}
	parent := github.WorkItem{ID: "PVTI_plan", Title: "Plan", Body: "Approved request", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner}
	parent.Approval = testApproval(parent)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(parent) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}}), project)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.applyPlannerBatch(t.Context(), mustAuthorizeTest(t, service.source, parent), plan, "plan")
	if err != nil || len(created) != 2 {
		t.Fatalf("stage complete batch: created=%#v error=%v", created, err)
	}
	for _, child := range created {
		if child.Status != "Needs assessment" || !strings.HasPrefix(child.Approval, "v2:") {
			t.Fatalf("planner directly authorized a staged child: %#v", child)
		}
	}
	for _, call := range project.calls {
		if strings.Contains(call, "--single-select-option-id O_ready") {
			t.Fatalf("planner wrote an executable status: %s", call)
		}
	}
}

func TestInterruptedPlannerBatchRejectsChangedExistingChild(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 1)
	project.loadRemoteItems()
	source.Status = "In Progress"
	source.Phase = "plan"
	source.Result = ""
	source.Approval = testApproval(source)
	project.remoteItems[0] = source
	project.remoteItems[1].Body += "\nchanged"

	metadata := github.DecodePlannedItemMetadata(project.remoteItems[1].Body)
	plan := ProjectPlan{
		GoalSummary: "Deliver bounded work", ProjectSuccessCriteria: []string{"Every child is reviewed together."}, SourceContext: "Approved planning source",
		ProjectConstraints: []string{}, OpenDecisions: []string{}, WorkItems: []github.PlannedItem{{
			Title: project.remoteItems[1].Title, Repository: "owner/repo", Summary: "Implement the bounded child.", AcceptanceCriteria: []string{"The child works."},
			Verification: []string{"Run its focused test."}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{},
		}},
	}
	project.remoteItems[1].PlanningSourceID = metadata.PlanningSourceID
	if _, err := service.applyPlannerBatch(t.Context(), mustAuthorizeTest(t, service.source, source), plan, "plan"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed interrupted child was silently reused: %v", err)
	}
	if project.createCount != 0 {
		t.Fatalf("changed interrupted batch created a duplicate: %d", project.createCount)
	}
}

func TestInterruptedPlannerBatchValidatesExistingChildrenBeforeCreatingMissingOnes(t *testing.T) {
	plan := directProjectPlanFixture()
	for index := range plan.WorkItems {
		plan.WorkItems[index].Repository = "owner/repo"
	}
	source := github.WorkItem{ID: "PVTI_plan", Title: "Plan", Body: "Approved source", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner}
	source.Approval = testApproval(source)
	normalized, err := normalizeProjectPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := planningBatchFingerprint(source.ID, "plan", "Ready", normalized)
	if err != nil {
		t.Fatal(err)
	}
	planned := projectWorkItems(normalized)
	for index := range planned {
		planned[index].PlanningSourceID = source.ID
		planned[index].PlanningSourceLane = "plan"
		planned[index].PlanningSourceFingerprint = github.PlanningSourceFingerprint(source)
		planned[index].PlanningDestination = "Ready"
		planned[index].PlanningBatchFingerprint = fingerprint
		planned[index].PlanningBatchSize = len(planned)
		planned[index].PlanningItemIndex = index + 1
	}
	changedSecond := github.WorkItem{
		ID: "PVTI_second", Title: planned[1].Title, Body: github.FormatPlannedItemBody(planned[1]) + "\nchanged", Status: "Needs assessment",
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(source) + `,` + projectItemJSON(changedSecond) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.applyPlannerBatch(t.Context(), mustAuthorizeTest(t, service.source, source), plan, "plan"); err == nil || !strings.Contains(err.Error(), "changed after creation") {
		t.Fatalf("changed existing child was not rejected: %v", err)
	}
	if project.createCount != 0 {
		t.Fatalf("validation created a missing child before rejecting changed staged content: %d", project.createCount)
	}
}

func TestProjectPlanMaximumIsEnforcedBeforeProjectWrites(t *testing.T) {
	items := make([]github.PlannedItem, github.MaxPlanningBatchChildren+1)
	for index := range items {
		items[index] = github.PlannedItem{
			Title: fmt.Sprintf("Work %d", index+1), Summary: "Bounded work", AcceptanceCriteria: []string{"Works"},
			Verification: []string{"Run the focused test"}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{},
		}
	}
	encoded, err := json.Marshal(ProjectPlan{
		GoalSummary: "Bound delegated fan-out", ProjectSuccessCriteria: []string{"The batch is reviewable."},
		ProjectConstraints: []string{}, OpenDecisions: []string{}, WorkItems: items,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeProjectPlan(string(encoded)); err == nil || !strings.Contains(err.Error(), "emergency safety maximum") {
		t.Fatalf("oversized model result was not rejected: %v", err)
	}
	if !strings.Contains(string(projectPlanOutlineSchema), fmt.Sprintf(`"maxItems": %d`, github.MaxPlanningBatchChildren)) {
		t.Fatalf("planner outline schema does not carry the production maximum: %s", projectPlanOutlineSchema)
	}

	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}}), project)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.ApplyProjectPlan(t.Context(), ProjectPlan{
		GoalSummary: "Bound delegated fan-out", ProjectSuccessCriteria: []string{"The batch is reviewable."}, SourceContext: "Plan safely.", WorkItems: items,
	})
	if err == nil || project.createCount != 0 {
		t.Fatalf("oversized production plan reached Project writes: error=%v creates=%d", err, project.createCount)
	}
}

func TestProjectPlanMaximumIsCheckedBeforeChildDecodeAllocation(t *testing.T) {
	items := make([]string, github.MaxPlanningBatchChildren)
	for index := range items {
		items[index] = `{}`
	}
	// The over-limit value is deliberately not a PlannedItem. A full struct
	// decode would reject its type first; the bounded token preflight must stop
	// as soon as it sees that this value would exceed the child limit.
	items = append(items, `42`)
	value := `{"goal_summary":"Goal","project_success_criteria":["Works"],"project_constraints":[],"open_decisions":[],"work_items":[` + strings.Join(items, ",") + `]}`
	if _, err := decodeProjectPlan(value); err == nil || !strings.Contains(err.Error(), "emergency safety maximum") {
		t.Fatalf("oversized plan was decoded before its allocation bound: %v", err)
	}
}

func TestOperatorPreviewsAndReleasesOnlyTheCompleteStagedPlannerBatch(t *testing.T) {
	service, project, source, children := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatalf("preview staged batch: %v", err)
	}
	if plan.Batch == nil || plan.Batch.Destination != "Ready" || len(plan.Batch.Children) != len(children) {
		t.Fatalf("approval preview omitted the complete batch: %#v", plan)
	}
	for index, child := range plan.Batch.Children {
		if child.Item.ID != children[index].ID || child.Role != config.WorkRoleImplementer || !strings.HasPrefix(child.Assertion, "v2:") {
			t.Fatalf("approval preview child %d is not exact and executable-authorized: %#v", index+1, child)
		}
	}
	for _, call := range project.calls {
		if strings.Contains(call, "project item-edit") {
			t.Fatalf("preview mutated Project state: %s", call)
		}
	}
	approved, err := service.ApplyProjectItemApproval(t.Context(), plan)
	if err != nil {
		t.Fatalf("release staged batch: %v", err)
	}
	if approved.Status != "Done" || !strings.HasPrefix(approved.Approval, "batch-v1:") || approved.Approval == plan.Batch.Source.Approval {
		t.Fatalf("planning source was not completed after the whole release: %#v", approved)
	}
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && (item.Status != "Ready" || !strings.HasPrefix(item.Approval, "v2:")) {
			t.Fatalf("released child lacks exact implementer authority: %#v", item)
		}
	}
}

func TestForgedPlannerBatchMetadataCannotMintAuthority(t *testing.T) {
	for _, test := range []struct {
		name        string
		removePhase bool
	}{
		{name: "public metadata and phase"},
		{name: "public metadata alone", removePhase: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, project, source, _ := stagedPlannerBatchFixture(t, 1)
			project.loadRemoteItems()
			for index := range project.remoteItems {
				if project.remoteItems[index].ID == source.ID {
					project.remoteItems[index].Approval = ""
					if test.removePhase {
						project.remoteItems[index].Phase = ""
					}
				}
			}
			project.calls = nil

			if _, err := service.PlanProjectItemApproval(t.Context(), source.ID); err == nil {
				t.Fatal("self-consistent public planning metadata minted authority")
			}
			for _, call := range project.calls {
				if strings.Contains(call, "project item-edit") {
					t.Fatalf("forged planning metadata reached a Project write: %s", call)
				}
			}
		})
	}
}

func TestAuthenticatedPlannerBatchRejectsEveryBoundMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*fakeGitHubProjectRunner, string)
	}{
		{name: "source content", change: func(project *fakeGitHubProjectRunner, sourceID string) {
			for index := range project.remoteItems {
				if project.remoteItems[index].ID == sourceID {
					project.remoteItems[index].Body += "\nchanged"
				}
			}
		}},
		{name: "child content and metadata", change: func(project *fakeGitHubProjectRunner, sourceID string) {
			for index := range project.remoteItems {
				if project.remoteItems[index].PlanningSourceID == sourceID {
					project.remoteItems[index].Body += "\nchanged"
				}
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, project, source, _ := stagedPlannerBatchFixture(t, 1)
			project.loadRemoteItems()
			test.change(project, source.ID)
			project.calls = nil
			if _, err := service.PlanProjectItemApproval(t.Context(), source.ID); err == nil {
				t.Fatalf("bound mutation retained staging provenance: %v", err)
			}
			for _, call := range project.calls {
				if strings.Contains(call, "project item-edit") {
					t.Fatalf("bound mutation reached a Project write: %s", call)
				}
			}
		})
	}
}

func TestStagedPlannerBatchApprovalRejectsChangedChildAfterPreview(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range project.remoteItems {
		if project.remoteItems[index].PlanningSourceID == source.ID {
			project.remoteItems[index].Body += "\nchanged after preview"
			break
		}
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "changed after the approval preview") {
		t.Fatalf("changed staged child was silently approved: %v", err)
	}
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && (item.Status != "Needs assessment" || item.Approval != "") {
			t.Fatalf("changed-child rejection altered staging authority: %#v", item)
		}
	}
}

func TestStagedPlannerBatchApprovalRevalidatesPlanningSourceAfterPreview(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 1)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	for index := range project.remoteItems {
		if project.remoteItems[index].ID == source.ID {
			project.remoteItems[index].Body = "changed planning source"
			break
		}
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "changed after the approval preview") {
		t.Fatalf("changed planning source was not revalidated: %v", err)
	}
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && (item.Status != "Needs assessment" || item.Approval != "") {
			t.Fatalf("source-change rejection altered child staging: %#v", item)
		}
	}
}

func TestStagedPlannerBatchApprovalFailureLeavesEveryChildNonExecutable(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	project.failApprovalAt = 2
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "approve planning child") {
		t.Fatalf("approval write failure was not reported: %v", err)
	}
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && (item.Status != "Needs assessment" || item.Approval != "") {
			t.Fatalf("partial approval failure left child authority behind: %#v", item)
		}
	}
}

func TestStagedPlannerBatchPartialReleaseRollsBackEveryChild(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	project.failStatusAt = 4 // two parking writes, one release, then failure
	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "release planning child 2 of 2") {
		t.Fatalf("partial release failure was not reported: %v", err)
	}
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && (item.Status != "Needs assessment" || item.Approval != "") {
			t.Fatalf("partial release left executable child state: %#v", item)
		}
	}
	for _, item := range project.remoteItems {
		if item.ID == source.ID && item.Status == "Done" {
			t.Fatalf("partial release completed its planning source: %#v", item)
		}
	}
}

func TestStagedPlannerBatchPartialReleaseRemainsUnclaimableWhenCleanupFails(t *testing.T) {
	service, project, source, _ := stagedPlannerBatchFixture(t, 2)
	plan, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	project.failStatusWrites = map[int]bool{
		4: true, // Fail the second child release after the first reaches Ready.
		5: true, // Fail to return the first child to assessment during cleanup.
	}
	project.failClearApprovalAt = 1 // Also leave the first child's signed Ready assertion behind.

	if _, err := service.ApplyProjectItemApproval(t.Context(), plan); err == nil || !strings.Contains(err.Error(), "release planning child 2 of 2") ||
		!strings.Contains(err.Error(), "park child") || !strings.Contains(err.Error(), "clear child") {
		t.Fatalf("release and cleanup failures were not reported together: %v", err)
	}
	project.loadRemoteItems()
	var stranded github.WorkItem
	for _, item := range project.remoteItems {
		if item.PlanningSourceID == source.ID && item.Status == "Ready" {
			stranded = item
		}
	}
	if stranded.ID == "" || !strings.HasPrefix(stranded.Approval, "v2:") {
		t.Fatalf("test did not reproduce a signed child stranded in Ready: %#v", stranded)
	}
	ready, err := service.source.Poll(t.Context(), 10)
	if err != nil {
		t.Fatalf("poll after partial parent-linked release: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("partial parent-linked batch became executable: %#v", ready)
	}
	action, err := service.source.Authorize(t.Context(), stranded)
	if err != nil {
		t.Fatalf("load stranded child's otherwise valid authority: %v", err)
	}
	if _, err := service.source.Claim(t.Context(), action); err == nil || !strings.Contains(err.Error(), "planning batch") {
		t.Fatalf("partial parent-linked batch was claimable after cleanup failure: %v", err)
	}
}

func TestProjectPlanningAvailabilityBlocksInterruptedBatchBeforePlanner(t *testing.T) {
	planned := github.PlannedItem{
		Title: "Interrupted child", Repository: "owner/repo", Summary: "Stage the child.",
		AcceptanceCriteria: []string{"The child works."}, Verification: []string{"Verify it."},
		Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}, PlanningSourceLane: directProjectPlanSourceLane,
		PlanningSourceFingerprint: "v1:source", PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch",
		PlanningBatchSize: 5, PlanningItemIndex: 1,
	}
	item := github.WorkItem{
		ID: "PVTI_partial", Title: planned.Title, Body: github.FormatPlannedItemBody(planned), Status: "Needs assessment",
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{
		Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
	}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	err = service.CheckProjectPlanningAvailability(t.Context())
	if err == nil || !strings.Contains(err.Error(), "1 of 5") || !strings.Contains(err.Error(), "PVTI_partial") {
		t.Fatalf("interrupted batch was not reported before planning: %v", err)
	}
	for _, call := range project.calls {
		if !strings.HasPrefix(call, "project view ") && !isProjectFieldsCall(call) && !isLifecycleItemsCall(call) {
			t.Fatalf("availability check performed a non-inspection call: %s", call)
		}
	}
}

func TestProjectPlanningAvailabilityDistinguishesCompleteAndReleasedBatch(t *testing.T) {
	planned := github.PlannedItem{
		Title: "Planned child", Repository: "owner/repo", Summary: "Stage the child.",
		AcceptanceCriteria: []string{"The child works."}, Verification: []string{"Verify it."},
		Risks: []string{}, NonGoals: []string{}, Dependencies: []string{}, PlanningSourceLane: directProjectPlanSourceLane,
		PlanningSourceFingerprint: "v1:source", PlanningDestination: "Ready", PlanningBatchFingerprint: "v1:batch",
		PlanningBatchSize: 1, PlanningItemIndex: 1,
	}
	for _, test := range []struct {
		name       string
		status     string
		wantErr    bool
		wantDetail string
	}{
		{name: "complete unapproved batch", status: "Needs assessment", wantErr: true, wantDetail: "--approve-staged v1:batch"},
		{name: "released batch", status: "Ready"},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := github.WorkItem{
				ID: "PVTI_planned", Title: planned.Title, Body: github.FormatPlannedItemBody(planned), Status: test.status,
			}
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
			cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{
				Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
			}})
			service, err := New(cfg, project)
			if err != nil {
				t.Fatal(err)
			}
			err = service.CheckProjectPlanningAvailability(t.Context())
			if test.wantErr && (err == nil || !strings.Contains(err.Error(), test.wantDetail)) {
				t.Fatalf("complete batch did not direct approval: %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("released batch blocked a new planner call: %v", err)
			}
		})
	}
}

func stagedPlannerBatchFixture(t *testing.T, count int) (*Engine, *fakeGitHubProjectRunner, github.WorkItem, []github.WorkItem) {
	t.Helper()
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	source := github.WorkItem{
		ID: "PVTI_plan", Title: "Plan the bounded work", Body: "Approved planning source", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner,
	}
	source.Approval = testApproval(source)
	plan := ProjectPlan{
		GoalSummary: "Deliver bounded work", ProjectSuccessCriteria: []string{"Every child is reviewed together."}, SourceContext: "Approved planning source",
		ProjectConstraints: []string{}, OpenDecisions: []string{}, WorkItems: make([]github.PlannedItem, count),
	}
	for index := range plan.WorkItems {
		plan.WorkItems[index] = github.PlannedItem{
			Title: fmt.Sprintf("Child %d", index+1), Repository: "owner/repo", Summary: "Implement the bounded child.",
			AcceptanceCriteria: []string{"The child works."}, Verification: []string{"Run its focused test."}, Risks: []string{}, NonGoals: []string{}, Dependencies: []string{},
		}
	}
	plan, err := normalizeProjectPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := planningBatchFingerprint(source.ID, "plan", "Ready", plan)
	if err != nil {
		t.Fatal(err)
	}
	planned := projectWorkItems(plan)
	children := make([]github.WorkItem, count)
	encoded := []string{projectItemJSON(source)}
	for index := range planned {
		planned[index].PlanningSourceID = source.ID
		planned[index].PlanningSourceLane = "plan"
		planned[index].PlanningSourceFingerprint = github.PlanningSourceFingerprint(source)
		planned[index].PlanningDestination = "Ready"
		planned[index].PlanningBatchFingerprint = fingerprint
		planned[index].PlanningBatchSize = count
		planned[index].PlanningItemIndex = index + 1
		planned[index].DependencyIDsResolved = true
		children[index] = github.WorkItem{
			ID: fmt.Sprintf("PVTI_child_%d", index+1), Title: planned[index].Title, Body: github.FormatPlannedItemBody(planned[index]), Repository: planned[index].Repository,
			Status: "Needs assessment", PlanningSourceID: source.ID, PlanningBatchFingerprint: fingerprint, PlanningBatchSize: count, PlanningItemIndex: index + 1,
		}
		encoded = append(encoded, projectItemJSON(children[index]))
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + strings.Join(encoded, ",") + `]}`}
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stagedChildren := make([]github.WorkItem, 0, count)
	for _, item := range items {
		if item.PlanningSourceID == source.ID {
			stagedChildren = append(stagedChildren, item)
		}
	}
	if err := service.source.StagePlanningApproval(t.Context(), mustAuthorizeTest(t, service.source, source), stagedChildren, "Planning completed and staged exact children."); err != nil {
		t.Fatalf("authenticate staged fixture: %v", err)
	}
	project.loadRemoteItems()
	for _, item := range project.remoteItems {
		if item.ID == source.ID {
			source = item
		}
	}
	project.calls = nil
	project.statusWrites = 0
	project.approvalWrites = 0
	project.clearApprovalWrites = 0
	return service, project, source, stagedChildren
}

func TestPlannerBatchStopsBeforeChildAuthorityWriteWhenParentChanges(t *testing.T) {
	plan := ProjectPlan{GoalSummary: "Bound plan", ProjectSuccessCriteria: []string{"The planned behavior works."}, SourceContext: "Original request", WorkItems: []github.PlannedItem{{
		Title: "Bound child", Repository: "owner/repo", Summary: "Implement safely", AcceptanceCriteria: []string{"Works"}, Verification: []string{"Test safety"}, Risks: []string{}, NonGoals: []string{},
	}}}
	parent := github.WorkItem{ID: "PVTI_plan", Title: "Plan", Body: "Approved planning request", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner}
	parent.Approval = testApproval(parent)
	changedParent := parent
	changedParent.Body = "Project editor changed the planning request"
	page := func(item github.WorkItem) string {
		return legacyItemsGraphQLJSON(`{"items":[` + projectItemJSON(item) + `]}`)
	}
	project := &fakeGitHubProjectRunner{
		itemPages: []string{page(parent)}, // Existing-child scan.
		directItemPages: []string{
			directProjectItemGraphQLJSON(parent),        // Initial authority construction.
			directProjectItemGraphQLJSON(parent),        // Batch-entry authority refresh.
			directProjectItemGraphQLJSON(parent),        // Enter child staging with current source authority.
			directProjectItemGraphQLJSON(changedParent), // Revalidate immediately before child creation.
		},
	}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	parentAction := mustAuthorizeTest(t, service.source, parent)
	created, err := service.applyPlannerBatch(t.Context(), parentAction, plan, "plan")
	if err == nil || !strings.Contains(err.Error(), "authority changed") {
		t.Fatalf("changed parent did not stop the planner batch with safe recovery guidance: created=%#v error=%v", created, err)
	}
	if len(created) != 0 || project.createCount != 0 {
		t.Fatalf("changed parent allowed child creation: created=%#v count=%d", created, project.createCount)
	}
	for _, call := range project.calls {
		if strings.Contains(call, "--field-id F_approval") || strings.Contains(call, "--single-select-option-id O_ready") {
			t.Fatalf("changed parent allowed a child authority or executable status write: calls=%#v", project.calls)
		}
	}
}

func TestApplyProjectPlanRejectsDifferentRepository(t *testing.T) {
	project := &fakeGitHubProjectRunner{}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	_, err = service.ApplyProjectPlan(t.Context(), ProjectPlan{
		GoalSummary: "Wrong repository", ProjectSuccessCriteria: []string{"The change works."}, SourceContext: "Make the requested change.", WorkItems: []github.PlannedItem{{
			Title: "Wrong", Repository: "other/repo", Summary: "Must be rejected", AcceptanceCriteria: []string{"Rejected"}, Verification: []string{"Test rejection"}, Risks: []string{}, NonGoals: []string{},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match configured repository") {
		t.Fatalf("different repository plan was accepted: %v", err)
	}
	if project.createdBody != "" {
		t.Fatalf("different repository plan created a Project item: %q", project.createdBody)
	}
}

func TestNormalizeProjectPlanExpandsExactRepositoryNameShorthand(t *testing.T) {
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	plan := ProjectPlan{WorkItems: []github.PlannedItem{{Repository: "repo"}}}
	if err := service.normalizePlanRepositories(&plan); err != nil {
		t.Fatalf("normalize repository shorthand: %v", err)
	}
	if plan.WorkItems[0].Repository != "owner/repo" {
		t.Fatalf("repository shorthand = %q, want owner/repo", plan.WorkItems[0].Repository)
	}
}

func TestRunMovesClaimedRepositoryFailureToBlocked(t *testing.T) {
	run := &fakeGitHubProjectRunner{}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4,
		},
	}
	service, err := New(completeEngineTestConfig(cfg), run)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || run.status != "Blocked" {
		t.Fatalf("repository failure did not reach Blocked: results=%#v status=%q", results, run.status)
	}
}

func TestImplementationMutableSourceMismatchStopsBeforeHarnessInvocation(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_mutable", Title: "Implement", Body: "Approved body", URL: "https://github.com/owner/repo/issues/90",
		Repository: "owner/repo", Status: "In Progress", Phase: "ready", Role: config.WorkRoleImplementer,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.source.Authorize(t.Context(), item)
	if err != nil {
		t.Fatalf("authorize fixture: %v", err)
	}
	changed := item
	changed.Body = "Changed behind the URL"
	project.itemsJSON = `{"items":[` + projectItemJSON(changed) + `]}`
	project.calls = nil

	result := service.executeImplementation(t.Context(), action)
	if result.Outcome != execution.OutcomeBlocked || result.FailureClass != string(execution.FailureIntegrityViolation) || !strings.Contains(result.Error, "digest changed") {
		t.Fatalf("mutable source mismatch was not blocked as an integrity violation: %#v", result)
	}
	for _, call := range project.calls {
		if strings.HasPrefix(call, "codex ") || strings.Contains(call, " codex ") {
			t.Fatalf("mutable source mismatch invoked the harness: calls=%#v", project.calls)
		}
	}
}

func TestImplementerLadderSelectsProfilesFromPersistedQAFailures(t *testing.T) {
	qwen, luna, sol := "qwen/local", "gpt-luna", "gpt-sol"
	cfg := completeEngineTestConfig(config.Config{})
	implementer := cfg.Roles[config.WorkRoleImplementer]
	implementer.Model = &qwen
	cfg.Roles[config.WorkRoleImplementer] = implementer
	cfg.Roles["implementer_luna"] = config.RoleConfig{Extends: config.WorkRoleImplementer, Model: &luna}
	cfg.Roles["implementer_sol"] = config.RoleConfig{Extends: config.WorkRoleImplementer, Model: &sol}
	cfg.ImplementerLadder = []string{config.WorkRoleImplementer, "implementer_luna", "implementer_sol"}
	service, err := New(cfg, &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatalf("configure implementer ladder: %v", err)
	}
	for _, test := range []struct {
		failures int
		role     string
		model    string
	}{
		{failures: 0, role: config.WorkRoleImplementer, model: qwen},
		{failures: 1, role: "implementer_luna", model: luna},
		{failures: 2, role: "implementer_sol", model: sol},
		{failures: 3, role: "implementer_sol", model: sol},
	} {
		item := github.WorkItem{Role: config.WorkRoleImplementer, QAFailures: test.failures}
		role := service.executionRole(item)
		profile, ok := service.cfg.RoleProfile(role)
		if !ok || role != test.role || profile.Model == nil || *profile.Model != test.model {
			t.Fatalf("QA failures %d selected role=%q profile=%#v", test.failures, role, profile)
		}
	}
	restarted, err := New(cfg, &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if role := restarted.executionRole(github.WorkItem{Role: config.WorkRoleImplementer, QAFailures: 1}); role != "implementer_luna" {
		t.Fatalf("restart changed persisted ladder position: %q", role)
	}
	if role := service.executionRole(github.WorkItem{Role: config.WorkRoleReviewer, QAFailures: 2}); role != config.WorkRoleReviewer {
		t.Fatalf("QA count changed a non-implementer role: %q", role)
	}
}

func TestImplementationLadderRunsSelectedProfileWithRetainedQAFeedback(t *testing.T) {
	for _, selected := range []bool{false, true} {
		t.Run(fmt.Sprintf("planner_selected_%t", selected), func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{
				ID: "PVTI_ladder", Title: "Refine implementation", Body: "Acceptance criteria", Repository: "owner/repo",
				Status: "In Progress", Phase: "ready", Role: config.WorkRoleImplementer, QAFailures: 1,
			}
			if selected {
				item.QAFailures = 0
				item.ImplementationProfile = "implementer_luna"
				item.PlanningSourceLane = "local_plan"
				item.PlanningSourceFingerprint = "v1:source"
				item.PlanningDestination = "Ready"
				item.PlanningBatchFingerprint = "v1:batch"
				item.PlanningBatchSize, item.PlanningItemIndex = 1, 1
				item.Body = github.FormatPlannedItemBody(github.PlannedItem{Summary: item.Body, Repository: item.Repository, ImplementationProfile: item.ImplementationProfile, ProfileReason: "Existing pattern", DependencyIDsResolved: true, PlanningSourceLane: item.PlanningSourceLane, PlanningSourceFingerprint: item.PlanningSourceFingerprint, PlanningDestination: item.PlanningDestination, PlanningBatchFingerprint: item.PlanningBatchFingerprint, PlanningBatchSize: 1, PlanningItemIndex: 1})
			}
			item.Approval = testApproval(item)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: item.QAFailures}
			qwen, luna := "qwen/local", "gpt-luna"
			cfg := completeEngineTestConfig(config.Config{
				ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			})
			implementer := cfg.Roles[config.WorkRoleImplementer]
			implementer.Model = &qwen
			cfg.Roles[config.WorkRoleImplementer] = implementer
			cfg.Roles["implementer_luna"] = config.RoleConfig{Extends: config.WorkRoleImplementer, Model: &luna, Description: "Mechanical changes"}
			if selected {
				cfg.PlannerImplementers = []string{"implementer_luna"}
			} else {
				cfg.ImplementerLadder = []string{config.WorkRoleImplementer, "implementer_luna"}
			}
			runner := &successfulImplementationRunner{project: project}
			service, err := New(cfg, runner)
			if err != nil {
				t.Fatal(err)
			}
			content := github.DelegatedContentFor(item)
			if err := service.saveReviewFeedback(item, content, execution.ReviewAssessment{
				Verdict: "needs_changes", Summary: "Preserve the previous edge case.",
			}, nil); err != nil {
				t.Fatalf("save QA feedback: %v", err)
			}
			action := mustAuthorizeTest(t, service.source, item)
			result := service.executeImplementation(t.Context(), action)
			joined := strings.Join(runner.args, " ")
			if result.Outcome != execution.OutcomeSucceeded || argumentValue(runner.args, "--model") != luna {
				t.Fatalf("selected implementation profile was not executed: result=%#v args=%s", result, joined)
			}
			if !strings.Contains(joined, "Preserve the previous edge case.") || strings.Contains(joined, qwen) {
				t.Fatalf("escalated profile lost QA feedback or used the first model: %s", joined)
			}
		})
	}
}

func TestImplementationResumesCompletedHarnessAfterPostProcessingFailure(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_resume_postprocess", Title: "Implement once", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/77",
		Repository: "owner/repo", Status: "Ready", Role: config.WorkRoleImplementer,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &successfulImplementationRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}
	verificationDir := filepath.Dir(service.verificationEvidencePath(item.ID))
	if err := os.MkdirAll(filepath.Dir(verificationDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(verificationDir, []byte("blocks verification storage\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstAction := mustAuthorizeTest(t, service.source, item)
	first := service.executeImplementation(t.Context(), firstAction)
	if first.Outcome != execution.OutcomeBlocked || runner.calls != 1 || !strings.Contains(first.Error, "verification") {
		t.Fatalf("post-processing failure did not retain one completed harness run: result=%#v harness_calls=%d", first, runner.calls)
	}
	if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); err != nil {
		t.Fatalf("completed harness checkpoint is missing: %v", err)
	}
	if err := os.Remove(verificationDir); err != nil {
		t.Fatal(err)
	}
	project.loadRemoteItems()
	var retried github.WorkItem
	for index := range project.remoteItems {
		if project.remoteItems[index].ID != item.ID {
			continue
		}
		retried = project.remoteItems[index]
		retried.Status, retried.Phase, retried.Activity = "Ready", "", ""
		retried.Approval = testApproval(retried)
		project.remoteItems[index] = retried
	}
	secondAction := mustAuthorizeTest(t, service.source, retried)
	second := service.executeImplementation(t.Context(), secondAction)
	if second.Outcome != execution.OutcomeSucceeded || !second.ResumedCheckpoint || runner.calls != 1 || project.status != "Agent QA" {
		t.Fatalf("retry repeated completed model work or failed to continue: result=%#v harness_calls=%d status=%q", second, runner.calls, project.status)
	}
	if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); !os.IsNotExist(err) {
		t.Fatalf("successful transition retained completed checkpoint: %v", err)
	}
}

func TestImplementationQuarantinesChangedContentAndRetainsWorkAcrossBaseAdvance(t *testing.T) {
	for _, test := range []struct {
		name           string
		changeContent  bool
		moveBase       bool
		wantQuarantine bool
	}{
		{name: "reapproved content", changeContent: true, wantQuarantine: true},
		{name: "resolved base movement", moveBase: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			oldItem := github.WorkItem{ID: "PVTI_reapproval", Title: "Implement", Body: "Original criteria", Repository: "owner/repo"}
			root := filepath.Join(filepath.Dir(repo), ".runner-worktrees")
			oldWorkspace, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_" + safeRefComponent(oldItem.ID),
				ItemID: oldItem.ID, DelegatedContentDigest: github.DelegatedContentFor(oldItem).Digest, Repository: oldItem.Repository,
				BranchPrefix: "runner", BaseRef: "origin/main",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(oldWorkspace.WorktreePath, "stale-uncommitted.txt"), []byte("valuable old work\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			current := oldItem
			if test.changeContent {
				current.Body = "Reapproved changed criteria"
			}
			if test.moveBase {
				advanceRemoteBase(t, repo, "base-moved.txt", "new approved base\n")
			}
			current.Status, current.Phase, current.Role = "In Progress", "ready", config.WorkRoleImplementer
			current.Approval = testApproval(current)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(current) + `]}`}
			runner := &successfulImplementationRunner{project: project, inspect: func(dir string) error {
				content, err := os.ReadFile(filepath.Join(dir, "stale-uncommitted.txt"))
				if test.wantQuarantine {
					if !errors.Is(err, os.ErrNotExist) {
						return fmt.Errorf("stale work entered replacement workspace: %w", err)
					}
					return nil
				}
				if err != nil || string(content) != "valuable old work\n" {
					return fmt.Errorf("authenticated work was not retained across the base advance: content=%q error=%w", content, err)
				}
				return nil
			}}
			service, err := New(completeEngineTestConfig(config.Config{
				ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			}), runner)
			if err != nil {
				t.Fatal(err)
			}
			action := mustAuthorizeTest(t, service.source, current)
			result := service.executeImplementation(t.Context(), action)
			if result.Outcome != execution.OutcomeSucceeded || runner.dir == "" {
				t.Fatalf("implementation did not restart cleanly: %#v", result)
			}
			quarantines, err := filepath.Glob(filepath.Join(root, ".runner-quarantine", "assignment_"+safeRefComponent(current.ID)+"-*"))
			wantQuarantines := 0
			if test.wantQuarantine {
				wantQuarantines = 1
			}
			if err != nil || len(quarantines) != wantQuarantines {
				t.Fatalf("unexpected implementation quarantine count: paths=%v error=%v", quarantines, err)
			}
			if test.wantQuarantine {
				if content, err := os.ReadFile(filepath.Join(quarantines[0], "stale-uncommitted.txt")); err != nil || string(content) != "valuable old work\n" {
					t.Fatalf("quarantine lost old work: content=%q error=%v", content, err)
				}
			}
		})
	}
}

func TestTransientBaseFetchFailurePreservesTheRetryLane(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	project := &fakeGitHubProjectRunner{}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir:    repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), baseFetchFailureRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].RetryDisposition != string(execution.RetryManual) || project.status != "Blocked" || project.phase != "ready" || !strings.Contains(project.result, "cortexium-runner retry") {
		t.Fatalf("transient fetch failure lost its retry lane: results=%#v status=%q phase=%q result=%q", results, project.status, project.phase, project.result)
	}
}

func TestHarnessPermissionFailureMovesItemToBlocked(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	project := &fakeGitHubProjectRunner{}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), permissionDeniedImplementationRunner{project: project})
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || project.status != "Blocked" {
		t.Fatalf("permission failure did not reach Blocked: results=%#v status=%q", results, project.status)
	}
	if strings.Contains(project.result, "permission denied") || strings.Contains(project.result, "denied the required command") || !strings.Contains(project.result, "local Runner output") {
		t.Fatalf("blocked result exposed local harness diagnostics: %q", project.result)
	}
	if !strings.Contains(results[0].Error, "permission denied") || !strings.Contains(results[0].Error, "denied the required command") {
		t.Fatalf("local result omitted the harness failure: %#v", results[0])
	}
}

func TestRetryableHarnessFailurePublishesSafeReasonAndNextAction(t *testing.T) {
	item := github.WorkItem{ID: "PVTI_retry", Title: "Review feature", Status: "Agent QA", Role: config.WorkRoleReviewer}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
		},
	})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	if _, err := service.source.LifecycleItems(t.Context()); err != nil {
		t.Fatalf("load project schema: %v", err)
	}
	_, lane := service.laneForItem(item)
	blocker := "Wait until after 10:40am (Europe/Copenhagen) before retrying Claude Code."
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "Claude Code session limit reached; the CLI reports that it resets at 10:40am (Europe/Copenhagen).",
		WorkDone: []string{}, Blocker: &blocker, RemoteDetailSafe: true, DiscardDiagnostics: true,
		FailureClass: execution.FailureCapacityExhausted, RetryDisposition: execution.RetryManual,
	}
	result := RunResult{Item: item, Harness: config.HarnessClaudeCLI, Error: `raw usage JSON with "session_id":"secret"`}
	action, err := service.source.Authorize(t.Context(), item)
	if err != nil {
		t.Fatalf("authorize retryable failure: %v", err)
	}

	result = service.failExecution(t.Context(), action, lane, result, "Agent QA failed", errors.New("exit status 1"), output)
	if project.status != "Blocked" || !strings.Contains(project.result, "structured adapter evidence") || !strings.Contains(project.result, "cortexium-runner retry") || project.phase != "agent_qa" {
		t.Fatalf("retryable failure was not actionable: lane=%#v status=%q project_result=%q run_result=%#v calls=%#v", lane, project.status, project.result, result, project.calls)
	}
	if strings.Contains(project.result, "session_id") || result.Error != "" || result.Summary != output.Summary {
		t.Fatalf("retryable failure exposed or retained raw diagnostics: project=%q result=%#v", project.result, result)
	}
}

func TestTransientHarnessFailureRetriesInPlaceBeforeBlocking(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_automatic_retry", Title: "Review feature", Body: "Acceptance criteria", Repository: "owner/repo",
		Status: "Agent QA", Role: config.WorkRoleReviewer, QAFailures: 1,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
		},
	}), project)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	loadItem := func() github.WorkItem {
		t.Helper()
		items, err := service.source.LifecycleItems(t.Context())
		if err != nil {
			t.Fatalf("load retry item: %v", err)
		}
		for _, current := range items {
			if current.ID == item.ID {
				return current
			}
		}
		t.Fatalf("retry item %s is missing", item.ID)
		return github.WorkItem{}
	}

	blocker := "Runner can retry after a short provider recovery delay."
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "The harness provider reported a transient service failure.",
		WorkDone: []string{}, Blocker: &blocker, RemoteDetailSafe: true, DiscardDiagnostics: true,
		FailureClass: execution.FailureTransientExternal, RetryDisposition: execution.RetryAutomatic,
	}
	for attempt := 1; attempt <= maxAutomaticRetries+1; attempt++ {
		current := loadItem()
		current.Role = config.WorkRoleReviewer
		action := mustAuthorizeTest(t, service.source, current)
		_, lane := service.laneForItem(current)
		result := service.failExecution(t.Context(), action, lane, RunResult{Item: current, Error: "token=private"}, "Agent QA failed", errors.New("exit status 1"), output)
		current = loadItem()
		if current.QAFailures != 1 {
			t.Fatalf("provider retry changed QA rejection count on attempt %d: %#v", attempt, current)
		}
		if attempt <= maxAutomaticRetries {
			if result.Outcome != "retry_scheduled" || result.RetryDisposition != string(execution.RetryAutomatic) || result.RetryAfter == "" ||
				current.Status != "Agent QA" || current.Activity != config.RunnerActivityWaitingForHarness || strings.Contains(current.Result, "cortexium-runner retry") {
				t.Fatalf("automatic retry %d was not retained in place: result=%#v item=%#v", attempt, result, current)
			}
			if !service.automaticRetryPending(current, time.Now()) {
				t.Fatalf("automatic retry %d was not delayed", attempt)
			}
			service.automaticRetryMu.Lock()
			state := service.automaticRetries[item.ID]
			state.notBefore = time.Now().Add(-time.Second)
			service.automaticRetries[item.ID] = state
			service.automaticRetryMu.Unlock()
			continue
		}
		if result.Outcome != execution.OutcomeBlocked || result.RetryDisposition != string(execution.RetryManual) ||
			current.Status != "Blocked" || current.Phase != "agent_qa" || !strings.Contains(current.Result, "cortexium-runner retry") {
			t.Fatalf("exhausted provider retries were not made actionable: result=%#v item=%#v", result, current)
		}
		if strings.Contains(current.Result, "token") || result.Error != "" {
			t.Fatalf("provider retry exposed retained diagnostics: result=%#v item=%#v", result, current)
		}
	}
}

func TestNonRetryableFailureDoesNotPublishRetryAction(t *testing.T) {
	item := github.WorkItem{ID: "PVTI_no_retry", Title: "Review feature", Status: "Agent QA", Role: config.WorkRoleReviewer}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo",
		},
	}), project)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	if _, err := service.source.LifecycleItems(t.Context()); err != nil {
		t.Fatalf("load project schema: %v", err)
	}
	_, lane := service.laneForItem(item)
	blocker := "Inspect the retained workspace before taking further action."
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "Workspace integrity could not be established.",
		WorkDone: []string{}, Blocker: &blocker, RemoteDetailSafe: true,
		FailureClass: execution.FailureIntegrityViolation, RetryDisposition: execution.RetryNone,
	}

	action, err := service.source.Authorize(t.Context(), item)
	if err != nil {
		t.Fatalf("authorize non-retryable failure: %v", err)
	}
	service.failExecution(t.Context(), action, lane, RunResult{Item: item}, output.Summary, errors.New("integrity failure"), output)
	if strings.Contains(project.result, "cortexium-runner retry") || project.phase != "" {
		t.Fatalf("non-retryable failure published a retry path: phase=%q result=%q", project.phase, project.result)
	}
}

func TestCombinedHarnessAndIntegrityFailuresRemainLocalAndBlockPublication(t *testing.T) {
	item := github.WorkItem{ID: "PVTI_combined_failure", Title: "Review safely", Status: "Agent QA", Role: config.WorkRoleReviewer}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.source.LifecycleItems(t.Context()); err != nil {
		t.Fatal(err)
	}
	action, err := service.source.Authorize(t.Context(), item)
	if err != nil {
		t.Fatal(err)
	}
	_, lane := service.laneForItem(item)
	combined := errors.Join(errors.New("raw harness token=secret"), errors.New("active checkout changed"))
	output := execution.Output{
		Outcome: execution.OutcomeBlocked, Summary: "Workspace integrity verification failed.",
		FailureClass: execution.FailureIntegrityViolation, RetryDisposition: execution.RetryNone, RemoteDetailSafe: true,
	}
	result := service.failExecution(t.Context(), action, lane, RunResult{Item: item}, output.Summary, combined, output)
	for _, cause := range []string{"raw harness token=secret", "active checkout changed"} {
		if !strings.Contains(result.Error, cause) {
			t.Fatalf("local result omitted combined cause %q: %#v", cause, result)
		}
		if strings.Contains(project.result, cause) {
			t.Fatalf("Project result exposed local cause %q: %q", cause, project.result)
		}
	}
	if project.status != "Blocked" || !strings.Contains(project.result, "workspace integrity violation") || strings.Contains(project.result, "cortexium-runner retry") {
		t.Fatalf("combined failure did not block through fixed integrity policy: status=%q result=%q", project.status, project.result)
	}
}

func TestBlockedItemCanBeRetriedByExactTitleIntoItsRecordedLane(t *testing.T) {
	current := github.WorkItem{
		ID: "PVTI_1", Title: "Implement the slice", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/1",
		Repository: "owner/repo", Status: "Blocked", Phase: "agent_qa", Result: "Previous browser capability blocker.",
	}
	project := &fakeGitHubProjectRunner{
		status: current.Status, phase: current.Phase, result: current.Result,
		approval: testApproval(current), approvalSet: true,
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	plan, err := service.PlanProjectItemRetry(t.Context(), current.Title)
	if err != nil || plan.TargetLaneID != "agent_qa" || plan.TargetStatus != "Agent QA" {
		t.Fatalf("plan retry: plan=%#v error=%v", plan, err)
	}
	retried, err := service.ApplyProjectItemRetry(t.Context(), plan)
	if err != nil {
		t.Fatalf("apply retry: %v", err)
	}
	if retried.Status != "Agent QA" || project.status != "Agent QA" || project.phase != "agent_qa" || project.result != current.Result {
		t.Fatalf("retry lost state: item=%#v status=%q phase=%q result=%q", retried, project.status, project.phase, project.result)
	}
}

func TestBlockedItemRetryCanReplaceStaleFeedbackAndResetQAFailures(t *testing.T) {
	current := github.WorkItem{
		ID: "PVTI_1", Title: "Implement the slice", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/1",
		Repository: "owner/repo", Status: "Blocked", Phase: "ready", Result: "Delete an operator-owned file.", QAFailures: 3,
	}
	project := &fakeGitHubProjectRunner{
		status: current.Status, phase: current.Phase, result: current.Result, qaFailures: current.QAFailures,
		approval: testApproval(current), approvalSet: true,
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	feedbackPath := service.reviewFeedbackPath(current.ID)
	if err := os.MkdirAll(filepath.Dir(feedbackPath), 0o700); err != nil {
		t.Fatalf("create private feedback directory: %v", err)
	}
	staleFeedback := `{"version":1,"item_id":"PVTI_1","delegated_content_digest":"v1:test","items":["Delete the task-owned file."]}`
	if err := os.WriteFile(feedbackPath, []byte(staleFeedback+"\n"), 0o600); err != nil {
		t.Fatalf("write stale private feedback: %v", err)
	}
	checkpointPath := service.implementationCheckpointPath(current.ID)
	if err := os.MkdirAll(filepath.Dir(checkpointPath), 0o700); err != nil {
		t.Fatalf("create private checkpoint directory: %v", err)
	}
	if err := os.WriteFile(checkpointPath, []byte("saved implementation result\n"), 0o600); err != nil {
		t.Fatalf("write saved implementation checkpoint: %v", err)
	}

	const correction = "Keep the task-owned file and leave unrelated operator changes untouched."
	plan, err := service.PlanProjectItemRetryWithFeedback(t.Context(), current.ID, correction)
	if err != nil || plan.FeedbackOverride != correction {
		t.Fatalf("plan retry feedback override: plan=%#v error=%v", plan, err)
	}
	retried, err := service.ApplyProjectItemRetry(t.Context(), plan)
	if err != nil {
		t.Fatalf("apply retry feedback override: %v", err)
	}
	if retried.Status != "Ready" || project.status != "Ready" || project.phase != "ready" || project.qaFailures != 0 {
		t.Fatalf("retry did not reset the item: item=%#v status=%q phase=%q failures=%d", retried, project.status, project.phase, project.qaFailures)
	}
	if !strings.Contains(project.result, correction) || strings.Contains(project.result, current.Result) {
		t.Fatalf("retry retained stale feedback: %q", project.result)
	}
	if _, err := os.Stat(feedbackPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry retained private Agent QA feedback: %v", err)
	}
	if _, err := os.Stat(checkpointPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retry feedback retained a saved implementation result: %v", err)
	}
}

func TestCandidateValidationPublishesCorrectionAndPlainRetryRerunsImplementation(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_candidate_correction", Title: "Correct candidate content", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/78",
		Repository: "owner/repo", Status: "Ready", Role: config.WorkRoleImplementer,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &successfulImplementationRunner{project: project}
	runner.inspect = func(dir string) error {
		content := "corrected candidate\n"
		if runner.calls == 1 {
			content = "PRIVATE-CANDIDATE-CONTENT  \n"
		}
		return os.WriteFile(filepath.Join(dir, "candidate.md"), []byte(content), 0o644)
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}
	first := service.executeImplementation(t.Context(), mustAuthorizeTest(t, service.source, item))
	if first.Outcome != execution.OutcomeBlocked || first.FailureClass != string(execution.FailureCandidateValidation) || first.RetryDisposition != string(execution.RetryManual) || runner.calls != 1 {
		t.Fatalf("candidate validation did not produce a retryable content failure: result=%#v harness_calls=%d", first, runner.calls)
	}
	if project.status != "Blocked" || project.phase != "ready" || !strings.Contains(project.result, "trailing whitespace") || !strings.Contains(project.result, "git diff --cached --check") {
		t.Fatalf("candidate correction was not published safely: status=%q phase=%q result=%q", project.status, project.phase, project.result)
	}
	if strings.Contains(project.result, "PRIVATE-CANDIDATE-CONTENT") || strings.Contains(project.result, "candidate.md") || strings.Contains(project.result, "workspace integrity violation") {
		t.Fatalf("candidate correction exposed content or used the wrong classification: %q", project.result)
	}
	if _, err := os.Stat(service.implementationCheckpointPath(item.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid candidate retained the completed harness checkpoint: %v", err)
	}

	plan, err := service.PlanProjectItemRetry(t.Context(), item.ID)
	if err != nil {
		t.Fatalf("plan candidate retry: %v", err)
	}
	if _, err := service.ApplyProjectItemRetry(t.Context(), plan); err != nil {
		t.Fatalf("apply candidate retry: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("reload candidate retry: %v", err)
	}
	var retried github.WorkItem
	for _, candidate := range items {
		if candidate.ID == item.ID {
			retried = candidate
			break
		}
	}
	second := service.executeImplementation(t.Context(), mustAuthorizeTest(t, service.source, retried))
	if second.Outcome != execution.OutcomeSucceeded || second.ResumedCheckpoint || runner.calls != 2 || project.status != "Agent QA" {
		t.Fatalf("plain retry did not rerun and correct the implementation: result=%#v harness_calls=%d status=%q", second, runner.calls, project.status)
	}
	if !strings.Contains(strings.Join(runner.args, " "), "trailing whitespace") {
		t.Fatalf("retry assignment omitted the actionable candidate correction: %s", strings.Join(runner.args, " "))
	}
}

func TestBlockedItemWithoutPhaseInfersUniqueAuthenticatedRoleLane(t *testing.T) {
	current := github.WorkItem{
		ID: "PVTI_1", Title: "Implement the slice", Body: "Acceptance criteria", URL: "https://github.com/owner/repo/issues/1",
		Repository: "owner/repo", Status: "Blocked", Result: "Workspace setup failed before the retry lane was recorded.",
	}
	project := &fakeGitHubProjectRunner{
		status: current.Status, result: current.Result,
		approval: testApproval(current), approvalSet: true,
	}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	plan, err := service.PlanProjectItemRetry(t.Context(), current.ID)
	if err != nil || plan.TargetLaneID != "ready" || plan.TargetStatus != "Ready" {
		t.Fatalf("infer unique authenticated role lane: plan=%#v error=%v", plan, err)
	}
	retried, err := service.ApplyProjectItemRetry(t.Context(), plan)
	if err != nil {
		t.Fatalf("apply inferred retry: %v", err)
	}
	if retried.Status != "Ready" || project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("inferred retry used the wrong lane: item=%#v status=%q phase=%q", retried, project.status, project.phase)
	}
}

func TestIdleRunCycleReusesOneProjectSnapshotAndCachedSchema(t *testing.T) {
	run := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4},
	}
	service, err := New(completeEngineTestConfig(cfg), run)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	for cycle := 0; cycle < 2; cycle++ {
		results, runErr := service.RunCycle(t.Context())
		if runErr != nil || len(results) != 0 {
			t.Fatalf("idle cycle %d: results=%#v error=%v", cycle+1, results, runErr)
		}
	}
	counts := map[string]int{}
	for _, call := range run.calls {
		if strings.HasPrefix(call, "project view ") {
			counts["project view"]++
		}
		if isProjectFieldsCall(call) {
			counts["fields"]++
		}
		if isLifecycleItemsCall(call) {
			counts["items"]++
		}
	}
	if counts["project view"] != 1 || counts["fields"] != 1 || counts["items"] != 2 {
		t.Fatalf("idle cycles reloaded Project state: counts=%#v calls=%#v", counts, run.calls)
	}
}

func TestPreparePollRevalidatesSeveralClaimsWithOneFreshSnapshot(t *testing.T) {
	items := []github.WorkItem{
		{ID: "PVTI_one", Title: "One", Body: "First", Repository: "owner/repo", Status: "Ready"},
		{ID: "PVTI_two", Title: "Two", Body: "Second", Repository: "owner/repo", Status: "Ready"},
	}
	for index := range items {
		items[index].Approval = testApproval(items[index])
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(items[0]) + `,` + projectItemJSON(items[1]) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), MaxParallelism: 2,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), project)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.preparePoll(t.Context(), 2, false, nil)
	if err != nil || len(prepared.claimed) != 2 {
		t.Fatalf("prepare claims: claimed=%#v error=%v", prepared.claimed, err)
	}
	lists, batches := 0, 0
	for _, call := range project.calls {
		if isLifecycleItemsCall(call) {
			lists++
		}
		if isBatchProjectUpdateCall(call) {
			batches++
		}
	}
	if lists != 2 || batches != 2 {
		t.Fatalf("selected claims used lists=%d batches=%d, want two snapshots and one batched mutation per claim: %#v", lists, batches, project.calls)
	}
}

func TestPendingProjectWorkKeepsContinuousPollingResponsive(t *testing.T) {
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), &fakeGitHubProjectRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if !service.hasPendingObservation([]github.WorkItem{{Status: "PR Ready"}}) {
		t.Fatal("PR Ready work was treated as a quiescent board")
	}
	if service.hasPendingObservation([]github.WorkItem{{Status: "Done"}, {Status: "Blocked"}}) {
		t.Fatal("terminal and human-blocked work prevented explicit idle backoff")
	}
}

func TestRunCycleCanThrottleAssessmentIntakeIndependently(t *testing.T) {
	run := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion,
		RunnerID:      "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), run)
	if err != nil {
		t.Fatalf("configure engine: %v", err)
	}
	if _, _, err := service.runCycle(t.Context(), true); err != nil {
		t.Fatalf("cycle with intake sync: %v", err)
	}
	if _, _, err := service.runCycle(t.Context(), false); err != nil {
		t.Fatalf("cycle without intake sync: %v", err)
	}
	issueLists := 0
	for _, call := range run.calls {
		if strings.HasPrefix(call, "issue list ") {
			issueLists++
		}
	}
	if issueLists != 1 {
		t.Fatalf("intake was not independently throttled: calls=%#v", run.calls)
	}
}

func TestConfigRejectsRemovedServiceIntegrationFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runner.config.json")
	configJSON := `{
	  "config_version":1,
  "runner_id":"runner",
  "api_base_url":"https://example.invalid",
  "project_dir":"/project",
  "github_project":{"owner":"example","number":1}
}`
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	_, err := config.LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("removed service integration field was accepted: %v", err)
	}
}

func TestConfigRejectsUnknownFields(t *testing.T) {
	configJSON := `{"config_version":1,"runner_id":"runner","unexpected":true,"project_dir":"/project","github_project":{"owner":"example","number":1}}`
	path := filepath.Join(t.TempDir(), "runner.config.json")
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := config.LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown configuration field was accepted: %v", err)
	}
}

func TestReconciliationTreatsClosedPRAsTerminalBeforeBranchValidation(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		Role: config.WorkRoleImplementer, PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), closedPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile closed PR: %v", err)
	}
	if project.status != "Blocked" || project.phase != "agent_qa" || !strings.Contains(project.result, "closed without merge") {
		t.Fatalf("closed PR did not block the item with a reviewer retry path: status=%q phase=%q result=%q", project.status, project.phase, project.result)
	}
}

func TestReconciliationMovesMergedPRToDone(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), mergedPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile merged PR: %v", err)
	} else if !changed {
		t.Fatal("merged PR reconciliation did not report progress")
	}
	if project.status != "Done" || !strings.Contains(project.result, "was merged") {
		t.Fatalf("merged PR did not complete item: status=%q result=%q", project.status, project.result)
	}
}

func TestRunCycleReportsMergedPullRequestAsProgress(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	project := &fakeGitHubProjectRunner{
		status: "PR Ready", pullRequest: "https://github.com/owner/repo/pull/12", branch: "cortexium/task", qaCommit: "qa-head",
	}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), mergedPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	results, madeProgress, err := service.runCycle(t.Context(), false)
	if err != nil {
		t.Fatalf("run merged PR cycle: %v", err)
	}
	if !madeProgress || project.status != "Done" {
		t.Fatalf("merged PR cycle progress=%t status=%q, want progress and Done", madeProgress, project.status)
	}
	if len(results) != 0 {
		t.Fatalf("merged PR transition produced execution results: %#v", results)
	}
}

func TestReconciliationRequestsMissingAutomaticMergeAtPRReady(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", false)
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile automatic merge: %v", err)
	}
	if !changed || runner.mergeRequests != 1 || runner.mergeCancels != 0 || !runner.enabled || project.status != "" {
		t.Fatalf("missing automatic merge was not recovered: changed=%t requests=%d cancels=%d enabled=%t status=%q", changed, runner.mergeRequests, runner.mergeCancels, runner.enabled, project.status)
	}
}

func TestRunCycleRequestsAutomaticMergeBeforeAdmissionAndParallelWork(t *testing.T) {
	service, runner, _ := autoMergeReconciliationService(t, "PR Ready", false)
	service.cfg.MaxParallelism = 1
	service.cfg.AdmissionBudget = &config.AdmissionBudgetConfig{WindowSeconds: 3600, MaxAttempts: 1}
	service.SetMetricsHistoryReader(func() (metrics.ReadResult, error) {
		return metrics.ReadResult{Attempts: []metrics.Attempt{{Event: metrics.Event{AttemptID: "used", StartedAt: time.Now().UTC().Add(-time.Second)}}}}, nil
	})

	results, madeProgress, err := service.runCycle(t.Context(), false)
	if err != nil {
		t.Fatalf("run admission-paused reconciliation cycle: %v", err)
	}
	if runner.mergeRequests != 1 || !runner.enabled || !madeProgress || len(results) != 0 {
		t.Fatalf("automatic merge was blocked by admission or parallelism: requests=%d enabled=%t progress=%t results=%#v", runner.mergeRequests, runner.enabled, madeProgress, results)
	}
	if decision := service.LastAdmissionDecision(); decision.Allowed {
		t.Fatalf("test did not exhaust execution admission: %#v", decision)
	}
}

func TestReconciliationRejectsMissingWorkspaceIdentityBeforeRequestingAutomaticMerge(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", false)
	identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent("PVTI_auto_merge_reconcile")+".json")
	if err := os.Remove(identityPath); err != nil {
		t.Fatalf("remove canonical workspace identity fixture: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile identity-mismatched publication: %v", err)
	}
	if !changed || runner.mergeRequests != 0 || runner.enabled || project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("identity-mismatched publication did not fail closed: changed=%t requests=%d enabled=%t status=%q phase=%q", changed, runner.mergeRequests, runner.enabled, project.status, project.phase)
	}
	if !strings.Contains(project.result, "workspace identity mismatch") || !strings.Contains(project.result, "implementation and QA must run again") {
		t.Fatalf("identity-mismatched publication did not report safe recovery guidance: %q", project.result)
	}
}

func TestReconciliationDisarmsAutomaticMergeBeforeRequeueingIdentityMismatch(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", true)
	identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent("PVTI_auto_merge_reconcile")+".json")
	if err := os.Remove(identityPath); err != nil {
		t.Fatalf("remove canonical workspace identity fixture: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile armed identity mismatch: %v", err)
	}
	if !changed || runner.mergeCancels != 1 || runner.mergeRequests != 0 || runner.enabled || project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("identity mismatch remained merge-armed: changed=%t requests=%d cancels=%d enabled=%t status=%q phase=%q", changed, runner.mergeRequests, runner.mergeCancels, runner.enabled, project.status, project.phase)
	}
}

func TestReconciliationDisarmsPreviouslyEnabledAutomaticMergeWhenConfigurationIsDisabled(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", true)
	service.cfg.GitHubProject.AutoMerge = false
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile previously armed identity mismatch: %v", err)
	}
	if !changed || runner.mergeCancels != 1 || runner.mergeRequests != 0 || runner.enabled || project.status != "" || project.phase != "" {
		t.Fatalf("previously enabled automatic merge remained armed: changed=%t requests=%d cancels=%d enabled=%t status=%q phase=%q", changed, runner.mergeRequests, runner.mergeCancels, runner.enabled, project.status, project.phase)
	}
}

func TestReconciliationBlocksIdentityMismatchWhenAutomaticMergeCannotBeDisabled(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", true)
	runner.mergeCancelErr = errors.New("merge permission denied")
	identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent("PVTI_auto_merge_reconcile")+".json")
	if err := os.Remove(identityPath); err != nil {
		t.Fatalf("remove canonical workspace identity fixture: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile failed automatic-merge cancellation: %v", err)
	}
	if !changed || runner.mergeCancels != 1 || runner.mergeRequests != 0 || !runner.enabled || project.status != "Blocked" {
		t.Fatalf("failed cancellation did not preserve manual recovery: changed=%t requests=%d cancels=%d enabled=%t status=%q phase=%q", changed, runner.mergeRequests, runner.mergeCancels, runner.enabled, project.status, project.phase)
	}
	if !strings.Contains(project.result, "disable auto-merge on the PR manually before retrying") {
		t.Fatalf("failed cancellation omitted manual recovery guidance: %q", project.result)
	}
}

func TestReconciliationRefetchesWorkspaceIdentityAtAutomaticMergeBoundary(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", false)
	identityPath := filepath.Join(service.implementationWorkspaceRoot(), ".runner-state", "assignment_"+safeRefComponent("PVTI_auto_merge_reconcile")+".json")
	runner.onItemList = func(call int) {
		if call == 3 {
			if err := os.Remove(identityPath); err != nil {
				t.Fatalf("remove identity between reconciliation and publication: %v", err)
			}
		}
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile publication identity race: %v", err)
	}
	if !changed || runner.mergeRequests != 0 || runner.enabled || project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("publication identity race did not fail closed: changed=%t requests=%d enabled=%t status=%q phase=%q calls=%d", changed, runner.mergeRequests, runner.enabled, project.status, project.phase, runner.itemListCalls)
	}
	if !strings.Contains(project.result, "workspace identity mismatch") || !strings.Contains(project.result, "implementation and QA must run again") {
		t.Fatalf("publication identity race did not report safe recovery guidance: %q", project.result)
	}
}

func TestReconciliationRetriesWhenBaseMovesAtAutomaticMergeBoundary(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "PR Ready", false)
	runner.onPRView = func(call int) {
		if call != 2 {
			return
		}
		advanceRemoteBase(t, service.cfg.ProjectDir, "base-at-integration.txt", "new base\n")
		remote := filepath.Join(filepath.Dir(service.cfg.ProjectDir), "origin.git")
		runner.baseRevision = strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"))
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile moving automatic integration base: %v", err)
	} else if !changed || len(warnings) != 1 || !strings.Contains(warnings[0].Summary, "retry against the latest base") || runner.mergeRequests != 0 || project.status != "" {
		t.Fatalf("moving base did not defer integration safely: warnings=%#v changed=%t requests=%d status=%q", warnings, changed, runner.mergeRequests, project.status)
	}
	items, err = service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("reload lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("retry moving automatic integration base: %v", err)
	} else if !changed || runner.mergeRequests != 0 || project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("moving base was not returned through implementation and QA: changed=%t requests=%d status=%q phase=%q", changed, runner.mergeRequests, project.status, project.phase)
	}
}

func TestReconciliationDisarmsAutomaticMergeBeforeRework(t *testing.T) {
	service, runner, project := autoMergeReconciliationService(t, "Ready", true)
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	_, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil {
		t.Fatalf("reconcile rework: %v", err)
	}
	if !changed || runner.mergeCancels != 1 || runner.mergeRequests != 0 || runner.enabled || project.phase != "ready" {
		t.Fatalf("automatic merge remained armed during rework: changed=%t requests=%d cancels=%d enabled=%t phase=%q", changed, runner.mergeRequests, runner.mergeCancels, runner.enabled, project.phase)
	}
}

func autoMergeReconciliationService(t *testing.T, status string, enabled bool) (*Engine, *autoMergeReconciliationRunner, *fakeGitHubProjectRunner) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_auto_merge_reconcile", Title: "Reconcile autonomous feature", Body: "Criteria", Repository: "owner/repo", Status: status,
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task",
	}
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID,
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatalf("prepare identity-bound publication workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte(item.ID+"\n"), 0o644); err != nil {
		t.Fatalf("write publication fixture: %v", err)
	}
	runGitTest(t, prepared.WorktreePath, "add", "--all")
	runGitTest(t, prepared.WorktreePath, "commit", "-m", "prepare publication fixture")
	runGitTest(t, prepared.WorktreePath, "push", "-u", "origin", item.Branch)
	head := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
	item.QACommit = head
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &autoMergeReconciliationRunner{project: project, head: head, baseRevision: prepared.BaseRevision, enabled: enabled}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true,
		},
	})
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	return service, runner, project
}

func serializedIntegrationService(t *testing.T, enabled ...bool) (*Engine, *serializedIntegrationRunner, []github.WorkItem) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	items := make([]github.WorkItem, len(enabled))
	pullRequests := make(map[string]*integrationPullRequest, len(enabled))
	projectItems := make([]string, 0, len(enabled))
	for index, autoMergeEnabled := range enabled {
		item := github.WorkItem{
			ID: fmt.Sprintf("PVTI_integration_%d", index+1), Title: fmt.Sprintf("Integrate feature %d", index+1), Body: "Criteria",
			Repository: "owner/repo", Status: "PR Ready", Branch: fmt.Sprintf("cortexium/task-%d", index+1),
			PullRequest: fmt.Sprintf("https://github.com/owner/repo/pull/%d", 12+index),
		}
		prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
			WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
			WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID,
			DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
			BranchName: item.Branch, BaseRef: "origin/main",
		})
		if err != nil {
			t.Fatalf("prepare integration workspace %d: %v", index+1, err)
		}
		runGitTest(t, prepared.WorktreePath, "push", "-u", "origin", item.Branch)
		item.QACommit = strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
		item.Approval = testApproval(item)
		items[index] = item
		projectItems = append(projectItems, projectItemJSON(item))
		pullRequests[item.ID] = &integrationPullRequest{
			url: item.PullRequest, number: 12 + index, branch: item.Branch, head: item.QACommit,
			baseRevision: prepared.BaseRevision, state: "OPEN", enabled: autoMergeEnabled,
		}
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + strings.Join(projectItems, ",") + `]}`}
	runner := &serializedIntegrationRunner{project: project, pullRequests: pullRequests}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true,
		},
	}), runner)
	if err != nil {
		t.Fatalf("configure serialized integration service: %v", err)
	}
	return service, runner, items
}

func TestReconciliationRequestsOnlyOneAutomaticIntegrationPerRepositoryBase(t *testing.T) {
	service, runner, _ := serializedIntegrationService(t, false, false)
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile competing integrations: %v", err)
	} else if !changed {
		t.Fatal("automatic integration request did not report progress")
	}
	if want := []string{"https://github.com/owner/repo/pull/12"}; !reflect.DeepEqual(runner.mergeRequests, want) {
		t.Fatalf("automatic integration requests = %#v, want %#v", runner.mergeRequests, want)
	}
	if runner.pullRequests["PVTI_integration_2"].enabled {
		t.Fatal("second pull request acquired the occupied repository/base integration resource")
	}
	latest, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("reload integration activities: %v", err)
	}
	activities := map[string]string{}
	for _, item := range latest {
		activities[item.ID] = item.Activity
	}
	if activities["PVTI_integration_1"] != config.RunnerActivityWaitingForMerge || activities["PVTI_integration_2"] != config.RunnerActivityWaitingForIntegration {
		t.Fatalf("integration activities = %#v", activities)
	}
}

func TestReconciliationRecoversExistingAutomaticIntegrationClaimBeforeItemOrder(t *testing.T) {
	service, runner, _ := serializedIntegrationService(t, false, true)
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile restart-stable integration owner: %v", err)
	}
	if len(runner.mergeRequests) != 0 || len(runner.mergeCancels) != 0 {
		t.Fatalf("existing integration owner was replaced: requests=%#v cancels=%#v", runner.mergeRequests, runner.mergeCancels)
	}
	if runner.pullRequests["PVTI_integration_1"].enabled || !runner.pullRequests["PVTI_integration_2"].enabled {
		t.Fatalf("integration ownership changed with item order: first=%t second=%t", runner.pullRequests["PVTI_integration_1"].enabled, runner.pullRequests["PVTI_integration_2"].enabled)
	}
}

func TestReconciliationDisarmsDuplicateAutomaticIntegrationClaims(t *testing.T) {
	service, runner, _ := serializedIntegrationService(t, true, true)
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile duplicate integration claims: %v", err)
	} else if !changed {
		t.Fatal("duplicate integration claim cancellation did not report progress")
	}
	if want := []string{"https://github.com/owner/repo/pull/13"}; !reflect.DeepEqual(runner.mergeCancels, want) {
		t.Fatalf("automatic integration cancellations = %#v, want %#v", runner.mergeCancels, want)
	}
	if !runner.pullRequests["PVTI_integration_1"].enabled || runner.pullRequests["PVTI_integration_2"].enabled {
		t.Fatalf("duplicate integration claims remain: first=%t second=%t", runner.pullRequests["PVTI_integration_1"].enabled, runner.pullRequests["PVTI_integration_2"].enabled)
	}
}

func TestFailedIntegrationChecksRequeueWithoutConsumingQAAndReleaseNextCandidate(t *testing.T) {
	service, runner, items := serializedIntegrationService(t, true, false)
	items[0].QAFailures = 2
	items[0].Activity = config.RunnerActivityWaitingForCI
	items[0].Approval = testApproval(items[0])
	items[1].Activity = config.RunnerActivityWaitingForIntegration
	items[1].Approval = testApproval(items[1])
	runner.project.itemsJSON = `{"items":[` + projectItemJSON(items[0]) + `,` + projectItemJSON(items[1]) + `]}`
	failed := runner.pullRequests[items[0].ID]
	failed.mergeState = "BLOCKED"
	failed.checks = `[{"__typename":"CheckRun","name":"Validate","workflowName":"CI","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/owner/repo/actions/runs/1"}]`

	observed, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), observed)
	if err != nil {
		t.Fatalf("reconcile failed integration checks: %v", err)
	}
	if !changed || len(warnings) != 1 || !strings.Contains(warnings[0].Summary, "without recording an Agent QA rejection") {
		t.Fatalf("failed checks did not produce one actionable event: warnings=%#v changed=%t", warnings, changed)
	}
	if want := []string{items[0].PullRequest}; !reflect.DeepEqual(runner.mergeCancels, want) {
		t.Fatalf("failed integration cancellation = %#v, want %#v", runner.mergeCancels, want)
	}
	if want := []string{items[1].PullRequest}; !reflect.DeepEqual(runner.mergeRequests, want) {
		t.Fatalf("next safe integration request = %#v, want %#v", runner.mergeRequests, want)
	}
	if want := []string{items[0].PullRequest}; !reflect.DeepEqual(runner.checkViews, want) {
		t.Fatalf("detailed check inspections = %#v, want active failed owner only %#v", runner.checkViews, want)
	}

	latest, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("reload reconciled items: %v", err)
	}
	byID := map[string]github.WorkItem{}
	for _, item := range latest {
		byID[item.ID] = item
	}
	requeued := byID[items[0].ID]
	if requeued.Status != "Ready" || requeued.Phase != "ready" || requeued.Activity != config.RunnerActivityCIFailed || requeued.QAFailures != 2 ||
		requeued.PullRequest != items[0].PullRequest || requeued.Branch != items[0].Branch || requeued.QACommit != items[0].QACommit {
		t.Fatalf("failed integration lost rework context: %#v", requeued)
	}
	if waiting := byID[items[1].ID]; waiting.Activity != config.RunnerActivityWaitingForMerge {
		t.Fatalf("next integration activity = %q, want %q", waiting.Activity, config.RunnerActivityWaitingForMerge)
	}
}

func TestPendingIntegrationChecksKeepOnlyActiveCandidateWaitingForCI(t *testing.T) {
	service, runner, items := serializedIntegrationService(t, true, false)
	items[0].Activity = config.RunnerActivityWaitingForMerge
	items[0].Approval = testApproval(items[0])
	items[1].Activity = config.RunnerActivityWaitingForIntegration
	items[1].Approval = testApproval(items[1])
	runner.project.itemsJSON = `{"items":[` + projectItemJSON(items[0]) + `,` + projectItemJSON(items[1]) + `]}`
	active := runner.pullRequests[items[0].ID]
	active.mergeState = "BLOCKED"
	active.checks = `[{"__typename":"CheckRun","name":"Validate","workflowName":"CI","status":"IN_PROGRESS","conclusion":""}]`

	observed, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if warnings, changed, err := service.reconcilePullRequests(t.Context(), observed); err != nil {
		t.Fatalf("reconcile pending integration checks: %v", err)
	} else if len(warnings) != 0 || !changed {
		t.Fatalf("pending checks produced unexpected result: warnings=%#v changed=%t", warnings, changed)
	}
	if want := []string{items[0].PullRequest}; !reflect.DeepEqual(runner.checkViews, want) {
		t.Fatalf("detailed check inspections = %#v, want active candidate only %#v", runner.checkViews, want)
	}
	if len(runner.mergeRequests) != 0 || len(runner.mergeCancels) != 0 {
		t.Fatalf("pending checks changed integration ownership: requests=%#v cancels=%#v", runner.mergeRequests, runner.mergeCancels)
	}

	latest, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("reload reconciled items: %v", err)
	}
	activities := map[string]string{}
	for _, item := range latest {
		activities[item.ID] = item.Activity
	}
	if activities[items[0].ID] != config.RunnerActivityWaitingForCI || activities[items[1].ID] != config.RunnerActivityWaitingForIntegration {
		t.Fatalf("pending integration activities = %#v", activities)
	}
}

func TestReconciliationRechecksPRBeforeRefreshingBranchWhenMergeRacesBaseFetch(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	originalHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	runGitTest(t, repo, "checkout", "main")
	advanceRemoteBase(t, repo, "base.txt", "base changed while PR was open\n")

	item := github.WorkItem{
		ID: "PVTI_merge_race", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true},
	}
	run := &openThenMergedPullRequestRunner{project: project}
	service, err := New(completeEngineTestConfig(cfg), run)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile merge racing base refresh: %v", err)
	} else if !changed {
		t.Fatal("merge race reconciliation did not report progress")
	}
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	if project.status != "Done" || !strings.Contains(project.result, "was merged") {
		t.Fatalf("merge race did not complete item: status=%q result=%q", project.status, project.result)
	}
	if remoteHead != originalHead {
		t.Fatalf("merge race mutated PR head: before=%s after=%s", originalHead, remoteHead)
	}
	if run.views != 2 {
		t.Fatalf("PR inspections = %d, want initial and pre-refresh recheck", run.views)
	}
}

func TestReconciliationDoesNotRefreshTaskBranchAlreadyIntegratedIntoBase(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	originalHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	runGitTest(t, repo, "checkout", "main")
	runGitTest(t, repo, "merge", "--no-ff", "cortexium/task", "-m", "merge task")
	runGitTest(t, repo, "push", "origin", "main")

	item := github.WorkItem{
		ID: "PVTI_integrated", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}

	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile integrated task branch: %v", err)
	} else if changed {
		t.Fatal("API-lagged merged branch changed Project state")
	}
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	if project.status != "" || remoteHead != originalHead {
		t.Fatalf("integrated task branch was refreshed: status=%q before=%s after=%s", project.status, originalHead, remoteHead)
	}
}

func TestRunCycleRecoversInterruptedMergedCardDirectlyToDone(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	project := &fakeGitHubProjectRunner{
		status: "In Progress", phase: "ready", pullRequest: "https://github.com/owner/repo/pull/12",
		branch: "cortexium/task", qaCommit: "qa-head",
	}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), mergedPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	results, madeProgress, err := service.runCycle(t.Context(), false)
	if err != nil {
		t.Fatalf("recover interrupted merged card: %v", err)
	}
	if !madeProgress || project.status != "Done" || project.phase != "" {
		t.Fatalf("recovered merged card: progress=%t status=%q phase=%q", madeProgress, project.status, project.phase)
	}
	if len(results) != 0 {
		t.Fatalf("recovered merged card invoked work: %#v", results)
	}
}

func TestTerminalPullRequestMismatchPreservesWorkspaceForDiagnosis(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{
		ID: "PVTI_terminal_mismatch", Title: "Preserve mismatched terminal workspace", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), terminalMismatchedPullRequestRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	prepared := filepath.Join(filepath.Dir(repo), ".runner-worktrees", "assignment_"+safeRefComponent(item.ID))
	if err := os.MkdirAll(prepared, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(prepared, "preserve.txt")
	if err := os.WriteFile(sentinel, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil || changed || len(warnings) != 1 || warnings[0].Outcome != "warning" {
		t.Fatalf("terminal mismatch did not preserve the item for diagnosis: warnings=%#v changed=%t error=%v", warnings, changed, err)
	}
	if !strings.Contains(warnings[0].Summary, "exact reviewed item") || !strings.Contains(warnings[0].Error, "head commit") {
		t.Fatalf("terminal mismatch warning lost identity detail: %#v", warnings[0])
	}
	for _, call := range project.calls {
		if strings.HasPrefix(call, "project item-edit ") {
			t.Fatalf("terminal mismatch rewrote the Project item: %s", call)
		}
	}
	if content, err := os.ReadFile(sentinel); err != nil || string(content) != "preserve\n" {
		t.Fatalf("terminal mismatch removed the recoverable workspace evidence: content=%q err=%v", content, err)
	}
}

func TestTerminalRebasePullRequestAcceptsTreeEquivalentReviewedCommit(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("reviewed tree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "reviewed candidate")
	qaCommit := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	runGitTest(t, repo, "commit", "--amend", "-m", "linearized candidate")
	mergedHead := strings.TrimSpace(runGitTest(t, repo, "rev-parse", "HEAD"))
	if qaCommit == mergedHead {
		t.Fatal("amended fixture did not produce a distinct commit")
	}

	item := github.WorkItem{
		ID: "PVTI_terminal_equivalent", Title: "Reconcile equivalent merged tree", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: qaCommit,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", MergeMethod: "rebase",
		},
	}), terminalTreeEquivalentPullRequestRunner{project: project, head: mergedHead})
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil || !changed {
		t.Fatalf("tree-equivalent terminal reconciliation failed: warnings=%#v changed=%t error=%v", warnings, changed, err)
	}
	for _, warning := range warnings {
		if strings.Contains(warning.Summary, "exact reviewed item") {
			t.Fatalf("tree-equivalent terminal reconciliation retained a commit-identity warning: %#v", warning)
		}
	}
	if project.status != "Done" || !strings.Contains(project.result, "was merged") {
		t.Fatalf("tree-equivalent terminal reconciliation did not complete item: status=%q result=%q", project.status, project.result)
	}
}

func TestReconciliationDoesNotRewriteAlreadyTerminalProjectState(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "Done",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	approved := item
	approved.Status = "PR Ready"
	item.Approval = testApproval(approved)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), terminalNoInspectRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile already terminal PR: %v", err)
	} else if changed {
		t.Fatal("already terminal PR reconciliation reported progress")
	}
	for _, call := range project.calls {
		if strings.HasPrefix(call, "project item-edit ") {
			t.Fatalf("already terminal item was rewritten: %s", call)
		}
	}
}

func TestPreparePollClaimsReadyWorkWithoutReprocessingTerminalPullRequests(t *testing.T) {
	terminal := github.WorkItem{
		ID: "PVTI_terminal", Title: "Already merged", Body: "Criteria", Repository: "owner/repo", Status: "Done",
		Role: config.WorkRoleReviewer, PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/terminal", QACommit: "qa-head",
	}
	terminal.Approval = testApproval(terminal)
	ready := github.WorkItem{ID: "PVTI_ready_after_terminal", Title: "Run ready work", Body: "Criteria", Repository: "owner/repo", Status: "Ready"}
	ready.Approval = testApproval(ready)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(terminal) + `,` + projectItemJSON(ready) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir:    t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), terminalNoInspectRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	prepared, err := service.preparePoll(t.Context(), 1, false, nil)
	if err != nil {
		t.Fatalf("prepare poll: %v", err)
	}
	if len(prepared.results) != 0 || len(prepared.claimed) != 1 || prepared.claimed[0].action.Item.ID != ready.ID {
		t.Fatalf("terminal cleanup delayed or polluted the ready claim: results=%#v claimed=%#v", prepared.results, prepared.claimed)
	}
	lifecycleCalls := 0
	for _, call := range project.calls {
		if isLifecycleItemsCall(call) {
			lifecycleCalls++
		}
	}
	if lifecycleCalls != 2 {
		t.Fatalf("Project snapshots = %d, want initial observation and fresh ready claim only", lifecycleCalls)
	}
}

func TestReconciliationStillRejectsNonTerminalAuthorizationMismatch(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	approved := item
	approved.Status = "PR Ready"
	item.Approval = testApproval(approved)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), terminalNoInspectRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err == nil || !strings.Contains(err.Error(), "action state changed") {
		t.Fatalf("non-terminal authorization mismatch was not rejected: %v", err)
	}
}

func TestRequeuesPullRequestWhenHeadChangesAfterQA(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head", QAFailures: 2,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), changedHeadPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile changed PR head: %v", err)
	}
	if project.status != "Ready" || project.qaFailures != 0 || !strings.Contains(project.result, "changed after agent QA") {
		t.Fatalf("changed PR head did not restart implementation and QA: status=%q failures=%d result=%q", project.status, project.qaFailures, project.result)
	}
}

func TestPullRequestInspectionFailureKeepsRawDiagnosticsLocal(t *testing.T) {
	item := github.WorkItem{
		ID: "PVTI_pr_inspection_failure", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), failingPullRequestInspectionRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil || !changed || len(warnings) != 1 {
		t.Fatalf("reconcile failed inspection: warnings=%#v changed=%t error=%v", warnings, changed, err)
	}
	if strings.Contains(project.result, "raw-token=secret") || !strings.Contains(project.result, "Details are retained in local Runner output") {
		t.Fatalf("raw pull request diagnostic reached Project result: %q", project.result)
	}
	if !strings.Contains(warnings[0].Error, "raw-token=secret") {
		t.Fatalf("raw pull request diagnostic was not retained locally: %#v", warnings[0])
	}
}
func TestPollDelayBacksOffForErrorsAndIdleCycles(t *testing.T) {
	base := DefaultPollInterval
	for failures, want := range map[int]time.Duration{0: base, 1: base, 2: time.Minute, 3: 2 * time.Minute, 10: 5 * time.Minute} {
		if got := pollDelay(base, 45*time.Second, failures, 0); got != want {
			t.Fatalf("error pollDelay(%s, %d) = %s, want %s", base, failures, got, want)
		}
	}
	for idle, want := range map[int]time.Duration{0: base, 1: base, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute, 5: 5 * time.Minute, 10: 5 * time.Minute} {
		if got := pollDelay(base, 5*time.Minute, 0, idle); got != want {
			t.Fatalf("idle pollDelay(%s, %d) = %s, want %s", base, idle, got, want)
		}
	}
	if got := pollDelay(base, DefaultMaxIdleInterval, 0, 10); got != base {
		t.Fatalf("default idle ceiling = %s, want responsive base interval %s", got, base)
	}
	if got := pollDelay(base, 45*time.Second, 0, 10); got != 45*time.Second {
		t.Fatalf("custom idle ceiling = %s, want 45s", got)
	}
	if got := pollDelay(time.Minute, 30*time.Second, 0, 10); got != time.Minute {
		t.Fatalf("idle ceiling below base interval = %s, want 1m", got)
	}
	if got := nextPollDelay(base, DefaultMaxIdleInterval, 0, 0, true); got != 0 {
		t.Fatalf("poll delay after progress = %s, want immediate follow-up", got)
	}
	if got := nextPollDelay(base, DefaultMaxIdleInterval, 0, 1, false); got != base {
		t.Fatalf("poll delay without progress = %s, want %s", got, base)
	}
}

func TestPollDelayDoesNotPassNextIssueIntake(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	if got := capPollDelayForIntake(5*time.Minute, now, now.Add(45*time.Second)); got != 45*time.Second {
		t.Fatalf("capped delay = %s, want 45s", got)
	}
	if got := capPollDelayForIntake(time.Minute, now, time.Time{}); got != time.Minute {
		t.Fatalf("unscheduled intake changed delay to %s", got)
	}
}

func TestHumanReworkImportsPRFeedbackAndResetsRejections(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head", QAFailures: 2,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project, feedback: "Please add the missing edge-case test."})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile human rework: %v", err)
	}
	if project.qaFailures != 0 || project.phase != "ready" || !strings.Contains(project.result, "https://github.com/owner/repo/pull/12") {
		t.Fatalf("human rework did not reset and import feedback: failures=%d phase=%q result=%q", project.qaFailures, project.phase, project.result)
	}
	if strings.Contains(project.result, "missing edge-case test") {
		t.Fatalf("human rework result exposed raw pull request feedback: %q", project.result)
	}
	if items[0].Phase != "ready" || service.reworkRequested(items[0], "ready") {
		t.Fatalf("reconciled rework would be inspected and reset again in the same cycle: %#v", items[0])
	}
}

func TestHumanReworkIgnoresUntrustedPRFeedbackBody(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head", QAFailures: 2,
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: 2}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project, feedback: "attacker prompt", feedbackActor: "attacker"})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile human rework with untrusted feedback: %v", err)
	}
	if !strings.Contains(project.result, "https://github.com/owner/repo/pull/12") || strings.Contains(project.result, "attacker prompt") {
		t.Fatalf("untrusted pull request feedback was not reduced to a safe reference: %q", project.result)
	}
}

func TestOutOfDateOpenPRIsUpdatedAtAutomaticIntegrationBoundary(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	advanceRemoteBase(t, repo, "base.txt", "base update\n")
	item := github.WorkItem{
		ID: "PVTI_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile out-of-date PR: %v", err)
	}
	if project.status != "Ready" || project.phase != "ready" || strings.TrimSpace(project.result) == "" {
		t.Fatalf("out-of-date PR was not updated and requeued: status=%q phase=%q result=%q", project.status, project.phase, project.result)
	}
}

func TestOutOfDateOpenPRCannotSkipFreshReview(t *testing.T) {
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	workflow := config.WorkflowTemplate(false)
	cfg.Workflow = &workflow
	if _, err := New(cfg, subprocess.OSRunner{}); err == nil || !strings.Contains(err.Error(), "require_review must be true") {
		t.Fatalf("review-skipping base refresh was accepted: %v", err)
	}
}

func TestDoesNotReconcileDirtyImplementationLoopBeforeAgentRuns(t *testing.T) {
	project := &fakeGitHubProjectRunner{
		status: "Ready", phase: "ready", branch: "cortexium/task",
		pullRequest: "https://github.com/owner/repo/pull/12",
	}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: t.TempDir(),
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile implementation loop: %v", err)
	}
	if project.status != "Ready" || project.phase != "ready" {
		t.Fatalf("implementation loop was changed before the agent ran: status=%q phase=%q", project.status, project.phase)
	}
}

func TestAgentQAFailsClosedBeforeReviewForChangedContentOrBaseIdentity(t *testing.T) {
	for _, test := range []struct {
		name          string
		changeContent bool
		deleteBranch  bool
	}{
		{name: "reapproved content", changeContent: true},
		{name: "missing retained branch", deleteBranch: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			original := github.WorkItem{
				ID: "PVTI_stale_qa", Title: "Review bound implementation", Body: "Original criteria", Repository: "owner/repo",
				Branch: "cortexium/task",
			}
			root := filepath.Join(filepath.Dir(repo), ".runner-worktrees")
			prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_" + safeRefComponent(original.ID),
				ItemID: original.ID, DelegatedContentDigest: github.DelegatedContentFor(original).Digest, Repository: original.Repository,
				BranchName: original.Branch, BaseRef: "origin/main",
			})
			if err != nil {
				t.Fatal(err)
			}
			valuable := filepath.Join(prepared.WorktreePath, "valuable-unreviewed.txt")
			if err := os.WriteFile(valuable, []byte("preserve for recovery\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			current := original
			if test.changeContent {
				current.Body = "Reapproved changed criteria"
			}
			if test.deleteBranch {
				runGitTest(t, repo, "push", "origin", original.Branch)
				if _, err := workspace.NewGitProvider(subprocess.OSRunner{}).Cleanup(t.Context(), workspace.CleanupRequest{
					WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_" + safeRefComponent(original.ID),
					ItemID: original.ID, DelegatedContentDigest: github.DelegatedContentFor(original).Digest, Repository: original.Repository,
					BranchName: original.Branch, BaseRef: "origin/main",
				}); err != nil {
					t.Fatalf("remove registered worktree: %v", err)
				}
				runGitTest(t, repo, "branch", "-D", original.Branch)
			}
			current.Status, current.Phase, current.Role = "Agent QA", "agent_qa", config.WorkRoleReviewer
			current.Approval = testApproval(current)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(current) + `]}`}
			runner := &reviewForbiddenRunner{project: project}
			service, err := New(completeEngineTestConfig(config.Config{
				ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			}), runner)
			if err != nil {
				t.Fatal(err)
			}

			results, err := service.RunCycle(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || runner.reviewCalls != 0 || project.status != "Blocked" || project.phase != "ready" || project.pullRequest != "" {
				t.Fatalf("identity-mismatched QA did not fail closed before review: results=%#v review_calls=%d status=%q phase=%q PR=%q", results, runner.reviewCalls, project.status, project.phase, project.pullRequest)
			}
			if !strings.Contains(results[0].Summary, "identity is not valid for QA") || !strings.Contains(project.result, "workspace integrity violation") || strings.Contains(project.result, "workspace identity mismatch") {
				t.Fatalf("identity-mismatched QA did not report safe recovery context: result=%#v project_result=%q", results[0], project.result)
			}
			if !test.deleteBranch {
				if content, err := os.ReadFile(valuable); err != nil || string(content) != "preserve for recovery\n" {
					t.Fatalf("failed-closed QA changed stale implementation work: content=%q error=%v", content, err)
				}
			} else {
				if _, err := exec.Command("git", "--git-dir", filepath.Join(filepath.Dir(repo), "origin.git"), "show-ref", "--verify", "--quiet", "refs/heads/"+original.Branch).CombinedOutput(); err != nil {
					t.Fatalf("test setup lost the remote implementation branch: %v", err)
				}
				if _, err := exec.Command("git", "-C", repo, "show-ref", "--verify", "--quiet", "refs/heads/"+original.Branch).CombinedOutput(); err == nil {
					t.Fatal("failed-closed QA recreated the missing retained branch from its remote")
				}
			}
		})
	}
}

func TestAgentQARefreshesAndRequeuesWhenBaseMovedBeforeQA(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_base_before_qa", Title: "Retain implementation across base updates", Body: "Criteria", Repository: "owner/repo",
		Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer, Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID,
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "README.md"), []byte("candidate implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "add", "README.md")
	runGitTest(t, prepared.WorktreePath, "commit", "-m", "Candidate implementation")
	candidateCommit := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
	advanceRemoteBase(t, repo, "README.md", "new base implementation\n")

	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &reviewForbiddenRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != "warning" || runner.reviewCalls != 0 || project.status != "Ready" || project.phase != "ready" || project.pullRequest != "" {
		t.Fatalf("pre-QA base move was not requeued before review: results=%#v review_calls=%d status=%q phase=%q PR=%q", results, runner.reviewCalls, project.status, project.phase, project.pullRequest)
	}
	if !strings.Contains(results[0].Summary, "retained merge conflicts") || !strings.Contains(project.result, "merge conflicts") {
		t.Fatalf("pre-QA base move lost recovery context: result=%#v project_result=%q", results[0], project.result)
	}
	if status := runGitTest(t, prepared.WorktreePath, "status", "--porcelain"); !strings.Contains(status, "UU README.md") {
		t.Fatalf("conflicted candidate was not retained for implementation: %q", status)
	}
	if _, err := exec.Command("git", "-C", prepared.WorktreePath, "merge-base", "--is-ancestor", candidateCommit, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("base refresh discarded the candidate commit: %v", err)
	}
	refreshed, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatalf("refreshed conflicted workspace is not reusable: %v", err)
	}
	if refreshed.BaseRevision == prepared.BaseRevision {
		t.Fatalf("base refresh did not advance the authenticated identity: before=%s after=%s", prepared.BaseRevision, refreshed.BaseRevision)
	}
}

func TestImplementationRefreshesRetainedCandidateBeforeRunningAgent(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	workID := "assignment_" + safeRefComponent("PVTI_base_before_implementation")
	item := github.WorkItem{
		ID: "PVTI_base_before_implementation", Title: "Continue implementation on current base", Body: "Criteria", Repository: "owner/repo",
		Status: "Ready", Phase: "ready", Role: config.WorkRoleImplementer, Branch: "runner/" + workID,
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
		WorkID: workID, ItemID: item.ID,
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "README.md"), []byte("candidate implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "add", "README.md")
	runGitTest(t, prepared.WorktreePath, "commit", "-m", "Candidate implementation")
	candidateCommit := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
	advanceRemoteBase(t, repo, "README.md", "new base implementation\n")

	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &successfulImplementationRunner{project: project, inspect: func(worktree string) error {
		status := runGitTest(t, worktree, "status", "--porcelain")
		if !strings.Contains(status, "UU README.md") {
			return fmt.Errorf("implementer did not receive retained merge conflicts: %q", status)
		}
		if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("resolved candidate and base\n"), 0o644); err != nil {
			return err
		}
		runGitTest(t, worktree, "add", "README.md")
		return nil
	}}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || project.status != "Agent QA" || project.phase != "agent_qa" {
		t.Fatalf("implementation did not continue from refreshed candidate: results=%#v status=%q phase=%q", results, project.status, project.phase)
	}
	if _, err := exec.Command("git", "-C", prepared.WorktreePath, "merge-base", "--is-ancestor", candidateCommit, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("implementation refresh discarded the retained candidate: %v", err)
	}
	if _, err := exec.Command("git", "-C", prepared.WorktreePath, "merge-base", "--is-ancestor", "origin/main", "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("implementation did not retain the refreshed base: %v", err)
	}
}

func TestQAWorkspaceReusesLocalImplementationBranchBeforePublication(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_local", Repository: "owner/repo", Branch: "cortexium/local-only"}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4},
	}
	service, err := New(completeEngineTestConfig(cfg), subprocess.OSRunner{})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	_, err = workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatalf("prepare local-only implementation branch: %v", err)
	}
	workspace, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatalf("reuse local-only implementation branch: %v", err)
	}
	if workspace.BranchName != "cortexium/local-only" {
		t.Fatalf("QA workspace changed the implementation branch: %#v", workspace)
	}
}

func TestAgentQARefreshesAndRequeuesWhenBaseMovesBeforePublication(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_publication_base", Title: "Publish only reviewed base", Body: "Criteria", Repository: "owner/repo",
		Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer, Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID,
		DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("reviewed implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &baseMovingReviewer{project: project, moveBase: func() {
		advanceRemoteBase(t, repo, "base-moved-after-qa.txt", "new publication base\n")
	}}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != "warning" || runner.reviews != 1 || project.status != "Ready" || project.phase != "ready" || project.pullRequest != "" {
		t.Fatalf("base-moved publication was not requeued: results=%#v reviews=%d status=%q phase=%q PR=%q", results, runner.reviews, project.status, project.phase, project.pullRequest)
	}
	if !strings.Contains(results[0].Summary, "Base branch advanced") || len(results[0].WorkDone) != 1 || !strings.Contains(project.result, "Runner refreshed the retained candidate locally") {
		t.Fatalf("base refresh lost review or recovery context: result=%#v project_result=%q", results[0], project.result)
	}
	if _, err := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", "refs/heads/"+item.Branch).CombinedOutput(); err == nil {
		t.Fatal("base refresh pushed the unpublished implementation branch")
	}
	if _, err := os.Stat(prepared.WorktreePath); err != nil {
		t.Fatalf("base refresh removed recoverable work: %v", err)
	}
	refreshed, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatalf("refreshed workspace is not reusable: %v", err)
	}
	if ancestor, err := exec.Command("git", "-C", refreshed.WorktreePath, "merge-base", "--is-ancestor", refreshed.BaseRevision, "HEAD").CombinedOutput(); err != nil {
		t.Fatalf("refreshed candidate does not contain the new base: %v: %s", err, ancestor)
	}
}

func TestAgentQARejectionUsesConfiguredRetryAndExhaustedTransitions(t *testing.T) {
	for _, test := range []struct {
		name, wantStatus, wantPhase, wantOutcome, wantSummary string
		failures                                              int
		priorFeedback                                         bool
	}{
		{name: "first rejection", failures: 0, wantStatus: "Ready", wantPhase: "ready", wantOutcome: config.WorkflowOutcomeRejected, wantSummary: "rejection 1 of 3"},
		{name: "second rejection", failures: 1, priorFeedback: true, wantStatus: "Ready", wantPhase: "ready", wantOutcome: config.WorkflowOutcomeRejected, wantSummary: "rejection 2 of 3"},
		{name: "third rejection blocks", failures: 2, priorFeedback: true, wantStatus: "Blocked", wantPhase: "ready", wantOutcome: execution.OutcomeBlocked, wantSummary: "rejection 3 of 3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, _ := createPublicationRepository(t)
			item := github.WorkItem{
				ID: "PVTI_qa", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
				Branch: "cortexium/task", QAFailures: test.failures,
			}
			prepared, prepareErr := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"),
				WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID,
				DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
				BranchName: item.Branch, BaseRef: "origin/main",
			})
			if prepareErr != nil {
				t.Fatalf("prepare implementation workspace: %v", prepareErr)
			}
			item.Approval = testApproval(item)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaFailures: test.failures}
			prompts := []string{}
			cfg := config.Config{
				ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
				GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			}
			service, err := New(completeEngineTestConfig(cfg), reviewerRejectRunner{project: project, prompts: &prompts})
			if err != nil {
				t.Fatalf("configure service: %v", err)
			}
			if test.priorFeedback {
				var baseline *execution.ReviewBaseline
				if test.failures == 1 {
					spec := service.assignment(item, github.DelegatedContentFor(item), nil, nil).Spec
					baseline = &execution.ReviewBaseline{CommitOID: prepared.BaseRevision, BaseOID: prepared.BaseRevision, ContextDigest: reviewContextDigest(spec, nil)}
				}
				if err := service.saveReviewFeedback(item, github.DelegatedContentFor(item), execution.ReviewAssessment{
					Verdict: "needs_changes", Summary: "Preserve the previously corrected edge case.",
				}, baseline); err != nil {
					t.Fatalf("save prior QA feedback: %v", err)
				}
			}
			results, err := service.RunCycle(t.Context())
			if err != nil {
				t.Fatalf("run QA cycle: %v", err)
			}
			if len(results) != 1 || results[0].Outcome != test.wantOutcome || project.status != test.wantStatus || project.phase != test.wantPhase || project.qaFailures != test.failures+1 || !strings.Contains(results[0].Summary, test.wantSummary) {
				t.Fatalf("unexpected QA routing: results=%#v status=%q phase=%q failures=%d", results, project.status, project.phase, project.qaFailures)
			}
			if len(prompts) != 1 {
				t.Fatalf("reviewer prompt count = %d, want one", len(prompts))
			}
			if got := strings.Contains(prompts[0], "Follow-up review:"); got != (test.failures == 1) {
				t.Fatalf("baseline review scope mismatch: follow-up=%t failures=%d", got, test.failures)
			}
			if test.priorFeedback && (!strings.Contains(prompts[0], "Preserve the previously corrected edge case.") || !strings.Contains(prompts[0], "Verify their correction in the current candidate")) {
				t.Fatalf("reviewer did not receive prior QA feedback as historical evidence: %#v", prompts)
			}
			worktreePath := filepath.Join(filepath.Dir(repo), ".runner-worktrees", "assignment_"+safeRefComponent(item.ID))
			if _, err := os.Stat(worktreePath); err != nil {
				t.Fatalf("resumable %s task lost its worktree: %v", test.wantStatus, err)
			}
			if status := runGitTest(t, worktreePath, "status", "--porcelain", "--untracked-files=all"); status != "" {
				t.Fatalf("rejected QA did not retain a clean committed candidate: %q", status)
			}
			if head := strings.TrimSpace(runGitTest(t, worktreePath, "rev-parse", "HEAD")); head == strings.TrimSpace(runGitTest(t, worktreePath, "rev-parse", "origin/main")) {
				t.Fatal("rejected QA retained the worktree without its candidate commit")
			}
		})
	}
}

func TestAcceptedAgentQAPublishesPRAndMovesToHumanGate(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_accept", Title: "Implement accepted feature", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
		Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	preparedWorkspace, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatalf("prepare implementation worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(preparedWorkspace.WorktreePath, "feature.txt"), []byte("accepted implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	runner := &candidateInspectingReviewer{project: project}
	service, err := New(completeEngineTestConfig(cfg), runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run accepted QA cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || project.status != "PR Ready" || project.activity != "Awaiting human review" || project.pullRequest != "https://github.com/owner/repo/pull/12" || project.branch != item.Branch || project.qaCommit == "" {
		t.Fatalf("accepted QA did not reach the human gate: results=%#v status=%q activity=%q PR=%q branch=%q", results, project.status, project.activity, project.pullRequest, project.branch)
	}
	if runner.status != "" || runner.head == "" || runner.tree == "" || runner.head != project.qaCommit {
		t.Fatalf("QA did not receive the clean committed candidate: head=%q tree=%q status=%q qa_commit=%q", runner.head, runner.tree, runner.status, project.qaCommit)
	}
	recordPath := filepath.Join(filepath.Dir(preparedWorkspace.WorktreePath), ".runner-state", "publications", "v3", project.qaCommit+".json")
	recordContent, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("read accepted publication tuple: %v", err)
	}
	var record workspace.PublicationRecord
	if err := json.Unmarshal(recordContent, &record); err != nil {
		t.Fatalf("decode accepted publication tuple: %v", err)
	}
	if record.CommitOID != runner.head || record.TreeOID != runner.tree || record.ItemID != item.ID || record.Repository != item.Repository || record.DestinationRef != "refs/heads/"+item.Branch {
		t.Fatalf("accepted publication tuple does not match QA candidate: %#v", record)
	}
	if remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/"+item.Branch)); remoteHead == "" {
		t.Fatal("accepted implementation branch was not pushed")
	}
	if !results[0].WorktreeCleaned {
		t.Fatalf("accepted QA did not report worktree cleanup: %#v", results[0])
	}
	if _, err := os.Lstat(preparedWorkspace.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published worktree was retained: %v", err)
	}
	item.PullRequest = project.pullRequest
	item.QACommit = project.qaCommit
	reopened, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatalf("recreate published workspace for rework: %v", err)
	}
	if reopened.WorktreePath != preparedWorkspace.WorktreePath || reopened.BranchName != item.Branch {
		t.Fatalf("recreated workspace changed task identity: before=%#v after=%#v", preparedWorkspace, reopened)
	}
}

func TestAgentQAResumesExactAcceptanceWithoutAnotherReviewerRun(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_resume_acceptance", Title: "Resume accepted publication", Body: "Criteria", Repository: "owner/repo",
		Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	prepared, err := provider.Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatalf("prepare accepted candidate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "accepted.txt"), []byte("accepted once\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidateForMergeMethod(t.Context(), prepared, item.Title, config.MergeMethodMerge); err != nil {
		t.Fatalf("construct accepted candidate: %v", err)
	}
	accepted, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, prepared.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatalf("snapshot accepted candidate: %v", err)
	}
	if _, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted, "**Runner QA classification:** Accepted", "## Cortexium Runner Agent QA\n\n**Verdict:** Accepted"); err != nil {
		t.Fatalf("record accepted candidate: %v", err)
	}

	project := &fakeGitHubProjectRunner{
		itemsJSON:    `{"items":[` + projectItemJSON(item) + `]}`,
		qaCommit:     accepted.Head,
		baseRevision: prepared.BaseRevision,
	}
	runner := &resumedAcceptanceRunner{project: project}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir:    repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("resume accepted QA: %v", err)
	}
	if runner.reviewRuns != 0 || len(results) != 1 || !results[0].ResumedCheckpoint || results[0].Outcome != execution.OutcomeSucceeded ||
		project.status != "PR Ready" || project.pullRequest != "https://github.com/owner/repo/pull/12" {
		t.Fatalf("accepted publication was not resumed exactly: review_runs=%d results=%#v status=%q PR=%q", runner.reviewRuns, results, project.status, project.pullRequest)
	}
}

func TestOutOfDateManualPullRequestWaitsForHumanWithoutEagerRefresh(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	originalHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	runGitTest(t, repo, "checkout", "main")
	advanceRemoteBase(t, repo, "base.txt", "base update\n")
	item := github.WorkItem{
		ID: "PVTI_manual_pr", Title: "Implement", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: false,
		},
	}), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile manual pull request: %v", err)
	} else if changed {
		t.Fatal("manual pull request reconciliation reported an integration action")
	}
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	if project.status != "" || project.phase != "" || project.result != "" || remoteHead != originalHead {
		t.Fatalf("manual pull request was eagerly refreshed: status=%q phase=%q result=%q before=%s after=%s", project.status, project.phase, project.result, originalHead, remoteHead)
	}
}

func TestAcceptedAgentQAQueuesConfiguredAutomaticIntegration(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_auto_merge", Title: "Implement autonomous feature", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
		Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatalf("prepare implementation worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("autonomous implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true,
		},
	})
	runner := &reviewerAutoMergeRunner{project: project, baseRevision: prepared.BaseRevision}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run accepted QA cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || runner.mergeRequests != 0 || project.status != "PR Ready" || project.activity != config.RunnerActivityWaitingForCI {
		t.Fatalf("QA did not leave visible automatic integration to reconciliation: results=%#v requests=%d status=%q activity=%q", results, runner.mergeRequests, project.status, project.activity)
	}
	if !strings.Contains(results[0].Summary, "queued for automatic integration") {
		t.Fatalf("automatic integration result was unclear: %#v", results[0])
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load published item: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile queued automatic integration: %v", err)
	} else if !changed || runner.mergeRequests != 1 {
		t.Fatalf("queued integration did not acquire its resource: changed=%t requests=%d", changed, runner.mergeRequests)
	}
	if project.activity != config.RunnerActivityWaitingForMerge {
		t.Fatalf("ordinary integration reconciliation cleared visible waiting state: %q", project.activity)
	}
}

func TestAutomaticIntegrationRequeuesWhenBaseMovesAfterQA(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_auto_merge_base_move", Title: "Implement autonomous feature", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
		Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatalf("prepare implementation worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("autonomous implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true,
		},
	})
	runner := &reviewerAutoMergeRunner{project: project, baseRevision: prepared.BaseRevision}
	runner.beforePostPublicationFetch = func() {
		advanceRemoteBase(t, repo, "base-after-publication.txt", "new base\n")
	}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run accepted QA cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || runner.mergeRequests != 0 || !runner.postPublicationFetchSeen || project.status != "PR Ready" {
		t.Fatalf("QA publication did not queue moved-base integration: results=%#v requests=%d fetched=%t status=%q phase=%q", results, runner.mergeRequests, runner.postPublicationFetchSeen, project.status, project.phase)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load published item: %v", err)
	}
	if _, changed, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile moved-base integration: %v", err)
	} else if !changed || runner.mergeRequests != 0 || project.status != "Ready" || project.phase != "ready" || project.activity != "" {
		t.Fatalf("moved-base integration was not returned for implementation and QA: changed=%t requests=%d status=%q phase=%q activity=%q", changed, runner.mergeRequests, project.status, project.phase, project.activity)
	}
}

func TestAutomaticMergeFailureBlocksCardWithRetryGuidance(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_auto_merge_failure", Title: "Implement autonomous feature", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
		Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatalf("prepare implementation worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("autonomous implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", AutoMerge: true,
		},
	})
	runner := &reviewerAutoMergeRunner{project: project, baseRevision: prepared.BaseRevision, mergeErr: errors.New("exit status 1")}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}

	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run accepted QA cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || runner.mergeRequests != 0 || project.status != "PR Ready" {
		t.Fatalf("QA publication did not queue failed integration attempt: results=%#v requests=%d status=%q", results, runner.mergeRequests, project.status)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load published item: %v", err)
	}
	warnings, changed, err := service.reconcilePullRequests(t.Context(), items)
	if err != nil || !changed || len(warnings) != 1 || warnings[0].Outcome != execution.OutcomeBlocked || runner.mergeRequests != 1 || project.status != "Blocked" || project.phase != "agent_qa" || project.activity != "" {
		t.Fatalf("automatic merge failure was not retryable: warnings=%#v changed=%t error=%v requests=%d status=%q phase=%q activity=%q", warnings, changed, err, runner.mergeRequests, project.status, project.phase, project.activity)
	}
	if project.pullRequest == "" || !strings.Contains(project.result, "Automatic merge could not be enabled") || !strings.Contains(project.result, "retry this card") || strings.Contains(project.result, "repository auto-merge is disabled") {
		t.Fatalf("automatic merge failure lost safe PR guidance: PR=%q result=%q", project.pullRequest, project.result)
	}
}

func TestReconciliationCleansPublishedWorkspaceLeftByInterruptedCleanup(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{
		ID: "PVTI_cleanup", Title: "Clean published workspace", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	if _, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main", QuarantineMismatch: true,
	}); err != nil {
		t.Fatalf("bind interrupted workspace identity: %v", err)
	}
	prepared, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatalf("prepare stale published workspace: %v", err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatalf("load lifecycle items: %v", err)
	}
	if _, _, err := service.reconcilePullRequests(t.Context(), items); err != nil {
		t.Fatalf("reconcile published workspace cleanup: %v", err)
	}
	if _, err := os.Lstat(prepared.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale published worktree remains at %s: %v", prepared.WorktreePath, err)
	}
}

func TestWorkspaceCleanupRefreshesBoundAuthority(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "feature")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{
		ID: "PVTI_stale_cleanup", Title: "Reject stale cleanup", Body: "Criteria", Repository: "owner/repo", Status: "PR Ready",
		PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task", QACommit: "qa-head",
	}
	item.Approval = testApproval(item)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), openPullRequestRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main", QuarantineMismatch: true,
	}); err != nil {
		t.Fatal(err)
	}
	prepared, err := service.workspaceForItem(t.Context(), item, github.DelegatedContentFor(item).Digest, repo)
	if err != nil {
		t.Fatal(err)
	}
	action := mustAuthorizeTest(t, service.source, item)
	changed := item
	changed.Result = "Project action changed before cleanup"
	changed.Approval = testApproval(changed)
	project.itemsJSON = `{"items":[` + projectItemJSON(changed) + `]}`
	if _, err := service.cleanupAuthorizedItemWorkspace(t.Context(), action); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("stale cleanup authority was accepted: %v", err)
	}
	if _, err := service.cleanupAuthorizedItemWorkspace(t.Context(), github.AuthorizedAction{}); err == nil || !strings.Contains(err.Error(), "validated Runner authority") {
		t.Fatalf("zero-value cleanup authority was accepted: %v", err)
	}
	if _, err := os.Lstat(prepared.WorktreePath); err != nil {
		t.Fatalf("stale cleanup removed worktree %s: %v", prepared.WorktreePath, err)
	}
}

func TestWorkspaceCleanupFailureIsReportedWithoutFailingReconciliation(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_cleanup_warning", Title: "Cleanup warning", Repository: "owner/repo", Status: "Done", PullRequest: "https://github.com/owner/repo/pull/12", Branch: "cortexium/task"}
	item.Approval = testApproval(item)
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	service, err := New(cfg, openPullRequestRunner{project: project})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(service.implementationWorkspaceRoot(), "assignment_"+safeRefComponent(item.ID))
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	action := mustAuthorizeTest(t, service.source, item)
	if _, cleanupErr := service.cleanupAuthorizedItemWorkspace(t.Context(), action); cleanupErr == nil {
		t.Fatalf("unowned cleanup fixture did not fail: path=%s root=%s", path, service.implementationWorkspaceRoot())
	}
	warnings, _, err := service.reconcilePullRequests(t.Context(), []github.WorkItem{item})
	if err != nil || len(warnings) != 1 || warnings[0].Outcome != "warning" {
		t.Fatalf("cleanup warning stalled reconciliation: warnings=%#v error=%v", warnings, err)
	}
}

func TestAgentQAFailsClosedWhenWorkspaceChangesDuringReview(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_mutated_qa", Title: "Implement accepted feature", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa",
		Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	workspace, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatalf("prepare implementation worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace.WorktreePath, "feature.txt"), []byte("accepted implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := config.Config{
		ConfigVersion: config.ConfigVersion, RunnerID: "runner", ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}
	service, err := New(completeEngineTestConfig(cfg), mutatingReviewerAcceptRunner{project: project})
	if err != nil {
		t.Fatalf("configure service: %v", err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatalf("run QA cycle: %v", err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || project.status != "Blocked" || project.phase != "ready" || project.pullRequest != "" || !strings.Contains(results[0].Summary, "changed the private review workspace") {
		t.Fatalf("workspace mutation did not fail closed before publication: results=%#v status=%q PR=%q", results, project.status, project.pullRequest)
	}
	if !strings.Contains(results[0].Error, `"changed-during-qa.txt"`) {
		t.Fatalf("local error does not name the reviewer-changed path: %q", results[0].Error)
	}
	if strings.Contains(project.result, "changed-during-qa.txt") || strings.Contains(project.result, "Agent QA accepted the implementation") || !strings.Contains(project.result, "workspace integrity violation") {
		t.Fatalf("Project result exposed local integrity or reviewer diagnostics: %q", project.result)
	}
}

func TestAgentQAControlStateDriftBlocksCommitPushPRAndCleanup(t *testing.T) {
	for _, name := range []string{"index flag", "common config", "ignored control file", "submodule"} {
		t.Run(name, func(t *testing.T) {
			repo, remote := createPublicationRepository(t)
			var mutate func(string) error
			switch name {
			case "index flag":
				mutate = func(worktree string) error {
					return runGitMutation(worktree, "update-index", "--assume-unchanged", "README.md")
				}
			case "common config":
				mutate = func(worktree string) error {
					return runGitMutation(worktree, "config", "snapshot.qa-mutated", "true")
				}
			case "ignored control file":
				if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("concealed/\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, repo, "add", ".gitignore")
				runGitTest(t, repo, "commit", "-m", "ignore concealed files")
				runGitTest(t, repo, "push", "origin", "main")
				mutate = func(worktree string) error {
					path := filepath.Join(worktree, "concealed", "deep", ".gitattributes")
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("*.key -diff\n"), 0o644)
				}
			case "submodule":
				source := filepath.Join(t.TempDir(), "source")
				runGitTest(t, "", "init", "-b", "main", source)
				runGitTest(t, source, "config", "user.name", "Test User")
				runGitTest(t, source, "config", "user.email", "test@example.com")
				if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("submodule\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				runGitTest(t, source, "add", "README.md")
				runGitTest(t, source, "commit", "-m", "initial submodule")
				runGitTest(t, repo, "-c", "protocol.file.allow=always", "submodule", "add", source, "module")
				runGitTest(t, repo, "commit", "-am", "add submodule")
				runGitTest(t, repo, "push", "origin", "main")
				mutate = func(worktree string) error {
					path := filepath.Join(worktree, "module", "concealed", "secret.txt")
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("hidden QA mutation\n"), 0o644)
				}
			}

			item := github.WorkItem{
				ID: "PVTI_integrity_" + strings.ReplaceAll(name, " ", "_"), Title: "Publish reviewed state", Body: "Criteria", Repository: "owner/repo",
				Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer, Branch: "cortexium/task",
			}
			item.Approval = testApproval(item)
			prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
				WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID),
				ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
				BranchName: item.Branch, BaseRef: "origin/main",
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("reviewed implementation\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
			runner := &integrityMutatingReviewer{project: project, mutate: mutate}
			service, err := New(completeEngineTestConfig(config.Config{
				ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
			}), runner)
			if err != nil {
				t.Fatal(err)
			}

			results, err := service.RunCycle(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || project.status != "Blocked" || project.pullRequest != "" {
				t.Fatalf("%s drift did not stop at the integrity boundary: results=%#v status=%q phase=%q PR=%q", name, results, project.status, project.phase, project.pullRequest)
			}
			if len(runner.privileged) != 0 {
				t.Fatalf("%s drift reached privileged follow-on operations: %#v", name, runner.privileged)
			}
			if _, err := exec.Command("git", "--git-dir", remote, "show-ref", "--verify", "--quiet", "refs/heads/"+item.Branch).CombinedOutput(); err == nil {
				t.Fatalf("%s drift pushed the task branch", name)
			}
			if _, err := os.Stat(prepared.WorktreePath); err != nil {
				t.Fatalf("%s drift allowed cleanup to remove recoverable work: %v", name, err)
			}
		})
	}
}

func TestAgentQAAllowsConcurrentBranchTrackingConfigChange(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_parallel_branch_config", Title: "Publish through concurrent Git administration", Body: "Criteria", Repository: "owner/repo",
		Status: "Agent QA", Phase: "agent_qa", Role: config.WorkRoleReviewer, Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("reviewed implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	runner := &integrityMutatingReviewer{project: project, mutate: func(worktree string) error {
		if err := runGitMutation(worktree, "config", "branch.unrelated.remote", "origin"); err != nil {
			return err
		}
		return runGitMutation(worktree, "config", "branch.unrelated.merge", "refs/heads/unrelated")
	}}
	service, err := New(completeEngineTestConfig(config.Config{
		ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"},
	}), runner)
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || project.status != "PR Ready" || project.pullRequest == "" {
		t.Fatalf("concurrent branch tracking change blocked QA publication: results=%#v status=%q PR=%q", results, project.status, project.pullRequest)
	}
}

func runGitMutation(dir string, args ...string) error {
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func TestAgentQAFailsClosedWhenActiveCheckoutChangesDuringReview(t *testing.T) {
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_mutated_source", Title: "Review safely", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/task",
	}
	item.Approval = testApproval(item)
	root := filepath.Join(filepath.Dir(repo), ".runner-worktrees")
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: root, WorkID: "assignment_" + safeRefComponent(item.ID), BranchName: item.Branch, BaseRef: "origin/main",
		ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, activeCheckoutMutatingReviewer{project: project, active: repo})
	if err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Outcome != execution.OutcomeBlocked || project.phase != "ready" || !strings.Contains(results[0].Summary, "active project checkout") || project.pullRequest != "" {
		t.Fatalf("active checkout mutation was not blocked: %#v", results)
	}
	if !strings.Contains(results[0].Error, `"qa-side-effect.txt"`) {
		t.Fatalf("local error does not name the active-checkout path changed during review: %q", results[0].Error)
	}
	if strings.Contains(project.result, "qa-side-effect.txt") || !strings.Contains(project.result, "workspace integrity violation") {
		t.Fatalf("Project result exposed the active-checkout diagnostic: %q", project.result)
	}
}

func TestWorkspaceForPullRequestFastForwardsToLatestRemoteHead(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	runGitTest(t, repo, "checkout", "-b", "cortexium/task")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repo, "add", "--all")
	runGitTest(t, repo, "commit", "-m", "first")
	runGitTest(t, repo, "push", "-u", "origin", "cortexium/task")
	runGitTest(t, repo, "checkout", "main")
	item := github.WorkItem{ID: "PVTI_remote_sync", Repository: "owner/repo", Branch: "cortexium/task", PullRequest: "https://github.com/owner/repo/pull/12"}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	service, err := New(cfg, subprocess.OSRunner{})
	if err != nil {
		t.Fatal(err)
	}
	digest := github.DelegatedContentFor(item).Digest
	prepared, err := workspace.NewGitProvider(subprocess.OSRunner{}).Prepare(t.Context(), workspace.Request{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main", QuarantineMismatch: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewGitProvider(service.run).Cleanup(t.Context(), workspace.CleanupRequest{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(),
		WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID, DelegatedContentDigest: digest,
		Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main",
	}); err != nil {
		t.Fatal(err)
	}
	updater := filepath.Join(t.TempDir(), "updater")
	runGitTest(t, "", "clone", remote, updater)
	runGitTest(t, updater, "checkout", "cortexium/task")
	runGitTest(t, updater, "config", "user.name", "Test User")
	runGitTest(t, updater, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(updater, "feature.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, updater, "add", "--all")
	runGitTest(t, updater, "commit", "-m", "second")
	runGitTest(t, updater, "push", "origin", "cortexium/task")
	prepared, err = service.workspaceForItem(t.Context(), item, digest, repo)
	if err != nil {
		t.Fatal(err)
	}
	localHead := strings.TrimSpace(runGitTest(t, prepared.WorktreePath, "rev-parse", "HEAD"))
	remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/cortexium/task"))
	if localHead != remoteHead {
		t.Fatalf("workspace head %s is stale; remote is %s", localHead, remoteHead)
	}
}

func TestWorkspaceForTrackedRebasePullRequestKeepsExactLocalRewriteForQA(t *testing.T) {
	repo, remote := createPublicationRepository(t)
	item := github.WorkItem{
		ID: "PVTI_rebase_rework", Title: "Correct the published implementation", Body: "Criteria", Repository: "owner/repo",
		Branch: "cortexium/task", PullRequest: "https://github.com/owner/repo/pull/12",
	}
	digest := github.DelegatedContentFor(item).Digest
	cfg := completeEngineTestConfig(config.Config{
		ProjectDir: repo,
		GitHubProject: &config.GitHubProjectConfig{
			Owner: "owner", Number: 4, IntakeRepository: "owner/repo", MergeMethod: config.MergeMethodRebase,
		},
	})
	testRunner := reviewerAcceptRunner{project: &fakeGitHubProjectRunner{}}
	service, err := New(cfg, testRunner)
	if err != nil {
		t.Fatal(err)
	}
	request := workspace.Request{
		WorkingDir: repo, WorktreeRoot: service.implementationWorkspaceRoot(), WorkID: "assignment_" + safeRefComponent(item.ID),
		ItemID: item.ID, DelegatedContentDigest: digest, Repository: item.Repository,
		BranchName: item.Branch, BaseRef: "origin/main",
	}
	provider := workspace.NewGitProvider(testRunner)
	prepared, err := provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("published implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	published, err := provider.ConstructCandidateForMergeMethod(t.Context(), prepared, item.Title, config.MergeMethodRebase)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), testRunner, prepared.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.RecordPublicationAcceptance(t.Context(), prepared, accepted, "QA accepted.", "Accepted candidate."); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, prepared.WorktreePath, "push", "origin", published.CommitOID+":refs/heads/"+item.Branch)
	item.QACommit = published.CommitOID

	advanceRemoteBase(t, repo, "base.txt", "new base\n")
	refresh, err := provider.RefreshBaseForMergeMethod(t.Context(), prepared, "origin", "main", config.MergeMethodRebase)
	if err != nil || !refresh.Updated || refresh.Conflicted {
		t.Fatalf("refresh published candidate: %#v, error=%v", refresh, err)
	}
	prepared, err = provider.Prepare(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared.WorktreePath, "feature.txt"), []byte("corrected implementation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	corrected, err := provider.ConstructCandidateForMergeMethod(t.Context(), prepared, item.Title, config.MergeMethodRebase)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.Command("git", "-C", prepared.WorktreePath, "merge-base", "--is-ancestor", published.CommitOID, corrected.CommitOID).CombinedOutput(); err == nil {
		t.Fatal("test setup did not create the expected rebase-history divergence")
	}

	reused, err := service.workspaceForItem(t.Context(), item, digest, repo)
	if err != nil {
		t.Fatalf("reuse exact Runner-owned rebase rewrite for QA: %v", err)
	}
	if localHead := strings.TrimSpace(runGitTest(t, reused.WorktreePath, "rev-parse", "HEAD")); localHead != corrected.CommitOID {
		t.Fatalf("QA workspace head = %s, want corrected local candidate %s", localHead, corrected.CommitOID)
	}
	if remoteHead := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/"+item.Branch)); remoteHead != published.CommitOID {
		t.Fatalf("QA preflight changed remote head = %s, want prior accepted commit %s", remoteHead, published.CommitOID)
	}

	staleProjectLease := item
	staleProjectLease.QACommit = strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/main"))
	if _, err := service.workspaceForItem(t.Context(), staleProjectLease, digest, repo); err != nil {
		t.Fatalf("private publication acceptance did not recover the stale Project QA commit: %v", err)
	}
	publicationPath := filepath.Join(filepath.Dir(prepared.WorktreePath), ".runner-state", "publications", "v3", published.CommitOID+".json")
	if err := os.Remove(publicationPath); err != nil {
		t.Fatal(err)
	}
	if _, err := service.workspaceForItem(t.Context(), staleProjectLease, digest, repo); err == nil || !strings.Contains(err.Error(), "have diverged") {
		t.Fatalf("rebase rewrite without Project or private lease authority was accepted: %v", err)
	}

	mergeConfig := cfg
	mergeProject := *cfg.GitHubProject
	mergeProject.MergeMethod = config.MergeMethodMerge
	mergeConfig.GitHubProject = &mergeProject
	mergeService, err := New(mergeConfig, testRunner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mergeService.workspaceForItem(t.Context(), item, digest, repo); err == nil || !strings.Contains(err.Error(), "have diverged") {
		t.Fatalf("merge-mode divergence was accepted as a rebase rewrite: %v", err)
	}
}

func completeEngineTestConfig(cfg config.Config) config.Config {
	if cfg.ConfigVersion == 0 {
		cfg.ConfigVersion = config.ConfigVersion
	}
	if strings.TrimSpace(cfg.RunnerID) == "" {
		cfg.RunnerID = "runner_test"
	}
	if strings.TrimSpace(cfg.ProjectDir) == "" {
		cfg.ProjectDir = "/project"
	}
	if cfg.MaxParallelism == 0 {
		cfg.MaxParallelism = 1
	}
	if len(cfg.Roles) == 0 {
		cfg.Roles = config.RoleTemplate(config.HarnessCodexCLI)
	}
	if cfg.Workflow == nil {
		workflow := config.WorkflowTemplate(true)
		cfg.Workflow = &workflow
	}
	if cfg.GitHubProject == nil {
		cfg.GitHubProject = &config.GitHubProjectConfig{Owner: "owner", Number: 4}
	}
	project := cfg.GitHubProject
	if strings.TrimSpace(project.IntakeRepository) == "" {
		project.IntakeRepository = "owner/repo"
	}
	if strings.TrimSpace(project.IntakeLabel) == "" {
		project.IntakeLabel = "needs-assessment"
	}
	if strings.TrimSpace(project.MergeMethod) == "" {
		project.MergeMethod = config.MergeMethodMerge
	}
	for destination, value := range map[*string]string{
		&project.ResultField: "Runner Result", &project.ApprovalField: "Runner Approval", &project.PhaseField: "Runner Phase",
		&project.TransitionField: config.RunnerTransitionFieldName,
		&project.QAFailuresField: "QA Failures", &project.BranchField: "Runner Branch", &project.PullRequestField: "Pull Request",
		&project.QACommitField: "QA Commit", &project.BaseBranch: "main", &project.RemoteName: "origin",
	} {
		if strings.TrimSpace(*destination) == "" {
			*destination = value
		}
	}
	neededHarnesses := map[string]bool{}
	for _, role := range cfg.Roles {
		if strings.TrimSpace(role.Harness) != "" {
			neededHarnesses[strings.TrimSpace(role.Harness)] = true
		}
	}
	configured := map[string]bool{}
	for index := range cfg.Harnesses {
		harness := &cfg.Harnesses[index]
		configured[harness.Kind] = true
		if harness.Enabled == nil {
			enabled := true
			harness.Enabled = &enabled
		}
		if strings.TrimSpace(harness.Command) == "" {
			harness.Command = map[string]string{config.HarnessCodexCLI: "codex", config.HarnessClaudeCLI: "claude", config.HarnessPiCLI: "pi"}[harness.Kind]
		}
		if strings.TrimSpace(harness.WorkspaceWriteRoot) == "" {
			harness.WorkspaceWriteRoot = filepath.Join(filepath.Dir(cfg.ProjectDir), ".runner-worktrees")
		}
	}
	for kind := range neededHarnesses {
		if configured[kind] {
			continue
		}
		enabled := true
		cfg.Harnesses = append(cfg.Harnesses, config.HarnessConfig{
			Kind: kind, Command: map[string]string{config.HarnessCodexCLI: "codex", config.HarnessClaudeCLI: "claude", config.HarnessPiCLI: "pi"}[kind], Enabled: &enabled,
			WorkspaceWriteRoot: filepath.Join(filepath.Dir(cfg.ProjectDir), ".runner-worktrees"),
		})
	}
	return cfg
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
