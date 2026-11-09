package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

type RecoverPlanningRequest struct {
	RequestID string `json:"request_id"`
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
	Revision  int64  `json:"revision"`
}

const planningRecovery = "planning_recovery"

func rejectedPlanFeedback(reason string, rejected Value) Value {
	var plan string
	if len(rejected.Inline) <= 8<<10 && json.Unmarshal(rejected.Inline, &plan) == nil && plan != "" {
		return Text(reason + "\n\nNo cards from this plan were admitted. Correct the saved proposal below within the original request.\n" + plan)
	}
	return Text(reason)
}

// RecoverPlanning rejoins a settled planner after a rejected, wholly
// unadmitted task collection. The next visit consumes the original family
// allowance and goes through ordinary task admission.
func (c *Core) RecoverPlanning(req RecoverPlanningRequest) (Run, error) {
	args, _ := json.Marshal(req)
	c.mu.Lock()
	if r, ok, err := c.replay(req.RequestID, "recover_planning", args); ok || err != nil {
		c.mu.Unlock()
		return r, err
	}
	r, source, err := c.planPlanningRecovery(req)
	validator, supported := c.executor.(SourceRecoveryValidator)
	c.mu.Unlock()
	if err != nil {
		return Run{}, err
	}
	if !supported {
		return Run{}, errors.New("executor cannot validate retained planning source")
	}
	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	err = validator.ValidateSourceRecovery(ctx, Clone(r), source)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	if replay, ok, err := c.replay(req.RequestID, "recover_planning", args); ok || err != nil {
		return replay, err
	}
	if err != nil {
		return Run{}, fmt.Errorf("retained planning source: %w", err)
	}
	r, _, err = c.planPlanningRecovery(req)
	if err != nil {
		return Run{}, err
	}
	plan := r.Process.Steps[r.Step]
	prior, rejected := r.Attempts[len(r.Attempts)-3], r.Attempts[len(r.Attempts)-2]
	feedback := rejectedPlanFeedback(rejected.End.Result.Reason, prior.End.Result.Outputs["tasks"])
	var prompt string
	if json.Unmarshal(feedback.Inline, &prompt) != nil {
		return Run{}, errors.New("retained planner feedback is unavailable")
	}
	prompt += "\n\nThis recovery has no accepted cards or dependency manifest. Return a compact admissible collection. " +
		"If the original request requires measurement before repair, plan only the small observational scaffold now. " +
		"The configured checks and measurement collect that evidence before the separate source correction owner."
	failed := len(r.Attempts) - 1
	inputs := r.StepInputs
	c.advance(&r, &StepResult{Outcome: planningRecovery}, Route{Next: plan.ID})
	if r.Status != "Queued" {
		return Run{}, fmt.Errorf("saved planner cannot rejoin: %s", r.Reason)
	}
	for name, value := range inputs {
		if name != "feedback" && !reflect.DeepEqual(r.StepInputs[name], value) {
			return Run{}, errors.New("recovered planner inputs differ from the saved request and evidence")
		}
	}
	r.Attempts[failed].Superseded = false // Keep the settled blocked result as recorded.
	r.StepInputs["feedback"] = Text(prompt)
	if err := c.validatePlannerInputs(r); err != nil {
		return Run{}, fmt.Errorf("recovered planner input: %w", err)
	}
	r.ResourcesReleased = false
	receipt := Receipt{Key: req.RequestID, Kind: "recover_planning", Args: args, RunID: r.ID}
	if err := c.persist(r, nil, &receipt); err != nil {
		return Run{}, err
	}
	c.Wake()
	return Clone(c.runs[r.ID]), nil
}

func (c *Core) planPlanningRecovery(req RecoverPlanningRequest) (Run, SourceResult, error) {
	r, ok := c.runs[req.RunID]
	if !ok {
		return Run{}, SourceResult{}, ErrNotFound
	}
	r = Clone(r)
	reject := func(reason string) (Run, SourceResult, error) {
		return Run{}, SourceResult{}, errors.New(reason)
	}
	if req.Revision < 1 || r.Revision != req.Revision || r.Task != nil || r.Status != "Failed" || r.Active() ||
		r.CancelRequested || r.Decision != nil || c.inFlight[r.ID] || r.CleanupBlocked || r.CleanupError != "" ||
		!r.ResourcesReleased || !allAttemptsReleased(r) || len(r.TaskGroups) != 0 {
		return reject("planning recovery needs the exact settled failed root revision before any tasks were admitted")
	}
	if r.Step < 2 || r.Step+1 >= len(r.Process.Steps) || len(r.Attempts) < 5 {
		return reject("saved planner and rejected task collection are unavailable")
	}
	plan, cards := r.Process.Steps[r.Step], r.Process.Steps[r.Step+1]
	role := r.Roles[plan.Role]
	profile, err := role.Profile(r.Profiles[plan.Role])
	feedback := plan.Inputs["feedback"]
	invalidRoute := cards.Routes["invalid_plan"]
	if err != nil || profile.Harness != "codex" || profile.Access != "workspace-read" || plan.ForEach != nil ||
		plan.Escalation != nil || role.Inputs["feedback"] != "text" || role.Inputs["baseline"] != "artifact" ||
		!reflect.DeepEqual(role.Outputs, map[string]string{"tasks": "text"}) || plan.Routes["blocked"] != (Route{Next: "$fail"}) ||
		plan.Routes["ready"] != (Route{Next: cards.ID}) || cards.ForEach == nil || cards.ForEach.Mode != "isolated" ||
		cards.ForEach.Items != (Binding{FromStep: plan.ID, Output: "tasks"}) ||
		feedback.FromStep != cards.ID || feedback.Output != "feedback" || !feedback.Previous ||
		invalidRoute.Next != plan.ID || invalidRoute.Approval || invalidRoute.MaxTimes < 1 ||
		r.RouteUses[plan.ID+"/"+planningRecovery] != 0 {
		return reject("saved process has no ungranted retained investigation planner recovery")
	}
	blocked := r.Current()
	prior, rejected := r.Attempts[len(r.Attempts)-3], r.Attempts[len(r.Attempts)-2]
	if blocked == nil || blocked.ID != req.AttemptID || blocked.Superseded || !planningOutcome(*blocked, "blocked") ||
		Clip(blocked.End.Result.Reason) != r.Reason || prior.Step != r.Step ||
		rejected.Step != r.Step+1 || prior.Visit+1 != rejected.Visit || rejected.Visit+1 != r.Visit ||
		!prior.Superseded || !rejected.Superseded || !planningOutcome(prior, "ready") || !planningOutcome(rejected, "invalid_plan") ||
		prior.Profile != blocked.Profile || blocked.Profile != r.Profiles[plan.Role] ||
		r.RouteUses[cards.ID+"/invalid_plan"] < 1 {
		return reject("planning recovery needs the exact planner failure immediately after an unadmitted plan rejection")
	}
	var proposal string
	value := prior.End.Result.Outputs["tasks"]
	if len(value.Inline) > 8<<10 || json.Unmarshal(value.Inline, &proposal) != nil || proposal == "" {
		return reject("the complete rejected planner proposal is unavailable")
	}
	savedFeedback := rejected.End.Result.Outputs["feedback"]
	currentFeedback := r.StepInputs["feedback"].Inline
	if !bytes.Equal(savedFeedback.Inline, Text(rejected.End.Result.Reason).Inline) ||
		(!bytes.Equal(currentFeedback, savedFeedback.Inline) &&
			!bytes.Equal(currentFeedback, rejectedPlanFeedback(rejected.End.Result.Reason, value).Inline)) {
		return reject("the blocked planner did not receive the saved admission feedback")
	}
	baseline := r.StepInputs["baseline"]
	if baseline.ArtifactID == "" || c.recoveryArtifact(r, baseline.ArtifactID) != nil {
		return reject("the saved planner baseline is unavailable")
	}
	var source *SourceResult
	for _, attempt := range r.Attempts[:len(r.Attempts)-3] {
		if attempt.Superseded || attempt.Source == nil || attempt.Source.PatchID == "" {
			continue
		}
		if attempt.Step < 0 || attempt.Step >= len(r.Process.Steps) {
			return reject("saved source adoption step is unavailable")
		}
		role := r.Roles[r.Process.Steps[attempt.Step].Role]
		p, err := role.Profile(attempt.Profile)
		if source != nil || err != nil || p.Harness != "release" || p.Mode != "source-adopt" ||
			!planningOutcome(attempt, "ready") || attempt.Source.AttemptID != attempt.ID ||
			attempt.End.Result.Outputs["patch"].ArtifactID != attempt.Source.PatchID ||
			c.recoveryArtifact(r, attempt.Source.PatchID) != nil {
			return reject("retained source adoption evidence differs")
		}
		source = attempt.Source
	}
	if source == nil {
		return reject("an accepted retained source adoption is required")
	}
	for _, attempt := range []Attempt{prior, *blocked} {
		read := attempt.Source
		if read == nil || read.PatchID != "" || read.InputPatchID != source.PatchID ||
			read.AttemptID != attempt.ID || read.Base != source.Base || read.Repository != source.Repository ||
			read.WorkDir != source.WorkDir || read.Tree != "" {
			return reject("the planner attempts did not read the adopted source")
		}
	}
	admission := Clone(r)
	admission.Step++
	if err := c.taskGroupAdmission(admission, 1, 1); err != nil {
		return Run{}, SourceResult{}, err
	}
	return r, *source, nil
}

func planningOutcome(a Attempt, outcome string) bool {
	return !a.TimedOut && !a.StopRequested && a.End != nil && a.End.Kind == "returned" &&
		a.End.Result != nil && a.End.Result.Outcome == outcome
}

func (c *Core) validatePlannerInputs(r Run) error {
	plan := r.Process.Steps[r.Step]
	if err := ValidateValues(r.StepInputs, r.Roles[plan.Role].Inputs); err != nil {
		return err
	}
	if err := admitRunRecord(r, c.disk.Policy.Database.MaxRecord); err != nil {
		return err
	}
	validator, ok := c.executor.(AssignmentValidator)
	if !ok {
		return nil
	}
	deadline := plan.AttemptTimeout
	if deadline == "" {
		deadline = r.Process.AttemptTimeout
	}
	timeout, _ := time.ParseDuration(deadline)
	a, err := c.assignment(r, Attempt{
		ID: "attempt_" + strings.Repeat("A", 26), Step: r.Step, Visit: r.Visit,
		Profile: r.Profiles[plan.Role], Deadline: time.Now().UTC().Add(timeout),
	})
	if err != nil {
		return err
	}
	return validator.ValidateAssignment(a)
}
