package runner

import "errors"

func childResultIDs(g *TaskGraphState) (map[string]string, map[string]int) {
	ids, counts := map[string]string{}, map[string]int{}
	for _, link := range g.Children {
		ids[link.RunID] = link.Spec.ID
		counts[link.RunID]++
	}
	return ids, counts
}

func restoreTaskResultIDs(g *TaskGraphState) error {
	if g == nil || len(g.Results) == 0 {
		return nil
	}
	ids, counts := childResultIDs(g)
	for i := range g.Results {
		ref := &g.Results[i]
		if ref.ID != "" {
			continue
		}
		if ref.RunID == "" || counts[ref.RunID] != 1 || ids[ref.RunID] == "" {
			return errors.New("normalized task result has no unique retained child identity")
		}
		ref.ID = ids[ref.RunID]
	}
	return nil
}
