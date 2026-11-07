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
	plan, cards, join := p.Steps[first], p.Steps[first+1], p.Steps[first+2]
	ownerIndex := first + 3
	if taskProfile(project, p.Steps[ownerIndex]).Harness == "command" {
		if !plannedMeasurementSnapshot(p, project, ownerIndex, release) {
			return 0, bad
		}
		ownerIndex++
	}
	owner := p.Steps[ownerIndex]
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
		len(join.Routes) != 3 || join.Routes["ready"] != (runner.Route{Next: p.Steps[ownerIndex+1].ID}) || join.Routes["blocked"] != (runner.Route{Next: "$fail"}) ||
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
	for _, review := range p.Steps[ownerIndex+1 : release-1] {
		if review.ID != verdict.FromStep {
			continue
		}
		rp := taskProfile(project, review)
		if rp.Harness == "codex" && rp.Access == "workspace-read" && review.Inputs["request"] == (runner.Binding{FromInput: "request"}) &&
			review.Inputs["manifest"] == (runner.Binding{FromStep: join.ID, Output: "manifest"}) && review.Inputs["patch"] == (runner.Binding{FromCandidate: true}) {
			return ownerIndex, nil
		}
	}
	return 0, bad
}

// The optional snapshot is physically before the correction owner, so later
// owner backedges cannot supersede it. Its only entry follows the first actual
// candidate measurement; the typed baseline fallback is never used on that path.
func plannedMeasurementSnapshot(p runner.Process, project runner.Project, index, release int) bool {
	if index+3 >= release {
		return false
	}
	snapshot, owner := p.Steps[index], p.Steps[index+1]
	role := project.Roles[snapshot.Role]
	expected := runner.Profile{
		Harness: "command",
		Access:  "workspace-read",
		Command: &runner.Command{
			Args:     []string{"/bin/cp", "{input.measurement}", "{scratch}/before-measurements.json"},
			Artifact: "before-measurements.json",
		},
	}
	if snapshot.AttemptTimeout != "" || snapshot.ForEach != nil || snapshot.Reuse || snapshot.Escalation != nil ||
		!reflect.DeepEqual(taskProfile(project, snapshot), expected) ||
		!reflect.DeepEqual(role.Inputs, map[string]string{"measurement": "artifact"}) ||
		!reflect.DeepEqual(role.Outputs, map[string]string{"check": "text", "artifact": "artifact"}) ||
		!reflect.DeepEqual(snapshot.Routes, map[string]runner.Route{"ready": {Next: owner.ID}, "blocked": {Next: "$fail"}}) {
		return false
	}
	measurement := snapshot.Inputs["measurement"].FromStep
	baseline := p.Steps[0]
	binding := runner.Binding{
		FromStep: measurement,
		Output:   "artifact",
		Previous: true,
		Fallback: &runner.Binding{FromStep: baseline.ID, Output: "artifact"},
	}
	if measurement == "" || !reflect.DeepEqual(snapshot.Inputs, map[string]runner.Binding{"measurement": binding}) ||
		taskProfile(project, baseline).Harness != "command" || taskProfile(project, baseline).Access != "workspace-read" || project.Roles[baseline.Role].Outputs["artifact"] != "artifact" {
		return false
	}
	reviewID := p.Steps[release-1].Inputs["verdict"].FromStep
	measurementIndex, reviewIndex, incoming := -1, -1, 0
	for i, step := range p.Steps {
		if step.ID == measurement {
			measurementIndex = i
			profile := taskProfile(project, step)
			if profile.Harness != "command" || profile.Access != "workspace-read" || project.Roles[step.Role].Outputs["artifact"] != "artifact" || step.Routes["ready"] != (runner.Route{Next: reviewID}) {
				return false
			}
		}
		if step.ID == reviewID {
			reviewIndex = i
			if step.Inputs["before_measurements"] != (runner.Binding{FromStep: snapshot.ID, Output: "artifact"}) {
				return false
			}
		}
		for outcome, route := range step.Routes {
			if route.Next == snapshot.ID {
				incoming++
				if step.ID != measurement || outcome != "continue" || route != (runner.Route{Next: snapshot.ID, MaxTimes: 1}) {
					return false
				}
			}
		}
	}
	return incoming == 1 && measurementIndex > index+1 && measurementIndex < reviewIndex && reviewIndex < release-1
}
