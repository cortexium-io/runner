package runner

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (c *Core) graphAction(r *Run, result *StepResult) error {
	action := result.TaskAction
	if r.TaskGraph == nil || action == nil || len(result.Outputs) != 0 || result.Outcome != "ready" && result.Outcome != action.Kind {
		return errors.New("task action needs an opted-in role, no final outputs and a matching outcome")
	}
	root, members, reserved, err := c.graphFamily(*r)
	if err != nil {
		return err
	}
	if action.Kind == "ask_human" {
		if strings.TrimSpace(action.Question) == "" || len(action.Tasks) != 0 || len(action.Replace) != 0 {
			return errors.New("ask_human needs a specific question and cannot also change tasks")
		}
		if err := c.graphReserve(*r, 0, true); err != nil {
			return err
		}
		r.TaskGraph.Phase, r.TaskGraph.Question, r.TaskGraph.Answer = TaskWaitingInput, action.Question, ""
		r.Status, r.Reason = "Waiting", "task needs human input"
		return nil
	}
	if action.Kind != "delegate" && action.Kind != "revise" {
		return errors.New("unsupported task action")
	}
	if len(action.Tasks) == 0 || action.Question != "" {
		return errors.New("delegation needs role tasks and cannot also ask a question")
	}
	policy := root.Process.Tasks
	if len(members)-1+reserved+len(action.Tasks) > policy.MaxTasks {
		return errors.New("task proposal exceeds the frozen goal task allowance")
	}
	if len(c.runs)+c.graphReservations()+len(action.Tasks) > c.disk.Policy.RetainedRunCapacity() {
		return errors.New("task proposal exceeds retained run capacity")
	}
	depth := 0
	for current := *r; current.TaskGraph.ParentRunID != ""; depth++ {
		current = c.runs[current.TaskGraph.ParentRunID]
	}
	if depth+1 > policy.MaxDepth {
		return errors.New("task proposal exceeds the frozen planning depth")
	}
	revisions := 0
	for _, member := range members {
		revisions += member.TaskGraph.Revisions
	}
	replace := map[string]bool{}
	for _, id := range action.Replace {
		if replace[id] {
			return errors.New("revision names a child more than once")
		}
		replace[id] = true
	}
	if action.Kind == "delegate" && len(replace) != 0 {
		return errors.New("delegate cannot replace accepted work; use revise")
	}
	if action.Kind == "revise" && (len(replace) == 0 || strings.TrimSpace(action.Reason) == "" || revisions >= policy.MaxRevisions) {
		return errors.New("revision needs affected child IDs, a reason and remaining revision allowance")
	}
	current := map[string]RoleTaskLink{}
	replacedRuns := map[string]bool{}
	for _, link := range r.TaskGraph.Children {
		if link.Retired {
			continue
		}
		if replace[link.Spec.ID] {
			child, exists := c.runs[link.RunID]
			if !exists || !child.Terminal() || !c.graphSubtreeSettled(child) {
				return errors.New("revision requires affected child executions to be settled")
			}
			replacedRuns[link.RunID] = true
		} else {
			current[link.Spec.ID] = link
		}
	}
	if len(replacedRuns) != len(replace) {
		return errors.New("revision names an unavailable current child")
	}
	for _, link := range current {
		child := c.runs[link.RunID]
		for _, dep := range child.TaskGraph.DependsOn {
			if replacedRuns[dep] {
				return errors.New("revision must explicitly replace children whose evidence depends on affected work")
			}
		}
	}
	for _, spec := range action.Tasks {
		if !graphTaskID(spec.ID) || strings.TrimSpace(spec.Brief) == "" || !slices.Contains(policy.Roles, spec.Role) || root.Roles[spec.Role].ID != spec.Role {
			return errors.New("each task needs a unique ID, brief and allowed frozen role")
		}
		if _, exists := current[spec.ID]; exists {
			return errors.New("task proposal duplicates a current child ID")
		}
		if err := ValidateValues(spec.Inputs, root.Roles[spec.Role].Inputs); err != nil {
			return fmt.Errorf("task %s: %w", spec.ID, err)
		}
		for _, value := range spec.Inputs {
			if value.ArtifactID != "" && !c.graphArtifactAllowed(*r, value.ArtifactID) {
				return fmt.Errorf("task %s artifact input is not available through this task's inputs or declared results", spec.ID)
			}
		}
		if err := validGraphPaths(spec.WritePaths); err != nil {
			return fmt.Errorf("task %s: %w", spec.ID, err)
		}
		for _, claim := range spec.WritePaths {
			if _, err := graphPhysicalPath(filepath.Join(root.Repository, filepath.FromSlash(claim))); err != nil {
				return fmt.Errorf("task %s write scope: %w", spec.ID, err)
			}
		}
		profile, err := root.Roles[spec.Role].Profile(root.Profiles[spec.Role])
		if err != nil {
			return err
		}
		if profile.Access == "workspace-write" && len(spec.WritePaths) == 0 {
			return fmt.Errorf("task %s writable role needs explicit write paths", spec.ID)
		}
		for _, claim := range spec.WritePaths {
			if len(r.TaskGraph.WritePaths) == 0 {
				continue // A planning-only parent can select the configured role's scope.
			}
			within := false
			for _, parentClaim := range r.TaskGraph.WritePaths {
				within = within || parentClaim == "." || claim == parentClaim || strings.HasPrefix(claim, parentClaim+"/")
			}
			if !within {
				return fmt.Errorf("task %s write claim exceeds its parent task scope", spec.ID)
			}
		}
		current[spec.ID] = RoleTaskLink{Spec: Clone(spec), RunID: "run_" + rand.Text()}
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var check func(string) error
	check = func(id string) error {
		if visiting[id] {
			return errors.New("task proposal contains cyclic dependencies")
		}
		if done[id] {
			return nil
		}
		link, exists := current[id]
		if !exists {
			return errors.New("task dependency does not name a current sibling")
		}
		visiting[id] = true
		seen := map[string]bool{}
		for _, dep := range link.Spec.DependsOn {
			if seen[dep] {
				return errors.New("task dependency is duplicated")
			}
			seen[dep] = true
			if err := check(dep); err != nil {
				return err
			}
		}
		visiting[id], done[id] = false, true
		return nil
	}
	for id := range current {
		if err := check(id); err != nil {
			return err
		}
	}
	// Every admitted child gets one execution, and each suspended ancestor keeps
	// one resumption. Speculative repairs consume the same frozen root allowance.
	if err := c.graphReserve(*r, len(action.Tasks), true); err != nil {
		return err
	}
	proposal := Clone(*r)
	for i := range proposal.TaskGraph.Children {
		if replace[proposal.TaskGraph.Children[i].Spec.ID] {
			proposal.TaskGraph.Children[i].Retired = true
		}
	}
	for _, spec := range action.Tasks {
		proposal.TaskGraph.Children = append(proposal.TaskGraph.Children, current[spec.ID])
	}
	if action.Kind == "revise" {
		proposal.TaskGraph.Revisions++
	}
	proposal.TaskGraph.Phase = TaskWaitingChildren
	proposal.Status, proposal.Reason = "Waiting", "waiting for role tasks"
	if err := admitRunRecord(proposal, c.disk.Policy.Database.MaxRecord); err != nil {
		return fmt.Errorf("task proposal: %w", err)
	}
	resumption := Clone(proposal)
	resumption.TaskGraph.Results = graphResultRefs(proposal)
	resumption.TaskGraph.Phase = ""
	resumption.Visit++
	resumption.VisitExtraAttempts = 0
	resumption.Status, resumption.Reason = "Queued", "evaluating retained child results"
	if err := admitRunRecord(resumption, c.disk.Policy.Database.MaxRecord); err != nil {
		return fmt.Errorf("task proposal parent resumption: %w", err)
	}
	for _, spec := range action.Tasks {
		child := c.graphChild(proposal, root, current[spec.ID])
		if err := admitRunRecord(child, c.disk.Policy.Database.MaxRecord); err != nil {
			return fmt.Errorf("task %s: %w", spec.ID, err)
		}
	}
	*r = proposal
	return nil
}

func (c *Core) graphReservations() int {
	n := 0
	for _, r := range c.runs {
		if r.TaskGraph == nil {
			continue
		}
		for _, link := range r.TaskGraph.Children {
			if _, exists := c.runs[link.RunID]; !exists {
				n++
			}
		}
	}
	return n
}

func (c *Core) graphReserve(r Run, extra int, resuming bool) error {
	root, members, reserved, err := c.graphFamily(r)
	if err != nil {
		return err
	}
	need := reserved + extra
	for _, member := range members {
		if member.Terminal() || member.Active() {
			continue
		}
		if member.ID == r.ID && !resuming {
			continue
		}
		need++
	}
	if resuming && r.Active() {
		need++
	}
	if remaining := root.Process.MaxAttempts - c.familyAttempts(r); need > remaining {
		return fmt.Errorf("task admission needs %d completion attempts; %d remain in the frozen goal allowance", need, remaining)
	}
	return nil
}

// InterveneTask changes only the selected task and its future execution. A pause
// prevents new dispatch; running attempts still reconcile and settle normally.
func (c *Core) InterveneTask(req TaskInterventionRequest) (Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	args, _ := json.Marshal(req)
	if r, ok, err := c.replay(req.RequestID, "task_intervention", args); ok || err != nil {
		return c.outputRun(r), err
	}
	r, exists := c.runs[req.RunID]
	if !exists {
		return Run{}, ErrNotFound
	}
	failedSteer := r.Status == "Failed" && req.Action == "steer"
	if r.TaskGraph == nil || r.Terminal() && !failedSteer {
		return Run{}, errors.New("intervention needs a retained nonterminal general task; only explicit steering can reopen failed work")
	}
	if req.Revision != c.outputRun(r).Revision {
		return Run{}, errors.New("task revision changed; inspect current state before intervening")
	}
	if req.Action != "reassign" && req.Profile != "" {
		return Run{}, errors.New("profile is only valid for reassign")
	}
	r = Clone(r)
	switch req.Action {
	case "pause":
		r.TaskGraph.Paused = true
		if !r.Active() {
			r.Status, r.Reason = "Waiting", "task paused by human"
		}
	case "resume":
		if !r.TaskGraph.Paused {
			return Run{}, errors.New("task is not paused")
		}
		r.TaskGraph.Paused = false
		if r.TaskGraph.Phase == "" && !r.Active() {
			r.Status, r.Reason = "Queued", "task resumed by human"
		}
	case "answer":
		if r.TaskGraph.Phase != TaskWaitingInput || strings.TrimSpace(req.Text) == "" || !c.graphOwnSettled(r) {
			return Run{}, errors.New("answer needs a settled task with a saved question and nonempty text")
		}
		r.TaskGraph.Answer, r.TaskGraph.Phase = req.Text, ""
		r.Visit++
		r.VisitExtraAttempts = 0
		r.Status, r.Reason = "Queued", "resuming with human answer"
	case "steer":
		if strings.TrimSpace(req.Text) == "" || !c.graphSubtreeQuiescent(r) {
			return Run{}, errors.New("steering needs nonempty text and settled affected work; pause and wait for active attempts")
		}
		if failedSteer {
			if r.Step < 0 || r.Step >= len(r.Process.Steps) || c.familyRemaining(r) <= 0 || !c.graphSubtreeCleanupClear(r) {
				return Run{}, errors.New("failed task steering needs a valid current step, remaining goal allowance and settled cleanup without errors")
			}
			root, err := c.graphRoot(r)
			if err != nil {
				return Run{}, err
			}
			if r.ID != root.ID {
				parent := c.runs[r.TaskGraph.ParentRunID]
				current := false
				if parent.TaskGraph != nil {
					for _, link := range parent.TaskGraph.Children {
						current = current || link.RunID == r.ID && !link.Retired
					}
				}
				if root.Terminal() || parent.Terminal() || parent.TaskGraph == nil || parent.TaskGraph.Phase != TaskWaitingChildren || !current {
					return Run{}, errors.New("parent already evaluated or superseded this task; steer the owning failed goal instead")
				}
			}
			r.TaskGraph.Phase, r.TaskGraph.Paused = "", false
			r.TaskGraph.Results = graphResultRefs(r)
			if _, err := c.graphContext(r); err != nil {
				return Run{}, err
			}
			r.ResourcesReleased = false
		}
		r.TaskGraph.Directive = req.Text
		if r.TaskGraph.Phase != TaskWaitingChildren && r.TaskGraph.Phase != TaskWaitingInput {
			r.Visit++
			r.VisitExtraAttempts = 0
			r.Status, r.Reason = "Queued", "task steered by human"
		}
	case "reassign":
		if req.Profile == "" || !c.graphSubtreeQuiescent(r) || r.Step >= len(r.Process.Steps) {
			return Run{}, errors.New("reassignment needs a named profile and settled affected work")
		}
		profile, err := r.Roles[r.Process.Steps[r.Step].Role].Profile(req.Profile)
		if err != nil {
			return Run{}, err
		}
		if err := graphProfile(profile, r.TaskGraph.WritePaths); err != nil {
			return Run{}, err
		}
		r.NextProfile = req.Profile
	default:
		return Run{}, errors.New("task action must be pause, resume, steer, answer or reassign")
	}
	if err := admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	receipt := Receipt{Key: req.RequestID, Kind: "task_intervention", Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &receipt); err != nil {
		return Run{}, err
	}
	c.Wake()
	return c.outputRun(c.runs[r.ID]), nil
}

func graphProfile(profile Profile, claims []string) error {
	if err := ValidateRoleTaskProfile(profile); err != nil {
		return err
	}
	if profile.Access == "workspace-write" && len(claims) == 0 {
		return errors.New("writable general task execution requires explicit task write paths")
	}
	return nil
}

// ValidateRoleTaskProfile defines the executors that consume the shared task
// workspace. Legacy source adapters retain their existing isolated contract.
func ValidateRoleTaskProfile(profile Profile) error {
	if profile.Harness == "native" || profile.Harness == "deterministic" {
		return nil
	}
	if profile.Harness == "command" && profile.Mode == "shared" && (profile.Access == "workspace-read" || profile.Access == "workspace-write") && profile.Command != nil {
		if profile.Command.Directory != "" {
			return errors.New("shared commands run at the frozen project root; directory must be empty")
		}
		return nil
	}
	return errors.New("general task roles require native, deterministic or shared command execution; legacy source adapters do not consume shared task context")
}

// General task retries use the root's aggregate allowance. A child can receive
// a per-visit grant within that allowance; it cannot enlarge a private copy of
// the goal budget. The root operation retains the existing explicit grant API.
func (c *Core) retryGraph(r Run, req RetryRequest, args []byte) (Run, error) {
	root, err := c.graphRoot(r)
	if err != nil {
		return Run{}, err
	}
	if (r.Status != "Waiting" && r.Status != "Failed") || !c.graphSubtreeQuiescent(r) {
		return Run{}, errors.New("retry needs settled failed/waiting task work; pause and reconcile active execution first")
	}
	if r.ID != root.ID {
		parent, exists := c.runs[r.TaskGraph.ParentRunID]
		if !exists || parent.TaskGraph == nil {
			return Run{}, errors.New("general task parent ownership is unavailable")
		}
		current := false
		for _, link := range parent.TaskGraph.Children {
			current = current || link.RunID == r.ID && !link.Retired
		}
		if root.Terminal() || parent.Terminal() || parent.TaskGraph.Phase != TaskWaitingChildren || !current {
			return Run{}, errors.New("parent already evaluated or superseded this task; request an explicit parent revision instead of replaying accepted work")
		}
	}
	if r.Decision != nil || r.TaskGraph.Phase == TaskWaitingInput {
		return Run{}, errors.New("pending human input requires its saved decision or task answer, not retry")
	}
	if r.Step >= len(r.Process.Steps) {
		return Run{}, errors.New("task has no current role visit to retry")
	}
	goalGrant := !req.WithinBudget && req.ExtraAttempts > 0 && r.ID == root.ID
	if req.Revision != 0 && (!goalGrant || req.Revision < 1 || req.Revision != c.outputRun(r).Revision) {
		return Run{}, errors.New("root grant revision changed; inspect the current goal before retrying")
	}
	if taskDefinitionAmendment(req) {
		if !goalGrant || req.Revision < 1 {
			return Run{}, errors.New("definition amendment requires an explicit root aggregate grant and exact current revision")
		}
		_, family, reserved, err := c.graphFamily(r)
		if err != nil || reserved != 0 {
			return Run{}, errors.New("definition amendment requires a complete retained task family")
		}
		for _, member := range family {
			if !c.graphOwnSettled(member) || !member.ResourcesReleased || member.CleanupBlocked || member.CleanupError != "" {
				return Run{}, errors.New("definition amendment requires all family execution and cleanup to settle")
			}
		}
	}
	if req.Reason != "" && (!goalGrant || strings.TrimSpace(req.Reason) == "" || len(req.Reason) > 2048) {
		return Run{}, errors.New("retry reason needs a root goal allowance grant and 1–2048 nonblank bytes")
	}
	if req.Directive != "" && (!goalGrant || strings.TrimSpace(req.Directive) == "" || len(req.Directive) > 2048) {
		return Run{}, errors.New("retry directive needs a root goal allowance grant and 1–2048 nonblank bytes")
	}
	if req.ExtraAttempts < 0 || !goalGrant && req.ExtraAttempts > 4 {
		return Run{}, errors.New("extra_attempts exceeds the per-operation allowance")
	}
	if !req.WithinBudget && req.ExtraAttempts > 0 && r.ID != root.ID {
		return Run{}, fmt.Errorf("goal attempt allowance belongs to root %s; grant it on that root or use within_budget for this task visit", root.ID)
	}
	if !req.WithinBudget && req.ExtraAttempts > MaxGeneralTaskAttempts-root.Process.MaxAttempts {
		return Run{}, errors.New("extra_attempts exceeds the general goal allowance")
	}
	if req.Profile != "" {
		profile, err := r.Roles[r.Process.Steps[r.Step].Role].Profile(req.Profile)
		if err != nil {
			return Run{}, err
		}
		if err := graphProfile(profile, r.TaskGraph.WritePaths); err != nil {
			return Run{}, err
		}
		r.NextProfile = req.Profile
	}
	var definition []byte
	var definitionRef *CardRecoveryArtifact
	if goalGrant {
		spent := c.familyAttempts(r)
		if spent == invalidFamilyAttempts {
			return Run{}, errors.New("goal allowance grant requires intact family ownership")
		}
		grant := TaskAllowanceGrant{
			RequestID:     req.RequestID,
			PreviousLimit: r.Process.MaxAttempts,
			Limit:         r.Process.MaxAttempts + req.ExtraAttempts,
			Spent:         spent,
			Reason:        taskAllowanceGrantReason(req),
			GrantedAt:     time.Now().UTC(),
		}
		definition, err = applyTaskDefinitionAmendment(&r, req, &grant)
		if err != nil {
			return Run{}, err
		}
		definitionRef = grant.PreviousDefinition
		r.TaskGraph.AllowanceGrants = append(r.TaskGraph.AllowanceGrants, grant)
		r.Process.MaxAttempts += req.ExtraAttempts
		if req.Directive != "" {
			r.TaskGraph.Directive = req.Directive
		}
	}
	if r.TaskGraph.Phase == TaskWaitingChildren {
		if req.WithinBudget {
			return Run{}, errors.New("a task continuation has no role visit to extend; retry without within_budget")
		}
		if r.Status != "Failed" && (r.ID != root.ID || req.ExtraAttempts == 0) {
			return Run{}, errors.New("pending role tasks resume automatically; only an explicit root goal allowance grant applies while waiting for children")
		}
		// A failed continuation has not created its next role visit yet.
		// Reopen the wait, reserving that visit before the ordinary graph
		// drive gathers the same retained child results exactly once.
		r.Status = "Waiting"
		if err := c.graphReserve(r, 0, true); err != nil {
			return Run{}, err
		}
		r.Reason = "authorized root goal allowance grant"
		if req.ExtraAttempts == 0 {
			r.Reason = "retrying retained task continuation"
		}
	} else {
		stepAttempts := visitAttempts(r)
		visitExtra := 0
		if req.WithinBudget {
			visitExtra = req.ExtraAttempts
		}
		if stepAttempts >= visitAttemptLimit(r)+visitExtra {
			return Run{}, errors.New("step attempt allowance exhausted; explicitly grant extra_attempts with within_budget")
		}
		requested := 1
		if req.WithinBudget {
			requested = max(0, visitAttemptLimit(r)-stepAttempts) + req.ExtraAttempts
		}
		if err := c.graphReserve(r, requested, false); err != nil {
			return Run{}, err
		}
		r.VisitExtraAttempts += visitExtra
		r.Status, r.Reason = "Queued", "authorized task retry"
	}
	r.ResourcesReleased, r.CleanupBlocked, r.CleanupError = false, false, ""
	if err := admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
		return Run{}, err
	}
	var artifacts []Artifact
	if definitionRef != nil {
		if err := c.readTaskDefinitionProofs(c.runs[r.ID]); err != nil {
			return Run{}, err
		}
		if validator, ok := c.executor.(AssignmentValidator); ok {
			profile := r.NextProfile
			if profile == "" {
				profile = r.Profiles[r.Process.Steps[r.Step].Role]
			}
			assignment, err := c.assignment(r, Attempt{ID: "attempt_" + strings.Repeat("A", 26), Step: r.Step, Visit: r.Visit, Profile: profile, Deadline: time.Now().UTC().Add(time.Minute)})
			if err != nil {
				return Run{}, err
			}
			if err := validator.ValidateAssignment(assignment); err != nil {
				return Run{}, fmt.Errorf("amended role assignment: %w", err)
			}
		}
		if err = c.writeTaskDefinitionProof(r, definitionRef, definition); err != nil {
			return Run{}, err
		}
		artifacts = []Artifact{{ID: definitionRef.ID, RunID: r.ID, ProjectID: r.ProjectID, Bytes: definitionRef.Bytes}}
	}
	receipt := Receipt{Key: req.RequestID, Kind: "retry", Args: args, RunID: r.ID}
	if err := c.persist(r, artifacts, &receipt); err != nil {
		// A commit acknowledgment can fail after the transaction committed.
		// Preserve this bounded content-addressed proof; a retry against the
		// unchanged definition reuses it rather than creating another copy.
		return Run{}, err
	}
	c.Wake()
	return c.outputRun(c.runs[r.ID]), nil
}
