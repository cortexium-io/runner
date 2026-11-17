package runner_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

type durableHandoffExecutor struct{ *correctionRecoveryExecutor }

func (*durableHandoffExecutor) ValidateEvidenceHandoff(context.Context, runner.Run, runner.RecoverEvidenceHandoffRequest) error {
	return nil
}

type handoffCommitStore struct {
	runner.Store
	mode       string
	failed     atomic.Bool
	committed  atomic.Bool
	readFailed atomic.Bool
}

func (s *handoffCommitStore) Save(ctx context.Context, r runner.Run, a []runner.Artifact, receipt *runner.Receipt) error {
	if receipt != nil && receipt.Kind == "recover_evidence_handoff" && s.failed.CompareAndSwap(false, true) {
		if s.mode == "refused" {
			return errors.New("fixture refused commit")
		}
		if err := s.Store.Save(ctx, r, a, receipt); err != nil {
			return err
		}
		s.committed.Store(true)
		return errors.New("fixture lost acknowledgement")
	}
	return s.Store.Save(ctx, r, a, receipt)
}
func (s *handoffCommitStore) FindReceipt(ctx context.Context, id string) (runner.Receipt, error) {
	if s.mode == "lost-read" && s.committed.Load() && s.readFailed.CompareAndSwap(false, true) {
		return runner.Receipt{}, errors.New("fixture receipt read unavailable")
	}
	return s.Store.FindReceipt(ctx, id)
}

func TestEvidenceHandoffSQLiteRestartCommitUncertaintyAndOldReplay(t *testing.T) {
	for _, mode := range []string{"refused", "lost-ack", "lost-read"} {
		t.Run(mode, func(t *testing.T) {
			digest := func(data string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(data))) }
			var req runner.RecoverEvidenceHandoffRequest
			var phase runner.RecoverCorrectionsRequest
			setup := func(r *runner.Run) {
				r.Revision = 676
				r.Step = 0
				r.Visit = 30
				r.Reason = "Supply complete retained evidence."
				r.Process.Steps[0].Routes = map[string]runner.Route{"blocked": {Next: "$fail"}}
				r.Process.Steps[1].Routes["changes_requested"] = runner.Route{Next: "owner", MaxTimes: 3}
				ownerRole := r.Roles["owner"]
				ownerRole.Outputs["patch"] = "artifact"
				r.Roles["owner"] = ownerRole
				accepted := runner.Clone(r.Attempts[1])
				accepted.Visit = 26
				accepted.Superseded = true
				accepted.End.Result.Outputs["patch"] = runner.Value{ArtifactID: accepted.Source.PatchID}
				check := runner.Clone(r.Attempts[2])
				check.Visit = 29
				check.Superseded = true
				producer := runner.Clone(check)
				producer.ID = "attempt_project"
				producer.Visit = 28
				producer.Source.AttemptID = producer.ID
				producer.End.Result.Outputs["artifact"] = runner.Value{ArtifactID: producer.ID + "/result"}
				blocked := runner.Clone(accepted)
				blocked.ID = "attempt_blocked"
				blocked.Visit = 30
				blocked.Superseded = false
				blocked.Source.AttemptID = blocked.ID
				blocked.Source.InputPatchID = accepted.Source.PatchID
				blocked.Source.PatchID = blocked.ID + "/changes.patch"
				blocked.End.Result.Outcome = "blocked"
				blocked.End.Result.Reason = r.Reason
				blocked.End.Result.Outputs["patch"] = runner.Value{ArtifactID: blocked.Source.PatchID}
				r.Attempts = []runner.Attempt{accepted, producer, check, blocked}
				r.StepInputs = map[string]runner.Value{"checks": {ArtifactID: check.ID + "/result"}, "check": check.End.Result.Outputs["check"]}
				r.RouteUses["check/changes_requested"] = 7
				r.RouteUses["owner/blocked"] = 1
				r.CorrectionRecovery = &runner.CorrectionRecovery{RequestID: "approved-phase8", AttemptID: "earlier-check", CurrentRoute: true, PreviousUses: 5, AdditionalUses: 3, MaxUses: 8, Route: "check/changes_requested", RouteLimits: map[string]int{"check/changes_requested": 8}, FromVisit: 26, Previous: &runner.CorrectionRecovery{RequestID: "one-use5", MaxUses: 5, PreviousUses: 4}}
				req = runner.RecoverEvidenceHandoffRequest{RequestID: "approved-handoff8", RunID: r.ID, Revision: 676, PhaseRequestID: "approved-phase8", AttemptID: blocked.ID, SourceAttemptID: accepted.ID, PatchSHA256: digest("retained fixture source"), CheckAttemptID: check.ID, CheckSHA256: digest(`{"executed":259,"failed":1}`), Evidence: runner.CorrectionContext{ID: producer.ID + "/result", Bytes: int64(len("full archive")), SHA256: digest("full archive"), Roles: []string{"owner", "review"}}}
				three := 3
				phase = runner.RecoverCorrectionsRequest{RequestID: "approved-phase8", RunID: r.ID, AttemptID: "earlier-check", CurrentRoute: true, AdditionalUses: &three, MaxUses: 8}
			}
			rig, cfg, before, _, original, _ := correctionRecoveryRig(t, nil)
			rig.close()
			setup(&before)
			dir := t.TempDir()
			rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return original })
			rig.core.Close()
			var artifacts []runner.Artifact
			for id, body := range map[string]string{
				"attempt_owner/changes.patch":   "retained fixture source",
				"attempt_check/result":          `{"executed":259,"failed":1}`,
				"attempt_blocked/changes.patch": "retained fixture source",
				"attempt_project/result":        "full archive",
			} {
				if err := rig.disk.Write("artifacts/"+id, []byte(body), rig.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
				artifacts = append(artifacts, runner.Artifact{ID: id, RunID: before.ID, ProjectID: before.ProjectID, Bytes: int64(len(body))})
			}
			phaseArgs, _ := json.Marshal(phase)
			if err := rig.store.Save(t.Context(), before, artifacts, &runner.Receipt{Key: phase.RequestID, Kind: "recover_corrections", Args: phaseArgs, RunID: before.ID}); err != nil {
				t.Fatal(err)
			}
			store := &handoffCommitStore{Store: rig.store, mode: mode}
			executor := &durableHandoffExecutor{original}
			core, err := runner.New(t.Context(), store, executor, rig.disk, cfg)
			if err != nil {
				t.Fatal(err)
			}
			rig.core = core
			recovered, err := core.RecoverEvidenceHandoff(req)
			if mode == "lost-ack" && err != nil {
				t.Fatal(err)
			}
			if mode != "lost-ack" {
				if err == nil {
					t.Fatal("fixture commit failure not observed")
				}
				if mode == "refused" {
					got, _ := core.Get(before.ID)
					if got.Status != "Failed" || got.CorrectionRecovery.EvidenceHandoff != nil || got.RouteUses["check/changes_requested"] != 7 {
						t.Fatal("refused commit admitted work")
					}
				} else {
					// A different request must reconcile durable spending before it can mutate
					// deliberately stale in-memory state after a lost receipt read.
					other := req
					other.RequestID = "unapproved-second"
					if _, err := core.RecoverEvidenceHandoff(other); err == nil {
						t.Fatal("lost acknowledgement allowed a second handoff")
					}
				}
				recovered, err = core.RecoverEvidenceHandoff(req)
			}
			if err != nil || recovered.RouteUses["check/changes_requested"] != 8 || recovered.CorrectionRecovery.EvidenceHandoff == nil || !reflect.DeepEqual(recovered.CorrectionRecovery.Previous, before.CorrectionRecovery.Previous) {
				t.Fatal("same request did not reconcile exact phase use", err)
			}
			select {
			case assignment := <-original.started:
				if assignment.RecoveryEvidence == nil || !reflect.DeepEqual(*assignment.RecoveryEvidence, req.Evidence) || !reflect.DeepEqual(assignment.Inputs, before.StepInputs) {
					t.Fatal("ordinary dispatched owner lacks exact archive or original inputs")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("ordinary owner not dispatched")
			}
			core.Close()
			// Reopen the actual SQLite state after clearing every in-memory Core field.
			if err := rig.store.Close(); err != nil {
				t.Fatal(err)
			}
			rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
			defer rig.close()
			for range 2 {
				replay, err := rig.core.RecoverEvidenceHandoff(req)
				if err != nil || replay.RouteUses["check/changes_requested"] != 8 || replay.CorrectionRecovery.EvidenceHandoff == nil {
					t.Fatal("restart lost audit or spent again", err)
				}
				old, err := rig.core.RecoverCorrections(phase)
				if err != nil || old.CorrectionRecovery.EvidenceHandoff == nil || old.RouteUses["check/changes_requested"] != 8 {
					t.Fatal("old grant replay discarded handoff", err)
				}
			}
			if len(original.started) != 0 {
				t.Fatal("receipt replay launched another owner")
			}
		})
	}
}
