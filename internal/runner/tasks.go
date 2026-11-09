package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

func selectedRoles(steps []Step, roles map[string]Role, profiles map[string]string, p Project) {
	for _, step := range steps {
		if step.ForEach != nil {
			selectedRoles(step.ForEach.Steps, roles, profiles, p)
			continue
		}
		roles[step.Role] = Clone(p.Roles[step.Role])
		profiles[step.Role] = p.Profiles[step.Role]
	}
}

// Expansion is a persisted transformation of a declared template. The same
// coordinator, attempts, bindings, workspace and retention own the resulting work.
func expandTasks(r *Run) error {
	step := r.Process.Steps[r.Step]
	g := step.ForEach
	inputs, err := bindings(*r, map[string]Binding{"items": g.Items})
	if err != nil {
		return err
	}
	data := inputs["items"].Inline
	var text string
	if json.Unmarshal(data, &text) == nil {
		data = []byte(text)
	}
	var items []TaskItem
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&items); err != nil || dec.Decode(new(any)) != io.EOF || len(items) < 1 || len(items) > 8 {
		return errors.New("task collection must contain 1–8 declared items")
	}
	byID := map[string]TaskItem{}
	for _, item := range items {
		if !workflowID(item.ID) || byID[item.ID].ID != "" {
			return errors.New("task IDs must be valid and unique")
		}
		values := map[string]Value{}
		for name, value := range item.Inputs {
			values[name] = Value{Inline: value}
		}
		if err := ValidateValues(values, g.Inputs); err != nil {
			return fmt.Errorf("task %s: %w", item.ID, err)
		}
		byID[item.ID] = item
	}
	for _, item := range items {
		seen := map[string]bool{}
		for _, dep := range item.DependsOn {
			if byID[dep].ID == "" || dep == item.ID || seen[dep] {
				return fmt.Errorf("task %s has invalid dependency %s", item.ID, dep)
			}
			seen[dep] = true
		}
	}
	ordered := []TaskItem{}
	done := map[string]bool{}
	for len(ordered) < len(items) {
		progress := false
		for _, item := range items {
			if done[item.ID] {
				continue
			}
			ready := true
			for _, dep := range item.DependsOn {
				if !done[dep] {
					ready = false
				}
			}
			if ready {
				ordered = append(ordered, item)
				done[item.ID] = true
				progress = true
			}
		}
		if !progress {
			return errors.New("task dependency cycle")
		}
	}
	if len(r.Process.Steps)+len(items)*len(g.Steps) > 64 {
		return errors.New("expanded process exceeds 64 steps")
	}
	join := Step{ID: step.ID, Role: step.ID + "-join", Inputs: map[string]Binding{}, Routes: Clone(step.Routes)}
	role := Role{ID: join.Role, Purpose: "Retain completed task results", Inputs: map[string]string{}, Outputs: map[string]string{"results": "text"}, Execution: &Profile{Harness: "deterministic", Mode: "collect", Output: "results"}}
	exportKind := ""
	expanded := []Step{}
	for _, item := range ordered {
		prefix := step.ID + "-" + item.ID + "-"
		names := map[string]bool{}
		for _, s := range g.Steps {
			names[s.ID] = true
		}
		bind := func(b Binding) Binding {
			b = Clone(b)
			if b.FromItem != "" {
				b.Literal = &Value{Inline: item.Inputs[b.FromItem]}
				b.FromItem = ""
			}
			if names[b.FromStep] {
				b.FromStep = prefix + b.FromStep
			}
			if b.Fallback != nil && names[b.Fallback.FromStep] {
				b.Fallback.FromStep = prefix + b.Fallback.FromStep
			}
			return b
		}
		for _, template := range g.Steps {
			s := Clone(template)
			s.ID = prefix + s.ID
			if !workflowID(s.ID) {
				return errors.New("expanded task step ID exceeds 64 bytes")
			}
			for name, b := range s.Inputs {
				s.Inputs[name] = bind(b)
			}
			for outcome, route := range s.Routes {
				if names[route.Next] {
					route.Next = prefix + route.Next
				}
				s.Routes[outcome] = route
			}
			expanded = append(expanded, s)
		}
		output := bind(g.Output)
		join.Inputs[item.ID] = output
		for _, s := range g.Steps {
			if s.ID == g.Output.FromStep {
				exportKind = r.Roles[s.Role].Outputs[g.Output.Output]
			}
		}
		role.Inputs[item.ID] = exportKind
	}
	// A group's final export is useful for cumulative source patches; the results
	// manifest retains each item's selected output without loading artifact bytes.
	lastItem := ordered[len(ordered)-1]
	role.Outputs["last"] = exportKind
	role.Execution.Input = lastItem.ID
	r.Roles[role.ID] = role
	expanded = append(expanded, join)
	steps := append([]Step{}, r.Process.Steps[:r.Step]...)
	steps = append(steps, expanded...)
	steps = append(steps, r.Process.Steps[r.Step+1:]...)
	ids := map[string]bool{}
	for _, step := range steps {
		if ids[step.ID] {
			return errors.New("expanded task step IDs conflict with the process")
		}
		ids[step.ID] = true
	}
	r.Process.Steps = steps
	r.StepInputs = nil
	return nil
}
func workflowID(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
