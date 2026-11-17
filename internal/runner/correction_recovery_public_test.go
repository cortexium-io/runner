package runner_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

func publicCorrectionFamilyRig(t *testing.T) (*rig, runner.Snapshot, runner.Run, runner.RecoverCorrectionsRequest, *correctionRecoveryExecutor, string) {
	t.Helper()
	rig, cfg, before, req, executor, dir := correctionRecoveryRig(t, func(r *runner.Run, q *runner.RecoverCorrectionsRequest) {
		currentCorrectionFixture(r, q)
		sharedCorrectionObservation(r)
		r.Revision = 288
		// The retained source-card collection is complete. Its child contributes
		// operational revision119; no new card or product proof is executed here.
		r.Process.Steps = append([]runner.Step{{ID: "cards", ForEach: &runner.TaskGroup{Mode: "source"}}}, r.Process.Steps...)
		r.Step++
		for i := range r.Attempts {
			r.Attempts[i].Step++
		}
		r.TaskGroups = map[string]*runner.TaskGroupState{"cards": {
			StepID:     "cards",
			AttemptID:  "attempt_cards",
			Repository: r.Repository,
			Base:       r.Source.Ref,
			Done:       true,
			Items:      []runner.TaskItem{{ID: "retained-card"}},
			Children:   []runner.TaskChild{{ItemID: "retained-card", RunID: "run_retained_card"}},
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
		Source:            runner.Clone(before.Source),
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
	for i, old := range before.Attempts {
		if !reflect.DeepEqual(recovered.Attempts[i].Source, old.Source) || !reflect.DeepEqual(recovered.Attempts[i].End, old.End) {
			t.Fatal("recovery rewrote retained provenance or evidence")
		}
	}
	if before.Current().Source.InputPatchID != "" {
		t.Fatal("fixture must retain the shared command's missing duplicate stamp")
	}
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
	select {
	case <-executor.started:
	case <-time.After(4 * time.Second):
		state, _ := rig.core.Get(before.ID)
		t.Fatalf("recovered shared observation did not dispatch its owner: status=%s reason=%s step=%d visit=%d current=%+v", state.Status, state.Reason, state.Step, state.Visit, state.Current())
	}
	rig.close()
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	defer rig.close()
	assertPublicReplay(rig.core)
	if len(executor.started) != 0 {
		t.Fatal("same grant dispatched multiple owners")
	}
}

func sharedCorrectionObservation(r *runner.Run) {
	r.Roles["check"].Execution.Mode = "shared"
	r.Current().Source.InputPatchID = ""
}

func TestCorrectionRecoverySharedObservationRejectsUnboundOrConflictingSource(t *testing.T) {
	for _, mode := range []string{"explicit-patch", "missing-inputs", "wrong-input", "missing-candidate-binding", "other-harness", "other-mode", "other-access", "other-attempt", "repository", "base", "work-dir"} {
		t.Run(mode, func(t *testing.T) {
			rig, _, before, req, executor, _ := correctionRecoveryRig(t, func(r *runner.Run, q *runner.RecoverCorrectionsRequest) {
				currentCorrectionFixture(r, q)
				sharedCorrectionObservation(r)
				switch mode {
				case "explicit-patch":
					r.Current().Source.InputPatchID = "another/changes.patch"
				case "missing-inputs":
					r.StepInputs = nil
				case "wrong-input":
					r.StepInputs["patch"] = runner.Value{ArtifactID: "another/changes.patch"}
				case "missing-candidate-binding":
					delete(r.Process.Steps[r.Step].Inputs, "patch")
				case "other-harness":
					r.Roles["check"].Execution.Harness = "codex"
				case "other-mode":
					r.Roles["check"].Execution.Mode = ""
				case "other-access":
					r.Roles["check"].Execution.Access = "input-only"
				case "other-attempt":
					r.Current().Source.AttemptID = "another-attempt"
				case "repository":
					r.Current().Source.Repository += "-other"
				case "base":
					r.Current().Source.Base = "another-base"
				case "work-dir":
					r.Current().Source.WorkDir += "-other"
				}
			})
			defer rig.close()
			if _, err := rig.core.RecoverCorrections(req); err == nil {
				t.Fatal("unbound or conflicting source was accepted")
			}
			got, err := rig.core.Get(before.ID)
			if err != nil || got.CorrectionRecovery != nil || got.Status != "Failed" || got.RouteUses["check/changes_requested"] != 3 || !reflect.DeepEqual(got.Attempts, before.Attempts) || len(executor.started) != 0 {
				t.Fatal("rejected recovery changed history or dispatched work", err)
			}
		})
	}
}
