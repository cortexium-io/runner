package runner

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type evidenceHandoffExecutor struct {
	budgetCorrectionExecutor
	mu            sync.Mutex
	validate      func(Run, RecoverEvidenceHandoffRequest) error
	source        *SourceResult
	sourceErr     error
	evidenceCalls int
}

func (e *evidenceHandoffExecutor) ValidateSourceRecovery(_ context.Context, _ Run, source SourceResult) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.source = Clone(&source)
	return e.sourceErr
}
func (e *evidenceHandoffExecutor) ValidateEvidenceHandoff(_ context.Context, r Run, req RecoverEvidenceHandoffRequest) error {
	e.mu.Lock()
	e.evidenceCalls++
	e.mu.Unlock()
	if e.validate != nil {
		return e.validate(r, req)
	}
	return nil
}

func evidenceHandoffFixture(t *testing.T) (*Core, *frontendRecheckStore, *evidenceHandoffExecutor, Run, RecoverEvidenceHandoffRequest, []RecoverCorrectionsRequest) {
	t.Helper()
	c, store, _, earlier, phase := correctionPhaseFixture(t)
	if _, err := c.RecoverCorrections(phase); err != nil {
		t.Fatal(err)
	}
	r := Clone(c.runs[phase.RunID])
	finishBudgetCorrectionOwner(t, c, store, &r, 6)
	accepted := Clone(r.Attempts[len(r.Attempts)-1])
	finishBudgetCorrectionCheck(t, c, store, &r, 6, false)
	check := &r.Attempts[len(r.Attempts)-1]
	check.Visit++
	r.Visit++ // One ordinary source-owned evidence step between owner and check.
	producer := Clone(*check)
	producer.ID, producer.Visit = "attempt_project_proof", check.Visit-1
	producer.Source.AttemptID = producer.ID
	producer.End.Result.Outputs["artifact"] = budgetCorrectionArtifact(t, c, store, r, producer.ID+"/result", []byte("full retained archive"))
	r.Attempts = append(r.Attempts[:len(r.Attempts)-1], producer, *check)
	ownerStep := &r.Process.Steps[r.Step]
	ownerStep.Routes["blocked"] = Route{Next: "$fail"}
	ownerStep.Inputs["checks"] = Binding{FromStep: "check", Output: "artifact", Previous: true}
	r.StepInputs["checks"] = r.Attempts[len(r.Attempts)-1].End.Result.Outputs["artifact"]
	source := Clone(accepted.Source)
	blocked := Attempt{ID: "attempt_blocked_owner", Step: r.Step, Visit: r.Visit, State: Finished, Released: true}
	source.AttemptID, source.InputPatchID, source.PatchID = blocked.ID, accepted.Source.PatchID, blocked.ID+"/changes.patch"
	patch, err := c.disk.Read("artifacts/"+accepted.Source.PatchID, c.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	blocked.Source = source
	blocked.End = &ExecutionEnd{Kind: "returned", Result: &StepResult{Outcome: "blocked", Reason: "Supply the complete retained evidence archive.", Outputs: map[string]Value{
		"summary": Text("No source changes or checks performed"),
		"patch":   budgetCorrectionArtifact(t, c, store, r, source.PatchID, patch),
	}}}
	r.Attempts = append(r.Attempts, blocked)
	for _, artifact := range c.finish(&r, len(r.Attempts)-1) {
		store.artifacts[artifact.ID] = artifact
	}
	r.ResourcesReleased = true
	if err := c.persist(r, nil, nil); err != nil {
		t.Fatal(err)
	}
	r = Clone(c.runs[r.ID])
	if proof := r.CorrectionRecovery.Context; proof != nil {
		store.artifacts[proof.ID] = Artifact{ID: proof.ID, RunID: r.ID, ProjectID: r.ProjectID, Bytes: proof.Bytes}
	}
	check = &r.Attempts[len(r.Attempts)-2]
	checkData, err := c.disk.Read("artifacts/"+check.End.Result.Outputs["artifact"].ArtifactID, c.disk.Policy.MaxArtifact)
	if err != nil {
		t.Fatal(err)
	}
	req := RecoverEvidenceHandoffRequest{
		RequestID: "approved-handoff", RunID: r.ID, Revision: c.outputRun(r).Revision, PhaseRequestID: phase.RequestID,
		AttemptID: blocked.ID, SourceAttemptID: accepted.ID, PatchSHA256: correctionDigest(patch), CheckAttemptID: check.ID, CheckSHA256: correctionDigest(checkData),
		Evidence: CorrectionContext{ID: producer.ID + "/result", Bytes: int64(len("full retained archive")), SHA256: correctionDigest([]byte("full retained archive")), Roles: []string{ownerStep.Role}},
	}
	exec := &evidenceHandoffExecutor{}
	c.executor = exec
	return c, store, exec, r, req, append(earlier, phase)
}

func TestEvidenceHandoffSpendsLastExistingPhaseUseAndRetainsFailures(t *testing.T) {
	for _, pass := range []bool{false, true} {
		t.Run(map[bool]string{false: "stops-at-eight", true: "fresh-check-protected-tail"}[pass], func(t *testing.T) {
			c, store, exec, before, req, earlier := evidenceHandoffFixture(t)
			_, definition, _ := MarshalRunRecord(before)
			r, err := c.RecoverEvidenceHandoff(req)
			if err != nil {
				t.Fatal(err)
			}
			r = Clone(c.runs[r.ID])
			if r.Status != "Queued" || r.Step != before.Step || r.Visit != before.Visit+1 || r.RouteUses["check/changes_requested"] != 8 || len(r.Attempts) != len(before.Attempts) || r.VisitExtraAttempts != 0 || r.ResourcesReleased || !reflect.DeepEqual(r.StepInputs, before.StepInputs) || !reflect.DeepEqual(exec.source, before.Current().Source) {
				t.Fatal("handoff did not spend exactly the existing phase use with unchanged source/input/history", r.Status, r.Reason)
			}
			originalGrant := Clone(r.CorrectionRecovery)
			originalGrant.EvidenceHandoff = nil
			if !reflect.DeepEqual(originalGrant, before.CorrectionRecovery) || r.CorrectionRecovery.EvidenceHandoff.Request.RequestID != req.RequestID {
				t.Fatal("handoff rewrote a grant or failed to audit")
			}
			_, afterDefinition, _ := MarshalRunRecord(r)
			if !bytes.Equal(definition, afterDefinition) || !reflect.DeepEqual(before.TaskGroups, r.TaskGroups) {
				t.Fatal("handoff changed frozen definitions or child work")
			}
			for i, old := range before.Attempts {
				current := Clone(r.Attempts[i])
				current.Superseded = old.Superseded
				if !reflect.DeepEqual(old, current) {
					t.Fatal("handoff rewrote prior history", old.ID)
				}
			}
			assignment, err := c.assignment(r, Attempt{ID: "next-owner", Step: r.Step, Visit: r.Visit, Deadline: time.Now().Add(time.Minute)})
			if err != nil || !reflect.DeepEqual(assignment.RecoveryEvidence, &req.Evidence) || !reflect.DeepEqual(assignment.RecoveryContext, before.CorrectionRecovery.Context) {
				t.Fatal("ordinary owner lacks scoped retained evidence or prior policy context", err)
			}
			hidden := Clone(r)
			hidden.CorrectionRecovery.EvidenceHandoff.Request.Evidence.Roles = []string{"another-role"}
			if proof, err := c.evidenceHandoffAssignment(hidden, Attempt{Step: r.Step, Visit: r.Visit}); err != nil || proof != nil {
				t.Fatal("unselected role received evidence")
			}
			c.runs[r.ID] = Clone(before) // Lost acknowledgement/stale memory.
			saves := store.saves
			replay, err := c.RecoverEvidenceHandoff(req)
			if err != nil || replay.RouteUses["check/changes_requested"] != 8 || store.saves != saves || exec.evidenceCalls != 1 {
				t.Fatal("replay repeated validation/grant", err)
			}
			for _, old := range earlier {
				c.runs[r.ID] = Clone(before)
				if replay, err := c.RecoverCorrections(old); err != nil || replay.RouteUses["check/changes_requested"] != 8 || replay.CorrectionRecovery.EvidenceHandoff == nil {
					t.Fatal("old receipt discarded evidence handoff", err)
				}
			}
			changed := req
			changed.RequestID = "unapproved-second-handoff"
			if _, err := c.RecoverEvidenceHandoff(changed); err == nil {
				t.Fatal("new request spent a second route use")
			}
			r = Clone(c.runs[r.ID])
			finishBudgetCorrectionOwner(t, c, store, &r, 8)
			finishBudgetCorrectionCheck(t, c, store, &r, 8, pass)
			if pass {
				if r.Status != "Queued" || r.Process.Steps[r.Step].ID != "measure" || r.Decision != nil {
					t.Fatal("fresh passing check bypassed protected tail")
				}
			} else if r.Status != "Failed" || r.RouteUses["check/changes_requested"] != 8 || r.Reason != "repetition allowance exhausted: check/changes_requested" {
				t.Fatal("failed use8 admitted a ninth correction")
			}
		})
	}
}

func TestEvidenceHandoffRejectsChangedAuthorityAndEvidence(t *testing.T) {
	cases := []string{"revision", "phase", "blocked", "source", "check", "check-sha", "patch-sha", "evidence-sha", "size", "foreign", "missing", "cancel", "active", "cleanup", "unreleased", "blocked-tree", "blocked-seed", "blocked-bytes", "outcome", "ceiling", "grant", "recipient", "producer", "check-source", "source-validation", "evidence-validation", "changed-during-validation", "changed-artifact-during-validation"}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			c, store, exec, before, req, _ := evidenceHandoffFixture(t)
			r := Clone(before)
			switch mode {
			case "revision":
				req.Revision--
			case "phase":
				req.PhaseRequestID = "other-phase"
			case "blocked":
				req.AttemptID = "other-blocked"
			case "source":
				req.SourceAttemptID = "other-owner"
			case "check":
				req.CheckAttemptID = "other-check"
			case "check-sha":
				req.CheckSHA256 = correctionDigest([]byte("other"))
			case "patch-sha":
				req.PatchSHA256 = correctionDigest([]byte("other"))
			case "evidence-sha":
				req.Evidence.SHA256 = correctionDigest([]byte("other"))
			case "size":
				req.Evidence.Bytes++
			case "foreign":
				a := store.artifacts[req.Evidence.ID]
				a.RunID = "foreign"
				store.artifacts[a.ID] = a
			case "missing":
				delete(store.artifacts, req.Evidence.ID)
			case "cancel":
				r.CancelRequested = true
			case "active":
				r.Current().State = Running
			case "cleanup":
				r.CleanupBlocked = true
			case "unreleased":
				r.Current().Released = false
			case "blocked-tree":
				r.Current().Source.Tree = "changed-tree"
			case "blocked-seed":
				r.Current().Source.InputPatchID = "other-seed"
			case "blocked-bytes":
				if err := c.disk.Write("artifacts/"+r.Current().Source.PatchID, []byte("changed"), c.disk.Policy.MaxArtifact); err != nil {
					t.Fatal(err)
				}
			case "outcome":
				r.Current().End.Result.Outcome = "ready"
			case "ceiling":
				r.RouteUses["check/changes_requested"] = 8
			case "grant":
				r.CorrectionRecovery.RouteLimits["review/changes_requested"] = 8
			case "recipient":
				req.Evidence.Roles = []string{"unknown"}
			case "producer":
				r.Attempts[len(r.Attempts)-3].Source.InputPatchID = "foreign-patch"
			case "check-source":
				r.Attempts[len(r.Attempts)-2].Source.InputPatchID = "foreign-patch"
			case "source-validation":
				exec.sourceErr = errors.New("owned source capture differs")
			case "evidence-validation":
				exec.validate = func(Run, RecoverEvidenceHandoffRequest) error { return errors.New("retained proof differs") }
			case "changed-during-validation":
				exec.validate = func(Run, RecoverEvidenceHandoffRequest) error {
					c.mu.Lock()
					defer c.mu.Unlock()
					changed := Clone(c.runs[r.ID])
					changed.CancelRequested = true
					changed.Revision++
					c.runs[r.ID] = changed
					return nil
				}
			case "changed-artifact-during-validation":
				exec.validate = func(Run, RecoverEvidenceHandoffRequest) error {
					return c.disk.Write("artifacts/"+req.Evidence.ID, []byte("other retained archive"), c.disk.Policy.MaxArtifact)
				}
			}
			c.runs[r.ID] = r
			saves := store.saves
			if _, err := c.RecoverEvidenceHandoff(req); err == nil || store.saves != saves || c.runs[r.ID].RouteUses["check/changes_requested"] != r.RouteUses["check/changes_requested"] {
				t.Fatal("invalid handoff mutated saved work", err)
			}
		})
	}
}

func TestEvidenceHandoffAcceptsOnlyFrozenSharedCommandMissingInputStamps(t *testing.T) {
	for _, mode := range []string{"shared", "other-mode", "writable", "no-candidate-binding"} {
		t.Run(mode, func(t *testing.T) {
			c, store, _, r, req, _ := evidenceHandoffFixture(t)
			check := &r.Attempts[len(r.Attempts)-2]
			producer := &r.Attempts[len(r.Attempts)-3]
			check.Source.InputPatchID = ""
			producer.Source.InputPatchID = ""
			// The original owner has an implicit workspace seed and no patch input.
			delete(r.StepInputs, "patch")
			roleID := r.Process.Steps[check.Step].Role
			role := r.Roles[roleID]
			role.Execution.Mode = "shared"
			if mode == "other-mode" {
				role.Execution.Mode = ""
			}
			if mode == "writable" {
				role.Execution.Access = "workspace-write"
			}
			if mode == "no-candidate-binding" {
				delete(r.Process.Steps[check.Step].Inputs, "patch")
			}
			r.Roles[roleID] = role
			c.runs[r.ID] = Clone(r)
			if err := store.Save(t.Context(), r, nil, nil); err != nil {
				t.Fatal(err)
			}
			recovered, err := c.RecoverEvidenceHandoff(req)
			if mode != "shared" {
				if err == nil {
					t.Fatal("unsupported missing provenance accepted")
				}
				return
			}
			if err != nil || recovered.RouteUses["check/changes_requested"] != 8 || recovered.StepInputs["patch"].ArtifactID != "" {
				t.Fatal("historical shared-command handoff did not preserve implicit owner seed", err)
			}
			for _, old := range recovered.Attempts {
				if old.ID == check.ID || old.ID == producer.ID {
					if old.Source.InputPatchID != "" {
						t.Fatal("recovery fabricated missing historical input stamp")
					}
				}
			}
		})
	}
}

func TestEvidenceHandoffConcurrentSameRequestSpendsOnce(t *testing.T) {
	c, _, executor, _, req, _ := evidenceHandoffFixture(t)
	entered := make(chan struct{}, 2)
	proceed := make(chan struct{})
	executor.validate = func(Run, RecoverEvidenceHandoffRequest) error { entered <- struct{}{}; <-proceed; return nil }
	results := make(chan error, 2)
	for range 2 {
		go func() {
			r, err := c.RecoverEvidenceHandoff(req)
			if err == nil && r.RouteUses["check/changes_requested"] != 8 {
				err = errors.New("incorrect spent count")
			}
			results <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(4 * time.Second):
			t.Fatal("concurrent validation did not start")
		}
	}
	close(proceed)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	r := c.runs[req.RunID]
	if r.RouteUses["check/changes_requested"] != 8 || r.CorrectionRecovery.EvidenceHandoff == nil || r.CorrectionRecovery.EvidenceHandoff.Previous != nil {
		t.Fatal("concurrent request duplicated handoff")
	}
}
