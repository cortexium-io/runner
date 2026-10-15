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
	if t.Attention != nil {
		v.Summary = string(t.Attention.Kind) + ": " + t.Attention.Detail
		v.NextAction = "inspect evidence with " + show + "; " + replace
		v.Recovery = &Recovery{Preserved: "task versions, attempts and evidence; any candidate remains retained", Repeats: "no automatic invocation"}
		if a != nil {
			v.Recovery.Role = a.Role
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
		v.Group, v.Summary = "Retained", "reviewed candidate; delivery is not implemented"
		v.NextAction = "inspect candidate and evidence with " + show + "; integration is still pending"
		return
	case core.Stopped, core.Completed:
		v.Group, v.Summary, v.NextAction = "Retained", string(t.Stage), "inspect retained work with "+show
		return
	}
	if t.Release != nil {
		v.Group, v.Summary = "Waiting", "released plan; children must complete integration"
		v.NextAction = "inspect child tasks with " + show
		return
	}
	v.Group, v.Summary = "Waiting", v.Eligibility.WaitingFor
	v.NextAction = "wait; " + show + " for details"
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
