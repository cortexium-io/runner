package runner

import "testing"

func TestSourceSharedDispatchKeepsSourceIdentityAndCandidateInput(t *testing.T) {
	policy := DefaultStorage()
	r := Run{
		ID: "run_owned", ProjectID: "demo", Repository: "/configured/repository", Source: &SourceSettings{Ref: "frozen-source"},
		Roles: map[string]Role{"check": {ID: "check", Purpose: "Check current source", Outputs: map[string]string{"check": "text"},
			Execution: &Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &Command{Args: []string{"/fixed/node", "{workspace}/verify.mjs"}}}}},
		Process:    Process{Steps: []Step{{ID: "check", Role: "check", Inputs: map[string]Binding{"patch": {FromCandidate: true}}}}},
		StepInputs: map[string]Value{"patch": {ArtifactID: "attempt_owner/changes.patch"}},
	}
	c := &Core{disk: &Disk{Root: t.TempDir(), Policy: policy}, snapshot: Snapshot{Instance: Instance{Concurrency: 1}}}
	for _, kind := range []string{"valid", "no source", "write access", "caller patch"} {
		t.Run(kind, func(t *testing.T) {
			r := Clone(r)
			switch kind {
			case "no source":
				r.Source = nil
			case "write access":
				r.Roles["check"].Execution.Access = "workspace-write"
			case "caller patch":
				r.Process.Steps[0].Inputs["patch"] = Binding{FromInput: "patch"}
			}
			a, err := c.assignment(r, Attempt{ID: "attempt_check"})
			if (err == nil) != (kind == "valid") {
				t.Fatal("source command dispatch eligibility differs", err)
			}
			if err == nil && (a.Source == nil || a.Source.Ref != r.Source.Ref || a.TaskContext != nil || a.Inputs["patch"].ArtifactID != r.StepInputs["patch"].ArtifactID || a.WorkDir == r.Repository) {
				t.Fatal("source command acquired a shared project or lost its frozen input")
			}
		})
	}
}
