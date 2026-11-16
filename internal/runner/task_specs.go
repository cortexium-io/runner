package runner

import (
	"errors"
	"reflect"
)

// TaskSpecSource points to the parent attempt that admitted this specification.
// Attempt rows retain the complete specification; operational links need only
// its task ID and the admitting attempt's identity.
type TaskSpecSource struct {
	AttemptID string `json:"attempt_id"`
}

func admittedTaskSpecs(a Attempt) []RoleTask {
	if a.End == nil || a.End.Kind != "returned" || a.End.Result == nil {
		return nil
	}
	result := a.End.Result
	action := result.TaskAction
	if action == nil || action.Kind != "delegate" && action.Kind != "revise" || result.Outcome != "ready" && result.Outcome != action.Kind {
		return nil
	}
	return action.Tasks
}

func compactTaskSpecifications(r *Run) error {
	if r.TaskGraph == nil || len(r.TaskGraph.Children) == 0 {
		return nil
	}
	g := *r.TaskGraph
	g.Children = Clone(g.Children)
	for i := range g.Children {
		link := &g.Children[i]
		if link.SpecSource != nil {
			return errors.New("task specifications must be restored before saving")
		}
		for _, attempt := range r.Attempts {
			found := false
			for _, spec := range admittedTaskSpecs(attempt) {
				if reflect.DeepEqual(link.Spec, spec) {
					link.SpecSource = &TaskSpecSource{AttemptID: attempt.ID}
					link.Spec = RoleTask{ID: spec.ID}
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	r.TaskGraph = &g
	return nil
}

// RestoreTaskSpecifications resolves normalized links after their parent's
// attempt rows have been loaded. It never substitutes a child or a rejected
// proposal, including when an admitted child has not yet materialized.
func RestoreTaskSpecifications(r *Run) error {
	if r.TaskGraph == nil {
		return nil
	}
	for i := range r.TaskGraph.Children {
		link := &r.TaskGraph.Children[i]
		if link.SpecSource == nil {
			continue
		}
		if link.Spec.ID == "" || !reflect.DeepEqual(link.Spec, RoleTask{ID: link.Spec.ID}) {
			return errors.New("normalized task link has an invalid inline specification")
		}
		var match *RoleTask
		for _, attempt := range r.Attempts {
			if attempt.ID != link.SpecSource.AttemptID {
				continue
			}
			for _, spec := range admittedTaskSpecs(attempt) {
				if spec.ID != link.Spec.ID {
					continue
				}
				if match != nil {
					return errors.New("normalized task specification is ambiguous")
				}
				copy := Clone(spec)
				match = &copy
			}
		}
		if match == nil {
			return errors.New("normalized task specification has no admitted parent attempt")
		}
		link.Spec, link.SpecSource = *match, nil
	}
	return nil
}
