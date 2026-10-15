package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cortexium-io/runner/internal/runner/core"
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
