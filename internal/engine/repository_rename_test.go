package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/config"
	"github.com/cortexium-io/runner/internal/execution"
	"github.com/cortexium-io/runner/internal/github"
	"github.com/cortexium-io/runner/internal/subprocess"
	"github.com/cortexium-io/runner/internal/workspace"
)

type renameTestRunner struct {
	project      *fakeGitHubProjectRunner
	differentID  bool
	missingID    bool
	canonical    string
	failAt       int
	writes       int
	afterWrite   bool
	observe      func()
	remoteBranch bool
}

func (r *renameTestRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	joined := strings.Join(args, " ")
	if command == "git" {
		if r.remoteBranch && strings.Contains(joined, "ls-remote") {
			return subprocess.Result{Stdout: strings.Repeat("a", 40) + "\trefs/heads/runner/task\n"}, nil
		}
		return runEngineTestGit(ctx, args, dir, timeout)
	}
	if strings.HasPrefix(joined, "api repos/") {
		identity := github.RepositoryIdentity{ID: 1234, NodeID: "R_same_repository", FullName: "owner/repo"}
		if r.differentID && args[1] == "repos/owner/old" {
			identity.ID++
		}
		if r.missingID {
			identity.NodeID = ""
		}
		if r.canonical != "" {
			identity.FullName = r.canonical
		}
		encoded, _ := json.Marshal(identity)
		return subprocess.Result{Stdout: string(encoded)}, nil
	}
	if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
		return subprocess.Result{Stdout: "[]"}, nil
	}
	issueBody := len(args) >= 3 && args[0] == "issue" && args[1] == "edit" && argumentValue(args, "--body") != ""
	write := issueBody || strings.Contains(joined, "project item-edit") || isBatchProjectUpdateCall(joined)
	fail := false
	if write {
		r.writes++
		fail = r.failAt == r.writes
		if fail && !r.afterWrite {
			return subprocess.Result{}, errors.New("injected rename interruption")
		}
	}
	var result subprocess.Result
	var err error
	if issueBody {
		for i := range r.project.remoteItems {
			if r.project.remoteItems[i].URL == args[2] {
				r.project.remoteItems[i].Body = argumentValue(args, "--body")
			}
		}
	} else {
		result, err = r.project.Run(ctx, command, args, dir, timeout)
	}
	if write && err == nil && r.observe != nil {
		r.observe()
	}
	if fail && err == nil {
		return subprocess.Result{}, errors.New("injected lost rename response")
	}
	return result, err
}

func repositoryRenameFixture(t *testing.T, retained bool) (*Engine, *renameTestRunner, workspace.Metadata) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	repo, _ := createPublicationRepository(t)
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, GitHubProject: &config.GitHubProjectConfig{Owner: "owner", Number: 4, IntakeRepository: "owner/old"}})
	source := github.WorkItem{ID: "PVTI_parent", Title: "Plan", Body: "Approved unchanged planning request", Repository: "owner/old", URL: "https://github.com/owner/old/issues/1", Status: "In Progress", Phase: "plan", Role: config.WorkRolePlanner, IssueState: "OPEN"}
	source.Approval = testApproval(source)
	encoded := []string{projectItemJSON(source)}
	for i := 1; i <= 4; i++ {
		planned := github.PlannedItem{Title: fmt.Sprintf("Child %d", i), Repository: "owner/old", Summary: "Preserve the exact requirements mentioning owner/old.",
			AcceptanceCriteria: []string{"The original behavior works."}, Verification: []string{"Run the owning test."}, PlanningSourceID: source.ID,
			PlanningSourceLane: "plan", PlanningSourceFingerprint: github.PlanningSourceFingerprint(source), PlanningDestination: "Ready",
			PlanningBatchFingerprint: "v1:original-planning-batch", PlanningBatchSize: 4, PlanningItemIndex: i, DependencyIDsResolved: true}
		if i > 2 {
			planned.ResolvedDependencies = []github.PlannedDependency{{ItemID: fmt.Sprintf("PVTI_child_%d", i-2), Title: fmt.Sprintf("Child %d", i-2)}}
		}
		child := github.WorkItem{ID: fmt.Sprintf("PVTI_child_%d", i), Title: planned.Title, Body: github.FormatPlannedItemBody(planned), Repository: "owner/old", URL: fmt.Sprintf("https://github.com/owner/old/issues/%d", i+1), Status: "Needs assessment", IssueState: "OPEN"}
		encoded = append(encoded, projectItemJSON(child))
	}
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[` + strings.Join(encoded, ",") + `]}`}
	runner := &renameTestRunner{project: project}
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.source.StagePlanningApproval(t.Context(), mustAuthorizeTest(t, service.source, source), items[1:], "Exact released plan"); err != nil {
		t.Fatal(err)
	}
	approval, err := service.PlanProjectItemApproval(t.Context(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyProjectItemApproval(t.Context(), approval); err != nil {
		t.Fatal(err)
	}
	var metadata workspace.Metadata
	if retained {
		items, err = service.source.LifecycleItems(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		child := items[1]
		metadata, err = workspace.NewGitProvider(runner).Prepare(t.Context(), service.workspaceRequestForItem(child, github.DelegatedContentFor(child).Digest, repo, false))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("preserve dirty candidate bytes\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		snapshot, err := service.checkoutSnapshotState(t.Context(), metadata.WorktreePath)
		if err != nil {
			t.Fatal(err)
		}
		candidate := workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}
		if err := service.saveImplementationCheckpoint(child, github.DelegatedContentFor(child), "original-context", metadata, snapshot, candidate,
			execution.Output{Summary: "Retained previous implementation", Outcome: execution.OutcomeSucceeded, WorkDone: []string{"Original work"}, Verification: []string{"Original evidence"}}, time.Now().Add(time.Hour).UTC(), true); err != nil {
			t.Fatal(err)
		}
		if err := service.saveVerificationEvidence(child, github.DelegatedContentFor(child), metadata, candidate, []string{"Run the owning test."}, []string{"Historical evidence"}); err != nil {
			t.Fatal(err)
		}
		if err := service.saveReviewFeedback(child, github.DelegatedContentFor(child), execution.ReviewAssessment{Verdict: "needs_changes", Summary: "Fix the real activation regression; keyboard input must work."}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate GitHub's administrative rename and normal authority quarantine.
	for i := range project.remoteItems {
		item := &project.remoteItems[i]
		item.Repository = "owner/repo"
		item.URL = strings.Replace(item.URL, "owner/old", "owner/repo", 1)
		if i > 0 {
			item.Status, item.Approval = "Needs assessment", ""
			if i == 1 && retained {
				item.Phase, item.QAFailures = "ready", 2
				// Deliberately no Project branch: the private identity owns it.
			}
		}
	}
	service.cfg.GitHubProject.IntakeRepository = "owner/repo"
	service.source, err = github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, runner)
	if err != nil {
		t.Fatal(err)
	}
	runner.writes = 0
	return service, runner, metadata
}

func TestRepositoryRenameRecoversExactFourCardBatchAndDirtyUnpublishedWork(t *testing.T) {
	service, runner, metadata := repositoryRenameFixture(t, true)
	before := append([]github.WorkItem(nil), runner.project.remoteItems...)
	feedbackBefore, err := os.ReadFile(service.reviewFeedbackPath(before[1].ID))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := service.PlanRepositoryRename(t.Context(), before[2].ID, "owner/old")
	if err != nil {
		t.Fatal(err)
	}
	if runner.writes != 0 || !reflect.DeepEqual(before, runner.project.remoteItems) {
		t.Fatal("preview wrote state")
	}
	if len(plan.Workspaces) != 1 || plan.Workspaces[0].Snapshot.Clean || plan.Workspaces[0].Before != metadata.Identity {
		t.Fatal("preview failed to identify exact dirty retained work without a Project branch")
	}
	if _, err := os.Stat(plan.Journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preview persisted a journal")
	}
	if err := service.ApplyRepositoryRename(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range items[1:] {
		original := plan.Batch.Children[i].Before
		if item.ID != original.ID || item.QAFailures != original.QAFailures || item.Branch != original.Branch || item.Status != "Ready" || item.Transition != "" || item.PullRequest != "" || item.QACommit != "" || !reflect.DeepEqual(item.Dependencies, original.Dependencies) {
			t.Fatalf("recovery changed work or granted delivery: %+v", item)
		}
		if !strings.Contains(item.Body, "requirements mentioning owner/old") {
			t.Fatal("recovery rewrote requirement text")
		}
		if item.Body != original.Body || item.PlanningSourceFingerprint != original.PlanningSourceFingerprint {
			t.Fatal("recovery rewrote original planning provenance")
		}
		if _, err := service.source.Authorize(t.Context(), item); err != nil {
			t.Fatal(err)
		}
	}
	ready, err := service.source.ReadyItems(t.Context(), items, 10)
	if err != nil || len(ready) != 2 {
		t.Fatalf("dependencies no longer gate admission: ready=%d error=%v", len(ready), err)
	}
	approval, err := service.PlanProjectItemApproval(t.Context(), plan.Batch.Source.ID)
	if err != nil || approval.Batch == nil || !approval.Batch.Released {
		t.Fatalf("ordinary inspection lost authenticated complete-batch release: %v", err)
	}
	retained, err := workspace.NewGitProvider(runner).ValidateRetainedIdentity(t.Context(), service.workspaceRequestForItem(items[1], github.DelegatedContentFor(items[1]).Digest, service.cfg.ProjectDir, false))
	if err != nil || retained != plan.Workspaces[0].After {
		t.Fatalf("retained workspace cannot resume: %+v %v", retained, err)
	}
	data, err := os.ReadFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"))
	if err != nil || string(data) != "preserve dirty candidate bytes\n" {
		t.Fatal("recovery lost dirty bytes")
	}
	journal, err := os.ReadFile(plan.Journal)
	if err != nil {
		t.Fatal(err)
	}
	var saved RepositoryRenameRecovery
	if err := json.Unmarshal(journal, &saved); err != nil {
		t.Fatal(err)
	}
	foundFeedback := false
	for _, record := range saved.History {
		if record.Path == service.reviewFeedbackPath(before[1].ID) {
			foundFeedback = bytes.Equal(record.Before, feedbackBefore)
		}
	}
	if !foundFeedback {
		t.Fatal("original feedback history was not preserved byte-exact")
	}
	feedback, err := service.loadReviewFeedbackRecord(items[1], github.DelegatedContentFor(items[1]))
	if err != nil || feedback == nil || feedback.Baseline != nil || len(feedback.Items) == 0 {
		t.Fatalf("actionable defects were lost or old acceptance carried forward: %+v %v", feedback, err)
	}
	metadata.Identity = retained
	snapshot, err := service.workspaceSnapshotState(t.Context(), metadata.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, reused, err := service.loadImplementationCheckpoint(items[1], github.DelegatedContentFor(items[1]), "original-context", metadata, snapshot, []string{"Run the owning test."})
	if err != nil || reused || !checkpoint.CorrectionUsed || checkpoint.ExecutionDeadline.IsZero() || checkpoint.Candidate.CommitOID != "" || checkpoint.Output.Outcome != "" {
		t.Fatalf("old implementer checkpoint bypassed fresh verification: reused=%t error=%v", reused, err)
	}
	if _, err := service.loadVerificationEvidence(items[1], github.DelegatedContentFor(items[1]), metadata, workspace.Candidate{CommitOID: snapshot.Head, TreeOID: snapshot.Tree}, []string{"Run the owning test."}); err == nil {
		t.Fatal("old evidence was accepted for the renewed source binding")
	}
	for _, record := range saved.History {
		if record.Path == service.implementationCheckpointPath(items[1].ID) || record.Path == service.verificationEvidencePath(items[1].ID) {
			if len(record.Before) == 0 {
				t.Fatal("original checkpoint or evidence was not archived before fresh verification")
			}
		}
		if record.Path == service.implementationCheckpointPath(items[1].ID) {
			var original implementationCheckpointRecord
			if err := json.Unmarshal(record.Before, &original); err != nil {
				t.Fatal(err)
			}
			if !original.ExecutionDeadline.Equal(checkpoint.ExecutionDeadline) || original.CorrectionUsed != checkpoint.CorrectionUsed {
				t.Fatal("rename recovery replenished an execution deadline or corrective allowance")
			}
		}
	}
}

func TestRepositoryRenameResumesReboundDirtyWorkspaceAfterRestart(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-source-approval-response-%t", after), func(t *testing.T) {
			service, runner, _ := repositoryRenameFixture(t, true)
			plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
			if err != nil {
				t.Fatal(err)
			}
			// Five fences and four child lifecycle updates precede the private
			// bindings and source approval. Recovery never writes issue bodies.
			runner.failAt, runner.afterWrite = 10, after
			if err := service.ApplyRepositoryRename(t.Context(), plan); err == nil {
				t.Fatal("expected source approval interruption")
			}
			source, err := github.NewProjectWithRunnerAuthority(service.cfg.GitHubProject, runner)
			if err != nil {
				t.Fatal(err)
			}
			restarted := &Engine{cfg: service.cfg, run: runner, source: source}
			fresh, err := restarted.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
			if err != nil {
				t.Fatal(err)
			}
			runner.failAt = 0
			if err := restarted.ApplyRepositoryRename(t.Context(), fresh); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRepositoryRenameRetainsDirtyWorkWithoutAnImplementationCheckpoint(t *testing.T) {
	service, runner, metadata := repositoryRenameFixture(t, true)
	for _, path := range []string{service.implementationCheckpointPath("PVTI_child_1"), service.verificationEvidencePath("PVTI_child_1"), service.reviewFeedbackPath("PVTI_child_1")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	runner.project.remoteItems[1].QAFailures = 0
	plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Workspaces) != 1 || plan.Workspaces[0].Before != metadata.Identity {
		t.Fatal("missing checkpoint was mistaken for missing retained work")
	}
	if err := service.ApplyRepositoryRename(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	if runner.project.remoteItems[1].QAFailures != 0 {
		t.Fatal("recovery changed zero QA rejections")
	}
	data, err := os.ReadFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"))
	if err != nil || string(data) != "preserve dirty candidate bytes\n" {
		t.Fatal("recovery lost dirty work without a checkpoint")
	}
}

func TestRepositoryRenameRefusesInterruptedContinuationWithoutChangingHistory(t *testing.T) {
	for _, specialist := range []bool{false, true} {
		t.Run(fmt.Sprintf("specialist-%t", specialist), func(t *testing.T) {
			service, runner, _ := repositoryRenameFixture(t, true)
			record, err := service.readImplementationCheckpoint("PVTI_child_1")
			if err != nil {
				t.Fatal(err)
			}
			record.Incomplete, record.Blocker, record.CandidateCommitOID, record.CandidateTreeOID = true, "Retain the interrupted correction", "", ""
			if specialist {
				request := &execution.TestSpecialistRequest{CriterionIndices: []int{0}, Reason: "Run focused proof", ExistingChecks: []string{"Inspected checks"}, Paths: []string{"checks_test.go"}}
				record.CorrectionUsed, record.Blocker = false, ""
				record.Specialist = &implementationSpecialistState{Phase: specialistStarted, HandoffID: "retained-handoff", SettingsDigest: strings.Repeat("a", 64), AssignmentDigest: strings.Repeat("b", 64), RequestDigest: specialistDigest(request), SourceFingerprint: record.SnapshotFingerprint, StartedAt: time.Now().Add(-time.Minute).UTC(), Previous: execution.Output{Outcome: execution.OutcomeTestRequested, TestRequest: request}, AllowedPaths: request.Paths}
			}
			if err := validateImplementationCheckpointRecord(*record); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			path := service.implementationCheckpointPath(record.ItemID)
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			before := append([]github.WorkItem(nil), runner.project.remoteItems...)
			if _, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old"); err == nil || !strings.Contains(err.Error(), "interrupted correction/specialist") {
				t.Fatalf("continuation was renewed: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(encoded, after) || runner.writes != 0 || !reflect.DeepEqual(before, runner.project.remoteItems) {
				t.Fatal("refused recovery changed continuation history or work")
			}
		})
	}
}

func TestRepositoryRenameRefusesCleanupQuarantineAndStandaloneOperations(t *testing.T) {
	for _, operation := range []string{"cleanup", "verification", "review"} {
		t.Run(operation, func(t *testing.T) {
			service, runner, _ := repositoryRenameFixture(t, false)
			if operation == "verification" {
				service.cfg.Verification = map[string]config.VerificationEntrypoint{"complete": {Command: "/bin/true"}}
			}
			plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
			if err != nil {
				t.Fatal(err)
			}
			var lock *github.ProcessLock
			switch operation {
			case "cleanup":
				service.processOwnership.RecordCleanupFailure()
			case "verification":
				lock, err = github.AcquireVerificationOperationLock(service.cfg.GitHubProject.GitHubProjectConfig, "complete")
			case "review":
				lock, err = github.AcquireQAReviewLock(service.cfg.GitHubProject.GitHubProjectConfig, "PVTI_child_1")
			}
			if err != nil {
				t.Fatal(err)
			}
			if lock != nil {
				defer lock.Release()
			}
			if err := service.ApplyRepositoryRename(t.Context(), plan); err == nil {
				t.Fatal("recovery ignored existing cleanup/operation exclusion")
			}
			if runner.writes != 0 {
				t.Fatal("quiescence refusal came after an external write")
			}
		})
	}
}

func TestRepositoryRenamePreservesConcurrentRequirementsEdit(t *testing.T) {
	service, runner, _ := repositoryRenameFixture(t, false)
	plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
	if err != nil {
		t.Fatal(err)
	}
	const requirement = "\n\nHuman added a material requirement during recovery."
	var editedBody string
	runner.observe = func() {
		if runner.writes == 5 {
			body := runner.project.remoteItems[1].Body
			editedBody = strings.Replace(body, "\n\n## Runner planning metadata", requirement+"\n\n## Runner planning metadata", 1)
			if editedBody == body {
				t.Fatal("fixture did not retain canonical planning metadata")
			}
			runner.project.remoteItems[1].Body = editedBody
		}
	}
	if err := service.ApplyRepositoryRename(t.Context(), plan); err == nil {
		t.Fatal("recovery authorized an intervening requirements edit")
	}
	if runner.project.remoteItems[1].Body != editedBody {
		t.Fatal("recovery overwrote the human's requirements")
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if items[1].PlanningMetadataInvalid {
		t.Fatal("regression only checked malformed metadata instead of changed requirements")
	}
	ready, err := service.source.ReadyItems(t.Context(), items, 10)
	if err != nil || len(ready) != 0 {
		t.Fatal("changed batch gained execution authority")
	}
}

func TestRepositoryRenamePreservesExplicitRequirementsAmendment(t *testing.T) {
	service, runner, metadata := repositoryRenameFixture(t, true)
	for _, path := range []string{service.implementationCheckpointPath("PVTI_child_1"), service.verificationEvidencePath("PVTI_child_1")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	runner.project.remoteItems[1].Branch = metadata.BranchName
	plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyRepositoryRename(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	metadata.Identity = plan.Workspaces[0].After
	if _, err := workspace.NewGitProvider(runner).ConstructCandidate(t.Context(), metadata, "Retain candidate before explicit amendment"); err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := items[1]
	const newRequirement = "\n\nAdditional explicitly approved requirement."
	replacement := strings.Replace(before.Body, "\n\n## Runner planning metadata", newRequirement+"\n\n## Runner planning metadata", 1)
	if replacement == before.Body {
		t.Fatal("fixture did not preserve canonical metadata")
	}
	preview, err := service.PlanRequirementAmendment(t.Context(), before.ID, replacement)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.source.LifecycleItems(t.Context())
	if err != nil || !reflect.DeepEqual(items, unchanged) {
		t.Fatal("amendment preview changed authority or requirements")
	}
	amended, err := service.ApplyRequirementAmendment(t.Context(), preview)
	if err != nil {
		t.Fatal(err)
	}
	if amended.Status != "Blocked" || amended.Body != replacement || amended.Repository != before.Repository || amended.QAFailures != before.QAFailures || amended.Branch != before.Branch || amended.PullRequest != "" || amended.QACommit != "" {
		t.Fatalf("amendment lost its pause, binding, or counters: %+v", amended)
	}
	after, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i < len(items); i++ {
		if !reflect.DeepEqual(items[i], after[i]) {
			t.Fatal("amendment renewed a sibling's requirements or action authority")
		}
	}
	if _, err := service.source.Authorize(t.Context(), after[1]); err != nil {
		t.Fatalf("amended action lost canonical repository authority: %v", err)
	}
	identity, err := workspace.NewGitProvider(runner).ValidateRetainedIdentity(t.Context(), service.workspaceRequestForItem(after[1], github.DelegatedContentFor(after[1]).Digest, service.cfg.ProjectDir, false))
	if err != nil || identity.Repository != plan.Batch.To {
		t.Fatalf("amended worktree lost its exact current binding: %+v %v", identity, err)
	}
	approval, err := service.PlanProjectItemApproval(t.Context(), plan.Batch.Source.ID)
	if err != nil || approval.Batch == nil || !approval.Batch.Released {
		t.Fatalf("amendment lost complete-batch authority: %v", err)
	}
	journal, err := os.ReadFile(plan.Journal)
	if err != nil {
		t.Fatal(err)
	}
	var original RepositoryRenameRecovery
	if err := json.Unmarshal(journal, &original); err != nil || original.Batch.Children[0].Before.Body != before.Body {
		t.Fatal("amendment rewrote original recovery history")
	}
}

func TestRepositoryRenameRejectsInvalidIdentityBatchAndStalePreview(t *testing.T) {
	for _, change := range []string{"different repository", "ambiguous identity", "noncanonical", "requirements", "source", "partial", "mixed", "index", "profile", "dependency", "active", "published", "accepted", "transition", "after preview", "modified preview", "dirty after preview", "remote after preview", "worker", "execution"} {
		t.Run(change, func(t *testing.T) {
			retained := change == "dirty after preview" || change == "remote after preview"
			service, runner, metadata := repositoryRenameFixture(t, retained)
			var plan RepositoryRenameRecovery
			var err error
			late := strings.Contains(change, "after preview") || change == "modified preview" || change == "worker" || change == "execution"
			if late {
				plan, err = service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
				if err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "different repository":
				runner.differentID = true
			case "ambiguous identity":
				runner.missingID = true
			case "noncanonical":
				runner.canonical = "owner/other"
			case "requirements", "after preview":
				runner.project.remoteItems[1].Body += "\nChanged requirements"
			case "source":
				runner.project.remoteItems[0].Body += "Changed source"
			case "partial":
				runner.project.remoteItems = runner.project.remoteItems[:4]
			case "mixed":
				runner.project.remoteItems[1].Body = strings.Replace(runner.project.remoteItems[1].Body, "v1:original-planning-batch", "v1:mixed-batch", 1)
			case "index":
				runner.project.remoteItems[1].Body = strings.Replace(runner.project.remoteItems[1].Body, `"planning_item_index":1`, `"planning_item_index":2`, 1)
			case "profile":
				runner.project.remoteItems[1].Body = strings.Replace(runner.project.remoteItems[1].Body, `"planning_item_index":1`, `"planning_item_index":1,"implementation_profile":"stronger"`, 1)
			case "dependency":
				runner.project.remoteItems[3].Body = strings.Replace(runner.project.remoteItems[3].Body, "PVTI_child_1", "PVTI_child_2", 1)
			case "active":
				runner.project.remoteItems[1].Activity = "Implementing"
			case "published":
				runner.project.remoteItems[1].PullRequest = "https://github.com/owner/repo/pull/6"
			case "accepted":
				runner.project.remoteItems[1].QACommit = strings.Repeat("a", 40)
			case "transition":
				runner.project.remoteItems[1].Transition = "v1"
			case "modified preview":
				plan.Batch.Children[0].After.QAFailures = 0
				plan.Batch.Children[0].After.Body += "tampered"
			case "dirty after preview":
				if err := os.WriteFile(filepath.Join(metadata.WorktreePath, "unfinished.txt"), []byte("different bytes"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "remote after preview":
				runner.remoteBranch = true
			case "worker", "execution":
				var lock *github.ProcessLock
				if change == "worker" {
					lock, err = github.AcquireProcessLock(service.cfg.GitHubProject.GitHubProjectConfig)
				} else {
					lock, err = github.AcquireExecutionSlot(service.cfg.GitHubProject.GitHubProjectConfig, 1)
				}
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = lock.Release() })
			}
			before := append([]github.WorkItem(nil), runner.project.remoteItems...)
			if late {
				err = service.ApplyRepositoryRename(t.Context(), plan)
			} else {
				_, err = service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
			}
			if err == nil {
				t.Fatal("invalid recovery accepted")
			}
			if !reflect.DeepEqual(before, runner.project.remoteItems) || runner.writes != 0 {
				t.Fatal("rejected recovery mutated work")
			}
		})
	}
}

func TestRepositoryRenameResumesEveryRemoteWriteWithoutPollingRepair(t *testing.T) {
	probe, runner, _ := repositoryRenameFixture(t, false)
	plan, err := probe.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.ApplyRepositoryRename(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	writeCount := runner.writes
	for at := 1; at <= writeCount; at++ {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("write-%d/after-%t", at, after), func(t *testing.T) {
				service, runner, _ := repositoryRenameFixture(t, false)
				plan, err := service.PlanRepositoryRename(t.Context(), "PVTI_parent", "owner/old")
				if err != nil {
					t.Fatal(err)
				}
				runner.failAt, runner.afterWrite = at, after
				if err := service.ApplyRepositoryRename(t.Context(), plan); err == nil {
					t.Fatal("interruption reported success")
				}
				before := append([]github.WorkItem(nil), runner.project.remoteItems...)
				items, err := service.source.LifecycleItems(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := service.source.RecoverInterruptedFrom(t.Context(), items); err != nil {
					t.Fatal(err)
				}
				ready, err := service.source.ReadyItems(t.Context(), items, 10)
				if err != nil {
					t.Fatal(err)
				}
				if at != writeCount || !after {
					if len(ready) != 0 {
						t.Fatal("partial rename became executable")
					}
				}
				if !reflect.DeepEqual(before, runner.project.remoteItems) {
					t.Fatal("polling silently repaired confirmed rename state")
				}
				fresh, err := service.PlanRepositoryRename(t.Context(), "PVTI_child_1", "owner/old")
				if err != nil {
					t.Fatal(err)
				}
				runner.failAt = 0
				if err := service.ApplyRepositoryRename(t.Context(), fresh); err != nil {
					t.Fatal(err)
				}
				items, err = service.source.LifecycleItems(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				ready, err = service.source.ReadyItems(t.Context(), items, 10)
				if err != nil || len(ready) != 2 {
					t.Fatalf("batch did not recover: %v %v", ready, err)
				}
			})
		}
	}
}
