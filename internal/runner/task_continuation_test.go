package runner

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type continuationStore struct {
	*frontendRecheckStore
	commits int
	fail    bool
}

func (s *continuationStore) SaveTaskContinuation(_ context.Context, origin, target Run, family []Run, receipt *Receipt) error {
	if err := ValidateTaskContinuation(origin, target, family, receipt); err != nil {
		return err
	}
	if s.fail {
		return fmt.Errorf("injected atomic store failure")
	}
	s.commits++
	s.receipts[receipt.Key] = Clone(*receipt)
	return nil
}

type continuationExecutor struct {
	Executor
	calls    int
	validate func(Run, SourceResult) error
	start    func(Assignment) Observation
}

func (e *continuationExecutor) Start(_ context.Context, a Assignment) (Observation, error) {
	if e.start == nil {
		return Observation{}, fmt.Errorf("fixture has no execution permission")
	}
	return e.start(a), nil
}

func (e *continuationExecutor) Release(context.Context, string) error { return nil }

func (e *continuationExecutor) ValidateSourceRecovery(_ context.Context, r Run, source SourceResult) error {
	e.calls++
	if e.validate != nil {
		return e.validate(r, source)
	}
	return nil
}

func taskContinuationFixture(t *testing.T) (*Core, *continuationStore, *continuationExecutor, ContinueTaskRequest) {
	t.Helper()
	c, retained, grant := plannedSerialFrontendFixture(t)
	r, err := c.RecoverFrontendCheck(grant)
	if err != nil {
		t.Fatal(err)
	}
	var source SourceResult
	for _, a := range r.Attempts {
		if a.Source != nil && a.Source.PatchID == grant.AttemptID+"/changes.patch" {
			source = *a.Source
		}
	}
	// The recovery request names the prior failed check. The grant itself owns
	// the accepted candidate identity, independent of that check attempt.
	for _, a := range r.Attempts {
		if a.Source != nil && a.Source.PatchID == r.FrontendCheckRecovery.PatchID {
			source = *a.Source
		}
	}
	const checkID = "attempt_continuation_pass"
	data, _ := json.Marshal(timeoutReportFixture(t, 0, false))
	if err = c.disk.Write("artifacts/"+checkID+"/result", data, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	retained.artifacts[checkID+"/result"] = Artifact{ID: checkID + "/result", RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(data))}
	checked := source
	checked.AttemptID, checked.InputPatchID, checked.PatchID = checkID, source.PatchID, ""
	checked.Tree = "" // Legacy read-only checks attest their input patch, not a new source capture.
	passed := Attempt{
		ID: checkID, Step: taskStepIndex(r.Process, "check"), Visit: r.Visit, State: Finished, Released: true,
		Source: &checked,
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{
			"check": Text("all selected checks passed"), "artifact": {ArtifactID: checkID + "/result"},
		}}},
	}
	r.Process.MaxAttempts = 64
	r.Step, r.Visit, r.Status, r.Reason = taskStepIndex(r.Process, "review"), r.Visit+1, "Failed", "retained source review blocked"
	r.ResourcesReleased = true
	r.StepInputs = nil
	c.runs[r.ID] = r
	for c.familyAttempts(r) < 34 {
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("attempt_preserved_%d", len(r.Attempts)), State: Finished, Released: true, Superseded: true})
		c.runs[r.ID] = r
	}
	r.Attempts = append(r.Attempts, passed, Attempt{
		ID: "attempt_blocked_review", Step: r.Step, Visit: r.Visit, State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "blocked", Reason: r.Reason}},
	})
	c.runs[r.ID] = r
	store := &continuationStore{frontendRecheckStore: retained}
	executor := &continuationExecutor{Executor: c.executor}
	c.store, c.executor = store, executor
	role := Role{
		ID: "lead", Purpose: "Complete the visibly improved candidate with evidence",
		Inputs: map[string]string{"request": "text", "patch": "artifact", "checks": "artifact"}, Outputs: map[string]string{"result": "text"},
		Execution: &Profile{Harness: "deterministic", Mode: "echo", Input: "request", Output: "result"},
	}
	reader := Role{
		ID: "reader", Purpose: "Evaluate declared retained evidence", Inputs: map[string]string{"patch": "artifact", "checks": "artifact"},
		Outputs: map[string]string{"result": "text"}, Execution: &Profile{Harness: "deterministic"},
	}
	process := Process{
		ID: "continue-preview", Inputs: Clone(role.Inputs), Outputs: map[string]Binding{"result": {FromStep: "lead", Output: "result"}},
		Steps: []Step{{ID: "lead", Role: "lead", Inputs: map[string]Binding{
			"request": {FromInput: "request"}, "patch": {FromInput: "patch"}, "checks": {FromInput: "checks"},
		}}},
		Tasks:       &TaskPolicy{Roles: []string{"lead", "reader"}, MaxTasks: 16, MaxDepth: 4, MaxRevisions: 2, MaxConcurrent: 2},
		MaxAttempts: 64, MaxAttemptsPerStep: 2, AttemptTimeout: "1m",
	}
	project := Project{
		ID: "preview", Workspace: t.TempDir(), DefaultProcess: process.ID,
		Processes: map[string]Process{process.ID: process}, Roles: map[string]Role{role.ID: role, reader.ID: reader}, Profiles: map[string]string{},
	}
	c.snapshot = Snapshot{Instance: Instance{EnforceWorkLimits: true}, Projects: map[string]Project{project.ID: project}}
	view, err := c.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, store, executor, ContinueTaskRequest{
		RequestID: "authorized-task-continuation", RunID: r.ID, Revision: view.Revision,
		Project: project.ID, Process: process.ID, SourceAttemptID: source.AttemptID,
		PatchSHA256: grant.PatchSHA256, CheckAttemptID: checkID, CheckSHA256: fmt.Sprintf("%x", sha256.Sum256(data)),
		Inputs: map[string]Value{"request": Text("Deliver a visibly improved verified Sudoku preview"), "patch": {ArtifactID: source.PatchID}, "checks": {ArtifactID: checkID + "/result"}},
	}
}

func TestTaskContinuationPreservesHistoryAndDebitsExactRemainingAllowance(t *testing.T) {
	c, store, executor, req := taskContinuationFixture(t)
	before := Clone(c.runs[req.RunID])
	r, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == before.ID || r.ProjectID != req.Project || r.Process.ID != req.Process || r.Status != "Queued" || r.TaskGraph == nil || r.TaskGraph.Lineage == nil || r.TaskGraph.Lineage.PriorAttempts != 36 || c.familyRemaining(r) != 28 || r.Source != nil || r.TaskGraph.Lineage.Source.WorkDir != "" || store.commits != 1 || executor.calls != 1 {
		t.Fatalf("continuation reset ownership/allowance: %+v error=%v", r, err)
	}
	got := Clone(c.runs[req.RunID])
	if got.ContinuedBy == nil || got.ContinuedBy.RunID != r.ID || got.ContinuedBy.Lineage != *r.TaskGraph.Lineage {
		t.Fatal("original run lacks exact immutable forward audit")
	}
	got.ContinuedBy, got.Updated, got.Revision = nil, before.Updated, before.Revision
	if !reflect.DeepEqual(got, before) {
		t.Fatal("handoff changed old source, status, attempt facts, definitions or limits")
	}
	originSummary := Summarize(c.outputRun(c.runs[req.RunID]), time.Now())
	if originSummary.Status != "Failed" || originSummary.ContinuedBy != r.ID || originSummary.Progress.NextAction != "follow_task_continuation" || len(originSummary.Progress.Attention) != 0 {
		t.Fatal("old history still asks for recovery after ownership transferred", originSummary)
	}
	if replay, err := c.ContinueTask(req); err != nil || replay.ID != r.ID || store.commits != 1 || executor.calls != 1 {
		t.Fatal("lost acknowledgement did not replay the same audited root", err)
	}
	req.RequestID = "second-transfer"
	if _, err = c.ContinueTask(req); err == nil || store.commits != 1 {
		t.Fatal("same origin created a second continuation")
	}
	ctx, err := c.graphContext(r)
	if err != nil || ctx.RemainingAttempts != 28 || ctx.Lineage == nil || ctx.Lineage.PriorAttempts != 36 {
		t.Fatal("planner did not receive historical debit", ctx, err)
	}
	// Replaying 28 ordinary future reservations exhausts the original 64. No
	// detached child or parent reserve can create an additional private budget.
	for i := range 28 {
		r.Attempts = append(r.Attempts, Attempt{ID: fmt.Sprintf("new_attempt_%d", i), State: Finished, Released: true})
	}
	c.runs[r.ID] = r
	if c.familyAttempts(r) != 64 || c.familyRemaining(r) != 0 || c.graphReserve(r, 1, false) == nil || c.activeAttempts() != 0 {
		t.Fatal("continuation exceeded original total or counted history as active work")
	}
	view := c.outputRun(r)
	summary := Summarize(view, time.Now())
	if summary.Attempts != 64 || summary.AttemptBudget == nil || summary.AttemptBudget.Spent != 64 || summary.AttemptBudget.PriorAttempts != 36 || summary.AttemptBudget.Remaining != 0 {
		t.Fatal("status hid the transferred debit", summary)
	}
}

func TestTaskContinuationRestorationAndRetentionKeepBothFamilies(t *testing.T) {
	c, _, _, req := taskContinuationFixture(t)
	r, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.validateTaskContinuationLinks(); err != nil {
		t.Fatal("valid continuation could not restore", err)
	}
	family, err := c.retainedTaskFamily(r)
	if err != nil || len(family) != 6 || family[0].ID != r.ID {
		t.Fatal("retention separated old history from new execution", family, err)
	}
	ids := map[string]bool{}
	for _, member := range family {
		ids[member.ID] = true
	}
	if !ids[req.RunID] {
		t.Fatal("continuation retention omitted its failed origin")
	}
	for _, link := range c.runs[req.RunID].TaskGroups["cards"].Children {
		if !ids[link.RunID] {
			t.Fatal("continuation retention omitted old source-card history")
		}
	}
	root := c.runs[r.ID]
	root.TaskGraph.Lineage.PriorAttempts--
	c.runs[r.ID] = root
	if err = c.validateTaskContinuationLinks(); err == nil {
		t.Fatal("restoration silently reset a historical debit")
	}
}

func TestTaskContinuationRunsNestedWorkThroughTheExistingCore(t *testing.T) {
	c, _, executor, req := taskContinuationFixture(t)
	starts := make(chan Assignment, 3)
	executor.start = func(a Assignment) Observation {
		starts <- a
		result := &StepResult{Outcome: "ready"}
		if a.TaskContext.TaskID == "root" && len(a.TaskContext.Results) == 0 {
			result.TaskAction = &TaskAction{Kind: "delegate", Tasks: []RoleTask{{
				ID: "assess-candidate", Role: "reader", Brief: "Check the exact retained evidence against the new goal",
				Inputs: map[string]Value{"patch": req.Inputs["patch"], "checks": req.Inputs["checks"]},
			}}}
		} else {
			result.Outputs = map[string]Value{"result": Text("verified retained candidate result")}
		}
		return Observation{State: Finished, End: &ExecutionEnd{Kind: "returned", Result: result}}
	}
	c.snapshot.Instance.Concurrency, c.snapshot.Instance.MaxActiveAttempts = 2, 2
	c.snapshot.Instance.ReconcileInterval = "1h"
	c.ctx, c.cancel = context.WithCancel(t.Context())
	c.loopDone = make(chan struct{})
	go c.loop()
	defer c.Close()
	r, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for {
		r, err = c.Get(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		settled := r.Terminal()
		for _, a := range r.Attempts {
			settled = settled && a.Released
		}
		for _, task := range r.TaskStatuses {
			settled = settled && !task.Unsettled && !task.CleanupPending
		}
		if settled {
			break
		}
		if _, err = c.Wait(ctx, r.ID, r.Revision); err != nil {
			t.Fatal("normal Core did not finish retained handoff", err, r.Status, r.Reason)
		}
	}
	if r.Status != "Completed" || len(r.TaskStatuses) != 1 || len(r.Attempts) != 2 || r.TaskBudget == nil || r.TaskBudget.Spent != 39 || r.TaskBudget.Remaining != 25 || Summarize(r, time.Now()).Attempts != 39 || len(starts) != 3 {
		t.Fatal("handoff used another lifecycle or reset the allowance", r.Status, r.Reason, r.TaskBudget, len(starts))
	}
	origin, err := c.Get(req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		a := <-starts
		if a.TaskContext == nil || a.TaskContext.Lineage == nil || a.TaskContext.Lineage.PriorAttempts != 36 || a.TaskContext.RemainingAttempts > 27 || a.WorkDir == origin.Repository {
			t.Fatal("existing dispatch lost continuation ownership/budget", a.TaskContext)
		}
	}
}

func TestTaskContinuationImportsOnlySelectedDeclaredEvidenceThroughChildren(t *testing.T) {
	c, store, _, req := taskContinuationFixture(t)
	r, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	patch, checks := req.Inputs["patch"], req.Inputs["checks"]
	unselected := Artifact{ID: "attempt_unselected/result", RunID: req.RunID, ProjectID: "demo", Bytes: 1}
	store.artifacts[unselected.ID] = unselected
	if !c.graphArtifactAllowed(r, patch.ArtifactID) || !c.graphArtifactAllowed(r, checks.ArtifactID) || c.graphArtifactAllowed(r, unselected.ID) {
		t.Fatal("root artifact authority is broader than the exact selected lineage")
	}
	spec := RoleTask{ID: "read-evidence", Role: "reader", Brief: "Evaluate the retained candidate", Inputs: map[string]Value{"patch": patch, "checks": checks}}
	if err = c.graphAction(&r, &StepResult{Outcome: "ready", TaskAction: &TaskAction{Kind: "delegate", Tasks: []RoleTask{spec}}}); err != nil {
		t.Fatal(err)
	}
	c.runs[r.ID] = r
	child := graphChild(r, r, r.TaskGraph.Children[0])
	c.runs[child.ID] = child
	if child.TaskGraph.Lineage != nil || c.familyRemaining(child) != 28 || !c.graphArtifactAllowed(child, patch.ArtifactID) || c.graphArtifactAllowed(child, unselected.ID) {
		t.Fatal("child duplicated allowance or broadened imported evidence")
	}
	child.Inputs = nil
	if c.graphArtifactAllowed(child, patch.ArtifactID) {
		t.Fatal("lineage alone granted an undeclared child input")
	}
}

func TestTaskContinuationForwardsConfiguredPreparationEvidenceOnlyWhenAssigned(t *testing.T) {
	c, store, _, req := taskContinuationFixture(t)
	r, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	const id = "attempt_source_materializer/handoff.json"
	store.artifacts[id] = Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: 20}
	leader := r.Process.Steps[0]
	leader.Inputs["handoff"] = Binding{FromStep: "materialize", Output: "artifact"}
	r.Process.Steps = []Step{{ID: "materialize", Role: "initializer"}, leader}
	r.Step = 1
	r.Attempts = []Attempt{{
		ID: "attempt_source_materializer", Step: 0, State: Finished, Released: true,
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"artifact": {ArtifactID: id}}}},
	}}
	c.runs[r.ID] = r
	if !c.graphArtifactAllowed(r, id) {
		t.Fatal("current role cannot forward an explicitly assigned preparation artifact")
	}
	delete(r.Process.Steps[1].Inputs, "handoff")
	if c.graphArtifactAllowed(r, id) {
		t.Fatal("an unassigned prior output became a general artifact grant")
	}
	r.StepInputs = map[string]Value{"handoff": {ArtifactID: id}}
	if !c.graphArtifactAllowed(r, id) {
		t.Fatal("frozen current-visit inputs lost their canonical artifact authority")
	}
}

func TestTaskContinuationFreezesEveryOldFamilyMutationAndExtraBudget(t *testing.T) {
	c, store, executor, req := taskContinuationFixture(t)
	continued, err := c.ContinueTask(req)
	if err != nil {
		t.Fatal(err)
	}
	origin := c.runs[req.RunID]
	child := c.runs[origin.TaskGroups["cards"].Children[0].RunID]
	for _, r := range []Run{origin, child} {
		if _, err = c.Retry(RetryRequest{RequestID: "retry-" + r.ID, RunID: r.ID, ExtraAttempts: 1}); err == nil {
			t.Fatal("old family retry spent after transfer", r.ID)
		}
		if _, err = c.Cancel(r.ID); err == nil {
			t.Fatal("old family cancellation mutated retained source", r.ID)
		}
		if _, err = c.Decide(DecisionRequest{RequestID: "decide-" + r.ID, RunID: r.ID, Choice: "approve"}); err == nil {
			t.Fatal("old family decision survived transfer", r.ID)
		}
		if c.persist(r, nil, nil) == nil || c.taskDispatchAllowed(r, 0) {
			t.Fatal("central persistence or dispatch boundary allowed frozen source", r.ID)
		}
	}
	for _, action := range []func() error{
		func() error {
			_, err := c.RecoverCorrections(RecoverCorrectionsRequest{RequestID: "old-correction", RunID: origin.ID, PreserveSource: true, MaxUses: 3})
			return err
		},
		func() error {
			_, err := c.RecoverCollection(RecoverCollectionRequest{RequestID: "old-collection", RunID: origin.ID})
			return err
		},
		func() error {
			_, err := c.RecoverPlanning(RecoverPlanningRequest{RequestID: "old-plan", RunID: origin.ID})
			return err
		},
		func() error {
			_, err := c.RecoverFrontendCheck(RecoverFrontendCheckRequest{RequestID: "old-check", RunID: origin.ID})
			return err
		},
		func() error {
			_, err := c.CheckUnpublishedCardRecovery(RecoverUnpublishedCardRequest{RunID: origin.ID})
			return err
		},
		func() error {
			_, err := c.CheckSourceHandoff(AdjudicateSourceHandoffRequest{RecoverUnpublishedCardRequest: RecoverUnpublishedCardRequest{RunID: origin.ID}})
			return err
		},
	} {
		if err = action(); err == nil {
			t.Fatal("old source recovery escaped freeze")
		}
	}
	if store.commits != 1 || executor.calls != 1 {
		t.Fatal("old controls reached execution validation or another commit")
	}
	continued.Status, continued.Reason = "Failed", "explicit test failure"
	c.runs[continued.ID] = continued
	granted, err := c.Retry(RetryRequest{RequestID: "new-extra", RunID: continued.ID, ExtraAttempts: 1})
	if err != nil || granted.Process.MaxAttempts != 65 || granted.TaskGraph.Lineage.MaxAttempts != 64 || len(granted.TaskGraph.AllowanceGrants) != 1 {
		t.Fatal("owner grant failed to preserve original authority while amending its effective ceiling", err)
	}
}

func TestTaskContinuationRefusesChangedAuthorityBeforeAnyEffect(t *testing.T) {
	for _, fault := range []string{"revision", "active-child", "cleanup-child", "unreleased-child", "digest", "registration", "serial-grant", "check-result", "checked-tree", "checked-workspace", "source-outcome", "source-timeout", "selectors", "foreign-input", "target-cap", "source-rejected", "child-drift", "pending-retention", "atomic-failure"} {
		t.Run(fault, func(t *testing.T) {
			c, store, executor, req := taskContinuationFixture(t)
			r := c.runs[req.RunID]
			childID := r.TaskGroups["cards"].Children[0].RunID
			child := c.runs[childID]
			switch fault {
			case "revision":
				req.Revision++
			case "active-child":
				child.Attempts[0].State, child.Attempts[0].Released = Unknown, false
			case "cleanup-child":
				child.CleanupBlocked = true
			case "unreleased-child":
				child.ResourcesReleased = false
			case "digest":
				req.CheckSHA256 = strings.Repeat("f", 64)
			case "registration":
				a := store.artifacts[req.Inputs["patch"].ArtifactID]
				a.ProjectID = "foreign"
				store.artifacts[a.ID] = a
			case "serial-grant":
				r.FrontendCheckRecovery.Serial = false
			case "check-result":
				for i := range r.Attempts {
					if r.Attempts[i].ID == req.CheckAttemptID {
						r.Attempts[i].End.Result.Outcome = "changes_requested"
					}
				}
			case "checked-tree", "checked-workspace":
				for i := range r.Attempts {
					if r.Attempts[i].ID == req.CheckAttemptID {
						if fault == "checked-tree" {
							r.Attempts[i].Source.Tree = strings.Repeat("f", 40)
						} else {
							r.Attempts[i].Source.WorkDir = "/different/workspace"
						}
					}
				}
			case "source-outcome", "source-timeout":
				for i := range r.Attempts {
					if r.Attempts[i].ID == req.SourceAttemptID {
						if fault == "source-outcome" {
							r.Attempts[i].End.Result.Outcome = "blocked"
						} else {
							r.Attempts[i].TimedOut = true
						}
					}
				}
			case "selectors":
				r.Inputs["tests"] = Text(`["different.test.ts"]`)
			case "foreign-input":
				req.Inputs["patch"] = Value{ArtifactID: "attempt_join/changes.patch"}
			case "target-cap":
				p := c.snapshot.Projects[req.Project]
				proc := p.Processes[req.Process]
				proc.MaxAttempts++
				p.Processes[req.Process] = proc
				c.snapshot.Projects[req.Project] = p
			case "source-rejected":
				executor.validate = func(Run, SourceResult) error { return fmt.Errorf("capture ownership differs") }
			case "child-drift":
				executor.validate = func(Run, SourceResult) error {
					current := c.runs[childID]
					current.Revision++
					c.runs[childID] = current
					return nil
				}
			case "atomic-failure":
				store.fail = true
			case "pending-retention":
				if err := c.disk.Write(familyCollectionDirectory+"/"+r.ID+".json", []byte(`{}`), c.disk.Policy.Database.MaxRecord); err != nil {
					t.Fatal(err)
				}
			}
			c.runs[r.ID], c.runs[childID] = r, child
			count := len(c.runs)
			if _, err := c.ContinueTask(req); err == nil || store.commits != 0 || c.runs[r.ID].ContinuedBy != nil || len(c.runs) != count {
				t.Fatal("changed authority created a partial handoff", fault, err)
			}
			if _, err := store.FindReceipt(t.Context(), req.RequestID); err == nil {
				t.Fatal("rejected continuation retained a receipt")
			}
		})
	}
}

func continuationExpandedReport(t *testing.T) frontendTimeoutReport {
	t.Helper()
	data, err := json.Marshal(timeoutReportFixture(t, 0, false))
	var report frontendTimeoutReport
	if err != nil || json.Unmarshal(data, &report) != nil {
		t.Fatal("decode complete report fixture", err)
	}
	const added = "src/games/sudoku/digit-gestures.test.ts"
	const reported = "/owned/scratch/frontend/" + added
	file := report.Files[0]
	file.Selected, file.Reported = added, []string{reported}
	result := Clone(report.Vitest.Results[0])
	result.Name = reported
	report.Tests = append(report.Tests, added)
	report.Files = append(report.Files, file)
	report.Vitest.Results = append(report.Vitest.Results, result)
	report.Executed += len(result.Assertions)
	report.Vitest.Total, report.Vitest.Passed = report.Executed, report.Executed
	return report
}

func TestTaskContinuationAcceptsAuthenticatedExpandedRegressionCoverage(t *testing.T) {
	c, store, _, req := taskContinuationFixture(t)
	report := continuationExpandedReport(t)
	original := c.runs[req.RunID].Inputs["tests"]
	if _, _, err := report.execution(original); err == nil {
		t.Fatal("fixture does not reproduce the exact legacy timeout-selection rejection")
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	id := req.Inputs["checks"].ArtifactID
	if err = c.disk.Write("artifacts/"+id, data, c.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	artifact := store.artifacts[id]
	artifact.Bytes = int64(len(data))
	store.artifacts[id] = artifact
	req.CheckSHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	continued, err := c.ContinueTask(req)
	if err != nil || continued.TaskGraph.Lineage.CheckSHA256 != req.CheckSHA256 || store.commits != 1 {
		t.Fatal("authenticated added regression prevented the unchanged source handoff", err)
	}
	if !reflect.DeepEqual(c.runs[req.RunID].Inputs["tests"], original) || c.familyRemaining(continued) != 28 {
		t.Fatal("report expansion changed original selectors or granted attempts")
	}
}

func TestTaskContinuationExpandedSelectionPreservesCompleteRequiredCoverage(t *testing.T) {
	for _, fault := range []string{"missing-original", "reordered-original", "duplicate", "pattern", "outside", "too-many", "too-long", "missing-file", "missing-assertions", "wrong-count", "incomplete-typecheck"} {
		t.Run(fault, func(t *testing.T) {
			report := continuationExpandedReport(t)
			required := Text(`["src/board.test.ts"]`)
			switch fault {
			case "missing-original":
				report.Tests = report.Tests[1:]
			case "reordered-original":
				required = Text(`["src/board.test.ts","src/another.test.ts"]`)
				report.Tests = []string{"src/another.test.ts", "src/board.test.ts", report.Tests[1]}
			case "duplicate":
				report.Tests[1] = report.Tests[0]
			case "pattern":
				report.Tests[1] = "src/**/*.test.ts"
			case "outside":
				report.Tests[1] = "src/../other.test.ts"
			case "too-many":
				for i := range 32 {
					report.Tests = append(report.Tests, fmt.Sprintf("src/extra%d.test.ts", i))
				}
			case "too-long":
				report.Tests[1] = "src/" + strings.Repeat("a", 4096) + ".test.ts"
			case "missing-file":
				report.Files = report.Files[:1]
			case "missing-assertions":
				report.Vitest.Results[1].Assertions = nil
			case "wrong-count":
				report.Executed++
			case "incomplete-typecheck":
				report.Typecheck.Complete = false
			}
			selection, err := continuationFrontendSelection(required, report.Tests)
			if err == nil {
				_, _, err = report.execution(selection)
			}
			if err == nil {
				t.Fatal("expanded inventory bypassed required complete coverage", fault)
			}
		})
	}
}
