package runner_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type controlledGraph struct {
	runner.Executor
	mu           sync.Mutex
	started      chan runner.Assignment
	holds        map[string]chan struct{}
	byID         map[string]runner.Assignment
	tasks        []runner.RoleTask
	projectTasks map[string][]runner.RoleTask
	stopped      map[string]bool
	stopFailures int
	delegations  map[string][]runner.RoleTask
}

func (e *controlledGraph) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	if a.TaskContext.TaskID == "root" {
		if len(a.TaskContext.Results) == 0 {
			tasks := e.tasks
			if selected, ok := e.projectTasks[a.ProjectID]; ok {
				tasks = selected
			}
			return graphAction("delegate", tasks...), nil
		}
		return graphFinal("integrated result"), nil
	}
	if tasks, ok := e.delegations[a.TaskContext.TaskID]; ok {
		if len(a.TaskContext.Results) == 0 {
			return graphAction("delegate", tasks...), nil
		}
		return graphFinal("accepted section"), nil
	}
	e.mu.Lock()
	e.holds[a.AttemptID] = make(chan struct{})
	e.byID[a.AttemptID] = a
	e.mu.Unlock()
	e.started <- a
	return runner.Observation{State: runner.Running}, nil
}

func (e *controlledGraph) Observe(_ context.Context, id string) (runner.Observation, error) {
	e.mu.Lock()
	hold, assignment, stopped := e.holds[id], e.byID[id], e.stopped[id]
	e.mu.Unlock()
	if hold == nil {
		return runner.Observation{}, fmt.Errorf("unknown graph attempt %s", id)
	}
	if stopped {
		return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "stopped"}}, nil
	}
	select {
	case <-hold:
		return graphFinal(assignment.TaskContext.TaskID), nil
	default:
		return runner.Observation{State: runner.Running}, nil
	}
}

func (e *controlledGraph) Stop(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.holds[id] == nil {
		return runner.ErrNotFound
	}
	if e.stopFailures > 0 {
		e.stopFailures--
		return fmt.Errorf("temporary stop failure")
	}
	if e.stopped == nil {
		e.stopped = map[string]bool{}
	}
	e.stopped[id] = true
	return nil
}

func (e *controlledGraph) settle(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	close(e.holds[id])
}

func (e *controlledGraph) next(t *testing.T) runner.Assignment {
	t.Helper()
	select {
	case assignment := <-e.started:
		return assignment
	case <-time.After(3 * time.Second):
		t.Fatal("expected eligible task was not dispatched")
		return runner.Assignment{}
	}
}

func TestGeneralTasksParallelClaimsAndWholeWorkspaceReaders(t *testing.T) {
	s := graphSnapshot(t)
	p := s.Projects["demo"]
	for role, access := range map[string]string{"writer": "workspace-write", "reviewer": "workspace-read"} {
		r := p.Roles[role]
		r.Execution.Access = access
		p.Roles[role] = r
	}
	proc := p.Processes["demo"]
	proc.Tasks.MaxConcurrent = 4
	p.Processes["demo"] = proc
	s.Projects["demo"] = p
	one, overlap, disjoint, read, note := graphTask("one", "writer"), graphTask("overlap", "writer"), graphTask("disjoint", "writer"), graphTask("read", "reviewer"), graphTask("note", "planner")
	one.WritePaths, overlap.WritePaths, disjoint.WritePaths = []string{"backend"}, []string{"backend/api"}, []string{"frontend"}
	var executor *controlledGraph
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		executor = &controlledGraph{
			Executor: e,
			started:  make(chan runner.Assignment, 8),
			holds:    map[string]chan struct{}{},
			byID:     map[string]runner.Assignment{},
			tasks:    []runner.RoleTask{one, overlap, disjoint, read, note},
		}
		return executor
	})
	defer r.close()
	root := begin(t, r.core, "parallel", "demo")
	t.Cleanup(func() {
		if t.Failed() {
			state, _ := r.core.Get(root.ID)
			t.Logf("dispatch failure: %s %s tasks=%+v", state.Status, state.Reason, state.TaskStatuses)
		}
	})
	active := map[string]runner.Assignment{}
	for range 3 {
		a := executor.next(t)
		active[a.TaskContext.TaskID] = a
		if a.TaskContext.TaskID != "note" && (a.WorkDir != p.Workspace || a.Repository != p.Workspace || a.Source != nil) {
			t.Fatalf("general task did not inherit its shared authoritative workspace: %+v", a)
		}
	}
	if len(active) != 3 || active["one"].AttemptID == "" || active["disjoint"].AttemptID == "" || active["note"].AttemptID == "" {
		t.Fatalf("conflicting work dispatched while disjoint work waited: %+v", active)
	}
	state, _ := r.core.Get(root.ID)
	for _, task := range state.TaskStatuses {
		if (task.ID == "overlap" || task.ID == "read") && task.Attempts != 0 {
			t.Fatalf("overlapping writer or whole-workspace reader ran concurrently with a writer: %+v", task)
		}
	}
	executor.settle(active["one"].AttemptID)
	next := executor.next(t)
	if next.TaskContext.TaskID != "overlap" {
		t.Fatalf("reader ran while another disjoint writer was active: %s", next.TaskContext.TaskID)
	}
	executor.settle(next.AttemptID)
	executor.settle(active["disjoint"].AttemptID)
	reader := executor.next(t)
	if reader.TaskContext.TaskID != "read" {
		t.Fatalf("expected final reader, got %s", reader.TaskContext.TaskID)
	}
	executor.settle(reader.AttemptID)
	executor.settle(active["note"].AttemptID)
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" {
		t.Fatalf("shared workspace graph did not finish: %s", done.Reason)
	}
}

func TestGeneralTasksPauseSteerAndReassignFutureVisit(t *testing.T) {
	s := graphSnapshot(t)
	project := s.Projects["demo"]
	role := project.Roles["planner"]
	role.Profiles = map[string]runner.Profile{"primary": *role.Execution, "alternate": *role.Execution}
	role.Execution, role.DefaultProfile = nil, "primary"
	project.Roles["planner"] = role
	s.Projects["demo"] = project
	var mu sync.Mutex
	var future runner.Assignment
	stages := 0
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		return graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			stages++
			if stages == 1 {
				return graphReturned(runner.StepResult{Outcome: "ready", TaskAction: &runner.TaskAction{Kind: "ask_human", Question: "Which style?"}})
			}
			future = a
			return graphFinal("complete")
		}}
	})
	defer r.close()
	root := begin(t, r.core, "intervention", "demo")
	state := await(t, r.core, root.ID, func(r runner.Run) bool {
		return r.TaskGraph.Phase == runner.TaskWaitingInput && len(r.Attempts) == 1 && r.Attempts[0].Released
	})
	intervene := func(action, text, profile string) {
		t.Helper()
		var err error
		state, err = r.core.InterveneTask(runner.TaskInterventionRequest{
			RequestID: action, RunID: root.ID, Revision: state.Revision, Action: action, Text: text, Profile: profile,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	intervene("pause", "", "")
	intervene("steer", "Keep it concise", "")
	intervene("reassign", "", "alternate")
	intervene("answer", "Technical", "")
	state, _ = r.core.Get(root.ID)
	if len(state.Attempts) != 1 || !state.TaskGraph.Paused {
		t.Fatal("paused task dispatched after an answer")
	}
	intervene("resume", "", "")
	done := await(t, r.core, root.ID, released)
	mu.Lock()
	defer mu.Unlock()
	if done.Status != "Completed" || len(done.Attempts) != 2 || done.Attempts[0].Profile != "" || done.Attempts[1].Profile != "alternate" || future.TaskContext.Answer != "Technical" || !strings.Contains(future.TaskContext.Directive, "Keep it concise") {
		t.Fatalf("intervention lost old facts or did not affect the next visit: %+v future=%+v", done, future)
	}
}

func TestGeneralTasksWorkspaceAliasesShareWriterClaims(t *testing.T) {
	s := graphSnapshot(t)
	first := s.Projects["demo"]
	role := first.Roles["writer"]
	role.Execution.Access = "workspace-write"
	first.Roles["writer"] = role
	s.Projects["demo"] = first
	alias := filepath.Join(t.TempDir(), "same-project")
	if err := os.Symlink(first.Workspace, alias); err != nil {
		t.Fatal(err)
	}
	second := runner.Clone(first)
	second.ID, second.Workspace = "alias", alias
	s.Projects["alias"] = second
	write := graphTask("write", "writer")
	write.WritePaths = []string{"."}
	var executor *controlledGraph
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		executor = &controlledGraph{Executor: e, started: make(chan runner.Assignment, 4), holds: map[string]chan struct{}{}, byID: map[string]runner.Assignment{}, tasks: []runner.RoleTask{write}}
		return executor
	})
	defer r.close()
	rootOne := begin(t, r.core, "first-project", "demo")
	firstWriter := executor.next(t)
	rootTwo := begin(t, r.core, "second-project", "alias")
	state := await(t, r.core, rootTwo.ID, func(r runner.Run) bool { return len(r.TaskStatuses) == 1 })
	if state.Repository != first.Workspace || state.TaskStatuses[0].Attempts != 0 {
		t.Fatalf("workspace alias admitted a second conflicting writer: %+v", state)
	}
	executor.settle(firstWriter.AttemptID)
	secondWriter := executor.next(t)
	if secondWriter.WorkDir != firstWriter.WorkDir || secondWriter.ProjectID != "alias" {
		t.Fatalf("aliased project changed physical workspace or project identity: %+v", secondWriter)
	}
	executor.settle(secondWriter.AttemptID)
	for _, root := range []runner.Run{rootOne, rootTwo} {
		if done := await(t, r.core, root.ID, released); done.Status != "Completed" {
			t.Fatalf("aliased graph failed: %s", done.Reason)
		}
	}
}

func TestGeneralTasksNestedProjectScopesPreserveSafeConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name, outerRole, claim, innerRole string
		blocked                           bool
	}{
		{"nested writer", "writer", "frontend", "writer", true},
		{"nested reader", "writer", "frontend", "reviewer", true},
		{"outer reader", "reviewer", "", "writer", true},
		{"symlink scope", "writer", "linked-frontend", "writer", true},
		{"disjoint source", "writer", "backend", "writer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := graphSnapshot(t)
			outer := s.Projects["demo"]
			for _, dir := range []string{"frontend", "backend"} {
				if err := os.Mkdir(filepath.Join(outer.Workspace, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(filepath.Join(outer.Workspace, "frontend"), filepath.Join(outer.Workspace, "linked-frontend")); err != nil {
				t.Fatal(err)
			}
			for name, access := range map[string]string{"writer": "workspace-write", "reviewer": "workspace-read"} {
				role := outer.Roles[name]
				role.Execution.Access = access
				outer.Roles[name] = role
			}
			s.Projects["demo"] = outer
			inner := runner.Clone(outer)
			inner.ID, inner.Workspace = "inner", filepath.Join(outer.Workspace, "frontend")
			s.Projects["inner"] = inner
			outerTask, innerTask := graphTask("outer", tc.outerRole), graphTask("inner", tc.innerRole)
			if tc.claim != "" {
				outerTask.WritePaths = []string{tc.claim}
			}
			if tc.innerRole == "writer" {
				innerTask.WritePaths = []string{"."}
			}
			var executor *controlledGraph
			r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
				executor = &controlledGraph{
					Executor: e, started: make(chan runner.Assignment, 8), holds: map[string]chan struct{}{}, byID: map[string]runner.Assignment{},
					projectTasks: map[string][]runner.RoleTask{
						"demo":  {outerTask},
						"inner": {innerTask, graphTask("sentinel", "planner")},
					},
				}
				return executor
			})
			defer r.close()
			outerRoot := begin(t, r.core, "outer-scope", "demo")
			activeOuter := executor.next(t)
			innerRoot := begin(t, r.core, "inner-scope", "inner")
			active := map[string]runner.Assignment{}
			count := 2
			if tc.blocked {
				count = 1
			}
			for range count {
				a := executor.next(t)
				active[a.TaskContext.TaskID] = a
			}
			if active["sentinel"].AttemptID == "" {
				t.Fatal("independent task did not pass the source task's dispatch position")
			}
			state, _ := r.core.Get(innerRoot.ID)
			for _, child := range state.TaskStatuses {
				if child.ID == "inner" && (child.Attempts == 0) != tc.blocked {
					t.Fatalf("physical nested scope admission was wrong: %+v", child)
				}
			}
			executor.settle(activeOuter.AttemptID)
			if tc.blocked {
				a := executor.next(t)
				if a.TaskContext.TaskID != "inner" {
					t.Fatalf("expected released nested source task, got %s", a.TaskContext.TaskID)
				}
				active["inner"] = a
			}
			for _, a := range active {
				executor.settle(a.AttemptID)
			}
			for _, root := range []runner.Run{outerRoot, innerRoot} {
				if done := await(t, r.core, root.ID, released); done.Status != "Completed" {
					t.Fatalf("nested scope graph did not finish: %s", done.Reason)
				}
			}
		})
	}
}

func TestGeneralTasksCancelReportsAndSettlesWholeFamily(t *testing.T) {
	var executor *controlledGraph
	r := openRig(t, t.TempDir(), graphSnapshot(t), func(e runner.Executor) runner.Executor {
		executor = &controlledGraph{Executor: e, started: make(chan runner.Assignment, 4), holds: map[string]chan struct{}{}, byID: map[string]runner.Assignment{}, tasks: []runner.RoleTask{graphTask("one", "writer"), graphTask("two", "writer")}}
		return executor
	})
	defer r.close()
	root := begin(t, r.core, "cancel-family", "demo")
	executor.next(t)
	executor.next(t)
	await(t, r.core, root.ID, func(r runner.Run) bool { return runner.Summarize(r, time.Now()).Progress.Active == 2 })
	cancelled, err := r.core.Cancel(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary := runner.Summarize(cancelled, time.Now()); summary.Attempts != 3 || len(cancelled.TaskStatuses) != 2 || summary.Progress.Active != 2 {
		t.Fatalf("cancellation response omitted active task family: %+v", summary)
	}
	done := await(t, r.core, root.ID, func(r runner.Run) bool {
		if !r.Terminal() {
			return false
		}
		for _, child := range r.TaskStatuses {
			if child.Status != "Cancelled" || child.Unsettled {
				return false
			}
		}
		return true
	})
	if again, err := r.core.Cancel(root.ID); err != nil || runner.Summarize(again, time.Now()).Attempts != runner.Summarize(done, time.Now()).Attempts {
		t.Fatal("settled cancellation replay lost family accounting", err)
	}
}

func TestGeneralTasksRepeatRootCancelRenewsUnsettledChildStops(t *testing.T) {
	var executor *controlledGraph
	r := openRig(t, t.TempDir(), graphSnapshot(t), func(e runner.Executor) runner.Executor {
		executor = &controlledGraph{
			Executor: e, started: make(chan runner.Assignment, 2), holds: map[string]chan struct{}{}, byID: map[string]runner.Assignment{},
			tasks: []runner.RoleTask{graphTask("section", "planner")}, stopFailures: 3,
			delegations: map[string][]runner.RoleTask{"section": {graphTask("child", "writer")}},
		}
		return executor
	})
	defer r.close()
	root := begin(t, r.core, "repeat-cancel", "demo")
	assignment := executor.next(t)
	if _, err := r.core.Cancel(root.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, assignment.RunID, func(r runner.Run) bool { return strings.Contains(r.Reason, "stop retries exhausted") })
	if _, err := r.core.Cancel(root.ID); err != nil {
		t.Fatal(err)
	}
	child := await(t, r.core, assignment.RunID, released)
	if child.Status != "Cancelled" || child.Attempts[0].StopAttempts != 4 {
		t.Fatalf("terminal root prevented explicit renewal of unsettled child cancellation: %+v", child)
	}
}

func TestGeneralTasksShareOneActiveAttemptLimitAcrossFamilies(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.MaxActiveAttempts = 2
	second := runner.Clone(s.Projects["demo"])
	second.ID = "second"
	s.Projects["second"] = second
	var executor *controlledGraph
	r := openRig(t, t.TempDir(), s, func(e runner.Executor) runner.Executor {
		executor = &controlledGraph{
			Executor: e, started: make(chan runner.Assignment, 8), holds: map[string]chan struct{}{}, byID: map[string]runner.Assignment{},
			projectTasks: map[string][]runner.RoleTask{
				"demo":   {graphTask("one", "writer"), graphTask("after-one", "writer", "one")},
				"second": {graphTask("one", "writer"), graphTask("two", "writer")},
			},
		}
		return executor
	})
	defer r.close()
	firstRoot := begin(t, r.core, "first-capped-family", "demo")
	first := executor.next(t)
	secondRoot := begin(t, r.core, "second-capped-family", "second")
	t.Cleanup(func() {
		if t.Failed() {
			for _, id := range []string{firstRoot.ID, secondRoot.ID} {
				state, _ := r.core.Get(id)
				t.Logf("dispatch failure: %s %s tasks=%+v", state.Status, state.Reason, state.TaskStatuses)
			}
		}
	})
	secondWorker := executor.next(t)
	if first.ProjectID == secondWorker.ProjectID {
		t.Fatal("machine limit unnecessarily serialized independent families")
	}
	status := r.core.Status()
	if status.MaxActiveAttempts != 2 || status.ActiveAttempts != 2 {
		t.Fatalf("global worker budget omitted reserved attempts or double-counted descendants: %+v", status)
	}
	state, _ := r.core.Get(secondRoot.ID)
	for _, task := range state.TaskStatuses {
		if task.RunID != secondWorker.RunID && (task.Attempts != 0 || task.Status != "Queued") {
			t.Fatalf("another family exceeded the machine budget instead of queueing: %+v", task)
		}
	}
	executor.settle(secondWorker.AttemptID)
	third := executor.next(t)
	if third.ProjectID != "second" || r.core.Status().ActiveAttempts > 2 {
		t.Fatal("released capacity did not resume the next eligible queued task")
	}
	executor.settle(third.AttemptID)
	executor.settle(first.AttemptID)
	last := executor.next(t)
	if last.TaskContext.TaskID != "after-one" || r.core.Status().ActiveAttempts > 2 {
		t.Fatal("dependency release bypassed the global cap")
	}
	executor.settle(last.AttemptID)
	for _, root := range []runner.Run{firstRoot, secondRoot} {
		if done := await(t, r.core, root.ID, released); done.Status != "Completed" {
			t.Fatalf("queue capacity became a workflow halt: %s", done.Reason)
		}
	}
}
