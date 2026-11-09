package config

import (
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
)

func TestBindingFallbackRequiresEarlierMatchingConfiguredOutput(t *testing.T) {
	p := runner.Process{
		ID: "evidence", Inputs: map[string]string{"text": "text"},
		Steps: []runner.Step{
			{ID: "baseline", Role: "measure", Inputs: map[string]runner.Binding{}},
			{ID: "owner", Role: "owner", Inputs: map[string]runner.Binding{"evidence": {FromStep: "measure", Output: "artifact", Previous: true, Fallback: &runner.Binding{FromStep: "baseline", Output: "artifact"}}}},
			{ID: "measure", Role: "measure", Inputs: map[string]runner.Binding{}},
		},
		Outputs:     map[string]runner.Binding{"artifact": {FromStep: "measure", Output: "artifact"}},
		MaxAttempts: 8, MaxAttemptsPerStep: 1, AttemptTimeout: "1m",
	}
	project := runner.Project{Roles: map[string]runner.Role{
		"measure": {Outputs: map[string]string{"artifact": "artifact", "text": "text"}},
		"owner":   {Inputs: map[string]string{"evidence": "artifact"}},
	}}
	if err := validateProcess(p, project); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*runner.Binding){
		"nested": func(b *runner.Binding) {
			b.Fallback.Fallback = &runner.Binding{FromStep: "baseline", Output: "artifact"}
		},
		"literal grant":     func(b *runner.Binding) { b.Fallback.Literal = &runner.Value{ArtifactID: "other-run/private"} },
		"input grant":       func(b *runner.Binding) { b.Fallback.FromInput = "text" },
		"default mixing":    func(b *runner.Binding) { v := runner.Text(""); b.Default = &v },
		"future fallback":   func(b *runner.Binding) { b.Fallback.FromStep = "measure" },
		"missing fallback":  func(b *runner.Binding) { b.Fallback.Output = "missing" },
		"wrong type":        func(b *runner.Binding) { b.Fallback.Output = "text" },
		"fallback previous": func(b *runner.Binding) { b.Fallback.Previous = true },
		"input primary":     func(b *runner.Binding) { b.FromStep = ""; b.FromInput = "text" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := runner.Clone(p)
			b := bad.Steps[1].Inputs["evidence"]
			change(&b)
			bad.Steps[1].Inputs["evidence"] = b
			if err := validateProcess(bad, project); err == nil {
				t.Fatal("invalid fallback accepted")
			}
		})
	}
}
