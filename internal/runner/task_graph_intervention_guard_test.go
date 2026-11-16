package runner

import (
	"context"
	"reflect"
	"testing"
)

type interventionGuardStore struct {
	Store
	saves int
}

func (*interventionGuardStore) FindReceipt(context.Context, string) (Receipt, error) {
	return Receipt{}, ErrNotFound
}

func (s *interventionGuardStore) Save(context.Context, Run, []Artifact, *Receipt) error {
	s.saves++
	return nil
}

func TestFailedTaskSteerGuardsHaveNoEffects(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Run, *TaskInterventionRequest)
	}{
		{"completed", func(r *Run, _ *TaskInterventionRequest) { r.Status = "Completed" }},
		{"cancelled", func(r *Run, _ *TaskInterventionRequest) { r.Status = "Cancelled" }},
		{"empty directive", func(_ *Run, q *TaskInterventionRequest) { q.Text = "  " }},
		{"exhausted goal", func(r *Run, _ *TaskInterventionRequest) { r.Process.MaxAttempts = 1 }},
		{"cleanup error", func(r *Run, _ *TaskInterventionRequest) { r.CleanupError = "unsettled resources" }},
		{"cleanup blocked", func(r *Run, _ *TaskInterventionRequest) { r.CleanupBlocked = true }},
		{"invalid step", func(r *Run, _ *TaskInterventionRequest) { r.Step = 1 }},
		{"active work", func(r *Run, _ *TaskInterventionRequest) { r.Attempts[0].State, r.Attempts[0].Released = Running, false }},
		{"profile change", func(_ *Run, q *TaskInterventionRequest) { q.Profile = "another" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Run{ID: "root", RecordFormat: 9, Status: "Failed", Revision: 7, Process: Process{MaxAttempts: 6, Steps: []Step{{Role: "planner"}}, Tasks: &TaskPolicy{}}, TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root"}, Attempts: []Attempt{{ID: "old", State: Finished, Released: true}}}
			q := TaskInterventionRequest{RequestID: "owner-steer", RunID: r.ID, Revision: r.Revision, Action: "steer", Text: "Corrected runtime; reassess the same goal"}
			tc.edit(&r, &q)
			store := &interventionGuardStore{}
			c := &Core{snapshot: Snapshot{Instance: Instance{EnforceWorkLimits: true}}, ctx: t.Context(), store: store, runs: map[string]Run{r.ID: Clone(r)}}
			if _, err := c.InterveneTask(q); err == nil || store.saves != 0 || !reflect.DeepEqual(c.runs[r.ID], r) {
				t.Fatalf("rejected steering changed retained state or wrote a receipt: %v saves=%d", err, store.saves)
			}
		})
	}
}

func TestSharedCommandDirectoryRejectedAtTaskAdmission(t *testing.T) {
	p := Profile{Harness: "command", Mode: "shared", Access: "workspace-read", Command: &Command{Args: []string{"/usr/bin/true"}, Directory: "frontend"}}
	if err := ValidateRoleTaskProfile(p); err == nil {
		t.Fatal("shared task admission silently accepted an ignored working-directory field")
	}
}

func TestGeneralTaskContextRejectsMalformedFamilyOwnership(t *testing.T) {
	r := Run{
		ID: "root", ProjectID: "demo", Process: Process{MaxAttempts: 1, Tasks: &TaskPolicy{}},
		TaskGraph: &TaskGraphState{RootRunID: "root", TaskID: "root", Children: []RoleTaskLink{{RunID: "child"}}},
	}
	c := &Core{runs: map[string]Run{r.ID: r, "child": {ID: "child", ProjectID: "foreign"}}}
	if _, err := c.graphContext(r); err == nil {
		t.Fatal("malformed ownership produced an actionable native context")
	}
}
