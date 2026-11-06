package config

import (
	"errors"
	"reflect"
	"strings"

	"github.com/cortexium-io/runner/internal/runner"
)

func taskSourceContract(role runner.Role, mode string) error {
	inputs := map[string]string{"dependencies": "json"}
	outputs := map[string]string{"patch": "artifact", "manifest": "artifact"}
	switch mode {
	case "pin":
		inputs = map[string]string{"patch": "artifact", "summary": "text"}
		outputs = map[string]string{"candidate": "json", "manifest": "artifact", "feedback": "text"}
	case "join":
		inputs = map[string]string{"results": "artifact"}
		outputs["feedback"] = "text"
	case "seed":
	default:
		return errors.New("unknown task-source mode")
	}
	if !reflect.DeepEqual(role.Inputs, inputs) || !reflect.DeepEqual(role.Outputs, outputs) {
		return errors.New("task-source role does not match its fixed seed/pin/join contract")
	}
	return nil
}

func taskProfile(p runner.Project, step runner.Step) runner.Profile {
	profile, _ := p.Roles[step.Role].Profile(p.Profiles[step.Role])
	return profile
}

// Only the frozen child template may seed/pin an unreviewed task. Root joins
// consume the immediately preceding group's owned artifact, never intake data.
func taskSourcePlacement(v runner.Process, p runner.Project) error {
	for i, step := range v.Steps {
		profile := taskProfile(p, step)
		if profile.Harness != "task-source" {
			continue
		}
		if profile.Mode != "join" || i == 0 || v.Steps[i-1].ForEach == nil || v.Steps[i-1].ForEach.Mode != "isolated" ||
			!reflect.DeepEqual(step.Inputs, map[string]runner.Binding{"results": {FromStep: v.Steps[i-1].ID, Output: "results"}}) {
			return errors.New("root task-source join must consume the preceding isolated group's results; seed/pin are child-only")
		}
	}
	return nil
}

func taskEvidence(v runner.Process, p runner.Project, index int, g runner.TaskGroup) error {
	if len(g.Evidence) > 3 || len(g.Evidence) > 0 && g.Mode != "isolated" {
		return errors.New("isolated tasks accept at most three configured artifact evidence bindings")
	}
	for name, binding := range g.Evidence {
		if !validID(name) || strings.HasPrefix(name, "_") || binding.FromStep == "" || binding.Output == "" || binding != (runner.Binding{FromStep: binding.FromStep, Output: binding.Output}) {
			return errors.New("task evidence selects only an earlier named artifact output")
		}
		found := false
		for _, step := range v.Steps[:index] {
			found = found || step.ID == binding.FromStep && p.Roles[step.Role].Outputs[binding.Output] == "artifact"
		}
		if !found {
			return errors.New("task evidence artifact producer must precede its collection")
		}
	}
	return nil
}

// Invalid unaccepted plan data may return to its immediately preceding planner.
// No later source/check step can use this exception to repeat accepted cards.
func invalidPlanReturn(v runner.Process, p runner.Project, index int, g runner.TaskGroup, outcome string, route runner.Route) bool {
	if index < 1 || outcome != "invalid_plan" || route.Approval || route.MaxTimes < 1 || route.MaxTimes > 2 {
		return false
	}
	plan := v.Steps[index-1]
	profile := taskProfile(p, plan)
	empty := runner.Text("")
	return g.Mode == "isolated" && route.Next == plan.ID && g.Items == (runner.Binding{FromStep: plan.ID, Output: "tasks"}) &&
		profile.Harness == "codex" && profile.Access == "workspace-read" && plan.ForEach == nil && !plan.Reuse && plan.Escalation == nil &&
		reflect.DeepEqual(plan.Inputs["feedback"], runner.Binding{FromStep: v.Steps[index].ID, Output: "feedback", Previous: true, Default: &empty})
}

func isolatedTemplate(v runner.Process, p runner.Project, g runner.TaskGroup) error {
	bad := errors.New("isolated tasks require source, explicit bounded concurrency/scope, and the fixed seed/owner/internal-pin template")
	if p.Source == nil || g.MaxParallel < 1 || g.MaxParallel > 8 || !validID(g.ScopeInput) || len(g.Inputs) != 2 || g.Inputs[g.ScopeInput] != "json" || g.Inputs["task"] != "text" || v.Inputs["request"] != "text" || len(g.Steps) != 3 {
		return bad
	}
	if _, exists := v.Inputs["_task_dependencies"]; exists {
		return errors.New("_task_dependencies is reserved for Core-owned task source")
	}
	for name := range v.Inputs {
		if strings.HasPrefix(name, "_task_evidence_") {
			return errors.New("_task_evidence_ is reserved for frozen task evidence")
		}
	}
	seed, owner, pin := g.Steps[0], g.Steps[1], g.Steps[2]
	sp, op, pp := taskProfile(p, seed), taskProfile(p, owner), taskProfile(p, pin)
	if sp.Harness != "task-source" || sp.Mode != "seed" || pp.Harness != "task-source" || pp.Mode != "pin" || op.Harness != "codex" || op.Access != "workspace-write" ||
		seed.Reuse || owner.Reuse || pin.Reuse || seed.Escalation != nil || owner.Escalation != nil || pin.Escalation != nil ||
		taskSourceContract(p.Roles[seed.Role], "seed") != nil || taskSourceContract(p.Roles[pin.Role], "pin") != nil {
		return bad
	}
	if !reflect.DeepEqual(seed.Inputs, map[string]runner.Binding{"dependencies": {FromInput: "_task_dependencies"}}) ||
		!reflect.DeepEqual(pin.Inputs, map[string]runner.Binding{"patch": {FromStep: owner.ID, Output: "patch"}, "summary": {FromStep: owner.ID, Output: "summary"}}) ||
		g.Output != (runner.Binding{FromStep: pin.ID, Output: "candidate"}) {
		return bad
	}
	empty := runner.Text("")
	ownerInputs := map[string]runner.Binding{
		"request":  {FromInput: "request"},
		"task":     {FromItem: "task"},
		"paths":    {FromItem: g.ScopeInput},
		"manifest": {FromStep: pin.ID, Output: "manifest", Previous: true, Fallback: &runner.Binding{FromStep: seed.ID, Output: "manifest"}},
		"feedback": {FromStep: pin.ID, Output: "feedback", Previous: true, Default: &empty},
	}
	for name := range g.Evidence {
		if _, exists := ownerInputs[name]; exists || p.Roles[owner.Role].Outputs[name] != "" {
			return errors.New("task evidence cannot shadow owner source or feedback ports")
		}
		ownerInputs[name] = runner.Binding{FromInput: "_task_evidence_" + name}
		if p.Roles[owner.Role].Inputs[name] != "artifact" {
			return errors.New("task evidence requires an explicit owner artifact input")
		}
	}
	if !reflect.DeepEqual(owner.Inputs, ownerInputs) {
		return errors.New("task owner requires original request, scoped card, authenticated source manifest and bounded pin feedback")
	}
	if !reflect.DeepEqual(seed.Routes, map[string]runner.Route{"blocked": {Next: "$fail"}}) || !reflect.DeepEqual(owner.Routes, map[string]runner.Route{"blocked": {Next: "$fail"}}) ||
		len(pin.Routes) != 2 || pin.Routes["blocked"] != (runner.Route{Next: "$fail"}) || pin.Routes["changes_requested"].Next != owner.ID || pin.Routes["changes_requested"].Approval || pin.Routes["changes_requested"].MaxTimes < 1 || pin.Routes["changes_requested"].MaxTimes > 3 {
		return bad
	}
	return nil
}
