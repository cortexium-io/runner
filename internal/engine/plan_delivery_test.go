package engine

import (
	"context"
	"encoding/json"
	"errors"
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
)

func TestDeliveryCLIStagesDurableParentAndReleasesExactManifest(t *testing.T) {
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: t.TempDir(), PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "go", ToolchainCommands: []string{"go"}, Args: []string{"test", "./..."}, TimeoutSeconds: 60, InputPaths: []string{"src", "go.mod"}},
	}})
	service, err := New(cfg, project)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	for i := range plan.WorkItems {
		plan.WorkItems[i].ImplementationProfile = "implementer"
		plan.WorkItems[i].ProfileReason = "Bounded deterministic fixture behavior"
	}
	children, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 || project.createCount != 3 {
		t.Fatalf("expected one parent and two members, children=%d created=%d", len(children), project.createCount)
	}
	parentID := children[0].PlanningSourceID
	if parentID == "" || children[1].PlanningSourceID != parentID {
		t.Fatal("CLI children lost durable parent")
	}
	for _, child := range children {
		if strings.Contains(child.Body, plan.SourceContext) {
			t.Fatal("shared request was duplicated into child")
		}
		for _, heading := range []string{"## Project outcome", "## Project success criteria", "## Project constraints", "## Original project request"} {
			if strings.Contains(child.Body, heading) {
				t.Fatalf("delivery member duplicated shared context: %s", heading)
			}
		}
		if !strings.Contains(child.Body, "## Acceptance criteria") || !strings.Contains(child.Body, "## Proof obligations") {
			t.Fatal("removing shared context discarded the member's local contract")
		}
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Parent == nil || preview.Parent.Batch == nil {
		t.Fatal("approval preview omitted shared parent contract")
	}
	manifest, present, err := github.ParsePlanManifest(preview.Parent.Item.Body)
	if err != nil || !present {
		t.Fatalf("missing canonical plan manifest: %v", err)
	}
	if manifest.Request != plan.SourceContext || manifest.Outcome != plan.GoalSummary || strings.Join(manifest.SuccessCriteria, "\n") != strings.Join(plan.ProjectSuccessCriteria, "\n") || strings.Join(manifest.Scope, "\n") != strings.Join(plan.ProjectConstraints, "\n") {
		t.Fatal("shared context was lost instead of retained in the authenticated parent")
	}
	replayed, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil || len(replayed) != 2 || project.createCount != 3 {
		t.Fatalf("staging retry duplicated work: %v count=%d", err, project.createCount)
	}
	if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	items, err := service.source.LifecycleItems(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var parent github.WorkItem
	for _, item := range items {
		if item.ID == parentID {
			parent = item
		}
	}
	if parent.Status == "Done" || parent.Phase != github.PlanDeliveryPhase || parent.PlanRelease == "" {
		t.Fatalf("planning completion confused with delivery: status=%s phase=%s", parent.Status, parent.Phase)
	}
	if _, err := service.source.ValidatePlanDelivery(parent, items); err != nil {
		t.Fatal(err)
	}
	ready, err := service.source.ReadyItems(t.Context(), items, len(items))
	if err != nil {
		t.Fatal(err)
	}
	if len(ready) != 1 || ready[0].Item.ID != children[0].ID {
		t.Fatalf("dependent or parent admitted before first integration: %d", len(ready))
	}
	assignment := execution.Assignment{Spec: execution.Spec{ItemID: ready[0].Item.ID, Repository: manifest.Repository}}
	if err := service.bindDeliveryAssignment(t.Context(), ready[0].Item, &assignment); err != nil {
		t.Fatal(err)
	}
	if assignment.Spec.PlanContext == nil || assignment.Spec.PlanContext.ApprovedBody != parent.Body || assignment.Spec.PlanContext.ID != parentID || assignment.Spec.PlanContext.Revision != github.PlanRevision(parent.Body) || assignment.Spec.ReviewScope != execution.ReviewScopeCard || len(assignment.Spec.PlanMemberBriefs) != 0 {
		t.Fatal("member assignment did not receive the exact shared authority without sibling/history duplication")
	}
}

// The transport substitutes only GitHub and paid model I/O. All coordinator,
// approval, workspace, Git publication, review parsing and complete-gate paths
// are the production entrypoints; Git mutations use a real disposable remote.
type deliveryMilestoneRunner struct {
	project                           *fakeGitHubProjectRunner
	implementations, reviews, creates int
	branch, head, base                string
	merged                            bool
	rejectFirst, crashIntegration     bool
	rejectCombined, combinedRejected  bool
	integrationLost                   bool
	planPushes                        int
	losePublication, publicationLost  bool
}

func (r *deliveryMilestoneRunner) RunBoundedHeadTailInput(ctx context.Context, command string, args []string, dir string, timeout time.Duration, input io.Reader, _ int, _ string) (subprocess.Result, error) {
	return runEngineTestWithInput(ctx, command, args, dir, timeout, input, r.Run)
}

func (r *deliveryMilestoneRunner) Run(ctx context.Context, command string, args []string, dir string, timeout time.Duration) (subprocess.Result, error) {
	if command == "git" {
		result, err := runEngineTestGit(ctx, args, dir, timeout)
		if containsArgument(args, "push") && strings.Contains(strings.Join(args, " "), ":refs/heads/runner/plan-") {
			r.planPushes++
			if err == nil && r.crashIntegration && !r.integrationLost && r.planPushes == 2 {
				r.integrationLost = true
				return result, errors.New("injected lost response after exact integration push")
			}
		}
		return result, err
	}
	if command == "codex" {
		var schema map[string]any
		encoded, err := os.ReadFile(argumentValue(args, "--output-schema"))
		if err != nil {
			return subprocess.Result{}, err
		}
		if err := json.Unmarshal(encoded, &schema); err != nil {
			return subprocess.Result{}, err
		}
		properties, _ := schema["properties"].(map[string]any)
		if properties["criteria"] != nil || properties["checks"] != nil {
			r.reviews++
			wholePlan := strings.Contains(strings.Join(args, " "), `"review_scope":"plan"`)
			reject := r.rejectFirst && r.reviews == 1
			if wholePlan && r.rejectCombined && !r.combinedRejected {
				reject = true
				r.combinedRejected = true
			}
			encoded, err = reviewerContentForSchema(args, reject, "member-1.txt contains broken instead of ready")
			if reject && wholePlan && err == nil {
				var content map[string]any
				err = json.Unmarshal(encoded, &content)
				if err == nil {
					content["repair_targets"] = []execution.PlanRepairTarget{{CheckKey: "P1", ItemID: "PVTI_created_2", Finding: "Restore member-1 ready after integration; preserve member-2", InScope: true}}
					encoded, err = json.Marshal(content)
				}
			}
		} else {
			r.implementations++
			target := profileReadRoot(args, dir)
			member := 1
			if strings.Contains(target, "created_3") {
				member = 2
			}
			value := "ready\n"
			if r.rejectCombined && member == 2 && !r.combinedRejected {
				if err := os.WriteFile(filepath.Join(target, "member-1.txt"), []byte("broken\n"), 0600); err != nil {
					return subprocess.Result{}, err
				}
			}
			if r.rejectFirst && r.implementations == 1 {
				value = "broken\n"
			}
			if err := os.WriteFile(filepath.Join(target, fmt.Sprintf("member-%d.txt", member)), []byte(value), 0600); err != nil {
				return subprocess.Result{}, err
			}
			encoded, err = json.Marshal(map[string]any{"outcome": "succeeded", "summary": "Implemented the exact fixture requirement.", "work_done": []string{"Added the approved ready behavior."}, "verification": []string{"Read the fixture and confirmed ready."}, "blockers": []string{}})
		}
		if err != nil {
			return subprocess.Result{}, err
		}
		return subprocess.Result{}, os.WriteFile(argumentValue(args, "--output-last-message"), encoded, 0600)
	}
	if command == "gh" && len(args) > 1 && args[0] == "pr" {
		switch args[1] {
		case "list":
			if r.creates == 0 {
				return subprocess.Result{Stdout: `[]`}, nil
			}
			b, _ := json.Marshal([]map[string]any{{"url": "https://github.com/owner/repo/pull/12", "number": 12, "headRefName": r.branch, "baseRefName": "main", "headRefOid": r.head, "state": "OPEN"}})
			return subprocess.Result{Stdout: string(b)}, nil
		case "create":
			r.creates++
			r.branch = argumentValue(args, "--head")
			r.head = runnerGitRevision(ctx, dir, timeout, "HEAD")
			r.base = runnerGitRevision(ctx, dir, timeout, "origin/main")
			if r.losePublication && !r.publicationLost {
				r.publicationLost = true
				return subprocess.Result{}, errors.New("injected lost response after final PR creation")
			}
			return subprocess.Result{Stdout: "https://github.com/owner/repo/pull/12\n"}, nil
		case "view":
			state := "OPEN"
			if r.merged {
				state = "MERGED"
			}
			b, _ := json.Marshal(map[string]any{"url": "https://github.com/owner/repo/pull/12", "number": 12, "state": state, "headRepository": map[string]string{"nameWithOwner": "owner/repo"}, "headRefName": r.branch, "headRefOid": r.head, "baseRefName": "main", "baseRefOid": r.base, "mergeStateStatus": "CLEAN", "comments": []any{}, "reviews": []any{}})
			return subprocess.Result{Stdout: string(b)}, nil
		}
	}
	return r.project.Run(ctx, command, args, dir, timeout)
}

func TestDeliveryProductionMilestone(t *testing.T) {
	for _, tc := range []struct {
		name          string
		reject, crash bool
		combined      bool
	}{{"complete", false, false, false}, {"concrete repair", true, false, false}, {"lost integration response", false, true, false}, {"combined journey repair", false, false, true}} {
		t.Run(tc.name, func(t *testing.T) { testDeliveryProductionMilestone(t, tc.reject, tc.crash, tc.combined) })
	}
}

func TestDeliveryProductionRecoversLostFinalPRResponse(t *testing.T) {
	testDeliveryProductionMilestoneWithPublicationLoss(t, false, false, false, true)
}

func TestDeliveryProductionRefusesChangedAuthorityBeforeAdmission(t *testing.T) {
	for _, change := range []string{"shared outcome", "destination", "membership", "profile reason", "profile settings", "child content", "missing parent", "cancelled", "gate settings"} {
		t.Run(change, func(t *testing.T) {
			repo, remote := createPublicationRepository(t)
			project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
			runner := &deliveryMilestoneRunner{project: project}
			cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
				"complete": {Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Args: []string{"-c", "exit 0"}, TimeoutSeconds: 30, InputPaths: []string{"member-1.txt", "member-2.txt"}},
			}})
			service, err := New(cfg, runner)
			if err != nil {
				t.Fatal(err)
			}
			children, err := service.ApplyProjectPlan(t.Context(), directProjectPlanFixture())
			if err != nil {
				t.Fatal(err)
			}
			preview, err := service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
				t.Fatal(err)
			}
			parentID := children[0].PlanningSourceID
			for index := range project.remoteItems {
				item := &project.remoteItems[index]
				if change == "child content" && item.ID == children[0].ID {
					item.Body += "\nUnapproved additional requirement"
				}
				if item.ID != parentID {
					continue
				}
				manifest, _, err := github.ParsePlanManifest(item.Body)
				if err != nil {
					t.Fatal(err)
				}
				switch change {
				case "shared outcome":
					manifest.Outcome = "A different unapproved outcome"
				case "destination":
					manifest.DestinationBranch = "other"
				case "membership":
					manifest.Members = manifest.Members[:1]
				case "profile reason":
					manifest.Members[0].ProfileReason = "A different unapproved profile choice"
				case "cancelled":
					item.Phase = github.PlanCancelledPhase
				}
				item.Body, err = github.FormatPlanManifest(manifest)
				if err != nil {
					t.Fatal(err)
				}
			}
			if change == "missing parent" {
				project.remoteItems = append([]github.WorkItem(nil), project.remoteItems[1:]...)
			}
			if change == "gate settings" {
				entry := cfg.Verification["complete"]
				entry.Args = []string{"-c", "exit 1"}
				cfg.Verification["complete"] = entry
			}
			if change == "profile settings" {
				profile := cfg.Roles["implementer"]
				model := "changed-model-under-the-same-profile-id"
				profile.Model = &model
				cfg.Roles["implementer"] = profile
			}
			// Reconstruct the real coordinator so admission cannot use cached
			// planning state or the release that was visible at approval time.
			service, err = New(cfg, runner)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = service.RunCycle(t.Context()) // Refusal can surface in reconciliation or item eligibility.
			if runner.implementations != 0 || runner.reviews != 0 || runner.creates != 0 || runner.planPushes != 0 {
				t.Fatalf("altered authority caused model work or publication: %+v", runner)
			}
			if refs := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "for-each-ref", "--format=%(refname)", "refs/heads/runner/plan-")); refs != "" {
				t.Fatalf("altered authority created plan branch: %s", refs)
			}
		})
	}
}

func testDeliveryProductionMilestone(t *testing.T, reject, crash, combined bool) {
	testDeliveryProductionMilestoneWithPublicationLoss(t, reject, crash, combined, false)
}

func testDeliveryProductionMilestoneWithPublicationLoss(t *testing.T, reject, crash, combined, publicationLoss bool) {
	repo, remote := createPublicationRepository(t)
	project := &fakeGitHubProjectRunner{itemsJSON: `{"items":[]}`}
	runner := &deliveryMilestoneRunner{project: project, rejectFirst: reject, crashIntegration: crash, rejectCombined: combined, losePublication: publicationLoss}
	cfg := completeEngineTestConfig(config.Config{ProjectDir: repo, PlanDelivery: &config.PlanDeliveryConfig{Enabled: true, CompleteVerification: "complete"}, Verification: map[string]config.VerificationEntrypoint{
		"complete": {Command: "/bin/sh", ToolchainCommands: []string{"/bin/sh"}, Args: []string{"-c", "test \"$(cat member-1.txt)\" = ready && test \"$(cat member-2.txt)\" = ready"}, TimeoutSeconds: 30, InputPaths: []string{"member-1.txt", "member-2.txt"}},
	}})
	profile := cfg.Roles["reviewer"]
	profile.Access = config.RoleAccessHost
	cfg.Roles["reviewer"] = profile
	service, err := New(cfg, runner)
	if err != nil {
		t.Fatal(err)
	}
	plan := directProjectPlanFixture()
	for i := range plan.WorkItems {
		plan.WorkItems[i].ImplementationProfile = "implementer"
		plan.WorkItems[i].ProfileReason = "Focused fixture"
	}
	children, err := service.ApplyProjectPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.PlanStagedProjectPlanApproval(t.Context(), children[0].PlanningBatchFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApplyProjectPlanApproval(t.Context(), preview); err != nil {
		t.Fatal(err)
	}
	parentID := children[0].PlanningSourceID
	for cycle := 0; cycle < 12; cycle++ {
		results, err := service.RunCycle(t.Context())
		if err != nil {
			t.Fatalf("cycle %d: %v", cycle, err)
		}
		for _, result := range results {
			if crash && result.Outcome == execution.OutcomeBlocked && strings.Contains(result.Error, "injected lost response") {
				service, err = New(cfg, runner)
				if err != nil {
					t.Fatal(err)
				}
				continue
			}
			if result.Error != "" || result.Outcome == execution.OutcomeBlocked {
				t.Fatalf("cycle %d: %#v", cycle, result)
			}
		}
		items, err := service.source.LifecycleItems(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.ID == parentID && item.Status == "PR Ready" {
				wantImpl, wantReview := 2, 3
				if reject {
					wantImpl++
					wantReview++
				}
				wantPush := 3 // Initial branch plus the two accepted integrations; final publication is already at this exact head.
				if combined {
					wantImpl++
					wantReview += 2
					wantPush++
				}
				if runner.creates != 1 || runner.implementations != wantImpl || runner.reviews != wantReview || runner.planPushes != wantPush {
					t.Fatalf("unexpected model/publication counts: %+v", runner)
				}
				for _, child := range items {
					if child.PlanningSourceID == parentID && (child.Phase != github.PlanIntegratedPhase || child.PullRequest != "" || child.Status == "Done") {
						t.Fatalf("premature child delivery %+v", child)
					}
				}
				if got := strings.TrimSpace(runGitTest(t, "", "--git-dir", remote, "rev-parse", "refs/heads/"+item.Branch)); got != item.QACommit {
					t.Fatal("final PR is not exact combined candidate")
				}
				// A PR is not delivery: confirm its exact merge, then use normal
				// terminal reconciliation twice to prove idempotent completion.
				runGitTest(t, "", "--git-dir", remote, "update-ref", "refs/heads/main", item.QACommit)
				runner.merged = true
				service, err = New(cfg, runner)
				if err != nil {
					t.Fatal(err)
				}
				for recovery := 0; recovery < 2; recovery++ {
					if _, err := service.RunCycle(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				done, err := service.source.LifecycleItems(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				for _, candidate := range done {
					if candidate.ID == parentID && candidate.Status != "Done" {
						t.Fatal("confirmed merge not recorded as delivery")
					}
				}
				if runner.creates != 1 || runner.implementations != wantImpl || runner.reviews != wantReview {
					t.Fatal("delivery recovery repeated model work or PR creation")
				}
				return
			}
		}
	}
	t.Fatal("production coordinator did not publish combined plan")
}
