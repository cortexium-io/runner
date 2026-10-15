package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/store"
)

type mergeAdapter struct {
	observe func(context.Context, core.Publication) (core.MergeObservation, error)
	merge   func(context.Context, core.Publication, core.MergeAdmission, func() error) error
}

func (a mergeAdapter) ObserveMerge(ctx context.Context, p core.Publication) (core.MergeObservation, error) {
	return a.observe(ctx, p)
}
func (a mergeAdapter) Merge(ctx context.Context, p core.Publication, m core.MergeAdmission, authorize func() error) error {
	return a.merge(ctx, p, m, authorize)
}

func readyMerge(task core.Task, p core.Publication) core.MergeObservation {
	remote := *p.Observation
	remote.Draft = false
	return core.MergeObservation{IntegrationObservation: core.IntegrationObservation{Remote: remote, Base: task.BaseProof.Base, Checks: p.CI.Observation.Checks}, Admission: core.MergeAdmission{Eligible: true, Detail: "protected", PullRequestID: "PR7", ProtectionRuleID: "rule"}}
}

func TestExplicitMergeAdmissionAndLostResponseRecovery(t *testing.T) {
	e, task, p := integrationReady(t)
	good := readyMerge(task, p)
	merged := mergedObservation(t, e, task, p)
	scenario := ""
	reads, writes := 0, 0
	tip := task.BaseProof.Base
	observation := good.IntegrationObservation
	e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) { return tip, nil })
	e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) { return observation, nil })
	e.Merges = mergeAdapter{
		observe: func(context.Context, core.Publication) (core.MergeObservation, error) {
			reads++
			o := good
			switch scenario {
			case "blocked":
				o.Admission.Eligible, o.Admission.Detail = false, "review pending"
			case "draft":
				o.Remote.Draft = true
			case "head changed":
				o.Remote.BranchCommit = "other"
			case "host base moved":
				o.Base = "other"
			case "CI changed after fetch":
				if reads == 2 {
					o.Checks = append([]core.RemoteCheck(nil), good.Checks...)
					o.Checks[0].Conclusion = "FAILURE"
				}
			case "policy changed after fetch":
				if reads == 2 {
					o.Admission.Eligible = false
					o.Admission.Detail = "base protection changed"
				}
			}
			return o, nil
		},
		merge: func(ctx context.Context, p core.Publication, a core.MergeAdmission, authorize func() error) error {
			if err := authorize(); err != nil {
				return err
			}
			saved := publication(t, e)
			if reads != 2 || saved.MergeDispatch == nil || !saved.Occupying || core.Digest(saved.MergeDispatch.Request) != core.Digest(p.IntegrationRequest) {
				t.Fatal("dispatch without durable exact admission", reads, saved)
			}
			writes++
			return errors.New("merge response lost")
		},
	}
	for _, name := range []string{"blocked", "draft", "head changed", "host base moved", "fetched base moved", "CI changed after fetch", "policy changed after fetch", "lost response"} {
		t.Run(name, func(t *testing.T) {
			scenario, reads, tip = name, 0, task.BaseProof.Base
			if name == "fetched base moved" {
				tip = "other"
			}
			if err := e.RequestMerge(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			claimed := deliveryClaim(t, e)
			if !claimed.IntegrationRequest.Merge {
				t.Fatal("lost explicit authority")
			}
			if name == "blocked" {
				reopen(t, e)
				claimed = deliveryClaim(t, e)
			} // Unsent admission survives restart.
			if err := e.publish(t.Context(), claimed); err != nil {
				t.Fatal(err)
			}
			got := publication(t, e)
			want := "blocked"
			switch name {
			case "head changed":
				want = "unavailable"
			case "host base moved", "fetched base moved":
				want = "stale"
			case "lost response":
				want = "uncertain"
			}
			if got.Integration.State != want || got.Occupying || got.IntegrationRequest != nil || current(t, e, task.ID).Stage == core.Completed {
				t.Fatal(name, got)
			}
			if (got.MergeDispatch != nil) != (name == "lost response") {
				t.Fatal("incorrect consumed write authority", got)
			}
			if err := e.preparePublications(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
				t.Fatal("operation polled or repeated itself", ok, err)
			}
		})
	}
	if writes != 1 {
		t.Fatal("wrong write count", writes)
	}
	if err := e.RequestMerge(t.Context(), task.ID); err == nil {
		t.Fatal("uncertain write was reauthorized")
	}
	saved := publication(t, e)
	originalDispatch := core.Digest(saved.MergeDispatch)
	saved.MergeDispatch = nil
	if err := e.savePublication(t.Context(), &saved); err == nil {
		t.Fatal("dispatch permission could be reset")
	}
	reopen(t, e)
	if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	observation, tip = merged, merged.MergeCommit
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	got := publication(t, e)
	if writes != 1 || current(t, e, task.ID).Stage != core.Completed || core.Digest(got.MergeDispatch) != originalDispatch || current(t, e, task.ID).Usage != task.Usage {
		t.Fatal("recovery changed authority or failed receipt", got)
	}
}

func TestMergeDispatchCrashAndStop(t *testing.T) {
	for _, scenario := range []string{"crash after dispatch marker", "stop at transport", "confirmed despite lost response"} {
		t.Run(scenario, func(t *testing.T) {
			e, task, p := integrationReady(t)
			good := readyMerge(task, p)
			merged := mergedObservation(t, e, task, p)
			writes := 0
			sent := false
			e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) {
				if sent {
					return merged.MergeCommit, nil
				}
				return task.BaseProof.Base, nil
			})
			e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) {
				if sent {
					return merged, nil
				}
				return good.IntegrationObservation, nil
			})
			e.Merges = mergeAdapter{
				observe: func(context.Context, core.Publication) (core.MergeObservation, error) { return good, nil },
				merge: func(ctx context.Context, p core.Publication, _ core.MergeAdmission, authorize func() error) error {
					if scenario == "stop at transport" {
						if err := e.Stop(ctx, task.ID); err != nil {
							t.Fatal(err)
						}
					}
					if err := authorize(); err != nil {
						return err
					}
					writes++
					sent = true
					return errors.New("response lost after successful merge")
				},
			}
			if err := e.RequestMerge(t.Context(), task.ID); err != nil {
				t.Fatal(err)
			}
			claimed := deliveryClaim(t, e)
			if scenario == "crash after dispatch marker" {
				claimed.MergeDispatch = &core.MergeDispatch{Request: *claimed.IntegrationRequest, Admission: good.Admission, RequestedAt: time.Now().UTC()}
				if err := e.savePublication(t.Context(), &claimed); err != nil {
					t.Fatal(err)
				}
				reopen(t, e)
				claimed = deliveryClaim(t, e)
			}
			if err := e.publish(t.Context(), claimed); err != nil {
				t.Fatal(err)
			}
			got := publication(t, e)
			now := current(t, e, task.ID)
			if got.MergeDispatch == nil || got.Occupying || got.IntegrationRequest != nil {
				t.Fatal("dispatch/release lost", got)
			}
			switch scenario {
			case "crash after dispatch marker":
				if writes != 0 || got.Integration.State != "uncertain" || now.Stage == core.Completed {
					t.Fatal("replayed ambiguous mutation", writes, got)
				}
			case "stop at transport":
				if writes != 0 || now.Stage != core.Stopped || got.Integration.State != "stale" {
					t.Fatal("stop lost before dispatch", writes, got)
				}
			case "confirmed despite lost response":
				if writes != 1 || now.Stage != core.Completed || now.Integration == nil {
					t.Fatal("exact receipt failed", writes, now)
				}
			}
			v, err := e.Show(t.Context(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "crash after dispatch marker" && (!strings.Contains(v.Summary, "uncertain") || !strings.Contains(v.NextAction, "integrate")) {
				t.Fatal("missing recovery advice", v)
			}
		})
	}
}

// Use the same real Git/SQLite publication journey as explicit merges. Policy
// changes must never replace approval, repeat a write, or spend model allowance.
func TestAutomaticMergeConsumesOnlyReadyObservation(t *testing.T) {
	e, task, p := integrationReady(t)
	good := readyMerge(task, p)
	observation := good.IntegrationObservation
	merged := mergedObservation(t, e, task, p)
	writes, admissions := 0, 0
	blocked := true
	tip := task.BaseProof.Base
	e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) { return tip, nil })
	e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) { return observation, nil })
	e.Merges = mergeAdapter{
		observe: func(context.Context, core.Publication) (core.MergeObservation, error) {
			admissions++
			o := good
			if blocked {
				o.Admission.Eligible, o.Admission.Detail = false, "review pending"
			}
			return o, nil
		},
		merge: func(ctx context.Context, p core.Publication, _ core.MergeAdmission, authorize func() error) error {
			if err := authorize(); err != nil {
				return err
			}
			if p.MergeDispatch == nil || p.MergeDispatch.Request.AutomaticPolicy == "" {
				t.Fatal("missing durable policy origin", p)
			}
			writes++
			observation, tip = merged, merged.MergeCommit
			return nil
		},
	}
	assertIdle := func() {
		t.Helper()
		if err := e.preparePublications(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
			t.Fatal("unexpected repeated admission", ok, err)
		}
	}
	inspect := func() {
		t.Helper()
		if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
			t.Fatal(err)
		}
	}
	// Omission remains human, even with an already observed ready PR.
	inspect()
	assertIdle()
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic" })
	settings, _, err := e.Store.Settings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Read-only inspection reflects new policy but does not derive a request.
	before := core.Digest(publication(t, e))
	v, err := e.Show(t.Context(), task.ID)
	if err != nil || v.MergePolicy != "automatic" || !strings.Contains(v.Summary, "automatic merge") {
		t.Fatal("policy not described", v.MergePolicy, v.Summary, err)
	}
	if !describeIntegration(&v, publication(t, e), true) || v.Group != "Waiting" {
		t.Fatal("automatic admission incorrectly requires operator action", v.Group, v.Summary)
	}
	if _, err := e.Status(t.Context(), settings.Config); err != nil {
		t.Fatal(err)
	}
	if core.Digest(publication(t, e)) != before {
		t.Fatal("status/show started automatic work")
	}
	// Existing approval is sufficient; one blocked admission consumes this read.
	claimed := deliveryClaim(t, e)
	if claimed.IntegrationRequest.AutomaticPolicy != settings.Config.Revision() {
		t.Fatal("incorrect policy origin", claimed)
	}
	if err := e.publish(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	if publication(t, e).Integration.State != "blocked" {
		t.Fatal(publication(t, e))
	}
	assertIdle()
	reopen(t, e)
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic"; c.Policy.MaxParallelism = 2 })
	assertIdle()
	if admissions != 1 || writes != 0 {
		t.Fatal("blocked assessment retried", admissions, writes)
	}
	// Closed and draft observations cannot authorize merging.
	observation.Remote.State = "CLOSED"
	inspect()
	assertIdle()
	v, err = e.Show(t.Context(), task.ID)
	if err != nil || !strings.Contains(v.NextAction, "revise or stop") {
		t.Fatal("closed PR advice", v.NextAction, err)
	}
	observation.Remote.State, observation.Remote.Draft = "OPEN", true
	inspect()
	assertIdle()
	// A fresh explicit read of a ready PR can trigger the next bounded attempt.
	observation.Remote.Draft, blocked = false, false
	inspect()
	claimed = deliveryClaim(t, e)
	if err := e.publish(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
	got := current(t, e, task.ID)
	if writes != 1 || got.Stage != core.Completed || got.Integration == nil || got.Usage != task.Usage {
		t.Fatal("automatic completion lost proof or spent model allowance", writes, got)
	}
	assertIdle()
}

func TestAutomaticMergePolicyRevocationAndExplicitOverride(t *testing.T) {
	e, task, p := integrationReady(t)
	good := readyMerge(task, p)
	e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) {
		return good.IntegrationObservation, nil
	})
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic" })
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	queued := publication(t, e)
	if queued.IntegrationRequest == nil || queued.IntegrationRequest.AutomaticPolicy == "" {
		t.Fatal("automatic request missing", queued)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
	if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
		t.Fatal("disabled request claimed", ok, err)
	}
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	canceled := publication(t, e)
	if canceled.IntegrationRequest != nil || canceled.Integration.State != "blocked" {
		t.Fatal("disabled request not settled", canceled)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic" })
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if publication(t, e).IntegrationRequest != nil {
		t.Fatal("policy toggle rearmed canceled request")
	}
	// The store cannot mint automatic authority from an old merge assessment.
	canceled.IntegrationRequest = queued.IntegrationRequest
	if err := e.savePublication(t.Context(), &canceled); !errors.Is(err, store.ErrConflict) {
		t.Fatal("reused observation admitted", err)
	}
	if err := e.InspectIntegration(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
		t.Fatal(err)
	}
	if err := e.preparePublications(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	// An explicit command replaces queued automatic authority. Later disabling
	// automatic mode must not revoke that independently authorized command.
	if err := e.RequestMerge(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
	claimed := deliveryClaim(t, e)
	if !claimed.IntegrationRequest.Merge || claimed.IntegrationRequest.AutomaticPolicy != "" {
		t.Fatal("explicit authority lost", claimed)
	}
	if err := e.mergeAuthority(t.Context(), claimed); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticMergeDisableDuringAdmissionAndRecovery(t *testing.T) {
	for _, scenario := range []string{"before dispatch persistence", "at transport", "restart after dispatch"} {
		t.Run(scenario, func(t *testing.T) {
			e, task, p := integrationReady(t)
			good := readyMerge(task, p)
			merged := mergedObservation(t, e, task, p)
			observation, tip := good.IntegrationObservation, task.BaseProof.Base
			writes := 0
			e.Bases = baseAdapter(func(context.Context, string, string, string) (string, error) { return tip, nil })
			e.Integrations = integrationAdapter(func(context.Context, core.Publication) (core.IntegrationObservation, error) { return observation, nil })
			e.Merges = mergeAdapter{
				observe: func(context.Context, core.Publication) (core.MergeObservation, error) {
					if scenario == "before dispatch persistence" {
						applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
					}
					return good, nil
				},
				merge: func(ctx context.Context, p core.Publication, _ core.MergeAdmission, authorize func() error) error {
					applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
					if err := authorize(); err != nil {
						return err
					}
					writes++
					return nil
				},
			}
			if err := e.publish(t.Context(), deliveryClaim(t, e)); err != nil {
				t.Fatal(err)
			}
			applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic" })
			if err := e.preparePublications(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			origin := publication(t, e).IntegrationRequest.AutomaticPolicy
			applyPolicy(t, e, func(c *config.Config) { c.Policy.MaxParallelism = 2 })
			claimed := deliveryClaim(t, e)
			reopen(t, e)
			claimed = deliveryClaim(t, e) // Interrupted unsent admission resumes under enabled policy.
			if claimed.IntegrationRequest.AutomaticPolicy != origin {
				t.Fatal("unrelated edit replaced merge origin")
			}
			if scenario == "restart after dispatch" {
				claimed.MergeDispatch = &core.MergeDispatch{Request: *claimed.IntegrationRequest, Admission: good.Admission, RequestedAt: time.Now().UTC()}
				// The same transaction that records dispatch refuses disabled policy.
				applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
				if err := e.savePublication(t.Context(), &claimed); !errors.Is(err, store.ErrConflict) {
					t.Fatal("disabled dispatch persisted", err)
				}
				applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "automatic" })
				if err := e.savePublication(t.Context(), &claimed); err != nil {
					t.Fatal(err)
				}
				applyPolicy(t, e, func(c *config.Config) { c.Policy.Merge = "human" })
				reopen(t, e)
				claimed = deliveryClaim(t, e)
				observation, tip = merged, merged.MergeCommit
			}
			if err := e.publish(t.Context(), claimed); err != nil {
				t.Fatal(err)
			}
			got := publication(t, e)
			if writes != 0 || got.Occupying || got.IntegrationRequest != nil {
				t.Fatal("disabled merge dispatched or reservation leaked", writes, got)
			}
			if scenario == "before dispatch persistence" {
				if got.MergeDispatch != nil || !strings.Contains(got.Integration.Detail, "disabled") {
					t.Fatal("lost policy revocation", got)
				}
			} else if got.MergeDispatch == nil {
				t.Fatal("lost consumed authority", got)
			}
			if (current(t, e, task.ID).Stage == core.Completed) != (scenario == "restart after dispatch") {
				t.Fatal("incorrect receipt completion", got)
			}
			if err := e.preparePublications(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := e.claimPublication(t.Context()); err != nil || ok {
				t.Fatal("disabled/consumed request repeated", ok, err)
			}
		})
	}
}
