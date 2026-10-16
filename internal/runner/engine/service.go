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
	Store        *store.Store
	Settings     config.Settings
	Executor     Executor
	Checker      execution.CheckRunner
	Delivery     DeliveryHost
	Bases        BaseHost
	Checks       ChecksHost
	Integrations IntegrationHost
	Merges       MergeHost
	clock        func() time.Time
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
	if !config.SameRepository(current, e.Settings) {
		return errors.New("repository authority changed")
	}
	id, _, err := workspace.Observe(ctx, e.Settings.Config)
	if err != nil {
		return err
	}
	if id != e.Settings.PhysicalRepository() {
		return errors.New("repository identity changed")
	}
	return nil
}
func (e *Engine) Add(ctx context.Context, body string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	if err := e.currentQuality(ctx); err != nil {
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
	Restoration            *config.Restoration `json:"restoration,omitempty"`
	MergePolicy            string              `json:"merge_policy"`
	DeliveryDestination    string              `json:"delivery_destination,omitempty"`
	Publications           []core.Publication  `json:"publications"`
	Group                  string              `json:"group"`
	Summary                string              `json:"summary"`
	FocusAttempt           *core.Attempt       `json:"focus_attempt,omitempty"`
	ElapsedSeconds         *int64              `json:"elapsed_seconds,omitempty"`
	Recovery               *Recovery           `json:"recovery,omitempty"`
	Task                   core.Task           `json:"task"`
	Attempts               []core.Attempt      `json:"attempts"`
	ObservedBase           string              `json:"observed_base,omitempty"`
	Preview                string              `json:"approval_preview,omitempty"`
	QualityAdoptionPreview string              `json:"quality_adoption_preview,omitempty"`
	CurrentQuality         string              `json:"current_quality_revision"`
	ReleasePreview         string              `json:"release_preview,omitempty"`
	NextAction             string              `json:"next_action"`
	CancellationPending    bool                `json:"cancellation_pending"`
	Eligibility            Eligibility         `json:"eligibility"`
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
	tasks, err := e.Store.Tasks(ctx)
	if err != nil {
		return View{}, err
	}
	view := View{Restoration: settings.Restoration, MergePolicy: settings.Config.MergePolicy(), CurrentQuality: settings.Quality.Revision(), Task: t, Attempts: attempts}
	view.DeliveryDestination = settings.Config.Delivery.Repository
	publications, err := e.Store.Publications(ctx, "")
	if err != nil {
		return view, err
	}
	for _, p := range publications {
		if p.Intent.TaskID == id {
			view.Publications = append(view.Publications, p)
			if p.Occupying && !p.Intent.Authorized(t) {
				view.CancellationPending = true
			}
		}
	}
	admission := eligible(settings.Config, ciRepairTask(t, publications), all, drained, e.now(), tasks...)
	view.Eligibility = publicationWait(admission, settings.Config, t, all, publications)
	for _, a := range attempts {
		if a.Occupying && !a.Authorized(t) {
			view.CancellationPending = true
		}
	}
	if settings.Restoration != nil && settings.Restoration.InspectionOnly {
		describeView(&view, false, e.now())
		view.Group, view.Summary = "Needs you", "restored snapshot is inspection-only; later history is unknown"
		view.NextAction = "reconcile retained and remote evidence; initialize fresh authority at distinct paths and authorize new work"
		view.Eligibility.Role = ""
		view.Eligibility.WaitKind, view.Eligibility.WaitingFor = "restoration", view.Summary
		view.Recovery = &Recovery{Preserved: "historical task, approval, counters, results and delivery records", Repeats: "none; this snapshot cannot resume"}
		return view, nil
	}
	repo, base, err := workspace.Observe(ctx, e.Settings.Config)
	if err != nil {
		return view, err
	}
	if repo != e.Settings.PhysicalRepository() {
		return view, errors.New("repository identity changed")
	}
	if t.Stage != core.Completed && t.Current().Quality != settings.Quality.Revision() && settings.Quality.Revision() != "" && t.Release == nil {
		view.QualityAdoptionPreview = adoptionPreview(t, settings.Quality.Revision())
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return view, err
	}
	e = pinned
	view.ObservedBase = base
	view.Preview = core.Preview(t, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision(), base)
	if t.PlanRequest && t.Proposal != nil && t.Stage == core.AwaitingApproval && t.Attention == nil {
		if t.Approval != nil && t.Approval.Base == base {
			view.ReleasePreview = core.ReleasePreview(t, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision(), base)
		}
	}
	running, err := e.observeCoordinator()
	if err != nil {
		return view, err
	}
	describeView(&view, running, e.now())
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
	if t.Current().Number != version {
		return t, errors.New("task version changed; inspect again")
	}
	if t.Approval != nil && t.Approval.Preview == preview && t.Approval.Generation == t.Generation && t.Stage != core.Stopped {
		return t, nil
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return t, err
	}
	e = pinned
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
	if t.PlanRequest || t.Current().NeedsVerification() {
		current, _, err := e.Store.Settings(ctx)
		if err != nil {
			return t, err
		}
		if current.Config.Roles["planner"] == "" {
			return t, errors.New("configure and apply an explicit planner profile before authorizing planning or verification preparation")
		}
		t.Stage = core.Planning
		t.NextRole = "prepare"
		if t.PlanRequest {
			t.NextRole = "plan"
		}
	}
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
	if t.Stage == core.Completed {
		return t, errors.New("completed tasks are immutable; add a new request")
	}
	if t.Current().Number != version {
		return t, store.ErrConflict
	}
	if t.Release != nil {
		return t, errors.New("released plans are immutable; stop the plan to revoke children and add a new request")
	}
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return t, err
	}
	e = pinned
	v, err := e.newVersion(t, version+1, body)
	if err != nil {
		return t, err
	}
	replaceVersion(&t, v)
	return t, e.Store.Commit(ctx, t.Revision, t, nil, false)
}
func (e *Engine) newVersion(t core.Task, number int, body string) (core.Version, error) {
	if t.PlanRequest {
		return core.NewPlanVersion(number, body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	}
	return core.NewVersion(number, body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision(), t.Current().Dependencies...)
}
func replaceVersion(t *core.Task, v core.Version) {
	t.Versions = append(t.Versions, v)
	t.Generation++
	t.Approval = nil
	t.Stage = core.Draft
	t.Usage = core.Usage{}
	t.WorkingBase, t.RefreshTarget, t.BaseProof = nil, nil, nil
	t.CIRepair, t.ConflictRepair, t.WorkspaceRepair = nil, nil, 0
	t.Pending = nil
	t.Prepared = nil
	t.Proposal = nil
	t.NextRole = ""
	t.Candidate = nil
	t.Review = nil
	t.Attention = nil
	t.AdoptionPreview = ""
}
func (e *Engine) Stop(ctx context.Context, id string) error {
	t, err := e.Store.Task(ctx, id)
	if err != nil {
		return err
	}
	if t.Stage == core.Stopped || t.Stage == core.Completed {
		return nil
	}
	t.Generation++
	t.Approval = nil
	t.Stage = core.Stopped
	t.Pending = nil
	t.NextRole = ""
	t.Attention = nil
	if t.Release != nil {
		return e.Store.StopPlan(ctx, t.Revision, t)
	}
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
	pinned, err := e.taskEngine(ctx, t)
	if err != nil {
		return "", err
	}
	e = pinned
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
		if a.Role == "audit" || a.Role == "verify" {
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
