package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
)

type Recovery struct {
	Role      string `json:"role,omitempty"`
	Preserved string `json:"preserved"`
	Repeats   string `json:"repeats"`
}

type Status struct {
	ObservedAt        time.Time `json:"observed_at"`
	Running           bool      `json:"coordinator_running"`
	Drained           bool      `json:"drained"`
	Unapplied         bool      `json:"unapplied_config"`
	QualityConfigured bool      `json:"quality_configured"`
	Parallelism       int       `json:"parallelism"`
	Occupied          int       `json:"occupied"`
	Tasks             []View    `json:"tasks"`
}

var StatusGroups = []string{"Needs you", "Working", "Waiting", "Retained"}

func (e *Engine) observeCoordinator() (bool, error) {
	running, err := execution.ObserveCoordinator(filepath.Join(e.Settings.Config.StateDir, "coordinator.lock"))
	if err != nil {
		return false, fmt.Errorf("coordinator ownership is unknown: %w", err)
	}
	return running, nil
}

// Status observes only. Every suggested action still goes through its command's
// authority checks; the coordinator may progress while these reads take place.
func (e *Engine) Status(ctx context.Context, file config.Config) (Status, error) {
	s, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return Status{}, err
	}
	running, err := e.observeCoordinator()
	if err != nil {
		return Status{}, err
	}
	out := Status{ObservedAt: e.now(), Running: running, Drained: drained, Unapplied: file.Revision() != s.Config.Revision(), QualityConfigured: s.Quality.Revision() != "", Parallelism: s.Config.Policy.MaxParallelism, Tasks: []View{}}
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return out, err
	}
	for _, t := range tasks {
		v, err := e.Show(ctx, t.ID)
		if err != nil {
			return out, err
		}
		for _, a := range v.Attempts {
			if a.Occupying {
				out.Occupied++
			}
		}
		for _, p := range v.Publications {
			if p.Occupying {
				out.Occupied++
			}
		}
		out.Tasks = append(out.Tasks, v)
	}
	rank := func(group string) int {
		for i, g := range StatusGroups {
			if g == group {
				return i
			}
		}
		return len(StatusGroups)
	}
	sort.SliceStable(out.Tasks, func(i, j int) bool { return rank(out.Tasks[i].Group) < rank(out.Tasks[j].Group) })
	return out, nil
}

func describeView(v *View, running bool, now time.Time) {
	t := v.Task
	for i := range v.Attempts {
		a := &v.Attempts[i]
		if a.Occupying || (t.Attention != nil && t.Attention.Attempt == a.ID) {
			v.FocusAttempt = a
			break
		}
		if a.Version == t.Current().Number {
			v.FocusAttempt = a
		}
	}
	a := v.FocusAttempt
	if a != nil {
		end := now
		if a.FinishedAt != nil {
			end = *a.FinishedAt
		}
		if !a.StartedAt.IsZero() {
			seconds := max(int64(0), int64(end.Sub(a.StartedAt).Seconds()))
			v.ElapsedSeconds = &seconds
		}
	}
	show := "task show " + t.ID
	replace := fmt.Sprintf("task replace %s --version %d --file CLARIFIED_TASK.md; inspect and approve the new version", t.ID, t.Current().Number)
	v.Group = "Needs you"
	for i := len(v.Publications) - 1; i >= 0; i-- {
		p := v.Publications[i]
		if p.Occupying {
			if p.IntegrationRequest != nil {
				v.Group, v.Summary = "Working", "inspecting integration"
				if p.IntegrationRequest.Merge && p.MergeDispatch == nil {
					v.Summary = "admitting explicit merge"
				}
				v.NextAction = "wait for the merge observation; " + show + " retains evidence"
				if v.CancellationPending {
					v.Summary = "integration cancellation pending; any already-sent merge cannot be recalled"
				}
				if !running {
					v.Group, v.NextAction = "Needs you", "run to recover integration; a dispatched merge is never repeated"
				}
				return
			}
			if p.State == "published" && p.CIRequest != nil {
				v.Group, v.Summary = "Working", "assessing CI for the confirmed draft"
				v.NextAction = "wait for the CI observation; " + show + " retains results"
				if v.CancellationPending {
					v.Summary = "CI read cancellation pending; any result remains historical"
				}
				if !running {
					v.Group, v.NextAction = "Needs you", "run to repeat the interrupted CI read safely"
				}
				return
			}
			v.Group, v.Summary = "Working", "publishing reviewed candidate; "+p.Intent.Repository
			v.NextAction = "wait for remote reconciliation; " + show + " retains operation identity and evidence"
			if v.CancellationPending {
				v.Summary = "publication cancellation pending; an already-sent remote effect may still settle"
			}
			if !running {
				v.Group, v.NextAction = "Needs you", "run to inspect the interrupted publication; uncertain PR creation cannot repeat"
			}
			return
		}
	}
	if t.Attention != nil {
		v.Summary = string(t.Attention.Kind) + ": " + t.Attention.Detail
		v.NextAction = "inspect evidence with " + show + "; " + replace
		v.Recovery = &Recovery{Preserved: "task versions, attempts and evidence; any candidate remains retained", Repeats: "no automatic invocation"}
		if a != nil {
			v.Recovery.Role = a.Role
		}
		if t.Attention.Kind == core.UnsupportedConflict {
			v.NextAction = "inspect the retained conflict with " + show + "; " + replace
			v.Recovery.Repeats = "no automatic conflict repair for unsupported or unrecognized conflicts"
			return
		}
		if a != nil && a.Role == "refresh" && t.Attention.Kind != "ownership" && t.Attention.Kind != "saved_result" {
			v.NextAction = "inspect the base verification failure; config edit --set base-refreshes=N, then save/apply if allowance is exhausted; task verify-base " + t.ID + "; then run"
			v.Recovery.Repeats = "base inspection and native checks for the retained cycle; no model invocation until a new candidate needs review"
			return
		}
		switch t.Attention.Kind {
		case core.Interrupted:
			v.Recovery.Repeats = "one invocation of the interrupted role, using its pinned profile, only with --allow-new-invocation"
			v.NextAction = fmt.Sprintf("task resume %s --attempt %s; inspect repetition details before adding --allow-new-invocation", t.ID, t.Attention.Attempt)
		case "saved_result":
			v.Recovery.Repeats = "integrity checks and deterministic continuation; no model call"
			v.NextAction = fmt.Sprintf("task resume %s --attempt %s", t.ID, t.Attention.Attempt)
		case "ownership":
			v.NextAction = "inspect the retained process identity with " + show + "; run reconciliation after ownership is resolved; do not replay or kill an unrelated PID"
		case core.Decision:
			v.NextAction = "answer the displayed question in the request, then " + replace
		}
		return
	}
	if a != nil && a.Occupying {
		v.Group, v.Summary = "Working", a.Role+" / "+a.State
		if a.Role == "implementer" && a.CIRepair != nil {
			v.Summary = "CI repair / " + a.State
		}
		if a.Role == "implementer" && a.ConflictRepair != nil {
			v.Summary = "conflict repair / " + a.State
		}
		v.NextAction = "wait for the recorded attempt; " + show + " for evidence"
		if v.CancellationPending {
			v.Summary = "cancellation pending; old execution still owns its reservation"
		}
		if !running {
			v.Group = "Needs you"
			v.Summary = "coordinator absent; execution reservation retained"
			v.NextAction = "run to reconcile ownership and saved work; no automatic replay"
		}
		return
	}
	if a != nil && a.Authorized(t) && a.State == "completed" {
		v.Group, v.Summary = "Working", "saved result awaiting deterministic continuation"
		v.NextAction = "wait for integrity checks; no new model call is needed"
		if !running {
			v.Group = "Needs you"
			v.NextAction = "run to reconcile the saved result; no new model call is needed"
		}
		return
	}
	switch t.Stage {
	case core.Draft:
		v.Summary = "request needs approval"
		v.NextAction = fmt.Sprintf("inspect %s; task approve %s --version %d --preview %s", show, t.ID, t.Current().Number, v.Preview)
		if v.DeliveryDestination != "" {
			v.Summary += "; acceptance will publish a draft to " + v.DeliveryDestination
		}
		return
	case core.AwaitingApproval:
		v.Summary = "proposal needs approval"
		if v.ReleasePreview == "" {
			v.Summary = "approved base changed; replan before release"
			v.NextAction = "replan: " + replace
		} else {
			v.NextAction = fmt.Sprintf("inspect %s; task release %s --version %d --preview %s", show, t.ID, t.Current().Number, v.ReleasePreview)
		}
		return
	case core.AwaitingDelivery:
		if t.NextRole == "refresh" {
			v.Group, v.Summary = "Waiting", "current-base verification queued"
			v.NextAction = "wait for admission; run if the coordinator is absent or drained"
			if v.Eligibility.WaitingFor != "" {
				v.Summary += "; " + v.Eligibility.WaitingFor
			}
			return
		}
		if t.BaseProof != nil && t.BaseProof.Matches(t) {
			v.Group, v.Summary = "Retained", "candidate verified against base "+t.BaseProof.Base+" at "+t.BaseProof.ObservedAt.Format(time.RFC3339)
			v.NextAction = "task verify-base " + t.ID + " to inspect again; CI and integration remain pending"
			if v.DeliveryDestination != "" {
				p := latestPublication(v.Publications, t)
				switch {
				case p != nil && p.State == "published" && p.Intent.Candidate == *t.Candidate:
					v.Summary += "; draft confirmed at this candidate"
					if p.Observation != nil {
						v.Summary += "; " + p.Observation.URL
					}
					if !describeIntegration(v, *p, running) {
						describeCI(v, *p, running)
					}
				case p != nil && p.State == "attention" && p.CanInspectIntegration(t):
					describeIntegration(v, *p, running)
				case p != nil && p.State == "attention":
					v.Group, v.Summary = "Needs you", "draft synchronization needs attention: "+p.Detail
					v.NextAction = "task delivery " + t.ID + " --reconcile; then run if absent or drained; settle this operation before another base refresh"
				default:
					v.Group, v.Summary = "Waiting", "verified candidate awaiting draft confirmation; inspect the last remote observation"
					v.NextAction = "wait for synchronization admission; run if the coordinator is absent or drained"
					if !running {
						v.Group = "Needs you"
					}
				}
			}
			return
		}
		v.Group, v.Summary = "Retained", "reviewed candidate; local delivery mode"
		v.NextAction = "inspect candidate and evidence with " + show + "; integration is still pending"
		if v.DeliveryDestination != "" {
			v.Group, v.Summary = "Waiting", "reviewed candidate awaiting draft publication"
			v.NextAction = "run to publish the exact accepted candidate as a draft"
			for i := len(v.Publications) - 1; i >= 0; i-- {
				p := v.Publications[i]
				if !p.Intent.Authorized(t) {
					continue
				}
				v.Summary = "publication " + p.State + "; " + p.Detail
				v.NextAction = "task delivery " + t.ID + " --reconcile; then run if the coordinator is absent"
				if p.State == "attention" {
					v.Group = "Needs you"
				}
				if p.State == "published" {
					v.Group = "Retained"
					v.NextAction = "inspect the draft and evidence with " + show + "; latest-base verification and integration remain pending"
				}
				if p.State == "queued" {
					v.NextAction = "wait for publication admission; run if the coordinator is absent"
				}
				if p.Observation != nil && p.Observation.URL != "" {
					v.Summary += "; " + p.Observation.URL
				}
				break
			}
			if v.Group == "Waiting" && !running {
				v.Group, v.NextAction = "Needs you", "run to reconcile and admit draft publication"
			}
		}
		return
	case core.Stopped, core.Completed:
		v.Group, v.Summary, v.NextAction = "Retained", string(t.Stage), "inspect retained work with "+show
		if t.Stage == core.Completed {
			if t.Integration != nil {
				v.Summary = "completed; confirmed merge " + t.Integration.Assessment.Observation.MergeCommit
			} else if t.Release != nil {
				v.Summary = "completed; all original released children integrated"
			}
		}
		return
	}
	if t.Release != nil {
		v.Group, v.Summary = "Waiting", "released plan; children must complete integration"
		v.NextAction = "inspect child tasks with " + show
		return
	}
	v.Group, v.Summary = "Waiting", v.Eligibility.WaitingFor
	v.NextAction = "wait; " + show + " for details"
	if t.Pending != nil && t.Pending.Kind == "conflict_repair" {
		v.Summary = "text conflicts; bounded repair against reserved base; " + v.Eligibility.WaitingFor
	}

	switch v.Eligibility.WaitKind {
	case "configuration", "repair_limit", "retry_limit", "invocation_limit":
		v.Group = "Needs you"
		v.NextAction = "inspect findings and allowances with " + show + "; config edit, then config explain and config apply --preview DIGEST"
	case "drained":
		v.Group = "Needs you"
		v.NextAction = "run to resume admission"
	case "dependency":
		v.NextAction = "inspect the named prerequisite; only its exact completed version can release this task"
	case "":
		v.Summary = "eligible for " + v.Eligibility.Role
		if t.Pending != nil && t.Pending.Kind == "conflict_repair" {
			v.Summary = "eligible for bounded conflict repair"
		}
		if running {
			v.NextAction = "wait for coordinator admission"
		} else {
			v.Group = "Needs you"
			v.Summary = "coordinator absent; " + v.Summary
			v.NextAction = "run to reconcile and admit eligible work"
		}
	}
}

func TaskTitle(t core.Task) string {
	for _, line := range strings.Split(t.Current().Body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return "Untitled request"
}

// CI is an observation of the pinned agreement, never permission to merge.
func describeCI(v *View, p core.Publication, running bool) {
	id := v.Task.ID
	if p.CanRepairCI(v.Task) {
		v.Group, v.Summary = "Waiting", "required CI failed; bounded repair eligible without model escalation"
		v.NextAction = "wait for repair admission; run if absent or drained; task assess-ci " + id + " can reassess before admission"
		if v.Eligibility.WaitingFor != "" {
			v.Summary += "; " + v.Eligibility.WaitingFor
		}
		if !running || v.Eligibility.WaitKind == "drained" || v.Eligibility.WaitKind == "repair_limit" || v.Eligibility.WaitKind == "invocation_limit" {
			v.Group = "Needs you"
		}
		if v.Eligibility.WaitKind == "repair_limit" || v.Eligibility.WaitKind == "invocation_limit" {
			v.NextAction = "inspect failure and allowances with task show " + id + "; config edit then config explain/apply for more allowance, or task assess-ci " + id + " to reassess"
		}
		return
	}
	if p.CIRequest != nil {
		v.Group, v.Summary = "Waiting", "CI assessment queued for the confirmed draft"
		v.NextAction = "wait for admission; run if the coordinator is absent or drained"
		if !running {
			v.Group = "Needs you"
		}
		return
	}
	if !p.CurrentCI(v.Task) {
		return
	}
	v.Summary += "; CI " + p.CI.State + " at " + p.CI.ObservedAt.Format(time.RFC3339)
	v.NextAction = "task assess-ci " + id + " to observe again; human integration requires task integrate"
	switch p.CI.State {
	case "passed":
		v.Group = "Retained"
	case "pending":
		v.Group = "Waiting"
		v.NextAction = "wait for hosted jobs, then task assess-ci " + id + "; run if absent or drained"
	case "stale":
		v.Group = "Needs you"
		v.NextAction = "task verify-base " + id + "; then run before reassessing CI"
	case "unconfigured":
		v.Group = "Needs you"
		v.NextAction = "configure required_ci through quality preview/check/apply, then inspect task show " + id + " for explicit agreement adoption"
	default:
		v.Group = "Needs you"
		v.NextAction = "inspect CI details with task show " + id + "; task assess-ci " + id + " after resolution; run if absent or drained"
	}
	v.Summary += "; " + p.CI.Detail
}

// Integration and explicit merge are bounded operations; neither polls.
func describeIntegration(v *View, p core.Publication, running bool) bool {
	if !p.CanInspectIntegration(v.Task) {
		return false
	}
	v.Group, v.Summary = "Needs you", "CI passed; human integration awaits inspection"
	v.NextAction = "task integrate " + v.Task.ID + "; run if absent or drained"
	if p.IntegrationRequest != nil {
		v.Summary = "integration inspection queued"
		if p.IntegrationRequest.Merge && p.MergeDispatch == nil {
			v.Summary = "explicit merge queued; live admission required"
		}
		if p.Occupying {
			v.Summary += "; running"
		}
		if running {
			v.Group = "Waiting"
		}
		v.NextAction = "wait for admission and inspection; run if absent or drained"
	} else if r := p.Integration; r != nil && r.Request.Matches(v.Task, p) {
		v.Summary = "integration " + r.State + "; " + r.Detail
		if r.State == "open" && p.MergeDispatch == nil {
			v.NextAction = "follow host reviews and mark ready; task merge " + v.Task.ID + " authorizes one guarded merge, or merge manually then task integrate " + v.Task.ID
		}
		if r.Request.Merge && r.State == "blocked" && p.MergeDispatch == nil {
			v.NextAction = "resolve admission details; task merge " + v.Task.ID + " retries admission; run if absent or drained"
		}
		if r.State == "stale" {
			v.NextAction = "return PR to draft; task verify-base " + v.Task.ID + "; then run"
		}
	}
	if p.Observation != nil {
		v.Summary += "; " + p.Observation.URL
	}
	return true
}
