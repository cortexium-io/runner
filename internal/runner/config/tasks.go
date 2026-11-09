package config

import (
	"errors"
	"fmt"

	"github.com/cortexium-io/runner/internal/runner"
)

// Validate each template using the ordinary process contracts, then represent
// its join for the outer process's binding/routing checks. This does not expand
// task data or change the definition selected for execution.
func validateGroups(v runner.Process, p runner.Project) (runner.Process, runner.Project, error) {
	v = runner.Clone(v)
	p = runner.Clone(p)
	for i, step := range v.Steps {
		g := step.ForEach
		if g == nil {
			continue
		}
		if step.Role != "" || len(step.Inputs) != 0 || step.Escalation != nil || step.AttemptTimeout != "" || len(g.Steps) == 0 || len(g.Steps) > 8 {
			return v, p, errors.New("for_each selects a template, not a role; template needs 1–8 steps")
		}
		if g.Mode != "" && g.Mode != "isolated" || g.Mode == "" && (g.MaxParallel != 0 || g.ScopeInput != "") {
			return v, p, errors.New("parallel task settings require explicit isolated mode")
		}
		isolated := g.Mode == "isolated"
		if err := taskEvidence(v, p, i, *g); err != nil {
			return v, p, err
		}
		if isolated {
			if err := isolatedTemplate(v, p, *g); err != nil {
				return v, p, err
			}
		} else {
			for _, child := range g.Steps {
				if taskProfile(p, child).Harness == "task-source" {
					return v, p, errors.New("task-source child modes require isolated source ownership")
				}
			}
		}
		if err := types(g.Inputs); err != nil {
			return v, p, err
		}
		for _, kind := range g.Inputs {
			if kind == "artifact" {
				return v, p, errors.New("task data cannot grant artifact access")
			}
		}
		template := runner.Process{ID: step.ID, Inputs: runner.Clone(v.Inputs), Steps: runner.Clone(g.Steps), Outputs: map[string]runner.Binding{"selected": g.Output}, MaxAttempts: 128, MaxAttemptsPerStep: v.MaxAttemptsPerStep, AttemptTimeout: v.AttemptTimeout}
		if template.Inputs == nil {
			template.Inputs = map[string]string{}
		}
		if isolated {
			template.Inputs["_task_dependencies"] = "json"
			for name := range g.Evidence {
				template.Inputs["_task_evidence_"+name] = "artifact"
			}
		}
		for name, kind := range g.Inputs {
			if _, exists := template.Inputs["_item_"+name]; exists {
				return v, p, errors.New("task input namespace conflicts with a run input")
			}
			template.Inputs["_item_"+name] = kind
		}
		for j, s := range template.Steps {
			if s.ForEach != nil {
				return v, p, errors.New("nested task expansion is not supported")
			}
			for name, b := range s.Inputs {
				if b.FromItem != "" {
					if g.Inputs[b.FromItem] == "" || b.FromInput != "" || b.FromStep != "" || b.Literal != nil || b.Default != nil || b.Fallback != nil {
						return v, p, errors.New("from_item must select one declared item input")
					}
					b.FromInput = "_item_" + b.FromItem
					b.FromItem = ""
					s.Inputs[name] = b
				}
			}
			for _, route := range s.Routes {
				if route.Next == "$complete" {
					return v, p, errors.New("task templates finish at their join, not the whole run")
				}
			}
			template.Steps[j] = s
		}
		if err := validateProcessInputs(template, p, 16); err != nil {
			return v, p, fmt.Errorf("group %s: %w", step.ID, err)
		}
		kind := ""
		for _, s := range g.Steps {
			if s.ID == g.Output.FromStep {
				kind = p.Roles[s.Role].Outputs[g.Output.Output]
			}
		}
		if kind == "" || g.Output.FromInput != "" || g.Output.Default != nil || g.Output.Literal != nil || g.Output.Fallback != nil {
			return v, p, errors.New("task output must select a template step output")
		}
		name := step.ID + "-join"
		if p.Roles[name].ID != "" {
			return v, p, errors.New("task join role name conflicts with a configured role")
		}
		p.Roles[name] = runner.Role{ID: name, Inputs: map[string]string{"items": "json"}, Outputs: map[string]string{"results": "text", "last": kind}}
		if isolated {
			p.Roles[name] = runner.Role{ID: name, Inputs: map[string]string{"items": "json"}, Outputs: map[string]string{"results": "artifact", "feedback": "text"}}
		}
		// Items may come from a planner's text JSON, or direct JSON intake.
		items := g.Items
		if items.FromInput != "" {
			p.Roles[name] = withItemsKind(p.Roles[name], v.Inputs[items.FromInput])
		} else {
			for _, s := range v.Steps {
				if s.ID == items.FromStep {
					p.Roles[name] = withItemsKind(p.Roles[name], p.Roles[s.Role].Outputs[items.Output])
				}
			}
		}
		itemKind := p.Roles[name].Inputs["items"]
		if itemKind != "text" && itemKind != "json" {
			return v, p, errors.New("task items must be declared text or JSON")
		}
		step.Role = name
		step.Inputs = map[string]runner.Binding{"items": items}
		for evidence, binding := range g.Evidence {
			key := "evidence_" + evidence
			p.Roles[name].Inputs[key] = "artifact"
			step.Inputs[key] = binding
		}
		step.ForEach = nil
		v.Steps[i] = step
		// Reentering a join must not silently skip reexecution of the collection.
		for j, s := range v.Steps {
			if j >= i {
				for outcome, route := range s.Routes {
					for k := 0; k <= i; k++ {
						if route.Next == v.Steps[k].ID && !(j == i && invalidPlanReturn(v, p, i, *g, outcome, route)) {
							return v, p, errors.New("return paths across task groups require separate task-local iteration")
						}
					}
				}
			}
		}
	}
	return v, p, nil
}
func withItemsKind(r runner.Role, kind string) runner.Role { r.Inputs["items"] = kind; return r }
