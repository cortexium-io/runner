package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// General tasks use the ordinary Run/Attempt lifecycle. A parent's persisted
// child reservations are the sole authority to create children after a crash.
func (c *Core) graphRoot(r Run) (Run, error) {
	if r.TaskGraph == nil || r.Task != nil || len(r.TaskGroups) != 0 {
		return Run{}, errors.New("general task ownership is unavailable")
	}
	root, ok := c.runs[r.TaskGraph.RootRunID]
	if r.ID == r.TaskGraph.RootRunID {
		root, ok = r, true
	}
	if !ok || root.TaskGraph == nil || root.TaskGraph.ParentRunID != "" || root.TaskGraph.RootRunID != root.ID || root.Process.Tasks == nil || root.ProjectID != r.ProjectID || root.Repository != r.Repository {
		return Run{}, errors.New("general task root or workspace ownership differs")
	}
	return root, nil
}

func (c *Core) graphFamily(r Run) (Run, []Run, int, error) {
	root, err := c.graphRoot(r)
	if err != nil {
		return Run{}, nil, 0, err
	}
	seen := map[string]bool{}
	members := []Run{}
	reserved := 0
	var visit func(Run) error
	visit = func(current Run) error {
		if current.ID == r.ID {
			current = r
		}
		if seen[current.ID] {
			return errors.New("general task has duplicated or cyclic ownership")
		}
		seen[current.ID] = true
		members = append(members, current)
		for _, link := range current.TaskGraph.Children {
			if link.RunID == "" || seen[link.RunID] {
				return errors.New("general task reservation identity differs")
			}
			child, ok := c.runs[link.RunID]
			if link.RunID == r.ID {
				child, ok = r, true
			}
			if !ok {
				seen[link.RunID] = true
				reserved++
				continue
			}
			if child.TaskGraph == nil || child.TaskGraph.RootRunID != root.ID || child.TaskGraph.ParentRunID != current.ID || child.TaskGraph.TaskID != link.Spec.ID || child.TaskGraph.Role != link.Spec.Role || child.ProjectID != root.ProjectID || child.Repository != root.Repository {
				return errors.New("general task child does not match its retained reservation")
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return Run{}, nil, 0, err
	}
	for id, current := range c.runs {
		if current.TaskGraph != nil && current.TaskGraph.RootRunID == root.ID && !seen[id] {
			return Run{}, nil, 0, errors.New("general task has an unreserved child")
		}
	}
	return root, members, reserved, nil
}

func (c *Core) graphPaused(r Run) bool {
	seen := map[string]bool{}
	for r.TaskGraph != nil {
		if seen[r.ID] || r.TaskGraph.Paused {
			return true
		}
		seen[r.ID] = true
		if r.TaskGraph.ParentRunID == "" {
			return false
		}
		var ok bool
		r, ok = c.runs[r.TaskGraph.ParentRunID]
		if !ok {
			return true
		}
	}
	return true
}

func (c *Core) graphOwnSettled(r Run) bool {
	if r.Active() || !allAttemptsReleased(r) || c.inFlight[r.ID] {
		return false
	}
	for _, a := range r.Attempts {
		if c.inFlight[a.ID] {
			return false
		}
	}
	return true
}

func (c *Core) graphSubtreeSettled(r Run) bool {
	if !c.graphOwnSettled(r) {
		return false
	}
	for _, link := range r.TaskGraph.Children {
		child, ok := c.runs[link.RunID]
		if !ok || !child.Terminal() || !c.graphSubtreeSettled(child) {
			return false
		}
	}
	return true
}

func (c *Core) graphSubtreeQuiescent(r Run) bool {
	if !c.graphOwnSettled(r) {
		return false
	}
	for _, link := range r.TaskGraph.Children {
		child, exists := c.runs[link.RunID]
		if exists && !c.graphSubtreeQuiescent(child) {
			return false
		}
	}
	return true
}

func (c *Core) graphSubtreeCleanupClear(r Run) bool {
	if r.CleanupError != "" || r.CleanupBlocked {
		return false
	}
	for _, link := range r.TaskGraph.Children {
		child, exists := c.runs[link.RunID]
		if !exists || !c.graphSubtreeCleanupClear(child) {
			return false
		}
	}
	return true
}

func (c *Core) graphChild(parent, root Run, link RoleTaskLink) Run {
	role := root.Roles[link.Spec.Role]
	inputs := map[string]Binding{}
	outputs := map[string]Binding{}
	for name := range role.Inputs {
		inputs[name] = Binding{FromInput: name}
	}
	for name := range role.Outputs {
		outputs[name] = Binding{FromStep: "task", Output: name}
	}
	deps := []string{}
	for _, name := range link.Spec.DependsOn {
		for _, sibling := range parent.TaskGraph.Children {
			if !sibling.Retired && sibling.Spec.ID == name {
				deps = append(deps, sibling.RunID)
			}
		}
	}
	child := Run{
		RecordFormat: 9,
		ID:           link.RunID,
		ProjectID:    root.ProjectID,
		Repository:   root.Repository,
		Inputs:       Clone(link.Spec.Inputs),
		Roles:        map[string]Role{role.ID: Clone(role)},
		Profiles:     map[string]string{role.ID: root.Profiles[role.ID]},
		Status:       "Queued",
		Created:      time.Now().UTC(),
		Process: Process{
			ID:                    root.Process.ID,
			Tasks:                 Clone(root.Process.Tasks),
			Inputs:                Clone(role.Inputs),
			Steps:                 []Step{{ID: "task", Role: role.ID, Inputs: inputs}},
			Outputs:               outputs,
			MaxAttempts:           root.Process.MaxAttempts,
			MaxAttemptsPerStep:    root.Process.MaxAttemptsPerStep,
			InfrastructureRetries: root.Process.InfrastructureRetries,
			AttemptTimeout:        root.Process.AttemptTimeout,
		},
		TaskGraph: &TaskGraphState{
			RootRunID:   root.ID,
			ParentRunID: parent.ID,
			TaskID:      link.Spec.ID,
			Role:        role.ID,
			Brief:       link.Spec.Brief,
			Acceptance:  Clone(link.Spec.Acceptance),
			DependsOn:   deps,
			WritePaths:  Clone(link.Spec.WritePaths),
		},
	}
	return child
}

func graphResult(r Run) RoleTaskResult {
	return RoleTaskResult{
		ID:      r.TaskGraph.TaskID,
		RunID:   r.ID,
		Role:    r.TaskGraph.Role,
		Status:  r.Status,
		Reason:  r.Reason,
		Outputs: Clone(r.Outputs),
	}
}

func (c *Core) graphResults(r Run) ([]RoleTaskResult, error) {
	root, err := c.graphRoot(r)
	if err != nil {
		return nil, err
	}
	g := r.TaskGraph
	results := []RoleTaskResult{}
	links := make(map[string]string, len(g.Children))
	for _, link := range g.Children {
		links[link.RunID] = link.Spec.ID
	}
	seen := map[string]bool{}
	for _, ref := range g.Results {
		child, exists := c.runs[ref.RunID]
		id, linked := links[ref.RunID]
		if !exists || !linked || id != ref.ID || seen[ref.RunID] || child.TaskGraph == nil || child.ProjectID != r.ProjectID || child.TaskGraph.RootRunID != root.ID || child.TaskGraph.ParentRunID != r.ID || child.TaskGraph.TaskID != ref.ID || !child.Terminal() {
			return nil, errors.New("retained task result does not identify an accepted owned child")
		}
		seen[ref.RunID] = true
		results = append(results, graphResult(child))
	}
	for _, id := range g.DependsOn {
		dep, ok := c.runs[id]
		if !ok || dep.TaskGraph == nil || dep.TaskGraph.ParentRunID != g.ParentRunID || dep.Status != "Completed" {
			return nil, errors.New("declared task dependency has no accepted result")
		}
		results = append(results, graphResult(dep))
	}
	return results, nil
}

func (c *Core) graphContext(r Run) (*TaskContext, error) {
	root, _, _, err := c.graphFamily(r)
	if err != nil {
		return nil, err
	}
	g := r.TaskGraph
	budget := c.familyAttemptBudget(r)
	if budget == nil || budget.Spent == invalidFamilyAttempts {
		return nil, errors.New("task context requires intact family budget ownership")
	}
	ctx := &TaskContext{
		AttemptBudget:     budget,
		Lineage:           Clone(root.TaskGraph.Lineage),
		RootRunID:         root.ID,
		ParentRunID:       g.ParentRunID,
		TaskID:            g.TaskID,
		Goal:              root.TaskGraph.Brief,
		Brief:             g.Brief,
		Acceptance:        Clone(g.Acceptance),
		WritePaths:        Clone(g.WritePaths),
		Directive:         g.Directive,
		Answer:            g.Answer,
		Revision:          g.Revisions,
		Policy:            Clone(*root.Process.Tasks),
		RemainingAttempts: budget.Remaining,
		AvailableRunSlots: max(0, c.disk.Policy.RetainedRunCapacity()-len(c.runs)-c.graphReservations()),
	}
	ctx.Results, err = c.graphResults(r)
	if err != nil {
		return nil, err
	}
	if audits := root.TaskGraph.Recoveries; len(audits) != 0 {
		ctx.Maintenance = Clone(&audits[len(audits)-1])
	}
	if grants := root.TaskGraph.AllowanceGrants; len(grants) != 0 {
		ctx.AllowanceGrant = Clone(&grants[len(grants)-1])
	}
	ancestors := []Run{}
	for current := r; current.TaskGraph.ParentRunID != ""; {
		current = c.runs[current.TaskGraph.ParentRunID]
		ancestors = append(ancestors, current)
	}
	slices.Reverse(ancestors)
	directives := []string{}
	for _, ancestor := range ancestors {
		if ancestor.TaskGraph.Directive != "" {
			directives = append(directives, "Instruction for ancestor task "+ancestor.TaskGraph.TaskID+": "+ancestor.TaskGraph.Directive)
		}
		if ancestor.TaskGraph.Answer != "" {
			directives = append(directives, "Human answer for ancestor task "+ancestor.TaskGraph.TaskID+": "+ancestor.TaskGraph.Answer)
		}
	}
	if g.Directive != "" {
		directives = append(directives, g.Directive)
	}
	ctx.Directive = strings.Join(directives, "\n\n")
	if _, members, reserved, err := c.graphFamily(r); err == nil {
		if c.workLimitsEnabled() {
			ctx.RemainingTasks = max(0, root.Process.Tasks.MaxTasks-len(members)+1-reserved)
		} else {
			ctx.UnlimitedTasks, ctx.RemainingTasks = true, -1
		}
	}
	if len(r.Attempts) > 0 {
		last := r.Attempts[len(r.Attempts)-1]
		if last.Step == r.Step && last.Visit == r.Visit && last.End != nil && last.End.Kind == "failed" {
			ctx.PreviousFailure = Clip(last.End.Reason)
		}
	}
	for _, name := range root.Process.Tasks.Roles {
		role := root.Roles[name]
		profile, err := role.Profile(root.Profiles[name])
		if err != nil {
			return nil, err
		}
		profileName := root.Profiles[name]
		if profileName == "" {
			profileName = role.DefaultProfile
		}
		if profileName == "" {
			profileName = "default"
		}
		contract := TaskRoleContract{
			ID:        role.ID,
			Purpose:   role.Purpose,
			Inputs:    Clone(role.Inputs),
			Outputs:   Clone(role.Outputs),
			Profile:   profileName,
			Harness:   profile.Harness,
			Mode:      profile.Mode,
			Model:     profile.Model,
			Reasoning: profile.Reasoning,
			Access:    profile.Access,
		}
		if profile.Native != nil {
			contract.Harness = profile.Native.Harness
			contract.Context = profile.Native.Context
			contract.Tools, contract.Skills = Clone(&profile.Native.Tools), Clone(&profile.Native.Skills)
			contract.Network = profile.Native.Network
		}
		if profile.Command != nil {
			contract.Network = profile.Command.Network
		}
		ctx.AllowedRoles = append(ctx.AllowedRoles, contract)
	}
	return ctx, nil
}

func (c *Core) graphArtifactAllowed(r Run, id string) bool {
	artifact, err := c.store.Artifact(c.ctx, id)
	if err != nil || artifact.ProjectID != r.ProjectID && !c.taskContinuationArtifact(r, artifact) {
		return false
	}
	for _, input := range r.Inputs {
		if input.ArtifactID == id {
			return true
		}
	}
	// A configured root preparation step can supply an authenticated artifact
	// to the current role. Delegation may forward that exact assigned input,
	// rather than granting access to every earlier root output.
	inputs := r.StepInputs
	if inputs == nil && r.Step >= 0 && r.Step < len(r.Process.Steps) {
		inputs, err = bindings(r, r.Process.Steps[r.Step].Inputs)
		if err != nil {
			return false
		}
	}
	for _, input := range inputs {
		if input.ArtifactID == id {
			return true
		}
	}
	ctx, err := c.graphContext(r)
	if err != nil {
		return false
	}
	for _, result := range ctx.Results {
		for _, value := range result.Outputs {
			if value.ArtifactID == id {
				owner, exists := c.runs[result.RunID]
				if !exists || owner.ProjectID != r.ProjectID || owner.TaskGraph == nil || owner.TaskGraph.RootRunID != r.TaskGraph.RootRunID {
					return false
				}
				linked := slices.Contains(r.TaskGraph.DependsOn, owner.ID)
				for _, child := range r.TaskGraph.Children {
					linked = linked || child.RunID == owner.ID
				}
				if !linked {
					return false
				}
				// An accepted planner output can forward its own child's exact
				// retained reference. Its original producer need not be the planner.
				for _, accepted := range owner.Outputs {
					if accepted.ArtifactID == id {
						return true
					}
				}
			}
		}
	}
	return false
}

func validGraphPaths(paths []string) error {
	seen := map[string]bool{}
	for _, name := range paths {
		if name != "." && (!fs.ValidPath(name) || strings.Contains(name, "\\")) || seen[name] {
			return errors.New("task write paths need unique literal relative paths or .")
		}
		seen[name] = true
		first := strings.ToLower(strings.Split(name, "/")[0])
		if slices.Contains([]string{".git", ".codex", ".agents", ".aws", ".runner-tmp"}, first) {
			return errors.New("task write paths cannot claim private harness or repository metadata")
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a, b = filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b))
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return a == "." || b == "." || a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (c *Core) graphDispatchAllowed(r Run) bool {
	root, members, _, err := c.graphFamily(r)
	if err != nil || c.graphPaused(r) || r.TaskGraph.Phase != "" {
		return false
	}
	active := 0
	for _, member := range members {
		if member.Active() {
			active++
		}
	}
	if active >= root.Process.Tasks.MaxConcurrent {
		return false
	}
	for _, depID := range r.TaskGraph.DependsOn {
		dep, exists := c.runs[depID]
		if !exists || dep.Status != "Completed" || !c.graphOwnSettled(dep) {
			return false
		}
	}
	reads, writes, err := graphScopes(r)
	if err != nil {
		return false
	}
	if len(reads) == 0 && len(writes) == 0 {
		return true
	}
	for _, other := range c.runs {
		if other.ID == r.ID || other.TaskGraph == nil || !other.Active() {
			continue
		}
		otherReads, otherWrites, err := graphScopes(other)
		if err != nil {
			return false
		}
		if graphScopesOverlap(reads, otherWrites) || graphScopesOverlap(writes, otherReads) || graphScopesOverlap(writes, otherWrites) {
			return false
		}
	}
	return true
}

func graphScopesOverlap(a, b []string) bool {
	for _, left := range a {
		for _, right := range b {
			if pathsOverlap(left, right) {
				return true
			}
		}
	}
	return false
}

func graphScopes(r Run) (reads, writes []string, err error) {
	access := graphAccess(r)
	if access == "workspace-read" {
		reads = []string{r.Repository}
	}
	for _, claim := range r.TaskGraph.WritePaths {
		physical, e := graphPhysicalPath(filepath.Join(r.Repository, filepath.FromSlash(claim)))
		if e != nil {
			return nil, nil, e
		}
		writes = append(writes, physical)
	}
	return reads, writes, nil
}

// Claims may name files a task will create. Resolve the existing physical
// prefix so nested projects and symlink aliases share the same scheduler lock.
func graphPhysicalPath(name string) (string, error) {
	missing := []string{}
	for {
		physical, err := filepath.EvalSymlinks(name)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				physical = filepath.Join(physical, missing[i])
			}
			return physical, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(name) == name {
			return "", err
		}
		if info, err := os.Lstat(name); err == nil && info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(name)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(name), target)
			}
			name = target
			continue
		}
		missing = append(missing, filepath.Base(name))
		name = filepath.Dir(name)
	}
}

func graphWorkspace(configured string) (string, error) {
	canonical, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", fmt.Errorf("general task project workspace: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() || filepath.Dir(canonical) == canonical {
		return "", errors.New("general tasks need an existing project workspace directory below the filesystem root")
	}
	return canonical, nil
}

func graphAccess(r Run) string {
	if r.Step >= len(r.Process.Steps) {
		return ""
	}
	role := r.Process.Steps[r.Step].Role
	name := r.NextProfile
	if a := r.Current(); a != nil && a.State != Finished {
		name = a.Profile
	}
	if name == "" {
		name = r.Profiles[role]
	}
	profile, _ := r.Roles[role].Profile(name)
	return profile.Access
}

// driveGraph only advances persisted parent/child control state. It never calls
// an executor; the same tick below dispatches every admitted role attempt.
func (c *Core) driveGraph(r Run) bool {
	if r.TaskGraph == nil {
		return false
	}
	root, err := c.graphRoot(r)
	if err != nil {
		if !r.Terminal() && r.Reason != err.Error() {
			r.Status, r.Reason = "Waiting", err.Error()
			_ = c.persist(r, nil, nil)
		}
		return !r.Active()
	}
	if r.CancelRequested || r.Terminal() {
		if err := c.cancelGraphChildren(r, false); err != nil {
			c.maintenanceError = Clip(err.Error())
		}
		return false
	}
	if r.Active() {
		return false
	}
	for _, depID := range r.TaskGraph.DependsOn {
		dep, exists := c.runs[depID]
		if exists && dep.Terminal() && dep.Status != "Completed" {
			r.Status, r.Reason = "Failed", "declared task dependency did not complete: "+dep.TaskGraph.TaskID
			_ = c.persist(r, nil, nil)
			return true
		}
	}
	if c.graphPaused(r) || r.TaskGraph.Phase == TaskWaitingInput {
		return true
	}
	if r.TaskGraph.Phase != TaskWaitingChildren {
		if _, _, err := graphScopes(r); err != nil {
			r.Status, r.Reason = "Failed", "declared task source scope is unavailable: "+Clip(err.Error())
			_ = c.persist(r, nil, nil)
			return true
		}
		return false
	}
	results := []TaskResultRef{}
	ready := true
	for _, link := range r.TaskGraph.Children {
		if link.Retired {
			continue
		}
		child, exists := c.runs[link.RunID]
		if !exists {
			child = c.graphChild(r, root, link)
			if err := c.persist(child, nil, nil); err != nil {
				return true
			}
			c.Wake()
			ready = false
			continue
		}
		if !child.Terminal() || !c.graphSubtreeSettled(child) {
			ready = false
			continue
		}
		results = append(results, TaskResultRef{ID: link.Spec.ID, RunID: child.ID})
	}
	if !ready {
		return true
	}
	r.TaskGraph.Results = results
	r.TaskGraph.Phase = ""
	r.Visit++
	r.VisitExtraAttempts = 0
	r.Status, r.Reason = "Queued", "evaluating retained child results"
	if c.persist(r, nil, nil) == nil {
		c.Wake()
	} else {
		retained := Clone(c.runs[r.ID])
		retained.Status, retained.Reason = "Failed", "parent resumption could not be persisted: "+Clip(c.maintenanceError)
		_ = c.persist(retained, nil, nil)
	}
	return true
}

func graphResultRefs(r Run) []TaskResultRef {
	refs := []TaskResultRef{}
	for _, link := range r.TaskGraph.Children {
		if !link.Retired {
			refs = append(refs, TaskResultRef{ID: link.Spec.ID, RunID: link.RunID})
		}
	}
	return refs
}

func (c *Core) cancelGraphChildren(r Run, renew bool) error {
	for _, link := range r.TaskGraph.Children {
		child, exists := c.runs[link.RunID]
		if !exists {
			root, err := c.graphRoot(r)
			if err != nil {
				return err
			}
			child = c.graphChild(r, root, link)
			child.Status, child.Reason, child.CancelRequested = "Cancelled", "parent cancelled before dispatch", true
			if err := c.persist(child, nil, nil); err != nil {
				return err
			}
			continue
		}
		if child.Terminal() {
			if child.Status != "Completed" {
				if err := c.cancelGraphChildren(child, renew); err != nil {
					return err
				}
			}
			continue
		}
		if !child.CancelRequested || renew {
			child = Clone(child)
			child.CancelRequested, child.Reason = true, "parent cancellation requested"
			if a := child.Current(); a != nil && a.State != Finished && renew {
				a.StopRequested, a.NextCheck, a.StopAllowance = false, time.Time{}, a.StopAttempts+3
			}
			if !child.Active() {
				child.Status = "Cancelled"
			}
			if err := c.persist(child, nil, nil); err != nil {
				return err
			}
		}
		if err := c.cancelGraphChildren(child, renew); err != nil {
			return err
		}
	}
	return nil
}

// RootGoal is deliberately deterministic: the submitted brief is data. Roles
// receive the same frozen goal across nested planning and later interventions.
func graphGoal(inputs map[string]Value) string {
	for _, name := range []string{"request", "goal", "text", "brief"} {
		var text string
		if json.Unmarshal(inputs[name].Inline, &text) == nil && text != "" {
			return text
		}
	}
	b, _ := json.Marshal(inputs)
	return string(b)
}

func graphTaskID(id string) bool {
	return id != "" && len(id) <= 80 && id == path.Base(id) && fs.ValidPath(id) && !strings.ContainsAny(id, "\\ ")
}
