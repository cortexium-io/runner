package runner

import "errors"

// Current child reservations remain authoritative even before their run row
// exists. Both task forms share the same physical database admission budget.
func (c *Core) pendingRunReservations() int {
	n := c.graphReservations()
	for _, r := range c.runs {
		for _, group := range r.TaskGroups {
			for _, child := range group.Children {
				if _, exists := c.runs[child.RunID]; !exists {
					n++
				}
			}
		}
	}
	return n
}

func (c *Core) availableRunSlots() (int, error) {
	available := -1
	if limit := c.disk.Policy.RetainedRunCapacity(); limit > 0 {
		available = max(0, limit-len(c.runs))
	}
	if store, supported := c.store.(RunCapacityStore); supported {
		physical, err := store.AvailableRunSlots(c.ctx)
		if err != nil {
			return 0, err
		}
		if physical < 0 {
			return 0, errors.New("store returned invalid byte admission capacity")
		}
		if available < 0 || physical < available {
			available = physical
		}
	}
	if available < 0 {
		return 0, errors.New("disabling max_runs requires a store with actual database-byte admission")
	}
	return max(0, available-c.pendingRunReservations()), nil
}
