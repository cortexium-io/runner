package runner_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner"
)

func currentCorrectionFixture(r *runner.Run, req *runner.RecoverCorrectionsRequest) {
	r.Revision = 10
	r.Inputs = map[string]runner.Value{"request": runner.Text("Immutable original owner request")}
	step := &r.Process.Steps[1]
	route := step.Routes["changes_requested"]
	route.MaxTimes = 3
	step.Routes["changes_requested"] = route
	r.RouteUses["check/changes_requested"] = 3
	content := `{"policy":"Preserve labels and all release gates","runtime":{"modules_env":"RUNNER_PROJECT_FRONTEND_MODULES"}}`
	*req = runner.RecoverCorrectionsRequest{
		RequestID:    "one-more-current-route",
		RunID:        r.ID,
		AttemptID:    r.Current().ID,
		CurrentRoute: true,
		Revision:     r.Revision,
		MaxUses:      4,
		PatchSHA256:  fmt.Sprintf("%x", sha256.Sum256([]byte("retained fixture source"))),
		CheckSHA256:  fmt.Sprintf("%x", sha256.Sum256([]byte(`{"executed":259,"failed":1}`))),
		Context: &runner.CorrectionContextInput{
			Content: content,
			Bytes:   int64(len(content)),
			SHA256:  fmt.Sprintf("%x", sha256.Sum256([]byte(content))),
			Roles:   []string{"owner", "review"},
		},
	}
}

type correctionCommitStore struct {
	runner.Store
	mode       string
	failed     atomic.Bool
	committed  atomic.Bool
	readFailed atomic.Bool
}

func (s *correctionCommitStore) Save(ctx context.Context, r runner.Run, artifacts []runner.Artifact, receipt *runner.Receipt) error {
	if receipt != nil && receipt.Kind == "recover_corrections" && s.failed.CompareAndSwap(false, true) {
		if s.mode == "refused" {
			return errors.New("fixture refused correction commit")
		}
		if err := s.Store.Save(ctx, r, artifacts, receipt); err != nil {
			return err
		}
		s.committed.Store(true)
		return errors.New("fixture lost commit acknowledgement")
	}
	return s.Store.Save(ctx, r, artifacts, receipt)
}

func (s *correctionCommitStore) FindReceipt(ctx context.Context, id string) (runner.Receipt, error) {
	if s.mode == "lost-read" && s.committed.Load() && s.readFailed.CompareAndSwap(false, true) {
		return runner.Receipt{}, errors.New("fixture receipt read unavailable")
	}
	return s.Store.FindReceipt(ctx, id)
}

func TestCorrectionRecoveryCurrentRouteCommitRefusalAndUncertainReplay(t *testing.T) {
	for _, scenario := range []string{"refused", "lost-ack", "lost-read", "successive-refused", "successive-lost-ack", "successive-lost-read"} {
		mode := strings.TrimPrefix(scenario, "successive-")
		t.Run(scenario, func(t *testing.T) {
			rig, cfg, before, req, executor, _ := correctionRecoveryRig(t, func(r *runner.Run, q *runner.RecoverCorrectionsRequest) {
				currentCorrectionFixture(r, q)
				if strings.HasPrefix(scenario, "successive-") {
					r.CorrectionRecovery = &runner.CorrectionRecovery{RequestID: "prior-grant", AttemptID: "prior-check", PatchID: "prior-owner/changes.patch", CurrentRoute: true, Route: "check/changes_requested", PreviousUses: 3, MaxUses: 4, RouteLimits: map[string]int{"check/changes_requested": 4}}
					r.RouteUses["check/changes_requested"] = 4
					q.PreviousRequestID = "prior-grant"
					q.MaxUses = 5
				}
			})
			rig.core.Close()
			store := &correctionCommitStore{Store: rig.store, mode: mode}
			core, err := runner.New(t.Context(), store, executor, rig.disk, cfg)
			if err != nil {
				rig.close()
				t.Fatal(err)
			}
			rig.core = core
			defer rig.close()
			recovered, err := core.RecoverCorrections(req)
			if mode != "lost-ack" {
				if err == nil {
					t.Fatal("fixture failure was not observed")
				}
				if mode == "refused" {
					got, _ := core.Get(before.ID)
					if !reflect.DeepEqual(got.CorrectionRecovery, before.CorrectionRecovery) || got.Status != "Failed" || got.RouteUses["check/changes_requested"] != before.RouteUses["check/changes_requested"] {
						t.Fatal("refused commit granted work")
					}
				}
				recovered, err = core.RecoverCorrections(req)
			}
			if err != nil || recovered.CorrectionRecovery == nil || recovered.RouteUses["check/changes_requested"] != req.MaxUses || !reflect.DeepEqual(recovered.CorrectionRecovery.Previous, before.CorrectionRecovery) {
				t.Fatal("same request did not reconcile one grant", err)
			}
			select {
			case a := <-executor.started:
				if a.RecoveryContext == nil {
					t.Fatal("reconciled owner lacks context")
				}
			case <-time.After(4 * time.Second):
				t.Fatal("reconciled owner was not dispatched")
			}
			if len(executor.started) != 0 {
				t.Fatal("replay launched another owner")
			}
		})
	}
}

func TestCorrectionRecoveryCurrentRoutePreservesSpendingAndScopedContext(t *testing.T) {
	rig, cfg, before, req, executor, dir := correctionRecoveryRig(t, currentCorrectionFixture)
	// This explicit finite local grant also works when aggregate caps are off.
	rig.close()
	cfg.Instance.EnforceWorkLimits = false
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	_, definition, _ := runner.MarshalRunRecord(before)
	recovered, err := rig.core.RecoverCorrections(req)
	if err != nil {
		rig.close()
		t.Fatal(err)
	}
	grant := recovered.CorrectionRecovery
	if grant == nil || !grant.CurrentRoute || grant.PreviousUses != 3 || grant.MaxUses != 4 || grant.RouteLimits["check/changes_requested"] != 4 || len(grant.RouteLimits) != 1 || grant.Revision != req.Revision || grant.FromVisit != recovered.Visit || recovered.RouteUses["check/changes_requested"] != 4 {
		t.Fatal("grant changed spending or another route", grant)
	}
	_, afterDefinition, _ := runner.MarshalRunRecord(recovered)
	if !bytes.Equal(definition, afterDefinition) || grant.DefinitionSHA256 != fmt.Sprintf("%x", sha256.Sum256(definition)) || !reflect.DeepEqual(before.Inputs, recovered.Inputs) || recovered.RouteUses["review/changes_requested"] != 1 {
		t.Fatal("frozen definition/original request/other route changed")
	}
	for i, old := range before.Attempts {
		a := recovered.Attempts[i]
		if a.ID != old.ID || !reflect.DeepEqual(a.End, old.End) || !reflect.DeepEqual(a.Source, old.Source) {
			t.Fatal("old source/evidence changed")
		}
	}
	if grant.Context == nil {
		t.Fatal("supplemental context absent")
	}
	receipt, err := rig.store.FindReceipt(t.Context(), req.RequestID)
	var durable runner.RecoverCorrectionsRequest
	if err != nil || json.Unmarshal(receipt.Args, &durable) != nil || durable.Context == nil || durable.Context.Content != "" || durable.Context.SHA256 != req.Context.SHA256 {
		t.Fatal("receipt duplicated document bytes or lost its exact identity", err)
	}
	content, err := rig.disk.Read("artifacts/"+grant.Context.ID, runner.MaxCorrectionContext)
	if err != nil || string(content) != req.Context.Content {
		t.Fatal("context bytes changed", err)
	}
	select {
	case a := <-executor.started:
		if a.RecoveryContext == nil || a.RecoveryContext.ID != grant.Context.ID || a.Setup.Profile.Access != "workspace-write" {
			t.Fatal("new owner did not receive scoped context", a.RecoveryContext)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("new owner was not dispatched")
	}
	rig.close()
	rig = openRig(t, dir, cfg, func(runner.Executor) runner.Executor { return executor })
	defer rig.close()
	replay, err := rig.core.RecoverCorrections(req)
	if err != nil || !reflect.DeepEqual(replay.CorrectionRecovery, grant) || replay.RouteUses["check/changes_requested"] != 4 {
		t.Fatal("restart replay changed one-use grant", err)
	}
	changed := runner.Clone(req)
	changed.Context.Content += " "
	if _, err := rig.core.RecoverCorrections(changed); err == nil {
		t.Fatal("changed content replay accepted")
	}
	changed = runner.Clone(req)
	changed.RequestID = "second-grant"
	if _, err := rig.core.RecoverCorrections(changed); err == nil {
		t.Fatal("second grant accepted")
	}
}

func TestCorrectionRecoveryCurrentRouteRejectsChangedAuthority(t *testing.T) {
	for _, mode := range []string{"stale", "patch", "check", "two-uses", "mixed-mode", "context-hash", "foreign-role", "cleanup", "foreign-artifact"} {
		t.Run(mode, func(t *testing.T) {
			rig, _, before, req, _, _ := correctionRecoveryRig(t, func(r *runner.Run, q *runner.RecoverCorrectionsRequest) {
				currentCorrectionFixture(r, q)
				switch mode {
				case "stale":
					q.Revision--
				case "patch":
					q.PatchSHA256 = strings.Repeat("0", 64)
				case "check":
					q.CheckSHA256 = strings.Repeat("0", 64)
				case "two-uses":
					q.MaxUses = 5
				case "mixed-mode":
					q.WithinBudget = true
				case "context-hash":
					q.Context.Content = `{"altered":true}`
				case "foreign-role":
					q.Context.Roles = []string{"unconfigured"}
				case "cleanup":
					r.Current().Released = false
				case "foreign-artifact":
					q.RunID = "another-root"
				}
			})
			defer rig.close()
			if _, err := rig.core.RecoverCorrections(req); err == nil {
				t.Fatal("changed authority was admitted")
			}
			got, err := rig.core.Get(before.ID)
			if err != nil || got.CorrectionRecovery != nil || got.Status != "Failed" || got.RouteUses["check/changes_requested"] != 3 {
				t.Fatal("rejected grant changed saved work", err)
			}
		})
	}
}

func TestCorrectionRecoveryCurrentRouteRechecksArtifactsAfterSourceValidation(t *testing.T) {
	for _, which := range []string{"patch", "check"} {
		t.Run(which, func(t *testing.T) {
			rig, _, before, req, executor, _ := correctionRecoveryRig(t, currentCorrectionFixture)
			defer rig.close()
			executor.validate = func(_ context.Context, _ runner.Run, source runner.SourceResult) error {
				id := source.PatchID
				if which == "check" {
					id = req.AttemptID + "/result"
				}
				data, err := rig.disk.Read("artifacts/"+id, rig.disk.Policy.MaxArtifact)
				if err != nil {
					return err
				}
				data[0] ^= 1 // Same ownership/length, changed authenticated bytes.
				return rig.disk.Write("artifacts/"+id, data, rig.disk.Policy.MaxArtifact)
			}
			if _, err := rig.core.RecoverCorrections(req); err == nil || !strings.Contains(err.Error(), "SHA256 differs") {
				t.Fatal("artifact changed during unlocked validation was accepted", err)
			}
			got, _ := rig.core.Get(before.ID)
			if got.CorrectionRecovery != nil || got.RouteUses["check/changes_requested"] != 3 || len(executor.started) != 0 {
				t.Fatal("failed revalidation granted work")
			}
		})
	}
}
