package runner_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func TestTaskFollowupExecutesNewGoalAndReopensWithAcceptedHistory(t *testing.T) {
	s := graphSnapshot(t)
	s.Instance.EnforceWorkLimits = false
	dir := t.TempDir()
	var mu sync.Mutex
	starts := map[string]int{}
	var badContext bool
	wrap := func(e runner.Executor) runner.Executor {
		return taskResourcesExecutor{graphExecutor{e, func(a runner.Assignment) runner.Observation {
			mu.Lock()
			defer mu.Unlock()
			starts[a.RunID]++
			ctx := a.TaskContext
			if ctx.Goal == "New owner feedback" && (ctx.Followup == nil || ctx.Followup.RequestID != "new-owner-goal" || len(ctx.Results) != 0 || ctx.Lineage != nil) {
				badContext = true
			}
			return graphFinal(ctx.Goal)
		}}}
	}
	r := openRig(t, dir, s, wrap)
	parent := begin(t, r.core, "accepted-parent", "demo")
	parent = await(t, r.core, parent.ID, func(r runner.Run) bool { return released(r) && r.ResourcesReleased })
	request := runner.StartRequest{
		RequestID: "new-owner-goal", Project: "demo", Process: "demo",
		Followup: &runner.TaskFollowupRequest{RunID: parent.ID, Revision: parent.Revision},
		Inputs:   map[string]runner.Value{"text": runner.Text("New owner feedback")},
	}
	started, err := r.core.Start(request)
	if err != nil {
		r.close()
		t.Fatal(err)
	}
	done := await(t, r.core, started.ID, func(r runner.Run) bool { return released(r) && r.ResourcesReleased })
	afterParent, _ := r.core.Get(parent.ID)
	if done.Status != "Completed" || done.ID == parent.ID || done.Followup == nil || done.Followup.RunID != parent.ID || string(done.Outputs["result"].Inline) != `"New owner feedback"` || !reflect.DeepEqual(parent, afterParent) {
		r.close()
		t.Fatal("follow-up reused or mutated the accepted goal", done.Status, done.Followup)
	}
	r.close()
	r = openRig(t, dir, s, wrap)
	defer r.close()
	restored, err := r.core.Get(done.ID)
	if err != nil || !reflect.DeepEqual(restored, done) {
		t.Fatal("restart changed the linked result", err)
	}
	replay, err := r.core.Start(request)
	if err != nil || replay.ID != done.ID {
		t.Fatal("start replay after restart admitted another follow-up", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if badContext || len(starts) != 2 || starts[parent.ID] != 1 || starts[done.ID] != 1 {
		t.Fatal("accepted task was replayed or follow-up context carried old work", starts, badContext)
	}
}

type lostFollowupAckStore struct {
	*sqlite.Store
	commits     atomic.Int32
	hideReceipt bool
}

func (s *lostFollowupAckStore) SaveTaskFollowup(ctx context.Context, target runner.Run, family []runner.Run, receipt *runner.Receipt) error {
	if err := s.Store.SaveTaskFollowup(ctx, target, family, receipt); err != nil {
		return err
	}
	s.commits.Add(1)
	return errors.New("injected lost commit acknowledgement")
}

func (s *lostFollowupAckStore) FindReceipt(ctx context.Context, key string) (runner.Receipt, error) {
	if s.commits.Load() != 0 && s.hideReceipt {
		s.hideReceipt = false
		return runner.Receipt{}, runner.ErrNotFound
	}
	return s.Store.FindReceipt(ctx, key)
}

func TestTaskFollowupLostCommitAcknowledgementRejoinsSameRoot(t *testing.T) {
	for _, deferReceipt := range []bool{false, true} {
		t.Run(map[bool]string{false: "immediate", true: "next-replay"}[deferReceipt], func(t *testing.T) {
			s := graphSnapshot(t)
			s.Instance.EnforceWorkLimits = false
			dir := t.TempDir()
			var starts atomic.Int32
			var executor runner.Executor
			r := openRig(t, dir, s, func(e runner.Executor) runner.Executor {
				executor = taskResourcesExecutor{graphExecutor{e, func(a runner.Assignment) runner.Observation {
					starts.Add(1)
					return graphFinal(a.TaskContext.Goal)
				}}}
				return executor
			})
			parent := begin(t, r.core, "accepted-lost-ack-parent", "demo")
			parent = await(t, r.core, parent.ID, func(r runner.Run) bool { return released(r) && r.ResourcesReleased })
			r.core.Close()
			store := &lostFollowupAckStore{Store: r.store, hideReceipt: deferReceipt}
			core, err := runner.New(context.Background(), store, executor, r.disk, s)
			if err != nil {
				r.store.Close()
				t.Fatal(err)
			}
			r.core = core
			request := runner.StartRequest{
				RequestID: "lost-ack-followup", Project: "demo", Process: "demo",
				Followup: &runner.TaskFollowupRequest{RunID: parent.ID, Revision: parent.Revision},
				Inputs:   map[string]runner.Value{"text": runner.Text("Continue accepted work once")},
			}
			started, err := r.core.Start(request)
			if deferReceipt {
				if err == nil {
					r.close()
					t.Fatal("fixture did not hide the uncertain commit receipt")
				}
				started, err = r.core.Start(request)
			}
			if err != nil || started.ID == "" {
				r.close()
				t.Fatal("committed follow-up did not reconcile to its saved root", err)
			}
			done := await(t, r.core, started.ID, func(r runner.Run) bool { return released(r) && r.ResourcesReleased })
			receipt, err := r.store.FindReceipt(t.Context(), request.RequestID)
			if err != nil || receipt.RunID != done.ID || store.commits.Load() != 1 || starts.Load() != 2 {
				r.close()
				t.Fatal("commit acknowledgement loss duplicated work or changed identity", err, starts.Load(), store.commits.Load())
			}
			r.close()
			r = openRig(t, dir, s, func(runner.Executor) runner.Executor { return executor })
			defer r.close()
			replayed, err := r.core.Start(request)
			if err != nil || replayed.ID != done.ID || starts.Load() != 2 {
				t.Fatal("restart repeated the reconciled follow-up", err)
			}
		})
	}
}
