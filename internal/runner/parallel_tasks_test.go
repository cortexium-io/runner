package runner_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

// This executor makes actual overlapping dispatch observable without a model,
// while Core still owns real durable attempts, artifacts, budgets and recovery.
type taskProbe struct {
	mu           sync.Mutex
	pending      map[string]runner.Assignment
	stopped      map[string]bool
	started      chan runner.Assignment
	allow        chan struct{}
	plans        []string
	planFeedback []string
	stopFailures int
	stopCounts   map[string]int
}

func newTaskProbe() *taskProbe {
	return &taskProbe{pending: map[string]runner.Assignment{}, stopped: map[string]bool{}, stopCounts: map[string]int{}, started: make(chan runner.Assignment, 8), allow: make(chan struct{})}
}
func (p *taskProbe) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	if a.Setup.Profile.Mode == "owner" {
		p.mu.Lock()
		p.pending[a.AttemptID] = a
		p.mu.Unlock()
		p.started <- a
		return runner.Observation{State: runner.Running}, nil
	}
	return p.finished(a)
}
func (p *taskProbe) Observe(_ context.Context, id string) (runner.Observation, error) {
	p.mu.Lock()
	a, ok := p.pending[id]
	stopped := p.stopped[id]
	p.mu.Unlock()
	if !ok {
		return runner.Observation{State: runner.NotStarted}, nil
	}
	if stopped {
		return runner.Observation{State: runner.Finished, End: &runner.ExecutionEnd{Kind: "stopped"}}, nil
	}
	select {
	case <-p.allow:
		return p.finished(a)
	default:
		return runner.Observation{State: runner.Running}, nil
	}
}
func (p *taskProbe) Stop(_ context.Context, id string) error {
	p.mu.Lock()
	p.stopCounts[id]++
	if p.stopCounts[id] <= p.stopFailures {
		p.mu.Unlock()
		return errors.New("stop acknowledgement unavailable")
	}
	p.stopped[id] = true
	p.mu.Unlock()
	return nil
}
func (p *taskProbe) Release(context.Context, string) error { return nil }
func (p *taskProbe) finished(a runner.Assignment) (runner.Observation, error) {
	out := map[string]runner.Value{}
	var source *runner.SourceResult
	if a.Setup.Profile.Mode == "plan" {
		p.mu.Lock()
		var feedback string
		_ = json.Unmarshal(a.Inputs["feedback"].Inline, &feedback)
		p.planFeedback = append(p.planFeedback, feedback)
		out["tasks"] = runner.Text(p.plans[0])
		p.plans = p.plans[1:]
		p.mu.Unlock()
	}
	for name, kind := range a.OutputTypes {
		if _, exists := out[name]; exists {
			continue
		}
		switch kind {
		case "artifact":
			if err := os.MkdirAll(a.OutputDir, 0700); err != nil {
				return runner.Observation{}, err
			}
			if err := os.WriteFile(filepath.Join(a.OutputDir, name), []byte(name), 0600); err != nil {
				return runner.Observation{}, err
			}
			out[name] = runner.Value{ArtifactID: a.AttemptID + "/" + name}
		case "text":
			out[name] = runner.Text("accepted")
		}
	}
	if out["patch"].ArtifactID != "" {
		source = &runner.SourceResult{Repository: a.Repository, Base: a.Source.Ref, Tree: strings.Repeat("3", 40), PatchID: out["patch"].ArtifactID}
	}
	if a.Setup.Profile.Mode == "pin" {
		candidate := runner.TaskCandidate{Identity: a.TaskSource.Identity, RunID: a.RunID, Repository: a.Repository, Base: a.Source.Ref, Commit: strings.Repeat("2", 40), Tree: strings.Repeat("3", 40), PatchID: a.Inputs["patch"].ArtifactID}
		raw, _ := json.Marshal(candidate)
		out["candidate"] = runner.Value{Inline: raw}
	}
	zero := int64(0)
	return runner.Observation{State: runner.Finished, Source: source, Usage: runner.Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero}, End: &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: "ready", Outputs: out}}}, nil
}

func plannedSnapshot(items string) runner.Snapshot {
	s := snapshot(policy())
	s.Instance.Concurrency = 1
	s.Instance.TaskConcurrency = 2
	role := func(id string, inputs, outputs map[string]string) runner.Role {
		return runner.Role{ID: id, Inputs: inputs, Outputs: outputs, Execution: &runner.Profile{Harness: "task-source", Mode: id}}
	}
	seed := runner.Step{ID: "seed", Role: "seed", Inputs: map[string]runner.Binding{"dependencies": {FromInput: "_task_dependencies"}}}
	owner := runner.Step{ID: "owner", Role: "owner", Inputs: map[string]runner.Binding{"task": {FromItem: "task"}, "paths": {FromItem: "paths"}, "manifest": {FromStep: "seed", Output: "manifest"}}}
	pin := runner.Step{ID: "pin", Role: "pin", Inputs: map[string]runner.Binding{"patch": {FromStep: "owner", Output: "patch"}, "summary": {FromStep: "owner", Output: "summary"}}}
	proc := runner.Process{ID: "planned", Inputs: map[string]string{"request": "text", "tasks": "json"}, MaxAttempts: 20, MaxAttemptsPerStep: 1, AttemptTimeout: "3s", Steps: []runner.Step{
		{ID: "cards", ForEach: &runner.TaskGroup{Mode: "isolated", MaxParallel: 2, ScopeInput: "paths", Items: runner.Binding{FromInput: "tasks"}, Inputs: map[string]string{"task": "text", "paths": "json"}, Steps: []runner.Step{seed, owner, pin}, Output: runner.Binding{FromStep: "pin", Output: "candidate"}}},
		{ID: "join", Role: "join", Inputs: map[string]runner.Binding{"results": {FromStep: "cards", Output: "results"}}},
	}, Outputs: map[string]runner.Binding{"manifest": {FromStep: "cards", Output: "results"}}}
	project := runner.Project{ID: "demo", Workspace: "/tmp/owned", Source: &runner.SourceSettings{Ref: strings.Repeat("1", 40)}, DefaultProcess: "planned", Processes: map[string]runner.Process{"planned": proc}, Roles: map[string]runner.Role{
		"seed":  role("seed", map[string]string{"dependencies": "json"}, map[string]string{"patch": "artifact", "manifest": "artifact"}),
		"owner": role("owner", map[string]string{"task": "text", "paths": "json", "manifest": "artifact"}, map[string]string{"patch": "artifact", "summary": "text"}),
		"pin":   role("pin", map[string]string{"patch": "artifact", "summary": "text"}, map[string]string{"candidate": "json", "manifest": "artifact", "feedback": "text"}),
		"join":  role("join", map[string]string{"results": "artifact"}, map[string]string{"patch": "artifact", "manifest": "artifact", "feedback": "text"}),
	}}
	s.Projects["demo"] = project
	return s
}

const threeCards = `[{"id":"left","inputs":{"task":"left","paths":["left.go"]}},{"id":"right","inputs":{"task":"right","paths":["right.go"]}},{"id":"dependent","depends_on":["left","right"],"inputs":{"task":"combine","paths":["joined.go"]}}]`

func startCards(t *testing.T, c *runner.Core, items string) runner.Run {
	t.Helper()
	r, e := c.Start(runner.StartRequest{RequestID: "planned", Project: "demo", Inputs: map[string]runner.Value{"request": runner.Text("complete the request"), "tasks": {Inline: json.RawMessage(items)}}})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func nextCard(t *testing.T, p *taskProbe) runner.Assignment {
	t.Helper()
	select {
	case a := <-p.started:
		return a
	case <-time.After(4 * time.Second):
		t.Fatal("task was not dispatched")
		return runner.Assignment{}
	}
}

func TestIsolatedTasksOverlapAndWaitForAcceptedDependencies(t *testing.T) {
	p := newTaskProbe()
	r := openRig(t, t.TempDir(), plannedSnapshot(threeCards), func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	a, b := nextCard(t, p), nextCard(t, p)
	if a.TaskSource.Identity.ItemID == b.TaskSource.Identity.ItemID || len(a.TaskSource.Dependencies) != 0 || len(b.TaskSource.Dependencies) != 0 {
		t.Fatal("independent owners did not overlap")
	}
	select {
	case third := <-p.started:
		t.Fatal("dependent ran before accepted pins", third)
	default:
	}
	projected, e := r.core.Get(root.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(projected.TaskStatuses) != 3 {
		t.Fatal("parent omitted card statuses", projected)
	}
	close(p.allow)
	dependent := nextCard(t, p)
	if dependent.TaskSource.Identity.ItemID != "dependent" || len(dependent.TaskSource.Dependencies) != 2 {
		t.Fatal("dependency source handoff incomplete", dependent.TaskSource)
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || len(done.TaskStatuses) != 3 || len(done.Attempts) != 2 {
		t.Fatal("complete workflow failed", done.Status, done.Reason, done.TaskStatuses)
	}
	total := len(done.Attempts)
	for _, s := range done.TaskStatuses {
		total += s.Attempts
		if s.Status != "Completed" {
			t.Fatal(s)
		}
	}
	if total != 11 || total > done.Process.MaxAttempts {
		t.Fatal("family budget was not shared", total)
	}
	raw, e := r.disk.Read("artifacts/"+done.Outputs["manifest"].ArtifactID, r.disk.Policy.MaxArtifact)
	if e != nil {
		t.Fatal(e)
	}
	var manifest runner.TaskManifest
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.Items) != 3 || manifest.Request != "complete the request" {
		t.Fatal("missing authoritative plan evidence", string(raw))
	}
}

func TestParentCancellationSettlesAllParallelTasks(t *testing.T) {
	p := newTaskProbe()
	r := openRig(t, t.TempDir(), plannedSnapshot(threeCards), func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	nextCard(t, p)
	nextCard(t, p)
	if _, e := r.core.Cancel(root.ID); e != nil {
		t.Fatal(e)
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Cancelled" {
		t.Fatal(done.Status, done.Reason)
	}
	for _, s := range done.TaskStatuses {
		if s.Active || s.Status != "Cancelled" {
			t.Fatal("child did not settle", s)
		}
	}
	select {
	case a := <-p.started:
		t.Fatal("cancelled dependency dispatched", a)
	default:
	}
}

func TestRestartReconcilesParallelOwnersWithoutDuplicatingTasks(t *testing.T) {
	p := newTaskProbe()
	dir, s := t.TempDir(), plannedSnapshot(threeCards)
	wrap := func(runner.Executor) runner.Executor { return p }
	first := openRig(t, dir, s, wrap)
	root := startCards(t, first.core, threeCards)
	a, b := nextCard(t, p), nextCard(t, p)
	// The probe signals entry to Start before Core persists its acknowledgement.
	// Wait for both acknowledgements to test observation of saved running owners.
	for _, owner := range []runner.Assignment{a, b} {
		await(t, first.core, owner.RunID, func(r runner.Run) bool {
			current := r.Current()
			return current != nil && current.ID == owner.AttemptID && current.State == runner.Running
		})
	}
	before, err := first.core.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	first.close()
	second := openRig(t, dir, s, wrap)
	defer second.close()
	after, err := second.core.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, child := range before.TaskStatuses {
		if after.TaskStatuses[i].RunID != child.RunID {
			t.Fatal("restart replaced a retained child")
		}
	}
	close(p.allow)
	dependent := nextCard(t, p)
	if dependent.RunID == a.RunID || dependent.RunID == b.RunID || dependent.TaskSource.Identity.ItemID != "dependent" {
		t.Fatal("restart repeated an owner instead of observing its saved operation", dependent)
	}
	done := await(t, second.core, root.ID, released)
	if done.Status != "Completed" {
		t.Fatal(done.Status, done.Reason)
	}
	for _, child := range done.TaskStatuses {
		if child.Attempts != 3 {
			t.Fatal("restart consumed duplicate attempts", child)
		}
	}
}

func TestTaskAdmissionReservesWholeFamilyBeforeDispatch(t *testing.T) {
	p := newTaskProbe()
	s := plannedSnapshot(threeCards)
	project := s.Projects["demo"]
	process := project.Processes["planned"]
	process.MaxAttempts = 10 // Three seed/owner/pin cards + manifest + join need11.
	project.Processes["planned"] = process
	s.Projects["demo"] = project
	r := openRig(t, t.TempDir(), s, func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	blocked := await(t, r.core, root.ID, func(r runner.Run) bool { return r.Status == "Waiting" })
	if len(blocked.TaskStatuses) != 0 || !strings.Contains(blocked.Reason, "reserve") {
		t.Fatal("partial task family was dispatched", blocked)
	}
	select {
	case a := <-p.started:
		t.Fatal("under-budget plan dispatched", a)
	default:
	}
}

func TestInvalidPlanCorrectsInternallyBeforeAnyCardStarts(t *testing.T) {
	bad := `[{"id":"left","inputs":{"task":"left","paths":["same"]}},{"id":"right","inputs":{"task":"right","paths":["same/file.go"]}}]`
	p := newTaskProbe()
	p.plans = []string{bad, threeCards}
	close(p.allow)
	s := plannedSnapshot(threeCards)
	project := s.Projects["demo"]
	process := project.Processes["planned"]
	project.Roles["plan"] = runner.Role{ID: "plan", Inputs: map[string]string{"feedback": "text"}, Outputs: map[string]string{"tasks": "text"}, Execution: &runner.Profile{Harness: "task-source", Mode: "plan"}}
	process.Steps[0].ForEach.Items = runner.Binding{FromStep: "plan", Output: "tasks"}
	process.Steps[0].Routes = map[string]runner.Route{"invalid_plan": {Next: "plan", MaxTimes: 2}}
	process.Steps = append([]runner.Step{{ID: "plan", Role: "plan", Inputs: map[string]runner.Binding{"feedback": {FromStep: "cards", Output: "feedback", Previous: true, Default: &runner.Value{Inline: json.RawMessage(`""`)}}}}}, process.Steps...)
	project.Processes["planned"] = process
	s.Projects["demo"] = project
	r := openRig(t, t.TempDir(), s, func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" || done.RouteUses["cards/invalid_plan"] != 1 {
		t.Fatal("planner correction did not complete", done.Status, done.Reason)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.planFeedback) != 2 || p.planFeedback[0] != "" || !strings.Contains(p.planFeedback[1], "overlap") ||
		!strings.Contains(p.planFeedback[1], bad) || !strings.Contains(p.planFeedback[1], "No cards from this plan were admitted") {
		t.Fatal("planner did not receive bounded validation feedback", p.planFeedback)
	}
}

func TestCompletedTaskFamilyCollectsAsOneUnit(t *testing.T) {
	p := newTaskProbe()
	close(p.allow)
	s := plannedSnapshot(threeCards)
	s.Instance.Storage.KeepRuns = "1ns"
	r := openRig(t, t.TempDir(), s, func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" {
		t.Fatal(done.Status, done.Reason)
	}
	if err := r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if r.core.Status().TotalRuns != 0 {
		t.Fatal("family artifact references prevented retention", r.core.Status())
	}
	loaded, err := r.store.Load(context.Background())
	if err != nil || len(loaded) != 0 {
		t.Fatal("family remained in SQLite", loaded, err)
	}
}

func TestRepeatedParentCancelRenewsOnlyBoundedChildStops(t *testing.T) {
	p := newTaskProbe()
	p.stopFailures = 3
	r := openRig(t, t.TempDir(), plannedSnapshot(threeCards), func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	nextCard(t, p)
	nextCard(t, p)
	if _, err := r.core.Cancel(root.ID); err != nil {
		t.Fatal(err)
	}
	await(t, r.core, root.ID, func(r runner.Run) bool {
		n := 0
		for _, s := range r.TaskStatuses {
			if s.Active && strings.Contains(s.Reason, "stop retries exhausted") {
				n++
			}
		}
		return n == 2
	})
	p.mu.Lock()
	for _, n := range p.stopCounts {
		if n != 3 {
			t.Fatal("reconciliation renewed stop allowance", n)
		}
	}
	p.mu.Unlock()
	if _, err := r.core.Cancel(root.ID); err != nil {
		t.Fatal(err)
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Cancelled" {
		t.Fatal(done.Status, done.Reason)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, n := range p.stopCounts {
		if n != 4 {
			t.Fatal("explicit parent cancel did not renew child stop", n)
		}
	}
}

type validatedTaskProbe struct{ *taskProbe }

func (p validatedTaskProbe) ValidateAssignment(a runner.Assignment) error {
	return new(execution.Service).ValidateAssignment(a)
}

func TestCardOwnerAdmissionUsesDispatchInputLimits(t *testing.T) {
	items, _ := json.Marshal([]runner.TaskItem{{ID: "large", Inputs: map[string]json.RawMessage{"task": json.RawMessage(`"` + strings.Repeat("x", 5000) + `"`), "paths": json.RawMessage(`["file.go"]`)}}})
	for _, tc := range []struct {
		name         string
		instructions string
	}{
		{name: "large stageable inputs"},
		{name: "irreducible instructions", instructions: strings.Repeat("x", 9000)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTaskProbe()
			close(p.allow)
			s := plannedSnapshot(string(items))
			s.Instance.Storage.Database.MaxRecord = 40 << 10
			project := s.Projects["demo"]
			owner := project.Roles["owner"]
			owner.Purpose = "Implement the assigned card"
			owner.Execution = &runner.Profile{
				Harness: "codex", Mode: "owner", Access: "workspace-write",
				Model: "gpt-6.1-sol", Reasoning: "xhigh", MaxInput: 8192, MaxOutput: 512, Instructions: tc.instructions,
			}
			process := project.Processes["planned"]
			process.Steps[0].ForEach.Steps[1].Inputs["request"] = runner.Binding{FromInput: "request"}
			owner.Inputs["request"] = "text"
			project.Roles["owner"] = owner
			project.Processes["planned"] = process
			s.Projects["demo"] = project
			r := openRig(t, t.TempDir(), s, func(runner.Executor) runner.Executor { return validatedTaskProbe{p} })
			defer r.close()
			root, err := r.core.Start(runner.StartRequest{
				RequestID: "card-inputs", Project: "demo",
				Inputs: map[string]runner.Value{"request": runner.Text(strings.Repeat("q", 4000)), "tasks": {Inline: items}},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := await(t, r.core, root.ID, released)
			if tc.instructions == "" {
				if done.Status != "Completed" || r.core.Status().TotalRuns != 2 {
					t.Fatal("preflight rejected inputs that dispatch can stage", done.Status, done.Reason)
				}
			} else if done.Status != "Failed" || len(done.TaskStatuses) != 0 || len(done.Attempts) != 1 ||
				!strings.Contains(done.Attempts[0].End.Result.Reason, "max_input") || r.core.Status().TotalRuns != 1 {
				t.Fatal("over-budget card created work", done.Status, done.Reason, done.TaskStatuses)
			}
		})
	}
}

func TestTaskOwnersReceiveFrozenBaselineArtifactAndFamilyCycleCollects(t *testing.T) {
	p := newTaskProbe()
	s := plannedSnapshot(threeCards)
	s.Instance.Storage.KeepRuns = "1ns"
	project := s.Projects["demo"]
	process := project.Processes["planned"]
	project.Roles["baseline"] = runner.Role{ID: "baseline", Inputs: map[string]string{}, Outputs: map[string]string{"artifact": "artifact"}, Execution: &runner.Profile{Harness: "task-source", Mode: "baseline"}}
	process.Steps[0].ForEach.Evidence = map[string]runner.Binding{"baseline": {FromStep: "baseline", Output: "artifact"}}
	process.Steps[0].ForEach.Steps[1].Inputs["baseline"] = runner.Binding{FromInput: "_task_evidence_baseline"}
	owner := project.Roles["owner"]
	owner.Inputs["baseline"] = "artifact"
	project.Roles["owner"] = owner
	process.Steps = append([]runner.Step{{ID: "baseline", Role: "baseline", Inputs: map[string]runner.Binding{}}}, process.Steps...)
	project.Processes["planned"] = process
	s.Projects["demo"] = project
	r := openRig(t, t.TempDir(), s, func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, threeCards)
	first, second := nextCard(t, p), nextCard(t, p)
	baseline := first.Inputs["baseline"].ArtifactID
	if baseline == "" || baseline != second.Inputs["baseline"].ArtifactID {
		t.Fatal("owners lost original baseline artifact")
	}
	artifact, err := r.store.Artifact(context.Background(), baseline)
	if err != nil || artifact.RunID != root.ID {
		t.Fatal("baseline lost root ownership", artifact, err)
	}
	close(p.allow)
	dependent := nextCard(t, p)
	if dependent.Inputs["baseline"].ArtifactID != baseline {
		t.Fatal("dependent received a replaced baseline")
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" {
		t.Fatal(done.Status, done.Reason)
	}
	raw, err := r.disk.Read("artifacts/"+done.Outputs["manifest"].ArtifactID, r.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	var manifest runner.TaskManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Evidence["baseline"].ArtifactID != baseline {
		t.Fatal("combined review lost original evidence", string(raw))
	}
	if err := r.core.Collect(); err != nil {
		t.Fatal(err)
	}
	if r.core.Status().TotalRuns != 0 {
		t.Fatal("root/child evidence cycle prevented collection")
	}
}

func TestTaskPlanRejectsScopeShapeBeforeDispatch(t *testing.T) {
	var many []runner.TaskItem
	for card := 0; card < 2; card++ {
		paths := []string{}
		for i := 0; i < 17; i++ {
			paths = append(paths, fmt.Sprintf("card%d/file%d.go", card, i))
		}
		raw, _ := json.Marshal(paths)
		many = append(many, runner.TaskItem{ID: fmt.Sprintf("card%d", card), Inputs: map[string]json.RawMessage{"task": json.RawMessage(`"implement"`), "paths": raw}})
	}
	wide, _ := json.Marshal(many)
	for _, items := range []string{
		`[{"id":"bad","inputs":{"task":"change","paths":["file.go","file.go"]}}]`,
		`[{"id":"bad","inputs":{"task":"change","paths":["nested/.CoDeX/policy"]}}]`,
		string(wide),
	} {
		t.Run(items, func(t *testing.T) {
			p := newTaskProbe()
			r := openRig(t, t.TempDir(), plannedSnapshot(items), func(runner.Executor) runner.Executor { return p })
			defer r.close()
			root := startCards(t, r.core, items)
			done := await(t, r.core, root.ID, released)
			if done.Status != "Failed" || len(done.TaskStatuses) != 0 || len(done.Attempts) != 1 || done.Attempts[0].End.Result.Outcome != "invalid_plan" {
				t.Fatal("unexecutable scope admitted", done.Status, done.Reason)
			}
			if r.core.Status().TotalRuns != 1 {
				t.Fatal("partial child was created")
			}
		})
	}
}

func TestOverlappingUnorderedTaskScopesDispatchNothing(t *testing.T) {
	items := `[{"id":"left","inputs":{"task":"left","paths":["same"]}},{"id":"right","inputs":{"task":"right","paths":["same/file.go"]}}]`
	p := newTaskProbe()
	r := openRig(t, t.TempDir(), plannedSnapshot(items), func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, items)
	done := await(t, r.core, root.ID, released)
	if done.Status != "Failed" || !strings.Contains(done.Reason, "invalid_plan") || len(done.TaskStatuses) != 0 {
		t.Fatal("unsafe graph accepted", done)
	}
	select {
	case a := <-p.started:
		t.Fatal("unsafe plan dispatched", a)
	default:
	}
}
