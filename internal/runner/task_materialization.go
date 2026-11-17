package runner

import (
	"reflect"
	"slices"
)

// TaskMaterialization authenticates consumption of a retained allocation, not
// new admission. Reusing the actual builders keeps every frozen role, process,
// input, dependency and source claim intact. No execution has started yet.
func TaskMaterialization(root, parent, child Run) bool {
	if child.ID == "" || child.Revision != 1 || child.Created.IsZero() || child.ProjectID != parent.ProjectID || len(child.Attempts) != 0 {
		return false
	}
	var expected Run
	if child.TaskGraph != nil && child.Task == nil {
		if root.TaskGraph == nil || parent.TaskGraph == nil || root.ID != root.TaskGraph.RootRunID || root.TaskGraph.ParentRunID != "" || parent.TaskGraph.RootRunID != root.ID || parent.ProjectID != root.ProjectID || parent.Repository != root.Repository || root.Process.Tasks == nil {
			return false
		}
		var selected *RoleTaskLink
		for i := range parent.TaskGraph.Children {
			link := &parent.TaskGraph.Children[i]
			if link.RunID == child.ID {
				if selected != nil {
					return false
				}
				selected = link
			}
		}
		if selected == nil || root.Roles[selected.Spec.Role].ID != selected.Spec.Role || !slices.Contains(root.Process.Tasks.Roles, selected.Spec.Role) {
			return false
		}
		cancelled := child.Status == "Cancelled" && child.CancelRequested && (parent.CancelRequested || parent.Terminal() || root.CancelRequested || root.Terminal())
		if !cancelled && (selected.Retired || parent.Terminal() || root.Terminal() || parent.CancelRequested || root.CancelRequested || parent.TaskGraph.Phase != TaskWaitingChildren) {
			return false
		}
		expected = graphChild(parent, root, *selected)
		if cancelled {
			expected = cancelledGraphChild(parent, root, *selected)
		}
		// General child creation owns its fresh timestamp; the allocation fixes
		// all authority, while the normal persistence boundary adds revision/time.
		expected.Created = child.Created
	} else if child.Task != nil && child.TaskGraph == nil {
		id := child.Task.Identity
		if root.ID != parent.ID || id.RootRunID != parent.ID || parent.TaskGraph != nil || !isolatedStep(parent) {
			return false
		}
		state := parent.TaskGroups[id.GroupStep]
		step := parent.Process.Steps[parent.Step]
		if state == nil || state.Done || state.StepID != id.GroupStep || step.ID != state.StepID || len(state.Children) != len(state.Items) {
			return false
		}
		index := -1
		for i, reservation := range state.Children {
			if reservation.RunID == child.ID {
				if index >= 0 || reservation.ItemID != id.ItemID || state.Items[i].ID != id.ItemID {
					return false
				}
				index = i
			}
		}
		if index < 0 {
			return false
		}
		if parent.Terminal() && !parent.CancelRequested && state.Failure == "" {
			return false
		}
		expected = initialTaskChild(parent, state, state.Items[index], child.ID)
	} else {
		return false
	}
	expected.Revision, expected.Updated = child.Revision, child.Updated
	return reflect.DeepEqual(Clone(expected), Clone(child))
}
