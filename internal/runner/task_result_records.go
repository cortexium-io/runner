package runner

import "errors"

type taskGraphRecordAlias TaskGraphState

// Only the disk representation omits duplicate task IDs. Public/wire results
// keep their complete identity and explicit result inputs are never compacted.
type taskGraphRecord struct {
	taskGraphRecordAlias
	Results []taskResultRecord `json:"results,omitempty"`
}

type taskResultRecord struct {
	ID    string `json:"id,omitempty"`
	RunID string `json:"run_id"`
}

func childResultIDs(g *TaskGraphState) (map[string]string, map[string]int) {
	ids, counts := map[string]string{}, map[string]int{}
	for _, link := range g.Children {
		ids[link.RunID] = link.Spec.ID
		counts[link.RunID]++
	}
	return ids, counts
}

func compactTaskResultIDs(g *TaskGraphState) *taskGraphRecord {
	if g == nil {
		return nil
	}
	ids, counts := childResultIDs(g)
	row := &taskGraphRecord{taskGraphRecordAlias: taskGraphRecordAlias(*g)}
	for _, ref := range g.Results {
		id := ref.ID
		if id != "" && ref.RunID != "" && counts[ref.RunID] == 1 && ids[ref.RunID] == id {
			id = ""
		}
		row.Results = append(row.Results, taskResultRecord{ID: id, RunID: ref.RunID})
	}
	return row
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
