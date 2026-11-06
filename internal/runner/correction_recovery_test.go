package runner_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
	"github.com/cortexium-io/runner/internal/runner/sqlite"
)

type correctionRecoveryExecutor struct {
	started        chan runner.Assignment
	cleanupEntered chan string
	validate       func(context.Context, runner.Run, runner.SourceResult) error
}

func (e *correctionRecoveryExecutor) Start(_ context.Context, a runner.Assignment) (runner.Observation, error) {
	e.started <- a
	return runner.Observation{State: runner.Unknown, Reason: "fixture holds the new owner"}, nil
}
func (*correctionRecoveryExecutor) Observe(context.Context, string) (runner.Observation, error) {
	return runner.Observation{State: runner.Unknown, Reason: "fixture holds the new owner"}, nil
}
func (*correctionRecoveryExecutor) Stop(context.Context, string) error { return nil }
func (e *correctionRecoveryExecutor) Release(ctx context.Context, id string) error {
	if e.cleanupEntered != nil {
		e.cleanupEntered <- id
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}
func (e *correctionRecoveryExecutor) ValidateSourceRecovery(ctx context.Context, r runner.Run, s runner.SourceResult) error {
	if e.validate != nil {
		return e.validate(ctx, r, s)
	}
	return nil
}

func correctionRecoveryRig(t *testing.T, change func(*runner.Run, *runner.RecoverCorrectionsRequest)) (*rig, runner.Snapshot, runner.Run, runner.RecoverCorrectionsRequest, *correctionRecoveryExecutor, string) {
	t.Helper()
	s := snapshot(policy())
	s.Instance.Concurrency = 1
	s.Instance.Storage.Database.MaxRecord = 64 << 10
	roles := map[string]runner.Role{
		"owner": {
			ID: "owner", Outputs: map[string]string{"summary": "text"},
			Execution: &runner.Profile{Harness: "codex", Access: "workspace-write", Model: "fixture", Reasoning: "high", MaxInput: 8192, MaxOutput: 512},
		},
		"check": {
			ID: "check", Outputs: map[string]string{"check": "text", "artifact": "artifact"},
			Execution: &runner.Profile{Harness: "command", Access: "workspace-read", Command: &runner.Command{Args: []string{"/fixture/check"}}},
		},
		"review": {
			ID: "review", Outputs: map[string]string{"summary": "text"},
			Execution: &runner.Profile{Harness: "codex", Access: "workspace-read"},
		},
		"qualify": {
			ID: "qualify", Outputs: map[string]string{"receipt": "json"},
			Execution: &runner.Profile{Harness: "release", Access: "input-only", Mode: "qualify"},
		},
	}
	proc := runner.Process{
		ID: "frontend", MaxAttempts: 35, MaxAttemptsPerStep: 1, AttemptTimeout: "20m",
		Steps: []runner.Step{
			{ID: "owner", Role: "owner", Inputs: map[string]runner.Binding{
				"checks": {FromStep: "check", Output: "artifact", Previous: true},
				"check":  {FromStep: "check", Output: "check", Previous: true},
			}},
			{ID: "check", Role: "check", Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}}, Routes: map[string]runner.Route{"changes_requested": {Next: "owner", MaxTimes: 1}}},
			{ID: "review", Role: "review", Inputs: map[string]runner.Binding{"patch": {FromCandidate: true}}, Routes: map[string]runner.Route{"changes_requested": {Next: "owner", MaxTimes: 1}}},
			{ID: "qualify", Role: "qualify", Routes: map[string]runner.Route{"changes_requested": {Next: "owner", MaxTimes: 1}, "ready": {Next: "$complete", Approval: true}}},
		},
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	source := &runner.SourceResult{AttemptID: "attempt_owner", PatchID: "attempt_owner/changes.patch", Tree: strings.Repeat("b", 40), Base: strings.Repeat("a", 40), Repository: filepath.Join(dir, "repository"), WorkDir: filepath.Join(dir, "checkout")}
	returned := func(outputs map[string]runner.Value, outcome string) *runner.ExecutionEnd {
		return &runner.ExecutionEnd{Kind: "returned", Result: &runner.StepResult{Outcome: outcome, Outputs: outputs}}
	}
	r := runner.Run{
		ID: "run_recovery", ProjectID: "demo", Repository: source.Repository, Source: &runner.SourceSettings{Ref: source.Base},
		Process: proc, Roles: roles, Profiles: map[string]string{}, Status: "Failed", Reason: "repetition allowance exhausted: check/changes_requested", Step: 1, Visit: 2,
		ResourcesReleased: true, Created: time.Now().UTC(),
		RouteUses:  map[string]int{"check/changes_requested": 1, "review/changes_requested": 1, "qualify/changes_requested": 1},
		StepInputs: map[string]runner.Value{"patch": {ArtifactID: source.PatchID}},
		Attempts: []runner.Attempt{
			{ID: "attempt_review", Step: 2, State: runner.Finished, Released: true, End: returned(map[string]runner.Value{"summary": runner.Text("earlier accepted review")}, "ready")},
			{ID: source.AttemptID, Step: 0, Visit: 2, State: runner.Finished, Released: true, Source: source, End: returned(map[string]runner.Value{"summary": runner.Text("retained owner")}, "ready")},
			{ID: "attempt_check", Step: 1, Visit: 2, State: runner.Finished, Released: true,
				Source: &runner.SourceResult{AttemptID: "attempt_check", InputPatchID: source.PatchID, Repository: source.Repository, Base: source.Base, WorkDir: source.WorkDir},
				End:    returned(map[string]runner.Value{"check": runner.Text("latest check failure"), "artifact": {ArtifactID: "attempt_check/result"}}, "changes_requested")},
		},
	}
	req := runner.RecoverCorrectionsRequest{RequestID: "owner-authorized-recovery", RunID: r.ID, AttemptID: "attempt_check", MaxUses: 3}
	if change != nil {
		change(&r, &req)
	}
	store, err := sqlite.Open(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := runner.OpenDisk(dir, s.Instance.Storage)
	if err != nil {
		t.Fatal(err)
	}
	var artifacts []runner.Artifact
	for id, text := range map[string]string{source.PatchID: "retained fixture source", "attempt_check/result": `{"executed":259,"failed":1}`} {
		if err := disk.Write("artifacts/"+id, []byte(text), disk.Policy.MaxArtifact); err != nil {
			t.Fatal(err)
		}
		artifacts = append(artifacts, runner.Artifact{ID: id, RunID: r.ID, ProjectID: r.ProjectID, Bytes: int64(len(text))})
	}
	if err := store.Save(t.Context(), r, artifacts, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	executor := &correctionRecoveryExecutor{started: make(chan runner.Assignment, 4)}
	for _, attempt := range r.Attempts {
		if !attempt.Released {
			// Preserve the negative fixture's unfinished cleanup until Core
			// shutdown. An immediate successful mock Release would otherwise
			// legitimately make recovery eligible before the assertion runs.
			executor.cleanupEntered = make(chan string, len(r.Attempts))
			break
		}
	}
	rig := openRig(t, dir, s, func(runner.Executor) runner.Executor { return executor })
	return rig, s, r, req, executor, dir
}

func TestCorrectionRecoveryRoutesSavedFailureWithoutChangingDefinitionOrHistory(t *testing.T) {
	rig, cfg, before, req, executor, dir := correctionRecoveryRig(t, nil)
	_, definition, _ := runner.MarshalRunRecord(before)
	recovered, err := rig.core.RecoverCorrections(req)
	if err != nil {
		rig.close()
		t.Fatal(err)
	}
	if recovered.Status != "Queued" || recovered.Step != 0 || recovered.Visit != before.Visit+1 || len(recovered.Attempts) != len(before.Attempts) || recovered.RouteUses["check/changes_requested"] != 2 || recovered.Process.MaxAttempts != 35 || recovered.Process.MaxAttemptsPerStep != 1 {
		t.Fatalf("grant repeated work or changed a budget: %+v", recovered)
	}
	_, afterDefinition, _ := runner.MarshalRunRecord(recovered)
	if !bytes.Equal(definition, afterDefinition) || recovered.StepInputs["checks"].ArtifactID != "attempt_check/result" || !reflect.DeepEqual(recovered.StepInputs["check"], runner.Text("latest check failure")) {
		t.Fatal("definition changed or saved failed-check feedback was lost")
	}
	for i, a := range before.Attempts {
		got := recovered.Attempts[i]
		if !got.Superseded || !reflect.DeepEqual(a.End, got.End) || !reflect.DeepEqual(a.Source, got.Source) || a.ID != got.ID {
			t.Fatal("normal invalidation rewrote attempt evidence", i)
		}
	}
	audit := recovered.CorrectionRecovery
	if audit == nil || audit.MaxUses != 3 || audit.RequestID != req.RequestID || audit.AttemptID != req.AttemptID || audit.Reason != before.Reason || len(audit.RouteLimits) != 3 || audit.PatchID != "attempt_owner/changes.patch" {
		t.Fatal("bounded audit lost the original failure/edge/source", audit)
	}
	for _, n := range audit.RouteLimits {
		if n != 3 {
			t.Fatal("grant means extra uses instead of total three", audit)
		}
	}
	select {
	case assignment := <-executor.started:
		if assignment.Setup.Profile.Access != "workspace-write" || assignment.Inputs["checks"].ArtifactID != "attempt_check/result" {
			t.Fatal("recovery reexecuted the failed check instead of entering owner", assignment)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("new owner was not dispatched")
	}
	rig.close()
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	defer rig.close()
	replay, err := rig.core.RecoverCorrections(req)
	if err != nil || !reflect.DeepEqual(replay.CorrectionRecovery, audit) || len(replay.Attempts) != len(before.Attempts)+1 {
		t.Fatal("restart replay granted more work or lost audit", err)
	}
	changed := req
	changed.MaxUses = 4
	if _, err := rig.core.RecoverCorrections(changed); err == nil {
		t.Fatal("request replay accepted different authorization")
	}
}

func TestCorrectionRecoveryRejectsOtherFailuresAndBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*runner.Run, *runner.RecoverCorrectionsRequest)
	}{
		{"wrong attempt", func(_ *runner.Run, q *runner.RecoverCorrectionsRequest) { q.AttemptID = "attempt_owner" }},
		{"not extra three", func(_ *runner.Run, q *runner.RecoverCorrectionsRequest) { q.MaxUses = 4 }},
		{"cancelled", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.CancelRequested = true }},
		{"waiting", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Status = "Waiting" }},
		{"unknown", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().State = runner.Unknown }},
		{"deadline", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().TimedOut = true }},
		{"stopped", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().StopRequested = true }},
		{"execution failure", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().End.Kind = "failed" }},
		{"infrastructure", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().End.Retryable = true }},
		{"blocked", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().End.Result.Outcome = "blocked" }},
		{"approval", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) {
			r.Decision = &runner.Decision{ID: "protected"}
		}},
		{"approval edge", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) {
			step := &r.Process.Steps[1]
			route := step.Routes["changes_requested"]
			route.Approval = true
			step.Routes["changes_requested"] = route
		}},
		{"release effects", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) {
			r.Roles["check"].Execution.Harness = "release"
			r.Roles["check"].Execution.Mode = "deploy"
		}},
		{"cleanup blocked", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.CleanupBlocked = true }},
		{"cleanup incomplete", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.ResourcesReleased = false }},
		{"attempt cleanup", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) { r.Current().Released = false }},
		{"35 exhausted", func(r *runner.Run, _ *runner.RecoverCorrectionsRequest) {
			for len(r.Attempts) < 35 {
				a := r.Attempts[0]
				r.Attempts = append([]runner.Attempt{a}, r.Attempts...)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rig, _, _, req, executor, _ := correctionRecoveryRig(t, test.change)
			defer rig.close()
			if executor.cleanupEntered != nil {
				select {
				case id := <-executor.cleanupEntered:
					if id != req.AttemptID {
						t.Fatal("fixture is holding a different attempt's cleanup", id)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("unfinished attempt cleanup did not start")
				}
			}
			_, err := rig.core.RecoverCorrections(req)
			if err == nil {
				t.Fatal("non-source recovery or allowance change accepted")
			}
			if executor.cleanupEntered != nil && !strings.Contains(err.Error(), "all execution and attempt cleanup must settle") {
				t.Fatal("unfinished cleanup was not the recovery barrier", err)
			}
		})
	}
}

func TestCorrectionRecoveryRefusesUnverifiedOrChangedSource(t *testing.T) {
	rig, _, before, req, executor, _ := correctionRecoveryRig(t, nil)
	defer rig.close()
	executor.validate = func(context.Context, runner.Run, runner.SourceResult) error {
		return errors.New("workspace ownership drift")
	}
	if _, err := rig.core.RecoverCorrections(req); err == nil || !strings.Contains(err.Error(), "ownership drift") {
		t.Fatal("source proof failure was hidden", err)
	}
	got, _ := rig.core.Get(before.ID)
	if got.CorrectionRecovery != nil || got.Revision != before.Revision || got.Status != "Failed" {
		t.Fatal("failed validation consumed the grant or rewrote failure", got)
	}
}

func TestConcurrentCorrectionRecoveryRejoinsCommittedReceiptAfterSourceChanged(t *testing.T) {
	rig, _, before, req, executor, _ := correctionRecoveryRig(t, nil)
	defer rig.close()
	entered := []chan struct{}{make(chan struct{}), make(chan struct{})}
	release := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var calls atomic.Int32
	executor.validate = func(ctx context.Context, _ runner.Run, _ runner.SourceResult) error {
		index := int(calls.Add(1)) - 1
		close(entered[index])
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-release[index]:
		}
		if index == 1 {
			return errors.New("workspace changed after the first request committed")
		}
		return nil
	}
	results := []chan error{make(chan error, 1), make(chan error, 1)}
	for i := range 2 {
		go func() { _, err := rig.core.RecoverCorrections(req); results[i] <- err }()
		<-entered[i]
	}
	close(release[0])
	if err := <-results[0]; err != nil {
		t.Fatal(err)
	}
	close(release[1])
	if err := <-results[1]; err != nil {
		t.Fatal("same owner request lost idempotent result after concurrent completion", err)
	}
	got, _ := rig.core.Get(before.ID)
	if got.CorrectionRecovery == nil || got.RouteUses["check/changes_requested"] != 2 || len(got.Attempts) > len(before.Attempts)+1 {
		t.Fatal("concurrent replay duplicated a grant or execution", got)
	}
}
