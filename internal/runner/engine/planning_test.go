package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

func approvedPlan(t *testing.T, e *Engine, result core.PlanningResult) core.Task {
	t.Helper()
	applyPolicy(t, e, func(c *config.Config) { c.Roles["planner"] = "build" })
	regular := e.Executor
	e.Executor = adapter(func(ctx context.Context, r execution.Request, started func(core.Process) error) execution.Completion {
		if core.PlanningRole(r.Attempt.Role) {
			if len(r.ReadRoots) != 1 || r.ReadRoots[0] == e.Settings.Config.Repository.Path || r.Dir == r.ReadRoots[0] {
				t.Error("planner did not receive a separate base copy and neutral working directory")
			}
			return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Prepared from existing coverage", Planning: &result}}
		}
		return regular.Execute(ctx, r, started)
	})
	task, err := e.AddPlan(t.Context(), "Change the greeting, then document its use. Preserve the file interface.")
	if err != nil {
		t.Fatal(err)
	}
	v, err := e.Show(t.Context(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Approve(t.Context(), task.ID, 1, v.Preview); err != nil {
		t.Fatal(err)
	}
	return current(t, e, task.ID)
}

func proposal() core.PlanningResult {
	return core.PlanningResult{Cards: []core.PlannedCard{{Key: "greeting", Body: brief}, {Key: "usage", Body: brief, Dependencies: []string{"greeting"}}}}
}

func TestPlanReleaseRecoveryAndDependencyWait(t *testing.T) {
	e := fixture(t)
	plan := approvedPlan(t, e, proposal())
	a := claim(t, e)
	if a.Role != "plan" {
		t.Fatal(a.Role)
	}
	complete(t, e, a)
	reopen(t, e)
	finish(t, e)
	v, err := e.Show(t.Context(), plan.ID)
	if err != nil || v.Task.Stage != core.AwaitingApproval || len(v.Attempts) != 1 {
		t.Fatal(v, err)
	}
	if tasks, _ := e.Store.Tasks(t.Context()); len(tasks) != 1 {
		t.Fatal("planning executed children before release")
	}
	if _, err = e.Release(t.Context(), plan.ID, 1, v.Preview); err == nil {
		t.Fatal("planning approval accepted as release approval")
	}
	if _, err = e.Release(t.Context(), plan.ID, 1, "stale"); err == nil {
		t.Fatal("stale release accepted")
	}
	next := e.Settings.Quality
	next.Definition = "New default after planning"
	if err = e.Store.ApplyQuality(t.Context(), e.Settings.Quality.Revision(), next); err != nil {
		t.Fatal(err)
	}
	plan, err = e.Release(t.Context(), plan.ID, 1, v.ReleasePreview)
	if err != nil {
		t.Fatal(err)
	}
	reopen(t, e)
	if _, err = e.Release(t.Context(), plan.ID, 1, v.ReleasePreview); err != nil {
		t.Fatal(err)
	}
	if tasks, _ := e.Store.Tasks(t.Context()); len(tasks) != 3 {
		t.Fatal("release repeated or partial")
	}
	root, dependent := plan.Release.Children[0], plan.Release.Children[1]
	if current(t, e, root).Current().Quality != plan.Current().Quality {
		t.Fatal("release changed pinned quality")
	}
	child := current(t, e, dependent)
	if !reflect.DeepEqual(child.Current().Dependencies, []core.Dependency{{TaskID: root, Version: 1}}) {
		t.Fatal("dependency not pinned", child)
	}
	for range 2 {
		a = claim(t, e)
		if a.TaskID != root {
			t.Fatal("dependent started before integration")
		}
		complete(t, e, a)
		finish(t, e)
	}
	if current(t, e, root).Stage != core.AwaitingDelivery {
		t.Fatal("root failed to progress independently")
	}
	waiting(t, e, dependent, "awaits confirmed integration")
	if current(t, e, plan.ID).Stage != core.Ready {
		t.Fatal("parent completed without integrated children")
	}
	if _, err = e.Replace(t.Context(), plan.ID, 1, "different plan"); err == nil {
		t.Fatal("released plan replaced")
	}
	if _, err = e.Replace(t.Context(), root, 1, brief); err != nil {
		t.Fatal(err)
	}
	waiting(t, e, dependent, "dependency version changed")
}

func TestPlanningCannotReleaseQuestionsMalformedOrChangedAuthority(t *testing.T) {
	for _, kind := range []string{"question", "malformed", "base moved", "superseded"} {
		t.Run(kind, func(t *testing.T) {
			e := fixture(t)
			p := proposal()
			if kind == "question" {
				p = core.PlanningResult{Questions: []string{"Which greeting is required?"}}
			}
			if kind == "malformed" {
				p.Cards[0].Dependencies = []string{"usage"}
			}
			plan := approvedPlan(t, e, p)
			f := e.execute(t.Context(), claim(t, e))
			if kind == "superseded" {
				if _, err := e.Replace(t.Context(), plan.ID, 1, "new request"); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.saveCompletion(t.Context(), f); err != nil {
				t.Fatal(err)
			}
			finish(t, e)
			if kind == "base moved" {
				git(t, e.Settings.Config.Repository.Path, "commit", "--allow-empty", "-m", "new base")
			}
			v, err := e.Show(t.Context(), plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "base moved" && (v.ReleasePreview != "" || !strings.Contains(v.NextAction, "replan")) {
				t.Fatal("stale base still offers release", v)
			}
			if _, err = e.Release(t.Context(), plan.ID, v.Task.Current().Number, v.ReleasePreview); err == nil {
				t.Fatal("invalid proposal released")
			}
			if tasks, _ := e.Store.Tasks(t.Context()); len(tasks) != 1 {
				t.Fatal("child authority leaked")
			}
			if kind == "question" && (v.Task.Attention == nil || v.Task.Attention.Kind != core.Decision) {
				t.Fatal("question lost")
			}
		})
	}
}

func TestStoppingReleasedPlanRevokesChildrenAndLateResult(t *testing.T) {
	e := fixture(t)
	plan := approvedPlan(t, e, proposal())
	complete(t, e, claim(t, e))
	finish(t, e)
	v, _ := e.Show(t.Context(), plan.ID)
	plan, err := e.Release(t.Context(), plan.ID, 1, v.ReleasePreview)
	if err != nil {
		t.Fatal(err)
	}
	a := claim(t, e)
	f := e.execute(t.Context(), a)
	if err = e.Stop(t.Context(), plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range plan.Release.Children {
		child := current(t, e, id)
		if child.Stage != core.Stopped || child.Approval != nil {
			t.Fatal("child authority survived stop")
		}
	}
	attempts, _ := e.Store.Attempts(t.Context(), a.TaskID)
	if !attempts[0].Occupying {
		t.Fatal("stop released a process claim without completion")
	}
	if err = e.saveCompletion(t.Context(), f); err != nil {
		t.Fatal(err)
	}
	finish(t, e)
	attempts, _ = e.Store.Attempts(t.Context(), a.TaskID)
	if attempts[0].State != "superseded" || attempts[0].Result == nil {
		t.Fatal("late evidence not retained")
	}
	if _, err = e.Replace(t.Context(), a.TaskID, 1, brief); err != nil {
		t.Fatal(err)
	}
	v, _ = e.Show(t.Context(), a.TaskID)
	if _, err = e.Approve(t.Context(), a.TaskID, 2, v.Preview); err != nil {
		t.Fatal(err)
	}
	waiting(t, e, a.TaskID, "parent plan is missing or revoked")
}

func TestReadyPreparationPreservesApprovalAndConsumesAllowance(t *testing.T) {
	e := fixture(t)
	body := strings.Split(brief, "## Verification")[0]
	task, err := e.Add(t.Context(), body)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := e.Show(t.Context(), task.ID)
	if _, err = e.Approve(t.Context(), task.ID, 1, v.Preview); err == nil {
		t.Fatal("unconfigured planner accepted")
	}
	if current(t, e, task.ID).Approval != nil {
		t.Fatal("failed approval wrote authority")
	}
	applyPolicy(t, e, func(c *config.Config) {
		c.Roles["planner"] = "build"
		limit := 1
		c.Policy.MaxInvocationsPerTask = &limit
	})
	if _, err = e.Approve(t.Context(), task.ID, 1, v.Preview); err != nil {
		t.Fatal(err)
	}
	settings, _, err := e.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	preview, err := e.PreviewPolicy(t.Context(), settings.Config)
	if err != nil || !strings.Contains(strings.Join(preview.Warnings, "\n"), "needs at least 3 further invocations") {
		t.Fatal("missing-brief journey budget was not explained", preview.Warnings, err)
	}
	task = current(t, e, task.ID)
	regular := e.Executor
	const proof = "Read greeting.txt at the existing file boundary; expect hello. No new suite is needed."
	e.Executor = adapter(func(ctx context.Context, r execution.Request, started func(core.Process) error) execution.Completion {
		if r.Attempt.Role == "prepare" {
			return execution.Completion{Stopped: true, Result: &core.Result{Outcome: "succeeded", Summary: "Prepared file proof", Planning: &core.PlanningResult{Verification: proof}}}
		}
		if len(r.Proofs) != 2 || r.Proofs[1].Requirement != proof {
			t.Error("prepared brief was not passed to execution/review")
		}
		return regular.Execute(ctx, r, started)
	})
	a := claim(t, e)
	if a.Role != "prepare" {
		t.Fatal("missing brief bypassed preparation")
	}
	complete(t, e, a)
	reopen(t, e)
	finish(t, e)
	after := current(t, e, task.ID)
	if !reflect.DeepEqual(task.Current(), after.Current()) || !reflect.DeepEqual(task.Approval, after.Approval) || after.Prepared == nil || after.Prepared.Attempt != a.ID {
		t.Fatal("preparation changed approved requirements or lost provenance")
	}
	waiting(t, e, task.ID, "invocation")
	applyPolicy(t, e, func(c *config.Config) { limit := 3; c.Policy.MaxInvocationsPerTask = &limit })
	for range 2 {
		complete(t, e, claim(t, e))
		finish(t, e)
	}
	after = current(t, e, task.ID)
	if after.Stage != core.AwaitingDelivery || after.Usage.Invocations != 3 {
		t.Fatal(after)
	}
}
