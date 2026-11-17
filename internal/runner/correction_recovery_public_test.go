package runner_test

import (
	"reflect"
	"testing"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func publicCorrectionFamilyRig(t *testing.T) (*rig, runner.Snapshot, runner.Run, runner.RecoverCorrectionsRequest, *correctionRecoveryExecutor, string) {
	t.Helper()
	rig, cfg, before, req, executor, dir := correctionRecoveryRig(t, func(r *runner.Run, q *runner.RecoverCorrectionsRequest) {
		currentCorrectionFixture(r, q)
		r.Revision = 288
		// The retained source-card collection is complete. Its child contributes
		// operational revision119; no new card or product proof is executed here.
		r.Process.Steps = append([]runner.Step{{ID: "cards", ForEach: &runner.TaskGroup{Mode: "source"}}}, r.Process.Steps...)
		r.Step++
		for i := range r.Attempts {
			r.Attempts[i].Step++
		}
		r.TaskGroups = map[string]*runner.TaskGroupState{"cards": {
			StepID:    "cards",
			AttemptID: "attempt_cards",
			Done:      true,
			Items:     []runner.TaskItem{{ID: "retained-card"}},
			Children:  []runner.TaskChild{{ItemID: "retained-card", RunID: "run_retained_card"}},
		}}
		q.Revision = 407
	})
	rig.close()
	store, err := sqlite.Open(dir, cfg.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	child := runner.Run{
		ID:                "run_retained_card",
		ProjectID:         before.ProjectID,
		Repository:        before.Repository,
		Status:            "Completed",
		Revision:          119,
		ResourcesReleased: true,
		Created:           before.Created,
		Task:              &runner.TaskRunLink{Identity: runner.TaskIdentity{RootRunID: before.ID, GroupStep: "cards", ItemID: "retained-card"}},
	}
	if err := store.Save(t.Context(), child, nil, nil); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	return rig, cfg, before, req, executor, dir
}

func TestCorrectionRecoveryCurrentRouteUsesPublicCardRevisionAcrossReplayAndRestart(t *testing.T) {
	rig, cfg, before, req, executor, dir := publicCorrectionFamilyRig(t)
	observed, err := rig.core.Get(before.ID)
	if err != nil || observed.Revision != 407 || before.Revision != 288 {
		rig.close()
		t.Fatal("fixture does not distinguish public and stored revisions", err)
	}
	private := runner.Clone(req)
	private.Revision = before.Revision
	if _, err := rig.core.RecoverCorrections(private); err == nil {
		rig.close()
		t.Fatal("private root revision bypassed observed family version")
	}
	recovered, err := rig.core.RecoverCorrections(req)
	if err != nil {
		rig.close()
		t.Fatal(err)
	}
	if recovered.Revision != req.Revision+1 || recovered.CorrectionRecovery == nil || recovered.CorrectionRecovery.Revision != req.Revision || recovered.RouteUses["check/changes_requested"] != 4 {
		rig.close()
		t.Fatal("grant returned an uncommitted/private revision or changed spending", recovered.Revision)
	}
	grant := runner.Clone(recovered.CorrectionRecovery)
	assertPublicReplay := func(core *runner.Core) {
		t.Helper()
		first, err := core.Get(before.ID)
		if err != nil {
			t.Fatal(err)
		}
		replay, err := core.RecoverCorrections(req)
		if err != nil {
			t.Fatal(err)
		}
		last, err := core.Get(before.ID)
		if err != nil || replay.Revision < first.Revision || replay.Revision > last.Revision || !reflect.DeepEqual(replay.CorrectionRecovery, grant) || replay.RouteUses["check/changes_requested"] != 4 {
			t.Fatal("replay exposed private revision or consumed another use", err, replay.Revision, first.Revision, last.Revision)
		}
	}
	assertPublicReplay(rig.core)
	rig.close()
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	defer rig.close()
	assertPublicReplay(rig.core)
}
