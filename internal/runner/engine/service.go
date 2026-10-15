// Package engine owns local application commands and one explicit coordinator.
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/cortexium-io/runner/internal/runner/config"
	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/execution"
	"github.com/cortexium-io/runner/internal/runner/store"
	"github.com/cortexium-io/runner/internal/runner/workspace"
)

type Executor interface {
	Execute(context.Context, execution.Request, func(core.Process) error) execution.Completion
}
type Engine struct {
	Store    *store.Store
	Settings config.Settings
	Executor Executor
	clock    func() time.Time
}

func (e *Engine) now() time.Time {
	if e.clock != nil {
		return e.clock().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) git() workspace.Git { return workspace.Git{Settings: e.Settings} }
func (e *Engine) policy() string     { return core.Digest(e.Settings.Config.Repository) }
func (e *Engine) validate(ctx context.Context) error {
	current, _, err := e.Store.Settings(ctx)
	if err != nil {
		return err
	}
	if !config.SameAuthority(current, e.Settings) {
		return errors.New("repository or quality authority changed")
	}
	id, _, err := workspace.Observe(ctx, e.Settings.Config)
	if err != nil {
		return err
	}
	if id != e.Settings.RepositoryID {
		return errors.New("repository identity changed")
	}
	return nil
}
func (e *Engine) Add(ctx context.Context, body string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	v, err := core.NewVersion(1, body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	if err != nil {
		return core.Task{}, err
	}
	t := core.Task{ID: core.NewID(), Stage: core.Draft, Versions: []core.Version{v}}
	return t, e.Store.Add(ctx, t)
}

type View struct {
	Task                core.Task      `json:"task"`
	Attempts            []core.Attempt `json:"attempts"`
	ObservedBase        string         `json:"observed_base,omitempty"`
	Preview             string         `json:"approval_preview,omitempty"`
	NextAction          string         `json:"next_action"`
	CancellationPending bool           `json:"cancellation_pending"`
	Eligibility         Eligibility    `json:"eligibility"`
}

func NextAction(t core.Task) string {
	if t.Attention != nil {
		switch t.Attention.Kind {
		case "interrupted":
			return "inspect task; task resume --attempt ID (add --allow-new-invocation only to authorize repetition)"
		case "saved_result":
			return "inspect task; task resume --attempt ID to reuse the saved result"
		case "ownership":
			return "resolve the displayed process ownership before resuming"
		default:
			return "inspect evidence; supply an explicit replacement task if requirements or implementation must change"
		}
	}
	switch t.Stage {
	case core.Draft:
		return "inspect approval preview, then task approve --version N --preview DIGEST"
	case core.AwaitingDelivery:
		return "inspect retained candidate; delivery is not configured"
	case core.Stopped:
		return "retained artifacts are available; replace the task to authorize new work"
	default:
		return "run (or wait for the running coordinator)"
	}
}
func (e *Engine) Show(ctx context.Context, id string) (View, error) {
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return View{}, err
	}
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return View{}, err
	}
	settings, drained, err := e.Store.Settings(ctx)
	if err != nil {
		return View{}, err
	}
	all, err := e.Store.Attempts(ctx, "")
	if err != nil {
		return View{}, err
	}
	admission := eligible(settings.Config, t, all, drained, e.now())
	view := View{Task: t, Attempts: attempts, NextAction: NextAction(t), Eligibility: admission}
	if t.NextRole != "" && t.Attention == nil {
		if admission.WaitingFor != "" {
			view.NextAction = admission.WaitingFor
		} else {
			view.NextAction = "eligible: " + admission.Role + " with " + admission.ProfileName + "; run or wait for coordinator"
		}
	}
	for _, a := range attempts {
		if a.Occupying && !a.Authorized(t) {
			view.CancellationPending = true
		}
	}
	repo, base, err := workspace.Observe(ctx, e.Settings.Config)
	if err != nil {
		return view, err
	}
	if repo != e.Settings.RepositoryID {
		return view, errors.New("repository identity changed")
	}
	view.ObservedBase = base
	view.Preview = core.Preview(t, repo, e.policy(), e.Settings.Quality.Revision(), base)
	return view, nil
}
func (e *Engine) Approve(ctx context.Context, id string, version int, preview string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	v, err := e.Show(ctx, id)
	if err != nil {
		return core.Task{}, err
	}
	t := v.Task
	if t.Current().Number != version || t.Current().Quality != e.Settings.Quality.Revision() {
		return t, errors.New("task version or quality revision changed; inspect again")
	}
	if t.Approval != nil && t.Approval.Preview == preview && t.Approval.Generation == t.Generation && t.Stage != core.Stopped {
		return t, nil
	}
	if err := e.git().CheckQuality(ctx, v.ObservedBase); err != nil {
		return t, err
	}
	if t.Stage != core.Draft || v.Preview != preview {
		return t, errors.New("approval preview is stale; inspect task again")
	}
	t.Generation++
	t.Approval = &core.Approval{Preview: preview, Base: v.ObservedBase, Generation: t.Generation}
	t.Stage = core.Ready
	t.NextRole = "implementer"
	t.Attention = nil
	return t, e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) Replace(ctx context.Context, id string, version int, body string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return t, err
	}
	if t.Current().Number != version {
		return t, store.ErrConflict
	}
	v, err := core.NewVersion(version+1, body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	if err != nil {
		return t, err
	}
	t.Versions = append(t.Versions, v)
	t.Generation++
	t.Approval = nil
	t.Stage = core.Draft
	t.Usage = core.Usage{}
	t.Pending = nil
	t.NextRole = ""
	t.Candidate = nil
	t.Review = nil
	t.Attention = nil
	return t, e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) Stop(ctx context.Context, id string) error {
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	if t.Stage == core.Stopped {
		return nil
	}
	t.Generation++
	t.Approval = nil
	t.Stage = core.Stopped
	t.Pending = nil
	t.NextRole = ""
	t.Attention = nil
	return e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) Resume(ctx context.Context, id, attempt string, allowNew bool) (string, error) {
	if err := e.validate(ctx); err != nil {
		return "", err
	}
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return "", err
	}
	attempts, err := e.Store.Attempts(ctx, id)
	if err != nil {
		return "", err
	}
	for _, a := range attempts {
		if a.ID != attempt {
			continue
		}
		if a.Resume == "new" {
			return "Recovery already requested for this attempt; no additional invocation queued.", nil
		}
		if !a.Authorized(t) {
			return "", errors.New("attempt no longer has authority")
		}
		if a.Occupying || (a.Process != nil && !execution.Gone(*a.Process)) {
			return "", errors.New("execution ownership is unresolved; run reconciliation first")
		}
		if a.State == "saved_result" && a.Result != nil {
			a.Resume = "saved"
			a.State = "completed"
			t.Attention = nil
			if err = e.Store.Commit(ctx, t.Revision, t, &a, false); err != nil {
				return "", err
			}
			return "Saved result queued for integrity checks and deterministic continuation; no model call authorized.", nil
		}
		if a.State != "interrupted" {
			return "", errors.New("only interrupted execution can repeat; rejected review cannot be repaired by resume")
		}
		if !allowNew {
			return "", fmt.Errorf("%s attempt %s was interrupted; artifacts remain in %s; --allow-new-invocation authorizes repeating that role with the pinned profile", a.Role, a.ID, a.EvidenceDir)
		}
		if a.Role != "implementer" {
			if err = e.git().ValidateCandidate(ctx, t); err != nil {
				return "", err
			}
		}
		a.Resume = "new"
		t.Pending = &core.Continuation{Kind: "resume", FromAttempt: a.ID, Reason: "operator-authorized repetition of interrupted " + a.Role, ProfileName: a.ProfileName, Profile: a.Profile, Retry: a.Retry, Repair: a.Repair}
		t.Attention = nil
		t.NextRole = a.Role
		if err = e.Store.Commit(ctx, t.Revision, t, &a, false); err != nil {
			return "", err
		}
		return "One new invocation requested for the interrupted role; artifacts and attempt history are preserved.", nil
	}
	return "", errors.New("attempt not found")
}
func (e *Engine) Running() bool {
	return execution.Running(filepath.Join(e.Settings.Config.StateDir, "coordinator.lock"))
}
