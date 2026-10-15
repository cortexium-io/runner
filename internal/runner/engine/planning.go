package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/cortexium-io/runner/internal/runner/core"
	"github.com/cortexium-io/runner/internal/runner/store"
)

func (e *Engine) AddPlan(ctx context.Context, body string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	v, err := core.NewPlanVersion(1, body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision())
	if err != nil {
		return core.Task{}, err
	}
	task := core.Task{ID: core.NewID(), PlanRequest: true, Stage: core.Draft, Versions: []core.Version{v}}
	return task, e.Store.Add(ctx, task)
}
func (e *Engine) Release(ctx context.Context, id string, version int, preview string) (core.Task, error) {
	if err := e.validate(ctx); err != nil {
		return core.Task{}, err
	}
	view, err := e.Show(ctx, id)
	if err != nil {
		return core.Task{}, err
	}
	task := view.Task
	if task.Current().Number != version {
		return task, store.ErrConflict
	}
	if task.Release != nil && task.Release.Preview == preview && task.Stage != core.Stopped {
		return task, nil
	}
	if !task.PlanRequest || task.Stage != core.AwaitingApproval || task.Proposal == nil || task.Approval == nil || task.Attention != nil || len(task.Proposal.Questions) != 0 {
		return task, errors.New("plan is not ready for release")
	}
	if preview == "" || preview != view.ReleasePreview || task.Approval.Base != view.ObservedBase || task.Current().Quality != e.Settings.Quality.Revision() {
		return task, errors.New("release preview, base or quality changed; inspect or replan")
	}
	if err = e.git().CheckQuality(ctx, view.ObservedBase); err != nil {
		return task, err
	}
	if err = core.ValidateProposal(*task.Proposal); err != nil {
		return task, err
	}
	ids := map[string]string{}
	for _, card := range task.Proposal.Cards {
		ids[card.Key] = core.NewID()
	}
	children := []core.Task{}
	receipt := &core.PlanRelease{Preview: preview, Children: []string{}}
	for _, card := range task.Proposal.Cards {
		dependencies := []core.Dependency{}
		for _, key := range card.Dependencies {
			dependencies = append(dependencies, core.Dependency{TaskID: ids[key], Version: 1})
		}
		v, err := core.NewVersion(1, card.Body, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision(), dependencies...)
		if err != nil {
			return task, err
		}
		child := core.Task{ID: ids[card.Key], ParentID: task.ID, PlanKey: card.Key, Stage: core.Ready, Generation: 1, Versions: []core.Version{v}, NextRole: "implementer"}
		child.Approval = &core.Approval{Generation: 1, Base: task.Approval.Base, Preview: core.Preview(child, e.Settings.RepositoryID, e.policy(), e.Settings.Quality.Revision(), task.Approval.Base)}
		children = append(children, child)
		receipt.Children = append(receipt.Children, child.ID)
	}
	task.Release = receipt
	task.Stage = core.Ready
	task.NextRole = ""
	return task, e.Store.Release(ctx, task.Revision, task, children)
}

func advancePlanning(t *core.Task, a *core.Attempt) error {
	if err := core.ValidateRoleResult(a.Role, *a.Result, nil); err != nil {
		return err
	}
	result := a.Result.Planning
	if a.Role == "plan" {
		t.Proposal = result
	}
	if len(result.Questions) != 0 || a.Result.Outcome == "needs_input" {
		t.Attention = &core.Attention{Kind: core.Decision, Detail: strings.Join(result.Questions, "\n"), Attempt: a.ID}
	} else if a.Result.Outcome != "succeeded" {
		t.Attention = &core.Attention{Kind: core.ProofGap, Detail: a.Result.Summary, Attempt: a.ID}
	} else if a.Role == "plan" {
		t.Stage = core.AwaitingApproval
		t.Attention = nil
	} else {
		t.Prepared = &core.PreparedVerification{Requirement: result.Verification, Attempt: a.ID}
		t.Stage = core.Ready
		t.NextRole = "implementer"
		t.Attention = nil
	}
	a.State = "applied"
	return nil
}
