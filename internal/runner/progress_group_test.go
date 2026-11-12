package runner_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type progressTaskProbe struct {
	*taskProbe
	approve atomic.Bool
	unknown atomic.Bool
}

func (p *progressTaskProbe) Observe(ctx context.Context, id string) (runner.Observation, error) {
	o, err := p.taskProbe.Observe(ctx, id)
	if err != nil || o.State != runner.Running {
		return o, err
	}
	p.mu.Lock()
	a := p.pending[id]
	p.mu.Unlock()
	if a.TaskSource.Identity.ItemID == "approval" && p.approve.Load() {
		return p.finished(a)
	}
	if a.TaskSource.Identity.ItemID == "active" && p.unknown.Load() {
		return runner.Observation{State: runner.Unknown, Reason: "fixture execution outcome unknown"}, nil
	}
	return o, nil
}

func TestProgressActualIsolatedGroupHasNoDeadlineLessExecutor(t *testing.T) {
	items := `[{"id":"approval","inputs":{"task":"approval","paths":["approval.go"]}},{"id":"active","inputs":{"task":"active","paths":["active.go"]}}]`
	cfg := plannedSnapshot(items)
	project := cfg.Projects["demo"]
	process := project.Processes["planned"]
	process.AttemptTimeout = "20m"
	process.Steps[0].ForEach.Steps[1].Routes = map[string]runner.Route{"ready": {Next: "pin", Approval: true}}
	project.Processes["planned"] = process
	cfg.Projects["demo"] = project
	p := &progressTaskProbe{taskProbe: newTaskProbe()}
	r := openRig(t, t.TempDir(), cfg, func(runner.Executor) runner.Executor { return p })
	defer r.close()
	root := startCards(t, r.core, items)
	a, b := nextCard(t, p.taskProbe), nextCard(t, p.taskProbe)
	active := await(t, r.core, root.ID, func(v runner.Run) bool { return runner.Summarize(v, time.Now()).Progress.Active == 2 })
	healthy := runner.Summarize(active, time.Now())
	group := active.TaskGroups["cards"]
	for _, attempt := range active.Attempts {
		if attempt.ID == group.AttemptID {
			t.Fatal("Core appended a group executor while children run", attempt)
		}
	}
	if healthy.Progress.Unknown != 0 || len(healthy.Progress.Attempts) != 2 {
		t.Fatal("healthy actual group reported unknown timing", healthy)
	}
	for _, fact := range healthy.Progress.Attention {
		if fact.Kind == "timing_unknown" || fact.Kind == "deadline_elapsed" {
			t.Fatal("false healthy-group warning", fact)
		}
	}
	p.approve.Store(true)
	r.core.Wake()
	decisionView := await(t, r.core, root.ID, func(v runner.Run) bool {
		progress := runner.Summarize(v, time.Now()).Progress
		return progress.Active == 1 && progress.NextAction == "decide_saved_approval"
	})
	if runner.Summarize(decisionView, time.Now()).Progress.Unknown != 0 {
		t.Fatal("saved independent approval invented unknown execution")
	}
	p.unknown.Store(true)
	r.core.Wake()
	unknownView := await(t, r.core, root.ID, func(v runner.Run) bool { return runner.Summarize(v, time.Now()).Progress.Unknown == 1 })
	unknown := runner.Summarize(unknownView, time.Now()).Progress
	if unknown.Active != 0 || unknown.NextAction != "decide_saved_approval" || len(unknown.Attempts) != 1 || !unknown.Attempts[0].DeadlineKnown {
		t.Fatal("real unknown child or saved approval hidden", unknown)
	}
	// Resume only these synthetic owner observations, then approve their actual
	// saved decisions. No model, product task or live state participates.
	p.unknown.Store(false)
	close(p.allow)
	r.core.Wake()
	for _, assignment := range []runner.Assignment{a, b} {
		child := await(t, r.core, assignment.RunID, func(v runner.Run) bool { return v.Decision != nil })
		if _, err := r.core.Decide(runner.DecisionRequest{RequestID: "fixture-" + assignment.RunID, RunID: assignment.RunID, DecisionID: child.Decision.ID, Choice: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	done := await(t, r.core, root.ID, released)
	if done.Status != "Completed" {
		t.Fatal("synthetic group failed", done.Status, done.Reason)
	}
	found := false
	for _, attempt := range done.Attempts {
		if attempt.ID == done.TaskGroups["cards"].AttemptID {
			found = true
			if attempt.State != runner.Finished || !attempt.Deadline.IsZero() {
				t.Fatal("group completion was not structural", attempt)
			}
		}
	}
	if !found {
		t.Fatal("finished group attempt was not retained")
	}
	for _, fact := range runner.Summarize(done, time.Now()).Progress.Attention {
		if fact.Kind == "timing_unknown" {
			t.Fatal("finished structural record warned", fact)
		}
	}
}
