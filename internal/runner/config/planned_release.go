package config

import (
	"errors"
	"reflect"

	"github.com/cortexium-io/runner/internal/runner"
)

// Planning adds only a fixed admitted source prefix. The existing release
// suffix still owns qualification, correction and every protected decision.
func plannedReleaseOwner(p runner.Process, project runner.Project, first, release int) (int, error) {
	bad := errors.New("planned delivery requires admitted planning, isolated source cards, an owned join and a separate combined correction owner")
	if first+5 >= release || p.MaxAttempts != 64 {
		return 0, bad
	}
	plan, cards, join, owner := p.Steps[first], p.Steps[first+1], p.Steps[first+2], p.Steps[first+3]
	empty := runner.Text("")
	planInputs := map[string]runner.Binding{"request": {FromInput: "request"}, "feedback": {FromStep: cards.ID, Output: "feedback", Previous: true, Default: &empty}}
	if cards.ForEach != nil {
		for name, binding := range cards.ForEach.Evidence {
			planInputs[name] = binding
		}
	}
	if plan.ForEach != nil || plan.Reuse || plan.Escalation != nil ||
		!reflect.DeepEqual(plan.Inputs, planInputs) ||
		!reflect.DeepEqual(project.Roles[plan.Role].Outputs, map[string]string{"tasks": "text"}) ||
		!reflect.DeepEqual(plan.Routes, map[string]runner.Route{"ready": {Next: cards.ID}, "blocked": {Next: "$fail"}}) ||
		cards.ForEach == nil || cards.ForEach.Mode != "isolated" || cards.ForEach.Items != (runner.Binding{FromStep: plan.ID, Output: "tasks"}) ||
		!reflect.DeepEqual(cards.Routes, map[string]runner.Route{"ready": {Next: join.ID}, "blocked": {Next: "$fail"}, "invalid_plan": {Next: plan.ID, MaxTimes: 2}}) || isolatedTemplate(p, project, *cards.ForEach) != nil {
		return 0, bad
	}
	jp, op := taskProfile(project, join), taskProfile(project, owner)
	if jp.Harness != "task-source" || jp.Mode != "join" || join.ForEach != nil || join.Reuse || join.Escalation != nil ||
		!reflect.DeepEqual(join.Inputs, map[string]runner.Binding{"results": {FromStep: cards.ID, Output: "results"}}) ||
		len(join.Routes) != 3 || join.Routes["ready"] != (runner.Route{Next: p.Steps[first+4].ID}) || join.Routes["blocked"] != (runner.Route{Next: "$fail"}) ||
		join.Routes["changes_requested"].Next != owner.ID || join.Routes["changes_requested"].Approval || join.Routes["changes_requested"].MaxTimes < 1 || join.Routes["changes_requested"].MaxTimes > 3 ||
		op.Harness != "codex" || op.Access != "workspace-write" || owner.ForEach != nil || owner.Reuse || owner.Escalation != nil ||
		owner.Inputs["request"] != (runner.Binding{FromInput: "request"}) || owner.Inputs["manifest"] != (runner.Binding{FromStep: join.ID, Output: "manifest"}) ||
		!reflect.DeepEqual(owner.Routes, map[string]runner.Route{"blocked": {Next: "$fail"}}) {
		return 0, bad
	}
	// The final review receives the accepted plan/card manifest and current
	// candidate; an internal task pin can never manufacture its APPROVE verdict.
	integrate := p.Steps[release-1]
	verdict := integrate.Inputs["verdict"]
	for _, review := range p.Steps[first+4 : release-1] {
		if review.ID != verdict.FromStep {
			continue
		}
		rp := taskProfile(project, review)
		if rp.Harness == "codex" && rp.Access == "workspace-read" && review.Inputs["request"] == (runner.Binding{FromInput: "request"}) &&
			review.Inputs["manifest"] == (runner.Binding{FromStep: join.ID, Output: "manifest"}) && review.Inputs["patch"] == (runner.Binding{FromCandidate: true}) {
			return first + 3, nil
		}
	}
	return 0, bad
}
