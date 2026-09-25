package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

func preparedCardGate(t *testing.T, script string) (*Engine, github.AuthorizedAction, workspace.Metadata, workspace.Candidate, *resumedAcceptanceRunner) {
	t.Helper()
	repo, _ := createPublicationRepository(t)
	item := github.WorkItem{ID: "PVTI_card_gate", Title: "Verify accepted card", Body: "Criteria", Repository: "owner/repo", Status: "Agent QA", Phase: "agent_qa", Branch: "cortexium/task"}
	item.Approval = testApproval(item)
	provider := workspace.NewGitProvider(subprocess.OSRunner{})
	metadata, err := provider.Prepare(t.Context(), workspace.Request{WorkingDir: repo, WorktreeRoot: filepath.Join(filepath.Dir(repo), ".runner-worktrees"), WorkID: "assignment_" + safeRefComponent(item.ID), ItemID: item.ID, DelegatedContentDigest: github.DelegatedContentFor(item).Digest, Repository: item.Repository, BranchName: item.Branch, BaseRef: "origin/main"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "accepted.txt"), []byte("accepted once\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ConstructCandidate(t.Context(), metadata, item.Title); err != nil {
		t.Fatal(err)
	}
	snapshot, err := workspace.CaptureCheckoutSnapshotStateWithLimits(t.Context(), subprocess.OSRunner{}, metadata.WorktreePath, 30*time.Second, workspace.DefaultSnapshotLimits())
	if err != nil {
		t.Fatal(err)
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + projectItemJSON(item) + `]}`, qaCommit: snapshot.Head, baseRevision: metadata.BaseRevision}
	runner := &resumedAcceptanceRunner{project: project}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/repo"}})
	cfg.CardVerification = &config.CardVerificationConfig{Entrypoint: "browser", Access: config.RoleAccessHost}
	cfg.Verification = map[string]config.VerificationEntrypoint{"browser": {Command: "/bin/sh", Args: []string{"-c", script}, ToolchainCommands: []string{"/bin/sh"}, InputPaths: []string{"accepted.txt"}, TimeoutSeconds: 30, RequireCurrentCandidate: true}}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	action, err := service.source.Authorize(t.Context(), item)
	if err != nil {
		t.Fatal(err)
	}
	return service, action, metadata, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}, runner
}

func TestCardVerificationRunsAndReusesOnlyMatchingReviewedSource(t *testing.T) {
	service, action, metadata, record, _ := preparedCardGate(t, `read -r value < accepted.txt; test "$value" = 'accepted once'`)
	guard, cleanup, observed, err := service.prepareCardVerification(t.Context(), action, metadata, record, "card-gate-test")
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if guard == nil || observed.Receipt == nil || observed.Receipt.SourceCommitOID != record.CommitOID {
		t.Fatal("missing candidate-bound gate result")
	}
	first, err := service.loadCardVerification(action.Item.ID)
	if err != nil || first == nil || first.Result.Receipt == nil || first.Result.Receipt.Outcome != "passed" || first.Result.Historical {
		t.Fatalf("fresh proof: %+v %v", first, err)
	}
	if err := guard(t.Context(), action); err != nil {
		t.Fatal(err)
	}
	_, cleanupAgain, _, err := service.prepareCardVerification(t.Context(), action, metadata, record, "card-gate-test")
	defer cleanupAgain()
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.loadCardVerification(action.Item.ID)
	if err != nil || !second.Result.Historical || second.Result.Receipt.ExecutionID != first.Result.Receipt.ExecutionID {
		t.Fatalf("publication recovery reran unchanged command: %+v %v", second, err)
	}
	changedAuthority := action
	changedAuthority.Item.Branch = "other-destination"
	if err := guard(t.Context(), changedAuthority); err == nil {
		t.Fatal("publication accepted a changed destination")
	}
	entry := service.cfg.Verification["browser"]
	entry.Args = []string{"-c", "exit 8"}
	service.cfg.Verification["browser"] = entry
	_, changedCleanup, _, changedErr := service.prepareCardVerification(t.Context(), action, metadata, record, "changed-settings")
	defer changedCleanup()
	if changedErr == nil {
		t.Fatal("changed command reused old passing evidence")
	}
	changed, err := service.loadCardVerification(action.Item.ID)
	if err != nil || changed == nil || changed.Result.Receipt == nil || changed.Result.Receipt.ExitCode == nil || *changed.Result.Receipt.ExitCode != 8 || changed.Result.Historical {
		t.Fatalf("changed settings did not execute the new command: %+v %v", changed, err)
	}
	if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "accepted.txt"), []byte("changed after review\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := guard(t.Context(), action); err == nil {
		t.Fatal("publication accepted changed source after verification")
	}
}

func TestCardVerificationFailureBlocksPublicationWithoutRepeatingQA(t *testing.T) {
	service, action, _, _, runner := preparedCardGate(t, `echo 'browser assertion failed' >&2; exit 7`)
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 {
		t.Fatalf("cycle: %+v %v", results, err)
	}
	if results[0].Outcome != execution.OutcomeBlocked || runner.project.pullRequest != "" || runner.reviewRuns != 0 || runner.project.qaFailures != 0 {
		t.Fatalf("failed gate was published or repeated review: %+v", results)
	}
	stored, err := service.loadCardVerification(action.Item.ID)
	if err != nil || stored == nil || stored.Result.Receipt == nil || stored.Result.Receipt.ExitCode == nil || *stored.Result.Receipt.ExitCode != 7 || !strings.Contains(stored.Result.Output.Stderr, "browser assertion failed") {
		t.Fatalf("missing native failure: %+v %v", stored, err)
	}
}

func TestCardVerificationPassPrecedesPublication(t *testing.T) {
	service, action, metadata, _, runner := preparedCardGate(t, `read -r value < accepted.txt; test "$value" = 'accepted once'`)
	snapshot, err := service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.NewGitProvider(subprocess.OSRunner{}).RecordPublicationAcceptance(t.Context(), metadata, snapshot, "QA accepted.", "Accepted source."); err != nil {
		t.Fatal(err)
	}
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 {
		t.Fatalf("cycle: %+v %v", results, err)
	}
	if results[0].Outcome != execution.OutcomeSucceeded || runner.project.pullRequest == "" || runner.reviewRuns != 0 {
		t.Fatalf("gate did not publish accepted source: %+v", results)
	}
	stored, err := service.loadCardVerification(action.Item.ID)
	if err != nil || stored == nil || stored.Result.Receipt.Outcome != "passed" {
		t.Fatalf("publication lacks protected native proof: %+v %v", stored, err)
	}
}

// This harness refuses QA unless the real configured command has already passed
// and its native evidence is present in the reviewer prompt.
type cardGateReviewer struct {
	service *Engine
	project *fakeGitHubProjectRunner
	itemID  string
	calls   int
}

func (r *cardGateReviewer) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *cardGateReviewer) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "codex" {
		r.calls++
		evidence, err := r.service.loadCardVerification(r.itemID)
		if err != nil || evidence == nil || evidence.Result.Receipt == nil || evidence.Result.Receipt.Outcome != "passed" || !strings.Contains(args[len(args)-1], "Runner-observed configured card verification") {
			return subprocess.Result{}, fmt.Errorf("QA started without native passing evidence: %v", err)
		}
	}
	return (reviewerAcceptRunner{project: r.project}).Run(ctx, command, args, dir, timeout)
}

func TestCardVerificationPassIsAvailableBeforeFreshQA(t *testing.T) {
	service, action, _, _, runner := preparedCardGate(t, `read -r value < accepted.txt; test "$value" = 'accepted once'`)
	reviewer := &cardGateReviewer{service: service, project: runner.project, itemID: action.Item.ID}
	service.run = reviewer
	results, err := service.RunCycle(t.Context())
	if err != nil || len(results) != 1 || results[0].Outcome != execution.OutcomeSucceeded || reviewer.calls != 1 {
		t.Fatalf("fresh QA lacked a passing gate: calls=%d results=%+v err=%v", reviewer.calls, results, err)
	}
	stored, err := service.loadCardVerification(action.Item.ID)
	if err != nil || stored == nil || !stored.Result.Historical {
		t.Fatalf("publication failed to reuse the pre-QA execution: %+v %v", stored, err)
	}
}
