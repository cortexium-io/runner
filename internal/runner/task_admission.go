package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (c *Core) admitTaskItems(root Run, state *TaskGroupState) error {
	if err := admitRunRecord(root, c.disk.Policy.Database.MaxRecord); err != nil {
		return err
	}
	for i, item := range state.Items {
		child := childForItem(root, state, item, state.Children[i].RunID)
		if err := admitRunRecord(child, c.disk.Policy.Database.MaxRecord); err != nil {
			return fmt.Errorf("task %s: %w", item.ID, err)
		}
		validator, supported := c.executor.(AssignmentValidator)
		if !supported {
			continue
		}
		// The fixed seed/owner/pin template passes seed evidence by artifact.
		// Resolve the owner's real inline inputs and exact native schema, using
		// ordinary-length artifact IDs before any accepted card can dispatch.
		seed := child.Process.Steps[0]
		outputs := map[string]Value{}
		for name, kind := range child.Roles[seed.Role].Outputs {
			if kind == "artifact" {
				outputs[name] = Value{ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/" + name}
			}
		}
		child.Attempts = []Attempt{{Step: 0, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: outputs}}}}
		child.Attempts = append(child.Attempts, Attempt{Step: 2, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "changes_requested", Outputs: map[string]Value{
			"manifest": {ArtifactID: "attempt_" + strings.Repeat("A", 26) + "/manifest"}, "feedback": Text(TaskScopeFeedback),
		}}}})
		step := child.Process.Steps[1]
		inputs, err := bindings(child, step.Inputs)
		if err != nil {
			return fmt.Errorf("task %s: %w", item.ID, err)
		}
		role := child.Roles[step.Role]
		profile, err := role.Profile(child.Profiles[step.Role])
		if err != nil {
			return err
		}
		timeout, _ := time.ParseDuration(child.Process.AttemptTimeout)
		if step.AttemptTimeout != "" {
			timeout, _ = time.ParseDuration(step.AttemptTimeout)
		}
		a := Assignment{
			AttemptID:   "attempt_" + strings.Repeat("A", 26),
			Setup:       ExecutionSetup{Profile: profile},
			Inputs:      inputs,
			Limits:      c.executionLimits(),
			Task:        role.Purpose,
			OutputTypes: role.Outputs,
			AllowHelp:   step.Escalation != nil,
			Outcomes:    stepOutcomes(step),
			Deadline:    time.Now().UTC().Add(timeout),
		}
		if err := validator.ValidateAssignment(a); err != nil {
			return fmt.Errorf("task %s owner input: %w", item.ID, err)
		}
	}
	return nil
}

func admitRunRecord(r Run, limit Bytes) error {
	state, definition, err := MarshalRunRecord(r)
	if err != nil {
		return err
	}
	if len(state) > int(limit)/2 || len(definition) > int(limit) {
		return errors.New("run state needs half-record recovery headroom and its frozen definition must fit one record; use declared artifacts for large inputs")
	}
	return nil
}

// readyTail counts the fixed successful path, not speculative worker decisions.
// The already validated release suffix contributes its bounded recovery routes.
func readyTail(r Run, start int) (int, error) {
	n, seen := 0, map[int]bool{}
	for index := start; index < len(r.Process.Steps); {
		if index < 0 || seen[index] {
			return 0, errors.New("completion reserve requires a finite ready path")
		}
		seen[index] = true
		step := r.Process.Steps[index]
		if step.ForEach != nil {
			return 0, errors.New("completion reserve cannot cross an unplanned task collection")
		}
		n++
		profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
		if err != nil {
			return 0, err
		}
		route, explicit := step.Routes["ready"]
		if profile.Harness == "release" && profile.Mode == "prepare" {
			target := taskStepIndex(r.Process, route.Next)
			uses := Clone(r.RouteUses)
			if uses == nil {
				uses = map[string]int{}
			}
			uses[step.ID+"/ready"]++
			remaining, err := releaseCompletionAttempts(r, target, uses)
			return n + remaining, err
		}
		if !explicit {
			index++
			continue
		}
		if route.Approval || route.Next == "$complete" {
			return n, nil
		}
		if route.Next == "$fail" {
			return 0, errors.New("ready path cannot complete its configured process")
		}
		index = taskStepIndex(r.Process, route.Next)
	}
	return n, nil
}

func taskStepIndex(p Process, id string) int {
	for i, step := range p.Steps {
		if step.ID == id {
			return i
		}
	}
	return -1
}

func readyNext(p Process, index int) int {
	if index < 0 || index >= len(p.Steps) {
		return len(p.Steps)
	}
	route, ok := p.Steps[index].Routes["ready"]
	if !ok {
		return index + 1
	}
	if route.Next == "$complete" {
		return len(p.Steps)
	}
	return taskStepIndex(p, route.Next)
}

func (c *Core) taskGroupAdmission(r Run, count, pendingAttempts int) error {
	g := r.Process.Steps[r.Step].ForEach
	if g == nil || g.Mode != "isolated" || g.MaxParallel < 1 || c.snapshot.Instance.TaskConcurrency < 1 {
		return errors.New("isolated tasks need explicitly configured task concurrency")
	}
	reserved := len(c.runs)
	for _, other := range c.runs {
		for _, group := range other.TaskGroups {
			for _, child := range group.Children {
				if _, exists := c.runs[child.RunID]; !exists {
					reserved++
				}
			}
		}
	}
	if reserved+count > c.disk.Policy.Database.MaxRuns {
		return errors.New("task plan needs more retained run slots; preserve history and resolve capacity before dispatch")
	}
	tail, err := readyTail(r, readyNext(r.Process, r.Step))
	if err != nil {
		return err
	}
	child := Run{Process: Process{Steps: g.Steps}, Roles: r.Roles, Profiles: r.Profiles}
	minimum, err := readyTail(child, 0)
	if err != nil {
		return err
	}
	if err := c.familyReserve(r, count*minimum+1+pendingAttempts, tail); err != nil {
		return err
	}
	// Reserve bounded metadata and one output artifact per isolated source lane.
	need := int64(count+1) * int64(3*c.disk.Policy.Database.MaxRecord+c.disk.Policy.MaxArtifact+c.disk.Policy.Logs.MaxPerAttempt)
	if c.disk.Used()+need > int64(c.disk.Policy.MaxManaged-c.disk.Policy.Database.MaxSize)-4096 {
		return fmt.Errorf("%w: task fan-out recovery reserve", ErrCapacity)
	}
	return nil
}

func (c *Core) taskCompletionReserve(r Run) (int, error) {
	if r.Task == nil {
		if len(r.TaskGroups) == 0 && (r.CorrectionRecovery == nil || !r.CorrectionRecovery.WithinBudget) {
			return 0, nil
		}
		return readyTail(r, readyNext(r.Process, r.Step))
	}
	root, err := c.taskRoot(r)
	if err != nil {
		return 0, err
	}
	group := root.TaskGroups[r.Task.Identity.GroupStep]
	tail, err := readyTail(root, readyNext(root.Process, root.Step))
	if err != nil {
		return 0, err
	}
	tail++ // The task collection's retained manifest completion.
	for _, reservation := range group.Children {
		child, ok := c.runs[reservation.RunID]
		if !ok {
			index := taskStepIndex(root.Process, group.StepID)
			template := root.Process.Steps[index].ForEach
			child = Run{Process: Process{Steps: template.Steps}, Roles: root.Roles, Profiles: root.Profiles}
		}
		if child.ID == r.ID {
			child = r
		}
		if child.Status == "Completed" {
			continue
		}
		start := child.Step
		if child.ID == r.ID || child.Active() {
			start = readyNext(child.Process, start)
		}
		n, err := readyTail(child, start)
		if err != nil {
			return 0, err
		}
		tail += n
	}
	return tail, nil
}

func (c *Core) taskDispatchAllowed(r Run) bool {
	activeRoots := map[string]bool{}
	activeTasks, activeGroup := 0, 0
	for _, current := range c.runs {
		a := current.Current()
		if a == nil || a.State == Finished {
			continue
		}
		if current.Task == nil {
			activeRoots[current.ID] = true
		} else {
			activeRoots[current.Task.Identity.RootRunID] = true
			activeTasks++
			if r.Task != nil && current.Task.Identity.RootRunID == r.Task.Identity.RootRunID && current.Task.Identity.GroupStep == r.Task.Identity.GroupStep {
				activeGroup++
			}
		}
	}
	rootID := r.ID
	if r.Task != nil {
		rootID = r.Task.Identity.RootRunID
	}
	if !activeRoots[rootID] && len(activeRoots) >= c.snapshot.Instance.Concurrency {
		return false
	}
	if r.Task == nil {
		return true
	}
	root, err := c.taskRoot(r)
	if err != nil || c.snapshot.Instance.TaskConcurrency < 1 || activeTasks >= c.snapshot.Instance.TaskConcurrency {
		return false
	}
	index := taskStepIndex(root.Process, r.Task.Identity.GroupStep)
	return index >= 0 && activeGroup < root.Process.Steps[index].ForEach.MaxParallel
}

func (c *Core) holdTaskResources(r Run) bool {
	if r.Task == nil {
		if len(r.TaskGroups) == 0 || !r.Terminal() {
			return false
		}
		for _, group := range r.TaskGroups {
			for _, child := range group.Children {
				if current, exists := c.runs[child.RunID]; exists && (!current.ResourcesReleased || c.inFlight[current.ID]) {
					return true
				}
			}
		}
		return false
	}
	root, err := c.taskRoot(r)
	if err != nil {
		return true
	}
	if root.Terminal() {
		return false
	}
	// A completed child no longer needs its full checkout once the parent has
	// durably accepted the exact immutable pin. ReleaseRun preserves that pin,
	// its patch/capture and source references until normal family retention.
	if root.Status != "Running" && root.Status != "Waiting" && root.Status != "Queued" || r.Status != "Completed" || r.CancelRequested || !allAttemptsReleased(r) {
		return true
	}
	group := root.TaskGroups[r.Task.Identity.GroupStep]
	var candidate TaskCandidate
	if json.Unmarshal(r.Outputs["result"].Inline, &candidate) != nil || !c.validTaskCandidate(r, group, candidate) {
		return true
	}
	for _, reservation := range group.Children {
		if reservation.RunID == r.ID && reservation.ItemID == r.Task.Identity.ItemID {
			return reservation.Candidate == nil || *reservation.Candidate != candidate
		}
	}
	return true
}
