package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type graphExecutor struct {
	runner.Executor
	start func(runner.Assignment) runner.Observation
}

func (e graphExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	return e.start(a), nil
}

func graphReturned(result runner.StepResult) runner.Observation {
	return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "returned", Result: &result}}
}

func graphFinal(text string) runner.Observation {
	return graphReturned(runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"result": runner.Text(text)}})
}

func graphAction(kind string, tasks ...runner.RoleTask) runner.Observation {
	return graphReturned(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: kind, Tasks: tasks}})
}

func graphTask(id, role string, deps ...string) runner.RoleTask {
	return runner.RoleTask{
		ID:         id,
		Role:       role,
		Brief:      "Complete " + id,
		Inputs:     map[string]runner.Value{"text": runner.Text(id)},
		Acceptance: []string{"Deliver the requested result"},
		DependsOn:  deps,
	}
}

func graphSnapshot(t *testing.T) runner.Snapshot {
	t.Helper()
	s := snapshot(policy())
	p := s.Projects["demo"]
	p.Workspace = t.TempDir()
	var err error
	p.Workspace, err = filepath.EvalSymlinks(p.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"planner", "writer", "reviewer"} {
		role := runner.Clone(p.Roles["worker"])
		role.ID, role.Purpose = id, "Perform the assigned "+id+" task against its goal and acceptance criteria"
		p.Roles[id] = role
	}
	proc := p.Processes["demo"]
	proc.Steps[0].Role = "planner"
	proc.MaxAttempts = 24
	proc.MaxAttemptsPerStep = 1
	proc.Tasks = &runner.TaskPolicy{
		Roles:         []string{"planner", "writer", "reviewer"},
		MaxTasks:      16,
		MaxDepth:      4,
		MaxRevisions:  2,
		MaxConcurrent: 3,
	}
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	return s
}

func TestGeneralTasksNestedPlanningAndDependencyResults(t *testing.T) {
	s := graphSnapshot(t)
	var mu sync.Mutex
	starts := map[string]int{}
	var failure string
	start := func(a runner.Assignment) runner.Observation {
		mu.Lock()
		defer mu.Unlock()
		ctx := a.TaskContext
		if ctx == nil || ctx.Goal != "Hello" || len(ctx.AllowedRoles) != 3 {
			failure = "role did not receive its frozen goal and available contracts"
			return graphFinal("bad context")
		}
		starts[ctx.TaskID]++
		switch ctx.TaskID {
		case "root":
			if starts["root"] == 1 {
				return graphAction("delegate", graphTask("section-plan", "planner"), graphTask("independent", "writer"))
			}
			if len(ctx.Results) != 2 || ctx.Results[0].Status != "Completed" || ctx.RemainingAttempts != 17 {
				failure = fmt.Sprintf("root results/accounting: %+v", ctx)
			}
			return graphFinal("accepted integrated deliverable")
		case "section-plan":
			if starts[ctx.TaskID] == 1 {
				return graphAction("delegate", graphTask("draft", "writer"), graphTask("review", "reviewer", "draft"))
			}
			if len(ctx.Results) != 2 || ctx.Results[1].Role != "reviewer" {
				failure = "nested planner did not receive child results"
			}
			return graphFinal("accepted section")
		case "review":
			if len(ctx.Results) != 1 || ctx.Results[0].ID != "draft" || string(ctx.Results[0].Outputs["result"].Inline) != `"draft"` {
				failure = "reviewer did not receive actual dependency output"
			}
		}
		return graphFinal(ctx.TaskID)
	}
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor { return graphExecutor{e, start} })
	defer r.close()
	root := begin(t, r.core, "general-nested", "demo")
	done := await(t, r.core, root.ID, released)
	mu.Lock()
	defer mu.Unlock()
	if failure != "" || done.Status != "Completed" || len(done.Attempts) != 2 || len(done.TaskStatuses) != 4 || len(done.TaskUsage) != 5 || runner.Summarize(done, time.Now()).Attempts != 7 {
		t.Fatalf("nested graph: %s; status=%s reason=%s root attempts=%d tasks=%+v usage=%d", failure, done.Status, done.Reason, len(done.Attempts), done.TaskStatuses, len(done.TaskUsage))
	}
	if starts["independent"] != 1 || starts["draft"] != 1 || starts["review"] != 1 {
		t.Fatalf("completed work was replayed: %+v", starts)
	}
}

func TestGeneralTasksRejectInvalidGraphAndRecoverWithinVisit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]runner.RoleTask) []runner.RoleTask
		want   string
	}{
		{name: "unknown role", mutate: func(tasks []runner.RoleTask) []runner.RoleTask { tasks[0].Role = "external"; return tasks }, want: "allowed frozen role"},
		{name: "cycle", mutate: func(tasks []runner.RoleTask) []runner.RoleTask {
			tasks[0].DependsOn = []string{"two"}
			tasks[1].DependsOn = []string{"one"}
			return tasks
		}, want: "cyclic"},
		{name: "artifact grant", mutate: func(tasks []runner.RoleTask) []runner.RoleTask {
			tasks[0].Inputs["text"] = runner.Value{ArtifactID: "unowned/report"}
			return tasks
		}, want: "needs inline text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := graphSnapshot(t)
			project := s.Projects["demo"]
			proc := project.Processes["demo"]
			proc.MaxAttemptsPerStep = 2
			project.Processes["demo"] = proc
			s.Projects["demo"] = project
			var mu sync.Mutex
			calls := 0
			var feedback string
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
				return graphExecutor{e, func(a runner.Assignment) runner.Observation {
					mu.Lock()
					defer mu.Unlock()
					calls++
					if calls == 1 {
						return graphAction("delegate", tc.mutate([]runner.RoleTask{graphTask("one", "writer"), graphTask("two", "writer")})...)
					}
					feedback = a.TaskContext.PreviousFailure
					return graphFinal("corrected without side effects")
				}}
			})
			defer r.close()
			root := begin(t, r.core, "invalid", "demo")
			done := await(t, r.core, root.ID, released)
			if done.Status != "Completed" || !strings.Contains(feedback, tc.want) || len(done.TaskStatuses) != 0 || len(done.Attempts) != 2 {
				t.Fatalf("invalid graph dispatched or escaped validation: %+v", done)
			}
		})
	}
}

func TestGeneralTasksExplicitRevisionPreservesOtherWork(t *testing.T) {
	s := graphSnapshot(t)
	var mu sync.Mutex
	starts := map[string]int{}
	var failure string
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			id := a.TaskContext.TaskID
			starts[id]++
			if id == "root" {
				switch starts[id] {
				case 1:
					return graphAction("delegate", graphTask("good", "writer"), graphTask("repair", "writer"))
				case 2:
					return graphReturned(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{
						Kind: "revise", Reason: "The second task needs a different approach", Replace: []string{"repair"}, Tasks: []runner.RoleTask{graphTask("repair", "writer")},
					}})
				default:
					if len(a.TaskContext.Results) != 2 || a.TaskContext.Revision != 1 || a.TaskContext.Results[1].Status != "Completed" {
						failure = "revised planner results were not current"
					}
					return graphFinal("complete")
				}
			}
			if id == "repair" && starts[id] == 1 {
				return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: "initial approach failed"}}
			}
			return graphFinal(id)
		}}
	})
	defer r.close()
	root := begin(t, r.core, "revision", "demo")
	done := await(t, r.core, root.ID, released)
	mu.Lock()
	defer mu.Unlock()
	if failure != "" || done.Status != "Completed" || starts["good"] != 1 || starts["repair"] != 2 || len(done.TaskStatuses) != 3 || !done.TaskStatuses[1].Retired {
		t.Fatalf("revision replayed unrelated work or lost history: %s %+v starts=%+v", failure, done, starts)
	}
	if runner.Summarize(done, time.Now()).Attempts != 6 || slices.ContainsFunc(runner.Summarize(done, time.Now()).Progress.Attention, func(a runner.Attention) bool { return a.Kind == "blocked" }) {
		t.Fatal("historical failed child polluted current progress or accounting")
	}
}

func TestGeneralTasksHumanInputAndReplaySurviveRestart(t *testing.T) {
	s := graphSnapshot(t)
	dir := t.TempDir()
	var mu sync.Mutex
	var contexts []runner.TaskContext
	wrap := func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			contexts = append(contexts, runner.Clone(*a.TaskContext))
			mu.Unlock()
			if a.TaskContext.Answer == "" {
				return graphReturned(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "ask_human", Question: "Which audience should the draft address?"}})
			}
			return graphFinal(a.TaskContext.Answer)
		}}
	}
	r := openRig(t, dir, s, wrap)
	root := begin(t, r.core, "human", "demo")
	waiting := await(t, r.core, root.ID, func(r runner.Run) bool {
		return r.TaskGraph.Phase == runner.TaskWaitingInput && len(r.Attempts) == 1 && r.Attempts[0].Released
	})
	if runner.TaskGraphWaiting(waiting) || runner.Summarize(waiting, time.Now()).Progress.NextAction != "answer_task_question" {
		t.Fatal("human question was presented as automatic progress")
	}
	r.close()
	r = openRig(t, dir, s, wrap)
	defer r.close()
	waiting, _ = r.core.Get(root.ID)
	req := runner.TaskInterventionRequest{RequestID: "answer", RunID: root.ID, Revision: waiting.Revision, Action: "answer", Text: "Experienced Go developers"}
	if _, err := r.core.InterveneTask(req); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || len(done.Attempts) != 2 || string(done.Outputs["result"].Inline) != `"Experienced Go developers"` {
		t.Fatalf("saved answer did not resume the same goal: %+v", done)
	}
	if _, err := r.core.InterveneTask(req); err != nil {
		t.Fatalf("idempotent answer did not replay after completion: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(contexts) != 2 || contexts[1].Goal != contexts[0].Goal || contexts[1].Answer != req.Text {
		t.Fatalf("goal or answer changed during resumption: %+v", contexts)
	}
}

func TestGeneralTasksArtifactForwardingAndFamilyRetention(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.Storage.KeepRuns = "1ns"
	project := s.Projects["demo"]
	for _, id := range []string{"planner", "writer"} {
		role := project.Roles[id]
		role.Outputs = map[string]string{"result": "artifact"}
		project.Roles[id] = role
	}
	s.Projects["demo"] = project
	var disk *runner.Disk
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			if a.TaskContext.TaskID == "root" && len(a.TaskContext.Results) == 0 {
				return graphAction("delegate", graphTask("section", "planner"))
			}
			if a.TaskContext.TaskID == "section" && len(a.TaskContext.Results) == 0 {
				return graphAction("delegate", graphTask("article", "writer"))
			}
			if a.TaskContext.TaskID != "article" {
				return graphReturned(runner.StepResult{Outcome: "ready", Outputs: a.TaskContext.Results[0].Outputs})
			}
			id := a.AttemptID + "/article.md"
			if err := disk.Write("artifacts/"+id, []byte("# Accepted article\n"), disk.Policy.MaxArtifact); err != nil {
				return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: err.Error()}}
			}
			return graphReturned(runner.StepResult{Outcome: "ready", Outputs: map[string]runner.Value{"result": {ArtifactID: id}}})
		}}
	})
	disk = r.disk
	defer r.close()
	root := begin(t, r.core, "artifact-family", "demo")
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || done.Outputs["result"].ArtifactID == "" {
		t.Fatalf("forwarding accepted child artifact failed: %+v", done)
	}
	if _, err := r.store.Artifact(t.Context(), done.Outputs["result"].ArtifactID); err != nil {
		t.Fatal(err)
	}
	if err := r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if len(r.core.Status().Runs) != 0 {
		t.Fatal("internal artifact references prevented atomic family collection")
	}
	if _, err := os.Stat(filepath.Join(disk.Root, "artifacts", done.Outputs["result"].ArtifactID)); !os.IsNotExist(err) {
		t.Fatalf("accepted artifact remained after its complete family expired: %v", err)
	}
}

func TestGeneralTasksReserveExactGoalBudgetBeforeDelegating(t *testing.T) {
	for _, limit := range []int{3, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s := graphSnapshot(t)
			p := s.Projects["demo"]
			proc := p.Processes["demo"]
			proc.MaxAttempts = limit
			p.Processes["demo"] = proc
			s.Projects["demo"] = p
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
				return graphExecutor{e, func(a runner.Assignment) runner.Observation {
					if a.TaskContext.TaskID == "root" && len(a.TaskContext.Results) == 0 {
						return graphAction("delegate", graphTask("one", "writer"), graphTask("two", "writer"))
					}
					return graphFinal("complete")
				}}
			})
			defer r.close()
			root := begin(t, r.core, "budget", "demo")
			done := await(t, r.core, root.ID, released)
			if limit == 3 {
				if done.Status != "Failed" || len(done.TaskStatuses) != 0 || !strings.Contains(done.Reason, "completion attempts") {
					t.Fatalf("unfinishable work was admitted: %+v", done)
				}
			} else if done.Status != "Completed" || runner.Summarize(done, time.Now()).Attempts != limit {
				t.Fatalf("exact-budget graph was overcharged or halted: %+v", done)
			}
		})
	}
}

func TestGeneralTasksInterventionHasNoHidden17ReceiptCeiling(t *testing.T) {
	r := openRig(t, t.TempDir(), graphSnapshot(t), func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			return graphReturned(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "ask_human", Question: "Choose the audience"}})
		}}
	})
	defer r.close()
	root := begin(t, r.core, "many-interventions", "demo")
	state := await(t, r.core, root.ID, func(r runner.Run) bool {
		return r.TaskGraph.Phase == runner.TaskWaitingInput && len(r.Attempts) == 1 && r.Attempts[0].Released
	})
	for i := range 24 {
		action := "pause"
		if i%2 == 1 {
			action = "resume"
		}
		var err error
		state, err = r.core.InterveneTask(runner.TaskInterventionRequest{
			RequestID: fmt.Sprintf("intervention-%d", i), RunID: root.ID, Revision: state.Revision, Action: action,
		})
		if err != nil {
			t.Fatalf("intervention %d unexpectedly halted: %v", i, err)
		}
	}
	if len(state.Attempts) != 1 || state.TaskGraph.Phase != runner.TaskWaitingInput {
		t.Fatal("control operations consumed attempts or changed the retained human question")
	}
}

func TestGeneralTasksRejectUnrelatedArtifactInSameProject(t *testing.T) {
	s := graphSnapshot(t)
	project := s.Projects["demo"]
	writer := project.Roles["writer"]
	writer.Inputs["evidence"] = "artifact"
	project.Roles["writer"] = writer
	s.Projects["demo"] = project
	id := "unrelated_attempt/report"
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			task := graphTask("steal", "writer")
			task.Inputs["evidence"] = runner.Value{ArtifactID: id}
			return graphAction("delegate", task)
		}}
	})
	defer r.close()
	if err := r.disk.Write("artifacts/"+id, []byte("Unrelated private result"), r.disk.Policy.MaxArtifact); err != nil {
		t.Fatal(err)
	}
	producer := runner.Run{RecordFormat: 3, ID: "unrelated", ProjectID: "demo", Status: "Completed"}
	if err := r.store.Save(t.Context(), producer, []runner.Artifact{{ID: id, RunID: producer.ID, ProjectID: producer.ProjectID, Bytes: 24}}, nil); err != nil {
		t.Fatal(err)
	}
	root := begin(t, r.core, "authority", "demo")
	done := await(t, r.core, root.ID, released)
	if done.Status != "Failed" || len(done.TaskStatuses) != 0 || !strings.Contains(done.Reason, "not available through this task's inputs or declared results") {
		t.Fatalf("same project alone granted artifact authority: %+v", done)
	}
}

func TestGeneralTasksFailedAttemptRetainsProvenancePastLogExpiry(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.Storage.Logs.KeepFor = "1ns"
	var disk *runner.Disk
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			id := a.AttemptID + "/provenance.json"
			if err := disk.Write("artifacts/"+id, []byte(`{"harness":"fixture","observed":"failed"}`), disk.Policy.MaxArtifact); err != nil {
				return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "failed", Reason: err.Error()}}
			}
			return runner.Observation{
				State:     runner.Finished,
				End:       &runner.ExecutionEnd{Kind: "failed", Reason: "Meaningful harness failure"},
				Effective: &runner.ExecutionFacts{Provenance: id},
			}
		}}
	})
	disk = r.disk
	defer r.close()
	root := begin(t, r.core, "failed-provenance", "demo")
	done := await(t, r.core, root.ID, released)
	id := done.Attempts[0].Effective.Provenance
	if done.Status != "Failed" || done.Reason != "Meaningful harness failure" {
		t.Fatalf("provenance changed failure semantics: %+v", done)
	}
	if _, err := r.store.Artifact(t.Context(), id); err != nil {
		t.Fatal("failed execution provenance was not registered", err)
	}
	if err := r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := disk.ArtifactSize(id); err != nil {
		t.Fatal("log expiry erased retained execution provenance", err)
	}
}

func TestGeneralTasksExposeSelectedRoleCapabilitiesWithoutPrivateConfiguration(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	role := p.Roles["writer"]
	role.Execution = nil
	role.Profiles = map[string]runner.Profile{"selected": {
		Harness: "native", Model: "writer-model", Access: "workspace-write", Reasoning: "high", Instructions: "PRIVATE_ROLE_INSTRUCTIONS",
		Native: &runner.NativeProfile{
			Harness: "codex", Network: true,
			Tools: runner.CapabilitySelection{Mode: "native"}, Skills: runner.CapabilitySelection{Mode: "native"},
			RuntimeWritePaths: []string{"/private/runtime-cache"},
		},
	}}
	p.Roles["writer"], p.Profiles["writer"] = role, "selected"
	reviewer := p.Roles["reviewer"]
	reviewer.Execution = &runner.Profile{Harness: "native", Model: "review-model", Access: "workspace-read", Native: &runner.NativeProfile{
		Harness: "claude", Tools: runner.CapabilitySelection{Mode: "selected", Names: []string{"Read", "Grep"}}, Skills: runner.CapabilitySelection{Mode: "none"},
	}}
	p.Roles["reviewer"] = reviewer
	s.Projects["demo"] = p
	var observed *runner.TaskContext
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			observed = runner.Clone(a.TaskContext)
			return graphFinal("complete")
		}}
	})
	defer r.close()
	root := begin(t, r.core, "role-capabilities", "demo")
	await(t, r.core, root.ID, released)
	contracts := map[string]runner.TaskRoleContract{}
	for _, role := range observed.AllowedRoles {
		contracts[role.ID] = role
	}
	writer := contracts["writer"]
	if writer.Profile != "selected" || writer.Harness != "codex" || writer.Model != "writer-model" || writer.Access != "workspace-write" || writer.Tools == nil || writer.Tools.Mode != "native" || !writer.Network {
		t.Fatalf("planner did not receive selected writer execution facts: %+v", writer)
	}
	if got := contracts["reviewer"]; got.Harness != "claude" || got.Model != "review-model" || got.Access != "workspace-read" || got.Tools == nil || !slices.Equal(got.Tools.Names, []string{"Read", "Grep"}) || got.Skills.Mode != "none" {
		t.Fatalf("planner did not receive selected reviewer capability facts: %+v", got)
	}
	encoded, err := json.Marshal(observed)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_ROLE_INSTRUCTIONS") || strings.Contains(string(encoded), "/private/runtime-cache") {
		t.Fatal("planner role catalog copied private instructions or runtime cache configuration", err)
	}
}

func TestGeneralTasksRejectSourceRefBeforeAdmissionPreservingLegacyReplay(t *testing.T) {
	s := graphSnapshot(t)
	project := s.Projects["demo"]
	project.Source = &runner.SourceSettings{Ref: "main"}
	linear := runner.Clone(project.Processes["demo"])
	linear.ID, linear.Tasks = "linear", nil
	project.Processes["linear"] = linear
	s.Projects["demo"] = project
	marker := filepath.Join(project.Workspace, "untouched.txt")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	started := make(chan runner.Assignment, 1)
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			started <- a
			return graphFinal("complete")
		}}
	})
	defer r.close()
	req := runner.StartRequest{RequestID: "selected-ref", Project: "demo", SourceRef: "feature/requested", Inputs: map[string]runner.Value{"text": runner.Text("work")}}
	if _, err := r.core.Start(req); err == nil || !strings.Contains(err.Error(), "branch selection is unsupported") {
		t.Fatalf("general task silently accepted an unsupported branch selection: %v", err)
	}
	if status := r.core.Status(); status.TotalRuns != 0 || status.ActiveAttempts != 0 {
		t.Fatalf("rejected source selection reserved execution: %+v", status)
	}
	if saved, err := r.store.Load(t.Context()); err != nil || len(saved) != 0 {
		t.Fatal("rejected source selection persisted run or attempt state", err)
	}
	if _, err := r.store.FindReceipt(t.Context(), req.RequestID); !errors.Is(err, runner.ErrNotFound) {
		t.Fatal("rejected source selection consumed its request identity", err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "unchanged" || r.disk.Used() != 0 {
		t.Fatal("rejected source selection changed workspace or managed resources", err)
	}
	req.Process = "linear"
	legacy, err := r.core.Start(req)
	if err != nil {
		t.Fatal("legacy source workflow rejected its supported explicit ref", err)
	}
	done := await(t, r.core, legacy.ID, released)
	if done.Status != "Completed" || done.Source == nil || done.Source.Ref != req.SourceRef {
		t.Fatalf("legacy source selection changed: %+v", done)
	}
	assignment := <-started
	if assignment.Source == nil || assignment.Source.Ref != req.SourceRef || assignment.TaskContext != nil {
		t.Fatal("legacy assignment lost its selected source or entered the general task path")
	}
	linear.Tasks = runner.Clone(project.Processes["demo"].Tasks)
	project.Processes["linear"] = linear
	s.Projects["demo"] = project
	r.core.Apply(s, nil)
	replay, err := r.core.Start(req)
	if err != nil || replay.ID != legacy.ID || len(replay.Attempts) != 1 || replay.Source == nil || replay.Source.Ref != req.SourceRef {
		t.Fatal("new general-task validation replaced or rejected a previously admitted request replay", err)
	}
}
