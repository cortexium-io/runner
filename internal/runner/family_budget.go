package runner

import (
	"errors"
	"fmt"
	"math"
)

// Callers hold Core.mu. Invalid family membership exhausts admission rather
// than accidentally giving an orphaned or duplicated child another budget.
const invalidFamilyAttempts = math.MaxInt / 2

func (c *Core) familyAttempts(r Run) int {
	root, err := c.taskRoot(r)
	if err != nil {
		return invalidFamilyAttempts
	}
	if r.ID == root.ID {
		root = r // Include the caller's current, not-yet-persisted state.
	}
	if root.Task == nil {
		for id, possibleParent := range c.runs {
			if id == root.ID {
				continue
			}
			for _, group := range possibleParent.TaskGroups {
				if group == nil {
					continue
				}
				for _, child := range group.Children {
					if child.RunID == root.ID {
						return invalidFamilyAttempts // A reserved child cannot become another root.
					}
				}
			}
		}
	}
	used := len(root.Attempts)
	children := map[string]bool{}
	for groupID, group := range root.TaskGroups {
		if group == nil || group.StepID != groupID {
			return invalidFamilyAttempts
		}
		for _, entry := range group.Children {
			if entry.RunID == "" || children[entry.RunID] {
				return invalidFamilyAttempts
			}
			child, ok := c.runs[entry.RunID]
			if r.ID == entry.RunID {
				child, ok = r, true
			}
			children[entry.RunID] = true
			if !ok {
				if !c.reservedTaskChild(root, groupID, entry) {
					return invalidFamilyAttempts
				}
				continue // A durable reservation has not dispatched any work yet.
			}
			if child.Task == nil || child.Task.Identity != (TaskIdentity{RootRunID: root.ID, GroupStep: groupID, ItemID: entry.ItemID}) {
				return invalidFamilyAttempts
			}
			owner, err := c.taskRoot(child)
			if err != nil || owner.ID != root.ID || len(child.Attempts) > invalidFamilyAttempts-used {
				return invalidFamilyAttempts
			}
			used += len(child.Attempts)
		}
	}
	for id, child := range c.runs {
		if child.Task != nil && child.Task.Identity.RootRunID == root.ID && !children[id] {
			return invalidFamilyAttempts
		}
	}
	return used
}

func (c *Core) reservedTaskChild(root Run, groupID string, entry TaskChild) bool {
	saved, ok := c.runs[root.ID]
	if !ok || saved.Task != nil || saved.ProjectID != root.ProjectID {
		return false
	}
	group := saved.TaskGroups[groupID]
	if group == nil || group.StepID != groupID || group.Done || entry.Candidate != nil {
		return false
	}
	items, reservations := 0, 0
	for _, item := range group.Items {
		if item.ID == entry.ItemID {
			items++
		}
	}
	for _, child := range group.Children {
		if child.RunID == entry.RunID && child.ItemID == entry.ItemID {
			if child.Candidate != nil {
				return false
			}
			reservations++
		}
	}
	return items == 1 && reservations == 1
}

func (c *Core) familyRemaining(r Run) int {
	root, err := c.taskRoot(r)
	if err != nil {
		return 0
	}
	return max(0, root.Process.MaxAttempts-c.familyAttempts(r))
}

// The scheduler supplies the finite, validated root-tail and unfinished-child
// cost. This helper only enforces their shared frozen ceiling; it does not infer
// scheduling authority or traverse arbitrary process graphs.
func (c *Core) familyReserve(r Run, requestedAttempts, reservedRootTail int) error {
	if requestedAttempts < 0 || reservedRootTail < 0 {
		return errors.New("shared task attempt reserve: costs must be nonnegative")
	}
	root, err := c.taskRoot(r)
	if err != nil {
		return fmt.Errorf("shared task attempt reserve: %w", err)
	}
	used := c.familyAttempts(r)
	if used == invalidFamilyAttempts {
		return errors.New("shared task attempt reserve: family ownership is invalid")
	}
	remaining := root.Process.MaxAttempts - used
	if requestedAttempts > remaining || reservedRootTail > remaining-requestedAttempts {
		return fmt.Errorf("shared task attempt reserve: need %d dispatch and %d reserved completion attempts, have %d remaining", requestedAttempts, reservedRootTail, remaining)
	}
	return nil
}
