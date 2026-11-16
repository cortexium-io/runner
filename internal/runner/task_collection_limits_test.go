package runner

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestLegacyTaskCollectionsHaveOptionalWorkCaps(t *testing.T) {
	for _, count := range []int{9, 65} {
		items := make([]TaskItem, count)
		for i := range items {
			items[i] = TaskItem{
				ID: fmt.Sprintf("card%d", i),
				Inputs: map[string]json.RawMessage{
					"paths": json.RawMessage(fmt.Sprintf(`["file%d"]`, i)),
				},
			}
		}
		data, err := json.Marshal(items)
		if err != nil {
			t.Fatal(err)
		}
		group := TaskGroup{
			Items:      Binding{FromInput: "items"},
			Inputs:     map[string]string{"paths": "json"},
			ScopeInput: "paths",
			Steps:      []Step{{ID: "work", Role: "worker"}},
			Output:     Binding{FromStep: "work", Output: "report"},
		}
		original := Run{
			Inputs: map[string]Value{"items": {Inline: data}},
			Roles: map[string]Role{
				"worker": {Outputs: map[string]string{"report": "text"}},
			},
			Process: Process{Steps: []Step{{ID: "cards", ForEach: &group}}},
		}
		for _, enforce := range []bool{false, true} {
			r := Clone(original)
			err := expandTasks(&r, enforce)
			if (err != nil) != enforce || !enforce && len(r.Process.Steps) != count+1 {
				t.Fatalf("serial expansion count=%d enforce=%v: %d %v", count, enforce, len(r.Process.Steps), err)
			}
			if count == 9 {
				got, err := decodeTaskItems(original, &group, enforce)
				if (err != nil) != enforce || !enforce && len(got) != count {
					t.Fatalf("isolated collection enforce=%v: %d %v", enforce, len(got), err)
				}
			}
		}
	}
}
