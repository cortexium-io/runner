package runner

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// taskRoot authenticates both sides of an internal child reservation. User
// requests and model outputs cannot create these links.
func (c *Core) taskRoot(r Run) (Run, error) {
	if r.Task == nil {
		return r, nil
	}
	id := r.Task.Identity
	root, ok := c.runs[id.RootRunID]
	if !ok || root.Task != nil || root.ProjectID != r.ProjectID {
		return Run{}, errors.New("task root ownership is unavailable")
	}
	g := root.TaskGroups[id.GroupStep]
	if g == nil || g.StepID != id.GroupStep || g.Repository != r.Repository || r.Source == nil || r.Source.Ref != g.Base {
		return Run{}, errors.New("task source ownership differs from its root reservation")
	}
	n := 0
	for _, child := range g.Children {
		if child.RunID == r.ID && child.ItemID == id.ItemID {
			n++
		}
	}
	if n != 1 || !slices.ContainsFunc(g.Items, func(item TaskItem) bool { return item.ID == id.ItemID }) {
		return Run{}, errors.New("task identity differs from its root reservation")
	}
	return root, nil
}

func activeTaskGroup(r Run) *TaskGroupState {
	if r.Step < 0 || r.Step >= len(r.Process.Steps) {
		return nil
	}
	g := r.TaskGroups[r.Process.Steps[r.Step].ID]
	if g != nil && !g.Done {
		return g
	}
	return nil
}

func isolatedStep(r Run) bool {
	return r.Step >= 0 && r.Step < len(r.Process.Steps) && r.Process.Steps[r.Step].ForEach != nil && r.Process.Steps[r.Step].ForEach.Mode == "isolated"
}

func decodeTaskItems(r Run, g *TaskGroup) ([]TaskItem, error) {
	values, err := bindings(r, map[string]Binding{"items": g.Items})
	if err != nil {
		return nil, err
	}
	raw := values["items"].Inline
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = []byte(text)
	}
	var items []TaskItem
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&items); err != nil || d.Decode(new(any)) != io.EOF || len(items) < 1 || len(items) > 8 {
		return nil, errors.New("task collection must contain 1–8 declared items")
	}
	byID := map[string]TaskItem{}
	allPaths := map[string]bool{}
	for _, item := range items {
		if !workflowID(item.ID) || byID[item.ID].ID != "" {
			return nil, errors.New("task IDs must be valid and unique")
		}
		values := map[string]Value{}
		for name, value := range item.Inputs {
			if _, inherited := r.Inputs[name]; inherited || strings.HasPrefix(name, "_") {
				return nil, fmt.Errorf("task %s cannot override inherited or reserved input %s", item.ID, name)
			}
			values[name] = Value{Inline: value}
		}
		if err := ValidateValues(values, g.Inputs); err != nil {
			return nil, fmt.Errorf("task %s: %w", item.ID, err)
		}
		paths, err := taskPaths(item, g)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", item.ID, err)
		}
		for _, p := range paths {
			allPaths[p] = true
		}
		if len(allPaths) > 32 {
			return nil, errors.New("combined task write scope exceeds 32 distinct paths")
		}
		byID[item.ID] = item
	}
	for _, item := range items {
		seen := map[string]bool{}
		for _, dep := range item.DependsOn {
			if dep == item.ID || seen[dep] || byID[dep].ID == "" {
				return nil, fmt.Errorf("task %s has invalid dependency %s", item.ID, dep)
			}
			seen[dep] = true
		}
	}
	ordered := []TaskItem{}
	done := map[string]bool{}
	for len(ordered) != len(items) {
		progress := false
		for _, item := range items {
			if done[item.ID] || slices.ContainsFunc(item.DependsOn, func(dep string) bool { return !done[dep] }) {
				continue
			}
			ordered, done[item.ID], progress = append(ordered, item), true, true
		}
		if !progress {
			return nil, errors.New("task dependency cycle")
		}
	}
	var depends func(string, string) bool
	depends = func(id, prerequisite string) bool {
		for _, dep := range byID[id].DependsOn {
			if dep == prerequisite || depends(dep, prerequisite) {
				return true
			}
		}
		return false
	}
	for i, a := range ordered {
		ap, _ := taskPaths(a, g)
		for _, b := range ordered[i+1:] {
			if depends(b.ID, a.ID) || depends(a.ID, b.ID) {
				continue
			}
			bp, _ := taskPaths(b, g)
			for _, left := range ap {
				for _, right := range bp {
					if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
						return nil, fmt.Errorf("tasks %s and %s overlap %s without a dependency", a.ID, b.ID, left)
					}
				}
			}
		}
	}
	return ordered, nil
}

func taskPaths(item TaskItem, group *TaskGroup) ([]string, error) {
	var paths []string
	if json.Unmarshal(item.Inputs[group.ScopeInput], &paths) != nil || len(paths) < 1 || len(paths) > 32 {
		return nil, errors.New("task write scope needs 1–32 explicit project paths")
	}
	if err := ValidateTaskPaths(paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func frozenTaskSource(r Run) (string, string, string, error) {
	base, repository, patch := "", r.Repository, ""
	for i := len(r.Attempts) - 1; i >= 0; i-- {
		a := r.Attempts[i]
		if a.Superseded || a.Source == nil || a.End == nil || a.End.Kind != "returned" {
			continue
		}
		if base == "" && a.Source.Base != "" {
			base = a.Source.Base
			if a.Source.Repository != "" {
				repository = a.Source.Repository
			}
		}
		if patch == "" && a.Source.PatchID != "" {
			patch = a.Source.PatchID
		}
	}
	if base == "" && r.Source != nil {
		base = r.Source.Ref
	}
	if patch == "" {
		patch = r.Inputs["patch"].ArtifactID
	}
	if len(base) != 40 || strings.Trim(base, "0123456789abcdef") != "" || repository == "" {
		return "", "", "", errors.New("isolated tasks need an admitted immutable source base")
	}
	return repository, base, patch, nil
}

func childForItem(root Run, state *TaskGroupState, item TaskItem, id string) Run {
	group := root.Process.Steps[root.Step].ForEach
	steps := Clone(group.Steps)
	for i := range steps {
		for name, b := range steps[i].Inputs {
			if b.FromItem != "" {
				b.FromInput, b.FromItem = b.FromItem, ""
				steps[i].Inputs[name] = b
			}
		}
	}
	inputs, contract := Clone(root.Inputs), Clone(root.Process.Inputs)
	for name, value := range item.Inputs {
		inputs[name] = Value{Inline: Clone(value)}
		contract[name] = group.Inputs[name]
	}
	inputs["_task_dependencies"] = Value{Inline: json.RawMessage(`[]`)}
	contract["_task_dependencies"] = "json"
	for name, value := range state.Evidence {
		inputs["_task_evidence_"+name] = Clone(value)
		contract["_task_evidence_"+name] = "artifact"
	}
	proc := Process{
		ID:                    "isolated-card",
		Inputs:                contract,
		Steps:                 steps,
		Outputs:               map[string]Binding{"result": Clone(group.Output)},
		MaxAttempts:           root.Process.MaxAttempts,
		MaxAttemptsPerStep:    root.Process.MaxAttemptsPerStep,
		InfrastructureRetries: root.Process.InfrastructureRetries,
		AttemptTimeout:        root.Process.AttemptTimeout,
	}
	roles, profiles := map[string]Role{}, map[string]string{}
	for _, step := range steps {
		roles[step.Role], profiles[step.Role] = Clone(root.Roles[step.Role]), root.Profiles[step.Role]
	}
	return Run{
		RecordFormat: 3,
		Task:         &TaskRunLink{Identity: TaskIdentity{RootRunID: root.ID, GroupStep: state.StepID, ItemID: item.ID}},
		Repository:   state.Repository,
		Source:       &SourceSettings{Ref: state.Base},
		ID:           id,
		ProjectID:    root.ProjectID,
		Process:      proc,
		Roles:        roles,
		Profiles:     profiles,
		Inputs:       inputs,
		Status:       "Waiting",
		Reason:       "waiting for declared task prerequisites",
		Created:      state.Started,
	}
}

func allAttemptsReleased(r Run) bool {
	for _, a := range r.Attempts {
		if a.State != Finished || !a.Released {
			return false
		}
	}
	return true
}

// Only a new explicit Cancel invocation renews the bounded stop allowance.
// Reconciliation ticks propagate cancellation without granting more retries.
func (c *Core) renewTaskStops(root Run) error {
	for _, group := range root.TaskGroups {
		for _, reservation := range group.Children {
			child, exists := c.runs[reservation.RunID]
			if !exists || child.Terminal() {
				continue
			}
			owner, err := c.taskRoot(child)
			if err != nil || owner.ID != root.ID {
				return errors.New("task cancellation ownership is unavailable")
			}
			child = Clone(child)
			child.CancelRequested = true
			if a := child.Current(); a != nil && a.State != Finished {
				a.StopRequested = false
				a.NextCheck = time.Time{}
				a.StopAllowance = a.StopAttempts + 3
			}
			if err := c.persist(child, nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Core) taskAssignment(r Run, step Step) (*TaskAssignment, error) {
	root := r
	var g *TaskGroupState
	identity := TaskIdentity{RootRunID: r.ID}
	var paths []string
	if r.Task != nil {
		var err error
		root, err = c.taskRoot(r)
		if err != nil {
			return nil, err
		}
		identity = r.Task.Identity
		g = root.TaskGroups[identity.GroupStep]
	} else {
		profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
		if err != nil || profile.Harness != "task-source" || profile.Mode != "join" {
			return nil, nil
		}
		identity.GroupStep = step.Inputs["results"].FromStep
		g = r.TaskGroups[identity.GroupStep]
		if g == nil || !g.Done {
			return nil, errors.New("task source join needs its completed owned group")
		}
	}
	var definition *TaskGroup
	for _, s := range root.Process.Steps {
		if s.ID == g.StepID {
			definition = s.ForEach
		}
	}
	if definition == nil {
		return nil, errors.New("frozen task group is unavailable")
	}
	dependencies := []TaskCandidate{}
	seenPaths := map[string]bool{}
	for _, item := range g.Items {
		if r.Task != nil && item.ID == identity.ItemID {
			paths, _ = taskPaths(item, definition)
		}
		needed := r.Task == nil
		if r.Task != nil {
			for _, current := range g.Items {
				if current.ID == identity.ItemID && slices.Contains(current.DependsOn, item.ID) {
					needed = true
				}
			}
		}
		if !needed {
			continue
		}
		for _, child := range g.Children {
			if child.ItemID == item.ID {
				if child.Candidate == nil {
					return nil, errors.New("accepted dependency source is unavailable")
				}
				dependencies = append(dependencies, Clone(*child.Candidate))
				if r.Task == nil {
					p, _ := taskPaths(item, definition)
					for _, scope := range p {
						if !seenPaths[scope] {
							paths = append(paths, scope)
							seenPaths[scope] = true
						}
					}
				}
			}
		}
	}
	return &TaskAssignment{Identity: identity, WritePaths: paths, Dependencies: dependencies, RootPatchID: g.RootPatchID}, nil
}

// driveTaskGroup advances only retained Core facts. Actual work is dispatched
// by the ordinary run loop, with its normal persistence and recovery protocol.
func (c *Core) driveTaskGroup(r Run) bool {
	if !isolatedStep(r) || r.Terminal() {
		return false
	}
	step := r.Process.Steps[r.Step]
	state := r.TaskGroups[step.ID]
	if state == nil {
		if r.Status != "Queued" || !allAttemptsReleased(r) {
			return true
		}
		items, err := decodeTaskItems(r, step.ForEach)
		if err != nil {
			c.rejectTaskPlan(r, err)
			return true
		}
		repository, base, patch, err := frozenTaskSource(r)
		if err == nil {
			err = c.taskGroupAdmission(r, len(items))
		}
		if err != nil {
			r.Status, r.Reason = "Waiting", Clip(err.Error())
			_ = c.persist(r, nil, nil)
			return true
		}
		evidence, err := bindings(r, step.ForEach.Evidence)
		if err != nil {
			r.Status, r.Reason = "Waiting", "task evidence binding: "+Clip(err.Error())
			_ = c.persist(r, nil, nil)
			return true
		}
		for _, value := range evidence {
			if value.ArtifactID == "" {
				r.Status, r.Reason = "Waiting", "task evidence requires configured artifact outputs"
				_ = c.persist(r, nil, nil)
				return true
			}
		}
		state = &TaskGroupState{
			StepID:      step.ID,
			AttemptID:   "attempt_" + rand.Text(),
			Repository:  repository,
			Base:        base,
			Started:     time.Now().UTC(),
			Items:       items,
			RootPatchID: patch,
			Evidence:    evidence,
		}
		for _, item := range items {
			state.Children = append(state.Children, TaskChild{ItemID: item.ID, RunID: "run_" + rand.Text()})
		}
		if r.TaskGroups == nil {
			r.TaskGroups = map[string]*TaskGroupState{}
		}
		r.TaskGroups[step.ID] = state
		if err := c.admitTaskItems(r, state); err != nil {
			delete(r.TaskGroups, step.ID)
			c.rejectTaskPlan(r, err)
			return true
		}
		r.Status, r.Reason = "Running", "executing isolated task group"
		if c.persist(r, nil, nil) == nil {
			c.Wake()
		}
		return true
	}
	if state.Done {
		return false
	}
	failed := ""
	changed := false
	for i := range state.Children {
		reservation := &state.Children[i]
		item := state.Items[i]
		child, exists := c.runs[reservation.RunID]
		if !exists {
			child = childForItem(r, state, item, reservation.RunID)
			if r.CancelRequested || state.Failure != "" {
				child.CancelRequested = true
				child.Status, child.Reason = "Cancelled", "parent task group is stopping"
			}
			if err := admitRunRecord(child, c.disk.Policy.Database.MaxRecord); err != nil {
				r.Status, r.Reason = "Waiting", Clip(err.Error())
				_ = c.persist(r, nil, nil)
				return true
			}
			if err := c.persist(child, nil, nil); err != nil {
				return true
			}
			c.Wake()
		}
		if child.Task == nil || child.Task.Identity != (TaskIdentity{RootRunID: r.ID, GroupStep: state.StepID, ItemID: reservation.ItemID}) {
			failed = "task identity differs from its retained reservation"
			break
		}
		if _, err := c.taskRoot(child); err != nil {
			failed = err.Error()
			break
		}
		if failed == "" && (child.Status == "Failed" || child.Status == "Cancelled") {
			failed = "task " + item.ID + ": " + child.Reason
		}
		if child.Status == "Completed" && allAttemptsReleased(child) && reservation.Candidate == nil {
			var candidate TaskCandidate
			if json.Unmarshal(child.Outputs["result"].Inline, &candidate) != nil || !c.validTaskCandidate(child, state, candidate) {
				failed = "task " + item.ID + " returned an invalid internal source candidate"
			} else {
				reservation.Candidate = &candidate
				changed = true
			}
		}
	}
	if state.Failure == "" && failed != "" {
		state.Failure = Clip(failed)
		if c.persist(r, nil, nil) != nil {
			return true
		}
	}
	if r.CancelRequested || state.Failure != "" {
		settled := true
		for _, reservation := range state.Children {
			child, ok := c.runs[reservation.RunID]
			if !ok {
				continue
			}
			if !child.Terminal() {
				child.CancelRequested = true
				child.Reason = "parent task group is stopping"
				if !child.Active() {
					child.Status = "Cancelled"
				}
				_ = c.persist(child, nil, nil)
				c.Wake()
			}
			if !allAttemptsReleased(child) || child.Active() || c.inFlight[child.ID] {
				settled = false
			}
		}
		if settled {
			state.Done = true
			r.Status, r.Reason = "Failed", state.Failure
			if r.CancelRequested {
				r.Status, r.Reason = "Cancelled", "parent cancellation settled all tasks"
			}
			_ = c.persist(r, nil, nil)
			c.Wake()
		}
		return true
	}
	complete := true
	for _, reservation := range state.Children {
		complete = complete && reservation.Candidate != nil
	}
	if complete {
		c.finishTaskGroup(r, state)
		return true
	}
	for _, reservation := range state.Children {
		child := Clone(c.runs[reservation.RunID])
		if child.Status != "Waiting" || len(child.Attempts) != 0 || child.Reason != "waiting for declared task prerequisites" {
			continue
		}
		ready := true
		for _, item := range state.Items {
			if item.ID == reservation.ItemID {
				for _, dependency := range item.DependsOn {
					ready = ready && slices.ContainsFunc(state.Children, func(other TaskChild) bool { return other.ItemID == dependency && other.Candidate != nil })
				}
			}
		}
		if ready {
			// Publish newly accepted dependencies before building the assignment.
			if changed {
				if c.persist(r, nil, nil) != nil {
					return true
				}
				changed = false
			}
			assignment, err := c.taskAssignment(child, child.Process.Steps[0])
			if err != nil {
				continue
			}
			raw, _ := json.Marshal(assignment.Dependencies)
			child.Inputs["_task_dependencies"] = Value{Inline: raw}
			child.Status, child.Reason = "Queued", "declared task prerequisites are ready"
			_ = c.persist(child, nil, nil)
			c.Wake()
		}
	}
	if changed {
		_ = c.persist(r, nil, nil)
		c.Wake()
	}
	// An unexpected child blocker belongs to the complete workflow result.
	// Keep the group live so cancellation and configured recovery still work.
	status, reason := "Running", "executing isolated task group"
	for _, reservation := range state.Children {
		child := c.runs[reservation.RunID]
		if child.Status == "Waiting" && child.Reason != "waiting for declared task prerequisites" {
			status, reason = "Waiting", "task "+reservation.ItemID+": "+child.Reason
			break
		}
	}
	if r.Status != status || r.Reason != reason {
		r.Status, r.Reason = status, Clip(reason)
		_ = c.persist(r, nil, nil)
	}
	return true
}

func (c *Core) validTaskCandidate(child Run, state *TaskGroupState, candidate TaskCandidate) bool {
	if candidate.RunID != child.ID || candidate.Identity != child.Task.Identity || candidate.Base != state.Base || candidate.Repository != state.Repository || len(candidate.Commit) != 40 || len(candidate.Tree) != 40 || strings.Trim(candidate.Commit+candidate.Tree, "0123456789abcdef") != "" {
		return false
	}
	artifact, err := c.store.Artifact(c.ctx, candidate.PatchID)
	return err == nil && artifact.RunID == child.ID && artifact.ProjectID == child.ProjectID
}

func (c *Core) finishTaskGroup(r Run, state *TaskGroupState) {
	definition := r.Process.Steps[r.Step].ForEach
	manifest := TaskManifest{RootRunID: r.ID, GroupStep: state.StepID, Repository: state.Repository, Base: state.Base, RootPatchID: state.RootPatchID, Evidence: Clone(state.Evidence)}
	_ = json.Unmarshal(r.Inputs["request"].Inline, &manifest.Request)
	for i, item := range state.Items {
		paths, _ := taskPaths(item, definition)
		child := state.Children[i]
		manifest.Items = append(manifest.Items, TaskManifestItem{
			ID: item.ID, DependsOn: item.DependsOn, Inputs: Clone(item.Inputs),
			WritePaths: paths, RunID: child.RunID, Candidate: *child.Candidate,
		})
	}
	raw, err := json.Marshal(manifest)
	if err == nil {
		err = c.disk.Write("artifacts/"+state.AttemptID+"/results", raw, c.disk.Policy.MaxArtifact)
	}
	if err != nil {
		r.Status, r.Reason = "Waiting", "task manifest persistence: "+Clip(err.Error())
		_ = c.persist(r, nil, nil)
		return
	}
	zero := int64(0)
	artifact := Artifact{ID: state.AttemptID + "/results", RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(raw))}
	a := Attempt{
		ID: state.AttemptID, Step: r.Step, Visit: r.Visit, State: Finished,
		Started: state.Started, FinishedAt: time.Now().UTC(), Released: true,
		Usage: Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
		End:   &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"results": {ArtifactID: artifact.ID}}}},
	}
	state.Done = true
	r.Attempts = append(r.Attempts, a)
	c.route(&r, &r.Attempts[len(r.Attempts)-1])
	if c.persist(r, []Artifact{artifact}, nil) == nil {
		c.Wake()
	}
}

func (c *Core) rejectTaskPlan(r Run, err error) {
	if c.familyRemaining(r) <= 0 {
		r.Status, r.Reason = "Waiting", "run attempt allowance exhausted before plan validation"
		_ = c.persist(r, nil, nil)
		return
	}
	zero := int64(0)
	now := time.Now().UTC()
	r.Attempts = append(r.Attempts, Attempt{
		ID: "attempt_" + rand.Text(), Step: r.Step, Visit: r.Visit,
		State: Finished, Released: true, Started: now, FinishedAt: now,
		Usage: Usage{InputTokens: &zero, OutputTokens: &zero, CachedInputTokens: &zero},
		End: &ExecutionEnd{Kind: "returned", Result: &StepResult{
			Outcome: "invalid_plan", Reason: Clip(err.Error()),
			Outputs: map[string]Value{"feedback": Text(Clip(err.Error()))},
		}},
	})
	c.route(&r, &r.Attempts[len(r.Attempts)-1])
	if c.persist(r, nil, nil) == nil {
		c.Wake()
	}
}
