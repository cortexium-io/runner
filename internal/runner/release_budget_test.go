package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ReleaseBudgetTestRun shares the real example's frozen suffix with external
// core/storage fixtures; it never loads runtimes or executes project work.
func ReleaseBudgetTestRun(t *testing.T, connected bool) Run {
	t.Helper()
	root := filepath.Join("..", "..", "examples", "jacks-break")
	read := func(path string, out any) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil || json.Unmarshal(data, out) != nil {
			t.Fatalf("release fixture unavailable: %s; %v", path, err)
		}
	}
	r := Run{Roles: map[string]Role{}, Profiles: map[string]string{}}
	read(filepath.Join(root, "retained-frontend.json"), &r.Process)
	for _, step := range r.Process.Steps {
		var role Role
		read(filepath.Join(root, "roles", step.Role+".json"), &role)
		r.Roles[step.Role] = role
	}
	if !connected {
		var steps []Step
		for _, step := range r.Process.Steps[ReleaseBudgetTestStep(r, "prepare"):] {
			p, _ := r.Roles[step.Role].Profile("")
			if p.Mode == "inspect" || p.Mode == "inspect-migration" || p.Mode == "restore" {
				continue
			}
			delete(step.Routes, "recovery_required")
			delete(step.Routes, "retry_safe")
			steps = append(steps, step)
		}
		r.Process.Steps = steps
		r.Process.MaxAttempts = 8
	}
	r.Step = ReleaseBudgetTestStep(r, "prepare")
	r.Process.Outputs = nil
	return r
}

func ReleaseBudgetTestStep(r Run, mode string) int {
	for i, step := range r.Process.Steps {
		profile, err := r.Roles[step.Role].Profile(r.Profiles[step.Role])
		if err == nil && profile.Harness == "release" && profile.Mode == mode {
			return i
		}
	}
	panic("missing release fixture mode: " + mode)
}

func TestReleaseApprovalReserveExactAndOneShort(t *testing.T) {
	for _, tc := range []struct {
		mode string
		need int
	}{{"prepare", 10}, {"backup", 8}, {"inspect", 3}, {"inspect-migration", 1}} {
		for _, short := range []bool{false, true} {
			t.Run(tc.mode+map[bool]string{false: "/exact", true: "/short"}[short], func(t *testing.T) {
				r := ReleaseBudgetTestRun(t, true)
				r.Step = ReleaseBudgetTestStep(r, tc.mode)
				used := r.Process.MaxAttempts - tc.need
				if short {
					used++
				}
				r.Attempts = make([]Attempt, used)
				c := &Core{}
				a := &Attempt{ID: "saved", End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready", Outputs: map[string]Value{"receipt": {Inline: json.RawMessage(`{}`)}}}}}
				c.route(&r, a)
				if short {
					if r.Status != "Waiting" || r.Decision != nil || !strings.HasPrefix(r.Reason, "release attempt reserve:") {
						t.Fatal("thin release approval was presented", r.Status, r.Reason)
					}
				} else if r.Decision == nil {
					t.Fatal("exact configured completion reserve was rejected", r.Reason)
				}
			})
		}
	}
}

func TestReleaseReserveUsesRemainingConfiguredRecoveryAndEffectiveTotal(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	stop := ReleaseBudgetTestStep(r, "stop")
	for _, tc := range []struct{ retry, inspect, want int }{{0, 0, 8}, {1, 0, 7}, {0, 1, 6}, {1, 1, 5}} {
		uses := map[string]int{"deploy/retry_safe": tc.retry, "inspect/ready": tc.inspect}
		got, err := releaseCompletionAttempts(r, stop, uses)
		if err != nil || got != tc.want {
			t.Fatal("consumed recovery allowances were reset or ignored", got, err)
		}
	}
	r.Attempts = make([]Attempt, 29)
	route := r.Process.Steps[r.Step].Routes["ready"]
	if err := releaseApprovalReserve(r, "ready", route); err == nil {
		t.Fatal("35 total admitted a ten-attempt suffix with six slots")
	}
	r.Process.MaxAttempts = 39 // Existing explicit Retry extra-attempt accounting.
	if err := releaseApprovalReserve(r, "ready", route); err != nil {
		t.Fatal("explicit effective total was ignored", err)
	}
	old := ReleaseBudgetTestRun(t, false)
	if got, err := releaseCompletionAttempts(old, ReleaseBudgetTestStep(old, "stop"), nil); err != nil || got != 5 {
		t.Fatal("standalone suffix acquired nonexistent recovery stages", got, err)
	}
	old.Attempts = make([]Attempt, 1)
	if err := releaseApprovalReserve(old, "ready", old.Process.Steps[old.Step].Routes["ready"]); err != nil {
		t.Fatal("original eight-attempt standalone release no longer fits", err)
	}
}

func TestReleaseReserveRejectsUnsupportedSuffixAndSkipsOrdinaryApproval(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	i := ReleaseBudgetTestStep(r, "deploy")
	route := r.Process.Steps[i].Routes["retry_safe"]
	route.MaxTimes = 0
	r.Process.Steps[i].Routes["retry_safe"] = route
	if err := releaseStopReserve(func() Run { r.Step = ReleaseBudgetTestStep(r, "stop"); return r }()); err == nil {
		t.Fatal("unsupported unbounded release loop was admitted")
	}
	r = ReleaseBudgetTestRun(t, true)
	stop := ReleaseBudgetTestStep(r, "stop")
	roleID := r.Process.Steps[stop].Role
	role := r.Roles[roleID]
	base := *role.Execution
	other := base
	other.Mode = "deploy"
	role.Execution = nil
	role.DefaultProfile = "frozen"
	role.Profiles = map[string]Profile{"frozen": base, "changed": other}
	r.Roles[roleID] = role
	r.Step, r.NextProfile = stop, "changed"
	if err := releaseStopReserve(r); err == nil {
		t.Fatal("named retry profile changed the protected release mode")
	}
	r = ReleaseBudgetTestRun(t, true)
	r.Step = 0
	r.Process.Steps[0].Reuse = true
	r.Process.Steps[0].ForEach = &TaskGroup{}
	r.Roles[r.Process.Steps[0].Role] = Role{Execution: &Profile{Harness: "deterministic"}}
	if err := releaseApprovalReserve(r, "ready", Route{Approval: true, Next: "$complete"}); err != nil {
		t.Fatal("release guard changed ordinary model/fanout approval behavior", err)
	}
}

func TestLongestConfiguredReleaseRecoveryFitsExactlyReservedEight(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.Step = ReleaseBudgetTestStep(r, "stop")
	r.Attempts = make([]Attempt, 27)
	c := &Core{}
	for _, outcome := range []string{"ready", "ready", "ready", "retry_safe", "recovery_required", "ready", "ready", "ready"} {
		r.Attempts = append(r.Attempts, Attempt{ID: "saved", Step: r.Step, Visit: r.Visit, State: Finished, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: outcome, Outputs: map[string]Value{"receipt": {Inline: json.RawMessage(`{}`)}}}}})
		c.route(&r, r.Current())
		if r.Decision != nil {
			if err := releaseApprovalReserve(r, outcome, r.Process.Steps[r.Step].Routes[outcome]); err != nil {
				t.Fatal("reserved recovery reapproval no longer fits", err)
			}
			r.Decision = nil
			c.advance(&r, r.Current().End.Result, r.Process.Steps[r.Step].Routes[outcome])
		}
	}
	if r.Status != "Completed" || len(r.Attempts) != 35 || r.RouteUses["deploy/retry_safe"] != 1 || r.RouteUses["inspect/ready"] != 1 {
		t.Fatal("configured longest path exceeded or silently enlarged total", r.Status, r.Reason, len(r.Attempts))
	}
}

type releaseBudgetReceiptLookup struct{ Store }

func (releaseBudgetReceiptLookup) FindReceipt(context.Context, string) (Receipt, error) {
	return Receipt{}, ErrNotFound
}

func TestDecideRechecksThinApprovalSavedByOlderOwner(t *testing.T) {
	r := ReleaseBudgetTestRun(t, true)
	r.ID, r.Status = "legacy", "Waiting"
	r.Attempts = make([]Attempt, 28)
	r.Attempts[27] = Attempt{ID: "saved", Step: r.Step, State: Finished, End: &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "ready"}}}
	r.Decision = &Decision{ID: "old-prompt", AttemptID: "saved", Step: r.Process.Steps[r.Step].ID, Outcome: "ready", Next: "merge"}
	c := &Core{ctx: t.Context(), store: releaseBudgetReceiptLookup{}, runs: map[string]Run{r.ID: r}}
	if _, err := c.Decide(DecisionRequest{RequestID: "approve-thin", RunID: r.ID, DecisionID: r.Decision.ID, Choice: "approve"}); err == nil || !strings.HasPrefix(err.Error(), "release attempt reserve:") {
		t.Fatal("old thin approval was accepted", err)
	}
	if c.runs[r.ID].Decision == nil || len(c.runs[r.ID].Attempts) != 28 {
		t.Fatal("guard executed release effects or rewrote saved decision evidence")
	}
}
